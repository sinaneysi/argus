package estate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The parity fixtures (testdata/parity) come from the earlier job (inventory.py collect, vmstate.py all, vmstate.py
// pulse; two rounds 6 hours apart, frozen time) run against a fake Prometheus with synthetic answers; fixtures.json
// holds every answer by round, site and query. The Go jobs must ask the same queries and write the same files
// (byte for byte) and the same rows.

type fixtures map[string]map[string]map[string]map[string][]map[string]any // round -> site -> instant|range -> key -> rows

func loadFixtures(t *testing.T) fixtures {
	b, err := os.ReadFile("testdata/parity/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// fakeProm serves site-a as a Grafana (data-source proxy) and site-b as a plain Prometheus.
func fakeProm(t *testing.T, fx fixtures, round *atomic.Value, missing *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		site, path := "site-b", r.URL.Path
		if strings.HasPrefix(path, "/a/") {
			site, path = "site-a", strings.TrimPrefix(path, "/a")
			if r.Header.Get("Authorization") != "Bearer tok-a" {
				w.WriteHeader(401)
				return
			}
			if path == "/api/frontend/settings" {
				w.Write([]byte(`{"datasources":{"Loki":{"type":"loki","uid":"lk"},"Prom":{"type":"prometheus","uid":"pm1"},"Prom2":{"type":"prometheus","uid":"pm2"}}}`))
				return
			}
			if !strings.HasPrefix(path, "/api/datasources/proxy/uid/pm1/") {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			path = strings.TrimPrefix(path, "/api/datasources/proxy/uid/pm1")
		} else {
			path = strings.TrimPrefix(path, "/b")
		}
		rd := fx[round.Load().(string)][site]
		q := r.URL.Query().Get("query")
		var rows []map[string]any
		var ok bool
		var result []map[string]any
		switch path {
		case "/api/v1/query":
			rows, ok = rd["instant"][q]
			for _, x := range rows {
				result = append(result, map[string]any{"metric": x["metric"], "value": []any{1767000000, x["value"]}})
			}
		case "/api/v1/query_range":
			rows, ok = rd["range"][q+"|"+r.URL.Query().Get("step")]
			for _, x := range rows {
				result = append(result, map[string]any{"metric": x["metric"], "values": x["values"]})
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !ok {
			*missing = append(*missing, site+" "+path+" "+q)
		}
		if result == nil {
			result = []map[string]any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
}

func dumpDB(t *testing.T, path string) any {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]any{}
	for _, tq := range [][2]string{{"vms", "id"}, {"vm_changes", "rowid"}, {"vm_state", "vm_id"}} {
		rows, err := queryRows(db, "SELECT * FROM "+tq[0]+" ORDER BY "+tq[1])
		if err != nil {
			t.Fatal(err)
		}
		l := []any{}
		for _, r := range rows {
			l = append(l, map[string]any(r))
		}
		out[tq[0]] = l
	}
	b, _ := json.Marshal(out)
	var v any
	json.Unmarshal(b, &v)
	return v
}

func readAny(t *testing.T, path string) any {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(path, err)
	}
	return v
}

func TestParityWithEarlierJob(t *testing.T) {
	fx := loadFixtures(t)
	var round atomic.Value
	round.Store("1")
	var missing []string
	srv := fakeProm(t, fx, &round, &missing)
	defer srv.Close()
	dir := t.TempDir()
	for _, f := range []string{"service-map.json", "proxy-live.json", "capacity.json"} {
		b, _ := os.ReadFile(filepath.Join("testdata/parity/seed", f))
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	now := time.Unix(1767000000, 0)
	cl := &Client{HTTP: srv.Client(), Timeout: 10 * time.Second}
	o := &Options{DB: filepath.Join(dir, "ops.db"), StateDir: dir, Client: cl,
		Sites:       []*Site{{Name: "site-a", URL: srv.URL + "/a", Token: "tok-a", Grafana: true}, {Name: "site-b", URL: srv.URL + "/b"}},
		EnvPrefixes: [][2]string{{"develop", "10.0.9."}, {"pre-release", "10.0.8."}, {"release", "10.0."}},
		AppUnits:    []string{"app-", "panel_"}, Now: func() time.Time { return now }}
	ctx := context.Background()
	for _, r := range []string{"1", "2"} {
		round.Store(r)
		if r == "2" {
			now = now.Add(6 * time.Hour)
		}
		if _, err := o.Inventory(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := o.VMState(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := o.Pulse(ctx); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join("testdata/parity", "r"+r)
		files := []string{"inventory-status.json", "vmstate-status.json", "vm-pulse.json", "proxy-live.json"}
		vm, _ := filepath.Glob(filepath.Join(want, "vm", "*.json"))
		for _, f := range vm {
			files = append(files, filepath.Join("vm", filepath.Base(f)))
		}
		for _, f := range files {
			w, _ := os.ReadFile(filepath.Join(want, f))
			g, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Errorf("round %s: %s: %v", r, f, err)
				continue
			}
			if !bytes.Equal(w, g) {
				t.Errorf("round %s: %s differs\nwant %s\n got %s", r, f, w, g)
			}
		}
		if g, w := dumpDB(t, o.DB), readAny(t, filepath.Join(want, "db.json")); !reflect.DeepEqual(g, w) {
			gb, _ := json.MarshalIndent(g, "", " ")
			wb, _ := json.MarshalIndent(w, "", " ")
			t.Errorf("round %s: rows differ\nwant %s\n got %s", r, wb, gb)
		}
	}
	if len(missing) > 0 {
		t.Errorf("queries the earlier job did not ask:\n%s", strings.Join(missing, "\n"))
	}
	if cl.Errors != 0 {
		t.Errorf("%d failed queries", cl.Errors)
	}
}

func TestNoSiteAndFailures(t *testing.T) {
	o := &Options{DB: filepath.Join(t.TempDir(), "ops.db"), StateDir: t.TempDir()}
	if _, err := o.Inventory(context.Background()); err != ErrNoSite {
		t.Fatalf("want ErrNoSite, got %v", err)
	}
	// a site that refuses: the inventory is empty (WARN), nothing fails hard, the error is counted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	o.Client = &Client{HTTP: srv.Client(), Timeout: time.Second}
	o.Sites = []*Site{{Name: "x", URL: srv.URL, Grafana: true}}
	st, err := o.Inventory(context.Background())
	if err != nil || st["status"] != "WARN" || o.Client.Errors == 0 {
		t.Fatalf("got %v %v errors=%d", st, err, o.Client.Errors)
	}
	b, _ := os.ReadFile(filepath.Join(o.StateDir, "inventory-status.json"))
	if !strings.Contains(string(b), `"prom_errors"`) {
		t.Fatalf("status without prom_errors: %s", b)
	}
	if _, err := o.VMState(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "inventory is empty") {
		t.Fatalf("want empty inventory, got %v", err)
	}
}

func TestSelectorAndUnitPrefixes(t *testing.T) {
	if _, ok := selector(`x",job="y`); ok {
		t.Fatal("selector accepted a quote")
	}
	o := &Options{AppUnits: []string{`bad"`, "svc-"}}
	if got := o.unitNames(); !strings.Contains(got, `(svc-).*`) || strings.Contains(got, `bad`) {
		t.Fatalf("unit names %s", got)
	}
	if o.kindOf("svc-shop.service") != "app" || o.kindOf("nginx.service") != "infra" || o.kindOf("node_exporter.service") != "agent" {
		t.Fatal("kinds")
	}
}
