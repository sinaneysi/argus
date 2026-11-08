// Package settingsapply is `argus settings apply` (root): it applies what Settings -> General saves.
//
// The web app (user 65534) writes ARGUS_SETTINGS_IN/settings.json; root validates the whole file and either applies
// it or rejects it and keeps what is live. Input is untrusted: opened without following a symlink, a small regular
// file only, every value checked against fixed names, choices and ranges. Root never writes into the input directory.
//
// Applied (EtcDir unless named otherwise; the jobs read these):
//
//	thresholds.env        KUMA_MIN_DOWN_SECONDS, KUMA_FLAP_COUNT, CERT_WARN_DAYS, CERT_FAIL_DAYS
//	rca-auto.json         automatic deep RCA (triggers, limits, quiet hours)
//	readiness.json        hub tiers and the sales-event readiness brief
//	qa-ask.json           QA follow-up questions
//	claude.json           Claude usage on/off (missing = on; anything but {"enabled": true} = off)
//	EgressFile            the egress-country guard, only when settings.json has "claude_egress" (else
//	                      `argus secrets apply-claude` owns it and it is left alone)
//	<prefix><task>.timer  schedule drop-in <SystemdDir>/<prefix><task>.timer.d/schedule.conf, enable/disable;
//	                      only when TimerPrefix is set (empty: the schedules are validated and stored, no timer changes)
//
// Status for the app (OutDir, mounted read-only into the app): applied.json ({"ok": false, "error": ...} for a
// rejected save, which the page shows as "Last save was rejected"), the live copies qa-ask.json, claude.json and
// claude-egress.json, and one line per save in audit.log.
package settingsapply

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// MaxFile is the largest settings.json accepted.
const MaxFile = 16384

// Tasks are the scheduled jobs of Settings -> General, in page order.
var Tasks = []string{"chat-triage", "log-rca", "backup-check", "cert-check"}

var intervals = map[string]string{"10m": "*:0/10", "15m": "*:0/15", "30m": "*:0/30", "1h": "hourly", "2h": "0/2:00",
	"4h": "0/4:00", "6h": "0/6:00", "12h": "0/12:00"}

var daily = regexp.MustCompile(`^daily@([01]\d|2[0-3]):(00|15|30|45)$`)

type threshold struct {
	name, env string
	lo, hi    int64
	mult      int64
}

var thresholds = []threshold{
	{"kuma_min_down_minutes", "KUMA_MIN_DOWN_SECONDS", 1, 60, 60},
	{"kuma_flap_count", "KUMA_FLAP_COUNT", 2, 20, 1},
	{"cert_warn_days", "CERT_WARN_DAYS", 7, 60, 1},
	{"cert_fail_days", "CERT_FAIL_DAYS", 1, 30, 1},
}

type field struct {
	k      string
	def    any // bool, int64 or string
	lo, hi int64
}

// The same names, defaults and ranges as the app's Settings -> General form (internal/app/pages_core.go).
var rcaAuto = []field{{"enabled", true, 0, 0}, {"trigger_page", true, 0, 0}, {"trigger_open", true, 0, 0},
	{"trigger_repeat", true, 0, 0}, {"trigger_qa", true, 0, 0}, {"open_hours", int64(2), 1, 72},
	{"repeat_count", int64(3), 2, 20}, {"daily_runs", int64(6), 0, 6}, {"daily_usd", int64(20), 1, 50},
	{"quiet_start", int64(23), 0, 23}, {"quiet_end", int64(8), 0, 23}}

var readiness = []field{{"hub_raise", true, 0, 0}, {"event_name", "Black Friday", 0, 0},
	{"event_date", "2026-11-27", 0, 0}, {"freeze_days", int64(7), 0, 30}, {"before_days", int64(30), 1, 90},
	{"after_days", int64(2), 0, 14}}

var qaAsk = []field{{"enabled", true, 0, 0}, {"dry_run", false, 0, 0}, {"read_images", false, 0, 0}}

