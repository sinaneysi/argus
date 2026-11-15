package actd

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/secret"
)

// Credential is what the status and `argus actd credential list` show of a stored credential: never the value.
type Credential struct {
	Name    string `json:"name"`
	Last4   string `json:"last4,omitempty"`
	Updated int64  `json:"updated"`
	Error   string `json:"error,omitempty"`
}

var credNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}/[a-z0-9][a-z0-9_.-]{0,63}$`)

// MaxCredentialBytes is the longest credential value accepted.
const MaxCredentialBytes = 16 << 10

// KeySource is where the executor's AES-256 key comes from (D2, amended): only keys/act.key in the executor volume,
// created at the first start while no credential is stored. No environment variable: the env file is shared with
// the other services.
func KeySource(actDir string, generate bool) secret.Source {
	return secret.Source{File: filepath.Join(actDir, "keys", "act.key"), Generate: generate,
		Secrets: func() []string { m, _ := filepath.Glob(filepath.Join(actDir, "secrets", "*", "*.enc")); return m }}
}

func credPath(actDir, name string) string {
	fam, n, _ := strings.Cut(name, "/")
	return filepath.Join(actDir, "secrets", fam, n+".enc")
}

func sealedName(name string) string { return "act/" + name }

// SetCredential seals value under name ("<family>/<name>") into secrets/<family>/<name>.enc (0600).
func SetCredential(actDir string, key secret.Key, name, value string) error {
	if !credNameRe.MatchString(name) || strings.Contains(name, "..") {
		return errors.New("the name must be <family>/<name>: lower-case letters, digits, '-', '_' and '.'")
	}
	value = strings.TrimRight(value, "\r\n")
	if value == "" || len(value) > MaxCredentialBytes {
		return fmt.Errorf("the value is empty or longer than %d bytes", MaxCredentialBytes)
	}
	if len(key) != 32 {
		return errors.New("no executor key")
	}
	enc, err := key.Seal(sealedName(name), value)
	if err != nil {
		return err
	}
	p := credPath(actDir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return writeFile(p, []byte(enc+"\n"), 0o600)
}

// OpenCredential is the value of a stored credential (for a capability; never printed).
func OpenCredential(actDir string, key secret.Key, name string) (string, error) {
	if !credNameRe.MatchString(name) {
		return "", errors.New("bad credential name")
	}
	b, err := os.ReadFile(credPath(actDir, name))
	if err != nil {
		return "", err
	}
	if !secret.Sealed(string(b)) {
		return "", errors.New("the credential file is not sealed")
	}
	return key.Open(sealedName(name), string(b))
}

// ListCredentials lists the stored credentials: name, last four characters, time of the last change.
func ListCredentials(actDir string, key secret.Key) []Credential {
	m, _ := filepath.Glob(filepath.Join(actDir, "secrets", "*", "*.enc"))
	sort.Strings(m)
	out := []Credential{}
	for _, p := range m {
		name := filepath.Base(filepath.Dir(p)) + "/" + strings.TrimSuffix(filepath.Base(p), ".enc")
		c := Credential{Name: name}
		if fi, err := os.Stat(p); err == nil {
			c.Updated = fi.ModTime().Unix()
		}
		if v, err := OpenCredential(actDir, key, name); err != nil {
			c.Error = "does not open with the executor key"
		} else if len(v) >= 12 {
			c.Last4 = v[len(v)-4:]
		}
		out = append(out, c)
	}
	return out
}

// ApprovalPubPath is the api's approval public key in the executor volume (ADR-0022 amendment 1, item A).
func ApprovalPubPath(actDir string) string { return filepath.Join(actDir, "keys", "approval.pub") }

// LoadApprovalPub reads keys/approval.pub. Missing: (nil, nil), and every step is refused (rule 3).
func LoadApprovalPub(actDir string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(ApprovalPubPath(actDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return capability.ParseApprovalPublic(b)
}

// SetApprovalPub stores the api's approval public key (PEM, from `argus approval-key public` in the api service).
func SetApprovalPub(actDir string, pemText []byte) (ed25519.PublicKey, error) {
	if len(pemText) > 4096 {
		return nil, errors.New("the public key is longer than 4096 bytes")
	}
	p, err := capability.ParseApprovalPublic(pemText)
	if err != nil {
		return nil, err
	}
	out, err := capability.PublicKeyPEM(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(actDir, "keys"), 0o700); err != nil {
		return nil, err
	}
	return p, writeFile(ApprovalPubPath(actDir), out, 0o600)
}
