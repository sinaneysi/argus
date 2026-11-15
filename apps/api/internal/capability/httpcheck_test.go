package capability

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newCheck(t *testing.T, ts *testServer, hosts ...string) *HTTPCheck {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []string{"svc.example.test", "*.dev.example.test"}
	}
	h, err := NewHTTPCheck(hosts, true, ts.transport())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHTTPCheckMatchAndMismatch(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts)
	ctx := context.Background()
	for _, c := range []struct {
		url    string
		expect any
		status string
		code   int
	}{
		{"http://svc.example.test/ok", 200, "ok", 200},
		{"http://api.dev.example.test/ok", []any{201, 200}, "ok", 200},
		{"http://svc.example.test/fail", 200, "mismatch", 503},
		{"http://svc.example.test/same", 200, "ok", 200}, // a redirect on the same host is followed
	} {
		args := Args{"url": c.url, "expect_status": c.expect}
		if err := h.Validate(args, Release); err != nil { // read tier: every environment (O8)
			t.Fatal(c.url, err)
		}
		p, err := h.Plan(ctx, args)
		if err != nil || p.Change != "none" || !strings.Contains(p.Summary, "GET ") {
			t.Fatal(p, err)
		}
		res, ref, err := h.Apply(ctx, args, p)
		if err != nil || ref != "" {
			t.Fatal(c.url, err, ref)
		}
		if res.Status != c.status || res.Fields["status"] != c.code || res.Fields["matched"] != (c.status == "ok") {
			t.Errorf("%s: %+v", c.url, res)
		}
		if _, ok := res.Fields["latency_ms"]; !ok {
			t.Errorf("%s: no latency", c.url)
		}
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), "BODY-MARKER") {
			t.Fatalf("the response body is in the result: %s", b)
		}
	}
	if _, err := h.Rollback(ctx, ""); err != ErrNoRollback {
		t.Fatal("read tier rollback:", err)
	}
}

func TestHTTPCheckPlanMakesNoCall(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts)
	if _, err := h.Plan(context.Background(), Args{"url": "http://svc.example.test/ok", "expect_status": 200}); err != nil {
		t.Fatal(err)
	}
	if ts.calls.Load() != 0 {
		t.Fatal("Plan called the target")
	}
}

func TestHTTPCheckTimeout(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts)
	start := time.Now()
	res, _, err := h.Apply(context.Background(), Args{"url": "http://svc.example.test/slow", "expect_status": 200, "timeout_sec": 1}, Plan{})
	if err == nil || res.Status != "failed" || !strings.Contains(res.Detail, "within 1s") {
		t.Fatalf("%+v %v", res, err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Fatal("the timeout was not applied")
	}
}

func TestHTTPCheckRedirectToAnotherHostRefused(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts, "svc.example.test", "other.example.test")
	res, _, err := h.Apply(context.Background(), Args{"url": "http://svc.example.test/away", "expect_status": 200}, Plan{})
	refusal(t, err, CodeAllowList)
	if res.Status != "failed" || ts.calls.Load() != 1 {
		t.Fatalf("%+v, %d calls (the other host must not be called)", res, ts.calls.Load())
	}
}

func TestHTTPCheckValidate(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts)
	strict, err := NewHTTPCheck([]string{"svc.example.test"}, false, ts.transport())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		c    *HTTPCheck
		args Args
		code Code
	}{
		{h, Args{"url": "http://not-listed.example.test/", "expect_status": 200}, CodeAllowList},
		{h, Args{"url": "http://example.test/", "expect_status": 200}, CodeAllowList},     // the suffix itself is not *.dev...
		{h, Args{"url": "http://dev.example.test/", "expect_status": 200}, CodeAllowList}, // "*.dev.example.test" needs a label
		{h, Args{"url": "http://svc.example.test.evil.test/", "expect_status": 200}, CodeAllowList},
		{h, Args{"url": "http://127.0.0.1/ok", "expect_status": 200}, CodeAllowList},
		{h, Args{"url": "http://[::1]/ok", "expect_status": 200}, CodeAllowList},
		{h, Args{"url": "http://" + userInfo + "svc.example.test/ok", "expect_status": 200}, CodeArguments},
		{h, Args{"url": "ftp://svc.example.test/", "expect_status": 200}, CodeArguments},
		{h, Args{"url": "/relative", "expect_status": 200}, CodeArguments},
		{strict, Args{"url": "http://svc.example.test/ok", "expect_status": 200}, CodeArguments}, // https only
		{h, Args{"url": "http://svc.example.test/ok"}, CodeArguments},
		{h, Args{"url": "http://svc.example.test/ok", "expect_status": 200, "timeout_sec": 31}, CodeArguments},
		{h, Args{"url": "http://svc.example.test/ok", "expect_status": 200, "method": "POST"}, CodeArguments},
		{h, Args{"url": "http://svc.example.test/ok", "expect_status": 200, "headers": "X-A: b"}, CodeArguments},
	}
	for _, c := range cases {
		refusal(t, c.c.Validate(c.args, Develop), c.code)
	}
	refusal(t, h.Validate(Args{"url": "http://svc.example.test/ok", "expect_status": 200}, Env("staging")), CodeEnvironment)
	if ts.calls.Load() != 0 {
		t.Fatal("Validate used the network")
	}
}

