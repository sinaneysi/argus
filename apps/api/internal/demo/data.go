package demo

import "sort"

const (
	COMPANY     = "Northwind Commerce"
	DOMAIN      = "northwind.example"
	GITLAB      = "https://gitlab.northwind.example"
	MATRIX      = "chat.northwind.example"
	ROOM_QA     = "!qaRoomNw01:" + MATRIX
	ROOM_ALERTS = "!alertsRoomNw01:" + MATRIX
	ROOM_KUMA   = "!kumaRoomNw01:" + MATRIX
	ROOM_NOTIFY = "!argusRoomNw01:" + MATRIX

	// The demo logins (throwaway, documented in demo/README.md).
	DEMO_VIEWER   = "demo.viewer"
	DEMO_ADMIN    = "demo.admin"
	DEMO_PASSWORD = "northwind-demo-2026"
)

type person struct{ user, name string }

// PEOPLE are obviously fictional.
var PEOPLE = []person{{"alex.doe", "Alex Doe"}, {"sam.roe", "Sam Roe"}, {"jamie.poe", "Jamie Poe"}, {"robin.moe", "Robin Moe"},
	{"taylor.noe", "Taylor Noe"}, {"casey.loe", "Casey Loe"}, {"morgan.koe", "Morgan Koe"}, {"drew.foe", "Drew Foe"}}

// nameOf is NAME.get(user): nil for an unknown user.
func nameOf(user any) any {
	if u, ok := user.(string); ok {
		for _, p := range PEOPLE {
			if p.user == u {
				return p.name
			}
		}
	}
	return nil
}

var QA_PEOPLE = []person{{"@riley.qa:" + MATRIX, "Riley (QA)"}, {"@quinn.qa:" + MATRIX, "Quinn (QA)"},
	{"@avery.support:" + MATRIX, "Avery (Support)"}}

type vm struct {
	name, env, dc, ip string
	vcpu, ram, disk   int
	role              string
	svcs              []string
}

func (v vm) has(svc string) bool {
	for _, s := range v.svcs {
		if s == svc {
			return true
		}
	}
	return false
}

func sl(s ...string) []string { return s }

// VMS is the estate: about 40 VMs in two datacenters.
var VMS = []vm{
	{"prod-edge-01", "release", "east", "10.20.30.11", 4, 8, 60, "edge", sl("nginx")},
	{"prod-edge-02", "release", "east", "10.20.30.12", 4, 8, 60, "edge", sl("nginx")},
	{"prod-shop-01", "release", "east", "10.20.30.21", 8, 16, 80, "app", sl("shop-front", "catalog-api", "reviews")},
	{"prod-shop-02", "release", "east", "10.20.30.22", 8, 16, 80, "app", sl("shop-front", "catalog-api", "reviews")},
	{"prod-api-01", "release", "east", "10.20.30.31", 8, 16, 80, "app", sl("cart", "checkout", "pricing", "coupon-service")},
	{"prod-api-02", "release", "east", "10.20.30.32", 8, 16, 80, "app", sl("cart", "checkout", "pricing", "coupon-service")},
	{"prod-pay-01", "release", "east", "10.20.30.41", 4, 8, 60, "app", sl("payment-gateway", "sms-otp", "fraud-check")},
	{"prod-pay-02", "release", "east", "10.20.30.42", 4, 8, 60, "app", sl("payment-gateway", "sms-otp", "fraud-check")},
	{"prod-wh-01", "release", "east", "10.20.30.51", 4, 8, 120, "app", sl("warehouse-api", "inventory-sync", "shipping-rates")},
	{"prod-order-01", "release", "east", "10.20.30.52", 8, 16, 80, "app", sl("order-service", "notification", "email-sender", "webhook-relay")},
	{"prod-user-01", "release", "east", "10.20.30.53", 4, 8, 60, "app", sl("auth-service", "user-profile", "loyalty")},
	{"prod-search-01", "release", "east", "10.20.30.61", 8, 32, 200, "data", sl("search", "opensearch")},
	{"prod-search-02", "release", "east", "10.20.30.62", 8, 32, 200, "data", sl("search", "opensearch")},
	{"prod-db-01", "release", "east", "10.20.30.71", 16, 64, 1000, "db", sl("postgresql")},
	{"prod-db-02", "release", "east", "10.20.30.72", 16, 64, 1000, "db", sl("postgresql")},
	{"prod-mongo-01", "release", "east", "10.20.30.81", 8, 32, 500, "db", sl("mongod")},
	{"prod-mongo-02", "release", "east", "10.20.30.82", 8, 32, 500, "db", sl("mongod")},
	{"prod-redis-01", "release", "east", "10.20.30.85", 4, 16, 60, "db", sl("redis")},
	{"prod-queue-01", "release", "east", "10.20.30.86", 4, 8, 80, "data", sl("rabbitmq")},
	{"prod-media-01", "release", "east", "10.20.30.91", 4, 8, 400, "app", sl("image-resizer", "cdn-origin")},
	{"prod-admin-01", "release", "east", "10.20.30.92", 4, 8, 60, "app", sl("admin-panel", "seller-portal", "report-builder")},
	{"prod-matomo-01", "release", "east", "10.20.30.95", 4, 8, 120, "app", sl("analytics-matomo")},
	{"prod-misc-01", "release", "east", "10.20.30.96", 4, 8, 60, "app", sl("recommendation", "feature-flags", "scheduler")},
	{"prod-mon-01", "release", "east", "10.20.30.101", 4, 16, 500, "infra", sl("prometheus", "loki", "grafana")},
	{"prod-backup-01", "release", "east", "10.20.30.110", 4, 8, 4000, "infra", sl("minio")},
	{"prod-edge-03", "release", "west", "10.30.30.11", 4, 8, 60, "edge", sl("nginx")},
	{"prod-shop-03", "release", "west", "10.30.30.21", 8, 16, 80, "app", sl("shop-front", "catalog-api")},
	{"prod-api-03", "release", "west", "10.30.30.31", 8, 16, 80, "app", sl("cart", "checkout", "pricing")},
	{"prod-db-03", "release", "west", "10.30.30.71", 16, 64, 1000, "db", sl("postgresql")},
	{"prod-mongo-03", "release", "west", "10.30.30.81", 8, 32, 500, "db", sl("mongod")},
	{"prod-mon-02", "release", "west", "10.30.30.101", 4, 16, 500, "infra", sl("prometheus", "grafana")},
	{"prod-backup-02", "release", "west", "10.30.30.110", 4, 8, 4000, "infra", sl("minio")},
	{"stg-edge-01", "pre-release", "east", "10.20.20.11", 2, 4, 40, "edge", sl("nginx")},
	{"stg-app-01", "pre-release", "east", "10.20.20.21", 8, 16, 120, "app", sl("shop-front", "cart", "checkout", "payment-gateway", "sms-otp")},
	{"stg-app-02", "pre-release", "east", "10.20.20.22", 8, 16, 120, "app", sl("warehouse-api", "order-service", "search", "auth-service")},
	{"stg-db-01", "pre-release", "east", "10.20.20.71", 8, 32, 400, "db", sl("postgresql", "mongod", "redis")},
	{"dev-swarm-01", "develop", "west", "10.30.10.21", 16, 64, 400, "app", sl("docker", "swarm-manager")},
	{"dev-swarm-02", "develop", "west", "10.30.10.22", 16, 64, 400, "app", sl("docker", "swarm-worker")},
	{"dev-db-01", "develop", "west", "10.30.10.71", 8, 32, 400, "db", sl("postgresql", "mongod", "redis")},
	{"dev-edge-01", "develop", "west", "10.30.10.11", 2, 4, 40, "edge", sl("nginx")},
	{"dev-runner-01", "develop", "west", "10.30.10.31", 8, 16, 200, "infra", sl("gitlab-runner", "docker")},
}

