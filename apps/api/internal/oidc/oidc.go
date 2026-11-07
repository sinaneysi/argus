// Package oidc is app/oidc.py: Keycloak single sign-on, the OpenID Connect authorization code flow with PKCE.
//
// Configured by OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET (optional OPS_PUBLIC_URL, OIDC_CA_FILE).
// Without the three, Configured() is false and the app has password login only.
//
// Security checks (same as the Python module):
//   - discovery and every back-channel call use HTTPS with certificate and host-name verification; the
//     discovery document must name exactly the configured issuer, and its endpoints must be https;
//   - /auth/login stores state, nonce and a PKCE verifier (S256) server-side (auth.db, hashed state, 10 min);
//     the browser gets the state in a short-lived HttpOnly, Secure, SameSite=Lax cookie; the callback needs
//     both, and the row is deleted on first use (no replay, no login CSRF);
//   - the id_token: RS256 only (PKCS#1 v1.5 against the issuer's JWKS, key >= 2048 bit), iss, aud/azp, exp,
//     iat and nbf with 60 s leeway, the nonce, a subject;
//   - tokens, codes and the client secret are never logged.
package oidc

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/py"
)

const (
	SCOPE      = "openid profile email"
	ROLE_CLAIM = "argus_roles"
	COOKIE     = "opsr_oidc"
	STATE_TTL  = 600
	LEEWAY     = 60
	RETRY      = 30
	JWKS_RETRY = 60
	MAX_BODY   = 1 << 20
)

// AllowLoopbackHTTP: tests only (a fake provider on 127.0.0.1 without TLS). Never set in the app.
var AllowLoopbackHTTP = false

// Error is a login that must not go through; its text is a short, safe reason.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(f string, a ...any) error { return &Error{fmt.Sprintf(f, a...)} }

var (
	ISSUER, CLIENT_ID, CLIENT_SECRET          string
	PUBLIC_URL, REDIRECT_URI, POST_LOGOUT_URI string
	CA_FILE                                   string

	mu        sync.Mutex
	meta      *py.OMap
	metaRetry float64
	keys      = map[string]*rsa.PublicKey{}
	keysAt    float64
	client    *http.Client
)

// Now is time.time() (tests may pin it).
var Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Config holds explicit values; empty fields fall back to the environment.
type Config struct {
	Issuer, ClientID, ClientSecret, PublicURL, CAFile *string
	Env                                               map[string]string
}

func strp(s string) *string { return &s }

// Configure is configure(...): (re)load the configuration and clear the caches.
func Configure(c Config) {
	mu.Lock()
	defer mu.Unlock()
	get := func(k string) string {
		if c.Env != nil {
			return c.Env[k]
		}
		v, _ := config.Get(k)
		return v
	}
	pick := func(p *string, k string) string {
		if p != nil {
			return *p
		}
		return get(k)
	}
	ISSUER = strings.TrimRight(py.Strip(pick(c.Issuer, "OIDC_ISSUER")), "/")
	CLIENT_ID = py.Strip(pick(c.ClientID, "OIDC_CLIENT_ID"))
	CLIENT_SECRET = py.Strip(pick(c.ClientSecret, "OIDC_CLIENT_SECRET"))
	pub := ""
	if c.PublicURL != nil && *c.PublicURL != "" {
		pub = *c.PublicURL
	} else if v := get("ARGUS_PUBLIC_URL"); v != "" {
		pub = v
	} else {
		pub = defPublicURL
	}
	PUBLIC_URL = strings.TrimRight(pub, "/")
	REDIRECT_URI, POST_LOGOUT_URI = PUBLIC_URL+"/auth/callback", PUBLIC_URL+"/login"
	CA_FILE = ""
	if c.CAFile != nil && *c.CAFile != "" {
		CA_FILE = *c.CAFile
	} else {
		CA_FILE = get("OIDC_CA_FILE")
	}
	meta, metaRetry, keys, keysAt = nil, 0, map[string]*rsa.PublicKey{}, 0
	client = nil
}

// ConfigureFromEnv is configure() with no arguments.
func ConfigureFromEnv() { Configure(Config{}) }

// ConfigureWith sets explicit values (tests).
func ConfigureWith(issuer, clientID, secret, publicURL, caFile string) {
	Configure(Config{Issuer: strp(issuer), ClientID: strp(clientID), ClientSecret: strp(secret),
		PublicURL: strp(publicURL), CAFile: strp(caFile), Env: map[string]string{}})
}

