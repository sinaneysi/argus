package app

// The Review inbox as JSON (GET /api/v1/review, /api/v1/review/item, POST /api/v1/review/act): the queue, filters,
// order and panel of views_review.go and views_review_panel.go, from the same item model (rvItem), filters (rvQuery),
// order (reviewSort) and panel builders; the act endpoint posts to each kind's existing handler with its fields.

import (
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

type apiReviewRow struct {
	Sel      string   `json:"sel"`
	Kind     string   `json:"kind"`
	KindRow  string   `json:"kindRow"`
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Severity string   `json:"severity,omitempty"` // the stored word (task proposals only)
	SevKey   string   `json:"sevKey"`             // critical, high, medium, none, low
	Env      string   `json:"env,omitempty"`
	EnvLabel string   `json:"envLabel,omitempty"`
	Prod     bool     `json:"prod"`
	Targets  []string `json:"targets"`
	Source   string   `json:"source,omitempty"`
	SrcLabel string   `json:"sourceLabel,omitempty"`
	At       int64    `json:"at"`
	Waiting  string   `json:"waiting"` // "waiting 3d"
	Due      int64    `json:"due,omitempty"`
	DueWord  string   `json:"dueWord,omitempty"` // "proposal expires" / "request expires"
	DueLeft  string   `json:"dueLeft,omitempty"` // "in 5h"
	DueSoon  bool     `json:"dueSoon,omitempty"`
	Summary  string   `json:"summary,omitempty"`
}

func reviewRowJSON(it *rvItem) apiReviewRow {
	r := apiReviewRow{Sel: it.sel(), Kind: it.k.Key, KindRow: it.k.Row, ID: it.id, Title: it.title, SevKey: rvSevKey(it.sevRank),
		Env: it.env, Prod: it.env == "release", Targets: append([]string{}, it.targets...), Source: it.src, At: it.at,
		Waiting: rvWait(it.at), Summary: it.summary}
	if it.k.Key == "proposals" {
		r.Severity = it.sev
	}
	if it.env != "" {
		r.EnvLabel = EnvLabel(it.env)
	}
	if it.src != "" {
		r.SrcLabel = proposalSrcLabel(it.src)
	}
	if it.due > 0 {
		r.Due, r.DueWord, r.DueLeft = it.due, rvDueWord(it), rvLeft(it.due)
		r.DueSoon = it.due-NowInt() < proposalSoonDays*86400
	}
	return r
}

// reviewFind is the waiting decision a selection names (<kind>:<id>), nil when it no longer waits or may not be seen.
func reviewFind(c *Ctx, sel string) *rvItem {
	k, _, ok := strings.Cut(sel, ":")
	kind := reviewKindOf(k)
	if !ok || !reviewVisible(kind, c.Sess) {
		return nil
	}
	for _, it := range reviewItems(c, kind, reviewPerKind) {
		if it.sel() == sel {
			return it
		}
	}
	return nil
}

// apiBannersOf turns Banner() HTML into data (the request banners after an action).
func apiBannersOf(html string) []apiBanner {
	out := []apiBanner{}
	for _, m := range regexp.MustCompile(`(?s)<div class="banner ([a-z]+)"[^>]*>(.*?)</div>`).FindAllStringSubmatch(html, -1) {
		out = append(out, apiBanner{Text: Text(m[2]), Kind: m[1]})
	}
	return out
}

type apiOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// apiReviewJSON is GET /api/v1/review: the queue page of the inbox with the same query parameters as /review.
func apiReviewJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	admin := sess.Role == "admin"
	f := reviewParse(qs, sess)
	var total, mins int64
	kinds := []map[string]any{}
	counts := map[string]int64{}
	for i := range REVIEW_KINDS {
		k := &REVIEW_KINDS[i]
		if !reviewVisible(k, sess) {
			continue
		}
		n := reviewCount(k, sess)
		counts[k.Key] = n
		total += n
		mins += n * k.Min
		kinds = append(kinds, map[string]any{"key": k.Key, "label": k.Label, "row": k.Row, "verb": k.Verb, "count": n})
	}
	all := reviewAll(c)
	var shown []*rvItem
	for _, it := range all {
		if f.match(it) {
			shown = append(shown, it)
		}
	}
	reviewSort(shown, f.sort)
	matched := int64(len(shown))
	pages := int((matched + reviewPageSize - 1) / reviewPageSize)
	if f.page > pages && pages > 0 {
		f.page = pages
	}
	rows := []apiReviewRow{}
	from := (f.page - 1) * reviewPageSize
	for i := from; i < len(shown) && i < from+reviewPageSize; i++ {
		rows = append(rows, reviewRowJSON(shown[i]))
	}
	kindTotal := total
	if f.kind != "" {
		kindTotal = counts[f.kind]
	}
	// the filter choices: severity buckets, the environments present, the proposal sources, the orders
	envSet := map[string]bool{}
	for _, it := range all {
		envSet[If(it.env == "", "none", it.env)] = true
	}
	if f.env != "" {
		envSet[f.env] = true
	}
	envKeys := make([]string, 0, len(envSet))
	for k := range envSet {
		envKeys = append(envKeys, k)
	}
	sort.Slice(envKeys, func(i, j int) bool {
		ri, rj := rvEnvRank(envKeys[i]), rvEnvRank(envKeys[j])
		return ri < rj || (ri == rj && envKeys[i] < envKeys[j])
	})
	envs := []apiOption{}
	for _, k := range envKeys {
		envs = append(envs, apiOption{k, If(k == "none", "No environment", rvEnvPhrase(k))})
	}
	sevs, srcs, sorts := []apiOption{}, []apiOption{}, []apiOption{}
	for _, s := range rvSevFilters {
		sevs = append(sevs, apiOption{s[0], s[1]})
	}
	for _, s := range proposalSrcFacets {
		srcs = append(srcs, apiOption{s[0], s[1]})
	}
	for _, s := range reviewSorts {
		sorts = append(sorts, apiOption{s.key, s.label})
	}
	out := map[string]any{
		"admin": admin, "total": total, "estMin": mins, "kinds": kinds, "kindTotal": kindTotal,
		"query": map[string]any{"kind": f.kind, "q": f.q, "sev": f.sev, "env": f.env, "src": f.src, "sort": f.sort,
			"page": max(f.page, 1), "sel": f.sel, "step": f.step},
		"filtered": f.filtered(), "matched": matched, "page": max(f.page, 1), "pages": max(pages, 1), "pageSize": reviewPageSize,
		"capped": f.kind == "" && !f.filtered() && total > int64(len(all)), "perKind": reviewPerKind,
		"options": map[string]any{"sev": sevs, "env": envs, "src": srcs, "sort": sorts},
		"order":   Text(rvOrderLine(f)), "items": rows,
		"fullMode": f.kind != "" && reviewFullMode(qs, f.kind),
		"banners":  apiBannersOf(BoardRequestBanner(c, qs) + probSentBanner(c, qs) + ideasSentBanner(c, qs)),
		"refresh":  c.Refresh != nil,
		"selected": nil, "selGone": false,
	}
	if f.sel != "" {
		if it := reviewFind(c, f.sel); it != nil {
			out["selected"] = apiReviewDetail(c, it, f)
		} else {
			out["selGone"] = true
		}
	}
	return out, nil
}

