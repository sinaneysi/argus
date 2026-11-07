package app

// Review (/review, IA v2, ADR-0016 D3; redesigned 2026-11-02, docs/architecture/argus-2/ia.md "Review"): every
// decision a person makes, in one inbox. The page is a queue beside a review panel. The queue is every waiting
// decision of the kinds the role may see, filtered (search, kind, severity, environment, source) and sorted (urgency
// by default) in Go from stored fields only, 25 a page. Selecting a row is a link (?sel=<kind>:<id>): the server renders
// the same queue, page and filters with the item's review panel beside it (desktop) or full width with a "Back to
// queue" link (narrow screens). Every view is a shareable URL; refresh and back/forward work without JavaScript.
//
// Every act form in the panel posts to the kind's existing handler with the same fields, the session CSRF token and
// the MFA policy (POST_ROUTES); root re-checks every request file. An action approval never posts from the panel: its
// primary action opens the plan review step (?step=plan), which shows the pinned plan version and posts it. A kind's
// old queue page (bulk with its confirm step, history tabs, settings) stays one link away (?mode=full), embedded
// without its frame. Admins decide; viewers read the kinds they may see (ideas, problem links) without act forms.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// reviewKind is one decision kind: its filter chip, the row label, the label of its primary action, who sees it and
// the rule-of-thumb minutes one decision takes (ia.md "Estimates").
type reviewKind struct {
	Key, Label, Row, Verb string
	Admin                 bool
	Min                   int64
}

// REVIEW_KINDS, in chip order. Knowledge promotion (ADR-0016 D8) joins when the eval harness exists (ia.md).
var REVIEW_KINDS = []reviewKind{
	{"proposals", "Task proposals", "Task proposal", "Create issue", true, 2},
	{"approvals", "Action approvals", "Action approval", "Review execution plan", true, 5},
	{"lessons", "Lessons", "Lesson", "Approve lesson", true, 2},
	{"ideas", "Ideas", "Idea", "Generate build prompt", false, 3},
	{"replies", "Chat requests", "Chat request", "Create task", true, 1},
	{"links", "Problem links", "Problem link", "Link to problem", false, 1},
}

const (
	reviewPerKind  = 200 // most waiting rows read per kind
	reviewPageSize = queuePageSize
	reviewMaxQ     = 100
)

