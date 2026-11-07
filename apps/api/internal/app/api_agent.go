package app

// The Agent section as JSON for the Next.js pages (ADR-0018): runs, one run, what Argus knows, and the fix runs with their
// review step. Same queries, request files, roles and 2FA policy as the Go pages; a fix request goes through
// FixerPostReview, so its checks stay in one place. Text only: the frontend never takes HTML from the API.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiRunCell(r Row, k string) string {
	if !r.T(k) {
		return "0"
	}
	return r.Str(k)
}

// apiRunsJSON is GET /api/v1/runs: the filtered page of runs, the day's count and the cost of the last 30 days.
func apiRunsJSON(qs py.QS, _ *Session) (any, *apiErr) {
	d := runsData(qs)
	rows := []map[string]any{}
	for _, r := range d.runs {
		rows = append(rows, map[string]any{"id": r.I("id"), "at": py.I(py.Or(r.Get("started_at"), int64(0))), "task": r.Str("task"), "status": apiAgentBadge(Badge(r.Get("status"))),
			"seconds": py.F(py.Num(py.Or(r.Get("duration_ms"), int64(0)))/1000, 0), "cost": "$" + py.F(py.Num(py.Or(r.Get("cost_usd"), int64(0))), 3),
			"turns": py.OrS(r.Get("turns"), "-"), "denied": apiRunCell(r, "denials"), "summary": FirstLine(r.Get("summary"), 100)})
	}
	spent := map[string]float64{}
	top := 0.0
	for _, p := range d.pts {
		spent[Strftime("2006-01-02", p.T)] = p.V
		top = max(top, p.V)
	}
	days := []map[string]any{} // every day of the window: a day without a run cost nothing
	for i := 29; i >= 0; i-- {
		day := Strftime("2006-01-02", Now()-float64(i)*86400)
		days = append(days, map[string]any{"day": day, "usd": spent[day], "label": "$" + py.F(spent[day], 2)})
	}
	out := map[string]any{"rows": rows, "list": apiListOf(d.l, d.matched, d.total, "runs", runsSorts), "facets": []apiFacet{}, "day": d.nDay, "dayBad": d.badDay,
		"cost": "$" + py.F(d.totalCost, 2), "days": days, "dayTop": top, "dayTopLabel": "$" + py.F(top, 2)}
	if d.total > 0 {
		tv, tn := d.facet("task", "task")
		sv, sn := d.facet("status", "status")
		bad := 0
		for _, v := range sv {
			if v[0] != "OK" && !strings.HasPrefix(strings.ToLower(v[0]), "skip") {
				bad += sn[v[0]]
			}
		}
		if bad > 0 {
			sv, sn[runsNotOK] = append([][2]string{{runsNotOK, "Not OK"}}, sv...), bad
		}
		out["facets"] = []apiFacet{apiFacetOf("task", "Task", d.l.get("task"), tv, tn), apiFacetOf("status", "Status", d.l.get("status"), sv, sn)}
	}
	return out, nil
}

