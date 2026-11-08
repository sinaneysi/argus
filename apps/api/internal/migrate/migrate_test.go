package migrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/board"
	"github.com/sinaneysi/argus/internal/config"
	"github.com/sinaneysi/argus/internal/sources/catalogue"
	"github.com/sinaneysi/argus/internal/sources/discover"
)

var ctx = context.Background()

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	c, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// shape is every table with its columns and every index, sorted.
func shape(t *testing.T, c *sql.DB) []string {
	t.Helper()
	rows, err := c.Query("SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var objs [][2]string
	for rows.Next() {
		var k, n string
		rows.Scan(&k, &n)
		objs = append(objs, [2]string{k, n})
	}
	rows.Close()
	var out []string
	for _, o := range objs {
		if o[0] != "table" {
			out = append(out, o[0]+" "+o[1])
			continue
		}
		cr, err := c.Query("SELECT name FROM pragma_table_info(?)", o[1])
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for cr.Next() {
			var n string
			cr.Scan(&n)
			cols = append(cols, n)
		}
		cr.Close()
		sort.Strings(cols)
		out = append(out, "table "+o[1]+"("+strings.Join(cols, ",")+")")
	}
	return out
}

func TestFreshIdempotent(t *testing.T) {
	dir := t.TempDir()
	for _, db := range DBs {
		p := filepath.Join(dir, db, db+".db")
		r, err := ApplyFile(ctx, p, db)
		if err != nil {
			t.Fatalf("%s: %v", db, err)
		}
		if r.From != 0 || r.To != Latest(db) || len(r.Applied) != Latest(db) || len(r.Adopted) != 0 {
			t.Fatalf("%s: first apply %+v", db, r)
		}
		before := shape(t, open(t, p))
		r, err = ApplyFile(ctx, p, db)
		if err != nil || len(r.Applied) != 0 || r.From != Latest(db) {
			t.Fatalf("%s: second apply %+v %v", db, r, err)
		}
		if after := shape(t, open(t, p)); strings.Join(before, "\n") != strings.Join(after, "\n") {
			t.Fatalf("%s: second apply changed the schema", db)
		}
		cur, pend, err := StatusFile(ctx, p, db)
		if err != nil || cur != Latest(db) || len(pend) != 0 {
			t.Fatalf("%s: status %d %d %v", db, cur, len(pend), err)
		}
	}
}

func TestDowngradeRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := ApplyFile(ctx, p, Ops); err != nil {
		t.Fatal(err)
	}
	open(t, p).Exec("INSERT INTO " + Table + " VALUES (999, 'future', 'x', 0)")
	if _, err := ApplyFile(ctx, p, Ops); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("want ErrDowngrade, got %v", err)
	}
}

func TestChangedMigrationRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := ApplyFile(ctx, p, Ops); err != nil {
		t.Fatal(err)
	}
	open(t, p).Exec("UPDATE " + Table + " SET sha256='0000' WHERE version=1")
	if _, err := ApplyFile(ctx, p, Ops); err == nil || !strings.Contains(err.Error(), "was changed") {
		t.Fatalf("want a changed-migration error, got %v", err)
	}
}

// An earlier installation's database: some tables exist with fewer columns, rows must survive.
func TestAdoptExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "board.db")
	c := open(t, p)
	for _, q := range []string{
		"CREATE TABLE tasks (id INTEGER PRIMARY KEY, jira_key TEXT UNIQUE, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'todo')",
		"INSERT INTO tasks (id, jira_key, title) VALUES (7, 'OPS-1', 'kept')",
		"CREATE INDEX tasks_status ON tasks(status)",
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	r, err := ApplyFile(ctx, p, Board)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Adopted, " ")
	for _, w := range []string{"tasks", "tasks.tracker", "tasks.gl_iid", "tasks_status"} {
		if !strings.Contains(" "+joined+" ", " "+w+" ") {
			t.Errorf("adopted %q missing in %v", w, r.Adopted)
		}
	}
	var title, tracker string
	if err := open(t, p).QueryRow("SELECT title, tracker FROM tasks WHERE id=7").Scan(&title, &tracker); err != nil || title != "kept" || tracker != "jira" {
		t.Fatalf("row after adoption: %q %q %v", title, tracker, err)
	}
	// the adopted database has every table of a fresh one
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	if _, err := ApplyFile(ctx, fresh, Board); err != nil {
		t.Fatal(err)
	}
	if a, b := shape(t, open(t, p)), shape(t, open(t, fresh)); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("adopted schema differs from a fresh one:\n%v\n%v", a, b)
	}
}

func TestReadOnlyNotTouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "ops.db")
	if _, err := ApplyFile(ctx, p, Ops); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o444)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if _, err := ApplyFile(ctx, p, Ops); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
}

// The tables packages still create on first use must already be in the migrations (one schema, ADR-0021 D2).
func TestPackageSchemasCovered(t *testing.T) {
	cases := map[string][]string{
		Ops:   {catalogue.Schema, discover.Schema},
		Board: {board.Schema},
		Auth:  {auth.SCHEMA, auth.MFA_SCHEMA},
	}
	for db, scripts := range cases {
		p := filepath.Join(t.TempDir(), db+".db")
		if _, err := ApplyFile(ctx, p, db); err != nil {
			t.Fatal(err)
		}
		c := open(t, p)
		before := shape(t, c)
		for _, s := range scripts {
			for _, st := range Split(s) {
				if _, err := c.Exec(st); err != nil {
					t.Fatalf("%s: %v\n%s", db, err, st)
				}
			}
		}
		if after := shape(t, c); strings.Join(before, "\n") != strings.Join(after, "\n") {
			t.Errorf("%s: a package schema adds objects the migrations lack:\n%s", db, diff(before, after))
		}
	}
}

func diff(a, b []string) string {
	in := map[string]bool{}
	for _, x := range a {
		in[x] = true
	}
	var out []string
	for _, x := range b {
		if !in[x] {
			out = append(out, x)
		}
	}
	return strings.Join(out, "\n")
}

func TestSplit(t *testing.T) {
	got := Split("-- c; x\nCREATE TABLE a (v TEXT DEFAULT ';');\n\nCREATE INDEX i ON a(v);")
	if len(got) != 2 || got[0] != "CREATE TABLE a (v TEXT DEFAULT ';')" || got[1] != "CREATE INDEX i ON a(v)" {
		t.Fatalf("%q", got)
	}
}

func TestEnsureDirsVolumeOnly(t *testing.T) {
	dir := t.TempDir()
	env := []string{"COMPANY_NAME=A", "ARGUS_PUBLIC_URL=https://a.example", "ARGUS_STATE_DIR=" + dir, "ARGUS_LAYOUT=volume"}
	c, err := config.Load(config.Options{File: filepath.Join(dir, "none.env"), Environ: env})
	if err == nil || c == nil { // the explicit file is missing: Load reports it, the values are still filled
		t.Fatalf("want a missing-file error, got %v", err)
	}
	if err := EnsureDirs(c); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"settings/in", "settings/out", "secrets/in", "etc/sources", "auth", "board", "keys", "reports/rca"} {
		if fi, err := os.Stat(filepath.Join(dir, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}
