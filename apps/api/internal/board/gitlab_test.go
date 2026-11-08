package board

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/secret"
)

const (
	glTok      = "fake-project-token-00000001"
	glGroupTok = "fake-group-token-000000002"
	glBadTok   = "fake-wrong-token-000000000"
)

var glPeople = map[string]map[string]any{
	"alex.admin": {"id": 11, "username": "alex.admin", "name": "Alex Admin"},
	"dev.one":    {"id": 12, "username": "dev.one", "name": "Dev One"},
}

// fakeGitLab is the GitLab REST v4 endpoints the tracker calls; the calls are recorded.
type fakeGitLab struct {
	mu       sync.Mutex
	url      string
	calls    []string
	bot      bool
	access   int
	group    bool
	projects map[int64]map[string]any // id -> project
	issues   map[int64]map[int64]map[string]any
	notes    map[int64]map[int64][]map[string]any
	boards   []map[string]any
	gboards  []map[string]any
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func newFakeGitLab(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{bot: true, access: 20, projects: map[int64]map[string]any{}, issues: map[int64]map[int64]map[string]any{},
		notes: map[int64]map[int64][]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	f.addProject(7, "team/tasks")
	lists := []any{map[string]any{"id": 31, "position": 0, "label": map[string]any{"name": "Doing"}},
		map[string]any{"id": 32, "position": 1, "label": map[string]any{"name": "Review"}},
		map[string]any{"id": 33, "position": 2, "assignee": map[string]any{"id": 11}}}
	f.boards = []map[string]any{{"id": 3, "name": "Ops", "labels": []any{}, "lists": lists}}
	f.gboards = []map[string]any{{"id": 9, "name": "Group", "labels": []any{}, "lists": lists[:2]}}
	return f
}

func (f *fakeGitLab) addProject(id int64, path string) {
	f.projects[id] = map[string]any{"id": id, "path_with_namespace": path, "name": path[strings.LastIndex(path, "/")+1:],
		"name_with_namespace": path, "web_url": f.url + "/" + path, "issues_enabled": true}
	f.issues[id], f.notes[id] = map[int64]map[string]any{}, map[int64][]map[string]any{}
}

func (f *fakeGitLab) addIssue(pid int64, title string, labels []string, state string, updated time.Time) int64 {
	iid := int64(len(f.issues[pid]) + 1)
	l := []any{}
	for _, x := range labels {
		l = append(l, x)
	}
	f.issues[pid][iid] = map[string]any{"iid": iid, "id": 1000 + iid, "title": title, "labels": l, "state": state,
		"description": "", "assignees": []any{}, "created_at": ts(updated), "updated_at": ts(updated)}
	return iid
}

func (f *fakeGitLab) issueJSON(pid int64, i map[string]any) map[string]any {
	p := f.projects[pid]
	out := map[string]any{}
	for k, v := range i {
		out[k] = v
	}
	out["web_url"] = fmt.Sprintf("%s/-/issues/%v", p["web_url"], i["iid"])
	out["project_id"] = pid
	out["references"] = map[string]any{"full": fmt.Sprintf("%s#%v", p["path_with_namespace"], i["iid"])}
	out["user_notes_count"] = len(f.notes[pid][i["iid"].(int64)])
	return out
}

func send(w http.ResponseWriter, code int, v any, next string) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	if next != "" {
		w.Header().Set("X-Next-Page", next)
	}
	w.WriteHeader(code)
	w.Write(b)
}

var reIssue = regexp.MustCompile(`^/api/v4/projects/([0-9]+)/issues/([0-9]+)(/notes)?$`)

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath()+" "+string(raw))
	tok := r.Header.Get("PRIVATE-TOKEN")
	user := map[string]any{"id": 900, "username": "project_7_bot", "name": "argus", "bot": f.bot}
	if tok == glGroupTok && f.group {
		user["username"] = "group_50_bot"
	} else if tok != glTok {
		send(w, 401, map[string]any{"message": "401 Unauthorized"}, "")
		return
	}
	p, q := r.URL.EscapedPath(), r.URL.Query()
	switch {
	case p == "/api/v4/user":
		send(w, 200, user, "")
		return
	case p == "/api/v4/users":
		if u := glPeople[q.Get("username")]; u != nil {
			send(w, 200, []any{u}, "")
		} else {
			send(w, 200, []any{}, "")
		}
		return
	case strings.HasPrefix(p, "/api/v4/groups/"):
		if !f.group || tok != glGroupTok {
			send(w, 404, map[string]any{"message": "404 Group Not Found"}, "")
			return
		}
		rest := strings.TrimPrefix(p, "/api/v4/groups/")
		switch {
		case rest == "50" || rest == "org%2Fteam":
			send(w, 200, map[string]any{"id": 50, "full_path": "org/team", "full_name": "org / team", "web_url": f.url + "/groups/org/team"}, "")
		case rest == "50/boards":
			send(w, 200, f.gboards, "")
		case rest == "50/boards/9":
			send(w, 200, f.gboards[0], "")
		case rest == "50/projects":
			var ps []any
			ids := []int64{}
			for id := range f.projects {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				ps = append(ps, f.projects[id])
			}
			send(w, 200, ps, "")
		case rest == "50/issues":
			var out []any
			for pid, is := range f.issues {
				for _, i := range is {
					if a := q.Get("updated_after"); a == "" || i["updated_at"].(string) > strings.Replace(a, "Z", ".000Z", 1) {
						out = append(out, f.issueJSON(pid, i))
					}
				}
			}
			send(w, 200, out, "")
		default:
			send(w, 404, map[string]any{"message": "404"}, "")
		}
		return
	}
	if m := regexp.MustCompile(`^/api/v4/projects/([^/]+)(/.*)?$`).FindStringSubmatch(p); m != nil {
		ref, _ := url.PathUnescape(m[1])
		var pid int64
		for id, pr := range f.projects {
			if ref == strconv.FormatInt(id, 10) || ref == pr["path_with_namespace"] {
				pid = id
			}
		}
		if pid == 0 || (tok == glTok && pid != 7) {
			send(w, 404, map[string]any{"message": "404 Project Not Found"}, "")
			return
		}
		rest := m[2]
		switch {
		case rest == "" && r.Method == "GET":
			pr := map[string]any{}
			for k, v := range f.projects[pid] {
				pr[k] = v
			}
			pr["permissions"] = map[string]any{"project_access": map[string]any{"access_level": f.access}, "group_access": nil}
			send(w, 200, pr, "")
		case rest == "/boards":
			send(w, 200, f.boards, "")
		case rest == "/boards/3":
			send(w, 200, f.boards[0], "")
		case rest == "/issues" && r.Method == "GET":
			var items []map[string]any
			for _, i := range f.issues[pid] {
				if a := q.Get("updated_after"); a == "" || i["updated_at"].(string) > strings.Replace(a, "Z", ".000Z", 1) {
					items = append(items, i)
				}
			}
			sort.Slice(items, func(a, b int) bool { return items[a]["updated_at"].(string) < items[b]["updated_at"].(string) })
			out := []any{}
			for _, i := range items {
				out = append(out, f.issueJSON(pid, i))
			}
			send(w, 200, out, "")
		case rest == "/issues" && r.Method == "POST":
			var labels []string
			for _, x := range strings.Split(fmt.Sprint(body["labels"]), ",") {
				if x != "" {
					labels = append(labels, x)
				}
			}
			iid := f.addIssue(pid, fmt.Sprint(body["title"]), labels, "opened", time.Now())
			f.issues[pid][iid]["description"] = body["description"]
			if a, ok := body["assignee_ids"].([]any); ok {
				f.assign(pid, iid, a)
			}
			send(w, 201, f.issueJSON(pid, f.issues[pid][iid]), "")
		default:
			mi := reIssue.FindStringSubmatch(p)
			if mi == nil {
				send(w, 404, map[string]any{"message": "404"}, "")
				return
			}
			iid, _ := strconv.ParseInt(mi[2], 10, 64)
			i := f.issues[pid][iid]
			if i == nil {
				send(w, 404, map[string]any{"message": "404 Issue Not Found"}, "")
				return
			}
			if mi[3] != "" {
				if r.Method == "POST" {
					n := map[string]any{"id": 5000 + len(f.notes[pid][iid]), "body": body["body"], "system": false,
						"author": map[string]any{"username": user["username"], "name": user["name"]}, "created_at": ts(time.Now())}
					f.notes[pid][iid] = append(f.notes[pid][iid], n)
					send(w, 201, n, "")
				} else {
					send(w, 200, f.notes[pid][iid], "")
				}
				return
			}
			if r.Method == "PUT" {
				labels := i["labels"].([]any)
				for _, rm := range strings.Split(fmt.Sprint(orDef(body["remove_labels"])), ",") {
					for k, x := range labels {
						if x == rm {
							labels = append(labels[:k], labels[k+1:]...)
							break
						}
					}
				}
				for _, ad := range strings.Split(fmt.Sprint(orDef(body["add_labels"])), ",") {
					if ad != "" {
						labels = append(labels, ad)
					}
				}
				i["labels"] = labels
				switch body["state_event"] {
				case "close":
					i["state"] = "closed"
				case "reopen":
					i["state"] = "opened"
				}
				if a, ok := body["assignee_ids"].([]any); ok {
					f.assign(pid, iid, a)
				}
				i["updated_at"] = ts(time.Now())
			}
			send(w, 200, f.issueJSON(pid, i), "")
		}
		return
	}
	send(w, 404, map[string]any{"message": "404"}, "")
}

