// Package migrate owns Argus's database schema (ADR-0021 D2): numbered SQL migrations per database, embedded in the
// binaries, applied by `argus migrate` and at the start of argus-api and argus worker.
//
// Rules:
//   - an applied migration is recorded in argus_schema_migrations (version, name, sha256, time) and never applied
//     again; a recorded migration whose sha256 changed is an error;
//   - a database that records a version above the newest one this binary knows is refused (ErrDowngrade);
//   - an existing database is adopted: a CREATE TABLE or CREATE INDEX for an object that already exists is skipped, and
//     the columns the statement defines but the table lacks are added (ALTER TABLE ... ADD COLUMN);
//   - a database file that cannot be written (a read-only mount) is not touched (ErrReadOnly).
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations
var files embed.FS

// The databases Argus owns.
const (
	Ops   = "ops"   // evidence database (ARGUS_DB)
	Board = "board" // decisions and tasks (ARGUS_BOARD_DB)
	Auth  = "auth"  // users and sessions (ARGUS_AUTH_DB)
)

// DBs lists every database, in apply order.
var DBs = []string{Ops, Board, Auth}

// Table is the bookkeeping table in every migrated database.
const Table = "argus_schema_migrations"

var (
	ErrDowngrade = errors.New("database is newer than this binary")
	ErrReadOnly  = errors.New("database is read-only")
)

// Migration is one numbered SQL file.
type Migration struct {
	DB      string
	Version int
	Name    string
	SQL     string
	Sum     string // sha256 of SQL, hex
}

var fileRe = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// List is the migrations of one database, by version. It panics on a malformed embedded file (a build mistake).
func List(db string) []Migration {
	ents, err := files.ReadDir("migrations/" + db)
	if err != nil {
		panic(fmt.Sprintf("migrate: unknown database %q", db))
	}
	var out []Migration
	for _, e := range ents {
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			panic("migrate: bad file name " + db + "/" + e.Name())
		}
		b, err := files.ReadFile("migrations/" + db + "/" + e.Name())
		if err != nil {
			panic(err)
		}
		v, _ := strconv.Atoi(m[1])
		sum := sha256.Sum256(b)
		out = append(out, Migration{DB: db, Version: v, Name: m[2], SQL: string(b), Sum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			panic(fmt.Sprintf("migrate: %s versions must be 1..n without gaps (found %d at %d)", db, m.Version, i+1))
		}
	}
	return out
}

// Latest is the newest version of one database.
func Latest(db string) int { return len(List(db)) }

// Q is what Apply needs: a *sql.DB, a *sql.Conn or a *sql.Tx.
type Q interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Result is what one Apply did.
type Result struct {
	DB      string
	From    int      // version before
	To      int      // version after
	Applied []string // "0002_name"
	Adopted []string // objects that existed and were kept, columns that were added ("table.column")
}

// Status is the version a database records and the migrations still pending.
func Status(ctx context.Context, q Q, db string) (current int, pending []Migration, err error) {
	rec, err := recorded(ctx, q)
	if err != nil {
		return 0, nil, err
	}
	all := List(db)
	for v := range rec {
		if v > current {
			current = v
		}
	}
	if current > len(all) {
		return current, nil, fmt.Errorf("%w: %s records version %d, this binary knows %d", ErrDowngrade, db, current, len(all))
	}
	for _, m := range all {
		r, ok := rec[m.Version]
		if !ok {
			pending = append(pending, m)
			continue
		}
		if r != m.Sum {
			return current, nil, fmt.Errorf("%s migration %04d_%s was changed after it was applied (sha256 %s, recorded %s)",
				db, m.Version, m.Name, short(m.Sum), short(r))
		}
	}
	return current, pending, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func recorded(ctx context.Context, q Q) (map[int]string, error) {
	out := map[int]string{}
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", Table).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT version, sha256 FROM "+Table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var s string
		if err := rows.Scan(&v, &s); err != nil {
			return nil, err
		}
		out[v] = s
	}
	return out, rows.Err()
}

// Apply brings one database to the newest version. Run it inside a transaction (ApplyFile does); now is the time
// recorded (nil: the clock).
func Apply(ctx context.Context, q Q, db string, now func() int64) (Result, error) {
	if now == nil {
		now = func() int64 { return time.Now().Unix() }
	}
	res := Result{DB: db}
	cur, pending, err := Status(ctx, q, db)
	res.From, res.To = cur, cur
	if err != nil {
		return res, err
	}
	if len(pending) == 0 {
		return res, nil
	}
	if _, err := q.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+Table+
		" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at INTEGER NOT NULL)"); err != nil {
		return res, err
	}
	scratch, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return res, err
	}
	defer scratch.Close()
	scratch.SetMaxOpenConns(1)
	for _, m := range pending {
		for _, st := range Split(m.SQL) {
			adopted, err := execStmt(ctx, q, scratch, st)
			if err != nil {
				return res, fmt.Errorf("%s %04d_%s: %w\n%s", db, m.Version, m.Name, err, firstLine(st))
			}
			res.Adopted = append(res.Adopted, adopted...)
		}
		if _, err := q.ExecContext(ctx, "INSERT INTO "+Table+" (version, name, sha256, applied_at) VALUES (?,?,?,?)",
			m.Version, m.Name, m.Sum, now()); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, fmt.Sprintf("%04d_%s", m.Version, m.Name))
		res.To = m.Version
	}
	return res, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

