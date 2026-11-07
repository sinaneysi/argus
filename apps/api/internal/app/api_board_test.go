package app

// The board's JSON API on the demo test data: reads need a session and have their shape, write controls are absent for a
// viewer, every write passes CSRF, role, 2FA and the demo refusal, and drops the same request file as the Go route.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var v1BoardWrites = []string{"/api/v1/board/create", "/api/v1/board/comment", "/api/v1/board/move", "/api/v1/board/assign", "/api/v1/board/fixer",
	"/api/v1/board/jmove", "/api/v1/board/refresh", "/api/v1/board/agent", "/api/v1/board/agent/spinup", "/api/v1/board/agent/chat",
	"/api/v1/board/task/secret", "/api/v1/board/mxpreview"}

func boardFirstTask(t *testing.T) (int64, string) {
	t.Helper()
	r := BoardBQ("SELECT t.id, t.jira_key FROM tasks t JOIN agent_tasks a ON a.key=t.jira_key WHERE t.gone=0 AND a.plan IS NOT NULL ORDER BY t.id LIMIT 1")
	if len(r) == 0 {
		t.Fatal("the demo has no task with a plan")
	}
	return r[0].I("id"), r[0].Str("jira_key")
}

func boardRequests(t *testing.T) []map[string]any {
	t.Helper()
	l, _ := filepath.Glob(filepath.Join(SET_IN, "board-requests", "*.json"))
	out := []map[string]any{}
	for _, f := range l {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if json.Unmarshal(b, &m) != nil {
			t.Fatal(f)
		}
		out = append(out, m)
	}
	return out
}

