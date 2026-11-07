package app

// Settings → Backup targets and Settings → Credentials as JSON for the Next.js pages (ADR-0018). Admins only, like every
// /admin path. Keys go to root once and stay there; only the fields named here reach an answer, so a value that root's
// status file might hold never does. The credentials file is metadata only (views_credentials.go).

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiBackupTarget(t *py.OMap) map[string]any {
	r := py.AsMap(py.Or(t.Get("test"), py.NewOMap()))
	parts := []string{}
	for _, x := range []struct{ k, label string }{{"list", "list "}, {"put", "put "}, {"delete", "delete "}} {
		if py.Truthy(r.Get(x.k)) {
			parts = append(parts, x.label+py.Str(r.Get(x.k)))
		}
	}
	if a := py.Str(r.GetD("admin", "")); a != "" {
		parts = append(parts, a)
	}
	ret := I2S(py.I(py.Or(t.Get("retention_days"), int64(14))))
	pre := ""
	if py.Truthy(t.Get("prefix")) {
		pre = py.Str(t.Get("prefix")) + "/"
	}
	envs := backupJoin(t.Get("envs"))
	if envs == "" {
		envs = "all environments"
	}
	at := ""
	if py.Truthy(r.Get("at")) {
		at = When(r.Get("at"))
	}
	return map[string]any{"name": py.Str(t.Get("name")), "type": py.Str(t.GetD("type", "minio")), "envs": envs, "endpoint": py.Str(t.GetD("endpoint", "")),
		"bucket": py.Str(t.GetD("bucket", "")), "prefix": py.Str(t.GetD("prefix", "")), "badge": apiAgentBadge(backupTargetState(t)),
		"detail": strings.Join(parts, " · "), "at": at, "retention": ret, "plans": py.I(py.Or(t.Get("plans"), int64(0))),
		"ilm": "mc ilm rule add --expire-days " + ret + " --noncurrent-expire-days 7 --prefix \"" + pre + "\" <admin alias>/" + py.Str(t.Get("bucket"))}
}

func apiAdminTargetsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/backup-targets"}
	notices := apiNotices(backupTargetBanner(c, qs))
	rows := []map[string]any{}
	for _, t := range backupTargets() {
		rows = append(rows, apiBackupTarget(t))
	}
	return map[string]any{"host": ARGUS_HOSTNAME, "notices": notices, "refresh": apiRefresh(c), "rows": rows, "envs": BPEnvs,
		"age_recipients": py.I(py.Or(backupTargetsDoc().Get("age_recipients"), int64(0)))}, nil
}

// apiCredExpires is the Expires cell as data (credExpires): the date, its pill, the rotation line and the note under it.
func apiCredExpires(it *py.OMap) map[string]any {
	st, band, date := it.S("status"), it.S("band"), it.S("expires_at")
	note := ""
	switch it.S("expiry_source") {
	case "estimate":
		note = "estimated: valid " + I2S(int64(py.Num(it.GetD("valid_days", int64(365))))) + " days from when it was set"
	case "last-known":
		note = "last known date (the check failed)"
	}
	cell := func(date, kind, text, note string) map[string]any {
		return map[string]any{"date": date, "pill": map[string]string{"kind": kind, "text": text}, "note": note, "rotate": ""}
	}
	dl := ""
	if v := it.Get("days_left"); v != nil {
		dl = I2S(int64(py.Num(v))) + " d"
	}
	switch {
	case st == "absent":
		return cell("", "", "", "")
	case st == "rejected":
		return cell(date, "fail", "rejected", note)
	case band == "ok":
		return cell(date, "ok", dl, note)
	case band == "warn":
		return cell(date, "warn", dl, note)
	case band == "crit":
		return cell(date, "fail", dl, note)
	case band == "expired":
		return cell(date, "fail", "expired", note)
	}
	out := cell("", "neutral", If(band == "never", "never expires", "no expiry info"), "")
	if due := it.S("rotate_due"); due != "" {
		rot := I2S(int64(py.Num(it.Get("rotate_days"))))
		if st == "rotate" {
			out["rotate"], out["rotate_now"] = "policy "+rot+" d, due "+due, true
		} else {
			out["rotate"] = "rotate by " + due + " (every " + rot + " d)"
		}
	}
	return out
}

