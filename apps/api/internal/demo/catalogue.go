package demo

// catalogue.go: the Data sources catalogue (ADR-0017 P2, /api/v1/sources/catalogue) for the demo. A synthetic inventory of the Northwind
// sources the agent may read (GitLab projects, Kubernetes objects of the stage cluster, Loki values, Prometheus jobs and
// dashboards of grafana/east), run through the real matcher (internal/sources/catalogue), then a few routes confirmed,
// rejected, edited by a person or gone stale, and the discovery runs (one partial, one failed). No randomness: the
// other generators' draws are untouched.

import (
	"encoding/json"

	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
)

func (g *Gen) sourceCatalogue() {
	o := g.ops
	g.exec(o, discover.Schema)
	if err := catalogue.Migrate(o); err != nil {
		panic(err)
	}
	seen := g.t(50 * M)
	item := func(src, kind, key string, attrs map[string]any) {
		b, _ := json.Marshal(attrs)
		g.exec(o, "INSERT INTO source_inventory (source, kind, key, attrs, first_seen, seen_at) VALUES (?,?,?,?,?,?)",
			src, kind, key, string(b), g.t(9*D), seen)
	}
	defs := map[string]catalogue.ServiceDef{}
	for i, svc := range serviceNames() {
		s := SERVICES[svc]
		path := s.group + "/" + svc
		var doms []string
		if s.domain != "" {
			doms = []string{s.domain}
		}
		defs[path] = catalogue.ServiceDef{GitLab: path, Domains: doms}
		item("gitlab/main", "project", path, map[string]any{"id": 300 + i, "default_branch": "main", "archived": false,
			"last_activity": g.iso(float64(i+1) * 5 * H)[:19] + "Z", "topics": []string{s.group}})
	}
	for _, p := range []string{"platform/ci-templates", "sandbox/api", "web/shop-front-e2e", "data/etl-nightly"} {
		item("gitlab/main", "project", p, map[string]any{"default_branch": "main", "archived": p == "sandbox/api", "topics": []string{}})
	}
	k8s := "kubernetes/stage"
	item(k8s, "namespace", "shop", map[string]any{"name": "shop", "labels": map[string]string{}})
	item(k8s, "namespace", "payments", map[string]any{"name": "payments", "labels": map[string]string{}})
	item(k8s, "namespace", "monitoring", map[string]any{"name": "monitoring", "labels": map[string]string{}})
	for _, w := range []struct{ ns, typ, name, label string }{
		{"shop", "deployment", "cart", "cart"}, {"shop", "deployment", "catalog-api", "catalog-api"},
		{"shop", "deployment", "pricing-svc", "pricing"}, {"shop", "deployment", "api", ""},
		{"payments", "deployment", "payment-gateway-api", "payment-gateway"}, {"payments", "statefulset", "fraud-check", ""},
		{"monitoring", "daemonset", "node-exporter", "node-exporter"}} {
		labels := map[string]string{}
		if w.label != "" {
			labels["app.kubernetes.io/name"] = w.label
		}
		item(k8s, "workload", w.ns+"/"+w.typ+"/"+w.name, map[string]any{"ns": w.ns, "name": w.name, "type": w.typ, "labels": labels})
	}
	item(k8s, "service", "shop/cart", map[string]any{"ns": "shop", "name": "cart", "labels": map[string]string{},
		"selector": map[string]string{"app.kubernetes.io/name": "cart"}})
	item(k8s, "ingress", "web/storefront", map[string]any{"ns": "web", "name": "storefront", "labels": map[string]string{},
		"hosts": []string{"www." + DOMAIN}, "backends": []string{"storefront"}})
	gr := "grafana/east"
	for _, l := range []struct{ label, value string }{{"app", "cart"}, {"service_name", "checkout-service"},
		{"container", "nw-search"}, {"compose_service", "orders_order-service"}, {"app", "sms-otp"}, {"unit", "loki"},
		{"container", "profile-worker"}} {
		a := map[string]any{"label": l.label, "value": l.value, "datasources": []string{"Loki east"}}
		if l.label == "compose_service" {
			a["prefix"] = true // Swarm task names folded to their service
		}
		item(gr, "loki", l.label+"="+l.value, a)
	}
	for _, j := range []string{"catalog-api", "payment-gateway", "node-exporter", "blackbox"} {
		item(gr, "prom-job", j, map[string]any{"job": j, "datasources": []string{"Prometheus east"}})
	}
	item(gr, "dashboard", "pay-gw", map[string]any{"title": "Payment gateway", "folder": "Payments"})
	item(gr, "dashboard", "co-pr", map[string]any{"title": "Checkout and pricing", "folder": "Shop"})
	item(gr, "dashboard", "node", map[string]any{"title": "Node exporter full", "folder": "General"})
	// a route a person confirmed earlier whose object is gone now: the build marks it stale
	g.exec(o, `INSERT INTO source_routes (service, source, kind, locator, label, confidence, origin, status, rule, evidence,
  evidence_hash, stale, created_at, updated_at, updated_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,0,?,?,?)`,
		"shop/reviews", k8s, "workload", "shop/deployment/reviews", "", 0.75, "discovered", "confirmed", "k8s-name",
		"deployment reviews matches the repository name reviews", "0000000000000000", g.t(8*D), g.t(6*D), "alex.doe")
	svcs := catalogue.NewServices(defs, []string{"nw-"})
	if _, err := catalogue.Build(o, svcs, g.t(45*M)); err != nil {
		panic(err)
	}
	// people's decisions
	g.exec(o, `UPDATE source_routes SET status='confirmed', updated_by='alex.doe', updated_at=? WHERE rule='gitlab-path'
  AND service IN ('shop/cart','shop/checkout','payments/payment-gateway','payments/sms-otp','search/search','web/shop-front')`, g.t(3*D))
	g.exec(o, `UPDATE source_routes SET status='confirmed', updated_by='sam.roe', updated_at=? WHERE service='shop/cart'
  AND source='kubernetes/stage'`, g.t(2*D))
	g.exec(o, `UPDATE source_routes SET status='rejected', updated_by='sam.roe', updated_at=? WHERE source='grafana/east'
  AND kind='prom-job' AND locator='{job="payment-gateway"}'`, g.t(2*D))
	g.exec(o, `UPDATE source_routes SET locator='{app="sms-otp", env="release"}', origin='human', status='confirmed', confidence=1.0,
  evidence='only the release stream: stage writes test codes', updated_by='alex.doe', updated_at=? WHERE source='grafana/east'
  AND kind='loki' AND service='payments/sms-otp'`, g.t(1*D))
	for _, a := range []struct {
		by, action string
		ago        float64
	}{{"alex.doe", "confirm", 3 * D}, {"sam.roe", "confirm", 2 * D}, {"sam.roe", "reject", 2 * D}, {"alex.doe", "edit", D}} {
		g.exec(o, "INSERT INTO source_route_audit (at, by_user, action, route_id, service, detail) VALUES (?,?,?,0,'','{}')", g.t(a.ago), a.by, a.action)
	}
	// discovery runs: the newest per source is what the page shows
	run := func(src, status, mode, trigger string, counts map[string]int, calls int, errText string, ago float64) {
		b, _ := json.Marshal(counts)
		g.exec(o, `INSERT INTO source_discovery_runs (source, started_at, finished_at, status, mode, trigger, counts, calls, error)
  VALUES (?,?,?,?,?,?,?,?,?)`, src, g.t(ago+2*M), g.t(ago), status, mode, trigger, string(b), calls, errText)
	}
	run("gitlab/main", "ok", "full", "timer", map[string]int{"project": 34}, 3, "", 26*H)
	run("gitlab/main", "ok", "incremental", "timer", map[string]int{"project": 4}, 2, "", 50*M)
	run("kubernetes/stage", "ok", "full", "timer", map[string]int{"namespace": 3, "workload": 7, "service": 1, "ingress": 1}, 6, "", 50*M)
	run("grafana/east", "partial", "full", "manual", map[string]int{"loki": 7, "prom-job": 4, "dashboard": 3}, 9,
		"Loki east: container has more than 5000 values; a cap was hit or a read failed, nothing was pruned", 50*M)
	run("elasticsearch/logs", "error", "", "timer", map[string]int{}, 1, "indices: the login was refused (HTTP 403)", 50*M)
}
