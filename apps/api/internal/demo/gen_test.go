package demo

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testNow = 1793318400 // 2026-10-30T00:00:00Z

func generate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := Generate(filepath.Join(dir, "data"), filepath.Join(dir, "auth"), testNow, DefaultSeed); err != nil {
		t.Fatal(err)
	}
	return dir
}

// textDigest hashes every JSON and Markdown file (path and bytes), in path order.
func textDigest(t *testing.T, root string) string {
	h := sha256.New()
	for _, rel := range listFiles(root) {
		if strings.HasSuffix(rel, ".json") || strings.HasSuffix(rel, ".md") {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			h.Write([]byte(rel + "\x00"))
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// goldenDigest pins the generated data (TZ=UTC). It matched the Python generator byte for byte until IA v2 dropped the
// SEO and design-review data and added the Review inbox data (ADR-0016); TestParityWithPython is history since then. A change here means the
// demo data changed: check it on purpose, then update the value.
const goldenDigest = "c432c25dbd370051" // + the Outline connection and three built pages

func TestGenerateIsDeterministicAndComplete(t *testing.T) {
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC
	a, b := generate(t), generate(t)
	da, db := textDigest(t, a), textDigest(t, b)
	if da != db {
		t.Fatalf("two runs differ: %s %s", da, db)
	}
	if da != goldenDigest {
		t.Errorf("demo data changed: digest %s, golden %s", da, goldenDigest)
	}
	t.Logf("digest %s", da)
	if !Ready(filepath.Join(a, "data"), filepath.Join(a, "auth")) {
		t.Fatal("Ready must be true after Generate")
	}
	for _, rel := range []string{"data/ops.db", "data/board/board.db", "auth/auth.db", "data/vm/41.json", "data/capacity.json",
		"data/cicd-status.json", "data/alerts-firing.json", "data/settings-out/board-status.json", "data/settings-in/settings.json",
		"data/backup-plans/ref/backup-templates/catalog.json", "data/reports/rca/2026-10-27-search-latency-reindex.md"} {
		if _, err := os.Stat(filepath.Join(a, rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	if st, err := os.Stat(filepath.Join(a, "data/settings-in/fix-requests")); err != nil || !st.IsDir() {
		t.Error("settings-in/fix-requests must exist (empty)")
	}
	ents, _ := os.ReadDir(filepath.Join(a, "data"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".next-") || strings.HasPrefix(e.Name(), ".auth-") {
			t.Errorf("temporary directory left behind: %s", e.Name())
		}
	}
	ops, err := sql.Open("sqlite", filepath.Join(a, "data/ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	for table, want := range map[string]int{"vms": 41, "incidents": 16, "rca_reports": 11, "cicd_projects": 32, "cve_findings": 11,
		"qa_issues": 13, "problems": 12, "ideas": 33} {
		var n int
		if err := ops.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Errorf("%s: %d rows (%v), want %d", table, n, err, want)
		}
	}
}

func TestAuthDBSurvivesRegeneration(t *testing.T) {
	dir := generate(t)
	authDB := filepath.Join(dir, "auth", "auth.db")
	db, err := sql.Open("sqlite", authDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sessions VALUES ('h', 'demo.viewer', 1, 1, 'c', NULL)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := Generate(filepath.Join(dir, "data"), filepath.Join(dir, "auth"), testNow+3600, DefaultSeed); err != nil {
		t.Fatal(err)
	}
	db, _ = sql.Open("sqlite", authDB)
	defer db.Close()
	var n int
	db.QueryRow("SELECT count(*) FROM sessions").Scan(&n)
	if n != 1 {
		t.Fatalf("the session must survive a regeneration, got %d", n)
	}
	var hash string
	db.QueryRow("SELECT hash FROM users WHERE username='demo.admin'").Scan(&hash)
	if !strings.HasPrefix(hash, "scrypt$16384$8$1$") {
		t.Fatalf("hash %q", hash)
	}
}

func TestRefreshLive(t *testing.T) {
	dir := generate(t)
	data := filepath.Join(dir, "data")
	if err := RefreshLive(data, testNow+600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(data, "cicd-status.json"))
	if !strings.Contains(string(b), `"at": 1793318880`) { // now + 600 - 120
		t.Fatalf("watch.at not refreshed:\n%s", b)
	}
	b, _ = os.ReadFile(filepath.Join(data, "vm-pulse.json"))
	if !strings.Contains(string(b), `"at": 1793318940`) {
		t.Fatalf("vm-pulse not refreshed:\n%s", b)
	}
}

func TestDumpsMatchesPython(t *testing.T) {
	v := om("a", 1, "b", 2.0, "c", L("x", nil, true), "d", "é — \"q\"\n", "e", om(), "f", L(), "g", 6.1e6, "h", 1e16)
	if got, want := jd(v), `{"a": 1, "b": 2.0, "c": ["x", null, true], "d": "é — \"q\"\n", "e": {}, "f": [], "g": 6100000.0, "h": 1e+16}`; got != want {
		t.Fatalf("jd:\n got %s\nwant %s", got, want)
	}
	if got, want := dumps(om("a", L(1, 2)), 1), "{\n \"a\": [\n  1,\n  2\n ]\n}"; got != want {
		t.Fatalf("indent:\n%s", got)
	}
	if round(2.675, 2) != 2.67 || round(0.125, 2) != 0.12 || roundi(2.5) != 2 || roundi(-10737418.24) != -10737418 {
		t.Fatal("round must follow Python")
	}
}
