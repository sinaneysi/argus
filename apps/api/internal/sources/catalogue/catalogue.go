// Package catalogue is the routing knowledge base of Data sources (ADR-0017 P2): which data source holds what for each
// service, built from the discovered inventory (internal/sources/discover) by a deterministic, explainable matcher.
//
// Every route carries the rule that made it and a reason sentence; nothing is guessed by a model.
//   - GitLab: the exact repository path of the service (1.0); otherwise the same repository name elsewhere (0.5).
//   - Names everywhere else (Kubernetes workloads, services, namespaces, Loki label values, Prometheus jobs, Sentry
//     projects, index patterns) are normalised: lower case, _ . / and spaces -> -, environment prefixes (stage-, prod-,
//     ... and the installation's own, services.json "prefixes") dropped, then one suffix (-api, -service, -svc, ...)
//     at a time, so shop/cart <-> cart, cart-api, cart-service, prod-shop-cart. group-repo beats repo; a generic
//     name (api, web, admin, ...) is never matched alone; a name that fits two services is not guessed but listed as
//     unmapped with "ambiguous: ..." unless one service's repository has exactly that name.
//   - Kubernetes app labels (app.kubernetes.io/name, app, ...) count more than object names; an ingress host that is
//     one of the service's domains counts most.
//   - A Grafana dashboard whose title names the service (whole words, 5+ characters) is a weak proposal.
//
// Status: discovered routes are proposed; an admin confirms or rejects them (the catalogue API, /api/v1/sources/catalogue).
// A rejected route is never proposed again unless its evidence (rule + reason) changes. Rows a person added or edited
// (origin human) are never touched by a build. A confirmed route discovery no longer sees becomes stale.
package catalogue

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Schema is the catalogue (ARGUS_DB, written by the agent's user).
const Schema = `CREATE TABLE IF NOT EXISTS source_routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT, service TEXT NOT NULL, source TEXT NOT NULL, kind TEXT NOT NULL,
  locator TEXT NOT NULL, label TEXT, confidence REAL, origin TEXT NOT NULL, status TEXT NOT NULL, rule TEXT,
  evidence TEXT, evidence_hash TEXT, stale INTEGER DEFAULT 0, created_at INTEGER, updated_at INTEGER, updated_by TEXT,
  UNIQUE (service, source, kind, locator));
CREATE INDEX IF NOT EXISTS source_routes_svc ON source_routes(service, status);
CREATE TABLE IF NOT EXISTS source_unmapped (
  source TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, label TEXT, note TEXT, PRIMARY KEY (source, kind, key));
CREATE TABLE IF NOT EXISTS source_catalogue_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER, services INTEGER, counts TEXT, unmapped INTEGER, note TEXT);
CREATE TABLE IF NOT EXISTS source_route_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER, by_user TEXT, action TEXT, route_id INTEGER, service TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS source_action_results (
  rid TEXT PRIMARY KEY, user TEXT, action TEXT, result TEXT, at INTEGER);`

// DB is what the catalogue needs of a database or a transaction.
type DB interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
	QueryRow(q string, args ...any) *sql.Row
}

// Migrate creates the tables.
func Migrate(db DB) error {
	_, err := db.Exec(Schema)
	return err
}

// DefaultPrefixes are environment words in front of a name; services.json "prefixes" adds the installation's own.
var DefaultPrefixes = []string{"pre-release-", "pre-", "stage-", "stg-", "staging-", "develop-", "dev-", "prod-",
	"production-", "release-", "main-"}

// Suffixes are dropped one at a time.
var Suffixes = []string{"-api", "-apis", "-service", "-svc", "-srv", "-ts", "-js", "-backend", "-server", "-app",
	"-deployment", "-deploy", "-prod", "-production", "-stage", "-staging", "-dev", "-develop", "-release", "-pre",
	"-main", "-multipack", "-multipackage"}

// Generic names are never matched alone.
var Generic = set("api", "web", "app", "apps", "admin", "backend", "frontend", "front", "server", "service", "services",
	"worker", "default", "kube-system", "kube-public", "kube-node-lease", "monitoring", "logging", "nginx", "redis",
	"mongo", "mongodb", "postgres", "mysql", "kafka", "node-exporter", "cadvisor", "prometheus", "loki", "grafana",
	"promtail", "alloy", "ingress-nginx", "cert-manager", "docker", "systemd", "kubelet", "core", "common", "test",
	"tests", "main", "node", "proxy", "gateway", "cron", "jobs", "job", "db", "cache")

