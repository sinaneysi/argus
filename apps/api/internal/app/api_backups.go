package app

// The Backups pages as JSON: GET /api/v1/backups (+ /set, /new, /plan). The connection string only ever travels in the
// POST /api/v1/backups/new body; no answer carries it, only the redacted form the root job wrote.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var regexpOption = regexp.MustCompile(`(?s)<option value="([^"]*)"([^>]*)>(.*?)</option>`)

func apiRestore(tests []any) map[string]any {
	if len(tests) == 0 {
		return map[string]any{"result": "never", "kind": "", "date": ""}
	}
	t := py.AsMap(tests[0])
	s := py.Str(t.GetD("result", "?"))
	return map[string]any{"result": s, "kind": If(s == "ok", "ok", If(s == "partial", "warn", "fail")), "date": py.Str(t.GetD("date", ""))}
}

func apiAccepted(o Override, sess *Session) map[string]any {
	return map[string]any{"id": o.ID, "by": o.By, "scope": scopeText(o), "reason": o.Reason, "canUndo": sess.Role == "admin"}
}

func apiCoverage(disc *py.OMap) map[string]any {
	items := backupCoverageItems(disc)
	fams := backupMaps(disc.GetD("families", []any{}))
	sets := backupMaps(disc.GetD("sets", []any{}))
	lists := map[string][]map[string]any{"high": {}, "gaps": {}, "unencrypted": {}, "noscript": {}}
	for _, i := range items {
		key := map[string]string{"gap": "gaps", "unencrypted": "unencrypted", "noscript": "noscript"}[i.group]
		if i.group == "defect" && i.risk == "high" {
			key = "high"
		}
		if key != "" {
			lists[key] = append(lists[key], map[string]any{"risk": i.risk, "kind": If(i.risk == "high", "fail", "warn"), "title": i.title, "meta": i.meta, "board": apiBoardBtn("backup", i.sid)})
		}
	}
	tb := 0.0
	for _, s := range sets {
		tb += py.Num(s.GetD("total_gb", int64(0)))
	}
	tb /= 1024
	tile := func(label string, n int, sub string) apiTile {
		return apiTile{Label: label, Figure: Itoa(n), Sub: sub, Hot: n != 0 && label != "Families"}
	}
	tiles := []apiTile{tile("Families", len(fams), Itoa(len(sets))+If(len(sets) == 1, " set, ", " sets, ")+py.F(tb, 1)+" TB"), tile("Sets without a known script", len(lists["noscript"]), "family E"),
		tile("Unencrypted sets", len(lists["unencrypted"]), defIssueUnencrypted), tile("Databases without a backup", len(lists["gaps"]), defBackupGapExamples),
		tile("High-risk defects", len(lists["high"]), "in the real scripts")}
	families := []map[string]any{}
	for _, f := range fams {
		n, gb := 0, 0.0
		for _, s := range sets {
			if s.Get("family") == f.Get("id") {
				n++
				gb += py.Num(s.GetD("total_gb", int64(0)))
			}
		}
		families = append(families, map[string]any{"id": py.Str(f.Get("id")), "name": py.Str(f.Get("name")), "sets": n, "size": If(gb != 0, py.F(gb/1024, 1)+" TB", ""), "engine": py.Str(f.Get("engine")),
			"how": py.Str(f.Get("how")), "defects": backupJoin(f.GetD("defects", []any{})), "issues": backupJoin(f.Get("issues"))})
	}
	m := py.AsMap(py.Or(disc.Get("minio"), py.NewOMap()))
	pct := py.Num(py.Or(m.Get("disk_pct"), int64(0)))
	return map[string]any{"generated": py.Str(disc.GetD("generated", "?")), "host": py.Str(m.GetD("host", "")), "diskPct": py.Str(m.GetD("disk_pct", "?")), "pct": apiBar(pct),
		"diskKind": If(pct >= 90, "fail", If(pct >= 80, "warn", "ok")), "used": py.Str(m.Get("disk_used_tb")), "total": py.Str(m.Get("disk_total_tb")), "issue": py.Str(m.GetD("issue", "")),
		"tiles": tiles, "high": lists["high"], "gaps": lists["gaps"], "unencrypted": lists["unencrypted"], "noscript": lists["noscript"], "families": families}
}

