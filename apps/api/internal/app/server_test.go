package app

// End-to-end tests of server.go (the gate, CSRF, cookies, headers, theme switch, settings tabs, the server
// pages) mirroring tests/test_theme.py and the security parts of tests/test_sso.py. Helpers are prefixed srv.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

type srvEnv struct {
	t   *testing.T
	ts  *httptest.Server
	dir string
}

type srvResp struct {
	status  int
	loc     string
	cookies []string
	body    string
	header  http.Header
}

func srvSetup(t *testing.T) *srvEnv {
	t.Helper()
	d := t.TempDir()
	for _, s := range []string{"in", "out", "rca"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	old := []string{DB, SET_IN, SET_OUT, REPORTS, CRED, ALERTS, auth.DB}
	DB, SET_IN, SET_OUT, REPORTS = filepath.Join(d, "ops.db"), filepath.Join(d, "in"), filepath.Join(d, "out"), filepath.Join(d, "rca")
	CRED, ALERTS = filepath.Join(d, "cred.json"), filepath.Join(d, "alerts.json")
	auth.DB = filepath.Join(d, "auth.db")
	auth.Reset()
	if err := auth.Init("", nil); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"admin", "admin"}, {"view.er", "viewer"}} {
		h, _ := auth.HashPW("Password-12345")
		auth.Con().Exec("INSERT INTO users VALUES (?,?,?,0,0,0)", u[0], h, u[1])
	}
	oidc.ConfigureWith("", "", "", "", "")
	key := filepath.Join(d, "totp.key")
	os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0o400)
	oldKey := auth.KeyFile
	auth.KeyFile = key
	auth.ResetKeys()
	t.Cleanup(func() { auth.KeyFile = oldKey; auth.ResetKeys() })
	e := &srvEnv{t: t, ts: httptest.NewServer(Handler{}), dir: d}
	Out = io.Discard
	t.Cleanup(func() {
		e.ts.Close()
		DB, SET_IN, SET_OUT, REPORTS, CRED, ALERTS, auth.DB = old[0], old[1], old[2], old[3], old[4], old[5], old[6]
		auth.Reset()
		Out = os.Stdout
	})
	return e
}