var (
	reDate = regexp.MustCompile(`^20\d\d-[01]\d-[0-3]\d$`)
	reName = regexp.MustCompile(`^[\p{L}\p{Nd}\p{Nl}\p{No}_ .'-]{1,40}$`)
	reCC   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Options says where to read and write. Zero SystemdDir means /etc/systemd/system.
type Options struct {
	In          string // settings.json the app writes
	OutDir      string // status files for the app
	EtcDir      string // applied settings the jobs read
	EgressFile  string // egress guard settings (default EtcDir/claude-egress.json)
	TimerPrefix string // "": skip the timer step
	SystemdDir  string
	Systemctl   func(args ...string) (string, error) // nil: run systemctl
	Now         func() time.Time
	Log         io.Writer // stdout
	Err         io.Writer // stderr
}

func (o *Options) fill() {
	if o.SystemdDir == "" {
		o.SystemdDir = "/etc/systemd/system"
	}
	if o.EgressFile == "" {
		o.EgressFile = filepath.Join(o.EtcDir, "claude-egress.json")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Err == nil {
		o.Err = io.Discard
	}
}

type rejected string

func (r rejected) Error() string { return string(r) }

func rej(format string, a ...any) error { return rejected(fmt.Sprintf(format, a...)) }

// object is a JSON object in key order, written like Python's json.dumps (", " and ": ", ASCII only).
type object []kv

type kv struct {
	k string
	v any
}

func dumps(v any) []byte {
	var b bytes.Buffer
	enc(&b, v)
	return b.Bytes()
}

func enc(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case string:
		encStr(b, x)
	case []string:
		b.WriteByte('[')
		for i, s := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			encStr(b, s)
		}
		b.WriteByte(']')
	case object:
		b.WriteByte('{')
		for i, p := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			encStr(b, p.k)
			b.WriteString(": ")
			enc(b, p.v)
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("settingsapply: cannot encode %T", v))
	}
}

func encStr(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r > 0x7e:
			if r > 0xffff {
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			} else {
				fmt.Fprintf(b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// sorted is the object of m with its keys in order (json.dumps(..., sort_keys=True)).
func sorted(m map[string]any) object {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := object{}
	for _, k := range keys {
		out = append(out, kv{k, m[k]})
	}
	return out
}

func line(m map[string]any) []byte { return append(dumps(sorted(m)), '\n') }

// pyStr is Python's str() of a decoded JSON value, for messages.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		return map[bool]string{true: "True", false: "False"}[x]
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func repr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func head(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// whole is a JSON integer (Python isinstance(v, int) and not bool).
func whole(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil
}

// pyInt is Python's int() of a decoded JSON value (thresholds accept "5", 5.0 and true like the Python job).
func pyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		if i, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return i, true
		}
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, false
		}
		return int64(f), true
	case string:
		i, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(x), "_", ""), 10, 64)
		return i, err == nil
	}
	return 0, false
}

func section(cfg map[string]any, name string) (map[string]any, bool, error) {
	raw, ok := cfg[name]
	if !ok || raw == nil {
		return nil, false, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, true, rej("%s must be an object", name)
	}
	return m, true, nil
}

func defaultsOf(fs []field) map[string]any {
	out := map[string]any{}
	for _, f := range fs {
		out[f.k] = f.def
	}
	return out
}

func unknown(raw map[string]any, fs []field) string {
	var extra []string
	for k := range raw {
		known := false
		for _, f := range fs {
			known = known || f.k == k
		}
		if !known {
			extra = append(extra, k)
		}
	}
	if len(extra) == 0 {
		return ""
	}
	sort.Strings(extra)
	return head(extra[0], 40)
}

func getOr(raw map[string]any, k string, d any) any {
	if v, ok := raw[k]; ok {
		return v
	}
	return d
}

