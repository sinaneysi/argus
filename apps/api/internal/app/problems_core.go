package app

// Problem records (HLD docs/architecture/HLD-problems.md, ADR-0008): shared helpers. bin/problems.py (argus) is the
// only writer of problems, problem_links and problem_events; the app reads them and writes one request file per
// control into settings-in/problem-actions/ (root creates the directory on deploy; without it no control shows).
// Go-only (ADR-0005): the Python app is the frozen rollback image. With the feature off nothing here renders.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var probStatuses = []string{"new", "investigating", "known_error", "fix_in_progress", "resolved", "closed"}
var probOpen = []string{"new", "investigating", "known_error", "fix_in_progress"}
var probWord = map[string]string{"new": "new", "investigating": "investigating", "known_error": "known error",
	"fix_in_progress": "fix in progress", "resolved": "resolved", "closed": "closed"}
var probKind = map[string]string{"new": "", "investigating": "warn", "known_error": "warn", "fix_in_progress": "warn",
	"resolved": "ok", "closed": ""}

// probHumanMoves mirrors HUMAN_MOVES in bin/problems.py (the authority).
var probHumanMoves = map[string][]string{
	"new":             {"investigating", "known_error", "fix_in_progress", "resolved", "closed"},
	"investigating":   {"known_error", "fix_in_progress", "resolved", "closed"},
	"known_error":     {"investigating", "fix_in_progress", "resolved", "closed"},
	"fix_in_progress": {"investigating", "known_error", "resolved", "closed"},
	"resolved":        {"investigating", "closed"},
	"closed":          {"investigating"},
}

var probIssueTypes = []string{"incident", "qa_issue", "cve_group", "cicd_finding", "capacity", "vm_report", "finding"}

const probNum = `^[0-9]{1,9}$`

// probRefRe and probLinkTypes mirror REF_TYPES in bin/problems.py.
var probRefRe = map[string]*regexp.Regexp{
	"incident": regexp.MustCompile(probNum), "qa_issue": regexp.MustCompile(probNum),
	"cve_group": cveGroupRe, // the stored group_key (<ecosystem>|<component>|<class>); HLD 4.3's pattern lacked '|'

	"cicd_finding": regexp.MustCompile(probNum),
	"capacity":     regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}\|/[A-Za-z0-9._/-]{0,120}$`),
	"vm_report":    regexp.MustCompile(probNum), "finding": regexp.MustCompile(`^[0-9]{1,9}:[0-9]{1,3}$`),
	"rca": regexp.MustCompile(probNum), "proposal": regexp.MustCompile(probNum),
	"task":    regexp.MustCompile(`^(?:[0-9]{1,9}|[A-Z][A-Z0-9]{1,9}-[0-9]{1,7})$`),
	"fix_run": regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}$`), "deploy": regexp.MustCompile(probNum),
	"mr": regexp.MustCompile(`^[0-9]{1,9}![0-9]{1,7}$`), "problem": regexp.MustCompile(probNum),
}

var probLinkTypes = map[string][]string{
	"incident": {"caused_by", "related_to"}, "qa_issue": {"caused_by", "related_to"},
	"cve_group": {"caused_by", "related_to"}, "cicd_finding": {"caused_by", "related_to"},
	"capacity": {"caused_by", "related_to"}, "vm_report": {"caused_by", "related_to"},
	"finding": {"caused_by", "related_to"}, "rca": {"explains", "related_to"},
	"proposal": {"fixed_by"}, "task": {"fixed_by"}, "fix_run": {"fixed_by"}, "deploy": {"fixed_by"}, "mr": {"fixed_by"},
	"problem": {"duplicate_of", "blocks", "related_to"},
}

var probLinkWord = map[string]string{"caused_by": "caused by", "explains": "explains", "fixed_by": "fixed by",
	"related_to": "related to", "duplicate_of": "duplicate of", "blocks": "blocks"}