func orDef(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func (f *fakeGitLab) assign(pid, iid int64, ids []any) {
	var as []any
	for _, u := range glPeople {
		for _, id := range ids {
			if fmt.Sprint(u["id"]) == fmt.Sprint(id) {
				as = append(as, u)
			}
		}
	}
	f.issues[pid][iid]["assignees"] = as
}

func (f *fakeGitLab) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}

// glEnv is a board env with the GitLab tracker wired to a fake GitLab.
func glEnv(t *testing.T) (*env, *fakeGitLab) {
	t.Helper()
	e := newEnv(t, false)
	f := newFakeGitLab(t)
	AllowHTTP = true
	t.Cleanup(func() { AllowHTTP = false })
	d := e.d
	e.o.GitLabReqDir, e.o.EtcDir = filepath.Join(d, "gl-in"), filepath.Join(d, "etc")
	os.MkdirAll(e.o.GitLabReqDir, 0o755)
	host := strings.TrimPrefix(f.url, "http://")
	os.WriteFile(filepath.Join(e.o.EtcDir, glHostsFile), []byte(host+"\n"), 0o644)
	e.o.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	e.o.NewGitLab = func(b, tok, pin string) (*GitLab, error) {
		g, err := NewGitLab(b, tok, pin, "")
		if g != nil {
			g.MinGap, g.Sleep = 0, func(time.Duration) {}
		}
		return g, err
	}
	return e, f
}

