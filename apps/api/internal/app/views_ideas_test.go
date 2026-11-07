package app

// Ideas (/ideas) on fixture data: the page for an admin and a viewer, escaping, the nav badge and the Overview line,
// the build-prompt download, the POST forms (CSRF, admins only, reason and ref rules) and the request files they write.
// The same pages are compared byte for byte with the Python app by tests/parity (seed_ideas.py, post_cases_ideas.py).

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func ideasSeed(t *testing.T, e *srvEnv) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	stmts := []string{
		`CREATE TABLE ideas (id INTEGER PRIMARY KEY, persona TEXT NOT NULL, title TEXT NOT NULL, area TEXT NOT NULL, problem TEXT,
		 evidence TEXT, proposal TEXT, value TEXT, effort TEXT, risk TEXT, prerequisites TEXT, success_metric TEXT, changed TEXT,
		 revives INTEGER, evidence_hash TEXT, status TEXT NOT NULL DEFAULT 'new', run_id INTEGER, created_at INTEGER NOT NULL,
		 updated_at INTEGER NOT NULL, decided_at INTEGER, decided_by TEXT, reason TEXT, built_ref TEXT, built_at INTEGER,
		 built_by TEXT, build_prompt TEXT, prompt_at INTEGER, prompt_refined INTEGER DEFAULT 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ev := `[{"text": "Incident #12 <b>9x</b>", "link": "/incidents/view?id=12"}, {"text": "plain", "link": ""}]`
	rows := [][]any{
		{1, "devops", "Watch the <deploy> lock", "ci-cd", "new", "M", nil, nil},
		{2, "qa", "Smoke-test checkout after deploys", "qa", "review", "S", nil, nil},
		{3, "noc", "Group flapping monitors", "monitoring", "accepted", "S", "# Build\n\n## Guardrails <x>", nil},
		{4, "devops", "Archive idle projects", "other", "rejected", "S", nil, "not ours"},
		{5, "devops", "Restore drill weekly", "backup", "built", "M", "# Build 5", nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO ideas (id, persona, title, area, problem, evidence, proposal, value, effort, risk,
			prerequisites, success_metric, status, created_at, updated_at, build_prompt, reason, decided_by, decided_at, built_ref)
			VALUES (?,?,?,?,'The lock was held 9 times.',?,'A job.','3 h a week',?,'low','none','no waits',?,?,?,?,?,'alex.doe',?,'!42')`,
			r[0], r[1], r[2], r[3], ev, r[5], r[4], now-3600, now, r[6], r[7], now-60); err != nil {
			t.Fatal(err)
		}
	}
	st := `{"at": 1, "enabled": true, "running": null, "personas": {
	  "devops": {"name": "Kai", "role": "Senior DevOps", "days": ["Mon"], "last_run": ` + I2S(now-600) + `, "last_cost": 0.41, "manual_today": false},
	  "qa": {"name": "Quinn", "role": "QA specialist", "days": ["Wed"], "last_run": null, "manual_today": true},
	  "noc": {"name": "<Nico>", "role": "NOC engineer", "days": ["Fri"], "manual_today": false}}}`
	if err := os.WriteFile(filepath.Join(e.dir, "ideas-status.json"), []byte(st), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIdeasPosts(t *testing.T) {
	e := srvSetup(t)
	ideasSeed(t, e)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	r := e.req("POST", "/api/v1/ideas/act", ck, url.Values{"csrf": {csrf}, "action": {"reject"}, "id": {"2"}, "reason": {" not \t ours "}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/review?kind=ideas&sent=reject&req=") || !strings.HasSuffix(r.loc, "#i-2") {
		t.Fatal(r.status, r.loc)
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "in", "idea-actions", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	var got map[string]any
	b, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(b, &got); err != nil || got["reason"] != "not ours" || got["action"] != "reject" ||
		got["by"] != "admin" || got["role"] != "admin" || got["id"] != float64(2) {
		t.Fatal(string(b))
	}
	os.Remove(files[0])
	for _, c := range []struct {
		form url.Values
		code int
	}{
		{url.Values{"csrf": {"0"}, "action": {"accept"}, "id": {"2"}}, 403},
		{url.Values{"csrf": {csrf}, "action": {"nuke"}, "id": {"2"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"accept"}, "id": {"0"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"reject"}, "id": {"2"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"reject"}, "id": {"2"}, "reason": {strings.Repeat("x", 301)}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"built"}, "id": {"3"}, "ref": {"javascript:alert(1)"}}, 400},
	} {
		if r := e.req("POST", "/api/v1/ideas/act", ck, c.form, nil); r.status != c.code {
			t.Fatal(c.form, r.status)
		}
	}
	vt, vc := e.login("view.er")
	if r := e.req("POST", "/api/v1/ideas/act", map[string]string{auth.COOKIE: vt}, url.Values{"csrf": {vc}, "action": {"accept"}, "id": {"2"}}, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	if files, _ := filepath.Glob(filepath.Join(e.dir, "in", "idea-actions", "*.json")); len(files) != 0 {
		t.Fatal(files)
	}
	r = e.req("POST", "/api/v1/ideas/generate", ck, url.Values{"csrf": {csrf}, "persona": {"noc"}}, nil)
	if r.status != 200 || r.loc != "/review?kind=ideas&sent=generate" {
		t.Fatal(r.status, r.loc)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "in", "requests", "ideas-noc.req")); err != nil {
		t.Fatal(err)
	}

}
