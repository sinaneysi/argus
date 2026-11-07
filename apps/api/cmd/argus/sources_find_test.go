package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesFindCommand(t *testing.T) {
	dir := t.TempDir()
	audit := filepath.Join(dir, "find.log")
	base := "COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_ADMIN_INITIAL_PASSWORD=x\n" +
		"ARGUS_SETTINGS_IN=" + dir + "/in\nARGUS_SETTINGS_OUT=" + dir + "/out\nARGUS_ETC_DIR=" + dir + "/etc\n" +
		"ARGUS_DB=" + dir + "/ops.db\nARGUS_AUTH_DB=" + dir + "/auth.db\nARGUS_SOURCES_FIND_AUDIT_LOG=" + audit + "\n"
	cfg := filepath.Join(dir, "argus.env")
	os.WriteFile(cfg, []byte(base), 0o600)
	t.Setenv("ARGUS_CONFIG", cfg)
	for _, a := range [][]string{{"find"}, {"find", ""}, {"find", "x\x01"}, {"find", "payment", "--type", "ftp"},
		{"find", "payment", "--bogus"}, {"find", strings.Repeat("a", 501)}} {
		if code, _, _ := call(append([]string{"sources"}, a...)...); code != 2 {
			t.Fatalf("%q: %d", a, code)
		}
	}
	db, _ := sql.Open("sqlite", filepath.Join(dir, "ops.db"))
	db.Exec("CREATE TABLE cicd_projects (path TEXT, kind TEXT, archived INTEGER)")
	db.Exec("INSERT INTO cicd_projects VALUES ('shop/cart','app',0)")
	db.Close()
	if code, out, e := call("sources", "find", "why is the cart service slow", "--type", "gitlab"); code != 0 ||
		!strings.Contains(out, "## shop/cart") || !strings.Contains(out, "searched 0 of 0 instances - nothing was read") {
		t.Fatalf("no routes, nothing enabled: %d %q %q", code, out, e)
	}
	db, _ = sql.Open("sqlite", filepath.Join(dir, "ops.db"))
	db.Exec(`INSERT INTO source_routes (service, source, kind, locator, confidence, origin, status, created_at, updated_at)
  VALUES ('shop/cart','kubernetes/k1','workload','shop/cart',0.8,'discovered','confirmed',1,1)`)
	db.Exec("CREATE TABLE data_sources (key TEXT PRIMARY KEY, type TEXT, name TEXT, env TEXT, status TEXT, enabled INTEGER, purpose TEXT)")
	db.Exec("INSERT INTO data_sources VALUES ('kubernetes/k1','kubernetes','k1','develop','readonly',1,'agent')")
	db.Close()
	code, out, _ := call("sources", "find", "cart", "--type", "kube", "--json", "--run", "run-7")
	var r map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("json: %d %q", code, out)
	}
	k := r["services"].([]any)[0].(map[string]any)["types"].(map[string]any)["k8s"].(map[string]any)
	if k["decision"] != "use" || !strings.Contains(out, "argus tools kube k1 get pods -n shop") {
		t.Fatalf("k8s: %v", k)
	}
	raw, _ := os.ReadFile(audit)
	if lines := strings.Count(string(raw), "\n"); lines != 2 || strings.Contains(string(raw), "cart service slow") ||
		!strings.Contains(string(raw), `"run":"run-7"`) {
		t.Fatalf("audit: %q", raw)
	}
	demo := filepath.Join(dir, "demo.env")
	os.WriteFile(demo, []byte(base+"DEMO_MODE=1\n"), 0o600)
	t.Setenv("ARGUS_CONFIG", demo)
	if code, out, _ := call("sources", "find", "cart", "--type", "gitlab"); code != 0 || strings.Contains(out, "fan-out:") ||
		!strings.Contains(out, "fan-out is disabled in the demo") {
		t.Fatalf("demo: %d %q", code, out)
	}
}