func (e *srvEnv) req(method, path string, cookies map[string]string, form url.Values, hdr map[string]string) srvResp {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, _ := http.NewRequest(method, e.ts.URL+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	var parts []string
	for k, v := range cookies {
		parts = append(parts, k+"="+v)
	}
	if len(parts) > 0 {
		r.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
		} else {
			r.Header.Set(k, v)
		}
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return srvResp{resp.StatusCode, srvLoc(resp, path, b), resp.Header.Values("Set-Cookie"), string(b), resp.Header}
}

// srvLoc is where an answer sends the browser: the Location header, or the page named by an API write's JSON.
func srvLoc(resp *http.Response, path string, b []byte) string {
	loc := resp.Header.Get("Location")
	var a struct{ Redirect, Next string }
	if loc == "" && strings.HasPrefix(path, "/api/") && json.Unmarshal(b, &a) == nil {
		loc = py.OrS(a.Redirect, a.Next)
	}
	return loc
}

// login opens a session as after a full sign-in with 2FA (the user is enrolled once, the code verified now), so
// every action passes the MFA middleware; loginNoMFA is a session after the password only.
func (e *srvEnv) login(user string) (string, string) {
	e.t.Helper()
	tok := auth.Login(user, "Password-12345", "127.0.0.1")
	if tok == "" {
		e.t.Fatal("login", user)
	}
	if !auth.Enabled(user) {
		srvEnrol(e.t, tok)
	}
	auth.Logout(tok)
	tok, _ = auth.NewSession(user, auth.Opts{MFAAt: NowInt(), MFAVia: "totp", IP: "127.0.0.1"})
	return tok, auth.GetSession(tok).CSRF
}

func (e *srvEnv) loginNoMFA(user string) (string, string) {
	e.t.Helper()
	tok := auth.Login(user, "Password-12345", "127.0.0.1")
	if tok == "" {
		e.t.Fatal("login", user)
	}
	return tok, auth.GetSession(tok).CSRF
}

// srvEnrol turns 2FA on for the session's user and returns the secret.
func srvEnrol(t *testing.T, tok string) []byte {
	t.Helper()
	s := auth.GetSession(tok)
	sec, err := auth.BeginEnrol(s, "", "127.0.0.1")
	if err != nil {
		t.Fatal("enrol", err)
	}
	if _, err := auth.ConfirmEnrol(s, auth.TOTPAt(sec, auth.Step(int64(auth.Now()))), "127.0.0.1"); err != nil {
		t.Fatal("confirm", err)
	}
	return sec
}

// srvNeedsGroup skips a test whose page calls into a view group that is still a stub (stub_<group>.go).
func srvNeedsGroup(t *testing.T, group string) {
	if _, err := os.Stat("stub_" + group + ".go"); err == nil {
		t.Skip("needs the " + group + " group (still a stub)")
	}
}

func srvCookie(set []string, name string) (string, string, bool) {
	for _, sc := range set {
		k, rest, _ := strings.Cut(sc, "=")
		if k == name {
			v, _, _ := strings.Cut(rest, ";")
			return v, sc, true
		}
	}
	return "", "", false
}

func TestSrvLoginFlowAndCookieFlags(t *testing.T) {
	e := srvSetup(t)
	r := e.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/runs"}, "username": {"admin"}, "password": {"Password-12345"}}, nil)
	v, raw, ok := srvCookie(r.cookies, "opsr_session")
	if r.status != 200 || r.loc != "/runs" || !ok || len(v) != 43 {
		t.Fatalf("login: %d %s %v", r.status, r.loc, r.cookies)
	}
	for _, f := range []string{"Path=/", "HttpOnly", "Secure", "SameSite=Lax", "Max-Age=43200"} {
		if !strings.Contains(raw, f) {
			t.Fatal("cookie flag", f)
		}
	}
	// wrong login token: the form expired; wrong password: one message for both cases
	r = e.req("POST", "/api/v1/auth/login", nil, url.Values{"csrf": {strings.Repeat("0", 64)}, "username": {"admin"}, "password": {"Password-12345"}}, nil)
	if _, _, ok := srvCookie(r.cookies, "opsr_session"); ok || r.status != 403 || !strings.Contains(r.body, "csrf token") {
		t.Fatal("login csrf")
	}
	r = e.v1Form("/api/v1/auth/login", nil, url.Values{"username": {"admin"}, "password": {"nope"}}, nil)
	if !strings.Contains(r.body, "Wrong username or password, or too many attempts.") {
		t.Fatal("login failure message")
	}
	// next is local only
	for _, bad := range []string{"//evil.example/x", "https://evil.example", `/\evil.example`, "/\t/evil", "evil", "/auth/login", "/é"} {
		if SafeNext(bad) != "/" {
			t.Errorf("SafeNext(%q)", bad)
		}
	}
	if SafeNext("/incidents/view?id=3&x=1") != "/incidents/view?id=3&x=1" {
		t.Fatal("SafeNext local")
	}
	// logout clears the cookie and the session
	sess, csrf := e.login("admin")
	r = e.req("POST", "/api/v1/auth/logout", map[string]string{"opsr_session": sess}, url.Values{"csrf": {csrf}}, nil)
	if _, raw, _ := srvCookie(r.cookies, "opsr_session"); r.status != 200 || r.loc != "/login" || !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("logout %d %s", r.status, r.loc)
	}
	if auth.GetSession(sess) != nil {
		t.Fatal("session kept after logout")
	}
}

