package app

// Account as JSON for the Next.js pages (ADR-0018): GET /api/v1/account and /api/v1/account/2fa, and the twins of the Go
// routes. Every policy stays the Go route's: a must-change session reaches only /account, an enrolment-pending one only
// /account/2fa (apiSession), and the password or the code is checked here exactly as there. A stored token never reaches
// an answer. The TOTP seed and the recovery codes appear only in the answer of the action that made them, and every API
// answer is no-store.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

// apiGateAllows: what a session that must still change its password or enrol 2FA may reach (the gate in server.go).
func apiGateAllows(path string) (must, enrol bool) {
	return path == "/api/v1/account", path == "/api/v1/account/2fa" || strings.HasPrefix(path, "/api/v1/account/2fa/")
}

func apiAccountMFA(sess *Session) map[string]any {
	st := auth.Status(sess.Username)
	state, kind, note := "off", "warn", "Needed for every change in Argus."
	if st.Enabled {
		state, kind, note = "on", "ok", "Authenticator app since "+When(st.EnabledAt)+" · "+Itoa(st.RecoveryLeft)+" recovery codes left."
	} else if sess.MFAVia == "idp" {
		state, kind, note = "via Keycloak", "ok", "This sign-in used Keycloak's second factor."
	}
	return map[string]any{"enabled": st.Enabled, "state": state, "kind": kind, "note": note}
}

// apiAccountSessions lists this user's sessions. The demo shares one account between visitors, so it never shows an address.
func apiAccountSessions(sess *Session, token string) []map[string]any {
	out := []map[string]any{}
	for _, x := range auth.UserSessions(sess.Username, token) {
		kind := If(x.Remember, "kept signed in", "this visit")
		if x.MFAAt > 0 {
			kind += " · 2FA " + Ago(x.MFAAt)
		}
		out = append(out, map[string]any{"id": x.ID, "device": deviceOf(x.UA), "current": x.Current, "kind": kind, "ip": If(x.IP == "" || DEMO, "-", x.IP), "last_seen": x.LastSeen, "created": When(x.Created)})
	}
	return out
}

func apiAccountJira(c *Ctx, sess *Session, qs py.QS) map[string]any {
	st := JiraState()
	hosts := jiraStrList(py.Or(st.Get("allowed_hosts"), []any{}))
	cn := JiraConn(sess.Username)
	url := py.OrS(cn.Get("url"), "")
	if url == "" && len(hosts) == 1 {
		url = "https://" + hosts[0]
	}
	out := map[string]any{"connected": py.Truthy(cn), "notices": apiNotices(jiraResult(c, qs.Get("jira"), sess.Username)), "allowed": Text(jiraAllowedNote(sess, hosts)), "url": url, "mode": py.OrS(cn.Get("mode"), "ro")}
	if !py.Truthy(cn) {
		return out
	}
	ver := []string{}
	for _, k := range []string{"version", "deployment"} {
		if py.Truthy(cn.Get(k)) {
			ver = append(ver, py.Str(cn.Get(k)))
		}
	}
	chosen := map[string]bool{}
	list := []string{}
	for _, p := range jiraStrList(py.Or(cn.Get("projects"), []any{})) {
		if !chosen[p] {
			chosen[p] = true
			list = append(list, p)
		}
	}
	sort.Strings(list)
	projects := []map[string]any{}
	for _, pv := range py.AsList(py.Or(cn.Get("available"), []any{})) {
		p := py.AsMap(pv)
		if p == nil || !jiraProjectRe.MatchString(py.Str(p.GetD("key", ""))) {
			continue
		}
		key := py.Str(p.Get("key"))
		projects = append(projects, map[string]any{"key": key, "name": py.Str(p.GetD("name", "")), "on": chosen[key]})
	}
	out["display"], out["jira_name"], out["jira_url"] = py.OrS(py.Or(cn.Get("display"), cn.Get("jira_name")), "?"), py.OrS(cn.Get("jira_name"), ""), py.OrS(cn.Get("url"), "")
	out["version"], out["last_sync"], out["last_error"] = py.OrS(strings.Join(ver, " "), "-"), py.I(py.Or(cn.Get("last_sync"), int64(0))), py.OrS(cn.Get("last_error"), "")
	out["projects"], out["chosen"], out["default_project"] = projects, list, py.OrS(cn.Get("default_project"), "")
	return out
}

