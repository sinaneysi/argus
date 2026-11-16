package capability_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

func TestStageEvidence(t *testing.T) {
	dev := capability.ParseDevelopLabels("")
	for _, c := range []struct {
		labels []string
		ok     bool
	}{
		{[]string{"ops", "env:stage"}, true},
		{[]string{"ENV:Stage"}, true},
		{nil, false},
		{[]string{"ops"}, false},
		{[]string{"env:stage", "env:production"}, false}, // a disagreement
		{[]string{"env:production"}, false},
	} {
		if ok, why := capability.StageEvidence(c.labels, dev); ok != c.ok || why == "" {
			t.Errorf("%v: %v %q", c.labels, ok, why)
		}
	}
	jira := capability.ParseDevelopLabels(" Stage-Env , env:stage ")
	if ok, _ := capability.StageEvidence([]string{"stage-env"}, jira); !ok {
		t.Fatal("a configured label is not accepted")
	}
}

// ARGUS_ACT_CATALOGUE_DETAIL=aliases: the model sees no host, project or realm; Go maps every alias back.
func TestCatalogueAliasesRoundTrip(t *testing.T) {
	reg, err := capability.Default(capability.Options{
		HTTPHosts: []string{"health.example.com", "internal:*.svc.example.com:8443"},
		GitLab:    capability.GitLabOptions{URL: "https://git.example.com", Projects: []string{"group/app"}},
		Keycloak:  capability.KeycloakOptions{URL: "https://id.example.com", Realms: []string{"lab"}, ClientID: "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	full, none := reg.CatalogueDetail(capability.DetailFull)
	if none != nil || len(full) != 5 {
		t.Fatal("full detail must keep the values and give no aliases")
	}
	cat, al := reg.CatalogueDetail(capability.DetailAliases)
	b, _ := json.Marshal(cat)
	for _, real := range []string{"health.example.com", "svc.example.com", "group/app", `"lab"`} {
		if strings.Contains(string(b), real) {
			t.Fatalf("the aliased catalogue holds %s", real)
		}
	}
	again, _ := reg.CatalogueDetail(capability.DetailAliases)
	b2, _ := json.Marshal(again)
	if string(b) != string(b2) {
		t.Fatal("aliases are not stable")
	}
	alias := map[string][]string{}
	for _, e := range cat {
		for k, v := range e.AllowLists {
			alias[k] = v
		}
	}
	host, wild := alias["ARGUS_ACT_HTTP_HOSTS"][0], alias["ARGUS_ACT_HTTP_HOSTS"][1]
	wildSuffix := strings.TrimSuffix(strings.TrimPrefix(wild, "internal:*"), ":8443")
	got := al.Resolve(map[string]any{"url": "https://" + host + "/healthz", "expect_status": 200})
	if got["url"] != "https://health.example.com/healthz" || got["expect_status"] != 200 {
		t.Fatal(got)
	}
	if got := al.Resolve(map[string]any{"url": "https://api" + wildSuffix + ":8443/ready"}); got["url"] != "https://api.svc.example.com:8443/ready" {
		t.Fatal(got)
	}
	p := al.Resolve(map[string]any{"project": alias["ARGUS_ACT_GITLAB_PROJECTS"][0], "key": "K", "environment_scope": "develop"})
	if p["project"] != "group/app" || p["key"] != "K" {
		t.Fatal(p)
	}
	set, _ := reg.Get("gitlab.ci-variable.set")
	args := capability.Args(al.Resolve(map[string]any{"project": alias["ARGUS_ACT_GITLAB_PROJECTS"][0], "key": "K", "environment_scope": "develop", "value_ref": "input:NEW_K"}))
	if err := set.Validate(args, capability.Develop); err != nil {
		t.Fatal("the resolved arguments do not validate:", err)
	}
	if r := al.Resolve(map[string]any{"realm": alias["ARGUS_ACT_KEYCLOAK_REALMS"][0]}); r["realm"] != "lab" {
		t.Fatal(r)
	}
	// a name that is no alias stays as written, and Validate refuses it
	if r := al.Resolve(map[string]any{"url": "https://other.example.net/"}); r["url"] != "https://other.example.net/" {
		t.Fatal(r)
	}
	var nilAl *capability.Aliases
	if r := nilAl.Resolve(map[string]any{"a": "b"}); r["a"] != "b" {
		t.Fatal("a nil Aliases must keep the arguments")
	}
}

func TestStatusSignature(t *testing.T) {
	pub, k, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte(`{"ts":1}`)
	sig := capability.SignStatus(k, body)
	if capability.VerifyStatus(pub, body, sig) != nil || capability.VerifyStatus(pub, []byte(`{"ts":2}`), sig) == nil ||
		capability.VerifyStatus(nil, body, sig) == nil || capability.VerifyStatus(pub, body, "") == nil {
		t.Fatal("status signature rules")
	}
	// a status signature is not an approval signature over the same bytes
	if capability.VerifyStatus(pub, body, base64.StdEncoding.EncodeToString(ed25519.Sign(k, body))) == nil {
		t.Fatal("a signature without the status domain verified")
	}
}
