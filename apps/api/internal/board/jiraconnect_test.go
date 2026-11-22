package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/secret"
)

// jcFake answers the reads of a connection test the way the earlier job's fake Jira does.
type jcFake struct {
	mu      sync.Mutex
	version []int
	calls   [][3]string
}

var jcTokens = map[string]struct {
	name, display string
	projects      []string
}{
	"good-token":            {"argus-bot", "Argus bot", nil},
	"tokA-1234567890abcdef": {"lead.dev", "Lead Dev", []string{"OPS", "DEV"}},
	"tokV-1234567890abcdef": {"view.er", "View Er", []string{"OPS"}},
}
var jcTokLabel = map[string]string{"good-token": "bot", "tokA-1234567890abcdef": "A", "tokV-1234567890abcdef": "V", "wrong-token-123456": "wrong"}

func (f *jcFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.calls = append(f.calls, [3]string{r.Method, r.URL.RequestURI(), jcTokLabel[tok]})
	send := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	who, ok := jcTokens[tok]
	if !ok {
		send(401, map[string]any{"errorMessages": []string{"Unauthorized"}})
		return
	}
	sees := func(p string) bool { return who.projects == nil || contains(who.projects, p) }
	type board struct {
		id         int
		name, proj string
	}
	boards := []board{{11, "OPS board", "OPS"}, {12, "OPS kanban", "OPS"}, {21, "DEV board", "DEV"}}
	notBoard := map[string]any{"errorMessages": []string{"Board does not exist or you do not have permission to see it."}}
	p := r.URL.Path
	switch {
	case p == "/rest/api/2/serverInfo":
		ver := []string{}
		for _, v := range f.version {
			ver = append(ver, itoa(v))
		}
		send(200, map[string]any{"version": strings.Join(ver, "."), "versionNumbers": f.version, "deploymentType": "Server", "serverTitle": "Fake Jira"})
	case p == "/rest/api/2/myself":
		send(200, map[string]any{"name": who.name, "key": who.name, "displayName": who.display})
	case p == "/rest/api/2/project":
		out := []any{}
		for _, x := range [][2]string{{"OPS", "Ops"}, {"DEV", "Development"}} {
			if sees(x[0]) {
				out = append(out, map[string]any{"key": x[0], "name": x[1]})
			}
		}
		send(200, out)
	case regexp.MustCompile(`^/rest/api/2/project/[A-Z0-9_]+/components$`).MatchString(p):
		send(200, []any{map[string]any{"id": "1", "name": "DevOps Dep"}, map[string]any{"id": "2", "name": "Docker"}})
	case p == "/rest/agile/1.0/board":
		pk := r.URL.Query().Get("projectKeyOrId")
		out := []any{}
		for _, b := range boards {
			if b.proj == pk && sees(b.proj) {
				out = append(out, map[string]any{"id": b.id, "name": b.name, "type": "scrum"})
			}
		}
		send(200, map[string]any{"values": out})
	case strings.HasPrefix(p, "/rest/agile/1.0/board/"):
		for _, b := range boards {
			if p == "/rest/agile/1.0/board/"+itoa(b.id) && sees(b.proj) {
				send(200, map[string]any{"id": b.id, "name": b.name, "type": "scrum"})
				return
			}
		}
		send(404, notBoard)
	default:
		send(404, map[string]any{"errorMessages": []string{"no route"}})
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

type jcGolden struct {
	Steps []struct {
		Version  []int `json:"version"`
		Requests []struct {
			File string         `json:"file"`
			Body map[string]any `json:"body"`
		} `json:"requests"`
		State     map[string]any `json:"state"`
		Env       []string       `json:"env"`
		Calls     [][3]string    `json:"calls"`
		Pats      []string       `json:"pats"`
		SharedPat bool           `json:"shared_pat"`
	} `json:"steps"`
}

func fill(v any, url string) any {
	switch x := v.(type) {
	case string:
		return strings.NewReplacer("{URL}", url, "{HOST}", strings.TrimPrefix(url, "http://"), "{TOK_A}", "tokA-1234567890abcdef",
			"{TOK_V}", "tokV-1234567890abcdef", "{TOK_BOT}", "good-token", "{TOK_WRONG}", "wrong-token-123456", "{USERINFO}", "u:p").Replace(x)
	case []any:
		out := []any{}
		for _, e := range x {
			out = append(out, fill(e, url))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = fill(e, url)
		}
		return out
	}
	return v
}

func jcNorm(t *testing.T, v any, port string) any {
	b, _ := json.Marshal(v)
	var out any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(b), ":"+port, ":PORT")), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestJiraConnectParity replays the earlier job's scenario (testdata/jiraconnect-parity.json, made by running its
// code against its fake Jira) and compares every result, the published state, the shared env lines, the stored
// token files and the Jira calls of each step. One reasoned difference: removing the shared connection also removes
// JIRA_COMPONENT (the earlier job left it behind).
func TestJiraConnectParity(t *testing.T) {
	var g jcGolden
	b, err := os.ReadFile("testdata/jiraconnect-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	AllowHTTP = true
	t.Cleanup(func() { AllowHTTP = false })
	d := t.TempDir()
	for _, s := range []string{"in/jira-connect", "out", "etc"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	a, _ := sql.Open("sqlite", filepath.Join(d, "auth.db"))
	a.Exec(`CREATE TABLE users (username TEXT PRIMARY KEY, role TEXT, disabled INTEGER)`)
	a.Exec(`INSERT INTO users VALUES ('admin.one','admin',0),('viewer.one','viewer',0)`)
	a.Close()
	fj := &jcFake{version: []int{9, 12, 3}}
	ts := httptest.NewServer(fj)
	defer ts.Close()
	port := ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	key, _ := secret.ParseKey(secret.NewKeyText())
	o := Options{Out: filepath.Join(d, "out"), AuthDB: filepath.Join(d, "auth.db"), EtcDir: filepath.Join(d, "etc"),
		EnvFiles: []string{filepath.Join(d, "etc", "jira.env")}, PATFile: filepath.Join(d, "etc", "jira.pat"),
		JiraConnectDir: filepath.Join(d, "in", "jira-connect"), Key: key, Now: time.Now, Name: "Argus",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for i, step := range g.Steps {
		if step.Version != nil {
			fj.version = step.Version
			continue
		}
		fj.calls = nil
		for _, rq := range step.Requests {
			user := rq.File[:strings.LastIndex(rq.File, "-")]
			body := fill(rq.Body, ts.URL).(map[string]any)
			full := map[string]any{"user": user, "at": 1}
			for k, v := range body {
				full[k] = v
			}
			data, _ := json.Marshal(full)
			os.WriteFile(filepath.Join(o.JiraConnectDir, rq.File), data, 0o600)
		}
		r := &run{o: &o, ctx: context.Background(), jcResults: map[string]any{}}
		r.jcProcess()
		if err := r.jcPublish(); err != nil {
			t.Fatal(err)
		}
		var st map[string]any
		sb, _ := os.ReadFile(filepath.Join(o.Out, jcUsersFile))
		json.Unmarshal(sb, &st)
		delete(st, "updated_at")
		res := map[string]any{}
		for k, v := range asMapAny(st["results"]) {
			if _, ok := r.jcResults[k]; ok {
				delete(v.(map[string]any), "at")
				res[k] = v
			}
		}
		st["results"] = res
		for _, u := range asMapAny(st["users"]) {
			delete(u.(map[string]any), "connected_at")
		}
		delete(asMapAny(st["shared"]), "connected_at")
		if got, want := jcNorm(t, st, port), jcNorm(t, step.State, "PORT"); !reflect.DeepEqual(got, want) {
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			t.Fatalf("step %d state:\n got %s\nwant %s", i, gb, wb)
		}
		env := []string{}
		if eb, err := os.ReadFile(o.EnvFiles[0]); err == nil {
			for _, l := range splitLines(string(eb)) {
				env = append(env, regexp.MustCompile(`=.*/(jira-ca\.pem)$`).ReplaceAllString(l, "=$1"))
			}
		}
		want := step.Env
		if len(want) == 1 && want[0] == "JIRA_COMPONENT=Docker" && !step.SharedPat { // the reasoned difference
			want = []string{}
		}
		if got := jcNorm(t, env, port); !reflect.DeepEqual(got, jcNorm(t, want, "PORT")) {
			t.Fatalf("step %d env: got %v want %v", i, got, want)
		}
		calls := [][3]string{}
		for _, c := range fj.calls {
			calls = append(calls, c)
		}
		if !reflect.DeepEqual(jcNorm(t, calls, port), jcNorm(t, step.Calls, "PORT")) {
			t.Fatalf("step %d calls: got %v want %v", i, calls, step.Calls)
		}
		pats := []string{}
		ents, _ := os.ReadDir(o.jcUsersDir())
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".pat") {
				pats = append(pats, e.Name())
			}
		}
		sort.Strings(pats)
		_, perr := os.Stat(o.PATFile)
		if !reflect.DeepEqual(pats, append([]string{}, step.Pats...)) || (perr == nil) != step.SharedPat {
			t.Fatalf("step %d token files: %v shared=%v, want %v shared=%v", i, pats, perr == nil, step.Pats, step.SharedPat)
		}
	}
	// no token in clear anywhere the app reads, and the stored tokens are sealed
	for _, root := range []string{filepath.Join(d, "out"), filepath.Join(d, "in")} {
		filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.Mode().IsRegular() {
				b, _ := os.ReadFile(p)
				for tok := range jcTokens {
					if strings.Contains(string(b), tok) {
						t.Errorf("%s holds a token", p)
					}
				}
			}
			return nil
		})
	}
	pb, _ := os.ReadFile(filepath.Join(o.jcUsersDir(), "admin.one.pat"))
	if !secret.Sealed(string(pb)) {
		t.Fatal("the user token is not sealed")
	}
	if got := o.jcReadToken(filepath.Join(o.jcUsersDir(), "admin.one.pat"), UserJiraName("admin.one")); got != "tokA-1234567890abcdef" {
		t.Fatal("the sealed user token does not open")
	}
}

func TestJiraConnectGuard(t *testing.T) {
	r := &run{o: &Options{Resolve: func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }}, ctx: context.Background()}
	try := func(raw string, admin bool) (msg string) {
		defer func() {
			if x := recover(); x != nil {
				msg = string(x.(refused))
			}
		}()
		r.jcCheckURL(raw, []string{"jira.example.com"}, admin)
		return ""
	}
	for raw, want := range map[string]string{
		"http://jira.example.com":       "https://",
		"https://jira.example.com:8443": "port 8443",
		"https://other.example.com":     "not an allowed Jira host",
		"https://169.254.169.254":       "metadata",
		"https://169.254.10.1":          "link-local",
		"https://[::ffff:127.0.0.1]":    "loopback",
		"https://0.0.0.0":               "unspecified",
		"https://jira.example.com/":     "loopback", // the name points inside
	} {
		admin := !strings.Contains(raw, "other")
		if got := try(raw, admin); !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