// apiReviewItemJSON is GET /api/v1/review/item?sel=<kind>:<id>[&step=plan]: one decision's panel.
func apiReviewItemJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	f := reviewParse(qs, sess)
	it := reviewFind(c, f.sel)
	if it == nil {
		return nil, &apiErr{404, "this decision is no longer waiting"}
	}
	return apiReviewDetail(c, it, f), nil
}

// --- the panel ----------------------------------------------------------------------------------------------------

type apiFact struct {
	Label   string `json:"label"`
	Value   string `json:"value"`
	Href    string `json:"href,omitempty"`
	Mono    bool   `json:"mono,omitempty"`
	Missing bool   `json:"missing,omitempty"` // the value says the field was not recorded
}

// apiSec is one part of the panel as data: prose, a list, facts, a code block, or the missing-evidence state.
type apiSec struct {
	Text      string    `json:"text,omitempty"`
	Items     []apiLink `json:"items,omitempty"`
	Facts     []apiFact `json:"facts,omitempty"`
	Code      string    `json:"code,omitempty"`
	CodeLabel string    `json:"codeLabel,omitempty"`
	Missing   bool      `json:"missing,omitempty"` // no evidence recorded (a warning)
	NoField   bool      `json:"noField,omitempty"` // the kind carries no such field (neutral)
}

