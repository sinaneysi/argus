package ideas

// run.go: the advisers' weekly model run. One adviser (devops, qa, noc) at a time reads a read-only workspace of
// redacted facts from ops.db, the board and the status files, and answers with JSON ideas; the answer is validated,
// deduplicated against every earlier idea and stored as new ideas (a rejected idea comes back only with changed
// evidence and a note of what changed).
//
// Schedule: each adviser once a week on its day (ideas.json, IDEAS_SCHEDULE overrides); one run per tick, a missed
// slot catches up on the next tick. A manual run ("Generate now") at most once a day per adviser. The daily budget
// IDEAS_DAILY_USD caps all runs of a day.
//
// Files (ARGUS_ETC_DIR, each with a built-in neutral default): ideas-run.md (the task prompt, {{max}} is the cap),
// ideas-agents/<agent>.md (the adviser's persona), ideas-features.md (what this installation already does),
// ideas-job-model.md (optional: the operator's recurring work and its coverage).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/model"
	"github.com/sinaneysi/argus/internal/py"
)

//go:embed run.md
var defaultRunPrompt string

//go:embed features.md
var defaultFeatures string

//go:embed agents/*.md
var agentFiles embed.FS

var (
	pages = []string{"incidents", "rca", "cicd", "qa", "alerts", "hygiene", "capacity", "inventory", "backups", "certs",
		"vulns", "runs", "proposals", "board", "admin", "ideas"}
	linkRe  = regexp.MustCompile(`^/(?:(?:` + strings.Join(pages, "|") + `)(?:/[a-z0-9_-]{1,30}){0,2})?(?:\?[A-Za-z0-9=&;_.:%+-]{0,160})?\n?\z`)
	agentRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}\n?\z`)
	fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")
	normRe  = regexp.MustCompile(`[^a-z0-9]+`)
	spaceRe = regexp.MustCompile(`\s+`)
	stop    = map[string]bool{}
	fields  = []struct {
		key    string
		lo, hi int
	}{{"title", 8, 100}, {"problem", 20, 1500}, {"proposal", 20, 3000}, {"value", 3, 600}, {"risk", 3, 600},
		{"prerequisites", 0, 600}, {"success_metric", 3, 300}, {"changed", 0, 600}}
	effortSet = []string{"S", "M", "L"}
)

func init() {
	for _, w := range strings.Fields("a an the to for of and in on with from by per into via our its it be is are this that argus add build " +
		"create make new use auto automatic automatically") {
		stop[w] = true
	}
}

// RunFiles are the installation's files of the model run (ARGUS_ETC_DIR) and where the run works.
type RunFiles struct {
	EtcDir  string // ideas-run.md, ideas-agents/, ideas-features.md, ideas-job-model.md ("": the built-in ones)
	BoardDB string // the board database (proposals, tasks); "" or missing: that section says nothing
	WorkDir string // the workspace ("": StateDir/ideas-work)
}

func (f RunFiles) etc(name string) ([]byte, bool) {
	if f.EtcDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(f.EtcDir, name))
	if err != nil || !utf8.Valid(b) {
		return nil, false
	}
	return b, true
}

// RunPrompt is the task prompt with the cap filled in and the workspace line.
func (o *Options) RunPrompt(f RunFiles) string {
	t := defaultRunPrompt
	if b, ok := f.etc("ideas-run.md"); ok {
		t = string(b)
	}
	return strings.ReplaceAll(t, "{{max}}", strconv.FormatInt(o.maxPerRun(), 10)) +
		"\n\nYour working directory is this run's workspace; start with INDEX.md."
}

func (f RunFiles) agent(name string) ([]byte, bool) {
	if b, ok := f.etc(filepath.Join("ideas-agents", name+".md")); ok {
		return b, true
	}
	b, err := agentFiles.ReadFile("agents/" + name + ".md")
	return b, err == nil
}

// --- schedule and caps ------------------------------------------------------------------------------------------------

func (o *Options) lastRun(q querier, persona, trigger string) (int64, error) {
	qs, args := `SELECT max(at) m FROM idea_events WHERE kind='run' AND persona=?`, []any{persona}
	if trigger != "" {
		qs += ` AND trigger=?`
		args = append(args, trigger)
	}
	r, err := one1(q, qs, args...)
	return r.i("m"), err
}

// slots are the starts (local midnight) of the most recent occurrence of each day, within the last 7 days.
func (o *Options) slots(ds []any, now int64) []int64 {
	t := time.Unix(now, 0).In(o.Loc)
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, o.Loc)
	wd := (int(d.Weekday()) + 6) % 7 // Monday 0, like Python's weekday()
	var out []int64
	for _, x := range ds {
		i := 0
		for j, n := range days {
			if n == py.Str(x) {
				i = j
			}
		}
		back := ((wd-i)%7 + 7) % 7
		out = append(out, d.AddDate(0, 0, -back).Unix())
	}
	return out
}

// Due is the adviser whose weekly slot came and has had no run since (the earliest slot first), or "".
func (o *Options) Due(q querier, cfg Config) (string, error) {
	o.defaults()
	now := o.Now().Unix()
	best, bp := int64(0), ""
	ps := cfg.m.Map("personas")
	for _, p := range ps.Keys() {
		d := ps.Map(p)
		if d == nil {
			continue
		}
		for _, s := range o.slots(d.List("days"), now) {
			last, err := o.lastRun(q, p, "")
			if err != nil {
				return "", err
			}
			if last < s && (bp == "" || s < best) {
				best, bp = s, p
			}
		}
	}
	return bp, nil
}

// Check is "" when a run may start, else the reason.
func (o *Options) Check(q querier, cfg Config, persona, trigger string) (string, error) {
	o.defaults()
	now := o.Now().Unix()
	if !o.Enabled() {
		return "Ideas are off (FEATURE_IDEAS=0)", nil
	}
	if cfg.persona(persona) == nil {
		return "unknown persona", nil
	}
	if trigger != "schedule" && trigger != "manual" {
		return "unknown trigger", nil
	}
	if trigger == "manual" {
		last, err := o.lastRun(q, persona, "manual")
		if err != nil {
			return "", err
		}
		if last >= o.dayStart(now) {
			return "Generate now was used today for this persona (once a day)", nil
		}
	}
	limit := o.envNum("IDEAS_DAILY_USD", 2.0, 0.1, 20, false)
	r, err := one1(q, `SELECT coalesce(sum(cost_usd), 0) s FROM idea_events WHERE kind IN ('run','prompt') AND at>=?`, o.dayStart(now))
	if err != nil {
		return "", err
	}
	if spent := py.Num(r["s"]); spent >= limit {
		return fmt.Sprintf("daily Ideas budget reached ($%s of $%s)", py.F(spent, 2), py.F(limit, 2)), nil
	}
	return "", nil
}

// --- the workspace (the model's only inputs) ----------------------------------------------------------------------

// wsSettings keep the CLI inside the workspace: read, glob and grep only.
const wsSettings = `{
 "permissions": {
  "allow": ["Read(./**)", "Glob(./**)", "Grep(./**)"],
  "deny": ["Bash", "Write", "Edit", "MultiEdit", "NotebookEdit", "WebFetch", "WebSearch", "Task", "Read(../**)",
   "Read(//etc/**)", "Read(//home/**)", "Read(//root/**)", "Read(//proc/**)", "Read(//opt/**)", "Read(//data/**)",
   "Read(~/**)", "Read(**/*.db)", "Read(**/.env)", "Read(**/*.pem)", "Read(**/*.key)"]
 }
}
`

// errName is the class name the reference prints for a failed section.
type sectionErr struct{ name string }

func (e sectionErr) Error() string { return e.name }

func errName(err error) string {
	var se sectionErr
	if errors.As(err, &se) {
		return se.name
	}
	return "OperationalError"
}

func section(title string, fn func() (string, error)) string {
	body, err := fn()
	if err != nil {
		body = "(unavailable: " + errName(err) + ")"
	}
	if body == "" {
		body = "(nothing)"
	}
	return "## " + title + "\n\n" + body + "\n"
}

// readMap is the reference's read_json(path) or {}: nil for a missing or bad file; a value that is not an object
// fails the section (AttributeError).
func readMap(path string) (*py.OMap, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return py.NewOMap(), nil
	}
	v, err := py.ParseJSON(b)
	if err != nil || !py.Truthy(v) {
		return py.NewOMap(), nil
	}
	m, ok := v.(*py.OMap)
	if !ok {
		return nil, sectionErr{"AttributeError"}
	}
	return m, nil
}

func s0(v any, def string) string {
	if py.Truthy(v) {
		return py.Str(v)
	}
	return def
}

func joinRows(rs []row, f func(row) string) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, f(r))
	}
	return strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (o *Options) incSection(q querier, since int64) (string, error) {
	rs, err := rows(q, `SELECT status, count(*) n FROM incidents WHERE last_seen>=? GROUP BY 1`, since)
	if err != nil {
		return "", err
	}
	out := []string{"Incidents seen in the last 30 days by status: " + orNone(joinRows(rs, func(r row) string { return r.s("status") + " " + py.Str(r["n"]) }))}
	env, err := rows(q, `SELECT coalesce(env,'?') env, count(*) n FROM incidents WHERE last_seen>=? GROUP BY 1 ORDER BY 2 DESC`, since)
	if err != nil {
		return "", err
	}
	out = append(out, "By environment: "+joinRows(env, func(r row) string { return py.Str(r["env"]) + " " + py.Str(r["n"]) }))
	fam, err := rows(q, `SELECT coalesce(family,'?') f, count(*) n, sum(occurrences) occ FROM incidents WHERE last_seen>=? GROUP BY 1 ORDER BY 2 DESC LIMIT 12`, since)
	if err != nil {
		return "", err
	}
	out = append(out, "By family (incidents / occurrences): "+joinRows(fam, func(r row) string {
		return py.Str(r["f"]) + " " + py.Str(r["n"]) + "/" + s0(r["occ"], "0")
	}))
	mttr, err := rows(q, `SELECT avg(resolved_at-first_seen) s, count(*) n FROM incidents WHERE resolved_at>=? AND first_seen>0`, since)
	if err != nil {
		return "", err
	}
	if len(mttr) > 0 && py.Truthy(mttr[0]["n"]) {
		out = append(out, fmt.Sprintf("Resolved: %s, mean time to resolve %s h", py.Str(mttr[0]["n"]), py.F(py.Num(mttr[0]["s"])/3600, 1)))
	}
	who, err := rows(q, `SELECT coalesce(resolved_by,'?') w, count(*) n FROM incidents WHERE resolved_at>=? GROUP BY 1 ORDER BY 2 DESC LIMIT 5`, since)
	if err != nil {
		return "", err
	}
	if len(who) > 0 {
		out = append(out, "Resolved by: "+joinRows(who, func(r row) string { return py.Str(r["w"]) + " " + py.Str(r["n"]) }))
	}
	out = append(out, "", "| id | status | env | family | title | seen | first seen | link |", "|---|---|---|---|---|---|---|---|")
	open, err := rows(q, `SELECT * FROM incidents WHERE status IN ('open','acknowledged') ORDER BY last_seen DESC LIMIT 20`)
	if err != nil {
		return "", err
	}
	for _, r := range open {
		out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %s | %sx | %s | /incidents/view?id=%s |", py.Str(r["id"]), r.s("status"),
			s0(r["env"], "-"), one(r["family"], 30), one(r["title"], 90), s0(r["occurrences"], "0"), o.date(r.i("first_seen")), py.Str(r["id"])))
	}
	rec, err := rows(q, `SELECT * FROM incidents WHERE last_seen>=? AND occurrences>=5 ORDER BY occurrences DESC LIMIT 10`, since)
	if err != nil {
		return "", err
	}
	if len(rec) > 0 {
		out = append(out, "", "Recurring (5+ occurrences):")
		for _, r := range rec {
			out = append(out, fmt.Sprintf("- #%s %s: %sx, %s (/incidents/view?id=%s)", py.Str(r["id"]), one(r["title"], 90),
				py.Str(r["occurrences"]), py.Str(r["status"]), py.Str(r["id"])))
		}
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) rcaSection(q querier, since int64) (string, error) {
	rs, err := rows(q, `SELECT coalesce(source,'?') s, status, count(*) n, sum(coalesce(cost_usd,0)) c FROM rca_reports WHERE created>=? GROUP BY 1, 2`, since)
	if err != nil {
		return "", err
	}
	out := []string{"RCA reports in 30 days (source, MR status, count, cost): " + orNone(joinRows(rs, func(r row) string {
		return py.Str(r["s"]) + "/" + py.Str(r["status"]) + " " + py.Str(r["n"]) + " ($" + py.F(py.Num(r["c"]), 2) + ")"
	}))}
	list, err := rows(q, `SELECT id, incident_id, title, status, created FROM rca_reports WHERE created>=? ORDER BY created DESC LIMIT 12`, since)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		l := fmt.Sprintf("- %s %s [%s] (/rca/view?id=%s", o.date(r.i("created")), one(r["title"], 90), py.Str(r["status"]), py.Str(r["id"]))
		if py.Truthy(r["incident_id"]) {
			l += ", incident /incidents/view?id=" + py.Str(r["incident_id"])
		}
		out = append(out, l+")")
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) qaSection(q querier, since int64) (string, error) {
	rs, err := rows(q, `SELECT coalesce(disposition,'?') d, count(*) n FROM chat_messages WHERE ts>=? GROUP BY 1 ORDER BY 2 DESC`, since)
	if err != nil {
		return "", err
	}
	out := []string{"QA room messages in 30 days by what Argus did with them: " + orNone(joinRows(rs, func(r row) string { return py.Str(r["d"]) + " " + py.Str(r["n"]) }))}
	iss, err := rows(q, `SELECT coalesce(actionability,'?') a, coalesce(ask_status,'-') s, count(*) n FROM qa_issues GROUP BY 1, 2`)
	if err != nil {
		return "", err
	}
	if len(iss) > 0 {
		out = append(out, "QA issues (actionability / question state): "+joinRows(iss, func(r row) string {
			return py.Str(r["a"]) + "/" + py.Str(r["s"]) + " " + py.Str(r["n"])
		}))
	}
	list, err := rows(q, `SELECT title, environment, actionability, created_at FROM qa_issues ORDER BY created_at DESC LIMIT 8`)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		out = append(out, fmt.Sprintf("- %s %s [%s, %s] (/qa)", o.date(r.i("created_at")), one(r["title"], 90), s0(r["environment"], "-"), s0(r["actionability"], "-")))
	}
	tp, err := rows(q, `SELECT count(*) n, sum(suspicious) s FROM triage_problems tp JOIN runs r ON r.id=tp.run_id WHERE r.started_at>=?`, since)
	if err != nil {
		return "", err
	}
	if len(tp) > 0 && py.Truthy(tp[0]["n"]) {
		out = append(out, fmt.Sprintf("Problems found by triage runs: %s (%s marked suspicious)", py.Str(tp[0]["n"]), s0(tp[0]["s"], "0")))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) hygieneSection() (string, error) {
	h, err := readMap(filepath.Join(o.StateDir, "hygiene.json"))
	if err != nil {
		return "", err
	}
	var out []string
	if c := h.Map("counts"); c != nil {
		var parts []string
		for i, k := range c.Keys() {
			if i >= 12 {
				break
			}
			parts = append(parts, k+" "+py.Str(c.Get(k)))
		}
		out = append(out, "Alert hygiene counts: "+strings.Join(parts, ", ")+" (/hygiene)")
	}
	fs := py.AsList(h.Get("findings"))
	for i, f := range fs {
		if i >= 12 {
			break
		}
		m := py.AsMap(f)
		if m == nil {
			continue
		}
		var parts []string
		for _, k := range []string{"kind", "rule", "alertname", "name", "reason", "count"} {
			v := m.Get(k)
			if v == nil || v == "" {
				continue
			}
			parts = append(parts, py.Str(v))
		}
		out = append(out, "- "+one(strings.Join(parts, " · "), 160))
	}
	a, err := readMap(filepath.Join(o.StateDir, "alerts-firing.json"))
	if err != nil {
		return "", err
	}
	if dcs := a.Map("dcs"); dcs != nil {
		var parts []string
		for _, dc := range dcs.Keys() {
			n := 0
			switch v := dcs.Get(dc).(type) {
			case *py.OMap:
				n = len(py.AsList(v.Get("alerts")))
			case []any:
				n = len(v)
			}
			parts = append(parts, dc+" "+strconv.Itoa(n))
		}
		out = append(out, "Alerts firing now: "+strings.Join(parts, ", ")+" (/alerts)")
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) cicdSection(q querier) (string, error) {
	rs, err := rows(q, `SELECT kind, coalesce(klass,'-') k, coalesce(env,'-') e, count(*) n, sum(occurrences) occ FROM cicd_findings WHERE status='open' GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 15`)
	if err != nil {
		return "", err
	}
	out := []string{"Open CI/CD findings (kind / class / env: findings, occurrences): " + orNone(joinRows(rs, func(r row) string {
		return py.Str(r["kind"]) + "/" + py.Str(r["k"]) + "/" + py.Str(r["e"]) + ": " + py.Str(r["n"]) + ", " + s0(r["occ"], "0")
	}))}
	list, err := rows(q, `SELECT id, project, env, title, occurrences FROM cicd_findings WHERE status='open' ORDER BY occurrences DESC, last_seen DESC LIMIT 10`)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		out = append(out, fmt.Sprintf("- %s [%s] %s: %sx (/cicd/finding?id=%s)", one(r["project"], 50), s0(r["env"], "-"), one(r["title"], 90),
			s0(r["occurrences"], "1"), py.Str(r["id"])))
	}
	pl, err := rows(q, `SELECT coalesce(env,'-') e, status, count(*) n FROM cicd_pipelines WHERE created_at>=? GROUP BY 1, 2 ORDER BY 1, 3 DESC`, o.Now().Unix()-7*86400)
	if err != nil {
		return "", err
	}
	if len(pl) > 0 {
		out = append(out, "Pipelines in 7 days (env / status): "+joinRows(pl, func(r row) string {
			return py.Str(r["e"]) + "/" + py.Str(r["status"]) + " " + py.Str(r["n"])
		})+" (/cicd)")
	}
	st, err := readMap(filepath.Join(o.StateDir, "cicd-status.json"))
	if err != nil {
		return "", err
	}
	if st.Len() > 0 {
		var parts []string
		for _, k := range st.Keys() {
			v := st.Get(k)
			var x any = v
			if m, ok := v.(*py.OMap); ok {
				x = m.Get("status")
			} else if v == nil {
				x = nil
			}
			parts = append(parts, k+" "+one(x, 30))
		}
		out = append(out, "CI/CD jobs: "+strings.Join(parts, ", "))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) capacitySection() (string, error) {
	c, err := readMap(filepath.Join(o.StateDir, "capacity.json"))
	if err != nil {
		return "", err
	}
	var out []string
	if s := c.Map("summary"); s != nil {
		out = append(out, "Capacity summary: "+one(dumpsUnicode(s), 400)+" (/capacity)")
	}
	type fsRow struct {
		m    *py.OMap
		days float64
	}
	var fs []fsRow
	for _, r := range py.AsList(c.Get("filesystems")) {
		m := py.AsMap(r)
		if m == nil {
			continue
		}
		d := m.Get("days")
		if b, isBool := d.(bool); isBool { // a bool is an int in the reference
			d = map[bool]int64{true: 1, false: 0}[b]
		} else if !py.IsNumber(d) {
			continue
		}
		if py.Num(d) < 30 {
			fs = append(fs, fsRow{m, py.Num(d)})
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].days < fs[j].days })
	for i, r := range fs {
		if i >= 10 {
			break
		}
		out = append(out, fmt.Sprintf("- %s %s: full in %s d, %s %% used [%s]", one(r.m.Get("host"), 40), one(r.m.Get("mount"), 40),
			py.F(r.days, 0), py.Str(r.m.GetD("used_pct", "?")), py.Str(r.m.GetD("dc", "-"))))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) cveSection(q querier) (string, error) {
	rs, err := rows(q, `SELECT priority, count(*) n FROM cve_findings WHERE state='open' GROUP BY 1 ORDER BY 1`)
	if err != nil {
		return "", err
	}
	out := []string{"Open vulnerabilities by priority: " + orNone(joinRows(rs, func(r row) string { return py.Str(r["priority"]) + " " + py.Str(r["n"]) })) + " (/vulns)"}
	k, err := rows(q, `SELECT count(*) n FROM cve_findings WHERE state='open' AND kev=1`)
	if err != nil {
		return "", err
	}
	n := "0"
	if len(k) > 0 {
		n = py.Str(k[0]["n"])
	}
	out = append(out, "Known exploited (KEV): "+n)
	list, err := rows(q, `SELECT id, component_name, priority, count(*) n, max(n_targets) t FROM cve_findings WHERE state='open' AND priority IN ('P1','P2') GROUP BY component_name ORDER BY priority, n DESC LIMIT 8`)
	if err != nil {
		return "", err
	}
	for _, r := range list {
		out = append(out, fmt.Sprintf("- %s %s: %s findings, up to %s targets (/vulns?id=%s)", one(r["component_name"], 40), py.Str(r["priority"]),
			py.Str(r["n"]), s0(r["t"], "0"), py.Str(r["id"])))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) backupSection(q querier) (string, error) {
	var out []string
	r, err := rows(q, `SELECT id, started_at FROM runs WHERE task='backup-check' ORDER BY started_at DESC LIMIT 1`)
	if err != nil {
		return "", err
	}
	if len(r) > 0 {
		st, err := rows(q, `SELECT status, count(*) n FROM backup_results WHERE run_id=? GROUP BY 1`, r[0]["id"])
		if err != nil {
			return "", err
		}
		out = append(out, "Latest backup-check "+o.date(r[0].i("started_at"))+": "+joinRows(st, func(x row) string { return py.Str(x["status"]) + " " + py.Str(x["n"]) })+" (/backups)")
		bad, err := rows(q, `SELECT name, status, age_h FROM backup_results WHERE run_id=? AND status!='OK' LIMIT 15`, r[0]["id"])
		if err != nil {
			return "", err
		}
		for _, x := range bad {
			out = append(out, fmt.Sprintf("- %s: %s, newest %s h old (/backups/set?name=%s)", one(x["name"], 60), py.Str(x["status"]), py.Str(x["age_h"]), one(x["name"], 60)))
		}
	}
	b, err := readMap(filepath.Join(o.StateDir, "backup-status.json"))
	if err != nil {
		return "", err
	}
	if b.Len() > 0 {
		var parts []string
		for _, k := range []string{"ok", "encrypted", "offhost", "restore_test"} {
			if b.Has(k) {
				parts = append(parts, k+" "+one(b.Get(k), 40))
			}
		}
		out = append(out, "Argus's own state backup: "+strings.Join(parts, ", "))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) certSection(q querier) (string, error) {
	r, err := rows(q, `SELECT id, started_at FROM runs WHERE task='cert-check' ORDER BY started_at DESC LIMIT 1`)
	if err != nil || len(r) == 0 {
		return "", err
	}
	st, err := rows(q, `SELECT status, count(*) n FROM cert_results WHERE run_id=? GROUP BY 1`, r[0]["id"])
	if err != nil {
		return "", err
	}
	out := []string{"Latest cert-check " + o.date(r[0].i("started_at")) + ": " + joinRows(st, func(x row) string { return py.Str(x["status"]) + " " + py.Str(x["n"]) }) + " (/certs)"}
	low, err := rows(q, `SELECT host, days_left FROM cert_results WHERE run_id=? AND days_left IS NOT NULL AND days_left<30 ORDER BY days_left LIMIT 10`, r[0]["id"])
	if err != nil {
		return "", err
	}
	for _, x := range low {
		out = append(out, "- "+one(x["host"], 60)+": "+py.Str(x["days_left"])+" days left")
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) selfcheckSection() (string, error) {
	s, err := readMap(filepath.Join(o.StateDir, "selfcheck.json"))
	if err != nil {
		return "", err
	}
	var checks, bad []*py.OMap
	for _, c := range py.AsList(s.Get("checks")) {
		if m := py.AsMap(c); m != nil {
			checks = append(checks, m)
			if strings.ToUpper(py.Str(m.GetD("status", ""))) != "OK" {
				bad = append(bad, m)
			}
		}
	}
	out := []string{fmt.Sprintf("Argus self-check: %s, %d of %d parts OK (/)", py.Str(s.GetD("status", "?")), len(checks)-len(bad), len(checks))}
	for i, c := range bad {
		if i >= 12 {
			break
		}
		note := py.Or(c.Get("detail"), py.Or(c.Get("note"), ""))
		out = append(out, "- "+one(c.Get("name"), 50)+": "+py.Str(c.Get("status"))+" "+one(note, 120))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) proposalsSection(boardDB string) (string, error) {
	if boardDB == "" {
		return "", nil
	}
	if _, err := os.Stat(boardDB); err != nil {
		return "", nil
	}
	b, err := sql.Open("sqlite", "file:"+boardDB+"?mode=ro")
	if err != nil {
		return "", nil
	}
	defer b.Close()
	rs, err := rows(b, `SELECT path, state, count(*) n FROM proposals GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return "", err
	}
	out := []string{"Proposed tasks (path / state): " + orNone(joinRows(rs, func(r row) string {
		return py.Str(r["path"]) + "/" + py.Str(r["state"]) + " " + py.Str(r["n"])
	})) + " (/proposals)"}
	rej, err := rows(b, `SELECT title, reason FROM proposals WHERE state='rejected' AND reason!='' ORDER BY decided_at DESC LIMIT 10`)
	if err != nil {
		return "", err
	}
	for _, r := range rej {
		out = append(out, "- rejected: "+one(r["title"], 80)+": "+one(r["reason"], 120))
	}
	if t, err := rows(b, `SELECT coalesce(status,'?') s, count(*) n FROM tasks GROUP BY 1`); err == nil {
		out = append(out, "Task board: "+joinRows(t, func(r row) string { return py.Str(r["s"]) + " " + py.Str(r["n"]) })+" (/board)")
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) runsSection(q querier, since int64) (string, error) {
	rs, err := rows(q, `SELECT task, count(*) n, sum(status IN ('ERROR','FAIL')) bad, sum(coalesce(cost_usd,0)) c, avg(turns) t, avg(duration_ms) d, sum(coalesce(denials,0)) den FROM runs WHERE started_at>=? GROUP BY 1 ORDER BY 4 DESC`, since)
	if err != nil {
		return "", err
	}
	out := []string{"| task | runs | failed/error | cost | avg turns | avg duration | denials |", "|---|---|---|---|---|---|---|"}
	total := 0.0
	for _, r := range rs {
		out = append(out, fmt.Sprintf("| %s | %s | %s | $%s | %s | %s min | %s |", py.Str(r["task"]), py.Str(r["n"]), s0(r["bad"], "0"),
			py.F(py.Num(r["c"]), 2), py.F(py.Num(r["t"]), 0), py.F(py.Num(r["d"])/60000, 1), s0(r["den"], "0")))
		total += py.Num(r["c"])
	}
	return "Model and job runs in the last 30 days, total $" + py.F(total, 2) + " (/runs):\n\n" + strings.Join(out, "\n"), nil
}

