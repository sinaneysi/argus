package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/oidc"
	"github.com/sinaneysi/argus/internal/py"
)

// The pages server.py renders itself (the rest live in the views_* ports).

var TASKS = [][3]string{{"backup-check", "Backups", "/backups"}, {"cert-check", "Certificates", "/certs"},
	{"log-rca", "Log RCA", "/incidents"}, {"chat-triage", "Chat triage", "/incidents"}}

var INC_KIND = map[string]string{"open": "fail", "acknowledged": "warn", "resolved": "ok"}

func getD(m map[string]string, k, d string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}

// staleBanner names the checks with no run in STALE_H hours.
func staleBanner(stale []string) string {
	if len(stale) == 0 {
		return ""
	}
	return `<div class="banner fail">` + Icon("alert") + `<span>No run in the last ` + Itoa(STALE_H) + ` h: ` + E(strings.Join(stale, ", ")) + `. ` +
		`Check the systemd timers on ` + ARGUS_HOSTNAME + `.</span></div>`
}

// failingBackups are the latest backup sets that fail and were not accepted by an admin.
func failingBackups() []Row {
	var fails []Row
	latest := LatestBackupRows()
	acc := BackupAccepted(latest)
	for _, x := range latest {
		if _, a := acc[x.Str("name")]; x.Str("status") != "OK" && !a {
			fails = append(fails, x)
		}
	}
	return fails
}

// backupAllAccepted: the latest backup-check has failing sets and an admin accepted every one of them.
func backupAllAccepted() bool {
	rows := LatestBackupRows()
	n := 0
	for _, x := range rows {
		if x.Str("status") != "OK" {
			n++
		}
	}
	return n > 0 && len(BackupAccepted(rows)) == n
}

// backupAgeCell: the newest backup's age; a set with no backup at all says so instead of "None h".
func backupAgeCell(x Row) string {
	if x.IsNull("age_h") {
		if strings.HasPrefix(x.Str("status"), "MISSING") || x.IsNull("newest") {
			return `<span class="dim">no backup found</span>`
		}
		return `<span class="dim">-</span>`
	}
	return E(x.Str("age_h")) + ` h`
}

