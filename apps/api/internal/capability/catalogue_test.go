package capability_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/capability"
)

func TestCatalogueFromRegistryAndAllowLists(t *testing.T) {
	var none *capability.Registry
	if c := none.Catalogue(); c == nil || len(c) != 0 {
		t.Fatalf("a nil registry must give an empty catalogue: %v", c)
	}
	reg, err := capability.Default(capability.Options{
		HTTPHosts: []string{"health.example.com", "internal:*.svc.example.com:8443"},
		GitLab:    capability.GitLabOptions{URL: "https://git.example.com", Projects: []string{"group/app", "42"}},
		Keycloak:  capability.KeycloakOptions{URL: "https://id.example.com", Realms: []string{"lab"}, ClientID: "client-xyz"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cat := reg.Catalogue()
	byName := map[string]capability.CatalogueEntry{}
	for _, e := range cat {
		byName[e.Name] = e
	}
	if len(cat) != 5 {
		t.Fatalf("want 5 capabilities, got %d", len(cat))
	}
	set := byName["gitlab.ci-variable.set"]
	if set.Tier != "write" || strings.Join(set.Environments, ",") != "develop" {
		t.Fatalf("set: %+v", set)
	}
	if got := strings.Join(set.AllowLists["ARGUS_ACT_GITLAB_PROJECTS"], ","); got != "42,group/app" {
		t.Fatalf("projects: %q", got)
	}
	var vref capability.CatalogueField
	for _, f := range set.Fields {
		if f.Name == "value_ref" {
			vref = f
		}
	}
	if !vref.Input || !vref.Secret || !vref.Required {
		t.Fatalf("value_ref must be a required secret input: %+v", vref)
	}
	if !strings.Contains(strings.Join(set.Rules, " "), `exactly "develop"`) {
		t.Fatal("the scope rule is missing")
	}
	hc := byName["http.check"]
	if hc.Tier != "read" || len(hc.Environments) != 4 {
		t.Fatalf("http.check: %+v", hc)
	}
	if got := strings.Join(hc.AllowLists["ARGUS_ACT_HTTP_HOSTS"], ","); got != "health.example.com,internal:*.svc.example.com:8443" {
		t.Fatalf("hosts: %q", got)
	}
	if got := strings.Join(byName["keycloak.read"].AllowLists["ARGUS_ACT_KEYCLOAK_REALMS"], ","); got != "lab" {
		t.Fatalf("realms: %q", got)
	}
	b, _ := json.Marshal(cat)
	for _, bad := range []string{"client-xyz", "id.example.com", "git.example.com"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("the catalogue holds %q", bad)
		}
	}
	// expect_status carries its range
	for _, f := range hc.Fields {
		if f.Name == "expect_status" && (f.Min == nil || *f.Min != 100 || *f.Max != 599) {
			t.Fatalf("expect_status range: %+v", f)
		}
	}
}
