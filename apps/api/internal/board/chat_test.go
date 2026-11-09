package board

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *env) chat(t *testing.T, r map[string]any) string {
	e.n++
	rid := "20261108" + strings.Repeat("0", 5) + string(rune('0'+e.n%10)) + "-0000000" + string(rune('0'+e.n%10))
	if r["by"] == nil {
		r["by"] = "alex.admin"
	}
	r["kind"] = "chat"
	b, _ := json.Marshal(r)
	os.MkdirAll(e.o.Agent.ChatReqDir, 0o755)
	os.WriteFile(filepath.Join(e.o.Agent.ChatReqDir, rid+".json"), b, 0o644)
	return rid
}

func TestTaskChat(t *testing.T) {
	e := agentEnv(t)
	e.o.Agent.ChatReqDir = filepath.Join(e.d, "chat-in")
	e.run(t) // the intake job starts
	off := e.chat(t, map[string]any{"key": "OPS-7", "text": "hello"})
	st := e.run(t)
	if result(st, off) != "refused: the task chat is off (Admin -> Task agent)" {
		t.Fatal(result(st, off))
	}
	e.req(t, map[string]any{"kind": "agent-settings", "enabled": true, "auto_done": false, "qa_enabled": false, "assignees": []any{},
		"column": "In Progress", "qa_threshold": "high", "qa_cap": 5, "chat": true})
	e.run(t)
	p := plan(map[string]any{"text": "look", "mode": "read-only"})
	p["questions"] = []any{map[string]any{"text": "Which host?", "blocking": true}}
	e.out(t, "OPS-7", "intake", p, map[string]any{"is_error": false})
	e.run(t) // the plan arrives: waiting, no job runs
	ok := e.chat(t, map[string]any{"key": "OPS-7", "text": "It is build-01. token=abcdefghijklmnop"})
	v := e.chat(t, map[string]any{"key": "OPS-7", "text": "hi", "by": "sam.viewer"})
	st = e.run(t)
	if result(st, ok) != "posted (a credential was removed)" || !strings.HasPrefix(result(st, v), "refused: viewers") {
		t.Fatal(result(st, ok), result(st, v))
	}
	if strings.Contains(e.q(t, `SELECT group_concat(body) FROM task_chat`), "abcdefghijklmnop") {
		t.Fatal("the credential was stored")
	}
	if e.q(t, `SELECT state FROM task_chat_turns`) != "running" {
		t.Fatal(e.q(t, `SELECT state FROM task_chat_turns`))
	}
	job := readJob(t, e, "OPS-7.chat.json")
	if len(job["messages"].([]any)) < 1 || job["plan_version"] != float64(1) {
		t.Fatal(job)
	}
	e.out(t, "OPS-7", "chat", map[string]any{"reply": "Thanks, build-01 it is.", "summary": "Host is build-01.", "replan": false,
		"answers": []any{map[string]any{"id": "q1", "answer": "build-01"}}}, map[string]any{"cost_usd": 0.05, "is_error": false, "finished": time.Now().Unix() + 5})
	e.run(t)
	if e.q(t, `SELECT state FROM task_chat_turns`) != "done" || e.q(t, `SELECT summary FROM task_chat_state WHERE key='OPS-7'`) != "Host is build-01." {
		t.Fatal("turn not done")
	}
	if !strings.Contains(e.q(t, `SELECT answers FROM agent_inputs WHERE key='OPS-7'`), "build-01") ||
		e.q(t, `SELECT state FROM agent_tasks WHERE key='OPS-7'`) != "planning" {
		t.Fatal("the answer did not re-plan")
	}
	if e.q(t, `SELECT author_kind FROM task_chat WHERE body LIKE 'Thanks%'`) != "argus" {
		t.Fatal("reply author")
	}
}
