package capability

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// RFC 5869 test case 1.
func TestHKDFVector(t *testing.T) {
	ikm, _ := hex.DecodeString("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
	salt, _ := hex.DecodeString("000102030405060708090a0b0c")
	info, _ := hex.DecodeString("f0f1f2f3f4f5f6f7f8f9")
	got := hex.EncodeToString(hkdfSHA256(ikm, salt, info, 42))
	if got != "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865" {
		t.Fatal(got)
	}
}

func TestSealedInputs(t *testing.T) {
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	id := "OPS-9.v1.abcdefabcdef"
	value := "v@lue-" + "with-symbols"
	s, err := SealInput(k.PublicKey(), id, 2, "NEW_TOKEN", value)
	if err != nil || !ValidSealed(s) || strings.Contains(s, value) {
		t.Fatal(s, err)
	}
	if v, err := OpenInput(k, id, 2, "NEW_TOKEN", s); err != nil || v != value {
		t.Fatal(v, err)
	}
	// bound to the action, the step and the name, and to the key
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	for _, c := range []struct {
		k    *ecdh.PrivateKey
		id   string
		step int
		name string
	}{{k, "OPS-9.v2.abcdefabcdef", 2, "NEW_TOKEN"}, {k, id, 3, "NEW_TOKEN"}, {k, id, 2, "OTHER"}, {other, id, 2, "NEW_TOKEN"}} {
		if _, err := OpenInput(c.k, c.id, c.step, c.name, s); err == nil {
			t.Fatalf("opened for %+v", c)
		}
	}
	if _, err := SealInput(k.PublicKey(), id, 2, "bad-name", value); err == nil {
		t.Fatal("a bad name was sealed")
	}
	pemText, _ := SealPublicPEM(k.PublicKey())
	if p, err := ParseSealPublic(pemText); err != nil || !p.Equal(k.PublicKey()) {
		t.Fatal(err)
	}
	priv, _ := SealPrivatePEM(k)
	if p, err := ParseSealPrivate(priv); err != nil || !p.Equal(k) {
		t.Fatal(err)
	}
	if n, name, ok := ParseInputKey(InputKey(12, "NEW_TOKEN")); !ok || n != 12 || name != "NEW_TOKEN" {
		t.Fatal(n, name, ok)
	}
	for _, bad := range []string{"0/X", "1/x", "X", "1/", "1/A/B"} {
		if _, _, ok := ParseInputKey(bad); ok {
			t.Fatal(bad)
		}
	}
}

func TestPlanInputs(t *testing.T) {
	plan := []any{
		map[string]any{"n": 1, "mode": "WRITE", "capability": "gitlab.ci-variable.set", "args": map[string]any{"value_ref": "input:B", "key": "K"}},
		map[string]any{"n": 2, "mode": "operator", "args": map[string]any{"x": "input:C"}},
		map[string]any{"n": 3, "mode": "read-only", "capability": "gitlab.ci-variable.get", "args": map[string]any{"matches": "input:B", "other": "input:A"}},
		map[string]any{"n": 4, "mode": "read-only", "capability": "http.check", "args": map[string]any{"url": "https://example.com/input:Z"}},
	}
	if got := PlanInputs(plan); !reflect.DeepEqual(got, map[int][]string{1: {"B"}, 3: {"A", "B"}}) {
		t.Fatal(got)
	}
}
