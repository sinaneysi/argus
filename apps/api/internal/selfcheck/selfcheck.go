// Package selfcheck is Argus's own health check (the worker job `selfcheck`, every 5 minutes; port of the earlier
// installation's self-check job). It checks Argus's own parts, never production:
//
//	jobs         every worker job: last run against its interval, last result; a job without its data source is
//	             "not configured", never a failure
//	alerts       the alerts snapshot's age, while the alerts collector runs
//	creds        the credential status file's age and the credentials that are not OK (when it is written)
//	credentials  the credential expiry file: its age, and one check per credential (expired or rejected FAIL,
//	             near expiry or rotation due WARN)
//	disk         free space of the state volume
//	databases    an integrity check of the databases (once an hour; the last result in between)
//	key          the master key is present and opens every sealed secret of the volume
//	sources      Settings -> Data sources entries whose last check failed
//	backup       the last state backup's age (when a backup job writes backup-status.json)
//	internal     own failures of the last 24 h: model run errors, sandbox denials, failed notices
//	claude       the Claude usage switch (off is the admin's choice: shown, never a failure)
//
// It writes selfcheck.json (0644, no secrets), the file the Overview reads. OK / WARN / FAIL per check, the worst
// wins. Only when Argus has been FAIL for longer than PageAfter does it send one notice ("<app>-internal",
// source selfcheck), and one more when it recovers. When the result is not FAIL and a push URL is set, it pings the
// push monitor (an external dead-man switch).
package selfcheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// Check is one result line.
type Check struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Rank orders the states.
var Rank = map[string]int{"OK": 0, "WARN": 1, "FAIL": 2}

// Group is the group of the checks of Argus's own parts.
const Group = "argus"

func mk(name, status, detail, group string) Check {
	if _, ok := Rank[status]; !ok {
		status = "WARN"
	}
	return Check{Name: name, Group: group, Status: status, Detail: detail}
}

func worse(a, b string) string {
	if Rank[b] > Rank[a] {
		return b
	}
	return a
}

// Human is a short age: "3d", "2h", "5m", "40s"; "never" for a negative age.
func Human(sec int64) string {
	if sec < 0 {
		return "never"
	}
	for _, u := range []struct {
		s string
		n int64
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if sec >= u.n {
			return strconv.FormatInt(sec/u.n, 10) + u.s
		}
	}
	return strconv.FormatInt(sec, 10) + "s"
}

// Age checks how old something is; age < 0 means it never happened (FAIL).
func Age(name string, age, warn, fail int64, what, group string) Check {
	if what == "" {
		what = "updated"
	}
	if group == "" {
		group = Group
	}
	if age < 0 {
		return mk(name, "FAIL", "never "+what, group)
	}
	st := "OK"
	if age > fail {
		st = "FAIL"
	} else if age > warn {
		st = "WARN"
	}
	return mk(name, st, what+" "+Human(age)+" ago", group)
}

// Job is what the selfcheck knows of one worker job (worker.State without the import).
type Job struct {
	Name      string
	Enabled   bool
	Reason    string
	Every     time.Duration
	LastStart time.Time
	LastEnd   time.Time
	LastError string
	Running   bool
}

