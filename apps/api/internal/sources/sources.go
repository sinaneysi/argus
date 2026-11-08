// Package sources is the root side and the agent catalogue of Settings -> Data sources (ADR-0017 P1).
//
// A data source is <type>/<name>: one instance of a system Argus reads (GitLab, Grafana, Outline,
// Elasticsearch/OpenSearch, Sentry; Kubernetes is mirrored from Settings -> Kubernetes), with its environment, address,
// purpose and the verdict of its last check. Root (`argus sources apply`, `argus sources check`) is the only writer of
// ARGUS_SOURCES_DIR/registry.json (0644, no secret) and of the credentials ARGUS_SOURCES_DIR/<type>/<name>.cred (0600
// root; 0640 root:<agent group> only while the source is enabled for the agent). It publishes
// ARGUS_SETTINGS_OUT/sources-status.json for the web page; `argus sources record` (the agent's user) copies that into
// the data_sources table of ARGUS_DB, which `argus sources list` reads.
//
// sources.go: the types, the address guard and the adapters. Every adapter only reads: it asks the system who the
// login is and what it may do, and returns a status (readonly, ok, refused, failed, unreachable, unchecked) with a
// detail that never contains the credential.
package sources

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	MaxCred     = 8 * 1024
	maxBody     = 1 << 20
	callTimeout = 10 * time.Second
	keepBackups = 5
	keepResults = 20
	maxRequest  = 64 * 1024
)

var (
	NameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	UserRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	Envs   = []string{"develop", "pre-release", "release", "shared"}
	reqRe  = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._@-]{0,63})-([0-9a-f]{16})\.json$`)
	// secretish: tokens of the supported systems and anything long and token-like (masked wherever text leaves root)
	secretish = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*|glpat-[A-Za-z0-9_-]{8,}|glsa_[A-Za-z0-9_]{8,}` +
		`|sntry[us]_[A-Za-z0-9_=-]{8,}|ol_api_[A-Za-z0-9]{8,}|[A-Za-z0-9+/=_-]{40,}`)
)

// testAllowLocal lets fake services on http://127.0.0.1 through. Tests only; nothing outside this package can set it.
var testAllowLocal = false

// Type is one kind of data source.
type Type struct {
	Label    string   `json:"label"`
	Purposes []string `json:"purposes"` // first = default
	Username bool     `json:"username"` // basic auth (username + password) is possible
	Managed  string   `json:"managed"`  // added and changed on another page
	NoCred   bool     `json:"no_cred"`  // the credential is optional (an Alertmanager without a login)
}

// Types are the P1 types (the page lists P2 types as "coming").
var Types = map[string]Type{
	"gitlab":        {Label: "GitLab", Purposes: []string{"agent", "tracker"}},
	"grafana":       {Label: "Grafana", Purposes: []string{"agent"}},
	"outline":       {Label: "Outline", Purposes: []string{"docs", "agent"}},
	"elasticsearch": {Label: "Elasticsearch / OpenSearch", Purposes: []string{"agent"}, Username: true},
	"sentry":        {Label: "Sentry", Purposes: []string{"agent"}},
	"kubernetes":    {Label: "Kubernetes", Purposes: []string{"agent"}, Managed: "/admin/kube"},
	"alertmanager":  {Label: "Alertmanager / Prometheus", Purposes: []string{"alerts"}, NoCred: true},
	"jira":          {Label: "Jira", Purposes: []string{"tracker"}, Username: true},
	"matrix":        {Label: "Matrix", Purposes: []string{"notify"}},
	"google":        {Label: "Google Search Console", Purposes: []string{"marketing"}},
}

