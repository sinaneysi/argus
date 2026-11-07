package app

// claude_page.go: the Settings -> Claude page (GET /admin/claude). It answers one question first, "Is Argus's AI
// working right now?", in a status block that combines the usage switch, the token and its last test, the location
// (egress) guard and the last real Claude run; then three sections in priority order: Claude token, Location guard,
// Claude usage. Rare settings fold. The POST handlers and the root request flow live in claude_token.go and
// claude_switch.go; this file only renders (DESIGN.md "Settings → Claude", static/claude.css).

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// claudeFacts is everything the status block is decided from (gathered by claudeFactsNow, built by hand in tests).
type claudeFacts struct {
	On bool // Claude usage switch, as live

	TokenKnown bool // root has published the token state at least once
	TokenSet   bool
	Last4      string

	TestResult string // ok | limit | error | stopped | "" (never tested)
	TestMsg    string
	TestAt     int64

	Guard         bool // location guard on
	EgressChecked bool
	EgressBlocked bool // the last check stopped AI (guard on)
	EgressLoc     string
	EgressIP      string
	EgressReason  string
	EgressErr     string
	EgressAt      int64

	HasRun     bool // a real Claude run exists (turns > 0, not skipped)
	RunID      int64
	RunTask    string
	RunOK      bool
	RunSummary string
	RunAt      int64
}

// claudeVerdict is the status block: one state, one sentence, at most one next action.
type claudeVerdict struct {
	Kind   string // ok | warn | fail | off | unknown
	Badge  string // the word on the badge
	Head   string // plain text: the answer
	Detail string // HTML (escaped by the caller): why, in one or two short sentences
	Act    string // the next action's label ("" = none)
	Href   string // where it goes (an anchor on this page)
}

