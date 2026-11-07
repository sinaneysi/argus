package app

// Problems and its Suggestions view as queue pages (DESIGN.md "Queue page"): view tabs with counts, search, facets,
// sort and 25-row pagination as GET parameters (filtered in memory: the severity words of problems map several stored
// values, probSevMatch), the contextual bulk bar on Suggestions (CSS :has(), no JavaScript) and its server-rendered
// confirm step. Presentation and read-only queries only: decisions still go through the unchanged POST /problems/act
// (csrf, action, link, note, back) and bin/problems.py applies them.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const probMaxQ = 100

var probStageFacets = [][2]string{{"new", "New"}, {"investigating", "Investigating"}, {"known_error", "Known error"},
	{"fix_in_progress", "Fix in progress"}}
var probSevFacets = [][2]string{{"critical", "Critical"}, {"high", "High"}, {"medium", "Medium"}, {"low", "Low"}}
var probEnvFacets = [][2]string{{"release", "release"}, {"pre-release", "pre-release"}, {"develop", "develop"}}

var probSorts = []queueSortOpt{
	{"worst", "Worst first", "", "Open before resolved, then worst severity, customer-facing, most recently updated"},
	{"updated", "Recently updated", "", "Most recently changed first"},
	{"oldest", "Longest open", "", "Earliest first seen first"},
	{"linked", "Most linked", "", "Most accepted incidents, QA issues and findings first"},
}

// probQuery is the Problems list's GET state; status, sev and env keep their names and values, so old links work.
type probQuery struct {
	status, sev, env, q, sort string
	words                     []string
	page                      int
}

func probParse(qs py.QS) probQuery {
	s := probSelOf(qs)
	f := probQuery{status: s.status, sev: s.sev, env: s.env, page: queuePageNum(qs.Get("page"))}
	f.q, f.words = queueWords(qs.Get("q"), probMaxQ, 6)
	f.sort = queueSortKey(probSorts, qs.Get("sort")).key
	return f
}

// tab: the view tab a status belongs to (the four open stages are the Open tab with a Stage chip).
func (f probQuery) tab() string {
	if In(f.status, probOpen...) {
		return "open"
	}
	return f.status
}

func (f probQuery) filtered() bool {
	return f.q != "" || f.sev != "" || f.env != "" || In(f.status, probOpen...)
}

func (f probQuery) pairs() [][2]string {
	var p [][2]string
	add := func(k, v string, skip bool) {
		if v != "" && !skip {
			p = append(p, [2]string{k, v})
		}
	}
	add("status", f.status, f.status == "open")
	add("q", f.q, false)
	add("sev", f.sev, false)
	add("env", f.env, false)
	add("sort", f.sort, f.sort == "worst")
	add("page", strconv.Itoa(f.page), f.page <= 1)
	return p
}

func (f probQuery) href() string {
	if p := f.pairs(); len(p) > 0 {
		return "/problems?" + py.Urlencode(p)
	}
	return "/problems"
}

func (f probQuery) with(mod func(*probQuery)) probQuery {
	g := f
	g.page = 1
	mod(&g)
	return g
}

// match: does row r pass the state, leaving out one dimension (skip: "status", "stage", "sev", "env")?
func (f probQuery) match(r Row, skip string) bool {
	st := r.S("status")
	switch {
	case skip == "status":
	case skip == "stage" && f.tab() == "open":
		if !In(st, probOpen...) {
			return false
		}
	case f.status == "open":
		if !In(st, probOpen...) {
			return false
		}
	case f.status != "all" && st != f.status:
		return false
	}
	if skip != "sev" && !probSevMatch(r.S("severity"), f.sev) {
		return false
	}
	if skip != "env" && f.env != "" && r.S("env") != f.env {
		return false
	}
	if len(f.words) > 0 {
		hay := strings.ToLower(strings.Join([]string{probPID(r.I("id")), r.S("title"), r.S("root_cause"), r.S("owner"),
			r.S("workaround")}, " "))
		for _, w := range f.words {
			if !strings.Contains(hay, strings.ToLower(w)) {
				return false
			}
		}
	}
	return true
}

func probLinkedTotal(m map[string]int64) int64 {
	var n int64
	for k, v := range m {
		if k != "rca" {
			n += v
		}
	}
	return n
}