// JobRule checks one worker job. Stale is no finished run for 2 x the interval + 5 minutes. started is when the
// worker started (a job that has not run yet is not stale before that grace time has passed).
func JobRule(j Job, now, started int64) Check {
	if !j.Enabled {
		if j.Reason != "" {
			return mk(j.Name, "OK", j.Reason, "jobs")
		}
		return mk(j.Name, "OK", "waiting for its first run", "jobs")
	}
	period := int64(j.Every / time.Second)
	last := int64(0)
	if !j.LastEnd.IsZero() {
		last = j.LastEnd.Unix()
	}
	if last == 0 {
		if j.Running && !j.LastStart.IsZero() {
			if age := now - j.LastStart.Unix(); period > 0 && age > 2*period+300 {
				return mk(j.Name, "FAIL", "first run still going after "+Human(age), "jobs")
			}
			return mk(j.Name, "OK", "first run going", "jobs")
		}
		if started > 0 && period > 0 && now-started > 2*period+300 {
			return mk(j.Name, "FAIL", "never ran since the worker started "+Human(now-started)+" ago", "jobs")
		}
		return mk(j.Name, "WARN", "never ran", "jobs")
	}
	age := now - last
	if period > 0 && age > 2*period+300 {
		return mk(j.Name, "FAIL", "last run "+Human(age)+" ago, schedule every "+Human(period), "jobs")
	}
	if j.LastError != "" {
		return mk(j.Name, "WARN", "last run "+Human(age)+" ago failed ("+clip(j.LastError, 120)+")", "jobs")
	}
	d := "last run " + Human(age) + " ago"
	if period > 0 {
		d += ", every " + Human(period)
	}
	return mk(j.Name, "OK", d, "jobs")
}

// Disk checks the free space of the volume at path: FAIL under 5 % or 500 MiB, WARN under 15 % or 2 GiB.
func Disk(path string, free, total uint64) Check {
	pct := 0.0
	if total > 0 {
		pct = float64(free) / float64(total) * 100
	}
	st := "OK"
	if pct < 5 || free < 500<<20 {
		st = "FAIL"
	} else if pct < 15 || free < 2<<30 {
		st = "WARN"
	}
	return mk("disk "+path, st, fmt.Sprintf("%.1f GiB free (%s %%)", float64(free)/(1<<30), pyRound(pct)), Group)
}

// pyRound is Python's f"{x:.0f}" (round half to even).
func pyRound(x float64) string { return strconv.FormatFloat(math.RoundToEven(x), 'f', 0, 64) }

// Run is one recorded run (runs table).
type Run struct {
	Task, Status, Summary string
	Denials, IsError      int64
}

var blockedRe = regexp.MustCompile(`(?i)\b(permission|denied|not allowed|blocked|sandbox|cannot (run|access|execute)|unable to (run|access|execute)|no result|unreadable log|see (the )?log)\b`)

// Reason is a short reason when the run failed because of Argus itself (a model run error, no result, sandbox
// denials that stopped the run), else "".
func Reason(r Run) string {
	status := strings.ToUpper(r.Status)
	if r.IsError != 0 {
		return "Claude run error"
	}
	if status == "ERROR" {
		return "no result from Claude"
	}
	if r.Denials != 0 && (status == "FAIL" || status == "WARN") && blockedRe.MatchString(head(r.Summary, 600)) {
		s := "s"
		if r.Denials == 1 {
			s = ""
		}
		return fmt.Sprintf("%d sandbox denial%s", r.Denials, s)
	}
	return ""
}

// Internal checks own failures of the last 24 h: runs, and failed notices (task -> unix time of the failure).
func Internal(app string, runs []Run, notifyFail map[string]int64, now int64) Check {
	name := app + "-internal failures"
	type bad struct{ task, why string }
	var bads []bad
	for _, r := range runs {
		if w := Reason(r); w != "" {
			bads = append(bads, bad{r.Task, w})
		}
	}
	var nf []string
	for t, ts := range notifyFail {
		if now-ts < 86400 {
			nf = append(nf, t)
		}
	}
	sort.Strings(nf)
	if len(bads) == 0 && len(nf) == 0 {
		return mk(name, "OK", "none in 24 h", "internal")
	}
	var bits []string
	for i, b := range bads {
		if i == 4 {
			break
		}
		bits = append(bits, b.task+": "+b.why)
	}
	if len(nf) > 0 {
		bits = append(bits, "notify failed: "+strings.Join(nf, ", "))
	}
	return mk(name, "WARN", fmt.Sprintf("%d in 24 h: %s", len(bads)+len(nf), strings.Join(bits, "; ")), "internal")
}

