package board

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func agentEnv(t *testing.T) *env {
	e := newEnv(t, false)
	e.run(t) // creates the database
	e.o.Agent = &AgentOptions{JobsDir: filepath.Join(e.d, "ta", "jobs"), OutDir: filepath.Join(e.d, "ta", "out"),
		ClaudeStatus: filepath.Join(e.d, "out", "claude-token-status.json"), Guard: func() (bool, string) { return true, "" },
		Debounce: time.Nanosecond, Environments: "develop, pre-release, release"}
	db, _ := sql.Open("sqlite", e.o.DB)
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO tasks (id, jira_key, title, description, status, assignee, labels, env, created_at, updated_at) VALUES
		 (7, 'OPS-7', 'Disk fills on a build host', 'Find what fills /var.', 'progress', 'alex.admin', '["ops"]', 'develop', 1, 1)`,
		`INSERT INTO tasks (id, jira_key, title, status, assignee, created_at, updated_at) VALUES (8, 'OPS-8', 'Viewer task', 'progress', 'sam.viewer', 1, 1)`,
		`INSERT INTO tasks (id, jira_key, title, status, assignee, created_at, updated_at) VALUES (9, 'OPS-9', 'Not in the column', 'todo', 'alex.admin', 1, 1)`,
		`INSERT INTO comments (task_id, author, body, created_at) VALUES (7, 'Kim', 'Started on Monday.', 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) out(t *testing.T, key, phase string, doc any, meta map[string]any) {
	os.MkdirAll(e.o.Agent.OutDir, 0o755)
	if doc != nil {
		b, _ := json.Marshal(doc)
		os.WriteFile(filepath.Join(e.o.Agent.OutDir, key+"."+phase+".json"), b, 0o644)
	}
	if meta["finished"] == nil {
		meta["finished"] = time.Now().Unix() + 5
	}
	b, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(e.o.Agent.OutDir, key+"."+phase+".meta.json"), b, 0o644)
}

func (e *env) clearOut(key, phase string) {
	os.Remove(filepath.Join(e.o.Agent.OutDir, key+"."+phase+".json"))
	os.Remove(filepath.Join(e.o.Agent.OutDir, key+"."+phase+".meta.json"))
}

func readJob(t *testing.T, e *env, name string) map[string]any {
	b, err := os.ReadFile(filepath.Join(e.o.Agent.JobsDir, name))
	if err != nil {
		t.Fatalf("no job %s: %v", name, err)
	}
	var d map[string]any
	json.Unmarshal(b, &d)
	return d
}

func plan(steps ...map[string]any) map[string]any {
	l := []any{}
	for _, s := range steps {
		l = append(l, s)
	}
	return map[string]any{"classification": map[string]any{"environment": "develop", "kind": "investigate"},
		"summary": "Look at the disk.", "plan": l, "questions": []any{}, "confidence": "medium"}
}

