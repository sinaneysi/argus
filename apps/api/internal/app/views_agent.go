package app

// views_agent.py: the Agent panel on a task page ("Argus works this task"), its badge on board cards, and its
// Admin settings. State, plan, questions and the run log come from board.db (written only by root); every action is
// a request file (kinds agent-*) that root re-checks.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var agentState = map[string][2]string{"planning": {"Planning", ""}, "waiting": {"Waiting for answers", "warn"},
	"ready": {"Ready to spin up", "ok"}, "approved": {"Starting", ""}, "running": {"Running", ""}, "done": {"Done", "ok"},
	"failed": {"Failed", "fail"}, "blocked": {"Blocked: needs the operator", "fail"}, "stopped": {"Stopped", ""}}

var agentBadgeMap = map[string][2]string{"planning": {"agent planning", ""}, "waiting": {"questions waiting", "warn"},
	"ready": {"plan ready", "ok"}, "approved": {"agent starting", ""}, "running": {"agent running", ""},
	"done": {"agent done", "ok"}, "failed": {"agent failed", "fail"}, "blocked": {"blocked", "fail"},
	"stopped": {"agent stopped", ""}}

var agentKind = map[string]string{"investigate": "investigate / review", "documentation": "documentation (MR)",
	"change": "change via the fixer", "production": "production: proposal only", "code": "code change (MR)", "unclear": "unclear"}

// agentKeyRe: a Jira key, or gl-<task id> for a GitLab task (bin/task_agent.py GL_KEY_RE).
var agentKeyRe = regexp.MustCompile(`^([A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}|gl-[0-9]{1,9})$`)

// AgentKey is the task agent's key of a task row: its Jira key, or gl-<id> for a GitLab task.
func AgentKey(t Row) string {
	if t.Str("tracker") == "gitlab" {
		return "gl-" + t.Str("id")
	}
	return py.OrS(t.Get("jira_key"), "")
}

// agentTaskURL is the task page of an agent key.
func agentTaskURL(key string) string {
	if strings.HasPrefix(key, "gl-") {
		return "/board/task?id=" + key[3:]
	}
	return "/board/task?key=" + key
}

func agentRow(key any) Row {
	r := BoardBQ("SELECT * FROM agent_tasks WHERE key=?", key)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

// AgentStates is views_agent.states(): {jira key: state}.
func AgentStates() map[string]string {
	out := map[string]string{}
	for _, r := range BoardBQ("SELECT key, state FROM agent_tasks") {
		out[r.Str("key")] = r.Str("state")
	}
	return out
}

// AgentBadge is views_agent.badge(state).
func AgentBadge(state string) string {
	b, ok := agentBadgeMap[state]
	if !ok {
		return ""
	}
	return `<span class="badge` + If(b[1] != "", " "+b[1], "") + `" title="Argus task agent">` + b[0] + `</span>`
}

func agentJSON(v any, def string) any {
	x, err := py.ParseJSON([]byte(py.OrS(v, def)))
	if err != nil {
		panic("bad JSON in board.db: " + err.Error())
	}
	return x
}

type agentInputs struct {
	instructions string
	answers      []any
	postJira     bool
}

func agentGetInputs(key any) agentInputs {
	r := BoardBQ("SELECT * FROM agent_inputs WHERE key=?", key)
	if len(r) == 0 {
		return agentInputs{"", nil, true}
	}
	return agentInputs{r[0].S("instructions"), py.AsList(agentJSON(r[0].Get("answers"), "[]")), r[0].T("post_jira")}
}

// FixerTriggerOn is FIXER_TRIGGER=column (ADR-0013 D1) as root publishes it in board-status.json: the old
// Ready-for-fixer column, status and label start the fixer. Off (the default): the column is ordinary and hidden
// when empty; Spin up agent is the way to the fixer.
func FixerTriggerOn() bool {
	st := py.AsMap(py.Or(BoardStatus().Get("agent"), py.NewOMap()))
	return py.AsMap(py.Or(st.Get("settings"), py.NewOMap())).Get("fixer_trigger") == "column"
}

// agentSpinUp is bin/task_agent.py spin_up(plan): what Spin up agent does (fixer | proposal | run | refused) and
// the button's words.
func agentSpinUp(plan *py.OMap) (string, string) {
	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	env := py.OrS(cl.Get("environment"), "")
	if env == "release" || cl.Get("kind") == "production" {
		return "proposal", "Write proposal for the operator"
	}
	write := false
	seen := map[string]bool{}
	var tg []string
	for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
		s := py.AsMap(sv)
		if s.Get("mode") != "WRITE" {
			continue
		}
		write = true
		for _, x := range py.AsList(py.Or(s.Get("targets"), []any{})) {
			if v := py.Str(x); !seen[v] {
				seen[v] = true
				tg = append(tg, v)
			}
		}
	}
	if !write {
		return "run", "Spin up agent (read-only; MRs on agent/* only)"
	}
	if In(env, "develop", "pre-release") {
		sort.Strings(tg)
		if len(tg) > 4 {
			tg = tg[:4]
		}
		return "fixer", "Spin up agent: writes on " + EnvLabel(env) + If(len(tg) > 0, " ("+strings.Join(tg, ", ")+")", "")
	}
	return "refused", "The plan has WRITE steps but no environment: tell Argus the environment in the instructions; it plans again"
}

