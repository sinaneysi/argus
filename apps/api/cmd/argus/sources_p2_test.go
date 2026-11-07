package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesP2Commands(t *testing.T) {
	dir := t.TempDir()
	base := "COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_ADMIN_INITIAL_PASSWORD=x\n" +
		"ARGUS_SETTINGS_IN=" + dir + "/in\nARGUS_SETTINGS_OUT=" + dir + "/out\nARGUS_ETC_DIR=" + dir + "/etc\n" +
		"ARGUS_DB=" + dir + "/state/ops.db\nARGUS_AUTH_DB=" + dir + "/auth.db\n"
	demo := filepath.Join(dir, "demo.env")
	os.WriteFile(demo, []byte(base+"DEMO_MODE=1\n"), 0o600)
	for _, sub := range []string{"discover", "catalogue", "actions"} {
		if code, _, e := call("sources", sub, "-file", demo); code != 1 || !strings.Contains(e, "disabled in the demo") {
			t.Fatalf("%s in the demo: %d %q", sub, code, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "ops.db")); err == nil {
		t.Fatal("the demo wrote ARGUS_DB")
	}
	for _, a := range [][]string{{"routes"}, {"routes", "a;b"}, {"unmapped", "Git;lab"}, {"unmapped", "a", "b"}} {
		if code, _, _ := call(append([]string{"sources"}, a...)...); code != 2 {
			t.Fatalf("%v: %d", a, code)
		}
	}
	cfg := filepath.Join(dir, "argus.env")
	os.WriteFile(cfg, []byte(base), 0o600)
	t.Setenv("ARGUS_CONFIG", cfg)
	if code, out, _ := call("sources", "routes", "cart"); code != 0 || !strings.Contains(out, "not built yet") {
		t.Fatalf("routes before any build: %d %q", code, out)
	}
	if code, _, e := call("sources", "discover", "-only", "bad"); code != 2 || !strings.Contains(e, "type/name") {
		t.Fatalf("bad -only: %d %q", code, e)
	}
	// nothing enabled: discovery writes nothing but the schema; the catalogue maps the CI/CD projects
	os.MkdirAll(filepath.Join(dir, "state"), 0o755)
	if code, out, e := call("sources", "discover"); code != 0 || !strings.Contains(out, "catalogue: 0 services") {
		t.Fatalf("discover: %d %q %q", code, out, e)
	}
	db, _ := sql.Open("sqlite", filepath.Join(dir, "state", "ops.db"))
	db.Exec("CREATE TABLE cicd_projects (path TEXT, kind TEXT, archived INTEGER)")
	db.Exec("INSERT INTO cicd_projects VALUES ('shop/cart','app',0)")
	db.Exec(`INSERT INTO source_inventory VALUES ('gitlab/main','project','shop/cart','{}',1,1), ('gitlab/main','project','sandbox/api','{}',1,1)`)
	db.Close()
	if code, out, _ := call("sources", "catalogue"); code != 0 || !strings.Contains(out, "catalogue: 1 services, 1 mapped") {
		t.Fatalf("catalogue: %d %q", code, out)
	}
	if code, out, _ := call("sources", "routes", "cart"); code != 0 || !strings.Contains(out, "proposed\tgitlab/main\tproject\tshop/cart\t1.00") {
		t.Fatalf("routes: %d %q", code, out)
	}
	if code, out, _ := call("sources", "unmapped", "gitlab"); code != 0 || !strings.Contains(out, "gitlab/main\tproject\tsandbox/api") {
		t.Fatalf("unmapped: %d %q", code, out)
	}
	os.MkdirAll(filepath.Join(dir, "in", "source-actions"), 0o755)
	os.WriteFile(filepath.Join(dir, "in", "source-actions", "admin-00000000000000b1.json"),
		[]byte(`{"action":"confirm","id":1,"user":"admin","role":"admin"}`), 0o600)
	if code, out, e := call("sources", "actions"); code != 0 || !strings.Contains(out, "route 1 confirmed") {
		t.Fatalf("actions: %d %q %q", code, out, e)
	}
}
