package app

import (
	"database/sql"
	"strings"
	"testing"
)

func TestStatusNamesFailedRunAndRunsFilterNotOK(t *testing.T) {
	e, ck, _ := srcSetup(t)
	db, err := sql.Open("sqlite", "file:"+DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	seed := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	seed(`CREATE TABLE IF NOT EXISTS runs (id INTEGER PRIMARY KEY, task TEXT NOT NULL, started_at INTEGER NOT NULL, duration_ms INTEGER, status TEXT,
	  summary TEXT, cost_usd REAL, turns INTEGER, denials INTEGER, is_error INTEGER, mr_url TEXT, log_path TEXT)`)
	seed(`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (71, 'cert-check', ` + I2S(now-60) + `, 'WARN', 'x', 0, 0)`)
	seed(`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (72, 'inventory', ` + I2S(now-50) + `, 'OK', 'y', 0, 0)`)
	code, m := apiReq(t, e, "GET", "/api/v1/status", ck, "", nil)
	if code != 200 || m["state"] != "failed" || m["failedHref"] != "/runs/view?id=71" {
		t.Fatalf("one failure: %d %v", code, m)
	}
	seed(`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (73, 'backup-check', ` + I2S(now-40) + `, 'FAIL', 'z', 0, 0)`)
	if _, m = apiReq(t, e, "GET", "/api/v1/status", ck, "", nil); m["failedHref"] != "/runs?status=notok" {
		t.Fatalf("two failures: %v", m)
	}
	code, m = apiReq(t, e, "GET", "/api/v1/runs?status=notok", ck, "", nil)
	rows, _ := m["rows"].([]any)
	if code != 200 || len(rows) != 2 {
		t.Fatalf("notok rows: %d %d", code, len(rows))
	}
	for _, r := range rows {
		if s, _ := r.(map[string]any)["summary"].(string); strings.Contains(s, "y") {
			t.Fatalf("OK run in notok: %v", r)
		}
	}
}
