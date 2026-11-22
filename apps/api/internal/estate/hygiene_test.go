package estate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Hygiene parity: the earlier hygiene.py collect on the same synthetic snapshot, counters and dead targets
// (testdata/hygiene/parity.json; the dead targets were fed in place of its Prometheus script). Same findings, order,
// counts, stats days and summary line.
func TestHygieneParity(t *testing.T) {
	b, err := os.ReadFile("testdata/hygiene/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Now      int64                          `json:"now"`
		Snapshot json.RawMessage                `json:"snapshot"`
		Stats    json.RawMessage                `json:"stats"`
		Dead     map[string][]map[string]string `json:"dead"`
		Want     any                            `json:"want"`
		Line     string                         `json:"line"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
		if r.Method != http.MethodGet || r.URL.Query().Get("query") != hygDeadQuery || !strings.HasSuffix(r.URL.Path, "/api/v1/query") {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		var res []any
		for _, m := range g.Dead[site] {
			res = append(res, map[string]any{"metric": m, "value": []any{1, "0"}})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alerts-firing.json"), g.Snapshot, 0o644)
	os.WriteFile(filepath.Join(dir, "watch-stats.json"), g.Stats, 0o644)
	o := &Options{StateDir: dir, Client: &Client{HTTP: srv.Client(), Timeout: 5 * time.Second},
		Sites: []*Site{{Name: "site-a", URL: srv.URL + "/site-a"}, {Name: "site-b", URL: srv.URL + "/site-b"}},
		Now:   func() time.Time { return time.Unix(g.Now, 0) }}
	line, err := o.Hygiene(context.Background())
	if err != nil || line+"\n" != g.Line {
		t.Errorf("line %q %v, want %q", line, err, g.Line)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "hygiene.json"))
	var got any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, g.Want) {
		a, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(g.Want, "", " ")
		la, lw := strings.Split(string(a), "\n"), strings.Split(string(w), "\n")
		for i := 0; i < len(la) && i < len(lw); i++ {
			if la[i] != lw[i] {
				t.Fatalf("hygiene.json differs at line %d:\n got %s\nwant %s", i, la[i], lw[i])
			}
		}
		t.Fatalf("hygiene.json length differs")
	}
}

func TestHygieneNoSiteAndFailedQuery(t *testing.T) {
	if _, err := (&Options{}).Hygiene(context.Background()); err != ErrNoSite {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	dir := t.TempDir()
	o := &Options{StateDir: dir, Client: &Client{HTTP: srv.Client(), Timeout: 5 * time.Second}, Sites: []*Site{{Name: "site-a", URL: srv.URL}}}
	line, err := o.Hygiene(context.Background())
	if err != nil || line != "hygiene: nothing; 1 errors" {
		t.Fatalf("%q %v", line, err)
	}
}
