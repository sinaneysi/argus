package actd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

// KeySource is where the executor's AES-256 key comes from (D2): ARGUS_ACT_KEY wins, else keys/act.key in the
// executor volume, created at the first start while no credential is stored.
func KeySource(actDir, env string, generate bool) secret.Source {
	return secret.Source{Env: env, File: filepath.Join(actDir, "keys", "act.key"), Generate: generate,
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
