package sources

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Obviously fake material: no real host, user or credential (Northwind is a neutral example name).
var (
	cred  = "fake-cred-" + strings.Repeat("z", 24)
	other = "fake-other-" + strings.Repeat("q", 24)
)

type route struct {
	st  int
	doc any
}

type fake struct {
	mu     sync.Mutex
	routes map[string]route // "GET /path"
	seen   []string         // the auth header of every call
	srv    *httptest.Server
	url    string
}

func (f *fake) set(r map[string]route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range r {
		f.routes[k] = v
	}
}

func (f *fake) del(k string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routes, k)
}

func serve(t *testing.T, routes map[string]route) *fake {
	t.Helper()
	testAllowLocal = true
	t.Cleanup(func() { testAllowLocal = false })
	f := &fake{routes: map[string]route{}}
	f.set(routes)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		a := r.Header.Get("Authorization")
		if a == "" {
			a = r.Header.Get("PRIVATE-TOKEN")
		}
		f.seen = append(f.seen, a)
		rt, ok := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if !ok {
			rt = route{404, map[string]any{"error": "not found"}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rt.st)
		json.NewEncoder(w).Encode(rt.doc)
	}))
	t.Cleanup(f.srv.Close)
	f.url = f.srv.URL
	return f
}

type M = map[string]any
type L = []any

func copyRoutes(r map[string]route) map[string]route {
	out := map[string]route{}
	for k, v := range r {
		out[k] = v
	}
	return out
}

var (
	gitlabRO = map[string]route{
		"GET /api/v4/personal_access_tokens/self": {200, M{"name": "nw-ro", "scopes": L{"read_api", "read_repository"}, "active": true, "revoked": false, "expires_at": "2027-01-01"}},
		"GET /api/v4/user":                        {200, M{"username": "nw-ro"}}}
	gitlabAPI = map[string]route{
		"GET /api/v4/personal_access_tokens/self": {200, M{"name": "nw-bot", "scopes": L{"api"}, "active": true}},
		"GET /api/v4/user":                        {200, M{"username": "nw-bot", "is_admin": false}}}
	grafanaViewer = map[string]route{
		"GET /api/user":        {200, M{"login": "sa-nw", "orgId": 1, "isGrafanaAdmin": false}},
		"GET /api/user/orgs":   {200, L{M{"orgId": 1, "name": "Main", "role": "Viewer"}}},
		"GET /api/datasources": {200, L{M{"type": "prometheus"}, M{"type": "loki"}, M{"type": "loki"}, M{"type": "mysql"}}}}
)

func grafanaEditor() map[string]route {
	r := copyRoutes(grafanaViewer)
	r["GET /api/user/orgs"] = route{200, L{M{"orgId": 1, "role": "Editor"}}}
	return r
}

func check(t *testing.T, typ string, routes map[string]route, purpose string, username string) (Check, *fake) {
	t.Helper()
	f := serve(t, routes)
	c, err := checks[typ](f.url, cred, &Entry{Type: typ, Name: "x", URL: f.url, Purpose: purpose, Username: username})
	if err != nil {
		t.Fatal(err)
	}
	return c, f
}

func TestGitLab(t *testing.T) {
	c, f := check(t, "gitlab", gitlabRO, "agent", "")
	if c.Status != "readonly" || !strings.Contains(c.Detail, "read_api") || f.seen[0] != cred {
		t.Fatalf("ro: %+v %v", c, f.seen)
	}
	if c, _ = check(t, "gitlab", gitlabAPI, "agent", ""); c.Status != "refused" || !strings.Contains(c.Detail, "scopes api") {
		t.Fatalf("api: %+v", c)
	}
	if c, _ = check(t, "gitlab", gitlabAPI, "tracker", ""); c.Status != "ok" {
		t.Fatalf("tracker: %+v", c)
	}
	if c, _ = check(t, "gitlab", gitlabRO, "tracker", ""); c.Status != "failed" || !strings.Contains(c.Detail, "needs scope api") {
		t.Fatalf("tracker ro: %+v", c)
	}
	if c, _ = check(t, "gitlab", map[string]route{"GET /api/v4/personal_access_tokens/self": {401, M{}}}, "agent", ""); c.Status != "failed" {
		t.Fatalf("401: %+v", c)
	}
	if c, _ = check(t, "gitlab", map[string]route{"GET /api/v4/personal_access_tokens/self": {200, M{"scopes": L{"read_api"}, "revoked": true}}}, "agent", ""); c.Status != "failed" {
		t.Fatalf("revoked: %+v", c)
	}
	admin := copyRoutes(gitlabRO)
	admin["GET /api/v4/user"] = route{200, M{"username": "root", "is_admin": true}}
	if c, _ = check(t, "gitlab", admin, "agent", ""); c.Status != "readonly" || !strings.Contains(strings.Join(c.Findings, ";"), "GitLab administrator") {
		t.Fatalf("admin: %+v", c)
	}
}

