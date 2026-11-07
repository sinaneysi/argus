package app

// views_agent_progress.go: what the task agent is doing right now (the step of its intake or run, the last activity,
// elapsed time), why it failed (a plain cause, what to do, the exact redacted message, the run) and Plan again.
//
// Sources, all written by the job or by root, never by the app:
//   - agent_tasks (board.db): state, phase, job_started, error, error_detail (JSON: class, raw, is_error, exit_code,
//     run_id, at; the task-agent dispatcher, docs/ROADMAP.md).
//   - <data>/task-agent/out/<key>.<intake|run>.progress.json (the task-agent runner): step, tools, last
//     tool, activity, started, updated. Read only when it belongs to the running job (started >= job_started).
// No percentages: the steps are the job's own; between steps the bar is indeterminate and says how long ago the
// last activity was. The page refreshes itself (meta refresh, CSP-safe) only while a job runs; "Stop refreshing"
// (?live=0) turns that off.

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// The same classes and words as the task-agent error_class (docs/ROADMAP.md) (keep the two in step).
var (
	agentAuthRe = regexp.MustCompile(`(?i)organi[sz]ation has disabled|subscription access|invalid (?:api[ _-]?key|bearer|token|x-api-key)|` +
		`oauth|token (?:has )?(?:expired|been revoked)|revoked|unauthori[sz]ed|authenticat|\b401\b|` +
		`not logged in|please run /login|/login\b|login (?:failed|required|expired)`)
	agentEgressRe = regexp.MustCompile(`(?i)\b403\b|request not allowed|unsupported (?:country|region)|not available in your (?:country|region)|` +
		`egress|unable to connect|connection (?:error|refused|reset)|getaddrinfo|ENOTFOUND|ECONNREFUSED|ETIMEDOUT|EAI_AGAIN`)
	agentOutputRe = regexp.MustCompile(`no output|not valid JSON|not a JSON object|output too large|classification missing|no valid result`)
)

// agentErrClass is error_class(error, raw): auth | egress | limit | timeout | start | no-steps | output | other.
func agentErrClass(errS, raw string) string {
	text := errS + " " + raw
	switch {
	case strings.HasPrefix(errS, "claude-limit:"):
		return "limit"
	case agentAuthRe.MatchString(text):
		return "auth"
	case agentEgressRe.MatchString(text):
		return "egress"
	case strings.HasPrefix(errS, "no result") || strings.Contains(errS, "timeout"):
		return "timeout"
	case strings.HasPrefix(errS, "could not start"):
		return "start"
	case strings.Contains(errS, "no plan steps"):
		return "no-steps"
	case agentOutputRe.MatchString(errS):
		return "output"
	}
	return "other"
}

// agentDetail is the stored error_detail (empty map when none or unreadable).
func agentDetail(a Row) map[string]any {
	d := map[string]any{}
	if s := a.S("error_detail"); s != "" {
		if json.Unmarshal([]byte(s), &d) != nil {
			return map[string]any{}
		}
	}
	return d
}

func detS(d map[string]any, k string) string {
	if s, ok := d[k].(string); ok {
		return s
	}
	return ""
}

func detI(d map[string]any, k string) (int64, bool) {
	if f, ok := d[k].(float64); ok {
		return int64(f), true
	}
	return 0, false
}

// agentSafeRaw is SafeErr with room for the whole stored message (300 characters, one line): secret-looking values,
// long tokens and absolute paths taken out, escaped.
func agentSafeRaw(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = reSecretKV.ReplaceAllString(s, "$1 [hidden]")
	s = reLongToken.ReplaceAllString(s, "[hidden]")
	s = rePath.ReplaceAllString(s, "$1[path]")
	if py.Len(s) > 300 {
		s = py.Head(s, 299) + "…"
	}
	return E(s)
}

func agentDur(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	switch {
	case sec < 60:
		return I2S(sec) + " s"
	case sec < 3600:
		return I2S(sec/60) + " min" + If(sec%60 != 0 && sec < 600, " "+I2S(sec%60)+" s", "")
	}
	return I2S(sec/3600) + " h " + I2S(sec%3600/60) + " min"
}