func (o *Options) miscSection(q querier) (string, error) {
	var out []string
	if f, err := rows(q, `SELECT count(*) n FROM fix_runs`); err == nil {
		out = append(out, "Fix runs (fixer on develop/pre-release) ever: "+py.Str(f[0]["n"]))
	}
	if v, err := rows(q, `SELECT count(*) n, sum(monitored=0) nm, sum(coalesce(ssh_ok,0)=0) ns FROM vms WHERE gone=0`); err == nil {
		out = append(out, fmt.Sprintf("VM inventory: %s VMs, %s not in Prometheus, %s without read-only SSH (/inventory)", py.Str(v[0]["n"]),
			s0(v[0]["nm"], "0"), s0(v[0]["ns"], "0")))
	}
	return strings.Join(out, "\n"), nil
}

func (o *Options) ideasMD(q querier, cfg Config) (string, error) {
	ps := cfg.m.Map("personas")
	rs, err := rows(q, `SELECT * FROM ideas ORDER BY id DESC LIMIT 300`)
	if err != nil {
		return "", err
	}
	out := []string{"# Earlier ideas (do not propose these again; a rejected one only with changed evidence)", ""}
	if len(rs) == 0 {
		out = append(out, "(none yet)")
	}
	for _, r := range rs {
		name := py.Str(r["persona"])
		if d := ps.Map(name); d != nil {
			name = py.Str(d.GetD("name", name))
		}
		line := fmt.Sprintf("- #%s [%s] %s · %s · effort %s: **%s**: %s", py.Str(r["id"]), py.Str(r["status"]), name, py.Str(r["area"]),
			py.Str(r["effort"]), one(r["title"], 100), one(r["problem"], 220))
		if r.s("status") == "rejected" {
			line += " REJECTED " + o.date(r.i("decided_at")) + ": " + one(r["reason"], 200)
		}
		var links []string
		ev, _ := py.ParseJSON([]byte(s0(r["evidence"], "[]")))
		for _, e := range py.AsList(ev) {
			if m := py.AsMap(e); m != nil && py.Truthy(m.Get("link")) {
				links = append(links, py.Str(m.Get("link")))
			}
		}
		if len(links) > 0 {
			if len(links) > 6 {
				links = links[:6]
			}
			line += " Evidence: " + strings.Join(links, ", ")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n", nil
}

// Workspace writes the read-only workspace of one run and returns the adviser's agent name.
func (o *Options) Workspace(db querier, cfg Config, f RunFiles, persona, ws string) (string, error) {
	o.defaults()
	d := cfg.persona(persona)
	if d == nil {
		return "", errors.New("unknown persona")
	}
	agent := py.Str(d.Get("agent"))
	text, ok := f.agent(agent)
	if !agentRe.MatchString(agent) || !ok {
		return "", errors.New("persona agent missing")
	}
	if err := os.RemoveAll(ws); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(ws, ".claude", "agents"), 0o750); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(ws, "inputs"), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(ws, ".claude", "agents", agent+".md"), text, 0o640); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(ws, ".claude", "settings.json"), []byte(wsSettings), 0o640); err != nil {
		return "", err
	}
	now := o.Now().Unix()
	since := now - 30*86400
	stamp := time.Unix(now, 0).In(o.Loc)
	estate := []string{"# This estate, as Argus sees it (facts from its database and status files; redacted)", "",
		"Collected " + stamp.Format("2006-01-02 15:04 MST") + ". Links are Argus pages.", ""}
	estate = append(estate,
		section("Incidents", func() (string, error) { return o.incSection(db, since) }),
		section("RCA reports", func() (string, error) { return o.rcaSection(db, since) }),
		section("QA reports (chat triage)", func() (string, error) { return o.qaSection(db, since) }),
		section("Alerts and alert hygiene", o.hygieneSection),
		section("CI/CD", func() (string, error) { return o.cicdSection(db) }),
		section("Capacity", o.capacitySection),
		section("Vulnerabilities", func() (string, error) { return o.cveSection(db) }),
		section("Backups", func() (string, error) { return o.backupSection(db) }),
		section("Certificates", func() (string, error) { return o.certSection(db) }),
		section("Argus health", o.selfcheckSection),
		section("Proposed tasks and the task board", func() (string, error) { return o.proposalsSection(f.BoardDB) }),
		section("Agent runs and costs", func() (string, error) { return o.runsSection(db, since) }),
		section("Other", func() (string, error) { return o.miscSection(db) }))
	feats := defaultFeatures
	if b, ok := f.etc("ideas-features.md"); ok {
		feats = string(b)
	}
	im, err := o.ideasMD(db, cfg)
	if err != nil {
		return "", err
	}
	files := [][2]string{{"estate.md", clean(strings.Join(estate, "\n"), 0)}, {"features.md", clean(feats, 0)}, {"ideas.md", clean(im, 0)}}
	if b, ok := f.etc("ideas-job-model.md"); ok {
		files = append(files, [2]string{"job-model.md", clean(string(b), 0)})
	}
	for _, x := range files {
		if err := os.WriteFile(filepath.Join(ws, "inputs", x[0]), []byte(x[1]), 0o640); err != nil {
			return "", err
		}
	}
	kb := func(i int) string { return strconv.Itoa(py.Len(files[i][1]) / 1000) }
	index := []string{fmt.Sprintf("# Workspace of %s (%s), %s", py.Str(d.Get("name")), py.Str(d.Get("role")), stamp.Format("2006-01-02")), "",
		fmt.Sprintf("Your field this run: %s. Propose at most %d ideas.", py.Str(d.GetD("focus", "")), o.maxPerRun()), "",
		"| file | what | size |", "|---|---|---|",
		"| inputs/estate.md | incidents, RCA, QA, alerts, CI/CD, capacity, CVE, backups, certs, Argus health, proposals, runs and costs (last 30 days) | " + kb(0) + " kB |",
		"| inputs/features.md | what Argus already does | " + kb(1) + " kB |",
		"| inputs/ideas.md | every earlier idea with its status; rejected ones with the reason | " + kb(2) + " kB |"}
	if len(files) > 3 {
		index = append(index, "| inputs/job-model.md | the operator's job: recurring tasks, Argus's coverage and gaps (redacted summary) | "+kb(3)+" kB |")
	}
	index = append(index, "", "Read all of them (they are small), then answer with the JSON only.")
	return agent, os.WriteFile(filepath.Join(ws, "INDEX.md"), []byte(strings.Join(index, "\n")+"\n"), 0o640)
}

