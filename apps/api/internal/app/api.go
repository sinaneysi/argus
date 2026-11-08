package app

// api.go: the /api/v1 JSON API (ADR-0018: the Next.js frontend in apps/web renders every page; Go renders none). It
// reuses the data functions and the write handlers; nothing here has business logic of its own.
//
//	401 {"error": "login required"}                         no session
//	403 {"error": "password change required"}               the account must change its password first
//	403 {"error": "two-factor enrolment required"}          "2FA for all users" or an admin's 2FA reset, and no 2FA yet
//	403 {"error": "cross-site request refused"}             POST with a foreign Origin
//	403 {"error": "Disabled in the demo"}                   any write with DEMO_MODE=1 (sign-in, sign-out, theme work)
//	403 {"error": "csrf token missing or wrong"}            POST without a valid token (below)
//	403 {"error": "two-factor authentication required", "mfa": "enrol"|"stepup"}   POST_ROUTES policy (mfa_views.go)
//	403 {"error": "admins only"}, 400 {"error": ...}, 404, 405, 413
//
// CSRF: X-CSRF-Token (or a "csrf" field) equal to the argus_csrf cookie (double submit; GET /api/v1/csrf sets it, the
// frontend's middleware too) or to the session's own token (GET /api/v1/session). A POST body is JSON
// (Content-Type application/json) or a form, at most 8 KB.
//
// Sign-in (auth/login, auth/mfa), sign-out and the theme are exempt from 2FA and allowed in the demo. Every other write
// is in apiIsPost and has its own policy in POST_ROUTES; review/act also passes the policy of the decision kind's write
// (apiReviewPolicyRoute).

