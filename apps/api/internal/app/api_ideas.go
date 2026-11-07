package app

// The full list of ideas as JSON for the Next.js Review page (ADR-0018): GET /api/v1/ideas and the twins of /ideas/act and
// /ideas/generate. Same queries, roles and request files as the Go page; every write goes through IdeasPostAct or
// IdeasPostGenerate, so their checks stay in one place. Text only: the frontend never takes HTML from the API.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// ideaFullHref is a link of the old page as its place in Review.
func ideaFullHref(h string) string {
	h = strings.TrimPrefix(h, "/ideas")
	return "/review?kind=ideas&mode=full" + strings.Replace(h, "?", "&", 1)
}

func apiIdeaRow(r Row, names map[string]ideaName, admin bool) map[string]any {
	st := py.OrS(r.Get("status"), "new")
	nm, ok := names[r.Str("persona")]
	if !ok {
		nm = ideaName{r.Str("persona"), ""}
	}
	evidence := []apiLink{}
	for _, x := range ideaEvidence(r.S("evidence")) {
		href := py.Str(py.Or(x.Get("link"), ""))
		evidence = append(evidence, apiLink{Text: py.Str(py.Or(x.Get("text"), py.Or(x.Get("link"), ""))), Href: If(strings.HasPrefix(href, "/"), apiSafeHref(href), "")})
	}
	facts := []apiOption{}
	for _, x := range [][2]string{{"Problem", r.S("problem")}, {"Proposal", r.S("proposal")}, {"Value", r.S("value")}, {"Risk", r.S("risk")},
		{"Prerequisites", r.S("prerequisites")}, {"Success metric", r.S("success_metric")}, {"What changed", r.S("changed")}} {
		if x[1] != "" {
			facts = append(facts, apiOption{x[1], x[0]})
		}
	}
	if st == "rejected" {
		facts = append(facts, apiOption{py.OrS(r.Get("reason"), "-") + " · " + py.OrS(r.Get("decided_by"), "-") + ", " + When(r.Get("decided_at")), "Rejected"})
	}
	if (st == "accepted" || st == "built") && r.T("decided_by") {
		facts = append(facts, apiOption{r.S("decided_by") + " · " + When(r.Get("decided_at")), "Accepted"})
	}
	out := map[string]any{"id": r.I("id"), "status": map[string]any{"text": py.OrS(ideaStatusWord[st], st), "kind": ideaStatusKind[st]}, "title": r.S("title"), "at": py.I(py.Or(r.Get("created_at"), int64(0))),
		"area": ideaAreaLabel(r.S("area")), "adviser": nm.name, "adviserRole": nm.role, "initial": ideaInitial(nm.name), "effort": py.OrS(r.Get("effort"), "?"), "value": r.S("value"),
		"evidence": evidence, "facts": facts, "actions": []string{}, "prompt": "", "wait": false}
	if r.T("revives") {
		out["revives"] = r.I("revives")
	}
	if st == "built" {
		built := map[string]any{"ref": py.OrS(r.Get("built_ref"), "-"), "by": py.OrS(r.Get("built_by"), "-"), "when": When(r.Get("built_at"))}
		if h := firstHref(ideaRefHTML(r.S("built_ref"))); h != "" {
			built["href"] = apiSafeHref(h)
		}
		out["built"] = built
	}
	if st == "accepted" || st == "built" {
		if r.T("build_prompt") {
			out["prompt"], out["promptHref"], out["refined"] = r.S("build_prompt"), "/ideas/prompt.md?id="+I2S(r.I("id")), r.T("prompt_refined")
		} else {
			out["wait"] = true
		}
	}
	if admin {
		acts := []string{}
		if st == "new" {
			acts = append(acts, "explore")
		}
		if st == "new" || st == "review" {
			acts = append(acts, "accept")
		}
		if st == "accepted" {
			acts = append(acts, "built")
		}
		if st == "new" || st == "review" || st == "accepted" {
			acts = append(acts, "reject")
		}
		out["actions"] = acts
	}
	return out
}