// agentProgressFile is the job's progress JSON, or nil.
func agentProgressFile(key, phase string) *py.OMap {
	if !agentKeyRe.MatchString(key) || !In(phase, "intake", "run") {
		return nil
	}
	m := ReadJSONMap(filepath.Join(filepath.Dir(DB), "task-agent", "out", key+"."+phase+".progress.json"))
	if m == nil || m.Len() == 0 || m.Get("phase") != phase {
		return nil
	}
	return m
}

var agentStepWords = map[string][]string{
	"intake": {"Queued", "Reading the task", "Gathering context", "Writing the plan", "Done"},
	"run":    {"Queued", "Reading the plan", "Working (read-only tools)", "Writing the report", "Done"},
}

// agentStepIndex: the job's step name -> its place in agentStepWords.
var agentStepIndex = map[string]int{"queued": 0, "prepare": 1, "reading": 1, "gathering": 2, "working": 2, "writing": 3,
	"recording": 4, "skipped": 0}

// agentLive reports whether this page view keeps refreshing (not ?live=0).
func agentLive(c *Ctx) bool {
	if c == nil {
		return true
	}
	u, err := url.Parse(c.Path)
	if err != nil {
		return true
	}
	return u.Query().Get("live") != "0"
}

// agentProgress is the live block while an intake or run is queued or running ("" otherwise).
// agentProg is the live progress of a task as data: the Go panel and /api/v1/board/task both render it. Now and Meta
// are HTML fragments (escaped, links kept); Fix is the fixer's own short form, without steps.
type agentProg struct {
	Label, Phase, Now string
	Cur               int
	Bar, Fix          bool
	Meta              []string
}

func agentProgressData(c *Ctx, a Row, key string) *agentProg {
	state, phase := a.Str("state"), py.OrS(a.Get("phase"), "intake")
	if !In(state, "planning", "running", "approved") {
		return nil
	}
	now := NowInt()
	live, forced := agentLive(c), false
	wait := ""
	if state == "planning" && !a.T("job_started") {
		wait = agentWaitNote(a) // Claude paused or the debounce
		if !ClaudeOn(nil) {
			live, forced = false, true // nothing will start: no point refreshing (the Now line says why)
		}
	}
	if phase == "fix" && state == "running" {
		fr := E(a.S("fix_request"))
		if live {
			c.Refresh = 10
		}
		return &agentProg{Fix: true, Bar: true, Phase: phase, Label: "Run progress",
			Now:  `the fixer works the approved plan` + If(fr != "", ` as <a href="/admin/fix/run?id=`+fr+`">fix run `+fr+`</a>`, "") + `.`,
			Meta: []string{"Running " + agentDur(now-a.I("job_started")), agentLiveLink(c, key, live)}}
	}
	if state == "approved" {
		phase = "run"
	}
	label := If(phase == "run", "Run progress", "Planning progress")
	cur, now1, meta := 0, "", []string{}
	if state == "approved" || !a.T("job_started") {
		now1 = If(state == "approved", "approved; the run starts at the next board sync (within 2 min).",
			"queued; planning starts at the next board sync (within 2 min).")
		if wait != "" {
			now1 = "queued. " + wait + "."
		}
	} else {
		started := a.I("job_started")
		meta = append(meta, If(phase == "run", "running ", "planning ")+agentDur(now-started))
		p := agentProgressFile(key, phase)
		if p != nil && py.I(py.Or(p.Get("started"), int64(0))) >= started-5 {
			if i, ok := agentStepIndex[py.Str(p.Get("step"))]; ok {
				cur = i
			}
			if act := py.Strip(py.OrS(p.Get("activity"), "")); act != "" {
				now1 = act
			}
			if n := py.I(py.Or(p.Get("tools"), int64(0))); n > 0 {
				meta = append(meta, Itoa(int(n))+" tool call"+py.Plural(int(n)))
			}
			if up := py.I(py.Or(p.Get("updated"), int64(0))); up > 0 {
				ago := now - up
				w := "last activity " + agentDur(ago) + " ago"
				if ago > 300 && cur < 4 {
					w = `<span class="warnc">` + w + `: Claude may be thinking, or stuck; <a href="#agent-stop">Stop</a> ends it</span>`
				}
				meta = append(meta, w)
			}
			if cur == 4 {
				now1 = "storing the result; the board sync shows it within 2 min."
			}
		} else {
			cur = 1
			now1 = "starting the job on " + ARGUS_HOSTNAME + "."
		}
	}
	if live {
		c.Refresh = 10
	}
	if !forced {
		meta = append(meta, agentLiveLink(c, key, live))
	}
	return &agentProg{Label: label, Phase: phase, Cur: cur, Now: E(now1), Bar: cur > 0 && cur < 4, Meta: meta}
}