// inline is the workspace as one text, for the API provider (no tools: every input goes into the prompt).
func inline(ws, agent string) string {
	var b strings.Builder
	if t, err := os.ReadFile(filepath.Join(ws, ".claude", "agents", agent+".md")); err == nil {
		b.WriteString("\n\n# Your persona\n\n" + string(t))
	}
	for _, name := range []string{"INDEX.md", "inputs/estate.md", "inputs/features.md", "inputs/ideas.md", "inputs/job-model.md"} {
		if t, err := os.ReadFile(filepath.Join(ws, name)); err == nil {
			b.WriteString("\n\n# File " + name + "\n\n" + string(t))
		}
	}
	return b.String()
}

// --- validation, dedupe, store --------------------------------------------------------------------------------------

// ExtractJSON is the JSON object of the answer (inside a code fence, or from the first "{" to the last "}").
func ExtractJSON(text string) (any, error) {
	t := py.Strip(text)
	if m := fenceRe.FindStringSubmatch(t); m != nil {
		t = m[1]
	}
	i, j := strings.Index(t, "{"), strings.LastIndex(t, "}")
	if i < 0 || j <= i {
		return nil, errors.New("no JSON object in the answer")
	}
	v, err := py.ParseJSON([]byte(t[i : j+1]))
	if err != nil {
		return nil, errors.New("the answer is not valid JSON")
	}
	return v, nil
}

