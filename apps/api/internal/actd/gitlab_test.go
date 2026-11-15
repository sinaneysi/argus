package actd

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		Token: func() (string, error) { return gitlabtest.Token, nil }, Backups: Vault{Dir: filepath.Join(f.act, "backups"), Key: f.key, Steps: filepath.Join(f.act, "steps")},
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
		s, err := capability.SealInput(f.seal.PublicKey(), req.ActionID, req.PlanSHA256, n, "NEW_TOKEN", value)
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
	other, _ := capability.SealInput(f.seal.PublicKey(), req.ActionID, req.PlanSHA256, 1, "NEW_TOKEN", "attacker-value")
	swapped.SealedInputs = map[string]string{capability.InputKey(1, "NEW_TOKEN"): other}
	if r := f.submit(d, swapped); r.Refusal == nil || r.Refusal.Rule != 3 || !strings.Contains(r.Refusal.Reason, "sealed") {
		t.Fatalf("%+v", r.Refusal)
	}
	// an input sealed for another step does not open for this one
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='GL-3'`)
	moved := req
	s2, _ := capability.SealInput(f.seal.PublicKey(), req.ActionID, req.PlanSHA256, 2, "NEW_TOKEN", glSecret)
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

// TestContinueKeepsTheStartsSealedInputs: the worker swaps a ciphertext before Continue (here a guess for the
// matches read, a value oracle) and even gets a continue signed over the swapped set: refused, nothing read.
func TestContinueKeepsTheStartsSealedInputs(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	steps := []map[string]any{glSet(), {"mode": "operator", "text": "start the develop pipeline"}, glGet()}
	steps[2]["n"] = 3
	req := f.task("GL-C", "develop", steps)
	f.sealFor("GL-C", &req, glSecret, 1, 3)
	if r := f.submit(d, req); r.State != StateOperator {
		t.Fatalf("%+v", r)
	}
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='GL-C'`)
	stub.Reset()
	cont := func(in map[string]string) *Response {
		rq := req
		rq.SealedInputs = in
		claim := capability.ApprovalClaim{Kind: capability.SignContinue, TaskKey: "GL-C", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
			At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(in), Step: 2}
		rq.Continue = &Signed{Step: 2, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
		return f.submit(d, rq)
	}
	guess, _ := capability.SealInput(f.seal.PublicKey(), req.ActionID, req.PlanSHA256, 3, "NEW_TOKEN", "a-guess")
	oracle := map[string]string{capability.InputKey(1, "NEW_TOKEN"): req.SealedInputs[capability.InputKey(1, "NEW_TOKEN")], capability.InputKey(3, "NEW_TOKEN"): guess}
	if r := cont(oracle); r.State != StateRefused || r.Refusal.Rule != 3 || !strings.Contains(r.Refusal.Reason, "sealed inputs") {
		t.Fatalf("%+v", r.Refusal)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Fatalf("%d GitLab calls after a swapped continue", n)
	}
	if r := cont(req.SealedInputs); r.State != StateDone || r.Steps[2].Fields["matches"] != true {
		t.Fatalf("%+v", r)
	}
	// a rollback with swapped sealed inputs is refused too
	f.exec(f.board, `UPDATE agent_tasks SET state='done' WHERE key='GL-C'`)
	rq := req
	rq.SealedInputs = oracle
	claim := capability.ApprovalClaim{Kind: capability.SignRollback, TaskKey: "GL-C", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(oracle), Step: 1}
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
	wantRefused(t, f.submit(d, rq), 3, capability.CodeApproval)
}

// TestWriteErrorReadsBack: the PUT reaches GitLab but its answer is a gateway timeout: the read-back shows it, the
// step is ok. When the read-back does not show it, the outcome is unknown: not stored, the marker stays, a retry is
// refused for a person to check.
func TestWriteErrorReadsBack(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old"})
	stub.FailAfter["PUT"] = 504
	req := f.task("GL-T", "develop", []map[string]any{glSet()})
	f.sealFor("GL-T", &req, glSecret, 1)
	if r := f.submit(d, req); r.State != StateDone || r.Steps[0].Status != "ok" || !strings.Contains(r.Steps[0].Detail, "read-back matches") {
		t.Fatalf("%+v", r)
	}
	delete(stub.FailAfter, "PUT")
	stub.Status["PUT"] = 504 // not applied this time
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old"})
	req2 := f.task("GL-U", "develop", []map[string]any{glSet()})
	f.sealFor("GL-U", &req2, glSecret, 1)
	r := f.submit(d, req2)
	if r.State != StateUnknown || r.Steps[0].Status != StatusUnknown || !strings.Contains(r.Steps[0].Detail, "outcome is unknown") {
		t.Fatalf("%+v", r)
	}
	if !d.store.Applying(req2.ActionID, 1) {
		t.Fatal("the interrupted marker was removed")
	}
	if _, ok := d.store.Get(req2.ActionID, 1); ok {
		t.Fatal("an unknown outcome was stored as a result")
	}
	f.reapprove("GL-U", req2)
	wantRefused(t, f.submit(d, req2), 5, capability.CodeInterrupted)
}

