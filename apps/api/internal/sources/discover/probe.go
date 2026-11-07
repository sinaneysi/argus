package discover

// probe.go: one capped look into one source for a few names, for `argus sources find` (ADR-0017 P3 fan-out). The same
// pinned, GET-only client as discovery; a handful of calls per source instead of a full inventory.

import (
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/sinaneysi/argus/internal/kube"
	"github.com/sinaneysi/argus/internal/sources"
)

// IsBudget says a cap (calls, time, answer size) stopped a probe: what it found is partial.
func IsBudget(err error) bool {
	var b budget
	return errors.As(err, &b)
}

// Probe searches one source for the search terms. typ is the find type: gitlab (project search), loki (values of the
// app-naming labels of the first Loki datasource), prometheus (job values of the first Prometheus datasource), sentry
// (project query in the first two organisations), es (indices named *term*), k8s (the read-only kube tool's
// inventory). At most calls requests, none after left. Returns the items (unfiltered: the caller matches them),
// the calls used and an error; IsBudget(err) means partial.
func (o *Options) Probe(typ string, e *sources.Entry, terms []string, calls int, left time.Duration) ([]Item, int, error) {
	o.defaults()
	if len(terms) == 0 {
		return nil, 0, refused("no search term")
	}
	if e.Type == "kubernetes" {
		if calls < len(kube.InventoryTypes) {
			return nil, 0, budget("call budget too small for a cluster inventory")
		}
		res, n, err := o.kubernetes(e)
		return res.items, n, err
	}
	cred := o.credential(e)
	if cred == "" {
		return nil, 0, refused("the credential is not readable for the agent")
	}
	c, err := o.client(e.URL, sources.AuthHeaders(e, cred))
	cred = ""
	if err != nil {
		return nil, 0, err
	}
	c.left, c.deadline = calls, o.now().Add(left)
	var items []Item
	switch typ {
	case "gitlab":
		items, err = probeGitLab(c, terms)
	case "loki", "prometheus":
		items, err = o.probeGrafana(c, typ)
	case "sentry":
		items, err = probeSentry(c, terms[0])
	case "es":
		items, err = probeES(c, terms[0])
	default:
		err = refused("no probe for " + typ)
	}
	return items, c.calls, err
}

func probeGitLab(c *client, terms []string) ([]Item, error) {
	var items []Item
	for _, t := range terms {
		st, d, _, err := c.get("/api/v4/projects", url.Values{"search": {t}, "simple": {"true"}, "per_page": {"50"}})
		if err != nil {
			return items, err
		}
		if err := okOr(st, "GitLab projects"); err != nil {
			return items, err
		}
		rows, _ := d.([]any)
		for _, x := range rows {
			p, _ := x.(map[string]any)
			if path := clean(p["path_with_namespace"], nameRe); path != "" {
				var id any
				if f, ok := p["id"].(float64); ok {
					id = int64(f)
				}
				items = append(items, Item{"project", path, map[string]any{"id": id}})
			}
		}
	}
	return items, nil
}

func (o *Options) probeGrafana(c *client, typ string) ([]Item, error) {
	dss, err := o.grafanaDatasources(c)
	if err != nil {
		return nil, err
	}
	want := map[string]string{"loki": "loki", "prometheus": "prometheus"}[typ]
	var ds *gds
	for i := range dss {
		if dss[i].typ == want {
			ds = &dss[i]
			break
		}
	}
	if ds == nil {
		return nil, nil
	}
	var items []Item
	if typ == "prometheus" {
		st, v, _, err := c.get("/api/datasources/proxy/uid/"+url.PathEscape(ds.uid)+"/api/v1/label/job/values", nil)
		if err != nil {
			return nil, err
		}
		vm, _ := v.(map[string]any)
		if st != 200 || vm == nil {
			return nil, nil
		}
		vl, _ := vm["data"].([]any)
		for _, x := range vl {
			if s := clean(x, valueRe); s != "" {
				items = append(items, Item{"prom-job", s, map[string]any{"job": s}})
			}
		}
		return items, nil
	}
	px := "/api/datasources/proxy/uid/" + url.PathEscape(ds.uid) + "/loki/api/v1"
	start := url.Values{"start": {strconv.FormatInt(o.now().Add(-24*time.Hour).UnixNano(), 10)}}
	st, d, _, err := c.get(px+"/labels", start)
	if err != nil {
		return nil, err
	}
	m, _ := d.(map[string]any)
	if st != 200 || m == nil {
		return nil, nil
	}
	have := map[string]bool{}
	l, _ := m["data"].([]any)
	for _, x := range l {
		if s, ok := x.(string); ok {
			have[s] = true
		}
	}
	n := 0
	for _, label := range LokiLabels {
		if !have[label] || n >= 4 {
			continue
		}
		n++
		st, v, _, err := c.get(px+"/label/"+label+"/values", start)
		if err != nil {
			return items, err
		}
		vm, _ := v.(map[string]any)
		if st != 200 || vm == nil {
			continue
		}
		vl, _ := vm["data"].([]any)
		for _, x := range vl {
			if f, task := FoldValue(x); f != "" {
				a := map[string]any{"label": label, "value": f}
				if task {
					a["prefix"] = true
				}
				items = append(items, Item{"loki", label + "=" + f, a})
			}
		}
	}
	return items, nil
}

func probeSentry(c *client, term string) ([]Item, error) {
	st, orgs, _, err := c.get("/api/0/organizations/", nil)
	if err != nil {
		return nil, err
	}
	if err := okOr(st, "Sentry organizations"); err != nil {
		return nil, err
	}
	ol, _ := orgs.([]any)
	if len(ol) > 2 {
		ol = ol[:2]
	}
	var items []Item
	for _, x := range ol {
		om, _ := x.(map[string]any)
		org := clean(om["slug"], valueRe)
		if org == "" {
			continue
		}
		st, d, _, err := c.get("/api/0/organizations/"+url.PathEscape(org)+"/projects/", url.Values{"query": {term}})
		if err != nil {
			return items, err
		}
		l, _ := d.([]any)
		if st != 200 {
			continue
		}
		for _, p := range l {
			pm, _ := p.(map[string]any)
			if slug := clean(pm["slug"], valueRe); slug != "" {
				items = append(items, Item{"project", org + "/" + slug, map[string]any{"org": org, "slug": slug}})
			}
		}
	}
	return items, nil
}

func probeES(c *client, term string) ([]Item, error) {
	st, d, _, err := c.get("/_cat/indices/*"+url.PathEscape(term)+"*", url.Values{"format": {"json"}, "h": {"index"}})
	if err != nil {
		return nil, err
	}
	rows, _ := d.([]any)
	if st != 200 {
		return nil, nil
	}
	seen := map[string]bool{}
	var items []Item
	for _, x := range rows {
		m, _ := x.(map[string]any)
		if n := clean(m["index"], valueRe); n != "" && n[0] != '.' {
			if p := IndexPattern(n); !seen[p] {
				seen[p] = true
				items = append(items, Item{"index", p, map[string]any{}})
			}
		}
	}
	return items, nil
}
