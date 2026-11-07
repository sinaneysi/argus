package claudetoken

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/claudegate"
)

var (
	good = "sk-ant-oat01-" + strings.Repeat("G00d_", 8) + "GOOD"
	bad  = "sk-ant-oat01-" + strings.Repeat("b4d-x", 8) + "BADX"
)

const fake = `#!/bin/sh
echo "$CLAUDE_CODE_OAUTH_TOKEN" > "%s"
case "$CLAUDE_CODE_OAUTH_TOKEN" in
  *GOOD) echo '{"type":"result","is_error":false,"result":"OK"}' ;;
  *LIMT) echo '{"type":"result","is_error":true,"result":"Claude AI usage limit reached"}'; exit 1 ;;
  *) echo '{"type":"result","is_error":true,"result":"Invalid API key '"$CLAUDE_CODE_OAUTH_TOKEN"'"}'; exit 1 ;;
esac
`

type env struct {
	t        *testing.T
	o        Options
	d        string
	marker   string
	original string
	found    claudegate.Found
	log      bytes.Buffer
	n        int
}

func setup(t *testing.T) *env {
	d := t.TempDir()
	e := &env{t: t, d: d, marker: filepath.Join(d, "claude-ran"), found: claudegate.Found{IP: "198.51.100.4", Loc: "NL", Source: "x"}}
	for _, s := range []string{"in", "out"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	bin := filepath.Join(d, "claude")
	os.WriteFile(bin, []byte(fmt.Sprintf(fake, e.marker)), 0o755)
	e.original = "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-" + strings.Repeat("0ld", 10) + "OLD1\n# keep me\nOTHER=1\n"
	os.WriteFile(filepath.Join(d, "claude.env"), []byte(e.original), 0o640)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(d, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE users (username TEXT, role TEXT, disabled INTEGER)")
	db.Exec("INSERT INTO users VALUES ('admin','admin',0),('view.er','viewer',0),('gone','admin',1)")
	db.Close()
	e.o = Options{ReqDir: filepath.Join(d, "in"), OutDir: filepath.Join(d, "out"), EnvFile: filepath.Join(d, "claude.env"),
		AuthDB: filepath.Join(d, "auth.db"), ClaudeBin: bin, EgressFile: filepath.Join(d, "egress.json"),
		EgressCheck: filepath.Join(d, "check.json"), AppUID: 65534, TestTimeout: 20 * time.Second, Log: &e.log,
		LookupFn: func(map[string]string, []string) claudegate.Found { return e.found }}
	return e
}

func (e *env) drop(user string, req map[string]any) string {
	e.n++
	rid := fmt.Sprintf("%016x", e.n)
	req["user"] = user
	b, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(e.o.ReqDir, user+"-"+rid+".json"), b, 0o600)
	return rid
}

func (e *env) apply() Status {
	e.t.Helper()
	if err := Apply(e.o); err != nil {
		e.t.Fatal(err)
	}
	if left, _ := os.ReadDir(e.o.ReqDir); len(left) != 0 {
		e.t.Fatalf("request files left: %v", left)
	}
	var st Status
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "claude-token-status.json"))
	if err := json.Unmarshal(b, &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) envText() string { b, _ := os.ReadFile(e.o.EnvFile); return string(b) }

func (e *env) backups() []string {
	m, _ := filepath.Glob(e.o.EnvFile + ".bak-*-claude")
	return m
}

func (e *env) noLeak(tokens ...string) {
	e.t.Helper()
	st, _ := os.ReadFile(filepath.Join(e.o.OutDir, "claude-token-status.json"))
	audit, _ := os.ReadFile(filepath.Join(e.o.OutDir, "audit.log"))
	for _, tok := range tokens {
		for _, text := range []string{e.log.String(), string(st), string(audit)} {
			if strings.Contains(text, tok) || strings.Contains(text, tok[13:len(tok)-4]) {
				e.t.Fatalf("the token leaked into: %s", text)
			}
		}
	}
}

func TestReplaceGoodKept(t *testing.T) {
	e := setup(t)
	rid := e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": good})
	st := e.apply()
	want := strings.Replace(e.original, strings.SplitN(e.original, "\n", 2)[0], Var+"="+good, 1)
	if e.envText() != want {
		t.Fatalf("env: %q", e.envText())
	}
	if fi, _ := os.Stat(e.o.EnvFile); fi.Mode().Perm() != 0o640 {
		t.Fatal("mode changed")
	}
	if b, _ := os.ReadFile(e.marker); strings.TrimSpace(string(b)) != good {
		t.Fatal("the test call did not run with the new token")
	}
	bk := e.backups()
	if len(bk) != 1 {
		t.Fatal(bk)
	}
	if b, _ := os.ReadFile(bk[0]); string(b) != e.original {
		t.Fatal("backup content")
	}
	if fi, _ := os.Stat(bk[0]); fi.Mode().Perm() != 0o640 {
		t.Fatal("backup mode")
	}
	if st.Test.Result != "ok" || st.Replaced["last4"] != "GOOD" || st.Token[Var]["last4"] != "GOOD" ||
		!strings.Contains(st.Results[rid]["result"].(string), "kept") {
		t.Fatalf("status: %+v", st)
	}
	audit, _ := os.ReadFile(filepath.Join(e.o.OutDir, "audit.log"))
	if !strings.Contains(string(audit), "admin: Claude token CLAUDE_CODE_OAUTH_TOKEN replaced (…GOOD); test ok; kept") {
		t.Fatal(string(audit))
	}
	e.noLeak(good)
}