func (e *env) glReq(t *testing.T, r map[string]any) string {
	e.n++
	rid := fmt.Sprintf("%016x", e.n)
	if r["user"] == nil {
		r["user"] = "alex.admin"
	}
	b, _ := json.Marshal(r)
	os.WriteFile(filepath.Join(e.o.GitLabReqDir, fmt.Sprint(r["user"])+"-"+rid+".json"), b, 0o600)
	return rid
}

func (e *env) glStatus(t *testing.T) map[string]any {
	var st map[string]any
	b, err := os.ReadFile(filepath.Join(e.o.Out, glStatusFile))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &st)
	return st
}

func (e *env) glResult(t *testing.T, rid string) string {
	return fmt.Sprint(e.glStatus(t)["results"].(map[string]any)[rid].(map[string]any)["result"])
}

func (e *env) connect(t *testing.T, extra map[string]any) string {
	r := map[string]any{"kind": "connect", "url": "", "project": "team/tasks", "token": glTok}
	for k, v := range extra {
		r[k] = v
	}
	if r["url"] == "" {
		r["url"] = e.glURL
	}
	rid := e.glReq(t, r)
	e.run(t)
	return e.glResult(t, rid)
}

func TestGitLabConnectAndTwoWay(t *testing.T) {
	e, f := glEnv(t)
	e.glURL = f.url
	res := e.connect(t, nil)
	if !strings.HasPrefix(res, "connected as project_7_bot to team/tasks; board Ops") {
		t.Fatal(res)
	}
	raw, _ := os.ReadFile(filepath.Join(e.o.Out, glStatusFile))
	if strings.Contains(string(raw), glTok) {
		t.Fatal("token in the status file")
	}
	if fi, _ := os.Stat(filepath.Join(e.o.EtcDir, glTokenFile)); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	st := e.glStatus(t)
	if st["connected"] != true || len(st["lists"].([]any)) != 2 || len(st["warnings"].([]any)) != 0 {
		t.Fatalf("%v", st)
	}
	rid := e.req(t, map[string]any{"kind": "create", "tracker": "gitlab", "title": "Rotate the logs", "env": "develop",
		"host": "app1", "labels": []any{"ops"}, "description": "Disk", "evidence": "df", "fix_prompt": "logrotate -f"})
	bs := e.run(t)
	if result(bs, rid) != "created" {
		t.Fatal(result(bs, rid))
	}
	if g := e.q(t, `SELECT tracker||' '||gl_project||' '||gl_iid||' '||gl_ref FROM tasks`); g != "gitlab 7 1 #1" {
		t.Fatal(g)
	}
	i := f.issues[7][1]
	if !strings.Contains(i["description"].(string), "````text\ndf\n````") || !strings.Contains(fmt.Sprint(i["labels"]), "ops") {
		t.Fatalf("%v", i)
	}
	e.req(t, map[string]any{"kind": "move", "task": 1, "to": "review"})
	e.req(t, map[string]any{"kind": "comment", "task": 1, "body": "looking"})
	e.req(t, map[string]any{"kind": "assign", "task": 1})
	e.run(t)
	if l := i["labels"].([]any); l[len(l)-1] != "Review" {
		t.Fatalf("%v", l)
	}
	if n := f.notes[7][1]; len(n) != 1 || !strings.HasPrefix(n[0]["body"].(string), "**[Argus · alex.admin]** looking") {
		t.Fatalf("%v", n)
	}
	if a := i["assignees"].([]any); len(a) != 1 || a[0].(map[string]any)["username"] != "alex.admin" {
		t.Fatalf("%v", a)
	}
	e.req(t, map[string]any{"kind": "move", "task": 1, "to": "done"})
	e.run(t)
	if i["state"] != "closed" {
		t.Fatal(i["state"])
	}
	// GitLab side: reopened into Doing, a note from a developer
	i["state"], i["labels"] = "opened", []any{"ops", "Doing"}
	f.notes[7][1] = append(f.notes[7][1], map[string]any{"id": 7777, "body": "on it", "system": false,
		"author": map[string]any{"username": "dev.one", "name": "Dev One"}, "created_at": ts(time.Now())})
	i["updated_at"] = ts(time.Now().Add(5 * time.Second))
	e.run(t)
	if s := e.q(t, `SELECT status||' '||jira_status FROM tasks`); s != "progress Doing" {
		t.Fatal(s)
	}
	if c := e.q(t, `SELECT count(*) FROM comments WHERE body='on it'`); c != "1" {
		t.Fatal(c)
	}
	if c := e.q(t, `SELECT count(*) FROM outbox WHERE state!='done'`); c != "0" {
		t.Fatal(c)
	}
}