var probTypeWord = map[string]string{"incident": "incident", "qa_issue": "QA issue", "cve_group": "CVE group",
	"cicd_finding": "CI/CD finding", "capacity": "capacity", "vm_report": "VM analysis", "finding": "finding",
	"rca": "RCA", "proposal": "proposal", "task": "task", "fix_run": "fix run", "deploy": "deploy", "mr": "MR",
	"problem": "problem"}

var probSourceWord = map[string]string{"human": "person", "argus": "Argus", "model": "model", "backfill": "backfill"}

func problemsState() *py.OMap { return ReadJSONMap(filepath.Join(filepath.Dir(DB), "problems.json")) }

// ProblemsOn: FEATURE_PROBLEMS=1 in the app's environment or "enabled" in problems.json, and the table exists.
func ProblemsOn() bool {
	if env("FEATURE_PROBLEMS", "0") != "1" && problemsState().Get("enabled") != true {
		return false
	}
	return len(Q("SELECT 1 FROM sqlite_master WHERE type='table' AND name='problems'")) > 0
}

// ProblemsCanAct: the request directory exists (root creates it; the app never does).
func ProblemsCanAct() bool {
	st, err := os.Stat(filepath.Join(SET_IN, "problem-actions"))
	return err == nil && st.IsDir()
}

func probStatusBadge(s string) string {
	k := probKind[s]
	cls := "badge"
	if k != "" {
		cls += " " + k
	}
	w := probWord[s]
	if w == "" {
		w = s
	}
	return `<span class="` + cls + `">` + E(w) + `</span>`
}

func probLabelTag(l string) string {
	switch l {
	case "proven":
		return `<span class="badge ok" title="the root cause is proven by evidence">proven</span>`
	case "probable", "unproven":
		return `<span class="badge warn">` + l + `</span>`
	}
	return ""
}

func probPID(id int64) string { return "P-" + I2S(id) }

// probParseRef splits "type:id" (cve_group keys may contain ':', split once).
func probParseRef(ref string) (string, string, bool) {
	t, id, ok := strings.Cut(ref, ":")
	if !ok || len(ref) > 330 {
		return "", "", false
	}
	re, known := probRefRe[t]
	if !known || !re.MatchString(id) {
		return "", "", false
	}
	return t, id, true
}

func probLinkAllowed(t, lt string) bool { return In(lt, probLinkTypes[t]...) }

// probItem is what a link points at, read from its own table (describe() in bin/problems.py).
type probItem struct {
	T, ID, Title, Status, Env, Sev, Href string
	Exists                               bool
	Inc                                  int64
	First, Last                          any
	DC                                   string
}

