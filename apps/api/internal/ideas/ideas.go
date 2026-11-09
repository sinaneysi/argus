// Package ideas is the decision side of Ideas (the port of the earlier installation's ideas job): the advisers'
// ideas are explored, accepted, rejected or marked built on the Ideas review (request files in
// settings-in/idea-actions); an accepted idea gets a deterministic build prompt for a coding agent; the status file
// ideas-status.json tells the page the switch, the advisers' schedule, their last runs and the caps.
//
// Configuration: FEATURE_IDEAS and the IDEAS_* keys; the advisers, areas and the values the build prompt names in
// ARGUS_ETC_DIR/ideas.json (default: the embedded default.json); the build prompt template in
// ARGUS_ETC_DIR/ideas-build.md (default: the embedded build.md).
//
// Tables (ops.db, migration 0001): ideas, idea_events.
//
// Not ported yet: the advisers' model run (the workspace of facts, the run, the validation and dedupe of the
// answer) and the optional model refinement of the build prompt (IDEAS_REFINE; the status says it is off).
package ideas

import (
	"database/sql"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/py"
)

//go:embed default.json
var defaultConfig []byte

//go:embed build.md
var defaultTemplate string

// Options configure the job.
type Options struct {
	StateDir   string              // ideas-status.json
	ActionsDir string              // settings-in/idea-actions
	ConfigFile string              // the advisers and build values ("" or missing: the embedded default)
	Template   string              // the build prompt template file ("" or missing: the embedded default)
	Env        func(string) string // FEATURE_IDEAS, IDEAS_*
	Actor      string              // the actor of automatic events ("argus")
	Loc        *time.Location      // the day boundaries and dates (ARGUS_TZ)
	Now        func() time.Time
	Log        *slog.Logger
}

func (o *Options) defaults() {
	if o.Actor == "" {
		o.Actor = "argus"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Loc == nil {
		o.Loc = time.UTC
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

func (o *Options) env(k string) (string, bool) {
	if o.Env == nil {
		return "", false
	}
	v := o.Env(k)
	return v, v != ""
}

var (
	days       = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	userRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}\n?\z`)
	actionName = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{8}\.json\n?\z`)
	refRe      = regexp.MustCompile(`^(?:[0-9a-f]{7,40}|![0-9]{1,7}|https://[A-Za-z0-9.-]{3,80}/[A-Za-z0-9_./~%+-]{1,200}` +
		`(?:/-/(?:merge_requests|commit)/[0-9a-f]{1,40})?)\n?\z`)
	ctrlAll  = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	ctrlKeep = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)
	slotRe   = regexp.MustCompile(`^[a-z]{2,12}$`)
	placeRe  = regexp.MustCompile(`\{\{([a-z_]+)\}\}`)
	blankRe  = regexp.MustCompile(`\n{3,}`)
)

const (
	minReason, maxReason = 3, 300
	maxTick              = 200
)

// envNum is the reference's env_num: the value (or the default when it does not parse), clamped.
func (o *Options) envNum(name string, def, lo, hi float64, integer bool) float64 {
	v := def
	if s, ok := o.env(name); ok {
		if integer {
			if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				v = float64(n)
			}
		} else if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			v = f
		}
	}
	return min(max(v, lo), hi)
}

// Enabled is FEATURE_IDEAS=1.
func (o *Options) Enabled() bool { v, _ := o.env("FEATURE_IDEAS"); return v == "1" }

func (o *Options) maxPerRun() int64 { return int64(o.envNum("IDEAS_MAX_PER_RUN", 5, 1, 10, true)) }

// Limits are the caps of an adviser run: turns, budget (USD), timeout (minutes), model, ideas.
type Limits struct {
	Turns, TimeoutMin, MaxIdeas int64
	BudgetUSD                   float64
	Model                       string
}

// Limits of one adviser run, validated and clamped.
func (o *Options) Limits() Limits {
	m, _ := o.env("IDEAS_MODEL")
	if !regexp.MustCompile(`^[a-z0-9.-]{2,40}$`).MatchString(m) {
		m = "sonnet"
	}
	b, _ := strconv.ParseFloat(py.F(o.envNum("IDEAS_BUDGET_USD", 0.60, 0.05, 2.0, false), 2), 64)
	return Limits{Turns: int64(o.envNum("IDEAS_MAX_TURNS", 25, 5, 60, true)), BudgetUSD: b,
		TimeoutMin: int64(o.envNum("IDEAS_TIMEOUT_MIN", 15, 2, 30, true)), Model: m, MaxIdeas: o.maxPerRun()}
}

