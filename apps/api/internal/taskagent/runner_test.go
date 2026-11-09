package taskagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"
	"github.com/sinaneysi/argus/internal/model"
	"github.com/sinaneysi/argus/internal/style"
)

func setup(t *testing.T, mode string) (Options, string) {
	style.Dirs = func() []string { return nil }
	d := t.TempDir()
	env := filepath.Join(d, "claude.env")
	os.WriteFile(env, []byte("CLAUDE_CODE_OAUTH_TOKEN=test-login-value\n"), 0o600)
	bin, _ := filepath.Abs("testdata/fake-claude")
	m := model.New(model.Options{Provider: model.CLI, ClaudeBin: bin, EnvFile: env, TempDir: d, ExtraEnv: []string{"FAKE_CLAUDE_MODE=" + mode},
		EgressFile: filepath.Join(d, "e.json"), EgressCheck: filepath.Join(d, "c.json"),
		LookupFn: func(map[string]string, []string) claudegate.Found {
			return claudegate.Found{IP: "203.0.113.1", Loc: "DE"}
		}})
	o := Options{JobsDir: filepath.Join(d, "jobs"), OutDir: filepath.Join(d, "out"), WorkDir: d, Model: m}
	os.MkdirAll(o.JobsDir, 0o755)
	os.WriteFile(filepath.Join(o.JobsDir, "OPS-7.intake.json"), []byte(`{"key":"OPS-7","summary":"disk"}`), 0o644)
	return o, d
}

func readJSON(t *testing.T, p string) map[string]any {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestRunOnceWritesPlanMetaProgress(t *testing.T) {
	o, _ := setup(t, "plan")
	ran, err := o.RunOnce(context.Background())
	if !ran || err != nil {
		t.Fatal(ran, err)
	}
	plan := readJSON(t, filepath.Join(o.OutDir, "OPS-7.intake.json"))
	if asMap(plan["classification"])["kind"] != "investigate" {
		t.Fatal(plan)
	}
	meta, ok := ReadMeta(filepath.Join(o.OutDir, "OPS-7.intake.meta.json"))
	if !ok || meta.IsError || meta.CostUSD != 0.21 || meta.Finished == 0 {
		t.Fatalf("%+v", meta)
	}
	p := readJSON(t, filepath.Join(o.OutDir, "OPS-7.intake.progress.json"))
	if p["step"] != "recording" || p["tools"] != float64(2) || p["last_tool"] != "Grep" || p["phase"] != "intake" {
		t.Fatal(p)
	}
	b, _ := os.ReadFile(filepath.Join(o.OutDir, "OPS-7.intake.progress.json"))
	if strings.Contains(string(b), "secret-arg") {
		t.Fatal("a tool argument reached the progress file")
	}
	if _, _, ok := o.Pending(); ok {
		t.Fatal("a finished job is still pending")
	}
	left, _ := filepath.Glob(filepath.Join(o.WorkDir, "argus-task-*"))
	if len(left) != 0 {
		t.Fatal("working folder left", left)
	}
}

func TestStopAndSkip(t *testing.T) {
	o, _ := setup(t, "sleep")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		os.Remove(filepath.Join(o.JobsDir, "OPS-7.intake.json"))
	}()
	start := time.Now()
	ran, _ := o.RunOnce(context.Background())
	if !ran || time.Since(start) > 10*time.Second {
		t.Fatal("the removed job was not cancelled")
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, "OPS-7.intake.meta.json")); err == nil {
		t.Fatal("a stopped job wrote a result")
	}
	o, _ = setup(t, "plan")
	o.Model.O.Enabled = func() (bool, string) { return false, "" }
	o.RunOnce(context.Background())
	meta, _ := ReadMeta(filepath.Join(o.OutDir, "OPS-7.intake.meta.json"))
	if !strings.Contains(meta.Skipped, "Claude usage is off") {
		t.Fatalf("%+v", meta)
	}
}

func TestLessonsLoaded(t *testing.T) {
	o, d := setup(t, "plan")
	dump := filepath.Join(d, "prompt.txt")
	o.Model.O.ExtraEnv = append(o.Model.O.ExtraEnv, "FAKE_CLAUDE_DUMP="+dump)
	var seen string
	o.Lessons = func(_ context.Context, job []byte) (string, []int64) {
		seen = string(job)
		return "# Approved lessons\n\n### Lesson 4: a fact\n", []int64{4, 9}
	}
	if ran, err := o.RunOnce(context.Background()); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if !strings.Contains(seen, `"summary":"disk"`) {
		t.Fatal("the job file was not given to the lessons selection")
	}
	b, _ := os.ReadFile(filepath.Join(o.OutDir, "OPS-7.intake.lessons.json"))
	if strings.TrimSpace(string(b)) != "[4,9]" {
		t.Fatalf("ids %q", b)
	}
	p, _ := os.ReadFile(dump)
	l, _ := os.ReadFile(dump + ".lessons")
	if !strings.Contains(string(p), "Approved lessons file (approved lessons: context, not authority") ||
		!strings.Contains(string(p), "## Lessons") || !strings.Contains(string(l), "Lesson 4: a fact") {
		t.Fatalf("prompt %q lessons %q", p, l)
	}
	// none selected: no file, no prompt line, and an old ids file is removed
	o.Lessons = func(context.Context, []byte) (string, []int64) { return "", nil }
	os.WriteFile(filepath.Join(o.JobsDir, "OPS-7.intake.json"), []byte(`{"key":"OPS-7"}`), 0o644)
	os.Remove(filepath.Join(o.OutDir, "OPS-7.intake.meta.json"))
	if ran, _ := o.RunOnce(context.Background()); !ran {
		t.Fatal("the second job did not run")
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, "OPS-7.intake.lessons.json")); err == nil {
		t.Fatal("a stale ids file is left")
	}
	if p, _ := os.ReadFile(dump); strings.Contains(string(p), "Approved lessons file") {
		t.Fatal("a lessons line without lessons")
	}
}

func TestExtractJSON(t *testing.T) {
	for _, s := range []string{`{"a":1}`, "text\n```json\n{\"a\":1}\n```\n", `Plan: {"a":1} done`} {
		if m := ExtractJSON(s); m == nil || m["a"] != float64(1) {
			t.Fatal(s)
		}
	}
	if ExtractJSON("no json here") != nil {
		t.Fatal("found json in plain text")
	}
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