// Idea is one validated idea.
type Idea struct {
	F        map[string]string // title, problem, proposal, value, risk, prerequisites, success_metric, changed
	Area     string
	Effort   string
	Evidence []any // [{"text", "link"}] as *py.OMap
}

func field(raw *py.OMap, key string, lo, hi int, required bool) (string, error) {
	v := raw.Get(key)
	if l, ok := v.([]any); ok {
		var parts []string
		for _, x := range l {
			switch x.(type) {
			case string, int64, float64, bool:
				parts = append(parts, py.Str(x))
			}
		}
		v = strings.Join(parts, "; ")
	}
	if v == nil {
		v = ""
	}
	switch v.(type) {
	case string, int64, float64, bool:
	default:
		return "", errors.New(key + ": not text")
	}
	s := py.Str(v)
	if key == "title" {
		s = strings.Join(py.Split(s), " ")
	} else {
		s = py.Strip(s)
	}
	s = clean(s, 0)
	if !required && s == "" {
		return "", nil
	}
	if py.Len(s) < lo {
		return "", errors.New(key + ": too short")
	}
	if py.Len(s) <= hi {
		return s, nil
	}
	return py.Head(s, hi-1) + "…", nil
}

func validateOne(raw any, cfg Config) (Idea, error) {
	m, ok := raw.(*py.OMap)
	if !ok {
		return Idea{}, errors.New("not an object")
	}
	v := Idea{F: map[string]string{}}
	for _, f := range fields {
		s, err := field(m, f.key, f.lo, f.hi, f.key != "prerequisites" && f.key != "changed")
		if err != nil {
			return Idea{}, err
		}
		v.F[f.key] = s
	}
	area := strings.ToLower(py.Strip(s0(m.Get("area"), "")))
	areas := []string{}
	for _, a := range cfg.m.List("areas") {
		areas = append(areas, py.Str(a))
	}
	if !in(area, areas) {
		return Idea{}, errors.New("unknown area")
	}
	effort := py.Head(strings.ToUpper(py.Strip(s0(m.Get("effort"), ""))), 1)
	if !in(effort, effortSet) {
		return Idea{}, errors.New("effort must be S, M or L")
	}
	if l, ok := m.Get("evidence").([]any); ok {
		if len(l) > 8 {
			l = l[:8]
		}
		for _, e := range l {
			if s, ok := e.(string); ok {
				e = py.NewOMap("text", s)
			}
			em, ok := e.(*py.OMap)
			if !ok {
				continue
			}
			text := clean(strings.Join(py.Split(s0(em.Get("text"), "")), " "), 300)
			link := py.Strip(s0(em.Get("link"), ""))
			if !linkRe.MatchString(link) {
				link = ""
			}
			if text != "" || link != "" {
				v.Evidence = append(v.Evidence, py.NewOMap("text", text, "link", link))
			}
		}
	}
	if len(v.Evidence) == 0 {
		return Idea{}, errors.New("no evidence")
	}
	v.Area, v.Effort = area, effort
	if v.F["prerequisites"] == "" {
		v.F["prerequisites"] = "none"
	}
	return v, nil
}

