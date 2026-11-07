// Package kube is the root side and the agent tool of Settings -> Kubernetes.
//
// config.go: kubeconfig shape checks and the read-only verdict. A kubeconfig is accepted only when nothing in it
// makes kubectl run a program or read another file on the host: inline data only (token, client-certificate-data,
// client-key-data, certificate-authority-data, username/password). Refused: exec and auth-provider plugins, file
// paths (client-certificate, client-key, certificate-authority, tokenFile), impersonation (as, as-groups, as-uid,
// as-user-extra) and every key this package does not know. Minimal keeps the chosen context, its cluster and its user,
// so what root stores is exactly what was checked.
//
// Classify turns `kubectl auth can-i --list` and a few cluster-wide `auth can-i` answers into a verdict:
//
//	read-only   get/list/watch on resources only, never on Secrets; nothing that writes, execs, escalates or impersonates
//	writes      can change something (create/update/patch/delete/deletecollection, exec/attach/port-forward/proxy,
//	            escalate/bind/impersonate, approve/sign, or *)
//	secrets     read-only, but can read Secrets (get/list/watch on secrets or on *)
//	unknown     the permission list was empty or could not be read
package kube

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxBytes is the largest kubeconfig accepted (the web app checks the same).
const MaxBytes = 64 * 1024

var (
	NameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	ContextRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,120}$`)
	Envs      = []string{"develop", "pre-release", "release"}
	serverRe  = regexp.MustCompile(`^https://[A-Za-z0-9.\[\]:-]+(:\d{1,5})?(/\S*)?$`)
	proxyRe   = regexp.MustCompile(`^(https?|socks5)://\S+$`)
)

var (
	topKeys     = set("apiVersion", "kind", "clusters", "contexts", "users", "current-context", "preferences", "extensions")
	clusterKeys = set("server", "certificate-authority-data", "insecure-skip-tls-verify", "tls-server-name", "proxy-url",
		"disable-compression", "extensions")
	userKeys    = set("token", "client-certificate-data", "client-key-data", "username", "password", "extensions")
	contextKeys = set("cluster", "user", "namespace", "extensions")
	// Why names the refusal: a plugin runs a program, a path reads a file, "as" impersonates.
	Why = map[string]string{
		"exec":                  "an exec plugin (runs a program on the host)",
		"auth-provider":         "an auth-provider plugin (runs a program or refreshes tokens on the host)",
		"client-certificate":    "a client-certificate file path (use client-certificate-data)",
		"client-key":            "a client-key file path (use client-key-data)",
		"certificate-authority": "a certificate-authority file path (use certificate-authority-data)",
		"tokenFile":             "a token file path (put the token inline)",
		"token-file":            "a token file path (put the token inline)",
		"as":                    "impersonation (as)", "as-groups": "impersonation (as-groups)", "as-uid": "impersonation (as-uid)",
		"as-user-extra": "impersonation (as-user-extra)",
	}
)

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Invalid is a kubeconfig refusal; its text never contains a credential.
type Invalid string

func (e Invalid) Error() string { return string(e) }

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ParseJSON is the kubeconfig as a map (JSON; YAML is converted by kubectl first, see Options.ToJSON).
func ParseJSON(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, Invalid("the kubeconfig is empty")
	}
	if len(text) > MaxBytes {
		return nil, Invalid(fmt.Sprintf("the kubeconfig is larger than %d KB", MaxBytes/1024))
	}
	if strings.ContainsRune(text, 0) {
		return nil, Invalid("the kubeconfig contains a NUL byte")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil || doc == nil {
		return nil, Invalid("the kubeconfig is not a mapping with clusters, contexts and users")
	}
	return doc, nil
}

func named(doc map[string]any, key, inner string, allowed map[string]bool) (map[string]map[string]any, error) {
	items, _ := doc[key].([]any)
	if len(items) == 0 {
		return nil, Invalid("the kubeconfig has no " + key)
	}
	out := map[string]map[string]any{}
	one := strings.TrimSuffix(key, "s")
	for _, it := range items {
		m, _ := it.(map[string]any)
		name, _ := m["name"].(string)
		if m == nil || name == "" {
			return nil, Invalid("an entry in " + key + " has no name")
		}
		body, ok := m[inner].(map[string]any)
		if !ok {
			return nil, Invalid(fmt.Sprintf("%s %q has no %s block", one, cut(name, 60), inner))
		}
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if w, bad := Why[k]; bad {
				return nil, Invalid(fmt.Sprintf("%s %q uses %s; only inline data is accepted", one, cut(name, 60), w))
			}
			if !allowed[k] {
				return nil, Invalid(fmt.Sprintf("%s %q has an unsupported key %q", one, cut(name, 60), cut(k, 40)))
			}
		}
		out[name] = body
	}
	return out, nil
}