func probSortRows(rows []Row, key string, counts map[int64]map[string]int64) {
	probSortWorst(rows)
	switch key {
	case "updated":
		sort.SliceStable(rows, func(a, b int) bool { return rows[a].I("updated_at") > rows[b].I("updated_at") })
	case "oldest":
		seen := func(r Row) int64 {
			if r.T("first_seen") {
				return r.I("first_seen")
			}
			return r.I("created_at")
		}
		sort.SliceStable(rows, func(a, b int) bool { return seen(rows[a]) < seen(rows[b]) })
	case "linked":
		sort.SliceStable(rows, func(a, b int) bool {
			return probLinkedTotal(counts[rows[a].I("id")]) > probLinkedTotal(counts[rows[b].I("id")])
		})
	}
}

// --- Suggestions --------------------------------------------------------------------------------------------------

var probConfBands = [][3]string{{"high", "High ≥ 90 %", "confidence ≥ 90 %"}, {"medium", "Medium 75–89 %", "75 – 89 %"},
	{"low", "Low < 75 %", "below 75 %"}}

var probSugSorts = []queueSortOpt{
	{"confidence", "Confidence", "", "Most confident first, then newest"},
	{"newest", "Newest", "", "Most recently suggested first"},
	{"problem", "By problem", "", "Grouped by problem, newest problem first"},
}

func probBand(cf float64) string {
	switch {
	case cf >= 0.9:
		return "high"
	case cf >= 0.75:
		return "medium"
	}
	return "low"
}

// probSugQuery is the Suggestions view's GET state; p (one problem) keeps its name.
type probSugQuery struct {
	p, conf, typ, q, sort string
	words                 []string
	page                  int
}

func probSugParse(qs py.QS) probSugQuery {
	f := probSugQuery{p: qs.Get("p"), conf: qs.Get("conf"), typ: qs.Get("type"), page: queuePageNum(qs.Get("page"))}
	if !(py.IsDigit(f.p) && len(f.p) <= 9) {
		f.p = ""
	}
	if !In(f.conf, "high", "medium", "low") {
		f.conf = ""
	}
	if _, ok := probTypeWord[f.typ]; !ok {
		f.typ = ""
	}
	f.q, f.words = queueWords(qs.Get("q"), probMaxQ, 6)
	f.sort = queueSortKey(probSugSorts, qs.Get("sort")).key
	return f
}

func (f probSugQuery) filtered() bool { return f.q != "" || f.conf != "" || f.typ != "" }

func (f probSugQuery) pairs() [][2]string {
	p := [][2]string{{"view", "suggestions"}}
	add := func(k, v string, skip bool) {
		if v != "" && !skip {
			p = append(p, [2]string{k, v})
		}
	}
	add("p", f.p, false)
	add("q", f.q, false)
	add("conf", f.conf, false)
	add("type", f.typ, false)
	add("sort", f.sort, f.sort == "confidence")
	add("page", strconv.Itoa(f.page), f.page <= 1)
	return p
}

func (f probSugQuery) href() string { return "/problems?" + py.Urlencode(f.pairs()) }

func (f probSugQuery) with(mod func(*probSugQuery)) probSugQuery {
	g := f
	g.page = 1
	mod(&g)
	return g
}

func (f probSugQuery) match(l Row, skip string) bool {
	if skip != "conf" && f.conf != "" && probBand(l.F("confidence")) != f.conf {
		return false
	}
	if skip != "type" && f.typ != "" && l.S("ref_type") != f.typ {
		return false
	}
	if len(f.words) > 0 {
		hay := strings.ToLower(strings.Join([]string{probPID(l.I("problem_id")), l.S("ptitle"), probTypeWord[l.S("ref_type")],
			l.S("ref_id"), l.S("reason"), probLinkWord[l.S("link_type")]}, " "))
		for _, w := range f.words {
			if !strings.Contains(hay, strings.ToLower(w)) {
				return false
			}
		}
	}
	return true
}

func probSugSortRows(rows []Row, key string) {
	switch key {
	case "newest":
		sort.SliceStable(rows, func(a, b int) bool { return rows[a].I("at") > rows[b].I("at") })
	case "problem":
		sort.SliceStable(rows, func(a, b int) bool { return rows[a].I("problem_id") > rows[b].I("problem_id") })
	}
}