type apiAction struct {
	Action         string `json:"action,omitempty"` // posted to /api/v1/review/act; "" for a link
	Href           string `json:"href,omitempty"`   // a link instead of a post
	Label          string `json:"label"`
	Style          string `json:"style"` // primary, secondary, danger
	Reason         string `json:"reason,omitempty"`
	ReasonRequired bool   `json:"reasonRequired,omitempty"`
	ReasonMin      int    `json:"reasonMin,omitempty"`
	Disabled       string `json:"disabled,omitempty"` // why it cannot be done now
	Confirm        bool   `json:"confirm,omitempty"`  // folded behind its verb (reject, discard)
}

var (
	reDTDD   = regexp.MustCompile(`(?s)<dt>(.*?)</dt><dd>(.*?)</dd>`)
	reLI     = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	rePre    = regexp.MustCompile(`(?s)<pre[^>]*><code>(.*?)</code></pre>`)
	reDetail = regexp.MustCompile(`(?s)<details[^>]*>(.*?)</details>`)
	reSum    = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	reBlock  = regexp.MustCompile(`(?s)<(dl|ul|ol)[^>]*>.*?</(dl|ul|ol)>`)
)

func apiFactsOf(html string) []apiFact {
	out := []apiFact{}
	for _, m := range reDTDD.FindAllStringSubmatch(html, -1) {
		out = append(out, apiFact{Label: Text(m[1]), Value: Text(m[2]), Href: firstHref(m[2]),
			Mono:    strings.HasPrefix(m[2], `<span class="mono">`) || strings.HasPrefix(m[2], "<code>"),
			Missing: strings.HasPrefix(m[2], `<span class="rvp-missing">`)})
	}
	return out
}

func unescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&#34;", `"`).Replace(s)
}

// apiSecOf turns one panel section's HTML into data.
func apiSecOf(html string) apiSec {
	var s apiSec
	if html == "" {
		return s
	}
	if strings.Contains(html, "rvp-noev") {
		s.NoField = strings.Contains(html, "rvp-nofield")
		s.Missing = !s.NoField
	}
	if m := rePre.FindStringSubmatch(html); m != nil {
		s.Code = unescape(m[1])
		if d := reDetail.FindStringSubmatch(html); d != nil && strings.Contains(d[1], "<pre") {
			if sm := reSum.FindStringSubmatch(d[1]); sm != nil {
				s.CodeLabel = Text(sm[1])
			}
		}
	}
	rest := reDetail.ReplaceAllString(html, "")
	rest = rePre.ReplaceAllString(rest, "")
	s.Facts = apiFactsOf(rest)
	for _, m := range reLI.FindAllStringSubmatch(rest, -1) {
		s.Items = append(s.Items, apiLink{Text: Text(m[1]), Href: firstHref(m[1])})
	}
	s.Text = Text(reBlock.ReplaceAllString(rest, " "))
	if len(s.Facts) == 0 {
		s.Facts = nil
	}
	return s
}