func TestSrvCSRFOriginAndSize(t *testing.T) {
	e := srvSetup(t)
	sess, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": sess}
	for _, bad := range []string{"", "0", strings.Repeat("0", 32), csrf + "x"} {
		r := e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {bad}, "tasks": {"all"}}, nil)
		if r.status != 403 || !strings.Contains(r.body, "csrf token") {
			t.Fatalf("csrf %q: %d", bad, r.status)
		}
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "requests")); err == nil {
		t.Fatal("request written without a valid token")
	}
	r := e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {"all"}, "back": {"/certs"}},
		map[string]string{"Origin": "https://evil.example"})
	if r.status != 403 || !strings.Contains(r.body, "cross-site request") {
		t.Fatal("cross-site POST")
	}
	r = e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {strings.Repeat("x", 9000)}}, nil)
	if r.status != 413 {
		t.Fatal("size limit", r.status)
	}
	r = e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {"cert-check,bogus"}, "back": {"//evil"}}, nil)
	if r.status != 200 || r.loc != "/" {
		t.Fatal("scan redirect", r.status, r.loc)
	}
	data, err := os.ReadFile(filepath.Join(SET_IN, "requests", "cert-check.req"))
	if err != nil || !regexp.MustCompile(`^\{"by": "admin", "at": \d+\}$`).Match(data) {
		t.Fatalf("request file %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", "bogus.req")); err == nil {
		t.Fatal("unknown task queued")
	}
}

// --- theme (tests/test_theme.py) ---------------------------------------------------------------------------

func TestSrvThemePost(t *testing.T) {
	e := srvSetup(t)
	r := e.v1Form("/api/v1/theme", nil, url.Values{"theme": {"dark"}, "next": {"/login?next=%2Fcerts"}}, nil)
	_, raw, ok := srvCookie(r.cookies, "opsr_theme")
	if r.status != 200 || !ok || !strings.HasPrefix(raw, "opsr_theme=dark;") {
		t.Fatalf("set: %d %s %v", r.status, r.loc, r.cookies)
	}
	for _, a := range []string{"Path=/", "Max-Age=31536000", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(raw, a) {
			t.Fatal(a)
		}
	}
	r = e.v1Form("/api/v1/theme", map[string]string{"opsr_theme": "dark"}, url.Values{"theme": {"auto"}, "next": {"/login"}}, nil)
	if _, raw, _ := srvCookie(r.cookies, "opsr_theme"); !strings.HasPrefix(raw, "opsr_theme=;") || !strings.Contains(raw, "Max-Age=0") {
		t.Fatal("clear")
	}
	for _, bad := range []string{"neon", "", "dark; Domain=evil", "LIGHT"} {
		r := e.v1Form("/api/v1/theme", nil, url.Values{"theme": {bad}, "next": {"/login"}}, nil)
		if _, _, ok := srvCookie(r.cookies, "opsr_theme"); r.status != 400 || ok {
			t.Fatal("bad value", bad)
		}
	}
	sess, _ := e.login("admin")
	for _, c := range []struct {
		ck  map[string]string
		tok string
	}{{nil, ""}, {nil, strings.Repeat("0", 64)}, {map[string]string{"opsr_session": sess}, strings.Repeat("0", 64)}} {
		r := e.req("POST", "/api/v1/theme", c.ck, url.Values{"csrf": {c.tok}, "theme": {"dark"}, "next": {"/"}}, nil)
		if _, _, ok := srvCookie(r.cookies, "opsr_theme"); r.status != 403 || ok {
			t.Fatal("theme csrf")
		}
	}
	_, other := e.login("admin")
	if r := e.req("POST", "/api/v1/theme", nil, url.Values{"csrf": {other}, "theme": {"dark"}}, nil); r.status != 403 {
		t.Fatal("another session's token signed out")
	}
}

