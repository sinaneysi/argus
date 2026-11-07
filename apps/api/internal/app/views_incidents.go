package app

// Port of app/views_incidents.py: incident list, one incident, ack/resolve/reopen requests
// (settings-in/incident-actions/, applied by argus).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var incidentsStatusKind = map[string]string{"open": "fail", "acknowledged": "warn", "resolved": "ok"}
var incidentsSevKind = map[string]string{"critical": "fail", "high": "fail", "medium": "warn", "low": "ok"}

var incidentsFilters = map[string]string{"active": "status IN ('open','acknowledged')", "open": "status='open'",
	"acknowledged": "status='acknowledged'", "resolved": "status='resolved'", "all": "1=1"}
var incidentsActions = map[string]string{"ack": "Acknowledge", "resolve": "Resolve", "reopen": "Re-open"}
var incidentsSent = map[string]string{"ack": "Acknowledgement sent.", "resolve": "Resolve request sent.",
	"reopen": "Re-open request sent.",
	"deep":   "Deep RCA requested. It runs read-only for up to 30 minutes; the report appears below and under RCA reports."}
var incidentsEvKinds = map[string]bool{"finding": true, "rca": true, "reopen": true, "auto-resolve": true, "resolve": true,
	"ack": true, "owner": true}

func incidentsKind(m map[string]string, v any, def string) string {
	if s, ok := v.(string); ok {
		if k, ok := m[s]; ok {
			return k
		}
	}
	return def
}

// IncidentsSevBadge is sev_badge(level).
func IncidentsSevBadge(level any) string {
	l := strings.ToLower(py.OrS(level, ""))
	if l == "" {
		return `<span class="dim">-</span>`
	}
	return `<span class="badge ` + incidentsKind(incidentsSevKind, l, "warn") + `">` + E(l) + `</span>`
}

// IncidentsSevText is sev_text(level).
func IncidentsSevText(level any) string {
	l := strings.ToLower(py.OrS(level, ""))
	if l == "" {
		return `<span class="dim">-</span>`
	}
	cls := map[string]string{"critical": "failnum", "high": "failnum", "medium": "warnnum"}[l]
	if cls == "" {
		cls = "dim"
	}
	return `<span class="` + cls + `">` + E(l) + `</span>`
}

// incidentsDur is dur(sec).
func incidentsDur(v any) string {
	if v == nil {
		return "-"
	}
	f := py.Num(v)
	if f < 0 {
		return "-"
	}
	sec := int64(f)
	switch {
	case sec < 3600:
		return I2S(py.FloorDiv(sec, 60)) + " min"
	case sec < 2*86400:
		return py.F(float64(sec)/3600, 1) + " h"
	}
	return py.F(float64(sec)/86400, 1) + " d"
}

func incidentsHub(r Row) string {
	if !r.Has("hub_service") || !r.T("hub_service") {
		return ""
	}
	return "hub service: " + I2S(r.I("hub_callers")) + " callers (" + r.Str("hub_service") + ", tier " + I2S(r.I("hub_tier")) + ")"
}

// incidents queue: status (one value, "active" by default), severity, environment and source facets, a search over the
// number, title, subject, key, family, owner and env, three sorts, server pagination (DESIGN.md "Queue page").
var incidentsStatusFacets = [][2]string{{"active", "Active"}, {"open", "Open"}, {"acknowledged", "Acknowledged"},
	{"resolved", "Resolved"}, {"all", "All"}}
var incidentsSevFacets = [][2]string{{"critical", "Critical"}, {"high", "High"}, {"medium", "Medium"}, {"low", "Low"}, {"none", "Unrated"}}
var incidentsSorts = [][3]string{{"priority", "Priority", "Unresolved first, customer-facing first, then the latest finding"},
	{"severity", "Severity", "Worst severity first, then the latest finding"},
	{"recent", "Latest finding", "Most recent finding first"}, {"newest", "Newest", "Most recently opened first"}}

const incidentsMaxRows = 5000 // newest incidents loaded for the in-memory filters

