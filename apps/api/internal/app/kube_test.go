package app

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Obviously fake material: no real cluster or credential.
const kubeFakeCred = "fake-kube-cred-xxxxxxxxxxxxxxxxxxxxxxxxxxxx"

func kubeYAML(user string) string {
	return "apiVersion: v1\nkind: Config\ncurrent-context: dev-ro\nclusters:\n- name: dev\n  cluster:\n    server: https://192.0.2.10:6443\n" +
		"contexts:\n- name: dev-ro\n  context:\n    cluster: dev\n    user: argus-ro\nusers:\n- name: argus-ro\n  user:\n" + user
}

var kubeGoodUser = "    token: " + kubeFakeCred + "\n"

func kubeSetup(t *testing.T) (*srvEnv, map[string]string, string) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	os.MkdirAll(filepath.Join(SET_IN, "kube"), 0o755)
	return e, map[string]string{"opsr_session": tok}, csrf
}

func kubeRequests(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(SET_IN, "kube", "*.json"))
	return m
}

func kubeStatusFile(t *testing.T, doc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(SET_OUT, "kube-status.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKubePageStates(t *testing.T) {
	srvSetup(t)

	if strings.Contains(kubeHelpYAML, "secrets") || strings.Contains(kubeHelpYAML, "[\"*\"]") {
		t.Fatal("the recommended role must not list secrets")
	}
	// kubectl missing: the status says so, with root's install error
	kubeStatusFile(t, `{"kubectl": {"installed": false, "version": "v1.34.13", "error": "could not install kubectl v1.34.13: network unreachable"},
		"clusters": {}}`)

	// three clusters: one read-only, one that writes (develop), one release that reads Secrets
	kubeStatusFile(t, `{"kubectl": {"installed": true, "verified": true, "version": "v1.34.13"}, "clusters": {
		"dev": {"env": "develop", "context": "dev-ro", "user": "argus-ro", "server_host": "192.0.2.10:6443", "namespace": "outline",
			"verdict": "read-only", "enabled": true, "why": "read-only", "set_by": "admin", "set_at": 1700000000, "findings": [],
			"test": {"result": "ok", "server_version": "v1.30.4", "namespaces": 12, "at": 1700000000, "by": "admin"}},
		"rw": {"env": "develop", "server_host": "192.0.2.11:6443", "verdict": "writes", "enabled": false, "why": "not read-only",
			"findings": ["can create/delete deployments.apps", "can exec into pods"], "test": {"result": "ok", "at": 1700000000}},
		"prod": {"env": "release", "server_host": "192.0.2.12:6443", "verdict": "secrets", "enabled": false, "insecure": true,
			"why": "release never takes the override", "findings": ["can read secrets"], "test": {"result": "ok", "at": 1700000000}},
		"Bad Name": {"env": "develop"}}}`)

	// Replace prefills name and environment and opens the form

}

func TestKubeVerdicts(t *testing.T) {
	ok := kubeFacts{Known: true, Kubectl: true, Clusters: []kubeCluster{{Name: "dev", Enabled: true, Verdict: "read-only"}}}
	if v := kubeVerdictOf(ok); v.Kind != "ok" || !strings.Contains(v.Head, "1 cluster, read-only") {
		t.Fatalf("ok: %+v", v)
	}
	ov := kubeFacts{Known: true, Kubectl: true, Clusters: []kubeCluster{{Name: "dev", Enabled: true, Override: true, Verdict: "writes"}}}
	if v := kubeVerdictOf(ov); v.Kind != "warn" || v.Badge != "Override" {
		t.Fatalf("override: %+v", v)
	}
	if v := kubeVerdictOf(kubeFacts{Known: true, Kubectl: true}); v.Badge != "None yet" || v.Href != "#kube-add" {
		t.Fatalf("empty: %+v", v)
	}
	for res, want := range map[string]string{
		"saved dev: read-only; enabled (read-only)":                             "ok|dev saved. The agent can read it, read-only.",
		"tested dev: writes; not enabled (not read-only)":                       "fail|dev checked, but refused for the agent: this kubeconfig can write.",
		"saved prod: secrets; not enabled (release never takes the override)":   "fail|prod saved, but refused for the agent: this kubeconfig can read Secrets.",
		"saved dev: writes; enabled (override)":                                 "warn|dev saved and enabled with the override: this kubeconfig can write.",
		"saved dev: unknown; not enabled (could not be checked)":                "warn|dev saved, but it could not be checked, so the agent cannot use it yet.",
		"removed dev; backup dev.kubeconfig.20261103-120000":                    "ok|dev removed. The agent can no longer read it.",
		"refused: release never takes the override: production stays read-only": "fail|Not done: release never takes the override: production stays read-only.",
		"something else": "warn|Finished.",
	} {
		k, title, _ := kubeResultWords(res)
		if k+"|"+title != want {
			t.Fatalf("%q -> %s|%s", res, k, title)
		}
	}
}

func TestKubeSaveWritesOneRequest(t *testing.T) {
	e, ck, csrf := kubeSetup(t)
	var log bytes.Buffer
	Out = &log
	good := kubeYAML(kubeGoodUser)
	bad := []struct {
		form url.Values
		want string
	}{
		{url.Values{"name": {"Dev!"}, "env": {"develop"}, "kubeconfig": {good}}, "lowercase letters"},
		{url.Values{"name": {"dev"}, "env": {"prod"}, "kubeconfig": {good}}, "Choose the environment"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "context": {"a b"}, "kubeconfig": {good}}, "A context name uses"},
		{url.Values{"name": {"dev"}, "env": {"release"}, "override": {"1"}, "kubeconfig": {good}}, "Release never takes the override"},
		{url.Values{"name": {"dev"}, "env": {"develop"}}, "Paste the kubeconfig"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML("    exec:\n      command: /bin/sh\n")}}, "exec plugin"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML("    auth-provider:\n      name: oidc\n")}}, "auth-provider plugin"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML("    client-certificate: /root/c.crt\n    client-key: /root/c.key\n")}}, "client certificate file"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML("    tokenFile: /var/run/t\n")}}, "token file"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML(kubeGoodUser + "    as: system:admin\n")}}, "impersonates"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {`{"users": [{"name": "u", "user": {"exec": {}}}], "clusters": [], "contexts": []}`}}, "exec plugin"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {"hello: world\n"}}, "does not look like a kubeconfig"},
		{url.Values{"name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML("    token: " + strings.Repeat("t", kubeMaxBytes) + "\n")}}, "larger than 64 KB"},
	}
	for _, b := range bad {
		b.form.Set("csrf", csrf)
		b.form.Set("action", "save")
		r := e.req("POST", "/api/v1/admin/kube", ck, b.form, nil)
		if r.status != 400 || !strings.Contains(r.body, b.want) || strings.Contains(r.body, kubeFakeCred) || !strings.Contains(r.body, `"field":`) {
			t.Fatalf("%v: %d, want %q", b.form.Get("name"), r.status, b.want)
		}
	}
	if n := len(kubeRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written for rejected input", n)
	}
	r := e.req("POST", "/api/v1/admin/kube", ck, url.Values{"csrf": {csrf}, "action": {"save"}, "name": {"dev"}, "env": {"pre-release"},
		"override": {"1"}, "context": {"dev-ro"}, "kubeconfig": {good}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/admin/kube?kr=") {
		t.Fatalf("save: %d %s", r.status, r.loc)
	}
	reqs := kubeRequests(t)
	if len(reqs) != 1 {
		t.Fatalf("requests: %v", reqs)
	}
	st, _ := os.Stat(reqs[0])
	if st.Mode().Perm() != 0o600 || !strings.HasPrefix(filepath.Base(reqs[0]), "admin-") {
		t.Fatalf("request file mode %v", st.Mode())
	}
	var doc map[string]any
	b, _ := os.ReadFile(reqs[0])
	json.Unmarshal(b, &doc)
	if doc["kind"] != "save" || doc["name"] != "dev" || doc["env"] != "pre-release" || doc["context"] != "dev-ro" ||
		doc["override"] != true || doc["kubeconfig"] != good || doc["user"] != "admin" {
		t.Fatalf("request: %v %v %v", doc["kind"], doc["name"], doc["env"])
	}
	if strings.Contains(log.String(), kubeFakeCred) {
		t.Fatal("the kubeconfig reached the log")
	}
	// pending: the page refreshes, the form stays folded, the kubeconfig is nowhere on the page
	rid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(reqs[0]), "admin-"), ".json")
	os.Remove(reqs[0])
	kubeStatusFile(t, `{"results": {"`+rid+`": {"user": "admin", "result": "saved dev: writes; not enabled (not read-only)"}}}`)

	// test and remove: one request each, no kubeconfig
	for _, act := range []string{"test", "remove"} {
		if r := e.req("POST", "/api/v1/admin/kube", ck, url.Values{"csrf": {csrf}, "action": {act}, "name": {"dev"}}, nil); r.status != 200 {
			t.Fatalf("%s: %d", act, r.status)
		}
	}
	if r := e.req("POST", "/api/v1/admin/kube", ck, url.Values{"csrf": {csrf}, "action": {"test"}, "name": {"../x"}}, nil); r.status != 400 {
		t.Fatalf("bad name test: %d", r.status)
	}
	if r := e.req("POST", "/api/v1/admin/kube", ck, url.Values{"csrf": {csrf}, "action": {"wipe"}, "name": {"dev"}}, nil); r.status != 400 {
		t.Fatalf("unknown action: %d", r.status)
	}
	if n := len(kubeRequests(t)); n != 2 {
		t.Fatalf("test + remove: %d requests", n)
	}
}

