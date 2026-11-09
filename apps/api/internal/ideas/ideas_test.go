package ideas

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/migrate"
)

// testdata/ideas-parity.json: the reference job applied the same request files to the same synthetic ideas with
// the embedded default.json and build.md (ideas, idea_events, the status file without "at", the files left).
func TestParity(t *testing.T) {
	b, err := os.ReadFile("testdata/ideas-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Seed     []string             `json:"seed"`
		Requests [][2]json.RawMessage `json:"requests"`
		T0       int64                `json:"t0"`
		Want     map[string]any       `json:"want"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	act := filepath.Join(d, "idea-actions")
	os.MkdirAll(act, 0o755)
	dbp := filepath.Join(d, "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), dbp, migrate.Ops); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", dbp)
	db.SetMaxOpenConns(1)
	defer db.Close()
	for _, s := range g.Seed {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range g.Requests {
		var name string
		json.Unmarshal(r[0], &name)
		os.WriteFile(filepath.Join(act, name), r[1], 0o644)
	}
	os.WriteFile(filepath.Join(act, "junk.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(act, "20251009090011-0000000c.json"), []byte("{bad"), 0o644)
	env := map[string]string{"FEATURE_IDEAS": "1", "IDEAS_SCHEDULE": "qa=Tue+Thu,noc=off", "IDEAS_MAX_TURNS": "99",
		"IDEAS_BUDGET_USD": "0.333", "IDEAS_MAX_PER_RUN": "x"}
	loc, _ := time.LoadLocation("Asia/Tokyo")
	o := &Options{StateDir: d, ActionsDir: act, Env: func(k string) string { return env[k] }, Loc: loc,
		Now: func() time.Time { return time.Unix(g.T0, 0) }}
	if _, err := o.Apply(db); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{"db": map[string]any{}}
	for _, tb := range []string{"ideas", "idea_events"} {
		rs, err := rows(db, "SELECT * FROM "+tb+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		got["db"].(map[string]any)[tb] = rs
	}
	var st map[string]any
	sb, _ := os.ReadFile(filepath.Join(d, "ideas-status.json"))
	if err := json.Unmarshal(sb, &st); err != nil {
		t.Fatal(err)
	}
	delete(st, "at")
	got["status"] = st
	ents, _ := os.ReadDir(act)
	left := []any{}
	for _, e := range ents {
		left = append(left, e.Name())
	}
	got["left"] = left
	gb, _ := json.Marshal(got)
	var gn map[string]any
	json.Unmarshal(gb, &gn)
	wb, _ := json.Marshal(g.Want)
	var wn map[string]any
	json.Unmarshal(wb, &wn)
	for _, k := range []string{"db", "status", "left"} {
		if !reflect.DeepEqual(gn[k], wn[k]) {
			a, _ := json.MarshalIndent(gn[k], "", " ")
			w, _ := json.MarshalIndent(wn[k], "", " ")
			t.Errorf("%s differs:\n got %s\nwant %s", k, firstLines(string(a), string(w)), "")
		}
	}
}

// firstLines shows the first differing line of two texts.
func firstLines(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) && i < len(lb); i++ {
		if la[i] != lb[i] {
			return la[i] + "\nwant " + lb[i]
		}
	}
	return "(length differs)"
}

func TestClampAndSchedule(t *testing.T) {
	o := &Options{Env: func(k string) string {
		return map[string]string{"IDEAS_BUDGET_USD": "9", "IDEAS_MODEL": "Bad Model", "IDEAS_SCHEDULE": "devops=Sun+Xyz,qa=off,BAD=Mon"}[k]
	}}
	if l := o.Limits(); l.BudgetUSD != 2 || l.Model != "sonnet" || l.Turns != 25 || l.TimeoutMin != 15 {
		t.Errorf("%+v", l)
	}
	c := o.LoadConfig()
	if d := c.persona("devops").Get("days"); !reflect.DeepEqual(d, []any{"Sun"}) {
		t.Errorf("devops %v", d)
	}
	if d := c.persona("qa").Get("days"); !reflect.DeepEqual(d, []any{}) {
		t.Errorf("qa %v", d)
	}
}
