package actd

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sinaneysi/argus/internal/capability"
)

// StatusKeyPath is actd's status signing key (Ed25519, PKCS#8 PEM, 0600) in its own volume.
func StatusKeyPath(actDir string) string { return filepath.Join(actDir, "keys", "status.key") }

// LoadStatusKey reads the status signing key; with create, a missing key is made.
func LoadStatusKey(actDir string, create bool) (ed25519.PrivateKey, error) {
	p := StatusKeyPath(actDir)
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) && create {
		priv, _, gerr := capability.NewApprovalKey()
		if gerr != nil {
			return nil, gerr
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := writeFile(p, priv, 0o600); err != nil {
			return nil, err
		}
		return capability.ParseApprovalPrivate(priv)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return nil, errors.New("the status key is not a small regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the status key %s has mode %04o, must be 0600", p, fi.Mode().Perm())
	}
	b := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	return capability.ParseApprovalPrivate(b)
}

// statusBody is the signed form of a status: its JSON without the signature.
func statusBody(s Status) []byte {
	s.Sig = ""
	b, _ := json.Marshal(s)
	return b
}

// VerifyStatus checks the status' signature with the pinned public key (the api's trust in the heartbeat).
func VerifyStatus(s Status, pub ed25519.PublicKey) error {
	return capability.VerifyStatus(pub, statusBody(s), s.Sig)
}
