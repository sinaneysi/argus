package app

// Settings → Integrations as JSON: admins only, the Go routes' policy and limit, a refused save names the field and never
// echoes the key or token, nothing else from root's status files reaches an answer.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1IntegrationsPolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/admin/outline": "/admin/outline", "/api/v1/admin/gitlab-issues": "/admin/gitlab-issues"}, map[string]any{"action": "sync"}, false)
}

func TestAPIv1IntegrationsRead(t *testing.T) {
	e := iaSetup(t)
	ck := v1Admins(t, e, "/api/v1/admin/outline")
	ol := `{"token":"` + v1FakeCred + `","connection":{"url":"https://docs.example","user":"u","team":"t","collection":"Services","connected_at":1800000000,"by":"a","token":"` + v1FakeCred + `"},
	"settings":{"mode":"publish","scope":"all"},"allowed_hosts":["docs.example"],"last_run":{"at":1800000000,"counts":{"updated":2}},"pages":{"a/b":{"state":"updated","url":"https://docs.example/d/1","flags":["human-edit"]},"c/d":{"url":"javascript:alert(1)"}}}`
	gl := `{"connected":true,"allowed_hosts":["gitlab.example"],"warnings":["check this"],"settings":{"default_tracker":"gitlab","list_map":{"QA":"review"},"user_map":{"a":"b","c":"d"}},
	"connection":{"mode":"group","url":"https://gitlab.example","group_path":"g/h","web_url":"javascript:alert(1)","board_id":9,"board_name":"Ops","bot":{"username":"bot1","bot":true},"token":"` + v1FakeCred + `","default_project_id":7,"default_project_path":"g/h/p"},
	"boards":[{"id":9,"name":"Ops"},{"id":10,"name":"Dev"}],"projects":[{"id":7,"path":"g/h/p","short":"p"}],"lists":[{"label":"QA","column":"review"}]}`
	for n, b := range map[string]string{"outline-status.json": ol, "gitlab-issues-status.json": gl} {
		if err := os.WriteFile(filepath.Join(SET_OUT, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := sql.Open("sqlite", DB)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	if _, err := ops.Exec("DELETE FROM outline_pages"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"_index", "a/b", "c/d"} {
		ops.Exec(`INSERT OR REPLACE INTO outline_pages(service,title,body,body_hash,prev_body,prev_hash,rendered_at,changed_at,redacted,blocked) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			s, "T "+s, "# Hello\n> quoted\nnew line\n", "h1", "# Hello\nold line\n", "h0", 1800000000, 1800000000, `{"token":2}`, If(s == "c/d", "secret found", ""))
	}
	r := e.req("GET", "/api/v1/admin/outline", ck, nil, nil)
	d := v1JSON(t, r)
	if strings.Contains(r.body, v1FakeCred) || strings.Contains(r.body, "javascript:") || strings.Contains(r.body, "<span") {
		t.Fatal(r.body)
	}
	if d["connected"] != true || d["mode"] != "publish" || d["built"].(float64) != 2 || d["connection"].(map[string]any)["team"] != "t" || d["user"] == "" {
		t.Fatal(r.body)
	}
	p := d["pages"].([]any)
	if len(p) != 3 || p[0].(map[string]any)["name"] != "Services overview" || p[1].(map[string]any)["url"] != "https://docs.example/d/1" || p[2].(map[string]any)["local"].(map[string]any)["text"] != "blocked" {
		t.Fatal(p)
	}
	g := d["gitlab"].(map[string]any)
	c := g["connection"].(map[string]any)
	if g["group"] != true || c["where"] != "g/h" || c["web_url"] != "" || g["project"] != "groups/g/h" || g["user_map"] != "a=b, c=d" || g["list_map"] != "QA=review" || g["default_project"] != "7" {
		t.Fatal(g)
	}
	if len(g["boards"].([]any)) != 2 || len(g["projects"].([]any)) != 1 || len(g["lists"].([]any)) != 3 || len(g["notices"].([]any)) == 0 {
		t.Fatal(g)
	}
	v := v1JSON(t, e.req("GET", "/api/v1/admin/outline/page?s=a/b", ck, nil, nil))
	if v["scan"] != "redacted token ×2" || v["outline"].(map[string]any)["url"] != "https://docs.example/d/1" || strings.Contains(v["view"].(string), ">") || !strings.Contains(v["body"].(string), "> quoted") {
		t.Fatal(v)
	}
	lines := v["diff"].(map[string]any)["lines"].([]any)
	if len(lines) == 0 || !strings.Contains(strings.Join(func() []string {
		o := []string{}
		for _, l := range lines {
			o = append(o, l.(map[string]any)["kind"].(string))
		}
		return o
	}(), " "), "add") {
		t.Fatal(lines)
	}
	if b := v1JSON(t, e.req("GET", "/api/v1/admin/outline/page?s=c/d", ck, nil, nil)); b["blocked"] != true || b["outline"].(map[string]any)["url"] != "" {
		t.Fatal(b)
	}
	for _, s := range []string{"nope/x", "../x"} {
		if r := e.req("GET", "/api/v1/admin/outline/page?s="+s, ck, nil, nil); r.status != 404 {
			t.Fatal(s, r.status)
		}
	}
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/admin/outline/page?s=a/b", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
}

func TestAPIv1IntegrationsWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	for _, d := range []string{"outline-requests", "gitlab-issues"} {
		if err := os.MkdirAll(filepath.Join(SET_IN, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		api, form, field string
		body             map[string]any
	}{
		{"outline", "outline-connect", "url", map[string]any{"action": "connect", "url": "ftp://x", "token": v1FakeCred}},
		{"outline", "outline-connect", "token", map[string]any{"action": "connect", "url": "https://d.example", "token": "short"}},
		{"outline", "outline-settings", "mode", map[string]any{"action": "settings", "mode": "x"}},
		{"outline", "outline-hosts", "hosts", map[string]any{"action": "hosts", "hosts": "https://bad/"}},
		{"gitlab-issues", "gitlab-connect", "project", map[string]any{"action": "connect", "project": "", "token": v1FakeCred}},
		{"gitlab-issues", "gitlab-connect", "token", map[string]any{"action": "connect", "project": "g/p", "token": "x y"}},
		{"gitlab-issues", "gitlab-board", "board", map[string]any{"action": "board", "board": "x"}},
		{"gitlab-issues", "gitlab-settings", "list_map", map[string]any{"action": "settings", "list_map": "a=nope"}},
		{"gitlab-issues", "gitlab-hosts", "hosts", map[string]any{"action": "hosts", "hosts": "https://bad/"}},
	}
	for _, c := range cases {
		r := e.v1Post("/api/v1/admin/"+c.api, ck, c.body, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != c.field || m["form"] != c.form || strings.Contains(r.body, v1FakeCred) {
			t.Fatal(c.field, r.status, r.body)
		}
	}
	for _, d := range []string{"outline-requests", "gitlab-issues"} {
		if l, _ := filepath.Glob(filepath.Join(SET_IN, d, "*")); len(l) != 0 {
			t.Fatal("a refused save wrote a request", l)
		}
	}
	for api, want := range map[string]string{"outline": "/admin/outline?r=", "gitlab-issues": "/admin/outline?g="} {
		if r := e.v1Post("/api/v1/admin/"+api, ck, map[string]any{"action": "nope"}, nil); r.status != 400 {
			t.Fatal(r.status)
		}
		r := e.v1Post("/api/v1/admin/"+api, ck, map[string]any{"action": "sync"}, nil)
		if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), want) {
			t.Fatal(r.status, r.body)
		}
	}
}