// RCAAuto validates "rca_auto"; missing means the defaults.
func RCAAuto(cfg map[string]any) (map[string]any, error) {
	raw, present, err := section(cfg, "rca_auto")
	if err != nil || !present {
		return defaultsOf(rcaAuto), err
	}
	out := map[string]any{}
	for _, f := range rcaAuto {
		v := getOr(raw, f.k, f.def)
		if _, isBool := f.def.(bool); isBool {
			b, ok := v.(bool)
			if !ok {
				return nil, rej("rca_auto.%s must be true or false", f.k)
			}
			out[f.k] = b
			continue
		}
		n, ok := whole(v)
		if d, isDef := v.(int64); isDef {
			n, ok = d, true
		}
		if !ok || n < f.lo || n > f.hi {
			return nil, rej("rca_auto.%s outside %d..%d", f.k, f.lo, f.hi)
		}
		out[f.k] = n
	}
	if x := unknown(raw, rcaAuto); x != "" {
		return nil, rej("rca_auto: unknown setting %s", x)
	}
	return out, nil
}

// Readiness validates "readiness"; missing means the defaults.
func Readiness(cfg map[string]any) (map[string]any, error) {
	raw, present, err := section(cfg, "readiness")
	if err != nil || !present {
		return defaultsOf(readiness), err
	}
	if x := unknown(raw, readiness); x != "" {
		return nil, rej("readiness: unknown setting %s", x)
	}
	out := map[string]any{}
	for _, f := range readiness {
		v := getOr(raw, f.k, f.def)
		switch f.def.(type) {
		case bool:
			b, ok := v.(bool)
			if !ok {
				return nil, rej("readiness.%s must be true or false", f.k)
			}
			out[f.k] = b
		case int64:
			n, ok := whole(v)
			if d, isDef := v.(int64); isDef {
				n, ok = d, true
			}
			if !ok || n < f.lo || n > f.hi {
				return nil, rej("readiness.%s outside %d..%d", f.k, f.lo, f.hi)
			}
			out[f.k] = n
		default:
			s, ok := v.(string)
			if f.k == "event_date" {
				if !ok || !reDate.MatchString(s) {
					return nil, rej("readiness.event_date must be YYYY-MM-DD")
				}
				if _, err := time.Parse("2006-01-02", s); err != nil {
					return nil, rej("readiness.event_date is not a real date")
				}
			} else if !ok || !reName.MatchString(s) {
				return nil, rej("readiness.event_name: 1-40 letters, digits, spaces, dots, dashes")
			}
			out[f.k] = s
		}
	}
	return out, nil
}

// QAAsk validates "qa_ask"; missing means the defaults (asking on, not a dry run).
func QAAsk(cfg map[string]any) (map[string]any, error) {
	raw, present, err := section(cfg, "qa_ask")
	if err != nil || !present {
		return defaultsOf(qaAsk), err
	}
	if x := unknown(raw, qaAsk); x != "" {
		return nil, rej("qa_ask: unknown setting %s", x)
	}
	out := map[string]any{}
	for _, f := range qaAsk {
		b, ok := getOr(raw, f.k, f.def).(bool)
		if !ok {
			return nil, rej("qa_ask.%s must be true or false", f.k)
		}
		out[f.k] = b
	}
	return out, nil
}

// Claude validates "claude"; missing means on.
func Claude(cfg map[string]any) (map[string]any, error) {
	raw, present, err := section(cfg, "claude")
	if err != nil || !present {
		return map[string]any{"enabled": true}, err
	}
	if x := unknown(raw, []field{{k: "enabled"}}); x != "" {
		return nil, rej("claude: unknown setting %s", x)
	}
	b, ok := getOr(raw, "enabled", true).(bool)
	if !ok {
		return nil, rej("claude.enabled must be true or false")
	}
	return map[string]any{"enabled": b}, nil
}

func egressDefaults() map[string]any {
	return map[string]any{"guard": true, "blocked": []string{"IR"}, "fail": "closed"}
}

