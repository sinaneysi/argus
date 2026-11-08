package board

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Jira is a small Jira Server / Data Center REST v2 client. The token is a personal access token sent as
// "Authorization: Bearer"; it is never logged and never in an error. HTTPS is verified (system roots, or CAFile);
// plain http only to a loopback address (local test servers). Only /rest/api/2/* and /rest/agile/1.0/* paths are
// called; redirects are never followed. At most Budget calls per run, MinGap between calls, 429 honours
// Retry-After, 5xx and network errors are retried with backoff, 401 (and 403 on /myself) fail at once.
type Jira struct {
	Base    string
	token   string
	prefix  string
	client  *http.Client
	Budget  int
	MinGap  time.Duration
	Retries int
	MaxWait time.Duration
	Sleep   func(time.Duration)
	Calls   int
	last    time.Time
	DryRun  bool // log writes (POST/PUT) instead of sending them; reads still go out
	Log     *slog.Logger
	dryN    int
}

// JiraError is a failed Jira call; Status is the HTTP status (0 for a network error).
type JiraError struct {
	Msg    string
	Status int
	Kind   string // "", "auth", "ratelimit", "budget"
}

func (e *JiraError) Error() string { return e.Msg }

func jerr(status int, kind, format string, a ...any) *JiraError {
	return &JiraError{Msg: fmt.Sprintf(format, a...), Status: status, Kind: kind}
}

// IsAuth, IsRate, IsBudget classify an error.
func IsAuth(err error) bool { var j *JiraError; return errors.As(err, &j) && j.Kind == "auth" }
func isStop(err error) bool {
	var j *JiraError
	return errors.As(err, &j) && (j.Kind == "auth" || j.Kind == "ratelimit" || j.Kind == "budget")
}
func asJira(err error) (*JiraError, bool) { var j *JiraError; ok := errors.As(err, &j); return j, ok }

var (
	pathRe = regexp.MustCompile(`^/rest/(api/2|agile/1\.0)/[A-Za-z0-9/_.~-]*$`)
)

// NewJira checks the URL and token and builds the client.
func NewJira(base, token, caFile string) (*Jira, error) {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, jerr(0, "", "JIRA_URL must be https://")
	}
	loop := false
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
		loop = true
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return nil, jerr(0, "", "JIRA_URL must be https://")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, jerr(0, "", "JIRA_URL must be a plain https://host[/path] URL")
	}
	if token == "" || strings.ContainsAny(token, "\r\n \t") {
		return nil, jerr(0, "auth", "no usable token")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pool, perr := x509.SystemCertPool()
		if perr != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pem, rerr := os.ReadFile(caFile)
		if rerr != nil || !pool.AppendCertsFromPEM(pem) {
			return nil, jerr(0, "", "JIRA_CA_FILE: no PEM certificate found")
		}
		tc.RootCAs = pool
	}
	tr.TLSClientConfig = tc
	tr.Proxy = nil
	return &Jira{Base: base, token: token, prefix: strings.TrimRight(u.Path, "/"), Budget: 300,
		MinGap: 100 * time.Millisecond, Retries: 3, MaxWait: 60 * time.Second, Sleep: time.Sleep,
		client: &http.Client{Transport: tr, Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (j *Jira) scrub(s string) string {
	if j.token != "" {
		s = strings.ReplaceAll(s, j.token, "[REDACTED]")
	}
	return s
}

func reason(body []byte) string {
	var d map[string]any
	if json.Unmarshal(body, &d) != nil {
		var x any
		if json.Unmarshal(body, &x) == nil {
			return ""
		}
		s := string(body)
		if len(s) > 160 {
			s = s[:160]
		}
		return strings.TrimSpace(s)
	}
	var msgs []string
	if em, ok := d["errorMessages"].([]any); ok {
		for _, m := range em {
			msgs = append(msgs, pyStr(m))
		}
	}
	if es, ok := d["errors"].(map[string]any); ok {
		for _, k := range orderedKeys(body, "errors") {
			if v, ok := es[k]; ok {
				msgs = append(msgs, k+": "+pyStr(v))
			}
		}
	}
	return head(strings.Join(msgs, "; "), 300)
}

// orderedKeys are the keys of the object at top-level field `field` in document order.
func orderedKeys(body []byte, field string) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(top[field]))
	var out []string
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return out
		}
		out = append(out, t.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return out
		}
	}
	return out
}

