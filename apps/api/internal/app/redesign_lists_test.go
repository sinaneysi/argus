package app

// Incidents and Alerts as queue pages: facet rows with counts (GET links), search, sort and server pagination instead of
// labelled selects + submit and ?all=1. Fictional data only.

import (
	"database/sql"
	"fmt"
	"testing"
)

func listsSeedIncidents(t *testing.T, e *srvEnv) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS incidents (id INTEGER PRIMARY KEY, key TEXT NOT NULL, dc TEXT, subject TEXT,
	  family TEXT, title TEXT, env TEXT, severity TEXT, alert_severity TEXT, customer_facing INTEGER DEFAULT 0,
	  status TEXT NOT NULL DEFAULT 'open', owner TEXT, first_seen INTEGER, last_seen INTEGER, occurrences INTEGER DEFAULT 0,
	  acked_at INTEGER, acked_by TEXT, resolved_at INTEGER, resolved_by TEXT, resolve_note TEXT, sources TEXT)`); err != nil {
		t.Fatal(err)
	}
	now := NowInt()
	sts := []string{"open", "acknowledged", "resolved"}
	sevs := []string{"critical", "high", "medium", "low", ""}
	envs := []string{"release", "develop"}
	srcs := []string{`["qa"]`, `["alertmanager"]`, `["kuma"]`}
	for i := 1; i <= 60; i++ {
		if _, err := db.Exec(`INSERT INTO incidents (id, key, dc, subject, family, title, env, severity, status, first_seen,
		  last_seen, occurrences, resolved_at, sources) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, i, fmt.Sprintf("K|svc-%02d.example|http-5xx", i),
			"dc-east", fmt.Sprintf("svc-%02d.example", i), "http-5xx", fmt.Sprintf("svc-%02d 502", i), envs[i%2], sevs[i%5], sts[i%3],
			now-int64(i)*600, now-int64(i)*60, 1, map[bool]any{true: now - 30, false: nil}[sts[i%3] == "resolved"], srcs[i%3]); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`UPDATE incidents SET title='Checkout gateway timeouts' WHERE id=7`)
}
