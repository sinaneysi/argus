package find

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/sources"
	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
	_ "modernc.org/sqlite"
)

// Obviously fake material (Northwind is a neutral example name).
const cred = "fake-find-cred-zzzzzzzzzzzzzzzzzzzzzzzz"

func svcs() *catalogue.Services {
	defs := map[string]catalogue.ServiceDef{"shop/payment": {GitLab: "northwind/shop/payment"}, "shop/orders": {},
		"shop/search": {}, "shop/billing-ts": {}, "shop/admin": {}, "shop/admin-backend": {},
		"web/storefront": {Domains: []string{"shop.northwind.example"}}, "shop/order-payment-ts": {},
		"web/notification-service": {}, "shop/notification": {}}
	return catalogue.NewServices(defs, nil)
}

func TestResolve(t *testing.T) {
	s := svcs()
	for text, want := range map[string][]string{
		"payment": {"shop/payment"}, "scan the payment repo": {"shop/payment"},
		"shop/admin-backend": {"shop/admin-backend"}, "SHOP/PAYMENT": {"shop/payment"},
		"why is payment-api slow": {"shop/payment"}, "payment service errors": {"shop/payment"},
		"billing-ts deploys": {"shop/billing-ts"}, "billing logs": {"shop/billing-ts"},
		"order payment ts pipeline": {"shop/order-payment-ts"}, "shop.northwind.example is slow": {"web/storefront"},
		"search": {"shop/search"}, "the search service": {"shop/search"}, "orders repo": {"shop/orders"},
	} {
		if got := ResolveService(text, s).Services; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	for _, text := range []string{"search the logs for errors", "why are orders failing", "the api is down", "admin",
		"check the app", "show me the latest deploy", "", "release pipeline stuck", "order logs"} {
		if got := ResolveService(text, s).Services; len(got) != 0 {
			t.Errorf("%q: %v, want nothing", text, got)
		}
	}
	r := ResolveService("payment and storefront", s)
	if !reflect.DeepEqual(r.Services, []string{"shop/payment", "web/storefront"}) || !r.Ambiguous {
		t.Fatalf("several: %+v", r)
	}
	if !AboutRelease("payment errors on production") || !AboutRelease("prod payment") || AboutRelease("payment on develop main branch") {
		t.Fatal("release question")
	}
}

func TestLimits(t *testing.T) {
	if l := LimitsFrom(func(string) string { return "" }); l != DefaultLimits() {
		t.Fatalf("defaults: %+v", l)
	}
	vals := map[string]string{"ARGUS_SOURCES_FIND_MIN_CONF": "7", "ARGUS_SOURCES_FIND_TOP": "9",
		"ARGUS_SOURCES_FANOUT_MAX_INSTANCES": "0", "ARGUS_SOURCES_FANOUT_MAX_SECONDS": "x"}
	l := LimitsFrom(func(k string) string { return vals[k] })
	if l.MinConf != 1 || l.Top != 3 || l.MaxInstances != 1 || l.MaxSeconds != 60 {
		t.Fatalf("clamped: %+v", l)
	}
	for _, a := range []string{"kubernetes", "kube", "prom", "elasticsearch", "opensearch", "logs", "GitLab"} {
		if _, ok := Canon(a); !ok {
			t.Fatal(a)
		}
	}
	if _, ok := Canon("ftp"); ok {
		t.Fatal("ftp")
	}
}

// --- the catalogue -----------------------------------------------------------------------------------------------------

func mkdb(t *testing.T) (*sql.DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ops.db")
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=synchronous(OFF)&_pragma=journal_mode(MEMORY)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := catalogue.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, p
}

func route(t *testing.T, db *sql.DB, svc, src, kind, loc string, conf float64, status string, stale int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO source_routes (service, source, kind, locator, confidence, origin, status, rule, evidence, stale,
  created_at, updated_at) VALUES (?,?,?,?,?,'discovered',?,'r','why',?,1,1)`, svc, src, kind, loc, conf, status, stale); err != nil {
		t.Fatal(err)
	}
}

var reg = map[string]*sources.Entry{
	"gitlab/main":    {Type: "gitlab", Name: "main", Env: "shared", Purpose: "agent", Status: "readonly", Enabled: true, URL: "https://gl.northwind.example"},
	"gitlab/tracker": {Type: "gitlab", Name: "tracker", Env: "shared", Purpose: "tracker", Status: "ok", URL: "https://gl.northwind.example"},
	"grafana/east":   {Type: "grafana", Name: "east", Env: "release", Purpose: "agent", Status: "readonly", Enabled: true, URL: "https://grafana-east.northwind.example"},
	"grafana/west":   {Type: "grafana", Name: "west", Env: "develop", Purpose: "agent", Status: "failed", URL: "https://grafana-west.northwind.example"},
	"kubernetes/k1":  {Type: "kubernetes", Name: "k1", Env: "develop", Purpose: "agent", Status: "readonly", Enabled: true},
}

func opts(db *sql.DB, entries map[string]*sources.Entry, lim Limits, audit string) Options {
	return Options{DB: db, Services: svcs(), Entries: entries, Limits: lim, AuditPath: audit,
		Now: func() time.Time { return time.Unix(1793318400, 0) }}
}

func one(r Result, typ string) *TypeResult { return r.Services[0].Types[typ] }

func locs(d *TypeResult) []string {
	var out []string
	for _, x := range d.Targets {
		out = append(out, x.Locator)
	}
	return out
}

func TestRanking(t *testing.T) {
	db, _ := mkdb(t)
	lim := DefaultLimits()
	route(t, db, "shop/payment", "gitlab/main", "project", "northwind/shop/payment", 1.0, "confirmed", 0)
	route(t, db, "shop/payment", "gitlab/main", "project", "northwind/old/payment", 0.9, "proposed", 0)
	d := one(Find("payment", []string{"gitlab"}, false, true, opts(db, reg, lim, "")), "gitlab")
	if d.Decision != "use" || !reflect.DeepEqual(locs(d), []string{"northwind/shop/payment"}) || !strings.Contains(d.Targets[0].Tool, "no read tool") {
		t.Fatalf("one confirmed: %+v", d)
	}
	// several confirmed: the top 3 by confidence; a proposed route never beats a confirmed one
	for i, c := range []float64{0.7, 0.9, 0.8, 0.6} {
		route(t, db, "shop/payment", "grafana/east", "loki", `{app="payment-`+string(rune('0'+i))+`"}`, c, "confirmed", 0)
	}
	route(t, db, "shop/payment", "grafana/east", "loki", `{app="p-high"}`, 0.99, "proposed", 0)
	d = one(Find("payment", []string{"loki"}, false, true, opts(db, reg, lim, "")), "loki")
	if d.Decision != "several" || !reflect.DeepEqual(locs(d), []string{`{app="payment-1"}`, `{app="payment-2"}`, `{app="payment-0"}`}) ||
		!strings.Contains(d.Note, "4 confirmed: using the top 3") || d.Targets[0].Env != "release" {
		t.Fatalf("several: %+v", d)
	}
	// stale confirmed ranks after a fresh one
	route(t, db, "shop/orders", "gitlab/main", "project", "northwind/a", 1.0, "confirmed", 1)
	route(t, db, "shop/orders", "gitlab/main", "project", "northwind/b", 0.8, "confirmed", 0)
	d = one(Find("shop/orders", []string{"gitlab"}, false, true, opts(db, reg, lim, "")), "gitlab")
	if !reflect.DeepEqual(locs(d), []string{"northwind/b", "northwind/a"}) {
		t.Fatalf("stale: %v", locs(d))
	}
}

func TestProposedThresholdTiesRejectedDisabled(t *testing.T) {
	db, _ := mkdb(t)
	route(t, db, "shop/payment", "grafana/east", "loki", `{app="b"}`, 0.75, "proposed", 0)
	route(t, db, "shop/payment", "grafana/east", "loki", `{app="a"}`, 0.75, "proposed", 0)
	route(t, db, "shop/payment", "grafana/east", "loki", `{app="low"}`, 0.55, "proposed", 0)
	route(t, db, "shop/payment", "grafana/east", "loki", `{app="no"}`, 0.95, "rejected", 0)
	route(t, db, "shop/payment", "grafana/west", "loki", `{app="off"}`, 0.9, "confirmed", 0) // instance not enabled
	lim := DefaultLimits()
	lim.Top = 1
	d := one(Find("payment", []string{"loki"}, false, true, opts(db, reg, lim, "")), "loki")
	if d.Decision != "proposed" || !reflect.DeepEqual(locs(d), []string{`{app="a"}`}) || !strings.Contains(d.Note, "tie at 0.75") ||
		!reflect.DeepEqual(d.Skipped, []string{"grafana/west (not enabled for the agent)"}) {
		t.Fatalf("proposed: %+v", d)
	}
	lim.MinConf = 0.8
	d = one(Find("payment", []string{"loki"}, false, false, opts(db, reg, lim, "")), "loki")
	if d.Decision != "none" || !strings.Contains(d.Note, "3 below") || len(d.Targets) != 0 {
		t.Fatalf("threshold: %+v", d)
	}
	// the data_sources table, when recorded, is what says an instance is enabled
	db.Exec(`CREATE TABLE data_sources (key TEXT PRIMARY KEY, type TEXT, name TEXT, env TEXT, status TEXT, enabled INTEGER, purpose TEXT)`)
	db.Exec(`INSERT INTO data_sources VALUES ('grafana/west','grafana','west','develop','readonly',1,'agent')`)
	d = one(Find("payment", []string{"loki"}, false, false, opts(db, reg, DefaultLimits(), "")), "loki")
	if d.Decision != "use" || locs(d)[0] != `{app="off"}` {
		t.Fatalf("data_sources: %+v", d)
	}
}

func TestNoFanoutWithoutATypeAndRender(t *testing.T) {
	db, _ := mkdb(t)
	called := false
	o := opts(db, reg, DefaultLimits(), "")
	o.Probe = func(string, *sources.Entry, []string, int, time.Duration) ([]discover.Item, int, error) {
		called = true
		return nil, 1, nil
	}
	out := Find("scan the payment repo", nil, false, true, o)
	for _, d := range out.Services[0].Types {
		if d.Fanout != nil {
			t.Fatal("fan-out without a type")
		}
	}
	if called || len(out.Services[0].Types) != len(Order) {
		t.Fatalf("types: %v", out.Types)
	}
	var b bytes.Buffer
	Render(out, &b)
	if !strings.Contains(b.String(), "## shop/payment") || strings.Contains(b.String(), "scan the") {
		t.Fatal(b.String())
	}
	if k := one(out, "k8s"); k.Decision != "none" {
		t.Fatal(k)
	}
	route(t, db, "shop/payment", "kubernetes/k1", "workload", "shop/payment", 0.8, "confirmed", 0)
	if d := one(Find("payment", []string{"k8s"}, false, true, o), "k8s"); d.Targets[0].Tool != "argus tools kube k1 get pods -n shop" {
		t.Fatal(d.Targets[0].Tool)
	}
}

func TestReadOnlyRoutingForTheAPI(t *testing.T) {
	db, _ := mkdb(t)
	o := opts(db, reg, DefaultLimits(), "")
	o.NoFanout = "fan-out runs in the agent"
	out := Find("payment", []string{"gitlab"}, false, true, o)
	d := one(out, "gitlab")
	if d.Fanout != nil || !strings.Contains(d.Note, "fan-out runs in the agent") || out.Note == "" {
		t.Fatalf("api: %+v", d)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"fanout":null`) || !strings.Contains(string(b), `"query_hash":"`+QHash("payment")+`"`) {
		t.Fatal(string(b))
	}
}

