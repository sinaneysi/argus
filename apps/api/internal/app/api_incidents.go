package app

// The Incidents section as JSON: GET /api/v1/incidents (+ /item, /findings, /run, /finding, /report), /api/v1/alerts,
// POST /api/v1/incidents/act and /deep. Same queries, filters and request files as the pages they replace.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiIncSources(r Row) []apiOption {
	out := []apiOption{}
	for _, k := range QAIncSources(r) {
		out = append(out, apiOption{k, QASourceLabels[k]})
	}
	return out
}

func apiIncRow(r Row, pmap map[string]Row) map[string]any {
	subj := r.S("subject")
	show := ""
	if strings.Contains(subj, ".") || strings.Contains(subj, " ") || strings.Count(subj, "-") < 4 {
		show = subj
	}
	res := r.Get("status") == "resolved"
	last := r.I("last_seen")
	if res {
		last = r.I("resolved_at")
	}
	out := map[string]any{"id": r.I("id"), "title": py.OrS(r.Get("title"), subj), "subject": show, "family": r.S("family"),
		"dc": strings.ToLower(r.S("dc")), "status": r.S("status"), "statusKind": getD(incidentsStatusKind, r.S("status"), "warn"),
		"severity": strings.ToLower(r.S("severity")), "sevKey": incidentsSevKey(r), "env": r.S("env"), "sources": apiIncSources(r),
		"public": r.T("customer_facing"), "hub": incidentsHub(r), "occurrences": r.I("occurrences"), "firstSeen": r.I("first_seen"),
		"last": last, "resolved": res, "owner": r.S("owner")}
	if p, ok := pmap[I2S(r.I("id"))]; ok {
		out["problem"] = map[string]any{"id": p.I("id"), "label": probPID(p.I("id")), "title": probWord[p.S("status")] + ": " + p.S("title")}
	}
	return out
}

// apiIncidentsJSON is GET /api/v1/incidents: the queue with the query parameters of /incidents.
func apiIncidentsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/incidents"}
	l := incidentsQuery(qs)
	now := Now()
	agg := Q("SELECT sum(status='open') o, sum(status='acknowledged') a, sum(status='resolved' AND resolved_at > ?) r7, "+
		"avg(CASE WHEN status='resolved' AND resolved_at > ? THEN resolved_at - first_seen END) mttr, "+
		"sum(occurrences) f, count(*) n FROM incidents", now-7*86400, now-30*86400)
	if len(agg) == 0 || agg[0].Get("n") == nil || agg[0].I("n") == 0 {
		return map[string]any{"empty": true, "emptyText": "They appear after the next investigation, or after the backfill on " + ARGUS_HOSTNAME + "."}, nil
	}
	a := agg[0]
	repeat := 0.0
	if a.T("f") {
		repeat = (a.F("f") - a.F("n")) / a.F("f") * 100
	}
	tile := func(key, label, fig, sub string, hot bool) apiTile {
		t := apiTile{Label: label, Figure: fig, Sub: sub, Hot: hot}
		if key == "" {
			return t
		}
		t.Href = l.with("status", key).href()
		if l.get("status") == key {
			t.Href, t.Current, t.Sub = l.with("status", "").href(), true, "shown below · show active"
		}
		return t
	}
	tiles := []apiTile{tile("open", "Open", I2S(a.I("o")), "not acknowledged yet", a.T("o")),
		tile("acknowledged", "Acknowledged", I2S(a.I("a")), "someone owns it", false),
		tile("", "MTTR, 30 days", incidentsDur(a.Get("mttr")), "first seen → resolved · "+I2S(a.I("r7"))+" resolved in 7 days", false),
		tile("", "Repeat findings", py.F(repeat, 0)+" %", "findings that matched a known incident", false)}
	il := incidentsCompute(l)
	st := apiFacet{Key: "status", Label: "Status", Value: l.get("status"), Options: []apiFacetOpt{}}
	for _, f := range incidentsStatusFacets {
		st.Options = append(st.Options, apiFacetOpt{f[0], f[1], int64(il.stN[f[0]])})
	}
	var src [][2]string
	for _, k := range QASources {
		src = append(src, [2]string{k, QASourceLabels[k]})
	}
	facets := []apiFacet{st, apiFacetOf("sev", "Severity", l.get("sev"), incidentsSevFacets, il.sevN),
		apiFacetOf("env", "Environment", l.get("env"), il.envs, il.envN), apiFacetOf("source", "Source", l.get("source"), src, il.srcN)}
	var pmap map[string]Row
	if ProblemsOn() {
		pmap = probPrimaryMap("incident")
	}
	from, to := listPage(&l, len(il.rows))
	rows := []map[string]any{}
	for _, r := range il.rows[from:to] {
		rows = append(rows, apiIncRow(r, pmap))
	}
	out := map[string]any{"tiles": tiles, "facets": facets, "list": apiListOf(l, len(il.rows), il.total, il.noun, incidentsSorts), "rows": rows,
		"room": MATRIX_QA_ROOM_LABEL, "findingsWord": FindingWord("problems"), "bulk": nil, "banners": apiBannersOf(probSentBanner(c, qs)),
		"refresh": c.Refresh != nil}
	if ProblemBulkOn(c) {
		ps := []apiOption{}
		for _, r := range probOpenRows("", 50) {
			ps = append(ps, apiOption{I2S(r.I("id")), "P-" + I2S(r.I("id")) + " · " + FirstLine(r.S("title"), 60)})
		}
		out["bulk"] = map[string]any{"problems": ps, "back": probBack(c, qs)}
	}
	return out, nil
}

