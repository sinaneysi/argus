package app

// The task page and the New task form as JSON (ADR-0018): the task's record, comments and history, the fixer, and the
// agent panel with its plan, progress, result, chat and credentials. A secret's value is never part of any answer.

import (
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var (
	reSectionNote = regexp.MustCompile(`(?s)<p class="section-note">(.*?)</p>`)
	reHostBox     = regexp.MustCompile(`name="host" value="([^"]*)"( checked)?`)
)

// apiFixer is boardFixerBlock as data: its notes, and the host choice when the task can be sent.
func apiFixer(t Row, sess *Session) map[string]any {
	h := boardFixerBlock(t, sess, "")
	notes := [][]apiLink{}
	for _, m := range reSectionNote.FindAllStringSubmatch(h, -1) {
		notes = append(notes, apiSegs(m[1]))
	}
	out := map[string]any{"heading": "Fixer", "notes": notes}
	if strings.Contains(h, `action="/board/fixer"`) {
		hosts := []map[string]any{}
		for _, m := range reHostBox.FindAllStringSubmatch(h, -1) {
			hosts = append(hosts, map[string]any{"alias": Text(m[1]), "checked": m[2] != ""})
		}
		out["heading"], out["hosts"], out["envLabel"] = "Send to the fixer", hosts, EnvLabel(t.S("env"))
	}
	return out
}

func apiComments(t Row, sess *Session) map[string]any {
	rows := BoardBQ("SELECT * FROM comments WHERE task_id=? ORDER BY created_at, id", t.I("id"))
	shown := rows
	if len(shown) > 100 {
		shown = shown[len(shown)-100:]
	}
	items := []map[string]any{}
	for _, cm := range shown {
		items = append(items, map[string]any{"author": py.OrS(cm.Get("author"), "?"), "at": cm.I("created_at"), "body": cm.S("body"), "pending": cm.T("pending")})
	}
	return map[string]any{"count": len(rows), "items": items, "blocked": TaskWriteBlock(sess, t), "max": BoardMaxComment,
		"tracker": If(TaskTracker(t) == "gitlab", "GitLab", "Jira")}
}

func apiHistory(t Row) []map[string]any {
	out := []map[string]any{}
	for _, ev := range BoardBQ("SELECT * FROM events WHERE task_id=? ORDER BY at DESC, id DESC LIMIT 100", t.I("id")) {
		k := boardEvKind[ev.Str("kind")]
		if k == "" {
			k = "other"
		}
		out = append(out, map[string]any{"at": ev.I("at"), "kind": k, "tag": ev.Str("kind"), "actor": ev.S("actor"), "text": ev.S("text")})
	}
	return out
}

// apiChat is AgentChat as data: the thread and what the composer may do; nil when the chat is off. Same order of
// checks as the Go page: a page that refreshes for another reason shows a pause link, so a reload never eats typing.
func apiChat(c *Ctx, key string, sess *Session) map[string]any {
	if key == "" || !AgentChatOn() {
		return nil
	}
	rows := BoardBQ("SELECT * FROM task_chat WHERE key=? ORDER BY seq DESC LIMIT 100", key)
	pending := len(BoardBQ("SELECT 1 FROM task_chat_turns WHERE key=? AND state IN ('queued','running') LIMIT 1", key)) > 0
	msgs := []map[string]any{}
	for i := len(rows) - 1; i >= 0; i-- {
		m := rows[i]
		ak := m.Str("author_kind")
		who := agentChatWho[ak]
		if who == "" {
			who = py.OrS(m.Get("author"), "someone")
			if ak == "matrix" {
				who += " (via Matrix)"
			}
		}
		msgs = append(msgs, map[string]any{"seq": py.I(m.Get("seq")), "who": who, "argus": ak == "argus", "at": py.I(m.Get("created")), "body": m.Str("body"),
			"proposal": m.Str("kind") == "proposal"})
	}
	ask := key == "new" || agentAskKeyRe.MatchString(key)
	out := map[string]any{"key": key, "messages": msgs, "pending": pending, "canPost": agentChatCanPost(sess), "ask": ask}
	if len(msgs) == 0 {
		out["empty"] = If(ask, "Ask Argus anything, or ask it to define a new task: it proposes the task into Proposed tasks, where an admin accepts it into Jira or GitLab. Nothing is created without Accept.",
			"No messages yet. Ask Argus about the task, answer its questions or correct the plan here; a change of plan makes it plan again.")
	}
	if pending {
		c.Refresh = 5
	}
	if !agentChatCanPost(sess) {
		out["note"] = "Viewers read the chat; operators and admins post."
		return out
	}
	if !pending && c.Refresh != nil {
		out["pause"] = apiLink{Text: "Write a message", Href: agentTaskURL(key) + "&live=0#chat"}
		return out
	}
	out["note"] = "Read-only: a message never approves a change; Spin up agent does. Never paste a credential here."
	if !ClaudeOn(nil) {
		out["note"] = "Claude paused: messages are stored; Argus replies once Claude usage is on."
	}
	return out
}

// apiSecrets is AgentSecrets as data: metadata only, never a value.
func apiSecrets(key string, a Row, taskEnv string, sess *Session) map[string]any {
	on := AgentSecretsOn()
	rows := BoardBQ("SELECT * FROM task_secrets WHERE key=? ORDER BY id", key)
	if !on && len(rows) == 0 {
		return nil
	}
	admin := sess != nil && sess.Role == "admin"
	release := agentReleaseTask(a, taskEnv)
	items, forms := []map[string]any{}, []map[string]any{}
	for _, r := range rows {
		name, hosts := r.Str("name"), secretHosts(r)
		switch r.Str("state") {
		case "requested":
			items = append(items, map[string]any{"name": name, "text": "requested by Argus for " + hosts + If(r.Str("purpose") != "", ": "+r.Str("purpose"), ""),
				"note": "Use a short-lived, narrowly scoped token and revoke it after the task."})
			if on && admin && !release {
				ttl := int(py.I(r.Get("expires_at")))
				if ttl < 1 || ttl > secretMaxTTL {
					ttl = secretTTL
				}
				forms = append(forms, map[string]any{"name": name, "hosts": hosts, "purpose": r.Str("purpose"), "ttl": ttl, "label": "Provide " + name})
			}
		case "provided":
			items = append(items, map[string]any{"name": name, "text": "provided by " + r.Str("provided_by") + " for " + hosts + ", expires " + Text(When(r.Get("expires_at"))) +
				", used " + I2S(py.I(r.Get("uses"))) + "×", "rotate": py.I(r.Get("rotate_recommended")) > 0,
				"note": "Argus never sees the value; it is wiped when the task ends or at expiry."})
		case "wiped", "expired":
			items = append(items, map[string]any{"name": name, "text": "wiped " + Text(When(r.Get("wiped_at"))) + " (" + r.Str("wipe_reason") + ").",
				"warn": "Revoke or rotate this credential now: Argus forgot it, the issuer has not."})
		}
	}
	out := map[string]any{"items": items, "forms": forms, "new": nil}
	switch {
	case !on:
		out["note"] = "Task secrets are off (Admin → Task agent)."
	case release:
		out["note"] = "Release tasks never get secrets: release stays a read-only proposal."
	case !admin:
		out["note"] = "Only admins provide credentials. Never paste one into the chat."
	default:
		out["new"] = map[string]any{"name": "", "hosts": "", "purpose": "", "ttl": secretTTL, "label": "Provide secret"}
	}
	return out
}

func apiAgentResult(a Row, sess *Session) map[string]any {
	res := py.AsMap(agentJSON(a.Get("result"), "null"))
	if !py.Truthy(res) {
		return nil
	}
	list := func(k string) []string {
		out := []string{}
		for _, x := range py.AsList(res.Get(k)) {
			out = append(out, py.Str(x))
		}
		return out
	}
	out := map[string]any{"summary": py.OrS(res.Get("summary"), ""), "evidence": list("evidence"), "rollback": list("rollback"), "needs": list("needs")}
	if u := apiSafeHref(py.OrS(res.Get("mr_url"), "")); u != "" {
		out["mr"] = u
	}
	if py.Truthy(res.Get("fix_run")) {
		out["fixRun"] = py.Str(res.Get("fix_run"))
	}
	if py.Truthy(res.Get("report_md")) {
		r := py.OrS(res.Get("report_md"), "")
		out["report"] = apiMd(r, agentReportHandoff(a, r))
	}
	if b, _ := JBoardBoard(); b != nil && sess.Role == "admin" && BoardWriteBlock(sess) == "" {
		sug := strings.ToLower(py.OrS(res.Get("suggested_column"), ""))
		moves := []map[string]any{}
		for i, cv := range py.AsList(b.Get("columns")) {
			name := py.Str(py.AsMap(cv).Get("name"))
			if In(strings.ToLower(name), "done", "document", "deploy", "pending") {
				moves = append(moves, map[string]any{"col": Itoa(i), "label": "Move to " + name, "primary": strings.ToLower(name) == sug})
			}
		}
		out["moves"] = moves
	}
	return out
}

// apiAgent is AgentPanel as data; nil when Argus does not work this task.
func apiAgent(c *Ctx, t Row, sess *Session) map[string]any {
	key := AgentKey(t)
	var a Row
	if key != "" {
		a = agentRow(key)
	}
	if a == nil {
		return nil
	}
	plan := py.AsMap(py.Or(agentJSON(a.Get("plan"), "null"), py.NewOMap()))
	if plan == nil {
		plan = py.NewOMap()
	}
	inputs := agentGetInputs(key)
	admin := sess.Role == "admin"
	stateS := a.Str("state")
	lk, ok := agentState[stateS]
	if !ok {
		lk = [2]string{stateS, ""}
	}
	label, kind := lk[0], lk[1]
	if agentLimitRe.MatchString(a.S("error")) {
		label, kind = "Paused: usage limit", "warn"
	}
	meta := []string{}
	if a.T("plan_version") {
		meta = append(meta, "plan v"+I2S(a.I("plan_version")))
	}
	if a.T("intake_cost") {
		meta = append(meta, "planning $"+py.F(a.F("intake_cost"), 2))
	}
	if a.T("run_cost") {
		meta = append(meta, "run $"+py.F(a.F("run_cost"), 2))
	}
	if py.Truthy(plan.Get("confidence")) {
		meta = append(meta, "confidence "+py.Str(plan.Get("confidence")))
	}
	if a.T("approved_by") && a.T("approved_version") {
		meta = append(meta, "v"+I2S(a.I("approved_version"))+" approved by "+a.Str("approved_by"))
	}
	chatOn := AgentChatOn()
	finished := a.T("result") && In(stateS, "done", "failed", "blocked", "stopped")
	pv := a.I("plan_version")
	out := map[string]any{"key": key, "state": stateS, "label": label, "kind": If(kind == "", "neutral", kind), "meta": meta, "admin": admin, "finished": finished,
		"chatOn": chatOn, "planVersion": pv}

	// the failure and Plan again, then the live progress: never inside the plan fold
	show, canReplan, why := agentCanReplan(a, admin)
	if show {
		cls := ""
		if stateS == "failed" {
			d := agentDetail(a)
			cls = py.OrS(detS(d, "class"), agentErrClass(a.S("error"), detS(d, "raw")))
		}
		hint := "Plans again with the saved instructions and answers; read-only."
		if !chatOn {
			hint = "Plans again with the saved instructions and answers. To change them first, use Save answers & re-plan below."
		}
		if cls == "auth" {
			hint = "Replace the token first; a token that passes its test plans this task again by itself."
		}
		if !canReplan && agentReplanPending(key) != "" && agentLive(c) {
			c.Refresh = 5
		}
		out["replan"] = map[string]any{"can": canReplan, "why": If(canReplan, hint, why), "quiet": cls == "auth"}
	}
	errBlock := agentErrorBlock(a, key, chatOn, "")
	if errBlock != "" {
		out["notices"] = apiNotices(errBlock)
		if strings.Contains(errBlock, "ag-facts") {
			d := agentDetail(a)
			phase := py.OrS(detS(d, "phase"), py.OrS(a.Get("phase"), "intake"))
			raw := py.OrS(detS(d, "raw"), a.S("error"))
			facts := agentErrorFacts(a, key, phase, raw, d)
			out["facts"] = apiKV(facts)
		}
	} else if a.T("error") {
		out["notices"] = []apiNotice{{Text: a.Str("error"), Kind: "warn"}}
	}
	if g := agentProgressData(c, a, key); g != nil {
		words := agentStepWords[g.Phase]
		if words == nil {
			words = agentStepWords["intake"]
		}
		m := [][]apiLink{}
		for _, x := range g.Meta {
			m = append(m, apiSegs(x))
		}
		p := map[string]any{"label": g.Label, "cur": g.Cur, "bar": g.Bar, "now": apiSegs(g.Now), "meta": m, "fix": g.Fix}
		if !g.Fix {
			p["steps"] = words
		}
		out["progress"] = p
	}
	out["result"] = apiAgentResult(a, sess)

	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	answered := map[string]any{}
	for _, xv := range inputs.answers {
		x := py.AsMap(xv)
		answered[py.Str(x.Get("q"))] = x.Get("a")
	}
	if py.Truthy(plan) {
		ck := cl.Get("kind")
		kl := py.OrS(ck, "")
		if v, ok := agentKind[kl]; ok {
			kl = v
		}
		steps := []map[string]any{}
		for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
			s := py.AsMap(sv)
			tg := []string{}
			for _, x := range py.AsList(py.Or(s.Get("targets"), []any{})) {
				tg = append(tg, py.Str(x))
			}
			steps = append(steps, map[string]any{"text": py.Str(s.Get("text")), "mode": py.Str(s.Get("mode")), "targets": tg})
		}
		env := ""
		if v := cl.Get("environment"); v != "none" {
			env = py.OrS(v, "")
		}
		p := map[string]any{"summary": py.OrS(plan.Get("summary"), ""), "env": env, "kind": kl, "production": ck == "production", "steps": steps,
			"prodNote": ck == "production" && !(t.Get("env") == "release" && t.Get("status") != "done")}
		if py.Truthy(plan.Get("estimate")) {
			e := py.AsMap(plan.Get("estimate"))
			p["estimate"] = "estimate " + py.OrS(e.Get("effort"), "?") + ", about $" + py.F(py.Num(py.Or(e.Get("cost_usd"), int64(0))), 2)
		}
		if h := FixHandoffTask(plan, py.OrS(cl.Get("environment"), "")); h.State != "missing" {
			fenv := t.S("env")
			if ce, ok := cl.Get("environment").(string); ok && In(ce, "develop", "pre-release", "release") {
				fenv = ce
			}
			act := ""
			if In(fenv, "develop", "pre-release") && ck == "change" {
				act = "Spin up agent sends the approved plan and this prompt to the fixer."
			}
			p["fix"] = apiFix(h, fenv, "task="+py.Quote(key), act, "")
		}
		needs := []string{}
		for _, x := range py.AsList(plan.Get("needs")) {
			needs = append(needs, py.Str(x))
		}
		p["needs"] = needs
		out["plan"] = p
	}
	qs := py.AsList(py.Or(plan.Get("questions"), []any{}))
	questions := []map[string]any{}
	blocked := false
	for _, qv := range qs {
		q := py.AsMap(qv)
		ans := py.OrS(answered[py.Str(q.Get("text"))], "")
		blocking := py.Truthy(q.Get("blocking"))
		blocked = blocked || (blocking && py.Strip(ans) == "")
		questions = append(questions, map[string]any{"id": py.Str(q.Get("id")), "text": py.Str(q.Get("text")), "blocking": blocking, "answer": ans})
	}
	out["questions"] = questions
	out["emptyDesc"] = py.Strip(t.S("description")) == ""
	running := In(stateS, "planning", "running", "approved")
	out["running"] = running && admin
	if !chatOn { // the old form: answers and instructions, only while the page does not reload under the typing
		out["inputs"] = map[string]any{"instructions": inputs.instructions, "postJira": inputs.postJira, "tracker": If(t.Str("tracker") == "gitlab", "GitLab", "Jira"), "paused": c.Refresh != nil}
		if c.Refresh != nil {
			out["pause"] = apiLink{Text: "Edit answers and instructions", Href: agentTaskURL(key) + "&live=0#agent"}
		}
	}
	if admin {
		okWrite, whyW := JiraCanWrite(sess.Username)
		path, spin := agentSpinUp(plan)
		can := In(stateS, "ready", "done", "failed", "blocked", "stopped") && py.Truthy(plan) && !blocked && okWrite && path != "refused"
		reason := ""
		if !can {
			switch {
			case !okWrite:
				reason = whyW
			case running && stateS != "planning":
				reason = "The agent is already running this plan; Stop ends it"
			case running || !py.Truthy(plan):
				reason = "Plan not ready yet"
				if w := agentWaitNote(a); w != "" {
					reason += ": " + strings.ToLower(w[:1]) + w[1:]
				} else if stateS == "failed" {
					reason += ": planning failed (see above)"
				} else if stateS == "planning" {
					reason += ": Argus is planning, read-only"
				}
			case stateS == "waiting" || blocked:
				reason = "Answer the blocking questions first" + If(chatOn, " (in the chat below)", "")
			case path == "refused":
				reason = spin
			}
		}
		if path == "refused" {
			spin = "Spin up agent"
		}
		out["spin"] = map[string]any{"can": can, "label": spin, "reason": reason, "primary": stateS == "ready", "proposal": path == "proposal"}
	}
	out["chat"] = apiChat(c, key, sess)
	out["secrets"] = apiSecrets(key, a, py.OrS(t.Get("env"), ""), sess)
	logs := []map[string]any{}
	for _, l := range BoardBQ("SELECT * FROM agent_log WHERE key=? ORDER BY at DESC, id DESC LIMIT 60", key) {
		logs = append(logs, map[string]any{"at": l.I("at"), "kind": l.Str("kind"), "text": l.Str("text")})
	}
	out["log"] = logs
	return out
}

