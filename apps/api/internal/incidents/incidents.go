// Package incidents is the incident record and the known-problem memory (the port of the earlier installation's
// incidents job). Every finding of an investigation (an incident job problem, a chat-triage problem, a log-RCA run)
// is attached to one incident, keyed by a stable problem key:
//
//	<DC>|<subject>|<family>     e.g.  EAST|www.example.com|http-5xx
//
// DC is the alert source (a monitoring site, KUMA) or "" (unknown, chat reports); subject is the host, instance or
// uptime monitor (lower case, no scheme or port); family is the symptom family of the alert or text (http-5xx,
// latency, memory, disk, down, ...). A finding with the same subject and family attaches to the open incident (an
// unknown DC matches any DC); a resolved one is re-opened within ReopenDays; otherwise a new incident starts.
//
// States: open -> acknowledged -> resolved. Auto-resolve: no matching alert firing (the alerts snapshot, the watch
// state, the uptime monitor state) for AutoHours after the last finding; incidents with no alert behind them (chat
// reports) after ChatHours. Ack, resolve and re-open from the web app arrive as request files
// (settings-in/incident-actions) and are applied by Tick after validation.
//
// Tables (ops.db, migration 0001): incidents, incident_events.
//
// Not ported: hub service tiers (raising the severity of a tier-1 hub); the incident history for a model prompt
// (the model runner is not in Go yet).
package incidents

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Options configure the job. Zero values take the defaults of the earlier installation.
type Options struct {
	StateDir   string   // watch-state.json, kuma-open.json (and alerts-firing.json when AlertsFile is empty)
	AlertsFile string   // the alerts snapshot (ARGUS_ALERTS_FILE)
	ActionsDir string   // settings-in/incident-actions
	Actor      string   // the actor of automatic events ("argus")
	AutoHours  int      // alert-backed incidents: hours with no alert firing before auto-resolve (6)
	ChatHours  int      // incidents with no alert behind them: hours with no new report (24)
	ReopenDays int      // a resolved incident re-opens within this many days (7)
	Sites      []string // monitoring site ids (OBS_SITES): their upper-case names are alert-backed DCs
	Customer   []string // customer-facing subject patterns (glob)
	Suffixes   []string // domain suffixes of a host name in text
	Prefixes   []string // prefixes of bare host names in text
	SkipHosts  []string // hosts never taken as a subject
	// Words are extra words per family ("latency" -> words in the team's language), matched in alert names and text
	Words map[string][]string
	Now   func() time.Time
	Log   *slog.Logger
	// Note posts a thread note for a changed incident that has a thread (nil: no notes).
	Note func(inc map[string]any, text string)

	hostRe         *regexp.Regexp
	fams, textFams []fam
}

func (o *Options) defaults() {
	if o.Actor == "" {
		o.Actor = "argus"
	}
	if o.AutoHours <= 0 {
		o.AutoHours = 6
	}
	if o.ChatHours <= 0 {
		o.ChatHours = 24
	}
	if o.ReopenDays <= 0 {
		o.ReopenDays = 7
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if len(o.Suffixes) == 0 {
		o.Suffixes = []string{"com", "net", "org", "io", "dev", "app", "cloud", "internal", "local", "test"}
	}
	if o.fams == nil {
		o.fams, o.textFams = withWords(families, o.Words), withWords(textFamilies, o.Words)
	}
	if o.hostRe == nil {
		var alts []string
		for _, p := range o.Prefixes {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				alts = append(alts, regexp.QuoteMeta(p)+`[a-z0-9-]+`)
			}
		}
		var sfx []string
		for _, s := range o.Suffixes {
			if s = strings.ToLower(strings.Trim(strings.TrimSpace(s), ".")); s != "" {
				sfx = append(sfx, regexp.QuoteMeta(s))
			}
		}
		alts = append(alts, `(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:`+strings.Join(sfx, "|")+`)`)
		o.hostRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(alts, "|") + `)\b`)
	}
}

// --- families, subjects, hosts -------------------------------------------------------------------------------------

type fam struct {
	name string
	re   *regexp.Regexp
}

// families: alertname (or text) -> family. First match wins; order matters.
var families = []fam{
	{"test", regexp.MustCompile(`^test|routingtest|selftest|self-test`)},
	{"http-5xx", regexp.MustCompile(`upstreamtimeout|http5\d\d|5xx|http_?50[0-4]|502|503|504|bad ?gateway|gateway ?time`)},
	{"http-4xx", regexp.MustCompile(`http4\d\d|4xx`)},
	{"latency", regexp.MustCompile(`responsetime|latency|slow`)},
	{"memory", regexp.MustCompile(`outofmemory|\boom|oomkill|memory|swap`)},
	{"disk", regexp.MustCompile(`disk|filesystem|inode|space`)},
	{"cert", regexp.MustCompile(`cert|ssl|tls`)},
	{"down", regexp.MustCompile(`down|unreachable|healthcheck|absent|^up$|probe|kuma`)},
	{"cpu", regexp.MustCompile(`cpu|load`)},
}

