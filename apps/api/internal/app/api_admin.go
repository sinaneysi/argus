package app

// Settings → General and Users and access as JSON for the Next.js pages (ADR-0018): GET /api/v1/admin and /admin/users, and
// the twins of POST /admin, /admin/claude, /admin/users and /admin/users/matrix. Admins only, same policies and limits as
// the Go routes, every write through AdminSaveField, ClaudeSave, UsersPostIP or matrixSetID. A temporary password exists
// only in the answer to the action that made it; no stored secret is read here.

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

func apiAdminOnly(sess *Session) *apiErr {
	if sess.Role != "admin" {
		return &apiErr{403, "Admins only"}
	}
	return nil
}

// apiSetting is one form field: its name, label, type and value (on for a checkbox).
func apiSetting(prefix string, s setting, cur *py.OMap) map[string]any {
	out := map[string]any{"name": prefix + s.k, "label": s.label}
	switch s.def.(type) {
	case bool:
		out["type"], out["on"] = "check", py.Truthy(cur.Get(s.k))
	case int64:
		out["type"], out["lo"], out["hi"], out["value"] = "number", s.lo, s.hi, I2S(py.I(cur.Get(s.k)))
	default:
		out["type"], out["value"] = If(s.k == "event_date", "date", "text"), py.Str(cur.Get(s.k))
	}
	return out
}

func apiSettings(prefix string, defs []setting, cur *py.OMap) []map[string]any {
	out := []map[string]any{}
	for _, s := range defs {
		out = append(out, apiSetting(prefix, s, cur))
	}
	return out
}

func apiAdminApplied(applied *py.OMap) map[string]any {
	if b, ok := applied.Get("ok").(bool); ok && !b {
		return map[string]any{"kind": "fail", "text": "Last save was rejected. Error: " + SafeErr(py.Str(applied.GetD("error", ""))) + ". Correct the setting and save again."}
	}
	if applied.Len() == 0 {
		return map[string]any{"kind": "none", "text": "Nothing applied from this page yet."}
	}
	ch := []string{}
	for _, x := range py.AsList(py.Or(applied.Get("changes"), []any{})) {
		ch = append(ch, py.Str(x))
	}
	return map[string]any{"kind": "ok", "text": "Live since " + When(applied.Get("at")) + " by " + py.Str(applied.GetD("by", "?")) + ": " + py.OrS(strings.Join(ch, ", "), "no change")}
}

func apiAdminJSON(_ py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	cfg := py.AsMap(ReadJSON(settingsPath(), defaults()))
	if cfg == nil {
		panic("settings.json is not an object")
	}
	applied := py.AsMap(ReadJSON(filepath.Join(SET_OUT, "applied.json"), py.NewOMap()))
	cred := py.AsMap(ReadJSON(CRED, py.NewOMap()))
	defs := defaults()
	sched := []map[string]any{}
	for _, at := range ADMIN_TASKS {
		t := py.AsMap(py.AsMap(cfg.GetD("tasks", py.NewOMap())).GetD(at[0], defs.Map("tasks").Get(at[0])))
		nxt := py.Str(py.AsMap(applied.Get("next")).GetD(at[0], "-"))
		sched = append(sched, map[string]any{"task": at[0], "label": at[1], "schedule": py.Str(t.Get("schedule")), "enabled": py.Truthy(t.Get("enabled")), "next": ShortTS(nxt), "nextFull": nxt})
	}
	th := py.AsMap(cfg.GetD("thresholds", defs.Get("thresholds")))
	limits := []map[string]any{}
	for _, l := range LIMITS {
		limits = append(limits, map[string]any{"name": "th:" + l.k, "label": l.desc, "type": "number", "lo": l.lo, "hi": l.hi, "value": I2S(py.I(th.GetD(l.k, defs.Map("thresholds").Get(l.k))))})
	}
	ra, rd, qa := merged(RCA_AUTO, py.Or(cfg.Get("rca_auto"), nil)), merged(READINESS, cfg.Get("readiness")), QAAskSettings(cfg)
	bad, good := []map[string]any{}, []map[string]any{}
	for _, iv := range py.AsList(cred.GetD("items", []any{})) {
		it := py.AsMap(iv)
		row := map[string]any{"name": py.Str(it.GetD("name", "")), "detail": py.Str(it.GetD("detail", "")), "ok": py.Truthy(it.Get("ok"))}
		if py.Truthy(it.Get("ok")) {
			good = append(good, row)
		} else {
			bad = append(bad, row)
		}
	}
	audit := []string{}
	if lines, err := readLinesPy(filepath.Join(SET_OUT, "audit.log")); err == nil {
		for i := len(lines) - 1; i >= 0 && len(audit) < 15; i-- {
			audit = append(audit, lines[i])
		}
	}
	ssoNote := "Keycloak is not configured, so SSO only has no effect until it is."
	if oidc.Configured() {
		ssoNote = "Keycloak single sign-on is configured (" + oidc.ISSUER + ", client " + oidc.CLIENT_ID + ")."
	}
	bg := append([]string(nil), BREAK_GLASS...)
	sort.Strings(bg)
	on := ClaudeOn(nil)
	return map[string]any{"host": ARGUS_HOSTNAME, "qaRoom": MATRIX_QA_ROOM_LABEL, "applied": apiAdminApplied(applied),
		"claude":    map[string]any{"on": on},
		"modules":   apiModules(),
		"schedules": sched, "options": SCHEDULES, "thresholds": limits, "rcaAuto": apiSettings("ra:", RCA_AUTO, ra), "readiness": apiSettings("rd:", READINESS, rd), "qa": apiSettings("qa:", QA_ASK, qa),
		"notes": map[string]string{"ra": If(py.Truthy(ra.Get("enabled")), "on", "off"), "rd": If(py.Truthy(rd.Get("event_date")), "event "+py.Str(rd.Get("event_date")), ""),
			"qa": If(!py.Truthy(qa.Get("enabled")), "off", If(py.Truthy(qa.Get("dry_run")), "dry run", "on"))},
		"sso":         map[string]any{"on": SSOOnly(cfg), "note": ssoNote, "breakGlass": strings.Join(bg, ", ")},
		"credentials": map[string]any{"checkedAt": py.I(py.Or(cred.Get("checked_at"), int64(0))), "bad": bad, "ok": good}, "log": audit}, nil
}