func incidentsSevKey(r Row) string {
	l := strings.ToLower(r.S("severity"))
	if In(l, "critical", "high", "medium", "low") {
		return l
	}
	return "none"
}

func incidentsStatusMatch(flt string, st string) bool {
	switch flt {
	case "all":
		return true
	case "active":
		return st == "open" || st == "acknowledged"
	}
	return st == flt
}

func incidentsQuery(qs py.QS) listQ {
	return newListQ("/incidents", qs, []string{"q", "status", "sev", "env", "source", "sort"},
		map[string]string{"status": "active", "sort": "priority"}, func(k, v string) bool {
			switch k {
			case "status":
				_, ok := incidentsFilters[v]
				return ok
			case "sev":
				return In(v, "critical", "high", "medium", "low", "none")
			case "env":
				return len(v) <= 20 && proposalLabelRe.MatchString(v)
			case "source":
				_, ok := QASourceLabels[v]
				return ok
			case "sort":
				for _, x := range incidentsSorts {
					if x[0] == v {
						return true
					}
				}
			}
			return false
		})
}

// incList is the Incidents queue for one query: the rows it shows in order, the facet counts that honour the other
// filters, and the size of the status's whole list (the page and GET /api/v1/incidents share it).
type incList struct {
	rows                  []Row
	stN, sevN, envN, srcN map[string]int
	envs                  [][2]string
	total                 int
	noun                  string
}

func incidentsCompute(l listQ) incList {
	all := Q("SELECT * FROM incidents ORDER BY id DESC LIMIT " + Itoa(incidentsMaxRows))
	words := l.words()
	match := func(r Row, skip string) bool {
		if skip != "status" && !incidentsStatusMatch(l.get("status"), r.S("status")) {
			return false
		}
		if skip != "sev" && l.get("sev") != "" && incidentsSevKey(r) != l.get("sev") {
			return false
		}
		if skip != "env" && l.get("env") != "" && r.S("env") != l.get("env") {
			return false
		}
		if skip != "source" && l.get("source") != "" && !In(l.get("source"), QAIncSources(r)...) {
			return false
		}
		return listMatch(words, "#"+I2S(r.I("id")), r.S("title"), r.S("subject"), r.S("key"), r.S("family"), r.S("owner"),
			r.S("env"), r.S("dc"))
	}
	stN, sevN, envN, srcN := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	var envs [][2]string
	var rows []Row
	for _, r := range all {
		if match(r, "status") {
			for _, f := range incidentsStatusFacets {
				if incidentsStatusMatch(f[0], r.S("status")) {
					stN[f[0]]++
				}
			}
		}
		if match(r, "sev") {
			sevN[incidentsSevKey(r)]++
		}
		if match(r, "env") && r.S("env") != "" {
			if envN[r.S("env")] == 0 {
				envs = append(envs, [2]string{r.S("env"), r.S("env")})
			}
			envN[r.S("env")]++
		}
		if match(r, "source") {
			for _, k := range QAIncSources(r) {
				srcN[k]++
			}
		}
		if match(r, "") {
			rows = append(rows, r)
		}
	}
	envRank := map[string]int{"release": 0, "pre-release": 1, "develop": 2}
	sort.SliceStable(envs, func(i, j int) bool {
		ri, ok := envRank[envs[i][0]]
		if !ok {
			ri = 5
		}
		rj, ok := envRank[envs[j][0]]
		if !ok {
			rj = 5
		}
		return ri < rj || (ri == rj && envs[i][0] < envs[j][0])
	})
	if e := l.get("env"); e != "" && envN[e] == 0 {
		envs = append(envs, [2]string{e, e})
	}
	sevIdx := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "none": 4}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		switch l.get("sort") {
		case "severity":
			if si, sj := sevIdx[incidentsSevKey(x)], sevIdx[incidentsSevKey(y)]; si != sj {
				return si < sj
			}
		case "recent":
		case "newest":
			if x.F("first_seen") != y.F("first_seen") {
				return x.F("first_seen") > y.F("first_seen")
			}
		default:
			if rx, ry := x.S("status") == "resolved", y.S("status") == "resolved"; rx != ry {
				return ry
			}
			if cx, cy := x.T("customer_facing"), y.T("customer_facing"); cx != cy {
				return cx
			}
		}
		return x.F("last_seen") > y.F("last_seen")
	})
	total := 0
	for _, r := range all {
		if incidentsStatusMatch(l.get("status"), r.S("status")) {
			total++
		}
	}
	noun := map[string]string{"active": "active incidents", "open": "open incidents", "acknowledged": "acknowledged incidents",
		"resolved": "resolved incidents", "all": "incidents"}[l.get("status")]
	return incList{rows, stN, sevN, envN, srcN, envs, total, noun}
}

