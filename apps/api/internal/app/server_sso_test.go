package app

// Keycloak SSO end to end against a fake OpenID provider that signs id_tokens with its own RSA key: the Go
// version of tests/test_sso.py (happy paths, every refusal, replay, open redirects, SSO only and break-glass,
// logout hand-off, not configured, discovery mismatch).

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
)

const srvSecret = "test-client-secret-0123456789"

var (
	srvKeyOnce sync.Once
	srvKey     *rsa.PrivateKey
)

type srvProvider struct {
	ts       *httptest.Server
	issuer   string
	override string
	mu       sync.Mutex
	codes    map[string][2]string // code -> (id_token, challenge)
	requests []url.Values
}

func srvB64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func srvSign(claims map[string]any, kid, alg string) string {
	h, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
	p, _ := json.Marshal(claims)
	in := srvB64u(h) + "." + srvB64u(p)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, srvKey, crypto.SHA256, sum[:])
	return in + "." + srvB64u(sig)
}

func srvNewProvider(t *testing.T) *srvProvider {
	srvKeyOnce.Do(func() { srvKey, _ = rsa.GenerateKey(rand.Reader, 2048) })
	p := &srvProvider{codes: map[string][2]string{}}
	p.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		b := p.ts.URL
		switch r.URL.Path {
		case "/realms/test/.well-known/openid-configuration":
			iss := p.issuer
			if p.override != "" {
				iss = p.override
			}
			reply(200, map[string]any{"issuer": iss, "authorization_endpoint": b + "/realms/test/protocol/openid-connect/auth",
				"token_endpoint": b + "/realms/test/protocol/openid-connect/token", "jwks_uri": b + "/realms/test/protocol/openid-connect/certs",
				"end_session_endpoint": b + "/realms/test/protocol/openid-connect/logout"})
		case "/realms/test/protocol/openid-connect/certs":
			reply(200, map[string]any{"keys": []any{
				map[string]any{"kid": "k1", "kty": "RSA", "alg": "RS256", "use": "sig", "n": srvB64u(srvKey.N.Bytes()),
					"e": srvB64u(big.NewInt(int64(srvKey.E)).Bytes())},
				map[string]any{"kid": "enc", "kty": "RSA", "use": "enc", "n": "AQAB", "e": "AQAB"}}})
		case "/realms/test/protocol/openid-connect/token":
			r.ParseForm()
			p.mu.Lock()
			p.requests = append(p.requests, r.PostForm)
			e, ok := p.codes[r.PostForm.Get("code")]
			delete(p.codes, r.PostForm.Get("code"))
			p.mu.Unlock()
			ch := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("client_secret") != srvSecret ||
				r.PostForm.Get("client_id") != "argus" || r.PostForm.Get("redirect_uri") != "https://argus.dev.northwind.example/auth/callback" ||
				srvB64u(ch[:]) != e[1] {
				reply(400, map[string]any{"error": "invalid_grant"})
				return
			}
			reply(200, map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": e[0]})
		default:
			reply(404, map[string]any{})
		}
	}))
	p.issuer = p.ts.URL + "/realms/test"
	t.Cleanup(p.ts.Close)
	return p
}

type srvSSO struct {
	*srvEnv
	p   *srvProvider
	log *strings.Builder
}

func srvSSOSetup(t *testing.T) *srvSSO {
	e := srvSetup(t)
	p := srvNewProvider(t)
	oidc.AllowLoopbackHTTP = true
	oidc.ConfigureWith(p.issuer, "argus", srvSecret, "https://argus.dev.northwind.example", "")
	h, _ := auth.HashPW("Password-12345")
	for _, u := range [][2]string{{"sam.roe", "viewer"}, {"local.admin", "admin"}} {
		auth.Con().Exec("INSERT INTO users VALUES (?,?,?,0,0,0)", u[0], h, u[1])
	}
	lb := &strings.Builder{}
	Out = lb
	t.Cleanup(func() { oidc.AllowLoopbackHTTP = false; oidc.ConfigureWith("", "", "", "", "") })
	return &srvSSO{e, p, lb}
}

func (s *srvSSO) start(next string) url.Values {
	s.t.Helper()
	r := s.req("GET", "/auth/login?"+url.Values{"next": {next}}.Encode(), nil, nil, nil)
	if r.status != 302 || !strings.HasPrefix(r.loc, s.p.issuer+"/protocol/openid-connect/auth?") {
		s.t.Fatalf("start: %d %s %s", r.status, r.loc, r.body)
	}
	u, _ := url.Parse(r.loc)
	q := u.Query()
	st, raw, _ := srvCookie(r.cookies, "opsr_oidc")
	if st != q.Get("state") {
		s.t.Fatal("state cookie")
	}
	for _, f := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Max-Age=600", "Path=/auth"} {
		if !strings.Contains(raw, f) {
			s.t.Fatal("state cookie flag", f)
		}
	}
	return q
}