// textFamilies: free text (chat), stricter words only.
var textFamilies = []fam{
	{"http-5xx", regexp.MustCompile(`\b50[234]\b|5xx|bad gateway|gateway time-?out|upstream`)},
	{"latency", regexp.MustCompile(`\bslow|latency|timeout|time-?out`)},
	{"memory", regexp.MustCompile(`out of memory|\boom\b|memory`)},
	{"disk", regexp.MustCompile(`disk|no space|filesystem`)},
	{"down", regexp.MustCompile(`\bdown\b|unreachable|not reachable|stuck|queue`)},
}

var (
	spaceUnderRe = regexp.MustCompile(`[\s_]+`)
	notFamRe     = regexp.MustCompile(`[^a-z0-9-]`)
	schemeRe     = regexp.MustCompile(`^[a-z]+://`)
	portRe       = regexp.MustCompile(`:\d*$`)
	slugRe       = regexp.MustCompile(`[^a-z0-9]+`)
	backtickRe   = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]{3,})`")
	camelRe      = regexp.MustCompile(`\b([A-Z][a-z]+(?:[A-Z][a-z0-9]+){2,}|service_down)\b`)
	knownIncRe   = regexp.MustCompile(`(?i)known incident #\d+`)
	selfFailRe   = regexp.MustCompile(`(?i)could not open the MR|sandbox denied|permission`)
	statusHeadRe = regexp.MustCompile(`^\W*(OK|WARN|FAIL)\W*`)
)

// FamilyOf is the symptom family of an alert name, else of free text (the built-in words only).
func FamilyOf(alertname, text string) string {
	return familyOf(families, textFamilies, alertname, text)
}

// familyOf is FamilyOf with the configured extra words.
func (o *Options) familyOf(alertname, text string) string {
	o.defaults()
	return familyOf(o.fams, o.textFams, alertname, text)
}

// withWords adds the extra words of each family to its pattern.
func withWords(base []fam, words map[string][]string) []fam {
	out := make([]fam, len(base))
	for i, f := range base {
		out[i] = f
		var alts []string
		for _, w := range words[f.name] {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
				alts = append(alts, regexp.QuoteMeta(w))
			}
		}
		if len(alts) > 0 {
			out[i].re = regexp.MustCompile(f.re.String() + "|" + strings.Join(alts, "|"))
		}
	}
	return out
}

func familyOf(fams, textFams []fam, alertname, text string) string {
	n := strings.ToLower(spaceUnderRe.ReplaceAllString(alertname, ""))
	if n != "" {
		for _, f := range fams {
			if f.re.MatchString(n) {
				return f.name
			}
		}
		if s := head(notFamRe.ReplaceAllString(n, ""), 40); s != "" {
			return s
		}
		return "other"
	}
	t := strings.ToLower(text)
	for _, f := range textFams {
		if f.re.MatchString(t) {
			return f.name
		}
	}
	return "other"
}

// NormSubject is a subject as incidents store it: lower case, no scheme, path or port, at most 120 characters.
func NormSubject(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = schemeRe.ReplaceAllString(s, "")
	s, _, _ = strings.Cut(s, "/")
	s = portRe.ReplaceAllString(s, "")
	return head(s, 120)
}

// HostsIn are the host names in text, in order, without duplicates and skipped hosts.
func (o *Options) HostsIn(text string) []string {
	o.defaults()
	var out []string
	for _, m := range o.hostRe.FindAllString(text, -1) {
		h := NormSubject(m)
		if !contains(out, h) && !contains(o.SkipHosts, h) && !strings.HasSuffix(h, ".md") {
			out = append(out, h)
		}
	}
	return out
}

func (o *Options) customerFacing(subject string) bool {
	s := NormSubject(subject)
	if s == "" {
		return false
	}
	for _, p := range o.Customer {
		if ok, _ := path.Match(strings.ToLower(strings.TrimSpace(p)), s); ok {
			return true
		}
	}
	return false
}

// KeyOf is the problem key DC|subject|family.
func KeyOf(dc, subject, family string) string {
	return strings.ToUpper(dc) + "|" + subject + "|" + family
}

// --- candidates ----------------------------------------------------------------------------------------------------

// Cand is one candidate problem key of a finding.
type Cand struct{ DC, Subject, Family string }