// --- configuration of the advisers -------------------------------------------------------------------------------

// Config is ideas.json.
type Config struct{ m *py.OMap }

// LoadConfig reads the advisers' file (the embedded default when it is missing or does not parse) and applies
// IDEAS_SCHEDULE.
func (o *Options) LoadConfig() Config {
	var m *py.OMap
	if o.ConfigFile != "" {
		if b, err := os.ReadFile(o.ConfigFile); err == nil {
			if v, err := py.ParseJSON(b); err == nil {
				m, _ = v.(*py.OMap)
			}
		}
	}
	if m == nil {
		v, _ := py.ParseJSON(defaultConfig)
		m = v.(*py.OMap)
	}
	sched, _ := o.env("IDEAS_SCHEDULE")
	sc := parseSchedule(sched)
	ps := m.Map("personas")
	for _, p := range ps.Keys() {
		d := ps.Map(p)
		if d == nil {
			continue
		}
		var ds []any
		if v, ok := sc[p]; ok {
			for _, x := range v {
				ds = append(ds, x)
			}
		} else {
			day := ""
			if py.Truthy(d.Get("day")) {
				day = py.Str(d.Get("day"))
			}
			for _, x := range strings.Split(day, "+") {
				if in(x, days) {
					ds = append(ds, x)
				}
			}
		}
		if ds == nil {
			ds = []any{}
		}
		d.Set("days", ds)
	}
	return Config{m}
}

func parseSchedule(text string) map[string][]string {
	out := map[string][]string{}
	for _, part := range strings.Split(text, ",") {
		k, v, _ := strings.Cut(py.Strip(part), "=")
		if !slotRe.MatchString(k) {
			continue
		}
		if strings.ToLower(py.Strip(v)) == "off" {
			out[k] = []string{}
			continue
		}
		var ds []string
		for _, x := range strings.Split(py.Strip(v), "+") {
			if in(x, days) {
				ds = append(ds, x)
			}
		}
		if len(ds) > 0 {
			out[k] = ds
		}
	}
	return out
}

func in(s string, set []string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

func (c Config) persona(p string) *py.OMap { return c.m.Map("personas").Map(p) }

// --- text ------------------------------------------------------------------------------------------------------------

// clean replaces control characters (not tab or newline), redacts secrets and caps with an ellipsis.
func clean(text any, n int) string {
	t := ""
	if py.Truthy(text) {
		t = py.Str(text)
	}
	t, _ = notify.Redact(ctrlKeep.ReplaceAllString(t, " "), nil)
	if n <= 0 || py.Len(t) <= n {
		return t
	}
	return py.Head(t, n-1) + "…"
}

func one(text any, n int) string {
	t := ""
	if py.Truthy(text) {
		t = py.Str(text)
	}
	return strings.ReplaceAll(clean(strings.Join(py.Split(t), " "), n), "|", "/")
}

func (o *Options) date(ts int64) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).In(o.Loc).Format("2006-01-02")
}

func (o *Options) dayStart(ts int64) int64 {
	t := time.Unix(ts, 0).In(o.Loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, o.Loc).Unix()
}

// --- rows -------------------------------------------------------------------------------------------------------------

type row map[string]any

func (r row) s(k string) string {
	if r == nil || r[k] == nil {
		return ""
	}
	return py.Str(r[k])
}
func (r row) i(k string) int64 { return py.I(r[k]) }

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	Exec(string, ...any) (sql.Result, error)
}

