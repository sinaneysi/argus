package sources

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/secret"
)

func TestOpsTypes(t *testing.T) {
	c, f := check(t, "alertmanager", map[string]route{"GET /api/v2/alerts": {200, L{M{"labels": M{"alertname": "X"}}}}}, "alerts", "")
	if c.Status != "ok" || c.Facts["api"] != "alertmanager" || !strings.HasPrefix(f.seen[0], "Bearer ") {
		t.Fatalf("alertmanager: %+v %v", c, f.seen)
	}
	c, _ = check(t, "alertmanager", map[string]route{"GET /api/v1/alerts": {200, M{"status": "success", "data": M{"alerts": L{}}}}}, "alerts", "")
	if c.Status != "ok" || c.Facts["api"] != "prometheus" || AlertsPath(&Entry{Facts: c.Facts}) != "/api/v1/alerts" {
		t.Fatalf("prometheus: %+v", c)
	}
	c, _ = check(t, "alertmanager", map[string]route{}, "alerts", "")
	if c.Status != "unreachable" {
		t.Fatalf("neither: %+v", c)
	}
	c, f = check(t, "jira", map[string]route{"GET /rest/api/2/myself": {200, M{"name": "nw-bot", "active": true}}}, "tracker", "nw-bot")
	if c.Status != "ok" || c.Identity != "nw-bot" || !strings.HasPrefix(f.seen[0], "Basic ") {
		t.Fatalf("jira: %+v %v", c, f.seen)
	}
	c, _ = check(t, "jira", map[string]route{"GET /rest/api/2/myself": {401, M{}}}, "tracker", "")
	if c.Status != "failed" {
		t.Fatalf("jira refused: %+v", c)
	}
	c, _ = check(t, "matrix", map[string]route{"GET /_matrix/client/v3/account/whoami": {200, M{"user_id": "@nw:chat.example.test"}}}, "notify", "")
	if c.Status != "ok" || c.Identity != "@nw:chat.example.test" {
		t.Fatalf("matrix: %+v", c)
	}
}

// an Alertmanager without a login is saved without a credential and checked
func TestAlertmanagerWithoutCredential(t *testing.T) {
	e := setup(t)
	f := serve(t, map[string]route{"GET /api/v2/alerts": {200, L{}}})
	rid := e.request("admin", M{"kind": "save", "mode": "add", "type": "alertmanager", "name": "main", "env": "shared", "url": f.url, "purpose": "alerts"})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved alertmanager/main: ok") {
		t.Fatal(r)
	}
	if f.seen[0] != "" {
		t.Fatalf("sent a header without a credential: %q", f.seen[0])
	}
	rid = e.request("admin", M{"kind": "save", "mode": "add", "type": "matrix", "name": "main", "env": "shared", "url": f.url, "purpose": "notify"})
	e.apply()
	if r := e.result(rid); !strings.Contains(r, "paste the token") {
		t.Fatal(r)
	}
}

func TestAllowHTTP(t *testing.T) {
	if _, err := CleanURL("http://stubs:8099/x"); err == nil {
		t.Fatal("http allowed by default")
	}
	AllowHTTP = true
	defer func() { AllowHTTP = false }()
	if u, err := CleanURL("http://stubs:8099/x/"); err != nil || u != "http://stubs:8099/x" {
		t.Fatal(u, err)
	}
}

// with a master key the stored credential is sealed, and the check still sends the real one
func TestCredentialSealed(t *testing.T) {
	e := setup(t)
	k, _ := secret.ParseKey(secret.NewKeyText())
	e.o.Key = k
	f := serve(t, map[string]route{"GET /_matrix/client/v3/account/whoami": {200, M{"user_id": "@nw:chat.example.test"}}})
	rid := e.request("admin", M{"kind": "save", "mode": "add", "type": "matrix", "name": "main", "env": "shared", "url": f.url, "purpose": "notify", "credential": cred})
	e.apply()
	if r := e.result(rid); !strings.HasPrefix(r, "saved matrix/main: ok") {
		t.Fatal(r)
	}
	raw := read(t, e.o.credPath("matrix", "main"))
	if !secret.Sealed(raw) || strings.Contains(raw, cred) || f.seen[0] != "Bearer "+cred {
		t.Fatalf("stored %q, sent %q", raw, f.seen[0])
	}
	if strings.Contains(e.everything(), cred) {
		t.Fatal("credential leaked into a status or log")
	}
}
