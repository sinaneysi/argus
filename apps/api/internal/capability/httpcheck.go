package capability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPCheck is the capability http.check (ADR-0022 D3): one GET of an https URL whose host is on
// ARGUS_ACT_HTTP_HOSTS, compared with the expected status code. No credentials, no custom headers, no redirect to
// another host. The result is the status, the latency and whether it matched; the body is read and dropped, never
// kept.
//
// Allow-list entries are "[internal:]host[:port]" with host a name or "*.suffix". Without a port only 443 (and 80
// when plain http is allowed) may be used. After the name is resolved, every address must be public: loopback,
// link-local, private (RFC 1918), shared (CGNAT, 100.64/10), unique local (fc00::/7), multicast and unspecified
// addresses are refused unless the entry starts with "internal:".
type HTTPCheck struct {
	hosts      []hostEntry
	allowPlain bool
	client     *http.Client
	// lookup resolves a host name (tests replace it); nil: the system resolver.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

type hostEntry struct {
	name     string // lower case, without "*."
	wildcard bool
	internal bool
	port     int // 0: the default ports
}

// HTTPCheckName is the capability's name.
const HTTPCheckName = "http.check"

const (
	httpDefaultTimeout = 10
	httpMaxRedirects   = 5
	httpDrainBytes     = 64 << 10
)

func parseHostEntry(raw string) (hostEntry, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	var e hostEntry
	if rest, ok := strings.CutPrefix(h, "internal:"); ok {
		e.internal, h = true, rest
	}
	if name, port, ok := strings.Cut(h, ":"); ok {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strings.Contains(port, ":") {
			return e, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: bad port", raw)
		}
		e.port, h = n, name
	}
	if rest, ok := strings.CutPrefix(h, "*."); ok {
		e.wildcard, h = true, rest
	}
	e.name = h
	switch {
	case h == "*" || strings.Contains(h, "*") || h == "":
		return e, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: only a leading \"*.\" is allowed", raw)
	case net.ParseIP(strings.Trim(h, "[]")) != nil || strings.HasPrefix(h, "["):
		return e, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: an IP address is not allowed, use a host name", raw)
	case !hostNameRe(h):
		return e, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q is not a host name", raw)
	case e.wildcard && !strings.Contains(h, "."):
		return e, fmt.Errorf("ARGUS_ACT_HTTP_HOSTS: %q: a wildcard needs a suffix with a dot", raw)
	}
	return e, nil
}

// ParseHostList reads ARGUS_ACT_HTTP_HOSTS: "[internal:]host[:port]", host a name or "*.suffix"; no IP literal, no
// bare "*".
func ParseHostList(list []string) ([]string, error) {
	var out []string
	for _, h := range list {
		if strings.TrimSpace(h) == "" {
			continue
		}
		if _, err := parseHostEntry(h); err != nil {
			return nil, err
		}
		out = append(out, strings.ToLower(strings.TrimSpace(h)))
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

// NewHTTPCheck builds http.check for an allow-list; base is the transport under the guard (nil: a direct one that
// applies the address rules after resolution).
func NewHTTPCheck(hosts []string, allowPlain bool, base http.RoundTripper) (*HTTPCheck, error) {
	h := &HTTPCheck{allowPlain: allowPlain}
	for _, raw := range hosts {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		e, err := parseHostEntry(raw)
		if err != nil {
			return nil, err
		}
		h.hosts = append(h.hosts, e)
	}
	if len(h.hosts) == 0 {
		return nil, errors.New("http.check: the host allow-list is empty")
	}
	if base == nil {
		base = &http.Transport{
			Proxy:                  nil, // direct: a proxy would hide the address the rules check
			DialContext:            h.dialContext,
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

func (e hostEntry) matches(host string) bool {
	if e.wildcard {
		return strings.HasSuffix(host, "."+e.name)
	}
	return host == e.name
}

// entry is the allow-list entry for host and port, if one allows them.
func (h *HTTPCheck) entry(host string, port int) (hostEntry, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return hostEntry{}, false
	}
	for _, e := range h.hosts {
		if !e.matches(host) {
			continue
		}
		if e.port != 0 && port == e.port || e.port == 0 && (port == 443 || port == 80 && h.allowPlain) {
			return e, true
		}
	}
	return hostEntry{}, false
}

// HostAllowed reports whether host (no port) is named by an allow-list entry. An IP literal never is.
func (h *HTTPCheck) HostAllowed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	for _, e := range h.hosts {
		if e.matches(host) {
			return true
		}
	}
	return false
}

// blockedAddr reports an address that is not public.
func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsValid() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsPrivate() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() || cgnat.Contains(a) || thisNet.Contains(a) ||
		a == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

var (
	cgnat   = netip.MustParsePrefix("100.64.0.0/10")
	thisNet = netip.MustParsePrefix("0.0.0.0/8")
)

// dialContext resolves the host, refuses non-public addresses (unless the entry is internal) and dials the first
// address it checked, so a second lookup cannot change the answer.
func (h *HTTPCheck) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(ps)
	e, ok := h.entry(host, port)
	if !ok {
		return nil, Refuse(4, CodeAllowList, "%s is not on ARGUS_ACT_HTTP_HOSTS", clipName(addr))
	}
	lookup := h.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("the name does not resolve")
	}
	if !e.internal {
		for _, a := range addrs {
			if blockedAddr(a) {
				return nil, Refuse(4, CodeAllowList, "%s resolves to a non-public address; mark the entry internal: to allow it", clipName(host))
			}
		}
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(addrs[0].Unmap().String(), ps))
}

func defaultPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
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
	if _, ok := h.entry(req.URL.Hostname(), defaultPort(req.URL)); !ok {
		return Refuse(4, CodeAllowList, "redirect to a port the allow-list does not allow refused")
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
	if _, ok := h.entry(host, defaultPort(u)); !ok {
		return nil, Refuse(4, CodeAllowList, "port %d of %q is not allowed (443, 80 with plain http, or the entry's own port)", defaultPort(u), clipName(host))
	}
	for k := range u.Query() {
		if credentialKey(k) {
			return nil, Refuse(4, CodeArguments, "argument \"url\": the query parameter %q looks like a credential", clipName(k))
		}
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

var credentialWords = map[string]bool{"token": true, "password": true, "passwd": true, "pwd": true, "secret": true, "key": true,
	"apikey": true, "signature": true, "sig": true, "auth": true, "authorization": true, "credential": true, "credentials": true,
	"session": true, "sessionid": true, "jwt": true, "otp": true}

// credentialKey reports a query parameter name that looks like a credential: any of its parts (split at _ - . and
// case changes ignored) is a credential word, e.g. token, private_token, access-token, api_key, password, signature.
func credentialKey(k string) bool {
	k = strings.ToLower(k)
	if credentialWords[k] {
		return true
	}
	for _, p := range strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == '[' || r == ']' }) {
		if credentialWords[p] {
			return true
		}
	}
	return false
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, "|")
}