func TestParseHostList(t *testing.T) {
	if _, err := ParseHostList([]string{"a.example.test", "*.b.example.test", " C.example.test ", "a.example.test:8443", "internal:c.example.test", "internal:*.d.example.test:9000"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"*", "10.0.0.1", "::1", "*.com", "a.*.example.test", "bad_host.test", "-a.test", "a.example.test:0", "a.example.test:x", "internal:10.0.0.1", "internal:*"} {
		if _, err := ParseHostList([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NewHTTPCheck(nil, false, nil); err == nil {
		t.Fatal("an empty allow-list built http.check")
	}
	r, err := Default(Options{})
	if err != nil || len(r.Names()) != 0 {
		t.Fatal("an empty allow-list must leave http.check out of the registry", r.Names(), err)
	}
	r, err = Default(Options{HTTPHosts: []string{"a.example.test"}})
	if err != nil || strings.Join(r.Names(), ",") != HTTPCheckName {
		t.Fatal(r.Names(), err)
	}
}

func TestHTTPCheckAddressRules(t *testing.T) {
	ts := newTestServer(t)
	port := ts.srv.Listener.Addr().String()
	u, _ := url.Parse("http://" + port)
	p := u.Port()
	loop := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	args := Args{"url": "http://svc.example.test:" + p + "/ok", "expect_status": 200}

	public, err := NewHTTPCheck([]string{"svc.example.test:" + p}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	public.lookup = loop
	if err := public.Validate(args, Develop); err != nil {
		t.Fatal(err)
	}
	_, _, err = public.Apply(context.Background(), args, Plan{})
	refusal(t, err, CodeAllowList)
	if ts.calls.Load() != 0 {
		t.Fatal("a name that resolves to loopback was dialled")
	}

	internal, err := NewHTTPCheck([]string{"internal:svc.example.test:" + p}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	internal.lookup = loop
	res, _, err := internal.Apply(context.Background(), args, Plan{})
	if err != nil || res.Status != "ok" || ts.calls.Load() != 1 {
		t.Fatalf("internal entry: %+v %v", res, err)
	}

	for a, want := range map[string]bool{"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true,
		"100.64.1.1": true, "169.254.1.1": true, "0.0.0.0": true, "::": true, "::1": true, "fd00::1": true, "fe80::1": true,
		"::ffff:127.0.0.1": true, "224.0.0.1": true, "203.0.113.5": false, "2001:db8::5": false} {
		if got := blockedAddr(netip.MustParseAddr(a)); got != want {
			t.Errorf("blockedAddr(%s) = %v", a, got)
		}
	}
}

func TestHTTPCheckPortsAndQuery(t *testing.T) {
	ts := newTestServer(t)
	h := newCheck(t, ts, "svc.example.test", "alt.example.test:8443")
	strict, _ := NewHTTPCheck([]string{"svc.example.test"}, false, ts.transport())
	for _, c := range []struct {
		c    *HTTPCheck
		url  string
		code Code
	}{
		{h, "http://svc.example.test:8080/ok", CodeAllowList},
		{h, "http://alt.example.test/ok", CodeAllowList}, // the entry names 8443 only
		{strict, "https://svc.example.test:80/ok", CodeAllowList},
		{h, "http://svc.example.test/ok?private_token=x", CodeArguments},
		{h, "http://svc.example.test/ok?access-token=x", CodeArguments},
		{h, "http://svc.example.test/ok?api_key=x", CodeArguments},
		{h, "http://svc.example.test/ok?Signature=x", CodeArguments},
		{h, "http://svc.example.test/ok?password=x", CodeArguments},
		{h, "http://svc.example.test/ok?auth=x", CodeArguments},
	} {
		refusal(t, c.c.Validate(Args{"url": c.url, "expect_status": 200}, Develop), c.code)
	}
	for _, ok := range []string{"http://alt.example.test:8443/ok", "http://svc.example.test/ok?page=2&monkey=1", "https://svc.example.test:443/"} {
		if err := h.Validate(Args{"url": ok, "expect_status": 200}, Develop); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}
