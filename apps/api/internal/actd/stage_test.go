package actd

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
)

// ADR-0022 PR 5 review: the model's develop classification is not enough for a write step; the request must carry
// the board's labels and they, and the labels in board.db, must say stage without an other env: label.
func TestWriteStepNeedsStageLabels(t *testing.T) {
	w := &fakeWrite{}
	f := newFx(t, w)
	d := f.daemon()
	cases := []struct {
		name string
		mut  func(key string, r *Request)
	}{
		{"no labels in the request", func(_ string, r *Request) { r.EnvLabels = nil }},
		{"request says production", func(_ string, r *Request) { r.EnvLabels = []string{"env:stage", "env:production"} }},
		{"board.db has no stage label", func(k string, _ *Request) {
			f.exec(f.board, `UPDATE tasks SET labels='["ops"]' WHERE jira_key=?`, k)
		}},
		{"board.db says production", func(k string, _ *Request) {
			f.exec(f.board, `UPDATE tasks SET labels='["env:production"]' WHERE jira_key=?`, k)
		}},
		{"no task row", func(k string, _ *Request) { f.exec(f.board, `DELETE FROM tasks WHERE jira_key=?`, k) }},
	}
	for i, c := range cases {
		key := "LB-" + string(rune('a'+i))
		req := f.task(key, "develop", []map[string]any{writeStep()})
		c.mut(key, &req)
		r := f.submit(d, req)
		wantRefused(t, r, 2, capability.CodeEnvironment)
		if !strings.Contains(r.Refusal.Reason, "label") {
			t.Fatalf("%s: %s", c.name, r.Refusal.Reason)
		}
	}
	if w.applies != 0 {
		t.Fatal("a write step ran without the board's stage label")
	}
	// a read-tier step needs no label (O8)
	req := f.task("LB-read", "develop", []map[string]any{check("/ok", 200)})
	req.EnvLabels = nil
	f.exec(f.board, `UPDATE tasks SET labels='[]' WHERE jira_key='LB-read'`)
	if r := f.submit(d, req); r.State != StateDone {
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
