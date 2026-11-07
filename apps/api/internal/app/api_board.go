package app

// The Work section's board as JSON for the Next.js pages (ADR-0018): GET /api/v1/board, /board/task and /board/new, and
// the twins of the board's POST routes. Same queries, request files, roles and 2FA policy as the Go pages; every write
// goes through BoardPost, so its checks stay in one place. Text only: the frontend never takes HTML from the API.

import (
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// apiBodyLimit: a twin of a Go route may carry what the Go route carries (a new task is far over the API's default).
func apiBodyLimit(path string) int64 {
	if strings.HasPrefix(path, "/api/v1/board/") {
		if n, ok := postLimits[strings.TrimPrefix(path, "/api/v1")]; ok {
			return n
		}
	}
	if strings.HasPrefix(path, "/api/v1/admin/") {
		if n, ok := postLimits[strings.TrimPrefix(path, "/api/v1")]; ok {
			return n
		}
	}
	if path == "/api/v1/fix/review" {
		return postLimits["/admin/fix/review"]
	}
	if strings.HasPrefix(path, "/api/v1/proposals/") || strings.HasPrefix(path, "/api/v1/ideas/") {
		if n, ok := postLimits[strings.TrimPrefix(path, "/api/v1")]; ok {
			return n
		}
	}
	if path == "/api/v1/lessons/act" {
		return postLimits["/admin/lessons/act"]
	}
	return apiMaxBody
}

func boardAPIPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/board"}
	return apiResult(BoardPost(c, strings.TrimPrefix(path, "/api/v1"), apiForm(body), sess))
}

type apiNotice struct {
	Text string    `json:"text"`
	Kind string    `json:"kind"`
	Segs []apiLink `json:"segs,omitempty"`
}

var reBoldOf = regexp.MustCompile(`<b>.*?</b>`)

var reBannerAny = regexp.MustCompile(`(?s)<div class="banner ?([a-z]*)"[^>]*>(.*?)</div>`)

// apiNotices is a banner as text, with its links kept as segments.
func apiNotices(html string) []apiNotice {
	out := []apiNotice{}
	for _, m := range reBannerAny.FindAllStringSubmatch(html, -1) {
		out = append(out, apiNotice{Text: Text(m[2]), Kind: py.OrS(m[1], "info"), Segs: apiSegs(m[2])})
	}
	return out
}

// boardPathOf is the page's own path and query, the form's return value (without the request banner).
func boardPathOf(path string, qs py.QS) string {
	if keep := qs.Pairs("req"); len(keep) > 0 {
		return path + "?" + py.Urlencode(keep)
	}
	return path
}

func apiSource(t Row) any {
	if !t.T("source_kind") {
		return nil
	}
	h := BoardSourceTag(t)
	return apiLink{Text: Text(h), Href: firstHref(h)}
}

func apiFixNote(t Row) any {
	h := boardFixNote(t)
	if h == "" {
		return nil
	}
	kind := "warn"
	if strings.Contains(h, "badge ok") {
		kind = "ok"
	} else if strings.Contains(h, "badge fail") {
		kind = "fail"
	}
	return map[string]any{"text": Text(h), "kind": kind, "title": If(kind == "fail", t.S("fix_state"), "")}
}

func apiAgentBadge(h string) any {
	if h == "" {
		return nil
	}
	kind := "neutral"
	for _, k := range []string{"ok", "warn", "fail"} {
		if strings.Contains(h, `class="badge `+k) {
			kind = k
		}
	}
	return map[string]any{"text": Text(h), "kind": kind}
}

func apiColOpts(cols []any, skip int64) []apiOption {
	out := []apiOption{}
	for i, cv := range cols {
		if int64(i) != skip {
			out = append(out, apiOption{Itoa(i), py.Str(py.AsMap(cv).Get("name"))})
		}
	}
	return out
}

func apiMoveOpts(t Row) []apiOption {
	out := []apiOption{}
	for _, col := range BoardColumns {
		if col != t.Str("status") && col != "fixer" {
			out = append(out, apiOption{col, BoardLabel[col]})
		}
	}
	return out
}

