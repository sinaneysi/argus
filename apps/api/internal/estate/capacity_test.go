package estate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Capacity parity: the earlier capacity.py collect, run after inventory.py collect against the same fake
// Prometheus (testdata/capacity, two rounds; the second keeps the first as capacity-prev.json). Listed differences:
// texts that named the earlier installation ("both DCs", "either DC", "run inventory.py collect").
func TestCapacityParity(t *testing.T) {
	fx := loadFixtures(t, "testdata/capacity/fixtures.json")
	var round atomic.Value
	round.Store("1")
	var missing []string
	srv := fakeProm(t, fx, &round, &missing)
	defer srv.Close()
	dir := t.TempDir()
	now := time.Unix(1767000000, 0)
	cl := &Client{HTTP: srv.Client(), Timeout: 10 * time.Second}
	o := &Options{DB: filepath.Join(dir, "ops.db"), StateDir: dir, Client: cl,
		Sites:       []*Site{{Name: "site-a", URL: srv.URL + "/a", Token: "tok-a", Grafana: true}, {Name: "site-b", URL: srv.URL + "/b"}},
		EnvPrefixes: [][2]string{{"develop", "10.0.9."}, {"pre-release", "10.0.8."}, {"release", "10.0."}},
		Now:         func() time.Time { return now }}
	fix := strings.NewReplacer("both DCs", "every site", "either DC", "any site", "(run inventory.py collect)", "(run argus estate inventory)")
	for _, r := range []string{"1", "2"} {
		round.Store(r)
		if r == "2" {
			now = now.Add(6 * time.Hour)
		}
		if _, err := o.Inventory(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := o.Capacity(context.Background()); err != nil {
			t.Fatal(err)
		}
		files := []string{"capacity.json"}
		if r == "2" {
			files = append(files, "capacity-prev.json")
		}
		for _, f := range files {
			w, _ := os.ReadFile(filepath.Join("testdata/capacity", "r"+r, f))
			g, _ := os.ReadFile(filepath.Join(dir, f))
			if want := fix.Replace(string(w)); want != string(g) {
				t.Errorf("round %s: %s differs\nwant %s\n got %s", r, f, want, g)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("queries the earlier job did not ask:\n%s", strings.Join(missing, "\n"))
	}
}

func TestCapacityLevels(t *testing.T) {
	for _, c := range []struct {
		days, free any
		want       string
	}{{6.9, 50.0, "critical"}, {13.0, 50.0, "warn"}, {nil, 9.0, "full"}, {20.0, 50.0, "watch"}, {nil, nil, "ok"}, {40.0, 5.0, "full"}} {
		if got := capLevel(c.days, c.free); got != c.want {
			t.Errorf("capLevel(%v, %v) = %s, want %s", c.days, c.free, got, c.want)
		}
	}
	if capHotLevel("cpu_p95", 97.0) != "critical" || capHotLevel("cpu_p95", 90.0) != "warn" || capHotLevel("io_p95", 99.0) != "ok" {
		t.Fatal("hot levels")
	}
}