// apiReviewDetail is one decision's panel: the panel builders' facts and sections as data, and the actions a person
// may take here (the same choices the page offers, by name; the forms are the frontend's).
func apiReviewDetail(c *Ctx, it *rvItem, f rvQuery) map[string]any {
	admin := c.Sess.Role == "admin"
	ret := f.queue().href()
	var p rvParts
	switch it.k.Key {
	case "proposals":
		p = rvPanelProposal(c, it, ret, false)
	case "approvals":
		p = rvPanelApproval(c, it, f, false)
	case "lessons":
		p = rvPanelLesson(c, it, ret, false)
	case "ideas":
		p = rvPanelIdea(c, it, false)
	case "replies":
		p = rvPanelReply(c, it, ret)
	case "links":
		p = rvPanelLink(c, it, ret, false)
	}
	facts := []apiFact{}
	for _, kv := range p.facts {
		if kv[1] == "" {
			continue
		}
		facts = append(facts, apiFact{Label: kv[0], Value: Text(kv[1]), Href: firstHref(kv[1]),
			Mono: strings.HasPrefix(kv[1], `<span class="mono">`), Missing: strings.HasPrefix(kv[1], `<span class="rvp-missing">`)})
	}
	acts, note := apiReviewActions(c, it, f)
	out := map[string]any{
		"row": reviewRowJSON(it), "facts": facts,
		"outcome": apiSecOf(p.outcome), "evidence": apiSecOf(p.evidence), "after": apiSecOf(p.after),
		"afterTitle": If(it.k.Key == "approvals", "If you approve", "If you accept"),
		"more":       apiFactsOf(p.more), "actions": acts, "note": note, "readOnly": !admin, "step": f.step,
	}
	if it.k.Key == "approvals" {
		out["plan"] = apiPlan(c, it)
	}
	return out
}

// apiPlan is the plan review step of an action approval (rvPlanStep) as data.
func apiPlan(c *Ctx, it *rvItem) map[string]any {
	a, plan := it.row, it.plan
	path, label := agentSpinUp(plan)
	steps := []map[string]any{}
	writes := 0
	for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
		s := py.AsMap(sv)
		if s == nil {
			continue
		}
		var tg []string
		for _, x := range py.AsList(py.Or(s.Get("targets"), []any{})) {
			tg = append(tg, py.Str(x))
		}
		mode := py.OrS(s.Get("mode"), "?")
		if mode == "WRITE" {
			writes++
		}
		steps = append(steps, map[string]any{"text": py.Str(s.Get("text")), "mode": mode, "targets": append([]string{}, tg...)})
	}
	est := ""
	if e := py.AsMap(plan.Get("estimate")); e != nil && py.Truthy(e) {
		est = py.OrS(e.Get("effort"), "?") + If(e.Get("cost_usd") != nil, ", about $"+py.F(py.Num(e.Get("cost_usd")), 2), "")
	}
	limits := []string{label}
	if path == "proposal" {
		limits = append(limits, "Production: proposal only, never executed by Argus")
	}
	pv := I2S(a.I("plan_version"))
	limits = append(limits, "Pinned to plan v"+pv+": a changed plan is refused and needs its own review",
		"Root re-checks the environment and the budget before it starts")
	questions := []map[string]any{}
	for _, qv := range py.AsList(py.Or(plan.Get("questions"), []any{})) {
		if q := py.AsMap(qv); q != nil {
			questions = append(questions, map[string]any{"text": py.Str(q.Get("text")), "blocking": py.Truthy(q.Get("blocking"))})
		}
	}
	return map[string]any{"version": a.I("plan_version"), "steps": steps, "writes": writes, "estimate": est,
		"spentUsd": a.F("spent_usd"), "limits": limits, "path": path, "pathLabel": label, "questions": questions,
		"taskHref": agentTaskURL(it.id) + "#agent", "blocked": rvApprovalBlock(c, it)}
}

