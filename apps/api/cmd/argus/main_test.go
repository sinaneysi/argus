package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestDispatch(t *testing.T) {
	if code, _, e := call(); code != 2 || !strings.Contains(e, "usage: argus") {
		t.Fatalf("no args: %d %q", code, e)
	}
	code, out, _ := call("-h")
	if code != 0 || !strings.Contains(out, "worker") || !strings.Contains(out, "(not implemented yet)") {
		t.Fatalf("help: %d %q", code, out)
	}
	if code, _, e := call("ingest"); code != 2 || !strings.Contains(e, "not implemented yet") {
		t.Fatalf("ingest: %d %q", code, e)
	}
	if code, _, e := call("nope"); code != 2 || !strings.Contains(e, "unknown command") {
		t.Fatalf("unknown: %d %q", code, e)
	}
	if code, out, _ := call("version"); code != 0 || !strings.HasPrefix(out, "argus ") {
		t.Fatalf("version: %d %q", code, out)
	}
	for _, args := range [][]string{{"version", "-h"}, {"config", "-h"}, {"config", "check", "-h"}, {"demo", "-h"}, {"checks", "-h"}, {"worker", "-h"}} {
		if code, out, _ := call(args...); code != 0 || !strings.Contains(out, "usage:") {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
}

func TestConfigCheck(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.env")
	os.WriteFile(good, []byte("COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_ADMIN_INITIAL_PASSWORD=topsecret\n"), 0o600)
	t.Setenv("OPS_DB", "/srv/ops.db")
	code, out, e := call("config", "check", "-file", good)
	if code != 0 || !strings.Contains(e, "config: OK") || !strings.Contains(e, "OPS_DB is deprecated") {
		t.Fatalf("good: %d\n%s\n%s", code, out, e)
	}
	if strings.Contains(out, "topsecret") || !strings.Contains(out, "ARGUS_DB=/srv/ops.db") {
		t.Fatalf("output:\n%s", out)
	}
	bad := filepath.Join(dir, "bad.env")
	os.WriteFile(bad, []byte("ARGUS_BIND=x\n"), 0o600)
	code, _, e = call("config", "check", "-q", "-file", bad)
	if code != 1 || !strings.Contains(e, "CONFIG ERROR: COMPANY_NAME: required") || !strings.Contains(e, "ARGUS_BIND") {
		t.Fatalf("bad: %d\n%s", code, e)
	}
}

func TestConfigShowAndSchema(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.env")
	os.WriteFile(good, []byte("COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nDEMO_PASSWORD=topsecret\n"), 0o600)
	t.Setenv("OPS_ALERTS", "/srv/alerts.json")
	code, out, _ := call("config", "show", "-json", "-file", good)
	if code != 0 || strings.Contains(out, "topsecret") || !strings.Contains(out, `"source": "alias"`) ||
		!strings.Contains(out, `"alias": "OPS_ALERTS"`) || !strings.Contains(out, `"source": "file"`) {
		t.Fatalf("show -json: %d\n%s", code, out)
	}
	if code, out, _ := call("config", "show", "-file", good); code != 0 || !strings.Contains(out, "ARGUS_ALERTS_FILE=/srv/alerts.json") {
		t.Fatalf("show: %d\n%s", code, out)
	}
	if code, out, _ := call("config", "schema"); code != 0 || !strings.Contains(out, `"key": "ARGUS_ALERTS_FILE"`) {
		t.Fatalf("schema: %d", code)
	}
}

func TestChecks(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "b", "x.tar"), []byte("data"), 0o644)
	os.WriteFile(filepath.Join(dir, "sets.json"), []byte(`{"sets": [{"name": "files_x", "path": "`+dir+`/b"}]}`), 0o644)
	env := filepath.Join(dir, "argus.env")
	base := "COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_STATE_DIR=" + dir + "\nARGUS_BACKUP_SETS_FILE=" + dir + "/sets.json\n"
	os.WriteFile(env, []byte(base), 0o600)
	if code, out, _ := call("checks", "list", "-file", env); code != 0 || !strings.Contains(out, "backups  off") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, e := call("checks", "all", "-file", env); code != 0 {
		t.Fatalf("all disabled: %d %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dir, "ops.db")); err == nil {
		t.Fatal("a disabled collector must not write")
	}
	os.WriteFile(env, []byte(base+"ARGUS_COLLECT_BACKUPS=1\n"), 0o600)
	if code, _, e := call("checks", "backups", "-file", env); code != 0 {
		t.Fatalf("backups: %d %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(dir, "ops.db")); err != nil {
		t.Fatal("backups must record its run in ARGUS_DB")
	}
	os.WriteFile(env, []byte(base+"ARGUS_COLLECT_ALERTS=1\n"), 0o600)
	if code, _, e := call("checks", "alerts", "-file", env); code != 1 || !strings.Contains(e, "ARGUS_ALERTS_SOURCES: required") {
		t.Fatalf("alerts without sources: %d %s", code, e)
	}
	if code, _, _ := call("checks", "nope"); code != 2 {
		t.Fatal("unknown collector")
	}
	if code, _, _ := call("worker", "-once", "-file", env); code != 1 {
		t.Fatal("worker with invalid config")
	}
}
