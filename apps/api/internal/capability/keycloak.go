package capability

// keycloak.go: keycloak.read (ADR-0022 D3, PR 4). Read-only admin queries of the realms on
// ARGUS_ACT_KEYCLOAK_REALMS. The identity provider may be shared with production, so the HTTP client's guard allows
// exactly one POST (the token endpoint of the client's realm) and GET of exactly the paths the four queries use;
// every other method and path is refused before a connection is made. Each query returns fixed fields only: a user
// carries first_name_set / last_name_set booleans, never the names, the e-mail, the attributes or a credential; an
// LDAP mapper carries only the config keys on a fixed list. The client secret and the access token never leave
// this file: not in a plan, a result, a detail, an error or a log.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The capability's name, its credential and its limits.
const (
	KeycloakRead = "keycloak.read"
	// KeycloakCredential is the stored client secret (argus actd credential set keycloak/client)
	KeycloakCredential = "keycloak/client"
	// KeycloakMaxRows is the most rows one query returns (users, mappers, scopes, a user's groups)
	KeycloakMaxRows = 50
	// KeycloakMaxSearch is the longest search text
	KeycloakMaxSearch = 100
	// kcTokenSlack: a cached token is used until this long before it expires
	kcTokenSlack = 30 * time.Second
	kcMaxBody    = 1 << 20
	kcMaxLDAP    = 10 // LDAP providers looked at in one realm
	kcMaxValue   = 200
)

// The four queries.
const (
	KcUsers        = "users"
	KcUser         = "user"
	KcLDAPMappers  = "ldap-mappers"
	KcClientScopes = "client-scopes"
)

