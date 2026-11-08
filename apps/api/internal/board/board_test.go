package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeJira is a tiny Jira: issues with a status, labels, comments; the calls are recorded.
type fakeJira struct {
	mu        sync.Mutex
	issues    map[string]map[string]any
	n         int
	calls     []string
	token     string
	noDone    bool
	moveOnGet string // status a GET of an issue reports (simulates a move in Jira meanwhile)
}

var statusCat = map[string]string{"To Do": "new", "In Progress": "indeterminate", "Review": "indeterminate", "Done": "done"}
var trans = [][3]string{{"11", "Reopen", "To Do"}, {"21", "Start progress", "In Progress"}, {"31", "Send to review", "Review"}, {"51", "Done", "Done"}}

func st(name string) map[string]any {
	return map[string]any{"name": name, "statusCategory": map[string]any{"key": statusCat[name]}}
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000+0000") }

func (f *fakeJira) add(summary, status string, labels ...string) string {
	f.n++
	key := fmt.Sprintf("OPS-%d", 100+f.n)
	if labels == nil {
		labels = []string{}
	}
	f.issues[key] = map[string]any{"key": key, "fields": map[string]any{"summary": summary, "status": st(status),
		"labels": toAny(labels), "description": "", "priority": map[string]any{"name": "Medium"},
		"created": stamp(time.Now()), "updated": stamp(time.Now()), "comment": map[string]any{"comments": []any{}}}}
	return key
}

func toAny(xs []string) []any {
	out := []any{}
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(401)
		io.WriteString(w, `{"errorMessages":["Unauthorized `+r.Header.Get("Authorization")+`"]}`)
		return
	}
	var in map[string]any
	json.Unmarshal(body, &in)
	out := func(v any) { json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/rest/api/2/myself":
		out(map[string]any{"name": "board-bot"})
	case p == "/rest/api/2/search":
		var list []any
		for _, k := range sortedKeys(f.issues) {
			list = append(list, f.issues[k])
		}
		out(map[string]any{"issues": list, "total": len(list)})
	case p == "/rest/api/2/issue" && r.Method == "POST":
		fl := in["fields"].(map[string]any)
		key := f.add(fl["summary"].(string), "To Do")
		f.issues[key]["fields"].(map[string]any)["labels"] = fl["labels"]
		f.issues[key]["fields"].(map[string]any)["description"] = fl["description"]
		out(map[string]any{"key": key, "id": "1"})
	case strings.HasSuffix(p, "/transitions"):
		key := strings.Split(p, "/")[5]
		if r.Method == "GET" {
			var ts []any
			for _, t := range trans {
				if !(f.noDone && t[2] == "Done") {
					ts = append(ts, map[string]any{"id": t[0], "name": t[1], "to": st(t[2])})
				}
			}
			out(map[string]any{"transitions": ts})
			return
		}
		id := in["transition"].(map[string]any)["id"].(string)
		for _, t := range trans {
			if t[0] == id {
				f.issues[key]["fields"].(map[string]any)["status"] = st(t[2])
			}
		}
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/comment"):
		key := strings.Split(p, "/")[5]
		c := f.issues[key]["fields"].(map[string]any)["comment"].(map[string]any)
		f.n++
		c["comments"] = append(c["comments"].([]any), map[string]any{"id": fmt.Sprint(50000 + f.n), "body": in["body"],
			"author": map[string]any{"name": "board-bot"}, "created": stamp(time.Now())})
		out(map[string]any{"id": fmt.Sprint(50000 + f.n)})
	case strings.HasSuffix(p, "/assignee"):
		key := strings.Split(p, "/")[5]
		f.issues[key]["fields"].(map[string]any)["assignee"] = map[string]any{"name": in["name"], "displayName": in["name"]}
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/rest/api/2/issue/"):
		key := strings.Split(p, "/")[5]
		is := f.issues[key]
		if f.moveOnGet != "" {
			is["fields"].(map[string]any)["status"] = st(f.moveOnGet)
			is["fields"].(map[string]any)["updated"] = stamp(time.Now().Add(time.Hour))
		}
		out(is)
	default:
		w.WriteHeader(404)
	}
}

func sortedKeys(m map[string]map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}

type env struct {
	d  string
	o  Options
	fj *fakeJira
	n  int
}