// apiIncSource is QASourceBlock as data: the messages behind the incident, else Argus's summary of its sources.
func apiIncSource(r Row) map[string]any {
	out := map[string]any{"tags": apiIncSources(r), "messages": []map[string]any{}, "lines": []string{}, "note": ""}
	if qaReady() {
		msgs := []map[string]any{}
		for _, m := range Q("SELECT * FROM chat_messages WHERE incident_id=? ORDER BY (source='qa') DESC, ts DESC LIMIT 50", r.I("id")) {
			msgs = append(msgs, map[string]any{"body": qaShownBody(m.Get("body")), "sender": qaSenderName(m), "mxid": m.S("sender"),
				"meta": apiSegs(strings.ReplaceAll(qaMetaLine(m, true), "</span><span>", "</span> · <span>")), "disposition": Text(qaDispBadge(m.Get("disposition"), nil))})
		}
		if len(msgs) > 0 {
			out["messages"] = msgs
			return out
		}
	}
	block := QASourceBlock(r)
	lines := []string{}
	for _, m := range reLI.FindAllStringSubmatch(block, -1) {
		lines = append(lines, Text(m[1]))
	}
	out["lines"] = lines
	if m := regexpSectionNote.FindStringSubmatch(block); m != nil {
		out["note"] = Text(m[1])
	}
	return out
}

