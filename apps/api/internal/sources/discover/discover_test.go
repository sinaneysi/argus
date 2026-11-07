package discover

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/sources"
)

// Obviously fake material: Northwind names, no real host or credential.
const fakeCred = "fake-discovery-cred-xxxxxxxxxxxxxxxx"

// plainGet is the test's Getter: a plain client to the httptest server (the guarded sources.Get refuses loopback).
func plainGet(base, path string, hdr map[string]string, maxBody int64, timeout time.Duration) (int, any, http.Header, error) {
	req, _ := http.NewRequest("GET", base+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if int64(len(raw)) > maxBody {
		return resp.StatusCode, nil, resp.Header, sources.ErrTooLarge
	}
	var d any
	json.Unmarshal(raw, &d)
	return resp.StatusCode, d, resp.Header, nil
}

type env struct {
	o   *Options
	dir string
	now time.Time
}

func setup(t *testing.T, srv *httptest.Server, typ string) *env {
	t.Helper()
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "sources", typ), 0o750)
	os.WriteFile(filepath.Join(d, "sources", typ, "main.cred"), []byte(fakeCred+"\n"), 0o640)
	e := &env{dir: d, now: time.Unix(1793318400, 0)}
	e.o = &Options{DB: filepath.Join(d, "ops.db"), SourcesDir: filepath.Join(d, "sources"), Get: plainGet,
		Now: func() time.Time { return e.now },
		Sources: map[string]*sources.Entry{typ + "/main": {Type: typ, Name: "main", URL: srv.URL, Purpose: "agent", Enabled: true,
			Status: "readonly"}}}
	return e
}

func (e *env) inv(t *testing.T) map[string]map[string]any {
	t.Helper()
	c, err := sql.Open("sqlite", e.o.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	q, err := c.Query("SELECT kind, key, attrs FROM source_inventory")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	out := map[string]map[string]any{}
	for q.Next() {
		var k, key, a string
		q.Scan(&k, &key, &a)
		var m map[string]any
		json.Unmarshal([]byte(a), &m)
		out[k+" "+key] = m
	}
	return out
}

func (e *env) run(t *testing.T, force bool) Run {
	t.Helper()
	runs, err := e.o.All(nil, force, "timer")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %v %v", runs, err)
	}
	return runs[0]
}

