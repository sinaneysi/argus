// Package discover is Data sources discovery (ADR-0017 P2): what each read-only data source holds, by name only.
//
// For every source the registry enables for the agent (purpose agent, verified read-only by root), it lists the things
// a service can live in, within budgets, and keeps them in ARGUS_DB source_inventory; every attempt is a row of
// source_discovery_runs. Names and metadata only: never a secret, a log line, a metric value or a document body.
//
//	gitlab         projects (path, id, default branch, last activity, archived, topics); incremental
//	               (last_activity_after) between full sweeps (FullEvery)
//	kubernetes     namespaces, deployments / statefulsets / daemonsets (app labels), services, ingresses (hosts), through
//	               the agent's read-only kube tool (internal/kube Inventory: enabled cluster, audited)
//	grafana        Loki label values of the app-naming labels that exist (Swarm task names folded to their service,
//	               session scopes dropped), Prometheus job values, dashboards (uid, title, folder)
//	sentry         projects (org/slug, platform), cursor pagination
//	elasticsearch  index patterns (dated and rollover indices folded) and data streams
//
// Credentials are the ones the agent's tools already read: a registry entry's <dir>/<type>/<name>.cred (root makes it
// readable to the agent's group only while enabled), or a legacy env:/secret: reference. A credential goes only into a
// request header and is never stored, logged or printed.
//
// Budgets per source: calls, wall time, pages and items per kind. A run that hits a cap is "partial" and prunes
// nothing; a complete full run removes what is gone. A source discovered less than MinGap ago is skipped unless forced.
package discover

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/kube"
	"github.com/sinaneysi/argus/internal/sources"
	_ "modernc.org/sqlite"
)

// Schema is the inventory and the run log (ARGUS_DB, written by the agent's user).
const Schema = `CREATE TABLE IF NOT EXISTS source_inventory (
  source TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, attrs TEXT, first_seen INTEGER, seen_at INTEGER,
  PRIMARY KEY (source, kind, key));
CREATE TABLE IF NOT EXISTS source_discovery_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL, started_at INTEGER, finished_at INTEGER,
  status TEXT, mode TEXT, trigger TEXT, counts TEXT, calls INTEGER, error TEXT);
CREATE INDEX IF NOT EXISTS source_discovery_runs_src ON source_discovery_runs(source, started_at);`

// Types are the types discovery reads, and the kinds each one writes (a complete full run prunes only these).
var Kinds = map[string][]string{
	"gitlab":        {"project"},
	"grafana":       {"loki", "prom-job", "dashboard"},
	"kubernetes":    {"namespace", "workload", "service", "ingress"},
	"sentry":        {"project"},
	"elasticsearch": {"index"},
}

// Caps per source and kind.
type Caps struct {
	GitLabPages, GitLabProjects, LokiValues, LokiDatasources, PromDatasources, PromJobs, Dashboards int
	SentryPages, SentryProjects, ESIndices, ESPatterns, KubeObjects                                 int
}

// DefaultCaps are generous for a few thousand services and small enough for one run.
var DefaultCaps = Caps{GitLabPages: 60, GitLabProjects: 6000, LokiValues: 5000, LokiDatasources: 3, PromDatasources: 3,
	PromJobs: 3000, Dashboards: 5000, SentryPages: 40, SentryProjects: 4000, ESIndices: 20000, ESPatterns: 3000, KubeObjects: 20000}

// Getter is one guarded GET (sources.Get); tests pass a fake.
type Getter func(base, path string, hdr map[string]string, maxBody int64, timeout time.Duration) (int, any, http.Header, error)

