package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitlabConnected(t *testing.T, e *boardTestEnv, def string) {
	t.Helper()
	e.json(t, "out/gitlab-issues-status.json", map[string]any{"connected": true,
		"connection": map[string]any{"url": "https://gitlab.example.com", "project_path": "ops/ops-tasks",
			"web_url": "https://gitlab.example.com/ops/ops-tasks", "board_id": 3, "board_name": "Ops",
			"bot": map[string]any{"username": "project_7_bot", "bot": true}, "connected_at": 1, "by": "alex.doe"},
		"lists":    []any{map[string]any{"label": "Doing", "column": "progress"}},
		"settings": map[string]any{"default_tracker": def}, "allowed_hosts": []any{"gitlab.example.com"}})
}

func gitlabTasks(t *testing.T, e *boardTestEnv) {
	t.Helper()
	now := NowInt()
	for _, q := range []string{
		"INSERT INTO tasks (id, jira_key, title, status, created_at, updated_at) VALUES (1, 'OPS-12', 'Jira one', 'todo', ?, ?)",
		"INSERT INTO tasks (id, tracker, gl_project, gl_iid, gl_url, title, status, jira_status, created_at, updated_at) " +
			"VALUES (2, 'gitlab', 7, 45, 'https://gitlab.example.com/ops/ops-tasks/-/issues/45', 'GitLab one', 'progress', 'Doing', ?, ?)",
	} {
		if _, err := e.board.Exec(q, now, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitLabPost(t *testing.T) {
	e := boardSetup(t)
	os.MkdirAll(filepath.Join(e.dir, "in", "gitlab-issues"), 0o755)
	if c, _ := GitLabPost(e.ctx(e.view), boardForm("action", "sync"), e.view); c != 403 {
		t.Fatalf("viewer: %d", c)
	}
	for _, f := range [][]string{
		{"action", "connect", "project", "ops/ops-tasks", "token", "short"},
		{"action", "connect", "project", "", "token", "glpat-abcdefghijklmnopqrstuvwxyz"},                                        // secret-scan: allow
		{"action", "connect", "url", "http://gitlab.example.com", "project", "a/b", "token", "glpat-abcdefghijklmnopqrstuvwxyz"}, // secret-scan: allow
		{"action", "board", "board", "x"},
		{"action", "settings", "default_tracker", "trello"},
		{"action", "settings", "list_map", "QA=qa"},
		{"action", "nope"},
	} {
		if c, _ := GitLabPost(e.ctx(e.admin), boardForm(f...), e.admin); c != 400 {
			t.Fatalf("%v: %d", f, c)
		}
	}
	tok := "glpat-abcdefghijklmnopqrstuvwxyz" // secret-scan: allow
	c, loc := GitLabPost(e.ctx(e.admin), boardForm("action", "connect", "project", "https://gitlab.example.com/ops/ops-tasks/-/boards/3",
		"token", tok), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/admin/outline?g=") || !strings.HasSuffix(loc, "#gitlab") {
		t.Fatalf("%d %s", c, loc)
	}
	rid := strings.TrimSuffix(strings.TrimPrefix(loc, "/admin/outline?g="), "#gitlab")
	p := filepath.Join(e.dir, "in", "gitlab-issues", "alex.doe-"+rid+".json")
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("request file: %v %v", st, err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), tok) || !strings.Contains(string(data), `"kind": "connect"`) {
		t.Fatalf("payload %s", data)
	}
	if PolicyOf("/admin/gitlab-issues", e.admin) != PolicySensitive {
		t.Fatal("the GitLab connect form must need a fresh 2FA code")
	}

}

func TestGitLabCreateAndWriteBlocks(t *testing.T) {
	e := boardSetup(t)
	gitlabTasks(t, e)
	if c, _ := BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t", "priority", "Medium", "tracker", "gitlab"), e.admin); c != 403 {
		t.Fatalf("GitLab not connected: %d", c)
	}
	if c, _ := BoardPost(e.ctx(e.admin), "/board/move", boardForm("task", "2", "to", "done"), e.admin); c != 403 {
		t.Fatalf("move GitLab task while not connected: %d", c)
	}

	gitlabConnected(t, e, "gitlab")

	c, loc := BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t", "priority", "Medium", "tracker", "gitlab"), e.admin)
	if c != 303 {
		t.Fatalf("%d %s", c, loc)
	}
	req, _ := boardReadRequest(t, loc)
	if req.Get("tracker") != "gitlab" {
		t.Fatalf("tracker %v", req.Get("tracker"))
	}
	c, loc = BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t2", "priority", "Medium", "tracker", "jira"), e.admin)
	req, _ = boardReadRequest(t, loc)
	if c != 303 || req.Has("tracker") {
		t.Fatal("a Jira create request keeps its old shape")
	}
	if c, _ := BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t", "priority", "Medium", "tracker", "trello"), e.admin); c != 400 {
		t.Fatalf("unknown tracker: %d", c)
	}
	if c, _ := BoardPost(e.ctx(e.admin), "/board/move", boardForm("task", "2", "to", "done"), e.admin); c != 303 {
		t.Fatalf("move GitLab task: %d", c)
	}
}