func newEnv(t *testing.T, jira bool) *env {
	t.Helper()
	d := t.TempDir()
	for _, s := range []string{"board", "in", "out", "etc"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	a, err := sql.Open("sqlite", filepath.Join(d, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	a.Exec(`CREATE TABLE users (username TEXT PRIMARY KEY, role TEXT, disabled INTEGER)`)
	a.Exec(`INSERT INTO users VALUES ('alex.admin','admin',0),('sam.viewer','viewer',0),('old.user','admin',1)`)
	a.Close()
	e := &env{d: d, fj: &fakeJira{issues: map[string]map[string]any{}, token: goodTok}}
	e.o = Options{DB: filepath.Join(d, "board", "board.db"), ReqDir: filepath.Join(d, "in"), Out: filepath.Join(d, "out"),
		AuthDB: filepath.Join(d, "auth.db"), EnvFiles: []string{filepath.Join(d, "etc", "jira.env")},
		PATFile: filepath.Join(d, "etc", "jira.pat"), OwnerUID: os.Getuid(), AppURL: "https://argus.example.com",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewJira: func(b, tok, ca string) (*Jira, error) {
			j, err := NewJira(b, tok, ca)
			if j != nil {
				j.MinGap, j.Sleep = 0, func(time.Duration) {}
			}
			return j, err
		}}
	if jira {
		ts := httptest.NewServer(e.fj)
		t.Cleanup(ts.Close)
		os.WriteFile(filepath.Join(d, "etc", "jira.env"), []byte("JIRA_URL="+ts.URL+"\nJIRA_PROJECT=OPS\n"), 0o644)
		os.WriteFile(e.o.PATFile, []byte(goodTok+"\n"), 0o600)
	}
	return e
}

func (e *env) req(t *testing.T, r map[string]any) string {
	e.n++
	rid := fmt.Sprintf("20261108%06d-%08x", e.n, e.n)
	if r["by"] == nil {
		r["by"] = "alex.admin"
	}
	b, _ := json.Marshal(r)
	os.WriteFile(filepath.Join(e.o.ReqDir, rid+".json"), b, 0o644)
	return rid
}

func (e *env) run(t *testing.T) map[string]any {
	t.Helper()
	if err := Run(context.Background(), e.o); err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	b, err := os.ReadFile(filepath.Join(e.o.Out, "board-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &st)
	return st
}

func result(st map[string]any, rid string) string {
	return fmt.Sprint(st["requests"].(map[string]any)[rid].(map[string]any)["result"])
}

func (e *env) q(t *testing.T, query string, a ...any) string {
	db, _ := sql.Open("sqlite", e.o.DB)
	defer db.Close()
	var s sql.NullString
	if err := db.QueryRow(query, a...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return s.String
}

func TestRequestsLocalMode(t *testing.T) {
	e := newEnv(t, false)
	ok := e.req(t, map[string]any{"kind": "create", "title": "  disk  full ", "env": "develop", "source_kind": "incident", "source_id": "8"})
	dup := e.req(t, map[string]any{"kind": "create", "title": "again", "source_kind": "incident", "source_id": "8"})
	bad := map[string]map[string]any{
		"refused: title must be 1 to 200 characters":                         {"kind": "create", "title": " "},
		"refused: unknown environment":                                       {"kind": "create", "title": "x", "env": "moon"},
		"refused: bad source":                                                {"kind": "create", "title": "x", "source_id": "8"},
		"refused: bad source link":                                           {"kind": "create", "title": "x", "source_kind": "rca", "source_id": "1", "source_url": "https://evil.example/"},
		"refused: unknown or disabled user":                                  {"kind": "create", "title": "x", "by": "old.user"},
		"refused: bad user":                                                  {"kind": "create", "title": "x", "by": "../etc"},
		"refused: only admins can change board settings":                     {"kind": "settings", "auto_incidents": true, "by": "sam.viewer"},
		"refused: unknown request kind":                                      {"kind": "launch"},
		"refused: no such task":                                              {"kind": "move", "task": 99, "to": "done"},
		"refused: unknown column":                                            {"kind": "move", "task": 1, "to": "sideways"},
		"refused: GitLab issue tasks are not handled by this board sync yet": {"kind": "create", "title": "x", "tracker": "gitlab"},
		"refused: lessons requests are not handled by this board sync yet":   {"kind": "lesson"},
	}
	rids := map[string]string{}
	for want, r := range bad {
		rids[e.req(t, r)] = want
	}
	os.WriteFile(filepath.Join(e.o.ReqDir, "not-an-id.json"), []byte("{}"), 0o644)
	st := e.run(t)
	if result(st, ok) != "created" || result(st, dup) != "exists" {
		t.Fatalf("%v %v", result(st, ok), result(st, dup))
	}
	for rid, want := range rids {
		if got := result(st, rid); got != want {
			t.Errorf("%s: got %q want %q", rid, got, want)
		}
	}
	if left, _ := os.ReadDir(e.o.ReqDir); len(left) != 0 {
		t.Fatalf("request files left: %v", left)
	}
	if st["mode"] != "local" || e.q(t, `SELECT title FROM tasks WHERE id=1`) != "disk full" ||
		e.q(t, `SELECT labels FROM tasks WHERE id=1`) != `["argus", "env-develop", "argus-src-incident-8"]` {
		t.Fatalf("%v %s", st["mode"], e.q(t, `SELECT labels FROM tasks WHERE id=1`))
	}
	if e.q(t, `SELECT count(*) FROM outbox WHERE state='queued'`) != "1" {
		t.Fatal("the create waits in the outbox without Jira")
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	e := newEnv(t, true)
	e.fj.add("Seen first in Jira", "In Progress", "env-pre-release")
	e.req(t, map[string]any{"kind": "create", "title": "Rotate the token", "env": "develop", "evidence": "expires in 3 days",
		"description": "token " + "gl" + "pat-" + strings.Repeat("Z", 20)})
	st := e.run(t)
	if st["configured"] != true || st["bot"] != "board-bot" {
		t.Fatalf("%v", st)
	}
	key := e.q(t, `SELECT jira_key FROM tasks WHERE title='Rotate the token'`)
	desc := e.fj.issues[key]["fields"].(map[string]any)["description"].(string)
	if strings.Contains(desc, strings.Repeat("Z", 20)) || !strings.Contains(desc, "[REDACTED:gitlab-token]") ||
		!strings.Contains(desc, "h3. Evidence\n{noformat}\nexpires in 3 days\n{noformat}") || !strings.Contains(desc, "Argus task · env: develop") {
		t.Fatalf("description %q", desc)
	}
	if e.q(t, `SELECT env FROM tasks WHERE jira_key='OPS-101'`) != "pre-release" || e.q(t, `SELECT status FROM tasks WHERE jira_key='OPS-101'`) != "progress" {
		t.Fatal("pulled issue")
	}
	tid := e.q(t, `SELECT id FROM tasks WHERE jira_key=?`, key)
	e.req(t, map[string]any{"kind": "move", "task": json.Number(tid), "to": "review"})
	e.req(t, map[string]any{"kind": "comment", "task": json.Number(tid), "body": "looked at it"})
	e.req(t, map[string]any{"kind": "assign", "task": json.Number(tid)})
	e.run(t)
	f := e.fj.issues[key]["fields"].(map[string]any)
	if f["status"].(map[string]any)["name"] != "Review" || f["assignee"].(map[string]any)["name"] != "alex.admin" {
		t.Fatalf("jira side %v", f)
	}
	if c := f["comment"].(map[string]any)["comments"].([]any); len(c) != 1 || c[0].(map[string]any)["body"] != "[Argus · alex.admin] looked at it" {
		t.Fatalf("comment %v", c)
	}
	if e.q(t, `SELECT count(*) FROM comments WHERE task_id=? AND pending=0 AND jira_id IS NOT NULL`, tid) != "1" {
		t.Fatal("local comment not linked to the Jira one")
	}
	if e.q(t, `SELECT count(*) FROM outbox WHERE state<>'done'`) != "0" {
		t.Fatal("outbox not empty")
	}
}

func TestConflictJiraWins(t *testing.T) {
	e := newEnv(t, true)
	key := e.fj.add("Moved in Jira meanwhile", "To Do")
	e.run(t)
	tid := e.q(t, `SELECT id FROM tasks WHERE jira_key=?`, key)
	e.fj.moveOnGet = "Done"
	e.req(t, map[string]any{"kind": "move", "task": json.Number(tid), "to": "progress"})
	st := e.run(t)
	if e.q(t, `SELECT state FROM outbox WHERE kind='transition'`) != "dropped" || e.q(t, `SELECT status FROM tasks WHERE id=?`, tid) != "done" ||
		st["stats"].(map[string]any)["conflicts"] != float64(1) {
		t.Fatalf("conflict: %s %s %v", e.q(t, `SELECT state FROM outbox WHERE kind='transition'`), e.q(t, `SELECT status FROM tasks WHERE id=?`, tid), st["stats"])
	}
}

func TestAuthErrorAndDryRun(t *testing.T) {
	e := newEnv(t, true)
	os.WriteFile(e.o.PATFile, []byte(badTok+"\n"), 0o600)
	e.req(t, map[string]any{"kind": "create", "title": "waits"})
	st := e.run(t)
	if !strings.Contains(fmt.Sprint(st["auth_error"]), "HTTP 401") || strings.Contains(fmt.Sprint(st), badTok) {
		t.Fatalf("auth: %v", st["auth_error"])
	}
	if e.q(t, `SELECT state FROM outbox`) != "queued" {
		t.Fatal("the create must wait")
	}
	// dry run: reads Jira, sends no write, keeps the request, the database and the status file
	os.WriteFile(e.o.PATFile, []byte(goodTok+"\n"), 0o600)
	rid := e.req(t, map[string]any{"kind": "create", "title": "dry"})
	before, _ := os.ReadFile(filepath.Join(e.o.Out, "board-status.json"))
	e.o.DryRun = true
	e.fj.calls = nil
	if err := Run(context.Background(), e.o); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.fj.calls {
		if !strings.HasPrefix(c, "GET ") && c != "POST /rest/api/2/search" {
			t.Fatalf("dry run sent %s", c)
		}
	}
	after, _ := os.ReadFile(filepath.Join(e.o.Out, "board-status.json"))
	if _, err := os.Stat(filepath.Join(e.o.ReqDir, rid+".json")); err != nil || string(before) != string(after) ||
		e.q(t, `SELECT count(*) FROM tasks`) != "1" {
		t.Fatal("dry run changed something")
	}
}

func TestComposeParseAndConfig(t *testing.T) {
	r := &run{o: &Options{Name: "Argus", AppURL: "https://argus.example.com"}}
	tk := row{"description": "Body {noformat} x", "evidence": "ev", "fix_prompt": "fp", "env": "develop", "host": "app-01",
		"source_kind": "rca", "source_id": "3", "source_url": "/rca/view?id=3", "requested_by": "alex.admin"}
	d := r.compose(tk)
	p := r.parse(d)
	if p.description != "Body {noformat} x" || *p.evidence != "ev" || *p.fixPrompt != "fp" || p.env != "develop" || p.host != "app-01" {
		t.Fatalf("%q -> %+v", d, p)
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "jira.env")
	os.WriteFile(f, []byte("JIRA_URL=https://jira.example\nJIRA_PROJECT=bad key\nOTHER=x\n"), 0o666)
	os.Chmod(f, 0o666)
	if cfg, probs := LoadConfig([]string{f}, os.Getuid()); len(cfg) != 0 || len(probs) != 1 {
		t.Fatalf("world-writable file must be ignored: %v %v", cfg, probs)
	}
	os.Chmod(f, 0o644)
	if cfg, probs := LoadConfig([]string{f}, os.Getuid()); cfg["JIRA_URL"] != "https://jira.example" || cfg["JIRA_PROJECT"] != "" || len(probs) != 1 {
		t.Fatalf("%v %v", cfg, probs)
	}
	tf := filepath.Join(dir, "pat")
	os.WriteFile(tf, []byte("tok\n"), 0o640)
	if tok, why := LoadToken(tf, os.Getuid()); tok != "" || !strings.Contains(why, "0600") {
		t.Fatal("a group-readable token must be refused")
	}
}

func TestJiraClientGuards(t *testing.T) {
	if _, err := NewJira("http://jira.example", "t", ""); err == nil {
		t.Fatal("plain http to a remote host")
	}
	if _, err := NewJira("https://u"+":"+"p@jira.example", "t", ""); err == nil {
		t.Fatal("user info in the URL")
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer redir.Close()
	j, _ := NewJira(redir.URL, "secret-tok-1", "")
	j.Sleep = func(time.Duration) {}
	if _, err := j.Myself(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := j.Request(context.Background(), "GET", "/secure/admin", nil); err == nil {
		t.Fatal("path outside the REST API")
	}
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"errorMessages":["bad `+r.Header.Get("Authorization")+`"]}`)
	}))
	defer echo.Close()
	j, _ = NewJira(echo.URL, "secret-tok-2", "")
	if _, err := j.Myself(context.Background()); err == nil || strings.Contains(err.Error(), "secret-tok-2") {
		t.Fatalf("token in error: %v", err)
	}
}

// synthetic bearer values of the fake Jira
var goodTok, badTok = "fake" + "-good", "fake" + "-bad"
