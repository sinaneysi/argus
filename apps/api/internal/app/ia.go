package app

// Information architecture v2 (ADR-0016 D3, docs/architecture/argus-2/ia.md): six collapsible groups in the sidebar
// (Home, Incidents, Infrastructure, Delivery, Work, Agent) and Marketing (SEO, 2026-11-07), each listing its pages as links; Settings and Help in its
// footer. The group of the current page is open (<details open>, set by the server), the others closed. The first
// page of a group leads with what needs a person and an estimate ("Needs you"). Old URLs answer 301 to their new home
// (REDIRECTS). Server-rendered, no JavaScript.

import (
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// navLink is one page in a sidebar group: its label, its page, the other request paths that show it (Match), the
// Layout "active" paths of its detail pages (Active), whether only admins see it, and the key of its count.
type navLink struct {
	Label, Href string
	Match       []string
	Active      []string
	Admin       bool
	Count       string
}

// section is one sidebar group: the question it answers (said by "Nothing here needs you"), its icon, its links (the
// first is the landing page that leads with "Needs you") and the today_items areas whose open items are its "Needs you".
type section struct {
	Key, Name, Question, Icon string
	Links                     []navLink
	Areas                     []string
}

// SECTIONS is the sidebar, in order: six collapsible groups (operator decision 2026-10-31, ia.md) and Marketing, which
// holds the SEO page again (operator decision 2026-11-07, reverses OD-8 for SEO only); TestIANavGroups checks them.
var SECTIONS = []section{
	{Key: "home", Name: "Home", Question: "What needs me now?", Icon: "today",
		Links: []navLink{{Label: "Today", Href: "/today", Match: []string{"/"}, Count: "today"},
			{Label: "Review inbox", Href: "/review", Count: "review"}}},
	{Key: "incidents", Name: "Incidents", Question: "What is broken, and why?", Icon: "incidents",
		Links: []navLink{{Label: "Incidents", Href: "/incidents", Count: "incidents"}, {Label: "Alerts", Href: "/alerts"},
			{Label: "Problems", Href: "/problems"}, {Label: "RCA library", Href: "/rca"}},
		Areas: []string{"incidents", "problems"}},
	{Key: "infrastructure", Name: "Infrastructure", Question: "What do we run, and is it healthy?", Icon: "inventory",
		Links: []navLink{{Label: "Inventory", Href: "/inventory"}, {Label: "Capacity", Href: "/capacity"},
			{Label: "Certificates", Href: "/certs"}, {Label: "Backups", Href: "/backups"},
			{Label: "Checks", Href: "/operations", Count: "checks"}, {Label: "Alert hygiene", Href: "/hygiene"}},
		Areas: []string{"backups", "capacity", "certs", "hygiene"}},
	{Key: "delivery", Name: "Delivery", Question: "What do we ship, and is it safe?", Icon: "cicd",
		Links: []navLink{{Label: "CI/CD", Href: "/cicd", Match: []string{"/cicd/failures", "/cicd/stuck", "/cicd/audit"}},
			{Label: "Check a repo", Href: "/cicd/repo"}, {Label: "Vulnerabilities", Href: "/vulns"}},
		Areas: []string{"cicd", "vulns"}},
	{Key: "work", Name: "Work", Question: "What is being done, and by whom?", Icon: "board",
		Links: []navLink{{Label: "Tasks", Href: "/board"}, {Label: "Conversations", Href: "/qa"},
			{Label: "New task", Href: "/board/new", Match: []string{"/board/ask"}}},
		Areas: []string{"qa"}},
	{Key: "agent", Name: "Agent", Question: "What is the agent doing, and is it getting better?", Icon: "runs",
		Links: []navLink{{Label: "Runs", Href: "/runs"}, {Label: "Knowledge", Href: "/knowledge"},
			{Label: "Fix runs", Href: "/admin/fix", Admin: true}}},
	{Key: "marketing", Name: "Marketing", Question: "Can search engines crawl and rank the shop?", Icon: "capacity",
		Links: []navLink{{Label: "SEO", Href: "/seo"}}},
}

// visibleLinks are the links a role sees whose page exists (Problems only while the feature is on).
func (s *section) visibleLinks(admin bool) []navLink {
	var out []navLink
	for _, l := range s.Links {
		if l.Admin && !admin {
			continue
		}
		if l.Href == "/problems" && !ProblemsOn() {
			continue
		}
		out = append(out, l)
	}
	return out
}

// --- the sidebar ---------------------------------------------------------------------------------------------

// sideCount is the number beside a sidebar link and what it counts: only for admins and only where a person must act
// (ia.md "Counts"). 0 is no count.
func sideCount(key string, sess *Session) (int64, string) {
	if sess == nil || sess.Role != "admin" || key == "" { // a viewer reads; counts are for the people who act
		return 0, ""
	}
	switch key {
	case "today":
		if TodayOn() {
			n := todayLeft()
			return n, I2S(n) + " left today"
		}
	case "review":
		n := reviewTotal(sess)
		return n, I2S(n) + " decision" + If(n == 1, "", "s") + " waiting"
	case "checks":
		n := int64(len(failingChecks()))
		return n, I2S(n) + " check" + If(n == 1, "", "s") + " need a look"
	case "incidents":
		if r := Q("SELECT count(*) n FROM incidents WHERE status='open'"); len(r) > 0 {
			n := r[0].I("n")
			return n, I2S(n) + " open incident" + If(n == 1, "", "s")
		}
	}
	return 0, ""
}

// --- "Needs you": the landing lead of a section ------------------------------------------------------------

type need struct {
	text, href, kind string
	n                int64
}

// failingChecks are the scheduled checks whose last run is not OK or stale (the Checks count).
func failingChecks() []string {
	var bad []string
	for _, t := range TASKS {
		r := LatestRun(t[0])
		if r == nil || Now()-r.F("started_at") > float64(STALE_H*3600) {
			bad = append(bad, t[1])
			continue
		}
		st := r.Get("status")
		if t[0] == "backup-check" && st != "OK" && backupAllAccepted() {
			st = "OK"
		}
		if st != "OK" {
			bad = append(bad, t[1])
		}
	}
	return bad
}

// --- old URLs ------------------------------------------------------------------------------------------------

// redirect is one old URL: its path, an optional query condition (key=value that must hold), and the new home with the
// query parameters to add. The rest of the old query string is kept.
type redirect struct {
	From, When, To string
}

// REDIRECTS: every page IA v2 merged answers 301 to its new home (ia.md "Old URLs"); TestIARedirects checks each
// target exists.
var REDIRECTS = []redirect{
	{From: "/proposals", To: "/review?kind=proposals"},
	{From: "/ideas", To: "/review?kind=ideas"},
	{From: "/admin/lessons", To: "/review?kind=lessons"},
	{From: "/problems", When: "view=suggestions", To: "/review?kind=links"},
}

// RedirectFor is the new home of an old URL (path and raw query), or "".
func RedirectFor(path, rawQuery string) string {
	for _, r := range REDIRECTS {
		if r.From != path {
			continue
		}
		qs := py.ParseQS(rawQuery)
		drop := ""
		if r.When != "" {
			k, v, _ := strings.Cut(r.When, "=")
			if qs.Get(k) != v {
				continue
			}
			drop = k
		}
		to := r.To
		var keep [][2]string
		for _, kv := range qs.Pairs(drop) {
			if kv[0] != "kind" {
				keep = append(keep, kv)
			}
		}
		if len(keep) > 0 {
			to += If(strings.Contains(to, "?"), "&", "?") + py.Urlencode(keep)
		}
		return to
	}
	return ""
}