func (s *srvSSO) claims(q url.Values, kv ...any) map[string]any {
	now := time.Now().Unix()
	c := map[string]any{"iss": s.p.issuer, "aud": "argus", "azp": "argus", "sub": "sub-x", "exp": now + 300, "iat": now,
		"nonce": q.Get("nonce"), "preferred_username": "devops", "argus_roles": []string{"admin"}}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			delete(c, kv[i].(string))
		} else {
			c[kv[i].(string)] = kv[i+1]
		}
	}
	return c
}

func (s *srvSSO) callback(q url.Values, token, cookieState string, extra url.Values) srvResp {
	s.t.Helper()
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	s.p.mu.Lock()
	s.p.codes[code] = [2]string{token, q.Get("code_challenge")}
	s.p.mu.Unlock()
	params := url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {s.p.issuer}}
	for k, v := range extra {
		params[k] = v
	}
	if cookieState == "-" {
		cookieState = q.Get("state")
	}
	s.log.Reset()
	r := s.req("GET", "/auth/callback?"+params.Encode(), map[string]string{"opsr_oidc": cookieState}, nil, nil)
	for _, secret := range []string{code, srvSecret, q.Get("state"), q.Get("nonce")} {
		if strings.Contains(s.log.String(), secret) {
			s.t.Fatal("secret in the log")
		}
	}
	return r
}

func (s *srvSSO) sso(next string, kv ...any) (url.Values, srvResp) {
	q := s.start(next)
	return q, s.callback(q, srvSign(s.claims(q, kv...), "k1", "RS256"), "-", nil)
}

func (s *srvSSO) refused(r srvResp, reason string) {
	s.t.Helper()
	if _, _, ok := srvCookie(r.cookies, "opsr_session"); r.status != 303 || ok || r.loc != "/login?sso=incomplete" {
		s.t.Fatalf("not refused: %d %v", r.status, r.cookies)
	}
	if !strings.Contains(s.log.String(), reason) {
		s.t.Fatalf("log %q has no %q", s.log.String(), reason)
	}
}

func TestSSOLoginPageAndRequest(t *testing.T) {
	s := srvSSOSetup(t)

	q := s.start("/certs")
	if q.Get("response_type") != "code" || q.Get("client_id") != "argus" || q.Get("redirect_uri") != "https://argus.dev.northwind.example/auth/callback" ||
		q.Get("scope") != "openid profile email" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 ||
		q.Get("state") == q.Get("nonce") {
		t.Fatalf("authorization request %v", q)
	}
	var n int
	auth.Con().QueryRow("SELECT COUNT(*) FROM oidc_states WHERE state_hash=?", oidc.StateHash(q.Get("state"))).Scan(&n)
	var raw int
	auth.Con().QueryRow("SELECT COUNT(*) FROM oidc_states WHERE state_hash=?", q.Get("state")).Scan(&raw)
	if n != 1 || raw != 0 {
		t.Fatal("state stored hashed")
	}
}

func TestSSOAdminHappyPathAndRoles(t *testing.T) {
	s := srvSSOSetup(t)
	q, r := s.sso("/certs", "argus_roles", []string{"admin", "viewer"})
	if r.status != 303 || r.loc != "/certs" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	tok, raw, _ := srvCookie(r.cookies, "opsr_session")
	for _, f := range []string{"HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(raw, f) {
			t.Fatal(f)
		}
	}
	if v, _, _ := srvCookie(r.cookies, "opsr_oidc"); v != "" {
		t.Fatal("state cookie not cleared")
	}
	ss := auth.GetSession(tok)
	if ss.Username != "devops" || ss.Role != "admin" || ss.Source != "keycloak" || ss.MustChange != 0 {
		t.Fatalf("%+v", ss)
	}
	if !strings.Contains(s.log.String(), "login via keycloak ok user='devops' role=admin") {
		t.Fatal(s.log.String())
	}
	tr := s.p.requests[len(s.p.requests)-1]
	ch := sha256.Sum256([]byte(tr.Get("code_verifier")))
	if srvB64u(ch[:]) != q.Get("code_challenge") {
		t.Fatal("PKCE")
	}
	// viewer, then admin at the next sign-in (the role comes from Keycloak every time)
	_, r = s.sso("/", "preferred_username", "viewer.one", "sub", "s-v1", "argus_roles", []string{"viewer"})

	_, r = s.sso("/", "preferred_username", "viewer.one", "sub", "s-v1", "argus_roles", []string{"admin"})
	at, _, _ := srvCookie(r.cookies, "opsr_session")
	if auth.GetSession(at).Role != "admin" {
		t.Fatal("role re-evaluated")
	}
	// no role: denied, no session
	_, r = s.sso("/", "preferred_username", "norole", "sub", "s-nr", "argus_roles", []string{"other"})
	if _, _, ok := srvCookie(r.cookies, "opsr_session"); r.status != 303 || ok || r.loc != "/login?sso=norole" {
		t.Fatal("no role")
	}
	_, r = s.sso("/", "preferred_username", "norole2", "sub", "s-nr2", "argus_roles", nil)
	if r.status != 303 || r.loc != "/login?sso=norole" {
		t.Fatal("no role claim")
	}
}