// Options say what to discover and where everything is.
type Options struct {
	DB         string                    // ARGUS_DB (absolute)
	Sources    map[string]*sources.Entry // the registry as root published it (sources-status.json)
	SourcesDir string                    // ARGUS_SOURCES_DIR: <type>/<name>.cred
	SecretsDir string                    // ARGUS_SECRETS_DIR: legacy secret: references
	Kube       *kube.Tool                // the agent's read-only kube tool (nil: Kubernetes sources are an error)
	Get        Getter                    // nil: sources.Get
	Now        func() time.Time
	Calls      int           // per source (default 150)
	Seconds    time.Duration // per source (default 240 s)
	MinGap     time.Duration // skip a source discovered this recently (default 1 h) unless forced
	FullEvery  time.Duration // GitLab full sweep interval (default 24 h)
	Timeout    time.Duration // per call (default 20 s)
	MaxBody    int64         // per answer (default 8 MB)
	Caps       *Caps

	defaulted, fakeGet bool
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) defaults() {
	if o.Get == nil {
		o.Get = sources.Get
	} else if !o.defaulted {
		o.fakeGet = true
	}
	o.defaulted = true
	if o.Calls == 0 {
		o.Calls = 150
	}
	if o.Seconds == 0 {
		o.Seconds = 240 * time.Second
	}
	if o.MinGap == 0 {
		o.MinGap = time.Hour
	}
	if o.FullEvery == 0 {
		o.FullEvery = 24 * time.Hour
	}
	if o.Timeout == 0 {
		o.Timeout = 20 * time.Second
	}
	if o.MaxBody == 0 {
		o.MaxBody = 8 << 20
	}
	if o.Caps == nil {
		c := DefaultCaps
		o.Caps = &c
	}
}

// Item is one inventory row.
type Item struct {
	Kind, Key string
	Attrs     map[string]any
}

// Run is one source's discovery attempt.
type Run struct {
	Source, Status, Mode, Trigger, Error string // status ok | partial | error | skipped
	Counts                               map[string]int
	Calls                                int
	StartedAt, FinishedAt                int64
}

// --- errors ------------------------------------------------------------------------------------------------------

// budget: a cap was hit; what was found is kept and nothing is pruned.
type budget string

func (b budget) Error() string { return string(b) }

// refused: the source could not be read at all (login refused, HTTP error, no credential).
type refused string

func (r refused) Error() string { return string(r) }

// --- the budgeted client ----------------------------------------------------------------------------------------

type client struct {
	o        *Options
	base     string
	hdr      map[string]string
	left     int
	calls    int
	deadline time.Time
}

func (o *Options) client(base string, hdr map[string]string) (*client, error) {
	u, err := sources.CleanURL(base) // https only; sources.Get checks the address again before every call
	if err != nil && !o.fakeGet {
		return nil, refused("the address is refused: " + err.Error())
	}
	if err != nil {
		u = strings.TrimRight(base, "/") // a test's fake on http://127.0.0.1
	}
	return &client{o: o, base: u, hdr: hdr, left: o.Calls, deadline: o.now().Add(o.Seconds)}, nil
}

func (c *client) get(path string, q url.Values) (int, any, http.Header, error) {
	if c.left <= 0 {
		return 0, nil, nil, budget(fmt.Sprintf("call budget used (%d calls)", c.calls))
	}
	if c.o.now().After(c.deadline) {
		return 0, nil, nil, budget("time budget used")
	}
	c.left--
	c.calls++
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	st, d, h, err := c.o.Get(c.base, path, c.hdr, c.o.MaxBody, c.o.Timeout)
	if errors.Is(err, sources.ErrTooLarge) {
		return 0, nil, nil, budget(fmt.Sprintf("an answer was larger than %d MB", c.o.MaxBody>>20))
	}
	if err != nil {
		return 0, nil, nil, refused(sources.NetError(err))
	}
	if h == nil {
		h = http.Header{}
	}
	return st, d, h, nil
}

func okOr(st int, what string) error {
	if st == 401 || st == 403 {
		return refused(fmt.Sprintf("%s: the login was refused (HTTP %d)", what, st))
	}
	if st != 200 {
		return refused(fmt.Sprintf("%s: HTTP %d", what, st))
	}
	return nil
}

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+=-]{0,250}$`)
	valueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,200}$`)
	// a Docker Swarm task: <stack>_<service>.<slot>.<task id>
	taskSuffix = regexp.MustCompile(`\.\d{1,4}\.[a-z0-9]{20,32}$`)
	noise      = regexp.MustCompile(`^(session-\d+\.scope|user@\d+\.service|user-\d+\.slice|[0-9a-f]{32,64}|run-[a-z0-9-]+\.scope)$`)
	dated      = regexp.MustCompile(`([-_.])(\d{4}[-_.]\d{2}([-_.]\d{2})?|\d{8}|\d{6})$`)
	nextLink   = regexp.MustCompile(`<[^>]*[?&]cursor=([^&>]+)[^>]*>;\s*rel="next";\s*results="true"`)
)

