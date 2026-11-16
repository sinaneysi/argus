package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// AllowLister is a capability that names the allow-lists limiting its arguments (configuration key -> entries). The
// entries are names (hosts, projects, realms), never a credential.
type AllowLister interface {
	AllowLists() map[string][]string
}

// CatalogueField is one argument as the intake sees it.
type CatalogueField struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Required  bool   `json:"required,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	MaxLength int    `json:"max_length,omitempty"`
	Min       *int   `json:"min,omitempty"`
	Max       *int   `json:"max,omitempty"`
	// Input: the value is a reference "input:<NAME>"; the admin types the secret at approval
	Input     bool   `json:"input,omitempty"`
	Secret    bool   `json:"secret,omitempty"`
	AllowList string `json:"allow_list,omitempty"`
}

// CatalogueEntry is one capability in the intake's task file (ADR-0022 D9): name, tier, schema, allow-lists and the
// environment rule. Built from the registry, so it lists only what actd would run.
type CatalogueEntry struct {
	Name         string              `json:"name"`
	Tier         string              `json:"tier"`
	Environments []string            `json:"environments"`
	Fields       []CatalogueField    `json:"fields"`
	AllowLists   map[string][]string `json:"allow_lists"`
	Rules        []string            `json:"rules"`
}

// catalogueRules are the fixed rules of each built-in capability, in words for the model.
var catalogueRules = map[string][]string{
	HTTPCheckName: {"one GET without credentials or custom headers; the result is the status, the latency and whether it matched, never the body",
		"the URL's host must be on ARGUS_ACT_HTTP_HOSTS; no IP address; no credentials or tokens in the URL"},
	GitLabVarGet: {"environment_scope is exactly \"develop\"; never \"*\", empty or another environment",
		"the result is exists and the attributes, never the value; matches = input:<NAME> compares with a value the admin types at approval"},
	GitLabVarSet: {"environment_scope is exactly \"develop\"; never \"*\", empty or another environment",
		"value_ref is input:<NAME>: the admin types the value at approval; never write a value",
		"the old value is backed up before the write and read back after it; rollback restores it"},
	GitLabVarDelete: {"environment_scope is exactly \"develop\"; never \"*\", empty or another environment",
		"the old value is backed up before the delete; rollback creates it again"},
	KeycloakRead: {"read-only; query is users, user, ldap-mappers or client-scopes",
		"users needs search; user needs the exact user name in search; results never hold e-mail, attributes or credentials"},
}

// Catalogue detail (ARGUS_ACT_CATALOGUE_DETAIL): what the model provider learns about the allow-lists.
const (
	DetailFull    = "full"    // the hosts, project paths and realms as configured
	DetailAliases = "aliases" // stable aliases; Go maps them back before Validate
)

// aliasDomain ends every host alias: a reserved name (RFC 6761) that never resolves.
const aliasDomain = ".alias.invalid"

// aliasOf is a stable alias of a value: prefix and the first 8 hex characters of its SHA-256.
func aliasOf(prefix, v string) string {
	sum := sha256.Sum256([]byte(v))
	return prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

// Aliases maps the aliases the model saw back to the configured values.
type Aliases struct {
	values map[string]string // project, realm alias -> value
	hosts  map[string]string // host alias -> host name
	suffix map[string]string // ".<alias>.alias.invalid" -> ".<suffix>" of a wildcard entry
}

// Resolve is a copy of a step's arguments with every alias replaced by its configured value: a whole value (a
// project, a realm), or the host of a URL (an alias of a listed host, or a name under the alias of a wildcard).
// Anything else is kept as written; Validate then checks it against the real allow-lists. nil a: args as given.
func (a *Aliases) Resolve(args map[string]any) map[string]any {
	if a == nil || args == nil {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		sv, ok := v.(string)
		switch {
		case !ok:
			out[k] = v
		case a.values[sv] != "":
			out[k] = a.values[sv]
		case k == "url":
			out[k] = a.resolveURL(sv)
		default:
			out[k] = v
		}
	}
	return out
}

func (a *Aliases) resolveURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	real := a.hosts[host]
	if real == "" {
		for al, suf := range a.suffix {
			if strings.HasSuffix(host, al) && len(host) > len(al) {
				real = strings.TrimSuffix(host, al) + suf
				break
			}
		}
	}
	if real == "" {
		return raw
	}
	if port != "" {
		real = net.JoinHostPort(real, port)
	}
	u.Host = real
	return u.String()
}

// CatalogueDetail is Catalogue at a detail level: with DetailAliases each allow-list entry is replaced by a stable
// alias, and the Aliases map them back; with DetailFull (or anything else) the entries are as configured and the
// Aliases are nil.
func (r *Registry) CatalogueDetail(detail string) ([]CatalogueEntry, *Aliases) {
	cat := r.Catalogue()
	if detail != DetailAliases {
		return cat, nil
	}
	a := &Aliases{values: map[string]string{}, hosts: map[string]string{}, suffix: map[string]string{}}
	for i := range cat {
		for key, vals := range cat[i].AllowLists {
			out := make([]string, 0, len(vals))
			for _, v := range vals {
				out = append(out, a.add(key, v))
			}
			cat[i].AllowLists[key] = out
		}
		cat[i].Rules = append(cat[i].Rules, "the allow-list entries are aliases: write them exactly as listed; Argus maps them to the real names")
	}
	return cat, a
}

func (a *Aliases) add(key, v string) string {
	switch key {
	case "ARGUS_ACT_HTTP_HOSTS":
		e, err := parseHostEntry(v)
		if err != nil {
			return v
		}
		pre := ""
		if e.internal {
			pre = "internal:"
		}
		port := ""
		if e.port > 0 {
			port = ":" + strconv.Itoa(e.port)
		}
		if e.wildcard {
			al := "." + aliasOf("hosts", e.name) + aliasDomain
			a.suffix[al] = "." + e.name
			return pre + "*" + al + port
		}
		al := aliasOf("host", e.name) + aliasDomain
		a.hosts[al] = e.name
		return pre + al + port
	case "ARGUS_ACT_GITLAB_PROJECTS":
		al := aliasOf("project", v)
		a.values[al] = v
		return al
	case "ARGUS_ACT_KEYCLOAK_REALMS":
		al := aliasOf("realm", v)
		a.values[al] = v
		return al
	}
	return v
}

// Catalogue is the capabilities offered, sorted by name, for the intake's task file. Empty when r is nil.
func (r *Registry) Catalogue() []CatalogueEntry {
	out := []CatalogueEntry{}
	for _, n := range r.Names() {
		c := r.m[n]
		e := CatalogueEntry{Name: n, Tier: c.Tier().String(), Fields: catalogueFields(c.Schema()), AllowLists: map[string][]string{},
			Rules: append([]string{}, catalogueRules[n]...)}
		if c.Tier() == Write {
			e.Environments = []string{string(Develop)}
		} else {
			e.Environments = []string{string(Develop), string(PreRelease), string(Release), string(None)}
		}
		if al, ok := c.(AllowLister); ok {
			for k, v := range al.AllowLists() {
				e.AllowLists[k] = append([]string{}, v...)
			}
		}
		out = append(out, e)
	}
	return out
}

func catalogueFields(s Schema) []CatalogueField {
	out := make([]CatalogueField, 0, len(s.Fields))
	for _, f := range s.Fields {
		cf := CatalogueField{Name: f.Name, Type: f.Type.String(), Required: f.Required, MaxLength: f.MaxLen, Input: f.Input,
			Secret: f.Secret, AllowList: f.AllowList}
		if f.Pattern != nil {
			cf.Pattern = f.Pattern.String()
		}
		if f.Max > 0 {
			lo, hi := f.Min, f.Max
			cf.Min, cf.Max = &lo, &hi
		}
		out = append(out, cf)
	}
	return out
}

// AllowLists: the host allow-list as configured.
func (h *HTTPCheck) AllowLists() map[string][]string {
	out := make([]string, 0, len(h.hosts))
	for _, e := range h.hosts {
		s := e.name
		if e.wildcard {
			s = "*." + s
		}
		if e.port > 0 {
			s += ":" + strconv.Itoa(e.port)
		}
		if e.internal {
			s = "internal:" + s
		}
		out = append(out, s)
	}
	return map[string][]string{"ARGUS_ACT_HTTP_HOSTS": out}
}

// projectList is the project allow-list as configured (ids and lower-case paths), with the resolved paths of listed
// ids once actd resolved them.
func (g *GitLab) projectList() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range g.entries {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	for _, p := range g.Projects() {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// AllowLists: the project allow-list.
func (c *GitLabGet) AllowLists() map[string][]string {
	return map[string][]string{"ARGUS_ACT_GITLAB_PROJECTS": c.g.projectList()}
}

// AllowLists: the project allow-list.
func (c *GitLabSet) AllowLists() map[string][]string {
	return map[string][]string{"ARGUS_ACT_GITLAB_PROJECTS": c.g.projectList()}
}

// AllowLists: the project allow-list.
func (c *GitLabDelete) AllowLists() map[string][]string {
	return map[string][]string{"ARGUS_ACT_GITLAB_PROJECTS": c.g.projectList()}
}

// AllowLists: the realm allow-list.
func (k *Keycloak) AllowLists() map[string][]string {
	return map[string][]string{"ARGUS_ACT_KEYCLOAK_REALMS": k.Realms()}
}