func TestGrafana(t *testing.T) {
	c, _ := check(t, "grafana", grafanaViewer, "agent", "")
	ds, _ := c.Facts["datasources"].(map[string]int)
	if c.Status != "readonly" || ds["prometheus"] != 1 || ds["loki"] != 2 || ds["other"] != 1 || c.Detail != "read-only: role Viewer, 2 loki, 1 other, 1 prometheus" {
		t.Fatalf("viewer: %+v", c)
	}
	if c, _ = check(t, "grafana", grafanaEditor(), "agent", ""); c.Status != "refused" || !strings.Contains(c.Detail, "role Editor") {
		t.Fatalf("editor: %+v", c)
	}
	sa := copyRoutes(grafanaViewer)
	sa["GET /api/user"] = route{200, M{"login": "admin", "orgId": 1, "isGrafanaAdmin": true}}
	if c, _ = check(t, "grafana", sa, "agent", ""); c.Status != "refused" {
		t.Fatalf("server admin: %+v", c)
	}
	perms := copyRoutes(grafanaViewer)
	delete(perms, "GET /api/user/orgs")
	none := copyRoutes(perms)
	perms["GET /api/access-control/user/permissions"] = route{200, M{"dashboards:read": L{"*"}, "datasources:query": L{"*"}}}
	if c, _ = check(t, "grafana", perms, "agent", ""); c.Status != "readonly" {
		t.Fatalf("perms read: %+v", c)
	}
	perms["GET /api/access-control/user/permissions"] = route{200, M{"dashboards:write": L{"*"}}}
	if c, _ = check(t, "grafana", perms, "agent", ""); c.Status != "refused" {
		t.Fatalf("perms write: %+v", c)
	}
	if c, _ = check(t, "grafana", none, "agent", ""); c.Status != "refused" || !strings.Contains(c.Detail, "could not verify") {
		t.Fatalf("unknown: %+v", c)
	}
	if c, _ = check(t, "grafana", map[string]route{"GET /api/user": {401, M{}}}, "agent", ""); c.Status != "failed" {
		t.Fatal("401")
	}
	if c, _ = check(t, "grafana", map[string]route{"GET /api/user": {502, M{}}}, "agent", ""); c.Status != "unreachable" {
		t.Fatal("502")
	}
}

func outlineInfo(role string) map[string]route {
	return map[string]route{"POST /api/auth.info": {200, M{"data": M{"user": M{"name": "Argus", "role": role}, "team": M{"name": "Northwind"}}}}}
}

func TestOutline(t *testing.T) {
	for _, c := range []struct{ role, purpose, want string }{
		{"member", "docs", "ok"}, {"viewer", "docs", "failed"}, {"viewer", "agent", "readonly"}, {"admin", "agent", "refused"},
	} {
		if r, _ := check(t, "outline", outlineInfo(c.role), c.purpose, ""); r.Status != c.want {
			t.Fatalf("%v: %+v", c, r)
		}
	}
	if r, _ := check(t, "outline", map[string]route{"POST /api/auth.info": {401, M{}}}, "docs", ""); r.Status != "failed" {
		t.Fatal("401")
	}
}

