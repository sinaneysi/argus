package today

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/py"
	_ "modernc.org/sqlite"
)

func openDB(t *testing.T, p, kind string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if kind != "" {
		if _, err := migrate.Apply(context.Background(), db, kind, func() int64 { return 1 }); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seed(t *testing.T, db *sql.DB, tables *py.OMap) {
	t.Helper()
	for _, tb := range tables.Keys() {
		for _, rv := range tables.List(tb) {
			r := rv.(*py.OMap)
			var vals []any
			for _, k := range r.Keys() {
				vals = append(vals, r.Get(k))
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tb, strings.Join(r.Keys(), ", "), strings.TrimSuffix(strings.Repeat("?,", len(vals)), ","))
			if _, err := db.Exec(q, vals...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
}

// norm makes a value comparable: numbers as float64, maps as plain maps.
func norm(v any) any {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case *py.OMap:
		m := map[string]any{}
		for _, k := range x.Keys() {
			m[k] = norm(x.Get(k))
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = norm(e)
		}
		return out
	}
	return v
}

func same(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || math.Abs(x-y) <= 1e-9*math.Max(math.Abs(x), math.Abs(y)))
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if !same(v, y[k]) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !same(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func diffRow(a, b map[string]any) string {
	var ks []string
	for k := range a {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var out []string
	for _, k := range ks {
		if !same(a[k], b[k]) {
			out = append(out, fmt.Sprintf("%s: got %#v want %#v", k, a[k], b[k]))
		}
	}
	return strings.Join(out, "; ")
}

var snapTables = []struct{ name, key string }{{"today_plans", "id"}, {"today_items", "id"}, {"today_events", "id"}, {"today_learn", "kind"},
	{"today_settings", "k"}}

// TestParity replays the reference planner's scenario (testdata/parity.json: 16 steps over five days; plan, refresh,
// carry-over, requests incl. rejected ones, settings, re-plan, the feature switch, budget and status overrides).
// Same rows in today_plans / today_items / today_events / today_learn / today_settings, same status file, same
// output and the same request files left after every step.
func TestParity(t *testing.T) {
	b, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	gv, err := py.ParseJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	g := gv.(*py.OMap)
	loc, err := time.LoadLocation(g.S("tz"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, d := range []string{"board", "settings-in/today-actions", "settings-out"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	ops := openDB(t, filepath.Join(dir, "ops.db"), "ops")
	brw := openDB(t, filepath.Join(dir, "board", "board.db"), "board")
	seed(t, ops, g.Map("seed_ops"))
	seed(t, brw, g.Map("seed_board"))
	for _, p := range g.Map("files").Keys() {
		os.WriteFile(filepath.Join(dir, p), []byte(py.Dumps(g.Map("files").Get(p), -1)), 0o644)
	}
	dec := filepath.Join(dir, "decisions.json")
	os.WriteFile(dec, []byte(py.Dumps(g.Get("decisions"), -1)), 0o644)
	bro, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "board", "board.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer bro.Close()
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	var clock int64
	want := map[string]map[string]map[string]any{}
	for _, tb := range snapTables {
		want[tb.name] = map[string]map[string]any{}
	}
	for i, sv := range g.List("steps") {
		s := sv.(*py.OMap)
		clock = i64(s.Get("at"))
		env := s.Map("env")
		var out, logb bytes.Buffer
		o := &Options{DB: ops, Board: bro, DecisionsFile: dec, StateDir: dir, SelfcheckFile: filepath.Join(dir, "selfcheck.json"),
			CredFile: filepath.Join(dir, "settings-out", "credentials.json"), ActionsDir: filepath.Join(dir, "settings-in", "today-actions"),
			StatusFile: filepath.Join(dir, "today-status.json"), Enabled: env.S("FEATURE_TODAY") == "1", Operator: env.S("TODAY_OPERATOR"),
			Statuses: env.S("TODAY_JIRA_STATUSES"), PlanAt: env.S("TODAY_PLAN_AT"), FocusMin: env.S("TODAY_FOCUS_MIN"),
			MaxItems: env.S("TODAY_MAX_ITEMS"), MaxRisky: env.S("TODAY_MAX_RISKY"), Loc: loc,
			Now: func() time.Time { return time.Unix(clock, 0) }, Out: &out, Log: &logb}
		for _, mv := range s.List("mutate") {
			m := mv.([]any)
			db := ops
			if m[0] == "board" {
				db = brw
			}
			if _, err := db.Exec(m[1].(string), m[2].([]any)...); err != nil {
				t.Fatal(err)
			}
		}
		for _, rv := range s.List("requests") {
			r := rv.([]any)
			raw := []byte(r[1].(string))
			if r[2] == "latin-1" {
				raw = nil
				for _, c := range r[1].(string) {
					raw = append(raw, byte(c))
				}
			}
			os.WriteFile(filepath.Join(o.ActionsDir, r[0].(string)), raw, 0o644)
		}
		ctx := context.Background()
		switch op := s.S("op"); op {
		case "tick":
			err = o.Tick(ctx, cfg, false)
		case "apply":
			err = o.Apply(ctx, cfg)
		case "plan":
			err = o.Plan(ctx, cfg, truthy(s.Get("replan")))
		case "show":
			var tx string
			tx, err = o.Text(ctx)
			fmt.Fprintln(&out, tx)
		case "status":
			_, err = o.WriteStatus(ctx, cfg)
		}
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		name := fmt.Sprintf("step %d (%s at %d)", i, s.S("op"), clock)
		if got := out.String(); got != s.S("stdout") {
			t.Fatalf("%s output:\n%s\nwant:\n%s", name, got, s.S("stdout"))
		}
		if n := strings.Count(logb.String(), "rejected"); int64(n) != i64(s.Get("rejected")) {
			t.Errorf("%s: %d rejected, want %d: %s", name, n, i64(s.Get("rejected")), logb.String())
		}
		w := s.Map("want")
		for _, tb := range snapTables {
			d := w.Map(tb.name)
			for _, rv := range d.List("changed") {
				r := norm(rv).(map[string]any)
				want[tb.name][py.Str(rv.(*py.OMap).Get(tb.key))] = r
			}
			for _, k := range d.List("gone") {
				delete(want[tb.name], py.Str(k))
			}
			rows, err := query(ctx, ops, "SELECT * FROM "+tb.name)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(want[tb.name]) {
				t.Fatalf("%s: %s has %d rows, want %d", name, tb.name, len(rows), len(want[tb.name]))
			}
			for _, r := range rows {
				m := map[string]any{}
				for j, c := range r.C {
					m[c] = norm(r.V[j])
				}
				k := py.Str(r.Get(tb.key))
				if ww, ok := want[tb.name][k]; !ok || !same(m, ww) {
					t.Fatalf("%s: %s %s differs: %s\nref %v", name, tb.name, k, diffRow(m, ww), ww["ref"])
				}
			}
		}
		st := py.ReadJSON(o.StatusFile, nil)
		if !same(norm(st), norm(w.Get("status"))) {
			t.Fatalf("%s: status\n got %s\nwant %s", name, py.Dumps(st, -1), py.Dumps(w.Get("status"), -1))
		}
		ents, _ := os.ReadDir(o.ActionsDir)
		var left []any
		for _, e := range ents {
			left = append(left, e.Name())
		}
		if left == nil {
			left = []any{}
		}
		if !same(left, norm(w.Get("left"))) {
			t.Fatalf("%s: left %v, want %v", name, left, w.Get("left"))
		}
	}
}

func TestTextHelpers(t *testing.T) {
	long := strings.Repeat("word ", 40)
	if got := one(long, 20); got != "word word word…" {
		t.Errorf("one %q", got)
	}
	if got := words("a b c, d", 3); got != "a b c…" {
		t.Errorf("words %q", got)
	}
	if floorDiv(-1, 7) != -1 || floorDiv(13, 7) != 1 {
		t.Error("floorDiv")
	}
	if roundMin(12.5) != 10 || roundMin(2.5) != 2 || roundMin(0.2) != 1 {
		t.Error("roundMin", roundMin(12.5), roundMin(2.5))
	}
	if quote("u|linux|prod/x y") != "u%7Clinux%7Cprod%2Fx%20y" {
		t.Error(quote("u|linux|prod/x y"))
	}
}