// Configured is configured().
func Configured() bool { return ISSUER != "" && CLIENT_ID != "" && CLIENT_SECRET != "" }

// --- back channel ----------------------------------------------------------------------------------

func checkURL(u string) (string, error) {
	p := py.URLSplit(u)
	if p.Scheme == "https" && p.Hostname() != "" {
		return u, nil
	}
	if AllowLoopbackHTTP && p.Scheme == "http" && (p.Hostname() == "127.0.0.1" || p.Hostname() == "localhost") {
		return u, nil
	}
	return "", errf("endpoint is not https")
}

func httpClient() (*http.Client, error) {
	if client != nil {
		return client, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if CA_FILE != "" {
		pem, err := os.ReadFile(CA_FILE)
		if err != nil {
			return nil, errf("unreachable (FileNotFoundError)")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errf("unreachable (SSLError)")
		}
		tc.RootCAs = pool
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tc,
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, TLSHandshakeTimeout: 8 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second}
	client = &http.Client{Transport: tr, Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if _, err := checkURL(req.URL.String()); err != nil { // never follow a redirect off https
				return err
			}
			return nil
		}}
	return client, nil
}

// call GETs (or POSTs a form) and returns the JSON object. Errors carry a safe reason only.
func call(u string, form [][2]string) (*py.OMap, error) {
	if _, err := checkURL(u); err != nil {
		return nil, err
	}
	c, err := httpClient()
	if err != nil {
		return nil, err
	}
	var req *http.Request
	if form != nil {
		req, err = http.NewRequest("POST", u, strings.NewReader(py.Urlencode(form)))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequest("GET", u, nil)
	}
	if err != nil {
		return nil, errf("unreachable (ValueError)")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		name := "URLError"
		if errors.As(err, &ue) {
			if ue.Timeout() {
				name = "TimeoutError"
			} else if strings.Contains(ue.Err.Error(), "certificate") || strings.Contains(ue.Err.Error(), "x509") {
				name = "SSLCertVerificationError"
			} else if strings.Contains(ue.Err.Error(), "refused") {
				name = "ConnectionRefusedError"
			}
		}
		return nil, errf("unreachable (%s)", name)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, MAX_BODY+1))
	if resp.StatusCode >= 400 {
		code := ""
		if len(raw) > 4096 {
			raw = raw[:4096]
		}
		if v, err := py.ParseJSON(raw); err == nil {
			if m, ok := v.(*py.OMap); ok {
				code = py.Head(py.Str(m.GetD("error", "")), 40)
			}
		}
		if code != "" {
			return nil, errf("HTTP %d %s", resp.StatusCode, code)
		}
		return nil, errf("HTTP %d", resp.StatusCode)
	}
	if len(raw) > MAX_BODY {
		return nil, errf("response too large")
	}
	v, err := py.ParseJSON(raw)
	if err != nil {
		return nil, errf("response is not JSON")
	}
	m, ok := v.(*py.OMap)
	if !ok {
		return nil, errf("response is not a JSON object")
	}
	return m, nil
}

// Metadata is metadata(): the discovery document, cached; nil while it cannot be fetched (retried every RETRY s).
func Metadata() *py.OMap {
	mu.Lock()
	defer mu.Unlock()
	return metadataLocked()
}

func metadataLocked() *py.OMap {
	if meta != nil || !Configured() {
		return meta
	}
	if Now() < metaRetry {
		return meta
	}
	doc, err := call(ISSUER+"/.well-known/openid-configuration", nil)
	if err == nil {
		if s, _ := doc.Get("issuer").(string); s != ISSUER {
			err = errf("discovery names another issuer")
		}
	}
	if err == nil {
		for _, k := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
			s, _ := doc.Get(k).(string)
			if _, e := checkURL(s); e != nil {
				err = e
				break
			}
		}
	}
	if err == nil && py.Truthy(doc.Get("end_session_endpoint")) {
		s, _ := doc.Get("end_session_endpoint").(string)
		if _, e := checkURL(s); e != nil {
			err = e
		}
	}
	if err != nil {
		metaRetry = Now() + RETRY
		fmt.Printf("oidc: discovery failed for %s: %v\n", ISSUER, err)
		return nil
	}
	meta = doc
	fmt.Printf("oidc: discovery ok for %s\n", ISSUER)
	return meta
}

// Prefetch fetches the discovery document in the background at startup.
func Prefetch() {
	if Configured() {
		go Metadata()
	}
}

// --- JWT / RS256 -----------------------------------------------------------------------------------