func TestElasticsearch(t *testing.T) {
	root := M{"cluster_name": "nw-logs", "version": M{"number": "8.14.0"}}
	priv := func(extra M) route {
		idx := M{"read": true, "write": false, "delete": false, "create_index": false, "manage": false}
		for k, v := range extra {
			idx[k] = v
		}
		return route{200, M{"cluster": M{"manage": false, "monitor": true, "all": false}, "index": M{"*": idx}}}
	}
	ro := map[string]route{"GET /": {200, root}, "GET /_security/_authenticate": {200, M{"username": "nw-reader"}},
		"POST /_security/user/_has_privileges": priv(nil)}
	c, f := check(t, "elasticsearch", ro, "agent", "nw-reader")
	if c.Status != "readonly" || !strings.HasPrefix(f.seen[0], "Basic ") {
		t.Fatalf("ro basic: %+v", c)
	}
	if _, f = check(t, "elasticsearch", ro, "agent", ""); f.seen[0] != "ApiKey "+cred {
		t.Fatal("api key header")
	}
	rw := copyRoutes(ro)
	rw["POST /_security/user/_has_privileges"] = priv(M{"write": true})
	if c, _ = check(t, "elasticsearch", rw, "agent", ""); c.Status != "refused" || !strings.Contains(c.Detail, "index write on *") {
		t.Fatalf("rw: %+v", c)
	}
	nosec := map[string]route{"GET /": {200, root}, "GET /_security/_authenticate": {500, M{"error": "security not enabled"}}}
	if c, _ = check(t, "elasticsearch", nosec, "agent", ""); c.Status != "refused" || !strings.Contains(c.Detail, "security is off") {
		t.Fatalf("nosec: %+v", c)
	}
	osr := map[string]route{"GET /": {200, M{"version": M{"distribution": "opensearch", "number": "2.15.0"}}},
		"GET /_plugins/_security/authinfo": {200, M{"user_name": "nw-reader", "roles": L{"readall"}}}}
	if c, _ = check(t, "elasticsearch", osr, "agent", ""); c.Status != "readonly" {
		t.Fatalf("opensearch ro: %+v", c)
	}
	osr["GET /_plugins/_security/authinfo"] = route{200, M{"user_name": "nw", "roles": L{"readall", "all_access"}}}
	if c, _ = check(t, "elasticsearch", osr, "agent", ""); c.Status != "refused" || !strings.Contains(strings.Join(c.Findings, ";"), "role all_access (not a known read-only role)") {
		t.Fatalf("opensearch rw: %+v", c)
	}
}

func TestSentry(t *testing.T) {
	api := func(scopes L) map[string]route {
		return map[string]route{"GET /api/0/": {200, M{"auth": M{"scopes": scopes}, "user": M{"username": "nw"}}},
			"GET /api/0/organizations/": {200, L{M{"slug": "northwind"}}}}
	}
	c, _ := check(t, "sentry", api(L{"project:read", "org:read", "event:read"}), "agent", "")
	if c.Status != "readonly" || c.Facts["organizations"] != 1 {
		t.Fatalf("ro: %+v", c)
	}
	if c, _ = check(t, "sentry", api(L{"project:read", "project:write"}), "agent", ""); c.Status != "refused" {
		t.Fatalf("rw: %+v", c)
	}
	if c, _ = check(t, "sentry", map[string]route{"GET /api/0/": {200, M{"version": "0"}}}, "agent", ""); c.Status != "failed" {
		t.Fatalf("no auth: %+v", c)
	}
}

func TestURLAndAddressGuard(t *testing.T) {
	testAllowLocal = false
	for _, bad := range []string{"http://grafana.northwind.example", "https://u" + ":p" + "@h.example", "https://h.example/?q=1",
		"https://h.example/#x", "ftp://h.example", "https://h .example", "https://h.example:99999", ""} {
		if _, err := CleanURL(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if u, err := CleanURL("https://Grafana.Northwind.example/sub/"); err != nil || u != "https://grafana.northwind.example/sub" {
		t.Fatalf("clean: %q %v", u, err)
	}
	for _, host := range []string{"127.0.0.1", "169.254.169.254", "::1", "0.0.0.0", "224.0.0.1"} {
		_, err := resolve(context.Background(), host)
		if _, ok := err.(refusal); !ok {
			t.Fatalf("%s: %v", host, err)
		}
	}
	// a real listener on loopback is refused before any byte is sent
	f := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("called") }))
	defer f.Close()
	if _, _, err := call("GET", strings.Replace(f.URL, "http://", "https://", 1), "/", nil, nil); err == nil || !strings.Contains(err.Error(), "never calls") {
		t.Fatalf("loopback: %v", err)
	}
	// no redirect is followed
	testAllowLocal = true
	defer func() { testAllowLocal = false }()
	r := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			t.Error("redirect followed")
		}
		http.Redirect(w, req, "/elsewhere", http.StatusFound)
	}))
	defer r.Close()
	if st, _, err := call("GET", r.URL, "/", nil, nil); err != nil || st != 302 {
		t.Fatalf("redirect: %d %v", st, err)
	}
	o := Options{}
	if c := o.RunCheck(&Entry{Type: "grafana", Name: "x", URL: "https://169.254.169.254", Purpose: "agent", CredRef: "env:NOPE"}); c.Status != "failed" {
		t.Fatalf("no credential: %+v", c)
	}
}