// Egress validates "claude_egress"; present is false when settings.json has none.
func Egress(cfg map[string]any) (out map[string]any, present bool, err error) {
	raw, present, err := section(cfg, "claude_egress")
	if err != nil || !present {
		return egressDefaults(), present, err
	}
	if x := unknown(raw, []field{{k: "guard"}, {k: "blocked"}, {k: "fail"}}); x != "" {
		return nil, true, rej("claude_egress: unknown setting %s", x)
	}
	guard, ok := getOr(raw, "guard", true).(bool)
	if !ok {
		return nil, true, rej("claude_egress.guard must be true or false")
	}
	bad := rej("claude_egress.blocked must be a list of two-letter country codes (at most 50)")
	var blocked []string
	switch l := getOr(raw, "blocked", nil).(type) {
	case nil:
		if _, has := raw["blocked"]; has {
			return nil, true, bad
		}
		blocked = []string{"IR"}
	case []any:
		if len(l) > 50 {
			return nil, true, bad
		}
		seen := map[string]bool{}
		for _, x := range l {
			s, ok := x.(string)
			if !ok || !reCC.MatchString(s) {
				return nil, true, bad
			}
			if !seen[s] {
				seen[s] = true
				blocked = append(blocked, s)
			}
		}
		sort.Strings(blocked)
	default:
		return nil, true, bad
	}
	if blocked == nil {
		blocked = []string{}
	}
	fail, _ := getOr(raw, "fail", "closed").(string)
	if fail != "closed" && fail != "open" {
		return nil, true, rej("claude_egress.fail must be closed or open")
	}
	return map[string]any{"guard": guard, "blocked": blocked, "fail": fail}, true, nil
}

