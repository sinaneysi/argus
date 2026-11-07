// Package app is the argus-api web app (Argus): a Go port of app/server.py and the app/views_*.py modules.
// Pages are built as strings exactly like the Python app (escape first with E, then compose), so the HTML is
// byte-identical; tests/parity compares the two apps route by route.
package app

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/py"
)

// E is html.escape(s) (quote=True).
var E = py.E

// env is a setting from the configuration loader (internal/config): the environment, the argus.env file, an old
// OPS_* alias or the schema default, in that order; def when none of them sets the key. k is the canonical name;
// an unknown name panics (config.Lookup), so every setting the web app reads is in the schema.
func env(k, def string) string {
	if v, ok := config.Get(k); ok {
		return v
	}
	return def
}

// envSet is the key's value when it is configured explicitly (not a schema default); "" otherwise.
func envSet(k string) string {
	if v := config.Lookup(k); v.Set && v.Source != "default" {
		return v.Raw
	}
	return ""
}

// Configuration, same variables and defaults as server.py. Tests may change them.
var (
	DB              = env("ARGUS_DB", "/var/lib/argus/ops.db")
	REPORTS         = env("ARGUS_REPORTS_DIR", "/opt/argus/reports/rca")
	BIND            = env("ARGUS_BIND", "127.0.0.1:8088")
	STALE_H         = envInt("ARGUS_STALE_HOURS", 25)
	BREAK_GLASS     = []string{env("ARGUS_ADMIN_USER", "admin")}
	ADMINS          = splitAdmins(env("ARGUS_ADMINS", defAdmins))
	IMPORT_HTPASSWD = env("ARGUS_IMPORT_HTPASSWD", "/import/htpasswd")
	SET_IN          = env("ARGUS_SETTINGS_IN", "/settings-in")
	SET_OUT         = env("ARGUS_SETTINGS_OUT", "/settings-out")
	SECRET_IN       = env("ARGUS_SECRET_IN", "/secret-in") // ADR-0013 P3: tmpfs, write-once task secrets (never on disk)
	CRED            = env("ARGUS_CRED_STATUS_FILE", "/var/lib/argus/cred-status.json")
	ALERTS          = env("ARGUS_ALERTS_FILE", "/var/lib/argus/alerts-firing.json")
	STATIC          = env("ARGUS_STATIC_DIR", defaultStatic())
)

