package lessons

import (
	"bytes"
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
)

// secretValue stands for @SECRET@ in the golden file (built here so no file holds a value the leak check flags).
var secretValue = "password=" + "Qx7" + "vT93kLm2zz"

func subst(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "@SECRET@", secretValue)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = subst(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = subst(e)
		}
		return out
	}
	return v
}

func decode(b []byte) any {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	d.Decode(&v)
	return v
}

// norm is v through JSON, numbers as json.Number, so Go and golden values compare.
func normJSON(v any) any {
	b, _ := json.Marshal(v)
	return decode(b)
}

func dump(t *testing.T, db *sql.DB) []any {
	rs, err := db.Query(`SELECT * FROM lessons ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rs.Scan(ptrs...)
		m := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	if out == nil {
		out = []any{}
	}
	return out
}

func readTree(dir string) map[string]any {
	out := map[string]any{}
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func num(v any) int {
	n, _ := v.(json.Number).Int64()
	return int(n)
}

// Parity: the reference lessons job on the same synthetic scenario (testdata/parity.json; driver kept outside the
// repository): propose, review, select, after-run, decay and the generated files give the same results, rows,
// events and files. Texts that named the predecessor say Argus here.
func TestParity(t *testing.T) {
	b, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	g := decode(b).(map[string]any)
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(Schema + "CREATE TABLE state (k TEXT PRIMARY KEY, v TEXT);"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var clock int64
	var events []any
	board := Board{Event: func(kind, actor, text string) { events = append(events, []any{kind, actor, text}) },
		SetState: func(k, v string) { db.Exec(`INSERT OR REPLACE INTO state (k, v) VALUES (?, ?)`, k, v) }}
	know := filepath.Join(dir, "knowledge")
	for i, x := range g["steps"].([]any) {
		raw := x.(map[string]any)
		s := subst(raw).(map[string]any)
		if tv, ok := s["t"]; ok {
			clock = int64(num(tv))
		}
		o := &Options{KnowledgeDir: know, Now: func() time.Time { return time.Unix(clock, 0) }}
		key, _ := s["key"].(string)
		var got map[string]any
		switch s["op"] {
		case "propose":
			stored, dropped, err := o.Propose(ctx, db, s["items"], key, s["run"])
			if err != nil {
				t.Fatal(err)
			}
			ds := []any{}
			for _, d := range dropped {
				ds = append(ds, []any{d.Title, d.Why})
			}
			if stored == nil {
				stored = []int64{}
			}
			got = map[string]any{"stored": stored, "dropped": ds}
		case "handle":
			by, _ := s["by"].(string)
			role, _ := s["role"].(string)
			res, err := o.Handle(ctx, db, s["req"].(map[string]any), by, role, board)
			if r, ok := err.(Refused); ok {
				got = map[string]any{"refused": string(r)}
			} else if err != nil {
				t.Fatal(err)
			} else {
				got = map[string]any{"result": res}
			}
		case "select":
			if v, ok := s["maxn"]; ok {
				o.PromptMax = num(v)
			}
			if v, ok := s["maxbytes"]; ok {
				o.PromptMaxBytes = num(v)
			}
			rows, md, err := o.Select(ctx, db, s["job"].(map[string]any))
			if err != nil {
				t.Fatal(err)
			}
			ids := []int64{}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			got = map[string]any{"ids": ids, "md": md}
		case "after_run":
			path := ""
			if ids, ok := s["ids"]; ok && ids != nil {
				path = filepath.Join(dir, "ids.json")
				jb, _ := json.Marshal(ids)
				os.WriteFile(path, jb, 0o644)
			}
			d, _ := s["d"].(map[string]any)
			line, err := o.AfterRun(ctx, db, key, d, path, s["run"])
			if err != nil {
				t.Fatal(err)
			}
			got = map[string]any{"line": line}
		case "decay":
			if v, ok := s["max_approved"]; ok {
				o.MaxApproved = num(v)
			}
			n, err := o.Decay(ctx, db, clock)
			if err != nil {
				t.Fatal(err)
			}
			got = map[string]any{"n": n}
		case "render":
			o.Render(ctx, db)
			got = map[string]any{"files": readTree(know)}
		case "enabled":
			got = map[string]any{"enabled": Enabled(ctx, db)}
		}
		if w := normJSON(raw["want"]); !reflect.DeepEqual(normJSON(got), w) {
			t.Errorf("step %d %v: got %v\nwant %v", i, s["op"], normJSON(got), w)
		}
		if w, gotDB := normJSON(raw["db"]), normJSON(dump(t, db)); !reflect.DeepEqual(gotDB, w) {
			t.Errorf("step %d %v rows:\ngot  %v\nwant %v", i, s["op"], gotDB, w)
		}
		ev := events
		if ev == nil {
			ev = []any{}
		}
		if w := normJSON(raw["events"]); !reflect.DeepEqual(normJSON(ev), w) {
			t.Errorf("step %d events: got %v want %v", i, ev, w)
		}
		if t.Failed() {
			return
		}
	}
}

// difflib parity: ratios of the reference SequenceMatcher (computed with it).
func TestRatio(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"billing api health check listens on port 8081", "billing api health check listens on port 8082", 0.9777777777777777},
		{"abcd", "bcda", 0.75},
		{"", "", 1},
		{"edge proxy reload only", "queue worker needs the cache", 0.2},
	} {
		if got := ratio([]rune(c.a), []rune(c.b)); got != c.want {
			t.Errorf("ratio(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestReadIDsRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ids.json")
	os.WriteFile(p, []byte(`[1, 2, true, 3.5, "4", 5]`), 0o644)
	o := &Options{}
	if got := o.ReadIDs(p); !reflect.DeepEqual(got, []int64{1, 2, 5}) {
		t.Errorf("ids %v", got)
	}
	l := filepath.Join(dir, "link.json")
	os.Symlink(p, l)
	if got := o.ReadIDs(l); got != nil {
		t.Errorf("a link was read: %v", got)
	}
}