// Validate returns the valid ideas of the answer (at most the cap) and the reasons for the rest.
func (o *Options) Validate(doc any, cfg Config) ([]Idea, []string, error) {
	var items []any
	switch d := doc.(type) {
	case *py.OMap:
		l, ok := d.Get("ideas").([]any)
		if !ok {
			return nil, nil, errors.New("no ideas list")
		}
		items = l
	case []any:
		items = d
	default:
		return nil, nil, errors.New("no ideas list")
	}
	if len(items) > 20 {
		items = items[:20]
	}
	var out []Idea
	var bad []string
	for i, raw := range items {
		v, err := validateOne(raw, cfg)
		if err != nil {
			bad = append(bad, fmt.Sprintf("idea %d: %s", i+1, err))
			continue
		}
		out = append(out, v)
	}
	if limit := int(o.maxPerRun()); len(out) > limit {
		bad = append(bad, fmt.Sprintf("%d over the limit of %d dropped", len(out)-limit, limit))
		out = out[:limit]
	}
	return out, bad, nil
}

func normTitle(t string) string {
	var ws []string
	for _, w := range strings.Fields(normRe.ReplaceAllString(strings.ToLower(t), " ")) {
		if !stop[w] {
			ws = append(ws, w)
		}
	}
	return strings.Join(ws, " ")
}

