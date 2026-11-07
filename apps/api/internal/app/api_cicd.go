package app

// The Delivery section's CI/CD pages as JSON: GET /api/v1/cicd (+ /failures, /stuck, /audit, /project, /finding). Same
// queries and rules as the Go pages they replace. Read-only: Argus never retries, cancels or plays a pipeline.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiCicdStatus(st any) apiPill {
	s := py.OrS(st, "")
	return apiPill{Text: strings.ReplaceAll(py.OrS(st, "-"), "_", " "), Kind: cicdGet(cicdStatusKind, s, "warn")}
}

// apiCicdStale is cicdStaleBanner as banners.
func apiCicdStale(st *py.OMap) []apiBanner {
	w := py.AsMap(py.Or(st.Get("watch"), nil))
	if w.Len() == 0 {
		return []apiBanner{{Text: "CI/CD data has not loaded: the watcher has written none yet. Failures, stuck pipelines and audits appear here after its first run (every 5 minutes). If this stays, check that argus-cicd-watch.timer is installed on " + ARGUS_HOSTNAME + ".", Kind: "fail"}}
	}
	if Now()-py.Num(py.Or(w.Get("at"), int64(0))) > 30*60 {
		return []apiBanner{{Text: "CI/CD data is out of date: the watcher last ran " + Ago(w.Get("at")) + ". It should run every 5 minutes, so what follows may be stale. Check argus-cicd-watch.timer on " + ARGUS_HOSTNAME + ".", Kind: "fail"}}
	}
	if w.Get("status") != "OK" {
		return []apiBanner{{Text: "The last watcher run ended with: " + py.OrS(w.Get("note"), "WARN"), Kind: "warn"}}
	}
	return []apiBanner{}
}

// apiCicdFinding is one finding row, for the plain rows and the Failures queue alike.
func apiCicdFinding(f Row) map[string]any {
	kind := cicdGet(cicdSevKind, f.S("severity"), "warn")
	if kind == "ok" && f.S("status") != "resolved" && f.S("status") != "closed" {
		kind = "warn" // an open finding is never green, whatever its severity
	}
	kk := py.OrS(py.Or(f.Get("klass"), f.Get("kind")), "")
	proposed := ""
	if props := cicdProposals(f); len(props) > 0 {
		proposed = py.OrS(props[0].Get("text"), "")
	}
	return map[string]any{"id": f.I("id"), "title": f.Str("title"), "kind": kind, "klass": cicdGet(cicdClassLbl, kk, kk), "severity": strings.ToLower(f.S("severity")),
		"status": f.S("status"), "env": f.S("env"), "lastSeen": f.I("last_seen"), "firstSeen": f.I("first_seen"), "resolvedAt": f.I("resolved_at"),
		"resolveNote": f.S("resolve_note"), "occurrences": max(f.I("occurrences"), 1), "project": f.S("project"), "projectId": f.I("project_id"),
		"job": f.S("job_name"), "author": f.S("author"), "mr": f.S("mr"), "detail": f.S("detail"), "proposed": proposed, "task": apiBoardBtn("cicd", f.I("id"))}
}

func apiCicdFindings(rs []Row) []map[string]any {
	out := []map[string]any{}
	for _, f := range rs {
		out = append(out, apiCicdFinding(f))
	}
	return out
}

// apiCicdCell is one project on one env branch, with the states the view filters use.
func apiCicdCell(p Row, env string, m *cicdMatrix) (map[string]any, map[string]bool) {
	k := csKey{p.I("id"), env}
	if b, ok := m.brs[k]; !ok || !b.T("present") {
		return map[string]any{"env": env, "branch": false}, map[string]bool{}
	}
	pl, d := m.last[k], m.dep[k]
	flags := cicdCellFlags(pl, m.openF[k], env)
	out := map[string]any{"env": env, "branch": true, "status": nil, "sha": "", "at": 0, "deployedAt": 0, "regression": false, "tags": []string{}}
	if pl != nil {
		out["status"], out["sha"], out["at"] = apiCicdStatus(pl.Get("status")), py.Head(pl.S("sha"), 8), pl.I("created_at")
	}
	if d != nil {
		out["deployedAt"], out["regression"] = d.I("at"), d.Get("verify_state") == "regression"
	}
	tags := []string{}
	for _, t := range []string{"drift", "stale", "stuck"} {
		if flags[t] {
			tags = append(tags, t)
		}
	}
	out["tags"] = tags
	return out, flags
}