func apiBackupPlans(plist []*py.OMap) []map[string]any {
	out := []map[string]any{}
	for i := len(plist) - 1; i >= 0; i-- {
		p := plist[i]
		kind, words := backupPlanState(p)
		out = append(out, map[string]any{"id": py.I(p.Get("id")), "state": apiPill{Text: words, Kind: kind}, "name": py.Str(p.Get("name")), "engine": bpEngineLabelOr(p.Get("engine"), "?"), "env": py.Str(p.Get("env")),
			"server": py.Str(p.GetD("server", "")), "schedule": py.Str(p.GetD("schedule_label", "")), "destination": py.Str(p.GetD("bucket", "")) + "/" + py.Str(p.GetD("prefix", "")),
			"restore": apiRestore(py.AsList(p.Get("restore_tests")))})
	}
	return out
}

// apiBackupsJSON is GET /api/v1/backups with the query parameters of /backups.
func apiBackupsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	out := map[string]any{"ran": false, "admin": sess.Role == "admin", "templates": map[string]string{"url": backupTemplatesURL, "repo": BACKUP_TEMPLATES_REPO},
		"coverage": apiCoverage(backupRef().Discovery), "plans": apiBackupPlans(BackupPlans()), "scan": apiScanBar("/backups"), "refresh": apiScanBusy("/backups"), "banners": []apiBanner{}}
	r := LatestRun("backup-check")
	if r == nil {
		return out, nil
	}
	flt, bucket := qs.GetD("status", "all"), qs.Get("bucket")
	rows := Q("SELECT * FROM backup_results WHERE run_id=? ORDER BY status != 'OK' DESC, name", r.Get("id"))
	acc := BackupAccepted(rows)
	isFail := func(x Row) bool { _, a := acc[x.Str("name")]; return x.Get("status") != "OK" && !a }
	bucketOf := func(x Row) string { b, _, _ := strings.Cut(x.Str("name"), "_"); return b }
	stN, bN := map[string]int{}, map[string]int{}
	nfail := 0
	plist := BackupPlans()
	foldOK := flt == "all" && bucket == ""
	body, okb := []map[string]any{}, []map[string]any{}
	for _, x := range rows {
		_, a := acc[x.Str("name")]
		if isFail(x) {
			nfail++
		}
		matchS := func(f string) bool {
			return f == "all" || (f == "fail" && isFail(x)) || (f == "ok" && x.Get("status") == "OK") || (f == "accepted" && a)
		}
		if bucket == "" || bucketOf(x) == bucket {
			for _, f := range []string{"all", "fail", "ok", "accepted"} {
				if matchS(f) {
					stN[f]++
				}
			}
		}
		if matchS(flt) {
			bN[bucketOf(x)]++
		}
		if !matchS(flt) || (bucket != "" && bucketOf(x) != bucket) {
			continue
		}
		ratio := "-"
		if x.T("size") && x.T("median_prev7") {
			ratio = py.F(x.F("size")/x.F("median_prev7")*100, 0) + " %"
		}
		row := map[string]any{"status": apiBadge(x.Get("status")), "name": x.Str("name"), "href": "/backups/set?name=" + py.Quote(x.Str("name")), "age": Text(backupAgeCell(x)), "size": Size(x.Get("size")),
			"ratio": ratio, "kinds": If(x.IsNull("kinds"), "-", x.Str("kinds")), "newest": x.S("newest"), "restore": apiRestore(BackupRestoreForSet(x.Str("name"), plist)),
			"accepted": nil, "mark": x.Get("status") != "OK" && !a && sess.Role == "admin"}
		if o, ok := acc[x.Str("name")]; ok {
			row["accepted"] = apiAccepted(o, sess)
		}
		if foldOK && x.Get("status") == "OK" {
			okb = append(okb, row)
		} else {
			body = append(body, row)
		}
	}
	var buckets [][2]string
	for b := range bN {
		buckets = append(buckets, [2]string{b, b})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i][0] < buckets[j][0] })
	st := apiFacet{Key: "status", Label: "Status", Value: flt, Options: []apiFacetOpt{{"all", "All", int64(stN["all"])}, {"fail", "Failing", int64(stN["fail"])}, {"ok", "OK", int64(stN["ok"])}}}
	if len(acc) > 0 || flt == "accepted" {
		st.Options = append(st.Options, apiFacetOpt{"accepted", "Accepted", int64(stN["accepted"])})
	}
	shown := len(body) + len(okb)
	out["ran"], out["status"], out["sets"], out["failing"], out["accepted"], out["at"], out["summary"] = true, apiBadge(r.Get("status")), len(rows), nfail, len(acc), r.I("started_at"), py.OrS(r.Get("summary"), "")
	out["facets"] = []apiFacet{st, apiFacetOf("bucket", "Bucket", bucket, buckets, bN)}
	out["list"] = map[string]any{"path": "/backups", "q": "", "sort": "", "sorts": []apiOption{}, "filtered": !foldOK, "clearHref": "/backups", "matched": shown, "total": len(rows), "noun": "sets", "page": 1, "pages": 1,
		"from": 1, "to": shown, "hidden": map[string]string{}, "noSearch": true}
	out["rows"], out["okRows"] = body, okb
	if qs.Get("sent") == "override" {
		out["banners"] = []apiBanner{{Text: "Sent. Argus applies it within a minute; reload to see it.", Kind: "ok"}}
	}
	return out, nil
}

