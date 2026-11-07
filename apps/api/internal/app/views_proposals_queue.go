package app

// The Proposed tasks queue (DESIGN.md "Queue page"): search, facet chips, sort and pagination as GET parameters run
// in SQL on board.db, a contextual bulk bar (CSS :has(), no JavaScript) that opens a server-rendered confirm step, and
// the confirm step itself. Presentation and read-only queries only: every decision still goes through the unchanged
// POST /proposals/act (same fields, same CSRF, same request file) and bin/proposals.py applies it.

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	proposalPageSize = queuePageSize // rows per page; the bulk cap (proposalMaxIDs, 50) stays above it
	proposalMaxQ     = 100           // search box, characters
	proposalMaxWords = 6
	proposalSoonDays = 2 // "expires soon": within 48 h of the automatic expiry
)

// severity facets: the stored sev_rank (bin/proposals.py SEV), worst first
var proposalSevFacets = []struct {
	key, label string
	rank       int64
}{{"critical", "Critical", 4}, {"high", "High", 3}, {"medium", "Medium", 2}, {"low", "Low", 1}, {"none", "Info or unrated", 0}}

// source facets: the proposal's path (bin/proposals.py PATHS), short labels for chips and rows
var proposalSrcFacets = [][2]string{{"qa", "QA"}, {"page", "Incident"}, {"cve", "CVE"}, {"cicd", "CI/CD"}, {"cred", "Credential"},
	{"chat", "Task chat"}}

var proposalSorts = []struct{ key, label, order, title string }{
	{"priority", "Priority", "coalesce(sev_rank,0) DESC, updated_at DESC, id DESC", "Worst severity first, then most recently updated"},
	{"newest", "Newest", "created_at DESC, id DESC", "Most recently proposed first"},
	{"expiring", "Expires first", "created_at ASC, id ASC", "Oldest first: the next to expire automatically"},
	{"updated", "Recently updated", "updated_at DESC, id DESC", "Most recently refreshed by its source first"},
}

var proposalSearchCols = []string{"title", "source_kind", "source_id", "host", "why", "labels", "assignee_suggestion", "env"}

func proposalSrcLabel(path string) string {
	for _, s := range proposalSrcFacets {
		if s[0] == path {
			return s[1]
		}
	}
	return path
}

// proposalQuery is the queue's GET state: view, search words, severity set, source, env, sort, page.
type proposalQuery struct {
	view, q, src, env, sort, out string
	words, sev                   []string
	page                         int
}

func proposalParse(qs py.QS) proposalQuery {
	f := proposalQuery{view: qs.GetD("view", "pending"), sort: qs.Get("sort"), page: 1}
	if !In(f.view, "pending", "snoozed", "decided") {
		f.view = "pending"
	}
	q := strings.Join(strings.Fields(qs.Get("q")), " ")
	if utf8.RuneCountInString(q) > proposalMaxQ {
		q = string([]rune(q)[:proposalMaxQ])
	}
	f.q = q
	for _, w := range strings.Fields(q) {
		if len(f.words) < proposalMaxWords {
			f.words = append(f.words, w)
		}
	}
	want := map[string]bool{}
	for _, s := range qs.List("sev") {
		want[s] = true
	}
	for _, s := range proposalSevFacets {
		if want[s.key] {
			f.sev = append(f.sev, s.key)
		}
	}
	if src := qs.Get("src"); proposalSrcLabel(src) != src {
		f.src = src
	}
	if env := qs.Get("env"); env == "none" || (env != "" && proposalLabelRe.MatchString(env) && len(env) <= 20) {
		f.env = env
	}
	ok := false
	for _, s := range proposalSorts {
		ok = ok || s.key == f.sort
	}
	if !ok {
		f.sort = "priority"
	}
	if n, err := strconv.Atoi(qs.Get("page")); err == nil && n > 1 && n < 100000 {
		f.page = n
	}
	if f.view == "decided" && In(qs.Get("out"), "accepted", "rejected", "expired") {
		f.out = qs.Get("out") // Decided only: the outcome facet
	}
	return f
}

func (f proposalQuery) filtered() bool {
	return f.q != "" || len(f.sev) > 0 || f.src != "" || f.env != "" || f.out != ""
}

// pairs is the query string of this state (defaults left out), in a stable order.
func (f proposalQuery) pairs() [][2]string {
	var p [][2]string
	if f.view != "pending" {
		p = append(p, [2]string{"view", f.view})
	}
	if f.q != "" {
		p = append(p, [2]string{"q", f.q})
	}
	for _, s := range f.sev {
		p = append(p, [2]string{"sev", s})
	}
	if f.src != "" {
		p = append(p, [2]string{"src", f.src})
	}
	if f.env != "" {
		p = append(p, [2]string{"env", f.env})
	}
	if f.out != "" {
		p = append(p, [2]string{"out", f.out})
	}
	if f.sort != "priority" && f.view != "decided" {
		p = append(p, [2]string{"sort", f.sort})
	}
	if f.page > 1 {
		p = append(p, [2]string{"page", strconv.Itoa(f.page)})
	}
	return p
}

