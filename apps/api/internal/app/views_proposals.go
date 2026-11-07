package app

// views_proposals.py: Proposed tasks (/proposals, admins), the approval queue in front of Jira. Root's
// bin/proposals.py writes board.db; this page reads it, renders, validates the form and writes one request file.

import (
	"regexp"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

var proposalPaths = [][2]string{{"qa", "QA and monitoring intake"}, {"page", "Page-level production incidents"},
	{"cve", "Security alerts (CVE)"}, {"cicd", "CI/CD findings"}, {"cred", "Credential expiry"}}

var proposalPriorities = []string{"Highest", "High", "Medium", "Low", "Lowest"}

var proposalSevKind = map[string]string{"critical": "fail", "page": "fail", "high": "fail", "medium": "warn", "warning": "warn",
	"low": "ok", "info": "ok"}

var proposalStateKind = map[string]string{"accepted": "ok", "rejected": "fail", "expired": "", "snoozed": "warn", "pending": "warn"}

var proposalViews = [][2]string{{"pending", "Waiting"}, {"snoozed", "Snoozed"}, {"decided", "Decided"}}

var (
	proposalNameRe    = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)
	proposalProjectRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)
	proposalLabelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
)

const (
	proposalMaxTitle    = 200
	proposalMaxReason   = 300
	proposalMaxIDs      = 50
	proposalDecidedDays = 14
)

type proposalSettings struct {
	auto       map[string]bool
	expiryDays int64
	rejectDays int64
}

func proposalsSettings() proposalSettings {
	s := proposalSettings{auto: map[string]bool{}}
	var d *py.OMap
	if r := BoardBQ("SELECT v FROM state WHERE k='proposal_settings'"); len(r) > 0 {
		if v, err := py.ParseJSON([]byte(r[0].S("v"))); err == nil {
			d = py.AsMap(v)
		}
	}
	if d == nil {
		d = py.NewOMap()
	}
	auto := py.AsMap(d.Get("auto"))
	for _, p := range proposalPaths {
		s.auto[p[0]] = auto != nil && auto.Get(p[0]) == true
	}
	s.expiryDays = py.I(py.Or(d.Get("expiry_days"), int64(14)))
	s.rejectDays = py.I(py.Or(d.Get("reject_days"), int64(7)))
	return s
}

func proposalOn(s proposalSettings) []string {
	var on []string
	for _, p := range proposalPaths {
		if s.auto[p[0]] {
			on = append(on, p[1])
		}
	}
	return on
}

// ProposalsPendingCount is views_proposals.pending_count().
func ProposalsPendingCount() int64 {
	r := BoardBQ("SELECT count(*) n FROM proposals WHERE state='pending'")
	if len(r) == 0 {
		return 0
	}
	return r[0].I("n")
}

func proposalDayStart() int64 {
	t := LocalTime(Now())
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local).Unix()
}

func proposalLiveAt() int64 {
	r := BoardBQ("SELECT v FROM state WHERE k='proposals_live_at'")
	if len(r) > 0 && py.IsDigit(r[0].Str("v")) {
		return r[0].I("v")
	}
	return 0
}

func proposalLegacy() []Row {
	lv := proposalLiveAt()
	if lv == 0 {
		return nil
	}
	return BoardBQ("SELECT * FROM tasks WHERE created_by='argus-auto' AND created_at<? ORDER BY created_at DESC LIMIT 500", lv)
}

func proposalSourceLink(p Row) string {
	kind, sid := p.Str("source_kind"), p.Str("source_id")
	name := kind + " " + sid
	switch kind {
	case "incident":
		name = "incident #" + sid
	case "cve":
		name = sid
	case "cicd":
		name = "CI/CD #" + sid
	case "rca":
		name = "RCA " + sid
	case "cred":
		name = "credential " + strings.TrimPrefix(sid, "cred-")
	case "chat": // ADR-0013 P2b: <thread key>.<message seq>; the link opens the message
		name = "chat " + sid
	}
	if p.T("source_url") && strings.HasPrefix(p.Str("source_url"), "/") {
		return `<a class="tag" href="` + E(p.Str("source_url")) + `">` + E(name) + `</a>`
	}
	return `<span class="tag">` + E(name) + `</span>`
}

func proposalSevBadge(sev any) string {
	s := strings.ToLower(py.OrS(sev, ""))
	if s == "" {
		return `<span class="badge" title="The source gave no severity">unrated</span>`
	}
	return `<span class="badge ` + proposalSevKind[s] + `">` + E(s) + `</span>`
}

