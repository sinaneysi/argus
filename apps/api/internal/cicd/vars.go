package cicd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The variable audit: keys, scopes and flags only, never values. Inferred from failures and pushes without a
// pipeline, or full from the CI variables metadata (ci-vars-meta, CollectMeta below), which reads each value once in
// memory and keeps only the key, scope, type, flags, whether it is empty, the production-marker classes it names,
// the DOTENV key names, and the value of the routing keys (an executor or inventory group name).

// MetaRow is one variable in the metadata.
type MetaRow struct {
	Key       string   `json:"key"`
	Scope     string   `json:"scope"`
	Type      string   `json:"type"`
	Masked    bool     `json:"masked"`
	Protected bool     `json:"protected"`
	Empty     bool     `json:"empty"`
	Markers   []string `json:"markers"`
	Prod      bool     `json:"prod"`
	Dotenv    []string `json:"dotenv_keys"`
	DotProd   []string `json:"dotenv_prod_keys"`
	Value     string   `json:"value,omitempty"`
	Group     string   `json:"group,omitempty"`
}

// Meta is the ci-vars-meta output (meta.json).
type Meta struct {
	At       int64                `json:"at"`
	Groups   map[string][]MetaRow `json:"groups"`
	Projects map[string]struct {
		Path       string    `json:"path"`
		Rows       []MetaRow `json:"rows"`
		SampleKeys []string  `json:"sample_keys,omitempty"`
	} `json:"projects"`
	Errors []string `json:"errors"`
}

// Marker is one production-marker class: a value that matches Re names production.
type Marker struct {
	Name string
	Re   *regexp.Regexp
}

// RoutingKeys keep their value in the metadata: build and routing names, never secrets.
var RoutingKeys = []string{"EXECUTOR", "TARGET_HOSTS", "ENVIRONMENT", "DEPLOY_TYPE", "DOCKERFILE_NAME", "NODE_VERSION"}

var (
	defaultScopes = regexp.MustCompile(`^(\*|develop|pre-release|release|main|master|review/.*|review\*)$`)
	dotenvLine    = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.-]{0,100})\s*=(.*)`)
	routingVal    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,60}$`)
)

// Finding is one result of a pure check.
type VarFinding struct{ Rule, Env, Severity, Title, Detail, Key string }

func scopeKeys(rows []MetaRow, env string) map[string]MetaRow {
	out := map[string]MetaRow{}
	for _, r := range rows {
		if r.Scope == "*" || r.Scope == env {
			if _, ok := out[r.Key]; !ok || r.Scope == env {
				out[r.Key] = r
			}
		}
	}
	return out
}

func relSev(env, rel, other string) string {
	if env == "release" {
		return rel
	}
	return other
}

func list15(xs []string) string {
	s := strings.Join(xs[:min(15, len(xs))], ", ")
	if len(xs) > 15 {
		s += " …"
	}
	return s
}

