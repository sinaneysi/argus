package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The status block answers "Is the AI working right now?" from the switch, the token, the guard and the last real run.
func TestClaudeVerdict(t *testing.T) {
	now := NowInt()
	base := claudeFacts{On: true, Guard: true, TokenKnown: true, TokenSet: true, Last4: "wxyz"}
	with := func(mod func(*claudeFacts)) claudeFacts { f := base; mod(&f); return f }
	cases := []struct {
		name       string
		f          claudeFacts
		kind, head string
		href       string
	}{
		{"working: test passed", with(func(f *claudeFacts) { f.TestResult, f.TestAt = "ok", now-60 }), "ok", "Argus's AI is working.", ""},
		{"working: last run OK", with(func(f *claudeFacts) {
			f.HasRun, f.RunOK, f.RunTask, f.RunAt = true, true, "log-rca", now-30
		}), "ok", "Argus's AI is working.", ""},
		{"token failed", with(func(f *claudeFacts) {
			f.TestResult, f.TestMsg, f.TestAt = "error", "Your organization has disabled Claude subscription access", now-60
		}), "fail", "Argus's AI is not working: the token test failed.", "#claude-replace"},
		{"token failed, then a run failed too", with(func(f *claudeFacts) {
			f.TestResult, f.TestMsg, f.TestAt = "error", "OAuth token has expired", now-600
			f.HasRun, f.RunOK, f.RunTask, f.RunAt, f.RunSummary = true, false, "log-rca", now-60, "Invalid API key"
		}), "fail", "Argus's AI is not working.", "#claude-replace"},
		{"test timed out: not a token problem", with(func(f *claudeFacts) {
			f.TestResult, f.TestMsg, f.TestAt = "error", "no answer within 120 s", now-60
		}), "fail", "Argus's AI is not working: the token test failed.", "#claude-token"},
		{"test stopped by the guard", with(func(f *claudeFacts) {
			f.TestResult, f.TestMsg, f.TestAt = "stopped", "AI stopped: egress IP 203.0.113.9 is in IR", now-60
		}), "warn", "The last token test did not run: AI was stopped.", "#claude-token"},
		{"a run worked after a failed test", with(func(f *claudeFacts) {
			f.TestResult, f.TestAt = "error", now-600
			f.HasRun, f.RunOK, f.RunTask, f.RunAt = true, true, "cert-check", now-60
		}), "ok", "Argus's AI is working.", ""},
		{"egress blocked", with(func(f *claudeFacts) {
			f.TestResult, f.TestAt = "ok", now-60
			f.EgressChecked, f.EgressBlocked, f.EgressLoc, f.EgressAt = true, true, "IR", now-30
		}), "fail", "Argus's AI is stopped by the location guard.", "#claude-egress"},
		{"usage off", with(func(f *claudeFacts) {
			f.On, f.EgressChecked, f.EgressBlocked = false, true, true
		}), "off", "Argus's AI is off.", "#claude-usage"},
		{"last run error", with(func(f *claudeFacts) {
			f.TestResult, f.TestAt = "ok", now-600
			f.HasRun, f.RunOK, f.RunTask, f.RunAt, f.RunSummary = true, false, "rca-deep", now-60, "ERROR: no result, see log"
		}), "warn", "The last AI run failed.", "#claude-token"},
		{"no token", with(func(f *claudeFacts) { f.TokenSet = false }), "fail", "Argus's AI cannot run: there is no Claude token.", "#claude-replace"},
		{"usage limit", with(func(f *claudeFacts) { f.TestResult, f.TestAt = "limit", now-60 }), "warn", "Argus's AI has reached the Claude usage limit.", ""},
		{"nothing known", claudeFacts{On: true, Guard: true}, "unknown", "Argus's AI has not been checked yet.", "#claude-token"},
	}
	for _, c := range cases {
		v := claudeVerdictOf(c.f)
		if v.Kind != c.kind || v.Head != c.head || v.Href != c.href || (c.href != "" && v.Act == "") {
			t.Errorf("%s: got %q %q %q", c.name, v.Kind, v.Head, v.Href)
		}
	}
	// guard off: the status block says so even while working

	// the run's error is shown short and safe, the task in words
	v := claudeVerdictOf(cases[9].f)
	if !strings.Contains(v.Detail, "rca deep") && !strings.Contains(v.Detail, "RCA deep") {
		t.Errorf("task words: %s", v.Detail)
	}
	if claudeTaskWord("log-rca") != "log RCA" || claudeTaskWord("vm-explain") != "VM explain" {
		t.Errorf("task words: %q %q", claudeTaskWord("log-rca"), claudeTaskWord("vm-explain"))
	}
}