func onCalendar(spec string) (string, error) {
	if c, ok := intervals[spec]; ok {
		return c, nil
	}
	if m := daily.FindStringSubmatch(spec); m != nil {
		return "*-*-* " + m[1] + ":" + m[2], nil
	}
	return "", rej("schedule %s not allowed", repr(spec))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func pyBool(v any) string {
	if b, ok := v.(bool); ok {
		return pyStr(b)
	}
	return fmt.Sprint(v)
}

// QASummary is the one-line state of the QA settings.
func QASummary(q map[string]any) string {
	s := "QA follow-up questions "
	switch {
	case q["enabled"] != true:
		s += "off"
	case q["dry_run"] == true:
		s += "on (dry run)"
	default:
		s += "on"
	}
	return s + ", read images " + onOff(q["read_images"] == true)
}

// ClaudeSummary is "Claude usage on|off".
func ClaudeSummary(c map[string]any) string { return "Claude usage " + onOff(c["enabled"] == true) }

func egressSummary(e map[string]any) string {
	if e["guard"] != true {
		return "Claude egress guard off"
	}
	b := strings.Join(e["blocked"].([]string), " ")
	if b == "" {
		b = "none"
	}
	return fmt.Sprintf("Claude egress guard on (blocked %s, fail %s)", b, e["fail"])
}

// plan is a validated settings.json.
type plan struct {
	user     string
	tasks    map[string][3]string // calendar, enabled ("1"/""), spec
	env      []string
	rca      map[string]any
	ready    map[string]any
	qa       map[string]any
	claude   map[string]any
	egress   map[string]any
	egressOn bool
}

func (o *Options) read() (map[string]any, error) {
	f, err := os.OpenFile(o.In, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > MaxFile {
		return nil, rej("settings.json is not a small regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFile {
		return nil, rej("settings.json is not a small regular file")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, rej("settings.json is not valid JSON: %v", err)
	}
	if d.More() {
		return nil, rej("settings.json is not valid JSON: extra data")
	}
	cfg, ok := v.(map[string]any)
	if !ok {
		return nil, rej("settings.json is not a JSON object")
	}
	return cfg, nil
}

func validate(cfg map[string]any) (*plan, error) {
	p := &plan{user: "?", tasks: map[string][3]string{}}
	if u, ok := cfg["updated_by"]; ok {
		p.user = head(pyStr(u), 64)
	}
	tasks, _ := cfg["tasks"].(map[string]any)
	for _, task := range Tasks {
		t, ok := tasks[task].(map[string]any)
		if !ok {
			return nil, rej("missing task %s", task)
		}
		spec := pyStr(t["schedule"])
		cal, err := onCalendar(spec)
		if err != nil {
			return nil, err
		}
		p.tasks[task] = [3]string{cal, map[bool]string{true: "1"}[truthy(t["enabled"])], spec}
	}
	th, ok := cfg["thresholds"].(map[string]any)
	if !ok {
		return nil, rej("thresholds must be an object")
	}
	vals := map[string]int64{}
	for _, t := range thresholds {
		v, ok := pyInt(th[t.name])
		if !ok {
			return nil, rej("%s must be a whole number", t.name)
		}
		if v < t.lo || v > t.hi {
			return nil, rej("%s=%d outside %d..%d", t.name, v, t.lo, t.hi)
		}
		vals[t.name] = v
		p.env = append(p.env, fmt.Sprintf("%s=%d", t.env, v*t.mult))
	}
	if vals["cert_fail_days"] >= vals["cert_warn_days"] {
		return nil, rej("cert_fail_days must be below cert_warn_days")
	}
	var err error
	if p.rca, err = RCAAuto(cfg); err != nil {
		return nil, err
	}
	if p.ready, err = Readiness(cfg); err != nil {
		return nil, err
	}
	if p.qa, err = QAAsk(cfg); err != nil {
		return nil, err
	}
	if p.claude, err = Claude(cfg); err != nil {
		return nil, err
	}
	if p.egress, p.egressOn, err = Egress(cfg); err != nil {
		return nil, err
	}
	return p, nil
}

// truthy is Python's bool() of a decoded JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// WriteFile replaces path atomically (tmp file next to it, never through a symlink), mode 0644.
func WriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if e := f.Chmod(0o644); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readOr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (o *Options) audit(text string) error {
	f, err := os.OpenFile(filepath.Join(o.OutDir, "audit.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", o.Now().Format("2006-01-02 15:04:05"), text)
	return err
}

func (o *Options) systemctl(args ...string) (string, error) {
	if o.Systemctl != nil {
		return o.Systemctl(args...)
	}
	return runSystemctl(args...)
}

// replaceIf writes text to path when it differs; true when it did.
func replaceIf(path string, text []byte) (bool, error) {
	if readOr(path) == string(text) {
		return false, nil
	}
	return true, WriteFile(path, text)
}

// Apply validates settings.json and applies it, or rejects it whole (exit status 1, applied.json says why).
// An error other than a rejection means a write or systemctl failed part way (exit status 1, applied.json kept).
func Apply(o Options) error {
	o.fill()
	cfg, err := o.read()
	var p *plan
	if err == nil {
		p, err = validate(cfg)
	}
	if err != nil {
		msg := head(err.Error(), 300)
		res := object{{"ok", false}, {"error", msg}, {"at", o.Now().Unix()}}
		if e := WriteFile(filepath.Join(o.OutDir, "applied.json"), dumps(res)); e != nil {
			return e
		}
		o.audit("REJECTED " + msg)
		fmt.Fprintln(o.Err, "rejected:", msg)
		return rejected(msg)
	}
	return o.apply(p)
}

func (o *Options) apply(p *plan) error {
	changes := []string{}
	timers := o.TimerPrefix != ""
	if timers {
		for _, task := range Tasks {
			t := p.tasks[task]
			d := filepath.Join(o.SystemdDir, o.TimerPrefix+task+".timer.d")
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			conf := fmt.Sprintf("# managed by argus settings apply (%s)\n[Timer]\nOnCalendar=\nOnCalendar=%s\n", t[2], t[0])
			if ch, err := replaceIf(filepath.Join(d, "schedule.conf"), []byte(conf)); err != nil {
				return err
			} else if ch {
				changes = append(changes, task+" schedule="+t[2])
			}
		}
	}
	if err := WriteFile(filepath.Join(o.EtcDir, "thresholds.env"), []byte(strings.Join(p.env, "\n")+"\n")); err != nil {
		return err
	}
	if ch, err := replaceIf(filepath.Join(o.EtcDir, "rca-auto.json"), line(p.rca)); err != nil {
		return err
	} else if ch {
		var kvs []string
		for _, x := range sorted(p.rca) {
			if x.k != "enabled" {
				kvs = append(kvs, x.k+"="+pyBool(x.v))
			}
		}
		changes = append(changes, "auto deep RCA "+onOff(p.rca["enabled"] == true)+" ("+strings.Join(kvs, ", ")+")")
	}
	if ch, err := replaceIf(filepath.Join(o.EtcDir, "readiness.json"), line(p.ready)); err != nil {
		return err
	} else if ch {
		r := p.ready
		changes = append(changes, fmt.Sprintf("hub raise %s, %s %s, freeze D-%d, brief D-%d..D+%d", onOff(r["hub_raise"] == true),
			head(r["event_name"].(string), 20), r["event_date"], r["freeze_days"], r["before_days"], r["after_days"]))
	}
	qa := line(p.qa)
	if ch, err := replaceIf(filepath.Join(o.EtcDir, "qa-ask.json"), qa); err != nil {
		return err
	} else if ch {
		changes = append(changes, QASummary(p.qa))
	}
	if err := WriteFile(filepath.Join(o.OutDir, "qa-ask.json"), qa); err != nil {
		return err
	}
	cl := line(p.claude)
	cpath := filepath.Join(o.EtcDir, "claude.json")
	old := readOr(cpath)
	if old != string(cl) {
		if err := WriteFile(cpath, cl); err != nil {
			return err
		}
		if old != "" || p.claude["enabled"] != true { // the first write of the default (on) is not a change
			changes = append(changes, ClaudeSummary(p.claude))
		}
	}
	if err := WriteFile(filepath.Join(o.OutDir, "claude.json"), cl); err != nil {
		return err
	}
	if p.egressOn {
		eg := line(p.egress)
		old := readOr(o.EgressFile)
		if old != string(eg) {
			if err := WriteFile(o.EgressFile, eg); err != nil {
				return err
			}
			if old != "" || string(eg) != string(line(egressDefaults())) {
				changes = append(changes, egressSummary(p.egress))
			}
		}
		if err := WriteFile(filepath.Join(o.OutDir, "claude-egress.json"), eg); err != nil {
			return err
		}
	}
	next := object{}
	if timers {
		if _, err := o.systemctl("daemon-reload"); err != nil {
			return err
		}
		for _, task := range Tasks {
			unit := o.TimerPrefix + task + ".timer"
			_, e := o.systemctl("is-enabled", "--quiet", unit)
			active, enabled := e == nil, p.tasks[task][1] == "1"
			switch {
			case enabled && !active:
				if _, err := o.systemctl("enable", "--now", unit); err != nil {
					return err
				}
				changes = append(changes, task+" enabled")
			case !enabled && active:
				if _, err := o.systemctl("disable", "--now", unit); err != nil {
					return err
				}
				changes = append(changes, task+" disabled")
			case enabled:
				if _, err := o.systemctl("restart", unit); err != nil { // pick up a new OnCalendar
					return err
				}
			}
		}
		for _, task := range Tasks {
			out, _ := o.systemctl("show", o.TimerPrefix+task+".timer", "-p", "NextElapseUSecRealtime", "--value")
			if out = strings.TrimSpace(out); out == "" {
				out = "-"
			}
			next = append(next, kv{task, out})
		}
	}
	res := object{{"ok", true}, {"at", o.Now().Unix()}, {"by", p.user}, {"changes", changes}, {"next", next},
		{"thresholds", p.env}}
	if err := WriteFile(filepath.Join(o.OutDir, "applied.json"), dumps(res)); err != nil {
		return err
	}
	summary := strings.Join(changes, ", ")
	if summary == "" {
		summary = "no change"
	}
	if !timers {
		summary += " (timers not managed)"
	}
	o.audit(p.user + ": " + summary + "; " + strings.Join(p.env, " "))
	fmt.Fprintln(o.Log, "applied:", summary)
	return nil
}

// IsRejected reports whether err is a rejected save (applied.json says why) rather than a failed write.
func IsRejected(err error) bool {
	var r rejected
	return errors.As(err, &r)
}

// QACLI words: on, off, dry-run, send, images-on, images-off.
var QACLI = map[string]kv{"on": {"enabled", true}, "off": {"enabled", false}, "dry-run": {"dry_run", true},
	"send": {"dry_run", false}, "images-on": {"read_images", true}, "images-off": {"read_images", false}}

// QAWords is the CLI order of QACLI.
var QAWords = []string{"on", "off", "dry-run", "send", "images-on", "images-off"}

// QA is `argus settings qa [words]`: print or change the live QA settings as root. The exit status.
func QA(o Options, words []string) int {
	o.fill()
	const user = "root (cli)"
	path := filepath.Join(o.EtcDir, "qa-ask.json")
	cur := defaultsOf(qaAsk)
	if b, err := os.ReadFile(path); err == nil {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		var m map[string]any
		if d.Decode(&m) == nil {
			kept := map[string]any{}
			for _, f := range qaAsk {
				if v, ok := m[f.k]; ok {
					kept[f.k] = v
				}
			}
			if q, err := QAAsk(map[string]any{"qa_ask": kept}); err == nil {
				cur = q
			}
		}
	}
	for _, w := range words {
		if _, ok := QACLI[w]; !ok {
			fmt.Fprintf(o.Err, "unknown: %s; use one of %s\n", head(w, 40), strings.Join(QAWords, ", "))
			return 2
		}
	}
	if len(words) == 0 {
		fmt.Fprintln(o.Log, QASummary(cur))
		return 0
	}
	nw := map[string]any{}
	for k, v := range cur {
		nw[k] = v
	}
	for _, w := range words {
		nw[QACLI[w].k] = QACLI[w].v
	}
	text, same := line(nw), string(line(nw)) == string(line(cur))
	if !same {
		if err := WriteFile(path, text); err != nil {
			fmt.Fprintln(o.Err, err)
			return 1
		}
		o.audit(user + ": " + QASummary(nw))
	}
	if err := WriteFile(filepath.Join(o.OutDir, "qa-ask.json"), text); err != nil {
		fmt.Fprintln(o.Err, err)
		return 1
	}
	fmt.Fprintln(o.Log, QASummary(nw)+map[bool]string{true: " (no change)"}[same])
	return 0
}

// ClaudeLive is the switch as the Claude runs read it: missing = on; anything but {"enabled": true} = off.
func ClaudeLive(etcDir string) map[string]any {
	b, err := os.ReadFile(filepath.Join(etcDir, "claude.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{"enabled": true}
	}
	var m map[string]any
	if err != nil || json.Unmarshal(b, &m) != nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{"enabled": m["enabled"] == true}
}

// ClaudeCLI is `argus settings claude [on|off]`: print or switch Claude usage as root. The exit status.
func ClaudeCLI(o Options, words []string) int {
	o.fill()
	const user = "root (cli)"
	if len(words) > 1 || (len(words) == 1 && words[0] != "on" && words[0] != "off") {
		fmt.Fprintln(o.Err, "usage: argus settings claude [on|off]")
		return 2
	}
	cur := ClaudeLive(o.EtcDir)
	if len(words) == 0 {
		fmt.Fprintln(o.Log, ClaudeSummary(cur))
		return 0
	}
	nw := map[string]any{"enabled": words[0] == "on"}
	path := filepath.Join(o.EtcDir, "claude.json")
	_, statErr := os.Stat(path)
	same := nw["enabled"] == cur["enabled"]
	if !same || statErr != nil {
		if err := WriteFile(path, line(nw)); err != nil {
			fmt.Fprintln(o.Err, err)
			return 1
		}
		o.audit(user + ": " + ClaudeSummary(nw))
	}
	if err := WriteFile(filepath.Join(o.OutDir, "claude.json"), line(nw)); err != nil {
		fmt.Fprintln(o.Err, err)
		return 1
	}
	fmt.Fprintln(o.Log, ClaudeSummary(nw)+map[bool]string{true: " (no change)"}[same])
	return 0
}

func runSystemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).Output()
	return string(out), err
}