// apiIncidentItemJSON is GET /api/v1/incidents/item?id=: everything on the incident page.
func apiIncidentItemJSON(qs py.QS, sess *Session) (any, *apiErr) {
	iid := qs.Get("id")
	if !py.IsDigit(iid) || len(iid) > 9 {
		return nil, &apiErr{404, "no such incident"}
	}
	rr := Q("SELECT * FROM incidents WHERE id=?", py.I(iid))
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such incident"}
	}
	r := rr[0]
	id := r.I("id")
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/incidents/view"}
	admin := sess.Role == "admin"
	status := r.S("status")
	banners := []apiBanner{}
	if s, ok := incidentsSent[qs.Get("sent")]; ok {
		if qs.Get("sent") != "deep" {
			s += " It is applied within a few seconds; reload to see it."
		}
		banners = append(banners, apiBanner{Text: s, Kind: "ok"})
	}
	acts := []string{}
	if status == "open" {
		acts = append(acts, "ack")
	}
	if (status == "open" || status == "acknowledged") && admin {
		acts = append(acts, "resolve")
	}
	if status == "resolved" && admin {
		acts = append(acts, "reopen")
	}
	evs := incidentEvents(r.Get("id"))
	timeline := []map[string]any{}
	var latest Row
	for _, ev := range evs {
		kind, src, detail := incidentEvParts(ev)
		if ev.Get("kind") == "finding" && latest == nil && ev.T("prca") {
			latest = ev
		}
		head, rest, two := strings.Cut(detail, "<br>")
		if !two {
			head, rest = "", detail
		}
		timeline = append(timeline, map[string]any{"at": ev.I("at"), "kind": kind, "source": apiSegs(src), "title": Text(head), "detail": apiSegs(rest)})
	}
	lazy, lzOK := LazyIncident(r)
	var fix map[string]any
	handoff := "fold"
	if status != "resolved" {
		h, src := FixHandoffIncident(r)
		env := FHNormEnv(r.S("env"))
		note := ""
		if src != "" {
			note = "Argus's options for this incident, " + src + "."
		}
		fix = apiFix(h, env, "incident="+I2S(id), incidentFixActions(c, r, h, src, env), note)
		if h.Prompt != "" {
			handoff = "strip"
		}
	}
	var rca map[string]any
	if latest != nil {
		rca = apiMd(latest.Get("prca"), handoff)
		if lzOK {
			rca["md"] = LazyStrip(py.Str(rca["md"]))
		}
		rca["run"], rca["title"], rca["folded"] = latest.I("run_id"), py.OrS(latest.Get("ptitle"), "RCA"), lzOK
	}
	reports := []map[string]any{}
	for _, x := range Q("SELECT * FROM rca_reports WHERE incident_id=? ORDER BY created DESC LIMIT 20", id) {
		reports = append(reports, apiRcaRef(x))
	}
	var deep map[string]any
	if admin {
		ok, msg := rcaDeepState(c, id)
		deep = map[string]any{"can": ok, "message": msg}
	}
	out := map[string]any{"id": id, "title": py.OrS(r.Get("title"), r.S("key")), "status": status, "statusKind": getD(incidentsStatusKind, status, "warn"),
		"severity": strings.ToLower(r.S("severity")), "sevKey": incidentsSevKey(r), "env": r.S("env"), "owner": r.S("owner"), "occurrences": r.I("occurrences"),
		"lastSeen": r.I("last_seen"), "actions": acts, "task": apiBoardBtn("incident", id), "admin": admin, "banners": banners,
		"prod": r.S("env") == "release" && status != "resolved", "lazy": apiLazy(lazy, lzOK), "source": apiIncSource(r), "reports": reports, "deep": deep,
		"fix": fix, "timeline": timeline, "rca": rca, "facts": apiKV(incidentRecord(r)),
		"problem": apiProblemPanel(c, qs, "incident", I2S(id), py.OrS(r.Get("title"), r.S("key")), "incident", id)}
	out["refresh"] = c.Refresh != nil
	return out, nil
}

// apiFindingsJSON is GET /api/v1/incidents/findings: the single findings, merged reports and log-rca runs.
func apiFindingsJSON(_ py.QS, _ *Session) (any, *apiErr) {
	findings, files, runs := []map[string]any{}, []apiLink{}, []map[string]any{}
	for _, p := range Q(`SELECT p.*, r.started_at FROM triage_problems p JOIN runs r ON r.id = p.run_id ORDER BY r.started_at DESC, p.idx LIMIT 100`) {
		findings = append(findings, map[string]any{"at": p.I("started_at"), "severity": py.OrS(p.Get("severity"), "?"), "sevKind": getD(sevKind, p.S("severity"), "warn"),
			"env": p.S("environment"), "title": py.OrS(p.Get("title"), "(untitled)"), "suspicious": p.T("suspicious"),
			"href": "/incidents/problem?run=" + p.Str("run_id") + "&idx=" + p.Str("idx")})
	}
	for _, f := range RcaFiles() {
		files = append(files, apiLink{Text: py.Slice(f, 0, -3), Href: "/incidents/rca?f=" + py.Quote(f)})
	}
	for _, r := range Q("SELECT * FROM runs WHERE task='log-rca' ORDER BY started_at DESC LIMIT 100") {
		runs = append(runs, map[string]any{"at": r.I("started_at"), "status": r.S("status"), "summary": FirstLine(r.Get("summary"), 140),
			"href": "/incidents/run?id=" + r.Str("id"), "mr": apiSafeHref(r.S("mr_url"))})
	}
	return map[string]any{"word": FindingWord("Problems"), "room": MATRIX_QA_ROOM_LABEL, "findings": findings, "files": files, "runs": runs}, nil
}