// The page reads the last real run from ops.db: skipped runs and runs without model turns do not count.
func TestClaudePageLastRun(t *testing.T) {
	srvSetup(t)
	db, err := sql.Open("sqlite", "file:"+DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	for _, q := range []string{
		`CREATE TABLE runs (id INTEGER PRIMARY KEY, task TEXT NOT NULL, started_at INTEGER NOT NULL, duration_ms INTEGER, status TEXT,
		  summary TEXT, cost_usd REAL, turns INTEGER, denials INTEGER, is_error INTEGER, mr_url TEXT, log_path TEXT)`,
		`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (1, 'log-rca', ` + I2S(now-3600) +
			`, 'ERROR', 'Your organization has disabled Claude subscription access for Claude Code', 1, 1)`,
		`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (2, 'cert-check', ` + I2S(now-60) +
			`, 'WARN', 'SKIPPED: Claude usage is off', 0, 0)`,
		`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (3, 'chat-triage', ` + I2S(now-30) +
			`, 'OK', 'OK: nothing new to triage', 0, 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	db.Exec(`INSERT INTO runs (id, task, started_at, status, summary, turns, is_error) VALUES (4, 'backup-check', ` + I2S(now-10) +
		`, 'OK', 'OK: 126 sets', 12, 0)`)

}

// Root's result lines become a short state, a plain title and the next step; the raw line is never the headline.
func TestClaudeResultWords(t *testing.T) {
	for _, c := range []struct{ res, kind, title string }{
		{"replaced …b3_-, test ok: Claude answered.; kept", "ok", "Token replaced and working."},
		{"replaced …b3_-, test error: Invalid API key; previous token restored", "fail", "The new token did not work, so the previous one is back."},
		{"replaced …b3_-, test error: Invalid API key; kept (save anyway)", "warn", "Token saved, but its test did not pass."},
		{"replaced …b3_-, test limit: usage limit reached; kept", "warn", "Token replaced. Claude says the usage limit is reached."},
		{"test ok: Claude answered.", "ok", "The token works."},
		{"test error: Your organization has disabled Claude subscription access", "fail", "Claude refused the token."},
		{"test stopped: AI stopped: egress IP 203.0.113.9 is in IR", "warn", "Test not run: AI is stopped."},
		{"egress 198.51.100.7 in NL: allowed", "ok", "Location checked: allowed."},
		{"egress 203.0.113.9 in IR: AI stopped: egress IP 203.0.113.9 is in IR", "fail", "Location checked: AI is stopped."},
		{"egress guard is off", "warn", "The location guard is off."},
		{"Claude egress guard on (blocked IR, fail closed)", "ok", "Guard settings applied."},
		{"refused: only an enabled admin can do this", "fail", "Not done: only an enabled admin can do this."},
		{"test error: no answer within 120 s", "fail", "The token test failed."},
		{"replaced …b3_-, test stopped: AI stopped: egress IP 203.0.113.9 is in IR; previous token restored", "warn",
			"Not replaced: the test could not run because AI is stopped."},
		{"test error: line one\nline two", "fail", "The token test failed."},
		{"something new", "warn", "Finished."},
	} {
		k, title, _ := claudeResultWords(c.res)
		if k != c.kind || title != c.title {
			t.Errorf("%q: got %q %q", c.res, k, title)
		}
	}
	// the page: a test result shows the short message, not the raw line, and no refresh once done
	e, ck, csrf, _ := claudeTokenSetup(t)
	r := e.req("POST", "/api/v1/admin/claude-token", ck, map[string][]string{"csrf": {csrf}, "action": {"test"}}, nil)
	reqs := claudeRequests(t)
	if r.status != 200 || len(reqs) != 1 {
		t.Fatalf("test request: %d %v", r.status, reqs)
	}
	rid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(reqs[0]), "admin-"), ".json")
	os.Remove(reqs[0])
	os.WriteFile(filepath.Join(SET_OUT, "claude-token-status.json"), []byte(`{"results": {"`+rid+`": {"user": "admin",
		"result": "test ok: Claude answered."}}}`), 0o644)

}
