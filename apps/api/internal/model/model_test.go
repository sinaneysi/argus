package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"
	"github.com/sinaneysi/argus/internal/sandbox"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/style"

	_ "modernc.org/sqlite"
)

// fake tokens, built at run time (a literal would look like a leaked secret)
var (
	tokenPrefix = "sk-" + "ant-oat01-"
	fakeToken   = tokenPrefix + strings.Repeat("fake", 8) + "0001"
)

// The test binary is also the model-exec helper (ARGUS_TEST_MODEL_EXEC=1), as the argus binary is in production.
func TestMain(m *testing.M) {
	if os.Getenv("ARGUS_TEST_MODEL_EXEC") == "1" {
		os.Exit(sandbox.Main(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}

func opsDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE runs (id INTEGER PRIMARY KEY, task TEXT NOT NULL, started_at INTEGER NOT NULL,
		duration_ms INTEGER, status TEXT, summary TEXT, cost_usd REAL, turns INTEGER, denials INTEGER, is_error INTEGER,
		mr_url TEXT, log_path TEXT)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func setup(t *testing.T) Options {
	style.Dirs = func() []string { return nil }
	dir := t.TempDir()
	key, _ := secret.ParseKey(secret.NewKeyText())
	sealed, err := key.Seal(secret.ClaudeName, fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(dir, "claude.env")
	os.WriteFile(env, []byte("CLAUDE_CODE_OAUTH_TOKEN="+sealed+"\nHTTPS_PROXY=\n"), 0o600)
	bin, _ := filepath.Abs("testdata/fake-claude")
	return Options{Provider: CLI, ClaudeBin: bin, EnvFile: env, Key: key, TempDir: dir, DB: opsDB(t), LogDir: filepath.Join(dir, "logs"),
		EgressFile: filepath.Join(dir, "egress.json"), EgressCheck: filepath.Join(dir, "check.json"),
		LookupFn: func(map[string]string, []string) claudegate.Found {
			return claudegate.Found{IP: "203.0.113.9", Loc: "DE"}
		}}
}

func TestCLIRunRecordsAndEvents(t *testing.T) {
	o := setup(t)
	var evs []Event
	res := New(o).Run(context.Background(), Request{Task: "task-intake", Prompt: "plan it", OnEvent: func(e Event) { evs = append(evs, e) }})
	if res.Error != "" || res.IsError || !strings.HasPrefix(res.Text, "OK done") || res.CostUSD != 0.12 || res.Turns != 3 || res.Denials != 1 {
		t.Fatalf("result %+v", res)
	}
	if len(evs) != 2 || evs[0].Type != "init" || evs[1].Tool != "Read" {
		t.Fatalf("events %+v", evs)
	}
	var task, status, mr string
	if err := o.DB.QueryRow("SELECT task, status, mr_url FROM runs WHERE id=?", res.RunID).Scan(&task, &status, &mr); err != nil {
		t.Fatal(err)
	}
	if task != "task-intake" || status != "OK" || !strings.HasSuffix(mr, "/merge_requests/7") {
		t.Fatal(task, status, mr)
	}
	if left, _ := filepath.Glob(filepath.Join(o.TempDir, "argus-model-*")); len(left) != 0 {
		t.Fatal("the throwaway HOME was not removed", left)
	}
}

func TestCLIEnvAndStyle(t *testing.T) {
	o := setup(t)
	o.ExtraEnv = []string{"FAKE_CLAUDE_MODE=env"}
	t.Setenv("SECRET_OF_WORKER", "worker-secret")
	res := New(o).Run(context.Background(), Request{Prompt: "x"})
	parts := strings.Split(res.Text, "|")
	if len(parts) != 3 || parts[0] != "OK "+fakeToken {
		t.Fatalf("the token was not opened for the CLI: %q", res.Text)
	}
	if parts[1] != "none" {
		t.Fatal("the worker's environment reached the CLI")
	}
	if !strings.HasPrefix(parts[2], "<writing-style>") {
		t.Fatal("the prompt was not built with style.Prepend:", parts[2])
	}
	// the record never holds the token
	var summary string
	o.DB.QueryRow("SELECT summary FROM runs WHERE id=?", res.RunID).Scan(&summary)
	if strings.Contains(summary, "faketoken") {
		t.Fatal("token in the run record:", summary)
	}
}

func TestCLIErrors(t *testing.T) {
	o := setup(t)
	o.ExtraEnv = []string{"FAKE_CLAUDE_MODE=error"}
	res := New(o).Run(context.Background(), Request{Prompt: "x"})
	if !res.IsError || res.ExitCode != 1 || res.Error != "Invalid bearer token" || res.Status() != "ERROR" {
		t.Fatalf("%+v", res)
	}
	o.ExtraEnv = []string{"FAKE_CLAUDE_MODE=nores"}
	res = New(o).Run(context.Background(), Request{Prompt: "x"})
	if !res.IsError || !strings.Contains(res.Error, "cannot reach") {
		t.Fatalf("%+v", res)
	}
	o.ExtraEnv = []string{"FAKE_CLAUDE_MODE=sleep"}
	start := time.Now()
	res = New(o).Run(context.Background(), Request{Prompt: "x", Timeout: 300 * time.Millisecond})
	if res.ExitCode != TimeoutExit || !res.IsError || time.Since(start) > 5*time.Second {
		t.Fatalf("time limit: %+v", res)
	}
	o.ClaudeBin = "/nonexistent/claude"
	res = New(o).Run(context.Background(), Request{Prompt: "x"})
	if !strings.HasPrefix(res.Error, "could not start") {
		t.Fatalf("%+v", res)
	}
}

func TestGuards(t *testing.T) {
	o := setup(t)
	o.Enabled = func() (bool, string) { return false, "" }
	if res := New(o).Run(context.Background(), Request{Prompt: "x"}); !strings.Contains(res.Skipped, "Claude usage is off") || res.RunID != 0 {
		t.Fatalf("%+v", res)
	}
	o.Enabled = nil
	o.LookupFn = func(map[string]string, []string) claudegate.Found {
		return claudegate.Found{IP: "198.51.100.1", Loc: "IR"}
	}
	if res := New(o).Run(context.Background(), Request{Prompt: "x"}); !strings.Contains(res.Skipped, "egress IP") {
		t.Fatalf("%+v", res)
	}
	os.WriteFile(o.EnvFile, nil, 0o600)
	if res := New(o).Run(context.Background(), Request{Prompt: "x"}); !strings.Contains(res.Skipped, "Settings -> Claude") {
		t.Fatalf("%+v", res)
	}
}

func TestLimits(t *testing.T) {
	r := New(Options{MaxBudgetUSD: 1, MaxTimeout: time.Minute})
	q := r.limits(Request{BudgetUSD: 9, Timeout: time.Hour})
	if q.BudgetUSD != 1 || q.Timeout != time.Minute || q.MaxTurns != 30 || q.MaxTokens != 16000 {
		t.Fatalf("%+v", q)
	}
	if got := r.cliArgs(Request{MaxTurns: 4, BudgetUSD: 0.5, Tools: []string{"Read", "Grep"}}); strings.Join(got, " ") !=
		"-p --output-format stream-json --verbose --no-session-persistence --max-turns 4 --max-budget-usd 0.50 --tools Read,Grep"+
			" --restricted --strict-mcp-config --settings "+cliSettings+" --disallowedTools "+
			"Read(//var/lib/argus/**),Read(//proc/**),Read(//sys/**),Read(//etc/**),Read(//dev/**)" {
		t.Fatal(got)
	}
}

func TestCLIDenyRulesAlways(t *testing.T) {
	r := New(Options{StateDir: "/srv/argus/"})
	got := r.cliArgs(Request{Tools: []string{"Read"}, Disallow: []string{"Read(//var/lib/argus/keys)", "Bash"}})
	i := len(got) - 2
	if got[i] != "--disallowedTools" || strings.Count(strings.Join(got, " "), "--disallowedTools") != 1 {
		t.Fatal("one deny list, last:", got)
	}
	deny := strings.Split(got[i+1], ",")
	for _, want := range []string{"Read(//var/lib/argus/**)", "Read(//proc/**)", "Read(//etc/**)", "Read(//srv/argus/**)", "Bash"} {
		found := false
		for _, d := range deny {
			found = found || d == want
		}
		if !found {
			t.Fatalf("%s missing from %v", want, deny)
		}
	}
	if !strings.Contains(strings.Join(got, " "), "--restricted") || !strings.Contains(cliSettings, `"blockReadsOutsideWorkingDirectories":true`) {
		t.Fatal(got)
	}
	// the state folder is not listed twice, and / is never a rule
	if n := strings.Count(strings.Join(New(Options{StateDir: "/var/lib/argus"}).denyRules(), ","), "argus"); n != 1 {
		t.Fatal(n)
	}
	if rs := New(Options{StateDir: "/"}).denyRules(); len(rs) != len(denyRead) {
		t.Fatal(rs)
	}
}

func TestSandboxRules(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	r := sandboxRules("/nonexistent/claude", home, dir)
	all := append(append(append([]string{}, r.Exec...), r.Read...), r.Write...)
	for _, p := range all {
		if p == "/" || p == "/etc" || p == "/var" || p == "/tmp" || strings.HasPrefix(p, "/var/lib/argus") {
			t.Fatalf("rule %q opens what must stay closed: %v", p, all)
		}
	}
	if strings.Join(r.Write, " ") != "/dev/null "+home+" "+dir {
		t.Fatal(r.Write)
	}
	if got := sandboxRules("/nonexistent/claude", home, home); len(got.Write) != 2 {
		t.Fatal("the HOME twice:", got.Write)
	}
}

func TestCLISandboxFailsClosed(t *testing.T) {
	o := setup(t)
	o.SandboxExec = []string{os.Args[0]}
	o.ExtraEnv = []string{"ARGUS_TEST_MODEL_EXEC=1"}
	defer func(f func() int) { landlockABI = f }(landlockABI)
	landlockABI = func() int { return 0 }
	res := New(o).Run(context.Background(), Request{Prompt: "x"})
	if !res.IsError || res.ExitCode != sandbox.Unavailable || res.Sandbox != "unavailable" || !strings.Contains(res.Error, "ARGUS_MODEL_SANDBOX=off") {
		t.Fatalf("%+v", res)
	}
	o.SandboxExec = nil // ARGUS_MODEL_SANDBOX=off
	if res := New(o).Run(context.Background(), Request{Prompt: "x"}); res.IsError || res.Sandbox != "off" {
		t.Fatalf("%+v", res)
	}
}

func TestCLISandboxBlocksOutside(t *testing.T) {
	if sandbox.ABI() < 1 {
		t.Skip("Landlock is not available on this kernel")
	}
	o := setup(t)
	o.SandboxExec = []string{os.Args[0]}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "task.json"), []byte("task-inside"), 0o644)
	run := func(file string) Result {
		o.ExtraEnv = []string{"ARGUS_TEST_MODEL_EXEC=1", "FAKE_CLAUDE_MODE=read", "FAKE_CLAUDE_READ=" + file}
		return New(o).Run(context.Background(), Request{Prompt: "x", Dir: work})
	}
	if res := run(filepath.Join(work, "task.json")); res.Text != "OK task-inside|yes" || !strings.HasPrefix(res.Sandbox, "landlock-v") {
		t.Fatalf("inside the working folder: %+v", res)
	}
	// the Claude env file sits next to the throwaway HOME, like the volume next to /tmp
	if res := run(o.EnvFile); res.Text != "OK denied|yes" {
		t.Fatalf("outside the working folder: %+v", res)
	}
	if res := run(fmt.Sprintf("/proc/%d/environ", os.Getpid())); res.Text != "OK denied|yes" {
		t.Fatalf("the worker's environment: %+v", res)
	}
}

func TestAPIProvider(t *testing.T) {
	o := setup(t)
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		hdr = r.Header
		if r.Header.Get("x-api-key") != "fake-key-0001" {
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"OK planned"}],"stop_reason":"end_turn","usage":{"input_tokens":1000000,"output_tokens":100000}}`)
	}))
	defer srv.Close()
	o.Provider, o.APIURL, o.APIKey = API, srv.URL, "fake-key-0001"
	res := New(o).Run(context.Background(), Request{Task: "task-intake", Prompt: "plan"})
	if res.IsError || res.Text != "OK planned" || res.CostUSD != 6 || res.RunID == 0 {
		t.Fatalf("%+v", res)
	}
	if got["model"] != DefaultModel || hdr.Get("anthropic-version") != DefaultAPIVersion {
		t.Fatal(got, hdr)
	}
	msgs := got["messages"].([]any)
	if !strings.HasPrefix(msgs[0].(map[string]any)["content"].(string), "<writing-style>") {
		t.Fatal("no writing style in the API prompt")
	}
	o.APIKey = "wrong"
	res = New(o).Run(context.Background(), Request{Prompt: "plan"})
	if !res.IsError || res.ExitCode != 401 || !strings.Contains(res.Error, "authentication_error") {
		t.Fatalf("%+v", res)
	}
	o.APIKey = ""
	if ok, why := New(o).Configured(); ok || !strings.Contains(why, "anthropic") {
		t.Fatal(why)
	}
}

func TestRedact(t *testing.T) {
	if got := Redact("login failed for "+tokenPrefix+"abcdefghijklmnopqrstuvwxyz\nnext", 0); strings.Contains(got, "abcdefgh") || strings.Contains(got, "\n") {
		t.Fatal(got)
	}
}