// apiRunJSON is GET /api/v1/runs/item?id=: one run with the messages it handled, its findings and its record.
func apiRunJSON(qs py.QS, _ *Session) (any, *apiErr) {
	rid := qs.Get("id")
	if !py.IsDigit(rid) || py.Len(rid) > 9 {
		return nil, &apiErr{404, "no such run"}
	}
	rr := Q("SELECT * FROM runs WHERE id=?", py.I(rid))
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such run"}
	}
	r := rr[0]
	var msgs []Row
	if qaReady() {
		msgs = Q("SELECT * FROM chat_messages WHERE run_id=? ORDER BY (source='qa') DESC, ts", r.Get("id"))
	}
	ch := qaNewChain()
	list := []map[string]any{}
	for _, m := range msgs {
		x := apiQaMsg(m)
		x["chain"] = apiChainOf(qaChainItems(m, ch, false))
		x["source"] = getD(QASourceLabels, m.S("source"), m.S("source"))
		list = append(list, x)
	}
	findings := []map[string]any{}
	for _, p := range Q("SELECT * FROM triage_problems WHERE run_id=? ORDER BY idx", r.Get("id")) {
		findings = append(findings, map[string]any{"href": "/incidents/problem?run=" + I2S(p.I("run_id")) + "&idx=" + I2S(p.I("idx")), "title": py.OrS(p.Get("title"), "(untitled)"),
			"severity": apiAgentBadge(IncidentsSevBadge(p.Get("severity"))), "env": p.S("environment"), "summary": FirstLine(p.S("summary"), 300)})
	}
	incidents := []apiLink{}
	for _, i := range Q("SELECT i.id, i.title FROM incidents i JOIN incident_events e ON e.incident_id=i.id WHERE e.run_id=? AND e.kind='finding' GROUP BY i.id ORDER BY i.id", r.Get("id")) {
		incidents = append(incidents, apiLink{Text: "#" + I2S(i.I("id")) + " " + i.Str("title"), Href: "/incidents/view?id=" + I2S(i.I("id"))})
	}
	logp := py.OrS(r.Get("log_path"), "-")
	if i := strings.LastIndexByte(logp, '/'); i >= 0 {
		logp = logp[i+1:]
	}
	rail := []apiFact{{Label: "Task", Value: r.Str("task")}, {Label: "Started", Value: When(r.Get("started_at"))}, {Label: "Status", Value: py.OrS(r.Get("status"), "?")},
		{Label: "Duration", Value: py.F(py.Num(py.Or(r.Get("duration_ms"), int64(0)))/1000, 0) + " s"}, {Label: "Cost", Value: "$" + py.F(py.Num(py.Or(r.Get("cost_usd"), int64(0))), 3)},
		{Label: "Turns", Value: py.OrS(r.Get("turns"), "-")}, {Label: "Denied", Value: apiRunCell(r, "denials")}}
	if r.T("mr_url") {
		rail = append(rail, apiFact{Label: "MR", Value: "merge request", Href: apiSafeHref(r.Str("mr_url"))})
	} else {
		rail = append(rail, apiFact{Label: "MR", Value: "-"})
	}
	if r.Str("task") == "log-rca" {
		rail = append(rail, apiFact{Label: "Report", Value: "log-rca report and its alerts", Href: "/incidents/run?id=" + I2S(r.I("id"))})
	}
	rail = append(rail, apiFact{Label: "Log file on the server", Value: logp, Mono: true})
	return map[string]any{"id": r.I("id"), "task": r.Str("task"), "at": py.I(py.Or(r.Get("started_at"), int64(0))), "status": apiAgentBadge(Badge(r.Get("status"))), "md": apiMd(r.Get("summary"), "fold"),
		"showMessages": len(msgs) > 0 || r.Get("task") == "chat-triage", "messages": list, "findings": findings, "findingsWord": FindingWord("Problems"), "incidents": incidents, "rail": rail}, nil
}

// apiKnowledgeJSON is GET /api/v1/knowledge: the approved lessons as one document, any signed-in user.
func apiKnowledgeJSON(_ py.QS, sess *Session) (any, *apiErr) {
	rows := BoardBQ("SELECT id, title, body, service, env, kind FROM lessons WHERE state='approved' ORDER BY coalesce(service,''), title, id LIMIT 300")
	services := []string{}
	seen := map[string]bool{}
	for _, r := range rows {
		s := If(r.Str("service") == "", "General", r.Str("service"))
		if !seen[s] {
			seen[s] = true
			services = append(services, s)
		}
	}
	md := ""
	if len(rows) > 0 {
		md = KnowledgeMarkdown(rows)
	}
	return map[string]any{"count": len(rows), "md": md, "services": services, "admin": sess.Role == "admin"}, nil
}

// --- fix runs (admins only, like every /admin page) ---------------------------------------------------------

var errFixAdmin = &apiErr{403, "admins only"}

var reLi = regexp.MustCompile(`(?s)<li>(.*?)</li>`)

// apiFixSteps is the operator's setup list; <code> becomes backticks, which the frontend shows as code.
func apiFixSteps() []string {
	out := []string{}
	for _, m := range reLi.FindAllStringSubmatch(fixerSetupSteps, -1) {
		out = append(out, Text(strings.NewReplacer("<code>", "`", "</code>", "`").Replace(m[1])))
	}
	return out
}

// apiFixWhy is fixerAvailability's explanation as notices, and the operator's steps when it carries them.
func apiFixWhy(why string) ([]apiNotice, []string) {
	steps := []string{}
	if strings.Contains(why, fixerSetupSteps) {
		steps = apiFixSteps()
		why = strings.NewReplacer(fixerSetupSteps, "", "<h3>Operator steps</h3>", "").Replace(why)
	}
	out := apiNotices(why)
	if len(out) == 0 {
		if t := Text(why); t != "" {
			out = append(out, apiNotice{Text: t, Kind: If(strings.Contains(why, "state fail") || strings.Contains(why, `role="alert"`), "fail", "warn")})
		}
	}
	return out, steps
}