// ratio is difflib.SequenceMatcher(None, a, b).ratio() (autojunk on, as in the reference).
func ratio(a, b string) float64 {
	b2j := map[byte][]int{}
	for i := 0; i < len(b); i++ {
		b2j[b[i]] = append(b2j[b[i]], i)
	}
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for c, idx := range b2j {
			if len(idx) > ntest {
				delete(b2j, c)
			}
		}
	}
	longest := func(alo, ahi, blo, bhi int) (int, int, int) {
		bi, bj, bs := alo, blo, 0
		j2len := map[int]int{}
		for i := alo; i < ahi; i++ {
			nj := map[int]int{}
			for _, j := range b2j[a[i]] {
				if j < blo {
					continue
				}
				if j >= bhi {
					break
				}
				k := j2len[j-1] + 1
				nj[j] = k
				if k > bs {
					bi, bj, bs = i-k+1, j-k+1, k
				}
			}
			j2len = nj
		}
		for bi > alo && bj > blo && a[bi-1] == b[bj-1] {
			bi, bj, bs = bi-1, bj-1, bs+1
		}
		for bi+bs < ahi && bj+bs < bhi && a[bi+bs] == b[bj+bs] {
			bs++
		}
		return bi, bj, bs
	}
	matches := 0
	queue := [][4]int{{0, len(a), 0, len(b)}}
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		i, j, k := longest(q[0], q[1], q[2], q[3])
		if k > 0 {
			matches += k
			if q[0] < i && q[2] < j {
				queue = append(queue, [4]int{q[0], i, q[2], j})
			}
			if i+k < q[1] && j+k < q[3] {
				queue = append(queue, [4]int{i + k, q[1], j + k, q[3]})
			}
		}
	}
	if t := len(a) + len(b); t > 0 {
		return 2 * float64(matches) / float64(t)
	}
	return 1
}