// --- fan-out against fake GitLabs ----------------------------------------------------------------------------------------

type fakeGL struct {
	mu       sync.Mutex
	projects map[string][]string // base URL -> project paths
	seen     map[string][]string // base URL -> PRIVATE-TOKEN of each call
}

func (f *fakeGL) get(base, path string, hdr map[string]string, _ int64, _ time.Duration) (int, any, http.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen[base] = append(f.seen[base], hdr["PRIVATE-TOKEN"])
	u, _ := url.Parse(path)
	out := []any{}
	if u.Path == "/api/v4/projects" {
		for i, p := range f.projects[base] {
			if strings.Contains(p, u.Query().Get("search")) {
				out = append(out, map[string]any{"id": float64(i + 7), "path_with_namespace": p})
			}
		}
	}
	return 200, out, http.Header{}, nil
}

type fan struct {
	db    *sql.DB
	gl    *fakeGL
	reg   map[string]*sources.Entry
	o     Options
	audit string
}

func newFan(t *testing.T) *fan {
	db, _ := mkdb(t)
	dir := t.TempDir()
	f := &fan{db: db, gl: &fakeGL{projects: map[string][]string{}, seen: map[string][]string{}}, reg: map[string]*sources.Entry{},
		audit: filepath.Join(dir, "find.log")}
	os.MkdirAll(filepath.Join(dir, "src", "gitlab"), 0o750)
	for _, x := range []struct{ name, env, project string }{{"alpha", "develop", ""}, {"beta", "pre-release", "northwind/shop/payment"},
		{"gamma", "release", "northwind/shop/payment"}, {"delta", "develop", ""}} {
		u := "https://gl-" + x.name + ".northwind.example"
		if x.project != "" {
			f.gl.projects[u] = []string{x.project}
		}
		os.WriteFile(filepath.Join(dir, "src", "gitlab", x.name+".cred"), []byte(cred+"\n"), 0o640)
		f.reg["gitlab/"+x.name] = &sources.Entry{Type: "gitlab", Name: x.name, Env: x.env, Purpose: "agent", Status: "readonly",
			Enabled: true, URL: u}
	}
	f.reg["gitlab/off"] = &sources.Entry{Type: "gitlab", Name: "off", Env: "develop", Purpose: "agent", Status: "failed",
		URL: "https://gl-off.northwind.example"}
	do := &discover.Options{SourcesDir: filepath.Join(dir, "src"), Get: f.gl.get}
	f.o = opts(db, f.reg, DefaultLimits(), f.audit)
	f.o.Probe, f.o.Learn = do.Probe, true
	return f
}

