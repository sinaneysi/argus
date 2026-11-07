package app

import (
	"os"
	"path/filepath"

	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// SafeNext is safe_next(n): a local path to go to after login; anything else (other host, scheme, //, \,
// controls, non-ASCII, /auth/...) is "/".
func SafeNext(n string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.Contains(n, `\`) || strings.HasPrefix(n, "/auth/") {
		return "/"
	}
	for _, r := range n {
		if r < 0x21 || r > 0x7e {
			return "/"
		}
	}
	return n
}

// --- static versions -------------------------------------------------------------------------------------

// --- navigation ---------------------------------------------------------------------------------------------
// The sidebar is the six collapsible groups of ia.go (SECTIONS); Settings and Help sit in its footer.

type tab struct{ Label, Href string }

const THEME_COOKIE = "opsr_theme"

// ThemeOf is theme_of(cookie_header): "light" or "dark", anything else is "auto".
func ThemeOf(header string) string {
	c, err := py.ParseCookies(header)
	if err != nil {
		return "auto"
	}
	if v := c[THEME_COOKIE]; v == "light" || v == "dark" {
		return v
	}
	return "auto"
}

// --- the scan bar ------------------------------------------------------------------------------------------

var SCAN_TASKS = map[string][]string{"/operations": {"backup-check", "cert-check", "log-rca", "chat-triage"}, "/backups": {"backup-check"},
	"/certs": {"cert-check"}, "/incidents": {"log-rca", "chat-triage"}, "/qa": {"chat-triage"},
	"/capacity": {"insights"}, "/hygiene": {"insights"}, "/inventory": {"inventory"}, "/vulns": {"cve"}}

var SCAN_LABEL = map[string]string{"backup-check": "Backups", "cert-check": "Certificates", "log-rca": "Log RCA",
	"chat-triage": "Chat triage", "insights": "Capacity and hygiene", "inventory": "VM inventory",
	"cve": "Vulnerability scan", "cred-expiry": "Credential check"}

// ScanState is scan_state(task): running / queued / last result, for the scan bar.
func ScanState(task string) string {
	rr := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	st := py.AsMap(rr.Map("tasks").GetD(task, py.NewOMap()))
	if st == nil {
		st = py.NewOMap()
	}
	last := LatestRun(task)
	req := st.GetD("requested", int64(0))
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", task+".req")); err == nil {
		return "queued"
	}
	result := py.Str(st.GetD("result", ""))
	if strings.HasPrefix(result, "skipped") && py.Truthy(req) && (last == nil || py.Num(req) > last.F("started_at")) {
		return result
	}
	if started := st.Get("started"); py.Truthy(started) && (last == nil || py.Num(started) > last.F("started_at")) {
		return "running since " + Ago(started)
	}
	if last != nil {
		return "last run " + Ago(last.Get("started_at"))
	}
	return "never run"
}

// scanStartedAt: when the current, unfinished scan of a task was asked for or started (0 when none is pending): the
// age of a queued request file, else the run's start time.
func scanStartedAt(task string) int64 {
	if fi, err := os.Stat(filepath.Join(SET_IN, "requests", task+".req")); err == nil {
		return fi.ModTime().Unix()
	}
	rr := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	st := py.AsMap(rr.Map("tasks").GetD(task, py.NewOMap()))
	if st == nil {
		return 0
	}
	started := st.Get("started")
	if !py.Truthy(started) {
		return 0
	}
	if last := LatestRun(task); last != nil && py.Num(started) <= last.F("started_at") {
		return 0
	}
	return int64(py.Num(started))
}

// ScanBusy is scan_busy(state).
func ScanBusy(state string) bool { return state == "queued" || strings.HasPrefix(state, "running") }

// scanBarState is the line beside "Scan now": its words, whether a scan runs, and when an overdue scan was asked (0: none).
func scanBarState(tasks []string) (string, bool, int64) {
	states := make([]string, len(tasks))
	busy, skipped := false, ""
	for i, t := range tasks {
		states[i] = ScanState(t)
		busy = busy || ScanBusy(states[i])
		if skipped == "" && strings.HasPrefix(states[i], "skipped") {
			skipped = states[i]
		}
	}
	meta, overdue := "", int64(0)
	if busy {
		meta = "Scan running · safe to leave, results appear here"
		for _, t := range tasks {
			if at := scanStartedAt(t); at > 0 && NowInt()-at > 2*3600 && (overdue == 0 || at < overdue) {
				overdue = at
			}
		}
	} else if skipped != "" {
		meta = strings.ReplaceAll(skipped, "skipped: ", "Not started: ")
	} else {
		var whens []float64
		for _, t := range tasks {
			r := LatestRun(t)
			if r == nil {
				continue
			}
			whens = append(whens, r.F("started_at"))
		}
		if len(whens) > 0 {
			mn := whens[0]
			for _, w := range whens {
				if w < mn {
					mn = w
				}
			}
			meta = "Last scan " + Ago(mn) // the page's own pill says how it went: a second status here could only disagree
		} else {
			meta = "Never scanned"
		}
	}
	return meta, busy, overdue
}
