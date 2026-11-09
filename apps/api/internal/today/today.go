// Package today is the Today planner (the worker jobs `today` and `today-actions`, `argus today`; port of the
// earlier installation's planner). It triages everything that waits for the operator into one short, ordered,
// time-boxed plan a day.
//
// Deterministic. The kinds, estimates and defaults are in today.json (embedded; ARGUS_TODAY_CONFIG replaces it), the
// operator's open decisions in an optional file (ARGUS_TODAY_DECISIONS). The app (/today) reads ops.db today_plans /
// today_items and today-status.json next to ops.db; it writes request files to settings-in/today-actions.
//
//	tick              at ARGUS_TODAY_PLAN_AT (07:30): build the day's plan once; later ticks only refresh it (closed
//	                  sources -> done, new page-level items added, counts refreshed)
//	plan [-replan]    build (or rebuild) today's plan now
//	apply             the app's requests: start, done, skip, defer, undo, today, replan, settings
//	show              print today's plan as text
//	status            rewrite today-status.json
//
// One item per root cause: a problem before its incidents, CVEs grouped by fix (one reboot wave, one patch wave),
// CI/CD findings by repo (release) or by class (pre-release, develop), disks by host family, proposals and questions
// as one batch. Score = urgency x impact x confidence / effort, effort = 1 + minutes / 60. The day is filled in score
// order up to the focus time (180 min) and item count (8), with at most one production change that needs a window;
// the rest goes to Later with its reason. Not done today carries over (re-scored, +0.5 urgency a day). Done: the
// actual minutes teach the estimate of the kind (moving average).
//
// Not ported: the optional model pass that only shortened the titles and one-liners (the plan is the deterministic
// one; the status file says refine false).
package today

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

//go:embed today.json
var defaultConfig []byte

// Options are the inputs of one run.
type Options struct {
	DB    DB // ops.db (read-write)
	Board DB // board.db (read-only; nil: no board sources)

	ConfigFile    string // replaces the embedded today.json ("": embedded)
	DecisionsFile string // the operator's open decisions ("" or missing: none)
	StateDir      string // capacity.json, problems-backfill-plan.json
	SelfcheckFile string // selfcheck.json
	CredFile      string // credentials.json of the credential expiry job
	ActionsDir    string // settings-in/today-actions
	StatusFile    string // today-status.json (the app reads it next to ops.db)

	Enabled  bool   // FEATURE_TODAY
	Operator string // whose Jira tasks are in the plan ("": none)
	Statuses string // Jira statuses of the operator's tasks, comma separated ("": In Progress)
	PlanAt   string // HH:MM ("": the config default)
	FocusMin string // raw values; invalid or empty: the config default (then clamped)
	MaxItems string
	MaxRisky string
	Loc      *time.Location

	App   string // the product name in texts ("Argus")
	Actor string // the planner's name in events ("argus")

	Now func() time.Time
	Out io.Writer // command output
	Log io.Writer // warnings and rejected requests
}

func (o *Options) now() int64 {
	if o.Now != nil {
		return o.Now().Unix()
	}
	return time.Now().Unix()
}

func (o *Options) loc() *time.Location {
	if o.Loc != nil {
		return o.Loc
	}
	return time.UTC
}

func (o *Options) app() string {
	if o.App != "" {
		return o.App
	}
	return "Argus"
}

func (o *Options) actor() string {
	if o.Actor != "" {
		return o.Actor
	}
	return "argus"
}

func (o *Options) out() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return io.Discard
}

func (o *Options) log() io.Writer {
	if o.Log != nil {
		return o.Log
	}
	return io.Discard
}

// --- configuration ---------------------------------------------------------------------------------------------

// Config is today.json.
type Config struct{ *py.OMap }

// LoadConfig reads path, or the embedded defaults when path is empty.
func LoadConfig(path string) (*Config, error) {
	b := defaultConfig
	if path != "" {
		var err error
		if b, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	v, err := py.ParseJSON(b)
	if err != nil {
		return nil, err
	}
	m, ok := v.(*py.OMap)
	if !ok {
		m = py.NewOMap()
	}
	for _, k := range []string{"defaults", "kinds"} {
		if m.Map(k) == nil {
			m.Set(k, py.NewOMap())
		}
	}
	if m.Map("size") == nil {
		m.Set("size", py.NewOMap("S", 0.75, "M", 1.0, "L", 1.5))
	}
	return &Config{m}, nil
}

func (c *Config) def(k string, d any) any { return c.Map("defaults").GetD(k, d) }

func (c *Config) kind(k string) *py.OMap {
	if m := c.Map("kinds").Map(k); m != nil {
		return m
	}
	if c.Map("kinds").Has(k) {
		return py.NewOMap()
	}
	return py.NewOMap("minutes", int64(20))
}

func envInt(raw string, def any, lo, hi int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		v = int(num(def))
	}
	return min(max(v, lo), hi)
}

