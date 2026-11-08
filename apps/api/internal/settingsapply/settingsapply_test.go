package settingsapply

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type env struct {
	o     Options
	calls []string
	in    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := t.TempDir()
	for _, s := range []string{"in", "out", "etc", "systemd"} {
		if err := os.MkdirAll(filepath.Join(d, s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{in: filepath.Join(d, "in", "settings.json")}
	e.o = Options{In: e.in, OutDir: filepath.Join(d, "out"), EtcDir: filepath.Join(d, "etc"),
		SystemdDir: filepath.Join(d, "systemd"), Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
	e.o.Systemctl = func(args ...string) (string, error) {
		e.calls = append(e.calls, strings.Join(args, " "))
		if args[0] == "is-enabled" {
			return "", errors.New("disabled")
		}
		if args[0] == "show" {
			return "Sat 2027-01-16 10:00:00 UTC\n", nil
		}
		return "", nil
	}
	return e
}

func valid() map[string]any {
	return map[string]any{
		"tasks": map[string]any{
			"chat-triage":  map[string]any{"schedule": "30m", "enabled": true},
			"log-rca":      map[string]any{"schedule": "4h", "enabled": false},
			"backup-check": map[string]any{"schedule": "daily@07:00", "enabled": true},
			"cert-check":   map[string]any{"schedule": "daily@07:30", "enabled": false},
		},
		"thresholds":  map[string]any{"kuma_min_down_minutes": 5, "kuma_flap_count": 3, "cert_warn_days": 30, "cert_fail_days": 14},
		"rca_auto":    map[string]any{"enabled": true, "open_hours": 4},
		"qa_ask":      map[string]any{"enabled": true, "dry_run": true, "read_images": false},
		"claude":      map[string]any{"enabled": true},
		"auth":        map[string]any{"sso_only": false},
		"updated_by":  "sam.roe",
		"updated_at":  1_800_000_000,
		"unknown_top": "ignored like the Python job",
	}
}

func (e *env) save(t *testing.T, cfg map[string]any) {
	t.Helper()
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(e.in, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestValidSaveApplied(t *testing.T) {
	e := newEnv(t)
	e.save(t, valid())
	notices := 0
	e.o.Notify = func(by string, changes []string) {
		notices++
		if by != "sam.roe" || len(changes) == 0 {
			t.Fatalf("notice %q %v", by, changes)
		}
	}
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json"))
	if a["ok"] != true || a["by"] != "sam.roe" || a["at"] != float64(1_800_000_000) {
		t.Fatalf("applied.json = %v", a)
	}
	th := a["thresholds"].([]any)
	if len(th) != 4 || th[0] != "KUMA_MIN_DOWN_SECONDS=300" {
		t.Fatalf("thresholds = %v", th)
	}
	if got := read(t, filepath.Join(e.o.EtcDir, "thresholds.env")); got != "KUMA_MIN_DOWN_SECONDS=300\nKUMA_FLAP_COUNT=3\nCERT_WARN_DAYS=30\nCERT_FAIL_DAYS=14\n" {
		t.Fatalf("thresholds.env = %q", got)
	}
	ra := readJSON(t, filepath.Join(e.o.EtcDir, "rca-auto.json"))
	if ra["open_hours"] != float64(4) || ra["daily_runs"] != float64(6) {
		t.Fatalf("rca-auto.json = %v", ra)
	}
	if got := read(t, filepath.Join(e.o.OutDir, "qa-ask.json")); got != `{"dry_run": true, "enabled": true, "read_images": false}`+"\n" {
		t.Fatalf("qa-ask.json = %q (Python json.dumps format expected)", got)
	}
	rd := readJSON(t, filepath.Join(e.o.EtcDir, "readiness.json"))
	if rd["event_date"] != "2026-11-27" {
		t.Fatalf("readiness.json = %v", rd)
	}
	ch := a["changes"].([]any)
	joined := ""
	for _, c := range ch {
		joined += c.(string) + "|"
	}
	if !strings.Contains(joined, "auto deep RCA on (daily_runs=6, daily_usd=20, open_hours=4, quiet_end=8, quiet_start=23, repeat_count=3, trigger_open=True") ||
		!strings.Contains(joined, "QA follow-up questions on (dry run), read images off") || strings.Contains(joined, "Claude usage") {
		t.Fatalf("changes = %v", ch)
	}
	if _, err := os.Stat(filepath.Join(e.o.OutDir, "claude-egress.json")); !os.IsNotExist(err) {
		t.Fatal("claude-egress.json written without a claude_egress section")
	}
	log := read(t, filepath.Join(e.o.OutDir, "audit.log"))
	if !strings.Contains(log, " sam.roe: ") || !strings.HasSuffix(log, "; KUMA_MIN_DOWN_SECONDS=300 KUMA_FLAP_COUNT=3 CERT_WARN_DAYS=30 CERT_FAIL_DAYS=14\n") {
		t.Fatalf("audit.log = %q", log)
	}

	// the same save again changes nothing
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	if a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json")); len(a["changes"].([]any)) != 0 {
		t.Fatalf("second apply changes = %v", a["changes"])
	}
	if notices != 1 {
		t.Fatalf("want one settings notice (none for a save without changes), got %d", notices)
	}
}

func TestOutOfRangeRejectedWithReason(t *testing.T) {
	cases := map[string]func(map[string]any){
		"kuma_flap_count=50 outside 2..20": func(c map[string]any) { c["thresholds"].(map[string]any)["kuma_flap_count"] = 50 },
		"cert_fail_days must be below cert_warn_days": func(c map[string]any) {
			c["thresholds"].(map[string]any)["cert_fail_days"] = 30
		},
		"rca_auto.daily_usd outside 1..50":     func(c map[string]any) { c["rca_auto"] = map[string]any{"daily_usd": 99} },
		"rca_auto.open_hours outside 1..72":    func(c map[string]any) { c["rca_auto"] = map[string]any{"open_hours": 2.5} },
		"schedule '7m' not allowed":            func(c map[string]any) { c["tasks"].(map[string]any)["log-rca"] = map[string]any{"schedule": "7m"} },
		"missing task cert-check":              func(c map[string]any) { delete(c["tasks"].(map[string]any), "cert-check") },
		"qa_ask: unknown setting x":            func(c map[string]any) { c["qa_ask"] = map[string]any{"x": true} },
		"claude.enabled must be true or false": func(c map[string]any) { c["claude"] = map[string]any{"enabled": "yes"} },
		"readiness.event_date is not a real date": func(c map[string]any) {
			c["readiness"] = map[string]any{"event_date": "2026-02-31"}
		},
		"claude_egress.blocked must be a list": func(c map[string]any) {
			c["claude_egress"] = map[string]any{"blocked": []any{"iran"}}
		},
	}
	for want, mod := range cases {
		e := newEnv(t)
		cfg := valid()
		mod(cfg)
		e.save(t, cfg)
		err := Apply(e.o)
		if !IsRejected(err) {
			t.Fatalf("%s: err = %v, want a rejection", want, err)
		}
		a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json"))
		if a["ok"] != false || !strings.HasPrefix(a["error"].(string), want) {
			t.Fatalf("%s: applied.json = %v", want, a)
		}
		if _, err := os.Stat(filepath.Join(e.o.EtcDir, "thresholds.env")); !os.IsNotExist(err) {
			t.Fatalf("%s: a rejected save wrote thresholds.env", want)
		}
		if log := read(t, filepath.Join(e.o.OutDir, "audit.log")); !strings.Contains(log, " REJECTED "+want) {
			t.Fatalf("%s: audit.log = %q", want, log)
		}
	}
}

func TestSymlinkAndLargeFileRejected(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(t.TempDir(), "real.json")
	b, _ := json.Marshal(valid())
	os.WriteFile(target, b, 0o644)
	if err := os.Symlink(target, e.in); err != nil {
		t.Fatal(err)
	}
	if err := Apply(e.o); !IsRejected(err) && err == nil {
		t.Fatal("a symlinked settings.json was applied")
	}
	os.Remove(e.in)
	os.WriteFile(e.in, []byte(`{"pad": "`+strings.Repeat("x", MaxFile)+`"}`), 0o644)
	if err := Apply(e.o); !IsRejected(err) {
		t.Fatalf("large file: err = %v", err)
	}
}

func TestClaudeSwitchWritesClaudeJSON(t *testing.T) {
	e := newEnv(t)
	cfg := valid()
	e.save(t, cfg)
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(e.o.OutDir, "claude.json")); got != `{"enabled": true}`+"\n" {
		t.Fatalf("live claude.json = %q", got)
	}
	cfg["claude"] = map[string]any{"enabled": false}
	e.save(t, cfg)
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(e.o.EtcDir, "claude.json"), filepath.Join(e.o.OutDir, "claude.json")} {
		if got := read(t, p); got != `{"enabled": false}`+"\n" {
			t.Fatalf("%s = %q", p, got)
		}
	}
	a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json"))
	if ch := a["changes"].([]any); len(ch) != 1 || ch[0] != "Claude usage off" {
		t.Fatalf("changes = %v", ch)
	}
	if ClaudeLive(e.o.EtcDir)["enabled"] != false {
		t.Fatal("ClaudeLive should read off")
	}

	// the CLI switches it back on, with an audit line and the live copy
	if rc := ClaudeCLI(e.o, []string{"on"}); rc != 0 {
		t.Fatalf("claude on: %d", rc)
	}
	if got := read(t, filepath.Join(e.o.OutDir, "claude.json")); got != `{"enabled": true}`+"\n" {
		t.Fatalf("after CLI: %q", got)
	}
	if log := read(t, filepath.Join(e.o.OutDir, "audit.log")); !strings.Contains(log, "root (cli): Claude usage on") {
		t.Fatalf("audit.log = %q", log)
	}
	if rc := ClaudeCLI(e.o, []string{"maybe"}); rc != 2 {
		t.Fatalf("bad word: %d", rc)
	}
	// unreadable switch file = off, like the Claude gate
	os.WriteFile(filepath.Join(e.o.EtcDir, "claude.json"), []byte("{"), 0o644)
	if ClaudeLive(e.o.EtcDir)["enabled"] != false {
		t.Fatal("unreadable claude.json should read off")
	}
}

func TestEgressAppliedOnlyWhenPresent(t *testing.T) {
	e := newEnv(t)
	cfg := valid()
	cfg["claude_egress"] = map[string]any{"guard": true, "blocked": []any{"XX", "AA", "XX"}, "fail": "open"}
	e.save(t, cfg)
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	want := `{"blocked": ["AA", "XX"], "fail": "open", "guard": true}` + "\n"
	for _, p := range []string{filepath.Join(e.o.EtcDir, "claude-egress.json"), filepath.Join(e.o.OutDir, "claude-egress.json")} {
		if got := read(t, p); got != want {
			t.Fatalf("%s = %q", p, got)
		}
	}
}

func TestTimersSkippedWhenPrefixUnset(t *testing.T) {
	e := newEnv(t)
	e.save(t, valid())
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 0 {
		t.Fatalf("systemctl called without a timer prefix: %v", e.calls)
	}
	if ents, _ := os.ReadDir(e.o.SystemdDir); len(ents) != 0 {
		t.Fatalf("drop-ins written without a timer prefix: %v", ents)
	}
	a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json"))
	if len(a["next"].(map[string]any)) != 0 {
		t.Fatalf("next = %v", a["next"])
	}
	if log := read(t, filepath.Join(e.o.OutDir, "audit.log")); !strings.Contains(log, "(timers not managed)") {
		t.Fatalf("audit.log = %q", log)
	}
}

func TestTimersAppliedWithPrefix(t *testing.T) {
	e := newEnv(t)
	e.o.TimerPrefix = "argus-"
	e.save(t, valid())
	if err := Apply(e.o); err != nil {
		t.Fatal(err)
	}
	conf := read(t, filepath.Join(e.o.SystemdDir, "argus-backup-check.timer.d", "schedule.conf"))
	if conf != "# managed by argus settings apply (daily@07:00)\n[Timer]\nOnCalendar=\nOnCalendar=*-*-* 07:00\n" {
		t.Fatalf("schedule.conf = %q", conf)
	}
	calls := strings.Join(e.calls, "\n")
	for _, want := range []string{"daemon-reload", "enable --now argus-chat-triage.timer", "enable --now argus-backup-check.timer"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("systemctl calls %v lack %q", e.calls, want)
		}
	}
	if strings.Contains(calls, "argus-log-rca.timer\n") && strings.Contains(calls, "enable --now argus-log-rca") {
		t.Fatal("a disabled task was enabled")
	}
	a := readJSON(t, filepath.Join(e.o.OutDir, "applied.json"))
	if a["next"].(map[string]any)["log-rca"] != "Sat 2027-01-16 10:00:00 UTC" {
		t.Fatalf("next = %v", a["next"])
	}
}

func TestQACLI(t *testing.T) {
	e := newEnv(t)
	if rc := QA(e.o, []string{"off", "images-on"}); rc != 0 {
		t.Fatalf("qa: %d", rc)
	}
	if got := read(t, filepath.Join(e.o.OutDir, "qa-ask.json")); got != `{"dry_run": false, "enabled": false, "read_images": true}`+"\n" {
		t.Fatalf("qa-ask.json = %q", got)
	}
	if rc := QA(e.o, []string{"loud"}); rc != 2 {
		t.Fatalf("bad word: %d", rc)
	}
}

func TestDumpsMatchesPython(t *testing.T) {
	got := string(dumps(object{{"ok", false}, {"error", "café \"x\""}, {"n", []string{}}}))
	if got != `{"ok": false, "error": "caf\u00e9 \"x\"", "n": []}` {
		t.Fatalf("dumps = %s", got)
	}
}