var createRe = regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?(TABLE|INDEX)\s+(IF\s+NOT\s+EXISTS\s+)?["\x60]?([A-Za-z_][A-Za-z0-9_]*)`)

// execStmt runs one statement, adopting an existing table or index.
func execStmt(ctx context.Context, q Q, scratch *sql.DB, st string) ([]string, error) {
	m := createRe.FindStringSubmatch(st)
	if m == nil {
		_, err := q.ExecContext(ctx, st)
		return nil, err
	}
	kind, name := strings.ToLower(m[2]), m[4]
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type=? AND name=?", kind, name).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		_, err := q.ExecContext(ctx, st)
		return nil, err
	}
	if kind == "index" {
		return []string{name}, nil
	}
	// the table exists: add the columns the statement defines and the table lacks
	want, err := columnsOf(ctx, scratch, name, st)
	if err != nil {
		return nil, err
	}
	have, err := tableInfo(ctx, q, name)
	if err != nil {
		return nil, err
	}
	out := []string{name}
	for _, c := range want {
		if _, ok := have[c.name]; ok {
			continue
		}
		def := `"` + c.name + `" ` + c.typ
		if c.dflt.Valid {
			def += " DEFAULT " + c.dflt.String
			if c.notNull {
				def += " NOT NULL"
			}
		}
		if _, err := q.ExecContext(ctx, "ALTER TABLE \""+name+"\" ADD COLUMN "+def); err != nil {
			return nil, fmt.Errorf("adopt %s: add column %s: %w", name, c.name, err)
		}
		out = append(out, name+"."+c.name)
	}
	return out, nil
}

type column struct {
	name, typ string
	notNull   bool
	dflt      sql.NullString
}

func columnsOf(ctx context.Context, scratch *sql.DB, name, st string) ([]column, error) {
	// a statement may reference other tables (REFERENCES): foreign keys are off, so the scratch copy stands alone
	if _, err := scratch.ExecContext(ctx, `DROP TABLE IF EXISTS "`+name+`"`); err != nil {
		return nil, err
	}
	if _, err := scratch.ExecContext(ctx, st); err != nil {
		return nil, err
	}
	rows, err := scratch.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value FROM pragma_table_info(?)`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var c column
		var nn int
		if err := rows.Scan(&c.name, &c.typ, &nn, &c.dflt); err != nil {
			return nil, err
		}
		c.notNull = nn != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func tableInfo(ctx context.Context, q Q, name string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// Split cuts a script into statements at semicolons outside quotes and comments; "--" comment lines are dropped.
func Split(script string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	rs := []rune(script)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
			b.WriteRune(r)
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			b.WriteRune('\n')
		case r == ';':
			if s := strings.TrimSpace(b.String()); s != "" {
				out = append(out, s)
			}
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// Writable reports whether path (or, when it does not exist yet, its nearest existing directory) can be written.
func Writable(path string) bool {
	p := path
	for {
		if _, err := os.Stat(p); err == nil {
			if syscall.Access(p, 2) != nil { // W_OK
				return false
			}
			if p == path { // an existing database also needs its directory (journal files)
				return syscall.Access(filepath.Dir(p), 2) == nil
			}
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// ApplyFile opens (creating when missing) the database file and applies its migrations in one transaction.
func ApplyFile(ctx context.Context, path, db string) (Result, error) {
	res := Result{DB: db}
	if !Writable(path) {
		return res, fmt.Errorf("%w: %s", ErrReadOnly, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return res, err
	}
	c, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)&_txlock=immediate")
	if err != nil {
		return res, err
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	t, err := c.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	res, err = Apply(ctx, t, db, nil)
	if err != nil {
		t.Rollback()
		return res, err
	}
	return res, t.Commit()
}

// StatusFile reads the version of a database file without writing (a missing file is version 0, all pending).
func StatusFile(ctx context.Context, path, db string) (int, []Migration, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, List(db), nil
	}
	c, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return 0, nil, err
	}
	defer c.Close()
	return Status(ctx, c, db)
}
