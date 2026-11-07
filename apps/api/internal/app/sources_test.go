package app

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/demo"
)

// Obviously fake material: no real host or credential (Northwind is a neutral example name).
const srcFakeCred = "fake-source-cred-xxxxxxxxxxxxxxxxxxxxxxxx"

func srcSetup(t *testing.T) (*srvEnv, map[string]string, string) {
	e := srvSetup(t)
	tok, csrf := e.login("admin")
	os.MkdirAll(filepath.Join(SET_IN, "sources"), 0o755)
	return e, map[string]string{"opsr_session": tok}, csrf
}

func srcRequests(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(SET_IN, "sources", "*.json"))
	return m
}

func srcStatusFile(t *testing.T, doc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(SET_OUT, "sources-status.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

const srcStatusDoc = `{"at": 1700000000, "sources": {
 "gitlab/main": {"type": "gitlab", "name": "main", "env": "shared", "url": "https://git.northwind.example", "purpose": "agent",
   "status": "readonly", "detail": "read-only: scopes read_api, read_repository", "identity": "nw-ro", "managed": "legacy",
   "cred_where": "the gitlab-ro-token secret in ARGUS_SECRETS_DIR", "manage": "operator", "enabled": true, "last_check": 1700000000},
 "gitlab/tracker": {"type": "gitlab", "name": "tracker", "env": "shared", "url": "https://git.northwind.example", "purpose": "tracker",
   "status": "ok", "detail": "works as the issue tracker (scope api)", "managed": "legacy", "manage": "/admin/outline",
   "cred_where": "Settings → Integrations → GitLab issues", "last_check": 1700000000},
 "grafana/east": {"type": "grafana", "name": "east", "env": "release", "url": "https://grafana-east.northwind.example", "purpose": "agent",
   "status": "refused", "detail": "this login can write (role Editor); the agent needs a read-only one", "findings": ["role Editor"],
   "managed": "registry", "cred_last4": "wxyz", "last_check": 1700000000},
 "grafana/west": {"type": "grafana", "name": "west", "env": "develop", "url": "https://grafana-west.northwind.example", "purpose": "agent",
   "status": "unreachable", "detail": "no answer within 10 s", "managed": "registry"},
 "sentry/errors": {"type": "sentry", "name": "errors", "env": "release", "url": "https://sentry.northwind.example", "purpose": "agent",
   "status": "failed", "detail": "the login was refused (HTTP 401); replace the credential", "managed": "registry"},
 "kubernetes/dev": {"type": "kubernetes", "name": "dev", "env": "develop", "url": "https://192.0.2.10:6443", "purpose": "agent",
   "status": "readonly", "detail": "read-only: get, list and watch", "managed": "legacy", "manage": "/admin/kube", "enabled": true},
 "bogus/x": {"type": "bogus"}, "gitlab/Bad Name": {"type": "gitlab"}}}`

func TestSourcesVerdictAndResults(t *testing.T) {
	ok := srcFacts{Known: true, Entries: []srcEntry{{Key: "gitlab/main", Status: "readonly", Purpose: "agent", Enabled: true}}}
	if v := srcVerdictOf(ok); v.Kind != "ok" || !strings.Contains(v.Head, "1 data source;") {
		t.Fatalf("ok: %+v", v)
	}
	ov := srcFacts{Known: true, Entries: []srcEntry{{Key: "grafana/a", Type: "grafana", Name: "a", Purpose: "agent", Status: "refused", Override: true, Enabled: true}}}
	if v := srcVerdictOf(ov); v.Kind != "warn" || v.Badge != "Override" || v.Href != "#src-grafana-a" {
		t.Fatalf("override: %+v", v)
	}
	un := srcFacts{Known: true, Entries: []srcEntry{{Key: "gitlab/main", Status: "readonly", Purpose: "agent", Enabled: true}, {Key: "sentry/x", Status: "unchecked"}}}
	if v := srcVerdictOf(un); v.Badge != "Not checked" || !strings.Contains(v.Head, "1 of 2 data sources are not checked yet.") {
		t.Fatalf("unchecked: %+v", v)
	}
	all := srcFacts{Known: true, Entries: []srcEntry{{Key: "sentry/x", Type: "sentry", Name: "x", Status: "failed"}}}
	if v := srcVerdictOf(all); v.Kind != "fail" || v.Badge != "Not working" {
		t.Fatalf("all failing: %+v", v)
	}
	for res, want := range map[string]string{
		"saved grafana/east: readonly (read-only: role Viewer)":              "ok|grafana/east saved: read-only. The agent can use it.",
		"tested gitlab/tracker: ok (works as the issue tracker (scope api))": "ok|gitlab/tracker checked: it works.",
		"saved grafana/east: refused (this login can write (role Editor))":   "fail|grafana/east saved, but refused for the agent.",
		"tested sentry/errors: failed (the login was refused (HTTP 401))":    "fail|sentry/errors checked, but the login failed.",
		"saved grafana/west: unreachable (no answer within 10 s)":            "warn|grafana/west saved, but Argus could not reach it.",
		"removed grafana/west; backup grafana-west.cred.20261103-120000":     "ok|grafana/west removed. Argus no longer reads it.",
		"checked 4 sources": "ok|Checked 4 data sources.",
		"refused: release and shared sources never take the override": "fail|Not done: release and shared sources never take the override.",
		"something else": "warn|Finished.",
	} {
		k, title, _ := srcResultWords(res)
		if k+"|"+title != want {
			t.Fatalf("%q -> %s|%s", res, k, title)
		}
	}
}

func TestSourcesSaveWritesOneRequest(t *testing.T) {
	e, ck, csrf := srcSetup(t)
	var log bytes.Buffer
	Out = &log
	base := func(kv ...string) url.Values {
		v := url.Values{"action": {"save"}, "mode": {"add"}, "type": {"grafana"}, "name": {"east"}, "env": {"develop"},
			"url": {"https://grafana.northwind.example"}, "credential": {srcFakeCred}}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == "" {
				v.Del(kv[i])
			} else {
				v.Set(kv[i], kv[i+1])
			}
		}
		return v
	}
	bad := []struct {
		form url.Values
		want string
	}{
		{base("name", "East!"), "lowercase letters"},
		{base("env", "prod"), "Choose the environment"},
		{base("type", "gitlab", "purpose", "docs"), "Choose what Argus uses it for"},
		{base("type", "gitlab", "purpose", "tracker", "env", "release"), "Release data sources are for the agent only"},
		{base("env", "release", "override", "1"), "Only develop and pre-release take the override"},
		{base("env", "shared", "override", "1"), "Only develop and pre-release take the override"},
		{base("url", "http://grafana.northwind.example"), "must start with https://"},
		{base("url", "https://u"+":p"+"@grafana.northwind.example"), "without a user"},
		{base("url", "https://grafana.northwind.example/?x=1"), "without a user, query"},
		{base("url", ""), "Enter the address"},
		{base("credential", ""), "Paste the service-account token"},
		{base("credential", "a\nb"), "line breaks"},
		{base("type", "elasticsearch", "username", "bad user"), "A username uses"},
	}
	for _, b := range bad {
		b.form.Set("csrf", csrf)
		r := e.req("POST", "/api/v1/admin/sources", ck, b.form, nil)
		if r.status != 400 || !strings.Contains(r.body, b.want) || strings.Contains(r.body, srcFakeCred) || !strings.Contains(r.body, `"field":`) {
			t.Fatalf("%v: %d, want %q", b.form, r.status, b.want)
		}
	}
	for _, f := range []url.Values{base("type", "kubernetes"), base("type", "nope"), base("mode", "replace"),
		base("username", "elastic"), base("purpose", "tracker")} {
		f.Set("csrf", csrf)
		if r := e.req("POST", "/api/v1/admin/sources", ck, f, nil); r.status != 400 {
			t.Fatalf("%v: %d", f, r.status)
		}
	}
	if n := len(srcRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written for rejected input", n)
	}
	f := base("env", "pre-release", "override", "1")
	f.Set("csrf", csrf)
	r := e.req("POST", "/api/v1/admin/sources", ck, f, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/admin/sources?sr=") {
		t.Fatalf("save: %d %s", r.status, r.loc)
	}
	reqs := srcRequests(t)
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
	if doc["kind"] != "save" || doc["mode"] != "add" || doc["type"] != "grafana" || doc["name"] != "east" || doc["env"] != "pre-release" ||
		doc["purpose"] != "agent" || doc["override"] != true || doc["credential"] != srcFakeCred || doc["user"] != "admin" ||
		doc["url"] != "https://grafana.northwind.example" {
		t.Fatalf("request: %v", doc["kind"])
	}
	if strings.Contains(log.String(), srcFakeCred) {
		t.Fatal("the credential reached the log")
	}
	// pending: the page refreshes, no form opens, the credential is nowhere
	rid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(reqs[0]), "admin-"), ".json")
	os.Remove(reqs[0])
	srcStatusFile(t, `{"results": {"`+rid+`": {"user": "admin", "result": "saved grafana/east: refused (this login can write (role Editor))"}}}`)

	// edit without a credential keeps the stored one (root decides); test, remove, check-all: one request each, no credential
	f = base("mode", "edit", "credential", "")
	f.Set("csrf", csrf)
	if r := e.req("POST", "/api/v1/admin/sources", ck, f, nil); r.status != 200 {
		t.Fatalf("edit: %d", r.status)
	}
	for _, act := range []string{"test", "remove", "check-all"} {
		if r := e.req("POST", "/api/v1/admin/sources", ck, url.Values{"csrf": {csrf}, "action": {act}, "type": {"grafana"}, "name": {"east"}}, nil); r.status != 200 {
			t.Fatalf("%s: %d", act, r.status)
		}
	}
	if r := e.req("POST", "/api/v1/admin/sources", ck, url.Values{"csrf": {csrf}, "action": {"test"}, "type": {"grafana"}, "name": {"../x"}}, nil); r.status != 400 {
		t.Fatalf("bad name: %d", r.status)
	}
	if r := e.req("POST", "/api/v1/admin/sources", ck, url.Values{"csrf": {csrf}, "action": {"wipe"}, "type": {"grafana"}, "name": {"east"}}, nil); r.status != 400 {
		t.Fatalf("unknown action: %d", r.status)
	}
	if n := len(srcRequests(t)); n != 4 {
		t.Fatalf("edit + test + remove + check-all: %d requests", n)
	}
	for _, p := range srcRequests(t) {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), srcFakeCred) {
			t.Fatal("a request without a pasted credential carries one")
		}
	}
}