func envInt(k string, def int) int {
	if v, ok := config.Get(k); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func splitAdmins(s string) []string {
	var out []string
	for _, u := range strings.Split(s, ",") {
		if u = py.Strip(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// defaultStatic: ./static next to the binary (the image), else ../packages/design/static from the repo (tests, local runs).
func defaultStatic() string {
	if exe, err := os.Executable(); err == nil {
		d := filepath.Join(filepath.Dir(exe), "static")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	for _, d := range []string{"static", "packages/design/static", "../packages/design/static", "../../packages/design/static", "../../../packages/design/static", "../../../../packages/design/static"} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			abs, _ := filepath.Abs(d)
			return abs
		}
	}
	return "/app/static"
}

// IsBreakGlass is `user in BREAK_GLASS`.
func IsBreakGlass(u string) bool {
	for _, b := range BREAK_GLASS {
		if b == u {
			return true
		}
	}
	return false
}

// NowFn is time.time(); tests may pin it.
var NowFn = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Now is time.time().
func Now() float64 { return NowFn() }

// NowInt is int(time.time()).
func NowInt() int64 { return int64(math.Floor(NowFn())) }

// Session is the dict auth.session() returns.
type Session = auth.Session

// Ctx is the per-request presentation context (Python's thread-local CTX).
type Ctx struct {
	Sess    *Session
	Refresh any // false (nil), true, an int (seconds) or a string (hand-off URL); see refreshMeta
	Theme   string
	IP      string
	Path    string // the raw request target (path?query) or, after a POST, its GET twin
	Token   string // the session cookie (Go only: the Account page marks "this one")
	Embed   bool   // Layout returns the body only (a kind's queue inside Review, views_review.go)
	Rej     *Rejected
}

// NewCtx is a context for tests and handlers.
func NewCtx(sess *Session, path string) *Ctx {
	return &Ctx{Sess: sess, Theme: "auto", Path: path}
}

// --- rows ------------------------------------------------------------------------------------------

// Row is a sqlite3.Row: column name -> int64, float64, string, []byte or nil. The first of two equally named
// columns wins (like sqlite3.Row lookups).
type Row map[string]any

// Get is r[k] (nil when missing).
func (r Row) Get(k string) any { return r[k] }

// Has is `k in r.keys()`.
func (r Row) Has(k string) bool { _, ok := r[k]; return ok }

// S is (r[k] or "") as a string.
func (r Row) S(k string) string { return py.OrS(r[k], "") }

// Str is str(r[k]) ("None" for NULL).
func (r Row) Str(k string) string { return py.Str(r[k]) }

// I is int(r[k] or 0).
func (r Row) I(k string) int64 { return py.I(r[k]) }

// F is float(r[k] or 0).
func (r Row) F(k string) float64 { return py.Num(r[k]) }

// T is bool(r[k]).
func (r Row) T(k string) bool { return py.Truthy(r[k]) }

// IsNull is r[k] is None.
func (r Row) IsNull(k string) bool { return r[k] == nil }

// --- JSON files ------------------------------------------------------------------------------------

// ReadJSON is read_json(path, default).
func ReadJSON(path string, def any) any { return py.ReadJSON(path, def) }

// ReadJSONMap is `read_json(path, {}) or {}` used as a dict ({} when it is not an object).
func ReadJSONMap(path string) *py.OMap { return py.ReadJSONMap(path) }

// --- formatting --------------------------------------------------------------------------------------

// Ago is ago(ts).
func Ago(ts any) string {
	if !py.Truthy(ts) {
		return "never"
	}
	s := int64(Now() - py.Num(ts))
	for _, u := range []struct {
		unit string
		n    int64
	}{{"d", 86400}, {"h", 3600}, {"m", 60}} {
		if s >= u.n {
			return strconv.FormatInt(py.FloorDiv(s, u.n), 10) + u.unit + " ago"
		}
	}
	return "just now"
}

// LocalTime is time.localtime(ts) as a Go time.
func LocalTime(ts float64) time.Time {
	return time.Unix(int64(math.Floor(ts)), 0).In(time.Local)
}

// When is when(ts): "%Y-%m-%d %H:%M" local, "-" when falsy.
func When(ts any) string {
	if !py.Truthy(ts) {
		return "-"
	}
	return LocalTime(py.Num(ts)).Format("2006-01-02 15:04")
}

// Strftime formats ts (seconds) with a Go layout in local time.
func Strftime(layout string, ts float64) string { return LocalTime(ts).Format(layout) }

// Size is size(n): bytes in B/KB/MB/GB/TB; "-" for None.
func Size(v any) string {
	if v == nil {
		return "-"
	}
	n := py.Num(v)
	for _, unit := range []string{"B", "KB", "MB", "GB", "TB"} {
		if n < 1024 || unit == "TB" {
			if unit == "B" {
				return py.F(n, 0) + " " + unit
			}
			return py.F(n, 1) + " " + unit
		}
		n /= 1024
	}
	return ""
}

// SizeF is size() for a float (the chart formatter).
func SizeF(n float64) string { return Size(n) }

// Badge is badge(status).
func Badge(status any) string {
	st := py.OrS(status, "?")
	s := strings.ToUpper(st)
	kind := "fail"
	if strings.HasPrefix(s, "OK") {
		kind = "ok"
	} else if strings.HasPrefix(s, "WARN") {
		kind = "warn"
	}
	return `<span class="badge ` + kind + `">` + E(st) + `</span>`
}

var reMarks = regexp.MustCompile("[`*]")

// FirstLine is first_line(text, n=160).
func FirstLine(text any, n ...int) string {
	lim := 160
	if len(n) > 0 {
		lim = n[0]
	}
	t := py.OrS(text, "")
	line := ""
	if st := py.Strip(t); st != "" {
		if ls := py.SplitLines(st); len(ls) > 0 {
			line = ls[0]
		}
	}
	line = reMarks.ReplaceAllString(line, "")
	if py.Len(line) <= lim {
		return line
	}
	return py.Head(line, lim-1) + "…"
}

// Banner is banner(msg, kind="ok").
func Banner(msg string, kind ...string) string {
	k := "ok"
	if len(kind) > 0 {
		k = kind[0]
	}
	if msg == "" {
		return ""
	}
	ic := Icon("alert")
	if k == "ok" {
		ic = Icon("check")
	}
	role := "status" // a failure needs attention: announce it as an alert
	if k == "fail" {
		role = "alert"
	}
	return `<div class="banner ` + k + `" role="` + role + `">` + ic + `<span>` + E(msg) + `</span></div>`
}

// --- states (DESIGN.md "States"): what happened, is it safe to leave, where the result appears, what next ---------

// State kinds. The three "nothing here" kinds are page content: an .empty block, no live region. Queued/running
// and saved-awaiting-sync are role="status"; a failure is role="alert"; a missing permission is a neutral banner
// with role="status" (nothing failed, nothing to retry).
const (
	StNoMatch = "nomatch" // data exists; the filters match none of it
	StNever   = "never"   // a scan or job has never run
	StBusy    = "busy"    // queued or running
	StFailed  = "failed"  // data failed to load, or an action failed
	StDenied  = "denied"  // the account lacks the role
	StSynced  = "synced"  // saved here, other views catch up at the next sync
)

// stateDefault is the kind's sentence when the caller gives no detail.
var stateDefault = map[string]string{
	StNoMatch: "Clear or widen the filters to see more.",
	StNever:   "It has not run yet, so there is nothing to show.",
	StBusy:    "Safe to leave this page: the work carries on and the result appears here. The page refreshes itself.",
	StFailed:  "Nothing was changed. Reload in a minute; if it keeps failing, tell an admin.",
	StDenied:  "Your account does not have the role this needs. Nothing was changed. Ask an admin if you need access.",
	StSynced:  "Saved. Other views catch up at the next sync, within a few minutes.",
}

// StateBox renders one state. title is plain text (what happened, a full sentence); detail and next are HTML the
// caller escaped (next is one link or form); an empty detail takes the kind's default sentence.
func StateBox(kind, title, detail, next string) string {
	if detail == "" {
		detail = stateDefault[kind]
	}
	tail := strings.TrimSpace(detail + " " + next)
	cls, role, ic := "", "status", ""
	switch kind {
	case StBusy:
		ic = "scan"
	case StSynced:
		cls, ic = " ok", "check"
	case StFailed:
		cls, role, ic = " fail", "alert", "alert"
	case StDenied:
		ic = "security"
	default: // empty, nomatch, never: content, not an announcement
		return `<div class="empty"><b>` + E(title) + `</b>` + tail + `</div>`
	}
	return `<div class="banner` + cls + `" role="` + role + `">` + Icon(ic) + `<span><b>` + E(title) + `</b> ` + tail + `</span></div>`
}

var (
	reSecretKV  = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|authorization|bearer|api[_-]?key|private[_-]?key)\b(\s*[:=]\s*|\s+)\S+`)
	reLongToken = regexp.MustCompile(`[A-Za-z0-9_\-+/=]{32,}`)
	rePath      = regexp.MustCompile(`(^|[\s"'(=:])(/[\w.@\-]+){2,}/?`)
)

// SafeErr turns a stored error string into one line an operator may see: the first line, at most 160 characters,
// with values after secret-looking keys, long token-like strings and absolute paths taken out. Escaped.
func SafeErr(s string) string {
	s = FirstLine(s, 400)
	s = reSecretKV.ReplaceAllString(s, "$1 [hidden]")
	s = reLongToken.ReplaceAllString(s, "[hidden]")
	s = rePath.ReplaceAllString(s, "$1[path]")
	if py.Len(s) > 160 {
		s = py.Head(s, 159) + "…"
	}
	return E(s)
}

var reShortTS = regexp.MustCompile(`\d{4}-(\d\d-\d\d)[ T](\d\d:\d\d)`)

// ShortTS is short_ts(text).
func ShortTS(text any) string {
	s := py.OrS(text, "")
	if m := reShortTS.FindStringSubmatch(s); m != nil {
		return m[1] + " " + m[2]
	}
	return py.OrS(text, "-")
}

// EnvTag is env_tag(env).
func EnvTag(envv any) string {
	en := py.OrS(envv, "")
	if en == "" {
		return `<span class="dim">-</span>`
	}
	prod := ""
	if en == "release" {
		prod = " prod"
	}
	return `<span class="tag` + prod + `">` + E(EnvLabel(en)) + `</span>`
}

// --- icons, page head, disclosure ---------------------------------------------------------------------

// ICONS are 24x24 stroke icons (SVG presentation attributes only).
var ICONS = map[string]string{
	"alerts":   "M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.7 21a2 2 0 0 1-3.4 0",
	"overview": "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
	"backups": "M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6" +
		"M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6",
	"certs":     "M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6zM9 12l2 2 4-4",
	"incidents": "M10.3 3.9L2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0zM12 9v4M12 17h.01",
	"runs":      "M3 12h4l3-8 4 16 3-8h4",
	"qa":        "M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2zM8 9h8M8 13h5",
	"capacity":  "M3 5h18v6H3zM3 13h18v6H3zM7 8h.01M7 16h.01",
	"hygiene":   "M4 20l7-7M14 4l6 6-7 7-6-6zM9 9l6 6",
	"admin":     "M4 7h9M17 7h3M4 17h3M11 17h9M15 5v4M9 15v4",
	"users": "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM22 21v-2a4 4 0 0 0-3-3.9" +
		"M16 3.1a4 4 0 0 1 0 7.8",
	"scan":      "M21 12a9 9 0 1 1-2.6-6.4L21 8M21 3v5h-5",
	"logout":    "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9",
	"alert":     "M12 8v5M12 16h.01M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z",
	"check":     "M20 6L9 17l-5-5",
	"today":     "M10 6h10M10 12h10M10 18h10M4 6l1.5 1.5L8 5M4 12l1.5 1.5L8 11M4 18l1.5 1.5L8 17",
	"back":      "M19 12H5M11 6l-6 6 6 6",
	"next":      "M5 12h14M13 6l6 6-6 6",
	"inventory": "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01M12 7h4M12 17h4",
	"rca":       "M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9zM14 3v6h6M8 13h8M8 17h5",
	"board":     "M4 4h4v16H4zM10 4h4v10h-4zM16 4h4v13h-4z",
	"proposals": "M4 13h4.5l1.5 2.5h4l1.5-2.5H20M6.5 5h11L20 13v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1v-5z",
	"design":    "M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
	"ideas":     "M9 18h6M10 21h4M12 3a6 6 0 0 0-3.6 10.8c.6.5 1 1.2 1 2v.2h5.2v-.2c0-.8.4-1.5 1-2A6 6 0 0 0 12 3z",
	"auto":      "M3 5h18v11H3zM8 20h8M12 16v4",
	"light": "M12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2" +
		"M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4",
	"dark":     "M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z",
	"cicd":     "M6 3v12M18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 9a9 9 0 0 1-9 9",
	"security": "M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6zM12 8v5M12 16h.01",
	"problems": "M12 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM5 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6z" +
		"M12 9v3M12 12l-5 3.5M12 12l5 3.5",
	"help": "M4 19.5A2.5 2.5 0 0 1 6.5 17H20V2H6.5A2.5 2.5 0 0 0 4 4.5zM4 19.5A2.5 2.5 0 0 0 6.5 22H20v-5",
	"fix": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.8-3.8a6 6 0 0 1-7.9 7.9l-6.9 6.9a2.1 2.1 0 0 1-3-3" +
		"l6.9-6.9a6 6 0 0 1 7.9-7.9z",
}

// Icon is icon(name). An unknown name panics like the Python KeyError (a 500).
func Icon(name string) string {
	d, ok := ICONS[name]
	if !ok {
		panic("unknown icon " + name)
	}
	return `<svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="` + d + `"/></svg>`
}

const (
	SHOW_N = 15
)

// WantsAll is wants_all(qs).
func WantsAll(qs py.QS) bool { return qs.Get("all") == "1" }

// Capped is capped(items, qs, n): the first n items, or all with ?all=1.
func Capped[T any](items []T, qs py.QS, n ...int) []T {
	lim := SHOW_N
	if len(n) > 0 {
		lim = n[0]
	}
	if WantsAll(qs) || len(items) <= lim {
		return items
	}
	return items[:lim]
}

// AllHref is all_href(qs, on, anchor).
func (c *Ctx) AllHref(qs py.QS, on bool, anchor string) string {
	p := c.Path
	if p == "" {
		p = "/"
	}
	path := py.URLSplit(p).Path
	keep := qs.Pairs("all")
	if on {
		keep = append(keep, [2]string{"all", "1"})
	}
	out := path
	if len(keep) > 0 {
		out += "?" + py.Urlencode(keep)
	}
	if anchor != "" {
		out += "#" + anchor
	}
	return out
}

// Fold is fold(title, inner, count=None, open_=False, note=""). count < 0 means None; note is HTML.
func Fold(title, inner string, count int, open bool, note string) string {
	c := ""
	if count >= 0 {
		c = ` <span class="count">` + strconv.Itoa(count) + `</span>`
	}
	n := ""
	if note != "" {
		n = `<span class="muted small">` + note + `</span>`
	}
	o := ""
	if open {
		o = " open"
	}
	return `<details` + o + `><summary>` + E(title) + c + n + `</summary>` + inner + `</details>`
}

// TableMore is table_more(head, rows, n=5, noun="rows", cls="tablewrap", tcls="").
func TableMore(head string, rows []string, n int, noun, cls, tcls string) string {
	if n == 0 {
		n = 5
	}
	if noun == "" {
		noun = "rows"
	}
	if cls == "" {
		cls = "tablewrap"
	}
	t := "<table>"
	if tcls != "" {
		t = `<table class="` + tcls + `">`
	}
	if len(rows) <= n+1 {
		return `<div class="` + cls + `">` + t + head + `<tbody>` + strings.Join(rows, "") + `</tbody></table></div>`
	}
	first := `<div class="` + cls + `">` + t + head + `<tbody>` + strings.Join(rows[:n], "") + `</tbody></table></div>`
	return first + Fold("More "+noun, `<div class="`+cls+`">`+t+head+`<tbody>`+strings.Join(rows[n:], "")+`</tbody></table></div>`,
		len(rows)-n, false, "")
}

// --- static file versions ----------------------------------------------------------------------------

// I2S is str(int).
func I2S(n int64) string { return strconv.FormatInt(n, 10) }

// Itoa is str(int).
func Itoa(n int) string { return strconv.Itoa(n) }

// Join is "".join(xs).
func Join(xs []string) string { return strings.Join(xs, "") }

// If is the conditional expression `a if cond else b` for strings.
func If(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// In is `x in (a, b, ...)`.
func In(x string, xs ...string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