func TestSrvShowMoreFoldTableMore(t *testing.T) {

	xs := make([]int, 40)
	if len(Capped(xs, py.QS{})) != 15 || len(Capped(xs, py.NewQS("all", "1"))) != 40 {
		t.Fatal("capped")
	}
	if Fold("3 sets OK", "<p>x</p>", -1, false, "") != "<details><summary>3 sets OK</summary><p>x</p></details>" ||
		strings.Contains(Fold("<script>", "", -1, false, ""), "<script>") {
		t.Fatal("fold")
	}
	rows := func(n int) []string {
		var r []string
		for i := 0; i < n; i++ {
			r = append(r, "<tr><td>x</td></tr>")
		}
		return r
	}
	if !strings.Contains(TableMore("<thead></thead>", rows(8), 5, "alerts", "", ""), `<summary>More alerts <span class="count">3</span></summary>`) {
		t.Fatal("table_more")
	}

}

func TestSrvUsersAdmin(t *testing.T) {
	e := srvSetup(t)
	sess, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": sess}
	post := func(f url.Values) string {
		f.Set("csrf", csrf)
		return e.req("POST", "/api/v1/admin/users/act", ck, f, nil).body
	}
	srvNeedsGroup(t, "board") // the Matrix column of the users page
	if b := post(url.Values{"action": {"add"}, "user": {"<script>"}}); !strings.Contains(b, "Invalid username or role.") {
		t.Fatal("bad name")
	}
	if b := post(url.Values{"action": {"disable"}, "user": {"admin"}}); !strings.Contains(b, "Not allowed.") {
		t.Fatal("self")
	}
}

func TestSrvEscapingOfServerPages(t *testing.T) {
	x := `<script>alert(1)</script>"'&`
	os.WriteFile(ALERTS, []byte(`{"fetched_at": 1, "dcs": [{"dc": "dc-east", "ok": true, "alerts": [{"alertname": `+
		py.Dumps(x, -1)+`, "state": "firing", "severity": "critical", "summary": `+py.Dumps(x, -1)+`, "instance": `+
		py.Dumps(x, -1)+`, "activeAt": "2026-10-21T10:00:00Z", "value": "1e9"}]}]}`), 0o644)

	md := Md("# T <b>\n\n| a | v |\n|---|---:|\n| `x|y` | **proven** <i> |\n\n- item https://x.example/a?b=1&c=<2>\n```\n<raw>\n```")
	for _, bad := range []string{"<b>", "<i>", "<raw>", "<2>"} {
		if strings.Contains(md, bad) {
			t.Fatalf("md leaked %s: %s", bad, md)
		}
	}
	if !strings.Contains(md, `<span class="badge ok">proven</span>`) || !strings.Contains(md, `<td class="num">`) ||
		!strings.Contains(md, `<code>x|y</code>`) {
		t.Fatal("md table", md)
	}
}

func TestSrvSecurityHeadersEverywhere(t *testing.T) {
	e := srvSetup(t)
	for _, p := range []string{"/login", "/healthz", "/", "/nope", "/api/v1/nope", "/static/favicon.svg", "/auth/login"} {
		r := e.req("GET", p, nil, nil, nil)
		if r.header.Get("Content-Security-Policy") != CSP || r.header.Get("X-Content-Type-Options") != "nosniff" ||
			r.header.Get("Referrer-Policy") != "same-origin" || r.header.Get("Cache-Control") == "" {
			t.Errorf("%s: headers %v", p, r.header)
		}
	}
	if !strings.Contains(CSP, "default-src 'none'") || strings.Contains(CSP, "script") || strings.Contains(CSP, "unsafe") {
		t.Fatal("CSP must forbid scripts")
	}
}

func TestSrvStaticWhitelist(t *testing.T) {
	e := srvSetup(t)
	for _, p := range []string{"/static/../server.py", "/static/style.css", "/static/favicon.svg/", "/static/x.css", "/static/fonts/../favicon.svg", "/static/%66avicon.svg"} {
		r := e.req("GET", p, nil, nil, nil)
		if r.status == 200 {
			t.Errorf("%s served", p)
		}
	}
	r := e.req("GET", "/static/favicon.svg", nil, nil, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(r.header.Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("favicon %d %v", r.status, r.header)
	}
}
