package catalogue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// northwind is a ~120-service estate with Northwind names (fictional): group/repo, a few public domains.
func northwind() map[string]ServiceDef {
	groups := map[string][]string{
		"shop":       {"cart", "checkout", "pricing", "coupon-service", "reviews", "catalog-api", "wishlist", "compare", "bundles", "gift-cards", "recommendation", "search-suggest"},
		"payments":   {"payment-gateway", "sms-otp", "fraud-check", "refunds", "invoices", "wallet", "card-vault", "settlement", "chargebacks", "payouts"},
		"warehouse":  {"warehouse-api", "inventory-sync", "shipping-rates", "picking", "returns", "labels", "dock-scheduler", "stock-alerts", "barcode", "transfers"},
		"orders":     {"order-service", "notification", "email-sender", "webhook-relay", "order-history", "tracking", "order-export", "invoicing-bridge", "subscriptions", "backorders"},
		"users":      {"auth-service", "user-profile", "loyalty", "consent", "addresses", "sessions", "referrals", "kyc", "support-chat", "feedback"},
		"media":      {"image-resizer", "cdn-origin", "video-transcoder", "thumbnails", "uploads", "asset-index", "watermark", "media-cache", "banner-builder", "media-admin"},
		"backoffice": {"admin-panel", "seller-portal", "report-builder", "price-import", "catalog-editor", "audit-trail", "tickets", "finance-export", "seller-payouts", "ops-console"},
		"platform":   {"feature-flags", "scheduler", "config-server", "rate-limiter", "edge-router", "secrets-broker", "job-runner", "health-board", "status-page", "dns-sync"},
		"search":     {"search", "indexer", "synonyms", "ranking", "query-log", "autocomplete", "facets", "search-admin", "spellcheck", "relevance-lab"},
		"analytics":  {"analytics-matomo", "events-collector", "funnels", "ab-tests", "attribution", "dashboards-api", "cohorts", "exports", "heatmaps", "sessions-replay"},
		"web":        {"shop-front", "mobile-bff", "landing", "blog", "help-center", "store-locator", "careers", "press", "affiliate-portal", "partner-docs"},
		"data":       {"etl-nightly", "warehouse-loader", "lake-sync", "dbt-models", "metrics-api", "anomaly", "forecast", "data-catalog", "pii-scanner", "backfill"},
	}
	out := map[string]ServiceDef{}
	for g, rs := range groups {
		for _, r := range rs {
			out[g+"/"+r] = ServiceDef{}
		}
	}
	out["web/shop-front"] = ServiceDef{Domains: []string{"www.northwind.example"}}
	out["payments/payment-gateway"] = ServiceDef{GitLab: "nw/payments/payment-gateway", Domains: []string{"pay.northwind.example"}}
	out["orders/sessions"] = ServiceDef{} // users/sessions too: an ambiguous short name
	return out
}