// claudeLastRun is the newest real Claude run: turns > 0 (the model answered or refused) and not skipped by the
// usage switch or the guard ("SKIPPED: …" from the Claude gate). nil when there is none.
func claudeLastRun() Row {
	r := Q(`SELECT id, task, started_at, status, is_error, summary FROM runs WHERE turns > 0
		AND coalesce(summary, '') NOT LIKE 'SKIPPED%' ORDER BY started_at DESC, id DESC LIMIT 1`)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func claudeFactsNow() claudeFacts {
	f := claudeFacts{On: ClaudeOn(nil)}
	st := ClaudeTokenStatus()
	if tok := py.AsMap(py.AsMap(st.Get("token")).Get(claudeTokenVar)); tok != nil {
		f.TokenKnown, f.TokenSet, f.Last4 = true, py.Truthy(tok.Get("set")), tok.S("last4")
	}
	if t := py.AsMap(st.Get("test")); t != nil && t.Len() > 0 {
		f.TestResult, f.TestMsg, f.TestAt = t.S("result"), t.S("message"), py.I(t.Get("at"))
	}
	f.Guard, _, _ = ClaudeEgressSettings()
	if ch := ClaudeEgressCheck(); ch.Len() > 0 && py.Truthy(ch.Get("checked_at")) {
		f.EgressChecked, f.EgressAt = true, py.I(ch.Get("checked_at"))
		f.EgressLoc, f.EgressIP, f.EgressReason, f.EgressErr = ch.S("loc"), ch.S("ip"), ch.S("reason"), ch.S("error")
		if a, ok := ch.Get("allowed").(bool); ok && !a && f.Guard {
			f.EgressBlocked = true
		}
	}
	if r := claudeLastRun(); r != nil {
		f.HasRun, f.RunID, f.RunTask, f.RunAt = true, r.I("id"), r.S("task"), r.I("started_at")
		f.RunOK = r.S("status") != "ERROR" && r.I("is_error") == 0
		f.RunSummary = r.S("summary")
	}
	return f
}

// claudeVerdictOf decides the status block. Order: switched off, stopped by the guard, no token, then the newest
// evidence between the last token test and the last real run (a run that worked after a failed test means the
// token works; a run that failed after a passing test is worth a look, not an alarm).
func claudeVerdictOf(f claudeFacts) claudeVerdict {
	if !f.On {
		return claudeVerdict{"off", "Off", "Argus's AI is off.",
			"Claude usage is switched off, so no model calls are made. Checks still collect their facts.",
			"Turn Claude usage on", "#claude-usage"}
	}
	if f.EgressBlocked {
		d := "Claude traffic would leave from a blocked country, so every AI run is stopped."
		if f.EgressLoc != "" {
			d = "Claude traffic would leave from " + E(f.EgressLoc) + ", which is on the blocked list, so every AI run is stopped."
		} else if f.EgressErr != "" || f.EgressReason != "" {
			d = "The country lookup failed and the guard is set to stop when it cannot tell, so every AI run is stopped."
		}
		return claudeVerdict{"fail", "Stopped", "Argus's AI is stopped by the location guard.",
			d + " Checked " + E(Ago(f.EgressAt)) + ".", "Check the location again", "#claude-egress"}
	}
	if f.TokenKnown && !f.TokenSet {
		return claudeVerdict{"fail", "Not working", "Argus's AI cannot run: there is no Claude token.",
			"Add a token from <span class=\"mono\">claude setup-token</span> below.", "Add a token", "#claude-replace"}
	}
	testBad := f.TestResult == "error"
	auth := reClaudeAuth.MatchString(f.TestMsg) // a new token can fix it (rejected, expired, or an org that blocks this account)
	fixAct, fixHref := "Test the token", "#claude-token"
	if auth {
		fixAct, fixHref = "Replace the token", "#claude-replace"
	}
	testGood := f.TestResult == "ok" || f.TestResult == "limit"
	runNewer := f.HasRun && f.RunAt >= f.TestAt
	runLine := func() string {
		s := E(claudeTaskWord(f.RunTask)) + ", " + E(Ago(f.RunAt))
		if !f.RunOK {
			if m := FirstLine(SafeErr(f.RunSummary), 140); m != "" {
				s += `: <q>` + E(m) + `</q>`
			}
		}
		return s
	}
	switch {
	case f.HasRun && runNewer && f.RunOK:
		return claudeVerdict{"ok", "Working", "Argus's AI is working.", "Last run OK: " + runLine() + ".", "", ""}
	case f.HasRun && runNewer && !f.RunOK && testBad:
		return claudeVerdict{"fail", "Not working", "Argus's AI is not working.",
			"The last token test and the last run both failed. Last run: " + runLine() + "." + claudeAuthHint(auth), fixAct, fixHref}
	case f.HasRun && runNewer && !f.RunOK:
		return claudeVerdict{"warn", "Check", "The last AI run failed.", runLine() + ". Test the token to see whether the login still works.",
			"Test the token", "#claude-token"}
	case testBad:
		d := "The last token test failed " + E(Ago(f.TestAt)) + "."
		if m := FirstLine(SafeErr(f.TestMsg), 160); m != "" {
			d += ` Claude said: <q>` + E(m) + `</q>.`
		}
		return claudeVerdict{"fail", "Not working", "Argus's AI is not working: the token test failed.", d + claudeAuthHint(auth),
			fixAct, fixHref}
	case f.TestResult == "limit":
		return claudeVerdict{"warn", "Paused", "Argus's AI has reached the Claude usage limit.",
			"The login works. Runs start again when the limit resets. Tested " + E(Ago(f.TestAt)) + ".", "", ""}
	case testGood:
		return claudeVerdict{"ok", "Working", "Argus's AI is working.", "The token test passed " + E(Ago(f.TestAt)) + ".", "", ""}
	}
	if f.TestResult == "stopped" {
		return claudeVerdict{"warn", "Check", "The last token test did not run: AI was stopped.",
			"Nothing was tested. Test the token again.", "Test the token", "#claude-token"}
	}
	return claudeVerdict{"unknown", "Not checked", "Argus's AI has not been checked yet.",
		"No AI run and no token test so far. Test the token to find out.", "Test the token", "#claude-token"}
}

// claudeAuthHint is the second sentence of a failed test: what a new token would (or would not) fix.
func claudeAuthHint(auth bool) string {
	if auth {
		return " Replace the token, from an account whose organization allows Claude Code."
	}
	return " This does not look like a bad token; test again in a few minutes."
}

// claudeTaskWord turns a run's task key into words for the status block ("log-rca" -> "log RCA").
func claudeTaskWord(t string) string {
	if t == "" {
		return "a run"
	}
	return reClaudeAbbr.ReplaceAllStringFunc(strings.ReplaceAll(t, "-", " "), strings.ToUpper)
}

type claudeFact struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	Href  string `json:"href,omitempty"`
}

