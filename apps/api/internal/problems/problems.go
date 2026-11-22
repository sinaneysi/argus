// Package problems is the problem record job (the port of the earlier installation's problem records). A problem
// (shown as P-<id>) is the root-cause record behind several issues: incidents, QA issues, CVE groups, CI/CD
// findings, capacity and VM findings and the findings of a run. Links between a problem and anything are typed
// (link_type), decided (state) and attributed (source, by, confidence, reason). This job is the only writer of the
// three tables; the web app reads them and writes request files (settings-in/problem-actions).
//
// Tick applies the app's requests (create, link, unlink, accept, reject, primary, status, edit, merge, resolve),
// closes problems resolved for PROBLEMS_CLOSE_DAYS and writes the state file problems.json. Suggest is the
// deterministic link suggester (suggest.go): suggestions only, unless PROBLEMS_AUTO_ACCEPT=1. Nothing is applied
// while FEATURE_PROBLEMS is not 1 (the requests are removed, the state file says so).
//
// Tables (ops.db, migration 0001): problems, problem_links, problem_events.
//
// Not ported: the one-time backfill from earlier RCA reports and the purge (operator commands of the earlier
// installation).
package problems

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/notify"
	"github.com/sinaneysi/argus/internal/py"
)

// Options configure the job.
type Options struct {
	StateDir    string              // problems.json, capacity.json, service-map.json, the lock
	ActionsDir  string              // settings-in/problem-actions
	AdminFile   string              // the admin overrides of the keys marked admin (JSON object; "": none)
	ServiceMaps []string            // service map files, the first that parses is used ("": StateDir/service-map.json)
	Env         func(string) string // FEATURE_PROBLEMS and the PROBLEMS_* keys ("" or nil: the defaults)
	Actor       string              // the actor and source of automatic decisions ("argus"; auto-accept: "<actor>-auto")
	// HostPrefixes are the prefixes of bare host names in text (such as "web-"): name:port upstreams and host names
	HostPrefixes []string
	SkipHosts    []string // host names never taken as a feature (the own, chat and code hosts)
	Incidents    *incidents.Options
	Now          func() time.Time
	Log          *slog.Logger

	cfg      *Config
	smap     *py.OMap
	smapRead bool
	hostRe   *regexp.Regexp
	namePort *regexp.Regexp
}

