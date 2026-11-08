package secret

import (
	"os"
	"path/filepath"
	"strings"
)

// Files are the secret files of the one-volume layout, by the name each value is sealed under.
type Files struct {
	SourcesDir string // ARGUS_SOURCES_DIR: <type>/<name>.cred
	ClaudeEnv  string // ARGUS_CLAUDE_ENV_FILE: CLAUDE_CODE_OAUTH_TOKEN=<sealed>, and its .bak-*-claude copies
	MatrixFile string // the Matrix token file
	EtcDir     string // ARGUS_ETC_DIR: the GitLab issues token (gitlab-issues.token)
}

// FilesOf are the secret files of a configuration (get is the configured value of a key).
func FilesOf(get func(string) string) Files {
	m := get("ARGUS_MATRIX_TOKEN_FILE")
	if m == "" {
		m = filepath.Join(get("ARGUS_SECRETS_DIR"), "matrix-token")
	}
	return Files{SourcesDir: get("ARGUS_SOURCES_DIR"), ClaudeEnv: get("ARGUS_CLAUDE_ENV_FILE"), MatrixFile: m, EtcDir: get("ARGUS_ETC_DIR")}
}

// EtcFiles are the token files in ARGUS_ETC_DIR: path -> sealed name ("etc/<file>").
func (f Files) EtcFiles() map[string]string {
	out := map[string]string{}
	if f.EtcDir == "" {
		return out
	}
	for _, n := range []string{"gitlab-issues.token"} {
		out[filepath.Join(f.EtcDir, n)] = "etc/" + n
	}
	return out
}

// CredName is the name a data-source credential is sealed under.
func CredName(typ, name string) string { return "sources/" + typ + "/" + name }

// ClaudeName and MatrixName are the names of the Claude token and the Matrix token.
const (
	ClaudeName = "claude/CLAUDE_CODE_OAUTH_TOKEN"
	MatrixName = "matrix/token"
)

// Creds are the data-source credential files: path -> sealed name.
func (f Files) Creds() map[string]string {
	out := map[string]string{}
	if f.SourcesDir == "" {
		return out
	}
	m, _ := filepath.Glob(filepath.Join(f.SourcesDir, "*", "*.cred"))
	for _, p := range m {
		out[p] = CredName(filepath.Base(filepath.Dir(p)), strings.TrimSuffix(filepath.Base(p), ".cred"))
	}
	return out
}

// ClaudeFiles are the Claude env file and its backups.
func (f Files) ClaudeFiles() []string {
	if f.ClaudeEnv == "" {
		return nil
	}
	out := []string{f.ClaudeEnv}
	m, _ := filepath.Glob(f.ClaudeEnv + ".bak-*-claude")
	return append(out, m...)
}

func nonEmpty(p string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.TrimSpace(string(b)) != ""
}

// Present lists the secrets the volume holds (paths, never values).
func (f Files) Present() []string {
	var out []string
	for p := range f.Creds() {
		if nonEmpty(p) {
			out = append(out, p)
		}
	}
	for _, p := range f.ClaudeFiles() {
		if EnvValue(p, "CLAUDE_CODE_OAUTH_TOKEN") != "" {
			out = append(out, p)
		}
	}
	if nonEmpty(f.MatrixFile) {
		out = append(out, f.MatrixFile)
	}
	for p := range f.EtcFiles() {
		if nonEmpty(p) {
			out = append(out, p)
		}
	}
	return out
}

// ForVolume is the master key of a configuration: nil outside ARGUS_LAYOUT=volume. generate creates the key file at
// the first start (argus-api and the worker); the other commands only read it.
func ForVolume(get func(string) string, generate bool) (Key, string, error) {
	if get("ARGUS_LAYOUT") != "volume" {
		return nil, "", nil
	}
	f := FilesOf(get)
	return Load(Source{Env: get("ARGUS_MASTER_KEY"), File: get("ARGUS_MASTER_KEY_FILE"), Secrets: f.Present, Generate: generate})
}

// EnvValue is the stored (maybe sealed) value of key in a KEY=value file; "" when missing or empty.
func EnvValue(path, key string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(strings.TrimPrefix(k, "export ")) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