func agentPageURL(c *Ctx, key, live, frag string) string {
	u, err := url.Parse(agentTaskURL(key))
	if c != nil && strings.HasPrefix(c.Path, "/board/task?") {
		if v, e := url.Parse(c.Path); e == nil {
			u, err = v, nil
		}
	}
	if err != nil {
		return agentTaskURL(key) + "#" + frag
	}
	q := u.Query()
	if live == "" {
		q.Del("live")
	} else {
		q.Set("live", live)
	}
	return u.Path + "?" + q.Encode() + "#" + frag
}

// agentLiveLink: "Stop refreshing" while the page refreshes itself, "Refresh live" after.
func agentLiveLink(c *Ctx, key string, live bool) string {
	if live {
		return `<a href="` + E(agentPageURL(c, key, "0", "agent")) + `">Pause live updates</a>`
	}
	return `Not refreshing. <a href="` + E(agentPageURL(c, key, "", "agent")) + `">Refresh live</a>`
}

// agentErrorWords is (headline, what to do) for a failure class: "Planning failed: <cause>." / "The run failed: <cause>.".
func agentErrorWords(cls, phase, key string, d map[string]any) (string, string) {
	what := If(phase == "run", "The run failed: ", "Planning failed: ")
	switch cls {
	case "auth":
		return what + "Claude rejected the login.", `Replace the token in <a href="/admin/claude">Settings → Claude</a>. When the new token passes ` +
			`its test, Argus plans this task again by itself.`
	case "egress":
		return what + "Claude could not be reached from " + ARGUS_HOSTNAME + " (network or egress country).",
			`Check the egress check in <a href="/admin/claude#claude-egress">Settings → Claude</a>, then choose Plan again. A replaced and tested ` +
				`token also re-plans it by itself.`
	case "timeout":
		mins := If(phase == "run", "65", "12")
		if x, ok := detI(d, "exit_code"); ok && x == 124 {
			mins = If(phase == "run", "60", "10")
		}
		return what + "timed out after " + mins + " min without a result.", "Plan again; if it repeats, narrow the task or the instructions."
	case "start":
		return what + "Argus could not start the job.", "Plan again; if it repeats, check the unit ops-agent-task-" + If(phase == "run", "run", "intake") +
			"@" + E(key) + " on " + E(ARGUS_HOSTNAME) + "."
	case "no-steps":
		return what + "the plan had no steps.", "Add detail to the task or instructions, then Plan again."
	case "output":
		return what + "no usable " + If(phase == "run", "result", "plan") + " came back.", "Plan again; the exact message is below."
	}
	return what + "the agent stopped with an error.", "Plan again; the exact message is below."
}

// agentErrorBlock is the failed state: a one-line cause and what to do, then the exact (redacted) message, Claude's
// flags, the run and the time. A Claude usage limit is a pause (amber), not a failure. "" otherwise.
// agentErrorFacts is "What failed exactly": label and HTML value of each fact.
func agentErrorFacts(a Row, key, phase, raw string, d map[string]any) [][2]string {
	var facts [][2]string
	add := func(k, v string) { facts = append(facts, [2]string{k, v}) }
	if raw != "" {
		add("Message", `<span class="mono ag-raw">`+agentSafeRaw(raw)+`</span>`)
	}
	if v, ok := d["is_error"].(bool); ok {
		add("Claude reported an error", If(v, "yes (is_error)", "no"))
	}
	if x, ok := detI(d, "exit_code"); ok {
		add("Exit code", I2S(x)+If(x == 124, " (stopped by the time limit)", ""))
	}
	if rid := detS(d, "run_id"); rid != "" && py.IsDigit(rid) {
		add("Run", `<a href="/runs/view?id=`+rid+`">run `+rid+`</a>`)
	}
	at, _ := detI(d, "at")
	if at == 0 {
		at = a.I("updated")
	}
	if at > 0 {
		add("When", When(at))
	}
	if p := agentProgressFile(key, phase); p != nil && at > 0 {
		st, up := py.I(py.Or(p.Get("started"), int64(0))), py.I(py.Or(p.Get("updated"), int64(0)))
		if st <= at && at-up < 7200 {
			i := agentStepIndex[py.Str(p.Get("step"))]
			w := agentStepWords[phase]
			if w != nil && i >= 0 && i < len(w) {
				last := w[i] + If(py.Truthy(p.Get("activity")), ": "+py.Str(p.Get("activity")), "")
				if n := py.I(py.Or(p.Get("tools"), int64(0))); n > 0 {
					last += " (" + Itoa(int(n)) + " tool call" + py.Plural(int(n)) + ")"
				}
				add("Last step", E(last))
			}
		}
	}
	return facts
}