func (f *fan) find(text string, lim Limits, learn bool) Result {
	o := f.o
	o.Limits, o.Learn = lim, learn
	return Find(text, []string{"gitlab"}, false, true, o)
}

func TestFanoutCappedHonestReleaseExcludedAndLearned(t *testing.T) {
	f := newFan(t)
	f.o.RunID = "run-42"
	out := Find("scan the payment repo", []string{"gitlab"}, false, true, f.o)
	d := one(out, "gitlab")
	r := d.Fanout
	if d.Decision != "none" || r == nil || r.Instances != 4 {
		t.Fatalf("fan-out: %+v", d)
	}
	if !reflect.DeepEqual(r.Searched, []string{"gitlab/alpha", "gitlab/beta", "gitlab/delta"}) ||
		!reflect.DeepEqual(r.SkippedRelease, []string{"gitlab/gamma"}) {
		t.Fatalf("searched: %+v", r)
	}
	if len(f.gl.seen["https://gl-gamma.northwind.example"]) != 0 || len(f.gl.seen["https://gl-off.northwind.example"]) != 0 {
		t.Fatal("a release or disabled instance was called")
	}
	if f.gl.seen["https://gl-beta.northwind.example"][0] != cred {
		t.Fatal("the credential did not go in the header")
	}
	if len(r.Hits) != 1 || r.Hits[0].Source != "gitlab/beta" || out.Learned != 1 {
		t.Fatalf("hits: %+v learned %d", r.Hits, out.Learned)
	}
	var origin, status, ev string
	var conf float64
	f.db.QueryRow("SELECT origin, status, confidence, evidence FROM source_routes").Scan(&origin, &status, &conf, &ev)
	if origin != "learned" || status != "proposed" || conf > 0.7 || !strings.Contains(ev, "run run-42") {
		t.Fatalf("learned row: %s %s %v %s", origin, status, conf, ev)
	}
	var audited int
	f.db.QueryRow("SELECT COUNT(*) FROM source_route_audit WHERE action='learned' AND by_user='sources-find'").Scan(&audited)
	if audited != 1 {
		t.Fatal("learned route not audited")
	}
	var b bytes.Buffer
	Render(out, &b)
	if !strings.Contains(b.String(), "searched 3 of 4 instances") || !strings.Contains(b.String(), "1 release skipped (gitlab/gamma)") ||
		!strings.Contains(b.String(), "1 learned route(s)") {
		t.Fatal(b.String())
	}
	// the next find uses the learned (proposed) route instead of fanning out again
	d2 := one(f.find("payment", DefaultLimits(), true), "gitlab")
	if d2.Decision != "proposed" || d2.Fanout != nil {
		t.Fatalf("second find: %+v", d2)
	}
	raw, _ := os.ReadFile(f.audit)
	js, _ := json.Marshal(out)
	if strings.Contains(string(raw), "scan the") || strings.Contains(string(raw)+b.String()+string(js), cred) {
		t.Fatal("the audit or the output has the question or the credential")
	}
	var rec map[string]any
	json.Unmarshal([]byte(strings.SplitN(string(raw), "\n", 2)[0]), &rec)
	fo := rec["fanout"].(map[string]any)["shop/payment:gitlab"].(map[string]any)
	if fo["searched"].(float64) != 3 || rec["q"] != QHash("scan the payment repo") || rec["run"] != "run-42" {
		t.Fatalf("audit: %v", rec)
	}
}