func rows(q querier, query string, args ...any) ([]row, error) {
	rs, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []row
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := row{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func one1(q querier, query string, args ...any) (row, error) {
	r, err := rows(q, query, args...)
	if err != nil || len(r) == 0 {
		return nil, err
	}
	return r[0], nil
}

func (o *Options) event(q querier, ideaID any, kind, actor, text string, persona any, cost any, trigger any, at int64) error {
	_, err := q.Exec(`INSERT INTO idea_events (idea_id, persona, at, kind, actor, text, run_id, cost_usd, trigger) VALUES (?,?,?,?,?,?,?,?,?)`,
		ideaID, persona, at, kind, actor, py.Head(text, 600), nil, cost, trigger)
	return err
}

// --- the build prompt ---------------------------------------------------------------------------------------------

func (o *Options) template() string {
	if o.Template != "" {
		if b, err := os.ReadFile(o.Template); err == nil && utf8.Valid(b) {
			return string(b)
		}
	}
	return defaultTemplate
}

var efforts = map[string]string{"S": "S (under a week)", "M": "M (one to three weeks)", "L": "L (more than three weeks)"}

// BuildPrompt is the deterministic build prompt of an idea row: the template with the idea and the build values.
func (o *Options) BuildPrompt(r row, cfg Config, acceptedBy string, acceptedAt int64, revived string) string {
	o.defaults()
	b := cfg.m.Map("build")
	p := cfg.persona(r.s("persona"))
	vals := map[string]string{}
	for _, k := range b.Keys() {
		vals[k] = py.Str(b.Get(k))
	}
	url := strings.TrimRight(vals["public_url"], "/")
	var ev []any
	if v, err := py.ParseJSON([]byte(py.OrS(r["evidence"], "[]"))); err == nil {
		ev = py.AsList(v)
	}
	var lines []string
	for _, x := range ev {
		e, _ := x.(*py.OMap)
		t := py.Or(e.Get("text"), e.Get("link"))
		line := "- " + py.Str(t)
		if py.Truthy(e.Get("link")) {
			line += ": " + url + py.Str(e.Get("link"))
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{"- (none)"}
	}
	ap := cfg.m.Map("area_paths")
	paths := py.AsList(ap.Get(r.s("area")))
	if len(paths) == 0 {
		paths = py.AsList(ap.Get("other"))
	}
	var pl []string
	for _, x := range paths {
		pl = append(pl, "- `"+py.Str(x)+"`")
	}
	title := r.s("title")
	want := "to " + strings.ToLower(py.Head(title, 1)) + py.Slice(title, 1)
	changed := ""
	if py.Truthy(r["changed"]) {
		changed = "It was rejected before as idea #" + py.Str(r["revives"])
		if revived != "" {
			changed += " (" + revived + ")"
		}
		changed += "; it came back because the evidence changed: " + r.s("changed") + "\n"
	}
	soThat := ""
	if py.Truthy(r["value"]) {
		soThat = py.Str(r["value"])
	}
	if rs := []rune(soThat); len(rs) > 1 && !(unicode.IsUpper(rs[0]) && unicode.IsUpper(rs[1])) {
		soThat = strings.ToLower(string(rs[0])) + string(rs[1:])
	}
	eff := r.s("effort")
	if e, ok := efforts[eff]; ok {
		eff = e
	}
	pre := r.s("prerequisites")
	if !py.Truthy(r["prerequisites"]) {
		pre = "none"
	}
	pname := r.s("persona")
	prole := ""
	if p != nil {
		if p.Has("name") {
			pname = py.Str(p.Get("name"))
		}
		if p.Has("role") {
			prole = py.Str(p.Get("role"))
		}
	}
	for k, v := range map[string]string{"title": title, "id": r.s("id"), "goal": title + ". Done when: " + r.s("success_metric"),
		"want": want, "so_that": soThat, "persona_name": pname, "persona_role": prole, "created": o.date(r.i("created_at")),
		"accepted_by": acceptedBy, "accepted": o.date(acceptedAt), "changed": changed, "problem": r.s("problem"),
		"evidence": strings.Join(lines, "\n"), "proposal": r.s("proposal"), "value": r.s("value"), "effort": eff,
		"risk": r.s("risk"), "prerequisites": pre, "success_metric": r.s("success_metric"), "area": r.s("area"),
		"area_paths": strings.Join(pl, "\n"), "public_url": url} {
		vals[k] = v
	}
	text := placeRe.ReplaceAllStringFunc(o.template(), func(m string) string {
		if v, ok := vals[m[2:len(m)-2]]; ok {
			return v
		}
		return m
	})
	text = blankRe.ReplaceAllString(text, "\n\n")
	return clean(strings.TrimRightFunc(text, py.IsSpaceRune)+"\n", 0)
}

// --- requests from the app ------------------------------------------------------------------------------------------

// Request is one validated request.
type Request struct {
	Action, By, Reason, Ref string
	ID                      int64
}

// ReadRequest reads and validates one request file (admins only; no symbolic link; at most 4096 bytes).
func ReadRequest(p string) (Request, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Request{}, err
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, 4097))
	if len(raw) > 4096 {
		return Request{}, fmt.Errorf("too large")
	}
	if !utf8.Valid(raw) {
		return Request{}, fmt.Errorf("not UTF-8")
	}
	v, err := py.ParseJSON(raw)
	if err != nil {
		return Request{}, err
	}
	m, ok := v.(*py.OMap)
	if !ok {
		return Request{}, fmt.Errorf("not an object")
	}
	action, _ := m.Get("action").(string)
	by := ""
	if py.Truthy(m.Get("by")) {
		by = py.Str(m.Get("by"))
	}
	if !in(action, []string{"explore", "accept", "reject", "built"}) || !userRe.MatchString(by) || m.Get("role") != "admin" {
		return Request{}, fmt.Errorf("bad fields (admins only)")
	}
	id, ok := m.Get("id").(int64)
	if !ok || id <= 0 || id >= 1_000_000_000 {
		return Request{}, fmt.Errorf("bad id")
	}
	reason, ref := "", ""
	if py.Truthy(m.Get("reason")) {
		reason = strings.Join(py.Split(ctrlAll.ReplaceAllString(py.Str(m.Get("reason")), " ")), " ")
	}
	if py.Truthy(m.Get("ref")) {
		ref = py.Strip(py.Str(m.Get("ref")))
	}
	if action == "reject" && !(py.Len(reason) >= minReason && py.Len(reason) <= maxReason) {
		return Request{}, fmt.Errorf("a reject needs a reason (3 to 300 characters)")
	}
	if action == "built" && !refRe.MatchString(ref) {
		return Request{}, fmt.Errorf("built needs a commit hash, !MR number or an https link")
	}
	return Request{Action: action, ID: id, By: by, Reason: py.Head(reason, maxReason), Ref: py.Head(ref, 300)}, nil
}

var allowed = map[string][]string{"explore": {"new"}, "accept": {"new", "review"}, "reject": {"new", "review", "accepted"}, "built": {"accepted"}}

// ApplyRequest applies one request inside the caller's transaction: what was done, or why nothing was.
func (o *Options) ApplyRequest(tx *sql.Tx, req Request, cfg Config) (string, error) {
	o.defaults()
	now := o.Now().Unix()
	r, err := one1(tx, `SELECT * FROM ideas WHERE id=?`, req.ID)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "no such idea", nil
	}
	if !in(r.s("status"), allowed[req.Action]) {
		return "not applied: the idea is " + r.s("status"), nil
	}
	persona := r["persona"]
	switch req.Action {
	case "explore":
		if _, err := tx.Exec(`UPDATE ideas SET status='review', updated_at=? WHERE id=?`, now, req.ID); err != nil {
			return "", err
		}
		err = o.event(tx, req.ID, "explore", req.By, "Under review", persona, nil, nil, now)
	case "reject":
		if _, err := tx.Exec(`UPDATE ideas SET status='rejected', decided_at=?, decided_by=?, reason=?, updated_at=? WHERE id=?`, now, req.By, req.Reason, now, req.ID); err != nil {
			return "", err
		}
		err = o.event(tx, req.ID, "reject", req.By, req.Reason, persona, nil, nil, now)
	case "built":
		if _, err := tx.Exec(`UPDATE ideas SET status='built', built_at=?, built_by=?, built_ref=?, updated_at=? WHERE id=?`, now, req.By, req.Ref, now, req.ID); err != nil {
			return "", err
		}
		err = o.event(tx, req.ID, "built", req.By, req.Ref, persona, nil, nil, now)
	case "accept":
		if _, err := tx.Exec(`UPDATE ideas SET status='accepted', decided_at=?, decided_by=?, reason=NULL, updated_at=? WHERE id=?`, now, req.By, now, req.ID); err != nil {
			return "", err
		}
		if err := o.event(tx, req.ID, "accept", req.By, "Accepted", persona, nil, nil, now); err != nil {
			return "", err
		}
		if r, err = one1(tx, `SELECT * FROM ideas WHERE id=?`, req.ID); err != nil {
			return "", err
		}
		revived := ""
		if r.i("revives") != 0 {
			if old, _ := one1(tx, `SELECT reason FROM ideas WHERE id=?`, r.i("revives")); old != nil && py.Truthy(old["reason"]) {
				revived = "reason then: " + old.s("reason")
			}
		}
		text := o.BuildPrompt(r, cfg, req.By, now, revived)
		if _, err := tx.Exec(`UPDATE ideas SET build_prompt=?, prompt_at=?, prompt_refined=? WHERE id=?`, text, now, 0, req.ID); err != nil {
			return "", err
		}
		err = o.event(tx, req.ID, "prompt", o.Actor, "Build prompt written", persona, nil, "template", now)
	}
	return req.Action, err
}