var (
	kcRealmRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	kcQueryRe  = regexp.MustCompile(`^(users|user|ldap-mappers|client-scopes)$`)
	kcSearchRe = regexp.MustCompile(`^[\p{L}\p{N} @._+*'"-]{1,100}$`)
	kcIDRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// kcMapperConfig are the LDAP mapper config keys a result may carry: names of attributes, DNs, filters and switches.
// Any other key (a bind credential, a key a later version adds) is dropped.
var kcMapperConfig = map[string]bool{
	"ldap.attribute": true, "user.model.attribute": true, "read.only": true, "always.read.value.from.ldap": true,
	"is.mandatory.in.ldap": true, "is.binary.attribute": true, "attribute.force.default": true,
	"ldap.full.name.attribute": true, "write.only": true, "ldap.attribute.name": true,
	"group.name.ldap.attribute": true, "group.object.classes": true, "groups.dn": true, "groups.path": true,
	"groups.ldap.filter": true, "preserve.group.inheritance": true, "ignore.missing.groups": true,
	"membership.ldap.attribute": true, "membership.attribute.type": true, "membership.user.ldap.attribute": true,
	"mode": true, "user.roles.retrieve.strategy": true, "memberof.ldap.attribute": true,
	"mapped.group.attributes": true, "drop.non.existing.groups.during.sync": true,
	"role.name.ldap.attribute": true, "role.object.classes": true, "roles.dn": true, "roles.ldap.filter": true,
	"use.realm.roles.mapping": true, "client.id": true, "role": true, "group": true,
}

// KeycloakOptions configure keycloak.read.
type KeycloakOptions struct {
	URL      string   // ARGUS_ACT_KEYCLOAK_URL: https://id.example.com (a path prefix such as /auth is kept); empty: off
	Realms   []string // ARGUS_ACT_KEYCLOAK_REALMS: the realms that may be read
	ClientID string   // ARGUS_ACT_KEYCLOAK_CLIENT_ID: the confidential client with a service account
	// ClientRealm is the realm the client lives in (ARGUS_ACT_KEYCLOAK_CLIENT_REALM); empty: the only realm on Realms
	ClientRealm string
	AllowPlain  bool // plain http (test installations only)
	// Secret is the client secret (actd: from argus-act); nil in the worker, which only validates
	Secret    func() (string, error)
	Transport http.RoundTripper // the base transport under the guard (nil: a direct one); tests set it
}

// Keycloak is the client of keycloak.read.
type Keycloak struct {
	base        *url.URL
	prefix      string
	realms      map[string]bool
	clientID    string
	clientRealm string
	secret      func() (string, error)
	client      *http.Client
	now         func() time.Time

	mu     sync.Mutex
	tok    string
	tokExp time.Time
}

// ParseRealmList reads ARGUS_ACT_KEYCLOAK_REALMS: realm names, case kept, no repeats.
func ParseRealmList(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range list {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		if !kcRealmRe.MatchString(r) || r == "." || r == ".." {
			return nil, fmt.Errorf("ARGUS_ACT_KEYCLOAK_REALMS: %q is not a realm name (A-Z, a-z, 0-9, _ . -, at most 64)", clipName(r))
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// NewKeycloak builds keycloak.read.
func NewKeycloak(o KeycloakOptions) (*Keycloak, error) {
	realms, err := ParseRealmList(o.Realms)
	if err != nil {
		return nil, err
	}
	if len(realms) == 0 {
		return nil, errors.New("keycloak.read: ARGUS_ACT_KEYCLOAK_URL is set but ARGUS_ACT_KEYCLOAK_REALMS is empty")
	}
	u, err := url.Parse(strings.TrimSpace(o.URL))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && o.AllowPlain) {
		return nil, errors.New("ARGUS_ACT_KEYCLOAK_URL must be an https URL without credentials or query (plain http only for test installations)")
	}
	cid := strings.TrimSpace(o.ClientID)
	if cid == "" || len(cid) > 255 || strings.ContainsAny(cid, "\x00\r\n") {
		return nil, errors.New("keycloak.read: ARGUS_ACT_KEYCLOAK_CLIENT_ID is required (the confidential client with a service account)")
	}
	cr := strings.TrimSpace(o.ClientRealm)
	switch {
	case cr == "" && len(realms) == 1:
		cr = realms[0]
	case cr == "":
		return nil, errors.New("keycloak.read: with several realms, ARGUS_ACT_KEYCLOAK_CLIENT_REALM names the realm the client lives in")
	case !kcRealmRe.MatchString(cr) || cr == "." || cr == "..":
		return nil, fmt.Errorf("ARGUS_ACT_KEYCLOAK_CLIENT_REALM: %q is not a realm name", clipName(cr))
	}
	k := &Keycloak{base: u, prefix: strings.TrimSuffix(u.EscapedPath(), "/"), realms: map[string]bool{}, clientID: cid,
		clientRealm: cr, secret: o.Secret, now: time.Now}
	for _, r := range realms {
		k.realms[r] = true
	}
	base := o.Transport
	if base == nil {
		base = &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: MaxCall, DisableKeepAlives: true,
			MaxResponseHeaderBytes: 64 << 10}
	}
	host := strings.ToLower(u.Hostname())
	hostOK := func(h string) bool { return h == host }
	guard := &Guard{Base: base, Rules: []Rule{
		{Method: http.MethodPost, Host: hostOK, Match: k.tokenPath},
		{Method: http.MethodGet, Host: hostOK, Match: k.adminPath},
	}}
	k.client = &http.Client{Transport: guard, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return k, nil
}

// Realms is the allow-list, sorted.
func (k *Keycloak) Realms() []string {
	out := make([]string, 0, len(k.realms))
	for r := range k.realms {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// tokenPath is the one POST the guard allows: the token endpoint of the client's realm.
func (k *Keycloak) tokenPath(path string) bool {
	return path == k.prefix+"/realms/"+k.clientRealm+"/protocol/openid-connect/token"
}

// kcAdminTemplates are the admin paths the four queries use (GET only).
var kcAdminTemplates = []string{"/users", "/users/:id/groups", "/components", "/client-scopes"}

// adminPath allows GET of the four queries' paths under a realm on the allow-list.
func (k *Keycloak) adminPath(path string) bool {
	rest, ok := strings.CutPrefix(path, k.prefix+"/admin/realms/")
	if !ok {
		return false
	}
	realm, _, _ := strings.Cut(rest, "/")
	if !k.realms[realm] {
		return false
	}
	for _, t := range kcAdminTemplates {
		if MatchPath(k.prefix+"/admin/realms/:realm"+t, path) {
			return true
		}
	}
	return false
}

func (k *Keycloak) Name() string { return KeycloakRead }
func (k *Keycloak) Tier() Tier   { return Read }

func (k *Keycloak) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "realm", Type: String, Required: true, MaxLen: 64, Pattern: kcRealmRe, AllowList: "ARGUS_ACT_KEYCLOAK_REALMS"},
		{Name: "query", Type: String, Required: true, MaxLen: 16, Pattern: kcQueryRe},
		{Name: "search", Type: String, MaxLen: KeycloakMaxSearch, Pattern: kcSearchRe},
	}}
}

// Validate: a known classification (Read tier: every one, O8), the schema, the realm allow-list, and a search for
// users (the text) and user (the exact user name). No network.
func (k *Keycloak) Validate(args Args, env Env) error {
	if !env.Known() {
		return Refuse(2, CodeEnvironment, "the task's classification is unknown")
	}
	if err := k.Schema().Check(args); err != nil {
		return err
	}
	if r := argStr(args, "realm"); !k.realms[r] {
		return Refuse(4, CodeAllowList, "realm %q is not on ARGUS_ACT_KEYCLOAK_REALMS", clipName(r))
	}
	if q := argStr(args, "query"); (q == KcUsers || q == KcUser) && argStr(args, "search") == "" {
		return Refuse(4, CodeArguments, "argument \"search\" is required for the query %s", q)
	}
	return nil
}

// Plan uses no network: it names the query Apply runs.
func (k *Keycloak) Plan(_ context.Context, args Args) (Plan, error) {
	if err := k.Validate(args, Develop); err != nil {
		return Plan{}, err
	}
	s := ""
	if v := argStr(args, "search"); v != "" {
		s = fmt.Sprintf(", search %q", v)
	}
	return Plan{Change: "none", Summary: fmt.Sprintf("read %s of realm %s%s (at most %d rows)", argStr(args, "query"), argStr(args, "realm"), s, KeycloakMaxRows)}, nil
}

// Apply runs the query.
func (k *Keycloak) Apply(ctx context.Context, args Args, _ Plan) (Result, BackupRef, error) {
	r, err := k.read(ctx, args)
	return r, "", err
}

// Verify runs the query again.
func (k *Keycloak) Verify(ctx context.Context, args Args, _ Plan) (Result, error) {
	return k.read(ctx, args)
}

// Rollback: nothing to roll back.
func (k *Keycloak) Rollback(context.Context, BackupRef) (Result, error) {
	return Result{}, ErrNoRollback
}

func (k *Keycloak) read(ctx context.Context, args Args) (Result, error) {
	if err := k.Validate(args, Develop); err != nil {
		return failed(err), err
	}
	realm, q, search := argStr(args, "realm"), argStr(args, "query"), argStr(args, "search")
	var (
		f   map[string]any
		det string
		err error
	)
	switch q {
	case KcUsers:
		f, det, err = k.users(ctx, realm, search)
	case KcUser:
		f, det, err = k.user(ctx, realm, search)
	case KcLDAPMappers:
		f, det, err = k.ldapMappers(ctx, realm, search)
	case KcClientScopes:
		f, det, err = k.clientScopes(ctx, realm, search)
	default:
		err = Refuse(4, CodeArguments, "unknown query")
	}
	if err != nil {
		return failed(err), err
	}
	f["realm"], f["query"] = realm, q
	return Result{Status: "ok", Detail: det, Fields: f}, nil
}

// ---- the client ---------------------------------------------------------------------------------------------

// token is the service account's access token, cached until kcTokenSlack before it expires.
func (k *Keycloak) token(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.tok != "" && k.now().Before(k.tokExp) {
		return k.tok, nil
	}
	k.tok, k.tokExp = "", time.Time{}
	if k.secret == nil {
		return "", errors.New("no Keycloak credential in this process (argus actd credential set " + KeycloakCredential + ")")
	}
	sec, err := k.secret()
	sec = strings.TrimSpace(sec)
	if err != nil || sec == "" {
		return "", errors.New("the Keycloak client secret is not stored or does not open (argus actd credential set " + KeycloakCredential + ")")
	}
	u := *k.base
	u.Path, u.RawPath = "", ""
	tu := u.String() + k.prefix + "/realms/" + k.clientRealm + "/protocol/openid-connect/token"
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.clientID}, "client_secret": {sec}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tu, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("bad token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-actd/keycloak.read")
	code, b, err := k.do(req)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("Keycloak answered %d to the token request (check the client id, its secret and that the service account is enabled)", code)
	}
	var t struct {
		Access    string `json:"access_token"`
		ExpiresIn int64  `json:"expires_in"`
		Type      string `json:"token_type"`
	}
	if json.Unmarshal(b, &t) != nil || t.Access == "" || !strings.EqualFold(t.Type, "bearer") {
		return "", errors.New("the Keycloak token answer is not a bearer token")
	}
	if life := time.Duration(t.ExpiresIn) * time.Second; life > kcTokenSlack {
		k.tok, k.tokExp = t.Access, k.now().Add(life-kcTokenSlack)
	}
	return t.Access, nil
}