func clean(v any, re *regexp.Regexp) string {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if re.MatchString(s) {
		return s
	}
	return ""
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// FoldValue is a Loki label value to keep: (value, true when it stands for every task of a Swarm service), "" for
// noise (session scopes, user slices, container ids).
func FoldValue(v any) (string, bool) {
	s := clean(v, valueRe)
	if s == "" || noise.MatchString(s) {
		return "", false
	}
	f := taskSuffix.ReplaceAllString(s, "")
	return f, f != s
}

// IndexPattern folds a dated or rollover index into its pattern: logs-shop-2026.11.03 -> logs-shop-*,
// orders-000012 -> orders-*; a plain name stays.
func IndexPattern(name string) string {
	if m := dated.FindStringIndex(name); m != nil {
		return name[:m[0]+1] + "*"
	}
	return name
}

// --- discoverers: (items, complete, mode, notes, error) -----------------------------------------------------------

type result struct {
	items    []Item
	complete bool
	mode     string
	notes    []string
}

func (o *Options) gitlab(c *client, lastFull int64) (result, error) {
	now := o.now()
	full := lastFull == 0 || now.Sub(time.Unix(lastFull, 0)) >= o.FullEvery
	base := url.Values{"simple": {"true"}, "per_page": {"100"}, "order_by": {"id"}, "sort": {"asc"}}
	if !full {
		base.Set("last_activity_after", time.Unix(lastFull-3600, 0).UTC().Format("2006-01-02T15:04:05Z"))
	}
	items := map[string]map[string]any{}
	archived := map[string]bool{}
	r := result{complete: true, mode: map[bool]string{true: "full", false: "incremental"}[full]}
	collect := func() error {
		for _, arch := range []bool{false, true} {
			page := 1
			for {
				if page > o.Caps.GitLabPages {
					return budget("GitLab page cap")
				}
				q := url.Values{}
				for k, v := range base {
					q[k] = v
				}
				q.Set("page", strconv.Itoa(page))
				if arch {
					q.Set("archived", "true")
				}
				st, d, h, err := c.get("/api/v4/projects", q)
				if err != nil {
					return err
				}
				if err := okOr(st, "GitLab projects"); err != nil {
					return err
				}
				rows, _ := d.([]any)
				for _, x := range rows {
					p, _ := x.(map[string]any)
					path := clean(p["path_with_namespace"], nameRe)
					if path == "" {
						continue
					}
					if arch {
						archived[path] = true
						continue
					}
					topics := []string{}
					tl, _ := p["topics"].([]any)
					if len(tl) == 0 {
						tl, _ = p["tag_list"].([]any)
					}
					for _, t := range tl {
						if s := clean(t, valueRe); s != "" && len(topics) < 10 {
							topics = append(topics, s)
						}
					}
					var id any
					if f, ok := p["id"].(float64); ok {
						id = int64(f)
					}
					la, _ := p["last_activity_at"].(string)
					items[path] = map[string]any{"id": id, "default_branch": clean(p["default_branch"], nameRe),
						"last_activity": cut(la, 20), "topics": topics, "archived": false}
					if len(items) > o.Caps.GitLabProjects {
						return budget("GitLab project cap")
					}
				}
				if nxt, ok := h["X-Next-Page"]; ok { // GitLab says where the next page is ("" on the last one)
					n, err := strconv.Atoi(strings.TrimSpace(strings.Join(nxt, "")))
					if err != nil || n <= page {
						break
					}
					page = n
				} else if len(rows) < 100 { // header left out (very large lists): a short page is the last
					break
				} else {
					page++
				}
			}
		}
		return nil
	}
	err := collect()
	var b budget
	if errors.As(err, &b) {
		r.complete = false
		r.notes = append(r.notes, string(b))
	} else if err != nil {
		return r, err
	}
	for p := range archived {
		if a := items[p]; a != nil {
			a["archived"] = true
		}
	}
	for p, a := range items {
		r.items = append(r.items, Item{"project", p, a})
	}
	return r, nil
}

// LokiLabels are the Loki labels that usually name an application; only those that exist are read.
var LokiLabels = []string{"app", "application", "service", "service_name", "container", "container_name",
	"compose_service", "job", "namespace", "unit", "systemd_unit"}

type gds struct{ name, typ, uid string }

func (o *Options) grafanaDatasources(c *client) ([]gds, error) {
	var out []gds
	add := func(name string, x map[string]any) {
		t, _ := x["type"].(string)
		if uid := clean(x["uid"], nameRe); uid != "" && (t == "loki" || t == "prometheus") {
			out = append(out, gds{cut(name, 80), t, uid})
		}
	}
	st, d, _, err := c.get("/api/frontend/settings", nil)
	if err != nil {
		return nil, err
	}
	if m, ok := d.(map[string]any); st == 200 && ok {
		if ds, ok := m["datasources"].(map[string]any); ok {
			names := make([]string, 0, len(ds))
			for k := range ds {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				if x, ok := ds[k].(map[string]any); ok {
					add(k, x)
				}
			}
			return out, nil
		}
	}
	st, d, _, err = c.get("/api/datasources", nil)
	if err != nil {
		return nil, err
	}
	if err := okOr(st, "Grafana datasources"); err != nil {
		return nil, err
	}
	l, _ := d.([]any)
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			n, _ := m["name"].(string)
			add(n, m)
		}
	}
	return out, nil
}

