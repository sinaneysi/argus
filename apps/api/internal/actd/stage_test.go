package actd

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
)

// ADR-0022 PR 5 review: the model's develop classification is not enough for a write step. The start approval signs
// the hash of the board labels the admin saw; actd recomputes it from the request's labels and needs them to say
// stage. Labels the worker writes after the approval do not verify.
func TestWriteStepNeedsSignedStageLabels(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	// signs the start over these labels and returns the request carrying them
	approved := func(key string, labels []string) Request {
		req := f.task(key, "develop", []map[string]any{writeStep()})
		req.EnvLabels = labels
		f.reapprove(key, req)
		return req
	}
	refused := func(name string, req Request, rule int, code capability.Code) {
		t.Helper()
		r := f.submit(d, req)
		wantRefused(t, r, rule, code)
	}
	// approved labels without stage evidence: rule 2
	refused("no stage label", approved("LB-a", []string{"ops"}), 2, capability.CodeEnvironment)
	refused("production label", approved("LB-b", []string{"env:stage", "Production"}), 2, capability.CodeEnvironment)
	refused("no labels at all", approved("LB-c", nil), 2, capability.CodeEnvironment)
	// the worker adds env:stage after an approval of ["ops"]: the labels no longer verify with the signature (rule 3)
	req := approved("LB-d", []string{"ops"})
	req.EnvLabels = []string{"ops", "env:stage"}
	refused("label added after approval", req, 3, capability.CodeApproval)
	// labels hash mismatch: the start signed one set, the request carries another that also says stage
	req = approved("LB-e", []string{"env:stage", "team-a"})
	req.EnvLabels = []string{"env:stage"}
	refused("labels hash mismatch", req, 3, capability.CodeApproval)
	// the worker drops the labels: the signed hash no longer verifies either
	req = approved("LB-f", []string{"env:stage"})
	req.EnvLabels = nil
	refused("labels dropped", req, 2, capability.CodeEnvironment)
	if w.applies != 0 {
		t.Fatal("a write step ran without signed stage labels")
	}
	// the approved stage labels, unchanged: the write runs
	if r := f.submit(d, approved("LB-ok", []string{"env:stage", "ops"})); r.State != StateDone || w.applies != 1 {
		t.Fatalf("approved stage labels: %+v", r)
	}
	// a read-tier step needs no label (O8)
	rq := f.task("LB-read", "develop", []map[string]any{check("/ok", 200)})
	rq.EnvLabels = nil
	f.reapprove("LB-read", rq)
	if r := f.submit(d, rq); r.State != StateDone {
		t.Fatalf("read tier: %+v", r)
	}
}

// The heartbeat is signed with keys/status.key; a changed field or another key does not verify.
func TestStatusSigned(t *testing.T) {
	f := newFx(t)
	k, err := LoadStatusKey(f.act, true)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(StatusKeyPath(f.act)); fi.Mode().Perm() != 0o600 {
		t.Fatal("the status key is not 0600")
	}
	again, _ := LoadStatusKey(f.act, false)
	if !again.Equal(k) {
		t.Fatal("the key changed when read again")
	}
	d := f.daemon()
	d.o.StatusKey = k
	if err := d.WriteStatus(); err != nil {
		t.Fatal(err)
	}
	st, ok := ReadStatus(StatusPath(f.actions))
	if !ok || st.Sig == "" {
		t.Fatal("the status is not signed")
	}
	pub := k.Public().(ed25519.PublicKey)
	if err := VerifyStatus(st, pub); err != nil {
		t.Fatal(err)
	}
	forged := st
	forged.Chain.OK, forged.TS = true, st.TS+60
	if VerifyStatus(forged, pub) == nil {
		t.Fatal("a changed status verified")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyStatus(st, other) == nil {
		t.Fatal("another key verified the status")
	}
	st.Sig = ""
	if VerifyStatus(st, pub) == nil {
		t.Fatal("an unsigned status verified")
	}
}