// apiIncidentRunJSON is GET /api/v1/incidents/run?id=: one log-rca run and the alerts firing then.
func apiIncidentRunJSON(qs py.QS, _ *Session) (any, *apiErr) {
	id := int64(0)
	if v := qs.Get("id"); py.IsDigit(v) && len(v) <= 12 {
		id = py.I(v)
	}
	rr := Q("SELECT * FROM runs WHERE id=? AND task='log-rca'", id)
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such run"}
	}
	r := rr[0]
	alerts := []map[string]any{}
	for _, a := range Q("SELECT * FROM alerts WHERE run_id=? ORDER BY active_at", r.Get("id")) {
		alerts = append(alerts, map[string]any{"since": a.S("active_at"), "severity": a.S("severity"), "name": a.S("alertname"), "target": a.S("target")})
	}
	return map[string]any{"id": r.I("id"), "at": r.I("started_at"), "status": r.S("status"), "md": apiMd(r.Get("summary"), "fold"), "alerts": alerts}, nil
}

// apiFindingJSON is GET /api/v1/incidents/finding?run=&idx=: one finding of a run.
func apiFindingJSON(qs py.QS, sess *Session) (any, *apiErr) {
	run, idx := qs.GetD("run", "0"), qs.GetD("idx", "0")
	if !py.IsDigit(run) || !py.IsDigit(idx) || len(run) > 9 || len(idx) > 3 {
		return nil, &apiErr{404, "no such finding"}
	}
	rn, ix := py.I(run), py.I(idx)
	rr := Q(`SELECT p.*, r.started_at FROM triage_problems p JOIN runs r ON r.id = p.run_id WHERE p.run_id=? AND p.idx=?`, rn, ix)
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such finding"}
	}
	p := rr[0]
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/incidents/problem"}
	sources := []string{}
	if v, err := py.ParseJSON([]byte(py.OrS(p.Get("sources"), "[]"))); err == nil {
		switch x := v.(type) {
		case []any:
			for _, s := range x {
				sources = append(sources, py.Str(s))
			}
		case *py.OMap:
			sources = append(sources, x.Keys()...)
		}
	}
	h := FixHandoffProblem(p)
	pd := Row{}
	for k, v := range p {
		pd[k] = v
	}
	pd["fix_prompt"] = h.Prompt
	lazy, lzOK := LazyRow(p, "rca")
	body := p.S("rca")
	if lzOK {
		body = LazyStrip(body)
	}
	rt, rid, noun := findingRef(rn, ix)
	var inc int64
	if e := probQ1("SELECT incident_id FROM incident_events WHERE run_id=? AND idx=? LIMIT 1", rn, ix); e != nil {
		inc = e.I("incident_id")
	}
	level := py.OrS(p.Get("severity"), "?")
	out := map[string]any{"title": py.OrS(p.Get("title"), "(untitled)"), "severity": level, "sevKind": getD(sevKind, level, "warn"),
		"env": py.OrS(p.Get("environment"), "unknown"), "at": p.I("started_at"), "suspicious": p.T("suspicious"), "prod": p.S("environment") == "release",
		"summary": p.S("summary"), "sources": sources, "lazy": apiLazy(lazy, lzOK), "md": apiMd(body, "strip"),
		"fix":     apiFix(h, p.S("environment"), "run="+I2S(p.I("run_id"))+"&idx="+I2S(p.I("idx")), FixerProblemActions(c, pd), ""),
		"problem": apiProblemPanel(c, qs, rt, rid, py.OrS(p.Get("title"), "Finding"), noun, inc)}
	out["refresh"] = c.Refresh != nil
	return out, nil
}