var (
	rvIDRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	rvAnchorRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

func reviewKindOf(key string) *reviewKind {
	for i := range REVIEW_KINDS {
		if REVIEW_KINDS[i].Key == key {
			return &REVIEW_KINDS[i]
		}
	}
	return nil
}

func reviewKindIdx(key string) int {
	for i := range REVIEW_KINDS {
		if REVIEW_KINDS[i].Key == key {
			return i
		}
	}
	return len(REVIEW_KINDS)
}

// reviewVisible: the kind exists here (its feature is on) and the role may see it.
func reviewVisible(k *reviewKind, sess *Session) bool {
	if k == nil || sess == nil || (k.Admin && sess.Role != "admin") {
		return false
	}
	if k.Key == "links" && !ProblemsOn() {
		return false
	}
	return true
}

// --- rows per kind (read-only queries) ----------------------------------------------------------------------------

func reviewProposals(limit int) []Row {
	return BoardBQ("SELECT * FROM proposals WHERE state='pending' ORDER BY sev_rank DESC, created_at LIMIT ?", limit)
}

// reviewApprovals are the agent tasks whose plan is ready and waits for a person to approve exactly its version.
func reviewApprovals() []Row {
	return BoardBQ("SELECT * FROM agent_tasks WHERE state='ready' AND coalesce(plan,'') NOT IN ('','null') ORDER BY intake_at")
}

func reviewLessons(limit int) []Row {
	return BoardBQ("SELECT * FROM lessons WHERE state='proposed' ORDER BY proposed_at LIMIT ?", limit)
}

func reviewIdeas(limit int) []Row {
	return Q("SELECT * FROM ideas WHERE status IN ('new','review') ORDER BY status='review', created_at DESC LIMIT ?", limit)
}

func reviewLinks(limit int) (out []Row) {
	defer func() { recover() }() // the problems tables exist only once the feature ran
	return Q("SELECT l.*, p.title ptitle FROM problem_links l LEFT JOIN problems p ON p.id=l.problem_id WHERE l.state='suggested' "+
		"ORDER BY l.confidence DESC, l.at LIMIT ?", limit)
}

// reviewCount is how many decisions of a kind wait (0 when the role may not see it).
func reviewCount(k *reviewKind, sess *Session) int64 {
	if !reviewVisible(k, sess) {
		return 0
	}
	one := func(rows []Row) int64 {
		if len(rows) == 0 {
			return 0
		}
		return rows[0].I("n")
	}
	switch k.Key {
	case "proposals":
		return ProposalsPendingCount()
	case "approvals":
		return int64(len(reviewApprovals()))
	case "lessons":
		return one(BoardBQ("SELECT count(*) n FROM lessons WHERE state='proposed'"))
	case "ideas":
		return one(Q("SELECT count(*) n FROM ideas WHERE status IN ('new','review')"))
	case "replies":
		return int64(len(matrixPending(sess.Username)))
	case "links":
		if r := probQ1("SELECT count(*) n FROM problem_links WHERE state='suggested'"); r != nil {
			return r.I("n")
		}
	}
	return 0
}

// reviewTotal is every decision the role can make (the sidebar count, admins only).
func reviewTotal(sess *Session) int64 {
	var n int64
	for i := range REVIEW_KINDS {
		n += reviewCount(&REVIEW_KINDS[i], sess)
	}
	return n
}

// --- the item model: one waiting decision, normalised from its kind's stored fields ------------------------------

// rvItem is one waiting decision. Every field comes from a stored column or plan field; nothing is scored.
type rvItem struct {
	k       *reviewKind
	id      string   // the kind's own id: proposal id, agent key, lesson id, idea id, preview pid, link id
	title   string   // plain text
	sev     string   // the stored severity word, "" when the kind carries none
	sevRank int      // urgency bucket (rvSevRank): critical 4, high 3, medium 2, unknown 1, low 0
	env     string   // environment key (develop / pre-release / release / ...), "" when none is stored
	targets []string // affected hosts or services, as stored
	src     string   // proposal path (the Source filter); "" for the other kinds
	at      int64    // waiting since
	due     int64    // decision deadline: proposal expiry or chat-request expiry; 0 when the kind has none
	summary string   // one line from a stored field that adds to the title; "" otherwise
	row     Row
	plan    *py.OMap // action approvals: the parsed plan
}

func (it *rvItem) sel() string { return it.k.Key + ":" + it.id }
func (it *rvItem) anchor() string {
	return "rv-" + it.k.Key + "-" + rvAnchorRe.ReplaceAllString(it.id, "-")
}

// rvSevRank is the urgency bucket of a severity word. An unknown severity (unrated, or a kind that carries none) is
// not assumed to be low: it sits between medium and low (ia.md "Review order").
func rvSevRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical", "page":
		return 4
	case "high":
		return 3
	case "medium", "warning":
		return 2
	case "low", "info":
		return 0
	}
	return 1
}

// rvSevKey is the Severity filter value of a bucket.
func rvSevKey(rank int) string {
	return map[int]string{4: "critical", 3: "high", 2: "medium", 1: "none", 0: "low"}[rank]
}

var rvSevFilters = [][2]string{{"critical", "Critical"}, {"high", "High"}, {"medium", "Medium"}, {"low", "Low or info"},
	{"none", "No severity"}}

// rvEnvRank: production (release) first, then staging (pre-release), develop, any other stored name, none.
func rvEnvRank(env string) int {
	switch env {
	case "release":
		return 0
	case "pre-release":
		return 1
	case "develop":
		return 2
	case "", "none":
		return 4
	}
	return 3
}