// Apply applies the request files (at most 200, each in its own transaction), removes them and rewrites the
// status file.
func (o *Options) Apply(db *sql.DB) (int, error) {
	o.defaults()
	cfg := o.LoadConfig()
	ents, _ := os.ReadDir(o.ActionsDir)
	n := 0
	for i, e := range ents {
		if i >= maxTick {
			break
		}
		name := e.Name()
		p := filepath.Join(o.ActionsDir, name)
		if !actionName.MatchString(name) {
			if !strings.HasPrefix(name, ".") {
				os.Remove(p)
			}
			continue
		}
		req, err := ReadRequest(p)
		if err == nil {
			var tx *sql.Tx
			if tx, err = db.Begin(); err != nil {
				return n, err
			}
			res, aerr := o.ApplyRequest(tx, req, cfg)
			if aerr != nil {
				tx.Rollback()
				return n, aerr
			}
			if err := tx.Commit(); err != nil {
				return n, err
			}
			n++
			o.Log.Info("idea request", "file", name, "action", req.Action, "id", req.ID, "by", req.By, "result", res)
		} else {
			o.Log.Warn("idea request refused", "file", name, "error", err.Error())
		}
		os.Remove(p)
	}
	return n, o.WriteStatus(db, cfg, "")
}

// --- status for the app -----------------------------------------------------------------------------------------------