// apiBoardTaskJSON is GET /api/v1/board/task?id=|key=.
func apiBoardTaskJSON(qs py.QS, sess *Session) (any, *apiErr) {
	t := boardGetTask(qs)
	if t == nil {
		return nil, &apiErr{404, "no such task"}
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: boardPathOf("/board/task", qs)}
	st := BoardStatus()
	var base any
	if t.Has("jira_url") {
		base = t.Get("jira_url")
	}
	jl := BoardJiraLink(st, t.Get("jira_key"), base)
	jb, _ := JBoardBoard()
	status := t.Str("status")
	statusLabel, known := boardStatusLabel(status)
	if !known {
		statusLabel = status
	}
	mirror := t.T("jira_key") && t.T("jira_status") && jb != nil
	if mirror {
		statusLabel = t.Str("jira_status")
	}
	gl := TaskTracker(t) == "gitlab"
	ro := TaskWriteBlock(sess, t)
	out := map[string]any{"id": t.I("id"), "ref": BoardRef(t), "title": t.Str("title"), "tag": Text(TrackerTag(t)), "status": statusLabel, "statusKey": If(mirror, "", status),
		"env": t.S("env"), "owner": py.OrS(py.Or(t.Get("assignee_name"), t.Get("assignee")), ""), "priority": t.S("priority"), "openedAt": t.I("created_at"), "readOnly": ro,
		"prod": t.Get("env") == "release" && status != "done", "return": boardPathOf("/board/task", qs)}
	if t.T("gone") {
		out["gone"] = If(gl, "This issue is no longer in the connected GitLab project (deleted, moved, or another project connected).", "This task is no longer in Jira (deleted or moved out of the project).")
	}
	if ro == "" {
		if jb != nil && t.T("jira_key") {
			cur := int64(-1)
			if r := BoardBQ("SELECT col FROM board_issues WHERE key=? LIMIT 1", t.Get("jira_key")); len(r) > 0 {
				cur = r[0].I("col")
			}
			out["move"] = map[string]any{"kind": "jira", "key": t.Str("jira_key"), "options": apiColOpts(py.AsList(jb.Get("columns")), cur)}
		} else {
			out["move"] = map[string]any{"kind": "task", "task": t.I("id"), "options": apiMoveOpts(t)}
		}
	}
	out["assign"] = ro == "" && t.S("assignee") != sess.Username
	links := []apiLink{}
	if jl != "" {
		links = append(links, apiLink{Text: "Open in Jira", Href: apiSafeHref(jl)})
	}
	gu := GitLabIssueLink(t)
	if gu != "" {
		links = append(links, apiLink{Text: "Open in GitLab", Href: apiSafeHref(gu)})
	}
	out["links"] = links
	out["description"] = py.Strip(t.S("description"))
	agent := apiAgent(c, t, sess)
	out["agent"] = agent
	if ev := py.Strip(t.S("evidence")); ev != "" {
		from := BoardSourceNames[t.S("source_kind")]
		if from == "" {
			from = "the task"
		}
		out["evidence"] = map[string]any{"text": ev, "from": from, "fold": agent != nil}
	}
	out["fixPrompt"] = py.Strip(t.S("fix_prompt"))
	if agent == nil {
		out["fixer"] = apiFixer(t, sess)
	}
	out["comments"] = apiComments(t, sess)
	out["history"] = apiHistory(t)
	labels := []string{}
	for _, x := range boardJSONList(t.Get("labels")) {
		if x != "argus" && !strings.HasPrefix(x, "env-") && !strings.HasPrefix(x, "argus-src-") {
			labels = append(labels, x)
		}
	}
	out["labels"] = labels
	lab := func(s string) string {
		if v, ok := BoardLabel[s]; ok {
			return v
		}
		return s
	}
	stat := lab(status)
	if mirror {
		stat = t.Str("jira_status") + " · Argus: " + lab(status)
	} else if t.T("jira_status") {
		stat += " · " + If(gl, "GitLab", "Jira") + ": " + t.Str("jira_status")
	}
	tracker := apiFact{Label: If(gl, "GitLab", "Jira"), Value: If(gl, "not in GitLab yet", "not in Jira yet")}
	if gl && (gu != "" || t.T("gl_iid")) {
		tracker.Value, tracker.Href, tracker.Mono = BoardRef(t), apiSafeHref(gu), true
	} else if !gl && t.T("jira_key") {
		tracker.Value, tracker.Href, tracker.Mono = t.Str("jira_key"), apiSafeHref(jl), true
	}
	created := py.OrS(t.Get("requested_by"), "a person")
	if t.Get("created_by") == "argus-auto" {
		created = "Argus (automatic)"
	}
	src := apiFact{Label: "Source", Value: py.OrS(Text(BoardSourceTag(t)), "-"), Href: firstHref(BoardSourceTag(t))}
	synced := "never"
	if t.T("synced_at") {
		synced = Text(When(t.Get("synced_at")))
	}
	out["record"] = []apiFact{tracker, {Label: "Status", Value: stat}, {Label: "Assignee", Value: py.OrS(py.Or(t.Get("assignee_name"), t.Get("assignee")), "-")},
		{Label: "Priority", Value: py.OrS(t.Get("priority"), "-")}, {Label: "Environment", Value: py.OrS(Text(EnvTag(t.Get("env"))), "-")},
		{Label: "Host", Value: py.OrS(t.Get("host"), "-"), Mono: t.T("host")}, src, {Label: "Created by", Value: created},
		{Label: "Created", Value: Text(When(t.Get("created_at")))}, {Label: "Updated", Value: Text(When(t.Get("updated_at")))}, {Label: "Synced", Value: synced}}
	out["notices"] = apiNotices(BoardRequestBanner(c, qs)) // after the panel, as on the Go page: its refresh never hides the composer
	out["choice"] = apiMoveChoice(qs)
	out["refresh"] = apiRefresh(c)
	return out, nil
}