// CheckProject is the key-set check of one project (pure).
func CheckProject(rows []MetaRow, envs, sampleKeys []string) []VarFinding {
	var out []VarFinding
	dot := map[string]map[string]bool{}
	for _, env := range envs {
		ks := scopeKeys(rows, env)
		for _, req := range []string{"EXECUTOR"} {
			r, ok := ks[req]
			if !ok {
				out = append(out, VarFinding{"missing-key", env, relSev(env, "medium", "low"), req + "@" + env + " is not set",
					fmt.Sprintf("No %s in scope %s or *: the %s pipeline would be empty.", req, env, env), req})
			} else if r.Empty {
				out = append(out, VarFinding{"empty-key", env, "medium", req + "@" + env + " is empty",
					fmt.Sprintf("%s exists in scope %s but is empty.", req, r.Scope), req})
			}
		}
		var names []string
		for k := range ks {
			if k == "DOTENV" || strings.HasPrefix(k, "DOTENV_") {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			out = append(out, VarFinding{"missing-dotenv", env, relSev(env, "medium", "low"), "DOTENV@" + env + " is not set",
				"Neither DOTENV nor DOTENV_<MODE> in scope " + env + " or *.", "DOTENV"})
		}
		set := map[string]bool{}
		for _, k := range names {
			r := ks[k]
			if r.Type != "file" {
				out = append(out, VarFinding{"dotenv-type", env, "low", r.Key + "@" + env + " is not a File variable",
					"The CI engine expects DOTENV as variable_type=file.", r.Key})
			}
			if r.Empty {
				out = append(out, VarFinding{"empty-dotenv", env, "medium", r.Key + "@" + env + " is empty", "The DOTENV variable has no content.", r.Key})
			}
			for _, d := range r.Dotenv {
				set[d] = true
			}
		}
		dot[env] = set
		if env == "develop" || env == "pre-release" {
			var mk []string
			for k := range ks {
				mk = append(mk, k)
			}
			sort.Strings(mk)
			for _, k := range mk {
				r := ks[k]
				if len(r.Markers) > 0 && r.Scope == env {
					out = append(out, VarFinding{"guardrail-4", env, "high", fmt.Sprintf("%s@%s names production (%s)", k, env, strings.Join(r.Markers, ", ")),
						"A " + env + " variable points at production (stage must never reach production). Value not shown.", k})
				}
			}
		}
		if len(sampleKeys) > 0 && len(names) > 0 {
			var missing []string
			for _, k := range sampleKeys {
				if !set[k] {
					missing = append(missing, k)
				}
			}
			sort.Strings(missing)
			missing = uniq(missing)
			if len(missing) > 0 {
				out = append(out, VarFinding{"sample-keys", env, "low", fmt.Sprintf("DOTENV@%s lacks %d key(s) of the repo's env sample", env, len(missing)),
					"Missing: " + list15(missing), "DOTENV"})
			}
		}
	}
	var order []string
	for _, e := range Envs {
		if len(dot[e]) > 0 {
			order = append(order, e)
		}
	}
	for i := 0; i+1 < len(order); i++ {
		lo, hi := order[i], order[i+1]
		var only []string
		for k := range dot[hi] {
			if !dot[lo][k] {
				only = append(only, k)
			}
		}
		sort.Strings(only)
		if len(only) > 0 {
			out = append(out, VarFinding{"key-diff", lo, "low", fmt.Sprintf("DOTENV@%s lacks %d key(s) that %s has", lo, len(only), hi),
				"Keys in " + hi + " only: " + list15(only), "DOTENV"})
		}
	}
	return out
}

func uniq(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func hasPrefixAny(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// RoutingChecks: TARGET_HOSTS per env names an inventory group of that env (prefixes: CICD_TARGET_PREFIXES); no
// production group at scope *; scope typos; DOTENV, SECRET_* and File variables of release protected (pure).
func (w *Watcher) RoutingChecks(rows []MetaRow, envs []string, groups map[string]bool) []VarFinding {
	var out []VarFinding
	prodPrefix := w.Opt.TargetPrefixes["release"]
	for _, env := range envs {
		ks := scopeKeys(rows, env)
		th, ok := ks["TARGET_HOSTS"]
		if (env == "pre-release" || env == "release") && !ok {
			out = append(out, VarFinding{"no-target", env, "high", "TARGET_HOSTS@" + env + " is not set",
				"Without it the CI engine targets the inventory group named after the environment (" + env + "), which may not exist or may be other hosts.", "TARGET_HOSTS"})
		} else if ok && th.Value != "" {
			v := th.Value
			if len(groups) > 0 && !groups[v] {
				out = append(out, VarFinding{"target-unknown", env, "high", "TARGET_HOSTS@" + env + " = " + v + ": no such inventory group",
					v + " is not a group in the CI engine's inventory; every deploy fails with 'no hosts to target'.", "TARGET_HOSTS"})
			}
			if ps := w.Opt.TargetPrefixes[env]; len(ps) > 0 && !hasPrefixAny(strings.ToLower(v), ps) {
				sev := "high"
				if env != "release" && hasPrefixAny(strings.ToLower(v), prodPrefix) {
					sev = "critical"
				}
				d := env + " must deploy to a " + strings.Join(ps, "/") + "* group"
				if sev == "critical" {
					d += " — this is a production group (stage must never reach production)"
				}
				if th.Scope != env {
					d += " (value comes from scope " + th.Scope + ")"
				}
				out = append(out, VarFinding{"target-env", env, sev, "TARGET_HOSTS@" + env + " = " + v + " belongs to another environment", d + ".", "TARGET_HOSTS"})
			}
		}
	}
	for _, r := range rows {
		if r.Key == "TARGET_HOSTS" && r.Scope == "*" && len(prodPrefix) > 0 && hasPrefixAny(strings.ToLower(r.Value), prodPrefix) {
			out = append(out, VarFinding{"target-star", "", "high", "TARGET_HOSTS@* = " + r.Value + " (production) is the fallback for every scope",
				"Move it to TARGET_HOSTS@release and delete the * value.", "TARGET_HOSTS"})
		}
	}
	scopes := w.Opt.VarScopes
	if scopes == nil {
		scopes = defaultScopes
	}
	for _, r := range rows {
		if !scopes.MatchString(r.Scope) {
			out = append(out, VarFinding{"scope-typo", "", "medium", fmt.Sprintf("%s has scope '%s'", r.Key, r.Scope),
				"No pipeline uses this environment scope (typo?), so the variable is never applied.", r.Key})
		}
		if (r.Scope == "release" || r.Scope == "main" || r.Scope == "master") && (strings.HasPrefix(r.Key, "DOTENV") ||
			strings.HasPrefix(r.Key, "SECRET_") || r.Type == "file") && !r.Protected {
			out = append(out, VarFinding{"unprotected", r.Scope, "high", r.Key + "@" + r.Scope + " is not protected",
				"Any Developer can read it from a pipeline on an unprotected branch with this environment. Mark it protected.", r.Key})
		}
	}
	return out
}

func matchKey(key string, pats []string) bool {
	for _, p := range pats {
		if strings.HasSuffix(p, "*") && strings.HasPrefix(key, strings.TrimSuffix(p, "*")) || key == p {
			return true
		}
	}
	return false
}

// GroupCheck is one group variable that must be protected.
type GroupCheck struct{ Group, Key, Scope, Severity string }

// GroupChecks: deploy keys and tokens at group level (CICD_GROUP_SECRET_KEYS) must be protected, and so must a
// variable of scope * that names production (pure).
func (w *Watcher) GroupChecks(groups map[string][]MetaRow) []GroupCheck {
	var out []GroupCheck
	var names []string
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		for _, r := range groups[g] {
			if matchKey(r.Key, w.Opt.GroupSecretKeys) && !r.Protected {
				sev := "high"
				if strings.HasSuffix(r.Key, "_PROD") || strings.HasSuffix(r.Key, "_PRERELEASE") {
					sev = "critical"
				}
				out = append(out, GroupCheck{g, r.Key, r.Scope, sev})
			}
			if r.Scope == "*" && r.Prod && !r.Protected {
				out = append(out, GroupCheck{g, r.Key, r.Scope, "medium"})
			}
		}
	}
	return out
}

func parents(path string) []string {
	parts := strings.Split(path, "/")
	parts = parts[:len(parts)-1]
	var out []string
	for i := len(parts); i > 0; i-- {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

// FullAudit records the findings of the metadata; the projects checked.
func (w *Watcher) FullAudit(meta Meta) (int, error) {
	var inv Inventory
	GetState(w.tx, "inventory", &inv)
	groups := map[string]bool{}
	for g := range inv.Groups {
		groups[g] = true
	}
	var ids []string
	for id := range meta.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	n := 0
	for _, id := range ids {
		pr := meta.Projects[id]
		p, err := one(w.tx, "SELECT * FROM cicd_projects WHERE id=?", id)
		if err != nil {
			return n, err
		}
		if p == nil {
			continue
		}
		rows := append([]MetaRow{}, pr.Rows...)
		for _, g := range parents(p.S("path")) {
			have := map[[2]string]bool{}
			for _, x := range rows {
				have[[2]string{x.Key, x.Scope}] = true
			}
			for _, r := range meta.Groups[g] {
				if !have[[2]string{r.Key, r.Scope}] {
					r.Group = g
					rows = append(rows, r)
				}
			}
		}
		brs, err := rows2branches(w, p.I("id"))
		if err != nil {
			return n, err
		}
		var results []VarFinding
		if p.S("kind") == "app" {
			results = append(CheckProject(rows, brs, pr.SampleKeys), w.RoutingChecks(rows, brs, groups)...)
		}
		for _, f := range results {
			pp := w.proposal("set_variable", p.I("id"), f.Env, f.Key, f.Detail, nil)
			at := ""
			if f.Env != "" {
				at = "@" + f.Env
			}
			pp.Text = "Operator: fix " + f.Key + at + "."
			if _, _, _, err := UpsertFinding(w.tx, "variable", fmt.Sprintf("variable|%d|%s|%s|%s", p.I("id"), f.Env, f.Rule, f.Key), w.now,
				F{"project_id": p.I("id"), "project": p.S("path"), "env": f.Env, "ref": f.Env, "severity": f.Severity, "klass": "variable",
					"rule": f.Rule, "title": f.Title, "detail": f.Detail + " (from ci-vars-meta: keys and flags only)", "proposals": []Proposal{pp}}, false); err != nil {
				return n, err
			}
		}
		n++
	}
	for _, g := range w.GroupChecks(meta.Groups) {
		if _, _, _, err := UpsertFinding(w.tx, "variable", fmt.Sprintf("variable|group|%s|%s|%s", g.Group, g.Key, g.Scope), w.now,
			F{"project": g.Group, "env": "", "severity": g.Severity, "klass": "variable", "rule": "group-unprotected",
				"title":  fmt.Sprintf("group %s: %s (scope %s) is not protected", g.Group, g.Key, g.Scope),
				"detail": "Every job of every project in the group, on any branch, can read it. Protect it and rotate it.", "proposals": []Proposal{}}, false); err != nil {
			return n, err
		}
	}
	return n, nil
}

func rows2branches(w *Watcher, pid int64) ([]string, error) {
	rs, err := rows(w.tx, "SELECT branch FROM cicd_branches WHERE project_id=? AND present=1", pid)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rs {
		if EnvOf(r.S("branch")) != "" {
			out = append(out, r.S("branch"))
		}
	}
	return out, nil
}

// VarsAudit is the variable part of the audit; its state goes to cicd_state "vars".
func (w *Watcher) VarsAudit() error {
	mode, checked := "inferred", 0
	if w.Opt.MetaFile != "" {
		if st, err := os.Stat(w.Opt.MetaFile); err == nil && w.Opt.Now().Sub(st.ModTime()) < 3*24*time.Hour {
			var meta Meta
			if b, err := os.ReadFile(w.Opt.MetaFile); err == nil && json.Unmarshal(b, &meta) == nil {
				n, err := w.FullAudit(meta)
				if err != nil {
					return err
				}
				checked = n
				if n > 0 {
					mode = "full"
				}
			}
		}
	}
	r, err := one(w.tx, "SELECT count(*) n FROM cicd_findings WHERE status='open' AND (kind='variable' OR klass='variable')")
	if err != nil {
		return err
	}
	needs := ""
	if mode != "full" {
		needs = "the ci-vars-meta job: a GitLab data source whose token reads CI variables (role Maintainer, scope read_api)"
	}
	return SetState(w.tx, "vars", map[string]any{"mode": mode, "at": w.now, "checked": checked, "open": r.I("n"), "needs": needs})
}

// --- ci-vars-meta: the collector --------------------------------------------------------------------------------

// ProductionMarkers are the marker classes a value names.
func ProductionMarkers(value string, ms []Marker) []string {
	var out []string
	for _, m := range ms {
		if m.Re.MatchString(value) {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return uniq(out)
}

// DotenvKeys are the key names of a dotenv text.
func DotenvKeys(text string) []string {
	var keys []string
	for _, l := range splitLines(text) {
		if m := dotenvLine.FindStringSubmatch(l); m != nil {
			keys = append(keys, m[1])
		}
	}
	sort.Strings(keys)
	return uniq(keys)
}

func dotenvProdKeys(text string, ms []Marker) []string {
	var out []string
	for _, l := range splitLines(text) {
		if m := dotenvLine.FindStringSubmatch(l); m != nil && len(ProductionMarkers(m[2], ms)) > 0 {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return uniq(out)
}

// Variables reads the variables at path and drops every value at once (keys, scope, flags, markers only; the
// value of a routing key when it is a plain name).
func (g *GitLab) Variables(ctx context.Context, path string, ms []Marker) ([]MetaRow, error) {
	vs, err := g.All(ctx, path, 10, Q())
	if err != nil {
		return nil, err
	}
	var out []MetaRow
	for _, v := range vs {
		val := str(v["value"])
		r := MetaRow{Key: runeHead(str(v["key"]), 100), Scope: runeHead(orS(str(v["environment_scope"]), "*"), 60),
			Type: orS(str(v["variable_type"]), "env_var"), Empty: strings.TrimSpace(val) == "", Markers: ProductionMarkers(val, ms)}
		r.Masked, _ = v["masked"].(bool)
		r.Protected, _ = v["protected"].(bool)
		r.Prod = len(r.Markers) > 0
		if r.Markers == nil {
			r.Markers = []string{}
		}
		r.Dotenv, r.DotProd = []string{}, []string{}
		if strings.HasPrefix(r.Key, "DOTENV") {
			r.Dotenv, r.DotProd = orEmpty(DotenvKeys(val)), orEmpty(dotenvProdKeys(val, ms))
		}
		if inList(r.Key, RoutingKeys) && routingVal.MatchString(strings.TrimSpace(val)) {
			r.Value = strings.TrimSpace(val)
		}
		out = append(out, r)
	}
	return out, nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// CollectMeta builds meta.json: the variables of every group (and parent group) of the watched projects and of each
// watched project. It needs a token that may read CI variables; without one GitLab answers 403 and nothing is kept.
func (w *Watcher) CollectMeta(ms []Marker) (Meta, error) {
	meta := Meta{At: w.Opt.Now().Unix(), Groups: map[string][]MetaRow{}, Errors: []string{}}
	meta.Projects = map[string]struct {
		Path       string    `json:"path"`
		Rows       []MetaRow `json:"rows"`
		SampleKeys []string  `json:"sample_keys,omitempty"`
	}{}
	cons, order, err := w.consumers()
	if err != nil {
		return meta, err
	}
	seen := map[string]bool{}
	for _, pid := range order {
		p := cons[pid]
		for _, g := range parents(p.S("path")) {
			if seen[g] {
				continue
			}
			seen[g] = true
			rs, err := w.Opt.GitLab.Variables(w.ctx, "groups/"+Enc(g)+"/variables", ms)
			if err != nil {
				return meta, err
			}
			if rs != nil {
				meta.Groups[g] = rs
			}
		}
		st, _, _, err := w.Opt.GitLab.do(w.ctx, fmt.Sprintf("projects/%d/variables", pid), Q("per_page", "1"), false, 1024)
		if err != nil {
			return meta, err
		}
		if st != 200 {
			meta.Errors = append(meta.Errors, fmt.Sprintf("project %s: HTTP %d (the token cannot read CI variables)", p.S("path"), st))
			continue
		}
		rs, err := w.Opt.GitLab.Variables(w.ctx, fmt.Sprintf("projects/%d/variables", pid), ms)
		if err != nil {
			return meta, err
		}
		e := meta.Projects[fmt.Sprint(pid)]
		e.Path, e.Rows = p.S("path"), orRows(rs)
		meta.Projects[fmt.Sprint(pid)] = e
	}
	return meta, nil
}

func orRows(rs []MetaRow) []MetaRow {
	if rs == nil {
		return []MetaRow{}
	}
	return rs
}

// ErrSecretInMeta: the output looked like it carried a secret and was not written.
var ErrSecretInMeta = errors.New("ci-vars-meta: refusing to write: the output looks like it carries a secret")

// WriteMeta writes meta.json atomically (0644) unless it looks like it carries a secret.
func WriteMeta(path string, meta Meta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if s := string(b); strings.Contains(s, "glpat-") || strings.Contains(s, "-----BEGIN") || Redact(s) != s {
		return ErrSecretInMeta
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}
