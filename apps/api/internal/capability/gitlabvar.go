package capability

// gitlabvar.go: gitlab.ci-variable.get|set|delete (ADR-0022 D3, PR 3). Project CI/CD variables of the projects on
// ARGUS_ACT_GITLAB_PROJECTS, environment scope exactly "develop". The value of a variable never leaves this file:
// not in a plan, a result, a detail, an error or a log. The old value is sealed to argus-act/backups before every
// write; set and delete roll back from it. The HTTP client's guard allows only the variables endpoints of a project
// and the lookup of the listed projects; pipelines, jobs, triggers and deployments are refused even if a bug here
// asked for them. Argus never starts a pipeline or plays a job (O3).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The capability names, the one scope and the credential name.
const (
	GitLabVarGet    = "gitlab.ci-variable.get"
	GitLabVarSet    = "gitlab.ci-variable.set"
	GitLabVarDelete = "gitlab.ci-variable.delete"
	GitLabScope     = "develop"
	// GitLabCredential is the stored group access token (argus actd credential set gitlab/token)
	GitLabCredential = "gitlab/token"
)

const glMaxBody = 64 << 10

var (
	glKeyRe     = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
	glIDRe      = regexp.MustCompile(`^[1-9][0-9]{0,11}$`)
	glPathRe    = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(/[A-Za-z0-9_.][A-Za-z0-9_.-]*)+$`)
	glProjectRe = regexp.MustCompile(`^([1-9][0-9]{0,11}|[A-Za-z0-9_.][A-Za-z0-9_.-]*(/[A-Za-z0-9_.][A-Za-z0-9_.-]*)+)$`)
	glTypeRe    = regexp.MustCompile(`^(env_var|file)$`)
)

// glDenied are path segments the GitLab guard never lets through (pipelines, jobs, triggers, deployments), whatever
// a rule says.
var glDenied = map[string]bool{"pipeline": true, "pipelines": true, "jobs": true, "job": true, "trigger": true, "triggers": true,
	"deployments": true, "deploy": true, "deploy_keys": true, "deploy_tokens": true, "environments": true, "schedules": true,
	"pipeline_schedules": true, "play": true, "retry": true, "runners": true}

// GitLabOptions configure the GitLab family.
type GitLabOptions struct {
	URL        string   // ARGUS_ACT_GITLAB_URL: https://gitlab.example.com (a path prefix is allowed)
	Projects   []string // ARGUS_ACT_GITLAB_PROJECTS: ids or paths
	AllowPlain bool     // plain http (test installations only)
	// Token is the group access token (actd: from argus-act); nil in the worker, which only validates
	Token     func() (string, error)
	Backups   Backups           // actd: argus-act/backups; nil in the worker
	Transport http.RoundTripper // the base transport under the guard (nil: a direct one); tests set it
}

// GitLab is the shared client of the three gitlab.ci-variable capabilities.
type GitLab struct {
	base    *url.URL
	prefix  string   // the URL's path, without a trailing slash
	entries []string // the allow-list: numeric ids and lower-case paths
	token   func() (string, error)
	backups Backups
	client  *http.Client
	dkey    []byte // the precondition digest key: random, in memory only

	mu       sync.Mutex
	resolved bool
	ids      map[int64]string // resolved: project id -> path
	byPath   map[string]int64 // resolved: lower-case path -> id
}

// ParseProjectList reads ARGUS_ACT_GITLAB_PROJECTS: numeric ids or group/project paths (lower case kept).
func ParseProjectList(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range list {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if len(e) > 255 || !(glIDRe.MatchString(e) || glPathRe.MatchString(e)) || strings.Contains(e, "..") {
			return nil, fmt.Errorf("ARGUS_ACT_GITLAB_PROJECTS: %q is not a project id or a group/project path", clipName(e))
		}
		e = strings.ToLower(e)
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// NewGitLab builds the GitLab family for an allow-list.
func NewGitLab(o GitLabOptions) (*GitLab, error) {
	entries, err := ParseProjectList(o.Projects)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("gitlab.ci-variable: the project allow-list is empty")
	}
	u, err := url.Parse(strings.TrimSpace(o.URL))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && o.AllowPlain) {
		return nil, errors.New("ARGUS_ACT_GITLAB_URL must be an https URL without credentials or query (plain http only for test installations)")
	}
	g := &GitLab{base: u, prefix: strings.TrimSuffix(u.EscapedPath(), "/"), entries: entries, token: o.Token, backups: o.Backups,
		dkey: make([]byte, 32)}
	if _, err := rand.Read(g.dkey); err != nil {
		return nil, err
	}
	base := o.Transport
	if base == nil {
		base = &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: MaxCall, DisableKeepAlives: true,
			MaxResponseHeaderBytes: 64 << 10}
	}
	host := strings.ToLower(u.Hostname())
	hostOK := func(h string) bool { return h == host }
	vars := g.prefix + "/api/v4/projects/:id/variables"
	guard := &Guard{Base: base, Deny: g.denied, Rules: []Rule{
		{Method: http.MethodGet, Path: vars + "/:key", Host: hostOK},
		{Method: http.MethodPost, Path: vars, Host: hostOK},
		{Method: http.MethodPut, Path: vars + "/:key", Host: hostOK},
		{Method: http.MethodDelete, Path: vars + "/:key", Host: hostOK},
		{Method: http.MethodGet, Host: hostOK, Match: g.lookupPath},
	}}
	g.client = &http.Client{Transport: guard, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return g, nil
}

// Capabilities are get, set and delete.
func (g *GitLab) Capabilities() []Capability {
	return []Capability{&GitLabGet{g}, &GitLabSet{g}, &GitLabDelete{g}}
}

// denied is the fixed deny list: a pipeline, job, trigger or deployment path is never called. The variable key
// segment of a variables path may hold any key (e.g. a variable named "jobs").
func (g *GitLab) denied(path string) bool {
	rest, ok := strings.CutPrefix(path, g.prefix+"/")
	if !ok {
		return true
	}
	seg := strings.Split(rest, "/")
	for i, s := range seg {
		if i == 5 && len(seg) == 6 && seg[4] == "variables" {
			continue
		}
		u, err := url.PathUnescape(s)
		if err != nil || glDenied[strings.ToLower(u)] || glDenied[strings.ToLower(s)] {
			return true
		}
	}
	return false
}

// lookupPath allows GET /api/v4/projects/<entry> for exactly the allow-list's entries (a path URL-encoded).
func (g *GitLab) lookupPath(path string) bool {
	for _, e := range g.entries {
		if path == g.prefix+"/api/v4/projects/"+url.PathEscape(e) {
			return true
		}
	}
	return false
}

func (g *GitLab) newRequest(ctx context.Context, method, rawPath string, query url.Values, body any) (*http.Request, error) {
	u := *g.base
	p, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, err
	}
	u.Path, u.RawPath = p, rawPath
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if g.token == nil {
		return nil, errors.New("no GitLab credential in this process (argus actd credential set " + GitLabCredential + ")")
	}
	tok, err := g.token()
	tok = strings.TrimSpace(tok)
	if err != nil || tok == "" {
		return nil, errors.New("the GitLab credential is not stored or does not open (argus actd credential set " + GitLabCredential + ")")
	}
	req.Header.Set("PRIVATE-TOKEN", tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-actd/gitlab.ci-variable")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do calls GitLab and reads at most glMaxBody bytes. An error never holds the response body (it may hold a value).
func (g *GitLab) do(req *http.Request) (int, []byte, error) {
	resp, err := g.client.Do(req)
	if err != nil {
		var ge *ErrGuard
		if errors.As(err, &ge) {
			return 0, nil, Refuse(4, CodeAllowList, "%s", ge.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, errors.New("GitLab did not answer in time")
		}
		return 0, nil, errors.New("the GitLab request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, glMaxBody+1))
	if err != nil || len(b) > glMaxBody {
		return resp.StatusCode, nil, errors.New("the GitLab answer is not readable or too large")
	}
	return resp.StatusCode, b, nil
}

// Prepare resolves the allow-list: each id or path once, so an id and a path of the same project are one project.
// actd calls it before it handles requests and again until it succeeds.
func (g *GitLab) Prepare(ctx context.Context) error {
	g.mu.Lock()
	done := g.resolved
	g.mu.Unlock()
	if done {
		return nil
	}
	ids, byPath := map[int64]string{}, map[string]int64{}
	for _, e := range g.entries {
		req, err := g.newRequest(ctx, http.MethodGet, g.prefix+"/api/v4/projects/"+url.PathEscape(e), nil, nil)
		if err != nil {
			return err
		}
		code, b, err := g.do(req)
		if err != nil {
			return err
		}
		if code != http.StatusOK {
			return fmt.Errorf("ARGUS_ACT_GITLAB_PROJECTS: GitLab answered %d for %q (missing, or the token cannot see it)", code, clipName(e))
		}
		var p struct {
			ID   int64  `json:"id"`
			Path string `json:"path_with_namespace"`
		}
		if json.Unmarshal(b, &p) != nil || p.ID <= 0 || !glPathRe.MatchString(p.Path) {
			return fmt.Errorf("ARGUS_ACT_GITLAB_PROJECTS: the GitLab answer for %q is not a project", clipName(e))
		}
		ids[p.ID] = strings.ToLower(p.Path)
		byPath[strings.ToLower(p.Path)] = p.ID
	}
	g.mu.Lock()
	g.ids, g.byPath, g.resolved = ids, byPath, true
	g.mu.Unlock()
	return nil
}

// Projects is the resolved allow-list (id -> path) for the status; nil before Prepare succeeded.
func (g *GitLab) Projects() map[int64]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.resolved {
		return nil
	}
	out := map[int64]string{}
	for k, v := range g.ids {
		out[k] = v
	}
	return out
}

// listed reports whether a project argument is on the allow-list: literally, or (after Prepare) as the other form
// of a listed project. It uses no network.
func (g *GitLab) listed(arg string) bool {
	a := strings.ToLower(arg)
	for _, e := range g.entries {
		if e == a {
			return true
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.resolved {
		return false
	}
	if n, err := strconv.ParseInt(a, 10, 64); err == nil && glIDRe.MatchString(a) {
		_, ok := g.ids[n]
		return ok
	}
	_, ok := g.byPath[a]
	return ok
}

// projectID is the numeric id of a listed project.
func (g *GitLab) projectID(ctx context.Context, arg string) (int64, error) {
	if !g.listed(arg) {
		return 0, Refuse(4, CodeAllowList, "project %q is not on ARGUS_ACT_GITLAB_PROJECTS", clipName(arg))
	}
	if err := g.Prepare(ctx); err != nil {
		return 0, fmt.Errorf("the project allow-list could not be resolved: %v", err)
	}
	a := strings.ToLower(arg)
	g.mu.Lock()
	defer g.mu.Unlock()
	if glIDRe.MatchString(a) {
		n, _ := strconv.ParseInt(a, 10, 64)
		if _, ok := g.ids[n]; ok {
			return n, nil
		}
	} else if n, ok := g.byPath[a]; ok {
		return n, nil
	}
	return 0, Refuse(4, CodeAllowList, "project %q is not on ARGUS_ACT_GITLAB_PROJECTS", clipName(arg))
}

// glVar is a variable as GitLab returns it. Value is nil when GitLab hides it.
type glVar struct {
	Key          string  `json:"key"`
	Value        *string `json:"value"`
	VariableType string  `json:"variable_type"`
	Protected    bool    `json:"protected"`
	Masked       bool    `json:"masked"`
	Raw          bool    `json:"raw"`
	Scope        string  `json:"environment_scope"`
	Hidden       bool    `json:"hidden"`
}

// glAttrs are the attributes set and rollback restore.
type glAttrs struct {
	Type      string `json:"variable_type"`
	Protected bool   `json:"protected"`
	Masked    bool   `json:"masked"`
	Raw       bool   `json:"raw"`
}

func (v *glVar) attrs() glAttrs {
	t := v.VariableType
	if t == "" {
		t = "env_var"
	}
	return glAttrs{Type: t, Protected: v.Protected, Masked: v.Masked, Raw: v.Raw}
}

func (a glAttrs) fields() map[string]any {
	return map[string]any{"variable_type": a.Type, "protected": a.Protected, "masked": a.Masked, "raw": a.Raw}
}

func varPath(prefix string, id int64, key string) string {
	return fmt.Sprintf("%s/api/v4/projects/%d/variables/%s", prefix, id, url.PathEscape(key))
}

func scopeQuery() url.Values { return url.Values{"filter[environment_scope]": {GitLabScope}} }

// getVar reads the variable of scope develop; nil when it does not exist.
func (g *GitLab) getVar(ctx context.Context, id int64, key string) (*glVar, error) {
	req, err := g.newRequest(ctx, http.MethodGet, varPath(g.prefix, id, key), scopeQuery(), nil)
	if err != nil {
		return nil, err
	}
	code, b, err := g.do(req)
	switch {
	case err != nil:
		return nil, err
	case code == http.StatusNotFound:
		return nil, nil
	case code != http.StatusOK:
		return nil, fmt.Errorf("GitLab answered %d to the read of the variable", code)
	}
	var v glVar
	if json.Unmarshal(b, &v) != nil || v.Key != key || (v.Scope != "" && v.Scope != GitLabScope) {
		return nil, errors.New("the GitLab answer is not the variable of scope develop")
	}
	if v.Hidden {
		v.Value = nil
	}
	return &v, nil
}

// write creates (POST) or updates (PUT, with the scope filter) the variable.
func (g *GitLab) write(ctx context.Context, id int64, key string, create bool, value string, a glAttrs) error {
	body := map[string]any{"value": value, "variable_type": a.Type, "protected": a.Protected, "masked": a.Masked, "raw": a.Raw,
		"environment_scope": GitLabScope}
	method, path, q := http.MethodPut, varPath(g.prefix, id, key), scopeQuery()
	if create {
		body["key"] = key
		method, path, q = http.MethodPost, fmt.Sprintf("%s/api/v4/projects/%d/variables", g.prefix, id), nil
	}
	req, err := g.newRequest(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	code, _, err := g.do(req)
	switch {
	case err != nil:
		return err
	case code == http.StatusOK || code == http.StatusCreated:
		return nil
	case code == http.StatusBadRequest:
		return errors.New("GitLab answered 400: the value or the attributes were refused (a masked value needs at least 8 characters of the allowed set)")
	}
	return fmt.Errorf("GitLab answered %d to the write of the variable", code)
}

// del deletes the variable of scope develop; a 404 is "already gone".
func (g *GitLab) del(ctx context.Context, id int64, key string) error {
	req, err := g.newRequest(ctx, http.MethodDelete, varPath(g.prefix, id, key), scopeQuery(), nil)
	if err != nil {
		return err
	}
	code, _, err := g.do(req)
	switch {
	case err != nil:
		return err
	case code == http.StatusNoContent || code == http.StatusOK || code == http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("GitLab answered %d to the delete of the variable", code)
}

// digest is the precondition digest of the variable's state (existence, attributes, value), keyed with a random
// in-memory key: it is not a hash of the value anyone can compute or keep.
func (g *GitLab) digest(v *glVar) string {
	m := hmac.New(sha256.New, g.dkey)
	if v == nil {
		m.Write([]byte("absent"))
	} else {
		a := v.attrs()
		fmt.Fprintf(m, "present\n%s\n%t\n%t\n%t\n", a.Type, a.Protected, a.Masked, a.Raw)
		if v.Value == nil {
			m.Write([]byte("hidden"))
		} else {
			m.Write([]byte("value\n" + *v.Value))
		}
	}
	return hex.EncodeToString(m.Sum(nil))
}

// sameValue compares two values in constant time (over their keyed digests, so the lengths do not show).
func (g *GitLab) sameValue(a, b string) bool {
	x, y := hmac.New(sha256.New, g.dkey), hmac.New(sha256.New, g.dkey)
	x.Write([]byte(a))
	y.Write([]byte(b))
	return subtle.ConstantTimeCompare(x.Sum(nil), y.Sum(nil)) == 1
}

func gitLabFields(input bool, write bool) []Field {
	f := []Field{
		{Name: "project", Type: String, Required: true, MaxLen: 255, Pattern: glProjectRe, AllowList: "ARGUS_ACT_GITLAB_PROJECTS"},
		{Name: "key", Type: String, Required: true, MaxLen: 255, Pattern: glKeyRe},
		{Name: "environment_scope", Type: String, Required: true, MaxLen: 64},
	}
	if input && write {
		f = append(f, Field{Name: "value_ref", Type: String, Required: true, MaxLen: 40, Input: true},
			Field{Name: "protected", Type: Bool}, Field{Name: "masked", Type: Bool}, Field{Name: "raw", Type: Bool},
			Field{Name: "variable_type", Type: String, MaxLen: 10, Pattern: glTypeRe})
	} else if input {
		f = append(f, Field{Name: "matches", Type: String, MaxLen: 40, Input: true})
	}
	return f
}

// validate is the common rule of the three: a known classification (write: develop only), the scope exactly
// develop (checked before the schema, so every other scope is refused with its own reason), the schema and the
// project allow-list. No network.
func (g *GitLab) validate(s Schema, tier Tier, args Args, env Env) error {
	if !env.Known() {
		return Refuse(2, CodeEnvironment, "the task's classification is unknown")
	}
	if tier == Write && env != Develop {
		return Refuse(2, CodeEnvironment, "a write-tier capability runs only for develop, the task is %s", env)
	}
	if sc, ok := args["environment_scope"].(string); !ok || sc != GitLabScope {
		return Refuse(4, CodeArguments, "argument \"environment_scope\" must be exactly %q (not *, empty, another environment or another case)", GitLabScope)
	}
	if err := s.Check(args); err != nil {
		return err
	}
	if p, _ := args["project"].(string); !g.listed(p) {
		return Refuse(4, CodeAllowList, "project %q is not on ARGUS_ACT_GITLAB_PROJECTS", clipName(p))
	}
	return nil
}

func argStr(a Args, k string) string { s, _ := a[k].(string); return s }

// target validates again (Plan, Apply, Verify run it before any call) and resolves the project.
func (g *GitLab) target(ctx context.Context, s Schema, tier Tier, args Args) (int64, string, error) {
	if err := g.validate(s, tier, args, Develop); err != nil {
		return 0, "", err
	}
	id, err := g.projectID(ctx, argStr(args, "project"))
	return id, argStr(args, "key"), err
}

func failed(err error) Result { return Result{Status: "failed", Detail: errDetail(err)} }

// errDetail is an error's text for a result: the capability's own messages only (they never hold a value).
func errDetail(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason
	}
	return err.Error()
}

// ---- gitlab.ci-variable.get -------------------------------------------------------------------------------

// GitLabGet is gitlab.ci-variable.get: whether the variable exists, its attributes, and with matches whether its
// value equals a sealed input. Never the value, its length or a hash.
type GitLabGet struct{ g *GitLab }

func (c *GitLabGet) Name() string   { return GitLabVarGet }
func (c *GitLabGet) Tier() Tier     { return Read }
func (c *GitLabGet) Schema() Schema { return Schema{Fields: gitLabFields(true, false)} }
func (c *GitLabGet) Validate(args Args, env Env) error {
	return c.g.validate(c.Schema(), Read, args, env)
}

func (c *GitLabGet) Plan(_ context.Context, args Args) (Plan, error) {
	if err := c.g.validate(c.Schema(), Read, args, Develop); err != nil {
		return Plan{}, err
	}
	what := ""
	if argStr(args, "matches") != "" {
		what = ", compared with " + argStr(args, "matches")
	}
	return Plan{Change: "none", Summary: fmt.Sprintf("read variable %s (scope develop) of project %s%s", argStr(args, "key"), argStr(args, "project"), what)}, nil
}

func (c *GitLabGet) read(ctx context.Context, args Args) (Result, error) {
	id, key, err := c.g.target(ctx, c.Schema(), Read, args)
	if err != nil {
		return failed(err), err
	}
	want := ""
	if ref := argStr(args, "matches"); ref != "" {
		if want, err = Input(ctx, ref); err != nil {
			return failed(err), err
		}
	}
	v, err := c.g.getVar(ctx, id, key)
	if err != nil {
		return failed(err), err
	}
	f := map[string]any{"exists": v != nil, "project_id": id}
	res := Result{Status: "ok", Fields: f}
	if v != nil {
		for k, x := range v.attrs().fields() {
			f[k] = x
		}
		res.Detail = fmt.Sprintf("variable %s exists (scope develop)", key)
	} else {
		res.Detail = fmt.Sprintf("variable %s does not exist (scope develop)", key)
	}
	if argStr(args, "matches") != "" {
		m := v != nil && v.Value != nil && c.g.sameValue(*v.Value, want)
		f["matches"] = m
		if !m {
			res.Status = "mismatch"
			res.Detail += "; the value does not match the input"
		} else {
			res.Detail += "; the value matches the input"
		}
	}
	return res, nil
}

func (c *GitLabGet) Apply(ctx context.Context, args Args, _ Plan) (Result, BackupRef, error) {
	r, err := c.read(ctx, args)
	return r, "", err
}

func (c *GitLabGet) Verify(ctx context.Context, args Args, _ Plan) (Result, error) {
	return c.read(ctx, args)
}

func (c *GitLabGet) Rollback(context.Context, BackupRef) (Result, error) {
	return Result{}, ErrNoRollback
}

// ---- gitlab.ci-variable.set -------------------------------------------------------------------------------

// GitLabSet is gitlab.ci-variable.set: create or update the variable of scope develop with the sealed input's value.
type GitLabSet struct{ g *GitLab }

func (c *GitLabSet) Name() string   { return GitLabVarSet }
func (c *GitLabSet) Tier() Tier     { return Write }
func (c *GitLabSet) Schema() Schema { return Schema{Fields: gitLabFields(true, true)} }
func (c *GitLabSet) Validate(args Args, env Env) error {
	return c.g.validate(c.Schema(), Write, args, env)
}

// wanted is the attributes after the set: the given ones, else the current ones, else GitLab's defaults.
func wanted(cur *glVar, args Args) glAttrs {
	a := glAttrs{Type: "env_var"}
	if cur != nil {
		a = cur.attrs()
	}
	if v, ok := args["protected"].(bool); ok {
		a.Protected = v
	}
	if v, ok := args["masked"].(bool); ok {
		a.Masked = v
	}
	if v, ok := args["raw"].(bool); ok {
		a.Raw = v
	}
	if v := argStr(args, "variable_type"); v != "" {
		a.Type = v
	}
	return a
}

func attrsLine(cur *glVar, want glAttrs) string {
	w := want.fields()
	var parts []string
	keys := []string{"variable_type", "protected", "masked", "raw"}
	if cur == nil {
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %v", k, w[k]))
		}
		return strings.Join(parts, ", ")
	}
	c := cur.attrs().fields()
	for _, k := range keys {
		if c[k] != w[k] {
			parts = append(parts, fmt.Sprintf("%s %v -> %v", k, c[k], w[k]))
		}
	}
	if len(parts) == 0 {
		return "attributes unchanged"
	}
	return strings.Join(parts, ", ")
}

type setState struct {
	id     int64
	key    string
	value  string
	cur    *glVar
	want   glAttrs
	change string
}

func (c *GitLabSet) state(ctx context.Context, args Args) (setState, error) {
	id, key, err := c.g.target(ctx, c.Schema(), Write, args)
	if err != nil {
		return setState{}, err
	}
	val, err := Input(ctx, argStr(args, "value_ref"))
	if err != nil {
		return setState{}, err
	}
	cur, err := c.g.getVar(ctx, id, key)
	if err != nil {
		return setState{}, err
	}
	st := setState{id: id, key: key, value: val, cur: cur, want: wanted(cur, args)}
	switch {
	case cur == nil:
		st.change = "created"
	case cur.Value == nil:
		return st, Refuse(4, CodeArguments, "variable %s is hidden in GitLab: its value cannot be backed up, so Argus does not change it", key)
	case c.g.sameValue(*cur.Value, val) && cur.attrs() == st.want:
		st.change = "unchanged"
	default:
		st.change = "updated"
	}
	return st, nil
}

func (c *GitLabSet) Plan(ctx context.Context, args Args) (Plan, error) {
	st, err := c.state(ctx, args)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Change: st.change, Digest: c.g.digest(st.cur),
		Summary: fmt.Sprintf("variable %s (scope develop) of project %d: %s; %s", st.key, st.id, st.change, attrsLine(st.cur, st.want))}, nil
}

func (c *GitLabSet) Apply(ctx context.Context, args Args, plan Plan) (Result, BackupRef, error) {
	info, ok := StepFrom(ctx)
	if !ok {
		err := errors.New("no action step: a write runs only in actd")
		return failed(err), "", err
	}
	st, err := c.state(ctx, args)
	if err != nil {
		return failed(err), "", err
	}
	if plan.Digest == "" || subtle.ConstantTimeCompare([]byte(c.g.digest(st.cur)), []byte(plan.Digest)) != 1 {
		return Result{Status: "failed", Detail: "the variable changed between plan and apply"}, "", ErrPrecondition
	}
	f := st.want.fields()
	f["change"], f["project_id"] = st.change, st.id
	if st.change == "unchanged" {
		return Result{Status: "unchanged", Detail: fmt.Sprintf("variable %s already holds the input and the attributes", st.key), Fields: f}, "", nil
	}
	ref, err := c.g.backup(info, st.id, st.key, st.cur)
	if err != nil {
		return failed(err), "", err
	}
	if err := c.g.write(ctx, st.id, st.key, st.cur == nil, st.value, st.want); err != nil {
		return failed(err), ref, err
	}
	return Result{Status: "ok", Detail: fmt.Sprintf("variable %s %s (scope develop); the old state is backed up", st.key, st.change), Fields: f}, ref, nil
}

func (c *GitLabSet) Verify(ctx context.Context, args Args, _ Plan) (Result, error) {
	id, key, err := c.g.target(ctx, c.Schema(), Write, args)
	if err != nil {
		return failed(err), err
	}
	val, err := Input(ctx, argStr(args, "value_ref"))
	if err != nil {
		return failed(err), err
	}
	v, err := c.g.getVar(ctx, id, key)
	if err != nil {
		return failed(err), err
	}
	if v == nil || v.Value == nil {
		return Result{Status: "mismatch", Detail: fmt.Sprintf("variable %s is not readable after the write", key)}, nil
	}
	want := wanted(v, args)
	f := v.attrs().fields()
	if !c.g.sameValue(*v.Value, val) || v.attrs() != want {
		return Result{Status: "mismatch", Detail: fmt.Sprintf("variable %s does not hold the input and the attributes after the write", key), Fields: f}, nil
	}
	return Result{Status: "ok", Detail: fmt.Sprintf("variable %s read back: the value and the attributes match", key), Fields: f}, nil
}

func (c *GitLabSet) Rollback(ctx context.Context, ref BackupRef) (Result, error) {
	return c.g.rollback(ctx, ref)
}

// ---- gitlab.ci-variable.delete ----------------------------------------------------------------------------

// GitLabDelete is gitlab.ci-variable.delete: back up, then delete the variable of scope develop.
type GitLabDelete struct{ g *GitLab }

func (c *GitLabDelete) Name() string   { return GitLabVarDelete }
func (c *GitLabDelete) Tier() Tier     { return Write }
func (c *GitLabDelete) Schema() Schema { return Schema{Fields: gitLabFields(false, true)} }
func (c *GitLabDelete) Validate(args Args, env Env) error {
	return c.g.validate(c.Schema(), Write, args, env)
}

func (c *GitLabDelete) state(ctx context.Context, args Args) (int64, string, *glVar, error) {
	id, key, err := c.g.target(ctx, c.Schema(), Write, args)
	if err != nil {
		return 0, "", nil, err
	}
	v, err := c.g.getVar(ctx, id, key)
	if err != nil {
		return 0, "", nil, err
	}
	if v != nil && v.Value == nil {
		return 0, "", nil, Refuse(4, CodeArguments, "variable %s is hidden in GitLab: its value cannot be backed up, so Argus does not delete it", key)
	}
	return id, key, v, nil
}

func (c *GitLabDelete) Plan(ctx context.Context, args Args) (Plan, error) {
	id, key, v, err := c.state(ctx, args)
	if err != nil {
		return Plan{}, err
	}
	if v == nil {
		return Plan{Change: "unchanged", Digest: c.g.digest(nil), Summary: fmt.Sprintf("variable %s (scope develop) of project %d does not exist: nothing to delete", key, id)}, nil
	}
	return Plan{Change: "deleted", Digest: c.g.digest(v), Summary: fmt.Sprintf("variable %s (scope develop) of project %d: deleted after a backup", key, id)}, nil
}

func (c *GitLabDelete) Apply(ctx context.Context, args Args, plan Plan) (Result, BackupRef, error) {
	info, ok := StepFrom(ctx)
	if !ok {
		err := errors.New("no action step: a write runs only in actd")
		return failed(err), "", err
	}
	id, key, v, err := c.state(ctx, args)
	if err != nil {
		return failed(err), "", err
	}
	if plan.Digest == "" || subtle.ConstantTimeCompare([]byte(c.g.digest(v)), []byte(plan.Digest)) != 1 {
		return Result{Status: "failed", Detail: "the variable changed between plan and apply"}, "", ErrPrecondition
	}
	if v == nil {
		return Result{Status: "unchanged", Detail: fmt.Sprintf("variable %s does not exist", key), Fields: map[string]any{"exists": false, "project_id": id}}, "", nil
	}
	ref, err := c.g.backup(info, id, key, v)
	if err != nil {
		return failed(err), "", err
	}
	if err := c.g.del(ctx, id, key); err != nil {
		return failed(err), ref, err
	}
	return Result{Status: "ok", Detail: fmt.Sprintf("variable %s deleted (scope develop); the old value is backed up", key),
		Fields: map[string]any{"exists": false, "project_id": id}}, ref, nil
}

func (c *GitLabDelete) Verify(ctx context.Context, args Args, _ Plan) (Result, error) {
	id, key, err := c.g.target(ctx, c.Schema(), Write, args)
	if err != nil {
		return failed(err), err
	}
	v, err := c.g.getVar(ctx, id, key)
	if err != nil {
		return failed(err), err
	}
	if v != nil {
		return Result{Status: "mismatch", Detail: fmt.Sprintf("variable %s still exists", key)}, nil
	}
	return Result{Status: "ok", Detail: fmt.Sprintf("variable %s is gone (404)", key), Fields: map[string]any{"exists": false}}, nil
}

func (c *GitLabDelete) Rollback(ctx context.Context, ref BackupRef) (Result, error) {
	return c.g.rollback(ctx, ref)
}

// ---- backup and rollback ----------------------------------------------------------------------------------

// glBackup is the sealed old state of one variable.
type glBackup struct {
	Existed bool    `json:"existed"`
	Value   string  `json:"value,omitempty"`
	Attrs   glAttrs `json:"attrs"`
}

func glMeta(id int64, key string) string {
	return fmt.Sprintf("gitlab.ci-variable project=%d key=%s scope=%s", id, key, GitLabScope)
}

var glMetaRe = regexp.MustCompile(`^gitlab\.ci-variable project=([1-9][0-9]{0,11}) key=([A-Za-z0-9_]{1,255}) scope=develop$`)

// backup seals the variable's state before a write (backups/<action-id>/<step>.enc).
func (g *GitLab) backup(info StepInfo, id int64, key string, cur *glVar) (BackupRef, error) {
	if g.backups == nil {
		return "", errors.New("no backup store in this process: nothing is written")
	}
	b := glBackup{Existed: cur != nil}
	if cur != nil {
		b.Value, b.Attrs = *cur.Value, cur.attrs()
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	ref := BackupRefOf(info.ActionID, info.Step)
	if err := g.backups.Put(ref, glMeta(id, key), plain); err != nil {
		return "", fmt.Errorf("the backup could not be written, nothing is changed: %v", err)
	}
	return ref, nil
}

// rollback restores the backed-up state: the old value and attributes, or no variable when there was none. It
// reads the variable back and says whether it holds the old state.
func (g *GitLab) rollback(ctx context.Context, ref BackupRef) (Result, error) {
	if g.backups == nil {
		err := errors.New("no backup store in this process")
		return failed(err), err
	}
	meta, plain, err := g.backups.Get(ref)
	if err != nil {
		err = fmt.Errorf("the backup is not readable: %v", err)
		return failed(err), err
	}
	m := glMetaRe.FindStringSubmatch(meta)
	var b glBackup
	if m == nil || json.Unmarshal(plain, &b) != nil || (b.Existed && !glTypeRe.MatchString(b.Attrs.Type)) {
		err := errors.New("the backup is not a gitlab.ci-variable backup")
		return failed(err), err
	}
	key := m[2]
	id, err := g.projectID(ctx, m[1])
	if err != nil {
		return failed(err), err
	}
	cur, err := g.getVar(ctx, id, key)
	if err != nil {
		return failed(err), err
	}
	if !b.Existed {
		if cur != nil {
			if err := g.del(ctx, id, key); err != nil {
				return failed(err), err
			}
		}
		if v, err := g.getVar(ctx, id, key); err != nil || v != nil {
			if err == nil {
				err = fmt.Errorf("variable %s still exists after the rollback", key)
			}
			return failed(err), err
		}
		return Result{Status: "ok", Detail: fmt.Sprintf("variable %s deleted: it did not exist before", key), Fields: map[string]any{"exists": false}}, nil
	}
	if err := g.write(ctx, id, key, cur == nil, b.Value, b.Attrs); err != nil {
		return failed(err), err
	}
	v, err := g.getVar(ctx, id, key)
	if err != nil {
		return failed(err), err
	}
	if v == nil || v.Value == nil || !g.sameValue(*v.Value, b.Value) || v.attrs() != b.Attrs {
		err := fmt.Errorf("variable %s does not hold the old value and attributes after the rollback", key)
		return failed(err), err
	}
	return Result{Status: "ok", Detail: fmt.Sprintf("variable %s restored: the old value and attributes", key), Fields: b.Attrs.fields()}, nil
}