func apiRepoLink(pid int64, rmap map[int64][2]string, path string) map[string]any {
	out := map[string]any{"href": "/cicd/repo?" + py.UrlencodeKV("project", path), "pill": nil}
	if v, ok := rmap[pid]; ok {
		out["pill"] = apiPill{Text: cicdGet(repoShort, v[0], v[0]), Kind: repoVerdictOf(v[0])[0]}
	}
	return out
}

func apiCicdEnvCards(s *CSSummary, kind string) map[string]any {
	noun := If(kind == "package", "packages", "repos")
	cards := []map[string]any{}
	for _, env := range CSEnvs {
		c := s.Envs[env]
		total := max(c["total"], 1)
		x := 0.0
		segs, labs := []map[string]any{}, []string{}
		for _, st := range cicdBarOrder {
			if w := float64(c[st]) / float64(total) * 100; w > 0 {
				segs = append(segs, map[string]any{"state": st, "x": x, "w": w})
				x += w
			}
			if c[st] != 0 {
				labs = append(labs, I2S(c[st])+" "+strings.ToLower(CSLabel[st]))
			}
		}
		rows := []map[string]any{}
		for _, st := range []string{"failed", "waiting", "canceled", "nopipeline", "nobranch"} {
			note := ""
			if st == "canceled" && c["skipped_only"] != 0 {
				note = I2S(c["skipped_only"]) + " only [skip ci]"
			}
			rows = append(rows, map[string]any{"state": st, "label": CSLabelOf(st, env), "count": c[st], "href": cicdSummaryLink(env, st, kind),
				"hot": st == "failed" && c[st] != 0 && env != "develop", "note": note})
		}
		cards = append(cards, map[string]any{"env": env, "branches": c["branches"], "success": c["success"], "href": cicdSummaryLink(env, "success", kind),
			"bar": segs, "barLabel": strings.Join(labs, ", "), "rows": rows})
	}
	return map[string]any{"cards": cards, "noun": noun, "kind": kind, "count": len(s.IDs), "engine": CICD_ENGINE_PROJECT, "days": s.Days,
		"since": Strftime("2006-01-02", float64(s.Since))}
}

