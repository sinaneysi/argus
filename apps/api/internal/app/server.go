package app

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

func init() {
	auth.Hook = SecurityEventsHook // security events to root's audit log and notice (mfa_views.go)
}

type staticFile struct{ rel, ctype string }

var FONTS = []string{"ibm-plex-sans-var-latin.woff2", "jetbrains-mono-var-latin.woff2", "vazirmatn-arabic.woff2"}

// STATIC_FILES is the exact-name whitelist: no request path ever reaches the filesystem.
var STATIC_FILES = func() map[string]staticFile {
	m := map[string]staticFile{
		"/favicon.ico":                 {"favicon.ico", "image/x-icon"},
		"/static/favicon.svg":          {"favicon.svg", "image/svg+xml"},
		"/static/favicon-32.png":       {"favicon-32.png", "image/png"},
		"/static/apple-touch-icon.png": {"apple-touch-icon.png", "image/png"},
		"/apple-touch-icon.png":        {"apple-touch-icon.png", "image/png"},
	}
	for _, f := range FONTS {
		m["/static/fonts/"+f] = staticFile{"fonts/" + f, "font/woff2"}
	}
	return m
}()

// CSP and the other security headers on every response: the API serves JSON, downloads, fonts and icons, never a page.
const CSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

var postLimits = map[string]int64{"/backups/new": 32768, "/admin/fix/review": 65536, "/board/comment": 65536,
	"/board/create": 262144, "/admin/jira-shared": 262144, "/board/agent": 262144, "/board/agent/chat": 65536, "/board/task/secret": 65536, "/admin/backup-targets": 65536,
	"/admin/lessons/act": 32768, "/admin/kube": 262144, "/admin/sources": 32768,
	"/admin/writing-style": 131072}

// Handler serves the app.
type Handler struct{}

type reqCtx struct {
	w        http.ResponseWriter
	r        *http.Request
	c        *Ctx
	url      py.URL
	head     bool
	status   int
	cookies  map[string]string
	cookieOK bool
}

// Out writes the log line of server.py's log_message (an /auth/ query string is redacted).
var Out io.Writer = os.Stdout

func logf(f string, a ...any) { fmt.Fprintf(Out, f+"\n", a...) }

var reRedact = regexp.MustCompile(`(/auth/[a-z]+)\?[^ "]*`)

func (h *reqCtx) send(code int, body []byte, ctype string, headers [][2]string, cache string) {
	if ctype == "" {
		ctype = "text/plain; charset=utf-8"
	}
	if cache == "" {
		cache = "no-store"
	}
	hd := h.w.Header()
	hd.Set("Server", "argus-api")
	hd.Set("Content-Type", ctype)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	hd.Set("Content-Security-Policy", CSP)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "same-origin")
	hd.Set("Cache-Control", cache)
	for _, kv := range headers {
		hd.Add(kv[0], kv[1])
	}
	h.w.WriteHeader(code)
	h.status = code
	if !h.head {
		h.w.Write(body)
	}
}

func (h *reqCtx) redirect(loc string, headers ...[2]string) {
	h.send(303, nil, "", append([][2]string{{"Location", loc}}, headers...), "")
}