// B64u is base64url without padding.
func B64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// B64uDec is b64u_dec: strict base64url (padding optional), Error on anything else.
func B64uDec(s string) ([]byte, error) {
	if s == "" || len(s)%4 == 1 {
		return nil, errf("bad base64")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '=') {
			return nil, errf("bad base64")
		}
	}
	t := strings.TrimRight(s, "=")
	if strings.Contains(t, "=") {
		return nil, errf("bad base64")
	}
	b, err := base64.RawURLEncoding.WithPadding(base64.NoPadding).Strict().DecodeString(t)
	if err != nil {
		// Python's b64decode does not check the unused bits of the last character
		b, err = base64.RawURLEncoding.DecodeString(t)
		if err != nil {
			return nil, errf("bad base64")
		}
	}
	return b, nil
}

// RSAVerify is rsa_verify(n, e, message, signature): RSASSA-PKCS1-v1_5 with SHA-256, n >= 2048 bit, e odd >= 3.
func RSAVerify(n *big.Int, e *big.Int, message, sig []byte) bool {
	k := (n.BitLen() + 7) / 8
	if n.BitLen() < 2048 || e.Cmp(big.NewInt(3)) < 0 || e.Bit(0) == 0 || len(sig) != k || !e.IsInt64() || e.Int64() > math.MaxInt32 {
		return false
	}
	if new(big.Int).SetBytes(sig).Cmp(n) >= 0 {
		return false
	}
	pub := &rsa.PublicKey{N: n, E: int(e.Int64())}
	h := sha256.Sum256(message)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig) == nil
}

func loadKeys() error {
	m := metadataLocked()
	if m == nil {
		return errf("Keycloak is unreachable")
	}
	ju, _ := m.Get("jwks_uri").(string)
	doc, err := call(ju, nil)
	if err != nil {
		return err
	}
	nk := map[string]*rsa.PublicKey{}
	for _, kv := range py.AsList(doc.Get("keys")) {
		k := py.AsMap(kv)
		if k == nil {
			continue
		}
		use := k.GetD("use", "sig")
		alg := k.GetD("alg", "RS256")
		ns, _ := k.Get("n").(string)
		es, _ := k.Get("e").(string)
		if k.Get("kty") == "RSA" && use == "sig" && alg == "RS256" && py.Truthy(k.Get("n")) && py.Truthy(k.Get("e")) {
			nb, err := B64uDec(ns)
			if err != nil {
				return err
			}
			eb, err := B64uDec(es)
			if err != nil {
				return err
			}
			e := new(big.Int).SetBytes(eb)
			if !e.IsInt64() || e.Int64() > math.MaxInt32 {
				continue
			}
			kid := py.Str(k.GetD("kid", ""))
			nk[kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}
		}
	}
	keys, keysAt = nk, Now()
	return nil
}

func key(kidv any) (*rsa.PublicKey, error) {
	kid := py.OrS(kidv, "")
	mu.Lock()
	defer mu.Unlock()
	if _, ok := keys[kid]; !ok && Now()-keysAt > JWKS_RETRY {
		if err := loadKeys(); err != nil {
			return nil, err
		}
	}
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(keys) == 1 {
		for _, k := range keys {
			return k, nil
		}
	}
	return nil, errf("unknown signing key")
}

func isNum(v any) bool {
	switch v.(type) {
	case int64, float64:
		return true
	}
	return false
}