// Credentials are the checks of the credential expiry file: its age (a daily job: WARN after 26 h, FAIL after
// 50 h) and one per credential; absent (not configured) credentials are left out.
func Credentials(doc map[string]any, age int64) []Check {
	if doc == nil || !truthy(doc["checked_at"]) {
		return []Check{mk("credential expiry", "WARN", "no credential expiry check yet (worker job cred-expiry)", "credentials")}
	}
	th, _ := doc["thresholds"].(map[string]any)
	warnD, critD := any(30), any(7)
	if th != nil {
		if v, ok := th["warn_days"]; ok {
			warnD = v
		}
		if v, ok := th["crit_days"]; ok {
			critD = v
		}
	}
	attn := any(0)
	if v, ok := doc["attention"]; ok {
		attn = v
	}
	c := Age("credential expiry", age, 26*3600, 50*3600, "checked", "credentials")
	c.Detail += fmt.Sprintf(", %s need attention (warn at %s d, incident at %s d)", pyStr(attn), pyStr(warnD), pyStr(critD))
	out := []Check{c}
	items, _ := doc["items"].([]any)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok || m["status"] == "absent" {
			continue
		}
		lvl, _ := m["level"].(string)
		name := "?"
		if v, ok := m["name"]; ok {
			name = pyStr(v)
		}
		out = append(out, mk(clip(name, 80), lvl, clip(pyStr(m["detail"]), 200), "credentials"))
	}
	return out
}

// CredStatus checks the credential status file (the credential check job, hourly): its age, and how many are not OK.
// It reads both shapes: {"items": [{"name", "ok"}]} and {"creds": {name: {"ok"}}}.
func CredStatus(doc map[string]any, age int64) Check {
	c := Age("credential status", age, 3*3600, 6*3600, "", "")
	var bad []string
	if items, ok := doc["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && !truthy(m["ok"]) && m["name"] != "Matrix notify" {
				bad = append(bad, pyStr(m["name"]))
			}
		}
	} else if creds, ok := doc["creds"].(map[string]any); ok {
		var names []string
		for k := range creds {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if m, ok := creds[k].(map[string]any); ok && !truthy(m["ok"]) {
				bad = append(bad, k)
			}
		}
	} else {
		return c
	}
	c.Detail += fmt.Sprintf(", %d not OK", len(bad))
	if len(bad) > 0 {
		if len(bad) > 3 {
			bad = bad[:3]
		}
		c.Detail += " (" + strings.Join(bad, ", ") + ")"
	}
	return c
}

// Backup checks the state backup status (backup-status.json): its age, encryption, off-host copy, restore test.
func Backup(b map[string]any, now int64) Check {
	age := int64(-1)
	if truthy(b["ts"]) {
		age = now - int64(num(b["ts"]))
	}
	c := Age("state backup", age, 26*3600, 50*3600, "made", "")
	if len(b) == 0 {
		return c
	}
	if truthy(b["encrypted"]) {
		c.Detail += ", encrypted"
	} else {
		c.Detail += ", NOT encrypted"
	}
	off := "not configured"
	if v, ok := b["offhost"]; ok {
		off = pyStr(v)
	}
	c.Detail += ", off-host " + off
	rt, _ := b["restore_test"].(map[string]any)
	if rt != nil && truthy(rt["ts"]) {
		res := "FAILED"
		if truthy(rt["ok"]) {
			res = "OK"
		}
		c.Detail += ", restore-test " + res + " " + Human(now-int64(num(rt["ts"]))) + " ago"
		if !truthy(rt["ok"]) {
			c.Status = worse(c.Status, "WARN")
		}
	} else {
		c.Detail += ", no restore-test yet"
	}
	okv, has := b["ok"]
	if (has && !truthy(okv)) || strings.HasPrefix(off, "failed") {
		c.Status = worse(c.Status, "WARN")
	}
	return c
}

// Overall is the worst state; OK when there is no check.
func Overall(checks []Check) string {
	st := "OK"
	for _, c := range checks {
		st = worse(st, c.Status)
	}
	return st
}

