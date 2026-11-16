package capability_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/capability"
	"github.com/sinaneysi/argus/internal/capability/keycloaktest"
)

type kcFx struct {
	stub *keycloaktest.Stub
	kc   *capability.Keycloak
}

func newKC(t *testing.T) *kcFx {
	t.Helper()
	f := &kcFx{stub: keycloaktest.New(t)}
	reg, err := capability.Default(capability.Options{Keycloak: capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"},
		ClientID: keycloaktest.ClientID, AllowPlain: true, Secret: func() (string, error) { return keycloaktest.Secret, nil },
		Transport: f.stub.Transport()}})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Get(capability.KeycloakRead)
	if !ok {
		t.Fatal("keycloak.read not registered")
	}
	f.kc = c.(*capability.Keycloak)
	return f
}

func kcArgs(query, search string) capability.Args {
	a := capability.Args{"realm": "lab", "query": query}
	if search != "" {
		a["search"] = search
	}
	return a
}

// kcNoLeak fails when a marker the stub returns (e-mail, attributes, credentials, names, the bind credential), the
// client secret or an access token is in any of the things.
func kcNoLeak(t *testing.T, things ...any) {
	t.Helper()
	for _, x := range things {
		b, _ := json.Marshal(x)
		if e, ok := x.(error); ok && e != nil {
			b = []byte(e.Error())
		}
		for _, m := range []string{keycloaktest.Email, "@example.com", keycloaktest.Attribute, keycloaktest.Credential, keycloaktest.Bind,
			keycloaktest.FirstName, keycloaktest.LastName, keycloaktest.Secret, "kc-access-", "LDAP_ENTRY_DN", "secretData", "bindCredential", "attributes"} {
			if strings.Contains(string(b), m) {
				t.Fatalf("%q leaked: %s", m, b)
			}
		}
	}
}