var lokiConf = map[string]float64{"app": 0.75, "application": 0.75, "service": 0.75, "service_name": 0.75,
	"compose_service": 0.75, "container": 0.7, "container_name": 0.7, "job": 0.6, "unit": 0.6, "systemd_unit": 0.6,
	"namespace": 0.5}
var k8sLabelConf = map[string]float64{"app.kubernetes.io/name": 0.85, "app": 0.8, "k8s-app": 0.8,
	"app.kubernetes.io/instance": 0.75, "service": 0.75, "app.kubernetes.io/part-of": 0.6,
	"app.kubernetes.io/component": 0.55, "component": 0.55, "release": 0.6}

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var (
	unitRe   = regexp.MustCompile(`\.service$`)
	sepRe    = regexp.MustCompile(`[_.\s/]+`)
	esTailRe = regexp.MustCompile(`[-_.]?\*$`)
	esHeadRe = regexp.MustCompile(`^(logs?|filebeat|logstash|app|apps|k8s|docker)[-_.]`)
)

// --- names ---------------------------------------------------------------------------------------------------------

// Simple is lower case with _ . / and spaces as -, a trailing .service dropped.
func Simple(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = unitRe.ReplaceAllString(n, "")
	return strings.Trim(sepRe.ReplaceAllString(n, "-"), "-")
}

// Services is the service catalogue the matcher maps to.
type Services struct {
	Names    []string
	Path     map[string]string
	prefixes []string
	byPath   map[string]string
	byFull   map[string]map[string]bool
	byBase   map[string]map[string]bool
	byRaw    map[string]map[string]bool
	domains  map[string]map[string]bool
}

func (s *Services) unprefix(name string) string {
	n := Simple(name)
	for changed := true; changed; {
		changed = false
		for _, p := range s.prefixes {
			if strings.HasPrefix(n, p) && len(n) > len(p)+1 {
				n, changed = n[len(p):], true
			}
		}
	}
	return n
}

// forms: the name, then without its prefixes, then one suffix less at a time (each step a candidate).
func (s *Services) forms(name string) []string {
	out := []string{Simple(name)}
	n := s.unprefix(name)
	out = append(out, n)
	for changed := true; changed; {
		changed = false
		for _, x := range Suffixes {
			if strings.HasSuffix(n, x) && len(n) > len(x)+1 {
				n, changed = n[:len(n)-len(x)], true
				out = append(out, n)
				break
			}
		}
	}
	seen := map[string]bool{}
	var u []string
	for _, x := range out {
		if !seen[x] {
			seen[x] = true
			u = append(u, x)
		}
	}
	return u
}

// Norm is the name without prefixes and suffixes: cart-api, prod-shop-cart, CART_SERVICE -> cart, shop-cart, cart.
func (s *Services) Norm(name string) string {
	n := s.unprefix(name)
	for changed := true; changed; {
		changed = false
		for _, p := range s.prefixes {
			if strings.HasPrefix(n, p) && len(n) > len(p)+1 {
				n, changed = n[len(p):], true
			}
		}
		for _, x := range Suffixes {
			if strings.HasSuffix(n, x) && len(n) > len(x)+1 {
				n, changed = n[:len(n)-len(x)], true
			}
		}
	}
	return n
}

func split(svc string) (grp, repo string) {
	p := strings.Split(svc, "/")
	if len(p) == 1 {
		return "", p[0]
	}
	return p[len(p)-2], p[len(p)-1]
}

