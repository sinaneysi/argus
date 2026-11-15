package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/actd"
	"github.com/sinaneysi/argus/internal/migrate"
)

func TestActdLegacyLayoutDoesNotStart(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=legacy\nARGUS_ACTIONS_MODE=on\n"), 0o600)
	var out, errb bytes.Buffer
	if rc := runActd([]string{"-once", "-file", env}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "does not start") {
		t.Fatalf("legacy: %d %s", rc, errb.String())
	}
}

func TestActdOnceCredentialAndVerify(t *testing.T) {
	vol := t.TempDir()
	act := filepath.Join(t.TempDir(), "act")
	os.Mkdir(act, 0o700)
	c, env := volumeConfig(t, vol, "ARGUS_ACTIONS_MODE=on\nARGUS_ACT_DIR="+act+"\nARGUS_ACT_HTTP_HOSTS=svc.example.test\n")
	if err := migrate.OnStart(context.Background(), c, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := runActd([]string{"-once", "-file", env}, &out, &errb); rc != 0 {
		t.Fatalf("once: %d %s", rc, errb.String())
	}
	st, ok := actd.ReadStatus(filepath.Join(vol, "actions", "actd-status.json"))
	if !ok || st.Mode != "on" || !st.Chain.OK || strings.Join(st.Capabilities, ",") != "http.check" {
		t.Fatalf("status %+v", st)
	}
	if fi, err := os.Stat(filepath.Join(act, "keys", "act.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("no executor key", err)
	}
	out.Reset()
	errb.Reset()
	if rc := runActdIO([]string{"credential", "-file", env, "set", "gitlab/group-token"}, strings.NewReader("fake-token-value-7788\n"), &out, &errb); rc != 0 {
		t.Fatalf("set: %d %s", rc, errb.String())
	}
	out.Reset()
	if rc := runActdIO([]string{"credential", "-file", env, "list"}, nil, &out, &errb); rc != 0 || !strings.Contains(out.String(), "gitlab/group-token") ||
		!strings.Contains(out.String(), "...7788") || strings.Contains(out.String(), "fake-token") {
		t.Fatalf("list: %d %q %s", rc, out.String(), errb.String())
	}
	b, _ := os.ReadFile(filepath.Join(act, "secrets", "gitlab", "group-token.enc"))
	if strings.Contains(string(b), "fake-token") {
		t.Fatal("the credential is stored in plain text")
	}
	out.Reset()
	if rc := runActd([]string{"verify-audit", "-file", env}, &out, &errb); rc != 0 || !strings.HasPrefix(out.String(), "OK 0 records") {
		t.Fatalf("verify: %d %s", rc, out.String())
	}
	os.WriteFile(actd.AuditPath(act), []byte(`{"ts":"x"}`+"\n"), 0o600)
	out.Reset()
	if rc := runActd([]string{"verify-audit", "-file", env}, &out, &errb); rc != 1 || !strings.HasPrefix(out.String(), "BROKEN") {
		t.Fatalf("verify broken: %d %s", rc, out.String())
	}
}

func TestActdBadModeDoesNotStart(t *testing.T) {
	vol := t.TempDir()
	env := filepath.Join(vol, "argus.env")
	os.WriteFile(env, []byte("COMPANY_NAME=Example\nARGUS_PUBLIC_URL=https://argus.example.com\nARGUS_LAYOUT=volume\nARGUS_STATE_DIR="+vol+"\nARGUS_ACTIONS_MODE=enforce\n"), 0o600)
	var out, errb bytes.Buffer
	if rc := runActd([]string{"-once", "-file", env}, &out, &errb); rc != 1 {
		t.Fatalf("mode enforce started: %d", rc)
	}
}