// apiIdeaAdvisers is ideasAdvisers as data.
func apiIdeaAdvisers(st *py.OMap, ps []ideaPersona, counts map[string]map[string]int64, admin bool) []map[string]any {
	on := st.Get("enabled") == true
	sp := st.Map("personas")
	if sp == nil {
		sp = py.NewOMap()
	}
	out := []map[string]any{}
	for _, p := range ps {
		v := sp.Map(p.key)
		if v == nil {
			v = py.NewOMap()
		}
		c := counts[p.key]
		meta := []string{}
		if p.role != "" {
			meta = append(meta, p.role)
		}
		meta = append(meta, If(len(p.days) > 0, "weekly on "+strings.Join(p.days, ", "), "no weekly slot"))
		if last := v.Get("last_run"); py.IsInt(last) {
			s := "last run " + Ago(last)
			if cost := v.Get("last_cost"); py.IsNumber(cost) {
				s += " ($" + py.F(py.Num(cost), 2) + ")"
			}
			meta = append(meta, s)
		} else {
			meta = append(meta, "no run yet")
		}
		meta = append(meta, I2S(c["all"])+" idea"+If(c["all"] == 1, "", "s")+", "+I2S(c["new"])+" new")
		a := map[string]any{"key": p.key, "name": p.name, "initial": ideaInitial(p.name), "meta": meta, "can": false, "why": ""}
		if admin {
			_, perr := os.Stat(filepath.Join(SET_IN, "requests", "ideas-"+p.key+".req"))
			why := ""
			switch {
			case !on:
				why = "Ideas are off"
			case st.Get("running") == p.key:
				why = "running now"
			case perr == nil:
				why = "requested"
			case v.Get("manual_today") == true:
				why = "used today; again tomorrow"
			}
			a["why"], a["can"] = why, why == ""
		}
		out = append(out, a)
	}
	return out
}

