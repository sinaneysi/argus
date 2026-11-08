package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/secret"
)

// fixture tokens: obviously fake, never real
const (
	fxGitLabRO = "fake-gitlab-ro-000000000001"
	fxTracker  = "fake-tracker-00000000000002"
	fxJira     = "fixture-jira-pat-0000000003"
	fxOutline  = "fake-outline-0000000000004"
	fxMatrix   = "fake-matrix-00000000000005"
	fxGrafana  = "fake-grafana-0000000000006"
	fxClaude   = "fake-claude-00000000000007"
	fxLabel    = "earlier-totp"
)

var fxSecrets = []string{fxGitLabRO, fxTracker, fxJira, fxOutline, fxMatrix, fxGrafana, fxClaude}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, text string, mode os.FileMode) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(text), mode))
}

// legacySeal is a 2FA secret as the earlier installation wrote it: key id and AAD under its own label.
func legacySeal(t *testing.T, key []byte, user, fid, plain string) string {
	b, _ := aes.NewCipher(key)
	a, _ := cipher.NewGCM(b)
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	ct := a.Seal(nil, nonce, []byte(plain), []byte(fxLabel+"|"+user+"|"+fid))
	id := sha256.Sum256(append([]byte(fxLabel+"-key-id|"), key...))
	return "v1." + hex.EncodeToString(id[:4]) + "." + base64.RawURLEncoding.EncodeToString(append(nonce, ct...))
}

// predecessor builds a synthetic earlier installation: state dir, etc dir, env file.
func predecessor(t *testing.T) (src, etc, envFile string, totpKey []byte) {
	root := t.TempDir()
	src, etc, envFile = filepath.Join(root, "state"), filepath.Join(root, "etc"), filepath.Join(root, "etc", "env")
	ctx := context.Background()
	must(t, os.MkdirAll(src, 0o755))
	ops, err := sql.Open("sqlite", filepath.Join(src, "ops.db"))
	must(t, err)
	_, err = ops.Exec(`CREATE TABLE runs (id INTEGER PRIMARY KEY, kind TEXT, started_at INTEGER); INSERT INTO runs (kind, started_at) VALUES ('alerts', 1), ('certs', 2)`)
	must(t, err)
	ops.Close()
	bdb, err := board.Connect(ctx, filepath.Join(src, "board", "board.db"))
	must(t, err)
	_, err = bdb.Exec(`INSERT INTO tasks (title, status, tracker) VALUES ('Rotate the logs', 'todo', 'gitlab')`)
	must(t, err)
	bdb.Close()
	totpKey = make([]byte, 32)
	rand.Read(totpKey)
	write(t, filepath.Join(etc, "totp.key"), hex.EncodeToString(totpKey), 0o600)
	a, err := sql.Open("sqlite", filepath.Join(src, "auth", "auth.db"))
	if err != nil {
		os.MkdirAll(filepath.Join(src, "auth"), 0o755)
	}
	os.MkdirAll(filepath.Join(src, "auth"), 0o755)
	a, err = sql.Open("sqlite", filepath.Join(src, "auth", "auth.db"))
	must(t, err)
	_, err = a.Exec(`CREATE TABLE users (username TEXT PRIMARY KEY, hash TEXT, role TEXT, disabled INTEGER, must_change INTEGER, created_at INTEGER);
CREATE TABLE mfa (username TEXT PRIMARY KEY, secret_enc TEXT, factor_id TEXT, pending_enc TEXT, pending_fid TEXT);
INSERT INTO users VALUES ('alex.admin', 'x', 'admin', 0, 0, 0);`)
	must(t, err)
	_, err = a.Exec(`INSERT INTO mfa (username, secret_enc, factor_id) VALUES ('alex.admin', ?, 'f1')`, legacySeal(t, totpKey, "alex.admin", "f1", "JBSWY3DPEHPK3PXP"))
	must(t, err)
	a.Close()
	write(t, filepath.Join(src, "alerts.json"), `{"alerts": []}`, 0o644)
	write(t, filepath.Join(src, "settings-in", "settings.json"), `{"tasks": {}}`, 0o600)
	write(t, filepath.Join(src, "settings-in", "board-requests", "20261108000001-00000001.json"), `{"kind": "sync"}`, 0o600)
	write(t, filepath.Join(src, "settings-out", "board-status.json"), `{"requests": {}}`, 0o644)
	write(t, filepath.Join(etc, "gitlab-issues.json"), `{"url": "https://gitlab.example.com", "mode": "project", "project_id": 7}`, 0o600)
	write(t, filepath.Join(etc, "gitlab-issues.token"), fxTracker+"\n", 0o600)
	write(t, filepath.Join(etc, "jira.env"), "JIRA_URL=https://jira.example.com\nJIRA_PROJECT=OPS\nOTHER_SECRET=nope\n", 0o644)
	write(t, filepath.Join(etc, "jira.pat"), fxJira+"\n", 0o600)
	write(t, filepath.Join(etc, "outline.json"), `{"url": "https://wiki.example.com"}`, 0o600)
	write(t, filepath.Join(etc, "outline.token"), fxOutline+"\n", 0o600)
	write(t, envFile, strings.Join([]string{"GITLAB_URL=https://gitlab.example.com", "GITLAB_RO_TOKEN=" + fxGitLabRO,
		"MATRIX_HOMESERVER=https://matrix.example.com", "MATRIX_ACCESS_TOKEN=" + fxMatrix, "PROM_URL=https://prometheus.example.com",
		"GRAFANA_SITE1_URL=https://grafana.example.com", "GRAFANA_SITE1_TOKEN=" + fxGrafana, "CLAUDE_CODE_OAUTH_TOKEN=" + fxClaude}, "\n")+"\n", 0o600)
	write(t, filepath.Join(root, "reports", "rca", "r1.md"), "# report\n", 0o644)
	return
}