func TestMatcher(t *testing.T) {
	s := NewServices(northwind(), []string{"nw"})
	if len(s.Names) < 120 {
		t.Fatalf("fixture has %d services", len(s.Names))
	}
	for _, c := range []struct {
		typ, kind, key string
		attrs          map[string]any
		svc, rule      string
		conf           float64
		note           string
	}{
		{"gitlab", "project", "nw/payments/payment-gateway", nil, "payments/payment-gateway", "gitlab-path", 1.0, ""},
		{"gitlab", "project", "shop/cart", nil, "shop/cart", "gitlab-path", 1.0, ""},
		{"gitlab", "project", "mirror/cart", nil, "shop/cart", "gitlab-name", 0.5, ""},
		{"gitlab", "project", "sandbox/api", nil, "", "", 0, ""},
		{"kubernetes", "workload", "shop/deployment/cart-api", map[string]any{"name": "cart-api", "type": "deployment"}, "shop/cart", "k8s-name", 0.75, ""},
		{"kubernetes", "workload", "x/deployment/prod-shop-cart", map[string]any{"name": "prod-shop-cart", "type": "deployment"}, "shop/cart", "k8s-name", 0.85, ""},
		{"kubernetes", "workload", "x/deployment/w", map[string]any{"name": "w", "labels": map[string]any{"app.kubernetes.io/name": "checkout"}}, "shop/checkout", "k8s-label", 0.85, ""},
		{"kubernetes", "ingress", "web/front", map[string]any{"name": "front", "hosts": []any{"www.northwind.example"}}, "web/shop-front", "ingress-host", 0.9, ""},
		{"kubernetes", "workload", "a/deployment/api", map[string]any{"name": "api"}, "", "", 0, ""},
		{"kubernetes", "namespace", "monitoring", map[string]any{"name": "monitoring"}, "", "", 0, ""},
		{"kubernetes", "workload", "a/deployment/sessions", map[string]any{"name": "sessions", "type": "deployment"}, "", "", 0, "ambiguous: deployment sessions fits orders/sessions, users/sessions"},
		{"kubernetes", "workload", "a/deployment/users-sessions", map[string]any{"name": "users-sessions", "type": "deployment"}, "users/sessions", "k8s-name", 0.85, ""},
		{"grafana", "loki", "container=nw-search", map[string]any{"label": "container", "value": "nw-search"}, "search/search", "loki-container", 0.7, ""},
		{"grafana", "loki", "compose_service=orders_order-service", map[string]any{"label": "compose_service", "value": "orders_order-service", "prefix": true}, "orders/order-service", "loki-compose_service", 0.85, ""},
		{"grafana", "loki", "app=checkout-service", map[string]any{"label": "app", "value": "checkout-service"}, "shop/checkout", "loki-app", 0.75, ""},
		{"grafana", "loki", "unit=loki", map[string]any{"label": "unit", "value": "loki"}, "", "", 0, ""},
		{"grafana", "prom-job", "catalog-api", nil, "shop/catalog-api", "prom-job", 0.6, ""},
		{"grafana", "dashboard", "d1", map[string]any{"title": "Payment gateway latency"}, "payments/payment-gateway", "dashboard-title", 0.5, ""},
		{"grafana", "dashboard", "d2", map[string]any{"title": "Checkout and pricing"}, "", "", 0, "ambiguous: dashboard title fits shop/checkout, shop/pricing"},
		{"grafana", "dashboard", "d3", map[string]any{"title": "Cartography"}, "", "", 0, ""},
		{"sentry", "project", "northwind/fraud-check", map[string]any{"slug": "fraud-check"}, "payments/fraud-check", "sentry-project", 0.8, ""},
		{"elasticsearch", "index", "logs-refunds-*", nil, "payments/refunds", "index-name", 0.6, ""},
	} {
		ms, note := s.MatchItem(c.typ, c.kind, c.key, c.attrs)
		if c.svc == "" {
			if len(ms) != 0 || note != c.note {
				t.Errorf("%s %s: want unmapped (%q), got %+v %q", c.typ, c.key, c.note, ms, note)
			}
			continue
		}
		if len(ms) != 1 || ms[0].Service != c.svc || ms[0].Rule != c.rule || ms[0].Confidence != c.conf || ms[0].Why == "" {
			t.Errorf("%s %s: want %s %s %.2f, got %+v %q", c.typ, c.key, c.svc, c.rule, c.conf, ms, note)
		}
	}
	if l, lab := Locator("loki", "compose_service=a_b.c", map[string]any{"label": "compose_service", "value": "a_b.c", "prefix": true}); l != `{compose_service=~"a_b[.]c[.].*"}` || lab == "" {
		t.Errorf("loki locator %q", l)
	}
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	c, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	c.Exec(`CREATE TABLE source_inventory (source TEXT, kind TEXT, key TEXT, attrs TEXT, first_seen INTEGER, seen_at INTEGER,
  PRIMARY KEY (source, kind, key)); CREATE TABLE source_discovery_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT,
  started_at INTEGER, finished_at INTEGER, status TEXT, mode TEXT, trigger TEXT, counts TEXT, calls INTEGER, error TEXT)`)
	Migrate(c)
	t.Cleanup(func() { c.Close() })
	return c
}