func TestGitLabPullNewAndConflict(t *testing.T) {
	e, f := glEnv(t)
	e.glURL = f.url
	e.connect(t, nil)
	f.addIssue(7, "Made in GitLab", []string{"Review", "env-develop", "priority::High"}, "opened", time.Now())
	f.addIssue(7, "Old closed", nil, "closed", time.Now().Add(-90*24*time.Hour))
	e.run(t)
	if s := e.q(t, `SELECT group_concat(title||'/'||status||'/'||env||'/'||priority) FROM tasks`); s != "Made in GitLab/review/develop/High" {
		t.Fatal(s)
	}
	// a move queued while GitLab is down; GitLab then closes the issue: GitLab wins
	e.req(t, map[string]any{"kind": "move", "task": 1, "to": "progress"})
	e.o.NewGitLab = func(string, string, string) (*GitLab, error) { return nil, glerr(0, "", "down") }
	e.run(t)
	if run := e.glStatus(t)["last_run"].(map[string]any); run["ok"] != false || !strings.Contains(fmt.Sprint(run["error"]), "down") {
		t.Fatalf("%v", run)
	}
	e2, _ := glEnv(t)
	e.o.NewGitLab = e2.o.NewGitLab
	f.issues[7][1]["state"] = "closed"
	f.issues[7][1]["updated_at"] = ts(time.Now().Add(time.Minute))
	e.run(t)
	if s := e.q(t, `SELECT status FROM tasks`); s != "done" {
		t.Fatal(s)
	}
	if s := e.q(t, `SELECT text FROM events WHERE kind='conflict'`); !strings.Contains(s, "GitLab wins") {
		t.Fatal(s)
	}
}