func (o *Options) defaults() {
	if o.Actor == "" {
		o.Actor = "argus"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.ServiceMaps == nil {
		o.ServiceMaps = []string{filepath.Join(o.StateDir, "service-map.json")}
	}
	if o.hostRe == nil {
		o.hostRe, o.namePort = hostRegexps(o.HostPrefixes)
	}
}

func (o *Options) now() int64 { return o.Now().Unix() }

// Invalid is a refused request or operation; nothing it touched is kept.
type Invalid struct{ msg string }

func (e *Invalid) Error() string { return e.msg }

func invalid(f string, a ...any) error { return &Invalid{fmt.Sprintf(f, a...)} }

// --- configuration ----------------------------------------------------------------------------------------------

// IssueTypes are the issue kinds a problem is caused by.
var IssueTypes = []string{"incident", "qa_issue", "cve_group", "cicd_finding", "capacity", "vm_report", "finding"}

var envClasses = []string{"dev", "staging", "production"}

type key struct {
	typ   string // bool int float list
	def   any
	lo    float64
	hi    float64
	set   []string
	admin bool
}

var keys = map[string]key{
	"FEATURE_PROBLEMS":                {typ: "bool", def: false},
	"PROBLEMS_SOURCES":                {typ: "list", def: IssueTypes, set: IssueTypes},
	"PROBLEMS_SUGGEST":                {typ: "bool", def: true, admin: true},
	"PROBLEMS_SUGGEST_LOOKBACK_DAYS":  {typ: "int", def: int64(14), lo: 1, hi: 90},
	"PROBLEMS_SUGGEST_WINDOW_MIN":     {typ: "int", def: int64(120), lo: 5, hi: 10080, admin: true},
	"PROBLEMS_SUGGEST_MIN_CONFIDENCE": {typ: "float", def: 0.60, lo: 0.30, hi: 0.99, admin: true},
	"PROBLEMS_SUGGEST_MAX_PER_ITEM":   {typ: "int", def: int64(3), lo: 1, hi: 10},
	"PROBLEMS_FANOUT_MAX":             {typ: "int", def: int64(20), lo: 2, hi: 1000},
	"PROBLEMS_REJECT_DAYS":            {typ: "int", def: int64(30), lo: 1, hi: 365},
	"PROBLEMS_AUTO_ACCEPT":            {typ: "bool", def: false, admin: true},
	"PROBLEMS_AUTO_ACCEPT_CONFIDENCE": {typ: "float", def: 0.95, lo: 0.90, hi: 0.99, admin: true},
	"PROBLEMS_AUTO_ACCEPT_CLASSES":    {typ: "list", def: []string{"dev", "staging"}, set: envClasses, admin: true},
	"PROBLEMS_REOPEN_DAYS":            {typ: "int", def: int64(30), lo: 1, hi: 365},
	"PROBLEMS_CLOSE_DAYS":             {typ: "int", def: int64(14), lo: 1, hi: 365},
}

// Config is the effective configuration.
type Config struct {
	Enabled, Suggest, AutoAccept                               bool
	Sources, AutoClasses                                       []string
	LookbackDays, WindowMin, MaxPerItem, FanoutMax, RejectDays int64
	ReopenDays, CloseDays                                      int64
	MinConfidence, AutoConfidence                              float64
	raw                                                        map[string]any
}

func parseKey(name string, raw *string) any {
	k := keys[name]
	if raw == nil || py.Strip(*raw) == "" {
		return k.def
	}
	s := py.Strip(*raw)
	switch k.typ {
	case "bool":
		switch strings.ToLower(s) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case "int":
		if v, err := strconv.ParseInt(strings.TrimPrefix(s, "+"), 10, 64); err == nil && float64(v) >= k.lo && float64(v) <= k.hi {
			return v
		}
	case "float":
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= k.lo && v <= k.hi {
			return v
		}
	case "list":
		var items []string
		for _, x := range strings.Split(s, ",") {
			if x = py.Strip(x); x != "" {
				items = append(items, x)
			}
		}
		if len(items) == 0 {
			return k.def
		}
		for _, x := range items {
			if !in(x, k.set) {
				return k.def
			}
		}
		return items
	}
	return k.def
}

// LoadConfig is the effective configuration: the environment, then the admin overrides for the keys marked admin.
func (o *Options) LoadConfig() *Config {
	raw := map[string]any{}
	for name := range keys {
		var v *string
		if o.Env != nil {
			if s := o.Env(name); s != "" {
				v = &s
			}
		}
		raw[name] = parseKey(name, v)
	}
	if o.AdminFile != "" {
		if b, err := os.ReadFile(o.AdminFile); err == nil {
			if m, ok := mustJSON(b).(*py.OMap); ok {
				for _, k := range m.Keys() {
					if kk, ok := keys[k]; ok && kk.admin {
						var s string
						switch v := m.Get(k).(type) {
						case []any:
							parts := make([]string, len(v))
							for i, x := range v {
								parts[i] = py.Str(x)
							}
							s = strings.Join(parts, ",")
						case bool:
							s = map[bool]string{true: "1", false: "0"}[v]
						default:
							s = py.Str(v)
							if v == nil {
								s = ""
							}
						}
						raw[k] = parseKey(k, &s)
					}
				}
			}
		}
	}
	c := &Config{raw: raw}
	c.Enabled, c.Suggest, c.AutoAccept = raw["FEATURE_PROBLEMS"].(bool), raw["PROBLEMS_SUGGEST"].(bool), raw["PROBLEMS_AUTO_ACCEPT"].(bool)
	c.Sources, c.AutoClasses = raw["PROBLEMS_SOURCES"].([]string), raw["PROBLEMS_AUTO_ACCEPT_CLASSES"].([]string)
	c.LookbackDays, c.WindowMin = raw["PROBLEMS_SUGGEST_LOOKBACK_DAYS"].(int64), raw["PROBLEMS_SUGGEST_WINDOW_MIN"].(int64)
	c.MaxPerItem, c.FanoutMax = raw["PROBLEMS_SUGGEST_MAX_PER_ITEM"].(int64), raw["PROBLEMS_FANOUT_MAX"].(int64)
	c.RejectDays, c.ReopenDays, c.CloseDays = raw["PROBLEMS_REJECT_DAYS"].(int64), raw["PROBLEMS_REOPEN_DAYS"].(int64), raw["PROBLEMS_CLOSE_DAYS"].(int64)
	c.MinConfidence, c.AutoConfidence = raw["PROBLEMS_SUGGEST_MIN_CONFIDENCE"].(float64), raw["PROBLEMS_AUTO_ACCEPT_CONFIDENCE"].(float64)
	return c
}

func (o *Options) config() *Config {
	if o.cfg == nil {
		o.cfg = o.LoadConfig()
	}
	return o.cfg
}

func mustJSON(b []byte) any {
	v, err := py.ParseJSON(b)
	if err != nil {
		return nil
	}
	return v
}

// --- reference and link types -----------------------------------------------------------------------------------

// pyRe compiles a pattern whose final "$" behaves like Python's (the end, or before a final newline).
func pyRe(p string) *regexp.Regexp {
	if strings.HasSuffix(p, "$") {
		p = strings.TrimSuffix(p, "$") + `\n?\z`
	}
	return regexp.MustCompile(p)
}

const num = `^[0-9]{1,9}$`

type refType struct {
	re    *regexp.Regexp
	links []string
}

var refTypes = map[string]refType{
	"incident":     {pyRe(num), []string{"caused_by", "related_to"}},
	"qa_issue":     {pyRe(num), []string{"caused_by", "related_to"}},
	"cve_group":    {pyRe(`^[A-Za-z0-9:._@/+ -]{1,80}\|[A-Za-z0-9:._@/+-]{1,214}\|(?:production|staging|dev)$`), []string{"caused_by", "related_to"}},
	"cicd_finding": {pyRe(num), []string{"caused_by", "related_to"}},
	"capacity":     {pyRe(`^[A-Za-z0-9._-]{1,80}\|/[A-Za-z0-9._/-]{0,120}$`), []string{"caused_by", "related_to"}},
	"vm_report":    {pyRe(num), []string{"caused_by", "related_to"}},
	"finding":      {pyRe(`^[0-9]{1,9}:[0-9]{1,3}$`), []string{"caused_by", "related_to"}},
	"rca":          {pyRe(num), []string{"explains", "related_to"}},
	"proposal":     {pyRe(num), []string{"fixed_by"}},
	"task":         {pyRe(`^(?:[0-9]{1,9}|[A-Z][A-Z0-9]{1,9}-[0-9]{1,7})$`), []string{"fixed_by"}},
	"fix_run":      {pyRe(`^[0-9]{14}-[0-9a-f]{8}$`), []string{"fixed_by"}},
	"deploy":       {pyRe(num), []string{"fixed_by"}},
	"mr":           {pyRe(`^[0-9]{1,9}![0-9]{1,7}$`), []string{"fixed_by"}},
	"problem":      {pyRe(num), []string{"duplicate_of", "blocks", "related_to"}},
}

var (
	linkTypes    = []string{"caused_by", "explains", "fixed_by", "related_to", "duplicate_of", "blocks"}
	statuses     = []string{"new", "investigating", "known_error", "fix_in_progress", "resolved", "closed"}
	openStatuses = []string{"new", "investigating", "known_error", "fix_in_progress"}
	labels       = []string{"proven", "probable", "unproven", ""}
	humanMoves   = map[string][]string{
		"new":             {"investigating", "known_error", "fix_in_progress", "resolved", "closed"},
		"investigating":   {"known_error", "fix_in_progress", "resolved", "closed"},
		"known_error":     {"investigating", "fix_in_progress", "resolved", "closed"},
		"fix_in_progress": {"investigating", "known_error", "resolved", "closed"},
		"resolved":        {"investigating", "closed"},
		"closed":          {"investigating"},
	}
	statusStamp = map[string]string{"investigating": "investigating_at", "known_error": "known_error_at",
		"fix_in_progress": "fix_started_at", "resolved": "resolved_at", "closed": "closed_at"}
	sevRank = map[string]int{"critical": 4, "page": 4, "high": 3, "error": 3, "medium": 2, "warning": 2, "low": 1, "info": 1}
	envRank = map[string]int{"release": 4, "pre-release": 3, "develop": 2, "unknown": 1, "": 0}
	envNorm = map[string]string{"production": "release", "prod": "release", "release": "release", "pre-production": "pre-release",
		"pre-release": "pre-release", "pre": "pre-release", "stage": "develop", "develop": "develop", "dev": "develop", "staging": "develop"}
	envClass   = map[string]string{"release": "production", "pre-release": "staging", "develop": "dev"}
	userRe     = pyRe(`^[a-z0-9][a-z0-9._-]{1,39}$`)
	actionName = pyRe(`^[0-9]{10}-[0-9a-f]{8}\.json$`)
	ctrlRe     = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

const (
	maxRefs = 50
	maxReq  = 4096
	maxTick = 200
)

func in(s string, set []string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

func idx(s string, set []string) int {
	for i, x := range set {
		if x == s {
			return i
		}
	}
	return -1
}

func normEnv(v any) string {
	if !py.Truthy(v) {
		return ""
	}
	if e, ok := envNorm[strings.ToLower(py.Strip(py.Str(v)))]; ok {
		return e
	}
	return "unknown"
}

// EnvClass is the environment class (dev, staging, production) of an environment name.
func EnvClass(v any) string { return envClass[normEnv(v)] }

// clean strips control characters, collapses white space, redacts secrets and caps the text (in characters).
func clean(text any, n int) string {
	s := ""
	if py.Truthy(text) {
		s = py.Str(text)
	}
	s = strings.Join(py.Split(ctrlRe.ReplaceAllString(s, " ")), " ")
	s, _ = notify.Redact(s, nil)
	return py.Head(s, n)
}

// rc is one line: at most 25 words and 180 characters.
func rc(text string) string {
	s := clean(text, 400)
	if w := py.Split(s); len(w) > 25 {
		s = strings.Join(w[:25], " ")
	}
	return py.Head(s, 180)
}

func parseRef(ref any) (string, string, error) {
	s, ok := ref.(string)
	if !ok || !strings.Contains(s, ":") || py.Len(s) > 330 {
		return "", "", invalid("bad ref")
	}
	rt, rid, _ := strings.Cut(s, ":")
	if t, ok := refTypes[rt]; !ok || !t.re.MatchString(rid) {
		return "", "", invalid("bad ref %s", py.Repr(py.Head(s, 60)))
	}
	return rt, rid, nil
}

func checkLinkType(rt string, lt any) error {
	s, ok := lt.(string)
	if !ok || !in(s, linkTypes) || !in(s, refTypes[rt].links) {
		r := py.Str(lt)
		if ok {
			r = py.Repr(s)
		}
		return invalid("link type %s not allowed for %s", r, rt)
	}
	return nil
}

// --- rows -------------------------------------------------------------------------------------------------------

type row map[string]any

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

// rowsQ is rows without the error: a missing table is no rows (the reference's _rows).
func rowsQ(q querier, query string, args ...any) []row {
	r, _ := rows(q, query, args...)
	return r
}

func one(q querier, query string, args ...any) row {
	r := rowsQ(q, query, args...)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func (r row) s(k string) string {
	if r == nil || r[k] == nil {
		return ""
	}
	return py.Str(r[k])
}

func (r row) i(k string) int64 {
	if r == nil {
		return 0
	}
	return py.I(r[k])
}

func (r row) has(k string) bool { return r != nil && py.Truthy(r[k]) }

func atoi(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }

// --- describe -----------------------------------------------------------------------------------------------------

// desc is what a reference points at.
type desc struct {
	exists         bool
	env, severity  string
	firstSeen      int64 // 0: unknown
	lastSeen       int64
	incidentID     int64
	customerFacing int64
}

func (o *Options) capacityJSON() []any {
	b, err := os.ReadFile(filepath.Join(o.StateDir, "capacity.json"))
	if err != nil {
		return nil
	}
	if m, ok := mustJSON(b).(*py.OMap); ok {
		l, _ := m.Get("filesystems").([]any)
		return l
	}
	return nil
}

func (o *Options) describe(q querier, rt, rid string) desc {
	d := desc{}
	var env, sev any
	switch rt {
	case "incident":
		if r := one(q, `SELECT title, key, status, env, severity, first_seen, last_seen, customer_facing FROM incidents WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("first_seen"), lastSeen: r.i("last_seen"), customerFacing: r.i("customer_facing"), incidentID: atoi(rid)}
			env, sev = r["env"], r["severity"]
		}
	case "qa_issue":
		if r := one(q, `SELECT title, actionability, environment, severity, first_ts, last_ts, incident_id FROM qa_issues WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("first_ts"), lastSeen: r.i("last_ts"), incidentID: r.i("incident_id")}
			env, sev = r["environment"], r["severity"]
		}
	case "cve_group":
		if r := one(q, `SELECT max(title) title, min(state) status, max(env_max_class) env, max(priority) severity, min(first_seen) first_seen, max(last_seen) last_seen, max(incident_id) incident_id, count(*) n FROM cve_findings WHERE group_key=?`, rid); r.has("n") {
			d = desc{exists: true, firstSeen: r.i("first_seen"), lastSeen: r.i("last_seen"), incidentID: r.i("incident_id")}
			env, sev = r["env"], r["severity"]
		}
	case "cicd_finding":
		if r := one(q, `SELECT title, status, env, severity, first_seen, last_seen, incident_id FROM cicd_findings WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("first_seen"), lastSeen: r.i("last_seen"), incidentID: r.i("incident_id")}
			env, sev = r["env"], r["severity"]
		}
	case "capacity":
		host, mount, _ := strings.Cut(rid, "|")
		if r := one(q, `SELECT host, mount, env, min(created) c, max(created) m FROM disk_reports WHERE (host=? OR alias=?) AND mount=?`, host, host, mount); r.has("host") {
			d = desc{exists: true, firstSeen: r.i("c"), lastSeen: r.i("m")}
			env = r["env"]
		} else {
			for _, x := range o.capacityJSON() {
				fs, ok := x.(*py.OMap)
				if !ok {
					continue
				}
				if (fs.Get("vm_name") == host || fs.Get("host") == host || fs.Get("vm") == host) && fs.Get("mount") == mount {
					d.exists = true
					env = py.Or(fs.Get("env"), "")
					break
				}
			}
		}
	case "vm_report":
		if r := one(q, `SELECT name, env, status, created FROM vm_reports WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("created"), lastSeen: r.i("created")}
			env = r["env"]
		}
	case "finding":
		run, i, _ := strings.Cut(rid, ":")
		if r := one(q, `SELECT p.title, p.severity, p.environment, r.started_at FROM triage_problems p LEFT JOIN runs r ON r.id=p.run_id WHERE p.run_id=? AND p.idx=?`, atoi(run), atoi(i)); r != nil {
			d = desc{exists: true, firstSeen: r.i("started_at"), lastSeen: r.i("started_at")}
			env, sev = r["environment"], r["severity"]
			if e := one(q, `SELECT incident_id FROM incident_events WHERE run_id=? AND idx=? LIMIT 1`, atoi(run), atoi(i)); e != nil {
				d.incidentID = e.i("incident_id")
			}
		}
	case "rca":
		if r := one(q, `SELECT title, status, created, incident_id, source FROM rca_reports WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("created"), lastSeen: r.i("created"), incidentID: r.i("incident_id")}
		}
	case "fix_run":
		if r := one(q, `SELECT problem_title, outcome, env, started_at FROM fix_runs WHERE id=?`, rid); r != nil {
			d = desc{exists: true, firstSeen: r.i("started_at")}
			env = r["env"]
		}
	case "deploy":
		if r := one(q, `SELECT project, env, title, at, verify_state FROM cicd_deploys WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("at")}
			env = r["env"]
		}
	case "problem":
		if r := one(q, `SELECT title, status, env, severity, first_seen FROM problems WHERE id=?`, atoi(rid)); r != nil {
			d = desc{exists: true, firstSeen: r.i("first_seen")}
			env, sev = r["env"], r["severity"]
		}
	case "proposal", "task", "mr":
		d.exists = true // board.db is the board job's; the app shows the stored title
	}
	d.env = normEnv(env)
	if py.Truthy(sev) {
		d.severity = strings.ToLower(py.Str(sev))
	}
	return d
}

// --- events, problems ---------------------------------------------------------------------------------------------

func event(q querier, pid int64, kind, actor, rt, rid, text string, at int64) error {
	var t, r1, r2 any
	if text != "" {
		t = clean(text, 300)
	}
	if rt != "" {
		r1, r2 = rt, rid
	}
	_, err := q.Exec(`INSERT INTO problem_events (problem_id, at, kind, actor, ref_type, ref_id, text) VALUES (?,?,?,?,?,?,?)`, pid, at, kind, actor, r1, r2, t)
	return err
}

func get(q querier, pid int64) row {
	return one(q, `SELECT * FROM problems WHERE id=?`, pid)
}

func nullS(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (o *Options) create(q querier, title, by string, at int64) (int64, error) {
	title = clean(title, 200)
	if py.Len(title) < 3 {
		return 0, invalid("a title needs at least 3 characters")
	}
	res, err := q.Exec(`INSERT INTO problems (title, status, origin, root_cause, root_cause_label, workaround, owner, created_at, created_by, updated_at, signature, fanout) VALUES (?,?,?,?,?,?,?,?,?,?, '{}', 0)`,
		title, "new", "manual", nil, nil, nil, nil, at, by, at)
	if err != nil {
		return 0, err
	}
	pid, _ := res.LastInsertId()
	return pid, event(q, pid, "created", by, "", "", title+" (manual)", at)
}

func (o *Options) setStatus(q querier, pid int64, to, by, note, rootCause, workaround string, auto bool, at int64) (bool, error) {
	p := get(q, pid)
	if p == nil {
		return false, invalid("no such problem")
	}
	cur := p.s("status")
	if !in(to, statuses) || to == "new" {
		return false, invalid("bad target status")
	}
	if to == cur {
		return false, nil
	}
	if auto {
		if idx(to, statuses) <= idx(cur, statuses) {
			return false, nil
		}
	} else if !in(to, humanMoves[cur]) {
		return false, invalid("%s -> %s is not allowed", cur, to)
	}
	r := ""
	if rootCause != "" {
		r = rc(rootCause)
	}
	if to == "known_error" && r == "" && !p.has("root_cause") {
		return false, invalid("a known error needs its root cause")
	}
	sets, args := []string{"status=?", "updated_at=?"}, []any{to, at}
	if st := statusStamp[to]; st != "" {
		sets, args = append(sets, st+"=?"), append(args, at)
	}
	if r != "" {
		sets, args = append(sets, "root_cause=?"), append(args, r)
	}
	if workaround != "" {
		sets, args = append(sets, "workaround=?"), append(args, clean(workaround, 600))
	}
	if to == "resolved" {
		sets, args = append(sets, "resolved_by=?", "resolve_note=?"), append(args, by, nullS(clean(note, 300)))
	}
	recur := (cur == "resolved" || cur == "closed") && in(to, openStatuses)
	if recur {
		sets = append(sets, "recurrences=COALESCE(recurrences,0)+1", "resolved_at=NULL", "resolved_by=NULL", "closed_at=NULL", "merged_into=NULL")
	}
	if _, err := q.Exec(`UPDATE problems SET `+strings.Join(sets, ", ")+` WHERE id=?`, append(args, pid)...); err != nil {
		return false, err
	}
	text := cur + " -> " + to
	if note != "" {
		text += ": " + clean(note, 200)
	}
	kind := "status"
	if recur {
		kind = "recurrence"
	}
	return true, event(q, pid, kind, by, "", "", text, at)
}

type editReq struct {
	title, owner, rootCause, label, workaround *string
}

func (o *Options) edit(q querier, pid int64, by string, e editReq, at int64) (bool, error) {
	if get(q, pid) == nil {
		return false, invalid("no such problem")
	}
	var sets, what []string
	var args []any
	if e.title != nil {
		t := clean(*e.title, 200)
		if py.Len(t) < 3 {
			return false, invalid("a title needs at least 3 characters")
		}
		sets, args, what = append(sets, "title=?"), append(args, t), append(what, "title")
	}
	if e.owner != nil {
		if *e.owner != "" && !userRe.MatchString(*e.owner) {
			return false, invalid("bad owner")
		}
		sets, args, what = append(sets, "owner=?"), append(args, nullS(*e.owner)), append(what, "owner")
	}
	if e.rootCause != nil {
		sets, args, what = append(sets, "root_cause=?"), append(args, nullS(rc(*e.rootCause))), append(what, "root cause")
	}
	if e.label != nil {
		if !in(*e.label, labels) {
			return false, invalid("bad label")
		}
		sets, args, what = append(sets, "root_cause_label=?"), append(args, nullS(*e.label)), append(what, "label")
	}
	if e.workaround != nil {
		sets, args, what = append(sets, "workaround=?"), append(args, nullS(clean(*e.workaround, 600))), append(what, "workaround")
	}
	if len(sets) == 0 {
		return false, nil
	}
	if _, err := q.Exec(`UPDATE problems SET `+strings.Join(sets, ", ")+`, updated_at=? WHERE id=?`, append(args, at, pid)...); err != nil {
		return false, err
	}
	return true, event(q, pid, "edit", by, "", "", "changed "+strings.Join(what, ", "), at)
}

// --- links ----------------------------------------------------------------------------------------------------------

func liveLink(q querier, pid int64, rt, rid, lt string) row {
	return one(q, `SELECT * FROM problem_links WHERE problem_id=? AND ref_type=? AND ref_id=? AND link_type=? AND state IN ('suggested','accepted')`, pid, rt, rid, lt)
}

func hasPrimary(q querier, rt, rid string) bool {
	return one(q, `SELECT problem_id FROM problem_links WHERE ref_type=? AND ref_id=? AND is_primary=1 AND state='accepted'`, rt, rid) != nil
}

type linkArgs struct {
	state, source, reason, note string
	confidence                  *float64
	features                    []string
	noCheck                     bool
}

// link adds a link (or upgrades a suggestion to accepted); the link id and whether anything changed.
func (o *Options) link(q querier, pid int64, rt, rid, lt, by string, a linkArgs, at int64) (int64, bool, error) {
	if a.state == "" {
		a.state = "accepted"
	}
	if a.source == "" {
		a.source = "human"
	}
	if get(q, pid) == nil {
		return 0, false, invalid("no such problem")
	}
	if t, ok := refTypes[rt]; !ok || !t.re.MatchString(rid) {
		return 0, false, invalid("bad ref")
	}
	if err := checkLinkType(rt, lt); err != nil {
		return 0, false, err
	}
	if (a.state != "suggested" && a.state != "accepted") || !in(a.source, []string{"human", o.Actor, "model", "backfill"}) {
		return 0, false, invalid("bad state or source")
	}
	if rt == "problem" && atoi(rid) == pid {
		return 0, false, invalid("a problem cannot link to itself")
	}
	if !a.noCheck && !o.describe(q, rt, rid).exists {
		return 0, false, invalid("%s:%s does not exist", rt, rid)
	}
	var conf any
	if a.confidence != nil {
		conf = *a.confidence
	} else if in(a.source, []string{"human", "backfill", o.Actor}) && a.state == "accepted" {
		conf = 1.0
	}
	if have := liveLink(q, pid, rt, rid, lt); have != nil {
		if have.s("state") == "accepted" || a.state == "suggested" {
			return have.i("id"), false, nil
		}
		n, err := o.accept(q, []int64{have.i("id")}, by, "", at)
		return have.i("id"), n > 0, err
	}
	var feats, decBy, decAt any
	if len(a.features) > 0 {
		l := make([]any, len(a.features))
		for i, f := range a.features {
			l[i] = f
		}
		feats = py.Dumps(l, -1)
	}
	if a.state == "accepted" {
		decBy, decAt = by, at
	}
	res, err := q.Exec(`INSERT INTO problem_links (problem_id, ref_type, ref_id, link_type, state, is_primary, source, confidence, reason, features, "by", at, decided_by, decided_at, note) VALUES (?,?,?,?,?,0,?,?,?,?,?,?,?,?,?)`,
		pid, rt, rid, lt, a.state, a.source, conf, nullS(clean(a.reason, 300)), feats, by, at, decBy, decAt, nullS(clean(a.note, 300)))
	if err != nil {
		return 0, false, err
	}
	lid, _ := res.LastInsertId()
	text := lt
	if c, ok := conf.(float64); ok && a.state == "suggested" && c != 0 {
		text += fmt.Sprintf(" (%s) %s", py.F(c, 2), clean(a.reason, 200))
	}
	if a.note != "" {
		text += ": " + clean(a.note, 200)
	}
	kind := "link"
	if a.state != "accepted" {
		kind = "suggest"
	}
	if err := event(q, pid, kind, by, rt, rid, text, at); err != nil {
		return 0, false, err
	}
	if a.state == "accepted" {
		if err := o.afterAccept(q, lid, by, at); err != nil {
			return 0, false, err
		}
	}
	return lid, true, nil
}

func (o *Options) afterAccept(q querier, lid int64, by string, at int64) error {
	lk := one(q, `SELECT * FROM problem_links WHERE id=?`, lid)
	pid, rt, rid, lt := lk.i("problem_id"), lk.s("ref_type"), lk.s("ref_id"), lk.s("link_type")
	if lt == "caused_by" && !hasPrimary(q, rt, rid) {
		if _, err := q.Exec(`UPDATE problem_links SET is_primary=1 WHERE id=?`, lid); err != nil {
			return err
		}
	}
	p := get(q, pid)
	st := p.s("status")
	var err error
	switch {
	case lt == "caused_by" && (st == "resolved" || st == "closed"):
		since := p.i("resolved_at")
		if since == 0 {
			since = p.i("closed_at")
		}
		if since == 0 {
			since = at
		}
		if st == "resolved" && at-since <= o.config().ReopenDays*86400 {
			_, err = o.setStatus(q, pid, "investigating", o.Actor, "recurrence: "+rt+":"+rid+" linked", "", "", false, at)
		}
	case lt == "fixed_by" && in(st, []string{"new", "investigating", "known_error"}):
		_, err = o.setStatus(q, pid, "fix_in_progress", o.Actor, "fixed by "+rt+":"+rid, "", "", true, at)
	case (lt == "caused_by" || lt == "explains") && st == "new":
		_, err = o.setStatus(q, pid, "investigating", o.Actor, "first link accepted", "", "", true, at)
	}
	if err != nil {
		return err
	}
	if lt == "duplicate_of" {
		if err := o.merge(q, pid, atoi(rid), by, true, at); err != nil {
			return err
		}
	}
	return o.recompute(q, pid)
}

func linksOf(q querier, ids []int64) []row {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, x := range ids {
		args[i] = x
	}
	return rowsQ(q, `SELECT * FROM problem_links WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
}

func (o *Options) accept(q querier, ids []int64, by, decidedBy string, at int64) (int, error) {
	if decidedBy == "" {
		decidedBy = by
	}
	n := 0
	for _, lk := range linksOf(q, ids) {
		if lk.s("state") != "suggested" {
			continue
		}
		if _, err := q.Exec(`UPDATE problem_links SET state='accepted', decided_by=?, decided_at=? WHERE id=?`, decidedBy, at, lk.i("id")); err != nil {
			return n, err
		}
		if err := event(q, lk.i("problem_id"), "accept", decidedBy, lk.s("ref_type"), lk.s("ref_id"), lk.s("link_type"), at); err != nil {
			return n, err
		}
		if err := o.afterAccept(q, lk.i("id"), by, at); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (o *Options) reject(q querier, ids []int64, by, reason string, at int64) (int, error) {
	n := 0
	for _, lk := range linksOf(q, ids) {
		if lk.s("state") != "suggested" {
			continue
		}
		if _, err := q.Exec(`UPDATE problem_links SET state='rejected', decided_by=?, decided_at=?, note=? WHERE id=?`, by, at, nullS(clean(reason, 300)), lk.i("id")); err != nil {
			return n, err
		}
		if err := event(q, lk.i("problem_id"), "reject", by, lk.s("ref_type"), lk.s("ref_id"), clean(reason, 200), at); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (o *Options) unlink(q querier, ids []int64, by, note string, at int64) (int, error) {
	n := 0
	var touched []int64
	seen := map[int64]bool{}
	for _, lk := range linksOf(q, ids) {
		if lk.s("state") != "accepted" {
			continue
		}
		if _, err := q.Exec(`UPDATE problem_links SET state='removed', is_primary=0, decided_by=?, decided_at=?, note=? WHERE id=?`, by, at, nullS(clean(note, 300)), lk.i("id")); err != nil {
			return n, err
		}
		if err := event(q, lk.i("problem_id"), "unlink", by, lk.s("ref_type"), lk.s("ref_id"), clean(note, 200), at); err != nil {
			return n, err
		}
		if lk.i("is_primary") != 0 { // the oldest other accepted caused_by takes over
			if nx := one(q, `SELECT id FROM problem_links WHERE ref_type=? AND ref_id=? AND link_type='caused_by' AND state='accepted' ORDER BY decided_at, id LIMIT 1`, lk.s("ref_type"), lk.s("ref_id")); nx != nil {
				if _, err := q.Exec(`UPDATE problem_links SET is_primary=1 WHERE id=?`, nx.i("id")); err != nil {
					return n, err
				}
			}
		}
		if p := lk.i("problem_id"); !seen[p] {
			seen[p] = true
			touched = append(touched, p)
		}
		n++
	}
	sort.Slice(touched, func(i, j int) bool { return touched[i] < touched[j] })
	for _, p := range touched {
		if err := o.recompute(q, p); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (o *Options) setPrimary(q querier, lid int64, by string, at int64) (bool, error) {
	lk := one(q, `SELECT * FROM problem_links WHERE id=?`, lid)
	if lk == nil || lk.s("state") != "accepted" || lk.s("link_type") != "caused_by" {
		return false, invalid("only an accepted caused_by link can be primary")
	}
	if lk.i("is_primary") != 0 {
		return false, nil
	}
	if _, err := q.Exec(`UPDATE problem_links SET is_primary=0 WHERE ref_type=? AND ref_id=? AND is_primary=1 AND state='accepted'`, lk.s("ref_type"), lk.s("ref_id")); err != nil {
		return false, err
	}
	if _, err := q.Exec(`UPDATE problem_links SET is_primary=1 WHERE id=?`, lid); err != nil {
		return false, err
	}
	return true, event(q, lk.i("problem_id"), "primary", by, lk.s("ref_type"), lk.s("ref_id"), "", at)
}

// merge: pid duplicates into; the accepted links move (events kept), pid is closed with merged_into.
func (o *Options) merge(q querier, pid, into int64, by string, viaLink bool, at int64) error {
	a, b := get(q, pid), get(q, into)
	if a == nil || b == nil || pid == into {
		return invalid("merge needs two different problems")
	}
	if b.s("status") == "closed" && b.has("merged_into") {
		return invalid("the target was itself merged")
	}
	for _, lk := range rowsQ(q, `SELECT * FROM problem_links WHERE problem_id=? AND state IN ('accepted','suggested')`, pid) {
		if lk.s("ref_type") == "problem" && atoi(lk.s("ref_id")) == into {
			continue
		}
		have := liveLink(q, into, lk.s("ref_type"), lk.s("ref_id"), lk.s("link_type"))
		if have == nil {
			if _, err := q.Exec(`UPDATE problem_links SET problem_id=? WHERE id=?`, into, lk.i("id")); err != nil {
				return err
			}
			if err := event(q, into, "merge", by, lk.s("ref_type"), lk.s("ref_id"), fmt.Sprintf("from P-%d", pid), at); err != nil {
				return err
			}
			continue
		}
		if _, err := q.Exec(`UPDATE problem_links SET state='removed', is_primary=0, decided_by=?, decided_at=?, note=? WHERE id=?`, by, at, fmt.Sprintf("merged into P-%d", into), lk.i("id")); err != nil {
			return err
		}
		if lk.i("is_primary") != 0 && lk.s("state") == "accepted" && have.i("is_primary") == 0 && have.s("state") == "accepted" {
			if _, err := q.Exec(`UPDATE problem_links SET is_primary=1 WHERE id=?`, have.i("id")); err != nil {
				return err
			}
		}
	}
	if !viaLink && liveLink(q, pid, "problem", strconv.FormatInt(into, 10), "duplicate_of") == nil {
		if _, err := q.Exec(`INSERT INTO problem_links (problem_id, ref_type, ref_id, link_type, state, source, confidence, "by", at, decided_by, decided_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			pid, "problem", strconv.FormatInt(into, 10), "duplicate_of", "accepted", "human", 1.0, by, at, by, at); err != nil {
			return err
		}
	}
	if _, err := q.Exec(`UPDATE problems SET status='closed', closed_at=?, merged_into=?, updated_at=? WHERE id=?`, at, into, at, pid); err != nil {
		return err
	}
	if err := event(q, pid, "merge", by, "problem", strconv.FormatInt(into, 10), fmt.Sprintf("closed as duplicate of P-%d", into), at); err != nil {
		return err
	}
	return o.recompute(q, into)
}

// recompute sets severity, env, customer_facing and first_seen (worst / earliest over the accepted caused_by links)
// and the signature.
func (o *Options) recompute(q querier, pid int64) error {
	sev, env, cf, first := "", "", int64(0), int64(0)
	for _, lk := range rowsQ(q, `SELECT ref_type, ref_id FROM problem_links WHERE problem_id=? AND state='accepted' AND link_type='caused_by'`, pid) {
		d := o.describe(q, lk.s("ref_type"), lk.s("ref_id"))
		if sevRank[d.severity] > sevRank[sev] {
			sev = d.severity
		}
		if envRank[d.env] > envRank[env] {
			env = d.env
		}
		if cf == 0 {
			cf = d.customerFacing
		}
		if d.firstSeen != 0 && (first == 0 || d.firstSeen < first) {
			first = d.firstSeen
		}
	}
	sig := o.signature(q, pid)
	out := py.NewOMap()
	for _, k := range featureKeys {
		if v := sig[k].sorted(); len(v) > 0 {
			if len(v) > 200 {
				v = v[:200]
			}
			l := make([]any, len(v))
			for i, x := range v {
				l[i] = x
			}
			out.Set(k, l)
		}
	}
	var firstV any
	if first != 0 {
		firstV = first
	}
	_, err := q.Exec(`UPDATE problems SET severity=?, env=?, customer_facing=?, first_seen=COALESCE(?, first_seen), signature=?, fanout=? WHERE id=?`,
		nullS(sev), nullS(env), cf, firstV, py.Dumps(out, -1), len(sig["hosts"]), pid)
	return err
}

// --- resolve with the linked incidents ---------------------------------------------------------------------------

func linkedOpenIncidents(q querier, pid int64) []int64 {
	var out []int64
	for _, lk := range rowsQ(q, `SELECT ref_id FROM problem_links WHERE problem_id=? AND ref_type='incident' AND link_type='caused_by' AND state='accepted'`, pid) {
		r := one(q, `SELECT id, dc, status FROM incidents WHERE id=?`, atoi(lk.s("ref_id")))
		if r != nil && (r.s("status") == "open" || r.s("status") == "acknowledged") && r.s("dc") != "CVE" && r.s("dc") != "CICD" {
			out = append(out, r.i("id"))
		}
	}
	return out
}

func intsRepr(xs []int64) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.FormatInt(x, 10)
	}
	return "[" + strings.Join(s, ", ") + "]"
}

