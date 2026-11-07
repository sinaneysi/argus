package kube

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Obviously fake material: no real cluster, user or credential.
const fakeCred = "fake-kube-cred-xxxxxxxxxxxxxxxxxxxxxxxxxxxx"

var otherCred = "other-" + strings.Repeat("y", 12)

func kc(user map[string]any, cluster map[string]any, extra map[string]any) string {
	if user == nil {
		user = map[string]any{"token": fakeCred}
	}
	if cluster == nil {
		cluster = map[string]any{"server": "https://192.0.2.10:6443", "certificate-authority-data": "RkFLRS1DQQ=="}
	}
	doc := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "dev-ro",
		"clusters": []any{map[string]any{"name": "dev", "cluster": cluster}},
		"users":    []any{map[string]any{"name": "agent-ro", "user": user}},
		"contexts": []any{map[string]any{"name": "dev-ro", "context": map[string]any{"cluster": "dev", "user": "agent-ro", "namespace": "docs"}}}}
	for k, v := range extra {
		doc[k] = v
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

const listRO = `Resources                                       Non-Resource URLs   Resource Names   Verbs
selfsubjectreviews.authentication.k8s.io        []                  []               [create]
selfsubjectaccessreviews.authorization.k8s.io   []                  []               [create]
selfsubjectrulesreviews.authorization.k8s.io    []                  []               [create]
pods                                            []                  []               [get list watch]
pods/log                                        []                  []               [get list watch]
deployments.apps                                []                  []               [get list watch]
                                                [/api/*]            []               [get]
                                                [/version]          []               [get]
`

func validate(t *testing.T, text, ctx string) (Summary, error) {
	t.Helper()
	doc, err := ParseJSON(text)
	if err != nil {
		return Summary{}, err
	}
	return Validate(doc, ctx)
}

func TestValidation(t *testing.T) {
	s, err := validate(t, kc(nil, nil, nil), "")
	if err != nil || s.Context != "dev-ro" || s.ServerHost != "192.0.2.10:6443" || s.Namespace != "docs" || s.Auth != "token" {
		t.Fatalf("good: %+v %v", s, err)
	}
	for _, c := range []struct {
		text, want string
	}{
		{kc(map[string]any{"exec": map[string]any{"command": "/bin/sh"}}, nil, nil), "exec plugin"},
		{kc(map[string]any{"auth-provider": map[string]any{"name": "oidc"}}, nil, nil), "auth-provider plugin"},
		{kc(map[string]any{"client-certificate": "/root/c.crt", "client-key": "/root/c.key"}, nil, nil), "client-certificate file path"},
		{kc(map[string]any{"tokenFile": "/var/run/t"}, nil, nil), "token file path"},
		{kc(nil, map[string]any{"server": "https://192.0.2.10", "certificate-authority": "/etc/ca"}, nil), "certificate-authority file path"},
		{kc(map[string]any{"token": fakeCred, "as": "system:admin"}, nil, nil), "impersonation"},
		{kc(map[string]any{"token": fakeCred, "surprise": "x"}, nil, nil), "unsupported key"},
		{kc(nil, nil, map[string]any{"exec": map[string]any{}}), "unsupported top-level key"},
		{kc(nil, map[string]any{"server": "http://192.0.2.10:8080"}, nil), "https:// server"},
		{kc(map[string]any{}, nil, nil), "no inline credential"},
		{kc(map[string]any{"client-certificate-data": "QQ=="}, nil, nil), "both client-certificate-data"},
		{kc(map[string]any{"token": strings.Repeat("t", MaxBytes)}, nil, nil), "larger than 64 KB"},
		{"[1]", "not a mapping"},
		{"", "empty"},
	} {
		_, err := validate(t, c.text, "")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), fakeCred) {
			t.Fatalf("want %q, got %v", c.want, err)
		}
	}
	if _, err := validate(t, kc(nil, nil, nil), "other"); err == nil || !strings.Contains(err.Error(), "not in the kubeconfig") {
		t.Fatal("unknown context accepted")
	}
	// minimal keeps only the chosen context
	doc, _ := ParseJSON(kc(nil, nil, nil))
	doc["users"] = append(doc["users"].([]any), map[string]any{"name": "admin", "user": map[string]any{"token": otherCred}})
	s, _ = Validate(doc, "")
	b, _ := json.Marshal(Minimal(doc, s))
	if strings.Contains(string(b), "other-") || !strings.Contains(string(b), fakeCred) {
		t.Fatal("minimal")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		listRO: "read-only",
		listRO + "deployments.apps   []   []   [get list watch create update patch delete]\n": "writes",
		listRO + "secrets   []   []   [get list watch]\n":                                     "secrets",
		"*.*   []   []   [*]\n   [*]   []   [*]\n":                                            "writes",
		"":          "unknown",
		"garbage\n": "unknown",
	}
	for text, want := range cases {
		if v := Classify(text, nil); v.Verdict != want {
			t.Fatalf("%q: %s, want %s", text, v.Verdict, want)
		}
	}
	if v := Classify(listRO, map[Probe]bool{Probes[3]: true}); v.Verdict != "writes" {
		t.Fatal("exec probe")
	}
	if v := Classify(listRO, map[Probe]bool{Probes[4]: true}); v.Verdict != "secrets" {
		t.Fatal("secrets probe")
	}
	if en, _, _ := MayEnable("release", "writes", true); en {
		t.Fatal("release took the override")
	}
	if en, ov, _ := MayEnable("develop", "secrets", true); !en || !ov {
		t.Fatal("develop override")
	}
	if en, _, _ := MayEnable("develop", "unknown", true); en {
		t.Fatal("unknown enabled")
	}
}

