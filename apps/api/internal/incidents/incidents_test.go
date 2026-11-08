package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/migrate"
)

const t0 = 1800000000

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), p, "ops"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *sql.DB, sqlText string) {
	t.Helper()
	if _, err := db.Exec(sqlText); err != nil {
		t.Fatal(err)
	}
}

func opts(dir string, now *int64) *Options {
	return &Options{StateDir: filepath.Join(dir, "state"), ActionsDir: filepath.Join(dir, "actions"), Sites: []string{"east", "west"},
		Customer: []string{"shop.example.com", "api.*"}, Now: func() time.Time { return time.Unix(*now, 0) }}
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func writeReq(t *testing.T, dir, name string, v any) {
	t.Helper()
	os.MkdirAll(dir, 0o750)
	b, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func norm(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}

// Parity with the earlier installation's incidents job on synthetic fixtures: testdata/golden.json was written by
// running that job's code on testdata/fixture.sql (attach four runs, apply requests, auto-resolve twice, re-open,
// resolve). The Go port must leave the same incidents and events.
func TestParity(t *testing.T) {
	td, _ := filepath.Abs("testdata")
	var golden map[string]any
	b, err := os.ReadFile(filepath.Join(td, "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &golden)
	db := testDB(t)
	fx, _ := os.ReadFile(filepath.Join(td, "fixture.sql"))
	exec(t, db, string(fx))
	dir := t.TempDir()
	now := int64(t0)
	o := opts(dir, &now)
	inTx(t, db, func(tx *sql.Tx) error {
		for _, r := range []struct {
			id   int64
			task string
			data string
		}{{1, "incident", "incident-1.log"}, {2, "chat-triage", ""}, {3, "log-rca", ""}, {4, "log-rca", ""}} {
			data := ""
			if r.data != "" {
				data = filepath.Join(td, r.data)
			}
			if _, err := o.AttachRun(tx, r.task, r.id, data); err != nil {
				return err
			}
		}
		return nil
	})
	writeReq(t, o.ActionsDir, "1800003600-0000000a.json", map[string]any{"id": 1, "action": "ack", "by": "alex.admin", "role": "admin", "note": ""})
	writeReq(t, o.ActionsDir, "1800003600-0000000b.json", map[string]any{"id": 2, "action": "resolve", "by": "vic.viewer", "role": "viewer"})
	writeReq(t, o.ActionsDir, "1800003600-0000000c.json", map[string]any{"id": 99, "action": "reopen", "by": "alex.admin", "role": "admin"})
	writeReq(t, o.ActionsDir, "bad-name.json", map[string]any{"id": 1})
	now = t0 + 3600
	if _, err := o.Tick(db, false); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(o.ActionsDir); len(left) != 0 {
		t.Fatalf("request files left: %d", len(left))
	}
	os.MkdirAll(o.StateDir, 0o750)
	for _, x := range []struct {
		file string
		at   int64
	}{{"firing-7h.json", t0 + 25200}, {"firing-31h.json", t0 + 111600}} {
		b, _ := os.ReadFile(filepath.Join(td, x.file))
		os.WriteFile(filepath.Join(o.StateDir, "alerts-firing.json"), b, 0o644)
		now = x.at
		if _, err := o.Tick(db, true); err != nil {
			t.Fatal(err)
		}
	}
	inTx(t, db, func(tx *sql.Tx) error {
		_, err := o.AttachRun(tx, "incident", 5, filepath.Join(td, "incident-5.log"))
		return err
	})
	writeReq(t, o.ActionsDir, "1800116000-0000000d.json", map[string]any{"id": 1, "action": "resolve", "by": "alex.admin", "role": "admin", "note": "fixed\tthe cache"})
	now = t0 + 116000
	if _, err := o.Tick(db, false); err != nil {
		t.Fatal(err)
	}
	incs, err := queryRows(db, `SELECT id, key, dc, subject, family, title, env, severity, alert_severity, customer_facing, status, owner,
 first_seen, last_seen, occurrences, acked_at, acked_by, resolved_at, resolved_by, resolve_note, last_firing, clear_since, fingerprints, sources
 FROM incidents ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	gi := golden["incidents"].([]any)
	if len(incs) != len(gi) {
		t.Fatalf("incidents: %d, want %d", len(incs), len(gi))
	}
	for i := range gi {
		if got, want := norm(incs[i]), gi[i]; !reflect.DeepEqual(got, want) {
			gm, wm := got.(map[string]any), want.(map[string]any)
			for k := range wm {
				if !reflect.DeepEqual(gm[k], wm[k]) {
					t.Errorf("incident %d %s: %v, want %v", i+1, k, gm[k], wm[k])
				}
			}
		}
	}
	evs, _ := queryRows(db, `SELECT incident_id, at, kind, run_id, idx, task, actor, text FROM incident_events ORDER BY rowid`)
	var got [][]any
	for _, e := range evs {
		got = append(got, []any{e["incident_id"], e["at"], e["kind"], e["run_id"], e["idx"], e["task"], e["actor"], e["text"]})
	}
	if g, w := norm(got), golden["events"]; !reflect.DeepEqual(g, w) {
		t.Errorf("events:\n got %v\nwant %v", g, w)
	}
	// candidates, families, subjects
	cases := map[string]struct {
		task   string
		p      Problem
		alerts []Alert
	}{
		"incident-alerts":   {"incident", Problem{Title: "x"}, JobAlerts(filepath.Join(td, "incident-1.log"))},
		"incident-no-alert": {"incident", Problem{Title: "Disk full on db-02.example.org", Summary: "db-02.example.org /var"}, nil},
		"incident-no-host":  {"incident", Problem{Title: "Queue stuck!"}, nil},
		"chat-host":         {"chat-triage", Problem{Title: "Slow", Summary: "https://shop.example.com/cart is slow", Sources: []string{"west alerts"}}, nil},
		"chat-camel":        {"chat-triage", Problem{Title: "Paging", Summary: "HighMemoryUsageAlert on worker", Sources: []string{"Kuma: x"}}, nil},
		"chat-slug":         {"chat-triage", Problem{Title: "Login button broken?", Summary: "no host"}, nil},
	}
	gc := golden["candidates"].(map[string]any)
	for name, c := range cases {
		var got [][]string
		for _, x := range o.Candidates(c.task, c.p, c.alerts, "", nil) {
			got = append(got, []string{x.DC, x.Subject, x.Family})
		}
		if g := norm(got); !reflect.DeepEqual(g, gc[name]) {
			t.Errorf("candidates %s: %v, want %v", name, g, gc[name])
		}
	}
	for n, want := range golden["families"].(map[string]any) {
		if g := FamilyOf(n, ""); g != want {
			t.Errorf("FamilyOf(%q) = %q, want %q", n, g, want)
		}
	}
	for n, want := range golden["text_families"].(map[string]any) {
		if g := FamilyOf("", n); g != want {
			t.Errorf("FamilyOf text %q = %q, want %q", n, g, want)
		}
	}
	for n, want := range golden["subjects"].(map[string]any) {
		if g := NormSubject(n); g != want {
			t.Errorf("NormSubject(%q) = %q, want %q", n, g, want)
		}
	}
}

// Port requirement (docs/ROADMAP.md): a log-RCA finding attaches only to the primary alert's host; hosts the summary
// names as already covered ("Known incident #N") are never candidates. The earlier job attached this run to the
// covered incident #1 (shop.example.com) and listed every host of the text.
func TestLogRCAPrimaryHostOnly(t *testing.T) {
	now := int64(t0)
	o := opts(t.TempDir(), &now)
	summary := "FAIL `HTTP503High`: Known incident #1 covers shop.example.com. cache.example.com and db-03.example.net are also slow."
	got := o.Candidates("log-rca", Problem{}, nil, summary, []AlertRow{{Name: "HTTP503High", Severity: "critical"}})
	if want := []Cand{{"EAST", "cache.example.com", "http-5xx"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no target: %v, want %v", got, want)
	}
	got = o.Candidates("log-rca", Problem{}, nil, summary, []AlertRow{{Name: "HTTP503High", Severity: "critical", Target: "web-07.example.com:9100"}})
	if want := []Cand{{"EAST", "web-07.example.com", "http-5xx"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target: %v, want %v", got, want)
	}
	// the alert's own target is covered by a known incident: the next uncovered host of the text
	got = o.Candidates("log-rca", Problem{}, nil, summary, []AlertRow{{Name: "HTTP503High", Target: "shop.example.com"}})
	if len(got) != 1 || got[0].Subject != "cache.example.com" {
		t.Fatalf("covered target: %v", got)
	}
	// end to end: the run opens a new incident instead of attaching to the covered one
	db := testDB(t)
	exec(t, db, `INSERT INTO runs (id, task, started_at, status, summary) VALUES (1, 'incident', 1800000000, 'FAIL', 'x'), (2, 'log-rca', 1800000100, 'FAIL', '`+strings.ReplaceAll(summary, "'", "''")+`');
INSERT INTO triage_problems (run_id, idx, title, summary) VALUES (1, 0, 'Checkout 503s', 'shop.example.com 503');
INSERT INTO alerts (run_id, severity, alertname, target) VALUES (2, 'critical', 'HTTP503High', '')`)
	inTx(t, db, func(tx *sql.Tx) error {
		if _, err := o.AttachRun(tx, "chat-triage", 1, ""); err != nil {
			return err
		}
		ids, err := o.AttachRun(tx, "log-rca", 2, "")
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] == 1 {
			t.Fatalf("log-RCA attached to %v", ids)
		}
		return nil
	})
	var subj, title, sev string
	db.QueryRow(`SELECT subject, title, severity FROM incidents WHERE id=2`).Scan(&subj, &title, &sev)
	if subj != "cache.example.com" || title != "HTTP503High on cache.example.com" || sev != "high" {
		t.Fatalf("incident 2: %s / %s / %s", subj, title, sev)
	}
}

func TestReadRequestRejects(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"big":      `{"id": 1, "action": "ack", "by": "a.b", "role": "admin", "note": "` + strings.Repeat("x", 5000) + `"}`,
		"action":   `{"id": 1, "action": "delete", "by": "a.b", "role": "admin"}`,
		"user":     `{"id": 1, "action": "ack", "by": "A B", "role": "admin"}`,
		"id":       `{"id": 1.5, "action": "ack", "by": "a.b", "role": "admin"}`,
		"negative": `{"id": -1, "action": "ack", "by": "a.b", "role": "admin"}`,
		"viewer":   `{"id": 1, "action": "reopen", "by": "a.b", "role": "viewer"}`,
		"array":    `[1]`,
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := ReadRequest(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	os.WriteFile(filepath.Join(dir, "target"), []byte(`{"id": 1, "action": "ack", "by": "a.b", "role": "admin"}`), 0o600)
	os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link"))
	if _, err := ReadRequest(filepath.Join(dir, "link")); err == nil {
		t.Error("a symlink was followed")
	}
	if r, err := ReadRequest(filepath.Join(dir, "target")); err != nil || r.ID != 1 || r.By != "a.b" {
		t.Fatalf("valid request: %v %v", r, err)
	}
}

func TestHostsInConfigured(t *testing.T) {
	o := &Options{Prefixes: []string{"node-"}, Suffixes: []string{"example"}, SkipHosts: []string{"chat.example"}}
	got := o.HostsIn("node-7 and web.example down, chat.example fine, see notes.md and main.go")
	if want := []string{"node-7", "web.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestFamilyWords(t *testing.T) {
	o := &Options{Words: map[string][]string{"latency": {"lento"}, "down": {"caido"}}}
	if o.familyOf("", "el sitio esta lento") != "latency" || o.familyOf("", "todo caido") != "down" || FamilyOf("", "todo caido") != "other" {
		t.Fatal("extra words")
	}
}