// VerifyIDToken is verify_id_token(token, nonce, now): the claims of a valid id_token, else an Error.
func VerifyIDToken(tokenv any, nonce string, now float64) (*py.OMap, error) {
	token, _ := tokenv.(string)
	parts := strings.Split(token, ".")
	if token == "" || len(parts) != 3 {
		return nil, errf("id_token is not a JWT")
	}
	hb, err := B64uDec(parts[0])
	if err != nil {
		return nil, err
	}
	cb, err := B64uDec(parts[1])
	if err != nil {
		return nil, err
	}
	hv, err1 := py.ParseJSON(hb)
	cv, err2 := py.ParseJSON(cb)
	if err1 != nil || err2 != nil {
		return nil, errf("id_token is not JSON")
	}
	header, ok1 := hv.(*py.OMap)
	claims, ok2 := cv.(*py.OMap)
	if !ok1 || !ok2 {
		return nil, errf("id_token is not JSON")
	}
	if header.Get("alg") != "RS256" {
		return nil, errf("id_token algorithm not allowed")
	}
	pub, err := key(header.Get("kid"))
	if err != nil {
		return nil, err
	}
	sig, err := B64uDec(parts[2])
	if err != nil {
		return nil, err
	}
	if !RSAVerify(pub.N, big.NewInt(int64(pub.E)), []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errf("id_token signature invalid")
	}
	if s, ok := claims.Get("iss").(string); !ok || s != ISSUER {
		return nil, errf("id_token issuer mismatch")
	}
	var aud []any
	switch a := claims.Get("aud").(type) {
	case string:
		aud = []any{a}
	case []any:
		aud = a
	}
	found := false
	for _, a := range aud {
		if s, ok := a.(string); ok && s == CLIENT_ID {
			found = true
		}
	}
	if !found {
		return nil, errf("id_token audience mismatch")
	}
	if claims.Has("azp") || len(aud) > 1 {
		if s, ok := claims.Get("azp").(string); !ok || s != CLIENT_ID {
			return nil, errf("id_token azp mismatch")
		}
	}
	exp, iat := claims.Get("exp"), claims.Get("iat")
	if !isNum(exp) || now > py.Num(exp)+LEEWAY {
		return nil, errf("id_token expired")
	}
	if !isNum(iat) || py.Num(iat) > now+LEEWAY || py.Num(iat) < now-STATE_TTL-LEEWAY {
		return nil, errf("id_token issued at a wrong time")
	}
	if nbf := claims.Get("nbf"); isNum(nbf) && py.Num(nbf) > now+LEEWAY {
		return nil, errf("id_token not valid yet")
	}
	n, ok := claims.Get("nonce").(string)
	if !ok || !hmac.Equal([]byte(n), []byte(nonce)) {
		return nil, errf("id_token nonce mismatch")
	}
	if s, ok := claims.Get("sub").(string); !ok || s == "" {
		return nil, errf("id_token has no subject")
	}
	return claims, nil
}

// --- the flow -------------------------------------------------------------------------------------

// StateHash is state_hash(state).
func StateHash(state string) string {
	s := sha256.Sum256([]byte(state))
	return hex.EncodeToString(s[:])
}

func randURL(n int) string {
	b := make([]byte, n)
	if _, err := randRead(b); err != nil {
		panic(err)
	}
	return B64u(b)
}

// Store keeps a pending login server-side (auth.OIDCPut).
type Store func(stateHash, nonce, verifier, next string) error

// Begin is begin(store, next): (authorization URL, state for the browser cookie).
func Begin(store Store, next string) (string, string, error) { return BeginWith(store, next, false) }

// BeginWith is Begin; reauth asks Keycloak to authenticate again (max_age=0, prompt=login), used before a 2FA
// enrolment by a Keycloak user whose sign-in is not recent.
func BeginWith(store Store, next string, reauth bool) (string, string, error) {
	m := Metadata()
	if m == nil {
		return "", "", errf("Keycloak is unreachable")
	}
	state, nonce, verifier := randURL(32), randURL(32), randURL(48)
	if err := store(StateHash(state), nonce, verifier, next); err != nil {
		return "", "", errf("state could not be stored")
	}
	ch := sha256.Sum256([]byte(verifier))
	q := py.UrlencodeKV("response_type", "code", "client_id", CLIENT_ID, "redirect_uri", REDIRECT_URI, "scope", SCOPE,
		"state", state, "nonce", nonce, "code_challenge_method", "S256", "code_challenge", B64u(ch[:]))
	if reauth {
		q += "&" + py.UrlencodeKV("max_age", "0", "prompt", "login")
	}
	ep, _ := m.Get("authorization_endpoint").(string)
	sep := "?"
	if strings.Contains(ep, "?") {
		sep = "&"
	}
	return ep + sep + q, state, nil
}

// Pending is a row oidc_take returns.
type Pending struct {
	Nonce, Verifier, Next string
	Created               int64
}

// Take returns and deletes the stored row, or nil.
type Take func(stateHash string) *Pending

// Result is what Finish returns.
type Result struct {
	Sub, Username string
	Roles         []string
	IDToken, Next string
	AMR           []string // id_token amr (authentication methods), e.g. ["pwd", "otp"]
	ACR           string   // id_token acr
	AuthTime      int64    // id_token auth_time (0 when absent)
}

// MFA_ACR / MFA_AMR are OIDC_MFA_ACR and OIDC_MFA_AMR (comma-separated): the exact acr value, or amr values,
// that mean Keycloak checked a second factor. Nothing is trusted unless one of them is set explicitly.
var (
	MFA_ACR = py.Strip(setting("OIDC_MFA_ACR"))
	MFA_AMR = splitList(setting("OIDC_MFA_AMR"))
)