func (o *Options) grafana(c *client, _ int64) (result, error) {
	r := result{complete: true, mode: "full"}
	idx := map[string]int{}
	add := func(kind, key string, attrs map[string]any) {
		k := kind + "\x00" + key
		if i, ok := idx[k]; ok {
			old, _ := r.items[i].Attrs["datasources"].([]string)
			nw, _ := attrs["datasources"].([]string)
			seen := map[string]bool{}
			var all []string
			for _, x := range append(old, nw...) {
				if !seen[x] {
					seen[x] = true
					all = append(all, x)
				}
			}
			sort.Strings(all)
			r.items[i].Attrs["datasources"] = all
			return
		}
		idx[k] = len(r.items)
		r.items = append(r.items, Item{kind, key, attrs})
	}
	err := o.grafanaRead(c, add, &r)
	var b budget
	if errors.As(err, &b) {
		r.complete = false
		r.notes = append(r.notes, string(b))
	} else if err != nil {
		return r, err
	}
	if len(r.items) == 0 && !r.complete {
		return r, refused(strings.Join(r.notes, "; "))
	}
	return r, nil
}

func (o *Options) grafanaRead(c *client, add func(string, string, map[string]any), r *result) error {
	dss, err := o.grafanaDatasources(c)
	if err != nil {
		return err
	}
	var lokis, proms []gds
	for _, d := range dss {
		if d.typ == "loki" {
			lokis = append(lokis, d)
		} else {
			proms = append(proms, d)
		}
	}
	if len(lokis) > o.Caps.LokiDatasources || len(proms) > o.Caps.PromDatasources {
		r.complete = false
		r.notes = append(r.notes, "more datasources than the cap; the first ones were read")
		if len(lokis) > o.Caps.LokiDatasources {
			lokis = lokis[:o.Caps.LokiDatasources]
		}
		if len(proms) > o.Caps.PromDatasources {
			proms = proms[:o.Caps.PromDatasources]
		}
	}
	start := url.Values{"start": {strconv.FormatInt(o.now().Add(-24*time.Hour).UnixNano(), 10)}}
	for _, ds := range lokis {
		px := "/api/datasources/proxy/uid/" + url.PathEscape(ds.uid) + "/loki/api/v1"
		st, d, _, err := c.get(px+"/labels", start)
		if err != nil {
			return err
		}
		m, ok := d.(map[string]any)
		if st != 200 || !ok {
			r.complete = false
			r.notes = append(r.notes, fmt.Sprintf("Loki %s: labels HTTP %d", ds.name, st))
			continue
		}
		have := map[string]bool{}
		l, _ := m["data"].([]any)
		for _, x := range l {
			if s, ok := x.(string); ok {
				have[s] = true
			}
		}
		for _, label := range LokiLabels {
			if !have[label] {
				continue
			}
			st, v, _, err := c.get(px+"/label/"+label+"/values", start)
			if err != nil {
				return err
			}
			vm, ok := v.(map[string]any)
			if st != 200 || !ok {
				r.complete = false
				r.notes = append(r.notes, fmt.Sprintf("Loki %s: %s values HTTP %d", ds.name, label, st))
				continue
			}
			folded := map[string]bool{}
			vl, _ := vm["data"].([]any)
			for _, x := range vl {
				if f, task := FoldValue(x); f != "" {
					folded[f] = folded[f] || task
				}
			}
			vals := make([]string, 0, len(folded))
			for k := range folded {
				vals = append(vals, k)
			}
			sort.Strings(vals)
			if len(vals) > o.Caps.LokiValues {
				r.complete = false
				r.notes = append(r.notes, fmt.Sprintf("Loki %s: %s has more than %d values", ds.name, label, o.Caps.LokiValues))
				vals = vals[:o.Caps.LokiValues]
			}
			for _, x := range vals {
				a := map[string]any{"label": label, "value": x, "datasources": []string{ds.name}}
				if folded[x] {
					a["prefix"] = true
				}
				add("loki", label+"="+x, a)
			}
		}
	}
	for _, ds := range proms {
		st, v, _, err := c.get("/api/datasources/proxy/uid/"+url.PathEscape(ds.uid)+"/api/v1/label/job/values", nil)
		if err != nil {
			return err
		}
		vm, ok := v.(map[string]any)
		if st != 200 || !ok {
			r.complete = false
			r.notes = append(r.notes, fmt.Sprintf("Prometheus %s: job values HTTP %d", ds.name, st))
			continue
		}
		var vals []string
		vl, _ := vm["data"].([]any)
		for _, x := range vl {
			if s := clean(x, valueRe); s != "" {
				vals = append(vals, s)
			}
		}
		sort.Strings(vals)
		if len(vals) > o.Caps.PromJobs {
			r.complete = false
			vals = vals[:o.Caps.PromJobs]
		}
		for _, x := range vals {
			add("prom-job", x, map[string]any{"job": x, "datasources": []string{ds.name}})
		}
	}
	st, d, _, err := c.get("/api/search", url.Values{"type": {"dash-db"}, "limit": {strconv.Itoa(o.Caps.Dashboards)}})
	if err != nil {
		return err
	}
	l, ok := d.([]any)
	if st != 200 || !ok {
		r.complete = false
		r.notes = append(r.notes, fmt.Sprintf("dashboards HTTP %d", st))
		return nil
	}
	if len(l) >= o.Caps.Dashboards {
		r.complete = false
	}
	for _, x := range l {
		m, _ := x.(map[string]any)
		if uid := clean(m["uid"], nameRe); uid != "" {
			title, _ := m["title"].(string)
			folder, _ := m["folderTitle"].(string)
			if folder == "" {
				folder = "General"
			}
			add("dashboard", uid, map[string]any{"title": cut(title, 120), "folder": cut(folder, 80)})
		}
	}
	return nil
}

