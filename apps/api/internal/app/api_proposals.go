package app

// The full list of proposed tasks as JSON for the Next.js Review page (ADR-0018): GET /api/v1/proposals and the twins of
// /proposals/act and /proposals/settings. Admins only, like /proposals. Same queries and request files as the Go page;
// every write goes through ProposalsPost, so its checks stay in one place. Text only: the frontend never takes HTML.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// proposalFullHref is a link of the old page as its place in Review.
func proposalFullHref(h string) string {
	return "/review?kind=proposals&mode=full" + strings.Replace(strings.TrimPrefix(h, "/proposals"), "?", "&", 1)
}

type apiChipN struct {
	Label string `json:"label"`
	Href  string `json:"href"`
	On    bool   `json:"on"`
	Count int64  `json:"count"` // -1: no count
}

type apiChipRow struct {
	Label string     `json:"label"`
	Chips []apiChipN `json:"chips"`
}

// apiProposalChips is proposalFacets as links: severity takes several values, so filters stay one click each.
func apiProposalChips(f proposalQuery) []apiChipRow {
	link := func(mod func(*proposalQuery)) string { return proposalFullHref(f.with(mod).href()) }
	rows := []apiChipRow{}
	add := func(label string, chips []apiChipN) {
		if len(chips) > 1 {
			rows = append(rows, apiChipRow{label, chips})
		}
	}
	if f.view == "decided" {
		n := f.facetCounts("out", "state")
		chips := []apiChipN{{"Any", link(func(g *proposalQuery) { g.out = "" }), f.out == "", -1}}
		for _, o := range [][2]string{{"accepted", "Accepted"}, {"rejected", "Rejected"}, {"expired", "Expired"}} {
			k, on := o[0], f.out == o[0]
			if n[k] > 0 || on {
				chips = append(chips, apiChipN{o[1], link(func(g *proposalQuery) { g.out = If(on, "", k) }), on, n[k]})
			}
		}
		add("Outcome", chips)
	}
	sevN := f.facetCounts("sev", "coalesce(sev_rank,0)")
	sev := []apiChipN{{"Any", link(func(g *proposalQuery) { g.sev = nil }), len(f.sev) == 0, -1}}
	for _, s := range proposalSevFacets {
		k, n, on := s.key, sevN[I2S(s.rank)], In(s.key, f.sev...)
		if n == 0 && !on {
			continue
		}
		sev = append(sev, apiChipN{s.label, link(func(g *proposalQuery) {
			keep := []string{}
			for _, x := range g.sev {
				if x != k {
					keep = append(keep, x)
				}
			}
			if !on {
				keep = append(keep, k)
				sort.SliceStable(keep, func(i, j int) bool { return proposalSevIdx(keep[i]) < proposalSevIdx(keep[j]) })
			}
			g.sev = keep
		}), on, n})
	}
	add("Severity", sev)
	srcN := f.facetCounts("src", "path")
	src := []apiChipN{{"Any", link(func(g *proposalQuery) { g.src = "" }), f.src == "", -1}}
	for _, s := range proposalSrcFacets {
		k, on := s[0], f.src == s[0]
		if srcN[k] > 0 || on {
			src = append(src, apiChipN{s[1], link(func(g *proposalQuery) { g.src = If(on, "", k) }), on, srcN[k]})
		}
	}
	add("Source", src)
	envN := f.facetCounts("env", "coalesce(nullif(env,''),'none')")
	keys := []string{}
	for k := range envN {
		keys = append(keys, k)
	}
	rank := func(k string) int { return map[string]int{"release": 0, "pre-release": 1, "develop": 2, "none": 9}[k] }
	sort.Slice(keys, func(i, j int) bool {
		return rank(keys[i]) < rank(keys[j]) || (rank(keys[i]) == rank(keys[j]) && keys[i] < keys[j])
	})
	env := []apiChipN{{"Any", link(func(g *proposalQuery) { g.env = "" }), f.env == "", -1}}
	for _, k := range keys {
		kk, on := k, f.env == k
		env = append(env, apiChipN{If(k == "none", "No env", k), link(func(g *proposalQuery) { g.env = If(on, "", kk) }), on, envN[k]})
	}
	add("Environment", env)
	return rows
}