// TestLiteralValueNeverInAudit: a plan with a literal where value_ref belongs is refused, and the audit and the
// ops rows hold "<redacted>", not the literal.
func TestLiteralValueNeverInAudit(t *testing.T) {
	f, _ := newGLFx(t)
	d := f.daemon()
	s := glSet()
	s["args"].(map[string]any)["value_ref"] = glSecret
	req := f.task("GL-L", "develop", []map[string]any{s})
	if r := f.submit(d, req); r.State != StateRefused {
		t.Fatalf("%+v", r)
	}
	for _, dir := range []string{f.act, f.actions, filepath.Dir(f.ops)} {
		if p := dirHas(t, dir, glSecret); p != "" && !strings.HasSuffix(p, "board.db") {
			t.Fatal("the literal is in", p)
		}
	}
}

// TestStatusHasNoCredentialDigits: the shared-volume status names the credentials only.
func TestStatusHasNoCredentialDigits(t *testing.T) {
	f, _ := newGLFx(t)
	if err := SetCredential(f.act, f.key, capability.GitLabCredential, "glx-"+"abcdefghijklmnop-WXYZ"); err != nil {
		t.Fatal(err)
	}
	d := f.daemon()
	d.o.Credentials = func() []Credential { return ListCredentials(f.act, f.key) }
	st := d.CurrentStatus()
	if len(st.Credentials) != 1 || st.Credentials[0].Name != capability.GitLabCredential || st.Credentials[0].Last4 != "" {
		t.Fatalf("%+v", st.Credentials)
	}
}

// TestPruneKeepsAppliedWrites: only backups of steps that are over without a change in place (rolled back, refused,
// failed) and older than the retention are removed; the backup of an applied write stays.
func TestPruneKeepsAppliedWrites(t *testing.T) {
	f, _ := newGLFx(t)
	v := Vault{Dir: filepath.Join(f.act, "backups"), Key: f.key, Steps: filepath.Join(f.act, "steps")}
	st := Store{Dir: v.Steps}
	id := "PR-1.v1.abcdefabcdef"
	for n, status := range map[int]string{1: "ok", 2: "rolled-back", 3: "rolled-back"} {
		ref := capability.BackupRefOf(id, n)
		if err := v.Put(ref, "m", []byte("x")); err != nil {
			t.Fatal(err)
		}
		st.Put(id, StepResult{N: n, Status: status})
	}
	old := f.now.Add(-40 * 24 * time.Hour)
	for _, n := range []string{"1", "2"} {
		os.Chtimes(filepath.Join(v.Dir, id, n+".enc"), old, old)
	}
	removed, kept, err := v.Prune(30, f.now)
	if err != nil || removed != 1 || kept != 2 {
		t.Fatal(removed, kept, err)
	}
	if _, _, err := v.Get(capability.BackupRefOf(id, 1)); err != nil {
		t.Fatal("the applied write's backup was pruned")
	}
	// a backup that exists without a stored result is not overwritten
	ref := capability.BackupRefOf(id, 4)
	v.Put(ref, "m", []byte("first"))
	if err := v.Put(ref, "m", []byte("second")); err == nil {
		t.Fatal("an interrupted step's backup was overwritten")
	}
}