// dropToken forgets the cached token (Keycloak answered 401: it was revoked or the session ended).
func (k *Keycloak) dropToken() {
	k.mu.Lock()
	k.tok, k.tokExp = "", time.Time{}
	k.mu.Unlock()
}

// do calls Keycloak and reads at most kcMaxBody bytes. An error never holds the response body.
func (k *Keycloak) do(req *http.Request) (int, []byte, error) {
	resp, err := k.client.Do(req)
	if err != nil {
		var ge *ErrGuard
		if errors.As(err, &ge) {
			return 0, nil, Refuse(4, CodeAllowList, "%s", ge.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, errors.New("Keycloak did not answer in time")
		}
		return 0, nil, errors.New("the Keycloak request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, kcMaxBody+1))
	if err != nil || len(b) > kcMaxBody {
		return resp.StatusCode, nil, errors.New("the Keycloak answer is not readable or too large")
	}
	return resp.StatusCode, b, nil
}

// get reads one admin path of a realm on the list into out.
func (k *Keycloak) get(ctx context.Context, realm, sub string, q url.Values, out any) error {
	if !k.realms[realm] {
		return Refuse(4, CodeAllowList, "realm %q is not on ARGUS_ACT_KEYCLOAK_REALMS", clipName(realm))
	}
	tok, err := k.token(ctx)
	if err != nil {
		return err
	}
	u := *k.base
	raw := k.prefix + "/admin/realms/" + realm + sub
	p, err := url.PathUnescape(raw)
	if err != nil {
		return errors.New("bad path")
	}
	u.Path, u.RawPath = p, raw
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errors.New("bad request")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "argus-actd/keycloak.read")
	code, b, err := k.do(req)
	switch {
	case err != nil:
		return err
	case code == http.StatusUnauthorized:
		k.dropToken()
		return errors.New("Keycloak answered 401 (the token was not accepted)")
	case code == http.StatusForbidden:
		return errors.New("Keycloak answered 403: the service account lacks the realm-management role this query needs")
	case code != http.StatusOK:
		return fmt.Errorf("Keycloak answered %d", code)
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("the Keycloak answer is not the expected JSON")
	}
	return nil
}

// ---- the four queries ---------------------------------------------------------------------------------------

// kcUser is a user as Keycloak returns it. Only the fields declared here are read; the others (e-mail,
// attributes, credentials) are never decoded.
type kcUser struct {
	ID              string   `json:"id"`
	Username        string   `json:"username"`
	Enabled         bool     `json:"enabled"`
	FederationLink  string   `json:"federationLink"`
	RequiredActions []string `json:"requiredActions"`
	FirstName       string   `json:"firstName"`
	LastName        string   `json:"lastName"`
}

// row is the user's fixed result fields: never the names, the e-mail, the attributes or a credential.
func (u kcUser) row() map[string]any {
	ra := make([]string, 0, len(u.RequiredActions))
	for i, a := range u.RequiredActions {
		if i >= 20 {
			break
		}
		ra = append(ra, clipValue(a, 64))
	}
	return map[string]any{
		"username":         clipValue(u.Username, 255),
		"enabled":          u.Enabled,
		"federation_link":  clipValue(u.FederationLink, 64),
		"required_actions": ra,
		"first_name_set":   strings.TrimSpace(u.FirstName) != "",
		"last_name_set":    strings.TrimSpace(u.LastName) != "",
	}
}

func (k *Keycloak) users(ctx context.Context, realm, search string) (map[string]any, string, error) {
	var list []kcUser
	q := url.Values{"search": {search}, "first": {"0"}, "max": {fmt.Sprint(KeycloakMaxRows + 1)}, "briefRepresentation": {"false"}}
	if err := k.get(ctx, realm, "/users", q, &list); err != nil {
		return nil, "", err
	}
	truncated := len(list) > KeycloakMaxRows
	if truncated {
		list = list[:KeycloakMaxRows]
	}
	rows := make([]map[string]any, 0, len(list))
	for _, u := range list {
		rows = append(rows, u.row())
	}
	det := fmt.Sprintf("%d users match", len(rows))
	if truncated {
		det = fmt.Sprintf("more than %d users match; the first %d are shown", KeycloakMaxRows, KeycloakMaxRows)
	}
	return map[string]any{"count": len(rows), "truncated": truncated, "users": rows}, det, nil
}

func (k *Keycloak) user(ctx context.Context, realm, username string) (map[string]any, string, error) {
	var list []kcUser
	q := url.Values{"username": {username}, "exact": {"true"}, "first": {"0"}, "max": {"2"}, "briefRepresentation": {"false"}}
	if err := k.get(ctx, realm, "/users", q, &list); err != nil {
		return nil, "", err
	}
	var hit *kcUser
	for i := range list {
		if strings.EqualFold(list[i].Username, username) {
			hit = &list[i]
			break
		}
	}
	if hit == nil {
		return map[string]any{"exists": false}, fmt.Sprintf("no user %q", clipValue(username, 100)), nil
	}
	if !kcIDRe.MatchString(hit.ID) {
		return nil, "", errors.New("the Keycloak answer has no usable user id")
	}
	var groups []struct {
		Name string `json:"name"`
	}
	gq := url.Values{"first": {"0"}, "max": {fmt.Sprint(KeycloakMaxRows + 1)}, "briefRepresentation": {"true"}}
	if err := k.get(ctx, realm, "/users/"+hit.ID+"/groups", gq, &groups); err != nil {
		return nil, "", err
	}
	gt := len(groups) > KeycloakMaxRows
	if gt {
		groups = groups[:KeycloakMaxRows]
	}
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, clipValue(g.Name, 255))
	}
	row := hit.row()
	row["groups"], row["groups_truncated"] = names, gt
	return map[string]any{"exists": true, "user": row}, fmt.Sprintf("user %q found, %d groups", clipValue(hit.Username, 100), len(names)), nil
}