func TestGitLabPaginationIncrementalAndPrune(t *testing.T) {
	var calls atomic.Int32
	var sawSince atomic.Value
	sawSince.Store("")
	projects := 230
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("PRIVATE-TOKEN") != fakeCred {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/api/v4/projects" {
			w.WriteHeader(404)
			return
		}
		if s := r.URL.Query().Get("last_activity_after"); s != "" {
			sawSince.Store(s)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var rows []map[string]any
		if r.URL.Query().Get("archived") == "true" {
			if page == 1 {
				rows = append(rows, map[string]any{"path_with_namespace": "shop/p-0003"})
			}
			w.Header().Set("X-Next-Page", "")
		} else {
			for i := (page - 1) * 100; i < page*100 && i < projects; i++ {
				rows = append(rows, map[string]any{"id": i, "path_with_namespace": fmt.Sprintf("shop/p-%04d", i), "default_branch": "main",
					"last_activity_at": "2026-11-01T10:00:00.000Z", "topics": []string{"shop", "bad topic"}})
			}
			if page*100 < projects {
				w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
			} else {
				w.Header().Set("X-Next-Page", "")
			}
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	e := setup(t, srv, "gitlab")
	r := e.run(t, false)
	if r.Status != "ok" || r.Mode != "full" || r.Counts["project"] != 230 || r.Calls != 4 {
		t.Fatalf("full run: %+v", r)
	}
	inv := e.inv(t)
	if a := inv["project shop/p-0003"]; a == nil || a["archived"] != true || a["default_branch"] != "main" {
		t.Fatalf("archived flag: %v", a)
	}
	if tp := inv["project shop/p-0001"]["topics"].([]any); len(tp) != 1 || tp[0] != "shop" {
		t.Fatalf("topics not cleaned: %v", tp)
	}
	// within MinGap: skipped; forced: incremental (since the last full sweep), nothing pruned
	e.now = e.now.Add(10 * time.Minute)
	if r := e.run(t, false); r.Status != "skipped" {
		t.Fatalf("not skipped: %+v", r)
	}
	projects = 120
	e.now = e.now.Add(2 * time.Hour)
	r = e.run(t, false)
	if r.Status != "ok" || r.Mode != "incremental" || sawSince.Load().(string) == "" {
		t.Fatalf("incremental: %+v since %v", r, sawSince.Load())
	}
	if n := len(e.inv(t)); n != 230 {
		t.Fatalf("an incremental run pruned: %d", n)
	}
	// the next full sweep (a day later) prunes what is gone
	e.now = e.now.Add(25 * time.Hour)
	r = e.run(t, false)
	if r.Status != "ok" || r.Mode != "full" || r.Counts["removed"] != 110 || len(e.inv(t)) != 120 {
		t.Fatalf("full prune: %+v %d", r, len(e.inv(t)))
	}
}

func TestGitLabCapIsPartialAndPrunesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var rows []map[string]any
		for i := 0; i < 100; i++ {
			rows = append(rows, map[string]any{"path_with_namespace": fmt.Sprintf("g/p-%d-%d", page, i)})
		}
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	e := setup(t, srv, "gitlab")
	c, _ := Open(e.o.DB)
	c.Exec("INSERT INTO source_inventory VALUES ('gitlab/main','project','old/one','{}',1,1)")
	c.Close()
	e.o.Caps = &Caps{GitLabPages: 3, GitLabProjects: 6000}
	r := e.run(t, true)
	if r.Status != "partial" || !strings.Contains(r.Error, "page cap") || !strings.Contains(r.Error, "nothing was pruned") {
		t.Fatalf("cap: %+v", r)
	}
	if inv := e.inv(t); inv["project old/one"] == nil || len(inv) != 301 {
		t.Fatalf("partial run pruned or lost items: %d", len(inv))
	}
	// call budget
	e.o.Caps, e.o.Calls = nil, 2
	r = e.run(t, true)
	if r.Status != "partial" || !strings.Contains(r.Error, "call budget used") {
		t.Fatalf("budget: %+v", r)
	}
}

func TestErrorsAndEligibility(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	e := setup(t, srv, "sentry")
	r := e.run(t, true)
	if r.Status != "error" || !strings.Contains(r.Error, "the login was refused (HTTP 401)") || strings.Contains(r.Error, fakeCred) {
		t.Fatalf("401: %+v", r)
	}
	// not enabled, not agent, a P1-only type: never discovered; their old inventory goes
	c, _ := Open(e.o.DB)
	c.Exec("INSERT INTO source_inventory VALUES ('grafana/old','loki','app=x','{}',1,1)")
	c.Close()
	e.o.Sources["grafana/off"] = &sources.Entry{Type: "grafana", Name: "off", Purpose: "agent", Enabled: false}
	e.o.Sources["gitlab/tracker"] = &sources.Entry{Type: "gitlab", Name: "tracker", Purpose: "tracker", Enabled: true}
	e.o.Sources["outline/wiki"] = &sources.Entry{Type: "outline", Name: "wiki", Purpose: "agent", Enabled: true}
	if el := Eligible(e.o.Sources); len(el) != 1 || el["sentry/main"] == nil {
		t.Fatalf("eligible: %v", el)
	}
	e.run(t, true)
	if inv := e.inv(t); len(inv) != 0 {
		t.Fatalf("inventory of a source no longer eligible kept: %v", inv)
	}
	// no readable credential
	os.Remove(filepath.Join(e.dir, "sources", "sentry", "main.cred"))
	if r := e.run(t, true); r.Status != "error" || !strings.Contains(r.Error, "credential is not readable") {
		t.Fatalf("no cred: %+v", r)
	}
	// the real guard: the production getter refuses a non-https address before any call
	e.o.Get, e.o.defaulted, e.o.fakeGet = nil, false, false
	os.WriteFile(filepath.Join(e.dir, "sources", "sentry", "main.cred"), []byte(fakeCred), 0o640)
	if r := e.run(t, true); r.Status != "error" || !strings.Contains(r.Error, "https") || r.Calls != 0 {
		t.Fatalf("guard: %+v", r)
	}
}

func TestGrafanaLokiPromDashboards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeCred {
			w.WriteHeader(401)
			return
		}
		j := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/frontend/settings":
			j(map[string]any{"datasources": map[string]any{"Loki east": map[string]any{"type": "loki", "uid": "lk1"},
				"Prom east": map[string]any{"type": "prometheus", "uid": "pm1"}, "TestData": map[string]any{"type": "testdata", "uid": "td"}}})
		case "/api/datasources/proxy/uid/lk1/loki/api/v1/labels":
			j(map[string]any{"data": []string{"app", "container", "unit", "filename"}})
		case "/api/datasources/proxy/uid/lk1/loki/api/v1/label/app/values":
			j(map[string]any{"data": []string{"cart", "checkout"}})
		case "/api/datasources/proxy/uid/lk1/loki/api/v1/label/container/values":
			j(map[string]any{"data": []string{"orders_order-service.1.abcdefghijklmnopqrstuvwxy", "orders_order-service.2.bcdefghijklmnopqrstuvwxyz",
				"0123456789abcdef0123456789abcdef", "bad value!"}})
		case "/api/datasources/proxy/uid/lk1/loki/api/v1/label/unit/values":
			j(map[string]any{"data": []string{"session-42.scope", "user@1000.service", "search.service"}})
		case "/api/datasources/proxy/uid/pm1/api/v1/label/job/values":
			j(map[string]any{"data": []string{"catalog-api", "node-exporter"}})
		case "/api/search":
			j([]any{map[string]any{"uid": "pay", "title": "Payment gateway", "folderTitle": "Payments"}, map[string]any{"uid": "n1", "title": "Nodes"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	e := setup(t, srv, "grafana")
	r := e.run(t, true)
	if r.Status != "ok" || r.Counts["loki"] != 4 || r.Counts["prom-job"] != 2 || r.Counts["dashboard"] != 2 {
		t.Fatalf("grafana: %+v %v", r, e.inv(t))
	}
	inv := e.inv(t)
	if a := inv["loki container=orders_order-service"]; a == nil || a["prefix"] != true {
		t.Fatalf("swarm task names not folded: %v", inv)
	}
	if inv["loki unit=search.service"] == nil || inv["loki unit=session-42.scope"] != nil || inv["dashboard n1"]["folder"] != "General" {
		t.Fatalf("noise or folder: %v", inv)
	}
	// one Loki read fails: partial, the rest kept, nothing pruned
	e.o.Caps = &Caps{LokiDatasources: 3, PromDatasources: 0, LokiValues: 1, Dashboards: 5000}
	r = e.run(t, true)
	if r.Status != "partial" || !strings.Contains(r.Error, "more than 1 values") || len(e.inv(t)) != len(inv) {
		t.Fatalf("partial grafana: %+v", r)
	}
}

func TestSentryCursorAndElasticsearch(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/0/organizations/":
			j([]any{map[string]any{"slug": "northwind"}})
		case "/api/0/organizations/northwind/projects/":
			if r.URL.Query().Get("cursor") == "" {
				w.Header().Set("Link", `<`+srvURL+`/api/0/organizations/northwind/projects/?cursor=0:100:0>; rel="previous"; results="false", `+
					`<`+srvURL+`/api/0/organizations/northwind/projects/?cursor=0%3A100%3A0>; rel="next"; results="true"`)
				j([]any{map[string]any{"slug": "cart", "platform": "python"}})
			} else {
				w.Header().Set("Link", `<x?cursor=0:200:0>; rel="next"; results="false"`)
				j([]any{map[string]any{"slug": "checkout", "platform": "node"}})
			}
		case "/_cat/indices":
			j([]any{map[string]any{"index": "logs-cart-2026.11.01"}, map[string]any{"index": "logs-cart-2026.11.02"},
				map[string]any{"index": "orders-000012"}, map[string]any{"index": ".kibana_1"}, map[string]any{"index": "plain"}})
		case "/_data_stream":
			j(map[string]any{"data_streams": []any{map[string]any{"name": "logs-search", "indices": []any{1, 2}}, map[string]any{"name": ".hidden"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	e := setup(t, srv, "sentry")
	if r := e.run(t, true); r.Status != "ok" || r.Counts["project"] != 2 || r.Calls != 3 {
		t.Fatalf("sentry: %+v", r)
	}
	if a := e.inv(t)["project northwind/checkout"]; a == nil || a["platform"] != "node" {
		t.Fatalf("sentry inventory: %v", e.inv(t))
	}
	e2 := setup(t, srv, "elasticsearch")
	e2.o.Sources["elasticsearch/main"].Username = "reader"
	if r := e2.run(t, true); r.Status != "ok" || r.Counts["index"] != 4 {
		t.Fatalf("es: %+v %v", r, e2.inv(t))
	}
	inv := e2.inv(t)
	if inv["index logs-cart-*"]["indices"] != float64(2) || inv["index orders-*"] == nil || inv["index logs-search"]["data_stream"] != true || inv["index plain"] == nil {
		t.Fatalf("es patterns: %v", inv)
	}
}

func TestFoldAndPattern(t *testing.T) {
	for in, want := range map[string]string{"logs-shop-2026.11.03": "logs-shop-*", "orders-000012": "orders-*", "app_20261103": "app_*",
		"plain": "plain", "metrics-2026-11": "metrics-*"} {
		if got := IndexPattern(in); got != want {
			t.Errorf("IndexPattern(%q) = %q, want %q", in, got, want)
		}
	}
	if v, task := FoldValue("web_front.3.q1w2e3r4t5y6u7i8o9p0asdfg"); v != "web_front" || !task {
		t.Errorf("fold: %q %v", v, task)
	}
	if v, _ := FoldValue("run-r1234.scope"); v != "" {
		t.Errorf("noise kept: %q", v)
	}
}