func latestRowOf(name string) Row {
	r := Q(`SELECT b.* FROM backup_results b WHERE b.name=? AND b.run_id = (SELECT id FROM runs WHERE task='backup-check'
	        ORDER BY started_at DESC LIMIT 1)`, name)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

// RcaFiles is rca_files(): the merged report names, newest first.
func RcaFiles() []string {
	ents, err := os.ReadDir(REPORTS)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

var sevKind = map[string]string{"critical": "fail", "high": "fail", "medium": "warn", "low": "ok"}

// runsSorts: newest first is the default (an operator opens this page to see what just ran).
var runsSorts = [][3]string{{"newest", "Newest", "started most recently first"}, {"oldest", "Oldest", "started longest ago first"},
	{"cost", "Cost", "most expensive first"}, {"duration", "Duration", "longest first"}}

var runsOrder = map[string]string{"newest": "started_at DESC, id DESC", "oldest": "started_at ASC, id ASC",
	"cost": "coalesce(cost_usd,0) DESC, started_at DESC", "duration": "coalesce(duration_ms,0) DESC, started_at DESC"}

// runsNotOK is the status filter for every run that did not end OK; the top bar's status pill links to it.
const runsNotOK = "notok"

var runsValRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// PageRuns is page_runs as a queue page (DESIGN.md "Queue page", in-memory lists): search over task, status and
// summary, facets Task and Status with counts that honour the other filters, four sorts, 25 runs a page; every
// filter is a SQL condition on the runs table, so the pages cover all runs, not only the latest 200.
// runsPage is what /runs shows: the page of runs under the filters, the last day's count, the 30-day cost by day.
type runsPage struct {
	l              listQ
	matched, total int
	runs           []Row
	nDay, badDay   int
	pts            []Pt
	totalCost      float64
	facet          func(col, skip string) ([][2]string, map[string]int)
}

func runsData(qs py.QS) runsPage {
	l := newListQ("/runs", qs, []string{"q", "task", "status", "sort"}, map[string]string{"sort": "newest"},
		func(k, v string) bool {
			if k == "sort" {
				_, ok := runsOrder[v]
				return ok
			}
			return runsValRe.MatchString(v)
		})
	l.anchor = "runs"
	// where: the conditions of every filter except skip ("" = all of them)
	where := func(skip string) (string, []any) {
		conds, args := queueLike(l.words(), []string{"task", "status", "summary", "CAST(id AS TEXT)"})
		if skip != "task" && l.get("task") != "" {
			conds, args = append(conds, "task=?"), append(args, l.get("task"))
		}
		if st := l.get("status"); skip != "status" && st == runsNotOK {
			conds = append(conds, "coalesce(status,'') NOT IN ('', 'OK') AND lower(status) NOT LIKE 'skip%'")
		} else if skip != "status" && st != "" {
			conds, args = append(conds, "coalesce(status,'')=?"), append(args, st)
		}
		if len(conds) == 0 {
			return "", nil
		}
		return " WHERE " + strings.Join(conds, " AND "), args
	}
	facet := func(col, skip string) ([][2]string, map[string]int) {
		w, args := where(skip)
		var vals [][2]string
		n := map[string]int{}
		for _, r := range Q("SELECT coalesce("+col+",'') v, count(*) n FROM runs"+w+" GROUP BY v ORDER BY n DESC, v LIMIT 60", args...) {
			v := r.Str("v")
			if v == "" {
				continue
			}
			vals, n[v] = append(vals, [2]string{v, v}), int(r.I("n"))
		}
		return vals, n
	}
	count := func(w string, args []any) int {
		if r := Q("SELECT count(*) n FROM runs"+w, args...); len(r) > 0 {
			return int(r[0].I("n"))
		}
		return 0
	}
	w, args := where("")
	matched, total := count(w, args), count("", nil)
	from, _ := listPage(&l, matched)
	runs := Q("SELECT * FROM runs"+w+" ORDER BY "+runsOrder[l.get("sort")]+" LIMIT ? OFFSET ?", append(args, l.per(), from)...)
	now := Now()
	nDay, badDay := 0, 0
	if r := Q("SELECT count(*) n, sum(CASE WHEN coalesce(status,'')<>'OK' THEN 1 ELSE 0 END) bad FROM runs WHERE started_at > ?", now-86400); len(r) > 0 {
		nDay, badDay = int(r[0].I("n")), int(r[0].I("bad"))
	}
	days := Q(`SELECT date(started_at,'unixepoch','localtime') d, SUM(cost_usd) c FROM runs
                WHERE started_at > ? GROUP BY d ORDER BY d`, now-30*86400)
	pts := make([]Pt, 0, len(days))
	var totalCost float64
	for _, x := range days {
		t, err := time.ParseInLocation("2006-01-02", x.Str("d"), time.Local)
		if err != nil {
			panic(err)
		}
		v := py.Num(py.Or(x.Get("c"), int64(0)))
		totalCost += v
		pts = append(pts, Pt{T: float64(t.Unix()), V: v})
	}
	return runsPage{l, matched, total, runs, nDay, badDay, pts, totalCost, facet}
}

// --- alerts ----------------------------------------------------------------------------------------------

var SEV_RANK = map[string]int{"critical": 0, "page": 0, "error": 1, "warning": 2, "info": 3, "none": 4}

func sevRank(v any) int {
	if r, ok := SEV_RANK[strings.ToLower(py.Str(v))]; ok {
		return r
	}
	return 4
}

// AlertsSnapshot is alerts_snapshot(): the snapshot and one row per alert with its dc.
func AlertsSnapshot() (*py.OMap, []*py.OMap) {
	v := ReadJSON(ALERTS, nil)
	snap, _ := v.(*py.OMap)
	if snap == nil || snap.Len() == 0 {
		snap = py.NewOMap()
	}
	var rows []*py.OMap
	for _, dv := range py.AsList(snap.GetD("dcs", []any{})) {
		d := py.AsMap(dv)
		for _, av := range py.AsList(d.GetD("alerts", []any{})) {
			a := py.AsMap(av)
			if a == nil {
				continue
			}
			r := a.Copy()
			r.Set("dc", d.GetD("dc", "?"))
			rows = append(rows, r)
		}
	}
	return snap, rows
}

// IsoTS is iso_ts(s): an ISO UTC time ("...T..:..:..") as epoch seconds, ok=false for None.
func IsoTS(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", py.Head(s, 19), time.UTC)
	if err != nil {
		return 0, false
	}
	return float64(t.Unix()), true
}

// Num is num(v): a metric value as text.
func Num(v any) string {
	f, ok := py.Float(v)
	if !ok || v == nil {
		return py.OrS(v, "")
	}
	if f < 1e6 && f > -1e6 {
		return py.G(f, 4)
	}
	return py.Exp(f, 3)
}

// alerts queue: state ("firing" by default), severity and datacenter facets, a search over name, instance, summary and
// DC, three sorts, server pagination (DESIGN.md "Queue page"). Rows come from the one-minute snapshot, in memory.
var alertsStateFacets = [][2]string{{"firing", "Firing"}, {"pending", "Pending"}, {"all", "All"}}
var alertsSevFacets = [][2]string{{"critical", "Critical"}, {"page", "Page"}, {"error", "Error"}, {"warning", "Warning"},
	{"info", "Info or unrated"}}
var alertsSorts = [][3]string{{"severity", "Severity", "Worst severity first, then the longest active"},
	{"newest", "Newest", "Most recently started first"}, {"oldest", "Longest active", "Longest active first"}}

// alertsSevKey: the stored severity, lower case; no severity or "none" counts as info (the Info figure counts both).
func alertsSevKey(a *py.OMap) string {
	l := strings.ToLower(py.OrS(a.Get("severity"), "none"))
	if l == "none" {
		return "info"
	}
	return l
}

func alertsQuery(qs py.QS) listQ {
	return newListQ("/alerts", qs, []string{"q", "state", "sev", "dc", "sort"}, map[string]string{"state": "firing", "sort": "severity"},
		func(k, v string) bool {
			switch k {
			case "state":
				return In(v, "firing", "pending", "all")
			case "sev", "dc":
				return len(v) <= 20 && proposalLabelRe.MatchString(v)
			case "sort":
				return In(v, "severity", "newest", "oldest")
			}
			return false
		})
}

// alertsList is the Alerts queue for one query (the page and GET /api/v1/alerts share it).
type alertsList struct {
	shown          []*py.OMap
	stN, sevN, dcN map[string]int
	sevs, dcs      [][2]string
}

func alertsCompute(l listQ, rows []*py.OMap) alertsList {
	words := l.words()
	match := func(a *py.OMap, skip string) bool {
		if skip != "state" && l.get("state") != "all" && a.Get("state") != l.get("state") {
			return false
		}
		if skip != "sev" && l.get("sev") != "" && alertsSevKey(a) != l.get("sev") {
			return false
		}
		if skip != "dc" && l.get("dc") != "" && py.Str(a.Get("dc")) != l.get("dc") {
			return false
		}
		return listMatch(words, py.Str(a.GetD("alertname", "")), py.Str(a.GetD("instance", "")), py.Str(a.GetD("summary", "")), py.Str(a.Get("dc")))
	}
	stN, sevN, dcN := map[string]int{}, map[string]int{}, map[string]int{}
	var sevs, dcs [][2]string
	for _, f := range alertsSevFacets {
		sevs = append(sevs, f)
	}
	var shown []*py.OMap
	for _, a := range rows {
		if match(a, "state") {
			stN[py.Str(a.Get("state"))]++
			stN["all"]++
		}
		if match(a, "sev") {
			k := alertsSevKey(a)
			if _, known := SEV_RANK[k]; !known && sevN[k] == 0 {
				sevs = append(sevs, [2]string{k, k})
			}
			sevN[k]++
		}
		if match(a, "dc") {
			d := py.Str(a.Get("dc"))
			if dcN[d] == 0 {
				dcs = append(dcs, [2]string{d, d})
			}
			dcN[d]++
		}
		if match(a, "") {
			shown = append(shown, a)
		}
	}
	sort.SliceStable(dcs, func(i, j int) bool { return dcs[i][0] < dcs[j][0] })
	sort.SliceStable(shown, func(i, j int) bool {
		ai, aj := py.OrS(shown[i].Get("activeAt"), ""), py.OrS(shown[j].Get("activeAt"), "")
		switch l.get("sort") {
		case "newest":
			return ai > aj
		case "oldest":
			return ai < aj
		}
		ri, rj := sevRank(shown[i].Get("severity")), sevRank(shown[j].Get("severity"))
		if ri != rj {
			return ri < rj
		}
		return ai < aj
	})
	return alertsList{shown, stN, sevN, dcN, sevs, dcs}
}

func alertsFindings() []Row {
	return Q("SELECT run_id, idx, title, sources FROM triage_problems ORDER BY run_id DESC LIMIT 200")
}

// alertsFindingHref is the page of the finding that names this alert ("" when none).
func alertsFindingHref(a *py.OMap, probs []Row) string {
	for _, p := range probs {
		src := p.S("sources") + p.S("title")
		an := a.Get("alertname")
		if py.Truthy(an) && strings.Contains(src, py.Str(an)) &&
			(strings.Contains(src, py.Str(a.GetD("instance", "-"))) || strings.Contains(src, strings.ToUpper(py.Str(a.Get("dc"))))) {
			return "/incidents/problem?run=" + p.Str("run_id") + "&idx=" + p.Str("idx")
		}
	}
	return ""
}

// --- admin: General --------------------------------------------------------------------------------------

var SCHEDULES = func() []string {
	s := []string{"10m", "15m", "30m", "1h", "2h", "4h", "6h", "12h"}
	for h := 0; h < 24; h++ {
		for _, m := range []int{0, 30} {
			s = append(s, fmt.Sprintf("daily@%02d:%02d", h, m))
		}
	}
	return s
}()

var ADMIN_TASKS = [][2]string{{"chat-triage", "Chat triage (Matrix rooms)"}, {"log-rca", "Log RCA (alerts + Loki)"},
	{"backup-check", "Backup check (MinIO)"}, {"cert-check", "Certificate check"}}

func defaults() *py.OMap {
	tasks := py.NewOMap(
		"chat-triage", py.NewOMap("schedule", "30m", "enabled", false),
		"log-rca", py.NewOMap("schedule", "4h", "enabled", false),
		"backup-check", py.NewOMap("schedule", "daily@07:00", "enabled", false),
		"cert-check", py.NewOMap("schedule", "daily@07:30", "enabled", false))
	th := py.NewOMap("kuma_min_down_minutes", int64(5), "kuma_flap_count", int64(3), "cert_warn_days", int64(30), "cert_fail_days", int64(14))
	return py.NewOMap("tasks", tasks, "thresholds", th)
}

type limit struct {
	k      string
	lo, hi int64
	desc   string
}

var LIMITS = []limit{{"kuma_min_down_minutes", 1, 60, "Kuma: minutes down before it counts"},
	{"kuma_flap_count", 2, 20, "Kuma: downs in one batch that count as flapping"},
	{"cert_warn_days", 7, 60, "Certificates: WARN below this many days"},
	{"cert_fail_days", 1, 30, "Certificates: FAIL below this many days"}}

type setting struct {
	k      string
	def    any // bool, int64 or string
	lo, hi int64
	label  string
}

var RCA_AUTO = []setting{
	{"enabled", true, 0, 0, "Automatic deep RCA"},
	{"trigger_page", true, 0, 0, "New page-level incident (production, customer-facing, critical)"},
	{"trigger_open", true, 0, 0, "Incident still open after N hours"},
	{"trigger_repeat", true, 0, 0, "Incident seen N times in 24 h"},
	{"trigger_qa", true, 0, 0, MATRIX_QA_ROOM_LABEL + " report about production"},
	{"open_hours", int64(2), 1, 72, "Open hours (trigger 2)"},
	{"repeat_count", int64(3), 2, 20, "Repeat count (trigger 3)"},
	{"daily_runs", int64(6), 0, 6, "Automatic runs per day"},
	{"daily_usd", int64(20), 1, 50, "Deep RCA cost per day, $"},
	{"quiet_start", int64(23), 0, 23, "Quiet hours start (" + ARGUS_TZ_LABEL + "; only trigger 1)"},
	{"quiet_end", int64(8), 0, 23, "Quiet hours end (" + ARGUS_TZ_LABEL + ")"},
}

var QA_ASK = []setting{{"enabled", true, 0, 0, "Ask QA follow-up questions in " + MATRIX_QA_ROOM_LABEL},
	{"dry_run", false, 0, 0, "Dry run: show the question on Conversations, do not send it"},
	{"read_images", false, 0, 0, "Read images in QA messages (screenshots go to the triage model, deleted after the run)"}}

var READINESS = []setting{
	{"hub_raise", true, 0, 0, "Hub services raise incident severity (tier 1: one step, never above critical)"},
	{"event_name", "Black Friday", 0, 0, "Sales event name"},
	{"event_date", "2026-11-27", 0, 0, "Sales event date (YYYY-MM-DD)"},
	{"freeze_days", int64(7), 0, 30, "Deploy freeze reminder from D-N"},
	{"before_days", int64(30), 1, 90, "Readiness brief from D-N"},
	{"after_days", int64(2), 0, 14, "Readiness brief until D+N"},
}

// merged is dict(DEFAULTS, **{k: v for k, v in (cfg.get(section) or {}).items() if k in DEFAULTS}).
func merged(defs []setting, saved any) *py.OMap {
	out := py.NewOMap()
	for _, s := range defs {
		out.Set(s.k, s.def)
	}
	if m := py.AsMap(saved); m != nil {
		for _, k := range m.Keys() {
			if out.Has(k) {
				out.Set(k, m.Get(k))
			}
		}
	}
	return out
}

func settingsPath() string { return filepath.Join(SET_IN, "settings.json") }

// QAAskSettings is qa_ask_settings(cfg): the saved values, and over them the live copy root writes
// (settings-out/qa-ask.json, also changed by `argus settings qa ...` on the Argus host).
func QAAskSettings(cfg any) *py.OMap {
	if cfg == nil { // qa_ask_settings() with no argument reads settings.json
		cfg = ReadJSON(settingsPath(), py.NewOMap())
	}
	var raw *py.OMap
	if m := py.AsMap(cfg); m != nil {
		raw = py.AsMap(m.Get("qa_ask"))
	}
	live := ReadJSONMap(filepath.Join(SET_OUT, "qa-ask.json"))
	out := py.NewOMap()
	for _, s := range QA_ASK {
		if b, ok := live.Get(s.k).(bool); ok {
			out.Set(s.k, b)
		} else if b, ok := raw.Get(s.k).(bool); ok {
			out.Set(s.k, b)
		} else {
			out.Set(s.k, s.def)
		}
	}
	return out
}

// SSOOnly is sso_only(cfg); cfg nil reads settings.json.
func SSOOnly(cfg any) bool {
	if cfg == nil {
		cfg = ReadJSON(settingsPath(), py.NewOMap())
	}
	m := py.AsMap(cfg)
	if m == nil {
		return false
	}
	a := py.AsMap(m.Get("auth"))
	b, ok := a.Get("sso_only").(bool)
	return ok && b
}

// SSOEnforced is sso_enforced().
func SSOEnforced() bool { return oidc.Configured() && SSOOnly(nil) }

func readLinesPy(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(strings.ReplaceAll(py.ToValidUTF8(data), "\r\n", "\n"), "\r", "\n")
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out, nil
}

var (
	reEventDate = regexp.MustCompile(`^20\d\d-\d\d-\d\d$`)
	reEventName = regexp.MustCompile(`^[\p{L}\p{Nd}\p{Nl}\p{No}_ .'-]{1,40}$`)
)

func inRange(v string, lo, hi int64) (int64, bool) {
	if !py.IsDigit(v) {
		return 0, false
	}
	n, ok := py.ParseInt(v)
	return n, ok && lo <= n && n <= hi
}

// WriteFileAtomic writes data to dir/.name.tmp and renames it to dir/name (mode 0666 & ~umask, like Python's open()).
func WriteFileAtomic(path, tmp string, data []byte) error {
	if err := os.WriteFile(tmp, data, 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AdminSaveField is AdminSave plus the name of the field a validation message is about ("" when saved).
func AdminSaveField(form py.QS, sess *Session) (string, string, error) {
	cfg := py.NewOMap("tasks", py.NewOMap(), "thresholds", py.NewOMap(), "updated_by", sess.Username, "updated_at", NowInt())
	for _, at := range ADMIN_TASKS {
		task := at[0]
		sched := form.Get("sched:" + task)
		if !In(sched, SCHEDULES...) {
			return "Invalid schedule for " + task + ".", "sched:" + task, nil
		}
		cfg.Map("tasks").Set(task, py.NewOMap("schedule", sched, "enabled", form.Get("on:"+task) == "1"))
	}
	for _, l := range LIMITS {
		n, ok := inRange(form.Get("th:"+l.k), l.lo, l.hi)
		if !ok {
			return l.desc + " must be between " + I2S(l.lo) + " and " + I2S(l.hi) + ".", "th:" + l.k, nil
		}
		cfg.Map("thresholds").Set(l.k, n)
	}
	if py.I(cfg.Map("thresholds").Get("cert_fail_days")) >= py.I(cfg.Map("thresholds").Get("cert_warn_days")) {
		return "Certificate FAIL days must be below WARN days.", "th:cert_fail_days", nil
	}
	ra := py.NewOMap()
	cfg.Set("rca_auto", ra)
	for _, s := range RCA_AUTO {
		v := form.Get("ra:" + s.k)
		if _, isBool := s.def.(bool); isBool {
			ra.Set(s.k, v == "1")
			continue
		}
		n, ok := inRange(v, s.lo, s.hi)
		if !ok {
			return s.label + " must be between " + I2S(s.lo) + " and " + I2S(s.hi) + ".", "ra:" + s.k, nil
		}
		ra.Set(s.k, n)
	}
	rd := py.NewOMap()
	cfg.Set("readiness", rd)
	if form.Get("rd:present") != "1" { // a form from before this section: keep what is saved
		old := py.AsMap(ReadJSON(settingsPath(), py.NewOMap()))
		rd = merged(READINESS, old.Get("readiness"))
		cfg.Set("readiness", rd)
	} else {
		for _, s := range READINESS {
			v := py.Strip(form.Get("rd:" + s.k))
			switch s.def.(type) {
			case bool:
				rd.Set(s.k, v == "1")
			case int64:
				n, ok := inRange(v, s.lo, s.hi)
				if !ok {
					return s.label + " must be between " + I2S(s.lo) + " and " + I2S(s.hi) + ".", "rd:" + s.k, nil
				}
				rd.Set(s.k, n)
			default:
				if s.k == "event_date" {
					if _, err := time.Parse("2006-01-02", v); err != nil || !reEventDate.MatchString(v) {
						return "Sales event date must be YYYY-MM-DD.", "rd:event_date", nil
					}
					rd.Set(s.k, v)
				} else {
					if !reEventName.MatchString(v) {
						return "Sales event name: 1-40 letters, digits, spaces, dots, dashes.", "rd:event_name", nil
					}
					rd.Set(s.k, v)
				}
			}
		}
	}
	if form.Get("qa:present") == "1" {
		q := py.NewOMap()
		for _, s := range QA_ASK {
			q.Set(s.k, form.Get("qa:"+s.k) == "1")
		}
		cfg.Set("qa_ask", q)
	} else {
		cfg.Set("qa_ask", QAAskSettings(py.Or(ReadJSON(settingsPath(), py.NewOMap()), py.NewOMap())))
	}
	cfg.Set("claude", py.NewOMap("enabled", ClaudeOn(nil))) // its own card (POST /admin/claude): keep what is live
	ssoNew := form.Get("auth:sso_only") == "1"
	cfg.Set("auth", py.NewOMap("sso_only", ssoNew))
	was := SSOOnly(nil)
	if err := WriteFileAtomic(settingsPath(), filepath.Join(SET_IN, ".settings.json.tmp"), []byte(py.Dumps(cfg, 1))); err != nil {
		return "", "", err
	}
	if was != ssoNew {
		fmt.Printf("settings: sso_only %s by %s\n", If(ssoNew, "on", "off"), sess.Username)
	}
	return "Saved. Applying now; reload in a few seconds to see the result.", "", nil
}

// ScanRequest is scan_request(form, sess): queue the scan requests for root; the number queued.
func ScanRequest(form py.QS, sess *Session) (int, error) {
	raw := form.Get("tasks")
	var tasks []string
	if raw == "all" {
		tasks = SCAN_TASKS["/operations"]
	} else {
		for _, t := range strings.Split(raw, ",") {
			if _, ok := SCAN_LABEL[t]; ok && (t != credTask || sess.Role == "admin") { // Credentials: admins only
				tasks = append(tasks, t)
			}
		}
	}
	if len(tasks) == 0 {
		return 0, nil
	}
	d := filepath.Join(SET_IN, "requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		return 0, err
	}
	for _, t := range tasks {
		body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
		if err := WriteFileAtomic(filepath.Join(d, t+".req"), filepath.Join(d, "."+t+".tmp"), []byte(body)); err != nil {
			return 0, err
		}
	}
	return len(tasks), nil
}

// --- login, account, users -----------------------------------------------------------------------------

// USERNAME is the account name rule.
var USERNAME = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)

// UsersPostIP is UsersPost with the client address for the audit log.
func UsersPostIP(form py.QS, sess *Session, ip string) (string, string, error) {
	action, user := form.Get("action"), form.Get("user")
	if action == "mfa-login-on" || action == "mfa-login-off" {
		v := If(action == "mfa-login-on", "1", "0")
		if err := auth.SetSetting("mfa_required_for_login", v); err != nil {
			return "", "", err
		}
		secEvent(sess, "policy.mfa-login", "", "required at sign-in: "+If(v == "1", "on", "off"), ip, "")
		return If(v == "1", "2FA is now required at sign-in for every user.", "2FA is no longer required at sign-in."), "ok", nil
	}
	exists := func() bool {
		for _, u := range auth.Users() {
			if u.Username == user {
				return true
			}
		}
		return false
	}
	if action == "add" {
		role := form.GetD("role", "viewer")
		if !USERNAME.MatchString(user) || !In(role, "viewer", "admin") {
			return "Invalid username or role.", "fail", nil
		}
		if exists() {
			return user + " already exists.", "fail", nil
		}
		pw, err := auth.AddUser(user, role)
		if err != nil {
			return "", "", err
		}
		return "Added " + user + " (" + role + "). Temporary password, shown only now: " + pw, "ok", nil
	}
	if user == sess.Username || !exists() {
		return "Not allowed.", "fail", nil
	}
	if In(action, "reset", "role-admin", "role-viewer") && auth.Source(user) == "keycloak" { // 2FA reset and sign-out work for both
		return user + " signs in with Keycloak: its password and role are managed there.", "fail", nil
	}
	switch action {
	case "reset-2fa":
		reason := strings.Join(strings.Fields(form.Get("reason")), " ")
		n, err := auth.AdminReset(sess.Username, user, reason, ip, sess.ID(), methodOf(sess))
		if err != nil {
			return "2FA of " + user + " not reset: " + err.Error() + ".", "fail", nil
		}
		return "2FA of " + user + " removed and " + I2S(n) + " session" + py.Plural(int(n)) + " ended. At their next sign-in they must set up a new app before they can open anything else.", "ok", nil
	case "revoke-sessions":
		n := auth.RevokeAll(user)
		secEvent(sess, "sessions.revoke-all", user, I2S(n)+" ended", ip, "")
		return user + " is signed out everywhere (" + I2S(n) + " session" + py.Plural(int(n)) + ").", "ok", nil
	case "reset":
		pw, err := auth.ResetPassword(user)
		if err != nil {
			return "", "", err
		}
		return "Password of " + user + " reset. Temporary password, shown only now: " + pw, "ok", nil
	case "role-admin", "role-viewer":
		if err := auth.SetRole(user, action[5:]); err != nil {
			return "", "", err
		}
		return user + " is now " + action[5:] + ".", "ok", nil
	case "disable", "enable":
		if err := auth.SetDisabled(user, action == "disable"); err != nil {
			return "", "", err
		}
		return user + " " + action + "d.", "ok", nil
	}
	return "Unknown action.", "fail", nil
}