// apiBackupSetJSON is GET /api/v1/backups/set?name=.
func apiBackupSetJSON(qs py.QS, sess *Session) (any, *apiErr) {
	name := qs.Get("name")
	hist := Q(`SELECT r.started_at, b.* FROM backup_results b JOIN runs r ON r.id=b.run_id
                WHERE b.name=? AND r.started_at > ? ORDER BY r.started_at`, name, Now()-30*86400)
	out := map[string]any{"name": name, "empty": len(hist) == 0, "banners": []apiBanner{}}
	if len(hist) == 0 {
		return out, nil
	}
	rows := []map[string]any{}
	pts := [][2]float64{}
	for i := len(hist) - 1; i >= 0; i-- {
		h := hist[i]
		rows = append(rows, map[string]any{"at": h.I("started_at"), "status": apiBadge(h.Get("status")), "age": Text(backupAgeCell(h)), "size": Size(h.Get("size")), "newest": h.S("newest")})
	}
	for _, h := range hist {
		if !h.IsNull("size") {
			pts = append(pts, [2]float64{h.F("started_at"), h.F("size")})
		}
	}
	var chart map[string]any
	if len(pts) >= 2 {
		mn, mx := pts[0][1], pts[0][1]
		for _, p := range pts {
			mn, mx = min(mn, p[1]), max(mx, p[1])
		}
		chart = map[string]any{"points": pts, "latest": SizeF(pts[len(pts)-1][1]), "min": SizeF(mn), "max": SizeF(mx), "low": mn, "high": mx, "from": int64(pts[0][0]), "to": int64(pts[len(pts)-1][0])}
	}
	last := hist[len(hist)-1]
	out["status"], out["accepted"], out["override"] = apiBadge(last.Get("status")), nil, nil
	if cur := latestRowOf(name); cur != nil && cur.Str("status") != "OK" {
		if o, ok := BackupAccepted([]Row{cur})[name]; ok {
			out["status"] = apiPill{Text: "Accepted", Kind: "info", Title: cur.Str("status") + " accepted by " + o.By}
			out["accepted"] = apiAccepted(o, sess)
			out["real"] = cur.Str("status")
		} else if sess.Role == "admin" {
			out["override"] = map[string]any{"check": cur.Str("status"), "openDays": OVERRIDE_OPEN_DAYS, "maxDays": OVERRIDE_MAX_DAYS}
		}
	}
	out["checks"], out["latest"], out["rows"], out["chart"] = len(hist), Size(last.Get("size")), rows, chart
	if qs.Get("sent") == "override" {
		out["banners"] = []apiBanner{{Text: "Sent. Argus applies it within a minute; reload to see it.", Kind: "ok"}}
	}
	return out, nil
}

func apiBackupTargets(tl []*py.OMap, cur, env string) ([]apiOption, string) {
	opts := []apiOption{}
	re := regexpOption.FindAllStringSubmatch(backupTargetOpts(tl, cur, env), -1)
	sel := ""
	for _, m := range re {
		v := unescape(m[1])
		if strings.Contains(m[2], "selected") {
			sel = v
		}
		opts = append(opts, apiOption{v, Text(m[3])})
	}
	return opts, sel
}