// apiTaskCard is boardCard as data.
func apiTaskCard(t Row, sess *Session) map[string]any {
	open := t.Str("status") != "done"
	key := AgentKey(t)
	var agent, plan any
	if open && key != "" {
		if a := agentRow(key); a != nil {
			agent = apiAgentBadge(AgentRowBadge(a))
		}
		if d := agentCardData(key); d != nil {
			plan = d
		}
	}
	ts, verb := t.Get("updated_at"), "updated"
	if !py.Truthy(ts) {
		ts, verb = t.Get("created_at"), "opened"
	}
	pr := t.S("priority")
	out := map[string]any{"ref": BoardRef(t), "tag": Text(TrackerTag(t)), "title": t.Str("title"), "href": "/board/task?id=" + I2S(t.I("id")),
		"owner": py.OrS(py.Or(t.Get("assignee_name"), t.Get("assignee")), ""), "open": open, "priority": If(pr != "Medium", pr, ""),
		"priorityHigh": pr == "Highest" || pr == "High", "env": t.S("env"), "source": apiSource(t), "auto": t.Get("created_by") == "argus-auto",
		"verb": verb, "at": py.I(py.Or(ts, int64(0))), "age": BoardShortAgo(ts), "fix": apiFixNote(t), "agent": agent, "plan": plan, "badges": []any{}}
	if boardCanChangeTask(sess, t) {
		out["move"] = map[string]any{"kind": "task", "task": t.I("id"), "options": apiMoveOpts(t)}
	}
	return out
}

// apiJiraCard is jboardCard as data.
func apiJiraCard(r, task Row, sess *Session, can bool, cols []any, agent string, mx any, hasMx bool) map[string]any {
	key := r.Str("key")
	badges := []any{}
	if hasMx {
		if b := Text(MatrixBadge(mx)); b != "" {
			badges = append(badges, map[string]any{"text": b, "kind": "neutral"})
		}
	}
	if agent != "" {
		if b := apiAgentBadge(AgentBadge(agent)); b != nil {
			badges = append(badges, b)
		}
	}
	if In(JBoardFixerLabl, boardJSONList(r.Get("labels"))...) && FixerTriggerOn() {
		badges = append(badges, map[string]any{"text": "Ready for fixer", "kind": "warn"})
	}
	href := ""
	var source any
	fixPrompt := false
	if task != nil {
		href = "/board/task?id=" + I2S(task.I("id"))
		source = apiSource(task)
		fixPrompt = py.Strip(task.S("fix_prompt")) != ""
		if In(task.S("fix_state"), "requested", "started") {
			badges = append(badges, map[string]any{"text": "fix running", "kind": "warn"})
		}
	} else if l := BoardJiraLink(py.NewOMap(), r.Get("key"), nil); l != "" {
		href = l
	}
	pr := r.S("priority")
	out := map[string]any{"ref": key, "tag": py.OrS(r.Get("type"), "Issue"), "title": r.Str("summary"), "href": href,
		"owner": py.OrS(py.Or(r.Get("assignee_name"), r.Get("assignee")), ""), "open": true, "priority": If(pr != "Medium", pr, ""),
		"priorityHigh": In(pr, jboardHiPrio...), "source": source, "fixPrompt": fixPrompt, "badges": badges}
	if agent != "" {
		if d := agentCardData(key); d != nil {
			out["plan"] = d
		}
	}
	if can && sess.Role == "admin" {
		skip := int64(-1)
		if r.Get("col") != nil {
			skip = r.I("col")
		}
		out["move"] = map[string]any{"kind": "jira", "key": key, "options": apiColOpts(cols, skip)}
	}
	return out
}

func apiChip(label, href string, on bool, title string) map[string]any {
	return map[string]any{"label": label, "href": href, "on": on, "title": title}
}

func boardHead(sess *Session, qs py.QS, path string) map[string]any {
	return map[string]any{"return": boardPathOf(path, qs), "ask": AgentChatOn(), "admin": sess.Role == "admin"}
}

// apiBoardJSON is GET /api/v1/board: the Jira mirror when one is configured, else Argus's own board.
func apiBoardJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: boardPathOf("/board", qs)}
	var out map[string]any
	if b, _ := JBoardBoard(); b != nil && !In(qs.Get("tracker"), "gitlab", "all") {
		out = apiJBoard(c, qs, sess)
	} else {
		out = apiLocalBoard(c, qs, sess)
	}
	for k, v := range boardHead(sess, qs, "/board") {
		out[k] = v
	}
	out["notices"] = apiNotices(BoardRequestBanner(c, qs))
	out["matrix"] = apiMatrixWaiting(sess)
	out["tabs"] = apiTrackerTabs(py.Str(out["tracker"]))
	out["newHref"] = boardNewHref(c.Path)
	out["refresh"] = apiRefresh(c)
	return out, nil
}