// --- alerts -----------------------------------------------------------------------------------------------------

// apiAlertsJSON is GET /api/v1/alerts: the snapshot with the query parameters of /alerts.
func apiAlertsJSON(qs py.QS, _ *Session) (any, *apiErr) {
	snap, rows := AlertsSnapshot()
	l := alertsQuery(qs)
	al := alertsCompute(l, rows)
	probs := alertsFindings()
	from, to := listPage(&l, len(al.shown))
	list := []map[string]any{}
	for _, a := range al.shown[from:to] {
		var since int64
		if ts, ok := IsoTS(a.Get("activeAt")); ok && ts != 0 {
			since = int64(ts)
		}
		sev := strings.ToLower(py.OrS(a.Get("severity"), "none"))
		rank, ok := SEV_RANK[sev]
		if !ok {
			rank = 4
		}
		list = append(list, map[string]any{"name": py.Str(a.GetD("alertname", "")), "pending": a.Get("state") != "firing", "summary": py.OrS(a.Get("summary"), ""),
			"severity": sev, "sevKind": map[int]string{0: "fail", 1: "fail", 2: "warn"}[rank], "since": since, "activeAt": py.Str(a.GetD("activeAt", "")),
			"dc": py.Str(a.Get("dc")), "instance": py.Str(a.GetD("instance", "-")), "value": Num(a.Get("value")), "rca": alertsFindingHref(a, probs)})
	}
	firing := 0
	sevN := map[string]int{}
	for _, a := range rows {
		if a.Get("state") == "firing" {
			firing++
			sevN[alertsSevKey(a)]++
		}
	}
	tiles := []apiTile{}
	for _, g := range []struct {
		k, key string
		hot    bool
	}{{"Critical", "critical", true}, {"Page", "page", true}, {"Error", "error", true}, {"Warning", "warning", false}, {"Info", "info", false}} {
		n := sevN[g.key]
		t := apiTile{Label: g.k, Figure: Itoa(n), Sub: If(n > 0, "firing now", "none firing"), Href: l.cleared().with("sev", g.key).href(), Hot: g.hot && n > 0}
		if g.key == l.get("sev") && l.get("state") == "firing" {
			t.Href, t.Current, t.Sub = l.with("sev", "").href(), true, "shown below · show all"
		}
		tiles = append(tiles, t)
	}
	st := apiFacet{Key: "state", Label: "State", Value: l.get("state"), Options: []apiFacetOpt{}}
	for _, f := range alertsStateFacets {
		st.Options = append(st.Options, apiFacetOpt{f[0], f[1], int64(al.stN[f[0]])})
	}
	total := 0
	for _, a := range rows {
		if l.get("state") == "all" || a.Get("state") == l.get("state") {
			total++
		}
	}
	noun := map[string]string{"firing": "firing alerts", "pending": "pending alerts", "all": "alerts"}[l.get("state")]
	var bad []string
	for _, dv := range py.AsList(snap.GetD("dcs", []any{})) {
		if d := py.AsMap(dv); !py.Truthy(d.Get("ok")) {
			bad = append(bad, py.Str(d.Get("dc")))
		}
	}
	age := Now() - py.Num(snap.GetD("fetched_at", int64(0)))
	warn := ""
	if snap.Len() > 0 && (age > 300 || len(bad) > 0) {
		warn = "Alert data is " + I2S(int64(age/60)) + " min old"
		if len(bad) > 0 {
			warn += "; could not read: " + strings.Join(bad, ", ")
		}
	}
	return map[string]any{"loaded": snap.Len() > 0, "host": ARGUS_HOSTNAME, "warn": warn, "firing": firing, "pending": len(rows) - firing,
		"fetchedAt": py.I(snap.GetD("fetched_at", int64(0))), "tiles": tiles,
		"facets": []apiFacet{st, apiFacetOf("sev", "Severity", l.get("sev"), al.sevs, al.sevN), apiFacetOf("dc", "Datacenter", l.get("dc"), al.dcs, al.dcN)},
		"list":   apiListOf(l, len(al.shown), total, noun, alertsSorts), "rows": list, "sites": ObsSiteLabels()}, nil
}

