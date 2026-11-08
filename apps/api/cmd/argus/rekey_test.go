package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/secret"
	"github.com/sinaneysi/argus/internal/sources"
)

func volumeConfig(t *testing.T, vol string, extra string) (*config.Config, string) {
	t.Helper()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\n"+extra), 0o600)
	c, err := config.Load(config.Options{File: env, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return c, env
}

// The first start creates the master key; secrets without a key refuse the start; rekey seals plaintext and moves
// values from the old key to the new one; consumers read them back (ADR-0021 D4).
func TestMasterKeyAndRekey(t *testing.T) {
	vol := t.TempDir()
	c, _ := volumeConfig(t, vol, "")
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(vol, "keys", "master.key")
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v", err)
	}
	// a plaintext credential from before encryption, and the key gone: the start is refused
	cred := filepath.Join(vol, "etc", "sources", "matrix", "main.cred")
	os.MkdirAll(filepath.Dir(cred), 0o750)
	os.WriteFile(cred, []byte("fake-matrix-token-0001\n"), 0o600)
	os.Remove(keyFile)
	err := migrate.OnStart(context.Background(), c, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "no master key") || strings.Contains(err.Error(), "fake-matrix") {
		t.Fatalf("secrets without a key: %v", err)
	}
	k1 := secret.NewKeyText()
	c, env := volumeConfig(t, vol, "ARGUS_MASTER_KEY="+k1+"\n")
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := runSecrets([]string{"rekey", "-file", env}, &out, &errb); rc != 0 || !strings.Contains(out.String(), "1 sealed") {
		t.Fatalf("rekey: %d %s %s", rc, out.String(), errb.String())
	}
	b, _ := os.ReadFile(cred)
	if !secret.Sealed(string(b)) || strings.Contains(string(b), "fake-matrix") {
		t.Fatalf("not sealed: %s", b)
	}
	e := &sources.Entry{Type: "matrix", Name: "main"}
	if o := sourcesOptions(c); o.Credential(e) != "fake-matrix-token-0001" {
		t.Fatal("consumer cannot read the sealed credential")
	}
	// rotate: old key in ARGUS_MASTER_KEY_OLD, a new one in ARGUS_MASTER_KEY
	k2 := secret.NewKeyText()
	c, env = volumeConfig(t, vol, "ARGUS_MASTER_KEY="+k2+"\n")
	if o := sourcesOptions(c); o.Credential(e) != "" {
		t.Fatal("opened with the wrong key")
	}
	out.Reset()
	if rc := runSecrets([]string{"rekey", "-file", env}, &out, &errb); rc != 1 {
		t.Fatal("rekey without the old key must fail")
	}
	c, env = volumeConfig(t, vol, "ARGUS_MASTER_KEY="+k2+"\nARGUS_MASTER_KEY_OLD="+k1+"\n")
	out.Reset()
	if rc := runSecrets([]string{"rekey", "-file", env}, &out, &errb); rc != 0 || !strings.Contains(out.String(), "1 sealed") {
		t.Fatalf("rotate: %s %s", out.String(), errb.String())
	}
	if o := sourcesOptions(c); o.Credential(e) != "fake-matrix-token-0001" {
		t.Fatal("not readable with the new key")
	}
	out.Reset()
	runSecrets([]string{"rekey", "-file", env}, &out, &errb)
	if !strings.Contains(out.String(), "0 sealed, 1 already current") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String()+errb.String(), "fake-matrix") {
		t.Fatal("a value was printed")
	}
}

// a save through the worker stores the credential sealed
func TestSourcesSaveSealed(t *testing.T) {
	vol := t.TempDir()
	c, _ := volumeConfig(t, vol, "ARGUS_MASTER_KEY="+secret.NewKeyText()+"\n")
	o := sourcesOptions(c)
	if o.Key == nil {
		t.Fatal("no key in the volume layout")
	}
	o.Dir = filepath.Join(vol, "etc", "sources")
	e := &sources.Entry{Type: "jira", Name: "main"}
	p := filepath.Join(o.Dir, "jira", "main.cred")
	os.MkdirAll(filepath.Dir(p), 0o750)
	s, _ := o.Key.Seal(secret.CredName("jira", "main"), "fake-jira-token")
	os.WriteFile(p, []byte(s+"\n"), 0o600)
	if o.Credential(e) != "fake-jira-token" {
		t.Fatal("sealed credential not opened")
	}
}