func apiTrackerTabs(cur string) []map[string]any {
	out := []map[string]any{}
	if !GitLabConnected() {
		return out
	}
	for _, x := range [][3]string{{"", "/board", "Jira board"}, {"gitlab", "/board?tracker=gitlab", "GitLab issues"}, {"all", "/board?tracker=all", "All tasks"}} {
		out = append(out, map[string]any{"label": x[2], "href": x[1], "current": cur == x[0]})
	}
	return out
}

// apiMatrixWaiting is MatrixWaitingSection as data: the previews that wait for this admin.
func apiMatrixWaiting(sess *Session) []map[string]any {
	out := []map[string]any{}
	if sess.Role != "admin" {
		return out
	}
	for _, r := range matrixPending(sess.Username) {
		assignee := r.S("assignee")
		opts, has := []apiOption{}, false
		if v, err := py.ParseJSON([]byte(py.OrS(r.Get("choices"), "[]"))); err == nil {
			for _, cv := range py.AsList(v) {
				cm := py.AsMap(cv)
				if cm == nil || !matrixNameRe.MatchString(py.Str(cm.GetD("name", ""))) {
					continue
				}
				n := py.Str(cm.Get("name"))
				has = has || n == assignee
				opts = append(opts, apiOption{n, py.OrS(cm.Get("display"), n) + If(r.Get("fwd_name") != nil && n == r.Str("fwd_name"), " (you)", "")})
			}
		}
		if assignee != "" && !has {
			opts = append([]apiOption{{assignee, py.OrS(py.Or(r.Get("assignee_name"), r.Get("assignee")), "")}}, opts...)
		}
		opts = append(opts, apiOption{"", "Unassigned"})
		left := py.FloorDiv(r.I("created")+matrixPreviewTTL-NowInt()+3599, 3600)
		if left < 1 {
			left = 1
		}
		var sl []string
		for _, ln := range py.SplitLines(r.S("summary")) {
			if py.Strip(ln) != "" {
				sl = append(sl, strings.Join(py.Split(ln), " "))
			}
		}
		req := py.OrS(r.Get("requester"), "unknown")
		req, _, _ = strings.Cut(req, " — ")
		req, _, _ = strings.Cut(req, " (")
		out = append(out, map[string]any{"pid": r.Str("pid"), "title": r.Str("title"), "requester": req, "forwarded": BoardShortAgo(r.Get("created")),
			"hoursLeft": left, "project": r.S("project"), "summary": strings.Join(sl, " · "), "assignee": assignee, "options": opts})
	}
	return out
}

