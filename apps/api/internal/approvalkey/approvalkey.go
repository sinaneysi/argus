// Package approvalkey holds the api's approval signing key (ADR-0022 amendment 1, item A): an Ed25519 key in a folder
// that only the api mounts (ARGUS_APPROVAL_KEY_DIR, compose volume argus-approve). The worker shares the data volume
// and the uid with the api, so the key must not be there: whoever can read it can forge approvals. actd holds only
// the public half (argus actd approval-key set).
package approvalkey

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sinaneysi/argus/internal/capability"
)

// The files in the key folder.
const (
	KeyFile = "approval.key" // PKCS#8 PEM, 0600
	PubFile = "approval.pub" // PKIX PEM, 0644: copy it to actd
)

// ErrNoDir: the key folder does not exist; the api then signs nothing and actd refuses every step.
var ErrNoDir = errors.New("no approval key folder")

// CheckDir checks the folder: an absolute path, a folder owned by this process's user, closed to others.
func CheckDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("ARGUS_APPROVAL_KEY_DIR must be an absolute path")
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoDir
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("ARGUS_APPROVAL_KEY_DIR %s is not a folder", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("ARGUS_APPROVAL_KEY_DIR %s is owned by uid %d, not by this process (uid %d)", dir, st.Uid, os.Geteuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ARGUS_APPROVAL_KEY_DIR %s has mode %04o, must be 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// Load reads the key; with create, a missing key is made (and its public half written next to it).
func Load(dir string, create bool) (ed25519.PrivateKey, error) {
	if err := CheckDir(dir); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, KeyFile)
	b, err := readNoFollow(p)
	if errors.Is(err, os.ErrNotExist) && create {
		priv, pub, gerr := capability.NewApprovalKey()
		if gerr != nil {
			return nil, gerr
		}
		if err := writeExcl(p, priv, 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, PubFile), pub, 0o644); err != nil {
			return nil, err
		}
		b, err = readNoFollow(p)
	}
	if err != nil {
		return nil, err
	}
	k, err := capability.ParseApprovalPrivate(b)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// PublicPEM is the public half of the key, as PEM.
func PublicPEM(k ed25519.PrivateKey) ([]byte, error) {
	return capability.PublicKeyPEM(k.Public().(ed25519.PublicKey))
}

func readNoFollow(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return nil, errors.New("the approval key is not a small regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the approval key %s has mode %04o, must be 0600", p, fi.Mode().Perm())
	}
	buf := make([]byte, fi.Size())
	_, err = f.Read(buf)
	return buf, err
}

func writeExcl(p string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(p)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