const fakeKubectl = `#!/bin/bash
prof=$(cat "%[1]s/profile")
a=" $* "
case "$a" in
  *" config view --raw -o json "*) cat "%[1]s/converted" ;;
  *" version -o json "*)
    [ "$prof" = down ] && { echo "Unable to connect to the server: dial tcp 192.0.2.10:6443: i/o timeout" >&2; exit 1; }
    echo '{"serverVersion":{"gitVersion":"v1.30.4"}}' ;;
  *" auth can-i --list "*) cat "%[1]s/list-$prof" ;;
  *" auth can-i "*)
    case "$prof:$a" in
      write:*" create pods "*) echo yes ;;
      secrets:*" get secrets "*) echo yes ;;
      *) echo no; exit 1 ;;
    esac ;;
  *" get namespaces -o name "*) printf 'namespace/default\nnamespace/docs\n' ;;
  *) echo "unexpected $*" >&2; exit 3 ;;
esac
`

type env struct {
	t   *testing.T
	o   Options
	lib string
	n   int
}

func setup(t *testing.T) *env {
	d := t.TempDir()
	lib := filepath.Join(d, "lib")
	for _, p := range []string{filepath.Join(d, "in"), filepath.Join(d, "out"), lib} {
		os.MkdirAll(p, 0o755)
	}
	k := filepath.Join(lib, "kubectl")
	os.WriteFile(k, []byte(fmt.Sprintf(fakeKubectl, lib)), 0o755)
	os.WriteFile(filepath.Join(lib, "list-ro"), []byte(listRO), 0o644)
	os.WriteFile(filepath.Join(lib, "list-write"), []byte(listRO), 0o644)
	os.WriteFile(filepath.Join(lib, "list-secrets"), []byte(listRO), 0o644)
	os.WriteFile(filepath.Join(lib, "profile"), []byte("ro"), 0o644)
	sum, _ := fileSHA(k)
	db := filepath.Join(d, "auth.db")
	c, _ := sql.Open("sqlite", db)
	c.Exec("CREATE TABLE users (username TEXT, role TEXT, disabled INT)")
	c.Exec("INSERT INTO users VALUES ('admin','admin',0),('viewer','viewer',0)")
	c.Close()
	g, _ := user.LookupGroupId(fmt.Sprint(os.Getgid()))
	o := Options{ReqDir: filepath.Join(d, "in"), OutDir: filepath.Join(d, "out"), AuthDB: db, KubeDir: filepath.Join(d, "kube"),
		Kubectl: k, Version: "v1.34.13", SHA256: sum, URL: "https://example.invalid/{version}", AgentGroup: g.Name, AppUID: 65534,
		CheckUID: -1} // as root (CI) the check would drop to nobody, who cannot run a kubectl in a 0700 temp dir
	return &env{t: t, o: o, lib: lib}
}

func (e *env) profile(p string) { os.WriteFile(filepath.Join(e.lib, "profile"), []byte(p), 0o644) }