func apiBackupRuns(v map[string]string) []map[string]any {
	keys := [][2]string{}
	for _, s := range BSSchedules {
		keys = append(keys, [2]string{s.Key, s.Label})
	}
	keys = append(keys, [2]string{"cron", "Cron"})
	now := backupNow()
	out := []map[string]any{}
	for _, k := range keys {
		runs := BSNextRuns(k[0], v["cron"], now)
		row := map[string]any{"key": k[0], "label": k[1], "runs": runs, "error": "", "note": ""}
		if len(runs) == 0 {
			row["runs"] = []string{}
			if k[0] == "cron" && py.Strip(v["cron"]) != "" {
				if _, _, _, err := BSScheduleOf("cron", v["cron"]); err != nil {
					row["error"] = err.Error()
				} else {
					row["note"] = "No run in the next year."
				}
			} else {
				row["note"] = "Type a cron expression (minute hour day month weekday), then Preview."
			}
		}
		out = append(out, row)
	}
	return out
}

// apiBackupNewJSON is GET /api/v1/backups/new: the form's state, from the query parameters of /backups/new.
func apiBackupNewJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/backups/new"}
	v := map[string]string{}
	for _, k := range append(append([]string{}, backupKeep...), "draft") {
		v[k] = py.Head(qs.Get(k), 200)
	}
	if v["schedule"] == "" {
		v["schedule"] = "daily"
	}
	draft := ""
	if BSDraftRE.MatchString(v["draft"]) {
		draft = v["draft"]
	}
	var d *py.OMap
	if draft != "" {
		if dv := backupDrafts().Get(draft); dv == nil {
			d = py.NewOMap("state", "testing", "member", py.OrS(v["member"], "the database host"))
		} else if d = py.AsMap(dv); d.Get("user") != sess.Username {
			d = nil
		}
	}
	dT := backupTruthyD(d)
	if dT && d.Get("state") == "testing" {
		c.Refresh = 3
	}
	st, _ := d.Get("state").(string)
	tested := dT && (st == "ok" || st == "deferred") && py.Truthy(d.Get("token"))
	test := map[string]any{"state": st, "member": py.Str(d.GetD("member", "")), "reason": py.OrS(d.Get("reason"), "failed"), "missing": "", "facts": []apiFact{}, "absent": []string{}}
	t := py.AsMap(py.Or(d.Get("test"), py.NewOMap()))
	if py.Truthy(t.Get("missing")) {
		test["missing"] = py.Str(t.Get("missing"))
	}
	if t.Len() > 0 && (st == "ok" || st == "fail") {
		client := []string{}
		for _, k := range []string{"mode", "container", "image"} {
			if py.Truthy(t.Get(k)) {
				client = append(client, py.Str(t.Get(k)))
			}
		}
		facts := []apiFact{}
		for _, e := range [][2]string{{"Version", py.OrS(t.Get("version"), "")}, {"Role", py.OrS(t.Get("role"), "")}, {"Client", strings.Join(client, " · ")},
			{"Databases", strings.ReplaceAll(py.OrS(t.Get("databases"), ""), ",", ", ")}} {
			if e[1] != "" {
				facts = append(facts, apiFact{Label: e[0], Value: e[1]})
			}
		}
		test["facts"] = facts
		dbs := strings.Join(py.Split(strings.ReplaceAll(py.OrS(v["databases"], "all"), ",", " ")), " ")
		if miss := BSMissingDatabases(py.OrS(dbs, "all"), t); len(miss) > 0 {
			test["absent"] = miss
		}
	}
	derived := []apiFact{}
	if dT {
		eng := bpEngineLabelOr(d.GetD("engine", ""), py.Str(d.GetD("engine", "")))
		derived = append(derived, apiFact{Label: "Engine", Value: eng}, apiFact{Label: "Environment", Value: py.Str(d.GetD("env", ""))},
			apiFact{Label: "Runs on", Value: py.Str(d.GetD("member", "")) + " (the database host)"}, apiFact{Label: "Reads", Value: If(d.Get("engine") == "mongodb", "a secondary", "the server in the string")})
	}
	derived = append(derived, apiFact{Label: "Encryption", Value: "age (public keys on the host)"}, apiFact{Label: "Compression", Value: "zstd"},
		apiFact{Label: "Staging", Value: "stream, no local copy"}, apiFact{Label: "Retention", Value: py.OrS(v["retention_days"], "14") + " days (target default)"})
	members := []string{}
	if ms := py.AsList(d.Get("members")); len(ms) > 1 {
		for _, m := range ms {
			members = append(members, py.Str(m))
		}
	}
	tl := backupTargets()
	env, _ := d.GetD("env", "").(string)
	targets, target := apiBackupTargets(tl, v["target"], env)
	banners := apiBannersOf(backupRequestBanner(c, qs))
	saved := map[string]any(nil)
	if qs.Has("req") {
		if s := py.AsMap(backupStatus().Map("requests").GetD(qs.Get("req"), py.NewOMap())); py.Truthy(s.Get("plan")) && strings.HasPrefix(py.Str(s.GetD("result", "")), "saved") {
			saved = map[string]any{"seconds": 2, "url": "/backups/plan?id=" + I2S(py.I(s.Get("plan")))}
		} else if c.Refresh == true {
			c.Refresh = 3
		}
	}
	refresh := apiRefresh(c)
	if saved != nil {
		refresh = saved
	}
	note := "Save unlocks when the test passes for this exact string."
	switch {
	case tested && st == "deferred":
		note = "Not tested by Argus: the operator's install bundle tests it before installing anything."
	case tested:
		note = "Tested for this exact string. Save stores the plan; the string stays root-only on " + ARGUS_HOSTNAME + " until it is installed."
	case st == "testing":
		note = "Testing on the database host; this page refreshes itself."
	}
	cur := map[string]any{"uriRedacted": "", "sha": "", "token": ""}
	if dT && py.Truthy(d.Get("uri_redacted")) {
		cur = map[string]any{"uriRedacted": py.Str(d.Get("uri_redacted")), "sha": py.Str(d.GetD("sha", "")), "token": py.Str(d.GetD("token", ""))}
	}
	return map[string]any{"values": v, "draft": draft, "current": cur, "admin": sess.Role == "admin", "tested": tested, "test": test, "derived": derived, "members": members, "member": py.Str(d.Get("member")),
		"topology": py.Str(d.GetD("topology", "")), "targets": targets, "target": target, "noTarget": len(tl) == 0, "schedules": apiBackupRuns(v), "tz": ARGUS_TZ_LABEL, "repo": BACKUP_TEMPLATES_REPO,
		"note": note, "prod": dT && d.Get("env") == "release", "replace": "/backups/new?" + backupKeepQS(v), "banners": banners, "refresh": refresh}, nil
}

