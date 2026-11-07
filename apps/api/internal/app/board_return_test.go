package app

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func TestBoardReturnValidation(t *testing.T) {
	ok := map[string]string{
		"/board":                                   "/board",
		"/board?tracker=gitlab&env=develop":        "/board?tracker=gitlab&env=develop",
		"/board?tracker=gitlab&req=abc&show=mine":  "/board?tracker=gitlab&show=mine",
		"/board/task?id=7&req=x":                   "/board/task?id=7",
		"/admin/board":                             "/admin/board",
		"/proposals?view=decided":                  "/proposals?view=decided",
		"/board?qf=1&qf=2&show=all":                "/board?qf=1&qf=2&show=all",
		"/board?label=" + strings.Repeat("a", 400): "/board?label=" + strings.Repeat("a", 400),
	}
	for in, want := range ok {
		if got := BoardReturn(in); got != want {
			t.Errorf("BoardReturn(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "https://evil.example/board", "//evil.example/board", "///board", "/\\evil.example",
		"board", "/boardx", "/etc/passwd", "/logout", "/board/../logout", "/board/%2e%2e/logout", "/board//x", "/board#x",
		"/board?x=1\n", "/board x", "javascript:alert(1)", "/admin/users", "/board?" + strings.Repeat("a", 600)} {
		if got := BoardReturn(bad); got != "" {
			t.Errorf("BoardReturn(%q) = %q, want refused", bad, got)
		}
	}
}

func TestBoardReturnFieldOnForms(t *testing.T) {

	if h := boardNewHref("/board?tracker=gitlab"); h != "/board/new?return=%2Fboard%3Ftracker%3Dgitlab" {
		t.Fatal(h)
	}
	if h := boardNewHref("/board"); h != "/board/new" {
		t.Fatal(h)
	}
}

func TestBoardFilterChipsApplyOnClick(t *testing.T) {
	_ = boardSetup(t)

	// every filter is a link that applies on click: no form, no select, no submit button (the operator's rule)

	// "mine" keeps every other parameter, drops the stale request banner

	// choosing another environment replaces env only

	// "all" drops env and keeps the rest

	// chosen chip: aria-current and a check mark, not colour only

	// advanced: open while one is set, says how many; each chip changes its one parameter and keeps the others

	// one current chip per link group (show, env, + tracker when GitLab is in use)

	// the active filters: one row, each removable on its own, then Clear all (keeps the tracker tab)

	// nothing active: closed, no active row

	if h := boardChipHref(py.ParseQS("req=x"), "show", ""); h != "/board" {
		t.Fatal(h)
	}
}

func TestBoardPostReturnsToTheView(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO tasks (id, title, status, created_at, updated_at, gone) VALUES (7, 't', 'todo', 1, 1, 0)`)
	view := "/board?tracker=gitlab&env=develop"
	code, loc := BoardPost(e.ctx(e.view), "/board/move", boardForm("task", "7", "to", "review", "back", "board", "return", view+"&req=old"), e.view)
	if code != 303 || !strings.HasPrefix(loc, view+"&req=") || strings.Contains(loc, "old") {
		t.Fatal(code, loc)
	}
	if p, _ := boardReadRequest(t, loc); p.S("to") != "review" {
		t.Fatal(p.PyRepr())
	}
	// invalid or hostile return values fall back to the default page
	for _, bad := range []string{"https://evil.example/board", "//evil.example", "/\\evil.example", "/logout", "/board/../x"} {
		code, loc = BoardPost(e.ctx(e.view), "/board/move", boardForm("task", "7", "to", "review", "back", "board", "return", bad), e.view)
		if code != 303 || !strings.HasPrefix(loc, "/board?req=") {
			t.Fatal(bad, code, loc)
		}
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/comment", boardForm("task", "7", "body", "hi", "return", "/board/task?id=7&all=1"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/board/task?id=7&all=1&req=") || !strings.HasSuffix(loc, "#comments") {
		t.Fatal(code, loc)
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/assign", boardForm("task", "7", "return", view), e.view)
	if code != 303 || !strings.HasPrefix(loc, view+"&req=") {
		t.Fatal(code, loc)
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/assign", boardForm("task", "7"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/board/task?id=7&req=") {
		t.Fatal("default", code, loc)
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/refresh", boardForm("return", "/board?show=mine&qf=3"), e.view)
	if code != 303 || !strings.HasPrefix(loc, "/board?show=mine&qf=3&req=") {
		t.Fatal(code, loc)
	}
	code, loc = BoardPost(e.ctx(e.view), "/board/create", boardForm("title", "x", "priority", "Medium", "return", view), e.view)
	if code != 303 || !strings.HasPrefix(loc, view+"&req=") {
		t.Fatal(code, loc)
	}
	code, loc = BoardPost(e.ctx(e.admin), "/admin/board", boardForm("action", "sync", "return", "/admin/board?all=1"), e.admin)
	if code != 303 || !strings.HasPrefix(loc, "/admin/board?all=1&req=") {
		t.Fatal(code, loc)
	}
	// the role checks run before the return field is looked at
	if c, _ := BoardPost(e.ctx(e.view), "/admin/board", boardForm("action", "sync", "return", "/admin/board"), e.view); c != 403 {
		t.Fatal("viewer", c)
	}
	// a failed create is a plain refusal; the form stays on the web page
	code, msg := BoardPost(e.ctx(e.view), "/board/create", boardForm("title", "", "priority", "Medium", "return", view), e.view)
	if code != 400 || !strings.Contains(msg, "The title must be") {
		t.Fatal(code, msg)
	}
}

func TestProposalPostReturnsToTheView(t *testing.T) {
	e := boardSetup(t)
	code, loc := ProposalsPost(e.ctx(e.admin), "/proposals/settings",
		boardForm("expiry_days", "7", "reject_days", "7", "return", "/proposals?view=decided"), e.admin)
	if code != 303 || !strings.HasPrefix(loc, "/proposals?view=decided&req=") {
		t.Fatal(code, loc)
	}
	code, loc = ProposalsPost(e.ctx(e.admin), "/proposals/settings",
		boardForm("expiry_days", "7", "reject_days", "7", "return", "https://evil.example/proposals"), e.admin)
	if code != 303 || !strings.HasPrefix(loc, "/review?kind=proposals&req=") {
		t.Fatal(code, loc)
	}
}