// Alert is one alert of an incident job's data file or of the live snapshot.
type Alert map[string]any

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprint(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (o *Options) alertCand(a Alert) Cand {
	dc := head(strings.ToUpper(str(a["dc"])), 20)
	labels, _ := a["labels"].(map[string]any)
	subj := str(labels["host"])
	if subj == "" {
		subj = str(a["instance"])
	}
	if subj == "" {
		subj = str(a["alertname"])
	}
	f := o.familyOf(str(a["alertname"]), "")
	if str(a["kind"]) == "kuma" || dc == "KUMA" {
		f = "down"
	}
	return Cand{dc, NormSubject(subj), f}
}

func fingerprint(a Alert) string {
	if fp := str(a["fp"]); fp != "" {
		return head(fp, 200)
	}
	return head(str(a["dc"])+"|"+str(a["alertname"])+"|"+str(a["instance"]), 200)
}

var sevRank = map[string]int{"critical": 4, "page": 4, "high": 3, "error": 3, "medium": 2, "warning": 2, "low": 1, "info": 1}
var envRank = map[string]int{"release": 4, "pre-release": 3, "develop": 2, "unknown": 1, "": 0}
var envNorm = map[string]string{"production": "release", "prod": "release", "release": "release",
	"pre-production": "pre-release", "pre-release": "pre-release", "pre": "pre-release", "stage": "develop",
	"develop": "develop", "dev": "develop"}

// Problem is one problem of a run (triage_problems row, or the run's summary).
type Problem struct {
	Idx         *int64
	Title       string
	Severity    string
	Environment string
	Summary     string
	Sources     []string
}

// AlertRow is one alert row of a log-RCA run (alerts table): name, severity, target.
type AlertRow struct{ Name, Severity, Target string }

// primaryRow is the alert a log-RCA summary talks about first (an RCA often names other, already-covered alerts
// later); else the first alert name in backticks; else the first row.
func primaryRow(text string, rows []AlertRow) (AlertRow, bool) {
	best, pos := AlertRow{}, -1
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		if i := strings.Index(text, r.Name); i >= 0 && (pos < 0 || i < pos || i == pos && r.Name < best.Name) {
			best, pos = r, i
		}
	}
	if pos >= 0 {
		return best, true
	}
	if m := backtickRe.FindStringSubmatch(text); m != nil {
		return AlertRow{Name: m[1]}, true
	}
	if len(rows) > 0 {
		return rows[0], true
	}
	return AlertRow{}, false
}

// coveredHosts are the hosts named in a sentence that says it is a known incident ("Known incident #N"): they are
// covered by that incident and are never candidates of this finding.
func (o *Options) coveredHosts(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, s := range strings.Split(line, ". ") {
			if knownIncRe.MatchString(s) {
				out = append(out, o.HostsIn(s)...)
			}
		}
	}
	return out
}

// Candidates are the ordered candidate keys of one finding. task: incident, log-rca, chat-triage (or another task
// with a problem).
//
// log-RCA (port requirement): the finding attaches only to the primary alert's host (its target, else the first host
// of the summary that is not covered by a known incident); hosts the summary names as already covered by a known
// incident ("Known incident #N") are never candidates.
func (o *Options) Candidates(task string, p Problem, alerts []Alert, summary string, rows []AlertRow) []Cand {
	o.defaults()
	var cands []Cand
	switch {
	case task == "incident" && len(alerts) > 0:
		ranked := append([]Alert{}, alerts...)
		sort.SliceStable(ranked, func(i, j int) bool {
			return sevRank[strings.ToLower(str(ranked[i]["severity"]))] > sevRank[strings.ToLower(str(ranked[j]["severity"]))]
		})
		for _, a := range ranked {
			cands = append(cands, o.alertCand(a))
		}
	case task == "log-rca":
		dc := ""
		if len(o.Sites) > 0 {
			dc = strings.ToUpper(o.Sites[0])
		}
		row, ok := primaryRow(summary, rows)
		f := o.familyOf("", summary)
		if ok && row.Name != "" {
			f = o.familyOf(row.Name, "")
		}
		covered := o.coveredHosts(summary)
		host := NormSubject(row.Target)
		if host == "" || contains(covered, host) || contains(o.SkipHosts, host) {
			host = ""
			for _, h := range o.HostsIn(summary) {
				if !contains(covered, h) {
					host = h
					break
				}
			}
		}
		if host != "" {
			cands = []Cand{{dc, host, f}}
		} else if ok && row.Name != "" {
			cands = []Cand{{dc, NormSubject(row.Name), f}}
		}
	default:
		text := p.Title + " " + p.Summary
		src := strings.Join(p.Sources, " ")
		dc := ""
		for _, s := range o.Sites {
			if s != "" && strings.Contains(strings.ToLower(src), strings.ToLower(s)) {
				dc = strings.ToUpper(s)
				break
			}
		}
		names := camelRe.FindAllStringSubmatch(src+" "+text, -1)
		f := o.familyOf("", text)
		if len(names) > 0 {
			f = o.familyOf(names[0][1], "")
		}
		hosts := o.HostsIn(text)
		if len(hosts) == 0 {
			hosts = o.HostsIn(src)
		}
		if len(hosts) > 0 {
			for _, h := range hosts {
				cands = append(cands, Cand{dc, h, f})
			}
		} else {
			cands = []Cand{{dc, slug(p.Title, "untitled"), f}}
		}
	}
	if task == "incident" && len(cands) == 0 {
		text := p.Title + " " + p.Summary
		for _, h := range o.HostsIn(text) {
			cands = append(cands, Cand{"", h, o.familyOf("", text)})
		}
		if len(cands) == 0 {
			t := p.Title
			if t == "" {
				t = "incident"
			}
			cands = []Cand{{"", slug(t, ""), "other"}}
		}
	}
	var out []Cand
	for _, c := range cands {
		if c.Subject != "" && !containsCand(out, c) {
			out = append(out, c)
		}
	}
	return out
}