// apiCicdJSON is /cicd: what needs someone, the figures, the environments and the estate matrix.
func apiCicdJSON(qs py.QS, _ *Session) (any, *apiErr) {
	view := qs.GetD("view", "attention")
	if !In(view, "attention", "failing", "stuck", "stale", "all") {
		view = "attention"
	}
	grp := qs.Get("group")
	if !cicdGroupRE.MatchString(grp) {
		grp = ""
	}
	kind := qs.GetD("kind", "app")
	if !In(kind, CSKinds...) {
		kind = "app"
	}
	fenv, fstate := qs.Get("env"), qs.Get("state")
	if !In(fenv, cicdEnvs...) || !In(fstate, CSStates...) {
		fenv, fstate = "", ""
	}
	summ := CSSummarize(kind, 0)
	var pick map[int64]string
	if summ != nil && fenv != "" {
		pick = summ.State[fenv]
	}
	m := cicdMatrixData()
	_, st := cicdStatusLine()
	fails := Q("SELECT * FROM cicd_findings WHERE status='open' AND kind IN ('failure','deploy') AND klass NOT IN " +
		"('flaky','canceled') ORDER BY " + cicdEnvRank + ", last_seen DESC")
	stuck := Q("SELECT * FROM cicd_findings WHERE status='open' AND kind='stuck' ORDER BY last_seen DESC")
	branch := Q("SELECT count(*) n FROM cicd_findings WHERE status='open' AND kind='branch' AND severity!='info'")
	stale := Q("SELECT count(*) n FROM cicd_findings WHERE status='open' AND kind='branch' AND rule='stale'")
	deps := Q("SELECT env, count(*) n FROM cicd_deploys WHERE at > ? GROUP BY env", NowInt()-86400)
	sec := Q("SELECT * FROM cicd_findings WHERE status='open' AND kind IN ('survey','variable') AND severity IN " +
		"('critical','high') ORDER BY CASE severity WHEN 'critical' THEN 0 ELSE 1 END, last_seen DESC")
	nrel := 0
	for _, f := range fails {
		if f.Get("env") == "release" {
			nrel++
		}
	}
	var crit []Row
	for _, f := range sec {
		if f.Get("severity") == "critical" {
			crit = append(crit, f)
		}
	}
	vst := py.NewOMap()
	if vars := Q("SELECT v FROM cicd_state WHERE k='vars'"); len(vars) > 0 {
		vst = py.AsMap(cicdJSON(vars[0].Get("v")))
	}
	var ndep int64
	depBits := []string{}
	for _, r := range deps {
		ndep += r.I("n")
		depBits = append(depBits, r.Str("env")+" "+r.Str("n"))
	}
	var bn, sn int64
	if len(branch) > 0 {
		bn = branch[0].I("n")
	}
	if len(stale) > 0 {
		sn = stale[0].I("n")
	}
	tiles := []apiTile{
		{Label: "Failing now", Figure: Itoa(len(fails)), Sub: Itoa(nrel) + " on release", Href: "/cicd/failures", Hot: nrel > 0},
		{Label: "Stuck pipelines", Figure: Itoa(len(stuck)), Sub: "lock wait > " + cicdStuckMinutes() + " min", Href: "/cicd/stuck", Hot: len(stuck) > 0},
		{Label: "Branch findings", Figure: I2S(bn), Sub: I2S(sn) + " stale env branches", Href: "/cicd/audit"},
		{Label: "Deploys, 24 h", Figure: I2S(ndep), Sub: py.OrS(strings.Join(depBits, " · "), "none"), Href: "#matrix"},
		{Label: "Security and config", Figure: Itoa(len(sec)), Sub: Itoa(len(crit)) + " critical" + If(vst.Get("mode") != "full", " · variables inferred", ""),
			Href: "/cicd/audit#security", Hot: len(crit) > 0}}
	var att []Row
	att = append(att, cicdHead(crit, 3)...)
	att = append(att, cicdHead(fails, 6)...)
	att = append(att, cicdHead(stuck, 3)...)

	rmap := RepoReadinessMap()
	counts, grpN := map[string]int{}, map[string]int{}
	gset := map[string]bool{}
	rows := []map[string]any{}
	for _, p := range m.projs {
		if p.T("grp") {
			gset[p.Str("grp")] = true
		}
		if pick != nil && pick[p.I("id")] != fstate {
			continue
		}
		cells := []map[string]any{}
		flags := map[string]bool{}
		for _, env := range cicdEnvs {
			cc, fl := apiCicdCell(p, env, m)
			cells = append(cells, cc)
			for k := range fl {
				flags[k] = true
			}
		}
		if len(m.openF[csKey{p.I("id"), ""}]) > 0 {
			flags["finding"] = true
		}
		need := flags["failing"] || flags["stuck"] || flags["reldrift"] || flags["finding"]
		viewOK := pick != nil || !((view == "attention" && !need) || (In(view, "failing", "stuck", "stale") && !flags[view]))
		if viewOK {
			grpN[p.Str("grp")]++
		}
		if grp != "" && p.Get("grp") != grp {
			continue
		}
		counts["all"]++
		if need {
			counts["attention"]++
		}
		for _, k := range []string{"failing", "stuck", "stale"} {
			if flags[k] {
				counts[k]++
			}
		}
		if !viewOK {
			continue
		}
		inc := ""
		if p.T("include_ref") && p.Get("include_ref") != "main" {
			inc = p.Str("include_ref")
		}
		rows = append(rows, map[string]any{"id": p.I("id"), "name": cicdShortPath(p.Str("path")), "executor": py.OrS(py.Or(p.Get("executor"), p.Get("kind")), "-"),
			"include": inc, "readiness": apiRepoLink(p.I("id"), rmap, p.Str("path")), "cells": cells})
	}
	groups := []string{}
	for g := range gset {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	facets := []apiFacet{}
	if pick == nil {
		show := apiFacet{Key: "view", Label: "Show projects", Value: view, Options: []apiFacetOpt{}}
		for _, n := range [][2]string{{"attention", "Needs attention"}, {"failing", "Failing"}, {"stuck", "Stuck"}, {"stale", "Stale"}, {"all", "All"}} {
			show.Options = append(show.Options, apiFacetOpt{n[0], n[1], int64(counts[n[0]])})
		}
		facets = append(facets, show)
	}
	gvals := [][2]string{}
	for _, g := range groups {
		gvals = append(gvals, [2]string{g, g})
	}
	if len(groups) > 0 {
		facets = append(facets, apiFacetOf("group", "Group", grp, gvals, grpN))
	}
	hidden := map[string]string{}
	for k, v := range map[string]string{"env": fenv, "state": fstate, "kind": If(kind != "app", kind, "")} {
		if v != "" {
			hidden[k] = v
		}
	}
	href := func(page int) string {
		pairs := [][2]string{}
		for _, kv := range [][2]string{{"view", view}, {"group", grp}, {"env", fenv}, {"state", fstate}, {"kind", If(kind != "app", kind, "")}} {
			if kv[1] != "" {
				pairs = append(pairs, kv)
			}
		}
		if page > 1 {
			pairs = append(pairs, [2]string{"page", Itoa(page)})
		}
		return "/cicd?" + py.Urlencode(pairs) + "#matrix"
	}
	n := len(rows)
	page := queueClamp(queuePageNum(qs.Get("page")), int64(n))
	pages := max(1, (n+queuePageSize-1)/queuePageSize)
	from, to := min((page-1)*queuePageSize, n), min(page*queuePageSize, n)
	list := map[string]any{"path": "/cicd#matrix", "q": "", "sort": "", "sorts": []apiOption{}, "noSearch": true, "filtered": grp != "" || pick != nil,
		"clearHref": "/cicd#matrix", "matched": n, "total": len(m.projs), "noun": "projects", "page": page, "pages": pages,
		"from": min(from+1, n), "to": to, "hidden": hidden}
	if page > 1 {
		list["prev"] = href(page - 1)
	}
	if page < pages {
		list["next"] = href(page + 1)
	}
	out := map[string]any{"banners": apiCicdStale(st), "tiles": tiles, "attention": apiCicdFindings(att), "attentionTotal": len(crit) + len(fails) + len(stuck),
		"failing": len(fails), "onRelease": nrel, "stuck": len(stuck), "envs": nil, "kindLinks": []apiLink{{Text: "Apps", Href: "/cicd"}, {Text: "Packages", Href: "/cicd?kind=package"}},
		"facets": facets, "list": list, "rows": rows[from:to], "pick": nil, "engine": CICD_ENGINE_PROJECT, "writes": cicdWriteNote(), "readiness": nil,
		"placeholder": repoGitlabURL + "/group/project"}
	if summ != nil {
		out["envs"] = apiCicdEnvCards(summ, kind)
	}
	if pick != nil {
		out["pick"] = map[string]any{"count": n, "noun": If(kind == "package", "packages", "repos"), "env": fenv, "state": strings.ToLower(CSLabelOf(fstate, fenv)),
			"clear": "/cicd?" + py.UrlencodeKV("view", view) + "#matrix"}
	}
	ids := make([]int64, 0, len(m.projs))
	for _, p := range m.projs {
		ids = append(ids, p.I("id"))
	}
	if line := RepoCountsLine(rmap, ids); line != "" {
		out["readiness"] = strings.TrimSpace(strings.TrimSuffix(Text(line), "Check a repo"))
	}
	return out, nil
}

// apiCicdFailuresJSON is /cicd/failures: the Open and Resolved queue.
func apiCicdFailuresJSON(qs py.QS, _ *Session) (any, *apiErr) {
	f := cicdFailParse(qs)
	tabN := f.counts("view", "CASE WHEN status='open' THEN 'open' ELSE 'resolved' END")
	var totOpen, rel, matched, inView int64
	if r := Q("SELECT count(*) n, sum(env='release') rel FROM cicd_findings WHERE kind IN ('failure','deploy') AND status='open'"); len(r) > 0 {
		totOpen, rel = r[0].I("n"), r[0].I("rel")
	}
	w, a := f.where("")
	if r := Q("SELECT count(*) n FROM cicd_findings WHERE "+w, a...); len(r) > 0 {
		matched = r[0].I("n")
	}
	unf := cicdFailQuery{view: f.view, sort: f.sort, page: 1}
	uw, ua := unf.where("")
	if r := Q("SELECT count(*) n FROM cicd_findings WHERE "+uw, ua...); len(r) > 0 {
		inView = r[0].I("n")
	}
	f.page = queueClamp(f.page, matched)
	order := queueSortKey(cicdFailSorts, f.sort).order
	if f.view == "resolved" {
		order = "resolved_at DESC, id DESC"
	}
	rows := Q("SELECT * FROM cicd_findings WHERE "+w+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(a, queuePageSize, (f.page-1)*queuePageSize)...)
	ints := func(m map[string]int64) map[string]int {
		o := map[string]int{}
		for k, v := range m {
			o[k] = int(v)
		}
		return o
	}
	classN := f.counts("class", "coalesce(klass,'')")
	classes := [][2]string{}
	for k := range classN {
		if lbl, ok := cicdClassLbl[k]; ok {
			classes = append(classes, [2]string{k, lbl})
		}
	}
	if f.class != "" && classN[f.class] == 0 {
		classes = append(classes, [2]string{f.class, cicdClassLbl[f.class]})
	}
	sort.Slice(classes, func(i, j int) bool {
		if x, y := classN[classes[i][0]], classN[classes[j][0]]; x != y {
			return x > y
		}
		return classes[i][1] < classes[j][1]
	})
	envs := [][2]string{{"release", "release"}, {"pre-release", "pre-release"}, {"develop", "develop"}, {"none", "No env"}}
	facets := []apiFacet{apiFacetOf("env", "Environment", f.env, envs, ints(f.counts("env", "coalesce(nullif(env,''),'none')"))),
		apiFacetOf("class", "Class", f.class, classes, ints(classN))}
	sorts := []apiOption{}
	if f.view == "open" {
		for _, s := range cicdFailSorts {
			sorts = append(sorts, apiOption{s.key, s.label})
		}
	}
	pages := max(1, int((matched+queuePageSize-1)/queuePageSize))
	hidden := map[string]string{}
	if f.view != "open" {
		hidden["view"] = f.view
	}
	list := map[string]any{"path": "/cicd/failures", "q": f.q, "sort": f.sort, "sorts": sorts, "filtered": f.filtered(), "clearHref": unf.href(), "matched": matched,
		"total": inView, "noun": f.view, "page": f.page, "pages": pages, "from": min(int64((f.page-1)*queuePageSize+1), matched),
		"to": min(int64(f.page*queuePageSize), matched), "hidden": hidden}
	if f.page > 1 {
		list["prev"] = f.with(func(g *cicdFailQuery) { g.page = f.page - 1 }).href()
	}
	if f.page < pages {
		list["next"] = f.with(func(g *cicdFailQuery) { g.page = f.page + 1 }).href()
	}
	tabs := []map[string]any{
		{"label": "Open", "count": tabN["open"], "href": f.with(func(g *cicdFailQuery) { g.view = "open" }).href(), "on": f.view == "open"},
		{"label": "Resolved", "count": tabN["resolved"], "href": f.with(func(g *cicdFailQuery) { g.view = "resolved" }).href(), "on": f.view == "resolved"}}
	return map[string]any{"open": totOpen, "onRelease": rel, "view": f.view, "tabs": tabs, "facets": facets, "list": list, "rows": apiCicdFindings(rows),
		"noTools": tabN["open"]+tabN["resolved"] == 0 && !f.filtered()}, nil
}

// apiCicdStuckJSON is /cicd/stuck.
func apiCicdStuckJSON(_ py.QS, _ *Session) (any, *apiErr) {
	rows := Q("SELECT * FROM cicd_findings WHERE kind='stuck' AND status='open' ORDER BY last_seen DESC")
	rel := Q("SELECT pl.*, p.path FROM cicd_pipelines pl JOIN cicd_projects p ON p.id=pl.project_id WHERE pl.env='release' "+
		"AND pl.status='running' AND pl.created_at < ? ORDER BY pl.created_at", NowInt()-3600)
	waiting := Q("SELECT * FROM cicd_findings WHERE kind='release' AND rule='waiting-manual' AND status='open' ORDER BY first_seen")
	running := []map[string]any{}
	for _, r := range rel {
		running = append(running, map[string]any{"projectId": r.I("project_id"), "project": cicdShortPath(r.Str("path")), "id": r.I("id"),
			"sha": py.Head(r.S("sha"), 8), "at": r.I("created_at")})
	}
	return map[string]any{"rows": apiCicdFindings(rows), "waiting": apiCicdFindings(waiting), "running": running, "minutes": cicdStuckMinutes(),
		"engineId": CICD_ENGINE_PROJECT_ID}, nil
}

// apiCicdAuditJSON is /cicd/audit: security and configuration, branch findings per rule, variables.
func apiCicdAuditJSON(_ py.QS, _ *Session) (any, *apiErr) {
	sec := Q("SELECT * FROM cicd_findings WHERE kind='survey' AND status='open' ORDER BY CASE severity WHEN 'critical' THEN 0 " +
		"WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, rule")
	rows := Q("SELECT * FROM cicd_findings WHERE kind IN ('branch','release') AND rule != 'waiting-manual' AND status='open' ORDER BY " +
		"CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END, project")
	vrows := Q("SELECT * FROM cicd_findings WHERE status='open' AND (kind='variable' OR klass='variable') ORDER BY " + cicdEnvRank)
	vst := py.NewOMap()
	if v := Q("SELECT v FROM cicd_state WHERE k='vars'"); len(v) > 0 {
		vst = py.AsMap(cicdJSON(v[0].Get("v")))
	}
	sections := []map[string]any{}
	for _, rt := range cicdAuditRules {
		var items []Row
		for _, r := range rows {
			if r.Get("rule") == rt[0] {
				items = append(items, r)
			}
		}
		if len(items) == 0 {
			continue
		}
		sevo := func(f Row) int {
			if v, ok := cicdSevOrder[f.S("severity")]; ok {
				return v
			}
			return 9
		}
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if sevo(a) != sevo(b) {
				return sevo(a) < sevo(b)
			}
			if ra, rb := a.Get("env") != "release", b.Get("env") != "release"; ra != rb {
				return !ra
			}
			return a.S("project") < b.S("project")
		})
		var lead, more []Row
		for _, f := range items {
			if In(rt[0], "drift", "unprotected", "devpush", "head-not-deployed", "missing") && len(lead) < 5 && In(f.S("severity"), "critical", "high", "medium") {
				lead = append(lead, f)
			} else {
				more = append(more, f)
			}
		}
		sections = append(sections, map[string]any{"rule": rt[0], "title": rt[1], "count": len(items), "lead": apiCicdFindings(lead),
			"more": apiCicdFindings(cicdHead(more, 200))})
	}
	var top, rest []Row
	for _, f := range sec {
		if In(f.S("severity"), "critical", "high") {
			top = append(top, f)
		} else {
			rest = append(rest, f)
		}
	}
	nb := 0
	for _, r := range rows {
		if r.Get("severity") != "info" {
			nb++
		}
	}
	mode := py.OrS(vst.Get("mode"), "inferred")
	out := map[string]any{"branchFindings": nb, "mode": mode, "auditedAt": 0, "secTop": apiCicdFindings(top), "secRest": apiCicdFindings(rest), "secCount": len(sec),
		"sections": sections, "variables": apiCicdFindings(vrows), "varsNote": "", "engine": CICD_ENGINE_PROJECT}
	if at := Q("SELECT v FROM cicd_state WHERE k='audit_at'"); len(at) > 0 {
		out["auditedAt"] = py.I(cicdJSON(at[0].Get("v")))
	}
	if mode != "full" {
		out["varsNote"] = "Full variable check needs a Maintainer read token for ci-vars-meta (group access token on " + CICD_GROUP + ", role Maintainer, scope read_api, root-only in /etc/argus/ci-vars-meta.token; Argus's agent never sees it). Until then the findings below are inferred from job logs, pushes that started no pipeline, and deploy targets."
	}
	return out, nil
}