func (o *Options) sentry(c *client, _ int64) (result, error) {
	r := result{complete: true, mode: "full"}
	st, orgs, _, err := c.get("/api/0/organizations/", nil)
	if err != nil {
		return r, err
	}
	if err := okOr(st, "Sentry organizations"); err != nil {
		return r, err
	}
	ol, _ := orgs.([]any)
	read := func() error {
		for _, x := range ol {
			om, _ := x.(map[string]any)
			org := clean(om["slug"], valueRe)
			if org == "" {
				continue
			}
			cursor, pages := "", 0
			for {
				pages++
				if pages > o.Caps.SentryPages {
					return budget("Sentry page cap")
				}
				var q url.Values
				if cursor != "" {
					q = url.Values{"cursor": {cursor}}
				}
				st, d, h, err := c.get("/api/0/organizations/"+url.PathEscape(org)+"/projects/", q)
				if err != nil {
					return err
				}
				if err := okOr(st, "Sentry projects of "+org); err != nil {
					return err
				}
				l, _ := d.([]any)
				for _, p := range l {
					pm, _ := p.(map[string]any)
					if slug := clean(pm["slug"], valueRe); slug != "" {
						plat, _ := pm["platform"].(string)
						r.items = append(r.items, Item{"project", org + "/" + slug, map[string]any{"org": org, "slug": slug,
							"platform": cut(plat, 40)}})
					}
				}
				if len(r.items) > o.Caps.SentryProjects {
					return budget("Sentry project cap")
				}
				m := nextLink.FindStringSubmatch(h.Get("Link"))
				if m == nil {
					break
				}
				cursor, _ = url.QueryUnescape(m[1])
			}
		}
		return nil
	}
	err = read()
	var b budget
	if errors.As(err, &b) {
		r.complete = false
		r.notes = append(r.notes, string(b))
	} else if err != nil {
		return r, err
	}
	return r, nil
}