// apiBoardNewJSON is GET /api/v1/board/new: the form's choices and values; from a source, its prefill.
func apiBoardNewJSON(qs py.QS, sess *Session) (any, *apiErr) {
	kind, sid := qs.Get("src"), qs.Get("id")
	v := py.NewOMap("title", "", "env", "", "host", "", "priority", "Medium", "description", "", "evidence", "", "fix_prompt", "", "labels", "", "what", "",
		"tracker", GitLabDefaultTracker(), "gl_project", "")
	out := map[string]any{}
	if kind != "" {
		if !boardIsSource(kind) || py.Len(sid) > 14 {
			return nil, &apiErr{404, "no such source"}
		}
		if t := BoardTaskForSource(kind, sid); t != nil {
			return map[string]any{"exists": apiLink{Text: BoardRef(t), Href: "/board/task?id=" + I2S(t.I("id"))}}, nil
		}
		pf := boardPrefill(kind, sid)
		if pf == nil {
			return nil, &apiErr{404, "no such source"}
		}
		for _, k := range pf.Keys() {
			v.Set(k, pf.Get(k))
		}
		out["src"], out["srcId"] = kind, sid
	}
	vals := map[string]string{}
	for _, k := range []string{"title", "env", "host", "priority", "description", "evidence", "fix_prompt", "labels", "tracker", "gl_project"} {
		vals[k] = py.Str(v.Get(k))
	}
	envs := []apiOption{{"", "not set"}}
	for _, e := range BoardEnvs {
		envs = append(envs, apiOption{e, EnvLabel(e)})
	}
	out["values"], out["envs"], out["priorities"], out["return"] = vals, envs, BoardPriorities, BoardReturn(qs.Get("return"))
	out["sub"] = If(GitLabConnected(), "A task on the board, sent to the tracker you choose", "A task on the board, sent to Jira when it is connected")
	if py.Truthy(v.Get("what")) {
		out["sub"] = "From " + py.Str(v.Get("what"))
	}
	out["blocked"] = If(!GitLabConnected(), BoardWriteBlock(sess), "")
	out["limits"] = map[string]int{"title": BoardMaxTitle, "text": BoardMaxText, "prompt": BoardMaxPrompt}
	if GitLabConnected() {
		st := GitLabStatus()
		sub := "Project " + py.OrS(py.AsMap(st.Get("connection")).Get("project_path"), "the connected project") + ", with Argus's project token."
		projects := []apiOption{}
		if gitlabGroupMode(st) {
			sub = "Group board " + py.OrS(py.AsMap(st.Get("connection")).Get("group_path"), "?") + ", in the project below."
			for _, p := range gitlabProjects(st) {
				projects = append(projects, apiOption{p[0], p[1]})
			}
		}
		out["trackers"] = map[string]any{"jira": py.OrS(BoardWriteBlock(sess), "Your Jira connection, else the shared one."), "gitlab": sub, "projects": projects, "project": gitlabDefaultProject(st)}
	}
	return out, nil
}
