package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

// boardTestEnv points the app at a temp data dir with an empty board.db (the live schema) and auth.db.
type boardTestEnv struct {
	dir   string
	board *sql.DB
	admin *Session
	view  *Session
}

func boardSetup(t *testing.T) *boardTestEnv {
	t.Helper()
	d := t.TempDir()
	oldDB, oldIn, oldOut := DB, SET_IN, SET_OUT
	DB, SET_IN, SET_OUT = filepath.Join(d, "ops.db"), filepath.Join(d, "in"), filepath.Join(d, "out")
	auth.DB = filepath.Join(d, "auth.db")
	auth.Reset()
	t.Cleanup(func() { DB, SET_IN, SET_OUT = oldDB, oldIn, oldOut; auth.Reset() })
	for _, sub := range []string{"in/jira-connect", "in/outline-requests", "out", "board"} {
		os.MkdirAll(filepath.Join(d, sub), 0o755)
	}
	if err := auth.Init("", nil); err != nil {
		t.Fatal(err)
	}
	auth.AddUser("alex.doe", "admin")
	auth.AddUser("viewer1", "viewer")
	schema, err := os.ReadFile("testdata/board_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	b, err := sql.Open("sqlite", filepath.Join(d, "board", "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range strings.Split(string(schema), ";\n") {
		if strings.TrimSpace(st) != "" {
			if _, err := b.Exec(st); err != nil {
				t.Fatal(err, st)
			}
		}
	}
	ops, _ := sql.Open("sqlite", DB)
	ops.Exec("CREATE TABLE outline_pages (service TEXT PRIMARY KEY, grp TEXT, title TEXT NOT NULL, body TEXT NOT NULL, " +
		"body_hash TEXT NOT NULL, prev_body TEXT, prev_hash TEXT, rendered_at INTEGER, changed_at INTEGER, redacted TEXT, " +
		"blocked TEXT, summary TEXT, doc_id TEXT, doc_url TEXT, publish_status TEXT, publish_flags TEXT, published_at INTEGER, publish_error TEXT)")
	ops.Close()
	t.Cleanup(func() { b.Close() })
	return &boardTestEnv{dir: d, board: b,
		admin: &Session{Username: "alex.doe", Role: "admin", CSRF: "c" + strings.Repeat("0", 31), Source: "local"},
		view:  &Session{Username: "viewer1", Role: "viewer", CSRF: "v" + strings.Repeat("0", 31), Source: "local"}}
}

func (e *boardTestEnv) json(t *testing.T, rel string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(e.dir, rel), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *boardTestEnv) ctx(s *Session) *Ctx { return &Ctx{Sess: s, Theme: "auto", Path: "/board"} }

func boardReadRequest(t *testing.T, loc string) (*py.OMap, os.FileInfo) {
	t.Helper()
	i := strings.Index(loc, "req=")
	if i < 0 {
		t.Fatalf("no req in %q", loc)
	}
	rid := loc[i+4 : i+4+23]
	p := filepath.Join(SET_IN, "board-requests", rid+".json")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	v, err := py.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return py.AsMap(v), st
}

const boardXSS = `<script>alert("x")</script>`

func boardForm(kv ...string) py.QS { return py.NewQS(kv...) }

func TestBoardCreateWritesTheRequestFile(t *testing.T) {
	e := boardSetup(t)
	code, loc := BoardPost(e.ctx(e.view), "/board/create", boardForm("title", "  A   new\ttask ", "env", "develop", "host", "app-01",
		"priority", "High", "labels", "a b", "description", "d\r\nx", "evidence", "e", "fix_prompt", "p"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/board?req=") {
		t.Fatal(code, loc)
	}
	p, st := boardReadRequest(t, loc)
	if got := strings.Join(p.Keys(), ","); got != "kind,title,env,host,priority,labels,description,evidence,fix_prompt,id,by,at" {
		t.Fatal(got)
	}
	if p.S("title") != "A new task" || p.S("by") != "viewer1" || p.S("description") != "d\nx" {
		t.Fatal(p.PyRepr())
	}
	if st.Mode().Perm()&0o022 != 0 && st.Mode().Perm() != 0o666&^boardUmask() {
		t.Fatal("mode", st.Mode())
	}
}

func boardUmask() os.FileMode {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return os.FileMode(m)
}

func TestBoardCreateRefusals(t *testing.T) {
	e := boardSetup(t)
	cases := map[string][]string{
		"The title must be":               {"title", " ", "priority", "Medium"},
		"Unknown environment.":            {"title", "t", "env", "prod", "priority", "Medium"},
		"The host must be":                {"title", "t", "host", "a b<", "priority", "Medium"},
		"Unknown priority.":               {"title", boardXSS, "priority", "Urgent"},
		"Labels: up to 10 words":          {"title", "t", "priority", "Medium", "labels", "a <b>"},
		"The fix prompt is limited to 16": {"title", "t", "priority", "Medium", "fix_prompt", strings.Repeat("é", 9000)},
	}
	for msg, kv := range cases {
		code, page := BoardPost(e.ctx(e.view), "/board/create", boardForm(kv...), e.view)
		if code != 400 || !strings.Contains(page, msg) {
			t.Errorf("%s: %d", msg, code)
		}
		if strings.Contains(page, "<script>alert") || strings.Contains(page, "a <b>") {
			t.Errorf("%s: unescaped value echoed", msg)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(SET_IN, "board-requests")); len(ents) != 0 {
		t.Fatal("a refused create wrote a request")
	}
	code, _ := BoardPost(e.ctx(e.view), "/board/create", boardForm("title", "t", "priority", "Medium", "src", "incident", "id", "1"), e.view)
	if code != 404 {
		t.Fatal("unknown source", code)
	}
}

func TestBoardMoveCommentAssign(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO tasks (id, title, status, created_at, updated_at, gone) VALUES (7, 't', 'todo', 1, 1, 0)`)
	code, loc := BoardPost(e.ctx(e.view), "/board/move", boardForm("task", "7", "to", "review", "back", "board"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/board?req=") {
		t.Fatal(code, loc)
	}
	p, _ := boardReadRequest(t, loc)
	if p.Get("task") != int64(7) || p.S("to") != "review" {
		t.Fatal(p.PyRepr())
	}
	if code, _ := BoardPost(e.ctx(e.view), "/board/move", boardForm("task", "7", "to", "fixer"), e.view); code != 400 {
		t.Fatal("fixer by move", code)
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/comment", boardForm("task", "7", "body", " hi\r\nthere "), e.view)
	if code != 303 || !strings.HasSuffix(loc, "#comments") {
		t.Fatal(code, loc)
	}
	if p, _ := boardReadRequest(t, loc); p.S("body") != "hi\nthere" {
		t.Fatal(p.PyRepr())
	}
	if code, _ := BoardPost(e.ctx(e.view), "/board/comment", boardForm("task", "7", "body", strings.Repeat("x", 4001)), e.view); code != 400 {
		t.Fatal("long comment")
	}
	if code, _ := BoardPost(e.ctx(e.view), "/board/assign", boardForm("task", "99"), e.view); code != 404 {
		t.Fatal("unknown task")
	}
}

func TestBoardFixerRules(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO tasks (id, title, status, env, fix_prompt, created_at, updated_at, gone) VALUES (1, 't', 'todo', 'release', 'p', 1, 1, 0)`)
	e.board.Exec(`INSERT INTO tasks (id, title, status, env, fix_prompt, created_at, updated_at, gone) VALUES (2, 't', 'todo', 'develop', '', 1, 1, 0)`)
	e.board.Exec(`INSERT INTO tasks (id, title, status, env, fix_prompt, created_at, updated_at, gone) VALUES (3, 't', 'todo', NULL, 'p', 1, 1, 0)`)
	try := func(s *Session, kv ...string) int {
		c, _ := BoardPost(e.ctx(s), "/board/fixer", boardForm(kv...), s)
		return c
	}
	if try(e.admin, "task", "2", "reviewed", "1") != 409 {
		t.Fatal("FIXER_TRIGGER=off (the default) retires the fixer column")
	}
	e.json(t, "out/board-status.json", map[string]any{"agent": map[string]any{"settings": map[string]any{"fixer_trigger": "column"}}})
	if try(e.view, "task", "2", "reviewed", "1") != 403 {
		t.Fatal("viewer")
	}
	if try(e.admin, "task", "1", "reviewed", "1") != 403 {
		t.Fatal("production")
	}
	if try(e.admin, "task", "3", "reviewed", "1") != 400 {
		t.Fatal("no env")
	}
	if try(e.admin, "task", "2") != 400 {
		t.Fatal("not reviewed")
	}
	if try(e.admin, "task", "2", "reviewed", "1") != 400 {
		t.Fatal("no fix prompt")
	}
}

func TestBoardReadOnlyConnectionBlocksWrites(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO tasks (id, title, status, created_at, updated_at, gone) VALUES (7, 't', 'todo', 1, 1, 0)`)
	e.json(t, "out/board-status.json", map[string]any{"configured": false})
	e.json(t, "out/jira-users.json", map[string]any{"users": map[string]any{"viewer1": map[string]any{"mode": "ro"}}})
	code, page := BoardPost(e.ctx(e.view), "/board/move", boardForm("task", "7", "to", "done"), e.view)
	if code != 403 || !strings.Contains(page, "read-only") {
		t.Fatal(code)
	}
	if ok, why := JiraCanWrite("alex.doe"); ok || !strings.Contains(why, "Connect Jira in Account") {
		t.Fatal("no connection while others connected", why)
	}
}

func TestBoardAdminActions(t *testing.T) {
	e := boardSetup(t)
	if c, _ := BoardPost(e.ctx(e.view), "/admin/board", boardForm("action", "sync"), e.view); c != 403 {
		t.Fatal("viewer")
	}
	c, loc := BoardPost(e.ctx(e.admin), "/admin/board", boardForm("action", "full"), e.admin)
	if c != 303 {
		t.Fatal(c)
	}
	if p, _ := boardReadRequest(t, loc); p.Get("full") != true || p.S("kind") != "sync" {
		t.Fatal(p.PyRepr())
	}
	if c, _ := BoardPost(e.ctx(e.admin), "/admin/board", boardForm("action", "x"), e.admin); c != 400 {
		t.Fatal("unknown")
	}
}

func TestBoardRequestBannerStates(t *testing.T) {
	e := boardSetup(t)
	e.json(t, "out/board-status.json", map[string]any{"requests": map[string]any{
		"20261021000000-abcdef12": map[string]any{"result": "refused: bad <x>"},
		"20261021000001-abcdef12": map[string]any{"result": "created", "task": 3}}})
	c := e.ctx(e.view)
	if b := BoardRequestBanner(c, py.NewQS("req", "20261021000000-abcdef12")); !strings.Contains(b, "Not applied: bad &lt;x&gt;") {
		t.Fatal(b)
	}
	if b := BoardRequestBanner(c, py.NewQS("req", "20261021000001-abcdef12")); !strings.Contains(b, `/board/task?id=3`) {
		t.Fatal(b)
	}
	c2 := e.ctx(e.view)
	if b := BoardRequestBanner(c2, py.NewQS("req", "20261021000009-abcdef12")); !strings.Contains(b, "Waiting") || c2.Refresh != true {
		t.Fatal(b)
	}
	if BoardRequestBanner(c, py.NewQS("req", "../x")) != "" {
		t.Fatal("bad id")
	}
}

func TestBoardRootCauseSection(t *testing.T) {
	body, ok := boardRootCause("# T\n## Root cause  \n\nIt broke.\n## Fix\n1. x")
	if !ok || body != "It broke.\n" {
		t.Fatalf("%q", body)
	}
	if _, ok := boardRootCause("## Root causes\nx"); ok {
		t.Fatal("no match expected")
	}
	if body, _ := boardRootCause("## Root cause\nend"); body != "end" {
		t.Fatalf("%q", body)
	}
}