// --- the root job ---------------------------------------------------------------------------------------------------

type env struct {
	t   *testing.T
	o   Options
	tmp string
	n   int
	log bytes.Buffer
}

func setup(t *testing.T) *env {
	d := t.TempDir()
	for _, p := range []string{"etc/kube", "etc/secrets", "in", "out", "auth"} {
		os.MkdirAll(filepath.Join(d, p), 0o755)
	}
	db := filepath.Join(d, "auth", "auth.db")
	c, _ := sql.Open("sqlite", db)
	c.Exec("CREATE TABLE users (username TEXT PRIMARY KEY, role TEXT, disabled INTEGER DEFAULT 0)")
	c.Exec("INSERT INTO users VALUES ('admin','admin',0),('viewer','viewer',0),('gone','admin',1)")
	c.Close()
	g, _ := user.LookupGroupId(fmt.Sprint(os.Getgid()))
	e := &env{t: t, tmp: d}
	e.o = Options{ReqDir: filepath.Join(d, "in"), OutDir: filepath.Join(d, "out"), AuthDB: db, Dir: filepath.Join(d, "etc", "sources"),
		AgentGroup: g.Name, AppUID: 65534, Legacy: Legacy{EtcDir: filepath.Join(d, "etc"), SecretsDir: filepath.Join(d, "etc", "secrets"),
			EnvFile: filepath.Join(d, "etc", "env"), KubeDir: filepath.Join(d, "etc", "kube")}}
	e.o.Log = &e.log
	return e
}

func (e *env) request(usr string, req M) string {
	e.n++
	rid := fmt.Sprintf("%016x", e.n)
	if _, ok := req["user"]; !ok {
		req["user"] = usr
	}
	b, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(e.o.ReqDir, usr+"-"+rid+".json"), b, 0o600)
	return rid
}

func (e *env) apply() {
	e.t.Helper()
	if err := Apply(e.o); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) status() Status {
	var st Status
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "sources-status.json"))
	json.Unmarshal(b, &st)
	return st
}

func (e *env) result(rid string) string { s, _ := e.status().Results[rid]["result"].(string); return s }

