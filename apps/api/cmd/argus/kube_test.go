package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubeCommands(t *testing.T) {
	if code, out, _ := call("tools"); code != 2 || !strings.Contains(out, "argus tools kube list") {
		t.Fatalf("tools: %d %q", code, out)
	}
	if code, _, e := call("tools", "helm"); code != 2 || !strings.Contains(e, "unknown tool") {
		t.Fatalf("tools helm: %d %q", code, e)
	}
	if code, out, _ := call("secrets", "-h"); code != 0 || !strings.Contains(out, "apply-kube") || !strings.Contains(out, "install-kubectl") {
		t.Fatalf("secrets -h: %d %q", code, out)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "argus.env")
	os.WriteFile(cfg, []byte("COMPANY_NAME=Acme\nARGUS_PUBLIC_URL=https://argus.acme.example\nARGUS_ADMIN_INITIAL_PASSWORD=x\n"+
		"DEMO_MODE=1\nARGUS_SETTINGS_IN="+dir+"/in\nARGUS_SETTINGS_OUT="+dir+"/out\nARGUS_KUBE_DIR="+dir+"/kube\n"), 0o600)
	for _, sub := range []string{"apply-kube", "install-kubectl"} {
		if code, _, e := call("secrets", sub, "-file", cfg); code != 1 || !strings.Contains(e, "disabled in the demo") {
			t.Fatalf("%s in the demo: %d %q", sub, code, e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "kube")); err == nil {
		t.Fatal("the demo wrote a kube dir")
	}
	// the tool refuses an unknown cluster before kubectl starts (no clusters.json)
	t.Setenv("ARGUS_CONFIG", cfg)
	t.Setenv("ARGUS_KUBE_AUDIT_LOG", filepath.Join(dir, "kube-ro.log"))
	if code, _, e := call("tools", "kube", "dev", "get", "pods"); code != 2 || !strings.Contains(e, "no kubeconfig for cluster 'dev'") {
		t.Fatalf("tools kube: %d %q", code, e)
	}
	if code, _, e := call("tools", "kube", "dev", "exec", "x"); code != 2 || !strings.Contains(e, "not a read-only command") {
		t.Fatalf("tools kube exec: %d %q", code, e)
	}
}