var (
	gitlabReadScopes    = set("read_api", "read_repository", "read_registry", "read_user", "read_virtual_registry")
	grafanaWriteActions = []string{"dashboards:write", "dashboards:create", "dashboards:delete", "datasources:write",
		"datasources:create", "datasources:delete", "folders:create", "folders:write", "users:write",
		"serviceaccounts:write", "alert.rules:write", "alert.rules:create", "org.users:write"}
	openSearchReadRoles = set("readall", "readall_and_monitor")
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func in(x string, xs ...string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

// Mask hides anything token-like.
func Mask(s string) string { return secretish.ReplaceAllString(s, "[REDACTED]") }

type refusal string

func (r refusal) Error() string { return string(r) }

// cut keeps the first n characters (runes).
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// str is a JSON value as text ("" for null and false).
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func strList(v any, max int) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// --- the address guard ------------------------------------------------------------------------------------------------

// CleanURL is https://host[:port][/path] without user info, query or fragment; a refusal otherwise.
func CleanURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 300 || strings.IndexFunc(raw, func(r rune) bool { return r < 33 }) >= 0 {
		return "", refusal("the URL is too long or has spaces")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", refusal("the URL must start with https://")
	}
	if u.Scheme != "https" && !((testAllowLocal || AllowHTTP) && u.Scheme == "http") {
		return "", refusal("the URL must start with https://")
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.ContainsAny(raw, "?#") {
		return "", refusal("the URL must be https://host[:port][/path], without a user, query or fragment")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", refusal("the URL has a bad port")
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// blocked is an address Argus never calls: loopback, link-local (cloud metadata), multicast or unspecified.
func blocked(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

// resolve is the first address of host; a refusal when any address of it is blocked.
func resolve(ctx context.Context, host string) (string, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return "", errors.New("the host name does not resolve")
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return "", errors.New("the host name does not resolve")
	}
	for _, ip := range ips {
		if !testAllowLocal && blocked(ip) {
			return "", refusal(fmt.Sprintf("%s resolves to an address Argus never calls (%s)", host, ip))
		}
	}
	return ips[0].String(), nil
}

// call is one read-only request: (status, parsed JSON or nil). The address is resolved and checked once and the
// connection goes to that address (the host name still names the TLS certificate); no redirect is followed; the body
// is capped at 1 MB; 10 s in all. The credential goes only into a request header.
func call(method, base, path string, hdr map[string]string, body []byte) (int, any, error) {
	st, data, _, err := fetch(method, base, path, hdr, body, maxBody, false, callTimeout)
	return st, data, err
}

// ErrTooLarge is an answer larger than the caller's cap (Get); nothing of it is returned.
var ErrTooLarge = errors.New("the answer is larger than the cap")

// Get is one read-only GET for discovery (ADR-0017 P2): the same guard as the checks (https only unless a test allows
// a local fake, the address resolved and checked once and pinned, no redirect, no proxy), with the caller's body cap
// and time limit, and the response headers (pagination). path may carry a query. A body over maxBody is ErrTooLarge.
func Get(base, path string, hdr map[string]string, maxBody int64, timeout time.Duration) (int, any, http.Header, error) {
	return fetch("GET", base, path, hdr, nil, maxBody, true, timeout)
}

// NetError is a network failure in a few plain words (never a URL with a credential, nor a header).
func NetError(err error) string { return netError(err) }

func fetch(method, base, path string, hdr map[string]string, body []byte, maxBody int64, strict bool,
	callTimeout time.Duration) (int, any, http.Header, error) {
	return fetchTo(method, base, path, hdr, body, maxBody, strict, callTimeout, nil)
}

// GetRaw is Get without the JSON parse: (status, body) of one guarded read-only GET, for answers too large to hold
// twice (a job's log reads). A body over maxBody is ErrTooLarge.
func GetRaw(base, path string, hdr map[string]string, maxBody int64, timeout time.Duration) (int, []byte, error) {
	var raw []byte
	st, _, _, err := fetchTo("GET", base, path, hdr, nil, maxBody, true, timeout, &raw)
	return st, raw, err
}

// PostRead is one guarded read that needs a body (an Elasticsearch / OpenSearch search): (status, body). The same
// guard as Get; the body is a query, never a change.
func PostRead(base, path string, hdr map[string]string, body []byte, maxBody int64, timeout time.Duration) (int, []byte, error) {
	var raw []byte
	st, _, _, err := fetchTo("POST", base, path, hdr, body, maxBody, true, timeout, &raw)
	return st, raw, err
}

func fetchTo(method, base, path string, hdr map[string]string, body []byte, maxBody int64, strict bool,
	callTimeout time.Duration, rawOut *[]byte) (int, any, http.Header, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil {
		return 0, nil, nil, refusal("bad URL")
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ip, err := resolve(ctx, u.Hostname())
	if err != nil {
		return 0, nil, nil, err
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: callTimeout}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: callTimeout,
		DisableKeepAlives:   true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: callTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, nil, refusal("bad URL")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-sources")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if strict && int64(len(raw)) > maxBody {
		return resp.StatusCode, nil, resp.Header, ErrTooLarge
	}
	if rawOut != nil {
		*rawOut = raw
		return resp.StatusCode, nil, resp.Header, nil
	}
	var data any
	if len(raw) > 0 && int64(len(raw)) <= maxBody && json.Unmarshal(raw, &data) != nil {
		data = nil
	}
	return resp.StatusCode, data, resp.Header, nil
}

// netError is a network failure in a few plain words (never the URL's credential: there is none, nor a header).
func netError(err error) string {
	var rf refusal
	var ne net.Error
	var ue x509.UnknownAuthorityError
	var he x509.HostnameError
	var ce x509.CertificateInvalidError
	var rh tls.RecordHeaderError
	switch {
	case errors.As(err, &rf):
		return string(rf)
	case errors.As(err, &ue), errors.As(err, &he), errors.As(err, &ce):
		return "TLS certificate not trusted"
	case errors.As(err, &rh) || strings.Contains(err.Error(), "tls:"):
		return "TLS error"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "no answer within 10 s"
	case err.Error() == "the host name does not resolve":
		return err.Error()
	}
	return "network error"
}

// --- adapters -------------------------------------------------------------------------------------------------------

// Check is what one adapter found.
type Check struct {
	Status   string
	Detail   string
	Identity string
	Findings []string
	Facts    map[string]any
}

func result(status, detail, identity string, findings []string, facts map[string]any) Check {
	f := []string{}
	for _, x := range findings {
		if len(f) < 8 {
			f = append(f, cut(x, 160))
		}
	}
	if facts == nil {
		facts = map[string]any{}
	}
	return Check{Status: status, Detail: cut(detail, 300), Identity: cut(identity, 80), Findings: f, Facts: facts}
}

func loginFailed(st int) Check {
	return result("failed", fmt.Sprintf("the login was refused (HTTP %d); replace the credential", st), "", nil, nil)
}

func bp(b bool) *bool { return &b }

// verdict is the status of a check that learned whether the login can write (writes nil: could not tell).
func verdict(purpose string, writes *bool, whyWrite, okDetail, identity string, findings []string, facts map[string]any,
	needsWrite string) Check {
	if purpose == "agent" {
		if writes == nil {
			return result("refused", "could not verify that this login is read-only, so the agent may not use it", identity, findings, facts)
		}
		if *writes {
			return result("refused", "this login can write ("+whyWrite+"); the agent needs a read-only one", identity, findings, facts)
		}
		return result("readonly", okDetail, identity, findings, facts)
	}
	if needsWrite != "" && writes != nil && !*writes {
		return result("failed", needsWrite, identity, findings, facts)
	}
	return result("ok", okDetail, identity, findings, facts)
}

type checkFn func(base, cred string, e *Entry) (Check, error)

// checks are the adapters; a type without one (kubernetes) is checked on its own page.
var checks = map[string]checkFn{"gitlab": checkGitLab, "grafana": checkGrafana, "outline": checkOutline,
	"elasticsearch": checkElasticsearch, "sentry": checkSentry}

func checkGitLab(base, cred string, e *Entry) (Check, error) {
	hdr := map[string]string{"PRIVATE-TOKEN": cred}
	st, d, err := call("GET", base, "/api/v4/personal_access_tokens/self", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	tok := asMap(d)
	if st != 200 || tok == nil {
		return result("unreachable", fmt.Sprintf("GitLab answered HTTP %d for the token's own details", st), "", nil, nil), nil
	}
	if tok["revoked"] == true || tok["active"] == false {
		return result("failed", "the token is revoked or expired; replace it", "", nil, nil), nil
	}
	scopes := strList(tok["scopes"], 12)
	exp, _ := tok["expires_at"].(string)
	facts := map[string]any{"scopes": scopes, "expires_at": exp}
	ident := str(tok["name"])
	var findings []string
	st, u, err := call("GET", base, "/api/v4/user", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if um := asMap(u); st == 200 && um != nil {
		if n := str(um["username"]); n != "" {
			ident = n
		}
		if um["is_admin"] == true {
			findings = append(findings, "the token's user is a GitLab administrator")
		}
	}
	var write []string
	for _, s := range scopes {
		if !gitlabReadScopes[s] {
			write = append(write, s)
			findings = append(findings, "scope "+s)
		}
	}
	why := ""
	if len(write) > 0 {
		why = "scopes " + strings.Join(write, ", ")
	}
	if e.Purpose == "tracker" {
		if !in("api", scopes...) {
			has := strings.Join(scopes, ", ")
			if has == "" {
				has = "no scope"
			}
			return result("failed", "the tracker needs scope api (it changes issues); this token has "+has, ident, findings, facts), nil
		}
		return result("ok", "works as the issue tracker (scope api)", ident, findings, facts), nil
	}
	ok := strings.Join(scopes, ", ")
	if ok == "" {
		ok = "none"
	}
	return verdict("agent", bp(len(write) > 0), why, "read-only: scopes "+ok, ident, findings, facts, ""), nil
}

func checkGrafana(base, cred string, _ *Entry) (Check, error) {
	hdr := map[string]string{"Authorization": "Bearer " + cred}
	st, d, err := call("GET", base, "/api/user", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	me := asMap(d)
	if st != 200 || me == nil {
		return result("unreachable", fmt.Sprintf("Grafana answered HTTP %d for /api/user", st), "", nil, nil), nil
	}
	ident := str(me["login"])
	var findings []string
	facts := map[string]any{}
	var writes *bool
	why := ""
	if me["isGrafanaAdmin"] == true {
		writes, why = bp(true), "Grafana server admin"
		findings = append(findings, "Grafana server admin")
	}
	role := ""
	st, orgs, err := call("GET", base, "/api/user/orgs", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if l, ok := orgs.([]any); st == 200 && ok {
		var mine, all []map[string]any
		for _, o := range l {
			if m := asMap(o); m != nil {
				all = append(all, m)
				if m["orgId"] == me["orgId"] {
					mine = append(mine, m)
				}
			}
		}
		if len(mine) == 0 {
			mine = all
		}
		if len(mine) > 0 {
			role = str(mine[0]["role"])
		}
	}
	if role == "" {
		st, perms, err := call("GET", base, "/api/access-control/user/permissions", hdr, nil)
		if err != nil {
			return Check{}, err
		}
		if pm := asMap(perms); st == 200 && pm != nil {
			role = "Viewer"
			for _, a := range grafanaWriteActions {
				if _, ok := pm[a]; ok {
					role = "Editor"
				}
			}
		}
	}
	if role != "" {
		facts["role"] = role
		if writes == nil {
			writes, why = bp(role != "Viewer"), "role "+role
		}
		if role != "Viewer" {
			findings = append(findings, "role "+role)
		}
	}
	st, ds, err := call("GET", base, "/api/datasources", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	var kinds map[string]int
	if l, ok := ds.([]any); st == 200 && ok {
		kinds = map[string]int{}
		for _, x := range l {
			t := str(asMap(x)["type"])
			if !in(t, "prometheus", "loki", "elasticsearch", "tempo") {
				t = "other"
			}
			kinds[t]++
		}
		facts["datasources"] = kinds
	} else {
		facts["datasources_error"] = fmt.Sprintf("HTTP %d", st)
	}
	shown := role
	if shown == "" {
		shown = "Viewer"
	}
	ok := "read-only: role " + shown
	if kinds != nil {
		keys := make([]string, 0, len(kinds))
		for k := range kinds {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%d %s", kinds[k], k)
		}
		ok += ", " + strings.Join(parts, ", ")
	}
	return verdict("agent", writes, why, ok, ident, findings, facts, ""), nil
}

func checkOutline(base, cred string, e *Entry) (Check, error) {
	st, d, err := call("POST", base, "/api/auth.info", map[string]string{"Authorization": "Bearer " + cred}, []byte("{}"))
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	data := asMap(asMap(d)["data"])
	if st != 200 || data == nil {
		return result("unreachable", fmt.Sprintf("Outline answered HTTP %d for auth.info", st), "", nil, nil), nil
	}
	u, team := asMap(data["user"]), asMap(data["team"])
	role := str(u["role"])
	if role == "" {
		if str(u["isAdmin"]) != "" {
			role = "admin"
		} else if str(u["isViewer"]) != "" {
			role = "viewer"
		}
	}
	facts := map[string]any{"role": role, "team": cut(str(team["name"]), 60)}
	var writes *bool
	if role != "" {
		writes = bp(!in(role, "viewer", "guest"))
	}
	ident := str(u["name"])
	if e.Purpose == "docs" {
		who, r := ident, role
		if who == "" {
			who = "this user"
		}
		if r == "" {
			r = "unknown"
		}
		return verdict("docs", writes, "", "publishes as "+who+" (role "+r+")", ident, nil, facts,
			"this login is a viewer; publishing service docs needs a member or admin"), nil
	}
	var findings []string
	if writes != nil && *writes {
		findings = []string{"role " + role}
	}
	return verdict("agent", writes, "role "+role, "read-only: role "+role, ident, findings, facts, ""), nil
}

func esHeaders(e *Entry, cred string) map[string]string {
	if e.Username != "" {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(e.Username+":"+cred))}
	}
	return map[string]string{"Authorization": "ApiKey " + cred}
}

func checkElasticsearch(base, cred string, e *Entry) (Check, error) {
	hdr := esHeaders(e, cred)
	st, d, err := call("GET", base, "/", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	root := asMap(d)
	if st != 200 || root == nil {
		return result("unreachable", fmt.Sprintf("the cluster answered HTTP %d for /", st), "", nil, nil), nil
	}
	ver := asMap(root["version"])
	flavour := "elasticsearch"
	if ver["distribution"] == "opensearch" {
		flavour = "opensearch"
	}
	facts := map[string]any{"flavour": flavour, "version": cut(str(ver["number"]), 20), "cluster": cut(str(root["cluster_name"]), 60)}
	if flavour == "opensearch" {
		st, info, err := call("GET", base, "/_plugins/_security/authinfo", hdr, nil)
		if err != nil {
			return Check{}, err
		}
		im := asMap(info)
		if st != 200 || im == nil {
			var w *bool
			if st == 400 || st == 404 {
				w = bp(true)
			}
			return verdict("agent", w, "the security plugin is off: anyone can write", "", "",
				[]string{fmt.Sprintf("authinfo HTTP %d", st)}, facts, ""), nil
		}
		var roles []string
		if l, ok := im["roles"].([]any); ok {
			for _, r := range l {
				roles = append(roles, str(r))
			}
		}
		sort.Strings(roles)
		if len(roles) > 12 {
			roles = roles[:12]
		}
		if roles == nil {
			roles = []string{}
		}
		facts["roles"] = roles
		var findings []string
		for _, r := range roles {
			if !openSearchReadRoles[r] {
				findings = append(findings, "role "+r+" (not a known read-only role)")
			}
		}
		var w *bool
		if len(roles) > 0 && len(findings) == 0 {
			w = bp(false)
		}
		return verdict("agent", w, "", "read-only: roles "+strings.Join(roles, ", "), str(im["user_name"]), findings, facts, ""), nil
	}
	st, who, err := call("GET", base, "/_security/_authenticate", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	wm := asMap(who)
	if st != 200 || wm == nil {
		var w *bool
		if st == 400 || st == 404 || st == 500 {
			w = bp(true)
		}
		return verdict("agent", w, "security is off: anyone can write", "", "",
			[]string{fmt.Sprintf("_authenticate HTTP %d", st)}, facts, ""), nil
	}
	ident := str(wm["username"])
	q, _ := json.Marshal(map[string]any{"cluster": []string{"manage", "monitor", "all"},
		"index": []any{map[string]any{"names": []string{"*"}, "privileges": []string{"read", "write", "delete", "create_index", "manage"}}}})
	st, p, err := call("POST", base, "/_security/user/_has_privileges", hdr, q) // a question, never a change
	if err != nil {
		return Check{}, err
	}
	pm := asMap(p)
	if st != 200 || pm == nil {
		return verdict("agent", nil, "", "", ident, []string{fmt.Sprintf("_has_privileges HTTP %d", st)}, facts, ""), nil
	}
	cl := asMap(pm["cluster"])
	idx := asMap(asMap(pm["index"])["*"])
	var found []string
	for _, k := range []string{"manage", "all"} {
		if cl[k] == true {
			found = append(found, "cluster "+k)
		}
	}
	for _, k := range []string{"write", "delete", "create_index", "manage"} {
		if idx[k] == true {
			found = append(found, "index "+k+" on *")
		}
	}
	facts["read_all_indices"] = idx["read"] == true
	ok := "read-only"
	if idx["read"] != true {
		ok += ": cannot read every index"
	}
	return verdict("agent", bp(len(found) > 0), strings.Join(found, ", "), ok, ident, found, facts, ""), nil
}

func checkSentry(base, cred string, _ *Entry) (Check, error) {
	hdr := map[string]string{"Authorization": "Bearer " + cred}
	st, d, err := call("GET", base, "/api/0/", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if st == 401 || st == 403 {
		return loginFailed(st), nil
	}
	dm := asMap(d)
	if st != 200 || dm == nil {
		return result("unreachable", fmt.Sprintf("Sentry answered HTTP %d for /api/0/", st), "", nil, nil), nil
	}
	auth := asMap(dm["auth"])
	if auth == nil {
		return loginFailed(401), nil
	}
	scopes := strList(auth["scopes"], 20)
	user := asMap(dm["user"])
	var write, findings []string
	for _, s := range scopes {
		if !strings.HasSuffix(s, ":read") {
			write = append(write, s)
			findings = append(findings, "scope "+s)
		}
	}
	facts := map[string]any{"scopes": scopes}
	st, orgs, err := call("GET", base, "/api/0/organizations/", hdr, nil)
	if err != nil {
		return Check{}, err
	}
	if l, ok := orgs.([]any); st == 200 && ok {
		facts["organizations"] = len(l)
	}
	ident := str(user["username"])
	if ident == "" {
		ident = str(user["email"])
	}
	all := strings.Join(scopes, ", ")
	if all == "" {
		all = "none"
	}
	return verdict("agent", bp(len(write) > 0), "scopes "+strings.Join(write, ", "), "read-only: scopes "+all, ident, findings, facts, ""), nil
}

// readSecretFile is a regular file's trimmed text, capped; "" when missing or not a regular file.
func readSecretFile(path string, max int64) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, max))
	return strings.TrimSpace(string(b))
}

// ParseEnv reads KEY=value lines (comments, export and quotes as in a shell env file).
func ParseEnv(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		k = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "export "))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '\'' || v[0] == '"') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// ReadSecret is a regular file's trimmed text (no symlink), capped at MaxCred; "" when missing or unreadable.
func ReadSecret(path string) string { return readSecretFile(path, MaxCred) }

// AuthHeaders are the request headers that carry an entry's credential, the same as its check uses.
func AuthHeaders(e *Entry, cred string) map[string]string {
	switch e.Type {
	case "gitlab":
		return map[string]string{"PRIVATE-TOKEN": cred}
	case "elasticsearch":
		return esHeaders(e, cred)
	case "jira":
		return jiraHeaders(e, cred)
	}
	return map[string]string{"Authorization": "Bearer " + cred}
}