func apiProposalRow(p Row, expiryDays int64) map[string]any {
	facts := []apiOption{{p.Str("source_kind") + " " + p.Str("source_id"), "Source"}, {py.OrS(p.Get("priority"), "Medium"), "Priority"}}
	if l := proposalLabels(p); l != "" {
		facts = append(facts, apiOption{l, "Labels"})
	}
	if p.T("why") {
		facts = append(facts, apiOption{p.Str("why"), "Why proposed"})
	}
	facts = append(facts, apiOption{When(p.Get("created_at")) + " (" + Ago(p.Get("created_at")) + ")", "Proposed"})
	if p.T("refreshed") {
		facts = append(facts, apiOption{When(p.Get("updated_at")) + " (refreshed " + I2S(p.I("refreshed")) + " ×)", "Updated"})
	}
	facts = append(facts, apiOption{When(p.I("created_at")+expiryDays*86400) + " unless decided", "Expires"})
	meta := []string{proposalSrcLabel(p.Str("path"))}
	if p.T("host") {
		meta = append(meta, p.Str("host"))
	}
	if p.T("assignee_suggestion") {
		meta = append(meta, "suggested: "+p.Str("assignee_suggestion"))
	}
	if p.T("refreshed") {
		meta = append(meta, "updated "+Ago(p.Get("updated_at")))
	}
	if p.Str("state") == "snoozed" {
		meta = append(meta, "back after "+When(p.Get("snooze_until")))
	}
	meta = append(meta, Text(proposalExpiresIn(p, expiryDays)))
	src := proposalSourceLink(p)
	left := p.I("created_at") + expiryDays*86400 - NowInt()
	return map[string]any{"id": p.I("id"), "title": p.Str("title"), "severity": apiAgentBadge(proposalSevBadge(p.Get("severity"))), "at": py.I(py.Or(p.Get("created_at"), int64(0))), "env": p.S("env"), "meta": meta,
		"source": apiLink{Text: Text(src), Href: firstHref(src)}, "why": p.S("why"), "description": py.Strip(py.OrS(p.Get("description"), "")), "evidence": py.Strip(py.OrS(p.Get("evidence"), "")),
		"fixPrompt": py.Strip(py.OrS(p.Get("fix_prompt"), "")), "facts": facts, "priority": py.OrS(p.Get("priority"), "Medium"), "labels": proposalLabels(p), "suggested": py.OrS(p.Get("assignee_suggestion"), ""),
		"snooze": If(p.Str("state") == "snoozed", "", If(left <= 86400, "expiring", "ok"))}
}

