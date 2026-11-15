package actd

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/capability/gitlabtest"
)

// glSecret is the synthetic value the no-leak test looks for everywhere actd writes.
var glSecret = "gl-" + "VALUE-" + "never-leak-7731"

// newGLFx is the fixture with the gitlab.ci-variable family on a stub, its backups in the executor volume.
func newGLFx(t *testing.T) (*fx, *gitlabtest.Stub) {
	t.Helper()
	f := newFx(t)
	stub := gitlabtest.New(t)
	gl, err := capability.NewGitLab(capability.GitLabOptions{URL: gitlabtest.URL, Projects: []string{"shop/checkout"}, AllowPlain: true,
		Token: func() (string, error) { return gitlabtest.Token, nil }, Backups: Vault{Dir: filepath.Join(f.act, "backups"), Key: f.key},
		Transport: stub.Transport()})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := f.reg.Get(capability.HTTPCheckName)
	if f.reg, err = capability.NewRegistry(append([]capability.Capability{h}, gl.Capabilities()...)...); err != nil {
		t.Fatal(err)
	}
	return f, stub
}

func glSet() map[string]any {
	return map[string]any{"mode": "WRITE", "text": "rotate the token", "capability": capability.GitLabVarSet,
		"args": map[string]any{"project": "shop/checkout", "key": "API_TOKEN", "environment_scope": "develop", "value_ref": "input:NEW_TOKEN", "masked": true}}
}

func glGet() map[string]any {
	return map[string]any{"mode": "read-only", "text": "check it", "capability": capability.GitLabVarGet,
		"args": map[string]any{"project": "101", "key": "API_TOKEN", "environment_scope": "develop", "matches": "input:NEW_TOKEN"}}
}

// sealFor seals the value for every step that refers to NEW_TOKEN and approves again over those sealed inputs.
func (f *fx) sealFor(key string, req *Request, value string, steps ...int) {
	f.t.Helper()
	req.SealedInputs = map[string]string{}
	for _, n := range steps {
		s, err := capability.SealInput(f.seal.PublicKey(), req.ActionID, n, "NEW_TOKEN", value)
		if err != nil {
			f.t.Fatal(err)
		}
		req.SealedInputs[capability.InputKey(n, "NEW_TOKEN")] = s
	}
	f.reapprove(key, *req)
}

// dirHas reports a file under dir whose bytes hold s.
func dirHas(t *testing.T, dir, s string) string {
	t.Helper()
	hit := ""
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), s) {
				hit = p
			}
		}
		return nil
	})
	return hit
}

// TestGitLabSetNoLeak: set, verify, a read that matches, a manual rollback; the value is never in the result, in
// actions/out, the audit, the ops rows, the stored steps, the backups (sealed), the status or the log.
func TestGitLabSetNoLeak(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	req := f.task("GL-1", "develop", []map[string]any{glSet(), glGet()})
	f.sealFor("GL-1", &req, glSecret, 1, 2)
	r := f.submit(d, req)
	if r.State != StateDone || len(r.Steps) != 2 || r.Steps[0].Status != "ok" || r.Steps[0].Phase != PhaseVerify || r.Steps[1].Fields["matches"] != true {
		t.Fatalf("%+v", r)
	}
	if v, ok := stub.Get(101, "API_TOKEN"); !ok || v.Value != glSecret || !v.Masked {
		t.Fatal("not set:", v)
	}
	if r.Steps[0].BackupRef != "backup:"+req.ActionID+"/1" {
		t.Fatal(r.Steps[0].BackupRef)
	}
	phases := []string{}
	for _, l := range f.auditLines() {
		phases = append(phases, l.Phase)
	}
	if got := strings.Join(phases, ","); got != "plan,apply,verify,plan,apply" {
		t.Fatal("audit:", got)
	}
	// the manual rollback deletes the variable (it did not exist before)
	f.exec(f.board, `UPDATE agent_tasks SET state='done' WHERE key='GL-1'`)
	claim := capability.ApprovalClaim{Kind: capability.SignRollback, TaskKey: "GL-1", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(req.SealedInputs), Step: 1}
	rq := req
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
	if rr := f.submit(d, rq); rr.State != StateDone || rr.Steps[0].Status != "rolled-back" {
		t.Fatalf("%+v", rr)
	}
	if _, ok := stub.Get(101, "API_TOKEN"); ok {
		t.Fatal("the rollback did not delete the created variable")
	}
	d.WriteStatus()
	for _, dir := range []string{f.act, f.actions, filepath.Dir(f.ops)} {
		if p := dirHas(t, dir, glSecret); p != "" {
			t.Fatal("the value is in", p)
		}
	}
	db, _ := sql.Open("sqlite", f.ops)
	defer db.Close()
	var rows string
	db.QueryRow(`SELECT group_concat(COALESCE(detail,'') || args, '|') FROM action_events`).Scan(&rows)
	if rows == "" || strings.Contains(rows, glSecret) || strings.Contains(f.logs.String(), glSecret) {
		t.Fatal("the value is in the ops rows or the log")
	}
	// the backup is sealed: it opens with the executor key only
	meta, plain, err := Vault{Dir: filepath.Join(f.act, "backups"), Key: f.key}.Get(capability.BackupRef(r.Steps[0].BackupRef))
	if err != nil || !strings.Contains(meta, "project=101 key=API_TOKEN scope=develop") || !strings.Contains(string(plain), `"existed":false`) {
		t.Fatal(meta, string(plain), err)
	}
}