func TestSSORefusals(t *testing.T) {
	s := srvSSOSetup(t)
	now := time.Now().Unix()
	q := s.start("/")
	s.refused(s.callback(q, srvSign(s.claims(q), "k1", "RS256"), strings.Repeat("x", 43), nil), "state does not match")
	q = s.start("/")
	s.refused(s.callback(q, srvSign(s.claims(q), "k1", "RS256"), "", nil), "state does not match")
	a, b := s.start("/"), s.start("/")
	s.refused(s.callback(a, srvSign(s.claims(a), "k1", "RS256"), b.Get("state"), nil), "state does not match")
	for _, c := range []struct {
		kv     []any
		reason string
	}{
		{[]any{"nonce", "other"}, "nonce mismatch"},
		{[]any{"aud", "other-client", "azp", nil}, "audience mismatch"},
		{[]any{"aud", []string{"argus", "other"}, "azp", "other"}, "azp mismatch"},
		{[]any{"aud", []string{"argus", "other"}, "azp", nil}, "azp mismatch"},
		{[]any{"iss", "https://evil.example/realms/test"}, "issuer mismatch"},
		{[]any{"exp", now - 120, "iat", now - 400}, "expired"},
		{[]any{"iat", now + 600}, "wrong time"},
		{[]any{"iat", now - 3600, "exp", now + 300}, "wrong time"},
		{[]any{"sub", ""}, "no subject"},
	} {
		q := s.start("/")
		s.refused(s.callback(q, srvSign(s.claims(q, c.kv...), "k1", "RS256"), "-", nil), c.reason)
	}
	q = s.start("/")
	s.refused(s.callback(q, srvSign(s.claims(q), "k1", "RS256"), "-", url.Values{"iss": {"https://evil.example"}}), "another issuer")
	// within the leeway: accepted
	q = s.start("/")
	if r := s.callback(q, srvSign(s.claims(q, "exp", now-30, "preferred_username", "leeway", "sub", "s-lw"), "k1", "RS256"), "-", nil); r.status != 303 {
		t.Fatal("leeway")
	}
}

func TestSSOSignatures(t *testing.T) {
	s := srvSSOSetup(t)
	q := s.start("/")
	good := srvSign(s.claims(q), "k1", "RS256")
	parts := strings.Split(good, ".")
	forged, _ := json.Marshal(s.claims(q, "preferred_username", "mallory"))
	s.refused(s.callback(q, parts[0]+"."+srvB64u(forged)+"."+parts[2], "-", nil), "signature invalid")
	q = s.start("/")
	hn, _ := json.Marshal(map[string]any{"alg": "none"})
	cl, _ := json.Marshal(s.claims(q))
	s.refused(s.callback(q, srvB64u(hn)+"."+srvB64u(cl)+".", "-", nil), "algorithm not allowed")
	q = s.start("/")
	s.refused(s.callback(q, srvSign(s.claims(q), "k1", "HS256"), "-", nil), "algorithm not allowed")
	q = s.start("/")
	s.refused(s.callback(q, srvSign(s.claims(q), "nope", "RS256"), "-", nil), "unknown signing key")
	q = s.start("/")
	s.refused(s.callback(q, "not.a-jwt", "-", nil), "id_token")
	// a 1024-bit key is refused even with a valid signature
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	msg := []byte("a.b")
	sum := sha256.Sum256(msg)
	sig, _ := rsa.SignPKCS1v15(rand.Reader, small, crypto.SHA256, sum[:])
	if oidc.RSAVerify(small.N, big.NewInt(int64(small.E)), msg, sig) {
		t.Fatal("1024-bit key accepted")
	}
	sum = sha256.Sum256(msg)
	sig, _ = rsa.SignPKCS1v15(rand.Reader, srvKey, crypto.SHA256, sum[:])
	E := big.NewInt(int64(srvKey.E))
	if !oidc.RSAVerify(srvKey.N, E, msg, sig) || oidc.RSAVerify(srvKey.N, E, []byte("a.bx"), sig) ||
		oidc.RSAVerify(srvKey.N, E, msg, sig[:len(sig)-1]) || oidc.RSAVerify(srvKey.N, E, msg, make([]byte, len(sig))) {
		t.Fatal("RSAVerify")
	}
}

