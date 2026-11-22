package googlebot

// Parity with the earlier Python job on synthetic fixtures (testdata/golden.json, written by running that job's
// functions on testdata/lines.json, the detect-*.sql.gz histories, sitemap.xml, paths.tsv and ranges.json).
// Listed differences: hist and cache JSON keys are sorted; the upstream kinds have neutral names; the p95 latency
// notice says p95 (the earlier text said p50); the primary edge group is the first one of the groups file.

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/migrate"
)

type golden struct {
	Pure struct {
		Normalize map[string][2]string `json:"normalize"`
		Unjson    map[string]string    `json:"unjson"`
		Crawler   map[string]string    `json:"crawler"`
		HBin      map[string]int       `json:"hbin"`
		HQuant    []*float64           `json:"hquant"`
		PClass    map[string]string    `json:"pclass"`
		Locs      [][2]string          `json:"locs"`
		ISO       map[string]*int64    `json:"iso"`
	} `json:"pure"`
	Tables map[string][]map[string]any `json:"tables"`
	Detect map[string]struct {
		Now      int64 `json:"now"`
		Findings []struct {
			Rule, Key, Severity string
			Observed, Baseline  *float64
			Detail              map[string]any
			Message, Resolved   string
		} `json:"findings"`
	} `json:"detect"`
}