func setting(k string) string {
	v, _ := config.Get(k)
	return v
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// MFA says whether the (signature-, iss-, aud-, nonce- and exp-checked) id_token states a second factor that the
// operator configured: acr equal to OIDC_MFA_ACR, or an amr value listed in OIDC_MFA_AMR (exact match), with an
// auth_time no older than maxAge seconds. Argus then trusts it (MFA_TRUST_IDP=1) as of auth_time.
func (r *Result) MFA(maxAge int64, now float64) bool {
	if r.AuthTime <= 0 || float64(r.AuthTime) < now-float64(maxAge) || float64(r.AuthTime) > now+60 {
		return false
	}
	if MFA_ACR != "" && r.ACR == MFA_ACR {
		return true
	}
	for _, m := range r.AMR {
		for _, want := range MFA_AMR {
			if m == want {
				return true
			}
		}
	}
	return false
}

// Finish is finish(params, cookie_state, take).
func Finish(params py.QS, cookieState string, take Take) (*Result, error) {
	state := params.Get("state")
	if state == "" || cookieState == "" || !hmac.Equal([]byte(state), []byte(cookieState)) {
		return nil, errf("state does not match this browser")
	}
	row := take(StateHash(state))
	if row == nil {
		return nil, errf("state unknown or already used")
	}
	if float64(row.Created) < Now()-STATE_TTL {
		return nil, errf("login took too long")
	}
	if e := params.Get("error"); e != "" {
		var b strings.Builder
		for _, ch := range py.Head(e, 40) {
			if py.IsAlnumRune(ch) || ch == '_' || ch == '-' {
				b.WriteRune(ch)
			}
		}
		return nil, errf("Keycloak returned %s", b.String())
	}
	if iss := params.Get("iss"); iss != "" && iss != ISSUER {
		return nil, errf("callback from another issuer")
	}
	code := params.Get("code")
	if code == "" {
		return nil, errf("no authorization code")
	}
	m := Metadata()
	if m == nil {
		return nil, errf("Keycloak is unreachable")
	}
	te, _ := m.Get("token_endpoint").(string)
	tok, err := call(te, [][2]string{{"grant_type", "authorization_code"}, {"code", code}, {"redirect_uri", REDIRECT_URI},
		{"client_id", CLIENT_ID}, {"client_secret", CLIENT_SECRET}, {"code_verifier", row.Verifier}})
	if err != nil {
		return nil, err
	}
	claims, err := VerifyIDToken(tok.Get("id_token"), row.Nonce, Now())
	if err != nil {
		return nil, err
	}
	var roles []string
	switch r := claims.Get(ROLE_CLAIM).(type) {
	case string:
		roles = []string{r}
	case []any:
		for _, x := range r {
			if s, ok := x.(string); ok {
				roles = append(roles, s)
			}
		}
	}
	idt, _ := tok.Get("id_token").(string)
	var amr []string
	if l, ok := claims.Get("amr").([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				amr = append(amr, s)
			}
		}
	}
	acr, _ := claims.Get("acr").(string)
	var at int64
	switch v := claims.Get("auth_time").(type) {
	case int64:
		at = v
	case float64:
		at = int64(v)
	}
	return &Result{Sub: claims.S("sub"), Username: py.OrS(claims.Get("preferred_username"), ""), Roles: roles,
		IDToken: idt, Next: row.Next, AMR: amr, ACR: acr, AuthTime: at}, nil
}

// RoleOf is role_of(roles): "admin", "viewer" or "".
func RoleOf(roles []string) string {
	has := func(r string) bool {
		for _, x := range roles {
			if x == r {
				return true
			}
		}
		return false
	}
	if has("admin") {
		return "admin"
	}
	if has("viewer") {
		return "viewer"
	}
	return ""
}

// LogoutURL is logout_url(id_token): Keycloak's end-session URL, or "" when the issuer has none.
func LogoutURL(idToken string) string {
	m := Metadata()
	ep, _ := m.Get("end_session_endpoint").(string)
	if ep == "" {
		return ""
	}
	pairs := [][2]string{{"post_logout_redirect_uri", POST_LOGOUT_URI}, {"client_id", CLIENT_ID}}
	if idToken != "" {
		pairs = append(pairs, [2]string{"id_token_hint", idToken})
	}
	sep := "?"
	if strings.Contains(ep, "?") {
		sep = "&"
	}
	return ep + sep + py.Urlencode(pairs)
}

var _ = json.Valid

var randRead = rand.Read