// incidentRecord is the incident's record rail: label and value HTML.
func incidentRecord(r Row) [][2]string {
	status := r.Get("status")
	var end float64
	if status == "resolved" {
		end = r.F("resolved_at")
	} else {
		end = Now()
	}
	hub := incidentsHub(r)
	envV := EnvTag(r.Get("env"))
	if r.T("customer_facing") {
		envV += ` <span class="tag prod">customer-facing</span>`
	}
	sevV := IncidentsSevBadge(r.Get("severity"))
	if r.T("alert_severity") {
		sevV += ` <span class="dim small">alert: ` + E(r.Str("alert_severity")) + `</span>`
	}
	hubV := "-"
	if hub != "" {
		hubV = E(hub)
		if strings.Contains(hub, "tier 1") {
			hubV += `. <span class="dim small">Tier 1 raises severity one step (admin setting).</span>`
		}
	}
	ack := "-"
	if r.T("acked_at") {
		ack = When(r.Get("acked_at")) + " by " + E(r.Str("acked_by"))
	}
	resolved := "-"
	if r.T("resolved_at") {
		resolved = When(r.Get("resolved_at")) + " by " + E(r.Str("resolved_by"))
		if r.T("resolve_note") {
			resolved += " · " + E(r.Str("resolve_note"))
		}
	}
	matrix := `<span class="dim">no thread (never paged)</span>`
	if r.T("thread_root") {
		matrix = "thread in the notify room"
	}
	openLbl := "Open for"
	if status == "resolved" {
		openLbl = "Time to resolve"
	}
	kv := [][2]string{{"Key", `<span class="mono small">` + E(r.Str("key")) + `</span>`},
		{"Environment", envV}, {"Severity", sevV}, {"Hub service", hubV},
		{"First seen", When(r.Get("first_seen"))},
		{"Last finding", When(r.Get("last_seen")) + ` <span class="dim">(` + Ago(r.Get("last_seen")) + `)</span>`},
		{"Findings", I2S(r.I("occurrences"))},
		{openLbl, incidentsDur(end - r.F("first_seen"))},
		{"Owner", E(py.OrS(r.Get("owner"), "-"))},
		{"Acknowledged", ack}, {"Resolved", resolved}, {"Matrix", matrix}}
	return kv
}

// incidentEvents is the incident's timeline, newest first.
func incidentEvents(id any) []Row {
	return Q("SELECT e.*, p.title ptitle, p.severity psev, p.rca prca, p.summary psum, r.summary rsum, r.status rstatus "+
		"FROM incident_events e LEFT JOIN triage_problems p ON p.run_id=e.run_id AND p.idx=e.idx "+
		"LEFT JOIN runs r ON r.id=e.run_id WHERE e.incident_id=? ORDER BY e.at DESC, e.rowid DESC", id)
}