func apiAccountMatrix(sess *Session, qs py.QS) map[string]any {
	mine := matrixAllIDs()[sess.Username]
	note := "Only admins can send tasks to Argus by Matrix; your ID is kept for later."
	if sess.Role == "admin" {
		note = "Forward a task or a message link to " + py.OrS(matrixBotID(), "the Argus bot") + " in a direct message; Argus files it in Jira for you."
	}
	return map[string]any{"id": mine, "note": note, "placeholder": "@name:" + MATRIX_SERVER_NAME, "saved": qs.Get("matrix") == "saved"}
}

// apiAccount is GET /api/v1/account.
func (h *reqCtx) apiAccount() {
	sess := h.apiSession()
	if sess == nil {
		return
	}
	qs := py.ParseQS(h.url.Query)
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/account"}
	out := map[string]any{"user": sess.Username, "role": sess.Role, "source": sess.Source, "must_change": sess.MustChange != 0, "min_pw": auth.MIN_PW}
	if sess.Source == "keycloak" {
		out["sso"] = map[string]string{"issuer": py.OrS(oidc.ISSUER, "not configured"), "client": py.OrS(oidc.CLIENT_ID, "argus")}
	}
	if sess.MustChange != 0 { // nothing else is reachable before the new password: the sections stay on the page after it
		h.apiJSON(200, out)
		return
	}
	out["host"], out["mfa"], out["sessions"] = ARGUS_HOSTNAME, apiAccountMFA(sess), apiAccountSessions(sess, h.token())
	out["limits"] = map[string]int64{"remember_days": auth.RememberMax / 86400, "remember_idle": auth.RememberIdle / 86400, "session_hours": auth.SessionMax / 3600, "idle_hours": auth.SessionIdle / 3600}
	out["jira"], out["matrix"] = apiAccountJira(c, sess, qs), apiAccountMatrix(sess, qs)
	out["refresh"] = apiRefresh(c)
	h.apiJSON(200, out)
}

// apiAccount2FA is GET /api/v1/account/2fa: what Account → Two-factor may offer this session. Never the factor itself.
func (h *reqCtx) apiAccount2FA() {
	sess := h.apiSession()
	if sess == nil {
		return
	}
	qs := py.ParseQS(h.url.Query)
	st := auth.Status(sess.Username)
	local := sess.Source != "keycloak"
	reauth := ""
	if !local && sess.AuthAt < NowInt()-auth.FRESH_AUTH {
		reauth = "/auth/login?" + py.UrlencodeKV("next", "/account/2fa", "reauth", "1")
	}
	h.apiJSON(200, map[string]any{"user": sess.Username, "local": local, "reauth": reauth, "auth_ago": Ago(sess.AuthAt), "key_ready": auth.KeyReady(), "idp": sess.MFAVia == "idp",
		"required": mfaEnrolPending(sess), "reset": auth.ReenrolDue(sess.Username), "enabled": st.Enabled, "enabled_at": When(st.EnabledAt), "last_used": st.LastUsed, "recovery_left": st.RecoveryLeft, "recovery_total": auth.RECOVERY_CODES,
		"next": SafeNext(qs.GetD("next", "/account"))})
}

// apiEnrolAnswer is the one answer that carries a seed: the action that began the enrolment, or a refused code of it.
func apiEnrolAnswer(sess *Session, secret []byte) map[string]any {
	uri := auth.OTPAuthURI(auth.MFAIssuerName, sess.Username, secret)
	n, d := qrPath(uri)
	return map[string]any{"key": groupKey(auth.B32(secret)), "uri": uri, "account": sess.Username, "qr": map[string]any{"n": n, "d": d}, "ttl": auth.ENROL_TTL / 60}
}

func (h *reqCtx) apiFresh(sess *Session, password string) (*Session, string, *apiErr) {
	if sess.Source == "keycloak" {
		return sess, h.token(), nil
	}
	if !auth.CheckPassword(sess.Username, password, h.ip()) {
		return nil, "", &apiErr{400, "The password is wrong (or too many attempts)."}
	}
	if nt := auth.RotateFresh(h.token()); nt != "" {
		s := auth.GetSession(nt)
		h.w.Header().Add("Set-Cookie", apiSessionCookie(nt, s.MaxAge()))
		return s, nt, nil
	}
	return sess, h.token(), nil
}