func TestReleaseQuestionIncludesVerifiedRelease(t *testing.T) {
	f := newFan(t)
	r := one(f.find("payment repo on production", DefaultLimits(), false), "gitlab").Fanout
	if len(r.Searched) != 4 || len(r.SkippedRelease) != 0 || len(r.Hits) != 2 {
		t.Fatalf("release: %+v", r)
	}
	var n int
	f.db.QueryRow("SELECT COUNT(*) FROM source_routes").Scan(&n)
	if n != 0 {
		t.Fatal("learned without learning")
	}
	// a release instance root did not verify read-only is never scanned, even for a release question
	f.reg["gitlab/gamma"].Status = "ok"
	f.reg["gitlab/gamma"].Enabled = true
	r = one(f.find("payment on production", DefaultLimits(), false), "gitlab").Fanout
	if !reflect.DeepEqual(r.SkippedRelease, []string{"gitlab/gamma"}) {
		t.Fatalf("unverified release: %+v", r)
	}
}

func TestMaxInstancesAndCalls(t *testing.T) {
	f := newFan(t)
	lim := DefaultLimits()
	lim.MaxInstances = 1
	r := one(f.find("payment", lim, false), "gitlab").Fanout
	if !reflect.DeepEqual(r.Searched, []string{"gitlab/alpha"}) || !reflect.DeepEqual(r.NotSearched, []string{"gitlab/beta", "gitlab/delta"}) ||
		!reflect.DeepEqual(r.LimitsHit, []string{"max instances 1"}) {
		t.Fatalf("max instances: %+v", r)
	}
	lim = DefaultLimits()
	lim.MaxCalls = 2
	out := f.find("payment", lim, false)
	r = one(out, "gitlab").Fanout
	if len(r.Searched) != 2 || !reflect.DeepEqual(r.LimitsHit, []string{"max calls 2"}) {
		t.Fatalf("max calls: %+v", r)
	}
	var b bytes.Buffer
	Render(out, &b)
	if !strings.Contains(b.String(), "searched 2 of 4 instances") || !strings.Contains(b.String(), "1 not searched (gitlab/delta)") {
		t.Fatal(b.String())
	}
	// the clock: once the time is used up nothing more is called, and the report says so
	lim = DefaultLimits()
	o := f.o
	o.Limits, o.Learn = lim, false
	start, ticks := time.Unix(1793318400, 0), 0
	o.Clock = func() time.Time { ticks++; return start.Add(time.Duration(ticks*30) * time.Second) }
	r = one(Find("payment", []string{"gitlab"}, false, true, o), "gitlab").Fanout
	if len(r.Searched)+len(r.Partial) >= 3 || len(r.NotSearched) == 0 || !strings.Contains(strings.Join(r.LimitsHit, ";"), "max seconds 60") {
		t.Fatalf("max seconds: %+v", r)
	}
	// output cap
	lim.MaxOutput = 1000
	out.Limits = lim
	out.How["shop/payment"] = strings.Repeat("x", 2000)
	b.Reset()
	Render(out, &b)
	if b.Len() > 1100 || !strings.Contains(b.String(), "output capped at 1000 bytes") {
		t.Fatal(b.Len())
	}
}