func (o *Options) elasticsearch(c *client, _ int64) (result, error) {
	r := result{complete: true, mode: "full"}
	st, d, _, err := c.get("/_cat/indices", url.Values{"format": {"json"}, "h": {"index"}, "expand_wildcards": {"open"}})
	if err != nil {
		return r, err
	}
	if err := okOr(st, "indices"); err != nil {
		return r, err
	}
	rows, _ := d.([]any)
	if len(rows) > o.Caps.ESIndices {
		r.complete = false
		r.notes = append(r.notes, fmt.Sprintf("more than %d indices", o.Caps.ESIndices))
		rows = rows[:o.Caps.ESIndices]
	}
	pats := map[string]int{}
	for _, x := range rows {
		m, _ := x.(map[string]any)
		if n := clean(m["index"], valueRe); n != "" && !strings.HasPrefix(n, ".") {
			pats[IndexPattern(n)]++
		}
	}
	keys := make([]string, 0, len(pats))
	for k := range pats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
		r.items = append(r.items, Item{"index", k, map[string]any{"indices": pats[k], "data_stream": false}})
	}
	st, d, _, err = c.get("/_data_stream", nil)
	if err != nil {
		var b budget
		if !errors.As(err, &b) {
			return r, err
		}
		r.complete = false
		r.notes = append(r.notes, string(b))
	} else if m, ok := d.(map[string]any); st == 200 && ok {
		l, _ := m["data_streams"].([]any)
		for _, x := range l {
			xm, _ := x.(map[string]any)
			n := clean(xm["name"], valueRe)
			if n == "" || strings.HasPrefix(n, ".") || seen[n] {
				continue
			}
			seen[n] = true
			il, _ := xm["indices"].([]any)
			r.items = append(r.items, Item{"index", n, map[string]any{"indices": len(il), "data_stream": true}})
		}
	}
	if len(r.items) > o.Caps.ESPatterns {
		r.complete = false
		r.notes = append(r.notes, fmt.Sprintf("more than %d patterns", o.Caps.ESPatterns))
		r.items = r.items[:o.Caps.ESPatterns]
	}
	return r, nil
}

func (o *Options) kubernetes(e *sources.Entry) (result, int, error) {
	if o.Kube == nil {
		return result{}, 0, refused("the read-only kube tool is not configured")
	}
	objs, complete, notes, err := o.Kube.Inventory(e.Name, o.Caps.KubeObjects)
	if err != nil {
		return result{}, 1, refused("kube inventory: " + err.Error())
	}
	r := result{complete: complete, mode: "full", notes: notes}
	for _, x := range objs {
		r.items = append(r.items, Item{x.Kind, x.Key, x.Attrs})
	}
	return r, len(kube.InventoryTypes), nil
}