func TestSSOReplayErrorExpiryExchange(t *testing.T) {
	s := srvSSOSetup(t)
	q := s.start("/")
	if r := s.callback(q, srvSign(s.claims(q, "preferred_username", "replay", "sub", "s-rp"), "k1", "RS256"), "-", nil); r.status != 303 {
		t.Fatal("first use")
	}
	s.refused(s.callback(q, srvSign(s.claims(q, "preferred_username", "replay", "sub", "s-rp"), "k1", "RS256"), "-", nil), "already used")
	q = s.start("/")
	s.log.Reset()
	r := s.req("GET", "/auth/callback?"+url.Values{"error": {"access_denied"}, "state": {q.Get("state")}}.Encode(),
		map[string]string{"opsr_oidc": q.Get("state")}, nil, nil)
	if r.status != 303 || !strings.Contains(s.log.String(), "Keycloak returned access_denied") || auth.OIDCTake(oidc.StateHash(q.Get("state"))) != nil {
		t.Fatal("keycloak error consumes the state")
	}
	q = s.start("/")
	auth.Con().Exec("UPDATE oidc_states SET created=created-700")
	s.refused(s.callback(q, srvSign(s.claims(q), "k1", "RS256"), "-", nil), "took too long")
	q = s.start("/")
	q2 := url.Values{}
	for k, v := range q {
		q2[k] = v
	}
	q2.Set("code_challenge", "wrong")
	s.refused(s.callback(q2, srvSign(s.claims(q), "k1", "RS256"), "-", nil), "HTTP 400 invalid_grant")
}

func TestSSOOpenRedirectNext(t *testing.T) {
	s := srvSSOSetup(t)
	for i, bad := range []string{"//evil.example/x", "https://evil.example", `/\evil.example`, "/\t/evil.example", "evil", "/\n/evil.example", "/auth/login"} {
		_, r := s.sso(bad, "preferred_username", "redir", "sub", "s-rd")
		if r.status != 303 || r.loc != "/today" {
			t.Fatalf("%d %q: %d", i, bad, r.status)
		}
	}
}

func TestSSOAccountsBindingDisabledReserved(t *testing.T) {
	s := srvSSOSetup(t)
	if _, r := s.sso("/", "preferred_username", "local.admin", "sub", "s-la", "argus_roles", []string{"viewer"}); r.status != 303 {
		t.Fatal("convert")
	}
	if auth.Source("local.admin") != "keycloak" || auth.Login("local.admin", "Password-12345", "127.0.0.9") != "" {
		t.Fatal("local account converted, password gone")
	}
	if _, r := s.sso("/", "preferred_username", "admin", "sub", "s-adm"); r.status != 303 || r.loc != "/login?sso=denied" || auth.Source("admin") != "local" {
		t.Fatal("break-glass reserved")
	}
	if _, r := s.sso("/", "preferred_username", "bound", "sub", "s-1"); r.status != 303 {
		t.Fatal("bound")
	}
	if _, r := s.sso("/", "preferred_username", "bound", "sub", "s-2"); r.status != 303 || r.loc != "/login?sso=denied" {
		t.Fatal("subject binding")
	}
	auth.SetDisabled("bound", true)
	if _, r := s.sso("/", "preferred_username", "bound", "sub", "s-1"); r.status != 303 || r.loc != "/login?sso=denied" {
		t.Fatal("disabled")
	}
	if _, r := s.sso("/", "preferred_username", "Evil<b>", "sub", "s-ev"); r.status != 303 || r.loc != "/login?sso=username" {
		t.Fatal("unusable username")
	}
}