type kcComponent struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	ProviderID string              `json:"providerId"`
	Config     map[string][]string `json:"config"`
}

func (k *Keycloak) ldapMappers(ctx context.Context, realm, search string) (map[string]any, string, error) {
	var providers []kcComponent
	if err := k.get(ctx, realm, "/components", url.Values{"type": {"org.keycloak.storage.UserStorageProvider"}}, &providers); err != nil {
		return nil, "", err
	}
	rows := []map[string]any{}
	truncated, seen := false, 0
	for _, p := range providers {
		if p.ProviderID != "ldap" || !kcIDRe.MatchString(p.ID) {
			continue
		}
		if seen++; seen > kcMaxLDAP {
			truncated = true
			break
		}
		var mappers []kcComponent
		q := url.Values{"parent": {p.ID}, "type": {"org.keycloak.storage.ldap.mappers.LDAPStorageMapper"}}
		if err := k.get(ctx, realm, "/components", q, &mappers); err != nil {
			return nil, "", err
		}
		sort.Slice(mappers, func(i, j int) bool { return mappers[i].Name < mappers[j].Name })
		for _, m := range mappers {
			if !nameMatches(m.Name, search) {
				continue
			}
			if len(rows) >= KeycloakMaxRows {
				truncated = true
				break
			}
			rows = append(rows, map[string]any{"provider": clipValue(p.Name, 255), "name": clipValue(m.Name, 255),
				"type": clipValue(m.ProviderID, 128), "config": mapperConfig(m.Config)})
		}
	}
	det := fmt.Sprintf("%d LDAP mappers", len(rows))
	if truncated {
		det += fmt.Sprintf(" (cut at %d)", KeycloakMaxRows)
	}
	return map[string]any{"count": len(rows), "truncated": truncated, "mappers": rows}, det, nil
}