func apiLocalBoard(c *Ctx, qs py.QS, sess *Session) map[string]any {
	st := BoardStatus()
	rows, f := boardFiltered(c, qs)
	every := rows
	if f.any() {
		every = BoardBQ("SELECT assignee, assignee_name, labels FROM tasks WHERE gone=0")
	}
	by := map[string][]Row{}
	for _, t := range rows {
		s := t.Str("status")
		if _, ok := BoardLabel[s]; !ok {
			s = "todo"
		}
		by[s] = append(by[s], t)
	}
	nOpen := 0
	for _, col := range BoardColumns {
		if col != "done" {
			nOpen += len(by[col])
		}
	}
	nFix := len(by["fixer"])
	fixerOn := FixerTriggerOn()
	syncs := []string{boardSyncLine(st)}
	if f.tracker == "gitlab" {
		syncs = []string{gitlabSyncLine(sess.Role == "admin")}
	} else if f.tracker == "all" && GitLabConnected() {
		syncs = append(syncs, gitlabSyncLine(sess.Role == "admin"))
	}
	line := syncs
	if fixerOn || nFix > 0 {
		line = append([]string{Itoa(nFix) + " ready for fixer"}, syncs...)
	}
	status := [][]apiLink{}
	for _, x := range line {
		status = append(status, apiSegs(x))
	}
	shown := []string{}
	for _, col := range BoardColumns {
		if col != "fixer" || fixerOn || nFix > 0 {
			shown = append(shown, col)
		}
	}
	labels, counts := make([]string, len(shown)), make([]int, len(shown))
	for i, col := range shown {
		labels[i], counts[i] = BoardLabel[col], len(by[col])
	}
	def := kanbanDefault(labels, counts)
	filtered := f.filtered()
	cols := []map[string]any{}
	for ci, col := range shown {
		capN := 15
		if col == "done" {
			capN = 10
		}
		if WantsAll(qs) {
			capN = len(by[col])
		}
		items := by[col]
		if len(items) > capN {
			items = items[:capN]
		}
		help := ""
		switch col {
		case "progress":
			help = "Argus plans each card here; an admin starts it with Spin up agent. Release tasks only get a proposal."
		case "fixer":
			help = "Reviewed develop and pre-release tasks wait here for the fixer."
			if !fixerOn {
				help = "Retired: move these tasks to In Progress (Admin → Task agent)."
			}
		case "done":
			help = "Done keeps the last " + Itoa(BoardDoneDays) + " days."
		}
		cards := []map[string]any{}
		for _, t := range items {
			cards = append(cards, apiTaskCard(t, sess))
		}
		cc := map[string]any{"id": col, "name": BoardLabel[col], "count": len(by[col]), "cards": cards, "first": ci == def,
			"empty": If(filtered, "No matching tasks", "No tasks"), "help": help, "fixer": col == "fixer" && fixerOn}
		if len(by[col]) > capN {
			cc["more"] = map[string]any{"hidden": len(by[col]) - capN, "href": c.AllHref(qs, true, "col-"+col)}
		}
		cols = append(cols, cc)
	}
	local := ""
	if py.Truthy(st) && !py.Truthy(st.Get("configured")) && f.tracker != "gitlab" {
		local = "Local board: tasks live in Argus only until Jira is connected; then every queued task, move and comment is sent to Jira in order, and Jira becomes the source of truth."
	}
	out := map[string]any{"mode": "local", "tracker": f.tracker, "status": status, "columns": cols, "local": local, "connectHref": If(local != "" && sess.Role == "admin", "/admin/board", ""),
		"readOnly": Text(boardReadOnlyFor(sess, f.tracker)), "canCreate": boardCanCreate(sess), "filters": apiBoardFilters(every, f, qs),
		"results": Text(boardResults(by, shown, nOpen, filtered, f))}
	if len(rows) == 0 {
		e := boardEmpty(f, sess)
		title := Text(reBoldOf.FindString(e))
		out["emptyAll"] = map[string]any{"title": title, "text": strings.TrimSpace(strings.TrimPrefix(Text(e), title))}
	}
	return out
}