// TestGitLabVerifyFailRollsBack: someone else changes the value right after the write: verify fails, the old value
// and attributes come back.
func TestGitLabVerifyFailRollsBack(t *testing.T) {
	f, stub := newGLFx(t)
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old-" + glSecret, Protected: true})
	stub.AfterW = func(s *gitlabtest.Stub) {
		s.AfterW = nil
		s.Put(101, "API_TOKEN", gitlabtest.Var{Value: "someone-else"})
	}
	d := f.daemon()
	req := f.task("GL-2", "develop", []map[string]any{glSet()})
	f.sealFor("GL-2", &req, glSecret, 1)
	r := f.submit(d, req)
	if r.State != StateFailed || r.Steps[0].Status != "rolled-back" {
		t.Fatalf("%+v", r)
	}
	if v, _ := stub.Get(101, "API_TOKEN"); v.Value != "old-"+glSecret || !v.Protected || v.Masked {
		t.Fatal("not restored:", v)
	}
	if p := dirHas(t, f.actions, glSecret); p != "" || strings.Contains(f.logs.String(), glSecret) {
		t.Fatal("the value leaked", p)
	}
}

// TestSealedInputRules: missing, extra, replaced or moved sealed inputs are refused before any call.
func TestSealedInputRules(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	// no sealed input
	req := f.task("GL-3", "develop", []map[string]any{glSet()})
	wantRefused(t, f.submit(d, req), 3, capability.CodeApproval)
	// replaced after approval: the signature covers the sealed inputs' hash
	f.sealFor("GL-3", &req, glSecret, 1)
	swapped := req
	other, _ := capability.SealInput(f.seal.PublicKey(), req.ActionID, 1, "NEW_TOKEN", "attacker-value")
	swapped.SealedInputs = map[string]string{capability.InputKey(1, "NEW_TOKEN"): other}
	if r := f.submit(d, swapped); r.Refusal == nil || r.Refusal.Rule != 3 || !strings.Contains(r.Refusal.Reason, "sealed") {
		t.Fatalf("%+v", r.Refusal)
	}
	// an input sealed for another step does not open for this one
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='GL-3'`)
	moved := req
	s2, _ := capability.SealInput(f.seal.PublicKey(), req.ActionID, 2, "NEW_TOKEN", glSecret)
	moved.SealedInputs = map[string]string{capability.InputKey(1, "NEW_TOKEN"): s2}
	f.reapprove("GL-3", moved)
	if r := f.submit(d, moved); r.State != StateRefused || r.Refusal.Rule != 3 {
		t.Fatalf("%+v", r)
	}
	// an extra input no step uses
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='GL-3'`)
	extra := req
	extra.SealedInputs = map[string]string{capability.InputKey(1, "NEW_TOKEN"): req.SealedInputs[capability.InputKey(1, "NEW_TOKEN")],
		capability.InputKey(1, "OTHER"): other}
	f.reapprove("GL-3", extra)
	wantRefused(t, f.submit(d, extra), 3, capability.CodeApproval)
	if n := len(stub.Writes()); n != 0 {
		t.Fatalf("%d writes", n)
	}
}

// TestGitLabScopeRefusedByActd: a stored plan with another scope never reaches GitLab.
func TestGitLabScopeRefusedByActd(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	for i, sc := range []string{"*", "", "pre-release", "release", "Develop"} {
		s := glSet()
		s["args"].(map[string]any)["environment_scope"] = sc
		key := "GL-S" + string(rune('A'+i))
		req := f.task(key, "develop", []map[string]any{s})
		f.sealFor(key, &req, glSecret, 1)
		if r := f.submit(d, req); r.State != StateRefused || r.Refusal.Rule != 4 {
			t.Fatalf("%q: %+v", sc, r)
		}
	}
	if n := len(stub.Calls()); n != 0 {
		t.Fatalf("%d calls", n)
	}
}

// TestSealKeyPublished: actd publishes its sealing public key in the shared volume.
func TestSealKeyPublished(t *testing.T) {
	f, _ := newGLFx(t)
	f.daemon()
	b, err := os.ReadFile(SealPubPath(f.actions))
	if err != nil {
		t.Fatal(err)
	}
	p, err := capability.ParseSealPublic(b)
	if err != nil || !p.Equal(f.seal.PublicKey()) {
		t.Fatal(err)
	}
	k1, err := LoadSealKey(f.act, true)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadSealKey(f.act, false)
	if err != nil || !k1.Equal(k2) {
		t.Fatal("the key is not kept", err)
	}
	if fi, _ := os.Stat(SealKeyPath(f.act)); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
}