// agentWaitNote is what a card or the task page says while no plan exists and no intake runs: Claude paused, or the
// debounce (ADR-0013: planning starts once the card has stayed TASK_PLAN_DEBOUNCE_SEC in the agent column).
func agentWaitNote(a Row) string {
	if a.Str("state") != "planning" || a.T("job_started") || a.T("plan") {
		return ""
	}
	if !ClaudeOn(nil) {
		return "Claude paused: planning starts when Claude usage is on"
	}
	st := py.AsMap(py.Or(BoardStatus().Get("agent"), py.NewOMap()))
	d := py.I(py.Or(st.Get("debounce"), int64(120)))
	if d <= 0 {
		return "Planning starts at the next sync"
	}
	return "Planning starts once the card has stayed " + If(d%60 == 0, I2S(d/60)+" min", I2S(d)+" s") + " in the column"
}

type agentCardWrite struct {
	Text    string   `json:"text"`
	Targets []string `json:"targets"`
}

// agentCard is the plan on a card as data (plain text; the Go card and /api/v1/board both render it).
type agentCard struct {
	Wait    string           `json:"wait,omitempty"`
	Env     string           `json:"env,omitempty"`
	Kind    string           `json:"kind,omitempty"`
	Summary string           `json:"summary,omitempty"`
	Writes  []agentCardWrite `json:"writes,omitempty"`
	More    int              `json:"more,omitempty"`
	Meta    []string         `json:"meta,omitempty"`
	Open    int              `json:"open,omitempty"`
}

func agentCardData(key string) *agentCard {
	if key == "" {
		return nil
	}
	a := agentRow(key)
	if a == nil {
		return nil
	}
	if w := agentWaitNote(a); w != "" {
		return &agentCard{Wait: w}
	}
	plan := py.AsMap(py.Or(agentJSON(a.Get("plan"), "null"), py.NewOMap()))
	if plan == nil || !py.Truthy(plan) {
		return nil
	}
	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	out := &agentCard{Summary: py.Head(py.OrS(plan.Get("summary"), ""), 240)}
	if v := cl.Get("environment"); v != "none" {
		out.Env = py.OrS(v, "")
	}
	out.Kind = py.OrS(cl.Get("kind"), "")
	if v, ok := agentKind[out.Kind]; ok {
		out.Kind = v
	}
	for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
		s := py.AsMap(sv)
		if s.Get("mode") != "WRITE" {
			continue
		}
		w := agentCardWrite{Text: py.Head(py.Str(s.Get("text")), 120), Targets: []string{}}
		for _, x := range py.AsList(py.Or(s.Get("targets"), []any{})) {
			w.Targets = append(w.Targets, py.Str(x))
		}
		out.Writes = append(out.Writes, w)
	}
	if len(out.Writes) > 3 {
		out.More = len(out.Writes) - 3
		out.Writes = out.Writes[:3]
	}
	answered := map[string]bool{}
	for _, xv := range agentGetInputs(key).answers {
		x := py.AsMap(xv)
		if py.Strip(py.OrS(x.Get("a"), "")) != "" {
			answered[py.Str(x.Get("q"))] = true
		}
	}
	for _, qv := range py.AsList(py.Or(plan.Get("questions"), []any{})) {
		if !answered[py.Str(py.AsMap(qv).Get("text"))] {
			out.Open++
		}
	}
	if e := py.AsMap(py.Or(plan.Get("estimate"), py.NewOMap())); py.Truthy(e.Get("effort")) {
		out.Meta = append(out.Meta, "estimate "+py.Str(e.Get("effort")))
	}
	spent := a.F("intake_cost") + a.F("run_cost")
	if a.T("spent_usd") {
		spent = a.F("spent_usd")
	}
	out.Meta = append(out.Meta, "spent $"+py.F(spent, 2), "plan v"+I2S(a.I("plan_version")))
	return out
}