// claudeFactList is the four facts under the status block, in plain text.
func claudeFactList(f claudeFacts) []claudeFact {
	tok := claudeFact{"", "Token", "not known yet", ""}
	set := func(kind, v string) { tok.Kind, tok.Value = kind, v }
	switch {
	case f.TokenKnown && !f.TokenSet:
		set("fail", "not set")
	case f.TestResult == "error":
		set("fail", "test failed "+Ago(f.TestAt))
	case f.TestResult == "ok":
		set("ok", "test passed "+Ago(f.TestAt))
	case f.TestResult == "limit":
		set("warn", "works, usage limit "+Ago(f.TestAt))
	case f.TestResult == "stopped":
		set("warn", "test not run "+Ago(f.TestAt))
	case f.TokenSet:
		set("", "set, not tested")
	}
	loc := claudeFact{"", "Location", "not checked", ""}
	switch {
	case !f.Guard:
		loc.Kind, loc.Value = "warn", "guard off"
	case f.EgressBlocked:
		loc.Kind, loc.Value = "fail", py.OrS(f.EgressLoc, "unknown")+", AI stopped"
	case f.EgressChecked:
		loc.Kind, loc.Value = "ok", py.OrS(f.EgressLoc, "unknown")+", allowed"
	}
	run := claudeFact{"", "Last AI run", "none yet", ""}
	if f.HasRun {
		run = claudeFact{If(f.RunOK, "ok", "fail"), "Last AI run", If(f.RunOK, "OK", "failed") + ", " + Ago(f.RunAt), "/runs/view?id=" + I2S(f.RunID)}
	}
	return []claudeFact{{If(f.On, "ok", "off"), "Usage", If(f.On, "On", "Off"), ""}, tok, loc, run}
}

// --- results of a request to root (?ct=<request id>) --------------------------------------------------------------

var (
	reClaudeReplaced = regexp.MustCompile(`(?s)^replaced …(\S{1,8}), test (\w+): (.*); (kept \(save anyway\)|kept|previous token restored)$`)
	reClaudeTest     = regexp.MustCompile(`(?s)^test (\w+): (.*)$`)
	reClaudeEgress   = regexp.MustCompile(`(?s)^egress (\S+) in (\S+): (.*)$`)
	reClaudeAbbr     = regexp.MustCompile(`\b(rca|vm|qa|cve|ci)\b`)
	reClaudeAuth     = regexp.MustCompile(`(?i)401|403|invalid|expired|revoked|unauthori[sz]ed|authenticat|oauth|organization has disabled|subscription access|login`)
	reClaudeSaid     = regexp.MustCompile(`Claude said: <q>.*?</q>\. `)
)

// claudeResult is the short message for one request: still being applied (the page refreshes itself, CSP-safe meta
// refresh), or what came of it and what to do next. Never the raw result line as the headline.
func claudeResult(c *Ctx, rid, user string) string {
	if !jiraRidRe.MatchString(rid) {
		return ""
	}
	if boardExists(filepath.Join(SET_IN, "claude-token", user+"-"+rid+".json")) {
		c.Refresh = 5
		return `<div class="cl-res">` + StateBox(StBusy, "Applying on "+ARGUS_HOSTNAME+"…",
			"This takes a few seconds. You can leave this page; the result shows here and the page refreshes itself.",
			`<a href="/admin/claude">Pause live updates</a>`) + `</div>`
	}
	r := py.AsMap(py.AsMap(ClaudeTokenStatus().Get("results")).Get(rid))
	if r == nil || r.Get("user") != user {
		c.Refresh = 5
		return `<div class="cl-res">` + StateBox(StBusy, "Sent. Waiting for root to pick it up.",
			"The result shows here. The page refreshes itself.", `<a href="/admin/claude">Pause live updates</a>`) + `</div>`
	}
	kind, title, detail := claudeResultWords(r.S("result"))
	box := ""
	switch kind {
	case "ok":
		box = StateBox(StSynced, title, detail, "")
	case "fail":
		box = StateBox(StFailed, title, reClaudeSaid.ReplaceAllString(detail, ""), "")
	default:
		box = `<div class="banner warn" role="status">` + Icon("alert") + `<span><b>` + E(title) + `</b> ` + detail + `</span></div>`
	}
	return `<div class="cl-res">` + box + `</div>`
}