func TestGitLabRefusals(t *testing.T) {
	e, f := glEnv(t)
	e.glURL = f.url
	rid := e.req(t, map[string]any{"kind": "create", "tracker": "gitlab", "title": "x"})
	if r := result(e.run(t), rid); !strings.Contains(r, "GitLab issues is not connected") {
		t.Fatal(r)
	}
	v := e.glReq(t, map[string]any{"user": "sam.viewer", "kind": "connect", "url": f.url, "project": "team/tasks", "token": glTok})
	e.run(t)
	if r := e.glResult(t, v); !strings.Contains(r, "only admins") {
		t.Fatal(r)
	}
	if r := e.connect(t, map[string]any{"token": glBadTok}); !strings.Contains(r, "refused the token") || strings.Contains(r, "fake-wrong") {
		t.Fatal(r)
	}
	if r := e.connect(t, map[string]any{"project": "team/other"}); !strings.Contains(r, "not found") {
		t.Fatal(r)
	}
	f.access = 10
	if r := e.connect(t, nil); !strings.Contains(r, "Guest") {
		t.Fatal(r)
	}
	f.access = 20
	if r := e.connect(t, map[string]any{"url": "https://gitlab.example.com"}); !strings.Contains(r, "not an allowed GitLab host") {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(e.o.EtcDir, glTokenFile)); err == nil {
		t.Fatal("token stored after refusals")
	}
	g, _ := NewGitLab(f.url, glTok, "", "")
	for _, p := range []string{"/api/v4/projects/7/members", "/api/v4/projects/7/issues/1/../../x", "/api/v4/admin", "/api/v4/projects/7//x"} {
		if _, _, err := g.Request(context.Background(), "GET", p, nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatal(p, err)
		}
	}
	AllowHTTP = false
	if _, err := NewGitLab(f.url, glTok, "", ""); err == nil {
		t.Fatal("http allowed")
	}
}

