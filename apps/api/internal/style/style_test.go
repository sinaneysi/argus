package style

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func use(t *testing.T, dirs ...string) {
	t.Helper()
	old := Dirs
	Dirs = func() []string { return dirs }
	t.Cleanup(func() { Dirs = old })
}

func TestDefaultWhenNothingSaved(t *testing.T) {
	use(t, t.TempDir(), filepath.Join(t.TempDir(), "missing"))
	if Current() != Default() {
		t.Fatal("not the default")
	}
	for _, w := range []string{"ASD-STE100", "one idea", "20 words", "25 words", "active voice", "imperative", "Reason:",
		"Warnings", "Write in English", "Never write a secret value", "exactly", "noun clusters"} {
		if !strings.Contains(Default(), w) {
			t.Fatal("default lacks", w)
		}
	}
	if len(Default()) > MaxBytes || Validate(Default()) != nil {
		t.Fatal("default does not pass its own check")
	}
}

func TestCustomWins(t *testing.T) {
	etc, out := t.TempDir(), t.TempDir()
	use(t, etc, out)
	if err := os.WriteFile(filepath.Join(out, FileName), []byte("Live copy.\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Current() != "Live copy.\n" {
		t.Fatalf("%q", Current())
	}
	if err := os.WriteFile(filepath.Join(etc, FileName), []byte("Write short.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Current() != "Write short.\n" {
		t.Fatalf("%q", Current())
	}
	p := Prepend("Summarise the incident.")
	if !strings.HasPrefix(p, "<writing-style>\n") || !strings.Contains(p, "Write short.\n</writing-style>") || !strings.HasSuffix(p, "\n\nSummarise the incident.") {
		t.Fatalf("%q", p)
	}
}

func TestOversizeOrBinaryFileIgnored(t *testing.T) {
	etc := t.TempDir()
	use(t, etc)
	for _, b := range [][]byte{[]byte(strings.Repeat("a", MaxBytes+1)), []byte("bad\x00text"), {0xff, 0xfe}, []byte("  \n")} {
		if err := os.WriteFile(filepath.Join(etc, FileName), b, 0o644); err != nil {
			t.Fatal(err)
		}
		if Current() != Default() {
			t.Fatalf("used %q", b[:min(len(b), 10)])
		}
	}
	// a symlink is never followed
	real := filepath.Join(t.TempDir(), "x.md")
	os.WriteFile(real, []byte("elsewhere"), 0o644)
	os.Remove(filepath.Join(etc, FileName))
	if err := os.Symlink(real, filepath.Join(etc, FileName)); err != nil {
		t.Fatal(err)
	}
	if Current() != Default() {
		t.Fatal("followed a symlink")
	}
}

func TestValidate(t *testing.T) {
	if Validate(strings.Repeat("a", MaxBytes)) != nil || Validate("") != nil || Validate("tab\there\nä ✓") != nil {
		t.Fatal("refused a valid text")
	}
	for _, s := range []string{strings.Repeat("a", MaxBytes+1), "a\x00b", "a\x1bb", string([]byte{0xff})} {
		if Validate(s) == nil {
			t.Fatalf("accepted %q", s[:min(len(s), 10)])
		}
	}
	if Normalize("\n\nA\r\nB  \n\n") != "A\nB\n" || Normalize(" \n") != "" {
		t.Fatal("normalize")
	}
}