// apiFixListJSON is GET /api/v1/fix: the runs still at the dispatcher, then the recorded ones.
func apiFixListJSON(_ py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, errFixAdmin
	}
	recorded := Q("SELECT * FROM fix_runs ORDER BY id DESC LIMIT 100")
	seen := map[string]bool{}
	for _, r := range recorded {
		seen[r.Str("id")] = true
	}
	runs := fixerStatusRuns()
	keys := append([]string(nil), runs.Keys()...)
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	rows := []map[string]any{}
	for _, rid := range keys {
		if seen[rid] || !FixerIDRE.MatchString(rid) {
			continue
		}
		st := py.AsMap(runs.Get(rid))
		started := st.Get("result") == "started"
		rows = append(rows, map[string]any{"id": rid, "at": py.I(py.Or(st.Get("requested"), int64(0))), "env": py.OrS(st.Get("env"), ""), "summary": py.Str(st.GetD("result", "")), "by": py.Str(st.GetD("by", "")),
			"status": map[string]any{"text": If(started, "running", "not started"), "kind": If(started, "warn", "fail")}})
	}
	for _, r := range recorded {
		rows = append(rows, map[string]any{"id": r.Str("id"), "at": py.I(py.Or(r.Get("started_at"), int64(0))), "env": r.S("env"), "summary": FirstLine(r.Get("summary"), 120), "by": r.S("requested_by"),
			"status": apiAgentBadge(Badge(r.Get("status")))})
	}
	sc := fixerScope()
	state := "unknown"
	if sc.Len() > 0 {
		state = If(py.Truthy(sc.Get("set_up")), "set up", "not set up")
	}
	notices := []apiNotice{}
	if py.Truthy(sc.Get("ignored")) {
		notices = append(notices, apiNotice{Text: "The scope file lists " + strings.Join(fixerStrList(sc.Get("ignored")), ", ") + ": ignored, Argus never fixes production.", Kind: "warn"})
	}
	steps := []string{}
	if sc.Len() > 0 && !py.Truthy(sc.Get("set_up")) {
		steps = apiFixSteps()
	}
	return map[string]any{"rows": rows, "state": state, "envs": py.OrS(strings.Join(fixerStrList(py.Or(sc.Get("envs"), []any{})), ", "), "none"), "notices": notices, "steps": steps}, nil
}

// apiFixCells is a JSON list column of a fix report as table rows; a plain item fills every cell, as on the Go page.
func apiFixCells(v any, keys ...string) [][]string {
	out := [][]string{}
	for _, a := range fixerJList(v) {
		row := []string{}
		for _, k := range keys {
			if m := py.AsMap(a); m != nil {
				row = append(row, py.Str(m.GetD(k, "")))
			} else {
				row = append(row, py.Str(a))
			}
		}
		out = append(out, row)
	}
	return out
}

// apiFixRunJSON is GET /api/v1/fix/run?id=: a run waiting at the dispatcher, or its full report.
func apiFixRunJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, errFixAdmin
	}
	rid := qs.Get("id")
	row, st, pending, found := fixerRunFiles(rid)
	if !found {
		return nil, &apiErr{404, "no such fix run"}
	}
	out := map[string]any{"id": rid, "refresh": false, "wait": nil, "report": nil}
	if row == nil {
		out["env"], out["hosts"], out["by"] = py.OrS(st.Get("env"), ""), fixerStrList(py.Or(st.Get("hosts"), []any{})), py.OrS(st.Get("by"), "?")
		result := py.Str(st.GetD("result", ""))
		wait := map[string]any{"text": "", "detail": "", "kind": "fail", "steps": []string{}}
		switch {
		case pending:
			out["refresh"] = true
			wait["text"], wait["kind"] = "Queued: waiting for the dispatcher on "+ARGUS_HOSTNAME+".", "busy"
		case result == "started" && Now()-py.Num(py.Or(st.Get("started"), int64(0))) < FixerStaleRun:
			out["refresh"] = true
			wait["text"], wait["kind"] = "Running since "+Ago(st.Get("started"))+".", "busy"
			wait["detail"] = "Safe to leave this page: the run carries on and its result appears here. It stops after 20 minutes at the latest; the page refreshes itself."
		case result == "started":
			wait["text"] = "No result recorded. Check on " + ARGUS_HOSTNAME + ": journalctl -u ops-fix@" + rid
		case strings.HasPrefix(result, "not set up"):
			wait["text"], wait["steps"] = "Not started: "+result, apiFixSteps()
		default:
			wait["text"] = "Not started: " + py.OrS(result, "unknown state")
		}
		out["wait"] = wait
		return out, nil
	}
	out["env"], out["hosts"], out["by"] = row.S("env"), fixerStrList(fixerJList(row.Get("hosts"))), py.OrS(row.Get("requested_by"), "?")
	status := py.OrS(row.Get("status"), "?")
	rollback := []string{}
	for _, r := range fixerJList(row.Get("rollback")) {
		if m := py.AsMap(r); m != nil {
			rollback = append(rollback, "# "+py.Str(m.GetD("host", "?"))+"\n"+py.Str(m.GetD("command", "")))
		} else {
			rollback = append(rollback, py.Str(r))
		}
	}
	rep := map[string]any{"status": map[string]any{"text": status, "kind": If(status == "OK", "ok", If(status == "WARN", "warn", "fail"))}, "outcome": py.OrS(row.Get("outcome"), "?"),
		"at": py.I(py.Or(row.Get("started_at"), int64(0))), "cost": "$" + py.F(py.Num(py.Or(row.Get("cost_usd"), int64(0))), 2), "turns": py.Str(py.Or(row.Get("turns"), int64(0))), "denied": apiRunCell(row, "denials"),
		"summary": row.S("summary"), "rollback": strings.Join(rollback, "\n"), "actions": apiFixCells(row.Get("actions"), "host", "command", "result"), "backups": apiFixCells(row.Get("backups"), "host", "path"),
		"verification": apiFixCells(row.Get("verification"), "host", "check", "result"), "md": row.S("report"), "prompt": row.S("prompt"), "promptSha": py.Head(row.S("prompt_sha256"), 16)}
	if row.Get("problem_run") != nil && row.Get("problem_idx") != nil {
		rep["problem"] = apiLink{Text: py.OrS(row.Get("problem_title"), "(untitled)"), Href: "/incidents/problem?run=" + I2S(row.I("problem_run")) + "&idx=" + I2S(row.I("problem_idx"))}
	}
	out["report"] = rep
	return out, nil
}