func treeSum(t *testing.T, dir string) string {
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			fi, _ := d.Info()
			h.Write([]byte(p + fi.Mode().String()))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func noSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range fxSecrets {
		if strings.Contains(text, s) {
			t.Fatalf("%s holds a secret value", where)
		}
	}
}

func TestImport(t *testing.T) {
	src, etc, envFile, _ := predecessor(t)
	root := filepath.Dir(src)
	before := treeSum(t, root)
	vol := t.TempDir()
	c, cfg := volumeConfig(t, vol, "TOTP_KEY_FILE="+vol+"/keys/totp.key\n")
	args := func(extra ...string) []string {
		return append(append([]string{"-file", cfg, "-etc", etc, "-env", envFile, "-reports", filepath.Join(root, "reports", "rca"),
			"-totp-legacy-label", fxLabel}, extra...), src)
	}
	// dry run: names only, nothing written
	var out, errb bytes.Buffer
	if rc := runImport(args("-dry-run"), &out, &errb); rc != 0 {
		t.Fatalf("dry run rc %d: %s", rc, errb.String())
	}
	noSecrets(t, "dry-run output", out.String()+errb.String())
	for _, want := range []string{"data source gitlab/main", "data source jira/main", "data source matrix/main", "data source grafana/site1",
		"data source outline/wiki", "data source alertmanager/main", "Claude token", "pending request files", "nothing was changed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry run lacks %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(c.AuthDB); err == nil {
		t.Fatal("dry run wrote the auth database")
	}
	// the import
	out.Reset()
	errb.Reset()
	if rc := runImport(args(), &out, &errb); rc != 0 {
		t.Fatalf("rc %d: %s\n%s", rc, errb.String(), out.String())
	}
	noSecrets(t, "output", out.String()+errb.String())
	if treeSum(t, root) != before {
		t.Fatal("the source folder was changed")
	}
	if n := countRows(c.DB, "runs"); n != 2 {
		t.Fatal("runs", n)
	}
	if n := countRows(c.BoardDB, "tasks"); n != 1 {
		t.Fatal("tasks", n)
	}
	// the 2FA secret now opens without the legacy label
	auth.DB, auth.KeyFile, auth.LegacyLabel = c.AuthDB, filepath.Join(vol, "keys", "totp.key"), ""
	auth.ResetKeys()
	auth.Reset()
	var box string
	must(t, auth.Con().QueryRow(`SELECT secret_enc FROM mfa WHERE username='alex.admin'`).Scan(&box))
	auth.Reset()
	if p, err := auth.Open("alex.admin", "f1", box); err != nil || string(p) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("2FA secret: %v", err)
	}
	for _, p := range []string{"alerts.json", "settings/in/settings.json", "settings/out/board-status.json", "reports/rca/r1.md"} {
		if _, err := os.Stat(filepath.Join(vol, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(c.SettingsIn, "board-requests", "20261108000001-00000001.json")); err == nil {
		t.Fatal("a pending request was imported")
	}
	// Data sources: entries, sealed credentials that open
	so := sourcesOptions(c)
	reg := so.Registry()
	for _, k := range []string{"gitlab/main", "jira/main", "outline/wiki", "matrix/main", "alertmanager/main", "grafana/site1"} {
		if reg[k] == nil {
			t.Fatal("missing source", k)
		}
	}
	if reg["jira/main"].Purpose != "tracker" || so.Credential(reg["jira/main"]) != fxJira || so.Credential(reg["matrix/main"]) != fxMatrix {
		t.Fatal("credentials do not open")
	}
	// every secret file in the volume is sealed, none holds a plaintext value
	filepath.WalkDir(vol, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasSuffix(p, ".db") {
			b, _ := os.ReadFile(p)
			noSecrets(t, p, string(b))
		}
		return nil
	})
	gt, _ := os.ReadFile(filepath.Join(c.EtcDir, "gitlab-issues.token"))
	if k, _, _ := secret.ForVolume(func(k string) string { return c.Values[k].Raw }, false); k == nil {
		t.Fatal("no master key")
	} else if v, err := k.Open(board.GitLabTokenName, string(gt)); err != nil || v != fxTracker {
		t.Fatal("tracker token")
	}
	if b, _ := os.ReadFile(filepath.Join(c.EtcDir, "jira.env")); strings.Contains(string(b), "OTHER_SECRET") || !strings.Contains(string(b), "JIRA_PROJECT=OPS") {
		t.Fatalf("jira.env: %s", b)
	}
	if v := secret.EnvValue(c.Values["ARGUS_CLAUDE_ENV_FILE"].Raw, "CLAUDE_CODE_OAUTH_TOKEN"); !secret.Sealed(v) {
		t.Fatal("claude token not sealed")
	}
	// a second run refuses a volume with data; -force imports again with the same result
	out.Reset()
	errb.Reset()
	if rc := runImport(args(), &out, &errb); rc != 1 || !strings.Contains(errb.String(), "already holds data") {
		t.Fatalf("second run: rc %d %s", rc, errb.String())
	}
	if rc := runImport(args("-force"), &out, &errb); rc != 0 {
		t.Fatalf("forced run: %s", errb.String())
	}
	if n := countRows(c.BoardDB, "tasks"); n != 1 || func() bool { o := sourcesOptions(c); return len(o.Registry()) != len(reg) }() {
		t.Fatal("not idempotent")
	}
}