// everything is every byte root wrote that is not a credential, plus the log.
func (e *env) everything() string {
	var b strings.Builder
	filepath.Walk(e.tmp, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		n := fi.Name()
		if strings.HasSuffix(n, ".cred") || strings.Contains(n, ".cred.") || strings.HasPrefix(p, filepath.Join(e.tmp, "in")) ||
			strings.HasPrefix(p, filepath.Join(e.tmp, "etc", "secrets")) || n == "env" || n == "auth.db" || strings.HasSuffix(n, ".token") {
			return nil
		}
		x, _ := os.ReadFile(p)
		b.Write(x)
		return nil
	})
	return b.String() + e.log.String()
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestAddEditTestRemove(t *testing.T) {
	e := setup(t)
	f := serve(t, grafanaViewer)
	save := func(m M) string {
		r := M{"kind": "save", "type": "grafana", "name": "east", "env": "develop", "url": f.url, "purpose": "agent", "credential": cred, "mode": "edit"}
		for k, v := range m {
			r[k] = v
		}
		return e.request("admin", r)
	}
	rid := save(M{"mode": "add"})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved grafana/east: readonly (read-only: role Viewer") {
		t.Fatal(r)
	}
	if ents, _ := os.ReadDir(e.o.ReqDir); len(ents) != 0 {
		t.Fatal("request not removed")
	}
	p := e.o.credPath("grafana", "east")
	if mode(t, p) != 0o640 || read(t, p) != cred || mode(t, e.o.Dir) != 0o750 || mode(t, filepath.Join(e.o.Dir, "registry.json")) != 0o644 ||
		mode(t, filepath.Join(e.o.Dir, "backup")) != 0o700 || mode(t, filepath.Join(e.o.OutDir, "sources-status.json")) != 0o644 {
		t.Fatal("modes")
	}
	s := e.status().Sources["grafana/east"]
	if s.Status != "readonly" || !s.Enabled || s.CredLast4 != "zzzz" || s.Managed != "registry" || s.CreatedBy != "admin" {
		t.Fatalf("entry %+v", s)
	}
	// duplicate name refused; another type may reuse it
	rid = save(M{"mode": "add"})
	e.apply()
	if r := e.result(rid); !strings.Contains(r, "refused: a Grafana source named east exists") {
		t.Fatal(r)
	}
	// edit to an Editor login without a new credential: kept, now refused and 0600
	f.set(grafanaEditor())
	rid = save(M{"credential": ""})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved grafana/east: refused") || mode(t, p) != 0o600 || read(t, p) != cred {
		t.Fatal(r)
	}
	// the override on develop enables it again; release and shared refuse it before anything changes
	save(M{"override": true, "credential": other})
	e.apply()
	s = e.status().Sources["grafana/east"]
	if s.Status != "refused" || !s.Enabled || !s.Override || mode(t, p) != 0o640 {
		t.Fatalf("override %+v", s)
	}
	if bk, _ := os.ReadDir(filepath.Join(e.o.Dir, "backup")); len(bk) != 1 || mode(t, filepath.Join(e.o.Dir, "backup", bk[0].Name())) != 0o600 {
		t.Fatalf("backup %v", bk)
	}
	for _, env := range []string{"release", "shared"} {
		rid = save(M{"env": env, "override": true})
		e.apply()
		if r := e.result(rid); !strings.Contains(r, "never take the override") {
			t.Fatal(r)
		}
	}
	if read(t, p) != other {
		t.Fatal("a refused save changed the credential")
	}
	f.set(grafanaViewer)
	rid = e.request("admin", M{"kind": "test", "type": "grafana", "name": "east"})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "tested grafana/east: readonly") {
		t.Fatal(r)
	}
	rid = e.request("admin", M{"kind": "check-all"})
	e.apply()
	if r := e.result(rid); r != "checked 1 sources" {
		t.Fatal(r)
	}
	rid = e.request("admin", M{"kind": "remove", "type": "grafana", "name": "east"})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "removed grafana/east; backup grafana-east.cred.") {
		t.Fatal(r)
	}
	if _, err := os.Stat(p); err == nil || e.status().Sources["grafana/east"] != nil {
		t.Fatal("not removed")
	}
	all := e.everything()
	if strings.Contains(all, cred) || strings.Contains(all, other) {
		t.Fatal("a credential reached the status, registry, audit log or log")
	}
	if !strings.Contains(read(t, filepath.Join(e.o.OutDir, "audit.log")), "admin: data source grafana/east") {
		t.Fatal("audit")
	}
	// a fake that echoes the credential: masked in the status anyway
	f.set(map[string]route{"GET /api/user": {200, M{"login": "glpat-" + strings.Repeat("e", 20), "orgId": 1}}})
	save(M{"mode": "add", "credential": cred})
	e.apply()
	if b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "sources-status.json")); strings.Contains(string(b), "glpat-eeee") {
		t.Fatal("token-like identity not masked")
	}
}

func TestRefusals(t *testing.T) {
	e := setup(t)
	f := serve(t, gitlabAPI)
	base := func(m M) M {
		r := M{"kind": "save", "mode": "add", "type": "gitlab", "name": "t", "env": "develop", "url": f.url, "purpose": "agent", "credential": cred}
		for k, v := range m {
			r[k] = v
		}
		return r
	}
	cases := []struct {
		req  M
		want string
	}{
		{base(M{"name": "Bad!"}), "the name must be"},
		{base(M{"env": "prod"}), "choose develop"},
		{base(M{"env": "release", "purpose": "tracker"}), "production is never written"},
		{base(M{"type": "grafana", "purpose": "tracker"}), "cannot have purpose"},
		{base(M{"credential": ""}), "paste the token"},
		{base(M{"credential": "a\nb"}), "line breaks"},
		{base(M{"url": "ftp://x"}), "https://"},
		{base(M{"type": "kubernetes"}), "Settings → Kubernetes"},
		{base(M{"mode": "edit", "name": "none"}), "there is no gitlab/none"},
		{base(M{"mode": "replace"}), "unknown save mode"},
		{base(M{"username": "elastic"}), "the username"},
		{M{"kind": "test", "type": "gitlab", "name": "none"}, "there is no gitlab/none"},
		{M{"kind": "wipe", "type": "gitlab", "name": "t"}, "unknown request kind"},
		{M{"kind": "save", "mode": "add", "type": "nope", "name": "t"}, "unknown data source type"},
	}
	var rids []string
	for _, c := range cases {
		rids = append(rids, e.request("admin", c.req))
	}
	viewer := e.request("viewer", M{"kind": "check-all"})
	gone := e.request("gone", M{"kind": "check-all"})
	e.apply()
	for i, c := range cases {
		if r := e.result(rids[i]); !strings.HasPrefix(r, "refused: ") || !strings.Contains(r, c.want) {
			t.Fatalf("%v: %q, want %q", c.req, r, c.want)
		}
	}
	for _, rid := range []string{viewer, gone} {
		if r := e.result(rid); !strings.Contains(r, "only an enabled admin") {
			t.Fatal(r)
		}
	}
	if len(e.status().Sources) != 0 || strings.Contains(e.everything(), cred) {
		t.Fatal("refused requests left something")
	}
}

