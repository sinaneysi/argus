package app

// Settings → Kubernetes as JSON: admins only, the Go route's policy and limit, a refused save names the field and never
// echoes the kubeconfig, release never takes the override, stored kubeconfigs are never read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1KubePolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/admin/kube": "/admin/kube"}, map[string]any{"action": "test", "name": "dev"}, false)
}

func TestAPIv1KubeRead(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/admin/kube", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/admin/kube", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	ck := map[string]string{auth.COOKIE: adm}
	r := e.req("GET", "/api/v1/admin/kube", ck, nil, nil)
	d := v1JSON(t, r)
	cl := d["clusters"].([]any)
	if len(cl) == 0 || d["status"] == nil || strings.Contains(r.body, "<span") || strings.Contains(r.body, "client-key") {
		t.Fatal(r.body[:300])
	}
	name := cl[0].(map[string]any)["name"].(string)
	a := v1JSON(t, e.req("GET", "/api/v1/admin/kube?replace="+name, ck, nil, nil))["add"].(map[string]any)
	if a["replace"] != name || a["env"] == "" || a["open"] != true {
		t.Fatal(a)
	}
	if a := v1JSON(t, e.req("GET", "/api/v1/admin/kube?replace=../x", ck, nil, nil))["add"].(map[string]any); a["replace"] != "" {
		t.Fatal(a)
	}
}

func TestAPIv1KubeWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	if err := os.MkdirAll(filepath.Join(SET_IN, "kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	for field, b := range map[string]map[string]any{
		"name":       {"action": "save", "name": "Bad Name"},
		"env":        {"action": "save", "name": "dev", "env": "x"},
		"context":    {"action": "save", "name": "dev", "env": "develop", "context": "a b"},
		"override":   {"action": "save", "name": "dev", "env": "release", "override": "1"},
		"kubeconfig": {"action": "save", "name": "dev", "env": "develop", "kubeconfig": "exec: " + v1FakeCred},
	} {
		r := e.v1Post("/api/v1/admin/kube", ck, b, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != field || strings.Contains(r.body, v1FakeCred) {
			t.Fatal(field, r.status, r.body)
		}
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "kube", "*")); len(l) != 0 {
		t.Fatal("a refused save wrote a request", l)
	}
	if r := e.v1Post("/api/v1/admin/kube", ck, map[string]any{"action": "nope", "name": "dev"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	r := e.v1Post("/api/v1/admin/kube", ck, map[string]any{"action": "test", "name": "dev"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/kube?kr=") {
		t.Fatal(r.status, r.body)
	}
}