func apiCredRow(it *py.OMap) map[string]any {
	second := []string{}
	for _, s := range []string{it.S("identity"), credSet(it)} {
		if s != "" {
			second = append(second, s)
		}
	}
	lines := []string{}
	if it.S("status") == "rejected" {
		lines = append(lines, it.S("detail"))
	}
	if e := it.S("error"); e != "" {
		lines = append(lines, "check failed: "+e)
	}
	if n := it.S("note"); n != "" && it.S("status") != "absent" {
		lines = append(lines, n)
	}
	return map[string]any{"id": it.S("id"), "name": it.S("name"), "second": strings.Join(second, " · "), "detail": strings.Join(lines, "; "),
		"kind": it.S("kind_label"), "used_by": it.S("used_by"), "where": it.S("where"), "expires": apiCredExpires(it),
		"last_used": py.I(py.Or(it.Get("last_used"), int64(0))), "checked": py.I(py.Or(it.Get("checked_at"), int64(0)))}
}

func apiCredHowto(items []*py.OMap) []map[string]any {
	out := []map[string]any{}
	for _, g := range credHowtoGroups(items) {
		creds, names := []map[string]any{}, []string{}
		for _, it := range g.items {
			names = append(names, it.S("name"))
			steps := []string{}
			for _, s := range it.List("howto") {
				steps = append(steps, py.Str(s))
			}
			creds = append(creds, map[string]any{"name": it.S("name"), "steps": steps})
		}
		note := strings.Join(names, ", ")
		if len(note) > 90 {
			note = Itoa(len(names)) + " credentials"
		}
		out = append(out, map[string]any{"kind": g.label, "note": note, "creds": creds})
	}
	return out
}

func apiAdminCredsJSON(_ py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/credentials"}
	d := credDoc()
	busy, skipped := credCheckState(d)
	if busy != "" {
		c.Refresh = true
	}
	meta := "Never checked"
	if d != nil {
		meta = "Checked " + Ago(d.Get("checked_at")) + " · daily 07:20"
	}
	scan := map[string]any{"tasks": credTask, "back": "/admin/credentials", "label": "Check now", "busy": busy != "", "meta": If(busy != "", busy+" · refreshing", If(skipped != "", skipped, meta)),
		"title": "Let every credential check itself again now (about 30 s)", "overdue": ""}
	bad, ok, absent := []map[string]any{}, []map[string]any{}, []map[string]any{}
	out := map[string]any{"host": ARGUS_HOSTNAME, "refresh": apiRefresh(c), "scan": scan, "checked": d != nil, "bad": bad, "ok": ok, "absent": absent, "howto": []map[string]any{}}
	if d == nil {
		return out, nil
	}
	items := credItems(d)
	for _, it := range items {
		switch {
		case it.S("status") == "absent":
			absent = append(absent, apiCredRow(it))
		case credAttention[it.S("status")]:
			bad = append(bad, apiCredRow(it))
		default:
			ok = append(ok, apiCredRow(it))
		}
	}
	th := d.Map("thresholds")
	if th == nil {
		th = py.NewOMap()
	}
	days := func(k string, def int64) int64 { return int64(py.Num(th.GetD(k, def))) }
	next := map[string]any(nil)
	if n := d.Map("next"); n != nil && n.Get("days_left") != nil && py.Num(n.Get("days_left")) > 0 {
		next = map[string]any{"name": n.S("name"), "days": int64(py.Num(n.Get("days_left")))}
	}
	certs := "/certs"
	if x := d.Map("certificates"); x != nil && strings.HasPrefix(x.S("link"), "/") {
		certs = SafeNext(x.S("link"))
	}
	out["bad"], out["ok"], out["absent"], out["next"], out["certs"], out["howto"] = bad, ok, absent, next, certs, apiCredHowto(items)
	out["thresholds"] = map[string]int64{"warn": days("warn_days", 30), "crit": days("crit_days", 7), "rotate": days("rotate_days", 180), "today": days("today_days", 14)}
	return out, nil
}