// apiProposalConfirm is the bulk confirm step (proposalConfirm) as data: what is selected and what the action does.
func apiProposalConfirm(sess *Session, f proposalQuery, act string, raw []string, s proposalSettings) map[string]any {
	out := map[string]any{"action": act, "cancel": proposalFullHref(f.href()), "head": "", "body": []string{}, "open": []map[string]any{}, "ids": []int64{}, "gone": []apiOption{}, "skipped": 0, "button": "", "danger": act == "reject", "block": "", "warn": ""}
	var ids []any
	seen := map[string]bool{}
	for _, x := range raw {
		if py.IsDigit(x) && len(x) < 10 && !seen[x] {
			seen[x] = true
			ids = append(ids, py.I(x))
		}
	}
	if !In(act, "accept", "snooze", "reject") {
		out["stop"] = "Unknown action. Nothing was changed."
		return out
	}
	if len(ids) == 0 || len(ids) > proposalMaxIDs || (act == "snooze" && f.view == "snoozed") {
		out["stop"] = "Choose 1 to " + Itoa(proposalMaxIDs) + " proposed tasks, then an action. Nothing was changed."
		return out
	}
	rows := BoardBQ("SELECT id, title, state, severity, env, created_at FROM proposals WHERE id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+") ORDER BY coalesce(sev_rank,0) DESC, id DESC", ids...)
	open, gone, keep := []map[string]any{}, []apiOption{}, []int64{}
	n, expire := 0, 0
	back24 := NowInt() + 86400
	for _, r := range rows {
		if In(r.Str("state"), "pending", "snoozed") {
			n++
			keep = append(keep, r.I("id"))
			open = append(open, map[string]any{"title": r.Str("title"), "severity": apiAgentBadge(proposalSevBadge(r.Get("severity"))), "env": r.S("env")})
			if r.I("created_at")+s.expiryDays*86400 <= back24 {
				expire++
			}
		} else {
			gone = append(gone, apiOption{r.Str("state"), r.Str("title")})
		}
	}
	if n == 0 {
		out["stop"] = "None of the selected proposed tasks is still waiting. They were decided or expired since the list loaded. Nothing was changed."
		return out
	}
	ns, pl := Itoa(n), n != 1
	what := ns + " proposed task" + If(pl, "s", "")
	switch act {
	case "accept":
		noun := proposalIssueNoun(pl)
		dest := "a task on the board and a " + proposalIssueNoun(false) + " in your default project"
		if proposalIssueNoun(false) == "task" {
			dest = "a task on the board"
		}
		out["head"], out["button"] = "Accept "+what+" and create "+ns+" "+noun+"?", "Accept "+ns+" & create "+ns+" "+noun
		out["body"] = []string{"Each one becomes " + dest + ", as proposed: its title, priority, labels and the suggested assignee (if Jira refuses the assignee, it stays unassigned). To change any of that, accept it on its own row instead.",
			If(GitLabDefaultTracker() == "gitlab", "Created with Argus's GitLab project token.", "Created with your read-and-write Jira connection, else the shared one.") + " There is no undo here: close the issue in the tracker if it was a mistake."}
		why := BoardWriteBlock(sess)
		if GitLabDefaultTracker() == "gitlab" {
			why = GitLabWriteBlock()
		}
		if why != "" {
			out["block"] = "Cannot accept now: " + why
		}
	case "snooze":
		out["head"], out["button"] = "Snooze "+what+" for 24 hours?", "Snooze "+ns+" for 24 h"
		out["body"] = []string{"They leave the waiting list and come back at about " + When(back24) + " (the first board sync after that). Snoozing does not extend the " + I2S(s.expiryDays) + "-day expiry, which counts from when each was proposed."}
		if expire > 0 {
			out["warn"] = Itoa(expire) + " of them expire before they come back, and are then recorded as rejected (\"expired\")."
		}
	case "reject":
		out["head"], out["button"], out["reason"] = "Reject "+what+"?", "Reject "+ns, proposalMaxReason
		out["body"] = []string{"Rejected proposals are kept in Decided with your name and reason. Argus does not propose the same source again for " + I2S(s.rejectDays) + " days, unless its severity rises. There is no undo."}
	}
	out["open"], out["ids"], out["gone"], out["skipped"] = open, keep, gone, len(ids)-n
	return out
}

func apiProposalLegacy(rows []Row) []map[string]any {
	st := BoardStatus()
	out := []map[string]any{}
	for _, t := range rows {
		var base any
		if t.Has("jira_url") {
			base = t.Get("jira_url")
		}
		key := py.OrS(t.Get("jira_key"), "SIR-"+I2S(t.I("id")))
		href := ""
		if jl := BoardJiraLink(st, t.Get("jira_key"), base); jl != "" && t.T("jira_key") {
			href = apiSafeHref(jl)
		}
		out = append(out, map[string]any{"key": apiLink{Text: key, Href: href}, "title": t.Str("title"), "href": "/board/task?id=" + I2S(t.I("id")), "source": apiSource(t), "status": py.OrS(t.Get("jira_status"), "not synced"),
			"gone": t.T("gone"), "env": t.S("env"), "at": py.I(py.Or(t.Get("created_at"), int64(0)))})
	}
	return out
}