func probQ1(sql string, args ...any) Row {
	defer func() { recover() }() // a table that does not exist yet reads as "no such item"
	r := Q(sql, args...)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func probDescribe(t, id string) probItem {
	it := probItem{T: t, ID: id}
	n := py.I(id)
	switch t {
	case "incident":
		if r := probQ1("SELECT title, key, status, env, severity, first_seen, last_seen, dc FROM incidents WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: py.OrS(r.Get("title"), r.S("key")), Status: r.S("status"),
				Env: r.S("env"), Sev: r.S("severity"), First: r.Get("first_seen"), Last: r.Get("last_seen"), Inc: n, DC: r.S("dc")}
		}
		it.Href = "/incidents/view?id=" + id
	case "qa_issue":
		if r := probQ1("SELECT title, actionability, environment, severity, first_ts, last_ts, incident_id FROM qa_issues WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("title"), Status: r.S("actionability"), Env: r.S("environment"),
				Sev: r.S("severity"), First: r.Get("first_ts"), Last: r.Get("last_ts"), Inc: r.I("incident_id")}
		}
		it.Href = "/qa?state=all#issue-" + id
	case "cve_group":
		if r := probQ1("SELECT max(title) title, min(state) st, max(env_max_class) env, min(priority) pri, min(first_seen) f, "+
			"max(last_seen) l, max(incident_id) inc, count(*) n, max(component_name) comp FROM cve_findings WHERE group_key=?", id); r != nil && r.I("n") > 0 {
			it = probItem{T: t, ID: id, Exists: true, Title: py.OrS(r.Get("comp"), id) + " · " + I2S(r.I("n")) + " vulnerabilities",
				Status: r.S("st"), Env: r.S("env"), Sev: r.S("pri"), First: r.Get("f"), Last: r.Get("l"), Inc: r.I("inc")}
		}
		it.Href = "/vulns?group=" + py.Quote(id, "")
	case "cicd_finding":
		if r := probQ1("SELECT title, status, env, severity, first_seen, last_seen, incident_id FROM cicd_findings WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("title"), Status: r.S("status"), Env: r.S("env"),
				Sev: r.S("severity"), First: r.Get("first_seen"), Last: r.Get("last_seen"), Inc: r.I("incident_id")}
		}
		it.Href = "/cicd/finding?id=" + id
	case "capacity":
		host, mount, _ := strings.Cut(id, "|")
		if r := probQ1("SELECT env, min(created) c, max(created) m, count(*) n FROM disk_reports WHERE (host=? OR alias=?) AND mount=?",
			host, host, mount); r != nil && r.I("n") > 0 {
			it = probItem{T: t, ID: id, Exists: true, Title: host + " " + mount, Env: r.S("env"), First: r.Get("c"), Last: r.Get("m")}
		} else {
			it.Title, it.Exists = host+" "+mount, true // capacity.json only: shown by name
		}
		it.Href = "/capacity"
	case "vm_report":
		if r := probQ1("SELECT vm_id, name, env, status, created, run_id FROM vm_reports WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: "VM analysis " + r.S("name"), Status: r.S("status"), Env: r.S("env"),
				First: r.Get("created"), Last: r.Get("created"), Href: "/inventory/view?id=" + I2S(r.I("vm_id")) + "#analysis"}
		}
	case "finding":
		run, idx, _ := strings.Cut(id, ":")
		if r := probQ1("SELECT p.title, p.severity, p.environment, r.started_at FROM triage_problems p LEFT JOIN runs r ON r.id=p.run_id "+
			"WHERE p.run_id=? AND p.idx=?", py.I(run), py.I(idx)); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("title"), Env: r.S("environment"), Sev: r.S("severity"),
				First: r.Get("started_at"), Last: r.Get("started_at")}
			if e := probQ1("SELECT incident_id FROM incident_events WHERE run_id=? AND idx=? LIMIT 1", py.I(run), py.I(idx)); e != nil {
				it.Inc = e.I("incident_id")
			}
		}
		it.Href = "/incidents/problem?run=" + run + "&idx=" + idx
	case "rca":
		if r := probQ1("SELECT title, status, created, incident_id FROM rca_reports WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("title"), Status: r.S("status"), First: r.Get("created"),
				Last: r.Get("created"), Inc: r.I("incident_id")}
		}
		it.Href = "/rca/view?id=" + id
	case "fix_run":
		if r := probQ1("SELECT problem_title, outcome, env, started_at FROM fix_runs WHERE id=?", id); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: py.OrS(r.Get("problem_title"), id), Status: r.S("outcome"),
				Env: r.S("env"), First: r.Get("started_at")}
		}
		it.Href = "/admin/fix/run?id=" + id
	case "deploy":
		if r := probQ1("SELECT project, env, title, at, verify_state FROM cicd_deploys WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("project") + ": " + r.S("title"), Status: r.S("verify_state"),
				Env: r.S("env"), First: r.Get("at")}
		}
	case "problem":
		if r := probQ1("SELECT title, status, env, severity, first_seen FROM problems WHERE id=?", n); r != nil {
			it = probItem{T: t, ID: id, Exists: true, Title: r.S("title"), Status: r.S("status"), Env: r.S("env"),
				Sev: r.S("severity"), First: r.Get("first_seen")}
		}
		it.Href = "/problems/view?id=" + id
	case "proposal":
		it.Exists, it.Title, it.Href = true, "proposal #"+id, "/review?kind=proposals"
	case "task":
		it.Exists, it.Title = true, "task "+id
		if py.IsDigit(id) {
			it.Href = "/board/task?id=" + id
		}
	case "mr":
		it.Exists, it.Title = true, "merge request "+id
	}
	return it
}