// apiFixReviewJSON is GET /api/v1/fix/review: the prompt to review and the hosts to pick, or why the fixer cannot run here.
func apiFixReviewJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, errFixAdmin
	}
	p := fixerSource(qs.Get)
	if p == nil {
		return nil, &apiErr{404, "no such problem"}
	}
	env := p.S("environment")
	src := map[string]string{}
	if v := qs.Get("rca"); v != "" {
		src["rca"] = v
	} else {
		src["run"], src["idx"] = qs.Get("run"), qs.Get("idx")
	}
	out := map[string]any{"env": env, "title": py.OrS(p.Get("title"), "(untitled)"), "source": src, "ready": false, "steps": []string{}, "notices": []apiNotice{},
		"back": apiLink{Text: py.Str(p.Get("back_label")), Href: strings.ReplaceAll(py.Str(p.Get("back")), "&amp;", "&")}}
	sc := fixerScope()
	switch {
	case env == "release":
		out["notices"] = []apiNotice{{Text: "Production problem: Argus never acts on production. Copy the prompt from the problem page; a human runs production changes.", Kind: "fail"}}
		return out, nil
	case !In(env, FixerEnvs...):
		out["notices"] = []apiNotice{{Text: "Environment " + py.OrS(env, "unknown") + ": only develop and pre-release problems can be fixed by Argus.", Kind: "fail"}}
		return out, nil
	}
	if ok, why := fixerAvailability(env, sc); !ok {
		out["notices"], out["steps"] = apiFixWhy(why)
		return out, nil
	}
	text := p.S("fix_prompt")
	notices := []apiNotice{}
	if marks := FixerProductionMarkers(text); len(marks) > 0 {
		notices = append(notices, apiNotice{Text: "This prompt names production (" + strings.Join(marks, ", ") + "). Remove it: the dispatcher refuses any prompt that names a production host, address or domain.", Kind: "fail"})
	}
	hosts := []string{}
	for _, hv := range py.AsList(py.Or(py.AsMap(sc.Get("hosts")).Get(env), []any{})) {
		if h, ok := hv.(string); ok && fixerAliasRE.MatchString(h) {
			hosts = append(hosts, h)
		}
	}
	out["ready"], out["notices"], out["hosts"], out["prompt"], out["maxBytes"], out["preamble"] = true, notices, hosts, text, FixerMaxPrompt, fixerPreamble()
	return out, nil
}

// apiFixReview is the twin of POST /admin/fix/review: every check, and the request file, stay in FixerPostReview.
func apiFixReview(body map[string]any, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, errFixAdmin
	}
	code, out, loc := FixerPostReview(&Ctx{Sess: sess, Theme: "auto", Path: "/admin/fix/review"}, apiForm(body), sess)
	if loc != "" {
		return apiResult(303, loc)
	}
	return apiResult(code, out)
}