import (
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/brand"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

const apiMaxBody = 8192

// CSRF_COOKIE is the double-submit cookie: readable by the page (not HttpOnly), SameSite=Strict.
const CSRF_COOKIE = "argus_csrf"

var reCSRFTok = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// apiErr is an error answer: HTTP status and a plain message.
type apiErr struct {
	code int
	msg  string
}

type apiGetFn func(py.QS, *Session) (any, *apiErr)

// apiGETs are the read endpoints (any signed-in user).
var apiGETs = map[string]apiGetFn{
	"/api/v1/session":                    apiSessionJSON,
	"/api/v1/nav":                        apiNavJSON,
	"/api/v1/status":                     apiStatusJSON,
	"/api/v1/today":                      apiTodayJSON,
	"/api/v1/review":                     apiReviewJSON,
	"/api/v1/review/item":                apiReviewItemJSON,
	"/api/v1/admin/backup-targets":       apiAdminTargetsJSON,
	"/api/v1/admin/credentials":          apiAdminCredsJSON,
	"/api/v1/admin/outline":              apiAdminOutlineJSON,
	"/api/v1/admin/outline/page":         apiOutlinePageJSON,
	"/api/v1/sources/catalogue":          catSummaryJSON,
	"/api/v1/sources/catalogue/routes":   catRoutesJSON,
	"/api/v1/sources/catalogue/unmapped": catUnmappedJSON,
	"/api/v1/sources/catalogue/runs":     catRunsJSON,
	"/api/v1/sources/catalogue/action":   catActionJSON,
	"/api/v1/sources/find":               sourcesFindJSON,
	"/api/v1/incidents":                  apiIncidentsJSON,
	"/api/v1/incidents/item":             apiIncidentItemJSON,
	"/api/v1/incidents/findings":         apiFindingsJSON,
	"/api/v1/incidents/run":              apiIncidentRunJSON,
	"/api/v1/incidents/finding":          apiFindingJSON,
	"/api/v1/incidents/report":           apiRcaFileJSON,
	"/api/v1/alerts":                     apiAlertsJSON,
	"/api/v1/problems":                   apiProblemsJSON,
	"/api/v1/problems/item":              apiProblemItemJSON,
	"/api/v1/rca":                        apiRcaJSON,
	"/api/v1/rca/item":                   apiRcaItemJSON,
	"/api/v1/inventory":                  apiInventoryJSON,
	"/api/v1/inventory/item":             apiInventoryItemJSON,
	"/api/v1/capacity":                   apiCapacityJSON,
	"/api/v1/certs":                      apiCertsJSON,
	"/api/v1/hygiene":                    apiHygieneJSON,
	"/api/v1/seo":                        apiSeoJSON,
	"/api/v1/checks":                     apiChecksJSON,
	"/api/v1/backups":                    apiBackupsJSON,
	"/api/v1/backups/set":                apiBackupSetJSON,
	"/api/v1/backups/new":                apiBackupNewJSON,
	"/api/v1/backups/plan":               apiBackupPlanJSON,
	"/api/v1/account/stepup":             apiStepupJSON,
	"/api/v1/board":                      apiBoardJSON,
	"/api/v1/board/task":                 apiBoardTaskJSON,
	"/api/v1/board/new":                  apiBoardNewJSON,
	"/api/v1/board/ask":                  apiBoardAskJSON,
	"/api/v1/qa":                         apiQaJSON,
	"/api/v1/cicd":                       apiCicdJSON,
	"/api/v1/cicd/failures":              apiCicdFailuresJSON,
	"/api/v1/cicd/stuck":                 apiCicdStuckJSON,
	"/api/v1/cicd/audit":                 apiCicdAuditJSON,
	"/api/v1/cicd/project":               apiCicdProjectJSON,
	"/api/v1/cicd/finding":               apiCicdFindingJSON,
	"/api/v1/cicd/repo":                  apiRepoJSON,
	"/api/v1/vulns":                      apiVulnsJSON,
	"/api/v1/vulns/group":                apiVulnGroupJSON,
	"/api/v1/runs":                       apiRunsJSON,
	"/api/v1/runs/item":                  apiRunJSON,
	"/api/v1/knowledge":                  apiKnowledgeJSON,
	"/api/v1/fix":                        apiFixListJSON,
	"/api/v1/fix/run":                    apiFixRunJSON,
	"/api/v1/fix/review":                 apiFixReviewJSON,
	"/api/v1/ideas":                      apiIdeasJSON,
	"/api/v1/proposals":                  apiProposalsJSON,
	"/api/v1/admin":                      apiAdminJSON,
	"/api/v1/admin/users":                apiUsersJSON,
	"/api/v1/admin/board":                apiAdminBoardJSON,
	"/api/v1/admin/claude":               apiAdminClaudeJSON,
	"/api/v1/admin/kube":                 apiAdminKubeJSON,
	"/api/v1/admin/sources":              apiAdminSourcesJSON,
	"/api/v1/lessons":                    apiLessonsJSON,
	"/api/v1/suggestions":                apiSuggestionsJSON,
	"/api/v1/replies":                    apiRepliesJSON,
	"/api/v1/help":                       apiHelpJSON,
	"/api/v1/help/page":                  apiHelpPageJSON,
	"/api/v1/help/all":                   apiHelpAllJSON,
}

// apiOpenGETs answer without a session (or, /me, for a session that must still change its password or enrol 2FA).
var apiOpenGETs = map[string]func(*reqCtx){
	"/api/v1/health":       func(h *reqCtx) { h.apiJSON(200, map[string]any{"ok": true}) },
	"/api/v1/csrf":         (*reqCtx).apiCSRF,
	"/api/v1/auth/options": (*reqCtx).apiAuthOptions,
	"/api/v1/auth/mfa":     (*reqCtx).apiMFAPending,
	"/api/v1/me":           (*reqCtx).apiMe,
	"/api/v1/account":      (*reqCtx).apiAccount,
	"/api/v1/account/2fa":  (*reqCtx).apiAccount2FA,
}

// apiAuthPOSTs: sign-in, sign-out and theme (no session needed; the page twin's policy: exempt, allowed in the demo).
var apiAuthPOSTs = map[string]func(*reqCtx, map[string]any){
	"/api/v1/auth/login":  (*reqCtx).apiLogin,
	"/api/v1/auth/mfa":    (*reqCtx).apiLoginMFA,
	"/api/v1/auth/logout": (*reqCtx).apiLogout,
	"/api/v1/theme":       (*reqCtx).apiTheme,
}

type apiPage struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Per   int `json:"per"`
	Pages int `json:"pages"`
}

// apiPaging reads ?page= and ?per= (1..200, default 50) and clamps the page: (bounds, from, to).
func apiPaging(qs py.QS, total int) (apiPage, int, int) {
	per, _ := strconv.Atoi(qs.Get("per"))
	if per < 1 || per > 200 {
		per = 50
	}
	page, _ := strconv.Atoi(qs.Get("page"))
	pages := max(1, (total+per-1)/per)
	page = min(max(page, 1), pages)
	from := (page - 1) * per
	return apiPage{total, page, per, pages}, min(from, total), min(from+per, total)
}

func apiSessionJSON(_ py.QS, sess *Session) (any, *apiErr) {
	return map[string]any{"user": sess.Username, "role": sess.Role, "csrf": sess.CSRF, "mfa_at": sess.MFAAt, "demo": DEMO}, nil
}

func (h *reqCtx) apiJSON(code int, v any, headers ...[2]string) {
	b, err := json.Marshal(v)
	if err != nil {
		code, b = 500, []byte(`{"error":"could not encode the answer"}`)
	}
	h.send(code, append(b, '\n'), "application/json; charset=utf-8", headers, "no-store")
}

func (h *reqCtx) apiFail(e *apiErr, extra ...any) {
	m := map[string]any{"error": e.msg}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	h.apiJSON(e.code, m)
}