// apiBackupNew is POST /api/v1/backups/new: Test, Save or Preview. The form comes back through its query string; the
// errors ride with the answer so the page can say them.
func apiBackupNew(form py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "Admins test connections and save backups."}
	}
	a := backupDecideNew(form, sess)
	if a.err != nil {
		logf("save failed on /backups/new: %v", a.err)
		return nil, &apiErr{500, "Not saved."}
	}
	if a.redirect != "" {
		return map[string]any{"ok": true, "redirect": SafeNext(a.redirect)}, nil
	}
	return map[string]any{"ok": true, "redirect": SafeNext("/backups/new?" + backupKeepQS(a.values, "draft", a.values["draft"])), "errors": a.errs}, nil
}

// apiBackupInstall is the Install section as data.
func apiBackupInstall(p *py.OMap) map[string]any {
	if p.Get("kind") != "simple" {
		return nil
	}
	id := I2S(py.I(p.Get("id")))
	if p.Get("env") == "release" {
		cmd := "ssh " + ARGUS_SSH_ALIAS + " 'sudo /usr/local/sbin/argus-backup-bundle " + id + "' \\\n" +
			"  | ssh <root alias of " + py.Str(p.Get("server")) + "> 'f=$(mktemp); cat >\"$f\"; sudo bash \"$f\"; rc=$?; rm -f \"$f\"; exit $rc'"
		return map[string]any{"mode": "operator", "command": cmd, "after": "ssh " + ARGUS_SSH_ALIAS + " 'sudo /usr/local/sbin/argus-backup-bundle " + id + " --installed'",
			"host": ARGUS_HOSTNAME, "installedAt": p.Get("installed_at")}
	}
	inst := py.AsMap(py.Or(p.Get("install"), py.NewOMap()))
	st := inst.Get("state")
	if !py.Truthy(st) {
		return map[string]any{"mode": "none", "server": py.Str(p.Get("server"))}
	}
	if st == "queued" || st == "running" {
		return map[string]any{"mode": "running", "by": py.Str(inst.GetD("by", "")), "at": inst.Get("at")}
	}
	steps := []map[string]any{}
	for _, lv := range py.AsList(inst.Get("steps")) {
		if m := backupStepRe.FindStringSubmatch(py.Str(lv)); m != nil {
			steps = append(steps, map[string]any{"kind": If(m[2] == "ok" || m[2] == "warn", m[2], "fail"), "name": m[1], "result": m[2], "detail": m[3]})
		}
	}
	for _, lv := range py.AsList(inst.Get("verify")) {
		line := py.Str(lv)
		if strings.HasPrefix(line, "object=") || strings.HasPrefix(line, "sha256=") || strings.HasPrefix(line, "header=") {
			steps = append(steps, map[string]any{"kind": "ok", "name": "", "result": "", "detail": "verify " + line})
		}
	}
	return map[string]any{"mode": "done", "ok": st == "ok", "by": py.Str(inst.GetD("by", "")), "at": inst.Get("at"), "secret": py.OrS(inst.Get("secret"), ""), "reason": py.OrS(inst.Get("reason"), ""), "steps": steps}
}