// Similarity of two titles: the larger of the character ratio and the word overlap of the normalized titles.
func Similarity(a, b string) float64 {
	na, nb := normTitle(a), normTitle(b)
	if na == "" || nb == "" {
		return 0
	}
	ta, tb := map[string]bool{}, map[string]bool{}
	for _, w := range strings.Fields(na) {
		ta[w] = true
	}
	for _, w := range strings.Fields(nb) {
		tb[w] = true
	}
	inter := 0
	union := len(tb)
	for w := range ta {
		if tb[w] {
			inter++
		} else {
			union++
		}
	}
	return max(ratio(na, nb), float64(inter)/float64(union))
}

func evidenceHash(ev []any) string {
	set := map[string]bool{}
	for _, e := range ev {
		m := py.AsMap(e)
		set[s0(m.Get("link"), "")+"|"+spaceRe.ReplaceAllString(strings.ToLower(s0(m.Get("text"), "")), " ")] = true
	}
	keys := make([]any, 0, len(set))
	ks := make([]string, 0, len(set))
	for k := range set {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		keys = append(keys, k)
	}
	h := sha256.Sum256([]byte(py.Dumps(keys, -1)))
	return hex.EncodeToString(h[:])[:16]
}

func (o *Options) match(v Idea, rs []row) row {
	sameArea := o.envNum("IDEAS_SIMILARITY", 0.6, 0.3, 0.95, false)
	var best row
	score := 0.0
	for _, r := range rs {
		s := Similarity(v.F["title"], r.s("title"))
		if (s >= sameArea && r.s("area") == v.Area) || s >= 0.85 {
			if s > score {
				best, score = r, s
			}
		}
	}
	return best
}

func (o *Options) eventRun(q querier, ideaID any, kind, text, persona string, runID, cost, trigger any, at int64) error {
	_, err := q.Exec(`INSERT INTO idea_events (idea_id, persona, at, kind, actor, text, run_id, cost_usd, trigger) VALUES (?,?,?,?,?,?,?,?,?)`,
		ideaID, persona, at, kind, o.Actor, py.Head(text, 600), runID, cost, trigger)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Store inserts the new ideas and returns their ids and a note for each one skipped as a duplicate.
func (o *Options) Store(q querier, cfg Config, persona string, ideas []Idea) ([]int64, []string, error) {
	o.defaults()
	now := o.Now().Unix()
	name := persona
	if d := cfg.persona(persona); d != nil && d.Has("name") {
		name = py.Str(d.Get("name"))
	}
	rs, err := rows(q, `SELECT * FROM ideas`)
	if err != nil {
		return nil, nil, err
	}
	var added []int64
	var notes []string
	for _, v := range ideas {
		prev := o.match(v, rs)
		h := evidenceHash(v.Evidence)
		var revives int64
		if prev != nil {
			if prev.s("status") != "rejected" {
				if err := o.eventRun(q, prev["id"], "duplicate", name+" proposed it again: "+v.F["title"], persona, nil, nil, nil, now); err != nil {
					return nil, nil, err
				}
				notes = append(notes, fmt.Sprintf("duplicate of #%s (%s): %s", py.Str(prev["id"]), prev.s("status"), v.F["title"]))
				continue
			}
			if v.F["changed"] == "" || py.Len(v.F["changed"]) < 20 || h == prev.s("evidence_hash") {
				if err := o.eventRun(q, prev["id"], "suppressed", name+" proposed it again without new evidence; kept rejected: "+v.F["title"], persona, nil, nil, nil, now); err != nil {
					return nil, nil, err
				}
				notes = append(notes, fmt.Sprintf("rejected before as #%s, evidence unchanged: %s", py.Str(prev["id"]), v.F["title"]))
				continue
			}
			revives = prev.i("id")
		}
		var rv any
		if revives != 0 {
			rv = revives
		}
		res, err := q.Exec(`INSERT INTO ideas (persona, title, area, problem, evidence, proposal, value, effort, risk, prerequisites,
			success_metric, changed, revives, evidence_hash, status, run_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'new',?,?,?)`,
			persona, v.F["title"], v.Area, v.F["problem"], dumpsUnicode(v.Evidence), v.F["proposal"], v.F["value"], v.Effort, v.F["risk"],
			v.F["prerequisites"], v.F["success_metric"], nullable(v.F["changed"]), rv, h, nil, now, now)
		if err != nil {
			return nil, nil, err
		}
		id, _ := res.LastInsertId()
		text := "Proposed by " + name
		if revives != 0 {
			text += fmt.Sprintf("; revives rejected #%d: %s", revives, v.F["changed"])
		}
		if err := o.eventRun(q, id, "new", text, persona, nil, nil, nil, now); err != nil {
			return nil, nil, err
		}
		if revives != 0 {
			if err := o.eventRun(q, revives, "revived", fmt.Sprintf("%s brought it back as #%d: %s", name, id, v.F["changed"]), persona, nil, nil, nil, now); err != nil {
				return nil, nil, err
			}
		}
		added = append(added, id)
		nr, err := one1(q, `SELECT * FROM ideas WHERE id=?`, id)
		if err != nil {
			return nil, nil, err
		}
		rs = append(rs, nr)
	}
	return added, notes, nil
}

// Record validates and stores the answer of one run, writes the run's summary into its runs row and the run event,
// and rewrites the status file. It returns the summary.
func (o *Options) Record(db *sql.DB, cfg Config, persona, trigger string, res model.Result) (string, error) {
	o.defaults()
	d := cfg.persona(persona)
	if d == nil {
		return "", errors.New("unknown persona")
	}
	name := persona
	if d.Has("name") {
		name = py.Str(d.Get("name"))
	}
	var ideas []Idea
	var bad []string
	errText := ""
	doc, err := ExtractJSON(res.Text)
	if err == nil {
		ideas, bad, err = o.Validate(doc, cfg)
	}
	if err != nil {
		errText = err.Error()
	}
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	added, notes, err := o.Store(tx, cfg, persona, ideas)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	var summary string
	if errText != "" || res.IsError || (res.Text == "" && res.Error != "") {
		why := errText
		if why == "" {
			why = s0(res.Error, "no result")
		}
		summary = fmt.Sprintf("WARN: %s (%s) gave no usable answer: %s", name, persona, why)
	} else {
		summary = fmt.Sprintf("OK: %d new idea%s from %s (%s)", len(added), map[bool]string{true: "", false: "s"}[len(added) == 1], name, persona)
		if len(notes) > 0 {
			summary += fmt.Sprintf(", %d skipped as duplicates", len(notes))
		}
		if len(bad) > 0 {
			summary += fmt.Sprintf(", %d invalid", len(bad))
		}
	}
	detail := ""
	if len(notes)+len(bad) > 0 {
		for _, n := range append(notes, bad...) {
			detail += "\n- " + n
		}
	}
	var runID any
	if res.RunID != 0 {
		runID = res.RunID
		st := strings.SplitN(summary, ":", 2)[0]
		if _, err := db.Exec(`UPDATE runs SET status=?, summary=? WHERE id=?`, st, model.Redact(summary+detail, 4000), res.RunID); err != nil {
			return "", err
		}
	}
	tx, err = db.Begin()
	if err != nil {
		return "", err
	}
	if runID != nil && len(added) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(added)), ",")
		args := []any{runID}
		for _, id := range added {
			args = append(args, id)
		}
		if _, err := tx.Exec(`UPDATE ideas SET run_id=? WHERE id IN (`+ph+`)`, args...); err != nil {
			tx.Rollback()
			return "", err
		}
		if _, err := tx.Exec(`UPDATE idea_events SET run_id=? WHERE kind='new' AND idea_id IN (`+ph+`)`, args...); err != nil {
			tx.Rollback()
			return "", err
		}
	}
	if err := o.eventRun(tx, nil, "run", summary, persona, runID, res.CostUSD, trigger, o.Now().Unix()); err != nil {
		tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return summary + detail, o.WriteStatus(db, cfg, "")
}