func TestSSOKeycloakUserHasNoPassword(t *testing.T) {
	s := srvSSOSetup(t)
	srvNeedsGroup(t, "board") // the account page shows the Jira and Matrix sections
	_, r := s.sso("/", "preferred_username", "kc.user", "sub", "s-kc", "argus_roles", []string{"viewer"})
	tok, _, _ := srvCookie(r.cookies, "opsr_session")

	csrf := auth.GetSession(tok).CSRF
	if r := s.req("POST", "/api/v1/account", map[string]string{"opsr_session": tok}, url.Values{"csrf": {csrf}, "new": {strings.Repeat("x", 14)}, "new2": {strings.Repeat("x", 14)}}, nil); r.status != 403 {
		t.Fatal("keycloak password change")
	}
	at, ac := s.login("admin")
	for _, action := range []string{"reset", "role-admin"} {
		r := s.req("POST", "/api/v1/admin/users/act", map[string]string{"opsr_session": at}, url.Values{"csrf": {ac}, "action": {action}, "user": {"kc.user"}}, nil)
		if !strings.Contains(r.body, "managed there") || strings.Contains(r.body, "Temporary password") {
			t.Fatal(action)
		}
	}

}

func TestSSOOnlyBreakGlassAndLogout(t *testing.T) {
	s := srvSSOSetup(t)
	pw := func(user string) (string, string) {
		r := s.v1Form("/api/v1/auth/login", nil, url.Values{"next": {"/"}, "username": {user}, "password": {"Password-12345"}}, nil)
		v, _, _ := srvCookie(r.cookies, "opsr_session")
		return v, r.body
	}
	if tok, _ := pw("sam.roe"); tok == "" {
		t.Fatal("default: password allowed")
	}
	os.WriteFile(filepath.Join(SET_IN, "settings.json"), []byte(`{"auth": {"sso_only": true}}`), 0o644)
	if tok, body := pw("sam.roe"); tok != "" || !strings.Contains(body, "Password sign-in is turned off") {
		t.Fatal("sso only")
	}
	if tok, _ := pw("admin"); tok == "" {
		t.Fatal("break-glass")
	}

	oidc.ConfigureWith("", "", "", "", "")
	if tok, _ := pw("sam.roe"); tok == "" {
		t.Fatal("no keycloak: no lock-out")
	}
	oidc.ConfigureWith(s.p.issuer, "argus", srvSecret, "https://argus.dev.northwind.example", "")
	os.Remove(filepath.Join(SET_IN, "settings.json"))
	// logout hands over to Keycloak's end-session endpoint
	_, r := s.sso("/", "preferred_username", "leaver", "sub", "s-lv")
	tok, _, _ := srvCookie(r.cookies, "opsr_session")
	var idt string
	auth.Con().QueryRow("SELECT id_token FROM sessions WHERE username='leaver'").Scan(&idt)
	r = s.req("POST", "/api/v1/auth/logout", map[string]string{"opsr_session": tok}, url.Values{"csrf": {auth.GetSession(tok).CSRF}}, nil)
	if r.status != 200 || r.loc == "" {
		t.Fatal("logout hand-off")
	}
	u, _ := url.Parse(r.loc)
	lq := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != s.p.issuer+"/protocol/openid-connect/logout" || lq.Get("post_logout_redirect_uri") != "https://argus.dev.northwind.example/login" ||
		lq.Get("id_token_hint") != idt || lq.Get("client_id") != "argus" || auth.GetSession(tok) != nil {
		t.Fatal("logout url", u)
	}
	if _, raw, _ := srvCookie(r.cookies, "opsr_session"); !strings.Contains(raw, "Max-Age=0") {
		t.Fatal("cookie cleared")
	}
	lt, lc := s.login("sam.roe")
	if r := s.req("POST", "/api/v1/auth/logout", map[string]string{"opsr_session": lt}, url.Values{"csrf": {lc}}, nil); r.status != 200 || r.loc != "/login" {
		t.Fatal("local logout")
	}
}

func TestSSONotConfiguredAndUnavailable(t *testing.T) {
	s := srvSSOSetup(t)
	oidc.ConfigureWith(s.p.issuer, "argus", "", "", "")
	if oidc.Configured() {
		t.Fatal("partial configuration is off")
	}
	oidc.ConfigureWith("", "", "", "", "")

	if s.req("GET", "/auth/login", nil, nil, nil).status != 404 || s.req("GET", "/auth/callback?code=x&state=y", nil, nil, nil).status != 404 {
		t.Fatal("auth routes 404")
	}
	oidc.ConfigureWith(s.p.issuer, "argus", srvSecret, "https://argus.dev.northwind.example", "")
	s.p.override = "https://evil.example/realms/test"
	r := s.req("GET", "/auth/login", nil, nil, nil)
	if r.status != 303 || !strings.HasPrefix(r.loc, "/login?sso=unavailable") {
		t.Fatal("discovery mismatch", r.status)
	}
	oidc.AllowLoopbackHTTP = false
	oidc.ConfigureWith("http://sso.dev.northwind.example/realms/master", "argus", srvSecret, "", "")
	if oidc.Metadata() != nil {
		t.Fatal("http issuer accepted")
	}
}