func agentErrorBlock(a Row, key string, chatOn bool, replan string) string {
	errS := a.S("error")
	d := agentDetail(a)
	phase := py.OrS(detS(d, "phase"), py.OrS(a.Get("phase"), "intake"))
	if agentLimitRe.MatchString(errS) {
		when := "the limit resets"
		if m := agentResetRe.FindStringSubmatch(errS); m != nil {
			when = m[1]
		}
		next := ""
		if r := a.I("retry_at"); r > 0 {
			next = " Planning again at " + E(When(r)) + "."
		}
		return `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>Claude usage limit reached.</b> Argus resumes after ` +
			E(when) + `. Nothing was changed; the plan will start automatically after the reset.` + next + `</span></div>` + replan
	}
	if a.Str("state") != "failed" {
		return ""
	}
	if phase == "fix" {
		return agentFailNote(a, chatOn) + replan
	}
	raw := detS(d, "raw")
	if raw == "" {
		raw = errS
	}
	cls := py.OrS(detS(d, "class"), agentErrClass(errS, raw))
	head, next := agentErrorWords(cls, phase, key, d)
	safe := If(phase == "run", "Nothing was merged: a run only reads hosts and its changes stay on an agent/* branch.",
		"Nothing was changed: planning only reads the task.")
	var facts []string
	for _, f := range agentErrorFacts(a, key, phase, raw, d) {
		facts = append(facts, `<dt>`+f[0]+`</dt><dd>`+f[1]+`</dd>`)
	}
	return `<div class="ag-err"><div class="banner fail" role="alert">` + Icon("alert") + `<span><b>` + E(head) + `</b> ` + next + ` ` + safe +
		`</span></div>` + replan + `<details class="plain ag-facts"><summary>What failed exactly</summary><dl class="kv">` +
		strings.Join(facts, "") + `</dl><p class="section-note">Every attempt stays in the run log below.</p></details></div>`
}

// agentReplanPending is the id of a Plan again request for key the board sync has not picked up yet ("" when none).
func agentReplanPending(key string) string {
	d := filepath.Join(SET_IN, "board-requests")
	ents, err := os.ReadDir(d)
	if err != nil {
		return ""
	}
	var ids []string
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") || len(ids) > 200 {
			continue
		}
		ids = append(ids, strings.TrimSuffix(n, ".json"))
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !boardIDRe.MatchString(id) {
			continue
		}
		m := ReadJSONMap(filepath.Join(d, id+".json"))
		if m != nil && m.Get("kind") == "agent-replan" && m.Get("key") == key {
			return id
		}
	}
	return ""
}

// agentCanReplan is (show the button, enabled, why not) for Plan again: shown when the agent failed, stopped, or
// waits without a plan; refused (disabled, with the reason) while Claude usage is off or the egress guard blocks.
func agentCanReplan(a Row, admin bool) (bool, bool, string) {
	state := a.Str("state")
	show := In(state, "failed", "stopped") || (state == "waiting" && !a.T("plan"))
	if !show || !admin {
		return show && admin, false, ""
	}
	if agentReplanPending(a.Str("key")) != "" {
		return true, false, "Plan again is queued; the board sync picks it up within seconds."
	}
	if !ClaudeOn(nil) {
		return true, false, "Claude usage is off (Settings → Claude): turn it on to plan again."
	}
	if stopped, why := claudeEgressStopped(); stopped {
		return true, false, "The Claude egress guard blocks model runs: " + why + "."
	}
	if a.S("error") != "" && strings.HasPrefix(a.S("error"), "moved out of the agent column") {
		return true, false, "The task left the agent column or its assignee changed: move it back to plan again."
	}
	return true, true, ""
}
