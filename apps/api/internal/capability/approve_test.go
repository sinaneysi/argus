package capability

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestApprovalSignature(t *testing.T) {
	privPEM, pubPEM, err := NewApprovalKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParseApprovalPrivate(privPEM)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParseApprovalPublic(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	if KeyFingerprint(pub) != KeyFingerprint(priv.Public().(ed25519.PublicKey)) {
		t.Fatal("the public key is not the private key's")
	}
	c := ApprovalClaim{Kind: SignStart, TaskKey: "OPS-1", PlanVersion: 3, PlanSHA256: sha, Approver: "ada", At: now.Unix(), SealedSHA256: SealedSHA256(nil)}
	sig, err := SignApproval(priv, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyApproval(pub, c, sig); err != nil {
		t.Fatal(err)
	}
	// every field is covered
	for name, mut := range map[string]func(*ApprovalClaim){
		"kind":    func(x *ApprovalClaim) { x.Kind, x.Step = SignContinue, 2 },
		"task":    func(x *ApprovalClaim) { x.TaskKey = "OPS-2" },
		"version": func(x *ApprovalClaim) { x.PlanVersion = 4 },
		"hash":    func(x *ApprovalClaim) { x.PlanSHA256 = strings.Repeat("e", 64) },
		"by":      func(x *ApprovalClaim) { x.Approver = "eve" },
		"at":      func(x *ApprovalClaim) { x.At++ },
		"sealed":  func(x *ApprovalClaim) { x.SealedSHA256 = SealedSHA256(map[string]string{"A": "b"}) },
	} {
		x := c
		mut(&x)
		if VerifyApproval(pub, x, sig) == nil {
			t.Errorf("%s: a changed claim verified", name)
		}
	}
	// a key the worker could make itself does not verify
	_, other, _ := NewApprovalKey()
	op, _ := ParseApprovalPublic(other)
	if VerifyApproval(op, c, sig) == nil {
		t.Fatal("another key verified")
	}
	if VerifyApproval(nil, c, sig) == nil || VerifyApproval(pub, c, "") == nil || VerifyApproval(pub, c, "!!") == nil {
		t.Fatal("no key, no signature or a bad one verified")
	}
	// fields with a newline or out of form cannot be signed
	bad := c
	bad.Approver = "ada\nstart"
	if _, err := SignApproval(priv, bad); err == nil {
		t.Fatal("a newline in a field was signed")
	}
	bad = c
	bad.Step = 1
	if _, err := SignApproval(priv, bad); err == nil {
		t.Fatal("a start approval with a step was signed")
	}
	if _, err := ParseApprovalPublic(append(pubPEM, pubPEM...)); err == nil {
		t.Fatal("two keys accepted")
	}
	if _, err := ParseApprovalPrivate(pubPEM); err == nil {
		t.Fatal("a public key accepted as the private key")
	}
}

func TestSealedSHA256(t *testing.T) {
	if SealedSHA256(nil) != SealedSHA256(map[string]string{}) {
		t.Fatal("nil and empty differ")
	}
	a := SealedSHA256(map[string]string{"A": "1", "B": "2"})
	if a != SealedSHA256(map[string]string{"B": "2", "A": "1"}) || a == SealedSHA256(map[string]string{"A": "1", "B": "3"}) {
		t.Fatal("not canonical")
	}
}