func TestSourcesNeeds2FAAndAdmin(t *testing.T) {
	e := srvSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "sources"), 0o755)
	if PolicyOf("/admin/sources", nil) != PolicySensitive {
		t.Fatal("/admin/sources must be sensitive")
	}
	form := func(csrf string) url.Values {
		return url.Values{"csrf": {csrf}, "action": {"save"}, "mode": {"add"}, "type": {"sentry"}, "name": {"errors"}, "env": {"develop"},
			"url": {"https://sentry.northwind.example"}, "credential": {srcFakeCred}}
	}
	tok, csrf := e.loginNoMFA("admin")
	if r := e.req("POST", "/api/v1/admin/sources", map[string]string{"opsr_session": tok}, form(csrf), nil); r.status != 403 {
		t.Fatalf("without 2FA: %d", r.status)
	}
	at, _ := e.login("admin")
	if r := e.req("POST", "/api/v1/admin/sources", map[string]string{"opsr_session": at}, form("wrong"), nil); r.status != 403 {
		t.Fatalf("bad CSRF: %d", r.status)
	}
	vt, vcsrf := e.login("view.er")
	vck := map[string]string{"opsr_session": vt}

	if r := e.req("POST", "/api/v1/admin/sources", vck, form(vcsrf), nil); r.status != 403 {
		t.Fatalf("viewer POST: %d", r.status)
	}
	if n := len(srcRequests(t)); n != 0 {
		t.Fatalf("%d request(s) written without 2FA, CSRF or the admin role", n)
	}
}

// The demo's Settings -> Data sources: the generated status renders every state, and every write is refused.
func TestSourcesDemo(t *testing.T) {
	e, ck, csrf := srcSetup(t)
	gen := t.TempDir()
	if err := demo.Generate(filepath.Join(gen, "data"), filepath.Join(gen, "auth"), 1793318400, demo.DefaultSeed); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(gen, "data", "settings-out", "sources-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	srcStatusFile(t, string(b))

	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	r := e.req("POST", "/api/v1/admin/sources", ck, url.Values{"csrf": {csrf}, "action": {"check-all"}}, nil)
	if r.status != 403 || !strings.Contains(r.body, "Disabled in the demo") || len(srcRequests(t)) != 0 {
		t.Fatalf("demo POST: %d", r.status)
	}
}
