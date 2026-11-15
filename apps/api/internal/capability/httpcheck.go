package capability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPCheck is the capability http.check (ADR-0022 D3): one GET of an https URL whose host is on
// ARGUS_ACT_HTTP_HOSTS, compared with the expected status code. No credentials, no custom headers, no redirect to
// another host. The result is the status, the latency and whether it matched; the body is read and dropped, never
// kept.
type HTTPCheck struct {
	hosts      []string // exact names and "*.suffix" entries, lower case
	allowPlain bool
	client     *http.Client
}

// HTTPCheckName is the capability's name.
const HTTPCheckName = "http.check"

const (
	httpDefaultTimeout = 10
	httpMaxRedirects   = 5
	httpDrainBytes     = 64 << 10
)

// ParseHostList reads ARGUS_ACT_HTTP_HOSTS: host names, "*.suffix" allowed; no IP literal, no bare "*", no port.
func ParseHostList(list []string) ([]string, error) {
	var out []string
	for _, h := range list {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		name := strings.TrimPrefix(h, "*.")
		switch {
		case h == "*" || strings.Contains(name, "*"):
			return nil, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: only a leading \"*.\" is allowed", h)
		case net.ParseIP(strings.Trim(name, "[]")) != nil:
			return nil, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: an IP address is not allowed, use a host name", h)
		case !hostNameRe(name):
			return nil, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q is not a host name", h)
		case strings.HasPrefix(h, "*.") && !strings.Contains(name, "."):
			return nil, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: a wildcard needs a suffix with a dot", h)
		}
		out = append(out, h)
	}
	return out, nil
}

func hostNameRe(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// NewHTTPCheck builds http.check for an allow-list; base is the transport under the guard (nil: a direct one).
func NewHTTPCheck(hosts []string, allowPlain bool, base http.RoundTripper) (*HTTPCheck, error) {
	hs, err := ParseHostList(hosts)
	if err != nil {
		return nil, err
	}
	if len(hs) == 0 {
		return nil, errors.New("http.check: the host allow-list is empty")
	}
	h := &HTTPCheck{hosts: hs, allowPlain: allowPlain}
	if base == nil {
		base = &http.Transport{
			Proxy:                  nil, // direct: a proxy would hide the host the allow-list checks
			DialContext:            (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  MaxCall,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 64 << 10,
		}
	}
	guard := &Guard{Base: base, NoCredentials: true, Rules: []Rule{{Method: http.MethodGet, Path: "/**", Host: h.HostAllowed}}}
	h.client = &http.Client{Transport: guard, CheckRedirect: h.checkRedirect}
	return h, nil
}

// HostAllowed reports whether host (no port) is on the allow-list. An IP literal never is.
func (h *HTTPCheck) HostAllowed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	for _, a := range h.hosts {
		if suf, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suf) {
				return true
			}
		} else if host == a {
			return true
		}
	}
	return false
}

func (h *HTTPCheck) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= httpMaxRedirects {
		return fmt.Errorf("more than %d redirects", httpMaxRedirects)
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Hostname(), first.Hostname()) {
		return Refuse(4, CodeAllowList, "redirect to another host refused")
	}
	if req.URL.Scheme != "https" && !h.allowPlain {
		return Refuse(4, CodeAllowList, "redirect to plain http refused")
	}
	return nil
}

func (h *HTTPCheck) Name() string { return HTTPCheckName }
func (h *HTTPCheck) Tier() Tier   { return Read }

func (h *HTTPCheck) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "url", Type: String, Required: true, MaxLen: 2048, AllowList: "ARGUS_ACT_HTTP_HOSTS"},
		{Name: "expect_status", Type: IntOrList, Required: true, MaxLen: 10, Min: 100, Max: 599},
		{Name: "timeout_sec", Type: Int, Min: 1, Max: int(MaxCall / time.Second)},
	}}
}