// Summary is what the page may show about a kubeconfig (no secret).
type Summary struct {
	Context    string `json:"context"`
	Cluster    string `json:"cluster"`
	User       string `json:"user"`
	ServerHost string `json:"server_host"`
	Namespace  string `json:"namespace"`
	Insecure   bool   `json:"insecure"`
	Auth       string `json:"auth"`
}

// Validate checks the whole document and picks the context (the given one, else current-context, else the only one).
func Validate(doc map[string]any, context string) (Summary, error) {
	var s Summary
	for k := range doc {
		if !topKeys[k] {
			return s, Invalid(fmt.Sprintf("unsupported top-level key %q", cut(k, 40)))
		}
	}
	clusters, err := named(doc, "clusters", "cluster", clusterKeys)
	if err != nil {
		return s, err
	}
	users, err := named(doc, "users", "user", userKeys)
	if err != nil {
		return s, err
	}
	contexts, err := named(doc, "contexts", "context", contextKeys)
	if err != nil {
		return s, err
	}
	for name, c := range clusters {
		srv, _ := c["server"].(string)
		if !serverRe.MatchString(srv) {
			return s, Invalid(fmt.Sprintf("cluster %q needs an https:// server URL", cut(name, 60)))
		}
		if px, ok := c["proxy-url"]; ok {
			if p, _ := px.(string); !proxyRe.MatchString(p) {
				return s, Invalid(fmt.Sprintf("cluster %q has an invalid proxy-url", cut(name, 60)))
			}
		}
	}
	for name, u := range users {
		for k, v := range u {
			if _, ok := v.(string); !ok && k != "extensions" {
				return s, Invalid(fmt.Sprintf("user %q: %s must be a string", cut(name, 60), k))
			}
		}
		str := func(k string) string { v, _ := u[k].(string); return v }
		if str("token") == "" && str("client-certificate-data") == "" && str("username") == "" {
			return s, Invalid(fmt.Sprintf("user %q has no inline credential (token or client-certificate-data)", cut(name, 60)))
		}
		if (str("client-certificate-data") == "") != (str("client-key-data") == "") {
			return s, Invalid(fmt.Sprintf("user %q needs both client-certificate-data and client-key-data", cut(name, 60)))
		}
	}
	ctx := context
	if ctx == "" {
		ctx, _ = doc["current-context"].(string)
	}
	if ctx == "" && len(contexts) == 1 {
		for k := range contexts {
			ctx = k
		}
	}
	if ctx == "" {
		return s, Invalid("several contexts and no current-context: name the context to use")
	}
	c, ok := contexts[ctx]
	if !ok {
		return s, Invalid(fmt.Sprintf("context %q is not in the kubeconfig", cut(ctx, 60)))
	}
	cl, _ := c["cluster"].(string)
	us, _ := c["user"].(string)
	if clusters[cl] == nil {
		return s, Invalid(fmt.Sprintf("context %q names a cluster that is not in the kubeconfig", cut(ctx, 60)))
	}
	if users[us] == nil {
		return s, Invalid(fmt.Sprintf("context %q names a user that is not in the kubeconfig", cut(ctx, 60)))
	}
	srv, _ := clusters[cl]["server"].(string)
	host := strings.SplitN(strings.TrimPrefix(srv, "https://"), "/", 2)[0]
	ns, _ := c["namespace"].(string)
	ins, _ := clusters[cl]["insecure-skip-tls-verify"].(bool)
	auth := "password"
	if v, _ := users[us]["token"].(string); v != "" {
		auth = "token"
	} else if v, _ := users[us]["client-certificate-data"].(string); v != "" {
		auth = "certificate"
	}
	return Summary{Context: ctx, Cluster: cl, User: us, ServerHost: cut(host, 120), Namespace: cut(ns, 63), Insecure: ins,
		Auth: auth}, nil
}

// Minimal is the kubeconfig reduced to the chosen context, its cluster and its user (JSON, which kubectl reads).
func Minimal(doc map[string]any, s Summary) map[string]any {
	pick := func(key, inner, name string) map[string]any {
		items, _ := doc[key].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			if n, _ := m["name"].(string); n == name {
				body := map[string]any{}
				for k, v := range m[inner].(map[string]any) {
					if k != "extensions" {
						body[k] = v
					}
				}
				return map[string]any{"name": name, inner: body}
			}
		}
		return nil
	}
	return map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": s.Context, "preferences": map[string]any{},
		"clusters": []any{pick("clusters", "cluster", s.Cluster)}, "users": []any{pick("users", "user", s.User)},
		"contexts": []any{pick("contexts", "context", s.Context)}}
}

// --- the read-only verdict ------------------------------------------------------------------------------------------

