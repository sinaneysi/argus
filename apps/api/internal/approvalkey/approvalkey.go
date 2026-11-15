// Package approvalkey holds the api's approval signing key (ADR-0022 amendment 1, item A): an Ed25519 key in a folder
// that only the api mounts (ARGUS_APPROVAL_KEY_DIR, compose volume argus-approve). The worker shares the data volume
// and the uid with the api, so the key must not be there: whoever can read it can forge approvals. actd holds only
// the public half (argus actd approval-key set).
package approvalkey

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/sinaneysi/argus/internal/capability"
)

// The files in the key folder.
const (
	KeyFile = "approval.key" // PKCS#8 PEM, 0600
	PubFile = "approval.pub" // PKIX PEM, 0644: copy it to actd
	// ActdFile is actd's sealing public key as pinned by the operator (argus approval-key trust-actd): the api seals
	// secret inputs only to this key, never to the copy in the shared volume (the worker could replace that one)
	ActdFile = "actd-seal.pub"
)

// TrustActd pins actd's sealing public key (PEM) in the key folder.
func TrustActd(dir string, pemText []byte) (*ecdh.PublicKey, error) {
	if err := CheckDir(dir); err != nil {
		return nil, err
	}
	if len(pemText) > 4096 {
		return nil, errors.New("the public key is longer than 4096 bytes")
	}
	p, err := capability.ParseSealPublic(pemText)
	if err != nil {
		return nil, err
	}
	out, err := capability.SealPublicPEM(p)
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, "."+ActdFile+".tmp")
	os.Remove(tmp)
	if err := writeExcl(tmp, out, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, ActdFile)); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return p, nil
}

// LoadActd is the pinned sealing key; (nil, nil) when none is pinned.
func LoadActd(dir string) (*ecdh.PublicKey, error) {
	if err := CheckDir(dir); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ActdFile), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return nil, errors.New("the pinned sealing key is not a small regular file")
	}
	b := make([]byte, fi.Size())
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	return capability.ParseSealPublic(b)
}

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
		// the public half: a new temporary file (O_EXCL, no link followed), then a rename over the name
		tmp := filepath.Join(dir, "."+PubFile+".tmp")
		os.Remove(tmp)
		if err := writeExcl(tmp, pub, 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, filepath.Join(dir, PubFile)); err != nil {
			os.Remove(tmp)
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
	_, err = io.ReadFull(f, buf)
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

// startRecord is what the api remembers of a start it signed and sent (in its own folder, which the worker cannot
// write): a continue or a rollback of that action is signed over the same sealed inputs' hash, never over
// board.db's copy. One record per action, so a later refused start cannot replace the running action's record.
type startRecord struct {
	ActionID     string `json:"action_id"`
	PlanVersion  int    `json:"plan_version"`
	PlanSHA256   string `json:"plan_sha256"`
	SealedSHA256 string `json:"sealed_sha256"`
}

var (
	startKeyRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	startActionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}\.v[0-9]{1,9}\.[0-9a-f]{12}$`)
)

func startPath(dir, key, actionID string) string {
	return filepath.Join(dir, "starts", key, actionID+".json")
}

// RecordStart keeps the sealed inputs' hash of a signed start that was sent (a temporary file, then a rename).
func RecordStart(dir, key, actionID string, version int, planSHA, sealedSHA string) error {
	if err := CheckDir(dir); err != nil {
		return err
	}
	if !startKeyRe.MatchString(key) || !startActionRe.MatchString(actionID) {
		return errors.New("bad task key or action id")
	}
	d := filepath.Dir(startPath(dir, key, actionID))
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(startRecord{ActionID: actionID, PlanVersion: version, PlanSHA256: planSHA, SealedSHA256: sealedSHA})
	f, err := os.CreateTemp(d, ".start-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, startPath(dir, key, actionID))
}

// StartSealed is the sealed inputs' hash of the start the api signed and sent for this action, plan version and
// hash.
func StartSealed(dir, key, actionID string, version int, planSHA string) (string, bool) {
	if CheckDir(dir) != nil || !startKeyRe.MatchString(key) || !startActionRe.MatchString(actionID) {
		return "", false
	}
	f, err := os.OpenFile(startPath(dir, key, actionID), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	var r startRecord
	if json.NewDecoder(io.LimitReader(f, 4096)).Decode(&r) != nil || r.ActionID != actionID || r.PlanVersion != version ||
		r.PlanSHA256 != planSHA || r.SealedSHA256 == "" {
		return "", false
	}
	return r.SealedSHA256, true
}
