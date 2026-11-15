package approvalkey

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
)

func TestLoadCreatesOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "k")
	if _, err := Load(dir, true); !errors.Is(err, ErrNoDir) {
		t.Fatal("a missing folder is not ErrNoDir:", err)
	}
	os.Mkdir(dir, 0o755)
	if _, err := Load(dir, true); err == nil {
		t.Fatal("a folder open to others was accepted")
	}
	os.Chmod(dir, 0o700)
	k, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := Load(dir, false)
	if err != nil || !k.Equal(k2) {
		t.Fatal("the key changed on the second load", err)
	}
	pub, _ := os.ReadFile(filepath.Join(dir, PubFile))
	p, err := capability.ParseApprovalPublic(pub)
	if err != nil || !p.Equal(k.Public().(ed25519.PublicKey)) {
		t.Fatal("approval.pub is not the key's public half", err)
	}
	fi, _ := os.Stat(filepath.Join(dir, KeyFile))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", fi.Mode().Perm())
	}
	os.Chmod(filepath.Join(dir, KeyFile), 0o644)
	if _, err := Load(dir, false); err == nil {
		t.Fatal("a readable key was accepted")
	}
}
