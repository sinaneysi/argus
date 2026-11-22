package problems

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/migrate"
)

// golden: testdata/problems-parity.json, written by the reference job on the same synthetic scenario (the
// automatic actor renamed to argus). Each step writes request files, runs a tick or a suggester pass at a fixed time
// and compares the three tables, the incidents, the state file and the files left.
type golden struct {
	Seed  []string                   `json:"seed"`
	Files map[string]json.RawMessage `json:"files"`
	Steps []struct {
		At       int64             `json:"at"`
		Run      string            `json:"run"`
		Env      map[string]string `json:"env"`
		Requests []struct {
			File string          `json:"file"`
			Body json.RawMessage `json:"body"`
		} `json:"requests"`
		Raw  [][2]string    `json:"raw"`
		Want map[string]any `json:"want"`
	} `json:"steps"`
}

func dumpDB(t *testing.T, db *sql.DB) map[string]any {
	out := map[string]any{}
	for _, tc := range [][2]string{{"problems", "*"}, {"problem_links", "*"}, {"problem_events", "*"},
		{"incidents", "id, status, resolved_at, resolved_by, resolve_note"}, {"incident_events", "incident_id, at, kind, actor, text"}} {
		rs, err := rows(db, "SELECT "+tc[1]+" FROM "+tc[0]+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		l := []any{}
		for _, r := range rs {
			l = append(l, map[string]any(r))
		}
		out[tc[0]] = l
	}
	return out
}

func roundTrip(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}

func TestParity(t *testing.T) {
	b, err := os.ReadFile("testdata/problems-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	state, act := filepath.Join(d, "state"), filepath.Join(d, "state", "settings-in", "problem-actions")
	os.MkdirAll(act, 0o755)
	dbp := filepath.Join(state, "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), dbp, migrate.Ops); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+dbp+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, s := range g.Seed {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v\n%s", err, s)
		}
	}
	for n, v := range g.Files {
		os.WriteFile(filepath.Join(state, n), v, 0o644)
	}
	var now int64
	env := map[string]string{}
	o := &Options{StateDir: state, ActionsDir: act, AdminFile: filepath.Join(state, "admin.json"),
		Env: func(k string) string { return env[k] }, HostPrefixes: []string{"app-", "db-"}, SkipHosts: []string{"chat.corp.test"},
		Incidents: &incidents.Options{Actor: "argus"}, Now: func() time.Time { return time.Unix(now, 0) }}
	for i, st := range g.Steps {
		now, env = st.At, st.Env
		for _, rq := range st.Requests {
			os.WriteFile(filepath.Join(act, rq.File), rq.Body, 0o644)
		}
		for _, r := range st.Raw {
			os.WriteFile(filepath.Join(act, r[0]), []byte(r[1]), 0o644)
		}
		if st.Run == "tick" {
			if _, err := o.Tick(db); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
		} else if _, _, err := o.Suggest(db); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		var sf map[string]any
		sb, _ := os.ReadFile(filepath.Join(state, "problems.json"))
		if err := json.Unmarshal(sb, &sf); err != nil {
			t.Fatalf("step %d: state file: %v", i, err)
		}
		delete(sf, "last_tick")
		delete(sf, "last_suggest")
		if m, ok := sf["last_suggest_result"].(map[string]any); ok {
			delete(m, "ms")
		}
		// the text of a request that is not JSON at all is the parser's own message: only "refused" is compared
		want := roundTrip(st.Want).(map[string]any)
		if ws, ok := want["state"].(map[string]any); ok {
			for j, x := range anyList(ws["last_requests"]) {
				if x.(map[string]any)["text"] == "*" {
					if gl := anyList(sf["last_requests"]); j < len(gl) {
						gl[j].(map[string]any)["text"] = "*"
					}
				}
			}
		}
		ents, _ := os.ReadDir(act)
		left := []any{}
		for _, e := range ents {
			left = append(left, e.Name())
		}
		got := roundTrip(map[string]any{"db": dumpDB(t, db), "state": sf, "left": left}).(map[string]any)
		for _, k := range []string{"db", "state", "left"} {
			if !reflect.DeepEqual(got[k], want[k]) {
				t.Errorf("step %d (%s): %s differs:\n%s", i, st.Run, k, firstDiff(k, got[k], want[k]))
			}
		}
		if t.Failed() {
			return
		}
	}
}

func anyList(v any) []any { l, _ := v.([]any); return l }

// firstDiff names the first differing path.
func firstDiff(path string, a, b any) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			break
		}
		for k := range x {
			if !reflect.DeepEqual(x[k], y[k]) {
				return firstDiff(path+"."+k, x[k], y[k])
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				return fmt.Sprintf("%s.%s: missing, want %v", path, k, y[k])
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			break
		}
		for i := 0; i < len(x) && i < len(y); i++ {
			if !reflect.DeepEqual(x[i], y[i]) {
				return firstDiff(fmt.Sprintf("%s[%d]", path, i), x[i], y[i])
			}
		}
		if len(x) != len(y) {
			return fmt.Sprintf("%s: %d items, want %d", path, len(x), len(y))
		}
	}
	return fmt.Sprintf("%s: got %v, want %v", path, a, b)
}

func TestTextFeatures(t *testing.T) {
	o := &Options{HostPrefixes: []string{"app-"}}
	o.defaults()
	f := o.textFeatures("1.1.2.3.4:80 10.0.0.5:8080 app-web-01:9000 see www.shop.example/a and 1.2.3.45. and EXAMPLE.COM", nil)
	if got := strings.Join(f["upstreams"].sorted(), ","); got != "10.0.0.5:8080,app-web-01:9000" {
		t.Errorf("upstreams %s", got)
	}
	if got := strings.Join(f["hosts"].sorted(), ","); got != "10.0.0.5,app-web-01" {
		t.Errorf("hosts %s", got)
	}
	if got := strings.Join(f["domains"].sorted(), ","); got != "www.shop.example" {
		t.Errorf("domains %s", got)
	}
}

func TestNamedAsHost(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{{"on app-web-01, ok", true}, {"on app-web-012", false}, {"x.app-web-01", false}, {"(app-web-01)", true}} {
		if namedAsHost(c.text, "app-web-01") != c.want {
			t.Errorf("%q", c.text)
		}
	}
}

func TestConfigClamp(t *testing.T) {
	env := map[string]string{"PROBLEMS_FANOUT_MAX": "1", "PROBLEMS_SUGGEST_MIN_CONFIDENCE": "0.5", "PROBLEMS_SOURCES": "incident,bogus", "FEATURE_PROBLEMS": "yes"}
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "admin.json"), []byte(`{"PROBLEMS_AUTO_ACCEPT": true, "PROBLEMS_FANOUT_MAX": 50, "PROBLEMS_AUTO_ACCEPT_CLASSES": ["production"]}`), 0o644)
	o := &Options{Env: func(k string) string { return env[k] }, AdminFile: filepath.Join(d, "admin.json")}
	c := o.LoadConfig()
	if c.FanoutMax != 20 || c.MinConfidence != 0.5 || len(c.Sources) != 7 || !c.Enabled || !c.AutoAccept || c.AutoClasses[0] != "production" {
		t.Errorf("%+v", c)
	}
}