// incidentEvParts is one timeline event: its kind and the HTML of its source and detail.
func incidentEvParts(ev Row) (string, string, string) {
	var src, detail string
	if ev.Get("kind") == "finding" {
		task := ev.Get("task")
		if ev.Get("idx") != nil && (task == "incident" || task == "chat-triage") {
			src = `<a href="/incidents/problem?run=` + I2S(ev.I("run_id")) + `&amp;idx=` + I2S(ev.I("idx")) + `">` +
				E(ev.Str("task")) + ` · run ` + I2S(ev.I("run_id")) + `</a>`
		} else if task == "log-rca" && ev.T("run_id") {
			src = `<a href="/incidents/run?id=` + I2S(ev.I("run_id")) + `">log-rca · run ` + I2S(ev.I("run_id")) + `</a>`
		} else {
			src = E(ev.S("task")) + ` · run ` + E(py.OrS(ev.Get("run_id"), "-"))
		}
		if ev.T("ptitle") {
			detail = `<b>` + E(ev.Str("ptitle")) + `</b> ` + IncidentsSevBadge(ev.Get("psev")) + `<br>`
		}
		detail += E(FirstLine(py.OrS(py.Or(py.Or(ev.Get("psum"), ev.Get("text")), ev.Get("rsum")), ""), 300))
	} else {
		src = `<span class="tag">` + E(ev.Str("kind")) + `</span>`
		detail = E(ev.S("actor"))
		if ev.T("text") {
			detail += ` · ` + E(ev.Str("text"))
		}
	}
	kind := "other"
	if k, ok := ev.Get("kind").(string); ok && incidentsEvKinds[k] {
		kind = k
	}
	return kind, src, detail
}

// newest timeline events shown open on the incident page

var (
	incidentsRefProblem = regexp.MustCompile(`^problem:(\p{Nd}{1,9}):(\p{Nd}{1,3})$`)
	incidentsSrcProblem = regexp.MustCompile(`run=(\p{Nd}+)&amp;idx=(\p{Nd}+)`)
	incidentsRefRCA     = regexp.MustCompile(`^rca:(\p{Nd}{1,9})$`)
	incidentsSrcRCA     = regexp.MustCompile(`/rca/view\?id=(\p{Nd}+)`)
)

// incidentFixActions is the "Take action" part of the fix cards, for an environment Argus may fix.
func incidentFixActions(c *Ctx, r Row, h Handoff, src, env string) string {
	if (env != "develop" && env != "pre-release") || h.Prompt == "" {
		return ""
	}
	ref := ""
	if r.Has("fix_ref") && r.T("fix_ref") {
		ref = r.Str("fix_ref")
	}
	m := incidentsRefProblem.FindStringSubmatch(ref)
	if m == nil {
		m = incidentsSrcProblem.FindStringSubmatch(src)
	}
	mr := incidentsRefRCA.FindStringSubmatch(ref)
	if mr == nil {
		mr = incidentsSrcRCA.FindStringSubmatch(src)
	}
	review := ""
	if m != nil {
		review = "run=" + m[1] + "&amp;idx=" + m[2]
	} else if mr != nil {
		review = "rca=" + mr[1]
	}
	if review == "" {
		return ""
	}
	return FixerProblemActions(c, Row{"environment": env, "fix_prompt": h.Prompt, "review": review})
}

// IncidentsPostAction is post_action(form, sess): (status, location or message).
func IncidentsPostAction(c *Ctx, form py.QS, sess *Session) (int, string) {
	iid, action := form.Get("id"), form.Get("action")
	if _, ok := incidentsActions[action]; !py.IsDigit(iid) || !ok {
		return 400, "Invalid request."
	}
	if (action == "resolve" || action == "reopen") && sess.Role != "admin" {
		return 403, "Only admins can resolve or re-open an incident."
	}
	n, _ := py.ParseInt(iid)
	if len(Q("SELECT id FROM incidents WHERE id=?", n)) == 0 {
		return 404, "No such incident."
	}
	note := ""
	if action == "resolve" {
		note = py.Head(strings.Join(py.Split(form.Get("note")), " "), 300)
	}
	d := filepath.Join(SET_IN, "incident-actions")
	name := I2S(NowInt()) + "-" + auth.TokenHex(4) + ".json"
	body := py.Dumps(py.NewOMap("id", n, "action", action, "by", sess.Username, "role", sess.Role, "note", note,
		"at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, name), filepath.Join(d, "."+name+".tmp"), []byte(body)); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err}) // Python: OSError -> 500
	}
	logf("incident %s #%d requested by %s", action, n, sess.Username)
	return 303, "/incidents/view?id=" + I2S(n) + "&sent=" + action
}