// mfaEnrolPending (the gate's rule): an admin reset this user's 2FA and they have not set up a new one, whatever
// the sign-in (a trusted Keycloak second factor too); or "Require 2FA for all users" is on and they have none.
func mfaEnrolPending(sess *Session) bool {
	if auth.ReenrolDue(sess.Username) {
		return true
	}
	return auth.MFALoginRequired() && sess.MFAVia != "idp" && !auth.Enabled(sess.Username)
}

// enrolFirst: where a sign-in goes next; straight to the 2FA setup for an account an admin reset.
func enrolFirst(user, next string) string {
	if auth.ReenrolDue(user) {
		return "/account/2fa?" + py.UrlencodeKV("next", next)
	}
	return next
}

// apiSession is the session, or nil after a JSON 401/403.
func (h *reqCtx) apiSession() *Session {
	sess := auth.GetSession(h.token())
	h.c.Sess = sess
	if sess == nil {
		h.apiFail(&apiErr{401, "login required"})
		return nil
	}
	h.c.Token = h.token()
	auth.TouchClient(h.c.Token, h.ip(), h.ua())
	must, enrol := apiGateAllows(h.url.Path)
	if sess.MustChange != 0 && !must {
		h.apiFail(&apiErr{403, "password change required"}, "next", "/account")
		return nil
	}
	if mfaEnrolPending(sess) && !enrol {
		h.apiFail(&apiErr{403, "two-factor enrolment required"}, "mfa", "enrol", "next", "/account/2fa")
		return nil
	}
	return sess
}

func (h *reqCtx) apiGET(path string) {
	if fn, ok := apiOpenGETs[path]; ok {
		fn(h)
		return
	}
	fn, ok := apiGETs[path]
	if ok && routeOff(path) { // an optional module that is off (ADR-0021 D6)
		h.apiFail(&apiErr{404, "no such endpoint"})
		return
	}
	if !ok {
		if apiIsPost(path) || apiAuthPOSTs[path] != nil {
			h.apiFail(&apiErr{405, "use POST"})
			return
		}
		h.apiFail(&apiErr{404, "no such endpoint"})
		return
	}
	sess := h.apiSession()
	if sess == nil {
		return
	}
	v, e := fn(py.ParseQS(h.url.Query), sess)
	if e != nil {
		h.apiFail(e)
		return
	}
	h.apiJSON(200, v)
}

// apiIsPost: the signed-in write endpoints (each has a policy in POST_ROUTES).
func apiIsPost(path string) bool {
	switch path {
	case "/api/v1/sources/catalogue/routes/confirm", "/api/v1/sources/catalogue/routes/reject",
		"/api/v1/sources/catalogue/routes/edit", "/api/v1/sources/catalogue/discover",
		"/api/v1/today/act", "/api/v1/review/act", "/api/v1/sources/review/act",
		"/api/v1/incidents/act", "/api/v1/incidents/deep", "/api/v1/problems/act",
		"/api/v1/scan", "/api/v1/inventory/refresh", "/api/v1/inventory/analyse", "/api/v1/capacity/analyse",
		"/api/v1/backups/new", "/api/v1/backups/restore", "/api/v1/backups/install", "/api/v1/backups/override",
		"/api/v1/account/stepup", "/api/v1/cicd/repo/check", "/api/v1/vulns/suppress", "/api/v1/seo/send",
		"/api/v1/board/create", "/api/v1/board/comment", "/api/v1/board/move", "/api/v1/board/assign", "/api/v1/board/fixer",
		"/api/v1/board/jmove", "/api/v1/board/refresh", "/api/v1/board/agent", "/api/v1/board/agent/spinup", "/api/v1/board/agent/chat",
		"/api/v1/board/task/secret", "/api/v1/board/mxpreview", "/api/v1/fix/review", "/api/v1/ideas/act", "/api/v1/ideas/generate",
		"/api/v1/proposals/act", "/api/v1/proposals/settings", "/api/v1/lessons/act", "/api/v1/lessons/settings",
		"/api/v1/admin/save", "/api/v1/admin/claude", "/api/v1/admin/modules", "/api/v1/admin/users/act", "/api/v1/admin/users/matrix",
		"/api/v1/admin/board", "/api/v1/admin/jira-hosts", "/api/v1/admin/jira-shared", "/api/v1/admin/agent", "/api/v1/admin/agent/fixer-move",
		"/api/v1/admin/claude-token", "/api/v1/admin/claude-egress", "/api/v1/admin/kube", "/api/v1/admin/sources",
		"/api/v1/admin/backup-targets", "/api/v1/admin/outline", "/api/v1/admin/gitlab-issues",
		"/api/v1/account", "/api/v1/account/jira", "/api/v1/account/matrix", "/api/v1/account/sessions",
		"/api/v1/account/2fa/begin", "/api/v1/account/2fa/confirm", "/api/v1/account/2fa/disable", "/api/v1/account/2fa/recovery":
		return true
	}
	return false
}

