package board

// gitlab.go: a small GitLab REST v4 client for the GitLab issues tracker (ADR-0009, ADR-0010). The token is a project
// or group access token (a bot user, scope api, role Reporter or Planner), sent as "PRIVATE-TOKEN"; it is never
// logged and never in an error. https only (plain http only with AllowHTTP, for test installations with a fake
// GitLab); the TCP connection goes to an address the SSRF guard checked (Pin) while TLS still verifies the URL's host
// name; redirects are never followed; only the paths of glPathRe are called. Nothing deletes.
//
// Limits: at most Budget requests per run, MinGap between requests, 429 honours Retry-After (capped), 5xx and network
// errors are retried with backoff; 401 (and 403 on /user) fail at once so a bad token is never hammered.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var glPathRe = regexp.MustCompile(`^/api/v4/(user|users|projects/[0-9]{1,10}(/(boards(/[0-9]{1,10})?|issues(/[0-9]{1,10}(/notes|/resource_label_events)?)?))?` +
	`|projects/[A-Za-z0-9._~%-]{1,400}` +
	`|groups/[0-9]{1,10}(/(boards(/[0-9]{1,10})?|issues|projects))?|groups/[A-Za-z0-9._~%-]{1,400})$`)

// GitLab is the client. Calls counts the requests of this run.
type GitLab struct {
	Base    string
	token   string
	host    string
	port    string
	prefix  string
	pin     string
	client  *http.Client
	Budget  int
	MinGap  time.Duration
	Retries int
	MaxWait time.Duration
	Sleep   func(time.Duration)
	Calls   int
	last    time.Time
}

// GitLabError is a failed GitLab call; Status is the HTTP status (0 for a network error). Kind: "", "auth",
// "ratelimit", "budget".
type GitLabError struct {
	Msg    string
	Status int
	Kind   string
}

func (e *GitLabError) Error() string { return e.Msg }

func glerr(status int, kind, format string, a ...any) *GitLabError {
	return &GitLabError{Msg: fmt.Sprintf(format, a...), Status: status, Kind: kind}
}

func asGitLab(err error) (*GitLabError, bool) {
	var g *GitLabError
	ok := errors.As(err, &g)
	return g, ok
}

// glStop: the token cannot be used any more this run (bad token, rate limit, budget).
func glStop(err error) bool {
	g, ok := asGitLab(err)
	return ok && (g.Kind == "auth" || g.Kind == "ratelimit" || g.Kind == "budget")
}

// NewGitLab checks the URL and token and builds the client. pin is the address the guard checked ("" dials the host).
func NewGitLab(base, token, pin, caFile string) (*GitLab, error) {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && !(AllowHTTP && u.Scheme == "http")) {
		return nil, glerr(0, "", "the GitLab URL must be https://")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, glerr(0, "", "the GitLab URL must be a plain https://host[/path] URL")
	}
	if token == "" || strings.ContainsAny(token, "\r\n \t") {
		return nil, glerr(0, "auth", "no usable token")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	g := &GitLab{Base: base, token: token, host: u.Hostname(), port: port, prefix: strings.TrimRight(u.Path, "/"), pin: pin,
		Budget: 300, MinGap: 100 * time.Millisecond, Retries: 3, MaxWait: 60 * time.Second, Sleep: time.Sleep}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pool, perr := x509.SystemCertPool()
		if perr != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, rerr := os.ReadFile(caFile)
		if rerr != nil || !pool.AppendCertsFromPEM(pem) {
			return nil, glerr(0, "", "the GitLab CA file holds no PEM certificate")
		}
		tc.RootCAs = pool
	}
	tr.TLSClientConfig = tc
	tr.Proxy = nil
	d := &net.Dialer{Timeout: 20 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if g.pin != "" {
			addr = net.JoinHostPort(g.pin, g.port)
		}
		return d.DialContext(ctx, network, addr)
	}
	g.client = &http.Client{Transport: tr, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return g, nil
}

func (g *GitLab) scrub(s string) string {
	if g.token != "" {
		s = strings.ReplaceAll(s, g.token, "[REDACTED]")
	}
	return s
}

// glReason is GitLab's own error text ({"message": ...} or {"error": ...}), short.
func glReason(body []byte) string {
	var d any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
		dec = json.NewDecoder(bytes.NewReader(body))
	}
	if dec.Decode(&d) != nil {
		return strings.TrimSpace(head(strings.ToValidUTF8(string(body[:min(len(body), 120)]), "�"), 120))
	}
	m, ok := d.(map[string]any)
	if !ok {
		return ""
	}
	v := m["message"]
	if orStr(v) == "" {
		v = m["error"]
	}
	switch x := v.(type) {
	case map[string]any:
		var parts []string
		for _, k := range orderedKeys(body, "message") {
			val := x[k]
			if l, ok := val.([]any); ok {
				var s []string
				for _, e := range l {
					s = append(s, pyStr(e))
				}
				parts = append(parts, k+": "+strings.Join(s, ", "))
			} else {
				parts = append(parts, k+": "+pyStr(val))
			}
		}
		return head(strings.Join(parts, "; "), 300)
	case []any:
		var s []string
		for _, e := range x {
			s = append(s, pyStr(e))
		}
		return head(strings.Join(s, "; "), 300)
	}
	return head(orStr(v), 300)
}