func put(c *sql.DB, src, kind, key string, attrs map[string]any) {
	b, _ := json.Marshal(attrs)
	c.Exec("INSERT OR REPLACE INTO source_inventory VALUES (?,?,?,?,1,1)", src, kind, key, string(b))
}

func route(t *testing.T, c *sql.DB, svc, src, loc string) (id int64, status, origin string, stale int, ev string) {
	t.Helper()
	c.QueryRow("SELECT id, status, origin, stale, evidence FROM source_routes WHERE service=? AND source=? AND locator=?", svc, src, loc).
		Scan(&id, &status, &origin, &stale, &ev)
	return
}

func TestBuildUpsertRules(t *testing.T) {
	c := openDB(t)
	s := NewServices(northwind(), nil)
	put(c, "gitlab/main", "project", "shop/cart", nil)
	put(c, "gitlab/main", "project", "mirror/cart", nil) // a name guess on a source that has the exact repository: dropped
	put(c, "kubernetes/stage", "workload", "shop/deployment/cart", map[string]any{"name": "cart", "type": "deployment"})
	put(c, "kubernetes/stage", "workload", "shop/deployment/pricing", map[string]any{"name": "pricing", "type": "deployment"})
	put(c, "kubernetes/stage", "workload", "x/deployment/api", map[string]any{"name": "api"})
	put(c, "grafana/east", "prom-job", "checkout", nil)
	sum, err := Build(c, s, 100)
	if err != nil || sum.New != 4 || sum.Unmapped != 2 {
		t.Fatalf("first build: %+v %v", sum, err)
	}
	if id, _, _, _, _ := route(t, c, "shop/cart", "gitlab/main", "mirror/cart"); id != 0 {
		t.Fatal("name guess kept next to the exact repository")
	}
	// people decide: confirm the cart workload, reject the pricing one, add a human route
	idCart, _, _, _, _ := route(t, c, "shop/cart", "kubernetes/stage", "shop/deployment/cart")
	idPricing, _, _, _, _ := route(t, c, "shop/pricing", "kubernetes/stage", "shop/deployment/pricing")
	idJob, _, _, _, _ := route(t, c, "shop/checkout", "grafana/east", `{job="checkout"}`)
	for _, r := range []Request{{Action: "confirm", ID: idCart, User: "alex.doe"}, {Action: "reject", ID: idPricing, User: "alex.doe"},
		{Action: "edit", ID: idJob, User: "sam.roe", Locator: `{job="checkout", env="release"}`, Note: "release only"}} {
		if _, err := Apply(c, r, 200, nil); err != nil {
			t.Fatal(err)
		}
	}
	// rebuild with the same inventory: rejected stays rejected, the human row untouched, no duplicate of the edited one
	sum, _ = Build(c, s, 300)
	if _, st, _, _, _ := route(t, c, "shop/pricing", "kubernetes/stage", "shop/deployment/pricing"); st != "rejected" || sum.Reopened != 0 {
		t.Fatalf("rejected re-proposed: %s %+v", st, sum)
	}
	if _, st, or, _, ev := route(t, c, "shop/checkout", "grafana/east", `{job="checkout", env="release"}`); st != "confirmed" || or != "human" || ev != "release only" {
		t.Fatalf("human row: %s %s %s", st, or, ev)
	}
	// the evidence of the rejected route changes (now a label names it): it is proposed again
	put(c, "kubernetes/stage", "workload", "shop/deployment/pricing", map[string]any{"name": "pricing", "type": "deployment",
		"labels": map[string]any{"app.kubernetes.io/name": "pricing"}})
	sum, _ = Build(c, s, 400)
	if _, st, _, _, ev := route(t, c, "shop/pricing", "kubernetes/stage", "shop/deployment/pricing"); st != "proposed" || sum.Reopened != 1 || !strings.Contains(ev, "evidence changed") {
		t.Fatalf("reopen: %s %+v %s", st, sum, ev)
	}
	// the confirmed cart workload disappears: stale, not deleted; a proposed one disappears: removed
	c.Exec("DELETE FROM source_inventory WHERE key IN ('shop/deployment/cart', 'shop/deployment/pricing')")
	sum, _ = Build(c, s, 500)
	if _, st, _, stale, _ := route(t, c, "shop/cart", "kubernetes/stage", "shop/deployment/cart"); st != "confirmed" || stale != 1 || sum.Stale != 1 || sum.Removed != 1 {
		t.Fatalf("stale: %s %d %+v", st, stale, sum)
	}
	// it comes back: no longer stale
	put(c, "kubernetes/stage", "workload", "shop/deployment/cart", map[string]any{"name": "cart", "type": "deployment"})
	Build(c, s, 600)
	if _, _, _, stale, _ := route(t, c, "shop/cart", "kubernetes/stage", "shop/deployment/cart"); stale != 0 {
		t.Fatal("still stale")
	}
	var unm int
	c.QueryRow("SELECT COUNT(*) FROM source_unmapped").Scan(&unm)
	if unm != 2 {
		t.Fatalf("unmapped %d", unm)
	}
}

