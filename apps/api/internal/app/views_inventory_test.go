package app

// Tests of the infra views (inventory, vm_search, capacity, cicd, certs, readiness, selfcheck), mirroring
// tests/test_inventory.py, test_capacity_all.py, test_cicd*.py, test_cert_selfcheck.py and test_readiness_tiers.py. Helpers are prefixed inventory (shared by the other infra test files).

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const inventoryHostile = `<script>alert("x")</script>`

type inventoryEnv struct {
	dir string
	db  *sql.DB
}

// inventorySetup points every infra path at a fresh temp dir with a writable ops.db.
func inventorySetup(t *testing.T) *inventoryEnv {
	t.Helper()
	d := t.TempDir()
	for _, s := range []string{"in", "out", "vm"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	oldDB, oldIn, oldOut := DB, SET_IN, SET_OUT
	oldC, oldCi, oldSc, oldR := CapacityData, CicdData, SelfcheckPath, ReadinessData
	DB, SET_IN, SET_OUT = filepath.Join(d, "ops.db"), filepath.Join(d, "in"), filepath.Join(d, "out")
	CapacityData, CicdData = d, d
	SelfcheckPath, ReadinessData = filepath.Join(d, "selfcheck.json"), filepath.Join(d, "readiness.json")
	Out = io.Discard
	db, err := sql.Open("sqlite", "file:"+DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		DB, SET_IN, SET_OUT = oldDB, oldIn, oldOut
		CapacityData, CicdData, SelfcheckPath, ReadinessData = oldC, oldCi, oldSc, oldR
		Out = os.Stdout
	})
	return &inventoryEnv{d, db}
}

func (e *inventoryEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		t.Fatalf("%v: %s", err, q)
	}
}

func (e *inventoryEnv) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(e.dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func inventoryAdmin() *Session {
	return &Session{Username: "admin", Role: "admin", CSRF: strings.Repeat("a", 32)}
}
func inventoryViewer() *Session {
	return &Session{Username: "view.er", Role: "viewer", CSRF: strings.Repeat("b", 32)}
}

func inventoryCtx(s *Session, path string) *Ctx { return &Ctx{Sess: s, Theme: "auto", Path: path} }

func (e *inventoryEnv) vms(t *testing.T) {
	e.exec(t, `CREATE TABLE vms (id INTEGER PRIMARY KEY, key TEXT NOT NULL UNIQUE, name TEXT, ips TEXT, env TEXT, dc TEXT, os TEXT,
	  os_id TEXT, os_version TEXT, kernel TEXT, arch TEXT, vcpu INTEGER, ram_bytes INTEGER, disk_total INTEGER, disk_used INTEGER,
	  mounts TEXT, boot_time INTEGER, virt TEXT, hypervisor TEXT, monitored INTEGER, up INTEGER, last_scrape INTEGER,
	  prom_instance TEXT, prom_job TEXT, ssh_alias TEXT, ssh_ok INTEGER, docker INTEGER, containers INTEGER, services_running INTEGER,
	  domains TEXT, ntp_sync TEXT, timezone TEXT, flags TEXT, facts TEXT, first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
	  gone INTEGER DEFAULT 0)`)
	e.exec(t, `CREATE TABLE vm_reports (id INTEGER PRIMARY KEY, vm_id INTEGER NOT NULL, name TEXT, env TEXT, alias TEXT, mode TEXT,
	  created INTEGER NOT NULL, requested_by TEXT, trigger TEXT, run_id INTEGER, status TEXT, cost_usd REAL)`)
	e.exec(t, `CREATE TABLE triage_problems (run_id INTEGER, idx INTEGER, title TEXT, severity TEXT, environment TEXT, sources TEXT,
	  summary TEXT, rca TEXT, fix_prompt TEXT, suspicious INTEGER, fix_options TEXT, fix_state TEXT, fix_flags TEXT)`)
	e.exec(t, `CREATE TABLE vm_changes (vm_id INTEGER, at INTEGER, field TEXT, old TEXT, new TEXT)`)
	e.exec(t, `CREATE TABLE incidents (id INTEGER PRIMARY KEY, key TEXT, title TEXT, status TEXT, subject TEXT, severity TEXT)`)
	ins := `INSERT INTO vms (id, key, name, ips, env, dc, vcpu, ram_bytes, disk_total, disk_used, monitored, up, flags, ssh_alias, ssh_ok,
	  first_seen, last_seen, prom_job) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	e.exec(t, ins, 1, "app-01", "app-01", "10.0.0.1,10.0.0.2", "release", "dc-east", 4, 8<<30, 100<<30, 50<<30, 1, 1, "", "app-01-ro", 1, 1, 2, "node")
	e.exec(t, ins, 2, "db-01", "db-01", "10.0.0.9", "release", "dc-west", 8, 16<<30, 0, 0, 1, 0, "ssh-skipped-db,down", "", 0, 1, 2, "node")
	e.exec(t, ins, 3, "x", inventoryHostile, "=1+1", "develop", "dc-east", nil, nil, nil, nil, 0, 0, "prom-only", "", 0, 1, 2, "app")
	e.exec(t, ins, 4, "application-02", "application-02", "10.0.0.3", "develop", "dc-east", 2, 4<<30, 10<<30, 9<<30, 1, 1, "", "", 0, 1, 2, "node")
}

func TestVMSearchWordsRankMark(t *testing.T) {
	if w := VMSearchWords("App  app DB"); strings.Join(w, ",") != "app,db" {
		t.Fatal(w)
	}
	if q := VMSearchQuery(py.NewQS("q", "  a \t b  "+strings.Repeat("x", 200))); py.Len(q) > VMSearchMaxQ || !strings.HasPrefix(q, "a b ") {
		t.Fatal(q)
	}
	ids := []any{"App-01", "10.0.0.1"}
	if VMSearchRank(ids, "app-01") != 0 || VMSearchRank(ids, "app-01 extra") != 1 || VMSearchRank(ids, "app") != 2 {
		t.Fatal("rank")
	}
	// overlapping words merge into one <mark>; the text around is escaped
	if m := VMSearchMark("<b>abcd", []string{"bc", "cd"}); m != "&lt;b&gt;a<mark>bcd</mark>" {
		t.Fatal(m)
	}
	if m := VMSearchMark(nil, []string{"x"}); m != "" {
		t.Fatal(m)
	}
	f := []VMSearchField{{"name", "web"}, {"service", "mongod"}, {"mount", "/data"}}
	if w := VMSearchWhy(f, []string{"mongo"}, map[string]bool{"name": true}); w != "service <mark>mongo</mark>d" {
		t.Fatal(w)
	}
	// exact first, stable within a rank
	items := [][]VMSearchField{{{"name", "app-10"}}, {{"name", "app-1"}}, {{"name", "app-11"}}}
	got := VMSearchSearch(3, "app-1", func(i int) []VMSearchField { return items[i] }, func(i int) []any { return []any{items[i][0].Val} })
	if len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 2 {
		t.Fatal(got)
	}
}

func TestInventoryListSearchAndEscaping(t *testing.T) {
	e := inventorySetup(t)
	e.vms(t)

	// the search: one match opens the VM, an exact name wins among several
	if id, ok := InventoryFindOne(py.NewQS("q", "10.0.0.9")); !ok || id != 2 {
		t.Fatal("find by IP", id, ok)
	}
	if _, ok := InventoryFindOne(py.NewQS("q", "app")); ok {
		t.Fatal("ambiguous search opened a VM")
	}
	if id, ok := InventoryFindOne(py.NewQS("q", "app-01")); !ok || id != 1 {
		t.Fatal("exact name", id, ok)
	}

	// filters: env and flag

}

func TestInventoryCSV(t *testing.T) {
	e := inventorySetup(t)
	e.vms(t)
	out := InventoryCSV(py.QS{})
	lines := strings.Split(out, "\r\n")
	if !strings.HasPrefix(lines[0], "name,ips,env,dc,") || !strings.HasSuffix(out, "\r\n") {
		t.Fatal(lines[0])
	}
	// spreadsheet formulas neutralised, empty text cells too (Python's v[:1] in "=+-@" is True for "")
	if !strings.Contains(out, `"<script>alert(""x"")</script>",'=1+1,develop`) {
		t.Fatal(out)
	}
	if !strings.Contains(out, `app-01,"10.0.0.1,10.0.0.2",release`) {
		t.Fatal("quoting")
	}
	if f := inventoryCSVField("q\"x"); f != `"q""x"` {
		t.Fatal(f)
	}
	if f := inventoryCSVField("line\nnext"); f != "\"line\nnext\"" {
		t.Fatal(f)
	}
}