// mapperConfig keeps only the config keys on kcMapperConfig; a value Keycloak masks or that looks like a credential
// is dropped.
func mapperConfig(c map[string][]string) map[string]string {
	out := map[string]string{}
	for key, vs := range c {
		if !kcMapperConfig[key] || credentialKey(key) {
			continue
		}
		v := strings.Join(vs, ",")
		if strings.Contains(v, "**********") || strings.ContainsAny(v, "\x00") {
			continue
		}
		out[key] = clipValue(v, kcMaxValue)
	}
	return out
}

func (k *Keycloak) clientScopes(ctx context.Context, realm, search string) (map[string]any, string, error) {
	var scopes []struct {
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		Mappers  []struct {
			Name string `json:"name"`
		} `json:"protocolMappers"`
	}
	if err := k.get(ctx, realm, "/client-scopes", nil, &scopes); err != nil {
		return nil, "", err
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Name < scopes[j].Name })
	rows := []map[string]any{}
	truncated := false
	for _, s := range scopes {
		if !nameMatches(s.Name, search) {
			continue
		}
		if len(rows) >= KeycloakMaxRows {
			truncated = true
			break
		}
		names := make([]string, 0, len(s.Mappers))
		for i, m := range s.Mappers {
			if i >= KeycloakMaxRows {
				break
			}
			names = append(names, clipValue(m.Name, 255))
		}
		sort.Strings(names)
		rows = append(rows, map[string]any{"name": clipValue(s.Name, 255), "protocol": clipValue(s.Protocol, 64), "mappers": names})
	}
	det := fmt.Sprintf("%d client scopes", len(rows))
	if truncated {
		det += fmt.Sprintf(" (cut at %d)", KeycloakMaxRows)
	}
	return map[string]any{"count": len(rows), "truncated": truncated, "scopes": rows}, det, nil
}

// nameMatches is the optional local name filter of ldap-mappers and client-scopes (case-insensitive substring).
func nameMatches(name, search string) bool {
	s := strings.Trim(search, "*")
	return s == "" || strings.Contains(strings.ToLower(name), strings.ToLower(s))
}

func clipValue(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
