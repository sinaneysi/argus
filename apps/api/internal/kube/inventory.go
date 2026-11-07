package kube

// inventory.go: names and app labels of a cluster, for Data sources discovery (ADR-0017 P2). The same gate as every
// tool call (the cluster enabled in root's clusters.json, the kubeconfig root-owned and readable by this user only while
// enabled, the pinned kubectl, one audit line per kubectl call), then one fixed `get <type> -A -o json` per type. Only
// names, a few app-naming labels and ingress hosts leave this file: never a Secret, a ConfigMap, an annotation, an env
// value or a spec beyond the template's labels.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// InventoryTypes are read in this order; a type the login may not list is noted and the rest still comes.
var InventoryTypes = []string{"namespaces", "deployments.apps", "statefulsets.apps", "daemonsets.apps", "services",
	"ingresses.networking.k8s.io"}

// AppLabels are the labels that usually name an application (nothing else of metadata is kept).
var AppLabels = []string{"app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/part-of",
	"app.kubernetes.io/component", "app", "k8s-app", "component", "release", "service"}

var (
	invValueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	invHostRe  = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*$`)
)

// Object is one inventory item: kind namespace | workload | service | ingress, a stable key and its attributes.
type Object struct {
	Kind  string
	Key   string
	Attrs map[string]any
}

func appLabels(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := map[string]string{}
	for _, k := range AppLabels {
		if s, ok := m[k].(string); ok && invValueRe.MatchString(s) {
			out[k] = s
		}
	}
	return out
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// InventoryObjects reduces one `kubectl get <type> -A -o json` answer to names, app labels and ingress hosts.
func InventoryObjects(doc map[string]any) []Object {
	var out []Object
	items, _ := doc["items"].([]any)
	for _, x := range items {
		it := mapOf(x)
		kind, _ := it["kind"].(string)
		md := mapOf(it["metadata"])
		name, _ := md["name"].(string)
		ns, _ := md["namespace"].(string)
		if !objRe.MatchString(name) || (ns != "" && !nsRe.MatchString(ns)) {
			continue
		}
		spec := mapOf(it["spec"])
		labels := appLabels(md["labels"])
		switch kind {
		case "Namespace":
			out = append(out, Object{"namespace", name, map[string]any{"name": name, "labels": labels}})
		case "Deployment", "StatefulSet", "DaemonSet":
			for k, v := range appLabels(mapOf(mapOf(spec["template"])["metadata"])["labels"]) {
				if _, ok := labels[k]; !ok {
					labels[k] = v
				}
			}
			t := strings.ToLower(kind)
			out = append(out, Object{"workload", ns + "/" + t + "/" + name, map[string]any{"ns": ns, "name": name, "type": t, "labels": labels}})
		case "Service":
			out = append(out, Object{"service", ns + "/" + name, map[string]any{"ns": ns, "name": name, "labels": labels,
				"selector": appLabels(spec["selector"])}})
		case "Ingress":
			hosts, backends := []string{}, []string{}
			rules, _ := spec["rules"].([]any)
			for _, r := range rules {
				rm := mapOf(r)
				if h, ok := rm["host"].(string); ok {
					h = strings.ToLower(h)
					if invHostRe.MatchString(h) && !inList(h, hosts...) && len(hosts) < 20 {
						hosts = append(hosts, h)
					}
				}
				paths, _ := mapOf(rm["http"])["paths"].([]any)
				for _, p := range paths {
					svc, _ := mapOf(mapOf(mapOf(p)["backend"])["service"])["name"].(string)
					if objRe.MatchString(svc) && !inList(svc, backends...) && len(backends) < 20 {
						backends = append(backends, svc)
					}
				}
			}
			out = append(out, Object{"ingress", ns + "/" + name, map[string]any{"ns": ns, "name": name, "labels": labels,
				"hosts": hosts, "backends": backends}})
		}
	}
	return out
}

// inList is whether x is one of xs.
func inList(x string, xs ...string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

// Inventory reads the cluster's names for discovery: (objects, complete, notes, error). A type that fails is a note
// and makes the run incomplete; more than maxObjects objects is incomplete too. The error is a refusal (cluster not
// enabled, kubeconfig not readable, kubectl missing) or every type failing.
func (t *Tool) Inventory(cluster string, maxObjects int) ([]Object, bool, []string, error) {
	if !NameRe.MatchString(cluster) {
		return nil, false, nil, refusal("bad cluster name")
	}
	cl, err := t.clusters()
	if err != nil {
		return nil, false, nil, err
	}
	_, path, err := t.kubeconfig(cluster, cl)
	if err != nil {
		return nil, false, nil, err
	}
	if fi, err := os.Stat(t.Kubectl); err != nil || fi.Mode().Perm()&0o111 == 0 {
		return nil, false, nil, refusal("kubectl is not installed on " + t.Host + " yet (root installs the pinned version)")
	}
	if !t.auditWritable() {
		return nil, false, nil, refusal("audit log " + t.AuditLog + " not writable (no audit, no kubectl)")
	}
	os.MkdirAll(t.CacheDir, 0o700)
	to := t.Timeout
	if to == 0 {
		to = 90 * time.Second
	}
	var out []Object
	var notes []string
	complete, failed := true, 0
	for _, typ := range InventoryTypes {
		ctx, cancel := context.WithTimeout(context.Background(), to)
		argv := []string{"--kubeconfig", path, "--request-timeout=30s", "--cache-dir", filepath.Join(t.CacheDir, cluster),
			"get", typ, "-A", "-o", "json"}
		cmd := exec.CommandContext(ctx, t.Kubectl, argv...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.CacheDir, "LANG=C.UTF-8", "KUBECONFIG=" + path}
		var so, se strings.Builder
		cmd.Stdout, cmd.Stderr = &limited{&so, 64 << 20}, &limited{&se, 64 << 10}
		rc := 0
		if err := cmd.Run(); ctx.Err() != nil {
			rc = 124
		} else if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				rc = ee.ExitCode()
			} else {
				rc = 126
			}
		}
		cancel()
		t.audit(cluster, "inventory", typ, "-A", strconv.Itoa(rc))
		if rc != 0 {
			complete, failed = false, failed+1
			why := fmt.Sprintf("exit %d", rc)
			if rc == 124 {
				why = fmt.Sprintf("no answer within %d s", int(to.Seconds()))
			} else if l := strings.Split(strings.TrimSpace(se.String()), "\n"); l[len(l)-1] != "" {
				why = l[len(l)-1]
			}
			notes = append(notes, typ+": "+cut(Redact(why), 160))
			continue
		}
		var doc map[string]any
		if json.Unmarshal([]byte(so.String()), &doc) != nil {
			complete, failed = false, failed+1
			notes = append(notes, typ+": kubectl did not return JSON")
			continue
		}
		for _, o := range InventoryObjects(doc) {
			if len(out) >= maxObjects {
				return out, false, append(notes, fmt.Sprintf("capped at %d objects", maxObjects)), nil
			}
			out = append(out, o)
		}
	}
	if failed == len(InventoryTypes) {
		return nil, false, notes, refusal("every inventory read failed (" + strings.Join(notes[:1], "") + ")")
	}
	return out, complete, notes, nil
}
