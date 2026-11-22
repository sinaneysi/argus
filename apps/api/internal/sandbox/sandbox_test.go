package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary is its own model-exec helper: with ARGUS_TEST_MODEL_EXEC=1 it runs Main instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("ARGUS_TEST_MODEL_EXEC") == "1" {
		os.Exit(Main(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}

func TestArgsAndCheck(t *testing.T) {
	r := Rules{Exec: []string{"/usr"}, Read: []string{"/etc/ssl", "/proc"}, Write: []string{"/tmp/h"}}
	if got := strings.Join(r.Args(), " "); got != "-x /usr -r /etc/ssl -r /proc -w /tmp/h" {
		t.Fatal(got)
	}
	for _, bad := range []Rules{{Read: []string{"/"}}, {Write: []string{"//"}}, {Exec: []string{"usr"}}} {
		if bad.check() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	var stderr bytes.Buffer
	if code := Main([]string{"-r", "/", "--", "/bin/true"}, &stderr); code != 2 {
		t.Fatal("a rule for / was not refused", code, stderr.String())
	}
	if code := Main(nil, &stderr); code != 2 {
		t.Fatal("no program: ", code)
	}
}

// helper runs `model-exec <args>` through the test binary and returns its output and exit code.
func helper(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "ARGUS_TEST_MODEL_EXEC=1")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestLandlockBlocksOutside(t *testing.T) {
	if ABI() < 1 {
		t.Skip("Landlock is not available on this kernel")
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	allowed, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(allowed, "task.json"), []byte("inside-ok"), 0o644)
	os.WriteFile(filepath.Join(outside, "master.key"), []byte("outside-secret"), 0o600)
	sys := []string{"-x", "/usr", "-x", "/bin", "-x", "/lib", "-x", "/lib64", "-r", "/etc/ld.so.cache", "-r", "/proc"}

	out, code := helper(t, append(sys, "-r", allowed, "--", cat, filepath.Join(allowed, "task.json"))...)
	if code != 0 || !strings.Contains(out, "inside-ok") {
		t.Fatalf("the allowed file was not readable: %d %q", code, out)
	}
	out, code = helper(t, append(sys, "-r", allowed, "--", cat, filepath.Join(outside, "master.key"))...)
	if code == 0 || strings.Contains(out, "outside-secret") {
		t.Fatalf("a file outside the rules was readable: %d %q", code, out)
	}
	// /proc is readable, but not another process's environment (Landlock's ptrace scope): this test's own.
	os.Setenv("ARGUS_TEST_PARENT_MARK", "parent-env-mark")
	out, code = helper(t, append(sys, "--", cat, fmt.Sprintf("/proc/%d/environ", os.Getpid()))...)
	if code == 0 || strings.Contains(out, "parent-env-mark") {
		t.Fatalf("the parent's environment was readable: %d %q", code, out)
	}
	// writing is allowed beneath -w only, and a -w path cannot execute
	w := t.TempDir()
	sh, _ := exec.LookPath("sh")
	out, code = helper(t, append(sys, "-w", w, "--", sh, "-c", "echo x > "+w+"/a && echo y > "+outside+"/b")...)
	if code == 0 {
		t.Fatalf("a write outside the rules succeeded: %q", out)
	}
	if _, err := os.Stat(filepath.Join(w, "a")); err != nil {
		t.Fatal("the write beneath -w failed:", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "b")); err == nil {
		t.Fatal("the file outside the rules was written")
	}
}