func (e *env) drop(usr string, req map[string]any) string {
	e.n++
	rid := fmt.Sprintf("%016x", e.n)
	req["user"] = usr
	b, _ := json.Marshal(req)
	os.WriteFile(filepath.Join(e.o.ReqDir, usr+"-"+rid+".json"), b, 0o600)
	return rid
}

func (e *env) apply() Status {
	e.t.Helper()
	var log bytes.Buffer
	e.o.Log = &log
	if err := Apply(e.o); err != nil {
		e.t.Fatal(err)
	}
	if strings.Contains(log.String(), fakeCred) {
		e.t.Fatal("the kubeconfig reached the log")
	}
	var st Status
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "kube-status.json"))
	json.Unmarshal(b, &st)
	return st
}

func mode(t *testing.T, p string) os.FileMode {
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestApplySaveTestRemove(t *testing.T) {
	e := setup(t)
	rid := e.drop("admin", map[string]any{"kind": "save", "name": "dev", "env": "develop", "kubeconfig": kc(nil, nil, nil)})
	st := e.apply()
	if r := st.Results[rid]["result"]; r != "saved dev: read-only; enabled (read-only)" {
		t.Fatalf("save: %v", r)
	}
	p := filepath.Join(e.o.KubeDir, "dev.kubeconfig")
	if mode(t, p) != 0o640 || mode(t, e.o.KubeDir) != 0o750 || mode(t, filepath.Join(e.o.KubeDir, "clusters.json")) != 0o644 {
		t.Fatal("modes")
	}
	c := st.Clusters["dev"]
	if c == nil || !c.Enabled || c.ServerHost != "192.0.2.10:6443" || c.Test["server_version"] != "v1.30.4" || c.Test["namespaces"] != float64(2) {
		t.Fatalf("entry: %+v", c)
	}
	if ents, _ := os.ReadDir(e.o.ReqDir); len(ents) != 0 {
		t.Fatal("request not removed")
	}
	// the cluster gained write access: a check disables it
	e.profile("write")
	rid = e.drop("admin", map[string]any{"kind": "test", "name": "dev"})
	st = e.apply()
	if r := st.Results[rid]["result"]; r != "tested dev: writes; not enabled (not read-only)" || mode(t, p) != 0o600 {
		t.Fatalf("test: %v", r)
	}
	// override on develop
	rid = e.drop("admin", map[string]any{"kind": "save", "name": "dev", "env": "develop", "override": true, "kubeconfig": kc(nil, nil, nil)})
	st = e.apply()
	if r := st.Results[rid]["result"]; r != "saved dev: writes; enabled (override)" || mode(t, p) != 0o640 {
		t.Fatalf("override: %v", r)
	}
	bk, _ := os.ReadDir(filepath.Join(e.o.KubeDir, "backup"))
	if len(bk) != 1 || mode(t, filepath.Join(e.o.KubeDir, "backup", bk[0].Name())) != 0o600 {
		t.Fatalf("backup: %v", bk)
	}
	rid = e.drop("admin", map[string]any{"kind": "remove", "name": "dev"})
	st = e.apply()
	if r, _ := st.Results[rid]["result"].(string); !strings.HasPrefix(r, "removed dev; backup dev.kubeconfig.") || st.Clusters["dev"] != nil {
		t.Fatalf("remove: %v", r)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file not removed")
	}
}

func TestApplyReleaseAndRefusals(t *testing.T) {
	e := setup(t)
	e.profile("secrets")
	cases := []struct {
		usr  string
		req  map[string]any
		want string
	}{
		{"admin", map[string]any{"kind": "save", "name": "prod", "env": "release", "override": true, "kubeconfig": kc(nil, nil, nil)}, "refused: release never takes the override"},
		{"admin", map[string]any{"kind": "save", "name": "prod", "env": "release", "kubeconfig": kc(nil, nil, nil)}, "saved prod: secrets; not enabled (release never takes the override)"},
		{"viewer", map[string]any{"kind": "save", "name": "dev", "env": "develop", "kubeconfig": kc(nil, nil, nil)}, "refused: only an enabled admin"},
		{"admin", map[string]any{"kind": "save", "name": "Dev!", "env": "develop", "kubeconfig": kc(nil, nil, nil)}, "refused: the cluster name"},
		{"admin", map[string]any{"kind": "save", "name": "dev", "env": "prod", "kubeconfig": kc(nil, nil, nil)}, "refused: choose develop"},
		{"admin", map[string]any{"kind": "save", "name": "dev", "env": "develop", "kubeconfig": kc(map[string]any{"exec": map[string]any{}}, nil, nil)}, "exec plugin"},
		{"admin", map[string]any{"kind": "test", "name": "nope"}, "refused: there is no kubeconfig for nope"},
		{"admin", map[string]any{"kind": "wipe", "name": "dev"}, "refused: unknown request kind"},
	}
	for _, c := range cases {
		rid := e.drop(c.usr, c.req)
		st := e.apply()
		if r, _ := st.Results[rid]["result"].(string); !strings.Contains(r, c.want) {
			t.Fatalf("%v: %q, want %q", c.req["name"], r, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(e.o.KubeDir, "prod.kubeconfig")); err != nil || mode(t, filepath.Join(e.o.KubeDir, "prod.kubeconfig")) != 0o600 {
		t.Fatal("release kubeconfig must be stored 0600 and refused")
	}
	// YAML goes through kubectl config view; unreachable is unknown
	os.WriteFile(filepath.Join(e.lib, "converted"), []byte(kc(nil, nil, nil)), 0o644)
	e.profile("down")
	rid := e.drop("admin", map[string]any{"kind": "save", "name": "dev", "env": "pre-release", "override": true, "kubeconfig": "apiVersion: v1\nkind: Config\n"})
	st := e.apply()
	if r := st.Results[rid]["result"]; r != "saved dev: unknown; not enabled (could not be checked)" ||
		!strings.Contains(fmt.Sprint(st.Clusters["dev"].Test["message"]), "i/o timeout") {
		t.Fatalf("unreachable: %v", r)
	}
	b, _ := os.ReadFile(filepath.Join(e.o.OutDir, "kube-status.json"))
	a, _ := os.ReadFile(filepath.Join(e.o.OutDir, "audit.log"))
	if strings.Contains(string(b)+string(a), fakeCred) {
		t.Fatal("the kubeconfig reached the status or audit log")
	}
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInstallKubectl(t *testing.T) {
	e := setup(t)
	os.Remove(e.o.Kubectl)
	body := []byte("#!/bin/sh\necho fake\n")
	sum := sha256.Sum256(body)
	e.o.SHA256 = hex.EncodeToString(sum[:])
	e.o.HTTP = &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v1.34.13") {
			t.Fatalf("url %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte("not kubectl")))}, nil
	})}
	if st := e.o.InstallKubectl(); !strings.Contains(fmt.Sprint(st["error"]), "sha256 mismatch") || st["installed"] != false {
		t.Fatalf("mismatch: %v", st)
	}
	e.o.HTTP = &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	if st := e.o.InstallKubectl(); st["installed"] != true || st["verified"] != true || mode(t, e.o.Kubectl) != 0o755 {
		t.Fatalf("install: %v", st)
	}
	e.o.Version = "latest"
	if st := e.o.InstallKubectl(); !strings.Contains(fmt.Sprint(st["error"]), "no valid kubectl pin") {
		t.Fatal("bad pin accepted")
	}
}

// --- the tool -------------------------------------------------------------------------------------------------------

func toolSetup(t *testing.T) (*Tool, string) {
	d := t.TempDir()
	kd := filepath.Join(d, "kube")
	os.MkdirAll(kd, 0o750)
	out := filepath.Join(d, "output")
	os.WriteFile(out, []byte("NAME READY\nweb-1 1/1\n"), 0o644)
	k := filepath.Join(d, "kubectl")
	os.WriteFile(k, []byte(fmt.Sprintf("#!/bin/bash\necho \"$*\" > %q\ncat %q\n", filepath.Join(d, "args"), out)), 0o755)
	cl := map[string]any{"clusters": map[string]any{
		"dev": map[string]any{"env": "develop", "enabled": true, "verdict": "read-only", "server_host": "192.0.2.10:6443", "context": "dev-ro"},
		"rw":  map[string]any{"env": "develop", "enabled": false, "verdict": "writes", "why": "not read-only"}}}
	b, _ := json.Marshal(cl)
	os.WriteFile(filepath.Join(kd, "clusters.json"), b, 0o644)
	os.WriteFile(filepath.Join(kd, "dev.kubeconfig"), []byte("{}"), 0o640)
	return &Tool{KubeDir: kd, Kubectl: k, AuditLog: filepath.Join(d, "kube-ro.log"), CacheDir: filepath.Join(d, "cache"),
		OwnerUID: os.Getuid(), Host: "testhost", Now: func() time.Time { return time.Unix(1700000000, 0) }}, d
}

func call(tl *Tool, args ...string) (int, string, string) {
	var o, e bytes.Buffer
	rc := tl.Run(args, &o, &e)
	return rc, o.String(), e.String()
}

func TestToolAllowed(t *testing.T) {
	tl, d := toolSetup(t)
	for _, c := range []struct{ argv, want []string }{
		{[]string{"get", "pods", "-n", "docs"}, []string{"get", "pods", "-n", "docs"}},
		{[]string{"get", "configmaps", "-o", "name"}, []string{"get", "configmaps", "-o", "name"}},
		{[]string{"describe", "pod", "web-1", "-n", "docs"}, []string{"describe", "pod", "web-1", "-n", "docs"}},
		{[]string{"logs", "web-1", "--tail", "5000", "--since", "2h"}, []string{"logs", "web-1", "--tail", "500", "--since", "2h", "--limit-bytes", "262144"}},
		{[]string{"logs", "deploy/docs"}, []string{"logs", "deploy/docs", "--tail", "200"}},
		{[]string{"top", "pods", "-A"}, []string{"top", "pods", "-A"}},
		{[]string{"events", "--for", "pod/web-1"}, []string{"events", "--for", "pod/web-1"}},
		{[]string{"api-resources"}, []string{"api-resources"}},
		{[]string{"version"}, []string{"version"}},
		{[]string{"can-i"}, []string{"auth", "can-i", "--list"}},
	} {
		rc, out, se := call(tl, append([]string{"dev"}, c.argv...)...)
		if rc != 0 || !strings.Contains(out, "# dev (develop)") {
			t.Fatalf("%v: %d %s", c.argv, rc, se)
		}
		b, _ := os.ReadFile(filepath.Join(d, "args"))
		got := strings.Fields(string(b))
		if got[0] != "--kubeconfig" || got[2] != "--request-timeout=20s" || strings.Join(got[5:5+len(c.want)], " ") != strings.Join(c.want, " ") {
			t.Fatalf("%v: argv %v", c.argv, got)
		}
	}
}

func TestToolRefused(t *testing.T) {
	tl, d := toolSetup(t)
	for _, argv := range [][]string{{"exec", "web-1", "--", "sh"}, {"cp", "a", "b"}, {"port-forward", "web-1", "80"}, {"proxy"},
		{"edit", "pod", "x"}, {"apply", "-f", "x"}, {"delete", "pod", "x"}, {"scale", "deploy/x"}, {"rollout", "restart", "deploy/x"},
		{"drain", "n1"}, {"cordon", "n1"}, {"attach", "x"}, {"get", "--raw", "/api"}, {"get", "secrets"}, {"get", "secret", "x", "-o", "yaml"},
		{"get", "pods,secrets"}, {"get", "secrets.v1", "-A"}, {"describe", "secret", "x"}, {"describe", "configmap", "x"},
		{"get", "cm", "-o", "yaml"}, {"get", "configmaps", "-o=json"}, {"get", "pods", "-w"}, {"logs", "-f", "x"},
		{"logs", "x", "--follow"}, {"get", "pods", "--kubeconfig", "/tmp/x"}, {"get", "pods", "--context", "a"},
		{"get", "pods", "--as", "system:admin"}, {"get", "pods", "--as-group=system:masters"}, {"get", "pods", "--token", "x"},
		{"get", "pods", "-s", "https://192.0.2.1"}, {"get", "pods", "-o", "jsonpath={.items}"}, {"get", "pods", "-n", "Bad_NS"},
		{"get", "*"}, {"logs", "x", "--since", "7h"}, {"logs", "secret/x"}, {"auth", "reconcile"}, {"get", "pods;id"},
		{"top", "secrets"}, {"get"}, {}, {"certificate", "approve", "x"}} {
		rc, out, se := call(tl, append([]string{"dev"}, argv...)...)
		if rc != 2 || out != "" || !strings.Contains(se, "REFUSED") {
			t.Fatalf("%v: rc %d out %q", argv, rc, out)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "args")); err == nil {
		t.Fatal("kubectl ran for a refused call")
	}
	if _, _, se := call(tl, "prod", "get", "pods"); !strings.Contains(se, "no kubeconfig for cluster 'prod'") || !strings.Contains(se, "Settings -> Kubernetes") {
		t.Fatal(se)
	}
	if _, _, se := call(tl, "rw", "get", "pods"); !strings.Contains(se, "not enabled for the agent (not read-only)") {
		t.Fatal(se)
	}
	os.Chmod(filepath.Join(tl.KubeDir, "dev.kubeconfig"), 0o644)
	if _, _, se := call(tl, "dev", "get", "pods"); !strings.Contains(se, "readable by others") {
		t.Fatal(se)
	}
	os.Chmod(filepath.Join(tl.KubeDir, "dev.kubeconfig"), 0o640)
	tl.OwnerUID = os.Getuid() + 4242
	if _, _, se := call(tl, "dev", "get", "pods"); !strings.Contains(se, "not owned by root") {
		t.Fatal(se)
	}
	tl.OwnerUID = os.Getuid()
	os.Remove(tl.Kubectl)
	if _, _, se := call(tl, "dev", "get", "pods"); !strings.Contains(se, "kubectl is not installed") {
		t.Fatal(se)
	}
	rc, out, _ := call(tl, "list")
	if rc != 0 || !strings.Contains(out, "dev  develop  read-only  enabled  192.0.2.10:6443") ||
		!strings.Contains(out, "rw  develop  writes  refused (not read-only)") || !strings.Contains(out, "never: Secrets") {
		t.Fatal(out)
	}
	os.Remove(filepath.Join(tl.KubeDir, "clusters.json"))
	if _, out, _ := call(tl, "list"); !strings.Contains(out, "no Kubernetes clusters configured yet") {
		t.Fatal(out)
	}
}

func TestToolRedactionCapsAudit(t *testing.T) {
	tl, d := toolSetup(t)
	lines := []string{"        - name: DB_PASSWORD", "          value: hunter2-plain", "        - name: LOG_LEVEL", "          value: debug",
		`          "name": "API_TOKEN",`, `          "value": "abc123-plain"`, "Authorization: Bear" + "er abcdefghijklmnop",
		"url: https://user:pw" + "@192.0.2.5/x", "client-key-data: LS0tLS1CRUdJTi1mYWtl", "jwt " + "eyJ" + "abcdefghij." + "eyJ" + "abcdefghij.sigabcdefgh",
		"-----BEGIN RSA " + "PRIVATE KEY-----", "MIIfake", "-----END RSA " + "PRIVATE KEY-----", "secret_key=plainvalue"}
	os.WriteFile(filepath.Join(d, "output"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	t.Setenv("ARGUS_RUN_ID", "run-42")
	_, out, _ := call(tl, "dev", "get", "pods", "-o", "yaml", "-n", "docs")
	for _, leak := range []string{"hunter2-plain", "abc123-plain", "abcdefghijklmnop", "user:pw@", "LS0tLS1CRUdJTi1mYWtl", "eyJabcdefghij", "MIIfake", "plainvalue"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leak %q in %s", leak, out)
		}
	}
	if !strings.Contains(out, "value: debug") {
		t.Fatal("over-redacted")
	}
	var big []string
	for i := 0; i < 2000; i++ {
		big = append(big, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(filepath.Join(d, "output"), []byte(strings.Join(big, "\n")+"\n"), 0o644)
	if _, out, _ := call(tl, "dev", "logs", "web-1"); !strings.Contains(out, "[kube: output capped at 500 of 2000 lines]") {
		t.Fatal("log cap")
	}
	call(tl, "dev", "exec", "x")
	b, _ := os.ReadFile(tl.AuditLog)
	al := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(al) != 3 || !strings.Contains(al[0], "cluster=dev verb=get resource=pods ns=docs run=run-42 exit=0") || !strings.HasSuffix(al[2], "exit=refused") {
		t.Fatalf("audit: %q", al)
	}
	tl.AuditLog = filepath.Join(d, "missing", "x.log")
	if rc, _, se := call(tl, "dev", "get", "pods"); rc != 2 || !strings.Contains(se, "audit log") {
		t.Fatal("no audit, still ran")
	}
}