// --- what to discover, credentials ------------------------------------------------------------------------------

// Eligible are the sources the agent may read: a discovered type, purpose agent, enabled by root.
func Eligible(all map[string]*sources.Entry) map[string]*sources.Entry {
	out := map[string]*sources.Entry{}
	for k, e := range all {
		if e == nil || Kinds[e.Type] == nil || e.Purpose != "agent" || !e.Enabled || k != e.Type+"/"+e.Name {
			continue
		}
		out[k] = e
	}
	return out
}

// credential is what the agent's own tools read for this source ("" when it is not readable for this user).
func (o *Options) credential(e *sources.Entry) string {
	kind, ref, _ := strings.Cut(e.CredRef, ":")
	switch kind {
	case "env":
		return strings.TrimSpace(getenv(ref))
	case "secret":
		if o.SecretsDir == "" {
			return ""
		}
		return sources.ReadSecret(filepath.Join(o.SecretsDir, filepath.Base(ref)))
	case "etc", "kube":
		return "" // a tracker or docs login; a cluster never leaves the kube tool
	}
	if o.SourcesDir == "" || !sources.NameRe.MatchString(e.Name) {
		return ""
	}
	return sources.ReadSecret(filepath.Join(o.SourcesDir, e.Type, e.Name+".cred"))
}

// --- storage -----------------------------------------------------------------------------------------------------

