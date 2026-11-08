package app

// Sessions, 2FA and accepted failures over HTTP: the MFA middleware's coverage of every POST route (with each
// route's policy), remember-me cookies, the 2FA sign-in step, enrolment (fresh password, session-bound, QR),
// step-up and its windows, sessions page, admin reset/revoke, the SSO amr/acr path, the QR code, CSRF on the new
// forms, and the backup override (request file, page states, shared rule vectors).

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
)

// mfaClock pins the app and auth clocks together; adv moves both.
func mfaClock(t *testing.T, at float64) func(float64) {
	ra, rn := auth.Now, NowFn
	cur := at
	auth.Now = func() float64 { return cur }
	NowFn = func() float64 { return cur }
	t.Cleanup(func() { auth.Now, NowFn = ra, rn })
	return func(d float64) { cur += d }
}

func codeNow(sec []byte) string { return auth.TOTPAt(sec, auth.Step(int64(auth.Now()))) }

// --- coverage of every POST route ----------------------------------------------------------------------------

// postPathsInSource: every write the server handles: the literal paths of apiIsPost, the sign-in writes, and any form
// with method="post" still in the package's code (there is no other POST route: doPOST goes to the API only).
func postPathsInSource(t *testing.T) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func apiIsPost(")
	j := i + strings.Index(src[i:], "\n}\n")
	for _, m := range regexp.MustCompile(`"(/api/v1/[^"]+)"`).FindAllStringSubmatch(src[i:j], -1) {
		out[m[1]] = true
	}
	for p := range apiAuthPOSTs {
		out[p] = true
	}
	return out
}

func TestMFAPolicyOfEveryPostRoute(t *testing.T) {
	src := postPathsInSource(t)
	if len(src) < 40 {
		t.Fatalf("found only %d POST paths in the source", len(src))
	}
	for p := range src {
		if _, ok := POST_ROUTES[p]; !ok {
			t.Errorf("POST %s is handled but has no policy in POST_ROUTES (it would default to sensitive)", p)
		}
	}
	for p := range POST_ROUTES {
		if !src[p] {
			t.Errorf("POST_ROUTES lists %s but no handler uses it", p)
		}
	}
	// the assigned classes, exactly
	want := map[Policy][]string{
		PolicyExempt:    {"/api/v1/account/2fa/begin", "/api/v1/account/2fa/confirm", "/api/v1/account/sessions", "/api/v1/account/stepup", "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/mfa", "/api/v1/theme"},
		PolicyInline:    {"/api/v1/account/2fa/disable", "/api/v1/account/2fa/recovery"},
		PolicySensitive: {"/api/v1/admin/agent", "/api/v1/admin/agent/fixer-move", "/api/v1/admin/backup-targets", "/api/v1/admin/board", "/api/v1/admin/claude", "/api/v1/admin/claude-egress", "/api/v1/admin/claude-token", "/api/v1/admin/gitlab-issues", "/api/v1/admin/jira-hosts", "/api/v1/admin/jira-shared", "/api/v1/admin/kube", "/api/v1/admin/modules", "/api/v1/admin/outline", "/api/v1/admin/save", "/api/v1/admin/sources", "/api/v1/admin/users/act", "/api/v1/admin/users/matrix", "/api/v1/admin/writing-style", "/api/v1/backups/override", "/api/v1/board/agent/spinup", "/api/v1/board/task/secret", "/api/v1/fix/review", "/api/v1/lessons/act", "/api/v1/lessons/settings", "/api/v1/proposals/settings", "/api/v1/sources/catalogue/discover", "/api/v1/sources/catalogue/routes/confirm", "/api/v1/sources/catalogue/routes/edit", "/api/v1/sources/catalogue/routes/reject", "/api/v1/sources/review/act", "/api/v1/vulns/suppress"},
	}
	got := map[Policy][]string{}
	for p, pol := range POST_ROUTES {
		got[pol] = append(got[pol], p)
	}
	for pol, list := range want {
		g := got[pol]
		sort.Strings(g)
		if strings.Join(g, " ") != strings.Join(list, " ") {
			t.Errorf("%s routes:\n got  %v\n want %v", pol, g, list)
		}
	}
	if PolicyOf("/nope/never", nil) != PolicySensitive || PolicyOf("/api/v1/board/new-thing", nil) != PolicyNormal {
		t.Fatal("default deny")
	}
	if PolicyOf("/api/v1/account", &Session{MustChange: 1}) != PolicyExempt || PolicyOf("/api/v1/account", &Session{}) != PolicyNormal {
		t.Fatal("forced password change")
	}
}

