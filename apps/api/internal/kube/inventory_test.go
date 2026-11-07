package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInventory answers `get <type> -A -o json` with a small cluster; services fail (a login that may not list them).
const fakeInventory = `#!/bin/bash
for a in "$@"; do case "$a" in
namespaces) echo '{"items":[{"kind":"Namespace","metadata":{"name":"shop","labels":{"team":"x"}}}]}'; exit 0;;
deployments.apps) echo '{"items":[{"kind":"Deployment","metadata":{"name":"cart","namespace":"shop","labels":{"app.kubernetes.io/name":"cart","secret-ish":"v"},"annotations":{"a":"b"}},"spec":{"template":{"metadata":{"labels":{"app":"cart","app.kubernetes.io/name":"other"}},"spec":{"containers":[{"env":[{"name":"PASSWORD","value":"x"}]}]}}}},{"kind":"Deployment","metadata":{"name":"Bad Name","namespace":"shop"}}]}'; exit 0;;
statefulsets.apps|daemonsets.apps) echo '{"items":[]}'; exit 0;;
services) echo 'Error from server (Forbidden): services is forbidden' >&2; exit 1;;
ingresses.networking.k8s.io) echo '{"items":[{"kind":"Ingress","metadata":{"name":"front","namespace":"web"},"spec":{"rules":[{"host":"WWW.Northwind.example","http":{"paths":[{"backend":{"service":{"name":"storefront"}}}]}},{"host":"bad host"}]}}]}'; exit 0;;
esac; done
exit 3
`

func TestInventory(t *testing.T) {
	tl, d := toolSetup(t)
	os.WriteFile(tl.Kubectl, []byte(fakeInventory), 0o755)
	objs, complete, notes, err := tl.Inventory("dev", 100)
	if err != nil || complete || len(notes) != 1 || !strings.Contains(notes[0], "services: Error from server (Forbidden)") {
		t.Fatalf("inventory: %v %v %v", complete, notes, err)
	}
	got := map[string]map[string]any{}
	for _, o := range objs {
		got[o.Kind+" "+o.Key] = o.Attrs
	}
	if len(got) != 3 {
		t.Fatalf("objects: %v", got)
	}
	w := got["workload shop/deployment/cart"]
	if w == nil || fmt.Sprint(w["labels"]) != "map[app:cart app.kubernetes.io/name:cart]" {
		t.Fatalf("workload labels: %v", w)
	}
	in := got["ingress web/front"]
	if fmt.Sprint(in["hosts"]) != "[www.northwind.example]" || fmt.Sprint(in["backends"]) != "[storefront]" {
		t.Fatalf("ingress: %v", in)
	}
	for _, o := range objs {
		if s := fmt.Sprint(o.Attrs); strings.Contains(s, "PASSWORD") || strings.Contains(s, "annotations") || strings.Contains(s, "team") {
			t.Fatalf("more than names left the cluster: %s", s)
		}
	}
	b, _ := os.ReadFile(filepath.Join(d, "kube-ro.log"))
	if n := strings.Count(string(b), "verb=inventory"); n != len(InventoryTypes) {
		t.Fatalf("audit lines: %d\n%s", n, b)
	}
	if _, complete, notes, _ := tl.Inventory("dev", 1); complete || !strings.Contains(strings.Join(notes, ";"), "capped at 1") {
		t.Fatal("cap not partial")
	}
	if _, _, _, err := tl.Inventory("rw", 100); err == nil || !strings.Contains(err.Error(), "not enabled for the agent") {
		t.Fatalf("disabled cluster: %v", err)
	}
	os.WriteFile(tl.Kubectl, []byte("#!/bin/bash\nexit 1\n"), 0o755)
	if _, _, _, err := tl.Inventory("dev", 100); err == nil || !strings.Contains(err.Error(), "every inventory read failed") {
		t.Fatalf("all failed: %v", err)
	}
}