func TestAPIv1BoardReads(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	id, key := boardFirstTask(t)
	get := func(tok, u string) map[string]any {
		t.Helper()
		r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil)
		if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
			t.Fatal(u, r.status, r.body)
		}
		return v1JSON(t, r)
	}
	for _, u := range []string{"/api/v1/board", "/api/v1/board/task?id=" + I2S(id), "/api/v1/board/new"} {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal("no session", u, r.status)
		}
	}
	b := get(adm, "/api/v1/board?show=all")
	cols := b["columns"].([]any)
	if b["mode"] != "jira" || len(cols) == 0 || b["canRefresh"] != true || b["return"] != "/board?show=all" {
		t.Fatal(v1Str(b))
	}
	cards, moves := 0, 0
	for _, c := range cols {
		for _, k := range c.(map[string]any)["cards"].([]any) {
			cards++
			if k.(map[string]any)["move"] != nil {
				moves++
			}
		}
	}
	if cards == 0 {
		t.Fatal(v1Str(b))
	}
	for _, c := range get(vw, "/api/v1/board?show=all")["columns"].([]any) {
		for _, k := range c.(map[string]any)["cards"].([]any) {
			if k.(map[string]any)["move"] != nil {
				t.Fatal("a viewer got a Move control")
			}
		}
	}
	l := get(adm, "/api/v1/board?tracker=all")
	if l["mode"] != "local" || l["filters"] == nil || l["results"] == "" {
		t.Fatal(v1Str(l))
	}
	d := get(adm, "/api/v1/board/task?key="+key)
	ag := d["agent"].(map[string]any)
	if d["ref"] != key || d["record"] == nil || ag["plan"] == nil || ag["spin"] == nil || ag["chat"] == nil {
		t.Fatal(v1Str(d))
	}
	if v := get(vw, "/api/v1/board/task?key="+key)["agent"].(map[string]any); v["spin"] != nil || v["chat"].(map[string]any)["canPost"] != false {
		t.Fatal(v1Str(v))
	}
	if r := e.req("GET", "/api/v1/board/task?id=999999", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
	if n := get(adm, "/api/v1/board/new"); n["values"] == nil || len(n["envs"].([]any)) < 2 {
		t.Fatal(v1Str(n))
	}
	if r := e.req("GET", "/api/v1/board/new?src=nonsense&id=1", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1BoardWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(s string) map[string]string { return map[string]string{auth.COOKIE: s} }
	id, key := boardFirstTask(t)
	for _, p := range v1BoardWrites {
		if r := e.v1Post(p, nil, map[string]any{}, nil); r.status != 401 {
			t.Fatal("no session", p, r.status)
		}
		if r := e.req("POST", p, ck(adm), nil, nil); r.status != 403 {
			t.Fatal("no csrf", p, r.status)
		}
		if r := e.req("GET", p, ck(adm), nil, nil); r.status != 405 {
			t.Fatal(p, r.status)
		}
	}
	// admins only, as on the Go routes
	for p, b := range map[string]map[string]any{"/api/v1/board/fixer": {"task": id}, "/api/v1/board/agent": {"key": key, "action": "stop"},
		"/api/v1/board/agent/spinup": {"key": key, "plan_version": 1}, "/api/v1/board/agent/chat": {"key": key, "text": "hello"},
		"/api/v1/board/task/secret": {"key": key, "name": "API_KEY"}, "/api/v1/board/mxpreview": {"pid": "x", "action": "discard"}} {
		if r := e.v1Post(p, ck(vw), b, nil); r.status != 403 {
			t.Fatal(p, r.status, r.body)
		}
	}
	r := e.v1Post("/api/v1/board/comment", ck(adm), map[string]any{"task": id, "body": "Checked on the Northwind side", "return": "/board/task?id=" + I2S(id)}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["ok"] != true || !strings.Contains(m["redirect"].(string), "req=") || !strings.HasSuffix(m["redirect"].(string), "#comments") {
		t.Fatal(r.status, r.body)
	}
	if q := boardRequests(t); len(q) != 1 || q[0]["kind"] != "comment" || q[0]["by"] != "admin" {
		t.Fatal(q)
	}
	r = e.v1Post("/api/v1/board/comment", ck(adm), map[string]any{"task": id, "body": "x", "return": "https://elsewhere.example/"}, nil)
	if m := v1JSON(t, r); r.status != 200 || strings.Contains(m["redirect"].(string), "elsewhere") {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/board/comment", ck(adm), map[string]any{"task": id, "body": " "}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/board/move", ck(adm), map[string]any{"task": 999999, "to": "done"}, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/board/jmove", ck(adm), map[string]any{"key": key, "col": 99}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	// a full-size task passes the API's body limit, as it passes the Go route's
	big := strings.Repeat("evidence line\n", 2000)
	before := len(boardRequests(t))
	r = e.v1Post("/api/v1/board/create", ck(adm), map[string]any{"title": "Rotate the Northwind cache", "priority": "Medium", "evidence": big}, nil)
	if r.status != 200 || len(boardRequests(t)) != before+1 {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/board/create", ck(adm), map[string]any{"title": " ", "priority": "Medium"}, nil)
	if m := v1JSON(t, r); r.status != 400 || !strings.Contains(m["error"].(string), "title") {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/board/agent/spinup", ck(adm), map[string]any{"key": key, "plan_version": 9999}, nil); r.status != 409 {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/comment", ck(adm), map[string]any{}, nil); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestAPIv1BoardChatAndSecret(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	_, key := boardFirstTask(t)
	r := e.v1Post("/api/v1/board/agent/chat", ck, map[string]any{"key": key, "text": "What is the plan?"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasSuffix(m["redirect"].(string), "#chat") {
		t.Fatal(r.status, r.body)
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "task-chat", "*.json")); len(l) != 1 {
		t.Fatal(l)
	}
	if r := e.v1Post("/api/v1/board/agent/chat", ck, map[string]any{"key": key, "text": strings.Repeat("x", 4001)}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	// a pending turn: the page refreshes and the composer waits
	b, err := sql.Open("sqlite", BoardDBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Exec("INSERT INTO task_chat_turns(key, state) VALUES(?, 'queued')", key); err != nil {
		t.Fatal(err)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/board/task?key="+key+"&live=0", ck, nil, nil))
	if c := d["agent"].(map[string]any)["chat"].(map[string]any); c["pending"] != true || d["refresh"] == nil {
		t.Fatal(v1Str(d))
	}
	// the secret store is off in the demo data: refused, and the value appears in no answer
	r = e.v1Post("/api/v1/board/task/secret", ck, map[string]any{"key": key, "name": "API_KEY", "value": "not-a-real-value-123", "hosts": "cache-01", "ttl_min": 10}, nil)
	if r.status != 409 || strings.Contains(r.body, "not-a-real-value") {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1BoardMFAAndDemo(t *testing.T) {
	e := iaSetup(t)
	nomfa, _ := e.loginNoMFA("admin")
	srvEnrol(t, nomfa)
	ck := map[string]string{auth.COOKIE: nomfa}
	id, key := boardFirstTask(t)
	r := e.v1Post("/api/v1/board/comment", ck, map[string]any{"task": id, "body": "x"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != false {
		t.Fatal(r.status, r.body)
	}
	for _, p := range []string{"/api/v1/board/agent/spinup", "/api/v1/board/task/secret"} {
		r = e.v1Post(p, ck, map[string]any{"key": key}, nil)
		if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != true {
			t.Fatal(p, r.status, r.body)
		}
	}
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for _, p := range v1BoardWrites {
		r := e.v1Post(p, map[string]string{auth.COOKIE: adm}, map[string]any{"task": id, "key": key}, nil)
		if r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(p, r.status, r.body)
		}
	}
	if len(boardRequests(t)) != 0 {
		t.Fatal("the demo wrote a request")
	}
}

func TestAPIv1WorkQaAndAsk(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	get := func(tok, u string) map[string]any {
		t.Helper()
		r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil)
		if r.status != 200 {
			t.Fatal(u, r.status, r.body)
		}
		return v1JSON(t, r)
	}
	for _, u := range []string{"/api/v1/qa", "/api/v1/board/ask"} {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal(u, r.status)
		}
	}
	if err := os.MkdirAll(filepath.Join(SET_IN, "problem-actions"), 0o755); err != nil { // the bulk link needs root's drop directory
		t.Fatal(err)
	}
	i := get(adm, "/api/v1/qa?state=all")
	if i["view"] != "issues" || i["ready"] != true || len(i["rows"].([]any)) == 0 || len(i["tiles"].([]any)) != 4 || i["bulk"] == nil {
		t.Fatal(v1Str(i))
	}
	if v := get(vw, "/api/v1/qa?state=all"); v["bulk"] != nil {
		t.Fatal("a viewer got the bulk link")
	}
	m := get(adm, "/api/v1/qa?view=messages")
	if m["view"] != "messages" || len(m["rows"].([]any)) == 0 || len(m["tiles"].([]any)) != 5 || len(m["facets"].([]any)) != 3 {
		t.Fatal(v1Str(m))
	}
	// the date fields sit in the one filter form: a hidden copy would come first in the query and beat a new date
	dt := get(adm, "/api/v1/qa?view=messages&from=2026-01-01&to=today")
	if h := dt["list"].(map[string]any)["hidden"].(map[string]any); h["from"] != nil || h["to"] != nil || h["view"] != "messages" || dt["dates"].(map[string]any)["from"] != "2026-01-01" {
		t.Fatal(v1Str(dt))
	}
	if d := get(adm, "/api/v1/qa?disp=problem"); d["view"] != "messages" { // an old link still lands on the messages
		t.Fatal(v1Str(d))
	}
	a := get(adm, "/api/v1/board/ask")
	if a["on"] != true || a["chat"].(map[string]any)["key"] != "new" || a["chat"].(map[string]any)["canPost"] != true {
		t.Fatal(v1Str(a))
	}
	if v := get(vw, "/api/v1/board/ask"); v["chat"].(map[string]any)["canPost"] != false {
		t.Fatal(v1Str(v))
	}
	if r := e.req("GET", "/api/v1/board/ask?key=OPS-1", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
	// a new thread: a generated key, and the page of that thread, whatever the form's return said
	r := e.v1Post("/api/v1/board/agent/chat", map[string]string{auth.COOKIE: adm}, map[string]any{"key": "new", "text": "Who owns the Northwind cache?", "return": "/board/ask"}, nil)
	if o := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(o["redirect"].(string), "/board/ask?key=new-") {
		t.Fatal(r.status, r.body)
	}
}
