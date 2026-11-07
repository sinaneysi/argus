package app

// Problems (/problems, /problems?view=suggestions, /problems/view?id=): the root-cause records of bin/problems.py.
// "Which root causes are open, and what do they explain?" and, per problem, "What is this root cause, what does it
// explain, and is it fixed?". Read-only pages plus request-file controls (problems_panel.go). Go-only (ADR-0005).

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var probViews = [][3]string{{"open", "Open", ""}, {"suggestions", "Suggestions", ""}, {"resolved", "Resolved", ""},
	{"closed", "Closed", ""}, {"all", "All", ""}}

type probSel struct{ status, sev, env string }

func probSelOf(qs py.QS) probSel {
	s := probSel{status: qs.GetD("status", "open"), sev: qs.Get("sev"), env: qs.Get("env")}
	if !In(s.status, append([]string{"open", "all"}, probStatuses...)...) {
		s.status = "open"
	}
	if !In(s.sev, "", "critical", "high", "medium", "low") {
		s.sev = ""
	}
	if !In(s.env, "", "release", "pre-release", "develop") {
		s.env = ""
	}
	return s
}

func probSevMatch(sev, want string) bool {
	s := strings.ToLower(sev)
	switch want {
	case "":
		return true
	case "critical":
		return s == "critical" || s == "page"
	case "high":
		return s == "high" || s == "error"
	case "medium":
		return s == "medium" || s == "warning"
	case "low":
		return s == "low" || s == "info"
	}
	return false
}

// probCounts: accepted caused_by / explains links per problem and ref type.
func probCounts() map[int64]map[string]int64 {
	out := map[int64]map[string]int64{}
	for _, r := range Q("SELECT problem_id, ref_type, count(*) n FROM problem_links WHERE state='accepted' " +
		"AND link_type IN ('caused_by','explains') GROUP BY 1, 2") {
		m := out[r.I("problem_id")]
		if m == nil {
			m = map[string]int64{}
			out[r.I("problem_id")] = m
		}
		m[r.S("ref_type")] = r.I("n")
	}
	return out
}

var probCountNouns = [][3]string{{"incident", "incident", "incidents"}, {"qa_issue", "QA issue", "QA issues"},
	{"cve_group", "CVE group", "CVE groups"}, {"cicd_finding", "CI/CD finding", "CI/CD findings"},
	{"capacity", "disk", "disks"}, {"vm_report", "VM analysis", "VM analyses"}, {"finding", "finding", "findings"},
	{"rca", "RCA", "RCAs"}}

func probCountText(m map[string]int64) string {
	var parts []string
	for _, n := range probCountNouns {
		if v := m[n[0]]; v > 0 {
			parts = append(parts, I2S(v)+" "+If(v == 1, n[1], n[2]))
		}
	}
	if len(parts) == 0 {
		return "nothing linked yet"
	}
	return strings.Join(parts, " · ")
}

func probAge(r Row) string {
	start := r.Get("first_seen")
	if !py.Truthy(start) {
		start = r.Get("created_at")
	}
	return strings.TrimSuffix(Ago(start), " ago")
}

func probAutoLine(st *py.OMap) string {
	if st.Get("auto_accept") != true {
		return "Auto-accept: off (every suggestion waits for a person)"
	}
	var cls []string
	for _, x := range st.List("auto_accept_classes") {
		cls = append(cls, py.Str(x))
	}
	return "Auto-accept: on for " + strings.Join(cls, ", ") + " at ≥ " + py.F(py.Num(st.Get("auto_accept_confidence"))*100, 0) + " %"
}