func add(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

// ServiceDef is one service of services.json.
type ServiceDef struct {
	GitLab  string   `json:"gitlab"`  // repository path (default: the service name)
	Domains []string `json:"domains"` // public host names (an ingress host among them routes it)
}

// NewServices builds the catalogue: service name (group/repo) -> its definition; extra prefixes of the installation.
func NewServices(defs map[string]ServiceDef, prefixes []string) *Services {
	s := &Services{Path: map[string]string{}, byPath: map[string]string{}, byFull: map[string]map[string]bool{},
		byBase: map[string]map[string]bool{}, byRaw: map[string]map[string]bool{}, domains: map[string]map[string]bool{}}
	for _, p := range prefixes {
		if p = Simple(p); p != "" {
			s.prefixes = append(s.prefixes, strings.TrimSuffix(p, "-")+"-")
		}
	}
	s.prefixes = append(s.prefixes, DefaultPrefixes...)
	for k := range defs {
		s.Names = append(s.Names, k)
	}
	sort.Strings(s.Names)
	for _, svc := range s.Names {
		d := defs[svc]
		path := d.GitLab
		if path == "" {
			path = svc
		}
		s.Path[svc] = path
		s.byPath[strings.ToLower(path)] = svc
		grp, repo := split(svc)
		if grp != "" {
			add(s.byFull, Simple(grp+"-"+repo), svc)
			add(s.byFull, Simple(grp)+"-"+s.Norm(repo), svc)
		}
		add(s.byBase, s.Norm(repo), svc)
		add(s.byRaw, Simple(repo), svc)
		for _, h := range d.Domains {
			add(s.domains, strings.ToLower(strings.TrimSpace(h)), svc)
		}
	}
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Domain is the services that have this public host name among their domains.
func (s *Services) Domain(host string) []string {
	return keys(s.domains[strings.ToLower(strings.TrimSpace(host))])
}

// Resolve is (services, how) for one name: how is full | exact-name | name; no services when nothing fits.
func (s *Services) Resolve(candidate string) ([]string, string) {
	raw, n := Simple(candidate), s.Norm(candidate)
	if len(n) < 3 {
		return nil, ""
	}
	steps := s.forms(candidate)
	for _, k := range steps { // least stripped first: seller-portal-api before seller-portal
		if hit := s.byFull[k]; len(hit) > 0 {
			return keys(hit), "full"
		}
	}
	if Generic[raw] {
		return nil, ""
	}
	for _, r := range steps {
		if hit := s.byRaw[r]; len(hit) == 1 && !Generic[r] {
			return keys(hit), "exact-name"
		}
	}
	if Generic[n] {
		return nil, ""
	}
	hit := s.byBase[n]
	if len(hit) > 1 && len(s.byRaw[raw]) > 0 {
		return keys(s.byRaw[raw]), "exact-name"
	}
	return keys(hit), "name"
}

// --- loading the services -----------------------------------------------------------------------------------------

// ServicesFile is services.json: {"prefixes": ["acme-"], "services": {"shop/cart": {"gitlab": "shop/cart",
// "domains": ["cart.example.com"]}}}.
type ServicesFile struct {
	Prefixes []string              `json:"prefixes"`
	Services map[string]ServiceDef `json:"services"`
}

// LoadServices reads the service catalogue from file; when it has none, the application projects of the CI/CD
// estate in db (cicd_projects kind app, not archived; service = path). The second value says where it came from.
func LoadServices(file string, db DB) (*Services, string, error) {
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			var f ServicesFile
			if err := json.Unmarshal(b, &f); err != nil {
				return nil, "", fmt.Errorf("%s: %v", file, err)
			}
			if len(f.Services) > 0 {
				return NewServices(f.Services, f.Prefixes), file, nil
			}
		}
	}
	defs := map[string]ServiceDef{}
	if db != nil {
		if q, err := db.Query(`SELECT path FROM cicd_projects WHERE COALESCE(kind,'app')='app' AND COALESCE(archived,0)=0`); err == nil {
			for q.Next() {
				var p string
				if q.Scan(&p) == nil && p != "" {
					defs[p] = ServiceDef{GitLab: p}
				}
			}
			q.Close()
		}
	}
	return NewServices(defs, nil), "cicd_projects", nil
}

// --- the matcher (pure) -------------------------------------------------------------------------------------------