// csrfOK: the token (X-CSRF-Token, else the "csrf" field) is the double-submit cookie's value or the session's token.
func (h *reqCtx) csrfOK(body map[string]any, sess *Session) bool {
	tok := h.r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok, _ = body["csrf"].(string)
	}
	if tok == "" {
		return false
	}
	if ck := h.cookie(CSRF_COOKIE); reCSRFTok.MatchString(ck) && hmacEq(tok, ck) {
		return true
	}
	return sess != nil && hmacEq(tok, sess.CSRF)
}

func (h *reqCtx) apiPOST(path string) {
	if routeOff(path) { // an optional module that is off (ADR-0021 D6)
		h.apiFail(&apiErr{404, "no such endpoint"})
		return
	}
	open := apiAuthPOSTs[path]
	if !apiIsPost(path) && open == nil {
		if _, ok := apiGETs[path]; ok || apiOpenGETs[path] != nil {
			h.apiFail(&apiErr{405, "use GET"})
			return
		}
		h.apiFail(&apiErr{404, "no such endpoint"})
		return
	}
	if origin := h.r.Header.Get("Origin"); origin != "" && py.URLSplit(origin).Netloc != h.r.Host {
		h.apiFail(&apiErr{403, "cross-site request refused"})
		return
	}
	limit := apiBodyLimit(path)
	if h.r.ContentLength > limit {
		h.apiFail(&apiErr{413, "request too large"})
		return
	}
	buf, _ := io.ReadAll(io.LimitReader(h.r.Body, limit+1))
	if int64(len(buf)) > limit {
		h.apiFail(&apiErr{413, "request too large"})
		return
	}
	if open != nil {
		body, ok := apiBody(h.r.Header.Get("Content-Type"), buf)
		if !ok {
			h.apiFail(&apiErr{400, "the body is not a JSON object"})
			return
		}
		if !h.csrfOK(body, auth.GetSession(h.token())) {
			h.apiFail(&apiErr{403, "csrf token missing or wrong"})
			return
		}
		open(h, body)
		return
	}
	sess := h.apiSession()
	if sess == nil {
		return
	}
	if DemoBlocksPost(path) {
		logf("demo: refused POST %s user=%s", path, sess.Username)
		h.apiFail(&apiErr{403, "Disabled in the demo"})
		return
	}
	body, ok := apiBody(h.r.Header.Get("Content-Type"), buf)
	if !ok {
		h.apiFail(&apiErr{400, "the body is not a JSON object"})
		return
	}
	if !h.csrfOK(body, sess) {
		h.apiFail(&apiErr{403, "csrf token missing or wrong"})
		return
	}
	if !h.apiMFA(path, sess) {
		return
	}
	var v any
	var e *apiErr
	switch path {
	case "/api/v1/today/act":
		v, e = apiResult(TodayPostAct(&Ctx{Sess: sess, Theme: "auto", Path: "/today"}, apiQS(body), sess))
	case "/api/v1/review/act":
		v, e = h.apiReviewAct(body, sess)
	case "/api/v1/incidents/act", "/api/v1/incidents/deep", "/api/v1/problems/act":
		v, e = incPost(path, body, sess)
	case "/api/v1/scan", "/api/v1/inventory/refresh", "/api/v1/inventory/analyse", "/api/v1/capacity/analyse",
		"/api/v1/backups/new", "/api/v1/backups/restore", "/api/v1/backups/install", "/api/v1/backups/override":
		v, e = infraPost(h, path, body, sess)
	case "/api/v1/account/stepup":
		v, e = h.apiStepup(body, sess)
	case "/api/v1/account", "/api/v1/account/sessions", "/api/v1/account/2fa/begin", "/api/v1/account/2fa/confirm", "/api/v1/account/2fa/disable", "/api/v1/account/2fa/recovery":
		v, e = h.accountPost(path, body, sess)
	case "/api/v1/account/jira", "/api/v1/account/matrix":
		v, e = h.settingsPost(strings.TrimPrefix(path, "/api/v1"), "/account", body, sess, func(c *Ctx, route string, form py.QS) (int, string) { return BoardPost(c, route, form, sess) })
	case "/api/v1/cicd/repo/check", "/api/v1/vulns/suppress":
		v, e = deliveryPost(path, body, sess)
	case "/api/v1/board/create", "/api/v1/board/comment", "/api/v1/board/move", "/api/v1/board/assign", "/api/v1/board/fixer",
		"/api/v1/board/jmove", "/api/v1/board/refresh", "/api/v1/board/agent", "/api/v1/board/agent/spinup", "/api/v1/board/agent/chat",
		"/api/v1/board/task/secret", "/api/v1/board/mxpreview":
		v, e = boardAPIPost(path, body, sess)
	case "/api/v1/fix/review":
		v, e = apiFixReview(body, sess)
	case "/api/v1/seo/send":
		v, e = seoSend(sess)
	case "/api/v1/ideas/act", "/api/v1/ideas/generate":
		v, e = ideasAPIPost(path, body, sess)
	case "/api/v1/proposals/act", "/api/v1/proposals/settings":
		v, e = proposalsAPIPost(path, body, sess)
	case "/api/v1/lessons/act", "/api/v1/lessons/settings":
		v, e = lessonsAPIPost(path, body, sess)
	case "/api/v1/admin/save", "/api/v1/admin/claude", "/api/v1/admin/modules", "/api/v1/admin/users/act", "/api/v1/admin/users/matrix":
		v, e = h.adminPost(path, body, sess)
	case "/api/v1/admin/board", "/api/v1/admin/jira-hosts", "/api/v1/admin/jira-shared", "/api/v1/admin/agent", "/api/v1/admin/agent/fixer-move":
		v, e = h.settingsPost(strings.TrimPrefix(path, "/api/v1"), "/admin/board", body, sess, func(c *Ctx, route string, form py.QS) (int, string) { return BoardPost(c, route, form, sess) })
	case "/api/v1/admin/sources":
		v, e = h.settingsPost("/admin/sources", "/admin/sources", body, sess, func(c *Ctx, _ string, form py.QS) (int, string) { return SourcesPost(c, form, sess) })
	case "/api/v1/admin/backup-targets":
		v, e = h.settingsPost("/admin/backup-targets", "/admin/backup-targets", body, sess, func(c *Ctx, route string, form py.QS) (int, string) { return BackupPost(c, route, form, sess) })
	case "/api/v1/admin/outline":
		v, e = h.settingsPost("/admin/outline", "/admin/outline", body, sess, func(c *Ctx, _ string, form py.QS) (int, string) { return OutlinePost(c, form, sess) })
	case "/api/v1/admin/gitlab-issues":
		v, e = h.settingsPost("/admin/gitlab-issues", "/admin/outline", body, sess, func(c *Ctx, _ string, form py.QS) (int, string) { return GitLabPost(c, form, sess) })
	case "/api/v1/admin/kube":
		v, e = h.settingsPost("/admin/kube", "/admin/kube", body, sess, func(c *Ctx, _ string, form py.QS) (int, string) { return KubePost(c, form, sess) })
	case "/api/v1/admin/claude-token", "/api/v1/admin/claude-egress":
		v, e = h.settingsPost(strings.TrimPrefix(path, "/api/v1"), "/admin/claude", body, sess, func(c *Ctx, route string, form py.QS) (int, string) {
			if route == "/admin/claude-token" {
				return ClaudeTokenPost(c, form, sess)
			}
			return ClaudeEgressPost(c, form, sess)
		})
	default:
		if v, e = catPost(path, body, sess); e == nil {
			h.apiJSON(202, v)
			return
		}
	}
	switch {
	case e != nil:
		h.apiFail(e)
	case v != nil: // nil, nil: the handler has answered (a 2FA step)
		h.apiJSON(200, v)
	}
}

