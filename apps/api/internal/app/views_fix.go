package app

// Port of app/views_fix.py: the fix handoff cards ("How to fix it" + "Prompt for a write-access agent") and the
// /rca/prompt.md download. Every value is escaped with E().

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var fixKindLabel = map[string]string{"mitigation": "mitigation", "permanent fix": "permanent fix", "permanent": "permanent fix",
	"prevention": "prevention"}

var fixApproval = map[string][2]string{"production": {"tag prod", "Production: operator approves"},
	"operator": {"tag appr-op", "Operator OK first"},
	"fixer":    {"tag appr-fx", "Fixer can do this"}}

var (
	fixRefRCA     = regexp.MustCompile(`^rca:(\p{Nd}{1,9})$`)
	fixRefProblem = regexp.MustCompile(`^problem:(\p{Nd}{1,9}):(\p{Nd}{1,3})$`)
	fixTaskKey    = regexp.MustCompile(`^([A-Z][A-Z0-9_]{0,19}-[0-9]{1,9}|gl-[0-9]{1,9})$`)
	fixNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

func fixMissing() Handoff { return Handoff{State: "missing"} }

func fixFlags(v any) []string {
	s, ok := v.(string)
	if !ok && v != nil {
		return nil
	}
	if s == "" {
		s = "[]"
	}
	parsed, err := py.ParseJSON([]byte(s))
	if err != nil {
		return nil
	}
	var out []string
	for _, x := range py.AsList(parsed) {
		out = append(out, py.Str(x))
	}
	return out
}

// fixStored is _stored(row): the handoff a bin script stored on the row, ok=false when fix_state is NULL/empty.
func fixStored(row Row) (Handoff, bool) {
	if !row.Has("fix_state") || !row.T("fix_state") {
		return Handoff{}, false
	}
	var flags []string
	if row.Has("fix_flags") {
		flags = fixFlags(row.Get("fix_flags"))
	} else {
		flags = fixFlags(nil)
	}
	return Handoff{Options: FHLoadOptions(row.Get("fix_options")), Prompt: row.S("fix_prompt"),
		State: row.Str("fix_state"), Flags: flags}, true
}

// fixFill is _fill(h, text, env, fallback_prompt): a stored handoff that lacks a part the markdown still has takes
// that part from the markdown at render time.
func fixFill(h Handoff, text, env, fallback string) Handoff {
	if (h.Prompt != "" && len(h.Options) > 0) || !FHHasSections(text) {
		return h
	}
	p := FHParse(text, env, fallback)
	if p.Prompt == "" && len(p.Options) == 0 {
		return h
	}
	out := h
	if len(out.Options) == 0 {
		out.Options = p.Options
	}
	if out.Prompt == "" {
		out.Prompt = p.Prompt
	}
	out.State = "partial"
	if len(out.Options) > 0 && out.Prompt != "" {
		out.State = "ok"
	}
	out.Flags = append(append([]string{}, h.Flags...), "parsed at render time")
	return out
}

// FixHandoffProblem is handoff_problem(p).
func FixHandoffProblem(p Row) Handoff {
	if h, ok := fixStored(p); ok {
		return fixFill(h, p.S("rca"), p.S("environment"), p.S("fix_prompt"))
	}
	return FHParse(p.S("rca"), p.S("environment"), p.S("fix_prompt"))
}

// FixHandoffRca is handoff_rca(r, env=None); env nil is None.
func FixHandoffRca(r Row, env any) Handoff {
	h, stored := fixStored(r)
	if stored && h.Prompt != "" && len(h.Options) > 0 {
		return h
	}
	var e string
	if env == nil {
		if r.Has("env") {
			e = py.Str(r.Get("env"))
			if r.Get("env") == nil {
				e = ""
			}
		}
		if e == "" && r.T("incident_id") {
			i := Q("SELECT env FROM incidents WHERE id=?", r.I("incident_id"))
			if len(i) > 0 {
				e = i[0].S("env")
			}
		}
	} else {
		e = py.OrS(env, "")
	}
	if stored {
		return fixFill(h, r.S("content"), e, "")
	}
	return FHParse(r.S("content"), e, "")
}

// FixHandoffIncident is handoff_incident(inc): (handoff, source html).
func FixHandoffIncident(inc Row) (Handoff, string) {
	if h, ok := fixStored(inc); ok {
		ref := ""
		if inc.Has("fix_ref") {
			ref = inc.S("fix_ref")
		}
		return h, fixRefLink(ref)
	}
	if reps := Q("SELECT * FROM rca_reports WHERE incident_id=? ORDER BY created DESC LIMIT 1", inc.I("id")); len(reps) > 0 {
		h := FixHandoffRca(reps[0], inc.S("env"))
		if h.State != "missing" {
			return h, fixRefLink("rca:" + I2S(reps[0].I("id")))
		}
	}
	ps := Q("SELECT p.* FROM incident_events e JOIN triage_problems p ON p.run_id=e.run_id AND p.idx=e.idx "+
		"WHERE e.incident_id=? AND e.kind='finding' ORDER BY e.at DESC LIMIT 1", inc.I("id"))
	if len(ps) > 0 {
		p := ps[0]
		h, ok := fixStored(p)
		if !ok {
			env := inc.S("env")
			if env == "" {
				env = p.S("environment")
			}
			h = FHParse(p.S("rca"), env, p.S("fix_prompt"))
		}
		return h, fixRefLink("problem:" + I2S(p.I("run_id")) + ":" + I2S(p.I("idx")))
	}
	return fixMissing(), ""
}

func fixRefLink(ref string) string {
	if m := fixRefRCA.FindStringSubmatch(ref); m != nil {
		n, _ := py.ParseInt(m[1])
		return `from <a href="/rca/view?id=` + I2S(n) + `">RCA report #` + I2S(n) + `</a>`
	}
	if m := fixRefProblem.FindStringSubmatch(ref); m != nil {
		a, _ := py.ParseInt(m[1])
		b, _ := py.ParseInt(m[2])
		return `from <a href="/incidents/problem?run=` + I2S(a) + `&amp;idx=` + I2S(b) + `">the finding of ` +
			`run ` + I2S(a) + `</a>`
	}
	return ""
}

// FixHandoffTask is handoff_task(plan, env).
func FixHandoffTask(plan any, env string) Handoff {
	p := py.AsMap(plan)
	if p == nil || !py.Truthy(p.Get("fix_state")) {
		return fixMissing()
	}
	var opts []*py.OMap
	for _, o := range py.AsList(py.Or(p.Get("fix_options"), []any{})) {
		if m := py.AsMap(o); m != nil {
			opts = append(opts, m)
		}
	}
	if len(opts) > FHMaxOptions {
		opts = opts[:FHMaxOptions]
	}
	var flags []string
	for _, x := range py.AsList(py.Or(p.Get("fix_flags"), []any{})) {
		flags = append(flags, py.Str(x))
	}
	return Handoff{Options: opts, Prompt: py.OrS(p.Get("fix_prompt"), ""), State: py.Str(p.Get("fix_state")), Flags: flags}
}

// --- rendering ---------------------------------------------------------------------------------------------

func fixApprovalTag(o *py.OMap, env string) string {
	kind := ""
	if k := o.Get("approval_kind"); py.Truthy(k) {
		kind = py.Str(k)
		if _, ok := k.(string); !ok {
			kind = "\x00"
		}
	} else {
		kind = FHApprovalKind(o.Get("approval"), env)
	}
	if FHNormEnv(env) == "release" {
		kind = "production"
	}
	a, ok := fixApproval[kind]
	if !ok {
		a = fixApproval["operator"]
	}
	title := a[1]
	if v := o.Get("approval"); py.Truthy(v) {
		title = py.Str(v)
	}
	return `<span class="` + a[0] + `" title="` + E(title) + `">` + E(a[1]) + `</span>`
}

func fixSame(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case int64, float64, bool:
		return py.Num(a) == py.Num(b) && py.IsNumber(b) || a == b
	}
	return false
}