// glQuery is urllib.parse.urlencode of ordered pairs.
func glQuery(q []string) string {
	var v []string
	for i := 0; i+1 < len(q); i += 2 {
		v = append(v, url.QueryEscape(q[i])+"="+url.QueryEscape(q[i+1]))
	}
	return strings.Join(v, "&")
}

// Request calls one REST path; query is ordered (key, value pairs). It returns the decoded body and the headers.
func (g *GitLab) Request(ctx context.Context, method, path string, body any, query ...string) (any, http.Header, error) {
	if !glPathRe.MatchString(path) || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return nil, nil, glerr(0, "", "path not allowed: %s", head(path, 60))
	}
	scheme := "https"
	if strings.HasPrefix(g.Base, "http://") {
		scheme = "http"
	}
	target := scheme + "://" + net.JoinHostPort(g.host, g.port) + g.prefix + path
	if len(query) > 0 {
		target += "?" + glQuery(query)
	}
	var data []byte
	if body != nil {
		data = pyDumps(body)
	}
	attempt := 0
	for {
		if g.Calls >= g.Budget {
			return nil, nil, glerr(0, "budget", "request budget of %d per run used up", g.Budget)
		}
		if gap := g.MinGap - time.Since(g.last); gap > 0 && !g.last.IsZero() {
			g.Sleep(gap)
		}
		g.Calls++
		g.last = time.Now()
		var rd io.Reader
		if data != nil {
			rd = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rd)
		if err != nil {
			return nil, nil, glerr(0, "", "bad request")
		}
		req.Header.Set("PRIVATE-TOKEN", g.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "argus-board-sync")
		if data != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := g.client.Do(req)
		if err != nil {
			if attempt < g.Retries {
				g.Sleep(time.Duration(min(1<<attempt, 8)) * time.Second)
				attempt++
				continue
			}
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return nil, nil, glerr(0, "", "%s", head(g.scrub("cannot reach GitLab: "+err.Error()), 200))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		st := resp.StatusCode
		if st >= 200 && st < 300 {
			if len(bytes.TrimSpace(raw)) == 0 {
				return map[string]any{}, resp.Header, nil
			}
			var out any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if d.Decode(&out) != nil {
				return nil, nil, glerr(st, "", "GitLab answered %s %s with something that is not JSON", method, path)
			}
			return out, resp.Header, nil
		}
		if st >= 300 && st < 400 {
			return nil, nil, glerr(st, "", "GitLab answered with a redirect (HTTP %d); redirects are not followed - check the URL", st)
		}
		why := g.scrub(glReason(raw))
		suffix := ""
		if why != "" {
			suffix = ": " + why
		}
		if st == 401 || (st == 403 && path == "/api/v4/user") {
			return nil, nil, glerr(st, "auth", "GitLab refused the token (HTTP %d)%s", st, suffix)
		}
		if st == 429 {
			wait := 5.0
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if f, err := strconv.ParseFloat(ra, 64); err == nil {
					wait = f
				}
			}
			if attempt >= g.Retries || wait > g.MaxWait.Seconds() {
				return nil, nil, glerr(st, "ratelimit", "GitLab rate limit (HTTP 429, retry after %.0f s)", wait)
			}
			g.Sleep(time.Duration(max(wait, 0.5) * float64(time.Second)))
			attempt++
			continue
		}
		if st >= 500 && attempt < g.Retries {
			g.Sleep(time.Duration(min(1<<attempt, 8)) * time.Second)
			attempt++
			continue
		}
		return nil, nil, glerr(st, "", "HTTP %d on %s %s%s", st, method, path, suffix)
	}
}

func (g *GitLab) obj(ctx context.Context, method, path string, body any) (map[string]any, error) {
	r, _, err := g.Request(ctx, method, path, body)
	return asMap(r), err
}

// pages is every page of a list endpoint (X-Next-Page), at most cap items.
func (g *GitLab) pages(ctx context.Context, path string, query []string, cap int) ([]map[string]any, error) {
	var out []map[string]any
	page := 1
	for len(out) < cap {
		q := append(append([]string{}, query...), "page", strconv.Itoa(page), "per_page", "100")
		items, h, err := g.Request(ctx, "GET", path, nil, q...)
		if err != nil {
			return out, err
		}
		l, ok := items.([]any)
		if !ok {
			break
		}
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		nxt := strings.TrimSpace(h.Get("X-Next-Page"))
		if len(l) == 0 || !isDigits(nxt) {
			break
		}
		page, _ = strconv.Atoi(nxt)
	}
	if len(out) > cap {
		out = out[:cap]
	}
	return out, nil
}