func (h *reqCtx) accountPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	ip, get := h.ip(), func(k string) string { return bodyStr(body, k) }
	next := SafeNext(py.OrS(get("next"), "/account"))
	switch path {
	case "/api/v1/account":
		if sess.Source == "keycloak" {
			return nil, &apiErr{403, "Your password is managed in Keycloak."}
		}
		if get("new") != get("new2") {
			h.apiFail(&apiErr{400, "The new passwords do not match."}, "field", "new2")
			return nil, nil
		}
		if msg := auth.ChangePassword(sess.Username, get("current"), get("new")); msg != "" {
			return nil, &apiErr{400, msg}
		}
		logf("password changed user=%s", sess.Username)
		return map[string]any{"ok": true, "message": "Password changed.", "redirect": "/account"}, nil
	case "/api/v1/account/2fa/begin":
		s, _, e := h.apiFresh(sess, get("password"))
		if e != nil {
			return nil, e
		}
		secret, err := auth.BeginEnrol(s, get("code"), ip)
		if err != nil {
			return nil, &apiErr{400, mfaMsg(err)}
		}
		return map[string]any{"ok": true, "enrol": apiEnrolAnswer(s, secret), "next": next}, nil
	case "/api/v1/account/2fa/confirm":
		codes, err := auth.ConfirmEnrol(sess, get("code"), ip)
		if err != nil {
			if secret, e2 := auth.PendingSecret(sess); e2 == nil && err != auth.ErrLocked { // the step again, as the Go page did: still this action's answer
				h.apiFail(&apiErr{400, mfaMsg(err)}, "enrol", apiEnrolAnswer(sess, secret))
				return nil, nil
			}
			return nil, &apiErr{400, mfaMsg(err)}
		}
		nt := auth.Rotate(h.token(), NowInt(), "totp") // the code just proved the factor on this session
		ns := auth.GetSession(nt)
		logf("2fa enrolled user=%s", sess.Username)
		h.apiJSON(200, map[string]any{"ok": true, "codes": codes, "title": "Two-factor authentication is on", "note": "Two-factor authentication is on.", "made": When(NowInt()), "next": next},
			[2]string{"Set-Cookie", apiSessionCookie(nt, ns.MaxAge())})
		return nil, nil
	case "/api/v1/account/2fa/disable":
		s, _, e := h.apiFresh(sess, get("password"))
		if e != nil {
			return nil, e
		}
		if err := auth.Disable(s, get("code"), ip); err != nil {
			return nil, &apiErr{400, mfaMsg(err)}
		}
		logf("2fa disabled user=%s", sess.Username)
		return map[string]any{"ok": true, "message": "Two-factor authentication is off. You can read pages; changes need it again.", "redirect": "/account/2fa"}, nil
	case "/api/v1/account/2fa/recovery":
		codes, err := auth.NewRecovery(sess, get("code"), ip)
		if err != nil {
			return nil, &apiErr{400, mfaMsg(err)}
		}
		return map[string]any{"ok": true, "codes": codes, "title": "New recovery codes", "note": "New recovery codes made; the old ones no longer work.", "made": When(NowInt()), "next": "/account/2fa"}, nil
	case "/api/v1/account/sessions":
		return h.accountSessions(body, sess)
	}
	return nil, &apiErr{404, "no such endpoint"}
}

func (h *reqCtx) accountSessions(body map[string]any, sess *Session) (any, *apiErr) {
	act, id, ip := bodyStr(body, "action"), bodyStr(body, "id"), h.ip()
	self := act == "revoke" && id == sess.ID()
	if !self && (auth.Enabled(sess.Username) || sess.MFAVia == "idp") {
		// ending other sessions of a 2FA account needs 2FA, so a password-only session cannot throw the owner's verified ones out
		if st := MFAState(sess, PolicyNormal); st != "" {
			h.apiFail(&apiErr{403, "two-factor authentication required"}, "mfa", st, "next", "/account/2fa", "sensitive", false)
			return nil, nil
		}
	}
	switch act {
	case "others":
		n := auth.RevokeOthers(sess.Username, h.token())
		secEvent(sess, "sessions.revoke-others", sess.Username, I2S(n)+" ended", ip, "")
		return map[string]any{"ok": true, "message": "Signed out of " + I2S(n) + " other session" + py.Plural(int(n)) + ".", "redirect": "/account"}, nil
	case "revoke":
		if self {
			auth.Logout(h.token())
			secEvent(sess, "sessions.revoke-one", sess.Username, "this session", ip, "")
			h.apiJSON(200, map[string]any{"ok": true, "redirect": "/login"}, [2]string{"Set-Cookie", auth.COOKIE + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Lax"})
			return nil, nil
		}
		if auth.RevokeSession(sess.Username, id) {
			secEvent(sess, "sessions.revoke-one", sess.Username, "session "+id, ip, "")
			return map[string]any{"ok": true, "message": "Session signed out.", "redirect": "/account"}, nil
		}
		return map[string]any{"ok": true, "message": "That session was already gone.", "redirect": "/account"}, nil
	}
	return nil, &apiErr{400, "Unknown action."}
}