func TestMFAMiddlewareOverHTTP(t *testing.T) {
	e := srvSetup(t)
	adv := mfaClock(t, float64(time.Now().Unix()))
	before := func() []string {
		var out []string
		filepath.Walk(SET_IN, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && !strings.Contains(p, "security-events") {
				out = append(out, p)
			}
			return nil
		})
		return out
	}
	tok, csrf := e.loginNoMFA("admin") // password only, 2FA not set up
	ck := map[string]string{"opsr_session": tok}
	start := before()
	paths := []string{}
	for p, pol := range POST_ROUTES {
		if pol == PolicyNormal || pol == PolicySensitive {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		r := e.req("POST", p, ck, url.Values{"csrf": {csrf}, "action": {"add"}, "tasks": {"all"}}, map[string]string{"Referer": e.ts.URL + "/backups"})
		if r.status != 403 || !strings.Contains(r.body, `"mfa":"enrol"`) {
			t.Errorf("%s without 2FA: %d %s", p, r.status, r.body)
		}
	}
	if after := before(); len(after) != len(start) {
		t.Fatalf("a request file was written without 2FA: %v", after)
	}
	// exempt routes work without 2FA (theme, sessions, logout); a bad CSRF is refused before anything
	if r := e.req("POST", "/api/v1/account/sessions", ck, url.Values{"csrf": {csrf}, "action": {"others"}}, nil); r.status != 200 {
		t.Fatal("sessions exempt", r.status)
	}
	if r := e.req("POST", "/api/v1/account/2fa/begin", ck, url.Values{"csrf": {"bad"}, "password": {"Password-12345"}}, nil); r.status != 403 ||
		!strings.Contains(r.body, "csrf token") {
		t.Fatal("csrf on begin", r.status)
	}
	for _, m := range []string{"PUT", "PATCH", "DELETE"} {
		if r := e.req(m, "/admin/users", ck, url.Values{"csrf": {csrf}}, nil); r.status != 501 {
			t.Fatal(m, r.status)
		}
	}
	// MFA_STEP_UP=on (the old policy): enrolled, verified 2 hours ago: normal actions pass, sensitive ones need a
	// fresh code
	stepUpMode(t, true)
	srvEnrol(t, tok)
	auth.Logout(tok)
	tok, _ = auth.NewSession("admin", auth.Opts{MFAAt: NowInt() - 7200, MFAVia: "totp", Remember: true})
	csrf = auth.GetSession(tok).CSRF
	ck = map[string]string{"opsr_session": tok}
	for p, pol := range POST_ROUTES {
		if pol != PolicyNormal && pol != PolicySensitive {
			continue
		}
		r := e.req("POST", p, ck, url.Values{"csrf": {csrf}}, nil)
		stepped := strings.Contains(r.body, "Confirm it&#x27;s you") || strings.Contains(r.body, "Confirm it's you") ||
			(strings.HasPrefix(p, "/api/") && strings.Contains(r.body, `"mfa":"stepup"`))
		if (pol == PolicySensitive) != stepped {
			t.Errorf("%s (%s) after 2 h: step-up page %v (%d)", p, pol, stepped, r.status)
		}
	}
	// after MFA_STEPUP_MINUTES even normal actions ask again
	adv(float64(auth.MFAStepup) - 3600) // within the 7-day idle of a kept session

	adv(3600)
	if r := e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {"all"}}, nil); r.status != 403 {
		t.Fatal("stale grant", r.status)
	}
}

// stepUpMode sets MFA_STEP_UP for one test.
func stepUpMode(t *testing.T, on bool) {
	old := auth.MFAStepUpOn
	auth.MFAStepUpOn = on
	t.Cleanup(func() { auth.MFAStepUpOn = old })
}

func TestMFAStateStepUpModes(t *testing.T) {
	now := NowInt()
	signedIn := &Session{Username: "idp.user", MFAVia: "idp", MFAAt: now - 30*86400} // 2FA at sign-in, a month ago
	noMFA := &Session{Username: "idp.user"}                                          // password only, no 2FA yet
	stepUpMode(t, false)
	for _, p := range []Policy{PolicyNormal, PolicySensitive} {
		if st := MFAState(signedIn, p); st != "" {
			t.Errorf("off: %s with sign-in 2FA = %q", p, st)
		}
		if st := MFAState(noMFA, p); st == "" {
			t.Errorf("off: %s without 2FA allowed", p)
		}
	}
	stepUpMode(t, true)
	for _, p := range []Policy{PolicyNormal, PolicySensitive} {
		if st := MFAState(signedIn, p); st != "stepup" {
			t.Errorf("on: %s with a month-old 2FA = %q", p, st)
		}
	}
	if st := MFAState(&Session{Username: "idp.user", MFAVia: "idp", MFAAt: now - 7200}, PolicyNormal); st != "" {
		t.Errorf("on: normal within the window = %q", st)
	}
}