func keys(m map[string]any) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Empty ARGUS_ACT_KEYCLOAK_URL: not in the catalogue. A URL without realms or client id is a configuration error.
func TestKeycloakConfig(t *testing.T) {
	reg, err := capability.Default(capability.Options{Keycloak: capability.KeycloakOptions{Realms: []string{"lab"}, ClientID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get(capability.KeycloakRead); ok {
		t.Fatal("offered without a URL")
	}
	for _, o := range []capability.KeycloakOptions{
		{URL: "https://id.example.com", ClientID: "x"},
		{URL: "https://id.example.com", Realms: []string{"lab"}},
		{URL: "https://id.example.com", Realms: []string{"lab", "other"}, ClientID: "x"}, // several realms: the client's realm is needed
		{URL: "http://id.example.com", Realms: []string{"lab"}, ClientID: "x"},
		{URL: "https://" + "u" + ":" + "p" + "@id.example.com", Realms: []string{"lab"}, ClientID: "x"},
		{URL: "https://id.example.com", Realms: []string{"../x"}, ClientID: "x"},
		{URL: "https://id.example.com", Realms: []string{"lab"}, ClientID: "x", ClientRealm: "a/b"},
	} {
		if _, err := capability.NewKeycloak(o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := capability.NewKeycloak(capability.KeycloakOptions{URL: "https://id.example.com/auth", Realms: []string{"lab", "other"}, ClientID: "x", ClientRealm: "master"}); err != nil {
		t.Fatal(err)
	}
}

// A realm not on the list is refused before any call, by Validate, by Apply and by the guard.
func TestKeycloakRealmNotOnList(t *testing.T) {
	f := newKC(t)
	a := capability.Args{"realm": "other", "query": "users", "search": "user"}
	var r *capability.Refusal
	if err := f.kc.Validate(a, capability.Develop); !errors.As(err, &r) || r.Code != capability.CodeAllowList {
		t.Fatal(err)
	}
	if _, _, err := f.kc.Apply(context.Background(), a, capability.Plan{}); !errors.As(err, &r) {
		t.Fatal(err)
	}
	if n := len(f.stub.Calls()); n != 0 {
		t.Fatalf("%d calls", n)
	}
}

// Read tier: every known environment (O8); an unknown one is refused.
func TestKeycloakEveryEnvironment(t *testing.T) {
	f := newKC(t)
	for _, e := range []capability.Env{capability.Develop, capability.PreRelease, capability.Release, capability.None} {
		if err := f.kc.Validate(kcArgs("users", "user"), e); err != nil {
			t.Fatal(e, err)
		}
	}
	if err := f.kc.Validate(kcArgs("users", "user"), capability.Env("prod")); err == nil {
		t.Fatal("unknown env accepted")
	}
}

// Every query returns its fixed fields only; e-mail, attributes, credentials, names and the bind credential the
// stub returns never pass.
func TestKeycloakFixedFields(t *testing.T) {
	f := newKC(t)
	ctx := context.Background()

	r, _, err := f.kc.Apply(ctx, kcArgs("users", "user"), capability.Plan{})
	if err != nil || r.Status != "ok" || keys(r.Fields) != "count,query,realm,truncated,users" {
		t.Fatal(r, err)
	}
	rows := r.Fields["users"].([]map[string]any)
	if len(rows) != 3 || keys(rows[0]) != "enabled,federation_link,first_name_set,last_name_set,username" || rows[0]["first_name_set"] != true || rows[1]["last_name_set"] != false {
		t.Fatalf("%+v", rows)
	}
	kcNoLeak(t, r)

	r, _, err = f.kc.Apply(ctx, kcArgs("user", "user02"), capability.Plan{})
	if err != nil || keys(r.Fields) != "exists,query,realm,user" || r.Fields["exists"] != true {
		t.Fatal(r, err)
	}
	u := r.Fields["user"].(map[string]any)
	if keys(u) != "enabled,federation_link,first_name_set,groups,groups_truncated,last_name_set,required_actions,username" ||
		strings.Join(u["groups"].([]string), ",") != "developers,ops" || u["username"] != "user02" {
		t.Fatalf("%+v", u)
	}
	kcNoLeak(t, r)
	if r, _, err := f.kc.Apply(ctx, kcArgs("user", "nobody"), capability.Plan{}); err != nil || r.Fields["exists"] != false {
		t.Fatal(r, err)
	}

	r, _, err = f.kc.Apply(ctx, kcArgs("ldap-mappers", ""), capability.Plan{})
	if err != nil || keys(r.Fields) != "count,mappers,query,realm,truncated" {
		t.Fatal(r, err)
	}
	ms := r.Fields["mappers"].([]map[string]any)
	if len(ms) != 2 || keys(ms[0]) != "config,name,provider,type" {
		t.Fatalf("%+v", ms)
	}
	cfg := ms[0]["config"].(map[string]string) // groups (sorted by name)
	if ms[0]["name"] != "groups" || cfg["groups.dn"] == "" || cfg["mode"] != "READ_ONLY" || len(cfg) != 2 {
		t.Fatalf("%+v", cfg)
	}
	if c := ms[1]["config"].(map[string]string); c["ldap.attribute"] != "uid" || len(c) != 3 {
		t.Fatalf("%+v", c)
	}
	kcNoLeak(t, r)

	r, _, err = f.kc.Apply(ctx, kcArgs("client-scopes", "prof"), capability.Plan{})
	if err != nil || keys(r.Fields) != "count,query,realm,scopes,truncated" {
		t.Fatal(r, err)
	}
	ss := r.Fields["scopes"].([]map[string]any)
	if len(ss) != 1 || keys(ss[0]) != "mappers,name,protocol" || strings.Join(ss[0]["mappers"].([]string), ",") != "family name,given name" {
		t.Fatalf("%+v", ss)
	}
	kcNoLeak(t, r)
}

// The search is capped at 100 characters and is required for users and user.
func TestKeycloakSearchCap(t *testing.T) {
	f := newKC(t)
	if err := f.kc.Validate(kcArgs("users", strings.Repeat("a", 100)), capability.Develop); err != nil {
		t.Fatal(err)
	}
	for _, a := range []capability.Args{kcArgs("users", strings.Repeat("a", 101)), kcArgs("users", ""), kcArgs("user", ""),
		kcArgs("users", "a/b"), kcArgs("roles", "x"), kcArgs("users", "*"), kcArgs("users", "**"), kcArgs("users", `"*"`),
		kcArgs("users", " * "), kcArgs("user", "*"), kcArgs("client-scopes", "*"), {"realm": "lab", "query": "users", "search": "x", "max": 500}} {
		if err := f.kc.Validate(a, capability.Develop); err == nil {
			t.Fatalf("accepted %v", a)
		}
	}
}

// At most 50 rows, whatever Keycloak returns.
func TestKeycloakRowCap(t *testing.T) {
	f := newKC(t)
	f.stub.Users = 60
	r, _, err := f.kc.Apply(context.Background(), kcArgs("users", "user"), capability.Plan{})
	if err != nil || len(r.Fields["users"].([]map[string]any)) != capability.KeycloakMaxRows || r.Fields["truncated"] != true || r.Fields["count"] != 50 {
		t.Fatal(r.Fields["count"], r.Fields["truncated"], err)
	}
	for _, c := range f.stub.Calls() {
		if strings.Contains(c.Path, "/users") && !strings.Contains(c.Query, "max=51") {
			t.Fatal("no max:", c.Query)
		}
	}
}

// The token is cached until 30 s before it expires, and is never in a result, a plan or an error.
func TestKeycloakTokenCache(t *testing.T) {
	f := newKC(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	capability.SetKeycloakClock(f.kc, func() time.Time { return now })
	ctx := context.Background()
	run := func() capability.Result {
		r, _, err := f.kc.Apply(ctx, kcArgs("users", "user"), capability.Plan{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1, r2 := run(), run()
	if f.stub.TokenCalls() != 1 {
		t.Fatal("tokens:", f.stub.TokenCalls())
	}
	now = now.Add(300*time.Second - 31*time.Second)
	run()
	if f.stub.TokenCalls() != 1 {
		t.Fatal("refreshed too early")
	}
	now = now.Add(2 * time.Second) // 29 s before expiry
	r3 := run()
	if f.stub.TokenCalls() != 2 {
		t.Fatal("not refreshed 30 s before expiry")
	}
	p, _ := f.kc.Plan(ctx, kcArgs("users", "user"))
	kcNoLeak(t, r1, r2, r3, p)
	// the token went only to the token endpoint and as a bearer header, never in a query
	for _, c := range f.stub.Calls() {
		if strings.Contains(c.Query, "kc-access-") || strings.Contains(c.Query, keycloaktest.Secret) {
			t.Fatal("credential in a query:", c.Query)
		}
	}
}

// A wrong secret: the error says so and holds neither the secret nor Keycloak's answer body.
func TestKeycloakBadSecret(t *testing.T) {
	f := newKC(t)
	reg, _ := capability.Default(capability.Options{Keycloak: capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"},
		ClientID: keycloaktest.ClientID, AllowPlain: true, Secret: func() (string, error) { return "wrong-" + keycloaktest.Secret, nil },
		Transport: f.stub.Transport()}})
	c, _ := reg.Get(capability.KeycloakRead)
	r, _, err := c.Apply(context.Background(), kcArgs("users", "user"), capability.Plan{})
	if err == nil || r.Status != "failed" || !strings.Contains(err.Error(), "401") {
		t.Fatal(r, err)
	}
	kcNoLeak(t, r, err)
	if strings.Contains(err.Error(), "wrong-") || strings.Contains(r.Detail, "wrong-") {
		t.Fatal("secret in the error")
	}
	// the worker has no secret: no call at all
	reg, _ = capability.Default(capability.Options{Keycloak: capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"},
		ClientID: keycloaktest.ClientID, AllowPlain: true, Transport: f.stub.Transport()}})
	c, _ = reg.Get(capability.KeycloakRead)
	n := len(f.stub.Calls())
	if _, _, err := c.Apply(context.Background(), kcArgs("users", "user"), capability.Plan{}); err == nil || len(f.stub.Calls()) != n {
		t.Fatal("called without a secret", err)
	}
}

// The guard allows only the token POST of the client's realm and GET of the four queries' paths under a listed
// realm: PUT, POST and DELETE under the realm, other admin paths and other realms are refused before any call.
func TestKeycloakGuardReadOnly(t *testing.T) {
	f := newKC(t)
	client := capability.KeycloakClientForTest(f.kc)
	for _, c := range []struct{ method, path string }{
		{"PUT", "/admin/realms/lab/users/0000-0001"},
		{"POST", "/admin/realms/lab/users"},
		{"DELETE", "/admin/realms/lab/users/0000-0001"},
		{"PUT", "/admin/realms/lab/users/0000-0001/reset-password"},
		{"POST", "/admin/realms/lab/users/0000-0001/impersonation"},
		{"DELETE", "/admin/realms/lab/client-scopes/s1"},
		{"PUT", "/admin/realms/lab/components"},
		{"POST", "/admin/realms/lab/user-storage/ldap-1/sync"},
		{"GET", "/admin/realms/lab/users/0000-0001"},
		{"GET", "/admin/realms/lab/users/0000-0001/credentials"},
		{"GET", "/admin/realms/lab/clients"},
		{"GET", "/admin/realms/lab/clients/x/client-secret"},
		{"GET", "/admin/realms/lab"},
		{"GET", "/admin/realms/other/users"},
		{"GET", "/admin/realms/lab/users/../clients"},
		{"GET", "/admin/realms/lab/users/a%2Fb/groups"},
		{"GET", "/admin/realms/lab/users/a_b/groups"},
		{"GET", "/admin/realms/lab/users/a%20b/groups"},
		{"POST", "/realms/other/protocol/openid-connect/token"},
		{"GET", "/realms/lab/protocol/openid-connect/token"},
		{"POST", "/realms/lab/protocol/openid-connect/logout"},
	} {
		req, _ := http.NewRequest(c.method, keycloaktest.URL+c.path, nil)
		if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "guard") {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
	}
	// another host: the token never goes elsewhere
	req, _ := http.NewRequest("POST", "http://evil.example.test"+keycloaktest.Prefix+"/realms/lab/protocol/openid-connect/token", nil)
	if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "guard") {
		t.Fatal(err)
	}
	if n := len(f.stub.Calls()); n != 0 {
		t.Fatalf("%d calls reached the identity provider", n)
	}
	// the allowed paths work and use only GET (plus the one token POST)
	for _, q := range []capability.Args{kcArgs("users", "u"), kcArgs("user", "user01"), kcArgs("ldap-mappers", ""), kcArgs("client-scopes", "")} {
		if _, _, err := f.kc.Apply(context.Background(), q, capability.Plan{}); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, c := range f.stub.Calls() {
		if c.Method != "GET" && !(c.Method == "POST" && strings.HasSuffix(c.Path, "/realms/lab/protocol/openid-connect/token")) {
			t.Fatal("not read-only:", c)
		}
	}
}

// Rollback: nothing; Plan uses no network.
func TestKeycloakPlanAndRollback(t *testing.T) {
	f := newKC(t)
	p, err := f.kc.Plan(context.Background(), kcArgs("users", "user"))
	if err != nil || p.Change != "none" || len(f.stub.Calls()) != 0 {
		t.Fatal(p, err)
	}
	if _, err := f.kc.Rollback(context.Background(), ""); !errors.Is(err, capability.ErrNoRollback) {
		t.Fatal(err)
	}
	if f.kc.Tier() != capability.Read {
		t.Fatal("tier")
	}
}

// The search text and the user name are never in the plan summary, the detail or the audit's arguments (a digest
// there).
func TestKeycloakNoSearchInPlanOrDetail(t *testing.T) {
	f := newKC(t)
	a := kcArgs("user", "user02")
	p, err := f.kc.Plan(context.Background(), a)
	if err != nil || strings.Contains(p.Summary, "user02") {
		t.Fatal(p, err)
	}
	r, _, err := f.kc.Apply(context.Background(), a, capability.Plan{})
	if err != nil || strings.Contains(r.Detail, "user02") {
		t.Fatal(r.Detail, err)
	}
	red := f.kc.Schema().Redacted(a)
	if s, _ := red["search"].(string); !strings.HasPrefix(s, "sha256:") || len(s) != 19 || red["query"] != "user" {
		t.Fatal(red)
	}
	if r, _, _ := f.kc.Apply(context.Background(), kcArgs("user", "nobody"), capability.Plan{}); strings.Contains(r.Detail, "nobody") {
		t.Fatal(r.Detail)
	}
}

// At most ARGUS_ACT_KEYCLOAK_MAX_READS user-returning calls per action; other queries and other actions are not
// counted against it.
func TestKeycloakReadBudget(t *testing.T) {
	f := newKC(t)
	ctxA := capability.WithStep(context.Background(), capability.StepInfo{ActionID: "OPS-1.v1.aaaaaaaaaaaa", Step: 1})
	ctxB := capability.WithStep(context.Background(), capability.StepInfo{ActionID: "OPS-2.v1.bbbbbbbbbbbb", Step: 1})
	for i := 0; i < capability.KeycloakDefaultMaxReads; i++ {
		q := kcArgs("users", "user")
		if i%2 == 1 {
			q = kcArgs("user", "user01")
		}
		if _, _, err := f.kc.Apply(ctxA, q, capability.Plan{}); err != nil {
			t.Fatal(i, err)
		}
	}
	n := len(f.stub.Calls())
	var r *capability.Refusal
	if _, _, err := f.kc.Apply(ctxA, kcArgs("users", "user"), capability.Plan{}); !errors.As(err, &r) || r.Code != capability.CodeLimit {
		t.Fatal(err)
	}
	if len(f.stub.Calls()) != n {
		t.Fatal("a call beyond the budget reached the identity provider")
	}
	if _, _, err := f.kc.Apply(ctxA, kcArgs("client-scopes", ""), capability.Plan{}); err != nil {
		t.Fatal("client-scopes counted:", err)
	}
	if _, _, err := f.kc.Apply(ctxB, kcArgs("users", "user"), capability.Plan{}); err != nil {
		t.Fatal("another action refused:", err)
	}
	if _, err := capability.NewKeycloak(capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"}, ClientID: "x", AllowPlain: true, MaxReads: 51}); err == nil {
		t.Fatal("MaxReads 51 accepted")
	}
}

// A failed token request is remembered for 60 s: the next queries get the same error without a new POST.
func TestKeycloakTokenFailureCached(t *testing.T) {
	f := newKC(t)
	reg, _ := capability.Default(capability.Options{Keycloak: capability.KeycloakOptions{URL: keycloaktest.URL, Realms: []string{"lab"},
		ClientID: keycloaktest.ClientID, AllowPlain: true, Secret: func() (string, error) { return "wrong", nil }, Transport: f.stub.Transport()}})
	c, _ := reg.Get(capability.KeycloakRead)
	kc := c.(*capability.Keycloak)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	capability.SetKeycloakClock(kc, func() time.Time { return now })
	posts := func() int {
		n := 0
		for _, c := range f.stub.Calls() {
			if c.Method == "POST" {
				n++
			}
		}
		return n
	}
	for _, q := range []capability.Args{kcArgs("client-scopes", ""), kcArgs("ldap-mappers", "")} {
		if _, _, err := kc.Apply(context.Background(), q, capability.Plan{}); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatal(err)
		}
	}
	if posts() != 1 {
		t.Fatal("token POSTs:", posts())
	}
	now = now.Add(61 * time.Second)
	kc.Apply(context.Background(), kcArgs("client-scopes", ""), capability.Plan{})
	if posts() != 2 {
		t.Fatal("not retried after 60 s:", posts())
	}
}