func vmByName(name string) (vm, bool) {
	for _, v := range VMS {
		if v.name == name {
			return v, true
		}
	}
	return vm{}, false
}

type service struct {
	group  string
	port   int
	domain string
	tier   int
	desc   string
}

// SERVICES: name -> (group, port, public domain or "", tier, description).
var SERVICES = map[string]service{
	"shop-front":       {"web", 3000, "www." + DOMAIN, 1, "the storefront (Next.js)"},
	"catalog-api":      {"shop", 8081, "api." + DOMAIN, 1, "product catalogue API"},
	"cart":             {"shop", 8082, "api." + DOMAIN, 1, "shopping cart"},
	"checkout":         {"shop", 8083, "checkout." + DOMAIN, 1, "checkout flow"},
	"pricing":          {"shop", 8084, "", 2, "price and promotion engine"},
	"coupon-service":   {"shop", 8085, "", 3, "coupons and vouchers"},
	"reviews":          {"shop", 8086, "api." + DOMAIN, 3, "product reviews"},
	"payment-gateway":  {"payments", 8090, "pay." + DOMAIN, 1, "card and wallet payments"},
	"sms-otp":          {"payments", 8091, "", 1, "SMS one-time passwords for payment and login"},
	"fraud-check":      {"payments", 8092, "", 2, "fraud scoring"},
	"warehouse-api":    {"warehouse", 8100, "wh." + DOMAIN, 2, "warehouse stock and picking"},
	"inventory-sync":   {"warehouse", 8101, "", 2, "stock sync between warehouse and catalogue"},
	"shipping-rates":   {"warehouse", 8102, "", 3, "carrier rates"},
	"order-service":    {"orders", 8110, "", 1, "orders"},
	"notification":     {"orders", 8111, "", 2, "push and in-app notifications"},
	"email-sender":     {"orders", 8112, "", 3, "transactional e-mail"},
	"webhook-relay":    {"orders", 8113, "hooks." + DOMAIN, 3, "partner webhooks"},
	"auth-service":     {"users", 8120, "account." + DOMAIN, 1, "login and sessions"},
	"user-profile":     {"users", 8121, "account." + DOMAIN, 2, "customer profiles"},
	"loyalty":          {"users", 8122, "", 3, "loyalty points"},
	"search":           {"search", 8130, "api." + DOMAIN, 1, "product search"},
	"image-resizer":    {"media", 8140, "img." + DOMAIN, 2, "image resizing"},
	"cdn-origin":       {"media", 8141, "cdn." + DOMAIN, 2, "CDN origin"},
	"admin-panel":      {"backoffice", 8150, "admin." + DOMAIN, 2, "back-office admin"},
	"seller-portal":    {"backoffice", 8151, "sellers." + DOMAIN, 2, "marketplace sellers"},
	"report-builder":   {"backoffice", 8152, "", 3, "finance reports"},
	"analytics-matomo": {"analytics", 8160, "stats." + DOMAIN, 3, "web analytics (Matomo, PHP container)"},
	"recommendation":   {"shop", 8170, "", 3, "recommendations"},
	"feature-flags":    {"platform", 8171, "", 2, "feature flags"},
	"scheduler":        {"platform", 8172, "", 3, "cron jobs"},
}

func serviceNames() []string {
	var out []string
	for k := range SERVICES {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// iif is Python's `a if c else b` for values that are already computed (no side effects in either branch).
func iif(c bool, a, b any) any {
	if c {
		return a
	}
	return b
}
