package app

// The review panel of the Review inbox (views_review.go): the selected decision in the order a person decides it
// (DESIGN.md "Review inbox"): title, kind, severity, environment; affected targets and deadlines; the proposed
// outcome; the evidence and its source; what happens after accepting; the actions (one primary, reject secondary
// with its reason rule); then provenance and history, folded. Every value is a stored field; a missing or generic
// one is said to be missing, never filled in. The forms are the kinds' existing ones (same handler, fields, CSRF,
// MFA policy); they return to the queue the person came from, without the selection, so nothing is picked for them.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// rvGenericEvidence: texts that name no observation (what the intake writes when the source gave nothing).
var rvGenericEvidence = map[string]bool{"from argus": true, "argus": true, "n/a": true, "na": true, "none": true, "-": true,
	"unknown": true, "see source": true, "see the linked argus page": true, "created from argus": true, "argus's own data": true}

// rvEvidenceMissing: no evidence, or a placeholder that names no observation.
func rvEvidenceMissing(ev string) bool {
	s := strings.ToLower(strings.Trim(ev, " .\t\r\n"))
	return s == "" || rvGenericEvidence[s]
}

// rvNoEvidenceField is the neutral state of a kind that has no evidence field at all (plans, chat requests): nothing
// is wrong with this item, so it is not shown as a warning.
func rvNoEvidenceField(what, look string) string {
	return `<div class="rvp-noev rvp-nofield" role="note"><p><b>` + E(what) + `</b></p>` + If(look != "", `<p>`+look+`</p>`, "") + `</div>`
}

// rvNoEvidence is the explicit missing-evidence state: what was stored, and where to look instead.
func rvNoEvidence(stored, look string) string {
	got := ""
	if s := strings.TrimSpace(stored); s != "" {
		got = "The source sent only “" + E(py.Head(s, 60)) + "”, which names no observation."
	}
	return `<div class="rvp-noev" role="note"><p><b>No evidence recorded.</b> ` + If(got != "", got+" ", "") + `Check the source before you decide.</p>` +
		If(look != "", `<p>`+look+`</p>`, "") + `</div>`
}

type rvParts struct {
	facts                                [][2]string
	outcome, evidence, after, acts, more string
}