// TRUSTED_PROXIES may set X-Real-IP (OPS_TRUSTED_PROXIES; the stage edges and loopback). From any other peer
// the header is ignored, so a client cannot pick the address the lockout counts against.
var TRUSTED_PROXIES = func() map[string]bool {
	m := map[string]bool{}
	for _, x := range strings.Split(env("ARGUS_TRUSTED_PROXIES", defTrustedProxies), ",") {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return m
}()

func (h *reqCtx) ip() string {
	host, _, err := net.SplitHostPort(h.r.RemoteAddr)
	if err != nil {
		host = h.r.RemoteAddr
	}
	if v := h.r.Header.Get("X-Real-IP"); v != "" && TRUSTED_PROXIES[host] {
		return v
	}
	return host
}

func (h *reqCtx) cookie(name string) string {
	if !h.cookieOK {
		c, err := py.ParseCookies(strings.Join(h.r.Header.Values("Cookie"), "; "))
		if err != nil {
			c = map[string]string{} // Python raised CookieError here (no response); no cookies is the safe reading
		}
		h.cookies, h.cookieOK = c, true
	}
	return h.cookies[name]
}

func (h *reqCtx) token() string { return h.cookie(auth.COOKIE) }

func (h *reqCtx) notFound() { h.apiFail(&apiErr{404, "not found"}) }

// gate is the session of a download route, or nil after a redirect to the web app's sign-in, account or 2FA page.
func (h *reqCtx) gate() *Session {
	h.c.Sess = auth.GetSession(h.token())
	if h.c.Sess == nil {
		h.redirect("/login?" + py.UrlencodeKV("next", h.r.RequestURI))
		return nil
	}
	h.c.Token = h.token()
	auth.TouchClient(h.c.Token, h.ip(), h.ua())
	if h.c.Sess.MustChange != 0 {
		h.redirect("/account")
		return nil
	}
	if mfaEnrolPending(h.c.Sess) { // "Require 2FA for all users": until enrolled, nothing else
		h.redirect("/account/2fa?" + py.UrlencodeKV("next", SafeNext(h.r.RequestURI)))
		return nil
	}
	return h.c.Sess
}

func (Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h := &reqCtx{w: w, r: r, head: r.Method == "HEAD"}
	h.url = py.URLSplit(r.RequestURI)
	h.c = &Ctx{Theme: ThemeOf(strings.Join(r.Header.Values("Cookie"), "; ")), IP: h.ip(), Path: r.RequestURI}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("error on %s: %v\n%s", h.url.Path, rec, debug.Stack())
			if h.status == 0 {
				h.apiFail(&apiErr{500, "internal error"})
			}
		}
		user := "-"
		if h.c.Sess != nil {
			user = h.c.Sess.Username
		}
		line := reRedact.ReplaceAllString(fmt.Sprintf(`"%s %s %s" %d -`, r.Method, r.RequestURI, r.Proto, h.status), "${1}?[redacted]")
		logf("%s %s %s %dms", h.ip(), user, line, time.Since(start).Milliseconds())
	}()
	switch r.Method {
	case "GET", "HEAD":
		h.doGET()
	case "POST":
		h.doPOST()
	default:
		h.send(501, []byte("Unsupported method\n"), "text/plain", nil, "")
	}
}

func (h *reqCtx) doGET() {
	path := h.url.Path
	if path == "/healthz" {
		h.send(200, []byte("ok\n"), "text/plain", nil, "")
		return
	}
	if sf, ok := STATIC_FILES[path]; ok {
		data, err := readStatic(sf.rel)
		if err != nil {
			panic(err)
		}
		h.send(200, data, sf.ctype, nil, "public, max-age=31536000, immutable")
		return
	}
	if strings.HasPrefix(path, "/api/") { // the JSON API (api.go): JSON errors, never a redirect
		h.apiGET(path)
		return
	}
	qs := py.ParseQS(h.url.Query)
	switch path {
	case "/auth/login", "/auth/callback":
		if !oidc.Configured() {
			h.notFound()
		} else if path == "/auth/login" {
			h.ssoLogin(qs)
		} else {
			h.ssoCallback(qs)
		}
	case "/cicd/summary.json", "/inventory.csv", "/rca/prompt.md", "/ideas/prompt.md":
		h.download(path, qs)
	default:
		h.notFound()
	}
}

// download serves the files the web app links to, with the session gate of a browser link (the summary is read by
// scripts: 401, no redirect).
func (h *reqCtx) download(path string, qs py.QS) {
	if path == "/cicd/summary.json" && auth.GetSession(h.token()) == nil {
		h.apiFail(&apiErr{401, "login required"})
		return
	}
	if h.gate() == nil {
		return
	}
	switch path {
	case "/cicd/summary.json":
		h.send(200, []byte(CicdSummaryJSON(qs)), "application/json; charset=utf-8", nil, "")
	case "/inventory.csv":
		h.send(200, []byte(InventoryCSV(qs)), "text/csv; charset=utf-8",
			[][2]string{{"Content-Disposition", `attachment; filename="argus-inventory.csv"`}}, "")
	case "/rca/prompt.md":
		_, data, hdrs := FixDownloadResponse(qs)
		if data == nil {
			h.notFound()
			return
		}
		h.send(200, data, "text/markdown; charset=utf-8", hdrs, "private, no-store")
	case "/ideas/prompt.md":
		name, text, ok := IdeasDownload(qs)
		if !ok {
			h.notFound()
			return
		}
		h.send(200, []byte(strings.TrimRight(text, "\n")+"\n"), "text/markdown; charset=utf-8",
			[][2]string{{"Content-Disposition", `attachment; filename="` + name + `"`}}, "private, no-store")
	}
}

func (h *reqCtx) ssoLogin(qs py.QS) {
	nxt := SafeNext(qs.GetD("next", "/"))
	remember := qs.Get("remember") == "1"
	store := func(sh, nonce, verifier, next string) error {
		if err := auth.OIDCPut(sh, nonce, verifier, next); err != nil {
			return err
		}
		auth.OIDCPutRemember(sh, remember)
		return nil
	}
	u, state, err := oidc.BeginWith(store, nxt, qs.Get("reauth") == "1")
	if err != nil {
		logf("login via keycloak unavailable: %v ip=%s", err, h.ip())
		h.redirect("/login?sso=unavailable&" + py.UrlencodeKV("next", nxt))
		return
	}
	ck := oidc.COOKIE + "=" + state + "; Path=/auth; HttpOnly; Secure; SameSite=Lax; Max-Age=" + strconv.Itoa(oidc.STATE_TTL)
	h.send(302, nil, "", [][2]string{{"Location", u}, {"Set-Cookie", ck}}, "")
}