// --- the run ------------------------------------------------------------------------------------------------------------

// Run is one adviser run: target "auto" (the adviser whose weekly slot is due, if any; trigger schedule) or an
// adviser's key ("Generate now"; trigger manual). It returns what happened in one line (and the skipped ideas).
func (o *Options) Run(ctx context.Context, db *sql.DB, r *model.Runner, f RunFiles, target string) (string, error) {
	o.defaults()
	if err := os.MkdirAll(o.StateDir, 0o750); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(o.StateDir, "ideas.lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return "ideas: another ideas run is active", nil
	}
	cfg := o.LoadConfig()
	persona, trigger := target, "manual"
	if target == "" || target == "auto" {
		if persona, err = o.Due(db, cfg); err != nil {
			return "", err
		}
		if persona == "" {
			return "ideas: nothing due", nil
		}
		trigger = "schedule"
	} else if !slotRe.MatchString(target) {
		return "", errors.New("the adviser must be auto or a key such as devops")
	}
	why, err := o.Check(db, cfg, persona, trigger)
	if err != nil || why != "" {
		return "ideas: " + why, err
	}
	l := o.Limits()
	ws := f.WorkDir
	if ws == "" {
		ws = filepath.Join(o.StateDir, "ideas-work")
	}
	agent, err := o.Workspace(db, cfg, f, persona, ws)
	if err != nil {
		o.WriteStatus(db, cfg, "")
		return "", err
	}
	prompt := o.RunPrompt(f)
	if r.O.Provider == model.API {
		prompt += inline(ws, agent)
	}
	o.WriteStatus(db, cfg, persona)
	res := r.Run(ctx, model.Request{Task: "ideas", Prompt: prompt, Model: l.Model, MaxTurns: int(l.Turns), BudgetUSD: l.BudgetUSD,
		Timeout: time.Duration(l.TimeoutMin) * time.Minute, Dir: ws, Agent: agent, Tools: []string{"Read", "Glob", "Grep"},
		Disallow: []string{"Bash", "Write", "Edit", "WebFetch", "WebSearch", "NotebookEdit"}})
	if res.Skipped != "" {
		return "ideas: skipped: " + res.Skipped, o.WriteStatus(db, cfg, "")
	}
	return o.Record(db, cfg, persona, trigger, res)
}

// dumpsUnicode is json.dumps(v, ensure_ascii=False): the default separators, non-ASCII kept as it is.
func dumpsUnicode(v any) string {
	var b strings.Builder
	var w func(any)
	w = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteByte('"')
			for _, r := range x {
				switch r {
				case '"':
					b.WriteString(`\"`)
				case '\\':
					b.WriteString(`\\`)
				case '\n':
					b.WriteString(`\n`)
				case '\r':
					b.WriteString(`\r`)
				case '\t':
					b.WriteString(`\t`)
				case '\b':
					b.WriteString(`\b`)
				case '\f':
					b.WriteString(`\f`)
				default:
					if r < 0x20 {
						fmt.Fprintf(&b, `\u%04x`, r)
					} else {
						b.WriteRune(r)
					}
				}
			}
			b.WriteByte('"')
		case []any:
			b.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					b.WriteString(", ")
				}
				w(e)
			}
			b.WriteByte(']')
		case *py.OMap:
			b.WriteByte('{')
			for i, k := range x.Keys() {
				if i > 0 {
					b.WriteString(", ")
				}
				w(k)
				b.WriteString(": ")
				w(x.Get(k))
			}
			b.WriteByte('}')
		default:
			b.WriteString(py.Dumps(x, -1))
		}
	}
	w(v)
	return b.String()
}
