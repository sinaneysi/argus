package app

// Review's full lists of lessons, problem links and chat requests as JSON for the Next.js page (ADR-0018): GET
// /api/v1/lessons, /suggestions and /replies, and the twins of /admin/lessons/act and /settings. Same queries, roles and
// request files as the Go pages; every lesson write goes through LessonsPost, so its checks stay in one place. Links
// decide through /api/v1/problems/act and chat requests through /api/v1/board/mxpreview, which already exist.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// lessonFullHref is a link of the old page as its place in Review. Review's own "kind" names the list, so the lessons'
// Kind filter travels as "lkind".
func lessonFullHref(h string) string {
	h = strings.TrimPrefix(h, "/admin/lessons")
	h = strings.ReplaceAll(strings.Replace(h, "?", "&", 1), "&kind=", "&lkind=")
	return "/review?kind=lessons&mode=full" + h
}

func apiLessonRow(r Row) map[string]any {
	st := r.S("state")
	acts := []string{}
	switch st {
	case "proposed":
		acts = []string{"approve", "reject"}
		if r.S("review") == "retire?" {
			acts = append(acts, "retire")
		}
	case "approved":
		acts = []string{"review", "retire"}
	}
	src := lessonSource(r.S("source_key"))
	out := map[string]any{"id": r.I("id"), "state": st, "stateKind": map[string]string{"approved": "ok", "proposed": "warn", "rejected": "fail"}[st],
		"title": r.S("title"), "body": r.S("body"), "service": r.S("service"), "env": r.S("env"), "kind": r.S("kind"), "tags": append([]string{}, lessonTags(r)...),
		"source": apiLink{Text: Text(src), Href: firstHref(src)}, "proposedAt": r.I("proposed_at"), "evidence": r.S("evidence"), "actions": acts,
		"editable": st == "proposed" || st == "approved" || st == "rejected", "review": r.S("review")}
	if r.S("review") == "update" {
		out["supersedes"] = r.I("supersedes")
	}
	if st == "approved" || r.I("uses") > 0 {
		out["used"] = map[string]any{"uses": r.I("uses"), "last": py.I(py.Or(r["last_used"], int64(0)))}
	}
	if d := r.S("decided_by"); !r.IsNull("decided_by") && d != "" {
		out["decided"] = map[string]any{"by": d, "at": r.I("decided_at")}
	}
	return out
}

func apiLessonsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "Admins only"}
	}
	q := py.NewQS()
	for _, kv := range qs.Pairs("kind", "lkind") {
		q.Add(kv[0], kv[1])
	}
	if k := qs.Get("lkind"); k != "" {
		q.Add("kind", k)
	}
	d := lessonsQuery(q)
	l, view := d.l, d.view
	tabs := []map[string]any{}
	for _, v := range lessonViews {
		g := l.with("view", v[0])
		g.v["sort"] = l.def["sort"]
		tabs = append(tabs, map[string]any{"label": v[1], "count": d.stN[v[0]], "current": v[0] == view, "href": lessonFullHref(g.href())})
	}
	envs, kinds := [][2]string{}, [][2]string{}
	for _, e := range lessonEnvs[1:] {
		envs = append(envs, [2]string{e, e})
	}
	for _, k := range lessonKinds[1:] {
		kinds = append(kinds, [2]string{k, k})
	}
	facets := []apiFacet{}
	if view == "proposed" {
		facets = append(facets, apiFacetOf("review", "Review", l.get("review"), lessonReviews, d.revN))
	}
	facets = append(facets, apiFacetOf("env", "Environment", l.get("env"), envs, d.envN), apiFacetOf("lkind", "Kind", l.get("kind"), kinds, d.kindN))
	sorts := [][3]string{{"newest", "Newest", ""}, {"oldest", "Oldest", ""}}
	switch view {
	case "approved":
		sorts = append([][3]string{{"service", "Service", ""}}, append(sorts, [3]string{"used", "Most used", ""})...)
	case "rejected", "retired":
		sorts = append([][3]string{{"decided", "Decided", ""}}, sorts...)
	}
	noun := map[string]string{"proposed": "waiting", "approved": "approved", "rejected": "rejected", "retired": "retired"}[view] + " lessons"
	from, to := listPage(&l, len(d.rows))
	list := apiListOf(l, len(d.rows), d.counts[view], noun, sorts)
	list["path"] = "/review"
	list["clearHref"] = lessonFullHref(l.cleared().href())
	for _, k := range []string{"prev", "next"} {
		if h, ok := list[k].(string); ok {
			list[k] = lessonFullHref(h)
		}
	}
	hidden := map[string]string{"kind": "lessons", "mode": "full"}
	if view != "proposed" {
		hidden["view"] = view
	}
	list["hidden"] = hidden
	rows := []map[string]any{}
	for _, r := range d.rows[from:to] {
		rows = append(rows, apiLessonRow(r))
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	banners := apiBannersOf(BoardRequestBanner(c, qs))
	empty := ""
	if len(rows) == 0 && !(l.filtered() && d.counts[view] > 0) {
		empty = "No " + noun + " yet."
		if view == "proposed" {
			empty = "Nothing waiting. Task runs propose up to 3 lessons each; they wait here until an admin approves them."
		}
	}
	return map[string]any{"view": view, "title": map[string]string{"proposed": "Waiting for review", "approved": "Approved", "rejected": "Rejected", "retired": "Retired"}[view],
		"waiting": d.counts["proposed"], "on": lessonsEnabled(), "tabs": tabs, "facets": facets, "list": list, "rows": rows, "empty": empty, "banners": banners, "refresh": c.Refresh,
		"envs": lessonEnvs, "kinds": lessonKinds, "max": map[string]int{"title": lessonTitleMax, "body": lessonBodyMax}, "here": lessonFullHref(l.href())}, nil
}

func lessonsAPIPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	return apiResult(LessonsPost(&Ctx{Sess: sess, Theme: "auto", Path: "/review"}, "/admin/lessons/"+strings.TrimPrefix(path, "/api/v1/lessons/"), apiForm(body), sess))
}

// sugFullHref is a link of the Suggestions view as its place in Review.
func sugFullHref(h string) string {
	h = strings.TrimPrefix(h, "/problems?")
	h = strings.TrimPrefix(strings.ReplaceAll("&"+h, "&view=suggestions", ""), "&")
	return "/review?kind=links&mode=full" + If(h != "", "&"+h, "")
}

func apiSugRow(l Row, can bool) map[string]any {
	x := apiProbSuggestion(l, true, true)
	x["canDecide"] = can
	return x
}

func apiSugFacets(f probSugQuery, all []Row) []apiFacet {
	count := func(skip string, key func(Row) string) map[string]int {
		out := map[string]int{}
		for _, l := range all {
			if f.match(l, skip) {
				out[key(l)]++
			}
		}
		return out
	}
	bands := [][2]string{}
	for _, b := range probConfBands {
		bands = append(bands, [2]string{b[0], b[1]})
	}
	typN := count("type", func(l Row) string { return l.S("ref_type") })
	types := [][2]string{}
	known := map[string]bool{}
	for _, n := range probCountNouns {
		types = append(types, [2]string{n[0], py.Capitalize(n[1])})
		known[n[0]] = true
	}
	more := []string{}
	for k := range typN {
		if !known[k] {
			more = append(more, k)
		}
	}
	sort.Strings(more)
	for _, k := range more {
		types = append(types, [2]string{k, py.Capitalize(py.OrS(probTypeWord[k], k))})
	}
	return []apiFacet{apiFacetOf("conf", "Confidence", f.conf, bands, count("conf", func(l Row) string { return probBand(l.F("confidence")) })),
		apiFacetOf("type", "Item", f.typ, types, typN)}
}