// Match is one route proposal.
type Match struct {
	Service    string
	Confidence float64
	Rule, Why  string
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func conf(base float64, how string) float64 {
	switch how {
	case "full":
		return round2(math.Min(0.95, base+0.1))
	case "exact-name":
		return round2(base)
	}
	return round2(math.Max(0.3, base-0.05))
}

func (s *Services) whyName(what, value, how, svc string) string {
	sep := " "
	if strings.HasSuffix(what, "=") {
		sep = ""
	}
	thing := what + sep + value
	_, repo := split(svc)
	switch how {
	case "full":
		return thing + " matches the service's group and repository (" + svc + ")"
	case "exact-name":
		if Simple(value) == Simple(repo) {
			return thing + " matches the repository name " + repo
		}
		return thing + " matches the repository name " + repo + " once prefixes and suffixes are dropped"
	}
	return thing + " matches " + repo + " once prefixes and suffixes are dropped (" + s.Norm(value) + ")"
}

// Locator is (where, label) a person reads: a LogQL selector for Loki, a job selector, the dashboard uid with its
// folder and title, the ingress with its hosts; else the key.
func Locator(kind, key string, attrs map[string]any) (string, string) {
	str := func(k string) string { s, _ := attrs[k].(string); return s }
	switch kind {
	case "loki":
		label, value := str("label"), str("value")
		if label == "" {
			label, value, _ = strings.Cut(key, "=")
		}
		if attrs["prefix"] == true {
			v := lokiEscRe.ReplaceAllString(value, "[$1]")
			return fmt.Sprintf(`{%s=~"%s[.].*"}`, label, v), "every task of the Swarm service"
		}
		return fmt.Sprintf(`{%s="%s"}`, label, value), ""
	case "prom-job":
		return fmt.Sprintf(`{job="%s"}`, key), ""
	case "dashboard":
		f := str("folder")
		if f == "" {
			f = "General"
		}
		t := str("title")
		if t == "" {
			t = key
		}
		return key, f + " / " + t
	case "ingress":
		var hs []string
		switch h := attrs["hosts"].(type) {
		case []any:
			for _, x := range h {
				if s, ok := x.(string); ok {
					hs = append(hs, s)
				}
			}
		case []string:
			hs = h
		}
		l := strings.Join(hs, ", ")
		if len(l) > 120 {
			l = l[:120]
		}
		return key, l
	}
	return key, ""
}

func strMap(v any) map[string]string {
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]any:
		for k, x := range m {
			if s, ok := x.(string); ok {
				out[k] = s
			}
		}
	case map[string]string:
		return m
	}
	return out
}