func TestLearningNeverTouchesAnExistingRow(t *testing.T) {
	db, _ := mkdb(t)
	route(t, db, "shop/payment", "gitlab/beta", "project", "northwind/shop/payment", 1.0, "rejected", 0)
	n, err := Learn(db, "shop/payment", []Hit{{Source: "gitlab/beta", Kind: "project", Locator: "northwind/shop/payment", Confidence: 1, Rule: "gitlab-path", Why: "x"},
		{Source: "gitlab/delta", Kind: "project", Locator: "northwind/x/payment", Confidence: 0.9, Rule: "gitlab-name", Why: "y"}}, "r1", 5)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	rows := map[string]string{}
	q, _ := db.Query("SELECT source, status FROM source_routes")
	for q.Next() {
		var a, b string
		q.Scan(&a, &b)
		rows[a] = b
	}
	q.Close()
	if !reflect.DeepEqual(rows, map[string]string{"gitlab/beta": "rejected", "gitlab/delta": "proposed"}) {
		t.Fatal(rows)
	}
	// a catalogue build leaves a learned row alone
	db.Exec("CREATE TABLE source_inventory (source TEXT, kind TEXT, key TEXT, attrs TEXT, first_seen INTEGER, seen_at INTEGER)")
	db.Exec("INSERT INTO source_inventory VALUES ('gitlab/delta','project','northwind/x/payment','{}',1,1)")
	if _, err := catalogue.Build(db, svcs(), 9); err != nil {
		t.Fatal(err)
	}
	var origin string
	var conf float64
	db.QueryRow("SELECT origin, confidence FROM source_routes WHERE source='gitlab/delta'").Scan(&origin, &conf)
	if origin != "learned" || conf != 0.7 {
		t.Fatal(origin, conf)
	}
}

func TestAmbiguousOrUnknownNeverFansOut(t *testing.T) {
	f := newFan(t)
	out := Find("payment and storefront", []string{"gitlab"}, false, true, f.o)
	for _, s := range out.Services {
		if s.Types["gitlab"].Fanout != nil {
			t.Fatal("fan-out for an ambiguous question")
		}
	}
	if out := Find("why is it slow", []string{"gitlab"}, false, true, f.o); len(out.Services) != 0 {
		t.Fatal(out.Services)
	}
	if len(f.gl.seen) != 0 {
		t.Fatal(f.gl.seen)
	}
}