// apiBoardFilters is boardFilterForm as data: rows of link chips, nothing to submit.
func apiBoardFilters(rowsAll []Row, f boardFilter, qs py.QS) map[string]any {
	envs, people, human, internal, srcs := boardVocab(rowsAll, f)
	chips := func(name string, opts [][2]string, cur string) []map[string]any {
		out := []map[string]any{}
		for _, o := range opts {
			out = append(out, apiChip(o[1], boardChipHref(qs, name, o[0]), o[0] == cur, ""))
		}
		return out
	}
	fold := func(name, summary string, opts [][2]string, cur string) map[string]any {
		open := false
		for _, o := range opts {
			open = open || o[0] == cur
		}
		return map[string]any{"summary": summary, "open": open, "chips": chips(name, opts, cur)}
	}
	top := func(xs [][2]string) ([][2]string, [][2]string) {
		if len(xs) <= boardTopN+1 {
			return xs, nil
		}
		return xs[:boardTopN], xs[boardTopN:]
	}
	pTop, pRest := top(people)
	hTop, hRest := top(human)
	row := func(label string, c []map[string]any, folds ...map[string]any) map[string]any {
		keep := []map[string]any{}
		for _, x := range folds {
			if len(x["chips"].([]map[string]any)) > 0 {
				keep = append(keep, x)
			}
		}
		return map[string]any{"label": label, "chips": c, "folds": keep}
	}
	main := []map[string]any{}
	if jb, _ := JBoardBoard(); !(jb != nil && GitLabConnected()) && (GitLabConnected() || (boardHasTracker() && len(BoardBQ("SELECT 1 FROM tasks WHERE tracker='gitlab' LIMIT 1")) > 0)) {
		cur := f.tracker
		if cur == "all" {
			cur = ""
		}
		main = append(main, row("Tracker", chips("tracker", [][2]string{{"", "Jira and GitLab"}, {"jira", "Jira"}, {"gitlab", "GitLab issues"}}, cur)))
	}
	main = append(main, row("Show", chips("show", [][2]string{{"", "all tasks"}, {"mine", "mine"}}, f.show)), row("Environment", chips("env", envs, f.env)))
	srcAll := append([][2]string{{"", "all"}}, srcs...)
	more := []map[string]any{
		row("Assignee", chips("assignee", append([][2]string{{"", "anyone"}, {"nobody", "Unassigned"}}, pTop...), f.assignee), fold("assignee", "All people", pRest, f.assignee)),
		row("Label", chips("label", append([][2]string{{"", "any"}}, hTop...), f.label), fold("label", "All labels", hRest, f.label), fold("label", "Argus labels", internal, f.label)),
		row("Source", chips("source", srcAll, f.source))}
	name := func(opts [][2]string, v string) string {
		for _, o := range opts {
			if o[0] == v {
				return o[1]
			}
		}
		return v
	}
	active := []map[string]any{}
	add := func(param, label, val string) {
		active = append(active, apiChip(label+": "+val, boardChipHref(qs, param, ""), true, ""))
	}
	if f.show != "" {
		add("show", "Show", "mine")
	}
	if f.env != "" {
		add("env", "Environment", name(envs, f.env))
	}
	if f.assignee != "" {
		add("assignee", "Assignee", name(append([][2]string{{"nobody", "Unassigned"}}, people...), f.assignee))
	}
	if f.label != "" {
		add("label", "Label", f.label)
	}
	if f.source != "" {
		add("source", "Source", name(srcs, f.source))
	}
	clear := "/board"
	if f.tracker != "" {
		clear += "?" + py.Urlencode([][2]string{{"tracker", f.tracker}})
	}
	return map[string]any{"main": main, "more": more, "advanced": f.advanced(), "active": active, "clear": clear}
}