// WriteStatus rewrites ideas-status.json: the switch, each adviser's schedule and last run, the caps.
func (o *Options) WriteStatus(db querier, cfg Config, running string) error {
	o.defaults()
	now := o.Now().Unix()
	today := o.dayStart(now)
	personas := py.NewOMap()
	ps := cfg.m.Map("personas")
	for _, p := range ps.Keys() {
		d := ps.Map(p)
		if d == nil {
			continue
		}
		last, err := one1(db, `SELECT * FROM idea_events WHERE kind='run' AND persona=? ORDER BY at DESC LIMIT 1`, p)
		if err != nil {
			return err
		}
		lm, err := one1(db, `SELECT max(at) m FROM idea_events WHERE kind='run' AND persona=? AND trigger='manual'`, p)
		if err != nil {
			return err
		}
		e := py.NewOMap("name", p, "role", "", "days", d.Get("days"))
		if d.Has("name") {
			e.Set("name", d.Get("name"))
		}
		if d.Has("role") {
			e.Set("role", d.Get("role"))
		}
		if last != nil {
			c, _ := strconv.ParseFloat(py.F(py.Num(last["cost_usd"]), 4), 64)
			e.Set("last_run", last["at"]).Set("last_trigger", last["trigger"]).Set("last_result", py.Head(last.s("text"), 200)).Set("last_cost", c)
		} else {
			e.Set("last_run", nil).Set("last_trigger", nil).Set("last_result", nil).Set("last_cost", nil)
		}
		e.Set("manual_today", lm.i("m") >= today)
		personas.Set(p, e)
	}
	var run any
	if running != "" {
		run = running
	}
	l := o.Limits()
	doc := py.NewOMap("at", now, "enabled", o.Enabled(), "personas", personas, "running", run, "refine", false,
		"caps", py.NewOMap("budget_usd", l.BudgetUSD, "max_turns", l.Turns, "max_ideas", l.MaxIdeas))
	path := filepath.Join(o.StateDir, "ideas-status.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(py.Dumps(doc, -1)), 0o644); err != nil {
		return err
	}
	os.Chmod(tmp, 0o644)
	return os.Rename(tmp, path)
}
