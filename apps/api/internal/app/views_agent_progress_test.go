package app

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agentProgressWrite(t *testing.T, e *boardTestEnv, key, phase string, doc map[string]any) {
	t.Helper()
	d := filepath.Join(e.dir, "task-agent", "out")
	os.MkdirAll(d, 0o755)
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(d, key+"."+phase+".progress.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentPlanAgain(t *testing.T) {
	e := boardSetup(t)
	now := NowInt()
	e.board.Exec(`INSERT INTO agent_tasks (key, state, phase, plan_version, error, updated, plan_entered_at) `+
		`VALUES ('OPS-3', 'failed', 'intake', 0, 'intake: Invalid API key', ?, ?)`, now, now)
	if c, _ := AgentPost(e.ctx(e.view), boardForm("key", "OPS-3", "action", "replan"), e.view); c != 403 {
		t.Fatal("viewer")
	}

	c, loc := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin)
	if c != 303 || !strings.HasSuffix(loc, "#agent") {
		t.Fatal(c, loc)
	}
	req, _ := boardReadRequest(t, loc)
	if req.S("kind") != "agent-replan" || req.S("key") != "OPS-3" || req.S("by") != e.admin.Username {
		t.Fatal(req.PyRepr())
	}
	// a second click while queued: the same request, no second file
	c2, loc2 := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin)
	ents, _ := os.ReadDir(filepath.Join(SET_IN, "board-requests"))
	if c2 != 303 || loc2 != loc || len(ents) != 1 {
		t.Fatal("not idempotent", loc, loc2, len(ents))
	}

	os.Remove(filepath.Join(SET_IN, "board-requests", ents[0].Name()))
	// refusals: Claude off, egress blocked, nothing to plan again
	os.WriteFile(filepath.Join(SET_OUT, "claude.json"), []byte(`{"enabled": false}`), 0o644)
	if c, body := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin); c != 409 || !strings.Contains(body, "Claude usage is off") {
		t.Fatal("claude off", c)
	}

	os.Remove(filepath.Join(SET_OUT, "claude.json"))
	e.board.Exec(`UPDATE agent_tasks SET state='ready', plan='{"plan": []}' WHERE key='OPS-3'`)
	if c, body := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin); c != 409 || !strings.Contains(body, "Nothing to plan again") {
		t.Fatal("ready", c)
	}
	e.board.Exec(`UPDATE agent_tasks SET state='running', job_started=? WHERE key='OPS-3'`, now)
	if c, _ := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin); c != 409 {
		t.Fatal("running")
	}
	e.board.Exec(`UPDATE agent_tasks SET state='stopped', job_started=NULL, error='moved out of the agent column or assigned to someone else' WHERE key='OPS-3'`)
	if c, _ := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin); c != 409 {
		t.Fatal("left the column")
	}
	e.board.Exec(`UPDATE agent_tasks SET error='stopped by alex.doe' WHERE key='OPS-3'`)
	if c, _ := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-3", "action", "replan"), e.admin); c != 303 {
		t.Fatal("stopped by hand")
	}
}

func TestAgentPlanAgainCSRFAndPolicy(t *testing.T) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	r := e.req("POST", "/api/v1/board/agent", ck, url.Values{"csrf": {"x" + csrf[1:]}, "key": {"OPS-1"}, "action": {"replan"}}, nil)
	if r.status != 403 {
		t.Fatal("bad CSRF must be refused", r.status)
	}
	if PolicyOf("/api/v1/board/agent", nil) != PolicyNormal {
		t.Fatal("Plan again keeps the agent actions' MFA class")
	}
}

func TestAgentErrClassMatchesPython(t *testing.T) {
	for in, want := range map[string]string{"Your organization has disabled Claude subscription access": "auth",
		"Invalid API key · Please run /login": "auth", "OAuth token has expired": "auth", "API Error: 403 Request not allowed": "egress",
		"Unable to connect to API (ECONNREFUSED)": "egress", "claude-limit: resets 9am": "limit", "no result (timeout)": "timeout",
		"could not start the intake": "start", "intake: no plan steps": "no-steps", "intake: no output": "output",
		"intake: something else": "other"} {
		if got := agentErrClass(in, ""); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestAgentFailureLeadsAndQueuedFollows(t *testing.T) {
	e := boardSetup(t)
	now := NowInt()
	// a failed run with a result: the failure and Plan again come before the result and outside the plan fold
	e.board.Exec(`INSERT INTO agent_tasks (key, state, phase, plan_version, error, result, updated, plan_entered_at) `+
		`VALUES ('OPS-4', 'failed', 'run', 1, 'run: no output', ?, ?, ?)`, `{"status": "failed", "summary": "run produced no valid result"}`, now, now)

	// queued Plan again: the page follows it
	if c, _ := AgentPost(e.ctx(e.admin), boardForm("key", "OPS-4", "action", "replan"), e.admin); c != 303 {
		t.Fatal(c)
	}

	// Claude off while queued: no refresh and no dead "Refresh live" link
	e.board.Exec(`INSERT INTO agent_tasks (key, state, phase, plan_version, updated, plan_entered_at) VALUES ('OPS-5', 'planning', 'intake', 0, ?, ?)`, now, now)
	os.WriteFile(filepath.Join(SET_OUT, "claude.json"), []byte(`{"enabled": false}`), 0o644)

}