// apiMFA is the 2FA middleware for a route (POST_ROUTES policy); false has answered 403.
func (h *reqCtx) apiMFA(route string, sess *Session) bool {
	if st := MFAState(sess, PolicyOf(route, sess)); st != "" {
		logf("2fa required: %s user=%s (%s)", route, sess.Username, st)
		h.apiFail(&apiErr{403, "two-factor authentication required"}, "mfa", st, "next", "/account/2fa", "sensitive", PolicyOf(route, sess) == PolicySensitive)
		return false
	}
	return true
}

// apiBody is the POST body as a map: JSON (Content-Type application/json) or a form (each field's first value).
func apiBody(ctype string, buf []byte) (map[string]any, bool) {
	if strings.HasPrefix(strings.ToLower(ctype), "application/json") {
		if t := strings.TrimSpace(string(buf)); t == "" || t == "null" {
			return map[string]any{}, true
		}
		var m map[string]any
		if json.Unmarshal(buf, &m) != nil || m == nil {
			return nil, false
		}
		return m, true
	}
	out := map[string]any{}
	form := py.ParseQS(py.ToValidUTF8(buf))
	for _, k := range form.Keys() {
		out[k] = form.Get(k)
	}
	return out, true
}

// apiQS is a body as the form a page handler reads (strings, numbers, booleans as 1/0; anything else dropped).
func apiQS(body map[string]any) py.QS {
	q := py.NewQS()
	for k, v := range body {
		switch x := v.(type) {
		case string:
			q.Add(k, x)
		case float64:
			q.Add(k, strconv.FormatFloat(x, 'f', -1, 64))
		case bool:
			q.Add(k, If(x, "1", "0"))
		}
	}
	return q
}