// apiIdeasJSON is GET /api/v1/ideas: the tabs, filters, page of ideas and the advisers.
func apiIdeasJSON(qs py.QS, sess *Session) (any, *apiErr) {
	admin := sess.Role == "admin"
	st := ideasStatus()
	ps := ideasPersonas(st)
	names := map[string]ideaName{}
	var order []string
	for _, p := range ps {
		if _, ok := names[p.key]; !ok {
			order = append(order, p.key)
		}
		names[p.key] = ideaName{p.name, p.role}
	}
	for _, r := range Q("SELECT DISTINCT persona FROM ideas ORDER BY persona") {
		k := r.Str("persona")
		if _, ok := names[k]; !ok {
			names[k] = ideaName{k, ""}
			order = append(order, k)
		}
	}
	known := map[string]bool{}
	for k := range names {
		known[k] = true
	}
	f := ideaParse(qs, known)
	counts := map[string]int64{}
	by := map[string]map[string]int64{}
	var total int64
	for _, r := range Q("SELECT persona, status, count(*) n FROM ideas GROUP BY 1, 2") {
		n := r.I("n")
		counts[r.Str("status")] += n
		total += n
		p := by[r.Str("persona")]
		if p == nil {
			p = map[string]int64{}
			by[r.Str("persona")] = p
		}
		p["all"] += n
		p[r.Str("status")] += n
	}
	on := st.Get("enabled") == true
	line := []string{If(counts["new"] > 0, I2S(counts["new"])+" new idea"+If(counts["new"] == 1, "", "s"), "No new ideas"), I2S(counts["review"]) + " under review", I2S(counts["accepted"]) + " accepted, waiting to be built"}
	if !on {
		line = append(line, "Ideas are off: set FEATURE_IDEAS=1 in the agent's environment")
	} else if len(ps) > 0 {
		w := []string{}
		for _, p := range ps {
			if len(p.days) > 0 {
				w = append(w, p.name+" "+strings.Join(p.days, "+"))
			}
		}
		line = append(line, "weekly: "+strings.Join(w, ", "))
	}
	tabN := f.counts("status", "status")
	tabs := []map[string]any{}
	for _, t := range ideaTabs {
		var n int64
		switch t[0] {
		case "open":
			n = tabN["new"] + tabN["review"] + tabN["accepted"]
		case "all":
			for _, v := range tabN {
				n += v
			}
		default:
			n = tabN[t[0]]
		}
		k := t[0]
		tabs = append(tabs, map[string]any{"label": t[1], "count": n, "current": k == f.status, "href": ideaFullHref(f.with(func(g *ideaQuery) { g.status = k }).href())})
	}
	w, a := f.where("")
	var matched, inView int64
	if r := Q("SELECT count(*) n FROM ideas WHERE "+w, a...); len(r) > 0 {
		matched = r[0].I("n")
	}
	unf := ideaQuery{status: f.status, sort: f.sort, page: 1}
	uw, ua := unf.where("")
	if r := Q("SELECT count(*) n FROM ideas WHERE "+uw, ua...); len(r) > 0 {
		inView = r[0].I("n")
	}
	f.page = queueClamp(f.page, matched)
	rows := []map[string]any{}
	for _, r := range Q("SELECT * FROM ideas WHERE "+w+" ORDER BY "+queueSortKey(ideaSorts, f.sort).order+" LIMIT ? OFFSET ?", append(a, queuePageSize, (f.page-1)*queuePageSize)...) {
		rows = append(rows, apiIdeaRow(r, names, admin))
	}
	facet := func(key, label, dim, cur string, vals [][2]string) apiFacet {
		n := map[string]int{}
		for k, v := range f.counts(dim, dim) {
			n[k] = int(v)
		}
		return apiFacetOf(key, label, cur, vals, n)
	}
	pv := [][2]string{}
	for _, k := range order {
		pv = append(pv, [2]string{k, names[k].name})
	}
	sorts := []apiOption{}
	for _, s := range ideaSorts {
		sorts = append(sorts, apiOption{s.key, s.label})
	}
	hidden := map[string]string{"kind": "ideas", "mode": "full"}
	if f.status != "open" {
		hidden["status"] = f.status
	}
	pages := max(1, int((matched+queuePageSize-1)/queuePageSize))
	list := map[string]any{"path": "/review", "q": f.q, "sort": f.sort, "sorts": sorts, "filtered": f.filtered(), "clearHref": ideaFullHref(unf.href()), "matched": matched, "total": inView, "noun": "ideas",
		"page": f.page, "pages": pages, "from": min(int64((f.page-1)*queuePageSize+1), matched), "to": min(int64(f.page*queuePageSize), matched), "hidden": hidden}
	if f.page > 1 {
		list["prev"] = ideaFullHref(f.with(func(g *ideaQuery) { g.page = f.page - 1 }).href())
	}
	if f.page < pages {
		list["next"] = ideaFullHref(f.with(func(g *ideaQuery) { g.page = f.page + 1 }).href())
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	banners := apiBannersOf(ideasSentBanner(c, qs))
	heading := map[string]string{"open": "Open ideas", "new": "New ideas", "review": "Under review", "accepted": "Accepted, waiting to be built", "built": "Built", "rejected": "Rejected", "all": "All ideas"}[f.status]
	empty := ""
	switch {
	case total == 0:
		empty = "No ideas yet. Kai (senior DevOps), Quinn (QA) and Nico (NOC) each propose up to 5 evidence-based ideas once a week, from Argus's own incidents, CI/CD, QA, alerts, capacity, backups, vulnerabilities and runs."
	case len(rows) == 0 && !f.filtered():
		empty = map[string]string{"open": "No open idea: nothing new, under review or accepted.", "new": "No new idea.", "review": "No idea is under review.", "accepted": "No accepted idea is waiting to be built.",
			"built": "No idea is marked built yet.", "rejected": "No idea was rejected."}[f.status]
	}
	return map[string]any{"admin": admin, "line": line, "tabs": tabs, "status": f.status, "total": total, "heading": heading, "list": list, "rows": rows, "empty": empty, "banners": banners, "refresh": c.Refresh,
		"facets":   []apiFacet{facet("persona", "Adviser", "persona", f.persona, pv), facet("area", "Area", "area", f.area, ideaAreas), facet("effort", "Effort", "effort", f.effort, [][2]string{{"S", "S"}, {"M", "M"}, {"L", "L"}})},
		"advisers": apiIdeaAdvisers(st, ps, by, admin), "reason": map[string]int{"min": ideaMinReason, "max": ideaMaxReason}}, nil
}

func ideasAPIPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	if path == "/api/v1/ideas/generate" {
		return apiResult(IdeasPostGenerate(c, apiForm(body), sess))
	}
	return apiResult(IdeasPostAct(c, apiForm(body), sess))
}