// apiReviewActions are the choices on the panel, in the page's order (one primary), and the note under them.
func apiReviewActions(c *Ctx, it *rvItem, f rvQuery) ([]apiAction, string) {
	if c.Sess.Role != "admin" {
		return []apiAction{}, "Read-only: admins decide."
	}
	var acts []apiAction
	note := ""
	r := it.row
	switch it.k.Key {
	case "proposals":
		noun := proposalIssueNoun(false)
		why := BoardWriteBlock(c.Sess)
		if GitLabDefaultTracker() == "gitlab" {
			why = GitLabWriteBlock()
		}
		acts = append(acts, apiAction{Action: "accept", Label: "Create " + noun, Style: "primary", Disabled: why},
			apiAction{Href: "/review?kind=proposals&mode=full#p" + it.id, Label: "Edit, then create", Style: "secondary"})
		note = "Expiry keeps running while a proposal is snoozed: snoozing never extends it."
		if it.due-NowInt() > 86400 {
			acts = append(acts, apiAction{Action: "snooze", Label: "Snooze 24 h", Style: "secondary"})
			note = "Snooze hides it for 24 hours. " + note
		} else {
			note = "Snooze is not offered: it lasts 24 hours, and this proposal expires " + rvLeft(it.due) + ", before it would come back. " + note
		}
		acts = append(acts, apiAction{Action: "reject", Label: "Reject proposal", Style: "danger", Reason: "Reason (optional, remembered)", Confirm: true})
	case "approvals":
		block := rvApprovalBlock(c, it)
		if f.step == "plan" {
			_, label := agentSpinUp(it.plan)
			acts = append(acts, apiAction{Action: "approve", Label: "Approve plan v" + I2S(r.I("plan_version")) + " & start", Style: "primary", Disabled: block},
				apiAction{Href: f.with(func(g *rvQuery) { g.step = "" }).href(), Label: "Back to the summary", Style: "secondary"})
			note = py.OrS(block, label)
		} else {
			acts = append(acts, apiAction{Href: f.with(func(g *rvQuery) { g.step = "plan" }).href(), Label: "Review execution plan", Style: "primary"},
				apiAction{Href: agentTaskURL(it.id) + "#agent", Label: "Open the task", Style: "secondary"})
			note = "Approval happens on the next step, after the plan version, environment, targets, changes and limits." +
				If(block != "", " Not approvable now: "+block+".", "")
		}
	case "lessons":
		acts = append(acts, apiAction{Action: "approve", Label: "Approve lesson", Style: "primary"},
			apiAction{Href: "/review?kind=lessons&mode=full#l" + it.id, Label: "Edit, then approve", Style: "secondary"})
		if r.S("review") == "retire?" {
			acts = append(acts, apiAction{Action: "retire", Label: "Retire lesson", Style: "danger"})
		}
		acts = append(acts, apiAction{Action: "reject", Label: "Reject lesson", Style: "danger", Confirm: true})
	case "ideas":
		acts = append(acts, apiAction{Action: "accept", Label: "Generate build prompt", Style: "primary"})
		if py.OrS(r.Get("status"), "new") == "new" {
			acts = append(acts, apiAction{Action: "explore", Label: "Mark under review", Style: "secondary"})
		}
		acts = append(acts, apiAction{Action: "reject", Label: "Reject idea", Style: "danger", Reason: "Reason (required, remembered)",
			ReasonRequired: true, ReasonMin: ideaMinReason, Confirm: true})
		note = "A reject needs a reason of at least " + Itoa(ideaMinReason) + " characters."
	case "replies":
		acts = append(acts, apiAction{Action: "create", Label: "Create task", Style: "primary"},
			apiAction{Href: "/review?kind=replies&mode=full", Label: "Choose another assignee", Style: "secondary"},
			apiAction{Action: "discard", Label: "Discard request", Style: "danger", Confirm: true})
	case "links":
		if !ProblemsCanAct() {
			return []apiAction{}, "Problem links cannot be decided from this installation yet: root has not created its request directory for problem actions."
		}
		acts = append(acts, apiAction{Action: "accept", Label: "Link to " + probPID(r.I("problem_id")), Style: "primary"},
			apiAction{Action: "reject", Label: "Reject link", Style: "danger", Reason: "Reason (optional, remembered)", Confirm: true})
	}
	return acts, note
}