// Open is ARGUS_DB with the schema (the agent's user writes it).
func Open(db string) (*sql.DB, error) {
	if !filepath.IsAbs(db) {
		return nil, fmt.Errorf("database path must be absolute: %q", db)
	}
	c, err := sql.Open("sqlite", "file:"+db+"?_pragma=busy_timeout(60000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	c.SetMaxOpenConns(1)
	if _, err := c.Exec(Schema); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

type lastRun struct {
	status            string
	started, finished int64
}

func lastRuns(c *sql.DB, key string) (last, full *lastRun) {
	var l lastRun
	if c.QueryRow(`SELECT COALESCE(status,''), COALESCE(started_at,0), COALESCE(finished_at,0) FROM source_discovery_runs
  WHERE source=? AND finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`, key).Scan(&l.status, &l.started, &l.finished) == nil {
		last = &l
	}
	var f lastRun
	if c.QueryRow(`SELECT status, started_at, finished_at FROM source_discovery_runs WHERE source=? AND status='ok' AND mode='full'
  ORDER BY id DESC LIMIT 1`, key).Scan(&f.status, &f.started, &f.finished) == nil {
		full = &f
	}
	return
}

// store upserts the items; with prune, it deletes the kinds' rows this run did not see. Returns how many went.
func store(c *sql.DB, key string, items []Item, started, at int64, prune []string) (int, error) {
	tx, err := c.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, it := range items {
		b, _ := json.Marshal(it.Attrs)
		if _, err := tx.Exec(`INSERT INTO source_inventory (source, kind, key, attrs, first_seen, seen_at) VALUES (?,?,?,?,?,?)
  ON CONFLICT(source, kind, key) DO UPDATE SET attrs=excluded.attrs, seen_at=excluded.seen_at`,
			key, it.Kind, it.Key, string(b), at, at); err != nil {
			return 0, err
		}
	}
	gone := 0
	for _, k := range prune {
		res, err := tx.Exec("DELETE FROM source_inventory WHERE source=? AND kind=? AND seen_at < ?", key, k, started)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		gone += int(n)
	}
	return gone, tx.Commit()
}

// One discovers one source and records the run.
func (o *Options) One(c *sql.DB, key string, e *sources.Entry, force bool, trigger string) Run {
	o.defaults()
	last, full := lastRuns(c, key)
	started := o.now().Unix()
	run := Run{Source: key, StartedAt: started, Trigger: trigger, Counts: map[string]int{}}
	if !force && last != nil && (last.status == "ok" || last.status == "partial") && time.Duration(started-last.finished)*time.Second < o.MinGap {
		run.Status = "skipped"
		run.Error = fmt.Sprintf("discovered %d min ago", (started-last.finished)/60)
		run.FinishedAt = started
		return run
	}
	var lastFull int64
	if full != nil {
		lastFull = full.started
	}
	var res result
	var err error
	calls := 0
	if e.Type == "kubernetes" {
		res, calls, err = o.kubernetes(e)
	} else if cred := o.credential(e); cred == "" {
		where := e.CredWhere
		if where == "" {
			where = "Settings → Data sources"
		}
		err = refused("the credential is not readable for the agent (" + where + ")")
	} else {
		var cl *client
		cl, err = o.client(e.URL, sources.AuthHeaders(e, cred))
		cred = ""
		if err == nil {
			switch e.Type {
			case "gitlab":
				res, err = o.gitlab(cl, lastFull)
			case "grafana":
				res, err = o.grafana(cl, lastFull)
			case "sentry":
				res, err = o.sentry(cl, lastFull)
			case "elasticsearch":
				res, err = o.elasticsearch(cl, lastFull)
			}
			calls = cl.calls
		}
	}
	run.Calls, run.Mode = calls, res.mode
	var b budget
	switch {
	case errors.As(err, &b):
		run.Status, run.Error = "partial", string(b)+"; nothing was pruned"
	case err != nil:
		run.Status, run.Error = "error", cut(sources.Mask(err.Error()), 300)
	default:
		at := o.now().Unix()
		var prune []string
		if res.complete && res.mode == "full" {
			prune = Kinds[e.Type]
		}
		gone, serr := store(c, key, res.items, started, at, prune)
		if serr != nil {
			run.Status, run.Error = "error", "could not store: "+cut(serr.Error(), 200)
			break
		}
		for _, it := range res.items {
			run.Counts[it.Kind]++
		}
		if gone > 0 {
			run.Counts["removed"] = gone
		}
		run.Status = "ok"
		if !res.complete {
			run.Status = "partial"
		}
		note := strings.Join(res.notes, "; ")
		if !res.complete {
			note = strings.TrimPrefix(note+"; a cap was hit or a read failed, nothing was pruned", "; ")
		}
		run.Error = cut(sources.Mask(note), 300)
	}
	run.FinishedAt = o.now().Unix()
	counts, _ := json.Marshal(run.Counts)
	c.Exec(`INSERT INTO source_discovery_runs (source, started_at, finished_at, status, mode, trigger, counts, calls, error)
  VALUES (?,?,?,?,?,?,?,?,?)`, key, run.StartedAt, run.FinishedAt, run.Status, run.Mode, trigger, string(counts), run.Calls, run.Error)
	c.Exec(`DELETE FROM source_discovery_runs WHERE source=? AND id NOT IN (SELECT id FROM source_discovery_runs WHERE source=?
  ORDER BY id DESC LIMIT 50)`, key, key)
	return run
}

// All discovers every eligible source (or only those named), after dropping the inventory of sources no longer
// eligible. Runs are in key order.
func (o *Options) All(only map[string]bool, force bool, trigger string) ([]Run, error) {
	o.defaults()
	c, err := Open(o.DB)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	el := Eligible(o.Sources)
	q, err := c.Query("SELECT DISTINCT source FROM source_inventory")
	if err != nil {
		return nil, err
	}
	var gone []string
	for q.Next() {
		var s string
		if q.Scan(&s) == nil && el[s] == nil {
			gone = append(gone, s)
		}
	}
	q.Close()
	for _, s := range gone {
		c.Exec("DELETE FROM source_inventory WHERE source=?", s)
	}
	keys := make([]string, 0, len(el))
	for k := range el {
		if len(only) == 0 || only[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Run
	for _, k := range keys {
		out = append(out, o.One(c, k, el[k], force, trigger))
	}
	return out, nil
}

// Line is one run as a log line.
func (r Run) Line() string {
	b, _ := json.Marshal(r.Counts)
	s := fmt.Sprintf("discovery %s: %s %s calls=%d", r.Source, r.Status, b, r.Calls)
	if r.Error != "" {
		s += " note=" + r.Error
	}
	return s
}

var getenv = os.Getenv