func TestRequestsAndActions(t *testing.T) {
	for _, c := range []struct {
		raw, want string
	}{
		{`{"action":"confirm","id":3,"user":"bob","role":"viewer"}`, "only an admin"},
		{`{"action":"confirm","id":3,"user":"eve","role":"admin"}`, "does not match"},
		{`{"action":"drop","id":3,"user":"bob","role":"admin"}`, "unknown action"},
		{`{"action":"confirm","id":-1,"user":"bob","role":"admin"}`, "bad route id"},
		{`{"action":"confirm","id":1.5,"user":"bob","role":"admin"}`, "bad route id"},
		{`{"action":"edit","id":3,"locator":"a|b","user":"bob","role":"admin"}`, "without ` or |"},
		{`{"action":"discover","source":"../etc","user":"bob","role":"admin"}`, "bad source"},
		{`[1]`, "not a JSON object"},
	} {
		if _, err := ParseRequest([]byte(c.raw), "bob"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.raw, err)
		}
	}
	db := openDB(t)
	db.Exec(`INSERT INTO source_routes (id, service, source, kind, locator, origin, status, confidence) VALUES
  (7, 'shop/cart', 'grafana/east', 'loki', '{app="cart"}', 'discovered', 'proposed', 0.75)`)
	dir := t.TempDir()
	write := func(name string, v any) {
		b, _ := json.Marshal(v)
		os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	write("alex.doe-00000000000000a1.json", map[string]any{"action": "confirm", "id": 7, "user": "alex.doe", "role": "admin"})
	write("alex.doe-00000000000000a2.json", map[string]any{"action": "discover", "source": "grafana/east", "user": "alex.doe", "role": "admin"})
	write("alex.doe-00000000000000a3.json", map[string]any{"action": "discover", "source": "grafana/east", "user": "alex.doe", "role": "admin"})
	write("view.er-00000000000000a4.json", map[string]any{"action": "reject", "id": 7, "user": "view.er", "role": "viewer"})
	write("junk.txt", "x")
	discovered, rebuilt := 0, 0
	o := ActionOptions{ReqDir: dir, AppUID: -1, Now: func() time.Time { return time.Unix(1000, 0) },
		Discover: func(src string) (string, error) {
			discovered++
			db.Exec("INSERT INTO source_discovery_runs (source, started_at, status) VALUES (?, 1000, 'ok')", src)
			return "discovery " + src + ": ok", nil
		},
		Rebuild: func(DB) error { rebuilt++; return nil }}
	done, err := Actions(db, o)
	if err != nil || len(done) != 4 || discovered != 1 || rebuilt != 1 {
		t.Fatalf("actions: %v %v discovered %d rebuilt %d", done, err, discovered, rebuilt)
	}
	res := map[string]string{}
	q, _ := db.Query("SELECT rid, result FROM source_action_results")
	for q.Next() {
		var a, b string
		q.Scan(&a, &b)
		res[a] = b
	}
	q.Close()
	if !strings.HasPrefix(res["00000000000000a1"], "route 7 confirmed") || !strings.Contains(res["00000000000000a3"], "less than 5 min ago") ||
		!strings.Contains(res["00000000000000a4"], "only an admin") {
		t.Fatalf("results: %v", res)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("requests left: %v", left)
	}
	var st string
	var audits int
	db.QueryRow("SELECT status FROM source_routes WHERE id=7").Scan(&st)
	db.QueryRow("SELECT COUNT(*) FROM source_route_audit").Scan(&audits)
	if st != "confirmed" || audits != 2 {
		t.Fatalf("status %s audits %d", st, audits)
	}
	// agent view
	var b strings.Builder
	PrintRoutes(db, "cart", &b)
	if !strings.Contains(b.String(), "# shop/cart: 1 confirmed, 0 proposed, 0 rejected") || !strings.Contains(b.String(), "confirmed\tgrafana/east\tloki\t{app=\"cart\"}\t0.75") {
		t.Fatal(b.String())
	}
	b.Reset()
	PrintRoutes(db, "nothing", &b)
	if !strings.Contains(b.String(), `no routes for "nothing"`) {
		t.Fatal(b.String())
	}
	db.Exec("INSERT INTO source_unmapped VALUES ('gitlab/main','project','sandbox/api','',''), ('grafana/east','prom-job','blackbox','','')")
	b.Reset()
	PrintUnmapped(db, "gitlab", &b, 300)
	if got := b.String(); !strings.HasPrefix(got, "# 1 discovered items no service claims (gitlab)") || strings.Contains(got, "blackbox") {
		t.Fatal(got)
	}
}

