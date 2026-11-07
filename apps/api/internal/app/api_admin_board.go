package app

// Settings → Jira sync as JSON for the Next.js page (ADR-0018): GET /api/v1/admin/board and the twins of POST /admin/board,
// /admin/jira-hosts, /admin/jira-shared, /admin/agent and /admin/agent/fixer-move. Admins only. It reads the sync's status
// files field by field, so nothing they might carry beyond these reaches the page; a Jira token is written once by the
// route and never read back.

import (
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func apiStrs(v any) []string {
	out := jiraStrList(py.Or(v, []any{}))
	if out == nil {
		return []string{}
	}
	return out
}

func apiBoardFails(st *py.OMap) []map[string]any {
	out := []map[string]any{}
	for _, rv := range py.AsList(py.Or(st.Get("recent_failures"), []any{})) {
		r := py.AsMap(rv)
		key := py.OrS(r.Get("jira_key"), "")
		if key == "" {
			key = "SIR-" + py.Str(r.Get("task_id"))
		}
		out = append(out, map[string]any{"at": py.I(py.Or(r.Get("done_at"), int64(0))), "kind": py.Str(r.GetD("kind", "")), "key": key,
			"taskId": py.I(py.Or(r.Get("task_id"), int64(0))), "error": py.OrS(r.Get("last_error"), "")})
	}
	return out
}

func apiBoardRetry(st *py.OMap) []map[string]any {
	out := []map[string]any{}
	for _, rv := range py.AsList(py.Or(st.Get("retrying"), []any{})) {
		r := py.AsMap(rv)
		out = append(out, map[string]any{"at": py.I(py.Or(r.Get("next_at"), int64(0))), "kind": py.Str(r.GetD("kind", "")),
			"attempts": py.I(py.Or(r.Get("attempts"), int64(0))), "error": py.OrS(r.Get("last_error"), "")})
	}
	return out
}

func apiBoardAgent(st *py.OMap) map[string]any {
	a := py.AsMap(py.Or(st.Get("agent"), py.NewOMap()))
	s := py.AsMap(py.Or(a.Get("settings"), py.NewOMap()))
	tasks := []map[string]any{}
	for _, rv := range py.AsList(py.Or(a.Get("fixer_tasks"), []any{})) {
		r := py.AsMap(rv)
		tasks = append(tasks, map[string]any{"id": py.I(py.Or(r.Get("id"), int64(0))), "key": py.Str(r.Get("key")), "tracker": py.Str(r.Get("tracker")),
			"env": py.OrS(r.Get("env"), "-"), "fixState": py.OrS(r.Get("fix_state"), "-"), "title": py.Str(r.Get("title"))})
	}
	on := func(k string, d bool) bool { return py.Truthy(s.GetD(k, d)) }
	return map[string]any{"tick": py.OrS(a.Get("tick"), "-"), "error": py.OrS(a.Get("error"), ""), "rwUsers": apiStrs(a.Get("rw_users")), "fixerTasks": tasks,
		"settings": map[string]any{"enabled": on("enabled", true), "assignees": strings.Join(apiStrs(s.Get("assignees")), ", "), "column": py.OrS(s.Get("column"), "In Progress"),
			"autoDone": on("auto_done", false), "qaEnabled": on("qa_enabled", true), "qaThreshold": py.Str(s.GetD("qa_threshold", "high")), "qaCap": py.I(py.Or(s.Get("qa_cap"), int64(10))),
			"qaOwner": py.OrS(s.Get("qa_owner"), ""), "fixerTrigger": py.OrS(s.Get("fixer_trigger"), "off"), "chat": on("chat", false), "secrets": on("secrets", false)}}
}

func apiJiraConnections(js *py.OMap) []map[string]any {
	users := py.AsMap(js.Get("users"))
	keys := append([]string(nil), users.Keys()...)
	sort.Strings(keys)
	out := []map[string]any{}
	for _, u := range keys {
		cn := py.AsMap(users.Get(u))
		out = append(out, map[string]any{"user": u, "identity": py.OrS(py.Or(cn.Get("display"), cn.Get("jira_name")), ""), "host": py.OrS(cn.Get("host"), ""),
			"mode": If(cn.Get("mode") == "rw", "read and write", "read-only"), "projects": strings.Join(apiStrs(cn.Get("projects")), ", "),
			"lastSync": py.I(py.Or(cn.Get("last_sync"), int64(0))), "lastError": py.OrS(cn.Get("last_error"), "")})
	}
	return out
}

// apiJiraShared is the shared connection: nil before it exists, else its facts and what the settings form offers.
func apiJiraShared(js, bs *py.OMap) map[string]any {
	m := py.AsMap(py.Or(js.Get("shared"), nil))
	if m == nil || !py.Truthy(m.Get("url")) {
		return nil
	}
	projects, boards := []apiOption{}, []apiOption{}
	for _, pv := range py.AsList(py.Or(m.Get("available"), []any{})) {
		if p := py.AsMap(pv); p != nil && jiraProjectRe.MatchString(py.Str(p.GetD("key", ""))) {
			projects = append(projects, apiOption{py.Str(p.Get("key")), py.Str(p.Get("key")) + " · " + py.Str(p.GetD("name", ""))})
		}
	}
	for _, bv := range py.AsList(py.Or(m.Get("boards"), []any{})) {
		if b := py.AsMap(bv); b != nil && py.IsDigit(py.Str(b.GetD("id", ""))) {
			id := I2S(py.I(b.Get("id")))
			boards = append(boards, apiOption{id, id + " · " + py.Str(b.GetD("name", ""))})
		}
	}
	return map[string]any{"facts": apiKV(jiraSharedKV(js, m, bs)), "url": py.Str(m.Get("url")), "project": py.OrS(m.Get("project"), ""), "boardId": py.OrS(m.Get("board_id"), ""),
		"mode": py.OrS(m.Get("mode"), "ro"), "approvers": py.OrS(m.Get("approvers"), ""), "component": py.OrS(m.Get("component"), ""),
		"projects": projects, "boards": boards, "components": apiStrs(m.Get("components"))}
}

func apiAdminBoardJSON(qs py.QS, sess *Session) (any, *apiErr) {
	if e := apiAdminOnly(sess); e != nil {
		return nil, e
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: boardPathOf("/admin/board", qs)}
	st, js := BoardStatus(), JiraState()
	notices := apiNotices(BoardRequestBanner(c, qs) + jiraResult(c, qs.Get("jira"), sess.Username))
	facts := apiKV(boardAdminKV(st))
	if u := py.Str(st.GetD("jira_url", "")); strings.HasPrefix(u, "https://") && py.IsDigit(py.Str(st.GetD("board_id", ""))) {
		for i := range facts {
			if facts[i].Label == "Board" {
				facts[i].Href = u + "/secure/RapidBoard.jspa?rapidView=" + I2S(py.I(st.Get("board_id")))
			}
		}
	}
	return map[string]any{"user": sess.Username, "host": ARGUS_HOSTNAME, "project": If(py.Truthy(st.Get("configured")), "Jira "+py.Str(st.GetD("project", "")), "Local board"),
		"queued": py.I(py.Or(st.Get("queued"), int64(0))), "state": apiNotices(boardAdminState(st)), "problems": apiStrs(st.Get("problems")),
		"notices": notices, "refresh": apiRefresh(c), "connection": facts, "shared": apiJiraShared(js, st),
		"autoIncidents": py.Truthy(py.AsMap(py.Or(st.Get("settings"), nil)).Get("auto_incidents")),
		"failed":        apiBoardFails(st), "retrying": apiBoardRetry(st), "agent": apiBoardAgent(st),
		"hosts": strings.Join(apiStrs(js.Get("allowed_hosts")), "\n"), "connections": apiJiraConnections(js)}, nil
}