func proposalProjectChoices(sess *Session) (string, []string) {
	c := JiraConn(sess.Username)
	if c == nil {
		c = py.NewOMap()
	}
	var raw []any
	if c.Get("mode") == "rw" {
		raw = append(raw, c.Get("default_project"))
		raw = append(raw, py.AsList(c.Get("projects"))...)
	}
	st := BoardStatus()
	if py.Truthy(st.Get("configured")) || len(raw) == 0 {
		raw = append(raw, st.Get("project"))
	}
	var out []string
	seen := map[string]bool{}
	for _, x := range raw {
		s, ok := x.(string)
		if ok && proposalProjectRe.MatchString(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out
}

type proposalUser struct{ name, display string }

func proposalAssignable(project string) []proposalUser {
	r := BoardBQ("SELECT v FROM state WHERE k=?", "mx_users:"+project)
	var out []proposalUser
	if len(r) == 0 {
		return nil
	}
	v, err := py.ParseJSON([]byte(r[0].S("v")))
	if err != nil {
		return nil
	}
	for _, u := range py.AsList(py.AsMap(v).Get("users")) {
		m := py.AsMap(u)
		if m == nil || !proposalNameRe.MatchString(py.Str(m.GetD("name", ""))) {
			continue
		}
		out = append(out, proposalUser{py.Str(m.Get("name")), py.OrS(m.Get("display"), py.Str(m.Get("name")))})
		if len(out) == 200 {
			break
		}
	}
	return out
}

func proposalLabels(p Row) string {
	v, err := py.ParseJSON([]byte(py.OrS(p.Get("labels"), "[]")))
	if err != nil {
		return ""
	}
	var xs []string
	for _, x := range py.AsList(v) {
		xs = append(xs, py.Str(x))
	}
	return strings.Join(xs, " ")
}

func proposalAcceptForm(p Row, sess *Session, project string, projects []string, users []proposalUser, ret string) string {
	pid := I2S(p.I("id"))
	sugg := py.OrS(p.Get("assignee_suggestion"), "")
	has := false
	for _, u := range users {
		if u.name == sugg {
			has = true
		}
	}
	var opts strings.Builder
	if sugg != "" && !has {
		opts.WriteString(`<option value="` + E(sugg) + `" selected>` + E(sugg) + ` (suggested)</option>`)
	}
	for _, u := range users {
		opts.WriteString(`<option value="` + E(u.name) + `"` + If(u.name == sugg, " selected", "") + `>` + E(u.display) + `</option>`)
	}
	opts.WriteString(`<option value=""` + If(sugg != "", "", " selected") + `>Unassigned</option>`)
	var popts strings.Builder
	for _, x := range projects {
		popts.WriteString(`<option` + If(x == project, " selected", "") + `>` + E(x) + `</option>`)
	}
	ps := popts.String()
	if ps == "" {
		ps = `<option value="">default</option>`
	}
	prio := py.OrS(p.Get("priority"), "Medium")
	var pri strings.Builder
	for _, x := range proposalPriorities {
		pri.WriteString(`<option` + If(x == prio, " selected", "") + `>` + x + `</option>`)
	}
	note := ""
	if len(users) == 0 {
		note = `<p class="hint">The assignable users of this project are not loaded yet (the board sync ` +
			`reads them from Jira); leave Unassigned or keep the suggestion.</p>`
	}
	return `<details class="pp-acc"><summary>Accept &amp; create ` + proposalIssueNoun(false) + `</summary>` +
		`<form method="post" action="/proposals/act" class="taskform pp-form"><input type="hidden" name="csrf" value="` + sess.CSRF + `">` + boardReturnInput(ret) +
		`<input type="hidden" name="action" value="accept"><input type="hidden" name="id" value="` + pid + `">` +
		`<input type="hidden" name="edit" value="1">` +
		trackerChoice(sess, GitLabDefaultTracker(), "") +
		`<label>Title<input name="title" value="` + E(p.Str("title")) + `" maxlength="` + Itoa(proposalMaxTitle) + `" required></label>` +
		`<div class="tf-row"><label>Assignee<select name="assignee">` + opts.String() + `</select></label>` +
		`<label>Project<select name="project">` + ps + `</select></label>` +
		`<label>Priority<select name="priority">` + pri.String() + `</select></label>` +
		`<label>Labels<input name="labels" value="` + E(proposalLabels(p)) + `" maxlength="300" placeholder="space separated"></label></div>` +
		note +
		`<div class="savebar"><button class="secondary">` + If(GitLabConnected(), "Accept &amp; create issue", "Accept &amp; create "+proposalIssueNoun(false)) + `</button>` +
		`<span class="muted small">` + If(GitLabConnected(), "Project applies to Jira only; in GitLab the assignee is matched by user name.",
		"Created with your Jira connection (read and write), else the shared one.") + `</span></div>` +
		`</form></details>`
}

func proposalCount(q string, args ...any) int64 {
	r := BoardBQ(q, args...)
	if len(r) == 0 {
		return 0
	}
	return r[0].I("n")
}

// ProposalsPost is views_proposals.post(path, form, sess).
func ProposalsPost(c *Ctx, path string, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if sess.Role != "admin" {
		return 403, "Admins only"
	}
	if path == "/proposals/settings" {
		exp, rej := get("expiry_days"), get("reject_days")
		if !(py.IsDigit(exp) && py.IsDigit(rej)) {
			return 400, "Days must be 1 to 90"
		}
		e, r := py.I(exp), py.I(rej)
		if !(1 <= e && e <= 90 && 1 <= r && r <= 90) || len(exp) > 9 || len(rej) > 9 {
			return 400, "Days must be 1 to 90"
		}
		auto := py.NewOMap()
		for _, p := range proposalPaths {
			auto.Set(p[0], get("auto_"+p[0]) == "1")
		}
		rid, err := BoardWriteRequest(py.NewOMap("kind", "proposal-settings", "auto", auto, "expiry_days", e, "reject_days", r), sess)
		if err != nil {
			return boardCouldNotSave(c, err)
		}
		return 303, boardRedirect(form, rid, "", "/review?kind=proposals&req="+rid)
	}
	if path != "/proposals/act" {
		return 404, "Not found"
	}
	act := get("action")
	ids := form.List("id")
	ok := In(act, "accept", "reject", "snooze") && len(ids) > 0 && len(ids) <= proposalMaxIDs
	for _, x := range ids {
		if !py.IsDigit(x) || len([]rune(x)) >= 10 {
			ok = false
		}
	}
	if !ok {
		return 400, "Choose at least one proposed task and an action"
	}
	var idl []any
	seen := map[int64]bool{}
	for _, x := range ids {
		n := py.I(x)
		if !seen[n] {
			seen[n] = true
			idl = append(idl, n)
		}
	}
	payload := py.NewOMap("kind", "proposal", "action", act, "ids", idl)
	if act == "reject" {
		reason := strings.Join(py.Split(get("reason")), " ")
		if len([]rune(reason)) > proposalMaxReason {
			return 400, "A reason is " + Itoa(proposalMaxReason) + " characters at most"
		}
		payload.Set("reason", reason)
	}
	if act == "accept" {
		tracker := get("tracker")
		if tracker == "" {
			tracker = GitLabDefaultTracker() // "Accept as proposed" (bulk): the default tracker
		}
		if !In(tracker, gitlabTrackers...) {
			return 400, "Choose Jira or GitLab issues"
		}
		why := BoardWriteBlock(sess)
		if tracker == "gitlab" {
			why = GitLabWriteBlock()
			payload.Set("tracker", "gitlab")
			glp, ok := gitlabCheckProject(get("gl_project"))
			if !ok {
				return 400, "Choose a GitLab project of the connected board"
			}
			if glp != "" {
				payload.Set("gl_project", glp)
			}
		}
		if why != "" {
			return 403, "Not sent: " + why
		}
		if get("edit") == "1" {
			if len(idl) != 1 {
				return 400, "Changes apply to one proposal at a time"
			}
			title := strings.Join(py.Split(get("title")), " ")
			labels := py.Split(get("labels"))
			a, project, prio := get("assignee"), get("project"), get("priority")
			if title == "" || len([]rune(title)) > proposalMaxTitle {
				return 400, "The title must be 1 to " + Itoa(proposalMaxTitle) + " characters"
			}
			if !In(prio, proposalPriorities...) || (a != "" && !proposalNameRe.MatchString(a)) ||
				(project != "" && !proposalProjectRe.MatchString(project)) {
				return 400, "Check the assignee, project and priority"
			}
			lok := len(labels) <= 10
			la := []any{}
			for _, x := range labels {
				if !proposalLabelRe.MatchString(x) {
					lok = false
				}
				la = append(la, x)
			}
			if !lok {
				return 400, "Labels: up to 10 words of letters, digits, dot, underscore and dash"
			}
			payload.Set("edit", py.NewOMap("title", title, "assignee", a, "project", project, "priority", prio, "labels", la))
		}
	}
	rid, err := BoardWriteRequest(payload, sess)
	if err != nil {
		return boardCouldNotSave(c, err)
	}
	return 303, boardRedirect(form, rid, "", "/review?kind=proposals&req="+rid)
}
