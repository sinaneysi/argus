package app

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lessons table as bin/lessons.py SCHEMA creates it (root writer).
const lessonsTestSchema = `CREATE TABLE IF NOT EXISTS lessons (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL,
 service TEXT, env TEXT, kind TEXT, tags TEXT, norm_hash TEXT UNIQUE, state TEXT NOT NULL, source_key TEXT, source_run INTEGER,
 evidence TEXT, proposed_at INTEGER, decided_by TEXT, decided_at INTEGER, uses INTEGER DEFAULT 0, last_used INTEGER,
 unhelpful INTEGER DEFAULT 0, supersedes INTEGER, review TEXT)`

func lessonsSeed(t *testing.T, path string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, st := range []string{lessonsTestSchema, "CREATE TABLE IF NOT EXISTS state (k TEXT PRIMARY KEY, v TEXT)",
		`INSERT INTO lessons (id, title, body, service, env, kind, tags, norm_hash, state, source_key, proposed_at, review) VALUES
		 (1, 'stage3 disk fills from images', 'Prune images older than 7 days.', 'ops/stage3', 'develop', 'change', '["disk"]', 'h1', 'approved', 'OPS-7', 1, NULL),
		 (2, 'proposed <b>idea</b>', 'not yet reviewed', NULL, NULL, NULL, '[]', 'h2', 'proposed', 'gl-12', 2, 'new'),
		 (3, 'rejected idea', 'never shown', NULL, NULL, NULL, '[]', 'h3', 'rejected', NULL, 3, NULL),
		 (4, 'retired idea', 'kept for audit', NULL, NULL, NULL, '[]', 'h4', 'retired', NULL, 4, NULL)`} {
		if _, err := b.Exec(st); err != nil {
			t.Fatal(err, st)
		}
	}
}

func TestLessonsPostWritesValidatedRequests(t *testing.T) {
	e := boardSetup(t)
	c := &Ctx{Sess: e.admin, Theme: "auto", Path: "/admin/lessons"}
	if code, _ := LessonsPost(c, "/admin/lessons/act", boardForm("action", "approve", "id", "2"), e.view); code != 403 {
		t.Fatal("viewer", code)
	}
	code, loc := LessonsPost(c, "/admin/lessons/act", boardForm("action", "approve", "id", "2", "return", "/admin/lessons?view=approved"), e.admin)
	if code != 303 || !strings.HasPrefix(loc, "/admin/lessons?view=approved&req=") {
		t.Fatal(code, loc)
	}
	if p, _ := boardReadRequest(t, loc); p.S("kind") != "lesson" || p.S("action") != "approve" || !strings.Contains(p.PyRepr(), "'lesson_id': 2") || p.S("by") != "alex.doe" {
		t.Fatal(p.PyRepr())
	}
	for _, bad := range [][]string{{"action", "approve-all", "id", "2"}, {"action", "approve", "id", "x"},
		{"action", "edit", "id", "2", "title", "", "body", "b"},
		{"action", "edit", "id", "2", "title", "t", "body", "b", "service", "../etc"},
		{"action", "edit", "id", "2", "title", "t", "body", "b", "tags", "a/b"},
		{"action", "edit", "id", "2", "title", "t", "body", strings.Repeat("x", 1501)}} {
		if code, _ := LessonsPost(c, "/admin/lessons/act", boardForm(bad...), e.admin); code != 400 {
			t.Fatal(bad, code)
		}
	}
	code, loc = LessonsPost(c, "/admin/lessons/act", boardForm("action", "edit", "id", "2", "title", " a  title ", "body", "b\r\nc",
		"service", "ops/stage3", "env", "develop", "kind", "change", "tags", "disk docker"), e.admin)
	p, _ := boardReadRequest(t, loc)
	if code != 303 || !strings.Contains(p.PyRepr(), "'title': 'a title'") || !strings.Contains(p.PyRepr(), "'body': 'b\\nc'") {
		t.Fatal(code, p.PyRepr())
	}
	code, loc = LessonsPost(c, "/admin/lessons/settings", boardForm(), e.admin)
	if p, _ := boardReadRequest(t, loc); code != 303 || p.S("kind") != "lesson-settings" || !strings.Contains(p.PyRepr(), "'enabled': False") {
		t.Fatal(p.PyRepr())
	}
}

func TestLessonsRoutesRoleCSRFAndMFA(t *testing.T) {
	for _, p := range []string{"/api/v1/lessons/act", "/api/v1/lessons/settings"} {
		if POST_ROUTES[p] != PolicySensitive {
			t.Fatal(p, "must be sensitive (it decides what reaches prompts)")
		}
	}
	e := srvSetup(t)
	lessonsSeed(t, BoardDBPath())
	vs, vcsrf := e.login("view.er")

	if r := e.req("POST", "/api/v1/lessons/act", map[string]string{"opsr_session": vs},
		url.Values{"csrf": {vcsrf}, "action": {"approve"}, "id": {"2"}}, nil); r.status != 403 {
		t.Fatal("viewer POST", r.status)
	}

	as, acsrf := e.login("admin")
	if r := e.req("POST", "/api/v1/lessons/act", map[string]string{"opsr_session": as},
		url.Values{"csrf": {acsrf + "x"}, "action": {"approve"}, "id": {"2"}}, nil); r.status != 403 {
		t.Fatal("bad csrf", r.status)
	}
	r := e.req("POST", "/api/v1/lessons/act", map[string]string{"opsr_session": as},
		url.Values{"csrf": {acsrf}, "action": {"approve"}, "id": {"2"}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/review?kind=lessons&req=") {
		t.Fatal("admin POST", r.status, r.loc)
	}
	ns, ncsrf := e.loginNoMFA("admin")
	if r := e.req("POST", "/api/v1/lessons/act", map[string]string{"opsr_session": ns},
		url.Values{"csrf": {ncsrf}, "action": {"approve"}, "id": {"2"}}, nil); r.status == 200 {
		t.Fatal("sensitive POST without 2FA went through")
	}
}

// /api/v1/knowledge shows approved lessons only: a proposed, rejected or retired lesson never reaches a reader.
func TestAPIv1KnowledgeApprovedOnly(t *testing.T) {
	e := boardSetup(t)
	lessonsSeed(t, BoardDBPath())
	v, er := apiKnowledgeJSON(boardForm(), e.view)
	if er != nil {
		t.Fatal(er.msg)
	}
	b, _ := json.Marshal(v)
	k := string(b)
	if !strings.Contains(k, "stage3 disk fills from images") || !strings.Contains(k, "ops/stage3") {
		t.Fatal("approved lesson missing", k)
	}
	for _, no := range []string{"proposed", "rejected idea", "retired idea", "never shown", "kept for audit"} {
		if strings.Contains(k, no) {
			t.Fatal("/api/v1/knowledge shows", no)
		}
	}
}