// --- acting -------------------------------------------------------------------------------------------------------

// apiReviewRoute is the handler key of a decision kind (the handlers switch on it).
func apiReviewRoute(kind string) string {
	return map[string]string{"proposals": "/proposals/act", "approvals": "/board/agent/spinup", "lessons": "/admin/lessons/act",
		"ideas": "/ideas/act", "replies": "/board/mxpreview", "links": "/problems/act"}[kind]
}

// apiReviewPolicyRoute is the write whose POST_ROUTES policy a decision kind takes.
func apiReviewPolicyRoute(kind string) string {
	return map[string]string{"proposals": "/api/v1/proposals/act", "approvals": "/api/v1/board/agent/spinup", "lessons": "/api/v1/lessons/act",
		"ideas": "/api/v1/ideas/act", "replies": "/api/v1/board/mxpreview", "links": "/api/v1/problems/act"}[kind]
}

var apiReviewVerbs = map[string][]string{"proposals": {"accept", "snooze", "reject"}, "approvals": {"approve"},
	"lessons": {"approve", "reject", "retire"}, "ideas": {"accept", "explore", "reject"}, "replies": {"create", "discard"},
	"links": {"accept", "reject"}}

// apiReviewAct is POST /api/v1/review/act {sel, action, reason?, plan_version?, return?}: the kind's own handler with
// the fields its panel form posts, after the policy of that handler's route (2FA class, admins only).
func (h *reqCtx) apiReviewAct(body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/review"}
	sel, act := bodyStr(body, "sel"), bodyStr(body, "action")
	kind, id, _ := strings.Cut(sel, ":")
	route := apiReviewRoute(kind)
	if route == "" || !rvIDRe.MatchString(id) {
		return nil, &apiErr{400, "sel is <kind>:<id> of a waiting decision"}
	}
	if !In(act, apiReviewVerbs[kind]...) {
		return nil, &apiErr{400, "action is one of " + strings.Join(apiReviewVerbs[kind], ", ")}
	}
	if sess.Role != "admin" {
		return nil, &apiErr{403, "admins only"}
	}
	if !h.apiMFA(apiReviewPolicyRoute(kind), sess) { // the kind's own class (lessons and plan approvals are sensitive)
		return nil, nil
	}
	it := reviewFind(c, sel)
	if it == nil {
		return nil, &apiErr{404, "this decision is no longer waiting"}
	}
	ret := SafeNext(py.OrS(bodyStr(body, "return"), "/review"))
	reason := bodyStr(body, "reason")
	switch kind {
	case "proposals":
		return apiResult(ProposalsPost(c, route, py.NewQS("action", act, "id", id, "reason", reason, "return", ret), sess))
	case "approvals":
		pv := bodyStr(body, "plan_version")
		if pv != I2S(it.row.I("plan_version")) {
			return nil, &apiErr{409, "the plan changed: review the new version"}
		}
		return apiResult(BoardPost(c, route, py.NewQS("key", id, "plan_version", pv, "return", ret), sess))
	case "lessons":
		return apiResult(LessonsPost(c, route, py.NewQS("action", act, "id", id, "return", ret), sess))
	case "ideas":
		return apiResult(IdeasPostAct(c, py.NewQS("action", act, "id", id, "reason", reason), sess))
	case "replies":
		return apiResult(BoardPost(c, route, py.NewQS("action", act, "pid", id, "return", ret, "assignee", it.row.S("assignee")), sess))
	default: // links
		code, out := ProblemsPostAct(c, py.NewQS("action", act, "link", id, "note", reason, "back", ret), sess)
		return apiResult(code, out)
	}
}