// apiProposalsJSON is GET /api/v1/proposals: one of the three views, or the bulk confirm step.
func apiProposalsJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if sess.Role != "admin" {
		return nil, &apiErr{403, "admins only"}
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	f := proposalParse(qs)
	s := proposalsSettings()
	day, now := proposalDayStart(), NowInt()
	counts := map[string]int64{}
	for _, r := range BoardBQ("SELECT state, count(*) n FROM proposals GROUP BY state") {
		counts[r.Str("state")] = r.I("n")
	}
	acc := proposalCount("SELECT count(*) n FROM proposals WHERE state='accepted' AND decided_at>=?", day)
	rej := proposalCount("SELECT count(*) n FROM proposals WHERE state IN ('rejected','expired') AND decided_at>=?", day)
	hi := proposalCount("SELECT count(*) n FROM proposals WHERE state='pending' AND coalesce(sev_rank,0)>=3")
	soon := proposalCount("SELECT count(*) n FROM proposals WHERE state='pending' AND created_at<=?", now+proposalSoonDays*86400-s.expiryDays*86400)
	nDec := proposalCount("SELECT count(*) n FROM proposals WHERE state IN ('accepted','rejected','expired') AND decided_at>=?", now-proposalDecidedDays*86400)
	old := proposalLegacy()
	pend := counts["pending"]
	on := proposalOn(s)
	hiQ := proposalQuery{view: "pending", sort: "priority", sev: []string{"critical", "high"}, page: 1}
	soonQ := proposalQuery{view: "pending", sort: "expiring", page: 1}
	tiles := []apiTile{{Label: "Waiting", Figure: I2S(pend), Sub: "to accept, snooze or reject", Href: proposalFullHref("/proposals")},
		{Label: "Critical or high", Figure: I2S(hi), Sub: "waiting, worst first", Href: proposalFullHref(hiQ.href()), Current: f.view == "pending" && strings.Join(f.sev, ",") == "critical,high" && f.q == "" && f.src == "" && f.env == ""},
		{Label: "Expire within 48 h", Figure: I2S(soon), Sub: "if nobody decides", Href: proposalFullHref(soonQ.href())},
		{Label: "Accepted today", Figure: I2S(acc), Sub: "created as " + proposalIssueNoun(true), Href: proposalFullHref("/proposals?view=decided")}}
	tabs := []map[string]any{}
	for _, v := range proposalViews {
		n := counts[v[0]]
		if v[0] == "decided" {
			n = nDec
		}
		tabs = append(tabs, map[string]any{"label": v[1] + If(v[0] == "decided", " (14 d)", ""), "count": n, "current": v[0] == f.view, "href": proposalFullHref("/proposals" + If(v[0] != "pending", "?view="+v[0], ""))})
	}
	pairs := [][2]string{{"kind", "proposals"}, {"mode", "full"}}
	for _, p := range f.pairs() {
		if p[0] != "q" && p[0] != "page" {
			pairs = append(pairs, p)
		}
	}
	bulkPairs := [][2]string{{"kind", "proposals"}, {"mode", "full"}}
	bulkPairs = append(bulkPairs, f.pairs()...)
	auto := []map[string]any{}
	for _, p := range proposalPaths {
		auto = append(auto, map[string]any{"key": p[0], "label": p[1], "on": s.auto[p[0]]})
	}
	out := map[string]any{"view": f.view, "pending": pend, "autoOn": on, "expiryDays": s.expiryDays, "tiles": tiles, "tabs": tabs, "rejectedToday": rej, "legacy": apiProposalLegacy(old), "banners": apiNotices(BoardRequestBanner(c, qs)),
		"here": proposalFullHref(f.href()), "q": f.q, "search": pairs, "chips": []apiChipRow{}, "sorts": []apiOption{}, "sort": f.sort, "filtered": f.filtered(), "clear": proposalFullHref(proposalQuery{view: f.view, sort: f.sort, page: 1}.href()),
		"settings": map[string]any{"auto": auto, "expiryDays": s.expiryDays, "rejectDays": s.rejectDays, "note": If(len(on) > 0, "auto-create ON for "+strings.Join(on, ", "), "auto-create off")},
		"rows":     []map[string]any{}, "decided": []map[string]any{}, "confirm": nil, "total": 0, "matched": 0, "pager": nil, "maxReason": proposalMaxReason}
	if act := qs.Get("confirm"); act != "" && f.view != "decided" {
		out["confirm"] = apiProposalConfirm(sess, f, act, qs.List("id"), s)
		out["refresh"] = c.Refresh
		return out, nil
	}
	w, a := f.where("")
	matched := proposalCount("SELECT count(*) n FROM proposals WHERE "+w, a...)
	if pages := int((matched + proposalPageSize - 1) / proposalPageSize); f.page > pages && pages > 0 {
		f.page = pages
	}
	pages := max(1, int((matched+proposalPageSize-1)/proposalPageSize))
	pager := map[string]any{"page": f.page, "pages": pages, "matched": matched, "from": min(int64((f.page-1)*proposalPageSize+1), matched), "to": min(int64(f.page*proposalPageSize), matched)}
	if f.page > 1 {
		pager["prev"] = proposalFullHref(f.with(func(g *proposalQuery) { g.page = f.page - 1 }).href())
	}
	if f.page < pages {
		pager["next"] = proposalFullHref(f.with(func(g *proposalQuery) { g.page = f.page + 1 }).href())
	}
	out["pager"], out["matched"], out["chips"] = pager, matched, apiProposalChips(f)
	if f.view == "decided" {
		list := []map[string]any{}
		for _, p := range BoardBQ("SELECT * FROM proposals WHERE "+w+" ORDER BY decided_at DESC, id DESC LIMIT ? OFFSET ?", append(a, proposalPageSize, (f.page-1)*proposalPageSize)...) {
			src := proposalSourceLink(p)
			x := map[string]any{"id": p.I("id"), "at": py.I(py.Or(p.Get("decided_at"), int64(0))), "title": p.Str("title"), "severity": apiAgentBadge(proposalSevBadge(p.Get("severity"))), "source": apiLink{Text: Text(src), Href: firstHref(src)},
				"state": map[string]any{"text": p.Str("state"), "kind": proposalStateKind[p.Str("state")]}, "by": py.OrS(p.Get("decided_by"), "-"), "reason": py.OrS(p.Get("reason"), "-")}
			if p.T("task_id") {
				x["task"] = apiLink{Text: "SIR-" + I2S(p.I("task_id")), Href: "/board/task?id=" + I2S(p.I("task_id"))}
			}
			list = append(list, x)
		}
		out["decided"], out["total"] = list, nDec
		return out, nil
	}
	sorts := []apiOption{}
	for _, so := range proposalSorts {
		sorts = append(sorts, apiOption{so.key, so.label})
	}
	out["sorts"], out["total"] = sorts, counts[f.view]
	order := proposalSorts[0].order
	for _, so := range proposalSorts {
		if so.key == f.sort {
			order = so.order
		}
	}
	rows := BoardBQ("SELECT * FROM proposals WHERE "+w+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(a, proposalPageSize, (f.page-1)*proposalPageSize)...)
	list := []map[string]any{}
	for _, p := range rows {
		list = append(list, apiProposalRow(p, s.expiryDays))
	}
	project, projects := proposalProjectChoices(sess)
	users := []apiOption{}
	if project != "" && len(rows) > 0 {
		for _, u := range proposalAssignable(project) {
			users = append(users, apiOption{u.name, u.display})
		}
	}
	sel := qs.Get("sel") == "page"
	head := f.href()
	out["rows"], out["picked"], out["bulk"] = list, sel, bulkPairs
	out["selHref"] = proposalFullHref(If(sel, head, head+If(strings.Contains(head, "?"), "&", "?")+"sel=page"))
	accept := map[string]any{"noun": proposalIssueNoun(false), "users": users, "project": project, "projects": append([]string{}, projects...), "priorities": proposalPriorities, "tracker": GitLabDefaultTracker(), "maxTitle": proposalMaxTitle,
		"button": If(GitLabConnected(), "Accept & create issue", "Accept & create "+proposalIssueNoun(false)),
		"note":   If(GitLabConnected(), "Project applies to Jira only; in GitLab the assignee is matched by user name.", "Created with your Jira connection (read and write), else the shared one.")}
	if GitLabConnected() {
		st := GitLabStatus()
		sub := "Project " + py.OrS(py.AsMap(st.Get("connection")).Get("project_path"), "the connected project") + ", with Argus's project token."
		glp := []apiOption{}
		if gitlabGroupMode(st) {
			sub = "Group board " + py.OrS(py.AsMap(st.Get("connection")).Get("group_path"), "?") + ", in the project below."
			for _, p := range gitlabProjects(st) {
				glp = append(glp, apiOption{p[0], p[1]})
			}
		}
		accept["trackers"] = map[string]any{"jira": py.OrS(BoardWriteBlock(sess), "Your Jira connection, else the shared one."), "gitlab": sub, "projects": glp, "project": gitlabDefaultProject(st)}
	}
	out["accept"], out["bulkNoun"] = accept, proposalIssueNoun(true)
	return out, nil
}

func proposalsAPIPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	return apiResult(ProposalsPost(&Ctx{Sess: sess, Theme: "auto", Path: "/review"}, strings.TrimPrefix(path, "/api/v1"), apiForm(body), sess))
}