func bodyStr(body map[string]any, k string) string {
	switch x := body[k].(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// apiResult turns a handler's (code, out) into JSON: 303 is success with the page to show next; anything else is a
// refusal in the handler's own words.
func apiResult(code int, out string) (any, *apiErr) {
	if code == 303 {
		return map[string]any{"ok": true, "redirect": SafeNext(out)}, nil
	}
	if code < 400 {
		code = 400
	}
	return nil, &apiErr{code, py.OrS(strings.TrimSpace(out), "Not saved")}
}

var (
	reTags  = regexp.MustCompile(`<[^>]*>`)
	reSpace = regexp.MustCompile(`\s+`)
	reHref  = regexp.MustCompile(`href="([^"]*)"`)
)

// Text is the visible text of an HTML fragment: tags dropped, entities decoded, white space collapsed.
func Text(html string) string {
	s := reTags.ReplaceAllString(html, " ")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&#34;", `"`,
		"&nbsp;", " ").Replace(s)
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	return strings.NewReplacer(" ,", ",", " .", ".", "( ", "(", " )", ")").Replace(s)
}

// firstHref is the first local link of an HTML fragment ("" when none).
func firstHref(html string) string {
	if m := reHref.FindStringSubmatch(html); m != nil {
		h := strings.ReplaceAll(m[1], "&amp;", "&")
		if strings.HasPrefix(h, "/") && !strings.HasPrefix(h, "//") {
			return h
		}
	}
	return ""
}

// --- sign-in, sign-out, theme ----------------------------------------------------------------------------------

// apiCSRF sets the double-submit cookie when it is missing and returns its value.
func (h *reqCtx) apiCSRF() {
	tok := h.cookie(CSRF_COOKIE)
	var hdr [][2]string
	if !reCSRFTok.MatchString(tok) {
		tok = auth.TokenHex(24)
		hdr = append(hdr, [2]string{"Set-Cookie", CSRF_COOKIE + "=" + tok + "; Path=/; Secure; SameSite=Strict"})
	}
	h.apiJSON(200, map[string]string{"csrf": tok}, hdr...)
}

type apiDemoUser struct {
	Username string `json:"username"`
	Label    string `json:"label"`
}

// apiAuthOptions is what the sign-in page offers: SSO, password sign-in, the demo's one-click accounts.
func (h *reqCtx) apiAuthOptions() {
	out := map[string]any{"product": brand.Product, "subtitle": BrandSubtitle(), "sso": oidc.Configured(),
		"passwordLogin": !SSOEnforced(), "demo": DEMO, "demoBanner": If(DEMO, strings.TrimPrefix(DEMO_BANNER, "Demo — "), "")}
	if DEMO && DEMO_PASSWORD != "" {
		// the demo's throwaway password is public (demo/README.md); the Go login page carries it the same way
		out["demoUsers"] = []apiDemoUser{{DEMO_VIEWER, "Sign in as viewer"}, {DEMO_ADMIN, "Sign in as admin"}}
		out["demoPassword"] = DEMO_PASSWORD
	}
	h.apiJSON(200, out)
}

func (h *reqCtx) apiMFAPending() {
	p := auth.GetPending(h.cookie(MFA_COOKIE))
	if p == nil {
		h.apiJSON(200, map[string]any{"pending": false})
		return
	}
	h.apiJSON(200, map[string]any{"pending": true, "username": p.Username})
}

// apiSessionCookie is the session cookie of an API sign-in (SameSite=Lax: links into the app from elsewhere work).
func apiSessionCookie(token string, maxAge int64) string {
	return auth.COOKIE + "=" + token + "; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=" + I2S(maxAge)
}

// apiLogin is POST /api/v1/auth/login: the password step of /login (same lockout, logs and SSO-only rule).
func (h *reqCtx) apiLogin(body map[string]any) {
	user := bodyStr(body, "username")
	nxt := TodayLanding(SafeNext(bodyStr(body, "next")))
	if SSOEnforced() && !IsBreakGlass(user) {
		logf("login REFUSED (sso only) user=%s ip=%s", py.Repr(py.Head(user, 40)), h.ip())
		h.apiFail(&apiErr{403, "Password sign-in is turned off. Use single sign-on."})
		return
	}
	ok := auth.CheckPassword(user, bodyStr(body, "password"), h.ip())
	logf("login %s user=%s ip=%s (api)", If(ok, "ok", "FAILED"), py.Repr(py.Head(user, 40)), h.ip())
	if !ok {
		h.apiFail(&apiErr{401, "Wrong username or password, or too many attempts."})
		return
	}
	if old := h.token(); old != "" {
		auth.Logout(old) // a new token at every sign-in
	}
	remember := bodyStr(body, "remember") == "1" || body["remember"] == true
	if auth.Enabled(user) { // the code step: a pending login, not a session
		auth.DropPending(h.cookie(MFA_COOKIE))
		pt, err := auth.NewPending(user, nxt, "local", remember, "")
		if err != nil {
			panic(err)
		}
		h.apiJSON(200, map[string]any{"state": "mfa", "next": nxt}, [2]string{"Set-Cookie", mfaCookie(pt)})
		return
	}
	token, err := auth.NewSession(user, auth.Opts{Remember: remember, IP: h.ip(), UA: h.ua()})
	if err != nil {
		panic(err)
	}
	h.apiJSON(200, map[string]any{"state": "ok", "next": enrolFirst(user, nxt)}, [2]string{"Set-Cookie", apiSessionCookie(token, maxAgeFor(remember))})
}

// apiLoginMFA is POST /api/v1/auth/mfa: the code step (the pending cookie, attempt limit and audit).
func (h *reqCtx) apiLoginMFA(body map[string]any) {
	clear := [2]string{"Set-Cookie", mfaCookieClear}
	pt := h.cookie(MFA_COOKIE)
	p := auth.GetPending(pt)
	expired := func(msg string) {
		auth.DropPending(pt)
		h.apiJSON(401, map[string]any{"state": "expired", "error": msg}, clear)
	}
	switch {
	case p == nil:
		expired("The sign-in expired. Sign in again.")
		return
	case bodyStr(body, "action") == "cancel":
		auth.DropPending(pt)
		h.apiJSON(200, map[string]any{"state": "cancelled"}, clear)
		return
	case !auth.PendingTry(pt): // charged before the check: parallel guesses cannot exceed the limit
		expired("Too many wrong codes. Sign in again.")
		return
	}
	via, err := auth.Verify(p.Username, bodyStr(body, "code"), h.ip())
	if err == auth.ErrNotActive {
		expired("Sign in again.")
		return
	}
	if err != nil {
		logf("login 2fa FAILED user=%s ip=%s (api)", py.Repr(p.Username), h.ip())
		auth.Log(auth.Event{Actor: p.Username, Action: "login.2fa", Target: p.Username, IP: h.ip(), Method: "totp", Outcome: "failed"})
		h.apiJSON(401, map[string]any{"state": "mfa", "error": mfaMsg(err)})
		return
	}
	token, pp, err := auth.FinishPending(pt, via, h.ip(), h.ua())
	if err != nil {
		expired("The sign-in expired. Sign in again.")
		return
	}
	logf("login 2fa ok user=%s via=%s ip=%s (api)", py.Repr(pp.Username), via, h.ip())
	h.apiJSON(200, map[string]any{"state": "ok", "next": TodayLanding(SafeNext(pp.Next))},
		[2]string{"Set-Cookie", apiSessionCookie(token, maxAgeFor(pp.Remember))}, clear)
}

// apiLogout ends the session (without one it only clears the cookie); redirect is Keycloak's sign-out when SSO is on.
func (h *reqCtx) apiLogout(_ map[string]any) {
	clear := [2]string{"Set-Cookie", auth.COOKIE + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax"}
	end := ""
	if sess := auth.GetSession(h.token()); sess != nil {
		if idToken := auth.Logout(h.token()); idToken != "" && oidc.Configured() {
			end = oidc.LogoutURL(idToken)
		}
		logf("logout user=%s (api)", sess.Username)
	}
	h.apiJSON(200, map[string]any{"ok": true, "redirect": py.OrS(end, "/login")}, clear)
}

// apiTheme sets the theme cookie (light, dark, or auto = follow the system).
func (h *reqCtx) apiTheme(body map[string]any) {
	var ck string
	switch t := bodyStr(body, "theme"); t {
	case "light", "dark":
		ck = THEME_COOKIE + "=" + t + "; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Lax"
	case "auto", "system":
		ck = THEME_COOKIE + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax"
	default:
		h.apiFail(&apiErr{400, "theme is light, dark or auto"})
		return
	}
	h.apiJSON(200, map[string]any{"ok": true}, [2]string{"Set-Cookie", ck})
}

// --- me, nav, status -------------------------------------------------------------------------------------------

type apiMeOut struct {
	Username    string          `json:"username"`
	Role        string          `json:"role"`
	Source      string          `json:"source"`
	MustChange  bool            `json:"mustChangePassword"`
	MFA         map[string]any  `json:"mfa"`
	Permissions map[string]bool `json:"permissions"`
	Theme       string          `json:"theme"`
	Demo        bool            `json:"demo"`
	DemoBanner  string          `json:"demoBanner,omitempty"`
	Product     string          `json:"product"`
	Subtitle    string          `json:"subtitle"`
}

// apiMe is GET /api/v1/me: the user, role and permissions; it answers for a session that must still change its
// password or enrol 2FA (mustChangePassword, mfa.enrolRequired) so the frontend can send the person there.
func (h *reqCtx) apiMe() {
	sess := auth.GetSession(h.token())
	if sess == nil {
		h.apiFail(&apiErr{401, "login required"})
		return
	}
	auth.TouchClient(h.token(), h.ip(), h.ua())
	admin := sess.Role == "admin"
	h.apiJSON(200, apiMeOut{Username: sess.Username, Role: sess.Role, Source: sess.Source, MustChange: sess.MustChange != 0,
		MFA: map[string]any{"enabled": auth.Enabled(sess.Username), "via": sess.MFAVia, "at": sess.MFAAt,
			"enrolRequired": mfaEnrolPending(sess)},
		Permissions: map[string]bool{"review.read": true, "review.decide": admin, "today.act": admin && TodayOn() && TodayCanAct(),
			"settings": admin},
		Theme: py.OrS(h.c.Theme, "auto"), Demo: DEMO, DemoBanner: If(DEMO, strings.TrimPrefix(DEMO_BANNER, "Demo — "), ""),
		Product: brand.Product, Subtitle: BrandSubtitle()})
}

type apiNavLink struct {
	Label      string `json:"label"`
	Href       string `json:"href"`
	Count      int64  `json:"count,omitempty"`
	CountTitle string `json:"countTitle,omitempty"`
	Alarm      bool   `json:"alarm,omitempty"` // the count is of something broken (open incidents, failing checks)
}

type apiNavGroup struct {
	Key      string       `json:"key"`
	Name     string       `json:"name"`
	Icon     string       `json:"icon"`
	Question string       `json:"question"`
	Links    []apiNavLink `json:"links"`
}

// apiNavJSON is the sidebar: the six groups with the links this role sees and their counts (admins only), the footer
// and the search form.
func apiNavJSON(_ py.QS, sess *Session) (any, *apiErr) {
	admin := sess.Role == "admin"
	groups := []apiNavGroup{}
	for i := range SECTIONS {
		s := &SECTIONS[i]
		if sectionOff(s.Key) {
			continue
		}
		g := apiNavGroup{Key: s.Key, Name: s.Name, Icon: s.Icon, Question: s.Question, Links: []apiNavLink{}}
		for _, l := range s.visibleLinks(admin) {
			n, title := sideCount(l.Count, sess)
			g.Links = append(g.Links, apiNavLink{Label: l.Label, Href: l.Href, Count: n, CountTitle: title,
				Alarm: n > 0 && (l.Count == "incidents" || l.Count == "checks")})
		}
		groups = append(groups, g)
	}
	foot := []apiNavLink{{Label: "Help", Href: "/help"}}
	if admin {
		foot = append(foot, apiNavLink{Label: "Settings", Href: "/admin"})
	}
	return map[string]any{"groups": groups, "footer": foot, "search": map[string]any{"action": "/inventory", "param": "q",
		"hidden": map[string]string{"go": "1"}, "placeholder": "VM name, IP or service"}}, nil
}

// apiStatusJSON is the "Data as of" pill: when the collectors last finished, when the checks last ran, and whether a
// source is stale (nothing finished for 2 h) or failed (its latest run in 24 h is not OK).
func apiStatusJSON(_ py.QS, _ *Session) (any, *apiErr) {
	now := NowInt()
	var asOf, checksAt int64
	failed := []string{}
	failedHref := "/runs?status=notok"
	func() {
		defer func() { recover() }() // a fresh install has no runs table yet
		if r := Q("SELECT max(started_at + coalesce(duration_ms, 0) / 1000) t FROM runs"); len(r) > 0 {
			asOf = r[0].I("t")
		}
		if r := Q("SELECT max(started_at) t FROM runs WHERE task IN ('backup-check', 'cert-check')"); len(r) > 0 {
			checksAt = r[0].I("t")
		}
		for _, r := range Q("SELECT r.id, r.task, r.status FROM runs r JOIN (SELECT task, max(started_at) m FROM runs WHERE started_at>=? "+
			"GROUP BY task) l ON l.task=r.task AND l.m=r.started_at ORDER BY r.task", now-86400) {
			if st := r.S("status"); st != "" && st != "OK" && !strings.HasPrefix(strings.ToLower(st), "skip") {
				failed = append(failed, r.S("task"))
				if len(failed) == 1 {
					failedHref = "/runs/view?id=" + I2S(r.I("id"))
				} else {
					failedHref = "/runs?status=notok"
				}
			}
		}
	}()
	state := "ok"
	switch {
	case asOf == 0:
		state = "unknown"
	case len(failed) > 0:
		state = "failed"
	case now-asOf > 2*3600:
		state = "stale"
	}
	return map[string]any{"now": now, "dataAsOf": asOf, "checksAt": checksAt, "state": state, "failed": failed, "failedHref": failedHref,
		"tz": LocalTime(float64(now)).Format("MST")}, nil
}