func (o *Options) resolve(tx *sql.Tx, pid int64, by, note string, issues []int64, at int64) (bool, int, error) {
	if note == "" || py.Len(clean(note, 300)) < 3 {
		return false, 0, invalid("a resolve needs a note")
	}
	allowed := map[int64]bool{}
	for _, x := range linkedOpenIncidents(tx, pid) {
		allowed[x] = true
	}
	var bad []int64
	for _, x := range issues {
		if !allowed[x] {
			bad = append(bad, x)
		}
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = bad[:5]
		}
		return false, 0, invalid("incidents %s are not open caused_by links of this problem", intsRepr(bad))
	}
	changed, err := o.setStatus(tx, pid, "resolved", by, note, "", "", false, at)
	if err != nil {
		return false, 0, err
	}
	done := 0
	if len(issues) > 0 && o.Incidents == nil {
		return false, 0, invalid("incident requests are not configured")
	}
	for _, iid := range issues { // the incident path keeps the incident semantics (events)
		io := *o.Incidents
		io.Now = func() time.Time { return time.Unix(at, 0) }
		t, err := io.ApplyRequest(tx, incidents.Request{Action: "resolve", ID: iid, By: by, Role: "admin", Note: clean(fmt.Sprintf("resolved with P-%d: %s", pid, note), 300)})
		if err != nil {
			return false, 0, err
		}
		if t != "" {
			done++
			if err := event(tx, pid, "status", by, "incident", strconv.FormatInt(iid, 10), "linked incident resolved", at); err != nil {
				return false, 0, err
			}
		}
	}
	return changed, done, nil
}