// apiCicdProjectJSON is /cicd/project.
func apiCicdProjectJSON(qs py.QS, _ *Session) (any, *apiErr) {
	id, ok := cicdID(qs)
	if !ok {
		return nil, &apiErr{404, "no such project"}
	}
	pp := Q("SELECT * FROM cicd_projects WHERE id=?", id)
	if len(pp) == 0 {
		return nil, &apiErr{404, "no such project"}
	}
	p := pp[0]
	m := cicdMatrixData()
	cells := []map[string]any{}
	for _, env := range cicdEnvs {
		cc, _ := apiCicdCell(p, env, m)
		cells = append(cells, cc)
	}
	fs := Q("SELECT * FROM cicd_findings WHERE project_id=? AND status='open' ORDER BY "+cicdEnvRank+", last_seen DESC", p.Get("id"))
	pipelines := []map[string]any{}
	for _, r := range Q("SELECT * FROM cicd_pipelines WHERE project_id=? ORDER BY id DESC LIMIT 30", p.Get("id")) {
		pipelines = append(pipelines, map[string]any{"id": r.I("id"), "env": r.S("env"), "status": apiCicdStatus(r.Get("status")), "sha": py.Head(r.S("sha"), 8),
			"at": r.I("created_at"), "bridge": py.OrS(r.Get("deploy_status"), "-")})
	}
	deploys := []map[string]any{}
	for _, r := range Q("SELECT * FROM cicd_deploys WHERE project_id=? ORDER BY at DESC LIMIT 20", p.Get("id")) {
		hs := []string{}
		for i, h := range py.AsList(cicdJSON(py.OrS(r.Get("hosts"), "[]"))) {
			if i < 4 {
				hs = append(hs, py.Str(h))
			}
		}
		vs := r.Get("verify_state")
		k := "warn"
		if vs == "regression" {
			k = "fail"
		} else if vs == "ok" {
			k = "ok"
		}
		deploys = append(deploys, map[string]any{"at": r.I("at"), "env": r.S("env"), "sha": py.Head(r.S("sha"), 8), "author": py.OrS(r.Get("author"), "-"),
			"hosts": py.OrS(strings.Join(hs, ", "), "-"), "verified": apiPill{Text: py.OrS(vs, "-"), Kind: k}})
	}
	incs := py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(p.Get("includes"), "{}"))); err == nil {
		if mm := py.AsMap(v); mm != nil {
			incs = mm
		}
	}
	keys := append([]string(nil), incs.Keys()...)
	sort.Strings(keys)
	incl := []string{}
	for _, k := range keys {
		v := py.AsList(incs.Get(k))
		s := k + ":"
		for i := 0; i < 2 && i < len(v); i++ {
			s += " " + py.Str(v[i])
		}
		incl = append(incl, s)
	}
	path := p.Str("path")
	rail := []apiFact{{Label: "Project", Value: path, Href: GITLAB_URL + "/" + py.Quote(path), Mono: true},
		{Label: "Kind", Value: py.OrS(p.Get("kind"), "-")}, {Label: "Executor", Value: py.OrS(p.Get("executor"), "unknown (inferred from jobs)")},
		{Label: "Default branch", Value: py.OrS(p.Get("default_branch"), "-"), Mono: true}, {Label: "Include", Value: py.OrS(strings.Join(incl, " · "), "-"), Mono: true},
		{Label: "Unit (systemd)", Value: strings.ReplaceAll(path, "/", "-") + ".service", Mono: true}}
	rl := apiRepoLink(p.I("id"), RepoReadinessMap(), path)
	word := "not checked"
	if pl, ok := rl["pill"].(apiPill); ok {
		word = pl.Text
	}
	rail = append(rail, apiFact{Label: "Readiness", Value: word, Href: rl["href"].(string)}, apiFact{Label: "Scanned", Value: Ago(p.Get("scanned_at"))})
	return map[string]any{"name": cicdShortPath(path), "path": path, "cells": cells, "findings": apiCicdFindings(fs), "deploys": deploys, "pipelines": pipelines, "rail": rail}, nil
}

