package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const secretCanary = "canary-Zq7vX2pL9sK4mN8w" // secret-scan: allow (fake test fixture)

func secretsOn(t *testing.T, e *boardTestEnv, on bool) {
	e.json(t, "out/board-status.json", map[string]any{"agent": map[string]any{"settings": map[string]any{"secrets": on}}})
}

func TestAgentSecretPost(t *testing.T) {
	e := boardSetup(t)
	SECRET_IN = filepath.Join(t.TempDir(), "secret-in")
	var logbuf bytes.Buffer
	oldOut := Out
	Out = &logbuf
	t.Cleanup(func() { Out = oldOut; SECRET_IN = "/secret-in" })
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version, plan) VALUES ('OPS-1', 'ready', 1, ?)`,
		`{"classification": {"environment": "develop", "kind": "investigate"}, "plan": []}`)
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version, plan) VALUES ('OPS-2', 'ready', 1, ?)`,
		`{"classification": {"environment": "release", "kind": "production"}, "plan": []}`)
	form := func(kv ...string) py.QS {
		base := []string{"key", "OPS-1", "name", "API_KEY", "value", secretCanary, "ttl_min", "60", "hosts", "dev-api.local", "purpose", "status"}
		for i := 0; i+1 < len(kv); i += 2 { // an override replaces the field (form.Get returns the first value)
			for j := 0; j+1 < len(base); j += 2 {
				if base[j] == kv[i] {
					base[j+1] = kv[i+1]
				}
			}
		}
		return boardForm(base...)
	}
	secretsOn(t, e, true)
	if c, _ := AgentPostSecret(e.ctx(e.view), form(), e.view); c != 403 {
		t.Fatal("a viewer cannot provide a secret", c)
	}
	if c, _ := AgentPostSecret(e.ctx(e.admin), form(), e.admin); c != 503 {
		t.Fatal("no tmpfs dir: refused, never created or a disk fallback", c)
	}
	if err := os.Mkdir(SECRET_IN, 0o700); err != nil {
		t.Fatal(err)
	}
	if c, _ := AgentPostSecret(e.ctx(e.admin), form("key", "OPS-2"), e.admin); c != 409 {
		t.Fatal("release task refused (D3)", c)
	}
	for _, kv := range [][]string{{"name", "api key"}, {"ttl_min", "1441"}, {"hosts", ""}, {"hosts", "a,b,c,d,e,f"},
		{"value", "short"}, {"value", "two\nlines-of-secret"}, {"hosts", "bad host"}} {
		if c, page := AgentPostSecret(e.ctx(e.admin), form(kv...), e.admin); c != 400 || strings.Contains(page, secretCanary) {
			t.Fatal("refused", kv, c)
		}
	}
	c, loc := AgentPostSecret(e.ctx(e.admin), form(), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/board/task?key=OPS-1&sec=") || !strings.HasSuffix(loc, "#secrets") || strings.Contains(loc, secretCanary) {
		t.Fatal(c, loc)
	}
	ents, _ := os.ReadDir(SECRET_IN)
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "alex.doe-") {
		t.Fatal("one write-once file named <user>-<rand>.json", ents)
	}
	st, _ := os.Stat(filepath.Join(SECRET_IN, ents[0].Name()))
	if st.Mode().Perm() != 0o600 {
		t.Fatal("0600", st.Mode())
	}
	data, _ := os.ReadFile(filepath.Join(SECRET_IN, ents[0].Name()))
	v, _ := py.ParseJSON(data)
	m := py.AsMap(v)
	if m.S("kind") != "secret" || m.S("value") != secretCanary || m.S("by") != "alex.doe" || m.S("name") != "API_KEY" {
		t.Fatal("request content")
	}
	if strings.Contains(logbuf.String(), secretCanary) || !strings.Contains(logbuf.String(), "task secret API_KEY for OPS-1") {
		t.Fatal("the log names the secret, never the value")
	}
	// nothing under settings-in (disk): the value goes to tmpfs only
	filepath.Walk(SET_IN, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secretCanary)) {
				t.Errorf("value on disk in %s", p)
			}
		}
		return nil
	})
	secretsOn(t, e, false)
	if c, _ := AgentPostSecret(e.ctx(e.admin), form(), e.admin); c != 409 {
		t.Fatal("feature off", c)
	}
	if PolicyOf("/board/task/secret", nil) != PolicySensitive {
		t.Fatal("providing a secret is a sensitive POST (MFA class sensitive)")
	}
}

// The task's secrets as data: metadata only; off means a note and no form; a viewer gets no form either.
func TestAPISecretsMetadataOnlyAndRoles(t *testing.T) {
	e := boardSetup(t)
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version, plan) VALUES ('OPS-1', 'ready', 1, ?)`,
		`{"classification": {"environment": "develop", "kind": "investigate"}, "plan": []}`)
	e.board.Exec(`INSERT INTO task_secrets (key, name, purpose, hosts, state, expires_at) VALUES
		('OPS-1', 'DEPLOY_TOKEN', 'read status', '["dev-api.local"]', 'requested', 30)`)
	a := agentRow("OPS-1")
	secretsOn(t, e, false)
	off := apiSecrets("OPS-1", a, "develop", e.admin)
	if off["new"] != nil || !strings.Contains(py.Str(off["note"]), "Task secrets are off") {
		t.Fatal("off", off)
	}
	secretsOn(t, e, true)
	if on := apiSecrets("OPS-1", a, "develop", e.admin); on["new"] == nil {
		t.Fatal("admin form", on)
	}
	vw := apiSecrets("OPS-1", a, "develop", e.view)
	b, _ := json.Marshal(vw)
	if vw["new"] != nil || strings.Contains(string(b), `"value"`) {
		t.Fatal("viewer", string(b))
	}
}