// apiBackupPlanJSON is GET /api/v1/backups/plan?id=.
func apiBackupPlanJSON(qs py.QS, sess *Session) (any, *apiErr) {
	p := backupGetPlan(qs.Get("id"))
	if p == nil {
		return nil, &apiErr{404, "no such plan"}
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/backups/plan"}
	kind, words := backupPlanState(p)
	prod := p.Get("env") == "release"
	admin := sess.Role == "admin"
	id := py.I(p.Get("id"))
	mr := py.AsMap(py.Or(p.Get("mr"), py.NewOMap()))
	mrURL := py.Str(mr.GetD("url", ""))
	ext := strings.HasPrefix(mrURL, "https://")
	mrFact := apiFact{Label: "Merge request", Value: py.Str(py.Or(py.Or(mr.Get("note"), mr.Get("state")), "not yet"))}
	if ext && py.Truthy(mr.Get("iid")) {
		mrFact = apiFact{Label: "Merge request", Value: "!" + I2S(py.I(mr.Get("iid"))) + " " + py.Str(mr.GetD("state", "")), Href: mrURL}
	}
	task := ""
	if py.Truthy(p.Get("task_id")) {
		task = "/board/task?id=" + I2S(py.I(p.Get("task_id")))
	}
	simple := p.Get("kind") == "simple"
	inst := py.AsMap(py.Or(p.Get("install"), py.NewOMap()))
	withdrawn := p.Get("status") == "withdrawn"
	var install map[string]any
	if simple && !prod && admin && !withdrawn {
		busy := inst.Get("state") == "queued" || inst.Get("state") == "running"
		if busy {
			c.Refresh = 5
		}
		install = map[string]any{"label": If(inst.Get("state") == "ok", "Install again", "Install now"), "busy": busy, "again": inst.Get("state") == "ok"}
	}
	files := py.AsMap(py.Or(p.Get("files"), py.NewOMap()))
	paths := append([]string{}, files.Keys()...)
	sort.Strings(paths)
	blocks := []map[string]any{}
	for _, path := range paths {
		if path != "PLAN.md" {
			txt := py.Str(files.Get(path))
			blocks = append(blocks, map[string]any{"path": py.Str(p.GetD("job_path", "")) + "/" + path, "lines": len(py.SplitLines(txt)), "text": txt})
		}
	}
	var keep []string
	for _, l := range py.SplitLines(py.Str(files.GetD("PLAN.md", ""))) {
		if !strings.HasPrefix(l, "> ") && !strings.HasPrefix(l, "# Backup plan") {
			keep = append(keep, l)
		}
	}
	planmd := strings.Join(keep, "\n")
	if simple {
		planmd = backupStripInstall(planmd)
	}
	tests := []map[string]any{}
	for _, tv := range py.AsList(p.Get("restore_tests")) {
		t := py.AsMap(tv)
		tests = append(tests, map[string]any{"date": py.Str(t.GetD("date", "")), "result": apiRestore([]any{tv}), "object": py.Str(t.GetD("object", "")), "notes": py.Str(t.GetD("notes", "")), "by": py.Str(t.GetD("by", ""))})
	}
	warnings := []string{}
	for _, wv := range py.AsList(p.Get("warnings")) {
		if w := py.Str(wv); !strings.HasPrefix(w, "PRODUCTION CHANGE") && !strings.HasPrefix(w, "PRODUCTION:") && !strings.HasPrefix(w, "Overlaps ") {
			warnings = append(warnings, w)
		}
	}
	overlaps := []string{}
	for _, o := range py.AsList(p.Get("overlaps")) {
		overlaps = append(overlaps, py.Str(py.AsMap(o).GetD("text", "")))
	}
	est := py.AsMap(py.Or(p.Get("estimate"), py.NewOMap()))
	rt := apiRestore(py.AsList(p.Get("restore_tests")))
	record := []apiFact{{Label: "Plan", Value: "#" + I2S(id)}, {Label: "State", Value: words}, mrFact, {Label: "Board", Value: If(task != "", "board task", "being created"), Href: task},
		{Label: "Server", Value: py.Str(p.Get("server")), Mono: true}, {Label: "Mode", Value: py.Str(p.Get("mode")) + If(py.Truthy(p.Get("container")), " · "+py.Str(p.Get("container")), "")},
		{Label: "Schedule", Value: py.Str(p.GetD("schedule_label", ""))}, {Label: "OnCalendar", Value: py.Str(p.GetD("oncalendar", "")), Mono: true},
		{Label: "Destination", Value: py.Str(p.Get("bucket")) + "/" + py.Str(p.Get("prefix")), Mono: true},
		{Label: "Retention", Value: I2S(py.I(p.GetD("retention_days", int64(0)))) + " days" + If(py.Truthy(p.Get("monthly")), " + monthly", "")},
		{Label: "Encryption", Value: py.Str(p.GetD("encryption", "")) + " · " + py.Str(p.GetD("compression", ""))},
		{Label: "Estimate", Value: "~" + BPHumanBytes(est.GetD("bytes", int64(0))) + " · ~" + I2S(py.I(est.GetD("minutes", int64(0)))) + " min"},
		{Label: "Owner", Value: py.Str(p.GetD("owner", ""))}, {Label: "Restore tested", Value: strings.TrimSpace(py.Str(rt["result"]) + " " + py.Str(rt["date"]))},
		{Label: "Templates", Value: py.Str(p.GetD("templates", "")), Mono: true}, {Label: "Created", Value: py.Str(p.GetD("created_by", "")) + " · " + When(p.Get("created_at"))}}
	banners := apiBannersOf(backupRequestBanner(c, qs))
	var form map[string]any
	if admin && !withdrawn {
		form = map[string]any{"today": LocalTime(Now()).Format("2006-01-02")}
	}
	return map[string]any{"id": id, "name": py.Str(p.Get("name")), "state": apiPill{Text: words, Kind: kind}, "engine": bpEngineLabelOr(p.Get("engine"), ""), "env": py.Str(p.Get("env")), "server": py.Str(p.Get("server")),
		"schedule": py.Str(p.GetD("schedule_label", "")), "prod": prod, "banners": banners, "installBtn": install, "fixerHref": If(!simple && !prod && task != "" && admin && !withdrawn, task+"#fixer", ""),
		"mrHref": If(ext, mrURL, ""), "taskHref": task, "warnings": warnings, "overlaps": overlaps, "install": apiBackupInstall(p), "plan": planmd, "jobPath": py.Str(p.GetD("job_path", "")), "files": blocks,
		"fixPrompt": If(!prod, py.OrS(p.Get("fix_prompt"), ""), ""), "tests": tests, "testForm": form, "record": record, "repo": BACKUP_TEMPLATES_REPO, "refresh": apiRefresh(c)}, nil
}