func load(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// zone is the fixtures' zone: UTC+03:30 without daylight saving time, so a bucket crosses the local midnight.
func zone(t *testing.T) *time.Location { return time.FixedZone("local", 3*3600+1800) }

func classes(t *testing.T) []Class {
	c, err := LoadClassesFile("testdata/paths.tsv")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPureParity(t *testing.T) {
	g := load(t)
	cl := classes(t)
	for u, want := range g.Pure.Normalize {
		a, b := Normalize(unjson(u))
		if a != want[0] || b != want[1] {
			t.Errorf("Normalize(%q) = %q %q, want %q %q", u, a, b, want[0], want[1])
		}
	}
	for u, want := range g.Pure.Unjson {
		if got := unjson(u); got != want {
			t.Errorf("unjson(%q) = %q, want %q", u, got, want)
		}
	}
	for u, want := range g.Pure.Crawler {
		if got := Crawler(unjson(u)); got != want {
			t.Errorf("Crawler(%q) = %q, want %q", u, got, want)
		}
	}
	for k, want := range g.Pure.HBin {
		ms, _ := strconv.ParseFloat(k, 64)
		if got := HBin(ms); got != want {
			t.Errorf("HBin(%v) = %d, want %d", ms, got, want)
		}
	}
	h := Hist{"0": 3, "5": 10, "20": 4, "40": 1}
	for i, q := range []float64{0.1, 0.5, 0.95, 0.99, 1.0} {
		got, ok := h.Quantile(q)
		if w := g.Pure.HQuant[i]; !ok || w == nil || math.Abs(got-*w) > 1e-9 {
			t.Errorf("Quantile(%v) = %v, want %v", q, got, w)
		}
	}
	for u, want := range g.Pure.PClass {
		n, _ := Normalize(unjson(u))
		if got := PClass(n, cl); got != want {
			t.Errorf("PClass(%q) = %q, want %q", n, got, want)
		}
	}
	f, _ := os.Open("testdata/sitemap.xml")
	defer f.Close()
	var locs [][2]string
	if err := Locs(f, func(l, m string) { locs = append(locs, [2]string{l, m}) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(locs, g.Pure.Locs) {
		t.Errorf("Locs = %v, want %v", locs, g.Pure.Locs)
	}
	for s, want := range g.Pure.ISO {
		got, ok := ISO(s)
		if (want == nil) != !ok || want != nil && got != *want {
			t.Errorf("ISO(%q) = %d %v, want %v", s, got, ok, want)
		}
	}
}

func nets(t *testing.T) Nets {
	r := &Ranges{Path: "testdata/ranges.json"}
	ns, ver := r.Load(context.Background())
	if ver != "2026-10-28T23:00:00.000000" || len(ns) != 3 {
		t.Fatalf("ranges %v %d", ver, len(ns))
	}
	return ns
}

var upNames = map[string]string{"iran-origin": "origin", "nginx": "edge", "foreign-cache": "cache", "local": "local", "external": "external"}

func TestAggregateParity(t *testing.T) {
	g := load(t)
	loc := zone(t)
	var fx struct {
		Bucket, Width int64
		Lines         []struct {
			Group, Label, Text string
			TS                 int64
		}
	}
	b, _ := os.ReadFile("testdata/lines.json")
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ns, cl := nets(t), classes(t)
	for _, grp := range []string{"primary", "cache"} {
		a := &Aggregator{Cache: grp == "cache", Nets: ns, Classes: cl, Loc: loc}
		for _, l := range fx.Lines {
			if l.Group == grp {
				a.Add(Line{l.Label, l.TS, l.Text})
			}
		}
		if err := inTx(db, func(tx *sql.Tx) error { return a.Store(tx, fx.Bucket, fx.Width, grp) }); err != nil {
			t.Fatal(err)
		}
	}
	cmpTable(t, db, g.Tables["gw_agg"], "gw_agg", []string{"edge", "host", "who", "crawler", "pclass", "upstream"})
	cmpTable(t, db, g.Tables["gw_url_day"], "gw_url_day", []string{"day", "host", "url"})
	cmpTable(t, db, g.Tables["gw_url_var"], "gw_url_var", []string{"day", "host", "url", "qhash"})
	cmpTable(t, db, g.Tables["gw_url"], "gw_url", []string{"host", "url"})
	var n int
	db.QueryRow("SELECT count(*) FROM gw_errors").Scan(&n)
	if n == 0 {
		t.Error("no gw_errors rows from the 5xx lines")
	}
}

func rowKey(r map[string]any, keys []string) string {
	var p []string
	for _, k := range keys {
		p = append(p, toS(r[k]))
	}
	return strings.Join(p, "\x00")
}

func toS(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return x
	case []byte:
		return string(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func cmpTable(t *testing.T, db *sql.DB, want []map[string]any, table string, keys []string) {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	got := map[string]map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		got[rowKey(m, keys)] = m
	}
	rows.Close()
	if len(got) != len(want) {
		t.Errorf("%s: %d rows, want %d", table, len(got), len(want))
	}
	for _, w := range want {
		if u, ok := w["upstream"].(string); ok {
			w["upstream"] = upNames[u]
		}
		k := rowKey(w, keys)
		gr, ok := got[k]
		if !ok {
			t.Errorf("%s: no row %q", table, k)
			continue
		}
		for c, wv := range w {
			gv := gr[c]
			switch c {
			case "hist", "cache":
				var a, b map[string]float64
				json.Unmarshal([]byte(toS(wv)), &a)
				json.Unmarshal([]byte(toS(gv)), &b)
				if !reflect.DeepEqual(a, b) {
					t.Errorf("%s %q %s = %v, want %v", table, k, c, b, a)
				}
				continue
			}
			if wf, ok := wv.(float64); ok {
				gf, _ := strconv.ParseFloat(toS(gv), 64)
				if math.Abs(wf-gf) > 1e-6 {
					t.Errorf("%s %q %s = %v, want %v", table, k, c, gv, wv)
				}
				continue
			}
			if toS(wv) != toS(gv) {
				t.Errorf("%s %q %s = %q, want %q", table, k, c, toS(gv), toS(wv))
			}
		}
	}
}

func loadSQL(t *testing.T, db *sql.DB, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	sc := bufio.NewScanner(z)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if _, err := tx.Exec(sc.Text()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func near(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) <= 1e-6*math.Max(1, math.Abs(*b))
}

func TestDetectorParity(t *testing.T) {
	g := load(t)
	loc := zone(t)
	for _, name := range []string{"A", "B"} {
		sc := g.Detect[name]
		dir := t.TempDir()
		o := &Options{DB: filepath.Join(dir, "g.db"), Groups: []Group{{Label: "primary", Parser: "json"}}, Loc: loc,
			TZLabel: "local", SiteHost: "www.example.com"}
		db, err := Open(context.Background(), o.DB)
		if err != nil {
			t.Fatal(err)
		}
		loadSQL(t, db, "testdata/detect-"+name+".sql.gz")
		found := o.Evaluate(db, sc.Now, true)
		byKey := map[string]Finding{}
		for _, f := range found {
			byKey[f.Rule+"/"+f.Key] = f
		}
		var want, have []string
		for _, w := range sc.Findings {
			want = append(want, w.Rule+"/"+w.Key)
			f, ok := byKey[w.Rule+"/"+w.Key]
			if !ok {
				continue
			}
			if f.Severity != w.Severity || !near(f.Observed, w.Observed) || !near(f.Baseline, w.Baseline) {
				t.Errorf("%s %s/%s: %s %v %v, want %s %v %v", name, w.Rule, w.Key, f.Severity, *f.Observed, *f.Baseline,
					w.Severity, *w.Observed, *w.Baseline)
			}
			gd, _ := json.Marshal(f.Detail)
			var gm map[string]any
			json.Unmarshal(gd, &gm)
			if !reflect.DeepEqual(gm, w.Detail) {
				t.Errorf("%s %s/%s detail %v, want %v", name, w.Rule, w.Key, gm, w.Detail)
			}
			msg := strings.Replace(w.Message, "Googlebot p50 latency", "Googlebot "+w.Key+" latency", 1)
			if m := Message(f.Rule, f.Key, f.Severity, f.Observed, f.Baseline, sc.Now-120, false, loc, "local"); m != msg {
				t.Errorf("message\n got %s\nwant %s", m, msg)
			}
		}
		for k := range byKey {
			have = append(have, k)
		}
		sort.Strings(want)
		sort.Strings(have)
		if !reflect.DeepEqual(want, have) {
			t.Errorf("%s findings %v, want %v", name, have, want)
		}
		db.Close()
	}
}

func TestAlertLifecycle(t *testing.T) {
	loc := zone(t)
	g := load(t)
	sc := g.Detect["A"]
	var sent []string
	o := &Options{DB: filepath.Join(t.TempDir(), "g.db"), Groups: []Group{{Label: "primary", Parser: "json"}}, Loc: loc,
		SiteHost: "www.example.com", Notify: func(_ context.Context, s string) error { sent = append(sent, s); return nil }}
	db, err := Open(context.Background(), o.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loadSQL(t, db, "testdata/detect-A.sql.gz")
	if _, err := o.detect(context.Background(), db, sc.Now, true); err != nil {
		t.Fatal(err)
	}
	n := len(sent)
	if n != len(sc.Findings) {
		t.Fatalf("sent %d notices, want %d", n, len(sc.Findings))
	}
	o.detect(context.Background(), db, sc.Now, true) // same state: no new notice
	if len(sent) != n {
		t.Fatalf("repeat notices: %v", sent[n:])
	}
	db.Exec("DELETE FROM gw_agg")
	db.Exec("DELETE FROM gw_buckets")
	db.Exec("DELETE FROM gw_state WHERE k LIKE 'gw_daily:%'")
	o.detect(context.Background(), db, sc.Now, true)
	var open int
	db.QueryRow("SELECT count(*) FROM gw_alerts WHERE resolved_ts IS NULL").Scan(&open)
	resolved := 0
	for _, s := range sent[n:] {
		if strings.HasPrefix(s, "RESOLVED") {
			resolved++
		}
	}
	// every alert resolves; the information alert (fake_spike) without a notice
	if open != 0 || resolved != n-1 {
		t.Fatalf("open %d, resolved notices %d: %v", open, resolved, sent[n:])
	}
}

func TestGroupsFile(t *testing.T) {
	gs, err := ParseGroups(strings.NewReader("# edges\nprimary\tjson\t{job=\"nginx\"}\ncache\tcache\t{job=\"cache\"}\twww.example.com\n"))
	if err != nil || len(gs) != 2 || gs[1].Host != "www.example.com" {
		t.Fatalf("%v %v", gs, err)
	}
	q := gs[0].Query("hostname")
	if !strings.Contains(q, `{job="nginx"} |~ "Googlebot|`) || !strings.Contains(q, "{{.remote_addr}}") || !strings.HasSuffix(q, "keep hostname") {
		t.Fatal(q)
	}
	for _, bad := range []string{"a\tjson\tnginx", "a\txml\t{x=\"1\"}", "a\tcache\t{x=\"1\"}", "a\tjson\t{x=\"1\"} | line_format \"x\"",
		"a\tjson\t{x=\"1\"}\na\tjson\t{x=\"2\"}", ""} {
		if _, err := ParseGroups(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSEOSnapshot(t *testing.T) {
	loc := zone(t)
	dir := t.TempDir()
	g, err := Open(context.Background(), filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var fx struct {
		Bucket, Width int64
		Lines         []struct {
			Group, Label, Text string
			TS                 int64
		}
	}
	b, _ := os.ReadFile("testdata/lines.json")
	json.Unmarshal(b, &fx)
	ns, cl := nets(t), classes(t)
	for _, grp := range []string{"primary", "cache"} {
		a := &Aggregator{Cache: grp == "cache", Nets: ns, Classes: cl, Loc: loc}
		for _, l := range fx.Lines {
			if l.Group == grp {
				a.Add(Line{l.Label, l.TS, l.Text})
			}
		}
		inTx(g, func(tx *sql.Tx) error { return a.Store(tx, fx.Bucket, fx.Width, grp) })
	}
	for _, ts := range []int64{fx.Bucket - 1200, fx.Bucket - 600, fx.Bucket} {
		g.Exec("INSERT INTO gw_buckets VALUES (?,600,'primary',1,0,0,0,NULL,0)", ts)
	}
	if _, err := migrate.ApplyFile(context.Background(), filepath.Join(dir, "ops.db"), migrate.Ops); err != nil {
		t.Fatal(err)
	}
	ops, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "ops.db"))
	defer ops.Close()
	os.WriteFile(filepath.Join(dir, "svc.json"), []byte(`{"backends": {"10.0.0.5:80": [{"alias": "shop-api", "location": "/", "domain": "api.example.com"}]}}`), 0o600)
	s := SEOOptions{Primary: "primary", Hosts: []string{"www.example.com", "example.com"}, ServiceMap: filepath.Join(dir, "svc.json"), SiteHost: "www.example.com"}
	ts, err := SEOSnapshot(g, ops, s, fx.Bucket+600)
	if err != nil || ts != fx.Bucket+300 {
		t.Fatalf("ts %d err %v", ts, err)
	}
	if again, _ := SEOSnapshot(g, ops, s, fx.Bucket+600); again != 0 {
		t.Fatal("the same window twice")
	}
	var n, hits, verified, total int64
	ops.QueryRow("SELECT count(*), sum(verified), sum(total) FROM seo_snapshots WHERE ts=? AND offset_d=0", ts).Scan(&n, &verified, &total)
	ops.QueryRow("SELECT count(*) FROM seo_snapshots WHERE hits IS NOT NULL").Scan(&hits)
	if n == 0 || hits == 0 || verified == 0 || verified >= total {
		t.Fatalf("rows %d, cache rows %d, verified %d of %d", n, hits, verified, total)
	}
	var hostRows int64
	ops.QueryRow("SELECT count(*) FROM seo_snapshots WHERE host NOT IN ('www.example.com','example.com','www.example.com/blog','example.com/blog')").Scan(&hostRows)
	if hostRows != 0 {
		t.Fatal("a host outside the list")
	}
	var errs, named int64
	ops.QueryRow("SELECT count(*), sum(alias<>'') FROM seo_errors WHERE ts=?", ts).Scan(&errs, &named)
	if errs == 0 || errs > 10 || named == 0 {
		t.Fatalf("seo_errors %d (named %d)", errs, named)
	}
}
