package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	k, err := ParseKey(NewKeyText())
	if err != nil {
		t.Fatal(err)
	}
	s, err := k.Seal("sources/gitlab/main", "fake-token-value")
	if err != nil || !Sealed(s) || strings.Contains(s, "fake-token-value") {
		t.Fatalf("seal: %q %v", s, err)
	}
	if p, err := k.Open("sources/gitlab/main", s); err != nil || p != "fake-token-value" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := k.Open("sources/gitlab/other", s); err == nil {
		t.Fatal("opened under another name")
	}
	k2, _ := ParseKey(NewKeyText())
	if _, err := k2.Open("sources/gitlab/main", s); err == nil {
		t.Fatal("opened with another key")
	}
	if _, err := Key(nil).Open("x", s); err == nil {
		t.Fatal("opened without a key")
	}
	if p, _ := Key(nil).Open("x", "plain"); p != "plain" {
		t.Fatal("plaintext passes through")
	}
	if p, _ := Key(nil).Seal("x", "plain"); p != "plain" {
		t.Fatal("no key: no encryption")
	}
	if _, err := ParseKey("c2hvcnQ="); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLoad(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "keys", "master.key")
	none := func() []string { return nil }
	k, from, err := Load(Source{File: f, Secrets: none, Generate: true})
	if err != nil || len(k) != 32 || !strings.Contains(from, "created") {
		t.Fatalf("first start: %v %q %v", len(k), from, err)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatal("key file mode")
	}
	k2, _, err := Load(Source{File: f, Secrets: none, Generate: true})
	if err != nil || string(k2) != string(k) {
		t.Fatal("second start must read the same key")
	}
	env := NewKeyText()
	if k3, from, _ := Load(Source{Env: env, File: f}); from != "ARGUS_MASTER_KEY" || string(k3) == string(k) {
		t.Fatal("the environment wins")
	}
	os.Remove(f)
	_, _, err = Load(Source{File: f, Secrets: func() []string { return []string{"sources/gitlab/main.cred"} }, Generate: true})
	if err == nil || !strings.Contains(err.Error(), "no master key") {
		t.Fatalf("secrets without a key: %v", err)
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("created a key that cannot read the secrets")
	}
	if _, _, err := Load(Source{Env: "not base64!"}); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestAudit(t *testing.T) {
	dir := t.TempDir()
	k, _ := ParseKey(NewKeyText())
	other, _ := ParseKey(NewKeyText())
	os.MkdirAll(filepath.Join(dir, "sources", "gitlab"), 0o700)
	good, _ := k.Seal(CredName("gitlab", "main"), "tok-1")
	bad, _ := other.Seal(CredName("gitlab", "old"), "tok-2")
	os.WriteFile(filepath.Join(dir, "sources", "gitlab", "main.cred"), []byte(good), 0o600)
	os.WriteFile(filepath.Join(dir, "sources", "gitlab", "old.cred"), []byte(bad), 0o600)
	os.WriteFile(filepath.Join(dir, "matrix-token"), []byte("plain-token"), 0o600)
	f := Files{SourcesDir: filepath.Join(dir, "sources"), MatrixFile: filepath.Join(dir, "matrix-token")}
	opened, plain, names := f.Audit(k)
	if opened != 1 || plain != 1 || len(names) != 1 || names[0] != "sources/gitlab/old" {
		t.Errorf("audit: %d %d %v", opened, plain, names)
	}
}