func TestRequestFileChecks(t *testing.T) {
	e := setup(t)
	rid := e.request("admin", M{"kind": "check-all"})
	os.Rename(filepath.Join(e.o.ReqDir, "admin-"+rid+".json"), filepath.Join(e.o.ReqDir, "viewer-"+rid+".json"))
	os.WriteFile(filepath.Join(e.o.ReqDir, "junk.txt"), []byte("x"), 0o600)
	link := e.request("admin", M{"kind": "check-all"})
	lp := filepath.Join(e.o.ReqDir, "admin-"+link+".json")
	os.Rename(lp, filepath.Join(e.tmp, "target.json"))
	os.Symlink(filepath.Join(e.tmp, "target.json"), lp)
	other := e.request("admin", M{"kind": "check-all"})
	e.o.AppUID = os.Getuid() + 4242 // the file is not the app's: refused unless written by root itself (euid)
	e.apply()
	if r := e.result(rid); !strings.Contains(r, "does not match its file name") {
		t.Fatal(r)
	}
	if r := e.result(link); !strings.Contains(r, "cannot open request") {
		t.Fatal(r)
	}
	if r := e.result(other); r != "checked 0 sources" { // written by the caller's own uid (tests run as the job's user)
		t.Fatal(r)
	}
	if ents, _ := os.ReadDir(e.o.ReqDir); len(ents) != 0 {
		t.Fatalf("left: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(e.tmp, "target.json")); err != nil {
		t.Fatal("the symlink target was touched")
	}
}

func TestTrackerNeverEnabled(t *testing.T) {
	e := setup(t)
	f := serve(t, gitlabAPI)
	rid := e.request("admin", M{"kind": "save", "mode": "add", "type": "gitlab", "name": "board", "env": "shared", "url": f.url, "purpose": "tracker", "credential": cred})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved gitlab/board: ok") || e.status().Sources["gitlab/board"].Enabled ||
		mode(t, e.o.credPath("gitlab", "board")) != 0o600 {
		t.Fatal(r)
	}
	// a read-only agent login on shared is enabled; shared never takes the override
	f.set(gitlabRO)
	rid = e.request("admin", M{"kind": "save", "mode": "add", "type": "gitlab", "name": "ro", "env": "shared", "url": f.url, "purpose": "agent", "credential": cred})
	e.apply()
	if !e.status().Sources["gitlab/ro"].Enabled {
		t.Fatal(e.result(rid))
	}
	for _, c := range []struct {
		env, status string
		ov, want    bool
	}{{"develop", "refused", true, true}, {"pre-release", "refused", true, true}, {"release", "refused", true, false},
		{"shared", "refused", true, false}, {"develop", "refused", false, false}, {"develop", "failed", true, false}} {
		if en, _ := MayEnable(&Entry{Purpose: "agent", Env: c.env, Status: c.status, OverrideRequested: c.ov}); en != c.want {
			t.Fatalf("%+v", c)
		}
	}
	if en, _ := MayEnable(&Entry{Purpose: "docs", Status: "readonly"}); en {
		t.Fatal("docs enabled")
	}
}

func (e *env) legacySetup(gl, gf, ol string) {
	etc := e.o.Legacy.EtcDir
	os.WriteFile(filepath.Join(etc, "env"), []byte("GITLAB_URL="+gl+"\nGITLAB_RO_TOKEN="+cred+"\nGITLAB_TOKEN="+other+"\n"+
		"GRAFANA_EAST_URL="+gf+"\nGRAFANA_WEST_URL="+gf+"\n"), 0o600)
	os.WriteFile(filepath.Join(etc, "secrets", "grafana-east-token"), []byte(cred+"\n"), 0o600)
	os.WriteFile(filepath.Join(etc, "gitlab-issues.json"), []byte(`{"url": "`+gl+`/northwind/ops", "mode": "project"}`), 0o644)
	os.WriteFile(filepath.Join(etc, "gitlab-issues.token"), []byte(other+"\n"), 0o600)
	os.WriteFile(filepath.Join(etc, "outline.json"), []byte(`{"url": "`+ol+`"}`), 0o644)
	os.WriteFile(filepath.Join(etc, "outline.token"), []byte(cred), 0o600)
	os.WriteFile(filepath.Join(etc, "kube", "clusters.json"), []byte(`{"clusters": {
  "dev": {"env": "develop", "server_host": "192.0.2.10:6443", "verdict": "read-only", "enabled": true, "user": "argus-ro",
          "set_by": "admin", "set_at": 1700000000, "test": {"result": "ok", "at": 1700000000}},
  "rw": {"env": "pre-release", "verdict": "writes", "enabled": false}}}`), 0o644)
}

func snapshot(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && !strings.Contains(p, string(filepath.Separator)+"sources"+string(filepath.Separator)) {
			b, _ := os.ReadFile(p)
			out[p] = string(b) + fi.Mode().String()
		}
		return nil
	})
	return out
}