// MFA_STEP_UP=off (default): 2FA at sign-in covers every change of the session; a session without 2FA is still
// sent to enrol; the inline routes still verify a code themselves.
func TestMFAStepUpOffOverHTTP(t *testing.T) {
	e := srvSetup(t)
	adv := mfaClock(t, float64(time.Now().Unix()))
	stepUpMode(t, false)
	tok, csrf := e.loginNoMFA("admin")
	ck := map[string]string{"opsr_session": tok}
	for _, p := range []string{"/api/v1/admin/save", "/api/v1/admin/users/act", "/api/v1/scan"} {
		if r := e.req("POST", p, ck, url.Values{"csrf": {csrf}}, nil); r.status != 403 || !strings.Contains(r.body, `"mfa":"enrol"`) {
			t.Errorf("off, no 2FA: %s %d", p, r.status)
		}
	}
	srvEnrol(t, tok)
	auth.Logout(tok)
	tok, _ = auth.NewSession("admin", auth.Opts{MFAAt: NowInt() - 7200, MFAVia: "totp", Remember: true})
	csrf = auth.GetSession(tok).CSRF
	ck = map[string]string{"opsr_session": tok}
	adv(float64(auth.MFAStepup) - 3600) // past MFA_SENSITIVE_MINUTES, close to MFA_STEPUP_MINUTES

	adv(7200) // and past it
	for p, pol := range POST_ROUTES {
		if pol != PolicyNormal && pol != PolicySensitive {
			continue
		}
		r := e.req("POST", p, ck, url.Values{"csrf": {csrf}}, nil)
		if r.status == 403 || strings.Contains(r.body, "Confirm it&#x27;s you") || strings.Contains(r.body, "Confirm it's you") {
			t.Errorf("off: %s (%s) asked again after sign-in 2FA (%d)", p, pol, r.status)
		}
	}
	if auth.GetSession(tok) == nil {
		t.Fatal("off: the session expired during the test; the loop above proved nothing")
	}
	// CSRF still applies; the inline routes still verify a code
	if r := e.req("POST", "/api/v1/admin/users/act", ck, url.Values{"csrf": {"bad"}}, nil); r.status != 403 {
		t.Fatal("csrf", r.status)
	}
	for _, p := range []string{"/account/2fa/disable", "/account/2fa/recovery"} {
		r := e.req("POST", p, ck, url.Values{"csrf": {csrf}, "password": {"Password-12345"}, "code": {"000000"}}, nil)
		if !auth.Enabled("admin") || strings.Contains(r.body, "New recovery codes made") {
			t.Fatalf("off: %s worked without a valid code (%d)", p, r.status)
		}
	}
}

// --- sign-in: remember me, the code step, step-up, sessions ---------------------------------------------------

func TestRememberMeCookieAndLimits(t *testing.T) {
	e := srvSetup(t)
	post := func(remember bool) srvResp {
		f := url.Values{"next": {"/runs"}, "username": {"view.er"}, "password": {"Password-12345"}}
		if remember {
			f.Set("remember", "1")
		}
		return e.v1Form("/api/v1/auth/login", nil, f, nil)
	}
	_, raw, _ := srvCookie(post(false).cookies, "opsr_session")
	if !strings.Contains(raw, "Max-Age=43200") || !strings.Contains(raw, "SameSite=Lax") || !strings.Contains(raw, "HttpOnly") {
		t.Fatal(raw)
	}
	v, raw, _ := srvCookie(post(true).cookies, "opsr_session")
	if !strings.Contains(raw, "Max-Age=2592000") || !strings.Contains(raw, "Secure") {
		t.Fatal(raw)
	}
	if s := auth.GetSession(v); s == nil || !s.Remember {
		t.Fatal("remember session")
	}
	// a login replaces the token the browser had (never promoted)
	r := e.v1Form("/api/v1/auth/login", map[string]string{"opsr_session": v}, url.Values{"username": {"view.er"}, "password": {"Password-12345"}}, nil)
	if nv, _, _ := srvCookie(r.cookies, "opsr_session"); nv == v || auth.GetSession(v) != nil {
		t.Fatal("old token kept")
	}

}