// PageDecision is (failing since, paged, action) with action "", "page" or "recovered": one notice once FAIL lasts
// after seconds, one when it ends.
func PageDecision(prevSince *int64, prevPaged bool, status string, now, after int64) (*int64, bool, string) {
	if status != "FAIL" {
		if prevPaged {
			return nil, false, "recovered"
		}
		return nil, false, ""
	}
	since := now
	if prevSince != nil && *prevSince != 0 {
		since = *prevSince
	}
	if !prevPaged && now-since >= after {
		return &since, true, "page"
	}
	return &since, prevPaged, ""
}

// --- the job --------------------------------------------------------------------------------------------------------

// Doc is selfcheck.json.
type Doc struct {
	TS           int64   `json:"ts"`
	Status       string  `json:"status"`
	Summary      string  `json:"summary"`
	Checks       []Check `json:"checks"`
	FailingSince *int64  `json:"failing_since"`
	Paged        bool    `json:"paged"`
	PageAfter    int64   `json:"page_after"`
	Kuma         string  `json:"kuma"`
}

// Options are the inputs of one self-check.
type Options struct {
	App          string       // the product name in notices ("Argus")
	StateDir     string       // the volume's state folder (disk check, notify-failed-*, backup-status.json)
	Out          string       // selfcheck.json
	DBs          []string     // databases to check (the first is ops.db, read for the runs)
	AlertsFile   string       // alerts-firing.json
	CredStatus   string       // cred-status.json
	CredExpiry   string       // credentials.json of the credential expiry job
	SourcesFile  string       // settings-out/sources-status.json
	ClaudeFile   string       // settings-out/claude.json (the Claude usage switch)
	AppURL       string       // the API's health address, "" = not checked
	PushURL      string       // the push monitor (https://<host>/api/push/<token>), "" = not configured
	PageAfter    int64        // seconds of FAIL before the notice (default 3600)
	Jobs         func() []Job // the worker's jobs
	Started      time.Time    // when the worker started
	Key          func() Check // the master key check; nil = not in the volume layout
	Notify       func(text string) error
	Client       *http.Client
	Now          func() time.Time
	IntegrityTTL time.Duration // how long an integrity result is kept (default 1 h)

	integrity   []Check
	integrityAt time.Time
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func mtimeAge(path string, now int64) int64 {
	if path == "" {
		return -1
	}
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return now - st.ModTime().Unix()
}

func readMap(path string) map[string]any {
	var m map[string]any
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// Collect runs every check.
func (o *Options) Collect(ctx context.Context) []Check {
	now := o.now().Unix()
	var checks []Check
	enabled := map[string]bool{}
	if o.Jobs != nil {
		jobs := o.Jobs()
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
		started := int64(0)
		if !o.Started.IsZero() {
			started = o.Started.Unix()
		}
		for _, j := range jobs {
			if j.Name == "selfcheck" {
				continue
			}
			enabled[j.Name] = j.Enabled
			checks = append(checks, JobRule(j, now, started))
		}
	}
	if o.AppURL != "" {
		ok, why := o.httpOK(ctx, o.AppURL)
		if ok {
			checks = append(checks, mk("api /healthz", "OK", "answering", Group))
		} else {
			checks = append(checks, mk("api /healthz", "FAIL", why, Group))
		}
	}
	if enabled["collect-alerts"] {
		checks = append(checks, Age("alerts snapshot", mtimeAge(o.AlertsFile, now), 300, 900, "", ""))
	}
	if age := mtimeAge(o.CredStatus, now); age >= 0 || enabled["credcheck"] {
		checks = append(checks, CredStatus(readMap(o.CredStatus), age))
	}
	if age := mtimeAge(o.CredExpiry, now); age >= 0 || enabled["cred-expiry"] {
		checks = append(checks, Credentials(readMap(o.CredExpiry), age)...)
	}
	if o.StateDir != "" {
		var fs syscall.Statfs_t
		if err := syscall.Statfs(o.StateDir, &fs); err == nil {
			checks = append(checks, Disk(o.StateDir, fs.Bavail*uint64(fs.Bsize), fs.Blocks*uint64(fs.Bsize)))
		} else {
			checks = append(checks, mk("disk "+o.StateDir, "FAIL", "cannot read: "+clip(err.Error(), 120), Group))
		}
	}
	checks = append(checks, o.databases(ctx)...)
	if o.Key != nil {
		checks = append(checks, o.Key())
	}
	if c, ok := Sources(readMap(o.SourcesFile)); ok {
		checks = append(checks, c)
	}
	if b := filepath.Join(o.StateDir, "backup-status.json"); o.StateDir != "" && mtimeAge(b, now) >= 0 {
		checks = append(checks, Backup(readMap(b), now))
	}
	checks = append(checks, Internal(o.app(), o.recentRuns(ctx, now), o.notifyFailed(), now))
	if o.ClaudeFile != "" {
		checks = append(checks, claude(o.ClaudeFile))
	}
	return checks
}

func (o *Options) app() string {
	if o.App == "" {
		return "Argus"
	}
	return o.App
}

// Sources checks the Settings -> Data sources entries: WARN when an entry's last check failed; ok false when no
// entry is saved.
func Sources(doc map[string]any) (Check, bool) {
	srcs, _ := doc["sources"].(map[string]any)
	var keys []string
	for k, v := range srcs {
		if _, ok := v.(map[string]any); ok && strings.Contains(k, "/") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return Check{}, false
	}
	sort.Strings(keys)
	var bad, unchecked []string
	for _, k := range keys {
		e := srcs[k].(map[string]any)
		switch st, _ := e["status"].(string); st {
		case "failed", "unreachable", "refused":
			bad = append(bad, k+" "+st)
		case "", "unchecked":
			unchecked = append(unchecked, k)
		}
	}
	d := fmt.Sprintf("%d of %d OK", len(keys)-len(bad)-len(unchecked), len(keys))
	if len(unchecked) > 0 {
		d += fmt.Sprintf(", %d not checked yet", len(unchecked))
	}
	if len(bad) > 0 {
		if len(bad) > 3 {
			bad = append(bad[:3], "...")
		}
		return mk("data sources", "WARN", d+": "+strings.Join(bad, ", "), Group), true
	}
	return mk("data sources", "OK", d, Group), true
}

func claude(path string) Check {
	on := true
	if _, err := os.Stat(path); err == nil {
		b, ok := readMap(path)["enabled"].(bool)
		on = ok && b
	}
	if on {
		return mk("claude usage", "OK", "on", Group)
	}
	return mk("claude usage", "OK", "Claude paused: Settings > Claude usage is off, no model runs", Group)
}

func (o *Options) httpOK(ctx context.Context, u string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, "bad address"
	}
	resp, err := o.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return false, "timeout"
		}
		return false, "not reachable"
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return true, "HTTP 200"
}