func strList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		var out []string
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MatchItem is the routes one inventory item proposes, or none with a note ("ambiguous: ...").
func (s *Services) MatchItem(srcType, kind, key string, attrs map[string]any) ([]Match, string) {
	if attrs == nil {
		attrs = map[string]any{}
	}
	str := func(k string) string { x, _ := attrs[k].(string); return x }
	best := map[string]Match{}
	offer := func(svc string, c float64, rule, why string) {
		if b, ok := best[svc]; !ok || c > b.Confidence {
			best[svc] = Match{svc, c, rule, why}
		}
	}
	var amb []string
	byName := func(value string, base float64, rule, what string) {
		hit, how := s.Resolve(value)
		if len(hit) == 1 {
			offer(hit[0], conf(base, how), rule, s.whyName(what, value, how, hit[0]))
		} else if len(hit) > 1 {
			if len(hit) > 4 {
				hit = hit[:4]
			}
			amb = append(amb, what+" "+value+" fits "+strings.Join(hit, ", "))
		}
	}
	switch {
	case srcType == "gitlab" && kind == "project":
		if svc := s.byPath[strings.ToLower(key)]; svc != "" {
			return []Match{{svc, 1.0, "gitlab-path", "exact repository path of the service"}}, ""
		}
		parts := strings.Split(key, "/")
		if len(parts) >= 2 {
			byName(parts[len(parts)-2]+"-"+parts[len(parts)-1], 0.5, "gitlab-name", "repository")
		}
		if len(best) == 0 {
			byName(parts[len(parts)-1], 0.5, "gitlab-name", "repository")
		}
	case srcType == "grafana" && kind == "loki":
		label, value := str("label"), str("value")
		if label == "" {
			label, value, _ = strings.Cut(key, "=")
		}
		c, ok := lokiConf[label]
		if !ok {
			c = 0.5
		}
		byName(value, c, "loki-"+label, "Loki label "+label+"=")
	case srcType == "grafana" && kind == "prom-job":
		byName(key, 0.6, "prom-job", "Prometheus job")
	case srcType == "grafana" && kind == "dashboard":
		title := Simple(str("title"))
		type hit struct {
			svc, cand string
			c         float64
		}
		var hits []hit
		svcs := map[string]bool{}
		for _, svc := range s.Names {
			grp, repo := split(svc)
			for _, cc := range []struct {
				cand string
				c    float64
			}{{Simple(grp + "-" + repo), 0.55}, {s.Norm(repo), 0.5}, {Simple(repo), 0.5}} {
				if len(cc.cand) >= 5 && !Generic[cc.cand] && wholeWord(title, cc.cand) {
					hits = append(hits, hit{svc, cc.cand, cc.c})
					svcs[svc] = true
					break
				}
			}
		}
		if len(svcs) == 1 {
			offer(hits[0].svc, hits[0].c, "dashboard-title", fmt.Sprintf("dashboard title %q names %s", str("title"), hits[0].cand))
		} else if len(svcs) > 1 {
			k := keys(svcs)
			if len(k) > 4 {
				k = k[:4]
			}
			amb = append(amb, "dashboard title fits "+strings.Join(k, ", "))
		}
	case srcType == "kubernetes":
		labels := strMap(attrs["labels"])
		for _, lk := range sortedKeys(labels) {
			c, ok := k8sLabelConf[lk]
			if !ok {
				c = 0.55
			}
			byName(labels[lk], c, "k8s-label", "label "+lk+"=")
		}
		sel := strMap(attrs["selector"])
		for _, lk := range sortedKeys(sel) {
			c, ok := k8sLabelConf[lk]
			if !ok {
				c = 0.55
			}
			byName(sel[lk], c-0.05, "k8s-selector", "selector "+lk+"=")
		}
		if kind == "ingress" {
			for _, h := range strList(attrs["hosts"]) {
				for _, svc := range keys(s.domains[strings.ToLower(h)]) {
					offer(svc, 0.9, "ingress-host", "ingress host "+h+" is a domain of the service")
				}
			}
			for _, b := range strList(attrs["backends"]) {
				byName(b, 0.6, "k8s-name", "backend service")
			}
		}
		name := str("name")
		if name == "" {
			name = key[strings.LastIndex(key, "/")+1:]
		}
		c := map[string]float64{"workload": 0.75, "service": 0.65, "namespace": 0.6, "ingress": 0.6}[kind]
		if c == 0 {
			c = 0.5
		}
		what := map[string]string{"service": "Service", "namespace": "namespace", "ingress": "ingress"}[kind]
		if kind == "workload" {
			what = str("type")
			if what == "" {
				what = "workload"
			}
		}
		if what == "" {
			what = kind
		}
		byName(name, c, "k8s-name", what)
	case srcType == "sentry" && kind == "project":
		slug := str("slug")
		if slug == "" {
			slug = key[strings.LastIndex(key, "/")+1:]
		}
		byName(slug, 0.8, "sentry-project", "Sentry project")
	case srcType == "elasticsearch" && kind == "index":
		v := esHeadRe.ReplaceAllString(esTailRe.ReplaceAllString(key, ""), "")
		byName(v, 0.6, "index-name", "index pattern "+key+" →")
	}
	var out []Match
	for _, svc := range sortedMatchKeys(best) {
		out = append(out, best[svc])
	}
	note := ""
	if len(amb) > 0 && len(out) == 0 {
		note = "ambiguous: " + strings.Join(amb, "; ")
		if len(note) > 300 {
			note = note[:300]
		}
	}
	return out, note
}

// wholeWord: w appears in s with no letter or digit right before or after it.
func wholeWord(s, w string) bool {
	alnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		if (j == 0 || !alnum(s[j-1])) && (j+len(w) == len(s) || !alnum(s[j+len(w)])) {
			return true
		}
		i = j + 1
	}
}

var lokiEscRe = regexp.MustCompile(`([.+])`)

func sortedMatchKeys(m map[string]Match) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EvidenceHash identifies the evidence of a route: a rejected route comes back only when it changes.
func EvidenceHash(rule, why string) string {
	h := sha256.Sum256([]byte(rule + "|" + why))
	return hex.EncodeToString(h[:])[:16]
}
