package board

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/capability/gitlabtest"
	"github.com/sinaneysi/argus/internal/secret"
)

// TestSealedInputsPassThrough (ADR-0022 PR 3): the api seals the value; the worker stores and forwards the ciphertext
// unopened; actd opens it and sets the variable. The value is never in board.db, the request file, the run job file,
// the agent_actions rows or the comment.
func TestSealedInputsPassThrough(t *testing.T) {
	value := "board-" + "SECRET-" + "never-here-5521"
	e := &actEnv{env: agentEnv(t)}
	stub := gitlabtest.New(t)
	actDir := filepath.Join(e.d, "act")
	os.Mkdir(actDir, 0o700)
	key, _ := secret.ParseKey(secret.NewKeyText())
	gl, err := capability.NewGitLab(capability.GitLabOptions{URL: gitlabtest.URL, Projects: []string{"shop/checkout"}, AllowPlain: true,
		Token: func() (string, error) { return gitlabtest.Token, nil }, Backups: actd.Vault{Dir: filepath.Join(actDir, "backups"), Key: key},
		Transport: stub.Transport()})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := capability.NewRegistry(gl.Capabilities()...)
	var pub ed25519.PublicKey
	pub, e.priv, _ = ed25519.GenerateKey(rand.Reader)
	seal, _ := ecdh.X25519().GenerateKey(rand.Reader)
	actions := filepath.Join(e.d, "actions")
	e.o.Agent.ActionsDir, e.o.Agent.ActionsMode, e.o.Agent.Registry = actions, "on", reg
	a, _ := sql.Open("sqlite", e.o.AuthDB)
	a.Exec(`CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT)`)
	a.Close()
	d, err := actd.New(actd.Options{Mode: capability.ModeOn, ActDir: actDir, Key: key, ActionsDir: actions, BoardDB: e.o.DB, AuthDB: e.o.AuthDB,
		OpsDB: filepath.Join(e.d, "ops.db"), Registry: reg, TTL: 24 * time.Hour, PerDay: 20, ApprovalPub: pub, SealKey: seal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	e.ex = d

	e.run(t)
	set := map[string]any{"text": "rotate API_TOKEN", "mode": "WRITE", "capability": capability.GitLabVarSet,
		"args": map[string]any{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "value_ref": "input:NEW_TOKEN"}}
	e.out(t, "OPS-7", "intake", classified("develop", "change", set), map[string]any{"cost_usd": 0.2, "is_error": false})
	e.run(t)
	sha := e.q(t, `SELECT plan_sha256 FROM agent_tasks WHERE key='OPS-7'`)
	id := capability.ActionID("OPS-7", 1, sha)
	// what the api does at approval: seal the typed value to actd's key, sign over the sealed inputs' hash
	sv, err := capability.SealInput(seal.PublicKey(), id, sha, 1, "NEW_TOKEN", value)
	if err != nil {
		t.Fatal(err)
	}
	sealed := map[string]string{capability.InputKey(1, "NEW_TOKEN"): sv}
	at := time.Now().Unix()
	sig, _ := capability.SignApproval(e.priv, capability.ApprovalClaim{Kind: capability.SignStart, TaskKey: "OPS-7", PlanVersion: 1, PlanSHA256: sha,
		Approver: "alex.admin", At: at, SealedSHA256: capability.SealedSHA256(sealed)})
	sealedAny := map[string]any{capability.InputKey(1, "NEW_TOKEN"): sv}
	// a malformed sealed input is refused by the worker
	bad := e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1, "plan_sha256": sha, "signed_at": at, "signature": sig,
		"sealed_inputs": map[string]any{"1/NEW_TOKEN": value}})
	if st := e.run(t); !strings.Contains(result(st, bad), "sealed") {
		t.Fatal(result(st, bad))
	}
	ok := e.req(t, map[string]any{"kind": "agent-start", "key": "OPS-7", "plan_version": 1, "plan_sha256": sha, "signed_at": at, "signature": sig,
		"sealed_inputs": sealedAny})
	if st := e.run(t); result(st, ok) != "approved; spinning up" {
		t.Fatal(result(st, ok))
	}
	reqFile, err := os.ReadFile(filepath.Join(actions, "in", id+".json"))
	if err != nil || !strings.Contains(string(reqFile), sv) || strings.Contains(string(reqFile), value) {
		t.Fatal("the request file does not carry the ciphertext only:", err)
	}
	e.actd(t)
	e.run(t)
	if v, ok := stub.Get(101, "API_TOKEN"); !ok || v.Value != value {
		t.Fatal("not set:", v, ok)
	}
	job := readJob(t, e.env, "OPS-7.run.json")
	if asMap(job["capability_results"])["state"] != "done" {
		t.Fatal(job["capability_results"])
	}
	e.out(t, "OPS-7", "run", map[string]any{"status": "done", "summary": "Rotated."}, map[string]any{"cost_usd": 0.1, "is_error": false})
	e.run(t)
	body := e.q(t, `SELECT payload FROM outbox WHERE kind='comment' ORDER BY id DESC LIMIT 1`)
	if !strings.Contains(body, "| 1 | gitlab.ci-variable.set | ok") {
		t.Fatal(body)
	}
	rows := e.q(t, `SELECT group_concat(COALESCE(summary,'') || COALESCE(detail,''), '|') FROM agent_actions`)
	for what, s := range map[string]string{"comment": body, "agent_actions": rows} {
		if strings.Contains(s, value) || strings.Contains(s, sv) {
			t.Fatal("the value or its ciphertext is in the", what)
		}
	}
	for _, dir := range []string{e.o.Agent.JobsDir, e.o.Agent.OutDir, actions, filepath.Dir(e.o.DB)} {
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && !strings.HasPrefix(p, actDir) {
				if b, _ := os.ReadFile(p); strings.Contains(string(b), value) {
					t.Fatal("the value is in", p)
				}
			}
			return nil
		})
	}
}