func (h *reqCtx) ssoCallback(qs py.QS) {
	clear := [2]string{"Set-Cookie", oidc.COOKIE + "=; Path=/auth; Max-Age=0; HttpOnly; Secure; SameSite=Lax"}
	take := func(sh string) *oidc.Pending {
		st := auth.OIDCTake(sh)
		if st == nil {
			return nil
		}
		return &oidc.Pending{Nonce: st.Nonce, Verifier: st.Verifier, Next: st.Next, Created: st.Created}
	}
	// refused sends the browser to the web app's sign-in page, which says why (app/login: the code, never free text).
	refused := func(code string) { h.redirect("/login?sso="+code, clear) }
	r, err := oidc.Finish(qs, h.cookie(oidc.COOKIE), take)
	if err != nil {
		logf("login via keycloak FAILED: %v ip=%s", err, h.ip())
		refused("incomplete")
		return
	}
	user, role := r.Username, oidc.RoleOf(r.Roles)
	if !USERNAME.MatchString(user) {
		logf("login via keycloak DENIED: unusable username %s ip=%s", py.Repr(py.Head(user, 40)), h.ip())
		refused("username")
		return
	}
	if role == "" {
		logf("login via keycloak DENIED: user=%s has no argus role ip=%s", py.Repr(user), h.ip())
		refused("norole")
		return
	}
	remember := auth.OIDCTakeRemember(oidc.StateHash(qs.Get("state")))
	o := auth.Opts{IDToken: r.IDToken, Remember: remember, IP: h.ip(), UA: h.ua(), AuthAt: r.AuthTime}
	idp := auth.MFATrustIDP && r.MFA(auth.MFAStepup, Now())
	if idp {
		o.MFAAt, o.MFAVia = r.AuthTime, "idp" // as of Keycloak's authentication, never later
	}
	if old := h.token(); old != "" {
		auth.Logout(old) // a new token at every sign-in
	}
	if !idp && auth.Enabled(user) { // Argus's own code after Keycloak
		if _, why := auth.SSOLoginWith(user, r.Sub, role, BREAK_GLASS, o, true); why != "" {
			logf("login via keycloak DENIED: user=%s %s ip=%s", py.Repr(user), why, h.ip())
			refused("denied")
			return
		}
		auth.DropPending(h.cookie(MFA_COOKIE)) // this browser's earlier pending login, never another browser's
		pt, err := auth.NewPendingAt(user, r.Next, "keycloak", remember, r.IDToken, r.AuthTime)
		if err != nil {
			refused("unsaved")
			return
		}
		logf("login via keycloak ok user=%s role=%s ip=%s (2fa next)", py.Repr(user), role, h.ip())
		h.redirect("/login?step=code", [2]string{"Set-Cookie", mfaCookie(pt)}, clear)
		return
	}
	token, why := auth.SSOLoginWith(user, r.Sub, role, BREAK_GLASS, o, false)
	if token == "" {
		logf("login via keycloak DENIED: user=%s %s ip=%s", py.Repr(user), why, h.ip())
		refused("denied")
		return
	}
	logf("login via keycloak ok user=%s role=%s ip=%s%s", py.Repr(user), role, h.ip(), If(idp, " (keycloak 2fa)", ""))
	h.redirect(enrolFirst(user, TodayLanding(r.Next)), [2]string{"Set-Cookie", apiSessionCookie(token, maxAgeFor(remember))}, clear)
}

func hmacEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// doPOST: every write goes through the JSON API (api.go); there is no other POST route.
func (h *reqCtx) doPOST() {
	if path := h.url.Path; strings.HasPrefix(path, "/api/") {
		h.apiPOST(path)
		return
	}
	h.apiFail(&apiErr{404, "no such endpoint"})
}

// --- static files ------------------------------------------------------------------------------------

var staticCache = map[string][]byte{}

func readStatic(rel string) ([]byte, error) {
	if b, ok := staticCache[rel]; ok {
		return b, nil
	}
	return os.ReadFile(filepath.Join(STATIC, rel))
}

// PreloadStatic reads every whitelisted file once (they never change while the process runs).
func PreloadStatic() {
	for _, sf := range STATIC_FILES {
		if b, err := os.ReadFile(filepath.Join(STATIC, sf.rel)); err == nil {
			staticCache[sf.rel] = b
		}
	}
}