// glRef is a project or group reference: a number as it is, a path URL-encoded.
func glRef(ref string) string {
	if isDigits(ref) {
		return ref
	}
	return url.PathEscape(ref)
}

func (g *GitLab) User(ctx context.Context) (map[string]any, error) {
	return g.obj(ctx, "GET", "/api/v4/user", nil)
}

func (g *GitLab) UserByName(ctx context.Context, name string) (map[string]any, error) {
	r, _, err := g.Request(ctx, "GET", "/api/v4/users", nil, "username", name)
	if l, ok := r.([]any); ok && len(l) > 0 {
		if m, ok := l[0].(map[string]any); ok {
			return m, err
		}
	}
	return nil, err
}

func (g *GitLab) Project(ctx context.Context, ref string) (map[string]any, error) {
	return g.obj(ctx, "GET", "/api/v4/projects/"+strings.ReplaceAll(glRef(ref), "/", "%2F"), nil)
}

func (g *GitLab) list(ctx context.Context, path string) ([]map[string]any, error) {
	r, _, err := g.Request(ctx, "GET", path, nil)
	var out []map[string]any
	if l, ok := r.([]any); ok {
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out, err
}

func (g *GitLab) Boards(ctx context.Context, pid int64) ([]map[string]any, error) {
	return g.list(ctx, fmt.Sprintf("/api/v4/projects/%d/boards", pid))
}

func (g *GitLab) Board(ctx context.Context, pid, bid int64) (map[string]any, error) {
	return g.obj(ctx, "GET", fmt.Sprintf("/api/v4/projects/%d/boards/%d", pid, bid), nil)
}

func issueQuery(after string, labels []string, group bool) []string {
	q := []string{"scope", "all", "state", "all", "order_by", "updated_at", "sort", "asc"}
	if group {
		q = append(q, "include_subgroups", "true")
	}
	if after != "" {
		q = append(q, "updated_after", after)
	}
	if len(labels) > 0 {
		q = append(q, "labels", strings.Join(labels, ","))
	}
	return q
}

func (g *GitLab) Issues(ctx context.Context, pid int64, after string, labels []string, cap int) ([]map[string]any, error) {
	return g.pages(ctx, fmt.Sprintf("/api/v4/projects/%d/issues", pid), issueQuery(after, labels, false), cap)
}

func (g *GitLab) Issue(ctx context.Context, pid, iid int64) (map[string]any, error) {
	return g.obj(ctx, "GET", fmt.Sprintf("/api/v4/projects/%d/issues/%d", pid, iid), nil)
}

func (g *GitLab) CreateIssue(ctx context.Context, pid int64, fields object) (map[string]any, error) {
	return g.obj(ctx, "POST", fmt.Sprintf("/api/v4/projects/%d/issues", pid), fields)
}

func (g *GitLab) UpdateIssue(ctx context.Context, pid, iid int64, fields object) (map[string]any, error) {
	return g.obj(ctx, "PUT", fmt.Sprintf("/api/v4/projects/%d/issues/%d", pid, iid), fields)
}

func (g *GitLab) Group(ctx context.Context, ref string) (map[string]any, error) {
	return g.obj(ctx, "GET", "/api/v4/groups/"+strings.ReplaceAll(glRef(ref), "/", "%2F"), nil)
}

func (g *GitLab) GroupBoards(ctx context.Context, gid int64) ([]map[string]any, error) {
	return g.list(ctx, fmt.Sprintf("/api/v4/groups/%d/boards", gid))
}

func (g *GitLab) GroupBoard(ctx context.Context, gid, bid int64) (map[string]any, error) {
	return g.obj(ctx, "GET", fmt.Sprintf("/api/v4/groups/%d/boards/%d", gid, bid), nil)
}

func (g *GitLab) GroupIssues(ctx context.Context, gid int64, after string, labels []string, cap int) ([]map[string]any, error) {
	return g.pages(ctx, fmt.Sprintf("/api/v4/groups/%d/issues", gid), issueQuery(after, labels, true), cap)
}

// GroupProjects are the group's projects (subgroups included) with issues turned on, archived ones left out.
func (g *GitLab) GroupProjects(ctx context.Context, gid int64, cap int) ([]map[string]any, error) {
	return g.pages(ctx, fmt.Sprintf("/api/v4/groups/%d/projects", gid), []string{"with_issues_enabled", "true",
		"include_subgroups", "true", "archived", "false", "simple", "true", "order_by", "path", "sort", "asc"}, cap)
}

func (g *GitLab) Notes(ctx context.Context, pid, iid int64) ([]map[string]any, error) {
	return g.pages(ctx, fmt.Sprintf("/api/v4/projects/%d/issues/%d/notes", pid, iid), []string{"sort", "asc", "order_by", "created_at"}, 100)
}

func (g *GitLab) AddNote(ctx context.Context, pid, iid int64, body string) (map[string]any, error) {
	return g.obj(ctx, "POST", fmt.Sprintf("/api/v4/projects/%d/issues/%d/notes", pid, iid), object{{"body", body}})
}