func apiUserActions(u auth.User, me string, mfaOn bool) []string {
	acts := []string{}
	if u.Username == me {
		return acts
	}
	if u.Source != "keycloak" {
		acts = append(acts, "reset", If(u.Role == "admin", "role-viewer", "role-admin"))
	}
	acts = append(acts, If(u.Disabled != 0, "enable", "disable"), "revoke-sessions")
	if mfaOn {
		acts = append(acts, "reset-2fa")
	}
	return acts
}

func apiUsersJSON(_ py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	mfa := auth.StatusAll()
	ids := matrixAllIDs()
	rows := []map[string]any{}
	names := []string{}
	for _, u := range auth.Users() {
		st := mfa[u.Username]
		state, kind := "active", "ok"
		if u.Disabled != 0 {
			state, kind = "disabled", "fail"
		} else if u.MustChange != 0 {
			state, kind = "must change password", "warn"
		}
		last := int64(0)
		if u.LastLogin.Valid {
			last = u.LastLogin.Int64
		}
		names = append(names, u.Username)
		rows = append(rows, map[string]any{"name": u.Username, "you": u.Username == sess.Username, "role": u.Role, "source": If(u.Source == "keycloak", "keycloak", "local"),
			"mfa": st.Enabled, "mfaUsed": st.LastUsed, "lastLogin": last, "matrix": ids[u.Username], "state": state, "stateKind": kind, "actions": apiUserActions(u, sess.Username, st.Enabled)})
	}
	on := auth.MFALoginRequired()
	events := []map[string]any{}
	for _, r := range auth.AuditLog(20) {
		events = append(events, map[string]any{"at": r.At, "actor": r.Actor, "action": r.Action, "outcome": r.Outcome, "target": r.Target, "detail": r.Detail, "reason": r.Reason})
	}
	note := "Every change needs 2FA" + If(auth.MFAActions, "", " (off: MFA_REQUIRED_FOR_ACTIONS=0)") + "; reading does not. " +
		If(auth.MFAStepUpOn, "A fresh code is asked by route (MFA_STEP_UP=on). ", "One code per sign-in covers every change of that session (MFA_STEP_UP=off). ")
	return map[string]any{"rows": rows, "names": names, "matrix": map[string]any{"bot": matrixBotID(), "linked": len(ids), "server": MATRIX_SERVER_NAME},
		"mfa": map[string]any{"required": on, "fromEnv": auth.MFALoginEnv, "note": note, "events": events}}, nil
}

// adminPost answers the settings writes; the page's own message comes back to show (a temporary password only here).
func (h *reqCtx) adminPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	form := apiForm(body)
	switch path {
	case "/api/v1/admin/save":
		msg, field, err := AdminSaveField(form, sess)
		if err != nil {
			panic(err)
		}
		if field != "" {
			h.apiFail(&apiErr{400, msg}, "field", field)
			return nil, nil
		}
		return map[string]any{"ok": true, "message": msg}, nil
	case "/api/v1/admin/writing-style":
		msg, field, err := StylePost(form, sess)
		if err != nil {
			panic(err)
		}
		if field != "" {
			h.apiFail(&apiErr{400, msg}, "field", field)
			return nil, nil
		}
		return map[string]any{"ok": true, "message": msg}, nil
	case "/api/v1/admin/modules":
		msg, e := ModuleSave(form.Get("module"), form.Get("on"), sess)
		if e != nil {
			return nil, e
		}
		return map[string]any{"ok": true, "message": msg}, nil
	case "/api/v1/admin/claude":
		msg, err := ClaudeSave(form, sess)
		if err != nil {
			panic(err)
		}
		if !strings.Contains(msg, "Applying now") {
			return nil, &apiErr{400, msg}
		}
		return map[string]any{"ok": true, "message": msg}, nil
	case "/api/v1/admin/users/matrix":
		user, found := form.Get("user"), false
		for _, u := range auth.Users() {
			found = found || u.Username == user
		}
		if !found {
			return nil, &apiErr{400, "No such user."}
		}
		msg, err := matrixSetID(user, form.Get("matrix_id"))
		if err != nil {
			panic(err)
		}
		if msg != "" {
			return nil, &apiErr{400, msg}
		}
		return map[string]any{"ok": true, "message": "Matrix ID of " + user + " saved.", "kind": "ok"}, nil
	}
	msg, kind, err := UsersPostIP(form, sess, h.ip())
	if err != nil {
		panic(err)
	}
	logf("users: %s %s %s -> %s", sess.Username, form.Get("action"), py.Repr(py.Head(form.Get("user"), 40)), kind)
	return map[string]any{"ok": kind == "ok", "message": msg, "kind": kind}, nil
}