// probBackfillPanel shows the D2 dry run (problems-backfill-plan.json) to admins while it is not applied.
func probBackfillPanel(c *Ctx, qs py.QS) string {
	if c.Sess == nil || c.Sess.Role != "admin" {
		return ""
	}
	plan := ReadJSONMap(filepath.Join(filepath.Dir(DB), "problems-backfill-plan.json"))
	if plan.Len() == 0 || plan.Get("applied") != false {
		return ""
	}
	sum := plan.Map("summary")
	if sum == nil {
		return ""
	}
	var lk []string
	if m := sum.Map("links"); m != nil {
		for _, k := range m.Keys() {
			lk = append(lk, I2S(py.I(m.Get(k)))+" "+k)
		}
	}
	var stl []string
	if m := sum.Map("status"); m != nil {
		for _, k := range m.Keys() {
			stl = append(stl, I2S(py.I(m.Get(k)))+" "+strings.ReplaceAll(k, "_", " "))
		}
	}
	head := `<p class="section-note">Generated ` + When(plan.Get("generated")) + ` by <code>problems.py backfill --dry-run</code>. ` +
		`<strong>Nothing was written.</strong> It would create <strong>` + I2S(py.I(sum.Get("problems_create"))) + ` problems</strong>` +
		If(len(stl) > 0, " ("+E(strings.Join(stl, ", "))+")", "") + `, ` + I2S(py.I(sum.Get("free_text_rcas"))) +
		` of them from RCAs without an incident, and add links: ` + E(strings.Join(lk, ", ")) + `. The operator applies it with ` +
		`<code>problems.py backfill --apply</code> after review; <code>problems.py purge --origin backfill</code> rolls it back.</p>`
	if sa := sum.Map("suggestions_after_apply"); sa != nil {
		var bs []string
		if b := sa.Map("bands"); b != nil {
			for _, k := range []string{"high", "medium", "low"} {
				bs = append(bs, I2S(py.I(b.Get(k)))+" "+k)
			}
		}
		var ts []string
		if b := sa.Map("by_type"); b != nil {
			for _, k := range b.Keys() {
				ts = append(ts, I2S(py.I(b.Get(k)))+" "+probTypeWord[k])
			}
		}
		head += `<p class="section-note">Then the suggester would propose <strong>` + I2S(py.I(sa.Get("suggested"))) + ` links</strong> (` +
			E(strings.Join(bs, ", ")) + If(len(ts) > 0, "; "+E(strings.Join(ts, ", ")), "") + `) from ` + I2S(py.I(sa.Get("items_scanned"))) +
			` open issues, every one waiting for a person.</p>`
	}
	var rows []string
	for _, x := range plan.List("problems") {
		p := py.AsMap(x)
		if p == nil {
			continue
		}
		var rc []string
		for _, r := range p.List("rcas") {
			rc = append(rc, `<a href="/rca/view?id=`+I2S(py.I(r))+`">#`+I2S(py.I(r))+`</a>`)
		}
		inc := "-"
		if py.Truthy(p.Get("incident")) {
			inc = `<a href="/incidents/view?id=` + I2S(py.I(p.Get("incident"))) + `">#` + I2S(py.I(p.Get("incident"))) + `</a> ` +
				`<span class="dim small">` + E(py.Str(p.Get("incident_status"))) + `</span>`
		}
		act := py.Str(p.Get("action"))
		if py.Truthy(p.Get("problem")) {
			act += " P-" + I2S(py.I(p.Get("problem")))
		}
		var sg []string
		for i, s := range p.List("suggestions") {
			m := py.AsMap(s)
			if m == nil || i >= 3 {
				continue
			}
			label := py.Str(m.Get("ref"))
			if t, id, ok := probParseRef(label); ok {
				label = probRefLabel(t, id)
			}
			sg = append(sg, E(label)+" "+py.F(py.Num(m.Get("confidence"))*100, 0)+" %")
		}
		more := ""
		if n := len(p.List("suggestions")); n > 0 {
			more = `<div class="dim small">suggests ` + strings.Join(sg, ", ") + If(n > 3, " +"+Itoa(n-3), "") + `</div>`
		}
		rows = append(rows, `<tr><td class="nw">`+E(act)+`</td><td class="wrap" dir="auto">`+E(py.Str(p.Get("title")))+
			If(py.Truthy(p.Get("root_cause")), `<div class="dim small">`+E(py.Str(p.Get("root_cause")))+`</div>`, "")+more+`</td>`+
			`<td>`+probStatusBadge(py.Str(p.Get("status")))+` `+probLabelTag(py.Str(p.Get("root_cause_label")))+`</td><td class="nw">`+inc+
			`</td><td class="nw">`+strings.Join(rc, ", ")+`</td></tr>`)
	}
	tbl := TableMore(`<tr><th>Action</th><th>Problem</th><th>Status</th><th>Incident</th><th>RCAs (explains)</th></tr>`, rows, 10,
		"planned problems", "tablewrap", "")
	return `<h2 id="backfill">Backfill plan (not applied)</h2>` + Fold("Planned problems", head+tbl, len(rows), false, "")
}

// --- suggestions ---------------------------------------------------------------------------------------------

// --- one problem ----------------------------------------------------------------------------------------------

type probEv struct {
	at         float64
	kind, src  string
	text, html string
}

var probEvKind = map[string]string{"created": "status", "status": "status", "recurrence": "reopen", "link": "link", "accept": "link",
	"merge": "link", "primary": "link", "unlink": "other", "reject": "other", "suggest": "other", "edit": "other", "rca": "rca"}