func TestLoadServices(t *testing.T) {
	db := openDB(t)
	db.Exec("CREATE TABLE cicd_projects (path TEXT, kind TEXT, archived INTEGER)")
	db.Exec("INSERT INTO cicd_projects VALUES ('shop/cart','app',0), ('shop/old','app',1), ('platform/ci','package',0)")
	s, from, err := LoadServices(filepath.Join(t.TempDir(), "missing.json"), db)
	if err != nil || from != "cicd_projects" || fmt.Sprint(s.Names) != "[shop/cart]" {
		t.Fatalf("fallback: %v %s %v", s.Names, from, err)
	}
	f := filepath.Join(t.TempDir(), "services.json")
	os.WriteFile(f, []byte(`{"prefixes":["nw-"],"services":{"shop/cart":{"domains":["cart.northwind.example"]}}}`), 0o644)
	s, from, err = LoadServices(f, db)
	if err != nil || from != f || s.Norm("nw-cart-api") != "cart" {
		t.Fatalf("file: %v %s %v", s.Names, from, err)
	}
	os.WriteFile(f, []byte(`{`), 0o644)
	if _, _, err := LoadServices(f, db); err == nil {
		t.Fatal("bad file accepted")
	}
}

func TestRejectReason(t *testing.T) {
	c := openDB(t)
	if _, err := c.Exec(`INSERT INTO source_routes (id, service, source, kind, locator, origin, status) VALUES
	  (1, 'shop/cart', 'gitlab/main', 'project', 'northwind/shop/cart', 'learned', 'proposed')`); err != nil {
		t.Fatal(err)
	}
	r, err := ParseRequest([]byte(`{"action": "reject", "id": 1, "user": "alex.doe", "role": "admin", "reason": "  an \n archived fork "}`), "alex.doe")
	if err != nil || r.Reason != "an archived fork" {
		t.Fatalf("parse: %v %q", err, r.Reason)
	}
	if _, err := Apply(c, r, 200, nil); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := c.QueryRow("SELECT detail FROM source_route_audit WHERE route_id=1").Scan(&detail); err != nil || !strings.Contains(detail, "an archived fork") {
		t.Fatalf("audit: %v %s", err, detail)
	}
	for _, bad := range []string{"a|b", strings.Repeat("x", 301)} {
		if _, err := ParseRequest([]byte(`{"action": "reject", "id": 1, "user": "alex.doe", "role": "admin", "reason": "`+bad+`"}`), "alex.doe"); err == nil {
			t.Fatalf("%q accepted", bad[:3])
		}
	}
}