func TestAgentPlanApproveRun(t *testing.T) {
	e := agentEnv(t)
	st := e.run(t)
	if got := st["agent"].(map[string]any)["tick"]; got != "intake OPS-7" {
		t.Fatalf("tick %v", got)
	}
	job := readJob(t, e, "OPS-7.intake.json")
	if job["summary"] != "Disk fills on a build host" || job["phase"] != "intake" || len(job["comments"].([]any)) != 1 {
		t.Fatalf("job %v", job)
	}
	if e.q(t, `SELECT count(*) FROM agent_tasks`) != "1" {
		t.Fatal("only the admin's task in the column is in scope")
	}
	// the intake asks a blocking question: waiting
	p := plan(map[string]any{"text": "List the largest folders", "mode": "read-only"})
	p["questions"] = []any{map[string]any{"text": "Which host?", "blocking": true}}
	e.out(t, "OPS-7", "intake", p, map[string]any{"cost_usd": 0.2, "status": "{...}", "is_error": false, "exit_code": 0})
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "waiting" {
		t.Fatal(s)
	}
	// the answer re-plans
	e.clearOut("OPS-7", "intake")
	e.req(t, map[string]any{"kind": "agent-answers", "key": "OPS-7", "answers": map[string]any{"q1": "build-01"}, "instructions": "Read only."})
	e.run(t)
	if s := e.q(t, `SELECT state || ':' || (job_started IS NOT NULL) FROM agent_tasks WHERE key='OPS-7'`); s != "planning:1" {
		t.Fatal(s)
	}
	job = readJob(t, e, "OPS-7.intake.json")
	if job["argus"].(map[string]any)["instructions"] != "Read only." {
		t.Fatal("instructions not in the job")
	}
	if e.q(t, `SELECT count(*) FROM outbox WHERE kind='comment'`) != "1" {
		t.Fatal("the answers are not posted to the issue")
	}
	p2 := plan(map[string]any{"text": "List the largest folders on build-01", "mode": "read-only", "targets": []any{"build-01"}})
	e.out(t, "OPS-7", "intake", p2, map[string]any{"cost_usd": 0.3, "is_error": false})
	e.run(t)
	if s := e.q(t, `SELECT state || ':' || plan_version FROM agent_tasks WHERE key='OPS-7'`); s != "ready:2" {
		t.Fatal(s)
	}
	// a stale version is refused; the shown one starts a read-only run
	stale := e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1})
	st = e.run(t)
	if !strings.Contains(result(st, stale), "the plan changed") {
		t.Fatal(result(st, stale))
	}
	ok := e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 2})
	st = e.run(t)
	if result(st, ok) != "approved; spinning up" || e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`) != "running" {
		t.Fatal(result(st, ok), e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`))
	}
	if readJob(t, e, "OPS-7.run.json")["approved_by"] != "alex.admin" {
		t.Fatal("run job")
	}
	e.out(t, "OPS-7", "run", map[string]any{"status": "done", "summary": "The cache folder fills the disk.", "evidence": []any{"du: 40G cache"}},
		map[string]any{"cost_usd": 1.5, "is_error": false})
	e.run(t)
	if s := e.q(t, `SELECT state || ':' || spent_usd FROM agent_tasks WHERE key='OPS-7'`); s != "done:2.0" {
		t.Fatal(s)
	}
	body := e.q(t, `SELECT payload FROM outbox WHERE kind='comment' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(body, "Task done: The cache folder") || !strings.Contains(body, "du: 40G cache") {
		t.Fatal(body)
	}
	if e.q(t, `SELECT count(*) FROM notices WHERE kind='agent-done'`) != "1" {
		t.Fatal("no notice")
	}
}

func TestAgentFailuresAndReplan(t *testing.T) {
	e := agentEnv(t)
	e.run(t)
	e.out(t, "OPS-7", "intake", nil, map[string]any{"is_error": true, "status": "Invalid bearer token", "exit_code": 1, "run_id": "12"})
	e.run(t)
	var d map[string]any
	json.Unmarshal([]byte(e.q(t, `SELECT error_detail FROM agent_tasks WHERE key='OPS-7'`)), &d)
	if d["class"] != "auth" || d["exit_code"] != float64(1) || d["run_id"] != "12" || e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`) != "failed" {
		t.Fatal(d)
	}
	// a replaced and tested token plans it again, once
	now := time.Now().Unix() + 60
	b, _ := json.Marshal(map[string]any{"replaced": map[string]any{"at": now}, "test": map[string]any{"result": "ok", "at": now}})
	os.WriteFile(e.o.Agent.ClaudeStatus, b, 0o644)
	e.clearOut("OPS-7", "intake")
	e.run(t)
	if e.q(t, `SELECT count(*) FROM agent_log WHERE text='Re-planning after the Claude token was replaced'`) != "1" ||
		e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`) != "planning" {
		t.Fatal("no re-plan after the token replacement")
	}
	// a usage limit: failed with the prefix and a retry time
	e.out(t, "OPS-7", "intake", nil, map[string]any{"is_error": true, "status": "Claude AI usage limit reached · resets 5pm (UTC)"})
	e.run(t)
	if s := e.q(t, `SELECT error FROM agent_tasks WHERE key='OPS-7'`); s != "claude-limit: resets 5pm (UTC)" || e.q(t, `SELECT retry_at IS NOT NULL FROM agent_tasks WHERE key='OPS-7'`) != "1" {
		t.Fatal(s)
	}
	// Plan again: queued once, then idempotent
	e.clearOut("OPS-7", "intake")
	os.Remove(e.o.Agent.ClaudeStatus)
	e.o.Agent.Guard = func() (bool, string) { return false, "Claude usage is off (Settings -> Claude)" }
	a := e.req(t, map[string]any{"kind": "agent-replan", "key": "OPS-7"})
	st := e.run(t)
	if !strings.Contains(result(st, a), "Claude usage is off") {
		t.Fatal(result(st, a))
	}
	e.o.Agent.Guard = func() (bool, string) { return true, "" }
	a = e.req(t, map[string]any{"kind": "agent-replan", "key": "OPS-7"})
	v := e.req(t, map[string]any{"kind": "agent-replan", "key": "OPS-7", "by": "sam.viewer"})
	st = e.run(t)
	if !strings.HasPrefix(result(st, a), "queued") || result(st, v) != "refused: only admins direct the task agent" {
		t.Fatal(result(st, a), result(st, v))
	}
	// a model answer that is not a plan
	e.out(t, "OPS-7", "intake", map[string]any{"plan": []any{}}, map[string]any{"is_error": false, "status": "here you go"})
	e.run(t)
	if s := e.q(t, `SELECT error FROM agent_tasks WHERE key='OPS-7'`); s != "intake: classification missing or unknown" {
		t.Fatal(s)
	}
}

func TestAgentGuardsAndRules(t *testing.T) {
	e := agentEnv(t)
	e.o.Agent.Guard = func() (bool, string) { return false, "AI stopped: egress IP 198.51.100.1 is in XX" }
	st := e.run(t)
	if got := st["agent"].(map[string]any)["tick"]; got != "AI stopped: egress IP 198.51.100.1 is in XX" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(e.o.Agent.JobsDir, "OPS-7.intake.json")); err == nil {
		t.Fatal("a job started behind the guard")
	}
	// release: whatever the model wrote, a proposal
	p := plan(map[string]any{"text": "Restart the service", "mode": "WRITE", "targets": []any{"app-01"}})
	p["classification"] = map[string]any{"environment": "release", "kind": "change"}
	got, err := validatePlan(p)
	if err != nil || got["classification"].(map[string]any)["kind"] != "production" {
		t.Fatal(got, err)
	}
	if path, _ := spinUp(got); path != "proposal" {
		t.Fatal(path)
	}
	p["classification"] = map[string]any{"environment": "none", "kind": "change"}
	got, _ = validatePlan(p)
	if path, _ := spinUp(got); path != "refused" {
		t.Fatal(path)
	}
	p["classification"] = map[string]any{"environment": "develop", "kind": "change"}
	got, _ = validatePlan(p)
	if path, _ := spinUp(got); path != "write-report" {
		t.Fatal(path)
	}
	// moved out of the column: the job is stopped
	e.o.Agent.Guard = func() (bool, string) { return true, "" }
	e.run(t)
	db, _ := sql.Open("sqlite", e.o.DB)
	db.Exec(`UPDATE tasks SET status='review' WHERE id=7`)
	db.Close()
	e.run(t)
	if s := e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`); s != "stopped" {
		t.Fatal(s)
	}
	if _, err := os.Stat(filepath.Join(e.o.Agent.JobsDir, "OPS-7.intake.json")); err == nil {
		t.Fatal("the job file of a stopped job is still there")
	}
}

