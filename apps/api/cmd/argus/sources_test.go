package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesCommands(t *testing.T) {
	if code, out, _ := call("sources"); code != 2 || !strings.Contains(out, "argus sources list [type]") {
		t.Fatalf("sources: %d %q", code, out)
	}
	if code, out, _ := call("sources", "-h"); code != 0 || !strings.Contains(out, "argus-sources.path") {
		t.Fatalf("sources -h: %d %q", code, out)
	}
	if code, _, e := call("sources", "wipe"); code != 2 || !strings.Contains(e, "unknown subcommand") {
		t.Fatalf("wipe: %d %q", code, e)
	}
	if code, _, e := call("sources", "list", "Git;lab"); code != 2 || !strings.Contains(e, "one word") {
		t.Fatalf("bad type: %d %q", code, e)
	}
	dir := t.TempDir()
	base := "COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_ADMIN_INITIAL_PASSWORD=x\n" +
		"ARGUS_SETTINGS_IN=" + dir + "/in\nARGUS_SETTINGS_OUT=" + dir + "/out\nARGUS_ETC_DIR=" + dir + "/etc\n" +
		"ARGUS_DB=" + dir + "/state/ops.db\nARGUS_AUTH_DB=" + dir + "/auth.db\n"
	demo := filepath.Join(dir, "demo.env")
	os.WriteFile(demo, []byte(base+"DEMO_MODE=1\n"), 0o600)
	for _, sub := range []string{"apply", "check", "record"} {
		if code, _, e := call("sources", sub, "-file", demo); code != 1 || !strings.Contains(e, "disabled in the demo") {
			t.Fatalf("%s in the demo: %d %q", sub, code, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "sources")); err == nil {
		t.Fatal("the demo wrote a sources dir")
	}
	// the catalogue: nothing yet, then from the status file, then from ARGUS_DB after record
	cfg := filepath.Join(dir, "argus.env")
	os.WriteFile(cfg, []byte(base), 0o600)
	t.Setenv("ARGUS_CONFIG", cfg)
	if code, out, _ := call("sources", "list"); code != 0 || !strings.Contains(out, "no data sources (Settings -> Data sources)") {
		t.Fatalf("list empty: %d %q", code, out)
	}
	os.MkdirAll(filepath.Join(dir, "out"), 0o755)
	os.MkdirAll(filepath.Join(dir, "state"), 0o755)
	st, _ := json.Marshal(map[string]any{"sources": map[string]any{"grafana/east": map[string]any{"type": "grafana", "name": "east",
		"env": "release", "purpose": "agent", "status": "readonly", "enabled": true, "cred_ref": "secret:grafana-east-token"}}})
	os.WriteFile(filepath.Join(dir, "out", "sources-status.json"), st, 0o644)
	if code, out, _ := call("sources", "list", "grafana"); code != 0 || !strings.Contains(out, "grafana\teast\trelease\tagent\tread-only verified\tyes") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, out, e := call("sources", "record"); code != 0 || out != "data sources recorded: 1\n" {
		t.Fatalf("record: %d %q %q", code, out, e)
	}
	os.Remove(filepath.Join(dir, "out", "sources-status.json"))
	if code, out, _ := call("sources", "list"); code != 0 || !strings.Contains(out, "grafana\teast") {
		t.Fatalf("list from the DB: %d %q", code, out)
	}
	// apply with no request: migrates nothing, writes the registry and the status
	if code, _, e := call("sources", "apply"); code != 0 {
		t.Fatalf("apply: %d %q", code, e)
	}
	if fi, err := os.Stat(filepath.Join(dir, "etc", "sources", "registry.json")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatal("registry not written")
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "sources-status.json")); err != nil {
		t.Fatal("status not written")
	}
}