// rvEnvPhrase names an environment key for sentences and the filter: its ENV_LABELS label, or for the two keys whose
// meaning is fixed (release is production, pre-release is staging: agentSpinUp, EnvTag) the key with that meaning.
func rvEnvPhrase(k string) string {
	if l := EnvLabel(k); l != k {
		return l + " (" + k + ")"
	}
	if m := map[string]string{"release": "production", "pre-release": "staging"}[k]; m != "" {
		return k + " (" + m + ")"
	}
	return k
}

// --- ordering -------------------------------------------------------------------------------------------------------

// reviewSorts: the queue's orders. Urgency is the default and explained on the page; the others are explicit.
var reviewSorts = []queueSortOpt{
	{"urgency", "Urgency", "", "Severity, then production first, then the nearest decision deadline, then the longest waiting"},
	{"oldest", "Oldest first", "", "The longest waiting first"},
	{"newest", "Newest first", "", "The most recent first"},
	{"expiring", "Expiring first", "", "The nearest decision deadline first (proposal or chat-request expiry); then by urgency"},
}

func rvOrd(less bool) int {
	if less {
		return -1
	}
	return 1
}

// rvUrgencyCmp orders two items by urgency: severity bucket (worst first), environment (production first), decision
// deadline (soonest first; none last), waiting time (longest first), then kind order and id so the order is total.
func rvUrgencyCmp(a, b *rvItem) int {
	if a.sevRank != b.sevRank {
		return rvOrd(a.sevRank > b.sevRank)
	}
	if ea, eb := rvEnvRank(a.env), rvEnvRank(b.env); ea != eb {
		return rvOrd(ea < eb)
	}
	if c := rvDueCmp(a, b); c != 0 {
		return c
	}
	if a.at != b.at {
		return rvOrd(a.at < b.at)
	}
	return rvTieCmp(a, b)
}

func rvDueCmp(a, b *rvItem) int {
	switch {
	case a.due == b.due:
		return 0
	case a.due == 0:
		return 1
	case b.due == 0:
		return -1
	}
	return rvOrd(a.due < b.due)
}

func rvTieCmp(a, b *rvItem) int {
	if ka, kb := reviewKindIdx(a.k.Key), reviewKindIdx(b.k.Key); ka != kb {
		return rvOrd(ka < kb)
	}
	if na, ea := strconv.ParseInt(a.id, 10, 64); ea == nil {
		if nb, eb := strconv.ParseInt(b.id, 10, 64); eb == nil && na != nb {
			return rvOrd(na < nb)
		}
	}
	return strings.Compare(a.id, b.id)
}

// reviewSort sorts items in place by the named order (an unknown name is urgency).
func reviewSort(items []*rvItem, key string) {
	cmp := rvUrgencyCmp
	switch key {
	case "oldest":
		cmp = func(a, b *rvItem) int {
			if a.at != b.at {
				return rvOrd(a.at < b.at)
			}
			return rvUrgencyCmp(a, b)
		}
	case "newest":
		cmp = func(a, b *rvItem) int {
			if a.at != b.at {
				return rvOrd(a.at > b.at)
			}
			return rvUrgencyCmp(a, b)
		}
	case "expiring":
		cmp = func(a, b *rvItem) int {
			if c := rvDueCmp(a, b); c != 0 {
				return c
			}
			return rvUrgencyCmp(a, b)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return cmp(items[i], items[j]) < 0 })
}

// --- loading ----------------------------------------------------------------------------------------------------

// rvSameText: the second text only repeats the first (a description that is the title plus a full stop).
func rvSameText(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.Join(strings.Fields(s), " "), ". "))
	}
	return norm(a) == norm(b)
}

func rvPlan(a Row) (p *py.OMap) {
	defer func() {
		if recover() != nil {
			p = py.NewOMap()
		}
	}()
	if p = py.AsMap(agentJSON(a.Get("plan"), "null")); p != nil {
		return p
	}
	return py.NewOMap()
}