// apiSugConfirm is the bulk confirm step as data (probSugConfirm): nothing is sent until the one POST.
func apiSugConfirm(f probSugQuery, act string, raw []string, rejectDays int64) map[string]any {
	back := sugFullHref(f.href())
	ids, ok := probFormIDs(raw)
	if !ok || !In(act, "accept", "reject") {
		return map[string]any{"error": "Choose 1 to 50 suggestions, then an action.", "note": "Nothing was changed.", "back": back}
	}
	qm := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows := Q("SELECT l.*, p.title ptitle FROM problem_links l JOIN problems p ON p.id=l.problem_id WHERE l.id IN ("+qm+") ORDER BY l.confidence DESC, l.id", ids...)
	items, gone, links := []map[string]any{}, []map[string]any{}, []int64{}
	for _, r := range rows {
		if r.S("state") == "suggested" {
			it := probDescribe(r.S("ref_type"), r.S("ref_id"))
			items = append(items, map[string]any{"confidence": py.F(r.F("confidence")*100, 0) + " %", "item": probRefLabel(it.T, it.ID), "linkWord": probLinkWord[r.S("link_type")],
				"problem": probPID(r.I("problem_id")), "title": FirstLine(r.S("ptitle"), 90)})
			links = append(links, r.I("id"))
		} else {
			gone = append(gone, map[string]any{"item": probRefLabel(r.S("ref_type"), r.S("ref_id")), "problem": probPID(r.I("problem_id")), "state": r.S("state")})
		}
	}
	if len(items) == 0 {
		return map[string]any{"error": "None of the selected suggestions is still waiting.", "note": "They were decided since the list loaded. Nothing was changed.", "back": back}
	}
	what := Itoa(len(items)) + " suggested link" + If(len(items) != 1, "s", "")
	out := map[string]any{"action": act, "items": items, "gone": gone, "skipped": len(ids) - len(items), "links": links, "back": back, "maxNote": probMaxNote}
	if act == "accept" {
		out["head"], out["button"] = "Accept "+what+"?", "Accept "+what
		out["body"] = []string{"Each item is linked to its problem as suggested (the link type is on each line). The item's page then shows the problem banner. A first accepted \"caused by\" or " +
			"\"explains\" link moves a new problem to Investigating; a \"fixed by\" link moves it to Fix in progress; a \"caused by\" link on a problem resolved recently reopens it; a \"duplicate of\" " +
			"link merges the two problems.", "There is no undo here: unlink on the problem's page, with a reason."}
	} else {
		out["head"], out["button"] = "Reject "+what+"?", "Reject "+what
		out["body"] = []string{"Argus remembers each pair and does not suggest it again for " + I2S(rejectDays) + " days. The problems and items are not changed."}
	}
	return out
}

func apiSuggestionsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if !ProblemsOn() {
		return nil, &apiErr{404, "Problem records are off."}
	}
	st := problemsState()
	f := probSugParse(qs)
	where, args := "l.state='suggested'", []any{}
	if f.p != "" {
		where += " AND l.problem_id=?"
		args = append(args, py.I(f.p))
	}
	all := Q("SELECT l.*, p.title ptitle FROM problem_links l JOIN problems p ON p.id=l.problem_id WHERE "+where+" ORDER BY l.confidence DESC, l.at DESC, l.id LIMIT 2000", args...)
	line := []string{If(len(all) > 0, Itoa(len(all))+" suggested link"+If(len(all) == 1, "", "s")+" waiting", "No suggestion waiting")}
	prec := "no decision yet"
	if dec := probQ1("SELECT sum(state='accepted') a, sum(state='rejected') r FROM problem_links WHERE source IN ('argus','model') AND decided_at IS NOT NULL"); dec != nil && dec.I("a")+dec.I("r") > 0 {
		prec = py.F(float64(dec.I("a"))/float64(dec.I("a")+dec.I("r"))*100, 0) + " % accepted of " + I2S(dec.I("a")+dec.I("r")) + " decided"
	}
	last := "suggester not run yet"
	if st.Has("last_suggest") {
		last = "last pass " + Ago(st.Get("last_suggest"))
		if lr := st.Map("last_suggest_result"); lr != nil {
			last += " (" + I2S(py.I(lr.Get("items"))) + " issues × " + I2S(py.I(lr.Get("problems"))) + " problems, " + I2S(py.I(lr.Get("suggested"))) + " new)"
		}
	}
	line = append(line, prec, last, probAutoLine(st))
	can := sess.Role == "admin" && ProblemsCanAct()
	rejectDays := py.I(st.GetD("reject_days", int64(30)))
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	out := map[string]any{"line": line, "can": can, "rejectDays": rejectDays, "any": len(all) > 0, "maxNote": 300, "problem": nil, "confirm": nil,
		"banners": apiBannersOf(probSentBanner(c, qs)), "minConfidence": py.F(py.Num(st.GetD("min_confidence", 0.6))*100, 0) + " %"}
	if f.p != "" {
		out["problem"] = map[string]any{"id": f.p, "label": "P-" + f.p}
	}
	if act := qs.Get("confirm"); act != "" && can {
		out["confirm"] = apiSugConfirm(f, act, qs.List("link"), rejectDays)
		out["refresh"] = c.Refresh
		return out, nil
	}
	rows := []Row{}
	for _, l := range all {
		if f.match(l, "") {
			rows = append(rows, l)
		}
	}
	probSugSortRows(rows, f.sort)
	f.page = queueClamp(f.page, int64(len(rows)))
	lo := min((f.page-1)*queuePageSize, len(rows))
	hi := min(lo+queuePageSize, len(rows))
	list := []map[string]any{}
	for _, r := range rows[lo:hi] {
		list = append(list, apiSugRow(r, can))
	}
	sorts := []apiOption{}
	for _, s := range probSugSorts {
		sorts = append(sorts, apiOption{s.key, s.label})
	}
	matched := len(rows)
	pages := max(1, (matched+queuePageSize-1)/queuePageSize)
	hidden := map[string]string{"kind": "links", "mode": "full"}
	if f.p != "" {
		hidden["p"] = f.p
	}
	state := map[string]any{"path": "/review", "q": f.q, "sort": f.sort, "sorts": sorts, "filtered": f.filtered(), "clearHref": sugFullHref(probSugQuery{p: f.p, sort: f.sort, page: 1}.href()),
		"matched": matched, "total": len(all), "noun": "waiting", "page": f.page, "pages": pages, "from": min((f.page-1)*queuePageSize+1, matched), "to": min(f.page*queuePageSize, matched), "hidden": hidden}
	if f.page > 1 {
		state["prev"] = sugFullHref(f.with(func(g *probSugQuery) { g.page = f.page - 1 }).href())
	}
	if f.page < pages {
		state["next"] = sugFullHref(f.with(func(g *probSugQuery) { g.page = f.page + 1 }).href())
	}
	sel := qs.Get("sel") == "page"
	head := sugFullHref(f.href())
	bulk := [][2]string{{"kind", "links"}, {"mode", "full"}}
	for _, p := range f.pairs() {
		if p[0] != "view" {
			bulk = append(bulk, p)
		}
	}
	out["rows"], out["list"], out["facets"], out["picked"], out["here"], out["bulk"] = list, state, apiSugFacets(f, all), sel, head, bulk
	out["selHref"] = If(sel, head, head+"&sel=page")
	out["refresh"] = c.Refresh
	return out, nil
}

func apiRepliesJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "Admins only"}
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	banners := apiBannersOf(BoardRequestBanner(c, qs))
	return map[string]any{"rows": matrixWaiting(sess), "banners": banners, "refresh": c.Refresh}, nil
}
