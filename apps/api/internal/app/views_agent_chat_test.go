package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func chatOn(t *testing.T, e *boardTestEnv, on bool) {
	e.json(t, "out/board-status.json", map[string]any{"agent": map[string]any{"settings": map[string]any{"chat": on}}})
}

func TestAgentChatPost(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version) VALUES ('OPS-1', 'ready', 1)`)
	chatOn(t, e, true)
	if c, _ := AgentPostChat(e.ctx(e.view), boardForm("key", "OPS-1", "text", "hi"), e.view); c != 403 {
		t.Fatal("a viewer cannot post")
	}
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "dev-1", "text", "hi"), e.admin); c != 400 {
		t.Fatal("bad key")
	}
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "OPS-2", "text", "hi"), e.admin); c != 404 {
		t.Fatal("not worked by Argus")
	}
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "OPS-1", "text", " \r\n "), e.admin); c != 400 {
		t.Fatal("empty")
	}
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "OPS-1", "text", strings.Repeat("ы", 4001)), e.admin); c != 400 {
		t.Fatal("too long")
	}
	c, loc := AgentPostChat(e.ctx(e.admin), boardForm("key", "OPS-1", "text", " use stage3\r\nnot stg2 ", "return", "/board/task?key=OPS-1"), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/board/task?key=OPS-1&req=") || !strings.HasSuffix(loc, "#chat") {
		t.Fatal(c, loc)
	}
	rid := loc[strings.Index(loc, "req=")+4 : strings.Index(loc, "#")]
	p := filepath.Join(SET_IN, "task-chat", rid+".json")
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("request file", err)
	}
	data, _ := os.ReadFile(p)
	v, _ := py.ParseJSON(data)
	m := py.AsMap(v)
	if m.S("kind") != "chat" || m.S("key") != "OPS-1" || m.S("text") != "use stage3\nnot stg2" || m.S("by") != "alex.doe" {
		t.Fatal(m.PyRepr())
	}
	chatOn(t, e, false)
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "OPS-1", "text", "hi"), e.admin); c != 409 {
		t.Fatal("chat off")
	}
	if PolicyOf("/api/v1/board/agent/chat", nil) != PolicyNormal {
		t.Fatal("a chat message is a normal POST (MFA class normal)")
	}
}

func TestAgentAskThread(t *testing.T) {
	e := boardSetup(t)
	chatOn(t, e, true)
	if c, _ := AgentPostChat(e.ctx(e.view), boardForm("key", "new", "text", "define a task"), e.view); c != 403 {
		t.Fatal("a viewer cannot start a thread")
	}
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "new-XYZ", "text", "hi"), e.admin); c != 400 {
		t.Fatal("bad board key")
	}
	c, loc := AgentPostChat(e.ctx(e.admin), boardForm("key", "new", "text", "define a task", "return", "/board/ask"), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/board/ask?key=new-") || !strings.HasSuffix(loc, "#chat") {
		t.Fatal("a new thread lands on its own page", c, loc)
	}
	key := loc[len("/board/ask?key="):strings.Index(loc, "&")]
	if !agentAskKeyRe.MatchString(key) {
		t.Fatal(key)
	}
	rid := loc[strings.Index(loc, "req=")+4 : strings.Index(loc, "#")]
	data, _ := os.ReadFile(filepath.Join(SET_IN, "task-chat", rid+".json"))
	v, _ := py.ParseJSON(data)
	if m := py.AsMap(v); m.S("key") != key || m.S("kind") != "chat" {
		t.Fatal(m.PyRepr())
	}
	// a follow-up in an existing board thread needs no agent_tasks row and keeps its return page
	c, loc = AgentPostChat(e.ctx(e.admin), boardForm("key", key, "text", "env develop", "return", "/board/ask?key="+key), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/board/ask?key="+key+"&req=") {
		t.Fatal(c, loc)
	}
	chatOn(t, e, false)
	if c, _ := AgentPostChat(e.ctx(e.admin), boardForm("key", "new", "text", "hi"), e.admin); c != 409 {
		t.Fatal("chat off")
	}
}

func TestAgentAskProposalLine(t *testing.T) {
	e := boardSetup(t)
	chatOn(t, e, true)
	e.board.Exec(`INSERT INTO task_chat (key, seq, author_kind, author, kind, body, created) VALUES
		('new-0a1b2c3d', 1, 'user', 'alex.doe', 'message', 'file a task', 1),
		('new-0a1b2c3d', 2, 'argus', 'argus', 'message', 'Proposed it.', 2),
		('new-0a1b2c3d', 3, 'system', NULL, 'proposal', ?, 3)`, "Proposed: "+boardXSS)
	defer boardSkipUnported(t)

	if !strings.Contains(proposalSourceLink(Row{"source_kind": "chat", "source_id": "OPS-1.4", "source_url": "/board/task?key=OPS-1#m4"}),
		`href="/board/task?key=OPS-1#m4"`) {
		t.Fatal("the proposal links back to the chat message")
	}
}