// rvPlanTargets is every target of the plan's steps, in order, once each.
func rvPlanTargets(plan *py.OMap) []string {
	seen := map[string]bool{}
	var out []string
	for _, sv := range py.AsList(py.Or(plan.Get("plan"), []any{})) {
		s := py.AsMap(sv)
		if s == nil {
			continue
		}
		for _, x := range py.AsList(py.Or(s.Get("targets"), []any{})) {
			if v := py.Str(x); v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func rvPlanEnv(plan *py.OMap) string {
	env := py.OrS(py.AsMap(py.Or(plan.Get("classification"), py.NewOMap())).Get("environment"), "")
	return If(env == "none", "", env)
}

// rvTaskTitle is the board task title of an agent key ("" when the task is not on the board).
func rvTaskTitle(key string) string {
	var r []Row
	if strings.HasPrefix(key, "gl-") {
		r = BoardBQ("SELECT title FROM tasks WHERE id=?", py.I(key[3:]))
	} else {
		r = BoardBQ("SELECT title FROM tasks WHERE jira_key=?", key)
	}
	if len(r) == 0 {
		return ""
	}
	return r[0].S("title")
}

// reviewItems loads the waiting decisions of one kind (at most limit) as items.
func reviewItems(c *Ctx, k *reviewKind, limit int) []*rvItem {
	var out []*rvItem
	add := func(it *rvItem) {
		if len(out) < limit {
			it.k = k
			out = append(out, it)
		}
	}
	switch k.Key {
	case "proposals":
		exp := proposalsSettings().expiryDays
		for _, p := range reviewProposals(limit) {
			sev := py.OrS(p.Get("severity"), "")
			it := &rvItem{id: I2S(p.I("id")), title: p.S("title"), sev: sev, env: p.S("env"), src: p.S("path"),
				at: p.I("created_at"), due: p.I("created_at") + exp*86400, row: p}
			it.sevRank = rvSevRank(sev)
			if sev == "" && p.I("sev_rank") > 0 { // a rank without a word: the stored rank decides
				it.sevRank = map[int64]int{4: 4, 3: 3, 2: 2, 1: 0}[p.I("sev_rank")]
			}
			if p.T("host") {
				it.targets = []string{p.S("host")}
			}
			if d := FirstLine(p.Get("description"), 200); d != "" && !rvSameText(d, it.title) {
				it.summary = d
			}
			add(it)
		}
	case "approvals":
		for _, a := range reviewApprovals() {
			plan := rvPlan(a)
			key := a.S("key")
			goal := FirstLine(py.OrS(plan.Get("summary"), py.OrS(plan.Get("goal"), "")), 200)
			title := rvTaskTitle(key)
			it := &rvItem{id: key, sevRank: 1, env: rvPlanEnv(plan), targets: rvPlanTargets(plan), at: a.I("intake_at"), row: a, plan: plan}
			if title == "" {
				title, goal = py.OrS(goal, key), ""
			}
			it.title, it.summary = key+" · "+title, goal
			add(it)
		}
	case "lessons":
		for _, l := range reviewLessons(limit) {
			it := &rvItem{id: I2S(l.I("id")), title: l.S("title"), sevRank: 1, env: l.S("env"), at: l.I("proposed_at"),
				summary: FirstLine(l.Get("body"), 200), row: l}
			if l.T("service") {
				it.targets = []string{l.S("service")}
			}
			add(it)
		}
	case "ideas":
		for _, r := range reviewIdeas(limit) {
			add(&rvItem{id: I2S(r.I("id")), title: r.S("title"), sevRank: 1, at: r.I("created_at"), summary: FirstLine(r.Get("problem"), 200), row: r})
		}
	case "replies":
		for _, r := range matrixPending(c.Sess.Username) {
			sum := strings.Join(py.Split(strings.ReplaceAll(r.S("summary"), "\n", " · ")), " ")
			add(&rvItem{id: r.S("pid"), title: r.S("title"), sevRank: 1, at: r.I("created"), due: r.I("created") + matrixPreviewTTL,
				summary: FirstLine(sum, 200), row: r})
		}
	case "links":
		for _, l := range reviewLinks(limit) {
			it := probDescribe(l.S("ref_type"), l.S("ref_id"))
			label := probRefLabel(it.T, it.ID)
			if it.Exists && it.Title != "" {
				label += ": " + it.Title
			}
			add(&rvItem{id: I2S(l.I("id")), title: label, sev: it.Sev, sevRank: rvSevRank(it.Sev), env: it.Env, at: l.I("at"), row: l,
				summary: py.OrS(probLinkWord[l.S("link_type")], l.S("link_type")) + " " + probPID(l.I("problem_id")) +
					If(l.T("ptitle"), ": "+l.S("ptitle"), "")})
		}
	}
	return out
}

// reviewAll is every waiting decision the role may see (each kind capped at reviewPerKind).
func reviewAll(c *Ctx) []*rvItem {
	var out []*rvItem
	for i := range REVIEW_KINDS {
		k := &REVIEW_KINDS[i]
		if reviewVisible(k, c.Sess) {
			out = append(out, reviewItems(c, k, reviewPerKind)...)
		}
	}
	return out
}

// --- the URL state ------------------------------------------------------------------------------------------------

// rvQuery is the page's GET state: kind, search, severity, environment, source, sort, page, the selected item
// (<kind>:<id>) and the plan review step of a selected action approval.
type rvQuery struct {
	kind, q, sev, env, src, sort, sel, step string
	words                                   []string
	page                                    int
}

func reviewParse(qs py.QS, sess *Session) rvQuery {
	f := rvQuery{kind: qs.Get("kind"), page: queuePageNum(qs.Get("page"))}
	if f.kind != "" && !reviewVisible(reviewKindOf(f.kind), sess) {
		f.kind = ""
	}
	f.q, f.words = queueWords(qs.Get("q"), reviewMaxQ, 6)
	for _, s := range rvSevFilters {
		if qs.Get("sev") == s[0] {
			f.sev = s[0]
		}
	}
	if env := qs.Get("env"); env == "none" || (env != "" && len(env) <= 20 && proposalLabelRe.MatchString(env)) {
		f.env = env
	}
	if src := qs.Get("src"); proposalSrcLabel(src) != src {
		f.src = src
	}
	f.sort = queueSortKey(reviewSorts, qs.Get("sort")).key
	if k, id, ok := strings.Cut(qs.Get("sel"), ":"); ok && reviewVisible(reviewKindOf(k), sess) && rvIDRe.MatchString(id) {
		f.sel = k + ":" + id
		if qs.Get("step") == "plan" && k == "approvals" {
			f.step = "plan"
		}
	}
	return f
}

func (f rvQuery) filtered() bool { return f.q != "" || f.sev != "" || f.env != "" || f.src != "" }

func (f rvQuery) pairs() [][2]string {
	var p [][2]string
	add := func(k, v string, skip bool) {
		if v != "" && !skip {
			p = append(p, [2]string{k, v})
		}
	}
	add("kind", f.kind, false)
	add("q", f.q, false)
	add("sev", f.sev, false)
	add("env", f.env, false)
	add("src", f.src, false)
	add("sort", f.sort, f.sort == "urgency")
	add("page", strconv.Itoa(f.page), f.page <= 1)
	add("sel", f.sel, false)
	add("step", f.step, false)
	return p
}

func (f rvQuery) href() string {
	if p := f.pairs(); len(p) > 0 {
		return "/review?" + strings.ReplaceAll(py.Urlencode(p), "%3A", ":") // sel=<kind>:<id> stays readable
	}
	return "/review"
}

// with returns a copy changed by mod.
func (f rvQuery) with(mod func(*rvQuery)) rvQuery {
	g := f
	mod(&g)
	return g
}

// queue is this state without the selection: the address of the queue the panel came from (and returns to).
func (f rvQuery) queue() rvQuery { return f.with(func(g *rvQuery) { g.sel, g.step = "", "" }) }

func (f rvQuery) match(it *rvItem) bool {
	if f.kind != "" && it.k.Key != f.kind {
		return false
	}
	if f.sev != "" && rvSevKey(it.sevRank) != f.sev {
		return false
	}
	if f.env != "" && If(it.env == "", "none", it.env) != f.env {
		return false
	}
	if f.src != "" && it.src != f.src {
		return false
	}
	if len(f.words) > 0 {
		hay := strings.ToLower(strings.Join(append([]string{it.title, it.summary, it.id, it.k.Row, it.sev, it.env, EnvLabel(it.env),
			proposalSrcLabel(it.src)}, it.targets...), " "))
		for _, w := range f.words {
			if !strings.Contains(hay, strings.ToLower(w)) {
				return false
			}
		}
	}
	return true
}

// --- the queue ------------------------------------------------------------------------------------------------------

// rvLeft is a short "in 5h" / "in 3d" for a deadline.
func rvLeft(at int64) string {
	left := at - NowInt()
	switch {
	case left >= 86400:
		return "in " + I2S(left/86400) + "d"
	case left >= 3600:
		return "in " + I2S(left/3600) + "h"
	case left > 0:
		return "in " + I2S(left/60+1) + "m"
	}
	return "at the next sync"
}

// rvWait is "waiting 3d" for the row.
func rvWait(at int64) string {
	s := NowInt() - at
	switch {
	case at <= 0:
		return "waiting since an unknown time"
	case s >= 86400:
		return "waiting " + I2S(s/86400) + "d"
	case s >= 3600:
		return "waiting " + I2S(s/3600) + "h"
	case s >= 60:
		return "waiting " + I2S(s/60) + "m"
	}
	return "just in"
}

// rvDueWord names the deadline: a proposal's expiry or a chat request's.
func rvDueWord(it *rvItem) string {
	return If(it.k.Key == "replies", "request expires", "proposal expires")
}

// rvOrderLine says, in one line, how the queue is ordered (and which filters only some kinds carry).
func rvOrderLine(f rvQuery) string {
	line := map[string]string{
		"urgency": "Sorted by urgency: severity (an unknown severity above low), then " + rvEnvPhrase("release") + " before " +
			rvEnvPhrase("pre-release") + " and " + rvEnvPhrase("develop") + ", then the nearest decision deadline, then the longest waiting.",
		"oldest":   "Sorted by waiting time: the longest waiting first.",
		"newest":   "Sorted by arrival: the most recent first.",
		"expiring": "Sorted by decision deadline: the proposal or chat request that expires first; items without one follow, by urgency.",
	}[f.sort]
	var notes []string
	if f.src != "" {
		notes = append(notes, "Source applies to task proposals only, so the other kinds are hidden.")
	}
	if f.sev != "" && f.sev != "none" {
		notes = append(notes, "Only task proposals carry a severity.")
	}
	return `<p class="rv-order">` + E(line) + If(len(notes) > 0, ` `+E(strings.Join(notes, " ")), "") +
		` <a href="/help/review#order">How Review orders decisions</a></p>`
}

// --- embedded kind queues (?mode=full) -----------------------------------------------------------------------------

// reviewFullMode: the request asks for a kind's own full list: mode=full, or a parameter only the old page knows (old
// links and bookmarks: /proposals?view=decided, /ideas?status=built, the proposals bulk confirm step, ...).
func reviewFullMode(qs py.QS, kind string) bool {
	if kind == "" || kind == "approvals" {
		return false
	}
	if qs.Get("mode") == "full" || qs.Get("sel") == "page" {
		return true
	}
	for _, k := range []string{"view", "confirm", "status", "persona", "area", "effort", "out", "p", "review", "all"} {
		if qs.Get(k) != "" {
			return true
		}
	}
	return false
}

// --- the page -------------------------------------------------------------------------------------------------------