func gitlabGroupConnected(t *testing.T, e *boardTestEnv, def string, defProject int) {
	t.Helper()
	conn := map[string]any{"mode": "group", "url": "https://gitlab.example.com", "group_id": 50, "group_path": "technogroup/devops",
		"web_url": "https://gitlab.example.com/groups/technogroup/devops", "board_id": 9, "board_name": "DevOps",
		"bot": map[string]any{"username": "group_50_bot", "bot": true}, "connected_at": 1, "by": "alex.doe"}
	if defProject > 0 {
		conn["default_project_id"], conn["default_project_path"] = defProject, "technogroup/devops/infra"
	}
	e.json(t, "out/gitlab-issues-status.json", map[string]any{"connected": true, "connection": conn,
		"projects": []any{map[string]any{"id": 7, "path": "technogroup/devops/ops-tasks", "short": "ops-tasks"},
			map[string]any{"id": 8, "path": "technogroup/devops/infra", "short": "infra"}},
		"lists":    []any{map[string]any{"label": "Doing", "column": "progress"}},
		"settings": map[string]any{"default_tracker": def}, "allowed_hosts": []any{"gitlab.example.com"}})
}

func TestGitLabGroupBoard(t *testing.T) {
	e := boardSetup(t)
	os.MkdirAll(filepath.Join(e.dir, "in", "gitlab-issues"), 0o755)
	gitlabGroupConnected(t, e, "gitlab", 0)

	if c, _ := GitLabPost(e.ctx(e.admin), boardForm("action", "project", "project", "x"), e.admin); c != 400 {
		t.Fatalf("bad project: %d", c)
	}
	c, loc := GitLabPost(e.ctx(e.admin), boardForm("action", "project", "project", "8"), e.admin)
	if c != 303 {
		t.Fatalf("%d %s", c, loc)
	}
	rid := strings.TrimSuffix(strings.TrimPrefix(loc, "/admin/outline?g="), "#gitlab")
	data, _ := os.ReadFile(filepath.Join(e.dir, "in", "gitlab-issues", "alex.doe-"+rid+".json"))
	if !strings.Contains(string(data), `"kind": "project"`) || !strings.Contains(string(data), `"project": "8"`) {
		t.Fatalf("payload %s", data)
	}

	// New task: the GitLab project select, the default project preselected; a project outside the board is refused
	gitlabGroupConnected(t, e, "gitlab", 8)

	c, loc = BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t", "priority", "Medium", "tracker", "gitlab",
		"gl_project", "7"), e.admin)
	if c != 303 {
		t.Fatalf("%d %s", c, loc)
	}
	req, _ := boardReadRequest(t, loc)
	if req.Get("gl_project") != "7" {
		t.Fatalf("gl_project %v", req.Get("gl_project"))
	}
	c, page := BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t", "priority", "Medium", "tracker", "gitlab",
		"gl_project", "99"), e.admin)
	if c != 400 || !strings.Contains(page, "Choose a GitLab project of the connected board") {
		t.Fatalf("project outside the board: %d", c)
	}
	c, loc = BoardPost(e.ctx(e.admin), "/board/create", boardForm("title", "t3", "priority", "Medium", "tracker", "jira",
		"gl_project", "7"), e.admin)
	req, _ = boardReadRequest(t, loc)
	if c != 303 || req.Has("gl_project") || req.Has("tracker") {
		t.Fatal("a Jira create request keeps its old shape")
	}
	if PolicyOf("/admin/gitlab-issues", e.admin) != PolicySensitive {
		t.Fatal("the GitLab form stays sensitive")
	}
}

func TestGitLabGroupRef(t *testing.T) {
	e := boardSetup(t)
	now := NowInt()
	if _, err := e.board.Exec("INSERT INTO tasks (id, tracker, gl_project, gl_iid, gl_ref, gl_url, title, status, created_at, "+
		"updated_at) VALUES (3, 'gitlab', 8, 45, 'infra#45', 'https://gitlab.example.com/technogroup/devops/infra/-/issues/45', "+
		"'Group one', 'todo', ?, ?)", now, now); err != nil {
		t.Fatal(err)
	}

}