// claudeResultWords maps root's result line (internal/claudetoken) to a state, a plain title and the next
// step (HTML, escaped here).
func claudeResultWords(res string) (kind, title, detail string) {
	said := func(m string) string {
		if m = strings.TrimSuffix(FirstLine(SafeErr(m), 160), "."); m == "" {
			return ""
		}
		return `Claude said: <q>` + E(m) + `</q>. `
	}
	if strings.HasPrefix(res, "refused: ") {
		return "fail", "Not done: " + strings.TrimSuffix(SafeErr(py.Slice(res, 9)), ".") + ".", "Nothing was changed."
	}
	if m := reClaudeReplaced.FindStringSubmatch(res); m != nil {
		switch {
		case m[2] == "stopped" && m[4] == "previous token restored":
			return "warn", "Not replaced: the test could not run because AI is stopped.",
				said(m[3]) + "The previous token is still in use. Fix Location guard or Claude usage, then try again."
		case m[4] == "previous token restored":
			return "fail", "The new token did not work, so the previous one is back.",
				said(m[3]) + "Check that you copied the whole token, then try again."
		case m[2] == "ok":
			return "ok", "Token replaced and working.", "Claude answered the test. The new token ends in " + E(m[1]) + "."
		case m[2] == "limit":
			return "warn", "Token replaced. Claude says the usage limit is reached.", "The login works; runs start again when the limit resets."
		}
		return "warn", "Token saved, but its test did not pass.", said(m[3]) + "Test it again once Claude is reachable."
	}
	if m := reClaudeTest.FindStringSubmatch(res); m != nil {
		switch m[1] {
		case "ok":
			return "ok", "The token works.", "Claude answered the test."
		case "limit":
			return "warn", "The token works, but the usage limit is reached.", "Runs start again when the limit resets."
		case "stopped":
			return "warn", "Test not run: AI is stopped.", said(m[2]) + "See Location guard below."
		}
		if reClaudeAuth.MatchString(m[2]) {
			return "fail", "Claude refused the token.", said(m[2]) + "Replace the token below."
		}
		return "fail", "The token test failed.", said(m[2]) + "This does not look like a bad token; test again in a few minutes."
	}
	if strings.HasPrefix(res, "Claude egress guard ") {
		return "ok", "Guard settings applied.", "Guard " + E(strings.TrimPrefix(res, "Claude egress guard ")) + "."
	}
	if res == "egress guard is off" {
		return "warn", "The location guard is off.", "AI runs start wherever the traffic leaves from."
	}
	if m := reClaudeEgress.FindStringSubmatch(res); m != nil {
		if m[3] == "allowed" {
			return "ok", "Location checked: allowed.", "Claude traffic leaves from " + E(m[2]) + " (" + E(m[1]) + ")."
		}
		return "fail", "Location checked: AI is stopped.", E(strings.TrimSuffix(SafeErr(m[3]), ".")) + "."
	}
	return "warn", "Finished.", E(strings.TrimSuffix(SafeErr(res), ".")) + "."
}

// --- the three sections -------------------------------------------------------------------------------------------