func (f proposalQuery) href() string {
	if p := f.pairs(); len(p) > 0 {
		return "/proposals?" + py.Urlencode(p)
	}
	return "/proposals"
}

// with returns a copy changed by mod; a filter change goes back to page 1 unless mod sets the page.
func (f proposalQuery) with(mod func(*proposalQuery)) proposalQuery {
	g := f
	g.sev = append([]string(nil), f.sev...)
	g.page = 1
	mod(&g)
	return g
}

// hidden carries the state through a GET form (the search box keeps the chips, the bulk bar keeps everything).
func (f proposalQuery) hidden(skip ...string) string {
	var b strings.Builder
	for _, p := range f.pairs() {
		if !In(p[0], skip...) {
			b.WriteString(`<input type="hidden" name="` + p[0] + `" value="` + E(p[1]) + `">`)
		}
	}
	return b.String()
}

func likeEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// where is the SQL condition of the state; skip names the facet left out (for that facet's own counts).
func (f proposalQuery) where(skip string) (string, []any) {
	conds, args := []string{"state=?"}, []any{f.view}
	if f.view == "decided" { // the Decided tab: the last proposalDecidedDays days, any outcome unless the facet sets one
		conds, args = []string{"state IN ('accepted','rejected','expired') AND decided_at>=?"}, []any{NowInt() - proposalDecidedDays*86400}
		if skip != "out" && f.out != "" {
			conds, args = append(conds, "state=?"), append(args, f.out)
		}
	}
	for _, w := range f.words {
		var or []string
		for _, col := range proposalSearchCols {
			or = append(or, "coalesce("+col+",'') LIKE ? ESCAPE '\\'")
			args = append(args, "%"+likeEsc(w)+"%")
		}
		conds = append(conds, "("+strings.Join(or, " OR ")+")")
	}
	if skip != "sev" && len(f.sev) > 0 {
		var qm []string
		for _, s := range proposalSevFacets {
			if In(s.key, f.sev...) {
				qm = append(qm, "?")
				args = append(args, s.rank)
			}
		}
		conds = append(conds, "coalesce(sev_rank,0) IN ("+strings.Join(qm, ",")+")")
	}
	if skip != "src" && f.src != "" {
		conds, args = append(conds, "path=?"), append(args, f.src)
	}
	if skip != "env" && f.env != "" {
		if f.env == "none" {
			conds = append(conds, "coalesce(env,'')=''")
		} else {
			conds, args = append(conds, "env=?"), append(args, f.env)
		}
	}
	return strings.Join(conds, " AND "), args
}

func (f proposalQuery) facetCounts(skip, expr string) map[string]int64 {
	w, a := f.where(skip)
	out := map[string]int64{}
	for _, r := range BoardBQ("SELECT "+expr+" k, count(*) n FROM proposals WHERE "+w+" GROUP BY k", a...) {
		out[py.Str(r.Get("k"))] = r.I("n")
	}
	return out
}

func proposalSevIdx(k string) int {
	for i, s := range proposalSevFacets {
		if s.key == k {
			return i
		}
	}
	return 99
}

// proposalIssueNoun names what an accept creates, from the tracker it goes to by default: "Jira issue",
// "GitLab issue", or "task" on a local board (no Jira in use, no GitLab).
func proposalIssueNoun(plural bool) string {
	n := "task"
	if GitLabDefaultTracker() == "gitlab" {
		n = "GitLab issue"
	} else if jiraInUse() || GitLabConnected() {
		n = "Jira issue"
	}
	return n + If(plural, "s", "")
}

// proposalExpiresIn: "expires in 3d" from created_at + expiry_days (bin/proposals.py tick(): snoozing does not reset it).
func proposalExpiresIn(p Row, expiryDays int64) string {
	at := p.I("created_at") + expiryDays*86400
	left := at - NowInt()
	txt := "expires at the next sync"
	switch {
	case left >= 86400:
		txt = "expires in " + I2S(left/86400) + "d"
	case left >= 3600:
		txt = "expires in " + I2S(left/3600) + "h"
	case left > 0:
		txt = "expires in " + I2S(left/60+1) + "m"
	}
	cls := If(left < proposalSoonDays*86400, ` class="warnc"`, "")
	return `<span` + cls + ` title="Undecided proposals expire ` + I2S(expiryDays) + ` days after they were proposed: ` + When(at) + `">` + txt + `</span>`
}