// Request calls one REST path. query is in order (key, value pairs).
func (j *Jira) Request(ctx context.Context, method, path string, body any, query ...string) (any, error) {
	if !pathRe.MatchString(path) || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return nil, jerr(0, "", "path not allowed: %s", head(path, 60))
	}
	target := j.Base[:len(j.Base)-len(j.prefix)] + j.prefix + path
	if len(query) > 0 {
		v := make([]string, 0, len(query)/2)
		for i := 0; i+1 < len(query); i += 2 {
			v = append(v, pyQuote(query[i])+"="+pyQuote(query[i+1]))
		}
		target += "?" + strings.Join(v, "&")
	}
	var data []byte
	if body != nil {
		data = pyDumps(body)
	}
	if j.DryRun && method != http.MethodGet && !(method == http.MethodPost && path == "/rest/api/2/search") {
		j.dryN++
		if j.Log != nil {
			j.Log.Info("dry run: not sent", "method", method, "path", path, "body", head(j.scrub(string(data)), 400))
		}
		return dryAnswer(method, path, j.dryN), nil
	}
	attempt := 0
	for {
		if j.Calls >= j.Budget {
			return nil, jerr(0, "budget", "request budget of %d per run used up", j.Budget)
		}
		if gap := j.MinGap - time.Since(j.last); gap > 0 && !j.last.IsZero() {
			j.Sleep(gap)
		}
		j.Calls++
		j.last = time.Now()
		var rd io.Reader
		if data != nil {
			rd = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rd)
		if err != nil {
			return nil, jerr(0, "", "bad request")
		}
		req.Header.Set("Authorization", "Bearer "+j.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "argus-board-sync")
		if data != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := j.client.Do(req)
		if err != nil {
			if attempt < j.Retries {
				j.Sleep(time.Duration(min(1<<attempt, 8)) * time.Second)
				attempt++
				continue
			}
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return nil, jerr(0, "", "%s", head(j.scrub("cannot reach Jira: "+err.Error()), 200))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		st := resp.StatusCode
		if st >= 200 && st < 300 {
			if len(bytes.TrimSpace(raw)) == 0 {
				return map[string]any{}, nil
			}
			var out any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if d.Decode(&out) != nil {
				return nil, jerr(st, "", "Jira answered %s %s with something that is not JSON", method, path)
			}
			return out, nil
		}
		if st >= 300 && st < 400 {
			return nil, jerr(st, "", "Jira answered with a redirect (HTTP %d); redirects are not followed - check the URL", st)
		}
		why := j.scrub(reason(raw))
		suffix := ""
		if why != "" {
			suffix = ": " + why
		}
		if st == 401 || (st == 403 && strings.HasPrefix(path, "/rest/api/2/myself")) {
			return nil, jerr(st, "auth", "Jira refused the token (HTTP %d)%s", st, suffix)
		}
		ra := resp.Header.Get("Retry-After")
		if st == 429 || (st == 503 && ra != "") {
			wait := 5.0
			if ra != "" {
				if f, err := strconv.ParseFloat(ra, 64); err == nil {
					wait = f
				}
			}
			if attempt >= j.Retries || time.Duration(wait*float64(time.Second)) > j.MaxWait {
				return nil, jerr(st, "ratelimit", "Jira rate limit (HTTP %d, retry after %.0f s)", st, wait)
			}
			j.Sleep(time.Duration(max(wait, 0.5) * float64(time.Second)))
			attempt++
			continue
		}
		if st >= 500 && attempt < j.Retries {
			j.Sleep(time.Duration(min(1<<attempt, 8)) * time.Second)
			attempt++
			continue
		}
		p, _, _ := strings.Cut(path, "?")
		return nil, jerr(st, "", "HTTP %d on %s %s%s", st, method, p, suffix)
	}
}

// dryAnswer is what a dry run pretends Jira answered to a write.
func dryAnswer(method, path string, n int) any {
	if method == http.MethodPost && path == "/rest/api/2/issue" {
		return map[string]any{"key": "DRYRUN-" + strconv.Itoa(n), "id": strconv.Itoa(n)}
	}
	if strings.HasSuffix(path, "/comment") {
		return map[string]any{"id": "dry-" + strconv.Itoa(n)}
	}
	return map[string]any{}
}

func esc(key string) string { return pyQuote(key) }

func (j *Jira) Myself(ctx context.Context) (map[string]any, error) {
	r, err := j.Request(ctx, "GET", "/rest/api/2/myself", nil)
	return asMap(r), err
}

func (j *Jira) Search(ctx context.Context, jql string, fields []string, start, limit int) (map[string]any, error) {
	r, err := j.Request(ctx, "POST", "/rest/api/2/search", object{{"jql", jql}, {"startAt", start}, {"maxResults", limit}, {"fields", fields}})
	return asMap(r), err
}

func (j *Jira) Issue(ctx context.Context, key string, fields []string, expand string) (map[string]any, error) {
	var q []string
	if len(fields) > 0 {
		q = append(q, "fields", strings.Join(fields, ","))
	}
	if expand != "" {
		q = append(q, "expand", expand)
	}
	r, err := j.Request(ctx, "GET", "/rest/api/2/issue/"+esc(key), nil, q...)
	return asMap(r), err
}

func (j *Jira) Create(ctx context.Context, fields object) (map[string]any, error) {
	r, err := j.Request(ctx, "POST", "/rest/api/2/issue", object{{"fields", fields}})
	return asMap(r), err
}

func (j *Jira) Transitions(ctx context.Context, key string) ([]any, error) {
	r, err := j.Request(ctx, "GET", "/rest/api/2/issue/"+esc(key)+"/transitions", nil)
	if err != nil {
		return nil, err
	}
	ts, _ := asMap(r)["transitions"].([]any)
	return ts, nil
}

func (j *Jira) Transition(ctx context.Context, key, id string) error {
	_, err := j.Request(ctx, "POST", "/rest/api/2/issue/"+esc(key)+"/transitions", object{{"transition", object{{"id", id}}}})
	return err
}

func (j *Jira) Comment(ctx context.Context, key, body string) (map[string]any, error) {
	r, err := j.Request(ctx, "POST", "/rest/api/2/issue/"+esc(key)+"/comment", object{{"body", body}})
	return asMap(r), err
}

func (j *Jira) Assign(ctx context.Context, key, name string) error {
	_, err := j.Request(ctx, "PUT", "/rest/api/2/issue/"+esc(key)+"/assignee", object{{"name", name}})
	return err
}

func (j *Jira) AddLabel(ctx context.Context, key, label string, add bool) error {
	op := "add"
	if !add {
		op = "remove"
	}
	_, err := j.Request(ctx, "PUT", "/rest/api/2/issue/"+esc(key), object{{"update", object{{"labels", []any{object{{op, label}}}}}}})
	return err
}
