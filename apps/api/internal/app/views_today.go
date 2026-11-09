package app

// Today (/today): the day's triaged, time-boxed plan (argus today writes ops.db today_plans / today_items and
// today-status.json). Admins mark items done, start, skip, defer, undo, pull a Later item into the day, re-plan and
// change the budget through request files (settings-in/today-actions, applied by argus); viewers read it.
// The same item renderer draws the "Act now" box on top of the long pages (Incidents, Problems, Vulnerabilities,
// CI/CD, Capacity, Backups, QA). Go only (ADR-0005): the Python app is the frozen rollback image. Everything renders
// only while FEATURE_TODAY is on (env or today-status.json "enabled"); off, every page is unchanged. No JavaScript.

import (
	"os"
	"path/filepath"
	"regexp"

	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var (
	todayActions  = []string{"start", "done", "skip", "defer", "undo", "today", "replan", "settings"}
	todayEnvShort = map[string]string{"release": "prod", "production": "prod", "pre-release": "pre", "staging": "pre",
		"develop": "dev", "dev": "dev"}

	todayRidRe = regexp.MustCompile(`^[0-9]{1,12}$`)
)

const todaySentWords = "start:Started|done:Marked done|skip:Skipped|defer:Deferred|undo:Back in the plan|today:Added to today|" +
	"replan:Re-planned|settings:Settings saved"

func todayStatus() *py.OMap { return ReadJSONMap(filepath.Join(filepath.Dir(DB), "today-status.json")) }

// TodayOn: the feature flag (env or the planner's status file) and the tables exist.
func TodayOn() bool {
	if env("FEATURE_TODAY", "0") != "1" && todayStatus().Get("enabled") != true {
		return false
	}
	return len(Q("SELECT 1 FROM sqlite_master WHERE type='table' AND name='today_items'")) > 0
}

// TodayCanAct: the request directory exists (root creates it; the app never does).
func TodayCanAct() bool {
	st, err := os.Stat(filepath.Join(SET_IN, "today-actions"))
	return err == nil && st.IsDir()
}

// TodayLanding is where a sign-in without a target lands: Today (the plan while the planner is on, else the
// day's health summary).
func TodayLanding(next string) string {
	if next == "/" || next == "" {
		return "/today"
	}
	return next
}

func todayPlan() Row {
	r := Q("SELECT * FROM today_plans ORDER BY day DESC LIMIT 1")
	if len(r) == 0 {
		return nil
	}
	return r[0]
}

func todayItems(pid int64) []Row {
	return Q("SELECT * FROM today_items WHERE plan_id=? ORDER BY rank, id", pid)
}

// todayLeft is the number of today's open items (the Today count in the sidebar).
func todayLeft() int64 {
	p := todayPlan()
	if p == nil {
		return 0
	}
	if r := Q("SELECT count(*) n FROM today_items WHERE plan_id=? AND slot='today' AND status IN ('planned','doing')", p.I("id")); len(r) > 0 {
		return r[0].I("n")
	}
	return 0
}

func todayLink(r Row) string {
	l := r.S("link")
	if !strings.HasPrefix(l, "/") || strings.HasPrefix(l, "//") {
		return "/today"
	}
	if x := todayExact(r, l); x != "" {
		return x
	}
	return l
}

// todayExact: the exact item behind a plan line whose stored link is only its list page (a link that would reload the
// page it sits on): the failing backup set, the VM of the filling disk, the first QA report waiting for an answer, the
// first vulnerability group of a wave. "" keeps the stored link. Display only: the planner's row is not changed.
func todayExact(r Row, l string) string {
	ref := r.S("ref")
	switch {
	case ref == "backups:failing" && l == "/backups":
		rows := LatestBackupRows()
		acc := BackupAccepted(rows)
		var bad []string
		for _, x := range rows {
			if _, a := acc[x.Str("name")]; x.Str("status") != "OK" && !a {
				bad = append(bad, x.Str("name"))
			}
		}
		if len(bad) == 1 {
			return "/backups/set?name=" + py.Quote(bad[0])
		}
		if len(bad) > 1 {
			return "/backups?status=fail"
		}
	case strings.HasPrefix(ref, "disk:") && l == "/capacity":
		parts := strings.SplitN(ref, ":", 4) // disk:<env>:<host or host group>:<mount>
		if len(parts) == 4 {
			for _, f := range py.AsList(capacityDoc().Get("filesystems")) {
				m := py.AsMap(f)
				if m != nil && strings.HasPrefix(m.S("host"), parts[2]) && m.S("mount") == parts[3] && py.I(m.Get("vm_id")) > 0 {
					return "/inventory/view?id=" + I2S(py.I(m.Get("vm_id")))
				}
			}
		}
	case ref == "qa:needs_info":
		if q := Q("SELECT id FROM qa_issues WHERE coalesce(actionability, '') != 'actionable' AND incident_id IS NULL " +
			"ORDER BY coalesce(last_ts, created_at) DESC, id DESC LIMIT 1"); len(q) > 0 {
			return "/qa?state=needs_info#issue-" + I2S(q[0].I("id"))
		}
	}
	if !strings.ContainsAny(l, "?#") { // a list page: the first detail that names one item
		v, err := py.ParseJSON([]byte(py.OrS(r.Get("details"), "[]")))
		if err == nil {
			for _, x := range py.AsList(v) {
				if m := py.AsMap(x); m != nil {
					d := py.Str(py.Or(m.Get("link"), ""))
					if strings.HasPrefix(d, "/") && !strings.HasPrefix(d, "//") && strings.ContainsAny(d, "?#") {
						return d
					}
				}
			}
		}
	}
	return ""
}

// todayCloseLabel names where "close the source" leads.
func todayCloseLabel(l string) string {
	switch {
	case strings.HasPrefix(l, "/cicd/"):
		return "Open the CI/CD finding"
	case strings.HasPrefix(l, "/incidents/"):
		return "Open the incident"
	case strings.HasPrefix(l, "/board/"):
		return "Open the task"
	case strings.HasPrefix(l, "/backups"):
		return "Open the backup set"
	}
	return "Open the source"
}

func todayIntField(form py.QS, k string, lo, hi int64) (int64, bool) {
	v := py.Strip(form.Get(k))
	if !py.IsDigit(v) || len(v) > 4 {
		return 0, false
	}
	n := py.I(v)
	return n, lo <= n && n <= hi
}

// TodayPostAct is POST /today/act: one request file per action, applied by argus today apply (the worker job today-actions).
func TodayPostAct(c *Ctx, form py.QS, sess *Session) (int, string) {
	if sess.Role != "admin" {
		return 403, "Only admins change the plan."
	}
	if !TodayOn() || !TodayCanAct() {
		return 400, "Today is not enabled."
	}
	act := form.Get("action")
	if !In(act, todayActions...) {
		return 400, "Unknown action."
	}
	now := NowInt()
	body := py.NewOMap("action", act, "by", sess.Username, "role", sess.Role, "at", now)
	anchor := ""
	if act != "replan" && act != "settings" {
		iid := form.Get("item")
		if !py.IsDigit(iid) || len(iid) > 9 || py.I(iid) <= 0 {
			return 400, "Choose an item."
		}
		body.Set("item", py.I(iid))
		anchor = "#t-" + iid
	}
	if r := strings.Join(py.Split(form.Get("reason")), " "); r != "" {
		if py.Len(r) > 200 {
			return 400, "The reason is at most 200 characters."
		}
		body.Set("reason", r)
	}
	if m := py.Strip(form.Get("minutes")); m != "" {
		n, ok := todayIntField(form, "minutes", 1, 600)
		if !ok {
			return 400, "Minutes must be 1 to 600."
		}
		body.Set("minutes", n)
	}
	if act == "defer" {
		n, ok := todayIntField(form, "days", 1, 30)
		if !ok {
			return 400, "Defer for 1 to 30 days."
		}
		body.Set("days", n)
	}
	if act == "settings" {
		for _, s := range []struct {
			k      string
			lo, hi int64
		}{{"focus_min", 15, 600}, {"max_items", 1, 20}, {"max_risky", 0, 3}} {
			n, ok := todayIntField(form, s.k, s.lo, s.hi)
			if !ok {
				return 400, "Focus time 15 to 600 min, 1 to 20 tasks, 0 to 3 changes with a window."
			}
			body.Set(s.k, n)
		}
	}
	rid := time.Now().Format("20060102150405") + "-" + auth.TokenHex(4)
	d := filepath.Join(SET_IN, "today-actions")
	if err := WriteFileAtomic(filepath.Join(d, rid+".json"), filepath.Join(d, "."+rid+".tmp"), []byte(py.Dumps(body, -1))); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	logf("today %s %s requested by %s: %s", act, form.Get("item"), sess.Username, rid)
	return 303, "/today?sent=" + act + "&at=" + I2S(now) + anchor
}