func rvKV(rows [][2]string, cls string) string {
	var b strings.Builder
	for _, r := range rows {
		if r[1] != "" {
			b.WriteString(`<dt>` + r[0] + `</dt><dd>` + r[1] + `</dd>`)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return `<dl class="kv ` + cls + `">` + b.String() + `</dl>`
}

func rvMissing(s string) string { return `<span class="rvp-missing">` + E(s) + `</span>` }

func rvMonoList(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, x := range xs {
		b.WriteString(`<span class="mono">` + E(x) + `</span>`)
	}
	return b.String()
}

func rvWhenAgo(at int64) string {
	if at <= 0 {
		return rvMissing("Not recorded")
	}
	return When(at) + ` <span class="muted">(` + E(Ago(at)) + `)</span>`
}

// --- task proposals ----------------------------------------------------------------------------------------------

func rvPanelProposal(c *Ctx, it *rvItem, ret string, admin bool) rvParts {
	p := it.row
	pid := it.id
	noun := proposalIssueNoun(false)
	now := NowInt()
	left := it.due - now
	var out rvParts
	out.facts = [][2]string{
		{"Affected", If(len(it.targets) > 0, rvMonoList(it.targets), rvMissing("No host recorded"))},
		{"Source", proposalSourceLink(p) + ` <span class="muted">` + E(proposalSrcLabel(p.S("path"))) + `</span>`},
		{"Decision deadline", `Proposal expires ` + When(it.due) + ` <span class="` + If(left < proposalSoonDays*86400, "warnc", "muted") + `">(` +
			E(rvLeft(it.due)) + `)</span><span class="rvp-note">An expired proposal is recorded as rejected (“expired”). That does not resolve the issue.</span>`},
		{"Operational deadline", rvMissing("None recorded")},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	var labels []string
	if l := proposalLabels(p); l != "" {
		labels = append(labels, "labels <code>"+E(l)+"</code>")
	}
	if p.T("assignee_suggestion") {
		labels = append(labels, "suggested assignee "+E(p.S("assignee_suggestion")))
	}
	out.outcome = `<p>Create a ` + E(noun) + ` titled “` + E(p.S("title")) + `”, priority ` + E(py.OrS(p.Get("priority"), "Medium")) +
		If(len(labels) > 0, ", "+strings.Join(labels, ", "), "") + `.</p>`
	if d := py.Strip(p.S("description")); d != "" && !rvSameText(d, p.S("title")) {
		out.outcome += `<div class="rvp-text" dir="auto">` + E(d) + `</div>`
	}
	if fp := py.Strip(p.S("fix_prompt")); fp != "" {
		out.outcome += `<details class="rvp-fold"><summary>Fix prompt that goes into the ` + E(noun) + `</summary><pre class="prompt"><code>` +
			E(fp) + `</code></pre></details>`
	}
	ev := py.Strip(p.S("evidence"))
	fresh := `Proposed ` + rvWhenAgo(p.I("created_at"))
	if p.T("refreshed") && p.I("updated_at") > 0 {
		fresh += `; last refreshed by its source ` + rvWhenAgo(p.I("updated_at")) + `, ` + I2S(p.I("refreshed")) + ` time` + If(p.I("refreshed") == 1, "", "s")
	}
	src := proposalSourceLink(p)
	look := `Source record: ` + src
	if p.T("source_url") && strings.HasPrefix(p.S("source_url"), "/") {
		look = `Inspect the source: ` + src
	}
	if rvEvidenceMissing(ev) {
		out.evidence = rvNoEvidence(ev, look) + `<p class="muted small">` + fresh + `.</p>`
	} else {
		out.evidence = `<pre class="prompt rvp-ev"><code>` + E(ev) + `</code></pre><p class="muted small">` + fresh + `.</p><p>` + look + `</p>`
	}
	out.after = `<p>Argus creates the ` + E(noun) + ` and a task on the board, as proposed. Accepting creates tracked work only: ` +
		`it runs nothing on a host. There is no undo here; close the issue in the tracker if it was a mistake.</p>`
	if admin {
		csrf := rvHidden("csrf", c.Sess.CSRF, "return", ret)
		why := BoardWriteBlock(c.Sess)
		if GitLabDefaultTracker() == "gitlab" {
			why = GitLabWriteBlock()
		}
		primary := `<form method="post" action="/proposals/act" class="inline">` + csrf + rvHidden("action", "accept", "id", pid) +
			`<button>Create ` + E(noun) + `</button></form>`
		if why != "" {
			primary = `<button disabled>Create ` + E(noun) + `</button><span class="muted small">` + E(why) + `</span>`
		}
		project, projects := proposalProjectChoices(c.Sess)
		var users []proposalUser
		if project != "" {
			users = proposalAssignable(project)
		}
		edit := proposalAcceptForm(p, c.Sess, project, projects, users, ret)
		edit = strings.Replace(edit, `<summary>Accept &amp; create `+noun+`</summary>`, `<summary>Edit, then create</summary>`, 1)
		snooze := ""
		note := "Expiry keeps running while a proposal is snoozed: snoozing never extends it."
		if left > 86400 {
			snooze = `<form method="post" action="/proposals/act" class="inline">` + csrf + rvHidden("action", "snooze", "id", pid) +
				`<button class="secondary small" title="Back in the waiting list after 24 hours">Snooze 24 h</button></form>`
			note = "Snooze hides it for 24 hours. " + note
		} else {
			note = "Snooze is not offered: it lasts 24 hours, and this proposal expires " + rvLeft(it.due) + ", before it would come back. " + note
		}
		reject := rvReject("/proposals/act", "p"+pid, csrf+rvHidden("action", "reject", "id", pid), "reason", "Reason (optional, remembered)", false,
			"proposal", p.S("title"))
		out.acts = `<div class="rvp-primary">` + primary + snooze + reject + `</div><p class="rvp-note">` + E(note) + `</p>` + edit
	}
	facts := [][2]string{{"Source", `<code>` + E(p.S("source_kind")) + ` ` + E(p.S("source_id")) + `</code>`}}
	if p.T("why") {
		facts = append(facts, [2]string{"Intake rule", `<span dir="auto">` + E(p.S("why")) + `</span>`})
	}
	facts = append(facts, [2]string{"Proposed", rvWhenAgo(p.I("created_at"))})
	if p.T("refreshed") {
		facts = append(facts, [2]string{"Updated", When(p.Get("updated_at")) + ` <span class="muted">(refreshed ` + I2S(p.I("refreshed")) + ` ×)</span>`})
	}
	facts = append(facts, [2]string{"Expires", When(it.due) + ` <span class="muted">unless decided; ` +
		I2S(proposalsSettings().expiryDays) + ` days after it was proposed</span>`},
		[2]string{"History", `<a href="/review?kind=proposals&amp;mode=full&amp;view=decided">Decided in the last ` + Itoa(proposalDecidedDays) +
			` days</a> · <a href="/review?kind=proposals&amp;mode=full&amp;view=snoozed">Snoozed</a>`})
	out.more = rvKV(facts, "rvp-kv")
	return out
}

// --- action approvals --------------------------------------------------------------------------------------------

// rvApprovalBlock is why the plan cannot be approved from here now ("" when it can): the same checks as the task
// page's Spin up (refused path, blocking questions not answered, the board write block).
func rvApprovalBlock(c *Ctx, it *rvItem) string {
	key := it.id
	path, label := agentSpinUp(it.plan)
	answered := map[string]bool{}
	for _, xv := range agentGetInputs(key).answers {
		x := py.AsMap(xv)
		if py.Strip(py.OrS(x.Get("a"), "")) != "" {
			answered[py.Str(x.Get("q"))] = true
		}
	}
	for _, qv := range py.AsList(py.Or(it.plan.Get("questions"), []any{})) {
		if q := py.AsMap(qv); q != nil && py.Truthy(q.Get("blocking")) && !answered[py.Str(q.Get("text"))] {
			return "Blocking questions: answer them on the task first"
		}
	}
	switch {
	case path == "refused":
		return label
	case !strings.HasPrefix(key, "gl-"):
		return BoardWriteBlock(c.Sess)
	}
	return ""
}

func rvApprovalAfter(it *rvItem) string {
	path, label := agentSpinUp(it.plan)
	var what string
	switch path {
	case "proposal":
		what = "Production stays read-only: approving starts a read-only run that writes a change proposal (command, backup, rollback, " +
			"verify) for the operator, and the task is blocked for them. Nothing changes on production."
	case "run":
		what = "A read-only run: the agent reads, and any change it makes is a merge request on an agent/* branch."
	case "fixer":
		what = label + ". The approved plan goes to the fixer, which makes the WRITE steps."
	default:
		what = "Not startable as planned: " + label + "."
	}
	return `<p>` + E(what) + ` Root re-checks the plan version, the environment and the budget before anything starts; if the plan ` +
		`changes, the new version needs its own review.</p>`
}

func rvPanelApproval(c *Ctx, it *rvItem, f rvQuery, admin bool) rvParts {
	a, plan, key := it.row, it.plan, it.id
	pv := I2S(a.I("plan_version"))
	cl := py.AsMap(py.Or(plan.Get("classification"), py.NewOMap()))
	kind := py.OrS(cl.Get("kind"), "")
	var out rvParts
	steps := py.AsList(py.Or(plan.Get("plan"), []any{}))
	writes := 0
	for _, sv := range steps {
		if s := py.AsMap(sv); s != nil && s.Get("mode") == "WRITE" {
			writes++
		}
	}
	out.facts = [][2]string{
		{"Affected", If(len(it.targets) > 0, rvMonoList(it.targets), rvMissing("No targets in the plan"))},
		{"Environment", If(it.env != "", EnvTag(it.env), rvMissing("Not set in the plan"))},
		{"Kind", If(kind != "", E(py.OrS(agentKind[kind], kind)), rvMissing("Not set in the plan"))},
		{"Plan", `v` + pv + ` <span class="muted">· ` + Itoa(len(steps)) + ` step` + If(len(steps) == 1, "", "s") + `, ` + Itoa(writes) + ` WRITE</span>`},
		{"Decision deadline", rvMissing("None: a plan waits until it is approved or changes")},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	if a.T("assignee") {
		out.facts = append(out.facts, [2]string{"For", E(a.S("assignee"))})
	}
	out.outcome = If(py.Truthy(plan.Get("summary")), `<p dir="auto">`+E(py.Str(plan.Get("summary")))+`</p>`, rvMissing("The plan has no summary."))
	var qs strings.Builder
	for _, qv := range py.AsList(py.Or(plan.Get("questions"), []any{})) {
		if q := py.AsMap(qv); q != nil {
			qs.WriteString(`<li dir="auto">` + E(py.Str(q.Get("text"))) + If(py.Truthy(q.Get("blocking")), ` <span class="badge warn">blocking</span>`,
				` <span class="muted">(optional)</span>`) + `</li>`)
		}
	}
	if qs.Len() > 0 {
		out.outcome += `<p class="rvp-sub">Questions in the plan</p><ul class="rvp-list">` + qs.String() + `</ul>`
	}
	task := `<a href="` + E(agentTaskURL(key)+"#agent") + `">Open task ` + E(key) + `</a>: its description, the conversation with Argus and the run log.`
	out.evidence = rvNoEvidenceField("Plans carry no evidence field.", task)
	out.after = rvApprovalAfter(it)
	if admin {
		block := rvApprovalBlock(c, it)
		stepHref := f.with(func(g *rvQuery) { g.step = "plan" }).href() + "#rvp-h"
		out.acts = `<div class="rvp-primary"><a class="btn" href="` + E(stepHref) + `">Review execution plan</a>` +
			`<a class="btn secondary small" href="` + E(agentTaskURL(key)+"#agent") + `">Open the task</a></div>` +
			`<p class="rvp-note">Approval happens on the next step, after the plan version, environment, targets, changes and limits.` +
			If(block != "", ` Not approvable now: `+E(block)+`.`, "") + `</p>`
	}
	out.more = rvKV([][2]string{{"Planned", rvWhenAgo(a.I("intake_at"))},
		{"Planning cost", If(a.T("intake_cost"), "$"+py.F(a.F("intake_cost"), 2), "")},
		{"Spent so far", If(a.T("spent_usd"), "$"+py.F(a.F("spent_usd"), 2), "")},
		{"Task", `<a href="` + E(agentTaskURL(key)+"#agent") + `">` + E(key) + `</a>`}}, "rvp-kv")
	return out
}

// --- lessons ---------------------------------------------------------------------------------------------------------

func rvPanelLesson(c *Ctx, it *rvItem, ret string, admin bool) rvParts {
	l := it.row
	id := l.I("id")
	var out rvParts
	review := map[string]string{"new": "New lesson", "update": "Update of lesson " + I2S(l.I("supersedes")),
		"retire?": "Back for review: unused for a long time or flagged as not helpful"}[l.S("review")]
	out.facts = [][2]string{
		{"Affected", If(len(it.targets) > 0, rvMonoList(it.targets), rvMissing("No service recorded"))},
		{"Kind", E(l.S("kind"))},
		{"Tags", E(strings.Join(lessonTags(l), " "))},
		{"Why it waits", E(review)},
		{"Decision deadline", rvMissing("None")},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	out.outcome = `<div class="rvp-text" dir="auto">` + lessonText(l.S("body")) + `</div>`
	look := ""
	if l.T("source_key") {
		look = `Learned on task ` + lessonSource(l.S("source_key")) + If(l.T("source_run"), ` (run #`+I2S(l.I("source_run"))+`)`, "") + `.`
	}
	if ev := l.S("evidence"); rvEvidenceMissing(ev) {
		out.evidence = rvNoEvidence(ev, look)
	} else {
		out.evidence = `<p dir="auto">` + E(ev) + `</p>` + If(look != "", `<p>`+look+`</p>`, "")
	}
	out.after = `<p>Later agent runs may use this lesson as guidance when they plan. Nothing runs now. An approved lesson can be ` +
		`sent back to review or retired from the full Lessons list.</p>`
	if admin {
		retire := ""
		if l.S("review") == "retire?" {
			retire = lessonButton(c.Sess, ret, "retire", id, "Retire lesson", "small danger")
		}
		out.acts = `<div class="rvp-primary">` + lessonButton(c.Sess, ret, "approve", id, "Approve lesson", "") +
			`<a class="btn secondary small" href="/review?kind=lessons&amp;mode=full#l` + I2S(id) + `">Edit, then approve</a>` + retire +
			rvFold("Reject", l.S("title"), lessonButton(c.Sess, ret, "reject", id, "Reject lesson", "small danger")) + `</div>`
	}
	out.more = rvKV([][2]string{{"Proposed", rvWhenAgo(l.I("proposed_at"))}, {"Source task", look}}, "rvp-kv")
	return out
}

// --- ideas -----------------------------------------------------------------------------------------------------------

func rvPanelIdea(c *Ctx, it *rvItem, admin bool) rvParts {
	r := it.row
	iid := it.id
	st := py.OrS(r.Get("status"), "new")
	var out rvParts
	out.facts = [][2]string{
		{"Area", E(ideaAreaLabel(r.S("area")))},
		{"From", E(If(len(r.S("persona")) <= 3, strings.ToUpper(r.S("persona")), r.S("persona")))},
		{"Effort", If(r.T("effort"), E(r.S("effort"))+` <span class="muted">`+map[string]string{"S": "under a week", "M": "one to three weeks", "L": "more"}[r.S("effort")]+`</span>`, "")},
		{"Risk", E(r.S("risk"))},
		{"Status", If(st == "review", "Under review", "New")},
		{"Decision deadline", rvMissing("None")},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	out.outcome = rvKV([][2]string{{"Proposal", E(r.S("proposal"))}, {"Problem", E(r.S("problem"))}, {"Value", E(r.S("value"))},
		{"Prerequisites", E(r.S("prerequisites"))}, {"Success metric", E(r.S("success_metric"))}, {"What changed", E(r.S("changed"))}}, "rvp-kv")
	var ev strings.Builder
	var said []string
	for _, x := range ideaEvidence(r.S("evidence")) {
		raw := py.Str(py.Or(x.Get("text"), py.Or(x.Get("link"), "")))
		text := E(raw)
		if href := py.Str(py.Or(x.Get("link"), "")); strings.HasPrefix(href, "/") {
			ev.WriteString(`<li><a href="` + E(href) + `">` + text + `</a></li>`)
		} else if !rvEvidenceMissing(raw) {
			ev.WriteString(`<li>` + text + `</li>`)
		} else if raw != "" {
			said = append(said, raw)
		}
	}
	if ev.Len() == 0 && len(said) > 0 {
		out.evidence = rvNoEvidence(strings.Join(said, "; "), `<a href="/review?kind=ideas&amp;mode=full#i-`+E(iid)+`">The idea on the full Ideas list</a>`)
	} else if ev.Len() > 0 {
		out.evidence = `<ul class="rvp-list">` + ev.String() + `</ul>`
	} else {
		out.evidence = rvNoEvidence("", `<a href="/review?kind=ideas&amp;mode=full#i-`+E(iid)+`">The idea on the full Ideas list</a>`)
	}
	out.after = `<p>Argus writes a build prompt for Claude Code from this idea. Nothing is built or deployed; the prompt appears on ` +
		`the full Ideas list, and the idea can still be marked built or rejected later.</p>`
	if admin {
		hid := rvHidden("csrf", c.Sess.CSRF, "id", iid)
		explore := ""
		if st == "new" {
			explore = `<form method="post" action="/ideas/act" class="inline">` + hid + rvHidden("action", "explore") +
				`<button class="secondary small" title="Moves it to Under review while you look into it">Mark under review</button></form>`
		}
		out.acts = `<div class="rvp-primary"><form method="post" action="/ideas/act" class="inline">` + hid + rvHidden("action", "accept") +
			`<button>Generate build prompt</button></form>` + explore +
			rvReject("/ideas/act", "i"+iid, hid+rvHidden("action", "reject"), "reason", "Reason (required, remembered)", true, "idea", r.S("title")) +
			`</div><p class="rvp-note">A reject needs a reason of at least ` + Itoa(ideaMinReason) + ` characters.</p>`
	}
	out.more = rvKV([][2]string{{"Proposed", rvWhenAgo(r.I("created_at"))}, {"Updated", rvWhenAgo(r.I("updated_at"))},
		{"Returns after", If(r.T("revives"), "rejected idea #"+I2S(r.I("revives")), "")}}, "rvp-kv")
	return out
}

// --- chat requests -----------------------------------------------------------------------------------------------

func rvPanelReply(c *Ctx, it *rvItem, ret string) rvParts {
	r := it.row
	pid := it.id
	req := py.OrS(r.Get("requester"), "unknown")
	req, _, _ = strings.Cut(req, " — ")
	req, _, _ = strings.Cut(req, " (")
	var out rvParts
	out.facts = [][2]string{
		{"Requester", E(req)},
		{"Project", `<span class="mono">` + E(r.S("project")) + `</span>`},
		{"Assignee", E(py.OrS(r.Get("assignee_name"), py.OrS(r.Get("assignee"), "Unassigned")))},
		{"Decision deadline", `Request expires ` + When(it.due) + ` <span class="warnc">(` + E(rvLeft(it.due)) + `)</span>` +
			`<span class="rvp-note">After that it no longer waits here.</span>`},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	out.outcome = `<p>Create the task “` + E(r.S("title")) + `” in ` + E(r.S("project")) + `.</p>`
	if s := strings.TrimSpace(r.S("summary")); s != "" {
		out.outcome += `<div class="rvp-text" dir="auto">` + E(s) + `</div>`
	}
	out.evidence = rvNoEvidenceField("Chat requests carry no evidence field.", "Argus keeps only the summary above; the message itself is in the Matrix chat.")
	out.after = `<p>Creates the task in ` + E(r.S("project")) + `; the requester is told in Matrix. Discarding sends nothing to the tracker.</p>`
	hid := rvHidden("csrf", c.Sess.CSRF, "pid", pid, "return", ret, "assignee", r.S("assignee"))
	out.acts = `<div class="rvp-primary"><form method="post" action="/board/mxpreview" class="inline">` + hid +
		`<button name="action" value="create">Create task</button></form>` +
		`<a class="btn secondary small" href="/review?kind=replies&amp;mode=full">Choose another assignee</a>` +
		rvFold("Discard", r.S("title"), `<form method="post" action="/board/mxpreview" class="pp-rform">`+hid+
			`<button class="small danger" name="action" value="discard">Discard request</button></form>`) + `</div>`
	out.more = rvKV([][2]string{{"Forwarded", rvWhenAgo(r.I("created"))}, {"Forwarded by", E(py.OrS(r.Get("fwd_name"), ""))}}, "rvp-kv")
	return out
}

// --- problem links -----------------------------------------------------------------------------------------------

func rvPanelLink(c *Ctx, it *rvItem, ret string, admin bool) rvParts {
	l := it.row
	lid := it.id
	item := probDescribe(l.S("ref_type"), l.S("ref_id"))
	pidS := probPID(l.I("problem_id"))
	word := py.OrS(probLinkWord[l.S("link_type")], l.S("link_type"))
	src := probSourceWord[l.S("source")]
	if l.S("source") == "human" {
		src = l.S("by")
	}
	probLink := `<a href="/problems/view?id=` + I2S(l.I("problem_id")) + `">` + pidS + If(l.T("ptitle"), " "+E(l.S("ptitle")), "") + `</a>`
	var out rvParts
	out.facts = [][2]string{
		{"Item", probItemHTML(item, true)},
		{"Problem", probLink},
		{"Item status", E(item.Status)},
		{"Item severity", E(item.Sev)},
		{"Suggested by", E(src)},
		{"Confidence", If(l.Get("confidence") != nil, py.F(l.F("confidence")*100, 0)+` %`, "")},
		{"Decision deadline", rvMissing("None")},
		{"Waiting since", rvWhenAgo(it.at)},
	}
	out.outcome = `<p>Link ` + E(probRefLabel(item.T, item.ID)) + ` to ` + pidS + ` as “` + E(word) + `”.</p>`
	look := `Compare the <a href="` + E(item.Href) + `">item</a> with ` + probLink + `.`
	if reason := l.S("reason"); rvEvidenceMissing(reason) {
		out.evidence = rvNoEvidence(reason, look)
	} else {
		out.evidence = `<p dir="auto">` + E(reason) + `</p><p>` + look + `</p>`
	}
	out.after = `<p>The item shows on the problem's page as ` + E(word) + ` ` + pidS + `. A rejected suggestion is remembered and not suggested again for a while.</p>`
	if admin && ProblemsCanAct() {
		out.acts = `<div class="rvp-primary"><form method="post" action="/problems/act" class="inline">` +
			probHidden(c.Sess, "action", "accept", "link", lid, "back", ret) + `<button>Link to ` + pidS + `</button></form>` +
			rvReject("/problems/act", "l"+lid, probHidden(c.Sess, "action", "reject", "link", lid, "back", ret), "note", "Reason (optional, remembered)", false,
				"link", probRefLabel(item.T, item.ID)+" to "+pidS) + `</div>`
	} else if admin {
		out.acts = `<p class="muted">Problem links cannot be decided from this installation yet: root has not created its request directory for problem actions.</p>`
	}
	out.more = rvKV([][2]string{{"Suggested", rvWhenAgo(l.I("at"))}, {"Link type", E(word)}}, "rvp-kv")
	return out
}

// --- shared form pieces --------------------------------------------------------------------------------------------

// rvFold is a secondary action folded behind its verb (Reject, Discard): never equal to the primary.
func rvFold(verb, plain, inner string) string {
	return `<details class="pp-rej"><summary>` + verb + `<span class="sr"> ` + E(plain) + `</span></summary>` + inner + `</details>`
}

func rvHidden(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString(`<input type="hidden" name="` + pairs[i] + `" value="` + E(pairs[i+1]) + `">`)
	}
	return b.String()
}

// rvReject is the folded Reject form every kind shares (its own handler, field and reason rule).
func rvReject(action, id, fields, reasonName, placeholder string, required bool, noun, plain string) string {
	return rvFold("Reject", plain, `<form method="post" action="`+action+`" class="pp-rform">`+fields+
		`<label class="sr" for="rvr-`+id+`">Reason</label><input id="rvr-`+id+`" name="`+reasonName+`" maxlength="300"`+
		If(required, ` minlength="`+Itoa(ideaMinReason)+`" required`, "")+` placeholder="`+placeholder+`">`+
		`<button class="small danger">Reject `+noun+`</button></form>`)
}