// probRefLabel is the short name of an item: #12, QA-3, RCA #9, P-4, ...
func probRefLabel(t, id string) string {
	switch t {
	case "incident":
		return "#" + id
	case "qa_issue":
		return "QA-" + id
	case "cicd_finding":
		return "CI #" + id
	case "rca":
		return "RCA #" + id
	case "problem":
		return "P-" + id
	case "vm_report":
		return "VM analysis #" + id
	case "finding":
		run, idx, _ := strings.Cut(id, ":")
		return "run " + run + " #" + idx
	case "capacity":
		return strings.Replace(id, "|", " ", 1)
	case "deploy":
		return "deploy #" + id
	}
	return id
}

// probItemHTML: type tag, short name (linked), title, status.
func probItemHTML(it probItem, withTitle bool) string {
	name := E(probRefLabel(it.T, it.ID))
	if it.Href != "" {
		name = `<a href="` + E(it.Href) + `">` + name + `</a>`
	}
	out := `<span class="tag">` + E(probTypeWord[it.T]) + `</span> ` + name
	if !it.Exists {
		return out + ` <span class="dim">this item no longer exists</span>`
	}
	if withTitle && it.Title != "" && it.T != "capacity" {
		out += ` <span class="pb-it" dir="auto">` + E(FirstLine(it.Title, 140)) + `</span>`
	}
	if it.Status != "" {
		out += ` <span class="dim small">` + E(it.Status) + `</span>`
	}
	return out
}

// --- the problem an item belongs to -----------------------------------------------------------------------------

type probOf struct {
	P    Row
	Via  string // "" direct, or "via incident #N"
	Kind string // caused_by | explains
}

// problemsFor lists the problems an item is linked to (accepted caused_by / explains), primary first; QA issues,
// CVE groups, CI/CD findings, Findings and RCAs without a direct link show their incident's (derived, not stored).
func problemsFor(t, id string, inc int64) []probOf {
	var out []probOf
	seen := map[int64]bool{}
	add := func(rows []Row, via string) {
		for _, r := range rows {
			p := probQ1("SELECT * FROM problems WHERE id=?", r.I("problem_id"))
			if p == nil || seen[p.I("id")] {
				continue
			}
			seen[p.I("id")] = true
			out = append(out, probOf{p, via, r.S("link_type")})
		}
	}
	add(Q("SELECT problem_id, link_type FROM problem_links WHERE ref_type=? AND ref_id=? AND state='accepted' "+
		"AND link_type IN ('caused_by','explains') ORDER BY is_primary DESC, decided_at, id", t, id), "")
	if len(out) == 0 && inc > 0 && t != "incident" {
		add(Q("SELECT problem_id, link_type FROM problem_links WHERE ref_type='incident' AND ref_id=? AND state='accepted' "+
			"AND link_type='caused_by' ORDER BY is_primary DESC, decided_at, id", I2S(inc)), "through incident #"+I2S(inc))
	}
	return out
}

func probLinkedCount(pid int64) int64 {
	r := probQ1("SELECT count(*) n FROM problem_links WHERE problem_id=? AND state='accepted' AND link_type='caused_by'", pid)
	if r == nil {
		return 0
	}
	return r.I("n")
}

func probBannerHTML(ps []probOf) string {
	p := ps[0]
	verb := "Part of"
	if p.Kind == "explains" {
		verb = "Explains"
	}
	n := probLinkedCount(p.P.I("id"))
	extra := ""
	if len(ps) > 1 {
		var others []string
		for _, o := range ps[1:] {
			others = append(others, `<a href="/problems/view?id=`+I2S(o.P.I("id"))+`">`+probPID(o.P.I("id"))+`</a>`)
		}
		extra = ` · also ` + strings.Join(others, ", ")
	}
	via := ""
	if p.Via != "" {
		via = ` <span class="dim">(` + E(p.Via) + `)</span>`
	}
	return `<div class="banner pbanner" role="note">` + Icon("problems") + `<span>` + verb + ` <a href="/problems/view?id=` +
		I2S(p.P.I("id")) + `"><b>` + probPID(p.P.I("id")) + `</b> ` + E(p.P.S("title")) + `</a> · ` + E(probWord[p.P.S("status")]) +
		` · ` + I2S(n) + ` linked` + via + extra + `</span></div>`
}