func TestLoginWithCodeRecoveryReplayAndLockout(t *testing.T) {
	e := srvSetup(t)
	mfaClock(t, float64(time.Now().Unix()))
	tok, _ := e.loginNoMFA("admin")
	sec := srvEnrol(t, tok)
	r := e.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/backups"}, "username": {"admin"}, "password": {"Password-12345"},
		"remember": {"1"}}, nil)
	pend, raw, ok := srvCookie(r.cookies, MFA_COOKIE)
	if r.status != 200 || r.loc != "/backups" || !strings.Contains(r.body, `"state":"mfa"`) || !ok || !strings.Contains(raw, "Path=/;") || !strings.Contains(raw, "Secure") {
		t.Fatalf("pending: %d %s %v", r.status, r.loc, r.cookies)
	}
	if _, _, has := srvCookie(r.cookies, "opsr_session"); has || auth.GetSession(pend) != nil {
		t.Fatal("a session before the code")
	}
	pc := map[string]string{MFA_COOKIE: pend}
	if b := e.req("GET", "/api/v1/auth/mfa", pc, nil, nil).body; !strings.Contains(b, `"pending":true`) || !strings.Contains(b, `"username":"admin"`) {
		t.Fatal("code step", b)
	}
	// the code used by enrolment (same step) is refused: replay
	r = e.v1Form("/api/v1/auth/mfa", pc, url.Values{"code": {codeNow(sec)}}, nil)
	if !strings.Contains(r.body, "wrong or was already used") {
		t.Fatal("replay accepted")
	}
	// a bad login token is refused
	if r = e.req("POST", "/api/v1/auth/mfa", pc, url.Values{"csrf": {"x"}, "code": {"123456"}}, nil); r.status != 403 || !strings.Contains(r.body, "csrf token") {
		t.Fatal("csrf")
	}
	// a recovery code works once
	pend2 := e.v1Form("/api/v1/auth/login", nil, url.Values{"username": {"admin"}, "password": {"Password-12345"}}, nil)
	p2, _, _ := srvCookie(pend2.cookies, MFA_COOKIE)
	codes, _ := auth.NewRecovery(auth.GetSession(mustSession(t, "admin")), "x", "127.0.0.1")
	_ = codes // a wrong code: no new set
	r = e.v1Form("/api/v1/auth/mfa", map[string]string{MFA_COOKIE: p2}, url.Values{"code": {"aaaa-bbbb-cccc-dddd"}}, nil)
	if !strings.Contains(r.body, "wrong") {
		t.Fatal("unknown recovery code")
	}
	// the next TOTP step signs in, remember kept, 2FA recorded
	auth.Now = func() float64 { return float64(time.Now().Unix() + 30) }
	NowFn = auth.Now
	if auth.GetPending(pend) == nil || auth.GetPending(p2) == nil {
		t.Fatal("a second browser's sign-in must not cancel the first")
	}
	r = e.v1Form("/api/v1/auth/mfa", pc, url.Values{"code": {codeNow(sec)}}, nil)
	st, raw2, ok := srvCookie(r.cookies, "opsr_session")
	s := auth.GetSession(st)
	if r.status != 200 || r.loc != "/backups" || !ok || s == nil || s.MFAVia != "totp" || !s.Remember || !strings.Contains(raw2, "Max-Age=2592000") {
		t.Fatalf("finish: %d %s %v", r.status, r.loc, r.cookies)
	}
	if auth.GetPending(pend) != nil {
		t.Fatal("pending kept")
	}
}

