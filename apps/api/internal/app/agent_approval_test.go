package app

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/approvalkey"
	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/py"
)

// ADR-0022 amendment 1: the api signs the approval (task key, plan version, plan hash, approver, time, sealed
// inputs); a hash other than the stored plan's is refused; continue and rollback are signed the same way.
func TestAgentApprovalSigned(t *testing.T) {
	e := boardSetup(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	SetApprovalKey(priv)
	t.Cleanup(func() { SetApprovalKey(nil) })
	mode := ACTIONS_MODE
	ACTIONS_MODE = "on"
	t.Cleanup(func() { ACTIONS_MODE = mode })
	plan := `{"classification": {"environment": "develop", "kind": "change"}, "summary": "s", "plan": [` +
		`{"n": 1, "text": "check", "mode": "read-only", "capability": "http.check", "args": {"url": "https://svc.example.test/", "expect_status": 200}},` +
		`{"n": 2, "text": "start the pipeline", "mode": "operator"}]}`
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version, plan) VALUES ('gl-4', 'ready', 2, ?)`, plan)
	sha := agentPlanSHA(agentRow("gl-4"))
	if len(sha) != 64 {
		t.Fatal(sha)
	}
	if path, _ := agentSpinUp(py.AsMap(agentJSON(plan, "null"))); path != "execute" {
		t.Fatal(path)
	}
	// an executable plan needs the hash the page showed, and the stored plan's hash
	if c, out := AgentPostSpinUp(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2"), e.admin); c != 409 || !strings.Contains(out, "hash") {
		t.Fatal(c, out)
	}
	if c, _ := AgentPostSpinUp(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", strings.Repeat("a", 64)), e.admin); c != 409 {
		t.Fatal("a stale hash was approved")
	}
	c, loc := AgentPostSpinUp(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	p, _ := boardReadRequest(t, loc)
	claim := capability.ApprovalClaim{Kind: capability.SignStart, TaskKey: "gl-4", PlanVersion: 2, PlanSHA256: sha, Approver: e.admin.Username,
		At: int64(py.Num(p.Get("signed_at"))), SealedSHA256: capability.SealedSHA256(nil)}
	if p.S("plan_sha256") != sha || capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal("the approval is not signed by the api:", p.PyRepr())
	}

	// continue: only while the agent waits for the operator, pinned to the version and hash
	if c, _ := AgentPostContinue(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha, "step", "2"), e.admin); c != 409 {
		t.Fatal("continue while not waiting for the operator")
	}
	id := capability.ActionID("gl-4", 2, sha)
	e.board.Exec(`UPDATE agent_tasks SET state='operator', approved_sha256=?, action_id=? WHERE key='gl-4'`, sha, id)
	old := ACTIONS_DIR
	ACTIONS_DIR = t.TempDir()
	t.Cleanup(func() { ACTIONS_DIR = old })
	os.MkdirAll(filepath.Join(ACTIONS_DIR, "out"), 0o700)
	writeOut := func(state string, waits int) {
		b, _ := json.Marshal(map[string]any{"action_id": id, "state": state, "steps": []any{map[string]any{"n": 1, "status": "ok"},
			map[string]any{"n": waits, "status": "needs the operator"}}})
		os.WriteFile(filepath.Join(ACTIONS_DIR, "out", id+".json"), b, 0o600)
	}
	// the waiting step comes from actd's result, not from board.db
	e.board.Exec(`INSERT INTO agent_actions (action_id, step, key, status) VALUES (?, 3, 'gl-4', 'needs the operator')`, id)
	if c, _ := AgentPostContinue(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha, "step", "2"), e.admin); c != 409 {
		t.Fatal("continue without an executor result")
	}
	writeOut("operator", 2)
	if c, _ := AgentPostContinue(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha, "step", "3"), e.admin); c != 409 {
		t.Fatal("continue for a step the executor does not wait for")
	}
	if c, _ := AgentPostContinue(e.ctx(e.view), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha, "step", "2"), e.view); c != 403 {
		t.Fatal("viewer")
	}
	if c, _ := AgentPostContinue(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", strings.Repeat("b", 64), "step", "2"), e.admin); c != 409 {
		t.Fatal("continue with a stale hash")
	}
	c, loc = AgentPostContinue(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha, "step", "2"), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	p, _ = boardReadRequest(t, loc)
	claim.Kind, claim.Step, claim.At = capability.SignContinue, 2, int64(py.Num(p.Get("signed_at")))
	if p.S("kind") != "agent-continue" || capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal(p.PyRepr())
	}

	// rollback: of the task's last action only, signed against the approved hash
	e.board.Exec(`UPDATE agent_tasks SET action_id=NULL, approved_version=2 WHERE key='gl-4'`)
	if c, _ := AgentPostRollback(e.ctx(e.admin), boardForm("key", "gl-4", "action_id", id, "step", "1"), e.admin); c != 409 {
		t.Fatal("rollback of an action the task does not have")
	}
	e.board.Exec(`UPDATE agent_tasks SET action_id=?, approved_sha256=? WHERE key='gl-4'`, id, strings.Repeat("c", 64))
	if c, _ := AgentPostRollback(e.ctx(e.admin), boardForm("key", "gl-4", "action_id", id, "step", "1"), e.admin); c != 409 {
		t.Fatal("rollback with an approved hash other than the stored plan's")
	}
	e.board.Exec(`UPDATE agent_tasks SET approved_sha256=? WHERE key='gl-4'`, sha)
	c, loc = AgentPostRollback(e.ctx(e.admin), boardForm("key", "gl-4", "action_id", id, "step", "1"), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	p, _ = boardReadRequest(t, loc)
	claim.Kind, claim.Step, claim.At = capability.SignRollback, 1, int64(py.Num(p.Get("signed_at")))
	if p.S("action_id") != id || capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal(p.PyRepr())
	}

	// stop is signed, so actd can revoke the task's earlier approvals
	c, loc = AgentPost(e.ctx(e.admin), boardForm("key", "gl-4", "action", "stop"), e.admin)
	p, _ = boardReadRequest(t, loc)
	claim.Kind, claim.Step, claim.At = capability.SignStop, 0, int64(py.Num(p.Get("signed_at")))
	if c != 303 || p.S("kind") != "agent-stop" || capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal(c, p.PyRepr())
	}

	// without a key: an executable plan, a continue and a rollback are refused before anything is sent
	SetApprovalKey(nil)
	e.board.Exec(`UPDATE agent_tasks SET state='ready' WHERE key='gl-4'`)
	if c, _ := AgentPostSpinUp(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha), e.admin); c != 500 {
		t.Fatal("an executable plan approved without a signature", c)
	}
	if c, _ := AgentPostRollback(e.ctx(e.admin), boardForm("key", "gl-4", "action_id", id, "step", "1"), e.admin); c != 500 {
		t.Fatal("an unsigned rollback was sent", c)
	}
	// a plan that does not execute still spins up unsigned
	ACTIONS_MODE = "off"
	c, loc = AgentPostSpinUp(e.ctx(e.admin), boardForm("key", "gl-4", "plan_version", "2", "plan_sha256", sha), e.admin)
	p, _ = boardReadRequest(t, loc)
	if c != 303 || p.Has("signature") {
		t.Fatal(c, p.PyRepr())
	}
}

// ADR-0022 PR 3: the approval seals each secret input to actd's pinned key, for this action and step, and signs over
// the sealed inputs' hash; the request file holds the ciphertext only. A continue is signed over the stored ones.
func TestAgentApprovalSealsInputs(t *testing.T) {
	e := boardSetup(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	SetApprovalKey(priv)
	t.Cleanup(func() { SetApprovalKey(nil) })
	mode, kdir, adir := ACTIONS_MODE, APPROVAL_KEY_DIR, ACTIONS_DIR
	ACTIONS_MODE, APPROVAL_KEY_DIR, ACTIONS_DIR = "on", filepath.Join(t.TempDir(), "approve"), t.TempDir()
	t.Cleanup(func() { ACTIONS_MODE, APPROVAL_KEY_DIR, ACTIONS_DIR = mode, kdir, adir })
	os.Mkdir(APPROVAL_KEY_DIR, 0o700)
	value := "typed-" + "SECRET-" + "4417"
	plan := `{"classification": {"environment": "develop", "kind": "change"}, "summary": "s", "plan": [` +
		`{"n": 1, "text": "rotate", "mode": "WRITE", "capability": "gitlab.ci-variable.set", "args": {"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "value_ref": "input:NEW_TOKEN"}},` +
		`{"n": 2, "text": "check", "mode": "read-only", "capability": "gitlab.ci-variable.get", "args": {"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "matches": "input:NEW_TOKEN"}}]}`
	e.board.Exec(`INSERT INTO agent_tasks (key, state, plan_version, plan) VALUES ('gl-5', 'ready', 1, ?)`, plan)
	sha := agentPlanSHA(agentRow("gl-5"))
	id := capability.ActionID("gl-5", 1, sha)
	form := func(kv ...string) py.QS {
		return boardForm(append([]string{"key", "gl-5", "plan_version", "1", "plan_sha256", sha}, kv...)...)
	}
	if names := agentInputNames(agentPlanInputs(agentRow("gl-5"))); len(names) != 1 || names[0] != "NEW_TOKEN" {
		t.Fatal(names)
	}
	// no sealing key pinned: refused, nothing sent
	if c, out := AgentPostSpinUp(e.ctx(e.admin), form("input_NEW_TOKEN", value), e.admin); c != 409 || !strings.Contains(out, "pinned") || strings.Contains(out, value) {
		t.Fatal(c, out)
	}
	seal, _ := ecdh.X25519().GenerateKey(rand.Reader)
	pemText, _ := capability.SealPublicPEM(seal.PublicKey())
	if _, err := approvalkey.TrustActd(APPROVAL_KEY_DIR, pemText); err != nil {
		t.Fatal(err)
	}
	// the published copy differs from the pin: refused
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	op, _ := capability.SealPublicPEM(other.PublicKey())
	os.WriteFile(filepath.Join(ACTIONS_DIR, "actd.pub"), op, 0o644)
	if c, out := AgentPostSpinUp(e.ctx(e.admin), form("input_NEW_TOKEN", value), e.admin); c != 409 || !strings.Contains(out, "not the pinned one") {
		t.Fatal(c, out)
	}
	os.WriteFile(filepath.Join(ACTIONS_DIR, "actd.pub"), pemText, 0o644)
	// no value typed: refused with the input's name
	if c, out := AgentPostSpinUp(e.ctx(e.admin), form(), e.admin); c != 409 || !strings.Contains(out, "NEW_TOKEN") {
		t.Fatal(c, out)
	}
	c, loc := AgentPostSpinUp(e.ctx(e.admin), form("input_NEW_TOKEN", value), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	p, _ := boardReadRequest(t, loc)
	raw := p.PyRepr()
	if strings.Contains(raw, value) {
		t.Fatal("the value is in the request file")
	}
	sealed := map[string]string{}
	for _, k := range []string{"1/NEW_TOKEN", "2/NEW_TOKEN"} {
		s := py.Str(py.AsMap(p.Get("sealed_inputs")).Get(k))
		v, err := capability.OpenInput(seal, id, mustStep(k), "NEW_TOKEN", s)
		if err != nil || v != value {
			t.Fatal(k, err)
		}
		sealed[k] = s
	}
	claim := capability.ApprovalClaim{Kind: capability.SignStart, TaskKey: "gl-5", PlanVersion: 1, PlanSHA256: sha, Approver: e.admin.Username,
		At: int64(py.Num(p.Get("signed_at"))), SealedSHA256: capability.SealedSHA256(sealed)}
	if capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal("the start is not signed over the sealed inputs")
	}
	// a continue is signed over the sealed inputs the worker stored with the approval
	sb, _ := json.Marshal(sealed)
	e.board.Exec(`UPDATE agent_tasks SET state='operator', approved_sha256=?, action_id=?, sealed_inputs=? WHERE key='gl-5'`, sha, id, string(sb))
	os.MkdirAll(filepath.Join(ACTIONS_DIR, "out"), 0o700)
	ob, _ := json.Marshal(map[string]any{"action_id": id, "state": "operator", "steps": []any{map[string]any{"n": 2, "status": "needs the operator"}}})
	os.WriteFile(filepath.Join(ACTIONS_DIR, "out", id+".json"), ob, 0o600)
	c, loc = AgentPostContinue(e.ctx(e.admin), form("step", "2"), e.admin)
	if c != 303 {
		t.Fatal(c, loc)
	}
	p, _ = boardReadRequest(t, loc)
	claim.Kind, claim.Step, claim.At = capability.SignContinue, 2, int64(py.Num(p.Get("signed_at")))
	if capability.VerifyApproval(pub, claim, p.S("signature")) != nil {
		t.Fatal("the continue is not signed over the stored sealed inputs")
	}
}

func mustStep(k string) int {
	n, _, _ := capability.ParseInputKey(k)
	return n
}