func keys(m map[string]*Entry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMigrationIdempotentAndReadOnly(t *testing.T) {
	e := setup(t)
	gl := serve(t, gitlabRO)
	gf := serve(t, grafanaViewer)
	ol := serve(t, map[string]route{"POST /api/auth.info": {200, M{"data": M{"user": M{"name": "Argus", "role": "member"}}}}})
	e.legacySetup(gl.url, gf.url, ol.url)
	e.o.Legacy.GrafanaURLs = map[string]string{"GRAFANA_NORTH_URL": gf.url} // no token anywhere: not mirrored
	before := snapshot(t, e.o.Legacy.EtcDir)
	if err := CheckAll(e.o); err != nil {
		t.Fatal(err)
	}
	s := e.status().Sources
	if got := strings.Join(keys(s), " "); got != "gitlab/main gitlab/tracker grafana/east kubernetes/dev kubernetes/rw outline/wiki" {
		t.Fatal(got)
	}
	m := s["gitlab/main"]
	if m.Status != "readonly" || m.Purpose != "agent" || m.Env != "shared" || m.CredWhere != "GITLAB_RO_TOKEN in the legacy env file (ARGUS_SOURCES_LEGACY_ENV)" {
		t.Fatalf("gitlab/main %+v", m)
	}
	if tr := s["gitlab/tracker"]; tr.Purpose != "tracker" || tr.URL != gl.url || tr.Status != "failed" {
		t.Fatalf("tracker %+v", tr)
	}
	if g := s["grafana/east"]; g.Status != "readonly" || g.Env != "release" || g.CredRef != "secret:grafana-east-token" {
		t.Fatalf("grafana %+v", g)
	}
	if s["outline/wiki"].Status != "ok" || s["kubernetes/dev"].Status != "readonly" || s["kubernetes/rw"].Status != "refused" ||
		s["kubernetes/dev"].URL != "https://192.0.2.10:6443" || s["kubernetes/dev"].UsedBy != "argus tools kube dev" {
		t.Fatalf("outline/kube %+v %+v", s["outline/wiki"], s["kubernetes/dev"])
	}
	for k, x := range s {
		if x.Managed != "legacy" {
			t.Fatalf("%s not legacy", k)
		}
	}
	seen := strings.Join(gl.seen, " ")
	if !strings.Contains(seen, cred) || !strings.Contains(seen, other) {
		t.Fatal("the right token per entry")
	}
	if after := snapshot(t, e.o.Legacy.EtcDir); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatal("the migration wrote an old location")
	}
	if _, err := os.Stat(filepath.Join(e.o.Dir, "gitlab")); err == nil {
		t.Fatal("a credential was copied")
	}
	// again: nothing new, created_at kept
	reg := e.o.Registry()
	if ch := e.o.Migrate(reg); len(ch) != 0 {
		t.Fatal(ch)
	}
	created := map[string]int64{}
	for k, x := range s {
		created[k] = x.CreatedAt
	}
	CheckAll(e.o)
	s2 := e.status().Sources
	if fmt.Sprint(keys(s2)) != fmt.Sprint(keys(s)) {
		t.Fatal("second run changed the set")
	}
	for k, x := range s2 {
		if x.CreatedAt != created[k] {
			t.Fatalf("%s created_at changed", k)
		}
	}
	// legacy entries are changed and removed where they live, never here
	for _, c := range []struct {
		req  M
		want string
	}{
		{M{"kind": "remove", "type": "gitlab", "name": "main"}, "remove it there"},
		{M{"kind": "save", "mode": "edit", "type": "grafana", "name": "east", "env": "release", "url": gf.url, "purpose": "agent", "credential": other}, "change it there"},
		{M{"kind": "test", "type": "kubernetes", "name": "dev"}, "Settings → Kubernetes"},
	} {
		rid := e.request("admin", c.req)
		e.apply()
		if r := e.result(rid); !strings.Contains(r, c.want) {
			t.Fatal(r)
		}
	}
	// an admin's own entry with a legacy name: the legacy one takes a suffix
	os.Remove(filepath.Join(e.o.Legacy.EtcDir, "outline.token"))
	rid := e.request("admin", M{"kind": "save", "mode": "add", "type": "outline", "name": "wiki", "env": "shared", "url": ol.url, "purpose": "docs", "credential": other})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved outline/wiki: ok") {
		t.Fatal(r)
	}
	os.WriteFile(filepath.Join(e.o.Legacy.EtcDir, "outline.token"), []byte(cred), 0o600)
	e.apply()
	s3 := e.status().Sources
	if s3["outline/wiki"].Managed != "registry" || s3["outline/wiki-env"] == nil || s3["outline/wiki-env"].Managed != "legacy" {
		t.Fatal(keys(s3))
	}
	// old configuration gone: its entry goes too
	os.Remove(filepath.Join(e.o.Legacy.EtcDir, "gitlab-issues.token"))
	os.WriteFile(filepath.Join(e.o.Legacy.KubeDir, "clusters.json"), []byte(`{"clusters": {}}`), 0o644)
	e.apply()
	s4 := e.status().Sources
	if s4["gitlab/tracker"] != nil || s4["kubernetes/dev"] != nil {
		t.Fatal(keys(s4))
	}
	if all := e.everything(); strings.Contains(all, cred) || strings.Contains(all, other) {
		t.Fatal("a credential leaked")
	}
}