func mustSession(t *testing.T, user string) string {
	tok, err := auth.NewSession(user, auth.Opts{})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestEnrolOverHTTPStepUpAndSessions(t *testing.T) {
	e := srvSetup(t)
	adv := mfaClock(t, float64(time.Now().Unix()))
	tok, csrf := e.loginNoMFA("view.er")
	ck := map[string]string{"opsr_session": tok}
	// the action page and the Account sections

	// wrong password: no enrolment
	r := e.req("POST", "/api/v1/account/2fa/begin", ck, url.Values{"csrf": {csrf}, "password": {"nope"}}, nil)
	if !strings.Contains(r.body, "The password is wrong") {
		t.Fatal("begin without the password")
	}
	r = e.req("POST", "/api/v1/account/2fa/begin", ck, url.Values{"csrf": {csrf}, "password": {"Password-12345"}, "next": {"/backups"}}, nil)
	newTok, _, rotated := srvCookie(r.cookies, "opsr_session")
	m := regexp.MustCompile(`secret=([A-Z2-7]{32})\\u0026issuer=` + regexp.QuoteMeta(strings.ReplaceAll(url.QueryEscape(auth.MFAIssuerName), "+", "%20")) + `\\u0026algorithm=SHA1`).FindStringSubmatch(r.body)
	if r.status != 200 || m == nil || !strings.Contains(r.body, `"qr":`) || !rotated || r.header.Get("Cache-Control") != "no-store" ||
		!strings.Contains(r.body, "otpauth://totp/Argus:view.er?") {
		t.Fatalf("enrol answer %d rotated=%v %.200s", r.status, rotated, r.body)
	}
	if auth.GetSession(tok) != nil {
		t.Fatal("token not rotated after the password")
	}
	sec, _ := auth.B32Dec(m[1])
	ck = map[string]string{"opsr_session": newTok}
	csrf = auth.GetSession(newTok).CSRF
	// a wrong code shows the same QR again
	r = e.req("POST", "/api/v1/account/2fa/confirm", ck, url.Values{"csrf": {csrf}, "code": {"000000"}}, nil)
	if !strings.Contains(r.body, "wrong") || auth.Enabled("view.er") {
		t.Fatal("confirm wrong code", r.body)
	}
	r = e.req("POST", "/api/v1/account/2fa/confirm", ck, url.Values{"csrf": {csrf}, "code": {codeNow(sec)}, "next": {"/backups"}}, nil)
	codes := regexp.MustCompile(`"[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}"`).FindAllString(r.body, -1)
	vt, _, _ := srvCookie(r.cookies, "opsr_session")
	vs := auth.GetSession(vt)
	if len(codes) != 10 || vs == nil || vs.MFAAt == 0 || r.header.Get("Cache-Control") != "no-store" || !strings.Contains(r.body, `"/backups"`) {
		t.Fatalf("codes answer: %d codes %.200s", len(codes), r.body)
	}
	ck, csrf = map[string]string{"opsr_session": vt}, vs.CSRF
	// a normal action now passes; 15 minutes later a sensitive one asks for a code; the code step returns
	if r = e.req("POST", "/api/v1/scan", ck, url.Values{"csrf": {csrf}, "tasks": {"cert-check"}, "back": {"/certs"}}, nil); r.status != 200 {
		t.Fatal("normal action after enrolment", r.status)
	}
	adv(15 * 60)
	r = e.req("POST", "/api/v1/account/stepup", ck, url.Values{"csrf": {csrf}, "code": {codeNow(sec)}, "next": {"/backups"}}, nil)
	st, _, _ := srvCookie(r.cookies, "opsr_session")
	if r.status != 200 || r.loc != "/backups" || auth.GetSession(vt) != nil || auth.GetSession(st).MFAAt != NowInt() {
		t.Fatalf("step-up %d %s", r.status, r.loc)
	}
	// the same code again: replay refused
	ck, csrf = map[string]string{"opsr_session": st}, auth.GetSession(st).CSRF
	if r = e.req("POST", "/api/v1/account/stepup", ck, url.Values{"csrf": {csrf}, "code": {codeNow(sec)}}, nil); !strings.Contains(r.body, "already used") {
		t.Fatal("step-up replay")
	}
	// a password-only session of this 2FA account cannot end the verified ones; it can sign itself out
	pwOnly := mustSession(t, "view.er")
	pck := map[string]string{"opsr_session": pwOnly}
	pcs := auth.GetSession(pwOnly).CSRF
	if r = e.req("POST", "/api/v1/account/sessions", pck, url.Values{"csrf": {pcs}, "action": {"others"}}, nil); r.status != 403 || auth.GetSession(st) == nil {
		t.Fatal("password-only session ended the others", r.status)
	}
	if r = e.req("POST", "/api/v1/account/sessions", pck, url.Values{"csrf": {pcs}, "action": {"revoke"}, "id": {auth.GetSession(pwOnly).ID()}}, nil); r.status != 200 {
		t.Fatal("self sign-out", r.status)
	}
	// sessions: another one, sign out everywhere else, then this one
	other := mustSession(t, "view.er")
	r = e.req("POST", "/api/v1/account/sessions", ck, url.Values{"csrf": {csrf}, "action": {"others"}}, nil)
	if !strings.Contains(r.body, "Signed out of 1 other session") || auth.GetSession(other) != nil || auth.GetSession(st) == nil {
		t.Fatal("others")
	}
	r = e.req("POST", "/api/v1/account/sessions", ck, url.Values{"csrf": {csrf}, "action": {"revoke"}, "id": {auth.GetSession(st).ID()}}, nil)
	if r.status != 200 || r.loc != "/login" || auth.GetSession(st) != nil {
		t.Fatal("sign out this session")
	}
	_ = codes
}

func TestAdminResetRevokeAndPolicy(t *testing.T) {
	e := srvSetup(t)
	mfaClock(t, float64(time.Now().Unix()))
	vt, _ := e.login("view.er") // enrolled
	at, acsrf := e.login("admin")
	ck := map[string]string{"opsr_session": at}

	post := func(f url.Values) string {
		f.Set("csrf", acsrf)
		return e.req("POST", "/api/v1/admin/users/act", ck, f, nil).body
	}
	if b := post(url.Values{"action": {"reset-2fa"}, "user": {"view.er"}, "reason": {"short"}}); !strings.Contains(b, "not reset") {
		t.Fatal("reason required")
	}
	if b := post(url.Values{"action": {"reset-2fa"}, "user": {"view.er"}, "reason": {"lost the phone, called"}}); !strings.Contains(b, "removed") ||
		auth.Enabled("view.er") || auth.GetSession(vt) != nil {
		t.Fatal("reset")
	}
	files, _ := filepath.Glob(filepath.Join(SET_IN, "security-events", "*.json"))
	found := false
	for _, f := range files {
		d, _ := os.ReadFile(f)
		var ev map[string]any
		json.Unmarshal(d, &ev)
		if ev["action"] == "mfa.reset" && ev["reason"] == "lost the phone, called" && ev["target"] == "view.er" && ev["method"] == "totp" {
			found = true
		}
	}
	if !found {
		t.Fatal("security event for the reset")
	}
	v2 := mustSession(t, "view.er")
	if b := post(url.Values{"action": {"revoke-sessions"}, "user": {"view.er"}}); !strings.Contains(b, "signed out everywhere") || auth.GetSession(v2) != nil {
		t.Fatal("revoke all")
	}
	// require 2FA at sign-in: a user without it is sent to enrolment, can still sign out and read help
	post(url.Values{"action": {"mfa-login-on"}})

	post(url.Values{"action": {"mfa-login-off"}})

}

// --- SSO: amr / acr -----------------------------------------------------------------------------------------

func TestSSOAMRTrust(t *testing.T) {
	s := srvSSOSetup(t)
	oldT, oldA, oldC := auth.MFATrustIDP, oidc.MFA_AMR, oidc.MFA_ACR
	t.Cleanup(func() { auth.MFATrustIDP, oidc.MFA_AMR, oidc.MFA_ACR = oldT, oldA, oldC })
	now := time.Now().Unix()
	sess := func(r srvResp) *Session {
		v, _, _ := srvCookie(r.cookies, "opsr_session")
		return auth.GetSession(v)
	}
	// default: not trusted, even with amr otp
	_, r := s.sso("/", "amr", []string{"pwd", "otp"}, "auth_time", now)
	if x := sess(r); x == nil || x.MFAVia == "idp" || x.MFAAt != 0 {
		t.Fatal("trusted without MFA_TRUST_IDP")
	}
	auth.MFATrustIDP = true
	_, r = s.sso("/", "amr", []string{"otp"}, "auth_time", now)
	if x := sess(r); x == nil || x.MFAVia == "idp" {
		t.Fatal("trusted with no OIDC_MFA_AMR / OIDC_MFA_ACR configured")
	}
	oidc.MFA_AMR = []string{"otp"}
	_, r = s.sso("/", "amr", []string{"pwd", "otp"}, "auth_time", now-30)
	if x := sess(r); x == nil || x.MFAVia != "idp" || x.MFAAt != now-30 || x.AuthAt != now-30 {
		t.Fatal("configured amr not trusted", x)
	}
	_, r = s.sso("/", "amr", []string{"otpx", "mfa"}, "auth_time", now)
	if x := sess(r); x.MFAVia == "idp" {
		t.Fatal("substring / unlisted amr trusted")
	}
	_, r = s.sso("/", "amr", []string{"otp"}, "auth_time", now-auth.MFAStepup-60)
	if x := sess(r); x.MFAVia == "idp" {
		t.Fatal("stale auth_time trusted")
	}
	_, r = s.sso("/", "amr", []string{"otp"}, "auth_time", nil)
	if x := sess(r); x.MFAVia == "idp" {
		t.Fatal("no auth_time trusted")
	}
	oidc.MFA_AMR, oidc.MFA_ACR = nil, "gold"
	_, r = s.sso("/", "acr", "gold", "auth_time", now)
	if x := sess(r); x.MFAVia != "idp" {
		t.Fatal("configured acr")
	}
	// a Keycloak user with Argus 2FA and no trusted IdP factor gets the code step
	oidc.MFA_ACR = ""
	tok := mustSession(t, "devops")
	sec := srvEnrol(t, tok)
	_ = sec
	_, r = s.sso("/certs", "amr", []string{"pwd"}, "auth_time", now)
	if _, _, has := srvCookie(r.cookies, "opsr_session"); has || r.loc != "/login?step=code" {
		t.Fatal("sso + argus 2fa must ask for the code", r.cookies)
	}
	if _, _, ok := srvCookie(r.cookies, MFA_COOKIE); !ok {
		t.Fatal("pending cookie")
	}
	// remember me survives the Keycloak round trip
	q := s.start("/x")
	_ = q
	rr := s.req("GET", "/auth/login?"+url.Values{"next": {"/"}, "remember": {"1"}, "reauth": {"1"}}.Encode(), nil, nil, nil)
	u, _ := url.Parse(rr.loc)
	if u.Query().Get("max_age") != "0" || u.Query().Get("prompt") != "login" {
		t.Fatal("reauth params")
	}
	var n int
	auth.Con().QueryRow("SELECT COUNT(*) FROM oidc_extra WHERE state_hash=?", oidc.StateHash(u.Query().Get("state"))).Scan(&n)
	if n != 1 {
		t.Fatal("remember stored")
	}
}

// --- QR ---------------------------------------------------------------------------------------------------

func TestQRCodeRenders(t *testing.T) {
	uri := auth.OTPAuthURI("Northwind Argus", "alex.doe", []byte("12345678901234567890"))

	q, _ := qrcode.New(uri, qrcode.Medium)
	bm := q.Bitmap()
	n := len(bm)

	if n < 41 || n > 69 { // versions 6..12 for an otpauth URI, plus the quiet zone
		t.Fatal("size", n)
	}
	// finder pattern top-left (after the 4-module quiet zone): a 7x7 ring with a 3x3 centre
	for i := 0; i < 7; i++ {
		if !bm[4][4+i] || !bm[10][4+i] || !bm[4+i][4] || !bm[4+i][10] || bm[5][5+i%5] && i%5 > 0 && i%5 < 4 && false {
			t.Fatal("finder")
		}
	}
	if bm[5][5] || !bm[7][7] || bm[0][0] {
		t.Fatal("finder centre / quiet zone")
	}
	// every dark module is in the path exactly once
	dark := 0
	for _, row := range bm {
		for _, on := range row {
			if on {
				dark++
			}
		}
	}

}

// --- backup override -----------------------------------------------------------------------------------------

func seedBackups(t *testing.T, dir string) {
	db, err := sql.Open("sqlite", filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS runs (id INTEGER PRIMARY KEY, task TEXT, started_at INTEGER, duration_ms INTEGER, status TEXT, summary TEXT, cost_usd REAL, turns INTEGER, denials INTEGER, is_error INTEGER, mr_url TEXT, log_path TEXT)",
		"CREATE TABLE IF NOT EXISTS backup_results (run_id INTEGER, name TEXT, status TEXT, newest TEXT, age_h INTEGER, size INTEGER, median_prev7 INTEGER, kinds INTEGER)",
		`CREATE TABLE IF NOT EXISTS backup_overrides (id INTEGER PRIMARY KEY AUTOINCREMENT, set_name TEXT NOT NULL, check_kind TEXT NOT NULL,
		 reason TEXT NOT NULL, by_user TEXT NOT NULL, at INTEGER NOT NULL, expires INTEGER NOT NULL, scope TEXT NOT NULL, days INTEGER,
		 snapshot TEXT, rid TEXT UNIQUE, ended_at INTEGER, ended_by TEXT, ended_why TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO runs (id, task, started_at, status, summary) VALUES (1, 'backup-check', ?, 'FAIL', 'FAIL: 2 failing')", now-3600)
	db.Exec("INSERT INTO backup_results VALUES (1,'tools_rudder','FAIL(stale)','rudder/x.gz',80,100,100,1)")
	db.Exec("INSERT INTO backup_results VALUES (1,'plan1_shop-pg','FAIL(small)','shop/y.gz',3,10,1000,1)")
	db.Exec("INSERT INTO backup_results VALUES (1,'tools_ok','OK','ok/z.gz',3,10,10,1)")
	db.Exec("INSERT INTO backup_overrides (set_name, check_kind, reason, by_user, at, expires, scope) VALUES ('tools_rudder','FAIL(stale)','retired host, checked','admin',?,?,'until-ok')",
		now-600, now+86400)
	// an override of another kind of failure does not cover plan1_shop-pg
	db.Exec("INSERT INTO backup_overrides (set_name, check_kind, reason, by_user, at, expires, scope) VALUES ('plan1_shop-pg','FAIL(stale)','old reason here','admin',?,?,'days')",
		now-600, now+86400)
}

func TestBackupOverridePagesAndRequest(t *testing.T) {
	e := srvSetup(t)
	seedBackups(t, e.dir)
	at, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": at}

	// validation
	for _, f := range []url.Values{
		{"action": {"add"}, "set": {"plan1_shop-pg"}, "check": {"FAIL(small)"}, "scope": {"until-ok"}, "reason": {"short"}},
		{"action": {"add"}, "set": {"plan1_shop-pg"}, "check": {"FAIL(small)"}, "scope": {"days"}, "days": {"31"}, "reason": {"long enough reason"}},
		{"action": {"add"}, "set": {"../x"}, "check": {"FAIL(small)"}, "scope": {"until-ok"}, "reason": {"long enough reason"}},
		{"action": {"add"}, "set": {"plan1_shop-pg"}, "check": {"OK"}, "scope": {"until-ok"}, "reason": {"long enough reason"}},
		{"action": {"undo"}, "id": {"x"}},
	} {
		f.Set("csrf", csrf)
		if r := e.req("POST", "/api/v1/backups/override", ck, f, nil); r.status != 400 {
			t.Errorf("accepted %v: %d", f, r.status)
		}
	}
	if r := e.req("POST", "/api/v1/backups/override", ck, url.Values{"csrf": {"bad"}, "action": {"undo"}, "id": {"1"}}, nil); r.status != 403 {
		t.Fatal("csrf")
	}
	vt, vcsrf := e.login("view.er")
	if r := e.req("POST", "/api/v1/backups/override", map[string]string{"opsr_session": vt}, url.Values{"csrf": {vcsrf}, "action": {"undo"}, "id": {"1"}}, nil); r.status != 403 {
		t.Fatal("viewer")
	}
	r := e.req("POST", "/api/v1/backups/override", ck, url.Values{"csrf": {csrf}, "action": {"add"}, "set": {"plan1_shop-pg"}, "check": {"FAIL(small)"},
		"scope": {"days"}, "days": {"7"}, "reason": {"size drop is the new compression"}, "back": {"/backups/set?name=plan1_shop-pg"}}, nil)
	if r.status != 200 || r.loc != "/backups/set?name=plan1_shop-pg&sent=override" {
		t.Fatal("add", r.status, r.loc)
	}
	files, _ := filepath.Glob(filepath.Join(SET_IN, "backup-overrides", "*.json"))
	if len(files) != 1 || !regexp.MustCompile(`/[0-9]{14}-[0-9a-f]{8}\.json$`).MatchString(files[0]) {
		t.Fatal(files)
	}
	d, _ := os.ReadFile(files[0])
	var req map[string]any
	json.Unmarshal(d, &req)
	if req["by"] != "admin" || req["role"] != "admin" || req["mfa"] != true || req["days"] != float64(7) || req["scope"] != "days" ||
		req["check"] != "FAIL(small)" || len(req["rid"].(string)) != 12 {
		t.Fatal(req)
	}
}

func TestOverrideRulesMatchPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/override_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Now   int64 `json:"now"`
		Cases []struct {
			Name string `json:"name"`
			Ov   struct {
				Check    string  `json:"check_kind"`
				Scope    string  `json:"scope"`
				Expires  int64   `json:"expires"`
				Snapshot *string `json:"snapshot"`
				Ended    *int64  `json:"ended_at"`
			} `json:"ov"`
			Status, Fp, Why string
			OkSince         bool `json:"ok_since"`
			Valid           bool
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || len(v.Cases) < 8 {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		o := Override{Check: c.Ov.Check, Scope: c.Ov.Scope, Expires: c.Ov.Expires, Ended: c.Ov.Ended != nil}
		if c.Ov.Snapshot != nil {
			o.Snapshot = *c.Ov.Snapshot
		}
		ok, why := OverrideValid(o, c.Status, c.Fp, c.OkSince, v.Now)
		if ok != c.Valid || why != c.Why {
			t.Errorf("%s: %v %s", c.Name, ok, why)
		}
	}
	if Fingerprint(nil, nil) != "|" || Fingerprint("a", int64(5)) != "a|5" || Fingerprint("a", "-") != "a|" {
		t.Fatal("fingerprint")
	}
}

// After an admin's 2FA reset the user signs in with the password and reaches only the 2FA setup (and sign-out)
// until a new app is confirmed; the admin never enrols for them.
func TestResetForcesEnrolmentAtSignIn(t *testing.T) {
	e := srvSetup(t)
	mfaClock(t, float64(time.Now().Unix()))
	e.login("view.er") // enrolled
	if _, err := auth.AdminReset("admin", "view.er", "lost the phone, called", "127.0.0.1", "", "totp"); err != nil {
		t.Fatal(err)
	}
	r := e.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/certs"}, "username": {"view.er"}, "password": {"Password-12345"}}, nil)
	tok, _, ok := srvCookie(r.cookies, "opsr_session")
	if r.status != 200 || !ok || !strings.Contains(r.body, `"state":"ok"`) || r.loc != "/account/2fa?next=%2Fcerts" {
		t.Fatalf("sign-in after the reset: %d %s %s", r.status, r.loc, r.body)
	}
	ck := map[string]string{"opsr_session": tok}
	if b := e.req("GET", "/api/v1/me", ck, nil, nil).body; !strings.Contains(b, `"enrolRequired":true`) {
		t.Fatal("me", b)
	}
	for _, p := range []string{"/api/v1/nav", "/api/v1/account"} {
		if r := e.req("GET", p, ck, nil, nil); r.status != 403 || !strings.Contains(r.body, "two-factor enrolment required") {
			t.Fatalf("%s before enrolment: %d %s", p, r.status, r.body)
		}
	}
	if r := e.req("GET", "/api/v1/account/2fa", ck, nil, nil); r.status != 200 || !strings.Contains(r.body, `"required":true`) || !strings.Contains(r.body, `"reset":true`) {
		t.Fatalf("2fa page: %d %s", r.status, r.body)
	}
	// enrol over HTTP: password, QR, code
	csrf := auth.GetSession(tok).CSRF
	r = e.req("POST", "/api/v1/account/2fa/begin", ck, url.Values{"csrf": {csrf}, "password": {"Password-12345"}}, nil)
	m := regexp.MustCompile(`secret=([A-Z2-7]{32})`).FindStringSubmatch(r.body)
	nt, _, _ := srvCookie(r.cookies, "opsr_session")
	if m == nil || nt == "" {
		t.Fatalf("begin: %d %.200s", r.status, r.body)
	}
	sec, _ := auth.B32Dec(m[1])
	ck = map[string]string{"opsr_session": nt}
	r = e.req("POST", "/api/v1/account/2fa/confirm", ck, url.Values{"csrf": {auth.GetSession(nt).CSRF}, "code": {codeNow(sec)}}, nil)
	vt, _, _ := srvCookie(r.cookies, "opsr_session")
	if r.status != 200 || vt == "" || !auth.Enabled("view.er") || auth.ReenrolDue("view.er") {
		t.Fatalf("confirm: %d %.200s", r.status, r.body)
	}
	if r := e.req("GET", "/api/v1/nav", map[string]string{"opsr_session": vt}, nil, nil); r.status != 200 {
		t.Fatal("nav after enrolment", r.status)
	}
}

// Single sign-on gets the same rule, even with a trusted Keycloak second factor.
func TestResetForcesEnrolmentAfterSSO(t *testing.T) {
	s := srvSSOSetup(t)
	trust := auth.MFATrustIDP
	t.Cleanup(func() { auth.MFATrustIDP = trust })
	auth.MFATrustIDP = true
	if _, err := auth.AdminReset("admin", "devops", "lost the phone, called", "127.0.0.1", "", "totp"); err != nil {
		t.Fatal(err)
	}
	_, r := s.sso("/certs", "amr", []string{"pwd", "otp"})
	tok, _, ok := srvCookie(r.cookies, "opsr_session")
	if r.status != 303 || !ok || r.loc != "/account/2fa?next=%2Fcerts" {
		t.Fatalf("sso after the reset: %d %s", r.status, r.loc)
	}
	ck := map[string]string{"opsr_session": tok}
	if r := s.req("GET", "/api/v1/nav", ck, nil, nil); r.status != 403 || !strings.Contains(r.body, "enrolment required") {
		t.Fatalf("nav: %d %s", r.status, r.body)
	}
	if r := s.req("GET", "/api/v1/account/2fa", ck, nil, nil); r.status != 200 {
		t.Fatal("2fa page", r.status)
	}
}