// Validate: schema, a known environment (Read tier: every one, O8), an https URL without credentials whose host is
// a name on the allow-list.
func (h *HTTPCheck) Validate(args Args, env Env) error {
	if !env.Known() {
		return Refuse(2, CodeEnvironment, "the task's classification is unknown")
	}
	if err := h.Schema().Check(args); err != nil {
		return err
	}
	_, err := h.target(args)
	return err
}

func (h *HTTPCheck) target(args Args) (*url.URL, error) {
	raw, _ := args["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !u.IsAbs() {
		return nil, Refuse(4, CodeArguments, "argument \"url\" is not an absolute URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && h.allowPlain:
	default:
		return nil, Refuse(4, CodeArguments, "argument \"url\" must be https")
	}
	if u.User != nil {
		return nil, Refuse(4, CodeArguments, "argument \"url\" must not carry credentials")
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return nil, Refuse(4, CodeAllowList, "argument \"url\": an IP address is not allowed, only host names on ARGUS_ACT_HTTP_HOSTS")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, Refuse(4, CodeArguments, "argument \"url\" has a bad port")
		}
	}
	if !h.HostAllowed(host) {
		return nil, Refuse(4, CodeAllowList, "host %q is not on ARGUS_ACT_HTTP_HOSTS", clipName(host))
	}
	u.Fragment = ""
	return u, nil
}

// Plan uses no network: it names the check that Apply runs.
func (h *HTTPCheck) Plan(_ context.Context, args Args) (Plan, error) {
	u, err := h.target(args)
	if err != nil {
		return Plan{}, err
	}
	want, _ := AsInts(args["expect_status"])
	return Plan{Change: "none", Summary: fmt.Sprintf("GET %s://%s%s, expect %s", u.Scheme, u.Host, u.EscapedPath(), joinInts(want))}, nil
}

// Apply runs the check.
func (h *HTTPCheck) Apply(ctx context.Context, args Args, _ Plan) (Result, BackupRef, error) {
	r, err := h.check(ctx, args)
	return r, "", err
}

// Verify runs the check again.
func (h *HTTPCheck) Verify(ctx context.Context, args Args, _ Plan) (Result, error) {
	return h.check(ctx, args)
}

// Rollback: nothing to roll back.
func (h *HTTPCheck) Rollback(context.Context, BackupRef) (Result, error) {
	return Result{}, ErrNoRollback
}

func (h *HTTPCheck) check(ctx context.Context, args Args) (Result, error) {
	u, err := h.target(args)
	if err != nil {
		return Result{Status: "failed", Detail: err.Error()}, err
	}
	want, _ := AsInts(args["expect_status"])
	timeout := httpDefaultTimeout
	if n, ok := AsInt(args["timeout_sec"]); ok {
		timeout = n
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{Status: "failed", Detail: "bad request"}, err
	}
	req.Header.Set("User-Agent", "argus-actd/http.check")
	start := time.Now()
	resp, err := h.client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		var ref *Refusal
		if errors.As(err, &ref) {
			return Result{Status: "failed", Detail: ref.Reason, Fields: map[string]any{"matched": false, "latency_ms": latency}}, ref
		}
		var g *ErrGuard
		if errors.As(err, &g) {
			return Result{Status: "failed", Detail: "refused by the guard", Fields: map[string]any{"matched": false}}, Refuse(4, CodeAllowList, "%s", g.Error())
		}
		why := "request failed"
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Timeout") {
			why = fmt.Sprintf("no answer within %ds", timeout)
		}
		return Result{Status: "failed", Detail: why, Fields: map[string]any{"matched": false, "latency_ms": latency}}, errors.New(why)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, httpDrainBytes)) // dropped: the body is never kept
	resp.Body.Close()
	matched := false
	for _, w := range want {
		if resp.StatusCode == w {
			matched = true
		}
	}
	st := "ok"
	if !matched {
		st = "mismatch"
	}
	return Result{Status: st, Detail: fmt.Sprintf("status %d, expected %s", resp.StatusCode, joinInts(want)),
		Fields: map[string]any{"status": resp.StatusCode, "latency_ms": latency, "matched": matched}}, nil
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, "|")
}