func TestKubeNeeds2FAAndAdmin(t *testing.T) {
	e := srvSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "kube"), 0o755)
	if PolicyOf("/admin/kube", nil) != PolicySensitive {
		t.Fatal("/admin/kube must be sensitive")
	}
	form := func(csrf string) url.Values {
		return url.Values{"csrf": {csrf}, "action": {"save"}, "name": {"dev"}, "env": {"develop"}, "kubeconfig": {kubeYAML(kubeGoodUser)}}
	}
	tok, csrf := e.loginNoMFA("admin")
	if r := e.req("POST", "/api/v1/admin/kube", map[string]string{"opsr_session": tok}, form(csrf), nil); r.status != 403 {
		t.Fatalf("without 2FA: %d", r.status)
	}
	at, _ := e.login("admin")
	if r := e.req("POST", "/api/v1/admin/kube", map[string]string{"opsr_session": at}, form("wrong"), nil); r.status != 403 {
		t.Fatalf("bad CSRF: %d", r.status)
	}
	vt, vcsrf := e.login("view.er")
	vck := map[string]string{"opsr_session": vt}

	if r := e.req("POST", "/api/v1/admin/kube", vck, form(vcsrf), nil); r.status != 403 {
		t.Fatalf("viewer POST: %d", r.status)
	}
	if n := len(kubeRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written without 2FA, CSRF or the admin role", n)
	}
}