// databases is a quick integrity check of each database, once per IntegrityTTL (the last result in between).
func (o *Options) databases(ctx context.Context) []Check {
	ttl := o.IntegrityTTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	if o.integrity != nil && o.now().Sub(o.integrityAt) < ttl {
		return o.integrity
	}
	var out []Check
	for _, p := range o.DBs {
		if p == "" {
			continue
		}
		name := "database " + filepath.Base(p)
		if _, err := os.Stat(p); err != nil {
			continue // not created yet (a page that was never used)
		}
		res, err := quickCheck(ctx, p)
		switch {
		case err != nil:
			out = append(out, mk(name, "FAIL", "cannot open: "+clip(err.Error(), 120), Group))
		case res == "ok":
			out = append(out, mk(name, "OK", "integrity ok", Group))
		default:
			out = append(out, mk(name, "FAIL", "integrity: "+clip(res, 160), Group))
		}
	}
	o.integrity, o.integrityAt = out, o.now()
	return out
}

func quickCheck(ctx context.Context, path string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&res); err != nil {
		return "", err
	}
	return res, nil
}

func (o *Options) recentRuns(ctx context.Context, now int64) []Run {
	if len(o.DBs) == 0 || o.DBs[0] == "" {
		return nil
	}
	if _, err := os.Stat(o.DBs[0]); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+o.DBs[0]+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT task, COALESCE(status,''), COALESCE(summary,''), COALESCE(denials,0), COALESCE(is_error,0) FROM runs WHERE started_at > ?`, now-86400)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if rows.Scan(&r.Task, &r.Status, &r.Summary, &r.Denials, &r.IsError) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (o *Options) notifyFailed() map[string]int64 {
	out := map[string]int64{}
	if o.StateDir == "" {
		return out
	}
	m, _ := filepath.Glob(filepath.Join(o.StateDir, "notify-failed-*"))
	for _, p := range m {
		ts := int64(0)
		if st, err := os.Stat(p); err == nil {
			ts = st.ModTime().Unix()
		}
		out[strings.TrimPrefix(filepath.Base(p), "notify-failed-")] = ts
	}
	return out
}

var pushRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{2,5})?/api/push/[A-Za-z0-9]{6,64}$`)
var pushMsgRe = regexp.MustCompile(`[^A-Za-z0-9 :/._-]`)