func slug(s, def string) string {
	v := head(strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-"), 60)
	if v == "" {
		return def
	}
	return v
}

// --- matching and attaching ----------------------------------------------------------------------------------------

type row = map[string]any

func queryRows(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]row, error) {
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

func i64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func s(v any) string {
	if v == nil {
		return ""
	}
	if x, ok := v.(string); ok {
		return x
	}
	return fmt.Sprint(v)
}

func open(status string) bool { return status == "open" || status == "acknowledged" }

// find is the incident of the first candidate that matches and how: "open" (attach) or "reopen" (resolved within
// ReopenDays).
func (o *Options) find(tx *sql.Tx, cands []Cand, now int64) (row, string, error) {
	for _, c := range cands {
		rows, err := queryRows(tx, `SELECT * FROM incidents WHERE subject=? AND family=? ORDER BY status='resolved', last_seen DESC`, c.Subject, c.Family)
		if err != nil {
			return nil, "", err
		}
		for _, r := range rows {
			if c.DC != "" && s(r["dc"]) != "" && s(r["dc"]) != c.DC {
				continue
			}
			if open(s(r["status"])) {
				return r, "open", nil
			}
			if ra := i64(r["resolved_at"]); ra != 0 && now-ra <= int64(o.ReopenDays)*86400 {
				return r, "reopen", nil
			}
		}
	}
	return nil, "", nil
}

func worse(a, b string, rank map[string]int) string {
	if rank[strings.ToLower(a)] >= rank[strings.ToLower(b)] {
		return a
	}
	return b
}

// Finding is one finding to attach.
type Finding struct {
	Task, Title, Severity, Env, AlertSeverity, Text string
	RunID                                           *int64
	Idx                                             *int64
	At                                              int64
	Fingerprints                                    []string
}

// Attach attaches one finding to the incident of its first matching candidate (or a new one) and returns its id;
// 0 without candidates.
func (o *Options) Attach(tx *sql.Tx, f Finding, cands []Cand) (int64, error) {
	o.defaults()
	if len(cands) == 0 {
		return 0, nil
	}
	env := envNorm[strings.ToLower(f.Env)]
	if env == "" && f.Env != "" {
		env = "unknown"
	}
	at := f.At
	inc, how, err := o.find(tx, cands, at)
	if err != nil {
		return 0, err
	}
	if inc == nil {
		c := cands[0]
		title := f.Title
		if title == "" {
			title = c.Subject + " " + c.Family
		}
		res, err := tx.Exec(`INSERT INTO incidents (key, dc, subject, family, title, env, severity, alert_severity, customer_facing,
 status, first_seen, last_seen, occurrences, fingerprints) VALUES (?,?,?,?,?,?,?,?,?,'open',?,?,0,'[]')`,
			KeyOf(c.DC, c.Subject, c.Family), c.DC, c.Subject, c.Family, head(title, 300), env, f.Severity, f.AlertSeverity,
			b2i(o.customerFacing(c.Subject)), at, at)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		rows, err := queryRows(tx, `SELECT * FROM incidents WHERE id=?`, id)
		if err != nil || len(rows) == 0 {
			return 0, fmt.Errorf("incidents: new incident not found: %v", err)
		}
		inc = rows[0]
	}
	id := i64(inc["id"])
	if how == "reopen" {
		if _, err := tx.Exec(`UPDATE incidents SET status='open', resolved_at=NULL, resolved_by=NULL, resolve_note=NULL,
 acked_at=NULL, acked_by=NULL, clear_since=NULL WHERE id=?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO incident_events (incident_id, at, kind, run_id, task, actor, text) VALUES (?,?,?,?,?,?,?)`,
			id, at, "reopen", f.RunID, f.Task, o.Actor, "re-opened: the same problem was found again"); err != nil {
			return 0, err
		}
	}
	dc := s(inc["dc"])
	if dc == "" {
		for _, c := range cands {
			if c.DC != "" {
				dc = c.DC
				break
			}
		}
	}
	var old []string
	json.Unmarshal([]byte(s(inc["fingerprints"])), &old)
	fps := append([]string{}, old...)
	for _, fp := range f.Fingerprints {
		if fp != "" && !contains(old, fp) {
			fps = append(fps, fp)
		}
	}
	if len(fps) > 30 {
		fps = fps[len(fps)-30:]
	}
	if fps == nil {
		fps = []string{}
	}
	fpj, _ := json.Marshal(fps)
	subj, family := s(inc["subject"]), s(inc["family"])
	if _, err := tx.Exec(`UPDATE incidents SET dc=?, key=?, last_seen=MAX(COALESCE(last_seen,0), ?), first_seen=MIN(COALESCE(first_seen,?), ?),
 occurrences=COALESCE(occurrences,0)+1, severity=?, alert_severity=?, env=?, customer_facing=?, clear_since=NULL, fingerprints=? WHERE id=?`,
		dc, KeyOf(dc, subj, family), at, at, at, worse(f.Severity, s(inc["severity"]), sevRank),
		worse(f.AlertSeverity, s(inc["alert_severity"]), sevRank), worse(env, s(inc["env"]), envRank),
		b2i(i64(inc["customer_facing"]) != 0 || o.customerFacing(subj)), pyJSON(fpj), id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO incident_events (incident_id, at, kind, run_id, idx, task, actor, text) VALUES (?,?,?,?,?,?,?,?)`,
		id, at, "finding", f.RunID, f.Idx, f.Task, o.Actor, head(f.Text, 2000)); err != nil {
		return 0, err
	}
	return id, refreshSources(tx, id)
}

// pyJSON writes a JSON list of strings as the earlier installation did (", " separators, non-ASCII as \uXXXX), so
// both write the same text.
func pyJSON(b []byte) string {
	var xs []string
	if json.Unmarshal(b, &xs) != nil {
		return string(b)
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		var q strings.Builder
		q.WriteByte('"')
		for _, r := range x {
			switch {
			case r == '"' || r == '\\':
				q.WriteByte('\\')
				q.WriteRune(r)
			case r == '\n':
				q.WriteString(`\n`)
			case r == '\t':
				q.WriteString(`\t`)
			case r == '\r':
				q.WriteString(`\r`)
			case r < 0x20 || r > 0x7e:
				if r > 0xffff {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(&q, `\u%04x\u%04x`, r1, r2)
				} else {
					fmt.Fprintf(&q, `\u%04x`, r)
				}
			default:
				q.WriteRune(r)
			}
		}
		q.WriteByte('"')
		parts[i] = q.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- where an incident came from (the Source filter of the incidents page) -----------------------------------------

var sourceOrder = []string{"qa", "kuma", "alertmanager", "cicd", "matrix", "security", "auto"}

var (
	qaRe     = regexp.MustCompile(`\bqa\b`)
	alertRe  = regexp.MustCompile(`alert|prometheus|grafana|loki`)
	cicdRe   = regexp.MustCompile(`gitlab|pipeline|ci/cd|\bci\b`)
	matrixRe = regexp.MustCompile(`matrix|\bdm\b|forward`)
)

func sourceKeys(task, dc string, sources []string) map[string]bool {
	dc = strings.ToUpper(dc)
	switch {
	case task == "cicd" || dc == "CICD":
		return map[string]bool{"cicd": true}
	case task == "cve" || dc == "CVE":
		return map[string]bool{"security": true}
	case task == "incident":
		if dc == "KUMA" {
			return map[string]bool{"kuma": true}
		}
		return map[string]bool{"alertmanager": true}
	case task == "log-rca":
		return map[string]bool{"alertmanager": true}
	case task == "chat-triage":
		out := map[string]bool{}
		for _, src := range sources {
			l := strings.ToLower(src)
			hd := head(l, 40)
			if strings.Contains(l, ":") {
				x, _, _ := strings.Cut(l, ":")
				hd = strings.TrimSpace(x)
			}
			switch {
			case strings.Contains(hd, "kuma") || hd == "" && strings.Contains(l, "kuma"):
				out["kuma"] = true
			case qaRe.MatchString(hd):
				out["qa"] = true
			case alertRe.MatchString(hd):
				out["alertmanager"] = true
			case cicdRe.MatchString(hd):
				out["cicd"] = true
			case matrixRe.MatchString(hd):
				out["matrix"] = true
			}
		}
		if len(out) == 0 {
			out["qa"] = true // chat triage reads the QA room: an unnamed source is a QA message
		}
		return out
	}
	return map[string]bool{"auto": true}
}

// refreshSources recomputes incidents.sources from the incident's findings and the messages linked to it.
func refreshSources(tx *sql.Tx, id int64) error {
	incs, err := queryRows(tx, `SELECT dc FROM incidents WHERE id=?`, id)
	if err != nil || len(incs) == 0 {
		return err
	}
	dc := s(incs[0]["dc"])
	evs, err := queryRows(tx, `SELECT e.task, p.sources FROM incident_events e LEFT JOIN triage_problems p ON p.run_id=e.run_id AND p.idx=e.idx
 WHERE e.incident_id=? AND e.kind='finding'`, id)
	if err != nil {
		return nil // no triage_problems table: leave the column as it is
	}
	keys := map[string]bool{}
	for _, e := range evs {
		var src []string
		raw := s(e["sources"])
		if raw != "" && json.Unmarshal([]byte(raw), &src) != nil {
			src = []string{raw}
		}
		for k := range sourceKeys(s(e["task"]), dc, src) {
			keys[k] = true
		}
	}
	if ms, err := queryRows(tx, `SELECT DISTINCT source FROM chat_messages WHERE incident_id=?`, id); err == nil {
		for _, m := range ms {
			if contains(sourceOrder, s(m["source"])) {
				keys[s(m["source"])] = true
			}
		}
	}
	if len(evs) == 0 && strings.ToUpper(dc) == "CICD" {
		keys["cicd"] = true
	}
	var ks []string
	for _, k := range sourceOrder {
		if keys[k] {
			ks = append(ks, k)
		}
	}
	b, _ := json.Marshal(ks)
	if ks == nil {
		b = []byte("[]")
	}
	_, err = tx.Exec(`UPDATE incidents SET sources=? WHERE id=?`, pyJSON(b), id)
	return err
}

// --- one recorded run -------------------------------------------------------------------------------------------------

// JobAlerts are the alerts of an incident job's data file ({"alerts": [...]}).
func JobAlerts(path string) []Alert {
	var doc struct {
		Alerts []any `json:"alerts"`
	}
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var out []Alert
	for _, a := range doc.Alerts {
		if m, ok := a.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// AttachRun attaches every finding of one recorded run (incident, chat-triage, log-rca) once; the ids attached.
// data is the incident job's data file (its alerts), may be empty.
func (o *Options) AttachRun(tx *sql.Tx, task string, runID int64, data string) ([]int64, error) {
	o.defaults()
	if r, err := queryRows(tx, `SELECT 1 FROM incident_events WHERE run_id=? AND kind='finding' LIMIT 1`, runID); err != nil || len(r) > 0 {
		return nil, err
	}
	runs, err := queryRows(tx, `SELECT * FROM runs WHERE id=?`, runID)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	run := runs[0]
	at := i64(run["started_at"])
	if at == 0 {
		at = o.Now().Unix()
	}
	rid := runID
	var ids []int64
	switch task {
	case "incident", "chat-triage":
		var alerts []Alert
		if task == "incident" && data != "" {
			alerts = JobAlerts(data)
		}
		var fps []string
		asev := ""
		for _, a := range alerts {
			fps = append(fps, fingerprint(a))
			if sv := strings.ToLower(str(a["severity"])); sevRank[sv] > sevRank[asev] {
				asev = sv
			}
		}
		probs, err := queryRows(tx, `SELECT * FROM triage_problems WHERE run_id=? ORDER BY idx`, runID)
		if err != nil {
			probs = nil
		}
		if len(probs) == 0 && task == "incident" && len(alerts) > 0 {
			probs = []row{{"idx": nil, "title": "", "severity": "", "environment": "", "summary": run["summary"]}}
		}
		skip := map[int64]bool{}
		if task == "chat-triage" {
			if rs, err := queryRows(tx, `SELECT idx FROM triage_actionability WHERE run_id=? AND actionability='needs_info'`, runID); err == nil {
				for _, r := range rs {
					skip[i64(r["idx"])] = true
				}
			}
		}
		for _, pr := range probs {
			if task == "incident" && s(pr["title"]) == "ops-watch self-test" {
				continue
			}
			var idx *int64
			if pr["idx"] != nil {
				v := i64(pr["idx"])
				idx = &v
				if skip[v] {
					continue
				}
			}
			p := Problem{Idx: idx, Title: s(pr["title"]), Severity: strings.ToLower(s(pr["severity"])), Environment: s(pr["environment"]), Summary: s(pr["summary"])}
			if raw := s(pr["sources"]); raw != "" && json.Unmarshal([]byte(raw), &p.Sources) != nil {
				p.Sources = []string{raw}
			}
			text := p.Summary
			if text == "" {
				text = s(run["summary"])
			}
			id, err := o.Attach(tx, Finding{Task: task, Title: p.Title, Severity: p.Severity, Env: p.Environment, AlertSeverity: asev,
				Text: text, RunID: &rid, Idx: idx, At: at, Fingerprints: fps}, o.Candidates(task, p, alerts, "", nil))
			if err != nil {
				return ids, err
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
	case "log-rca":
		if st := s(run["status"]); st != "WARN" && st != "FAIL" {
			return nil, nil
		}
		ar, err := queryRows(tx, `SELECT alertname, severity, target FROM alerts WHERE run_id=? ORDER BY CASE severity
 WHEN 'critical' THEN 0 WHEN 'page' THEN 0 WHEN 'error' THEN 1 ELSE 2 END`, runID)
		if err != nil {
			return nil, err
		}
		var rows []AlertRow
		for _, r := range ar {
			rows = append(rows, AlertRow{s(r["alertname"]), s(r["severity"]), s(r["target"])})
		}
		summary := s(run["summary"])
		if selfFailRe.MatchString(head(summary, 300)) && len(o.HostsIn(summary)) == 0 {
			return nil, nil // the agent's own failure, not a production problem
		}
		cands := o.Candidates("log-rca", Problem{}, nil, summary, rows)
		nameSev, name := "", "alert"
		if len(rows) > 0 {
			nameSev, name = rows[0].Severity, rows[0].Name
		}
		for _, r := range rows {
			if r.Name != "" && strings.Contains(summary, r.Name) {
				nameSev = r.Severity
				break
			}
		}
		if prim, ok := primaryRow(summary, rows); ok && prim.Name != "" && strings.Contains(summary, prim.Name) {
			name = prim.Name
		}
		env, title := "", ""
		if len(cands) > 0 {
			env = "release"
			title = name + " on " + cands[0].Subject
		} else {
			first, _, _ := strings.Cut(summary, "\n")
			title = head(statusHeadRe.ReplaceAllString(first, ""), 200)
		}
		sev := "medium"
		if l := strings.ToLower(nameSev); l == "critical" || l == "page" {
			sev = "high"
		}
		id, err := o.Attach(tx, Finding{Task: task, Title: title, Severity: sev, Env: env, AlertSeverity: strings.ToLower(nameSev),
			Text: summary, RunID: &rid, At: at}, cands)
		if err != nil {
			return nil, err
		}
		if id != 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// --- auto-resolve ------------------------------------------------------------------------------------------------------

// Live is the set of problem keys currently firing.
type Live map[Cand]bool

func loadJSON(p string, v any) bool {
	b, err := os.ReadFile(p)
	return err == nil && json.Unmarshal(b, v) == nil
}

// LiveAlerts reads what fires now: the alerts snapshot (alerts-firing.json), the watch state (watch-state.json) and
// the open uptime monitors (kuma-open.json), whichever exist. The DC names of the snapshot are returned too.
func (o *Options) LiveAlerts() (Live, []string) {
	live, dcs := Live{}, []string{}
	var snap struct {
		DCs []struct {
			DC     string  `json:"dc"`
			Alerts []Alert `json:"alerts"`
		} `json:"dcs"`
	}
	af := o.AlertsFile
	if af == "" {
		af = filepath.Join(o.StateDir, "alerts-firing.json")
	}
	if loadJSON(af, &snap) {
		for _, d := range snap.DCs {
			dc := strings.ToUpper(d.DC)
			dcs = append(dcs, dc)
			for _, a := range d.Alerts {
				if str(a["state"]) == "firing" {
					b := Alert{}
					for k, v := range a {
						b[k] = v
					}
					b["dc"] = dc
					live[o.alertCand(b)] = true
				}
			}
		}
	}
	var ws struct {
		Alerts map[string]Alert `json:"alerts"`
	}
	if loadJSON(filepath.Join(o.StateDir, "watch-state.json"), &ws) {
		for _, a := range ws.Alerts {
			if f, _ := a["firing"].(bool); f {
				live[o.alertCand(a)] = true
			}
		}
	}
	var kuma map[string]any
	if loadJSON(filepath.Join(o.StateDir, "kuma-open.json"), &kuma) {
		for mon := range kuma {
			live[Cand{"KUMA", NormSubject(mon), "down"}] = true
		}
	}
	return live, dcs
}

func firing(inc row, live Live) bool {
	for c := range live {
		if c.Subject == s(inc["subject"]) && c.Family == s(inc["family"]) && (s(inc["dc"]) == "" || c.DC == s(inc["dc"])) {
			return true
		}
	}
	return false
}

// Change is an incident that changed and its note for the thread.
type Change struct {
	ID   int64
	Text string
}

// AutoResolve resolves the open incidents with nothing firing for long enough. alertDCs are the DCs whose incidents
// are alert-backed (the monitoring sites, the snapshot's sources, KUMA).
func (o *Options) AutoResolve(tx *sql.Tx, live Live, alertDCs []string) ([]Change, error) {
	o.defaults()
	now := o.Now().Unix()
	incs, err := queryRows(tx, `SELECT * FROM incidents WHERE status IN ('open','acknowledged') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	backed := map[string]bool{"KUMA": true}
	for _, d := range append(append([]string{}, alertDCs...), o.Sites...) {
		if d != "" {
			backed[strings.ToUpper(d)] = true
		}
	}
	var changed []Change
	for _, inc := range incs {
		id, dc := i64(inc["id"]), s(inc["dc"])
		if dc == "CVE" || dc == "CRED" {
			continue // resolved on their own evidence, not by a timer
		}
		if firing(inc, live) {
			if _, err := tx.Exec(`UPDATE incidents SET last_firing=?, clear_since=NULL WHERE id=?`, now, id); err != nil {
				return nil, err
			}
			continue
		}
		clear := i64(inc["clear_since"])
		if clear == 0 {
			clear = max(i64(inc["last_seen"]), i64(inc["last_firing"]))
			if clear == 0 {
				clear = now
			}
			if _, err := tx.Exec(`UPDATE incidents SET clear_since=? WHERE id=?`, clear, id); err != nil {
				return nil, err
			}
		}
		alertBacked := backed[dc] || i64(inc["last_firing"]) != 0
		hours := o.ChatHours
		if alertBacked {
			hours = o.AutoHours
		}
		if now-max(clear, i64(inc["last_seen"])) >= int64(hours)*3600 {
			note := fmt.Sprintf("auto-resolved: no new report for %d h", hours)
			if alertBacked {
				note = fmt.Sprintf("auto-resolved: no matching alert firing for %d h", hours)
			}
			if _, err := tx.Exec(`UPDATE incidents SET status='resolved', resolved_at=?, resolved_by=?, resolve_note=? WHERE id=?`, clear, o.Actor, note, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`INSERT INTO incident_events (incident_id, at, kind, actor, text) VALUES (?,?,?,?,?)`, id, now, "auto-resolve", o.Actor, note); err != nil {
				return nil, err
			}
			changed = append(changed, Change{id, note})
		}
	}
	return changed, nil
}

// --- requests from the web app -----------------------------------------------------------------------------------------

var (
	actionNameRe = regexp.MustCompile(`^[0-9]{10}-[0-9a-f]{8}\.json$`)
	userRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)
	ctrlRe       = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

// Request is one validated ack / resolve / reopen request.
type Request struct {
	Action, By, Role, Note string
	ID                     int64
}

// ReadRequest reads and validates one request file (no symlink, at most 4096 bytes, fixed fields only).
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
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil || m == nil {
		return Request{}, fmt.Errorf("not an object")
	}
	action, _ := m["action"].(string)
	by := str(m["by"])
	role, _ := m["role"].(string)
	if (action != "ack" && action != "resolve" && action != "reopen") || !userRe.MatchString(by) || (role != "admin" && role != "viewer") {
		return Request{}, fmt.Errorf("bad fields")
	}
	n, ok := m["id"].(json.Number)
	id, err := n.Int64()
	if !ok || err != nil || id <= 0 {
		return Request{}, fmt.Errorf("bad id")
	}
	if (action == "resolve" || action == "reopen") && role != "admin" {
		return Request{}, fmt.Errorf("admin only")
	}
	note := ""
	if v, ok := m["note"].(string); ok {
		note = v
	} else if m["note"] != nil {
		note = str(m["note"])
	}
	return Request{Action: action, ID: id, By: by, Role: role, Note: head(ctrlRe.ReplaceAllString(note, " "), 300)}, nil
}

// ApplyRequest applies one validated request; the thread note, or "" when nothing changed.
func (o *Options) ApplyRequest(tx *sql.Tx, r Request) (string, error) {
	o.defaults()
	now := o.Now().Unix()
	incs, err := queryRows(tx, `SELECT * FROM incidents WHERE id=?`, r.ID)
	if err != nil || len(incs) == 0 {
		return "", err
	}
	st, text := s(incs[0]["status"]), ""
	var note any
	if r.Note != "" {
		note = r.Note
	}
	switch {
	case r.Action == "ack" && st == "open":
		_, err = tx.Exec(`UPDATE incidents SET status='acknowledged', acked_at=?, acked_by=?, owner=COALESCE(owner, ?) WHERE id=?`, now, r.By, r.By, r.ID)
		text = "acknowledged by " + r.By
	case r.Action == "resolve" && open(st):
		_, err = tx.Exec(`UPDATE incidents SET status='resolved', resolved_at=?, resolved_by=?, resolve_note=? WHERE id=?`, now, r.By, note, r.ID)
		text = "resolved by " + r.By
		if r.Note != "" {
			text += ": " + r.Note
		}
	case r.Action == "reopen" && st == "resolved":
		_, err = tx.Exec(`UPDATE incidents SET status='open', resolved_at=NULL, resolved_by=NULL, resolve_note=NULL, clear_since=NULL WHERE id=?`, r.ID)
		text = "re-opened by " + r.By
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(`INSERT INTO incident_events (incident_id, at, kind, actor, text) VALUES (?,?,?,?,?)`, r.ID, now, r.Action, r.By, note)
	return text, err
}

// ProcessRequests applies the request files of ActionsDir (at most 200 per run); every file is removed after it
// was read, a rejected one too.
func (o *Options) ProcessRequests(tx *sql.Tx) ([]Change, error) {
	o.defaults()
	ents, err := os.ReadDir(o.ActionsDir)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) > 200 {
		names = names[:200]
	}
	var done []Change
	for _, n := range names {
		p := filepath.Join(o.ActionsDir, n)
		if !actionNameRe.MatchString(n) {
			if !strings.HasPrefix(n, ".") {
				os.Remove(p)
			}
			continue
		}
		r, err := ReadRequest(p)
		if err == nil {
			var text string
			if text, err = o.ApplyRequest(tx, r); err != nil {
				return done, err
			}
			res := text
			if res == "" {
				res = "no change"
			}
			o.Log.Info("incident request", "file", n, "action", r.Action, "id", r.ID, "by", r.By, "result", res)
			if text != "" {
				done = append(done, Change{r.ID, text})
			}
		} else {
			o.Log.Warn("incident request rejected", "file", n, "reason", err.Error())
		}
		os.Remove(p)
	}
	return done, nil
}

// --- the job -------------------------------------------------------------------------------------------------------------

// Tick applies the web app's requests and, when resolve is set, auto-resolves against the live alerts; then posts a
// thread note for every changed incident that has a thread.
func (o *Options) Tick(db *sql.DB, resolve bool) ([]Change, error) {
	o.defaults()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	changes, err := o.ProcessRequests(tx)
	if err != nil {
		return nil, err
	}
	if resolve {
		live, dcs := o.LiveAlerts()
		more, err := o.AutoResolve(tx, live, dcs)
		if err != nil {
			return nil, err
		}
		changes = append(changes, more...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	o.notes(db, changes)
	return changes, nil
}

func (o *Options) notes(db *sql.DB, changes []Change) {
	if o.Note == nil {
		return
	}
	for _, c := range changes {
		rows, err := queryRows(db, `SELECT * FROM incidents WHERE id=?`, c.ID)
		if err != nil || len(rows) == 0 || s(rows[0]["thread_root"]) == "" {
			continue
		}
		o.Note(rows[0], c.Text)
	}
}

// Backfill attaches every recorded run (incident, chat-triage, log-rca) that has no incident yet, then auto-resolves
// when resolve is set. dataDir holds the incident job's data files (runs/<log file name>).
func (o *Options) Backfill(db *sql.DB, dataDir string, resolve bool) (int, error) {
	o.defaults()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	runs, err := queryRows(tx, `SELECT id, task, log_path FROM runs WHERE task IN ('incident','chat-triage','log-rca') ORDER BY started_at, id`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		data := ""
		if s(r["task"]) == "incident" && s(r["log_path"]) != "" {
			data = filepath.Join(dataDir, filepath.Base(s(r["log_path"])))
		}
		ids, err := o.AttachRun(tx, s(r["task"]), i64(r["id"]), data)
		if err != nil {
			return n, err
		}
		n += len(ids)
	}
	if resolve {
		live, dcs := o.LiveAlerts()
		if _, err := o.AutoResolve(tx, live, dcs); err != nil {
			return n, err
		}
	}
	return n, tx.Commit()
}

// HostOf is the lower-case host of a URL ("" when it has none): the own, GitLab and chat hosts are never subjects.
func HostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// --- helpers -------------------------------------------------------------------------------------------------------------

func head(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func containsCand(xs []Cand, x Cand) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