func TestErrorClassAndReset(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"claude-limit: resets 5pm", ""}:           "limit",
		{"intake: x", "OAuth token has expired"}:   "auth",
		{"intake: x", "Request not allowed (403)"}: "egress",
		{"no result (timeout)", ""}:                "timeout",
		{"could not start the intake", ""}:         "start",
		{"intake: no plan steps", ""}:              "no-steps",
		{"intake: output is not valid JSON", ""}:   "output",
		{"intake: something else", "odd"}:          "other",
	} {
		if got := errorClass(in[0], in[1]); got != want {
			t.Errorf("%v: %s, want %s", in, got, want)
		}
	}
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC).Unix()
	if got := resetEpoch("claude-limit: resets 5pm (UTC)", at, time.UTC); got != time.Date(2026, 11, 8, 17, 0, 0, 0, time.UTC).Unix() {
		t.Fatal(got)
	}
	if got := resetEpoch("claude-limit: resets Nov 9, 3:30am (UTC)", at, time.UTC); got != time.Date(2026, 11, 9, 3, 30, 0, 0, time.UTC).Unix() {
		t.Fatal(got)
	}
	if resetEpoch("claude-limit: resets 5", at, time.UTC) != 0 || nextRetry("claude-limit: reset time unknown", at, at, time.UTC) != at+1800 ||
		nextRetry("claude-limit: reset time unknown", at-25*3600, at, time.UTC) != nil {
		t.Fatal("retry rules")
	}
}
