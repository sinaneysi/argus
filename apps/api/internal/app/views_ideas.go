package app

// Port of app/views_ideas.py: Ideas (/ideas), the advisers' proposals (bin/ideas.py writes ops.db ideas and
// ideas-status.json). Admins explore, accept, reject (reason required) and mark built through a request file
// (settings-in/idea-actions, applied by argus) and start an adviser with "Generate now" (settings-in/requests/
// ideas-<persona>.req, root's run-requests: once a day). Every value is escaped; no JavaScript.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var ideaStatusKind = map[string]string{"new": "", "review": "warn", "accepted": "ok", "rejected": "", "built": "ok"}
var ideaStatusWord = map[string]string{"new": "new", "review": "under review", "accepted": "accepted", "rejected": "rejected",
	"built": "built"}
var ideaViews = []string{"open", "new", "review", "accepted", "rejected", "built", "all"}
var ideaAreas = [][2]string{{"ci-cd", "CI/CD"}, {"monitoring", "Monitoring"}, {"qa", "QA"}, {"security", "Security"},
	{"backup", "Backup"}, {"capacity", "Capacity"}, {"ux", "UX"}, {"other", "Other"}}
var ideaEfforts = []string{"S", "M", "L"}
var ideaActions = []string{"explore", "accept", "reject", "built"}

var (
	ideaPersonaRe = regexp.MustCompile(`^[a-z]{2,12}$`)
	ideaRidRe     = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}$`)
	ideaRefRe     = regexp.MustCompile(`^(?:[0-9a-f]{7,40}|![0-9]{1,7}|https://[A-Za-z0-9.-]{3,80}/[A-Za-z0-9_./~%+-]{1,200}` +
		`(?:/-/(?:merge_requests|commit)/[0-9a-f]{1,40})?)$`)
)

const (
	ideaMaxReason = 300
	ideaMinReason = 3

	ideaOrder = "CASE status WHEN 'new' THEN 0 WHEN 'review' THEN 1 WHEN 'accepted' THEN 2 WHEN 'built' THEN 3 ELSE 4 END, " +
		"created_at DESC, id DESC"
)

type ideaPersona struct {
	key, name, role string
	days            []string
}

func ideasStatus() *py.OMap { return ReadJSONMap(filepath.Join(filepath.Dir(DB), "ideas-status.json")) }

func ideasPersonas(st *py.OMap) []ideaPersona {
	var out []ideaPersona
	ps := st.Map("personas")
	if ps == nil {
		return nil
	}
	for _, k := range ps.Keys() {
		v := ps.Map(k)
		if !ideaPersonaRe.MatchString(k) || v == nil {
			continue
		}
		var days []string
		if dl, ok := v.Get("days").([]any); ok {
			for _, d := range dl {
				if s, ok := d.(string); ok {
					days = append(days, s)
				}
			}
			if len(days) > 7 {
				days = days[:7]
			}
		}
		out = append(out, ideaPersona{k, py.Str(py.Or(v.Get("name"), k)), py.Str(py.Or(v.Get("role"), "")), days})
	}
	return out
}

func ideaAreaLabel(a string) string {
	for _, x := range ideaAreas {
		if x[0] == a {
			return x[1]
		}
	}
	if a == "" {
		return "other"
	}
	return a
}

func ideaInitial(name string) string {
	s := py.Strip(name)
	if s == "" {
		return "?"
	}
	return strings.ToUpper(py.Head(s, 1))
}