// probPrimaryMap: ref_id -> primary problem (id, status) for one ref type, for list tags (one query per page).
func probPrimaryMap(t string) map[string]Row {
	out := map[string]Row{}
	for _, r := range Q("SELECT l.ref_id, p.id, p.status, p.title FROM problem_links l JOIN problems p ON p.id=l.problem_id "+
		"WHERE l.ref_type=? AND l.state='accepted' AND l.link_type='caused_by' ORDER BY l.is_primary, l.id", t) {
		out[r.S("ref_id")] = r // the primary is written last and wins
	}
	return out
}

// --- open problems for the pickers ---------------------------------------------------------------------------------

func probOpenRows(pq string, limit int) []Row {
	rows := Q("SELECT id, title, status, root_cause FROM problems WHERE status IN ('new','investigating','known_error','fix_in_progress') " +
		"ORDER BY updated_at DESC, id DESC LIMIT 500")
	pq = strings.ToLower(py.Strip(pq))
	if pq == "" {
		if len(rows) > limit {
			rows = rows[:limit]
		}
		return rows
	}
	num := strings.TrimPrefix(strings.TrimPrefix(pq, "p-"), "#")
	var out []Row
	for _, r := range rows {
		hay := strings.ToLower(r.S("title") + " " + r.S("root_cause"))
		match := true
		if py.IsDigit(num) {
			match = I2S(r.I("id")) == num
		} else {
			for _, w := range py.Split(pq) {
				if !strings.Contains(hay, w) {
					match = false
				}
			}
		}
		if match {
			out = append(out, r)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func probSortWorst(rows []Row) {
	sevRank := map[string]int{"critical": 4, "page": 4, "high": 3, "error": 3, "medium": 2, "warning": 2, "low": 1, "info": 1}
	stRank := map[string]int{"known_error": 0, "investigating": 1, "new": 2, "fix_in_progress": 3, "resolved": 4, "closed": 5}
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := rows[a], rows[b]
		if x, y := stRank[ra.S("status")], stRank[rb.S("status")]; (x >= 4) != (y >= 4) {
			return x < y
		}
		if x, y := sevRank[strings.ToLower(ra.S("severity"))], sevRank[strings.ToLower(rb.S("severity"))]; x != y {
			return x > y
		}
		if x, y := ra.I("customer_facing"), rb.I("customer_facing"); x != y {
			return x > y
		}
		return ra.I("updated_at") > rb.I("updated_at")
	})
}

// findingRef is the ref of a Finding page: a storage or VM analysis is linked as that, anything else as the finding.
func findingRef(run, idx int64) (string, string, string) {
	if idx == 0 {
		if r := probQ1("SELECT host, alias, mount FROM disk_reports WHERE run_id=? LIMIT 1", run); r != nil {
			host := py.OrS(r.Get("host"), r.S("alias"))
			id := host + "|" + r.S("mount")
			if probRefRe["capacity"].MatchString(id) {
				return "capacity", id, "storage analysis"
			}
		}
		if r := probQ1("SELECT id FROM vm_reports WHERE run_id=? LIMIT 1", run); r != nil {
			return "vm_report", I2S(r.I("id")), "VM analysis"
		}
	}
	return "finding", I2S(run) + ":" + I2S(idx), "finding"
}

// FindingWord relabels the per-run finding (triage_problems) "Finding" while Problem records are on, so "Problem"
// means only the root-cause record (P-n); with the feature off every page keeps its old words (HLD §2, UI only).
func FindingWord(old string) string {
	if !ProblemsOn() {
		return old
	}
	return map[string]string{"Problem": "Finding", "Problems": "Findings", "problems": "findings"}[old]
}