func fixReleaseNote() string {
	return `<span class="muted small">Argus never runs a production change. Hand this prompt to a person or an ` +
		`agent; the operator approves each step.</span>`
}

// --- download ----------------------------------------------------------------------------------------------

func fixDigits(s string, max int) (int64, bool) {
	if !py.IsDigit(s) || py.Len(s) > max {
		return 0, false
	}
	return py.ParseInt(s)
}

// fixDownload is download(qs): (filename, markdown), ok=false for a 404.
func fixDownload(qs py.QS) (string, string, bool) {
	rid, iid, run, idx, task, f := qs.Get("id"), qs.Get("incident"), qs.Get("run"), qs.Get("idx"), qs.Get("task"), qs.Get("file")
	if repo := qs.Get("repo"); repo != "" { // Check a repo: the prompt of a readiness report
		return repoDownload(repo)
	}
	if cve := qs.Get("cve"); cve != "" { // a vulnerability finding: the prompt of its component group (cve_fixes)
		n, ok := fixDigits(cve, 9)
		if !ok {
			return "", "", false
		}
		r := Q("SELECT x.fix_prompt FROM cve_findings f JOIN cve_fixes x ON x.group_key=f.group_key WHERE f.id=?", n)
		if len(r) == 0 {
			return "", "", false
		}
		return "argus-fix-cve-" + I2S(n) + ".md", r[0].S("fix_prompt"), true
	}
	switch {
	case f != "": // a merged report (only a name RcaFiles lists)
		if !In(f, RcaFiles()...) {
			return "", "", false
		}
		data, err := os.ReadFile(filepath.Join(REPORTS, f))
		if err != nil {
			panic(err)
		}
		text := strings.ReplaceAll(strings.ReplaceAll(py.ToValidUTF8(data), "\r\n", "\n"), "\r", "\n")
		return "argus-fix-" + py.Slice(f, 0, -3) + ".md", FHParse(text, "", "").Prompt, true
	case rid != "":
		n, ok := fixDigits(rid, 9)
		if !ok {
			return "", "", false
		}
		r := Q("SELECT * FROM rca_reports WHERE id=?", n)
		if len(r) == 0 {
			return "", "", false
		}
		return "argus-fix-rca-" + I2S(n) + ".md", FixHandoffRca(r[0], nil).Prompt, true
	case iid != "":
		n, ok := fixDigits(iid, 9)
		if !ok {
			return "", "", false
		}
		r := Q("SELECT * FROM incidents WHERE id=?", n)
		if len(r) == 0 {
			return "", "", false
		}
		h, _ := FixHandoffIncident(r[0])
		return "argus-fix-incident-" + I2S(n) + ".md", h.Prompt, true
	case run != "" || idx != "":
		a, ok1 := fixDigits(run, 9)
		b, ok2 := fixDigits(idx, 3)
		if !ok1 || !ok2 {
			return "", "", false
		}
		r := Q("SELECT * FROM triage_problems WHERE run_id=? AND idx=?", a, b)
		if len(r) == 0 {
			return "", "", false
		}
		return "argus-fix-problem-" + I2S(a) + "-" + I2S(b) + ".md", FixHandoffProblem(r[0]).Prompt, true
	case task != "":
		if !fixTaskKey.MatchString(task) {
			return "", "", false
		}
		a := BoardBQ("SELECT plan FROM agent_tasks WHERE key=?", task)
		t := BoardBQ("SELECT env FROM tasks WHERE jira_key=?", task)
		if strings.HasPrefix(task, "gl-") {
			t = BoardBQ("SELECT env FROM tasks WHERE id=? AND tracker='gitlab'", task[3:])
		}
		if len(a) == 0 {
			return "", "", false
		}
		var plan any = py.NewOMap()
		if v, err := py.ParseJSON([]byte(py.OrS(a[0].Get("plan"), "null"))); err == nil && py.Truthy(v) {
			plan = v
		}
		env := ""
		if len(t) > 0 {
			env = t[0].S("env")
		}
		if env == "" {
			if pm := py.AsMap(plan); pm != nil {
				env = py.OrS(py.AsMap(pm.Get("classification")).Get("environment"), "")
			}
		}
		return "argus-fix-" + task + ".md", FixHandoffTask(plan, env).Prompt, true
	}
	return "", "", false
}

// FixDownloadResponse is download_response(qs): (status, body or nil, headers).
func FixDownloadResponse(qs py.QS) (int, []byte, [][2]string) {
	name, text, ok := fixDownload(qs)
	if !ok || text == "" {
		return 404, nil, nil
	}
	name = py.Head(fixNameUnsafe.ReplaceAllString(name, "-"), 80)
	return 200, []byte(strings.TrimRight(text, "\n") + "\n"), [][2]string{{"Content-Disposition", `attachment; filename="` + name + `"`}}
}
