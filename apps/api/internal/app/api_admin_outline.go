package app

// Settings → Integrations as JSON for the Next.js pages (ADR-0018): GET /api/v1/admin/outline holds the Outline and GitLab
// issues sections, GET /api/v1/admin/outline/page one built page. Admins only, like every /admin path. The API key and the
// token go to root once; only the fields named here reach an answer, so nothing else in root's status files can.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// apiHTTPS is a link root published, kept only when it is https (the Go pages' rule).
func apiHTTPS(v any) string {
	if s := py.Str(v); strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

func apiOutlineRow(r Row, ps *py.OMap) map[string]any {
	svc := r.Str("service")
	p := py.AsMap(ps.Get(svc))
	same := sameVal(r.Get("changed_at"), r.Get("rendered_at"))
	local := map[string]string{"kind": "none", "text": "no change"}
	switch {
	case r.T("blocked"):
		local = map[string]string{"kind": "fail", "text": "blocked"}
	case r.T("prev_hash") && same:
		local = map[string]string{"kind": "warn", "text": "changed"}
	case !r.T("prev_hash") && same:
		local = map[string]string{"kind": "neutral", "text": "new"}
	}
	state := map[string]string(nil)
	if st := p.Get("state"); py.Truthy(st) {
		s := py.Str(st)
		lab, ok := outlineStateLabel[s]
		if !ok {
			lab = s
		}
		state = map[string]string{"kind": outlineStateKind[s], "text": lab}
	}
	name := svc
	if svc == "_index" {
		name = "Services overview"
	}
	return map[string]any{"service": svc, "name": name, "local": local, "state": state, "url": apiHTTPS(p.GetD("url", "")),
		"changed": If(r.T("changed_at"), When(r.Get("changed_at")), "-"), "note": py.OrS(py.Or(p.Get("error"), r.Get("blocked")), "")}
}

func apiOutlineState(st, run *py.OMap, mode string) string {
	switch {
	case !py.Truthy(st):
		return StateBox(StNever, "The Outline job has not run yet.", "Built pages and the last run appear here after the first run of argus-outline.timer on "+ARGUS_HOSTNAME+".", "")
	case py.Truthy(run.Get("error")):
		return StateBox(StFailed, "The last Outline run failed, "+Ago(run.Get("at"))+".", "Error: "+SafeErr(py.Str(run.Get("error")))+". Pages shown may be out of date; the job tries again on its timer.", "")
	case mode == "publish":
		counts := py.AsMap(py.Or(run.Get("counts"), py.NewOMap()))
		keys := append([]string(nil), counts.Keys()...)
		sort.Strings(keys)
		cs := []string{}
		for _, k := range keys {
			cs = append(cs, py.Str(counts.Get(k))+" "+k)
		}
		return Banner("Publishing to Outline; last run "+Ago(run.Get("at"))+": "+py.OrS(strings.Join(cs, ", "), "nothing to do")+".", "ok")
	}
	return Banner("Preview mode: the pages are built here only. Nothing is sent to Outline until you choose Publish.", "warn")
}

func apiGitlabState(st, run *py.OMap, connected bool) string {
	state := ""
	switch {
	case !py.Truthy(st):
		state = StateBox(StNever, "The GitLab sync has not run yet.", "Its state and the last sync time appear here after the first run of argus-board-sync on "+ARGUS_HOSTNAME+".", "")
	case connected && py.Truthy(run.Get("error")):
		state = StateBox(StFailed, "The last GitLab sync failed, "+Ago(run.Get("at"))+".", "Error: "+SafeErr(py.Str(run.Get("error")))+". Issues shown may be out of date; the sync tries again on its timer.", "")
	case connected && py.Truthy(run.Get("at")):
		state = Banner("Syncing with GitLab; last run "+Ago(run.Get("at"))+".", "ok")
	}
	for _, w := range py.AsList(st.Get("warnings")) {
		state += Banner(py.Str(w), "warn")
	}
	return state
}

// apiGitlabSection is the GitLab issues part of Settings → Integrations.
func apiGitlabSection(c *Ctx, qs py.QS, sess *Session) map[string]any {
	st := GitLabStatus()
	conn := py.AsMap(py.Or(st.Get("connection"), py.NewOMap()))
	connected := py.Truthy(st.Get("connected"))
	group := gitlabGroupMode(st)
	res := gitlabResult(c, qs.Get("g"), sess.Username)
	run := py.AsMap(py.Or(st.Get("last_run"), py.NewOMap()))
	s := py.AsMap(py.Or(st.Get("settings"), py.NewOMap()))
	prefill := py.OrS(conn.Get("project_path"), "")
	if group {
		prefill = "groups/" + py.OrS(conn.Get("group_path"), "")
	}
	out := map[string]any{"connected": connected, "group": group, "notices": apiNotices(res + apiGitlabState(st, run, connected)),
		"url": py.OrS(conn.Get("url"), py.OrS(st.Get("default_url"), "")), "project": prefill, "hosts": append([]string{}, jiraStrList(py.Or(st.Get("allowed_hosts"), []any{}))...),
		"default_tracker": py.OrS(s.Get("default_tracker"), "jira"), "list_map": gitlabPairs(py.AsMap(py.Or(s.Get("list_map"), py.NewOMap()))),
		"user_map": strings.ReplaceAll(gitlabPairs(py.AsMap(py.Or(s.Get("user_map"), py.NewOMap()))), "; ", ", ")}
	if !connected {
		return out
	}
	bot := py.AsMap(py.Or(conn.Get("bot"), py.NewOMap()))
	where := py.OrS(conn.Get("project_path"), "?")
	if group {
		where = py.OrS(conn.Get("group_path"), "?")
	}
	boards := []apiOption{}
	if l := py.AsList(st.Get("boards")); len(l) >= 2 {
		for _, b := range l {
			m := py.AsMap(b)
			id := I2S(py.I(m.Get("id")))
			boards = append(boards, apiOption{Value: id, Label: py.Str(m.Get("name")) + " (" + id + ")"})
		}
	}
	projects := []apiOption{}
	if group {
		for _, p := range gitlabProjects(st) {
			projects = append(projects, apiOption{Value: p[0], Label: p[1]})
		}
	}
	lists := [][2]string{{"Open (no list label)", "To do"}}
	for _, l := range py.AsList(st.Get("lists")) {
		m := py.AsMap(l)
		col, ok := BoardLabel[py.Str(m.Get("column"))]
		if !ok {
			col = py.Str(m.Get("column"))
		}
		lists = append(lists, [2]string{py.Str(m.Get("label")), col})
	}
	lists = append(lists, [2]string{"Closed", "Done"})
	out["connection"] = map[string]any{"gitlab": py.OrS(conn.Get("url"), ""), "where_kind": If(group, "Group", "Project"), "where": where, "web_url": apiHTTPS(conn.Get("web_url")),
		"board": py.OrS(conn.Get("board_name"), "?"), "board_id": py.I(py.Or(conn.Get("board_id"), int64(0))), "default_project": py.OrS(conn.Get("default_project_path"), ""),
		"token_user": py.OrS(bot.Get("username"), "?"), "bot": py.Truthy(bot.Get("bot")), "since": When(conn.Get("connected_at")), "by": py.OrS(conn.Get("by"), "?")}
	out["boards"], out["projects"], out["lists"], out["default_project"] = boards, projects, lists, gitlabDefaultProject(st)
	return out
}

func apiAdminOutlineJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/admin/outline"}
	st := outlineStatus()
	s := py.AsMap(py.Or(st.Get("settings"), py.NewOMap()))
	conn := py.AsMap(py.Or(st.Get("connection"), py.NewOMap()))
	run := py.AsMap(py.Or(st.Get("last_run"), py.NewOMap()))
	mode := py.OrS(s.Get("mode"), "preview")
	res := outlineResult(c, qs.Get("r"), sess.Username)
	ps := py.AsMap(st.Get("pages"))
	pages, built := []map[string]any{}, 0
	for _, r := range outlinePages() {
		pages = append(pages, apiOutlineRow(r, ps))
		if r.Get("service") != "_index" {
			built++
		}
	}
	gitlab := apiGitlabSection(c, qs, sess)
	connected := py.Truthy(conn)
	url := py.OrS(conn.Get("url"), "")
	if url == "" {
		url = py.OrS(s.Get("default_url"), outlineDefaultURL)
	}
	out := map[string]any{"host": ARGUS_HOSTNAME, "user": sess.Username, "outline_host": OUTLINE_HOST, "notices": apiNotices(res + apiOutlineState(st, run, mode)), "refresh": apiRefresh(c),
		"connected": connected, "mode": mode, "scope": py.OrS(s.Get("scope"), "pilot"), "url": url, "collection": py.OrS(conn.Get("collection"), "Services"),
		"hosts": jiraStrList(py.Or(st.Get("allowed_hosts"), []any{OUTLINE_HOST})), "built": built, "pages": pages, "gitlab": gitlab}
	if connected {
		out["connection"] = map[string]any{"url": py.OrS(conn.Get("url"), ""), "user": py.OrS(conn.Get("user"), "?"), "team": py.OrS(conn.Get("team"), "?"),
			"collection": py.OrS(conn.Get("collection"), "?"), "since": When(conn.Get("connected_at")), "by": py.OrS(conn.Get("by"), "?")}
	}
	return out, nil
}

func apiOutlinePageJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	svc := qs.Get("s")
	if !outlineSvcRe.MatchString(svc) {
		return nil, &apiErr{404, "no such page"}
	}
	rr := Q("SELECT * FROM outline_pages WHERE service=?", svc)
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such page"}
	}
	r := rr[0]
	st := outlineStatus()
	p := py.AsMap(py.AsMap(st.Get("pages")).Get(svc))
	mode := py.OrS(py.AsMap(st.Get("settings")).Get("mode"), "preview")
	red := py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(r.Get("redacted"), "{}"))); err == nil {
		if m := py.AsMap(v); py.Truthy(m) {
			red = m
		}
	}
	scan := "clean"
	if r.T("blocked") {
		scan = "blocked: " + r.Str("blocked")
	} else if red.Len() > 0 {
		xs := []string{}
		for _, k := range red.Keys() {
			xs = append(xs, k+" ×"+py.Str(red.Get(k)))
		}
		scan = "redacted " + strings.Join(xs, ", ")
	}
	url := apiHTTPS(p.GetD("url", ""))
	label := "not published" + If(mode != "publish", " (preview mode)", "")
	if url != "" {
		ok := false
		if label, ok = outlineStateLabel[py.Str(p.Get("state"))]; !ok {
			label = py.OrS(p.Get("state"), "open")
		}
	}
	diff := map[string]any(nil)
	if r.T("prev_body") {
		lines, more := outlineDiffLines(r.S("prev_body"), r.S("body"))
		diff = map[string]any{"lines": lines, "more": more}
	}
	name, title := svc, r.Str("title")
	if svc == "_index" {
		name, title = "Services overview", "Services overview"
	}
	return map[string]any{"service": svc, "name": name, "title": title, "mode": mode, "hash": r.S("body_hash"), "built": When(r.Get("rendered_at")), "changed": When(r.Get("changed_at")),
		"scan": scan, "blocked": r.T("blocked"), "outline": map[string]string{"label": label, "url": url}, "flags": append([]string{}, jiraStrList(p.Get("flags"))...), "diff": diff,
		"body": r.S("body"), "view": outlineQuoteRe.ReplaceAllString(r.S("body"), "")}, nil
}