// agentLimitRe finds Claude's usage-limit message in a stored error ("You've hit your weekly limit · resets Oct 29,
// 10:30am (UTC)"); the reset time is the only part shown.
var (
	agentLimitRe = regexp.MustCompile(`(?i)(claude-limit:|hit your [a-z ]*limit|usage limit)`)
	agentResetRe = regexp.MustCompile(`(?i)resets\s+([0-9A-Za-z ,:.]{3,40}?)\s*(\(|$)`)
)

// AgentRowBadge is a card's agent badge from its agent row: a Claude usage limit reads as a pause, not a failure.
func AgentRowBadge(a Row) string {
	if agentLimitRe.MatchString(a.S("error")) {
		return `<span class="badge warn" title="Argus task agent">paused: usage limit</span>`
	}
	return AgentBadge(a.Str("state"))
}

// agentFailNote is the failed state of the agent: what failed, what is safe, the next step, in words; the stored
// reason only as a short footnote, never a raw dump. A Claude usage limit is a wait, not a failure. "" otherwise.
func agentFailNote(a Row, chatOn bool) string {
	errS := a.S("error")
	if agentLimitRe.MatchString(errS) {
		when := "the limit resets"
		if m := agentResetRe.FindStringSubmatch(errS); m != nil {
			when = m[1]
		}
		return `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>Claude usage limit reached.</b> Argus resumes after ` +
			E(when) + `. Nothing was changed; the plan will start automatically after the reset.</span></div>`
	}
	if a.Str("state") != "failed" {
		return ""
	}
	phase := a.S("phase")
	what := "The agent stopped with an error."
	switch {
	case strings.HasPrefix(errS, "intake:"):
		what = "Planning failed: the planning run returned no usable plan."
	case strings.HasPrefix(errS, "no result"):
		what = If(phase == "intake", "Planning", "The run") + " timed out without a result."
	case strings.HasPrefix(errS, "could not start"):
		what = "Argus could not start the job."
	}
	safe := "Nothing was changed: planning only reads the task."
	next := "Ask Argus in the chat below to plan again, or add detail to the task; planning starts again when the task changes."
	if !chatOn {
		next = "An admin adds instructions and chooses Save answers & re-plan."
	}
	switch phase {
	case "run":
		safe, next = "Nothing was merged: a run only reads hosts and its changes stay on an agent/* branch.", "Review the plan and spin up again."
	case "fix":
		safe, next = "The fixer may have changed develop or pre-release.", "Check the run log below before you spin up again."
	}
	return `<div class="banner fail" role="alert">` + Icon("alert") + `<span><b>` + what + `</b> ` + safe + ` ` + next +
		If(errS != "", ` <span class="dim">Recorded reason: `+E(py.Head(errS, 120))+`.</span>`, "") + `</span></div>`
}

// agentReportHandoff is report_handoff(a, report): "strip" when the report's write-access prompt is the one in the
// plan's card on this page, else "fold".
func agentReportHandoff(a Row, report string) string {
	var plan any = py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(a.Get("plan"), "null"))); err == nil && py.Truthy(v) {
		plan = v
	}
	card := py.Strip(FixHandoffTask(plan, "").Prompt)
	_, _, own := FHSplit(report)
	own = py.Strip(own)
	if card != "" && own != "" && (own == card || strings.Contains(card, own)) {
		return "strip"
	}
	return "fold"
}