func apiJBoard(c *Ctx, qs py.QS, sess *Session) map[string]any {
	b, st := JBoardBoard()
	me := sess.Username
	rows, mine, view, qfOn, qfs := jboardSelect(b, qs, me)
	btype := py.Str(b.Get("type"))
	tasks := map[string]Row{}
	for _, t := range BoardBQ("SELECT * FROM tasks WHERE jira_key IS NOT NULL") {
		tasks[t.Str("jira_key")] = t
	}
	agents, mx := AgentStates(), MatrixBadges()
	cols := py.AsList(b.Get("columns"))
	can := BoardWriteBlock(sess) == ""
	by := make([][]Row, len(cols))
	for _, r := range rows {
		if ci, ok := r.Get("col").(int64); ok && ci >= 0 && int(ci) < len(cols) {
			by[ci] = append(by[ci], r)
		}
	}
	qfStr := []string{}
	for _, q := range qfOn {
		qfStr = append(qfStr, I2S(q))
	}
	base := jboardBase{show: If(mine, "mine", "all"), qf: qfStr, view: If(view == "backlog", "backlog", "")}
	with := func(f func(b *jboardBase)) string { x := base; f(&x); return jboardQS(x) }
	chips := []map[string]any{apiChip("Only my issues", with(func(b *jboardBase) { b.show = "mine" }), mine, ""),
		apiChip("All", with(func(b *jboardBase) { b.show = "all" }), !mine, "")}
	for _, q := range qfs {
		qid, isInt := q.Get("id").(int64)
		on := false
		nxt := []string{}
		for _, x := range qfOn {
			if isInt && x == qid {
				on = true
			} else {
				nxt = append(nxt, I2S(x))
			}
		}
		if !on {
			nxt = append(nxt, py.Str(q.Get("id")))
		}
		title := py.Str(q.GetD("jql", ""))
		if py.Truthy(q.Get("error")) {
			title = py.Str(q.Get("error"))
		}
		chips = append(chips, apiChip(py.Str(q.Get("name")), with(func(b *jboardBase) { b.qf = nxt }), on, title))
	}
	if btype == "scrum" {
		chips = append(chips, apiChip("Active sprint", with(func(b *jboardBase) { b.view = "" }), view == "sprint", ""),
			apiChip("Backlog", with(func(b *jboardBase) { b.view = "backlog" }), view == "backlog", ""))
	}
	names, counts := make([]string, len(cols)), make([]int, len(cols))
	for i, cv := range cols {
		names[i], counts[i] = py.Str(py.AsMap(cv).Get("name")), len(by[i])
	}
	def := kanbanDefault(names, counts)
	out := []map[string]any{}
	total := 0
	for i, cv := range cols {
		cm := py.AsMap(cv)
		n := len(by[i])
		total += n
		lim := []string{}
		if cm.Get("min") != nil {
			lim = append(lim, "Min "+I2S(py.I(cm.Get("min"))))
		}
		if cm.Get("max") != nil {
			lim = append(lim, "Max "+I2S(py.I(cm.Get("max"))))
		}
		sts := []string{}
		for _, s := range py.AsList(cm.Get("statuses")) {
			sts = append(sts, py.Str(py.AsMap(s).Get("name")))
		}
		items := by[i]
		if !WantsAll(qs) && len(items) > 15 {
			items = items[:15]
		}
		cards := []map[string]any{}
		for _, r := range items {
			key := r.Str("key")
			mv, hasMx := mx[key]
			cards = append(cards, apiJiraCard(r, tasks[key], sess, can, cols, agents[key], mv, hasMx))
		}
		cc := map[string]any{"id": Itoa(i), "name": py.Str(cm.Get("name")), "count": n, "cards": cards, "first": i == def, "title": strings.Join(sts, ", "),
			"limits": strings.Join(lim, " · "), "over": cm.Get("max") != nil && int64(n) > py.I(cm.Get("max")), "under": cm.Get("min") != nil && int64(n) < py.I(cm.Get("min")),
			"empty": If(mine || len(qfOn) > 0, "No matching issues", "No issues"), "help": ""}
		if n > 15 && !WantsAll(qs) {
			cc["more"] = map[string]any{"hidden": n - 15, "href": c.AllHref(qs, true, "col-"+Itoa(i))}
		}
		out = append(out, cc)
	}
	status := [][]apiLink{{{Text: py.OrS(b.Get("name"), "Board")}}, {{Text: btype + " board"}},
		{{Text: Itoa(total) + " issue" + py.Plural(total) + If(mine, " · mine", "")}}, {{Text: "refreshed " + Ago(st.Get("board_snap_at"))}}}
	note := ""
	if btype == "kanban" && py.Truthy(b.Get("done_days")) && len(cols) > 0 {
		note = "As on the Jira board: board filter, sub-query and column mapping. Issues in " + py.Str(py.AsMap(cols[len(cols)-1]).Get("name")) +
			" that were not updated in " + I2S(py.I(b.Get("done_days"))) + " days are hidden."
	}
	res := map[string]any{"mode": "jira", "tracker": "", "status": status, "columns": out, "chips": chips, "note": note, "readOnly": Text(BoardReadOnlyNote(sess)),
		"canCreate": BoardCanChange(sess), "canRefresh": true, "boardError": py.OrS(st.Get("board_error"), ""), "choice": apiMoveChoice(qs)}
	return res
}

// apiMoveChoice: Jira offers several transitions into the column; the visitor picks one.
func apiMoveChoice(qs py.QS) any {
	rid := qs.Get("req")
	if rid == "" {
		return nil
	}
	res := py.AsMap(py.AsMap(BoardStatus().Get("requests")).Get(rid))
	if !py.Truthy(res) || res.Get("result") != "choose" || !py.Truthy(res.Get("choices")) {
		return nil
	}
	key := py.OrS(res.GetD("key", ""), "")
	if !JBoardKeyRe.MatchString(key) {
		return nil
	}
	opts := []apiOption{}
	for _, cv := range py.AsList(res.Get("choices")) {
		ch := py.AsMap(cv)
		if py.IsDigit(py.Str(ch.GetD("id", ""))) {
			opts = append(opts, apiOption{py.Str(ch.Get("id")), py.Str(ch.Get("name")) + " → " + py.Str(ch.Get("to"))})
		}
	}
	col := ""
	if n, ok := res.Get("col").(int64); ok {
		col = I2S(n)
	} else if b, ok := res.Get("col").(bool); ok {
		col = If(b, "1", "0")
	}
	return map[string]any{"key": key, "col": col, "options": opts}
}