// unknownOutcome leaves step 1 of a set with an unknown outcome (the PUT answers 504 and is not applied).
func unknownOutcome(t *testing.T, f *fx, stub *gitlabtest.Stub, d *Daemon, key string) Request {
	t.Helper()
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old", Masked: true})
	stub.Status["PUT"] = 504
	req := f.task(key, "develop", []map[string]any{glSet(), glGet()})
	f.sealFor(key, &req, glSecret, 1, 2)
	if r := f.submit(d, req); r.State != StateUnknown {
		t.Fatalf("%+v", r)
	}
	delete(stub.Status, "PUT")
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key=?`, key)
	return req
}

func (f *fx) checked(req Request, verdict string, sig func(capability.ApprovalClaim) string) *Response {
	f.t.Helper()
	claim := capability.ApprovalClaim{Kind: capability.SignChecked, TaskKey: req.TaskKey, PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(req.SealedInputs), Step: 1, Verdict: verdict}
	rq := req
	rq.Checked = &Signed{Step: 1, Approver: "ada", At: claim.At, Verdict: verdict, Signature: sig(claim)}
	return f.submit(f.cur, rq)
}

// TestCheckedApplied: the admin says applied; the step is ok only when the target reads back right, then the
// action goes on, and the step can be rolled back from its backup.
func TestCheckedApplied(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	req := unknownOutcome(t, f, stub, d, "GL-K")
	// applied, but the target does not hold the value: refused, the marker stays
	wantRefused(t, f.checked(req, capability.VerdictApplied, f.sign), 5, capability.CodeInterrupted)
	if !d.store.Applying(req.ActionID, 1) {
		t.Fatal("the marker was cleared")
	}
	// the person fixed it by hand: now applied holds
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: glSecret, Masked: true})
	r := f.checked(req, capability.VerdictApplied, f.sign)
	if r.State != StateDone || len(r.Steps) != 2 || r.Steps[0].Status != "ok" || r.Steps[1].Fields["matches"] != true {
		t.Fatalf("%+v", r)
	}
	if d.store.Applying(req.ActionID, 1) {
		t.Fatal("the marker stayed")
	}
	// a replay finds no unknown outcome
	f.exec(f.board, `UPDATE agent_tasks SET state='approved' WHERE key='GL-K'`)
	if r := f.checked(req, capability.VerdictApplied, f.sign); r.State != StateRefused {
		t.Fatalf("%+v", r)
	}
	// the step rolls back from its backup
	f.exec(f.board, `UPDATE agent_tasks SET state='done' WHERE key='GL-K'`)
	claim := capability.ApprovalClaim{Kind: capability.SignRollback, TaskKey: "GL-K", PlanVersion: 1, PlanSHA256: req.PlanSHA256, Approver: "ada",
		At: f.now.Unix(), SealedSHA256: capability.SealedSHA256(req.SealedInputs), Step: 1}
	rq := req
	rq.Rollback = &Signed{Step: 1, Approver: "ada", At: claim.At, Signature: f.sign(claim)}
	if rr := f.submit(d, rq); rr.State != StateDone {
		t.Fatalf("%+v", rr)
	}
	if v, _ := stub.Get(101, "API_TOKEN"); v.Value != "old" || !v.Masked {
		t.Fatal("not rolled back:", v)
	}
}

// TestCheckedNotApplied: the step fails, the action ends, the marker is gone; unsigned or forged verdicts are
// refused.
func TestCheckedNotApplied(t *testing.T) {
	f, stub := newGLFx(t)
	d := f.daemon()
	req := unknownOutcome(t, f, stub, d, "GL-N")
	_, wpriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, sig := range map[string]func(capability.ApprovalClaim) string{
		"unsigned": func(capability.ApprovalClaim) string { return "" },
		"forged":   func(c capability.ApprovalClaim) string { s, _ := capability.SignApproval(wpriv, c); return s },
		"other verdict": func(c capability.ApprovalClaim) string {
			c.Verdict = capability.VerdictApplied
			return f.sign(c)
		},
	} {
		if r := f.checked(req, capability.VerdictNotApplied, sig); r.State != StateRefused || r.Refusal.Rule != 3 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	stub.Reset()
	r := f.checked(req, capability.VerdictNotApplied, f.sign)
	if r.State != StateFailed || len(r.Steps) != 1 || r.Steps[0].Status != "failed" || r.Steps[0].Phase != PhaseChecked {
		t.Fatalf("%+v", r)
	}
	if d.store.Applying(req.ActionID, 1) || len(stub.Writes()) != 0 {
		t.Fatal("the marker stayed, or something was written")
	}
	last := f.auditLines()[len(f.auditLines())-1]
	if last.Phase != PhaseChecked || last.Result != "failed" {
		t.Fatalf("%+v", last)
	}
}

// TestRefusedAfterBackupIsStored: the audit record of the apply cannot be written after the backup: the step is
// stored as refused (a retry does not apply again) and its backup ages out.
func TestRefusedAfterBackupIsStored(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	f, stub := newGLFx(t)
	d := f.daemon()
	stub.Put(101, "API_TOKEN", gitlabtest.Var{Value: "old"})
	stub.AfterW = func(*gitlabtest.Stub) { os.Chmod(AuditPath(f.act), 0o400) }
	req := f.task("GL-R", "develop", []map[string]any{glSet()})
	f.sealFor("GL-R", &req, glSecret, 1)
	r := f.submit(d, req)
	os.Chmod(AuditPath(f.act), 0o600)
	if r.State != StateRefused || r.Steps[0].BackupRef == "" {
		t.Fatalf("%+v", r)
	}
	got, ok := d.store.Get(req.ActionID, 1)
	if !ok || got.Status != StateRefused {
		t.Fatalf("not stored as refused: %+v %v", got, ok)
	}
	p := filepath.Join(f.act, "backups", req.ActionID, "1.enc")
	old := f.now.Add(-40 * 24 * time.Hour)
	os.Chtimes(p, old, old)
	if removed, _, err := d.vault().Prune(30, f.now); err != nil || removed != 1 {
		t.Fatal(removed, err)
	}
}