// AgentPost is views_agent.post(form, sess).
func AgentPost(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if sess.Role != "admin" {
		return 403, "Only admins direct the task agent"
	}
	key, act := get("key"), get("action")
	if !agentKeyRe.MatchString(key) || !In(act, "answers", "stop", "replan") {
		return 400, "Bad request"
	}
	a := agentRow(key)
	if a == nil {
		return 404, "Argus is not working this task"
	}
	var payload *py.OMap
	switch act {
	case "replan": // Plan again: the dispatcher re-checks everything (docs/ROADMAP.md); one queued request per task
		if rid := agentReplanPending(key); rid != "" {
			return 303, agentTaskURL(key) + "&req=" + rid + "#agent"
		}
		if show, ok, why := agentCanReplan(a, true); !show || !ok {
			if !show {
				why = "Nothing to plan again while the agent is " + strings.ToLower(agentState[a.Str("state")][0]) + "."
			}
			return 409, "Not planned again: " + why
		}
		payload = py.NewOMap("kind", "agent-replan", "key", key)
	case "answers":
		ins := strings.ReplaceAll(get("instructions"), "\r\n", "\n")
		answers := py.NewOMap()
		tooLong := false
		for _, k := range form.Keys() {
			if strings.HasPrefix(k, "a_q") && py.IsDigit(k[3:]) {
				v := strings.ReplaceAll(form.List(k)[0], "\r\n", "\n")
				answers.Set(k[2:], v)
				if py.Len(v) > 2000 {
					tooLong = true
				}
			}
		}
		if py.Len(ins) > 8000 || tooLong {
			return 400, "Instructions up to 8000 and answers up to 2000 characters"
		}
		payload = py.NewOMap("kind", "agent-answers", "key", key, "instructions", ins, "answers", answers, "post_jira", get("post_jira") == "1")
	default:
		payload = py.NewOMap("kind", "agent-stop", "key", key)
	}
	rid, err := BoardWriteRequest(payload, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, agentTaskURL(key) + "&req=" + rid + "#agent"
}

// AgentPostSpinUp is POST /board/agent/spinup (ADR-0013 P1; sensitive): an admin approves exactly the plan version
// shown. Root re-checks the version, the env (release -> proposal only, WRITE with env none -> refused) and budgets.
func AgentPostSpinUp(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Only admins spin up the agent"
	}
	key, v := form.Get("key"), form.Get("plan_version")
	if !agentKeyRe.MatchString(key) || !py.IsDigit(v) || len(v) > 9 {
		return 400, "Bad request"
	}
	a := agentRow(key)
	if a == nil {
		return 404, "Argus is not working this task"
	}
	if why := BoardWriteBlock(sess); why != "" && !strings.HasPrefix(key, "gl-") {
		return 403, "Not started: " + why
	}
	n, _ := py.ParseInt(v)
	if n != a.I("plan_version") {
		return 409, "The plan changed: review v" + I2S(a.I("plan_version"))
	}
	plan := py.AsMap(py.Or(agentJSON(a.Get("plan"), "null"), py.NewOMap()))
	if path, why := agentSpinUp(plan); path == "refused" {
		return 409, "Not started: " + why
	}
	rid, err := BoardWriteRequest(py.NewOMap("kind", "agent-start", "key", key, "plan_version", n), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", agentTaskURL(key)+"&req="+rid+"#agent")
}

// AgentPostFixerMove is POST /admin/agent/fixer-move (ADR-0013 D1 migration; sensitive): one admin click moves every
// task still in Ready for fixer to In Progress. expect pins the count the admin saw; root refuses a changed list.
func AgentPostFixerMove(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	v := form.Get("expect")
	if !py.IsDigit(v) || len(v) > 4 {
		return 400, "Bad count"
	}
	n, _ := py.ParseInt(v)
	rid, err := BoardWriteRequest(py.NewOMap("kind", "agent-fixer-move", "expect", n), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "agent", "/admin/board?req="+rid+"#agent")
}

// AgentPostSettings is views_agent.post_settings(form, sess).
func AgentPostSettings(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	get := form.Get
	var users []any
	for _, u := range strings.Split(strings.ReplaceAll(get("assignees"), ";", ","), ",") {
		if u = py.Strip(u); u != "" {
			users = append(users, u)
		}
	}
	if len(users) > 20 {
		users = users[:20]
	}
	if users == nil {
		users = []any{}
	}
	capS := get("qa_cap")
	capN, _ := py.ParseInt(capS)
	ft := py.OrS(get("fixer_trigger"), "off")
	switch {
	case !py.IsDigit(capS) || capN > 50:
		return 400, BoardRefused(c, Reject("agent", "qa_cap", "New QA proposals per day must be between 0 and 50.", form))
	case !In(get("qa_threshold"), "critical", "high", "medium"):
		return 400, BoardRefused(c, Reject("agent", "qa_threshold", "Choose a QA intake severity: critical, high or medium.", form))
	case !In(ft, "off", "column"):
		return 400, BoardRefused(c, Reject("agent", "fixer_trigger", "Choose a fixer trigger: off or column.", form))
	}
	col := py.Head(py.Strip(get("column")), 60)
	if col == "" {
		col = "In Progress"
	}
	rid, err := BoardWriteRequest(py.NewOMap("kind", "agent-settings", "enabled", get("enabled") == "1", "auto_done", get("auto_done") == "1",
		"qa_enabled", get("qa_enabled") == "1", "assignees", users, "column", col, "qa_threshold", get("qa_threshold"),
		"qa_cap", capN, "qa_owner", py.Strip(get("qa_owner")), "fixer_trigger", ft, "chat", get("chat") == "1",
		"secrets", get("secrets") == "1"), sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, "/admin/board?req=" + rid + "#agent"
}