// --- writes -----------------------------------------------------------------------------------------------------

// incPost answers the Incidents section's writes through the pages' handlers (same roles, checks and request files).
func incPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/incidents"}
	form := apiForm(body)
	switch path {
	case "/api/v1/incidents/act":
		return apiResult(IncidentsPostAction(c, form, sess))
	case "/api/v1/incidents/deep":
		return apiResult(RcaPostDeep(c, form, sess))
	}
	return apiResult(ProblemsPostAct(c, form, sess))
}

// --- Today's cards ----------------------------------------------------------------------------------------------

// apiTodayIncidents is Today's "Open incidents" card: the worst active incident with its stage, the others as rows.
func apiTodayIncidents() map[string]any {
	out := map[string]any{"total": 0, "open": 0, "top": nil, "rows": []map[string]any{}}
	defer func() { recover() }() // a fresh install has no incidents table yet
	rows := Q("SELECT * FROM incidents WHERE status IN ('open','acknowledged') ORDER BY id DESC LIMIT " + Itoa(incidentsMaxRows))
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "none": 4}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if ox, oy := x.S("status") == "open", y.S("status") == "open"; ox != oy {
			return ox
		}
		if si, sj := rank[incidentsSevKey(x)], rank[incidentsSevKey(y)]; si != sj {
			return si < sj
		}
		return x.F("last_seen") > y.F("last_seen")
	})
	nOpen := 0
	list := []map[string]any{}
	for i, r := range rows {
		if r.S("status") == "open" {
			nOpen++
		}
		if i == 0 || i > 4 {
			continue
		}
		list = append(list, map[string]any{"id": r.I("id"), "title": py.OrS(r.Get("title"), r.S("subject")), "status": r.S("status"), "sevKey": incidentsSevKey(r), "env": r.S("env"), "last": r.I("last_seen")})
	}
	out["total"], out["open"], out["rows"] = len(rows), nOpen, list
	if len(rows) > 0 {
		r := rows[0]
		since := r.I("first_seen")
		if r.S("status") == "acknowledged" && r.T("acked_at") {
			since = r.I("acked_at")
		}
		out["top"] = map[string]any{"id": r.I("id"), "title": py.OrS(r.Get("title"), r.S("subject")), "status": r.S("status"), "sevKey": incidentsSevKey(r), "env": r.S("env"),
			"firstSeen": r.I("first_seen"), "ackedAt": r.I("acked_at"), "ackedBy": r.S("acked_by"), "stageSince": since, "stageFor": incidentsDur(Now() - float64(since))}
	}
	return out
}

// apiTodayChecks is Today's "Checks, last 48 h" card: scheduled runs per hour from the runs table, split OK and not OK.
func apiTodayChecks() map[string]any {
	now := NowInt()
	start := (now/3600 - 47) * 3600
	type bucket struct {
		At   int64 `json:"at"`
		OK   int64 `json:"ok"`
		Fail int64 `json:"fail"`
	}
	hours := make([]bucket, 48)
	for i := range hours {
		hours[i].At = start + int64(i)*3600
	}
	func() {
		defer func() { recover() }() // a fresh install has no runs table yet
		for _, r := range Q("SELECT started_at, status FROM runs WHERE started_at>=? LIMIT 20000", start) {
			i := (r.I("started_at") - start) / 3600
			if i < 0 || i > 47 {
				continue
			}
			st := r.S("status")
			if st == "OK" || strings.HasPrefix(strings.ToLower(st), "skip") {
				hours[i].OK++
			} else {
				hours[i].Fail++
			}
		}
	}()
	var total, failed int64
	worst := -1
	for i, h := range hours {
		total += h.OK + h.Fail
		failed += h.Fail
		if h.Fail > 0 && (worst < 0 || h.Fail > hours[worst].Fail) {
			worst = i
		}
	}
	return map[string]any{"hours": hours, "total": total, "failed": failed, "worst": worst}
}