func apiCicdProposal(pr *py.OMap) map[string]any {
	call := ""
	if py.Truthy(pr.Get("path")) {
		call = py.Strip(py.OrS(pr.Get("method"), "") + " /api/v4/" + py.OrS(pr.Get("path"), ""))
	}
	rule := ""
	if pr.Get("action") != "none" {
		rule = If(py.Truthy(pr.Get("allowed")), "Allowed for Argus once writes are enabled", "Operator only") + ": " + py.OrS(pr.Get("rule"), "")
	}
	return map[string]any{"text": py.OrS(py.Or(pr.Get("text"), pr.Get("reason")), ""), "call": call, "curl": py.OrS(pr.Get("curl"), call), "rule": rule,
		"needsToken": (pr.Get("method") == "POST" || pr.Get("method") == "PUT") && py.Truthy(pr.Get("path"))}
}

// apiCicdFindingJSON is /cicd/finding.
func apiCicdFindingJSON(qs py.QS, sess *Session) (any, *apiErr) {
	id, ok := cicdID(qs)
	if !ok {
		return nil, &apiErr{404, "no such finding"}
	}
	rr := Q("SELECT * FROM cicd_findings WHERE id=?", id)
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such finding"}
	}
	f := rr[0]
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/cicd/finding?id=" + I2S(id)}
	links := []apiLink{}
	if f.T("pipeline_id") && f.T("project") {
		links = append(links, apiLink{Text: "pipeline " + I2S(f.I("pipeline_id")), Href: GITLAB_URL + "/" + py.Quote(f.S("project")) + "/-/pipelines/" + I2S(f.I("pipeline_id"))})
	}
	if f.T("job_id") {
		links = append(links, apiLink{Text: "job " + I2S(f.I("job_id"))})
	}
	if f.T("incident_id") {
		links = append(links, apiLink{Text: "incident #" + I2S(f.I("incident_id")), Href: "/incidents/view?id=" + I2S(f.I("incident_id"))})
	}
	kk := f.S("klass")
	cls := cicdGet(cicdClassLbl, kk, py.OrS(f.Get("klass"), "-")) + If(f.T("llm"), " (model)", "")
	rail := []apiFact{{Label: "Kind", Value: f.Str("kind")}, {Label: "Class", Value: cls}, {Label: "Rule", Value: py.OrS(f.Get("rule"), "-"), Mono: true},
		{Label: "Severity", Value: py.OrS(f.Get("severity"), "-")}, {Label: "Environment", Value: py.OrS(f.Get("env"), "-")},
		{Label: "Project", Value: py.OrS(f.Get("project"), "-"), Href: "/cicd/project?id=" + I2S(f.I("project_id"))},
		{Label: "Ref / job", Value: py.OrS(f.Get("ref"), "-") + " / " + py.OrS(f.Get("job_name"), "-")},
		{Label: "Commit", Value: strings.TrimSpace(py.OrS(py.Head(f.S("sha"), 10), "-") + " " + f.S("author") + " " + f.S("mr")), Mono: true},
		{Label: "Seen", Value: Text(When(f.Get("first_seen"))) + " → " + Text(When(f.Get("last_seen"))) + " · " + I2S(max(f.I("occurrences"), 1)) + "×"}}
	if f.Get("status") != "open" {
		rail = append(rail, apiFact{Label: "Resolved", Value: Text(When(f.Get("resolved_at"))) + ": " + f.S("resolve_note")})
	}
	props := []map[string]any{}
	for _, p := range cicdProposals(f) {
		props = append(props, apiCicdProposal(p))
	}
	return map[string]any{"id": f.I("id"), "title": py.OrS(f.Get("title"), "Finding"), "status": apiPill{Text: f.Str("status"), Kind: If(f.Get("status") == "open", "fail", "ok")},
		"prod": f.Get("env") == "release", "detail": f.S("detail"), "proposals": props, "writes": cicdWriteNote(), "evidence": py.OrS(f.Get("evidence"), "-"),
		"task": apiBoardBtn("cicd", f.I("id")), "rail": rail, "links": links,
		"problem": apiProblemPanel(c, qs, "cicd_finding", I2S(f.I("id")), py.OrS(f.Get("title"), "CI/CD finding"), "finding", f.I("incident_id"))}, nil
}
