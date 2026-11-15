package capability

import (
	"fmt"
	"net/http"
	"strings"
)

// Rule allows one method on one path template of the hosts Host accepts. A template is a path of fixed segments,
// ":name" (one non-empty segment) and a final "**" (any rest, also none).
type Rule struct {
	Method string
	Path   string
	Host   func(host string) bool // nil: no host passes
}

// Guard is the RoundTripper every capability's HTTP client wraps (ADR-0022 D3): a request that matches no rule is
// refused before a connection is made, even when a bug in a capability asks for it. It also refuses credentials
// and cookies on a family that has none (NoCredentials).
type Guard struct {
	Base          http.RoundTripper
	Rules         []Rule
	NoCredentials bool
}

// ErrGuard marks a request the guard refused.
type ErrGuard struct{ Method, Host, Path string }

func (e *ErrGuard) Error() string {
	return fmt.Sprintf("guard: %s %s%s is not an allowed call", e.Method, e.Host, e.Path)
}

// Allowed reports whether the guard lets req through.
func (g *Guard) Allowed(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if g.NoCredentials && (req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" ||
		req.Header.Get("Proxy-Authorization") != "" || req.URL.User != nil) {
		return false
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	host := strings.ToLower(req.URL.Hostname())
	for _, r := range g.Rules {
		if r.Method == req.Method && r.Host != nil && r.Host(host) && MatchPath(r.Path, path) {
			return true
		}
	}
	return false
}

// RoundTrip refuses a request outside the rules, else hands it to Base.
func (g *Guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if !g.Allowed(req) {
		if req != nil && req.Body != nil {
			req.Body.Close()
		}
		e := &ErrGuard{}
		if req != nil && req.URL != nil {
			e.Method, e.Host, e.Path = req.Method, req.URL.Host, req.URL.EscapedPath()
		}
		return nil, e
	}
	base := g.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// MatchPath matches an escaped request path against a template. "." and ".." segments and an encoded slash never
// match.
func MatchPath(tmpl, path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(strings.ToLower(path), "%2f") {
		return false
	}
	ps := strings.Split(path[1:], "/")
	for _, p := range ps {
		if p == "." || p == ".." {
			return false
		}
	}
	ts := strings.Split(strings.TrimPrefix(tmpl, "/"), "/")
	for i, t := range ts {
		if t == "**" && i == len(ts)-1 {
			return true
		}
		if i >= len(ps) {
			return false
		}
		switch {
		case strings.HasPrefix(t, ":"):
			if ps[i] == "" {
				return false
			}
		case t != ps[i]:
			return false
		}
	}
	return len(ps) == len(ts)
}