// Push pings the push monitor: "not configured", "skipped (FAIL)", "pushed" or "failed". The URL holds a token: it
// is never logged or returned.
func (o *Options) Push(ctx context.Context, status, summary string) string {
	if o.PushURL == "" {
		return "not configured"
	}
	if status == "FAIL" {
		return "skipped (FAIL)"
	}
	u, _, _ := strings.Cut(strings.TrimSpace(o.PushURL), "?")
	if !pushRe.MatchString(u) {
		return "failed (not an https://<host>/api/push/<token> address)"
	}
	msg := head(pushMsgRe.ReplaceAllString(summary, ""), 120)
	q := url.Values{"status": {"up"}, "msg": {msg}, "ping": {""}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return "failed"
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return "failed"
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return "failed (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
	}
	return "pushed"
}

// Run is one self-check: collect, decide the notice, ping the push monitor, write the file.
func (o *Options) Run(ctx context.Context) (Doc, error) {
	now := o.now().Unix()
	after := o.PageAfter
	if after <= 0 {
		after = 3600
	}
	var prev struct {
		FailingSince *int64 `json:"failing_since"`
		Paged        bool   `json:"paged"`
	}
	if b, err := os.ReadFile(o.Out); err == nil {
		json.Unmarshal(b, &prev)
	}
	checks := o.Collect(ctx)
	status := Overall(checks)
	since, paged, action := PageDecision(prev.FailingSince, prev.Paged, status, now, after)
	app := o.app()
	var text string
	switch action {
	case "page":
		var parts []string
		for _, c := range checks {
			if c.Status == "FAIL" && len(parts) < 4 {
				parts = append(parts, c.Name+" "+c.Detail)
			}
		}
		text = fmt.Sprintf("FAIL: %s-internal: %s has been failing for %s: %s", app, app, Human(now-*since), strings.Join(parts, "; "))
	case "recovered":
		from := now
		if prev.FailingSince != nil {
			from = *prev.FailingSince
		}
		text = fmt.Sprintf("OK: %s-internal: %s recovered after %s.", app, app, Human(now-from))
	}
	if text != "" && o.Notify != nil {
		if err := o.Notify(text); err != nil {
			paged = prev.Paged // not sent: try again at the next run
			if action == "recovered" {
				paged = true
			}
		}
	}
	ok := 0
	for _, c := range checks {
		if c.Status == "OK" {
			ok++
		}
	}
	if checks == nil {
		checks = []Check{}
	}
	doc := Doc{TS: now, Status: status, Summary: fmt.Sprintf("%s: %d/%d OK", status, ok, len(checks)), Checks: checks,
		FailingSince: since, Paged: paged, PageAfter: after}
	doc.Kuma = o.Push(ctx, status, doc.Summary)
	return doc, writeJSON(o.Out, doc)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	os.Chmod(tmp, 0o644)
	return os.Rename(tmp, path)
}

// --- helpers --------------------------------------------------------------------------------------------------------

func head(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func clip(s string, n int) string { return head(strings.TrimSpace(s), n) }

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// pyStr is Python's str() of a JSON value.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
