package settingsapply

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (e *env) styleReq(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(e.o.StyleRequestPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) styleStatus(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, StyleStatus))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err, string(b))
	}
	return m
}

func TestStyleAppliedAndReset(t *testing.T) {
	e := newEnv(t)
	if present, err := ApplyStyle(e.o); present || err != nil {
		t.Fatal(present, err)
	}
	var notes []string
	e.o.Notify = func(by string, ch []string) { notes = append(notes, by+": "+strings.Join(ch, ",")) }
	e.styleReq(t, map[string]any{"text": "Write short.\r\n", "reset": false, "updated_by": "sam.roe", "updated_at": 1_800_000_000})
	if present, err := ApplyStyle(e.o); !present || err != nil {
		t.Fatal(present, err)
	}
	for _, p := range []string{filepath.Join(e.o.EtcDir, "writing-style.md"), filepath.Join(e.o.OutDir, "writing-style.md")} {
		if b, _ := os.ReadFile(p); string(b) != "Write short.\n" {
			t.Fatalf("%s: %q", p, b)
		}
	}
	st := e.styleStatus(t)
	if st["ok"] != true || st["custom"] != true || st["by"] != "sam.roe" || st["request_at"] != float64(1_800_000_000) {
		t.Fatal(st)
	}
	ApplyStyle(e.o) // the same request again: no second notice
	if len(notes) != 1 || !strings.Contains(notes[0], "Writing style saved") {
		t.Fatal(notes)
	}
	e.styleReq(t, map[string]any{"text": "", "reset": true, "updated_by": "sam.roe", "updated_at": 1_800_000_100})
	if _, err := ApplyStyle(e.o); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(e.o.EtcDir, "writing-style.md"), filepath.Join(e.o.OutDir, "writing-style.md")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("not removed", p)
		}
	}
	if st := e.styleStatus(t); st["custom"] != false {
		t.Fatal(st)
	}
	audit, _ := os.ReadFile(filepath.Join(e.o.OutDir, "audit.log"))
	if !strings.Contains(string(audit), "Writing style reset to the default by sam.roe") {
		t.Fatal(string(audit))
	}
}

func TestStyleRejectedKeepsLive(t *testing.T) {
	e := newEnv(t)
	e.styleReq(t, map[string]any{"text": "Keep me.", "updated_by": "sam.roe", "updated_at": 1})
	if _, err := ApplyStyle(e.o); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{
		map[string]any{"text": strings.Repeat("a", 16385), "updated_at": 2},
		map[string]any{"text": "a\x00b", "updated_at": 2},
		map[string]any{"text": 5},
		map[string]any{"text": "x", "other": 1},
		[]any{"not", "an object"},
	} {
		e.styleReq(t, bad)
		if _, err := ApplyStyle(e.o); !IsRejected(err) {
			t.Fatal("accepted", bad, err)
		}
		if b, _ := os.ReadFile(filepath.Join(e.o.EtcDir, "writing-style.md")); string(b) != "Keep me.\n" {
			t.Fatalf("live changed: %q", b)
		}
		if st := e.styleStatus(t); st["ok"] != false || st["error"] == "" {
			t.Fatal(st)
		}
	}
	// a symlinked request is never followed
	real := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(real, []byte(`{"text":"elsewhere"}`), 0o644)
	os.Remove(e.o.StyleRequestPath())
	os.Symlink(real, e.o.StyleRequestPath())
	if _, err := ApplyStyle(e.o); err == nil {
		t.Fatal("followed a symlink")
	}
}
