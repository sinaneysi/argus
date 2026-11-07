package app

// CI/CD failures (/cicd/failures) as a queue page (DESIGN.md "Queue page"): Open / Resolved tabs with counts, search,
// environment and class facets, sort and 25-row pagination as GET parameters in SQL on ops.db cicd_findings (kind
// failure or deploy; bin/cicd_triage.py writes them). Read-only: the one action is "Create task…", the board's prefill
// form; Argus never retries, cancels or plays a pipeline.

import (
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const cicdMaxQ = 100

var cicdFailSorts = []queueSortOpt{
	{"priority", "Priority", cicdEnvRank + ", last_seen DESC, id DESC", "release first, then pre-release and develop; most recently failed first"},
	{"recent", "Most recent", "last_seen DESC, id DESC", "Most recently failed first"},
	{"frequent", "Most frequent", "coalesce(occurrences,1) DESC, last_seen DESC, id DESC", "Most occurrences first"},
}

var cicdFailSearchCols = []string{"title", "project", "detail", "job_name", "author", "mr", "ref", "sha", "rule"}

// cicdFailQuery is the page's GET state; env and class keep their names, so old links still work.
type cicdFailQuery struct {
	view, env, class, q, sort string
	words                     []string
	page                      int
}

func cicdFailParse(qs py.QS) cicdFailQuery {
	f := cicdFailQuery{view: qs.Get("view"), env: qs.Get("env"), class: qs.Get("class"), page: queuePageNum(qs.Get("page"))}
	if f.view != "resolved" {
		f.view = "open"
	}
	if !In(f.env, cicdEnvs...) && f.env != "none" {
		f.env = ""
	}
	if _, ok := cicdClassLbl[f.class]; !ok {
		f.class = ""
	}
	f.q, f.words = queueWords(qs.Get("q"), cicdMaxQ, 6)
	f.sort = queueSortKey(cicdFailSorts, qs.Get("sort")).key
	return f
}

func (f cicdFailQuery) filtered() bool { return f.q != "" || f.env != "" || f.class != "" }

func (f cicdFailQuery) pairs() [][2]string {
	var p [][2]string
	add := func(k, v string, skip bool) {
		if v != "" && !skip {
			p = append(p, [2]string{k, v})
		}
	}
	add("view", f.view, f.view == "open")
	add("q", f.q, false)
	add("env", f.env, false)
	add("class", f.class, false)
	add("sort", f.sort, f.sort == "priority")
	add("page", strconv.Itoa(f.page), f.page <= 1)
	return p
}

func (f cicdFailQuery) href() string {
	if p := f.pairs(); len(p) > 0 {
		return "/cicd/failures?" + py.Urlencode(p)
	}
	return "/cicd/failures"
}

func (f cicdFailQuery) with(mod func(*cicdFailQuery)) cicdFailQuery {
	g := f
	g.page = 1
	mod(&g)
	return g
}

// where: kind failure or deploy, the tab (unless skip is "view"), the search words and the facets (but skip).
func (f cicdFailQuery) where(skip string) (string, []any) {
	conds, args := queueLike(f.words, cicdFailSearchCols)
	conds = append([]string{"kind IN ('failure','deploy')"}, conds...)
	if skip != "view" {
		conds = append(conds, If(f.view == "open", "status='open'", "status!='open'"))
	}
	if skip != "env" && f.env != "" {
		if f.env == "none" {
			conds = append(conds, "coalesce(env,'')=''")
		} else {
			conds, args = append(conds, "env=?"), append(args, f.env)
		}
	}
	if skip != "class" && f.class != "" {
		conds, args = append(conds, "klass=?"), append(args, f.class)
	}
	return strings.Join(conds, " AND "), args
}

func (f cicdFailQuery) counts(skip, expr string) map[string]int64 {
	w, a := f.where(skip)
	out := map[string]int64{}
	for _, r := range Q("SELECT "+expr+" k, count(*) n FROM cicd_findings WHERE "+w+" GROUP BY k", a...) {
		out[r.Str("k")] = r.I("n")
	}
	return out
}