// Settings are the day's budget: admin settings (ops.db today_settings) > the options > config defaults.
type Settings struct{ FocusMin, MaxItems, MaxRisky int }

var settingLimits = []struct {
	k      string
	lo, hi int
}{{"focus_min", 15, 600}, {"max_items", 1, 20}, {"max_risky", 0, 3}}

func (o *Options) settings(ctx context.Context, d DB, cfg *Config) Settings {
	s := Settings{FocusMin: envInt(o.FocusMin, cfg.def("focus_min", int64(180)), 15, 600),
		MaxItems: envInt(o.MaxItems, cfg.def("max_items", int64(8)), 1, 20),
		MaxRisky: envInt(o.MaxRisky, cfg.def("max_risky", int64(1)), 0, 3)}
	for _, r := range soft(ctx, d, "SELECT k, v FROM today_settings") {
		v := py.Str(r.At(1))
		if !isDigits(v) {
			continue
		}
		n, _ := strconv.Atoi(v)
		for _, l := range settingLimits {
			if l.k == py.Str(r.At(0)) {
				n = min(max(n, l.lo), l.hi)
				switch l.k {
				case "focus_min":
					s.FocusMin = n
				case "max_items":
					s.MaxItems = n
				case "max_risky":
					s.MaxRisky = n
				}
			}
		}
	}
	return s
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) < 10
}

func (o *Options) dayOf(ts int64) string { return time.Unix(ts, 0).In(o.loc()).Format("2006-01-02") }

func dayAdd(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// --- text helpers ------------------------------------------------------------------------------------------------

var ctrl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func strOr(v any) string {
	if !truthy(v) {
		return ""
	}
	return py.Str(v)
}

func one(v any, capN int) string {
	t := strings.Join(py.Split(ctrl.ReplaceAllString(strOr(v), " ")), " ")
	r := []rune(t)
	if len(r) <= capN {
		return t
	}
	cut := r[:capN-1]
	if strings.Contains(string(cut[capN/2:]), " ") {
		s := string(cut)
		cut = []rune(s[:strings.LastIndex(s, " ")])
	}
	return strings.TrimRight(string(cut), " ,;:-") + "…"
}

func words(v any, n int) string {
	w := py.Split(one(v, 400))
	if len(w) <= n {
		return strings.Join(w, " ")
	}
	return strings.TrimRight(strings.Join(w[:n], " "), " ,;:-.") + "…"
}

func plural(n int, word string, many ...string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	if len(many) > 0 {
		return strconv.Itoa(n) + " " + many[0]
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func ago(ts any, now int64) string {
	if !truthy(ts) {
		return "-"
	}
	d := max(0, now-int64(num(ts)))
	switch {
	case d < 3600:
		return strconv.FormatInt(d/60, 10) + " min"
	case d < 86400:
		return strconv.FormatInt(d/3600, 10) + " h"
	}
	return strconv.FormatInt(d/86400, 10) + " d"
}

func shortRepo(p any) string {
	s := strOr(p)
	if i := strings.LastIndex(s, "/"); i >= 0 && i < len(s)-1 {
		return s[i+1:]
	}
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s
}

func sigOf(parts ...any) string {
	ss := make([]string, len(parts))
	for i, p := range parts {
		ss[i] = py.Str(p)
	}
	h := sha1.Sum([]byte(strings.Join(ss, "|")))
	return hex.EncodeToString(h[:])[:16]
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []byte:
		return len(x) > 0
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case *py.OMap:
		return x.Len() > 0
	case []any:
		return len(x) > 0
	}
	return true
}

func num(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

func isNum(v any) bool {
	switch v.(type) {
	case int64, int, float64:
		return true
	}
	return false
}

func i64(v any) int64 { return int64(num(v)) }

func or(v any, d any) any {
	if truthy(v) {
		return v
	}
	return d
}

// pyRound is Python's round(x, n) (correctly rounded, ties to even).
func pyRound(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	return f
}

func sortedSet(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func log2(x float64) float64 { return math.Log2(x) }
