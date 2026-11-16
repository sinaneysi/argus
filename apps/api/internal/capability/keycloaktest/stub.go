// Package keycloaktest is an identity provider stub for the tests of keycloak.read: the client-credentials token
// endpoint and the admin paths the four queries use, every call recorded. Its users carry e-mail, attributes and
// credentials, and its LDAP provider a bind credential, so a test can show none of them reaches a result. Synthetic
// data only; used by tests only.
package keycloaktest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Prefix is the stub's path prefix (the capability must keep the URL's path).
const Prefix = "/kc"

// URL is the identity provider URL the capability is configured with (plain http, dialled to the stub).
const URL = "http://id.example.test" + Prefix

// ClientID and Secret are the stub's accepted client (a fake one).
var (
	ClientID = "argus-read"
	Secret   = "kc-" + "client-" + "secret-0001"
)

// Markers are values the stub returns that must never pass the capability.
var (
	Email      = "first.user@" + "example.com"
	Attribute  = "attr-" + "phone-" + "do-not-leak"
	Credential = "cred-" + "hash-" + "do-not-leak"
	Bind       = "bind-" + "secret-" + "do-not-leak"
	FirstName  = "Firstname-" + "do-not-leak"
	LastName   = "Lastname-" + "do-not-leak"
)

// Call is one recorded request.
type Call struct {
	Method, Path, Query, Auth string
}

// Stub is the fake identity provider with the realms "lab" and "other".
type Stub struct {
	Srv       *httptest.Server
	mu        sync.Mutex
	calls     []Call
	tokens    int
	ExpiresIn int // the token's lifetime in seconds (default 300)
	Users     int // users of realm lab (default 3)
}

// New starts the stub.
func New(t *testing.T) *Stub {
	s := &Stub{ExpiresIn: 300, Users: 3}
	s.Srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Srv.Close)
	return s
}

// Transport dials the stub whatever host the URL names.
func (s *Stub) Transport() http.RoundTripper {
	addr := s.Srv.Listener.Addr().String()
	return &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
}

// Calls is a copy of the recorded requests.
func (s *Stub) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// TokenCalls is how many tokens the stub issued.
func (s *Stub) TokenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens
}

// Token is the n-th issued token (1-based).
func Token(n int) string { return fmt.Sprintf("kc-access-%04d", n) }

func send(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func user(i int) map[string]any {
	last := LastName
	if i == 2 {
		last = ""
	}
	return map[string]any{
		"id": fmt.Sprintf("0000-%04d", i), "username": fmt.Sprintf("user%02d", i), "enabled": i%2 == 1,
		"emailVerified": true, "email": Email, "firstName": FirstName, "lastName": last,
		"federationLink": "ldap-1", "requiredActions": []string{"UPDATE_PASSWORD"},
		"attributes":  map[string]any{"phone": []string{Attribute}, "LDAP_ENTRY_DN": []string{"uid=user,dc=example,dc=com"}},
		"credentials": []map[string]any{{"type": "password", "secretData": Credential, "credentialData": Credential}},
		"access":      map[string]any{"manage": true},
	}
}

func (s *Stub) serve(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")})
	issued := s.tokens
	s.mu.Unlock()
	p := r.URL.EscapedPath()
	if p == Prefix+"/realms/lab/protocol/openid-connect/token" && r.Method == http.MethodPost {
		if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != Secret {
			send(w, 401, map[string]any{"error": "unauthorized_client", "error_description": "secret " + r.PostForm.Get("client_secret")})
			return
		}
		s.mu.Lock()
		s.tokens++
		n := s.tokens
		s.mu.Unlock()
		send(w, 200, map[string]any{"access_token": Token(n), "expires_in": s.ExpiresIn, "token_type": "Bearer"})
		return
	}
	if issued == 0 || r.Header.Get("Authorization") != "Bearer "+Token(issued) {
		send(w, 401, map[string]any{"error": "HTTP 401 Unauthorized"})
		return
	}
	rest, ok := strings.CutPrefix(p, Prefix+"/admin/realms/")
	if !ok || r.Method != http.MethodGet {
		send(w, 405, nil)
		return
	}
	realm, sub, _ := strings.Cut(rest, "/")
	if realm != "lab" && realm != "other" {
		send(w, 404, map[string]any{"error": "Realm not found."})
		return
	}
	q := r.URL.Query()
	switch {
	case sub == "users":
		var out []map[string]any
		for i := 1; i <= s.Users; i++ {
			u := user(i)
			if q.Get("exact") == "true" && q.Get("username") != u["username"] {
				continue
			}
			out = append(out, u)
		}
		if out == nil {
			out = []map[string]any{}
		}
		send(w, 200, out) // ignores max on purpose: the capability must cap the rows itself
	case strings.HasPrefix(sub, "users/") && strings.HasSuffix(sub, "/groups"):
		send(w, 200, []map[string]any{{"id": "g1", "name": "developers", "path": "/developers", "attributes": map[string]any{"x": []string{Attribute}}},
			{"id": "g2", "name": "ops", "path": "/ops"}})
	case sub == "components" && q.Get("type") == "org.keycloak.storage.UserStorageProvider":
		send(w, 200, []map[string]any{
			{"id": "ldap-1", "name": "corp-ldap", "providerId": "ldap", "config": map[string][]string{
				"bindDn": {"cn=reader,dc=example,dc=com"}, "bindCredential": {Bind}, "connectionUrl": {"ldaps://ldap.example.com"}}},
			{"id": "kerb-1", "name": "kerberos", "providerId": "kerberos", "config": map[string][]string{"keyTab": {Bind}}},
		})
	case sub == "components" && q.Get("parent") == "ldap-1":
		send(w, 200, []map[string]any{
			{"id": "m1", "name": "username", "providerId": "user-attribute-ldap-mapper", "config": map[string][]string{
				"ldap.attribute": {"uid"}, "user.model.attribute": {"username"}, "read.only": {"true"}, "bindCredential": {Bind}}},
			{"id": "m2", "name": "groups", "providerId": "group-ldap-mapper", "config": map[string][]string{
				"groups.dn": {"ou=groups,dc=example,dc=com"}, "mode": {"READ_ONLY"}, "client.secret": {Bind}, "attribute.default.value": {Attribute}}},
		})
	case sub == "client-scopes":
		send(w, 200, []map[string]any{
			{"id": "s1", "name": "profile", "protocol": "openid-connect", "attributes": map[string]any{"secret": Bind},
				"protocolMappers": []map[string]any{{"name": "family name", "config": map[string]any{"claim.name": "family_name"}}, {"name": "given name"}}},
			{"id": "s2", "name": "email", "protocol": "openid-connect", "protocolMappers": []map[string]any{{"name": "email"}}},
		})
	default:
		send(w, 404, nil)
	}
}