func TestGitLabGroupDataSourceAndSealedToken(t *testing.T) {
	e, f := glEnv(t)
	f.group = true
	f.addProject(8, "org/team/infra")
	f.projects[7]["path_with_namespace"] = "org/team/tasks"
	e.o.GitLabURL, e.o.GitLabToken = f.url, glGroupTok
	rid := e.glReq(t, map[string]any{"kind": "connect", "url": f.url, "project": f.url + "/groups/org/team/-/boards/9"})
	e.run(t)
	if r := e.glResult(t, rid); !strings.Contains(r, "connected as group_50_bot to group org/team (token of the GitLab data source); board Group; 2 projects, choose the default project") {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(e.o.EtcDir, glTokenFile)); err == nil {
		t.Fatal("a data-source token must not be copied")
	}
	rid = e.glReq(t, map[string]any{"kind": "project", "project": "8"})
	e.run(t)
	if r := e.glResult(t, rid); r != "new GitLab issues go to org/team/infra unless the person picks another project" {
		t.Fatal(r)
	}
	e.req(t, map[string]any{"kind": "create", "tracker": "gitlab", "title": "In infra"})
	e.run(t)
	if s := e.q(t, `SELECT gl_project||' '||gl_ref FROM tasks`); s != "8 infra#1" {
		t.Fatal(s)
	}
	bad := e.req(t, map[string]any{"kind": "create", "tracker": "gitlab", "title": "bad", "gl_project": "99"})
	if r := result(e.run(t), bad); !strings.Contains(r, "not part of the connected group board") {
		t.Fatal(r)
	}
	// a pasted token in the one-volume layout is sealed with the master key
	key, _ := secret.ParseKey(secret.NewKeyText())
	e.o.Key, e.o.GitLabToken = key, ""
	rid = e.glReq(t, map[string]any{"kind": "connect", "url": f.url, "project": "org/team", "token": glGroupTok, "default_project": "8"})
	e.run(t)
	if r := e.glResult(t, rid); !strings.HasPrefix(r, "connected as group_50_bot") {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(e.o.EtcDir, glTokenFile))
	if !secret.Sealed(string(b)) || strings.Contains(string(b), glGroupTok) {
		t.Fatal("token not sealed")
	}
	if e.o.glFileToken() != glGroupTok {
		t.Fatal("sealed token does not open")
	}
}

func TestGitLabListsAndText(t *testing.T) {
	lists := []glList{{"Doing", "progress", 0}, {"Status::Review", "review", 1}}
	for in, want := range map[string]string{"Doing": "progress", "Status::In Review": "review", "To Do": "todo", "Ready for fixer": "fixer", "Whatever": "progress"} {
		if c := glListColumn(in, nil); c != want {
			t.Fatal(in, c)
		}
	}
	if glListColumn("Whatever", map[string]string{"whatever": "review"}) != "review" {
		t.Fatal("override")
	}
	is := func(state string, l ...string) map[string]any {
		x := []any{}
		for _, s := range l {
			x = append(x, s)
		}
		return map[string]any{"state": state, "labels": x}
	}
	if c, n := glColumnOf(is("opened", "x", "Doing"), lists, "argus-fixer"); c != "progress" || n != "Doing" {
		t.Fatal(c, n)
	}
	if c, _ := glColumnOf(is("closed", "Doing"), lists, ""); c != "done" {
		t.Fatal(c)
	}
	j := func(o object) string { return string(pyDumps(o)) }
	if s := j(glMoveFields(is("opened", "Doing", "keep"), "review", lists, "argus-fixer")); s != `{"add_labels": "Status::Review", "remove_labels": "Doing"}` {
		t.Fatal(s)
	}
	if s := j(glMoveFields(is("closed"), "progress", lists, "argus-fixer")); s != `{"state_event": "reopen", "add_labels": "Doing"}` {
		t.Fatal(s)
	}
	if glMoveFields(is("opened"), "review", nil, "argus-fixer") != nil {
		t.Fatal("no list must refuse")
	}
	r := &run{o: &Options{Name: "Argus", AppURL: "https://argus.example.com"}}
	tk := row{"description": "Disk full", "evidence": "df -h\n```\n````x", "fix_prompt": "clean", "env": "develop", "host": "app1",
		"source_kind": "vm", "source_id": "4", "source_url": "/inventory/view?id=4", "requested_by": "alex.admin"}
	p := r.glParse(r.glCompose(tk))
	if p.description != "Disk full" || *p.evidence != "df -h\n```\n````x" || *p.fixPrompt != "clean" || p.env != "develop" || p.host != "app1" {
		t.Fatalf("%+v", p)
	}
	if glTS("2026-10-27T10:00:00.000Z") != glTS("2026-10-27T13:30:00+03:30") || glTS("x") != 0 {
		t.Fatal("timestamps")
	}
}

func TestJiraBasicAuth(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"name":"bot"}`))
	}))
	defer srv.Close()
	j, _ := NewJira(srv.URL, "fake-pass-1", "")
	j.User = "svc.board"
	if _, err := j.Myself(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Basic ") {
		t.Fatal(got)
	}
}
