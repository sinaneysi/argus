package actd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/capability/keycloaktest"
)

// TestKeycloakReadThroughActd: keycloak.read runs as a read-tier step for a release task (O8) with the client secret
// from argus-act; the audit has the query; the secret, the token and the fields the stub returns but the capability
// drops are nowhere actd writes, nor in its log.
func TestKeycloakReadThroughActd(t *testing.T) {
	f := newFx(t)
	stub := keycloaktest.New(t)
	if err := SetCredential(f.act, f.key, capability.KeycloakCredential, keycloaktest.Secret); err != nil {
		t.Fatal(err)
	}
	kc, err := capability.NewKeycloak(capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"}, ClientID: keycloaktest.ClientID,
		AllowPlain: true, Transport: stub.Transport(),
		Secret: func() (string, error) { return OpenCredential(f.act, f.key, capability.KeycloakCredential) }})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := f.reg.Get(capability.HTTPCheckName)
	if f.reg, err = capability.NewRegistry(h, kc); err != nil {
		t.Fatal(err)
	}
	d := f.daemon()
	step := map[string]any{"mode": "read-only", "text": "who is user02", "capability": capability.KeycloakRead,
		"args": map[string]any{"realm": "lab", "query": "user", "search": "user02"}}
	req := f.task("KC-1", "release", []map[string]any{step})
	r := f.submit(d, req)
	if r.State != StateDone || len(r.Steps) != 1 || r.Steps[0].Status != "ok" || r.Steps[0].Fields["exists"] != true {
		t.Fatalf("%+v", r)
	}
	var got []string
	for _, l := range f.auditLines() {
		got = append(got, l.Capability+":"+l.Phase)
	}
	if s := strings.Join(got, ","); !strings.Contains(s, "keycloak.read:apply") {
		t.Fatal("audit:", s)
	}
	if b, _ := os.ReadFile(filepath.Join(f.act, "audit", "actions.jsonl")); len(b) == 0 || strings.Contains(string(b), "user02") {
		t.Fatal("the search text is in the audit, or no audit")
	}
	d.WriteStatus()
	for _, m := range []string{keycloaktest.Secret, "kc-access-", keycloaktest.Email, keycloaktest.Attribute, keycloaktest.Credential,
		keycloaktest.FirstName, keycloaktest.LastName} {
		for _, dir := range []string{f.act, f.actions, filepath.Dir(f.ops)} {
			if p := dirHas(t, dir, m); p != "" {
				t.Fatalf("%q is in %s", m, p)
			}
		}
		if strings.Contains(f.logs.String(), m) {
			t.Fatalf("%q is in the log", m)
		}
	}
	for _, c := range stub.Calls() {
		if c.Method != "GET" && !strings.HasSuffix(c.Path, "/realms/lab/protocol/openid-connect/token") {
			t.Fatal("not read-only:", c)
		}
	}
}