func TestRecordAndList(t *testing.T) {
	e := setup(t)
	e.o.Publish(nil, map[string]*Entry{
		"grafana/east": {Type: "grafana", Name: "east", Env: "release", Purpose: "agent", Status: "readonly", Enabled: true, LastCheck: 1,
			UsedBy: "monitoring reads of site east", Managed: "legacy"},
		"gitlab/board": {Type: "gitlab", Name: "board", Env: "shared", Purpose: "tracker", Status: "ok"}})
	status := filepath.Join(e.o.OutDir, "sources-status.json")
	db := filepath.Join(e.tmp, "ops.db")
	for i := 0; i < 2; i++ { // idempotent: replaces, never duplicates
		if n, err := Record(status, db); err != nil || n != 2 {
			t.Fatal(n, err)
		}
	}
	c, _ := sql.Open("sqlite", db)
	var got []string
	q, _ := c.Query("SELECT key, enabled, managed FROM data_sources ORDER BY key")
	for q.Next() {
		var k, m string
		var en int
		q.Scan(&k, &en, &m)
		got = append(got, fmt.Sprintf("%s %d %s", k, en, m))
	}
	q.Close()
	c.Close()
	if strings.Join(got, ",") != "gitlab/board 0 registry,grafana/east 1 legacy" {
		t.Fatal(got)
	}
	var out bytes.Buffer
	List("", db, status, &out)
	if !strings.Contains(out.String(), "grafana\teast\trelease\tagent\tread-only verified\tyes") ||
		!strings.Contains(out.String(), "gitlab\tboard\tshared\ttracker\tok\tno") {
		t.Fatal(out.String())
	}
	out.Reset()
	List("sentry", db, status, &out)
	if !strings.Contains(out.String(), "no data sources of type sentry") {
		t.Fatal(out.String())
	}
	out.Reset()
	List("gitlab", filepath.Join(e.tmp, "none.db"), status, &out) // no DB yet: the status file answers
	if !strings.Contains(out.String(), "gitlab\tboard") {
		t.Fatal(out.String())
	}
	if _, err := Record(status, "relative.db"); err == nil {
		t.Fatal("relative path accepted")
	}
}