var (
	readVerbs  = set("get", "list", "watch")
	selfReview = set("selfsubjectaccessreviews.authorization.k8s.io", "selfsubjectrulesreviews.authorization.k8s.io",
		"selfsubjectreviews.authentication.k8s.io")
	secretRes = set("secrets", "*", "*.*")
	rowRe     = regexp.MustCompile(`^(\S+)\s+(\[.*?\])\s+(\[.*?\])\s+(\[.*?\])\s*$`)
	bracketRe = regexp.MustCompile(`\[.*?\]`)
)

// Probe is one cluster-wide question asked on top of --list (which covers one namespace only).
type Probe struct{ Verb, Resource, Finding string }

// Probes are asked with `kubectl auth can-i <verb> <resource> --all-namespaces`.
var Probes = []Probe{
	{"create", "pods", "can create pods"},
	{"delete", "pods", "can delete pods"},
	{"patch", "deployments.apps", "can change deployments"},
	{"create", "pods/exec", "can exec into pods"},
	{"get", "secrets", "can read Secrets"},
	{"list", "secrets", "can list Secrets"},
	{"impersonate", "users", "can impersonate users"},
	{"escalate", "clusterroles.rbac.authorization.k8s.io", "can escalate roles"},
	{"*", "*", "has every permission (cluster-admin)"},
}

type row struct {
	res   string
	urls  []string
	verbs []string
}

func brackets(s string) []string {
	s = strings.TrimSpace(s)
	return strings.Fields(strings.TrimSpace(s[1 : len(s)-1]))
}

func parseList(text string) []row {
	var rows []row
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "Resources") {
			continue
		}
		if m := rowRe.FindStringSubmatch(t); m != nil {
			rows = append(rows, row{m[1], brackets(m[2]), brackets(m[4])})
			continue
		}
		if p := bracketRe.FindAllString(t, -1); len(p) == 3 {
			rows = append(rows, row{"", brackets(p[0]), brackets(p[2])})
		}
	}
	return rows
}

// Verdict is the result of Classify.
type Verdict struct {
	Verdict  string   `json:"verdict"`
	Findings []string `json:"findings"`
}

// Classify decides the verdict from the --list output and the probe answers (true = yes).
func Classify(listText string, probes map[Probe]bool) Verdict {
	rows := parseList(listText)
	var findings []string
	writes, secrets := false, false
	for _, r := range rows {
		vs := map[string]bool{}
		for _, v := range r.verbs {
			vs[strings.ToLower(v)] = true
		}
		if r.res == "" {
			var bad []string
			for v := range vs {
				if !readVerbs[v] && v != "head" && v != "options" {
					bad = append(bad, v)
				}
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				writes = true
				findings = append(findings, "can "+cut(strings.Join(bad, "/"), 40)+" on "+cut(strings.Join(r.urls, " "), 60))
			}
			continue
		}
		base := strings.ToLower(r.res)
		if selfReview[base] && len(vs) == 1 && vs["create"] {
			continue
		}
		var other []string
		for v := range vs {
			if !readVerbs[v] {
				other = append(other, v)
			}
		}
		if len(other) > 0 {
			sort.Strings(other)
			writes = true
			findings = append(findings, "can "+cut(strings.Join(other, "/"), 60)+" "+cut(r.res, 60))
		}
		if (secretRes[base] || strings.SplitN(base, ".", 2)[0] == "secrets") && (vs["get"] || vs["list"] || vs["watch"] || vs["*"]) {
			secrets = true
			f := "can read " + cut(r.res, 60)
			if strings.HasPrefix(base, "*") {
				f += " (all resources, Secrets included)"
			}
			findings = append(findings, f)
		}
	}
	anyYes := false
	for _, p := range Probes {
		if probes[p] {
			anyYes = true
			findings = append(findings, p.Finding)
			if p.Resource == "secrets" {
				secrets = true
			} else {
				writes = true
			}
		}
	}
	if len(rows) == 0 && !anyYes {
		return Verdict{"unknown", []string{"the permission list came back empty"}}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		if !seen[f] && len(out) < 20 {
			seen[f] = true
			out = append(out, f)
		}
	}
	v := "read-only"
	if writes {
		v = "writes"
	} else if secrets {
		v = "secrets"
	}
	if out == nil {
		out = []string{}
	}
	return Verdict{v, out}
}

// MayEnable is (enabled, override used, why) for a verdict. Release never takes the override.
func MayEnable(env, verdict string, override bool) (bool, bool, string) {
	switch {
	case verdict == "read-only":
		return true, false, "read-only"
	case verdict == "unknown":
		return false, false, "could not be checked"
	case env == "release":
		return false, false, "release never takes the override"
	case override:
		return true, true, "override"
	}
	return false, false, "not read-only"
}