// --- requests from the app (settings-in/problem-actions) ---------------------------------------------------------

// Request is one validated request.
type Request struct {
	Action, By, Role, Note             string
	Title, Root, Workaround, Label, To string
	FromType, FromID                   string
	Problem, Into, Link                int64
	Links, Issues                      []int64
	Refs                               [][3]string // ref type, ref id, link type
	edit                               editReq
}

var actions = []string{"create", "link", "unlink", "accept", "reject", "primary", "status", "edit", "merge", "resolve"}

func intOf(v any, what string) (int64, error) {
	n, ok := v.(int64)
	if !ok || n <= 0 || n >= 1_000_000_000 {
		return 0, invalid("bad %s", what)
	}
	return n, nil
}

func idsOf(v any, what string) ([]int64, error) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 || len(l) > maxRefs {
		return nil, invalid("bad %s", what)
	}
	seen := map[int64]bool{}
	var out []int64
	for _, x := range l {
		n, err := intOf(x, what)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Validate is the authoritative validation of one request (the app validates for the form only).
func Validate(v any) (Request, error) {
	m, ok := v.(*py.OMap)
	if !ok {
		return Request{}, invalid("not an object")
	}
	a, _ := m.Get("action").(string)
	by := ""
	if py.Truthy(m.Get("by")) {
		by = py.Str(m.Get("by"))
	}
	role, _ := m.Get("role").(string)
	if !in(a, actions) || !userRe.MatchString(by) || (role != "admin" && role != "viewer") {
		return Request{}, invalid("bad fields")
	}
	if role != "admin" && a != "link" {
		return Request{}, invalid("admin only")
	}
	r := Request{Action: a, By: by, Role: role}
	txt := func(k string, n int) string { return clean(m.Get(k), n) }
	var err error
	switch a {
	case "create":
		r.Title = txt("title", 200)
		if py.Len(r.Title) < 3 {
			return r, invalid("a title needs at least 3 characters")
		}
		if frm := m.Get("from"); py.Truthy(frm) {
			rt, rid, err := parseRef(frm)
			if err != nil {
				return r, err
			}
			if !in(rt, IssueTypes) && rt != "rca" {
				return r, invalid("create from an issue or an RCA only")
			}
			r.FromType, r.FromID = rt, rid
		}
		r.Note = txt("note", 300)
	case "link":
		if r.Problem, err = intOf(m.Get("problem"), "problem"); err != nil {
			return r, err
		}
		refs, ok := m.Get("refs").([]any)
		if !ok || len(refs) == 0 || len(refs) > maxRefs {
			return r, invalid("bad refs")
		}
		lt := m.Get("link_type")
		if !py.Truthy(lt) {
			lt = "caused_by"
		}
		seen := map[[3]string]bool{}
		for _, x := range refs {
			rt, rid, err := parseRef(x)
			if err != nil {
				return r, err
			}
			ltr := lt
			if lt == "caused_by" && rt == "rca" {
				ltr = "explains" // an RCA explains; the form's default type is for issues
			}
			if err := checkLinkType(rt, ltr); err != nil {
				return r, err
			}
			k := [3]string{rt, rid, ltr.(string)}
			if !seen[k] {
				seen[k] = true
				r.Refs = append(r.Refs, k)
			}
		}
		sort.Slice(r.Refs, func(i, j int) bool {
			a, b := r.Refs[i], r.Refs[j]
			for x := 0; x < 3; x++ {
				if a[x] != b[x] {
					return a[x] < b[x]
				}
			}
			return false
		})
		r.Note = txt("note", 300)
	case "unlink", "reject":
		if r.Links, err = idsOf(m.Get("links"), "links"); err != nil {
			return r, err
		}
		r.Note = txt("note", 300)
		if a == "unlink" && py.Len(r.Note) < 3 {
			return r, invalid("an unlink needs a reason")
		}
	case "accept":
		if r.Links, err = idsOf(m.Get("links"), "links"); err != nil {
			return r, err
		}
	case "primary":
		if r.Link, err = intOf(m.Get("link"), "link"); err != nil {
			return r, err
		}
	case "status":
		if r.Problem, err = intOf(m.Get("problem"), "problem"); err != nil {
			return r, err
		}
		to, _ := m.Get("to").(string)
		if !in(to, statuses) || to == "new" {
			return r, invalid("bad status")
		}
		r.To, r.Note, r.Root, r.Workaround = to, txt("note", 300), txt("root_cause", 400), txt("workaround", 600)
		lbl := m.Get("root_cause_label")
		if !py.Truthy(lbl) {
			lbl = ""
		}
		ls, ok := lbl.(string)
		if !ok || !in(ls, labels) {
			return r, invalid("bad label")
		}
		r.Label = ls
	case "edit":
		if r.Problem, err = intOf(m.Get("problem"), "problem"); err != nil {
			return r, err
		}
		for _, f := range []struct {
			k   string
			n   int
			dst **string
		}{{"title", 200, &r.edit.title}, {"owner", 40, &r.edit.owner}, {"root_cause", 400, &r.edit.rootCause}, {"workaround", 600, &r.edit.workaround}} {
			if m.Has(f.k) && m.Get(f.k) != nil {
				s := txt(f.k, f.n)
				*f.dst = &s
			}
		}
		if m.Has("root_cause_label") {
			ls, ok := m.Get("root_cause_label").(string)
			if !ok || !in(ls, labels) {
				return r, invalid("bad label")
			}
			r.edit.label = &ls
		}
	case "merge":
		if r.Problem, err = intOf(m.Get("problem"), "problem"); err != nil {
			return r, err
		}
		if r.Into, err = intOf(m.Get("into"), "target"); err != nil {
			return r, err
		}
		if r.Into == r.Problem {
			return r, invalid("merge into itself")
		}
	case "resolve":
		if r.Problem, err = intOf(m.Get("problem"), "problem"); err != nil {
			return r, err
		}
		r.Note = txt("note", 300)
		iss := m.Get("issues")
		if !py.Truthy(iss) {
			iss = []any{}
		}
		l, ok := iss.([]any)
		if !ok || len(l) > maxRefs {
			return r, invalid("bad issues")
		}
		if len(l) > 0 {
			if r.Issues, err = idsOf(l, "issue"); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

// ReadRequest reads and validates one request file (no symbolic link, a regular file of at most 4096 bytes).
func ReadRequest(p string) (Request, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Request{}, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return Request{}, invalid("not a file")
	}
	raw, _ := io.ReadAll(io.LimitReader(f, maxReq+1))
	if len(raw) > maxReq {
		return Request{}, invalid("too large")
	}
	if !utf8.Valid(raw) {
		return Request{}, errors.New("not UTF-8")
	}
	v, err := py.ParseJSON(raw)
	if err != nil {
		return Request{}, err
	}
	return Validate(v)
}

// ApplyRequest applies one validated request inside the caller's transaction: a short text, or "" (no change).
func (o *Options) ApplyRequest(tx *sql.Tx, r Request) (string, error) {
	o.defaults()
	at := o.now()
	by := r.By
	switch r.Action {
	case "create":
		pid, err := o.create(tx, r.Title, by, at)
		if err != nil {
			return "", err
		}
		if r.FromType != "" {
			lt := "caused_by"
			if r.FromType == "rca" {
				lt = "explains"
			}
			if _, _, err := o.link(tx, pid, r.FromType, r.FromID, lt, by, linkArgs{note: r.Note}, at); err != nil {
				return "", err
			}
			if r.FromType == "rca" { // the RCA's own incident is caused by what the RCA explains
				if d := o.describe(tx, "rca", r.FromID); d.incidentID != 0 && o.describe(tx, "incident", strconv.FormatInt(d.incidentID, 10)).exists {
					if _, _, err := o.link(tx, pid, "incident", strconv.FormatInt(d.incidentID, 10), "caused_by", by, linkArgs{}, at); err != nil {
						return "", err
					}
				}
			}
		}
		return fmt.Sprintf("created P-%d", pid), nil
	case "link":
		p := get(tx, r.Problem)
		if p == nil {
			return "", invalid("no such problem")
		}
		if p.s("status") == "closed" && p.has("merged_into") {
			return "", invalid("P-%d was merged into P-%d", r.Problem, p.i("merged_into"))
		}
		viewer := r.Role != "admin"
		n := 0
		for _, ref := range r.Refs {
			a := linkArgs{source: "human", note: r.Note}
			c := 1.0
			if viewer {
				a.state, a.reason, c = "suggested", "proposed by "+by, 0.8
			}
			a.confidence = &c
			_, ch, err := o.link(tx, r.Problem, ref[0], ref[1], ref[2], by, a, at)
			if err != nil {
				return "", err
			}
			if ch {
				n++
			}
		}
		if n == 0 {
			return "", nil
		}
		w := "added"
		if viewer {
			w = "suggested"
		}
		return fmt.Sprintf("P-%d: %d link(s) %s", r.Problem, n, w), nil
	case "unlink":
		n, err := o.unlink(tx, r.Links, by, r.Note, at)
		return fmt.Sprintf("%d unlinked", n), err
	case "accept":
		n, err := o.accept(tx, r.Links, by, "", at)
		if err != nil || n == 0 {
			return "", err
		}
		return fmt.Sprintf("%d accepted", n), nil
	case "reject":
		n, err := o.reject(tx, r.Links, by, r.Note, at)
		if err != nil || n == 0 {
			return "", err
		}
		return fmt.Sprintf("%d rejected", n), nil
	case "primary":
		ch, err := o.setPrimary(tx, r.Link, by, at)
		if err != nil || !ch {
			return "", err
		}
		return "primary set", nil
	case "status":
		if r.To == "resolved" {
			note := r.Note
			if note == "" {
				note = "resolved"
			}
			ch, _, err := o.resolve(tx, r.Problem, by, note, nil, at)
			if err != nil || !ch {
				return "", err
			}
			return "resolved", nil
		}
		if r.Label != "" {
			l := r.Label
			if _, err := o.edit(tx, r.Problem, by, editReq{label: &l}, at); err != nil {
				return "", err
			}
		}
		ch, err := o.setStatus(tx, r.Problem, r.To, by, r.Note, r.Root, r.Workaround, false, at)
		if err != nil || !ch {
			return "", err
		}
		return fmt.Sprintf("P-%d %s", r.Problem, r.To), nil
	case "edit":
		ch, err := o.edit(tx, r.Problem, by, r.edit, at)
		if err != nil || !ch {
			return "", err
		}
		return "edited", nil
	case "merge":
		if err := o.merge(tx, r.Problem, r.Into, by, false, at); err != nil {
			return "", err
		}
		return fmt.Sprintf("P-%d merged into P-%d", r.Problem, r.Into), nil
	case "resolve":
		ch, done, err := o.resolve(tx, r.Problem, by, r.Note, r.Issues, at)
		if err != nil || (!ch && done == 0) {
			return "", err
		}
		t := fmt.Sprintf("P-%d resolved", r.Problem)
		if done > 0 {
			t += fmt.Sprintf(", %d incident(s) resolved", done)
		}
		return t, nil
	}
	return "", nil
}

// LogEntry is what happened to one request file.
type LogEntry struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

func refusal(err error) bool {
	var inv *Invalid
	if errors.As(err, &inv) {
		return true
	}
	return strings.Contains(err.Error(), "constraint failed")
}

// ProcessRequests applies the request files (at most 200 per tick), each in its own savepoint: a refused one changes
// nothing. Every file is removed after it was read.
func (o *Options) ProcessRequests(tx *sql.Tx) ([]LogEntry, error) {
	o.defaults()
	ents, err := os.ReadDir(o.ActionsDir)
	if err != nil {
		return nil, nil
	}
	var log []LogEntry
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
		r, err := ReadRequest(p)
		text := ""
		if err == nil {
			if _, err = tx.Exec("SAVEPOINT req"); err != nil {
				return log, err
			}
			text, err = o.ApplyRequest(tx, r)
			if err != nil {
				tx.Exec("ROLLBACK TO req")
				tx.Exec("RELEASE req")
				if !refusal(err) {
					return log, err
				}
			} else if _, err = tx.Exec("RELEASE req"); err != nil {
				return log, err
			}
		}
		if err != nil {
			o.Log.Warn("problem request refused", "file", name, "error", err.Error())
			log = append(log, LogEntry{Name: name, OK: false, Text: py.Head(err.Error(), 200)})
		} else {
			if text == "" {
				text = "no change"
			}
			o.Log.Info("problem request", "file", name, "action", r.Action, "by", r.By, "result", text)
			log = append(log, LogEntry{Name: name, OK: true, Text: text})
		}
		os.Remove(p)
	}
	return log, nil
}

// autoClose closes the problems resolved for PROBLEMS_CLOSE_DAYS.
func (o *Options) autoClose(tx *sql.Tx) (int, error) {
	at, cfg := o.now(), o.config()
	n := 0
	for _, p := range rowsQ(tx, `SELECT id FROM problems WHERE status='resolved' AND resolved_at < ?`, at-cfg.CloseDays*86400) {
		if _, err := o.setStatus(tx, p.i("id"), "closed", o.Actor, fmt.Sprintf("no recurrence for %d days", cfg.CloseDays), "", "", true, at); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- state file ------------------------------------------------------------------------------------------------------

func (o *Options) stats(q querier) (*py.OMap, error) {
	by := py.NewOMap()
	for _, s := range statuses {
		by.Set(s, int64(0))
	}
	rs, err := rows(q, `SELECT status, count(*) n FROM problems GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		by.Set(r.s("status"), r.i("n"))
	}
	sug := one(q, `SELECT count(*) n FROM problem_links WHERE state='suggested'`).i("n")
	dec := one(q, `SELECT sum(state='accepted') a, sum(state='rejected') r FROM problem_links WHERE source IN (?,'model') AND decided_at IS NOT NULL`, o.Actor)
	acc, rej := dec.i("a"), dec.i("r")
	bands := py.NewOMap("high", int64(0), "medium", int64(0), "low", int64(0))
	for _, r := range rowsQ(q, `SELECT confidence c FROM problem_links WHERE state='suggested'`) {
		c := py.Num(r["c"])
		b := "low"
		if c >= 0.9 {
			b = "high"
		} else if c >= 0.75 {
			b = "medium"
		}
		bands.Set(b, bands.Get(b).(int64)+1)
	}
	var open int64
	for _, s := range openStatuses {
		open += py.I(by.Get(s))
	}
	var prec any
	if acc+rej > 0 {
		prec, _ = strconv.ParseFloat(py.F(float64(acc)/float64(acc+rej), 3), 64)
	}
	return py.NewOMap("by_status", by, "open", open, "suggested", sug, "suggested_bands", bands, "precision", prec, "decided", acc+rej), nil
}

func strList(xs []string) []any {
	l := make([]any, len(xs))
	for i, x := range xs {
		l[i] = x
	}
	return l
}

// writeState rewrites problems.json (the app reads it): the switches, the stats and extra.
func (o *Options) writeState(q querier, extra *py.OMap) error {
	path := filepath.Join(o.StateDir, "problems.json")
	st, _ := func() (*py.OMap, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		m, _ := mustJSON(b).(*py.OMap)
		return m, nil
	}()
	if st == nil {
		st = py.NewOMap()
	}
	c := o.config()
	st.Set("enabled", c.Enabled).Set("suggest", c.Suggest).Set("auto_accept", c.AutoAccept).
		Set("auto_accept_confidence", c.AutoConfidence).Set("auto_accept_classes", strList(c.AutoClasses)).
		Set("min_confidence", c.MinConfidence).Set("fanout_max", c.FanoutMax).Set("window_min", c.WindowMin).
		Set("lookback_days", c.LookbackDays).Set("reject_days", c.RejectDays)
	if s, err := o.stats(q); err == nil {
		st.Set("stats", s)
	}
	if extra != nil {
		for _, k := range extra.Keys() {
			st.Set(k, extra.Get(k))
		}
	}
	tmp, err := os.CreateTemp(o.StateDir, ".problems-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(py.Dumps(st, 1, true)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), path)
}

func (o *Options) lock() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(o.StateDir, ".problems.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Tick applies the requests, closes the problems resolved long ago and writes the state file. Off
// (FEATURE_PROBLEMS=0): the requests are removed unread.
func (o *Options) Tick(db *sql.DB) ([]LogEntry, error) {
	o.defaults()
	o.cfg = nil
	cfg := o.config()
	lk, err := o.lock()
	if err != nil {
		return nil, err
	}
	defer lk.Close()
	var log []LogEntry
	if cfg.Enabled {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		if log, err = o.ProcessRequests(tx); err == nil {
			_, err = o.autoClose(tx)
		}
		if err != nil {
			tx.Rollback()
			return log, err
		}
		if err := tx.Commit(); err != nil {
			return log, err
		}
	} else if ents, err := os.ReadDir(o.ActionsDir); err == nil {
		for i, e := range ents {
			if i < maxTick && !strings.HasPrefix(e.Name(), ".") {
				os.Remove(filepath.Join(o.ActionsDir, e.Name()))
			}
		}
	}
	rejected := int64(0)
	for _, x := range log {
		if !x.OK {
			rejected++
		}
	}
	last := log
	if len(last) > 20 {
		last = last[len(last)-20:]
	}
	l := make([]any, len(last))
	for i, x := range last {
		l[i] = py.NewOMap("name", x.Name, "ok", x.OK, "text", x.Text)
	}
	return log, o.writeState(db, py.NewOMap("last_tick", o.now(), "last_requests", l, "rejected_requests_last_tick", rejected))
}
