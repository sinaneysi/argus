package app

// RCA reports and Problems as JSON: GET /api/v1/rca (+ /item), /api/v1/incidents/report, /api/v1/problems (+ /item).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var regexpSectionNote = regexp.MustCompile(`(?s)<p class="section-note">(.*?)</p>`)

func apiRcaRef(x Row) map[string]any {
	return map[string]any{"id": x.I("id"), "title": x.S("title"), "href": "/rca/view?id=" + I2S(x.I("id")), "status": x.S("status"),
		"statusKind": getD(rcaStatusKind, x.S("status"), "warn"), "at": x.I("created"), "source": x.S("source"), "auto": x.Get("requested_by") == "auto"}
}

// apiRcaJSON is GET /api/v1/rca: every report with the query parameters of /rca.
func apiRcaJSON(qs py.QS, _ *Session) (any, *apiErr) {
	items := rcaRows()
	nOpen := 0
	for _, r := range items {
		if s := r.Get("status"); s == "open" || s == "local" {
			nOpen++
		}
	}
	l := rcaQuery(qs)
	shown, stN, srcN := rcaCompute(l, items)
	vals := func(n map[string]int) [][2]string {
		out := [][2]string{}
		for k := range n {
			if k != "" {
				out = append(out, [2]string{k, k})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
		return out
	}
	from, to := listPage(&l, len(shown))
	rows := []map[string]any{}
	for _, r := range shown[from:to] {
		row := map[string]any{"at": r.I("created"), "title": r.Str("title"), "href": rcaHref(r), "file": rcaBase(r.S("path")), "incident": r.I("incident_id"),
			"status": py.OrS(r.Get("status"), "?"), "statusKind": incidentsKind(rcaStatusKind, r.Get("status"), "warn"), "source": r.S("source"),
			"auto": r.Get("requested_by") == "auto", "cost": rcaCost(r) + rcaMins(r), "mr": ""}
		if strings.HasPrefix(r.S("mr_url"), "https://") {
			row["mr"] = r.S("mr_url")
		}
		rows = append(rows, row)
	}
	return map[string]any{"count": len(items), "notMerged": nOpen, "rows": rows,
		"facets": []apiFacet{apiFacetOf("status", "MR state", l.get("status"), vals(stN), stN), apiFacetOf("source", "Source", l.get("source"), vals(srcN), srcN)},
		"list": apiListOf(l, len(shown), len(items), "reports", [][3]string{{"newest", "Newest", "written most recently first"},
			{"oldest", "Oldest", "written longest ago first"}, {"cost", "Cost", "most expensive first"}})}, nil
}

func apiFacts(fs []Fact) []apiFact {
	kv := [][2]string{}
	for _, f := range fs {
		kv = append(kv, [2]string{f.Label, f.HTML})
	}
	return apiKV(kv)
}

// apiRcaItemJSON is GET /api/v1/rca/item?id=: one report.
func apiRcaItemJSON(qs py.QS, sess *Session) (any, *apiErr) {
	rid := qs.Get("id")
	if !py.IsDigit(rid) || len(rid) > 9 {
		return nil, &apiErr{404, "no such report"}
	}
	rr := Q("SELECT * FROM rca_reports WHERE id=?", py.I(rid))
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such report"}
	}
	r := rr[0]
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/rca/view"}
	facts := []Fact{{"State", E(py.OrS(r.Get("status"), "?"))}, {"Written", When(r.Get("created"))}}
	env := ""
	if r.T("incident_id") {
		facts = append(facts, Fact{"Incident", `<a href="/incidents/view?id=` + I2S(r.I("incident_id")) + `">incident #` + I2S(r.I("incident_id")) + `</a>`})
		if i := Q("SELECT env FROM incidents WHERE id=?", r.I("incident_id")); len(i) > 0 {
			env = i[0].S("env")
		}
	}
	if strings.HasPrefix(r.S("mr_url"), "https://") {
		facts = append(facts, Fact{"Review", `<a href="` + E(r.Str("mr_url")) + `">merge request</a>`})
	}
	facts = append(facts, Fact{"Source", E(py.OrS(r.Get("source"), "-")) + If(r.Get("requested_by") == "auto", " (auto)", "")})
	if py.IsNumber(r.Get("cost_usd")) {
		facts = append(facts, Fact{"Cost", rcaCost(r) + rcaMins(r)})
	}
	if r.T("path") {
		facts = append(facts, Fact{"File", `<span class="mono small">` + E(rcaBase(r.Str("path"))) + `</span>`})
	}
	if env != "" {
		facts = append(facts[:1], append([]Fact{{"Environment", E(env)}}, facts[1:]...)...)
	}
	banners := []apiBanner{}
	if s := r.Get("status"); s == "open" || s == "local" {
		banners = append(banners, apiBanner{Text: "Not merged yet: this is the report as the run wrote it.", Kind: "warn"})
	}
	if r.Has("redacted") && r.T("redacted") {
		banners = append(banners, apiBanner{Text: "Redacted: the secret scan replaced a token or password in this report before it was stored ([REDACTED:<kind>]).", Kind: "warn"})
	}
	h := FixHandoffRca(r, env)
	body := r.S("content")
	lazy, lzOK := LazyRow(r, "content")
	var outline []OutlineItem
	if lzOK {
		body = LazyStrip(body)
		outline = []OutlineItem{{"lazy-reading", "Summary"}, {"full-analysis", "Full analysis"}}
	}
	outline = append(append(outline, MdOutlineH(body, "strip")...), OutlineItem{"how-to-fix", "How to fix it"}, OutlineItem{"fix-prompt", "Prompt for a write-access agent"})
	actions := ""
	if ne := FHNormEnv(env); (ne == "develop" || ne == "pre-release") && h.Prompt != "" {
		actions = FixerProblemActions(c, Row{"environment": ne, "fix_prompt": h.Prompt, "review": "rca=" + I2S(r.I("id"))})
	}
	var tasks map[string]any
	var props []Proposal
	opts := len(h.Options) > 0
	if opts {
		for _, o := range h.Options {
			props = append(props, Proposal{N: py.I(o.Get("n")), Title: py.Str(o.Get("title"))})
		}
	} else {
		props = BoardProposals(r.S("content"))
	}
	if len(props) > 0 {
		items := []map[string]any{}
		for _, p := range props {
			items = append(items, map[string]any{"n": p.N, "title": p.Title, "task": apiBoardBtn("rca", I2S(r.I("id"))+"-"+I2S(p.N))})
		}
		tasks = map[string]any{"title": If(opts, "Fix options as tasks", "Fix proposals as tasks"), "items": items}
		outline = append(outline, OutlineItem{"fix-tasks", "Fix options as tasks"})
	}
	out := map[string]any{"id": r.I("id"), "title": r.Str("title"), "status": py.OrS(r.Get("status"), "?"), "statusKind": incidentsKind(rcaStatusKind, r.Get("status"), "warn"),
		"at": r.I("created"), "incident": r.I("incident_id"), "banners": banners, "facts": apiFacts(facts), "outline": apiOutline(outline), "lazy": apiLazy(lazy, lzOK),
		"md": apiMd(body, "strip"), "fix": apiFix(h, env, "id="+I2S(r.I("id")), actions, ""), "tasks": tasks,
		"problem": apiProblemPanel(c, qs, "rca", I2S(r.I("id")), r.Str("title"), "RCA", r.I("incident_id"))}
	out["refresh"] = c.Refresh != nil
	return out, nil
}

// apiRcaFileJSON is GET /api/v1/incidents/report?f=: a merged report from the repository (only a name RcaFiles lists).
func apiRcaFileJSON(qs py.QS, _ *Session) (any, *apiErr) {
	f := filepath.Base(qs.Get("f"))
	if qs.Get("f") == "" || strings.HasSuffix(qs.Get("f"), "/") || !In(f, RcaFiles()...) {
		return nil, &apiErr{404, "no such report"}
	}
	data, err := os.ReadFile(filepath.Join(REPORTS, f))
	if err != nil {
		return nil, &apiErr{404, "no such report"}
	}
	text := strings.ReplaceAll(strings.ReplaceAll(py.ToValidUTF8(data), "\r\n", "\n"), "\r", "\n")
	h := FHParse(text, "", "")
	lazy, lzOK := LazyParse(text)
	var outline []OutlineItem
	if lzOK {
		lazy.Source, text = "report", LazyStrip(text)
		outline = []OutlineItem{{"lazy-reading", "Summary"}, {"full-analysis", "Full analysis"}}
	}
	outline = append(outline, MdOutlineH(text, "strip")...)
	var fix map[string]any
	if h.State != "missing" {
		fix = apiFix(h, "", "file="+py.Quote(f), "", "")
		outline = append(outline, OutlineItem{"how-to-fix", "How to fix it"}, OutlineItem{"fix-prompt", "Prompt for a write-access agent"})
	}
	return map[string]any{"title": py.Slice(f, 0, -3), "file": f, "lazy": apiLazy(lazy, lzOK), "md": apiMd(text, "strip"), "fix": fix, "outline": apiOutline(outline)}, nil
}

// --- problems ---------------------------------------------------------------------------------------------------

func apiProbRow(r Row, counts map[string]int64) map[string]any {
	out := map[string]any{"id": r.I("id"), "label": probPID(r.I("id")), "title": r.S("title"), "status": r.S("status"), "statusWord": probWord[r.S("status")],
		"statusKind": probKind[r.S("status")], "linked": probCountText(counts), "env": r.S("env"), "severity": strings.ToLower(r.S("severity")),
		"public": r.T("customer_facing"), "age": probAge(r), "owner": r.S("owner"), "rootCause": r.S("root_cause"), "rootLabel": r.S("root_cause_label")}
	if r.T("merged_into") {
		out["mergedInto"] = r.I("merged_into")
	}
	return out
}

// apiProblemsJSON is GET /api/v1/problems: the queue with the query parameters of /problems.
func apiProblemsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if !ProblemsOn() {
		return nil, &apiErr{404, "problem records are off"}
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/problems"}
	f := probParse(qs)
	all := Q("SELECT * FROM problems ORDER BY updated_at DESC, id DESC LIMIT 2000")
	counts := probCounts()
	n := map[string]int{}
	now := Now()
	res7, issues := 0, int64(0)
	for _, r := range all {
		n[r.S("status")]++
		if r.S("status") == "resolved" && r.F("resolved_at") > now-7*86400 {
			res7++
		}
		if In(r.S("status"), probOpen...) {
			issues += probLinkedTotal(counts[r.I("id")])
		}
	}
	nOpen := n["new"] + n["investigating"] + n["known_error"] + n["fix_in_progress"]
	var sugN int64
	if r := probQ1("SELECT count(*) n FROM problem_links WHERE state='suggested'"); r != nil {
		sugN = r.I("n")
	}
	rows := []Row{}
	for _, r := range all {
		if f.match(r, "") {
			rows = append(rows, r)
		}
	}
	probSortRows(rows, f.sort, counts)
	unf := probQuery{status: If(f.tab() == "open", "open", f.status), sort: f.sort, page: 1}
	inView := 0
	for _, r := range all {
		if unf.match(r, "") {
			inView++
		}
	}
	matched := int64(len(rows))
	f.page = queueClamp(f.page, matched)
	lo := min((f.page-1)*queuePageSize, len(rows))
	hi := min(lo+queuePageSize, len(rows))
	list := []map[string]any{}
	for _, r := range rows[lo:hi] {
		list = append(list, apiProbRow(r, counts[r.I("id")]))
	}
	// tabs and facets, with the counts of probTabs and probFacets
	tn := map[string]int64{}
	for _, r := range all {
		if f.match(r, "status") {
			tn["all"]++
			tn[r.S("status")]++
			if In(r.S("status"), probOpen...) {
				tn["open"]++
			}
		}
	}
	tabs := []map[string]any{}
	for _, v := range probViews {
		cnt, href := tn[v[0]], f.with(func(g *probQuery) { g.status = v[0] }).href()
		if v[0] == "suggestions" {
			cnt, href = sugN, "/review?kind=links"
		}
		tabs = append(tabs, map[string]any{"key": v[0], "label": v[1], "count": cnt, "href": href, "on": v[0] == f.tab()})
	}
	facet := func(key, label, skip, cur string, vals [][2]string, of func(Row) string) apiFacet {
		cn := map[string]int{}
		for _, r := range all {
			if f.match(r, skip) {
				cn[of(r)]++
			}
		}
		return apiFacetOf(key, label, cur, vals, cn)
	}
	facets := []apiFacet{}
	if f.tab() == "open" {
		s := facet("status", "Stage", "stage", If(f.status == "open", "", f.status), probStageFacets, func(r Row) string { return r.S("status") })
		facets = append(facets, s)
	}
	facets = append(facets, facet("sev", "Severity", "sev", f.sev, probSevFacets, func(r Row) string {
		for _, s := range probSevFacets {
			if probSevMatch(r.S("severity"), s[0]) {
				return s[0]
			}
		}
		return ""
	}), facet("env", "Environment", "env", f.env, probEnvFacets, func(r Row) string { return r.S("env") }))
	sorts := []apiOption{}
	for _, s := range probSorts {
		sorts = append(sorts, apiOption{s.key, s.label})
	}
	pages := max(1, int((matched+queuePageSize-1)/queuePageSize))
	lst := map[string]any{"path": "/problems", "q": f.q, "sort": f.sort, "sorts": sorts, "filtered": f.filtered(), "clearHref": unf.href(), "matched": matched,
		"total": inView, "noun": map[string]string{"open": "open", "resolved": "resolved", "closed": "closed", "all": "in all"}[f.tab()], "page": f.page, "pages": pages,
		"from": min(lo+1, len(rows)), "to": hi, "hidden": map[string]string{}}
	if f.tab() != "open" {
		lst["hidden"] = map[string]string{"status": f.status}
	}
	if f.page > 1 {
		lst["prev"] = f.with(func(g *probQuery) { g.page = f.page - 1 }).href()
	}
	if f.page < pages {
		lst["next"] = f.with(func(g *probQuery) { g.page = f.page + 1 }).href()
	}
	out := map[string]any{"open": nOpen, "issues": issues, "suggested": sugN, "resolved7": res7, "autoLine": probAutoLine(problemsState()), "tab": f.tab(), "tabs": tabs,
		"facets": facets, "list": lst, "rows": list, "hasAny": len(all) > 0, "canCreate": sess.Role == "admin" && ProblemsCanAct(),
		"heading": map[string]string{"open": "Open problems", "resolved": "Resolved", "closed": "Closed", "all": "All problems"}[f.tab()],
		"banners": apiBannersOf(probSentBanner(c, qs)), "backfill": apiProbBackfill(c, qs)}
	out["refresh"] = c.Refresh != nil
	return out, nil
}

// apiProbBackfill is the dry-run panel for admins (probBackfillPanel) as data.
func apiProbBackfill(c *Ctx, qs py.QS) map[string]any {
	h := probBackfillPanel(c, qs)
	if h == "" {
		return nil
	}
	notes := []string{}
	for _, m := range regexp.MustCompile(`(?s)<p class="section-note">(.*?)</p>`).FindAllStringSubmatch(h, -1) {
		notes = append(notes, Text(m[1]))
	}
	rows := [][]apiLink{}
	for _, m := range regexp.MustCompile(`(?s)<tr><td(.*?)</tr>`).FindAllStringSubmatch(h, -1) {
		row := []apiLink{}
		for _, cell := range regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`).FindAllStringSubmatch("<td"+m[1], -1) {
			row = append(row, apiLink{Text: Text(cell[1]), Href: firstHref(cell[1])})
		}
		rows = append(rows, row)
	}
	return map[string]any{"notes": notes, "rows": rows}
}

func apiProbLink(c *Ctx, l Row, admin bool) map[string]any {
	src := probSourceWord[l.S("source")]
	if l.T("confidence") && l.F("confidence") < 1 {
		src += " · " + py.F(l.F("confidence")*100, 0) + " %"
	}
	at := py.Or(l.Get("decided_at"), l.Get("at"))
	return map[string]any{"id": l.I("id"), "item": apiProbItemOf(probDescribe(l.S("ref_type"), l.S("ref_id"))), "secondary": l.S("link_type") == "caused_by" && !l.T("is_primary"),
		"source": src, "by": py.OrS(l.Get("decided_by"), l.S("by")), "at": py.I(at), "why": py.OrS(l.Get("note"), l.S("reason")), "state": l.S("state"), "canEdit": admin}
}

// apiProblemItemJSON is GET /api/v1/problems/item?id=: one problem (P-12 or 12).
func apiProblemItemJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if !ProblemsOn() {
		return nil, &apiErr{404, "problem records are off"}
	}
	v := strings.TrimPrefix(qs.Get("id"), "P-")
	if !py.IsDigit(v) || len(v) > 9 {
		return nil, &apiErr{404, "no such problem"}
	}
	p := probQ1("SELECT * FROM problems WHERE id=?", py.I(v))
	if p == nil {
		return nil, &apiErr{404, "no such problem"}
	}
	pid := p.I("id")
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/problems/view"}
	admin := sess.Role == "admin" && ProblemsCanAct()
	links := Q("SELECT * FROM problem_links WHERE problem_id=? ORDER BY is_primary DESC, ref_type, id", pid)
	incoming := Q("SELECT l.*, p.title ptitle FROM problem_links l JOIN problems p ON p.id=l.problem_id WHERE l.ref_type='problem' "+
		"AND l.ref_id=? AND l.state='accepted'", I2S(pid))
	counts := map[string]int64{}
	byGroup := map[string][]map[string]any{}
	sugg, gone := []map[string]any{}, []map[string]any{}
	accepted := 0
	for _, l := range links {
		switch l.S("state") {
		case "accepted":
			accepted++
			byGroup[l.S("link_type")] = append(byGroup[l.S("link_type")], apiProbLink(c, l, admin))
			if In(l.S("link_type"), "caused_by", "explains") {
				counts[l.S("ref_type")]++
			}
		case "suggested":
			s := apiProbSuggestion(l, true, false)
			s["canDecide"] = admin
			sugg = append(sugg, s)
		default:
			gone = append(gone, apiProbLink(c, l, false))
		}
	}
	groups := []map[string]any{}
	for _, g := range probGroups {
		if ls := byGroup[g[0]]; len(ls) > 0 {
			groups = append(groups, map[string]any{"key": g[0], "title": g[1], "links": ls})
		}
	}
	in := []map[string]any{}
	for _, l := range incoming {
		in = append(in, map[string]any{"id": l.I("problem_id"), "label": probPID(l.I("problem_id")), "title": l.S("ptitle"), "word": probLinkWord[l.S("link_type")]})
	}
	timeline := []map[string]any{}
	for _, e := range probTimeline(p, links) {
		timeline = append(timeline, map[string]any{"at": int64(e.at), "kind": e.kind, "source": apiSegs(e.src), "detail": apiSegs(e.html)})
	}
	var fix map[string]any
	if h, ok := fixStored(p); ok {
		dl := ""
		if m := incidentsRefRCA.FindStringSubmatch(p.S("fix_ref")); m != nil {
			dl = "id=" + m[1]
		}
		fix = apiFix(h, p.S("env"), dl, "", "Argus's options for this problem, from "+E(probRefLabel("rca", strings.TrimPrefix(p.S("fix_ref"), "rca:")))+".")
	}
	var controls map[string]any
	if admin {
		moves := []apiOption{}
		for _, to := range probHumanMoves[p.S("status")] {
			if to == "resolved" {
				continue
			}
			w := probWord[to]
			if In(p.S("status"), "resolved", "closed") && to == "investigating" {
				w = "re-open (investigating)"
			}
			moves = append(moves, apiOption{to, w})
		}
		quiet := []map[string]any{}
		for _, l := range links {
			if l.S("ref_type") != "incident" || l.S("link_type") != "caused_by" || l.S("state") != "accepted" {
				continue
			}
			r := probQ1("SELECT id, title, key, status, dc, clear_since FROM incidents WHERE id=?", py.I(l.S("ref_id")))
			if r == nil || !In(r.S("status"), "open", "acknowledged") || In(r.S("dc"), "CVE", "CICD") {
				continue
			}
			state := "firing"
			if r.T("clear_since") {
				state = "quiet since " + Ago(r.Get("clear_since"))
			}
			quiet = append(quiet, map[string]any{"id": r.I("id"), "title": FirstLine(py.OrS(r.Get("title"), r.S("key")), 80), "quiet": r.T("clear_since"), "state": state})
		}
		controls = map[string]any{"moves": moves, "canResolve": In(p.S("status"), probOpen...), "issues": quiet}
	}
	banners := apiBannersOf(probSentBanner(c, qs))
	lazy, lzOK := LazyRow(p, "")
	out := map[string]any{"id": pid, "label": probPID(pid), "title": p.S("title"), "status": p.S("status"), "statusWord": probWord[p.S("status")], "statusKind": probKind[p.S("status")],
		"rootLabel": p.S("root_cause_label"), "rootCause": p.S("root_cause"), "workaround": p.S("workaround"), "owner": p.S("owner"), "env": p.S("env"),
		"explains": "Explains " + I2S(counts["incident"]) + " incident" + If(counts["incident"] == 1, "", "s") + " and " + I2S(counts["qa_issue"]) + " QA issue" + If(counts["qa_issue"] == 1, "", "s"),
		"fixSince": nil, "mergedInto": p.I("merged_into"), "prod": p.S("env") == "release" && In(p.S("status"), probOpen...), "banners": banners, "lazy": apiLazy(lazy, lzOK),
		"linkedCount": accepted, "groups": groups, "incoming": in, "suggestions": sugg, "gone": gone, "timeline": timeline, "fix": fix,
		"facts": apiKV(probRecord(p, counts)), "controls": controls, "back": "/problems/view?id=" + I2S(pid)}
	if p.S("status") == "fix_in_progress" && p.T("fix_started_at") {
		out["fixSince"] = p.I("fix_started_at")
	}
	out["refresh"] = c.Refresh != nil
	return out, nil
}