// claudeTokenState is the token's badge, state and test lines, Claude's words and the API key warning (all HTML).
func claudeTokenState(f claudeFacts) (badge, state, tl, why, warn string) {
	st := ClaudeTokenStatus()
	api := py.AsMap(py.AsMap(st.Get("token")).Get("ANTHROPIC_API_KEY"))
	rep := py.AsMap(st.Get("replaced"))
	test := py.AsMap(st.Get("test"))
	badge = `<span class="badge">Not tested</span>`
	switch f.TestResult {
	case "ok":
		badge = `<span class="badge ok">Working</span>`
	case "limit":
		badge = `<span class="badge warn">Usage limit</span>`
	case "stopped":
		badge = `<span class="badge warn">Not run</span>`
	case "error":
		badge = `<span class="badge fail">Failing</span>`
	}
	state = `Not checked yet. Test it once to see whether it works.`
	if f.TokenKnown && !f.TokenSet {
		state = `<b>Not set.</b> Argus has no Claude login.`
		badge = `<span class="badge fail">Not set</span>`
	}
	if f.TokenKnown {
		if f.TokenSet {
			state = `Set, ends in <span class="mono">…` + E(f.Last4) + `</span>.`
			badge = If(f.TestResult == "", `<span class="badge">Not tested</span>`, badge)
			if rep != nil && py.Truthy(rep.Get("at")) {
				state += ` Replaced <time title="` + E(When(rep.Get("at"))) + `">` + E(Ago(rep.Get("at"))) + `</time> by ` +
					E(py.OrS(rep.Get("by"), "?")) + `.`
			}
		}
	}
	tl = If(f.TokenKnown, `Not tested yet.`, "")
	if test != nil && test.Len() > 0 {
		word := map[string]string{"ok": "passed", "limit": "passed (usage limit reached)", "stopped": "not run (AI stopped)",
			"error": "failed"}[f.TestResult]
		tl = `Last test ` + E(py.OrS(word, "unknown")) + `, <time title="` + E(When(test.Get("at"))) + `">` +
			E(Ago(test.Get("at"))) + `</time> by ` + E(py.OrS(test.Get("by"), "?")) + `.`
	}
	quoted := strings.Contains(claudeVerdictOf(f).Detail, "Claude said:") // the status block already quotes it
	if m := FirstLine(SafeErr(f.TestMsg), 200); m != "" && f.TestResult != "ok" && f.TestResult != "" && !quoted {
		why = `<p class="cl-why">Claude said: <q>` + E(strings.TrimSuffix(m, ".")) + `</q></p>`
	}
	if api != nil && py.Truthy(api.Get("set")) {
		warn = Banner("An Anthropic API key is also set on "+ARGUS_HOSTNAME+". Claude Code uses it first and bills per use. "+
			"Ask root to remove it so this token is used.", "warn")
	}
	return
}

// claudeTokenOpen: the Replace fold starts open when the token is missing or refused, unless the page is refreshing.
func claudeTokenOpen(f claudeFacts, busy bool) bool {
	return !busy && ((f.TokenKnown && !f.TokenSet) || (f.TestResult == "error" && reClaudeAuth.MatchString(f.TestMsg)))
}

// claudeEgressState is the guard's badge, state line, why and the summary of its settings (all HTML).
func claudeEgressState(f claudeFacts) (badge, line, why, sum string) {
	g, blocked, fail := ClaudeEgressSettings()
	ch := ClaudeEgressCheck()
	badge = `<span class="badge">Not checked</span>`
	line = `On. Not checked yet.`
	switch {
	case !g:
		badge, line = `<span class="badge warn">Off</span>`, `<b>Off.</b> AI runs start wherever the traffic leaves from.`
	case f.EgressBlocked:
		badge = `<span class="badge fail">AI stopped</span>`
		line = `<b>AI stopped.</b> The country could not be checked, and the guard stops when it cannot tell.`
		if f.EgressLoc != "" {
			line = `<b>AI stopped.</b> Claude traffic leaves from <b>` + E(f.EgressLoc) + `</b>` +
				If(f.EgressIP != "", ` (<span class="mono">`+E(f.EgressIP)+`</span>)`, "") + `, which is blocked.`
		}
	case f.EgressChecked:
		badge = `<span class="badge ok">Allowed</span>`
		line = `On. Claude traffic leaves from <b>` + E(py.OrS(f.EgressLoc, "an unknown country")) + `</b>` +
			If(f.EgressIP != "", ` (<span class="mono">`+E(f.EgressIP)+`</span>)`, "") + `, which is allowed.`
	}
	if f.EgressChecked {
		line += ` Checked <time title="` + E(When(f.EgressAt)) + `">` + E(Ago(f.EgressAt)) + `</time>.`
	}
	if f.EgressErr != "" && f.EgressLoc == "" {
		why = `<p class="cl-why">The lookup failed: <q>` + E(FirstLine(SafeErr(f.EgressErr), 160)) + `</q></p>`
	}
	if n := py.I(ch.Get("stopped")); n > 0 && f.EgressBlocked {
		why += `<p class="cl-why">Runs stopped so far: ` + I2S(n) + `, the last ` + E(Ago(ch.Get("last_stopped_at"))) + `.</p>`
	}
	bl := strings.Join(blocked, ", ")
	sum = If(g, "On", "Off") + ` · blocks ` + E(py.OrS(bl, "nothing")) + ` · ` + If(fail == "open", "runs if the lookup fails", "stops if the lookup fails")
	return
}

// --- the page -----------------------------------------------------------------------------------------------------