func probTimeline(p Row, links []Row) []probEv {
	var evs []probEv
	pid := p.I("id")
	for _, e := range Q("SELECT * FROM problem_events WHERE problem_id=? ORDER BY at DESC LIMIT 300", pid) {
		src := `<span class="tag">` + E(e.S("kind")) + `</span>`
		txt := E(e.S("actor"))
		if e.T("ref_type") {
			it := probDescribe(e.S("ref_type"), e.S("ref_id"))
			name := E(probRefLabel(it.T, it.ID))
			if it.Href != "" {
				name = `<a href="` + E(it.Href) + `">` + name + `</a>`
			}
			txt += ` · ` + name
		}
		if e.T("text") {
			txt += ` · <span dir="auto">` + E(probHumanText(e.S("text"))) + `</span>`
		}
		k := probEvKind[e.S("kind")]
		if k == "" {
			k = "other"
		}
		evs = append(evs, probEv{at: e.F("at"), kind: k, src: src, html: txt})
	}
	n := 0
	for _, l := range links {
		if l.S("state") != "accepted" {
			continue
		}
		switch l.S("ref_type") {
		case "incident":
			if n >= 20 {
				continue
			}
			n++
			for _, e := range Q("SELECT e.*, tp.title ptitle FROM incident_events e LEFT JOIN triage_problems tp ON tp.run_id=e.run_id "+
				"AND tp.idx=e.idx WHERE e.incident_id=? ORDER BY e.at DESC LIMIT 30", py.I(l.S("ref_id"))) {
				k := e.S("kind")
				if !In(k, "finding", "rca", "reopen", "resolve", "auto-resolve", "ack") {
					k = "other"
				}
				d := E(e.S("kind"))
				if e.T("ptitle") {
					d = `<span dir="auto">` + E(FirstLine(e.S("ptitle"), 140)) + `</span>`
				} else if e.T("text") {
					d += ` · <span dir="auto">` + E(FirstLine(e.S("text"), 140)) + `</span>`
				}
				evs = append(evs, probEv{at: e.F("at"), kind: k, src: `<a href="/incidents/view?id=` + E(l.S("ref_id")) + `">#` + E(l.S("ref_id")) + `</a>`, html: d})
			}
		case "rca":
			it := probDescribe("rca", l.S("ref_id"))
			if it.Exists {
				evs = append(evs, probEv{at: py.Num(it.First), kind: "rca", src: `<a href="` + it.Href + `">RCA #` + E(it.ID) + `</a>`,
					html: `report written · <span dir="auto">` + E(FirstLine(it.Title, 140)) + `</span>`})
			}
		case "cicd_finding", "cve_group", "qa_issue":
			it := probDescribe(l.S("ref_type"), l.S("ref_id"))
			if it.Exists && py.Truthy(it.First) {
				evs = append(evs, probEv{at: py.Num(it.First), kind: "finding", src: `<a href="` + E(it.Href) + `">` + E(probRefLabel(it.T, it.ID)) + `</a>`,
					html: `first seen · <span dir="auto">` + E(FirstLine(it.Title, 140)) + `</span>`})
			}
		}
	}
	sort.SliceStable(evs, func(a, b int) bool { return evs[a].at > evs[b].at })
	return evs
}

var probGroups = [][2]string{{"caused_by", "Causes"}, {"explains", "Explained by RCA reports"}, {"fixed_by", "Fixed by"},
	{"related_to", "Related"}, {"blocks", "Blocks"}, {"duplicate_of", "Duplicate of"}}

// probRecord is the problem's record rail: label and value HTML.
func probRecord(p Row, counts map[string]int64) [][2]string {
	rc := `<span class="dim">not recorded</span>`
	if p.T("root_cause") {
		rc = `<span dir="auto">` + E(p.S("root_cause")) + `</span> ` + probLabelTag(p.S("root_cause_label"))
	}
	kv := [][2]string{{"Status", probStatusBadge(p.S("status"))}, {"Root cause", rc},
		{"Workaround", If(p.T("workaround"), `<span dir="auto">`+E(p.S("workaround"))+`</span>`, `<span class="dim">-</span>`)},
		{"Owner", E(py.OrS(p.Get("owner"), "-"))}, {"Severity", IncidentsSevText(p.Get("severity"))},
		{"Environment", If(p.T("env"), EnvTag(p.S("env")), `<span class="dim">-</span>`) + If(p.T("customer_facing"), ` <span class="tag prod">customer-facing</span>`, "")},
		{"First seen", If(p.T("first_seen"), When(p.Get("first_seen")), `<span class="dim">-</span>`)},
		{"Known error", If(p.T("known_error_at"), When(p.Get("known_error_at")), `<span class="dim">-</span>`)},
		{"Fix started", If(p.T("fix_started_at"), When(p.Get("fix_started_at")), `<span class="dim">-</span>`)},
		{"Resolved", If(p.T("resolved_at"), When(p.Get("resolved_at"))+" by "+E(p.S("resolved_by"))+If(p.T("resolve_note"), " · "+E(p.S("resolve_note")), ""), `<span class="dim">-</span>`)},
		{"Linked", E(probCountText(counts))},
		{"Recurrences", I2S(p.I("recurrences"))},
		{"Matrix", If(p.T("thread_root"), "thread in the notify room", `<span class="dim">no thread yet</span>`)},
		{"Created", When(p.Get("created_at")) + " by " + E(p.S("created_by")) + ` <span class="dim small">` + E(p.S("origin")) + `</span>`}}
	return kv
}

var probHumanRepl = strings.NewReplacer("caused_by", "caused by", "fixed_by", "fixed by", "related_to", "related to",
	"duplicate_of", "duplicate of", "known_error", "known error", "fix_in_progress", "fix in progress", " -> ", " → ")

// probHumanText turns bin/problems.py's event words (caused_by, known_error, ->) into the UI's words.
func probHumanText(s string) string { return probHumanRepl.Replace(s) }