func TestInventoryRequests(t *testing.T) {
	e := inventorySetup(t)
	e.vms(t)
	c := inventoryCtx(inventoryViewer(), "/")
	if code, _ := InventoryPost(c, "/inventory/refresh", py.NewQS("id", "1x"), inventoryViewer()); code != 400 {
		t.Fatal(code)
	}
	if code, _ := InventoryPost(c, "/inventory/refresh", py.NewQS("id", "1234567890"), inventoryViewer()); code != 400 {
		t.Fatal("long id", code)
	}
	if code, _ := InventoryPost(c, "/inventory/refresh", py.NewQS("id", "99"), inventoryViewer()); code != 404 {
		t.Fatal(code)
	}
	code, loc := InventoryPost(c, "/inventory/refresh", py.NewQS("id", "1"), inventoryViewer())
	if code != 303 || loc != "/inventory/view?id=1&sent=refresh" {
		t.Fatal(code, loc)
	}
	data, err := os.ReadFile(filepath.Join(SET_IN, "requests", "vm-refresh-1.req"))
	if err != nil || !strings.HasPrefix(string(data), `{"by": "view.er", "at": `) {
		t.Fatal(string(data), err)
	}
	if code, msg := InventoryPost(c, "/inventory/refresh", py.NewQS("id", "1"), inventoryViewer()); code != 429 || msg != "Not started: queued." {
		t.Fatal(code, msg)
	}
	if code, _ := InventoryPost(c, "/inventory/analyse", py.NewQS("id", "1"), inventoryViewer()); code != 403 {
		t.Fatal("viewer analyse")
	}
	if code, msg := InventoryPost(c, "/inventory/analyse", py.NewQS("id", "2"), inventoryAdmin()); code != 403 || !strings.Contains(msg, "DB hosts") {
		t.Fatal("DB host", code, msg)
	}
}