func ideaEvidence(raw string) []*py.OMap {
	if raw == "" {
		raw = "[]"
	}
	v, err := py.ParseJSON([]byte(raw))
	if err != nil {
		return nil
	}
	var out []*py.OMap
	for _, x := range py.AsList(v) {
		if m := py.AsMap(x); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func ideaRefHTML(ref string) string {
	if strings.HasPrefix(ref, "https://") {
		return `<a href="` + E(ref) + `" rel="noreferrer">` + E(ref) + `</a>`
	}
	return `<span class="mono">` + E(py.OrS(ref, "-")) + `</span>`
}

// --- the queue state (DESIGN.md "Queue page"): status tab, search, adviser / area / effort facets, sort, page -----

var ideaTabs = [][2]string{{"open", "Open"}, {"new", "New"}, {"review", "Under review"}, {"accepted", "Accepted"},
	{"built", "Built"}, {"rejected", "Rejected"}, {"all", "All"}}

var ideaSorts = []queueSortOpt{
	{"priority", "Priority", ideaOrder, "New first, then under review, accepted, built and rejected; newest first in each"},
	{"newest", "Newest", "created_at DESC, id DESC", "Most recently proposed first"},
	{"effort", "Smallest effort", "CASE effort WHEN 'S' THEN 0 WHEN 'M' THEN 1 WHEN 'L' THEN 2 ELSE 3 END, " + ideaOrder,
		"Effort S first (the quick wins), then M and L"},
}

var ideaSearchCols = []string{"title", "problem", "proposal", "value", "risk", "prerequisites", "success_metric", "evidence", "changed"}

const ideaMaxQ = 100

// ideaQuery is the page's GET state; status, persona, area and effort keep their old names, so old links still work.
type ideaQuery struct {
	status, persona, area, effort, q, sort string
	words                                  []string
	page                                   int
}

func ideaParse(qs py.QS, names map[string]bool) ideaQuery {
	f := ideaQuery{status: qs.Get("status"), persona: qs.Get("persona"), area: qs.Get("area"), effort: qs.Get("effort"),
		page: queuePageNum(qs.Get("page"))}
	if !In(f.status, ideaViews...) {
		f.status = "open"
	}
	if !names[f.persona] {
		f.persona = ""
	}
	if ideaAreaLabel(f.area) == f.area {
		f.area = ""
	}
	if !In(f.effort, ideaEfforts...) {
		f.effort = ""
	}
	f.q, f.words = queueWords(qs.Get("q"), ideaMaxQ, 6)
	f.sort = queueSortKey(ideaSorts, qs.Get("sort")).key
	return f
}

func (f ideaQuery) filtered() bool {
	return f.q != "" || f.persona != "" || f.area != "" || f.effort != ""
}

func (f ideaQuery) pairs() [][2]string {
	var p [][2]string
	add := func(k, v string, skip bool) {
		if v != "" && !skip {
			p = append(p, [2]string{k, v})
		}
	}
	add("status", f.status, f.status == "open")
	add("q", f.q, false)
	add("persona", f.persona, false)
	add("area", f.area, false)
	add("effort", f.effort, false)
	add("sort", f.sort, f.sort == "priority")
	add("page", strconv.Itoa(f.page), f.page <= 1)
	return p
}

func (f ideaQuery) href() string {
	if p := f.pairs(); len(p) > 0 {
		return "/ideas?" + py.Urlencode(p)
	}
	return "/ideas"
}

// with returns a copy changed by mod, back on page 1 unless mod sets the page.
func (f ideaQuery) with(mod func(*ideaQuery)) ideaQuery {
	g := f
	g.page = 1
	mod(&g)
	return g
}

// where is the SQL condition of the state; skip names the dimension left out (for its own tab or chip counts).
func (f ideaQuery) where(skip string) (string, []any) {
	conds, args := queueLike(f.words, ideaSearchCols)
	if skip != "status" {
		if f.status == "open" {
			conds = append(conds, "status IN ('new','review','accepted')")
		} else if f.status != "all" {
			conds, args = append(conds, "status=?"), append(args, f.status)
		}
	}
	for _, kv := range [][2]string{{"persona", f.persona}, {"area", f.area}, {"effort", f.effort}} {
		if kv[1] != "" && skip != kv[0] {
			conds, args = append(conds, kv[0]+"=?"), append(args, kv[1])
		}
	}
	if len(conds) == 0 {
		return "1=1", nil
	}
	return strings.Join(conds, " AND "), args
}

func (f ideaQuery) counts(skip, col string) map[string]int64 {
	w, a := f.where(skip)
	out := map[string]int64{}
	for _, r := range Q("SELECT coalesce("+col+",'') k, count(*) n FROM ideas WHERE "+w+" GROUP BY k", a...) {
		out[r.Str("k")] = r.I("n")
	}
	return out
}

// --- the page ------------------------------------------------------------------------------------------------

type ideaName struct{ name, role string }

func ideasSentBanner(c *Ctx, qs py.QS) string {
	sent, rid := qs.Get("sent"), qs.Get("req")
	if sent == "generate" {
		return Banner("Requested. The adviser starts within a minute (once a day each); new ideas appear here " +
			"when the run ends, in about five minutes.")
	}
	if !In(sent, ideaActions...) {
		return ""
	}
	if ideaRidRe.MatchString(rid) {
		if _, err := os.Stat(filepath.Join(SET_IN, "idea-actions", rid+".json")); err == nil {
			c.Refresh = true
			return Banner("Sent. Argus applies it within seconds; this page refreshes itself.")
		}
	}
	return Banner(map[string]string{"explore": "Marked under review.", "accept": "Accepted. The build prompt for Claude Code is on the " +
		"idea's card.", "reject": "Rejected; the reason is remembered.", "built": "Marked built."}[sent])
}

// --- downloads and posts -------------------------------------------------------------------------------------

// IdeasDownload is views_ideas.download(qs): (filename, markdown) or ok=false (404).
func IdeasDownload(qs py.QS) (string, string, bool) {
	v := qs.Get("id")
	if !py.IsDigit(v) || len([]rune(v)) > 9 {
		return "", "", false
	}
	r := Q("SELECT id, build_prompt FROM ideas WHERE id=?", py.I(v))
	if len(r) == 0 || !r[0].T("build_prompt") {
		return "", "", false
	}
	return "argus-idea-" + I2S(r[0].I("id")) + "-build-prompt.md", r[0].S("build_prompt"), true
}

// IdeasPostAct is views_ideas.post_act(form, sess).
func IdeasPostAct(c *Ctx, form py.QS, sess *Session) (int, string) {
	get := form.Get
	if sess.Role != "admin" {
		return 403, "Only admins decide ideas."
	}
	act, iid := get("action"), get("id")
	if !In(act, ideaActions...) || !py.IsDigit(iid) || len([]rune(iid)) > 9 || py.I(iid) <= 0 {
		return 400, "Choose an idea and an action."
	}
	reason := strings.Join(py.Split(get("reason")), " ")
	ref := py.Strip(get("ref"))
	if act == "reject" && !(ideaMinReason <= py.Len(reason) && py.Len(reason) <= ideaMaxReason) {
		return 400, "A reject needs a reason (" + Itoa(ideaMinReason) + " to " + Itoa(ideaMaxReason) + " characters)."
	}
	if act == "built" && !ideaRefRe.MatchString(ref) {
		return 400, "Mark built needs a commit hash, !MR number or an https link."
	}
	rid := time.Now().Format("20060102150405") + "-" + auth.TokenHex(4)
	d := filepath.Join(SET_IN, "idea-actions")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	n := py.I(iid)
	body := py.Dumps(py.NewOMap("id", n, "action", act, "by", sess.Username, "role", sess.Role,
		"reason", If(act == "reject", reason, ""), "ref", If(act == "built", ref, ""), "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(body)); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	logf("idea %s #%d requested by %s: %s", act, n, sess.Username, rid)
	return 303, "/review?kind=ideas&sent=" + act + "&req=" + rid + "#i-" + I2S(n)
}

// IdeasPostGenerate is views_ideas.post_generate(form, sess).
func IdeasPostGenerate(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Only admins start an adviser."
	}
	p := form.Get("persona")
	known := false
	for _, x := range ideasPersonas(ideasStatus()) {
		if x.key == p {
			known = true
		}
	}
	if !ideaPersonaRe.MatchString(p) || !known {
		return 400, "Unknown adviser."
	}
	d := filepath.Join(SET_IN, "requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, "ideas-"+p+".req"), filepath.Join(d, ".ideas-"+p+".tmp"), []byte(body)); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	logf("ideas %s requested by %s", p, sess.Username)
	return 303, "/review?kind=ideas&sent=generate"
}
