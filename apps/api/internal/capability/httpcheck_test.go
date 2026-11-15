package capability

import (
	"context"
	"encoding/json"
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
	if _, err := ParseHostList([]string{"a.example.test", "*.b.example.test", " C.example.test "}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"*", "10.0.0.1", "::1", "*.com", "a.*.example.test", "a.example.test:443", "bad_host.test", "-a.test"} {
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