func TestFailingRestoredUnlessSaveAnyway(t *testing.T) {
	e := setup(t)
	rid := e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": bad})
	st := e.apply()
	if e.envText() != e.original || st.Test.Result != "error" || !strings.Contains(st.Test.Message, "Invalid API key") ||
		!strings.Contains(st.Results[rid]["result"].(string), "previous token restored") || st.Replaced != nil {
		t.Fatalf("restore: %+v", st)
	}
	e.noLeak(bad)
	e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": bad, "save_anyway": true})
	st = e.apply()
	if !strings.Contains(e.envText(), Var+"="+bad+"\n") || st.Replaced["last4"] != "BADX" {
		t.Fatal("save anyway")
	}
	e.noLeak(bad)
	lim := good[:len(good)-4] + "LIMT"
	e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": lim})
	if st := e.apply(); st.Test.Result != "limit" || !strings.Contains(e.envText(), lim) {
		t.Fatal("a usage limit means the login works")
	}
}

func TestBlockedEgressNeverTests(t *testing.T) {
	e := setup(t)
	e.found = claudegate.Found{IP: "203.0.113.9", Loc: "IR", Source: "x"}
	e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": good})
	st := e.apply()
	if _, err := os.Stat(e.marker); err == nil {
		t.Fatal("claude ran from a blocked egress")
	}
	if st.Test.Result != "stopped" || !strings.Contains(st.Test.Message, "egress IP 203.0.113.9 is in IR") || e.envText() != e.original {
		t.Fatalf("%+v", st.Test)
	}
	rid := e.drop("admin", map[string]any{"kind": "egress"})
	if st := e.apply(); !strings.Contains(st.Results[rid]["result"].(string), "203.0.113.9 in IR: AI stopped") {
		t.Fatal(st.Results[rid])
	}
	if c := claudegate.ReadCheck(e.o.EgressCheck); c.Allowed || c.Loc != "IR" {
		t.Fatal("check file")
	}
}

func TestRefusals(t *testing.T) {
	e := setup(t)
	cases := []struct {
		user string
		req  map[string]any
	}{
		{"admin", map[string]any{"kind": "replace", "var": Var, "token": good[:16]}},
		{"admin", map[string]any{"kind": "replace", "var": "ANTHROPIC_API_KEY", "token": good}},
		{"view.er", map[string]any{"kind": "replace", "var": Var, "token": good}},
		{"gone", map[string]any{"kind": "test"}},
		{"admin", map[string]any{"kind": "rm"}},
		{"admin", map[string]any{"kind": "egress-settings", "guard": true, "blocked": []any{"IRN"}, "fail": "closed"}},
	}
	for _, c := range cases {
		rid := e.drop(c.user, c.req)
		if st := e.apply(); !strings.HasPrefix(st.Results[rid]["result"].(string), "refused: ") {
			t.Fatalf("%v: %v", c, st.Results[rid])
		}
	}
	os.WriteFile(filepath.Join(e.o.ReqDir, "admin-00000000000000ff.json"), []byte(`{"kind":"test","user":"x"}`), 0o600)
	if st := e.apply(); !strings.Contains(st.Results["00000000000000ff"]["result"].(string), "does not match") {
		t.Fatal("user mismatch")
	}
	os.Symlink(e.o.EnvFile, filepath.Join(e.o.ReqDir, "admin-00000000000000aa.json"))
	if st := e.apply(); !strings.HasPrefix(st.Results["00000000000000aa"]["result"].(string), "refused") {
		t.Fatal("symlink")
	}
	if e.envText() != e.original || len(e.backups()) != 0 {
		t.Fatal("changed")
	}
	if _, err := os.Stat(e.marker); err == nil {
		t.Fatal("claude ran")
	}
	e.noLeak(good)
}

func TestBackupsAndDuplicates(t *testing.T) {
	e := setup(t)
	f, _ := os.OpenFile(e.o.EnvFile, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("export " + Var + "=sk-ant-oat01-" + strings.Repeat("dup", 10) + "DUP1\n")
	f.Close()
	t0 := time.Now()
	for i := 0; i < 7; i++ {
		ts := t0.Add(time.Duration(i) * time.Second)
		e.o.Now = func() time.Time { return ts }
		e.drop("admin", map[string]any{"kind": "replace", "var": Var, "token": good[:len(good)-5] + fmt.Sprint(i) + "GOOD"})
		e.apply()
	}
	if n := len(e.backups()); n != 5 {
		t.Fatalf("%d backups", n)
	}
	lines := strings.Split(strings.TrimSpace(e.envText()), "\n")
	if lines[0] != Var+"="+good[:len(good)-5]+"6GOOD" || strings.Count(e.envText(), Var) != 1 || lines[1] != "# keep me" {
		t.Fatal(lines)
	}
}

func TestTestAndEgressSettings(t *testing.T) {
	e := setup(t)
	rid := e.drop("admin", map[string]any{"kind": "test"})
	if st := e.apply(); st.Test.Result != "error" || !strings.HasPrefix(st.Results[rid]["result"].(string), "test error") {
		t.Fatal(st.Test)
	}
	e.drop("admin", map[string]any{"kind": "egress-settings", "guard": false, "blocked": []any{"KP", "IR"}, "fail": "open"})
	e.apply()
	s := claudegate.LoadSettings(e.o.EgressFile)
	if s.Guard || strings.Join(s.Blocked, ",") != "IR,KP" || s.Fail != "open" {
		t.Fatalf("%+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "claude-egress.json")); !strings.Contains(string(b), `"fail":"open"`) {
		t.Fatal("live copy")
	}
	audit, _ := os.ReadFile(filepath.Join(e.o.OutDir, "audit.log"))
	if !strings.Contains(string(audit), "admin: Claude egress guard off") {
		t.Fatal(string(audit))
	}
}
