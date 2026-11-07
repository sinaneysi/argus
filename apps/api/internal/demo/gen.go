// Package demo writes the public demo's sample data: a fictional company, "Northwind Commerce", with about 40 VMs,
// incidents, QA reports, backups, certificates, vulnerabilities, CI/CD, a task board and the state files every page
// of argus-api reads. Nothing in it is real: addresses from RFC 1918 / RFC 5737, domains under northwind.example
// (RFC 2606), invented people.
//
// It is the Go port of the original Python generator (demo/generate.py, removed after the port; it is in the git
// history). The output is the same: the same SQLite schemas and rows, the same JSON files and keys, the same demo
// users. The random numbers come from a port of Python's Mersenne Twister, so the same seed and time give the same
// values as the Python generator did.
package demo

import (
	"crypto/sha1"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
	_ "modernc.org/sqlite"
)

//go:embed data/schema_ops.sql data/schema_board.sql data/schema_mfa.sql data/content/*.md
var files embed.FS

// Time and size units. They are variables, not constants, on purpose: Go folds constant expressions exactly, while
// Python computes 4.9 * H in float64. As variables, 4.9 * H is a float64 multiplication in both languages.
var (
	H, D, M = 3600.0, 86400.0, 60.0
	GBf     = float64(1 << 30)
)

const (
	GB = int64(1) << 30
	MB = int64(1) << 20
)

// DefaultSeed is the Python generator's default --seed.
const DefaultSeed = 20261024

type tx = *sql.Tx

// om is a Python dict literal: alternating keys and values, insertion order kept.
func om(kv ...any) *py.OMap { return py.NewOMap(kv...) }

// L is a Python list literal.
func L(xs ...any) []any { return xs }

// h12 is sha1(s).hexdigest()[:12].
func h12(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// jd is json.dumps(v, ensure_ascii=False).
func jd(v any) string { return dumps(v, -1) }

func content(name string) string {
	b, err := files.ReadFile("data/content/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func schema(name string) string {
	b, err := files.ReadFile("data/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// round is Python's round(x, n) for n >= 1: correctly rounded on the exact binary value, ties to even.
func round(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	return f
}

// roundi is Python's round(x): the nearest integer, ties to even.
func roundi(x float64) int64 { return int64(math.RoundToEven(x)) }

// Gen holds one generation run.
type Gen struct {
	out, authDir string
	now          int64
	seed         int64
	r            *pyRand
	runID        int64
	vmIDs        map[string]int
	inc          map[string]int
	ops, board   tx
	opsDB, brdDB *sql.DB

	// what later steps read back from earlier ones (the Python read some of it from the files it wrote)
	vmDocs map[int]vmNow
}

type vmNow struct {
	mounts             [][2]int64 // size, avail
	cpu, mem, psiIO    float64
	cpuIsInt, memIsInt bool
}

func newGen(out, authDir string, now float64, seed int64) *Gen {
	return &Gen{out: out, authDir: authDir, now: int64(now), seed: seed, r: newPyRand(seed), runID: 1000,
		vmIDs: map[string]int{}, inc: map[string]int{}, vmDocs: map[int]vmNow{}}
}

// t is a timestamp `ago` seconds before now (int(now - ago)).
func (g *Gen) t(ago float64) int64 { return int64(float64(g.now) - ago) }

func (g *Gen) at(ago float64) time.Time {
	return time.Unix(int64(math.Floor(float64(g.now)-ago)), 0)
}

// iso is the UTC time `ago` seconds before now as 2026-10-30T12:00:00Z.
func (g *Gen) iso(ago float64) string { return g.at(ago).UTC().Format("2006-01-02T15:04:05Z") }

// day is the local date `ago` seconds before now.
func (g *Gen) day(ago float64) string { return g.at(ago).Local().Format("2006-01-02") }

func (g *Gen) path(p ...string) string {
	full := filepath.Join(append([]string{g.out}, p...)...)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		panic(err)
	}
	return full
}

func (g *Gen) writeJSON(rel string, v any) {
	g.writeText(rel, dumps(v, 1)+"\n")
}

func (g *Gen) writeText(rel, text string) {
	if err := os.WriteFile(g.path(rel), []byte(text), 0o644); err != nil {
		panic(err)
	}
}

// ins is INSERT INTO table (cols) VALUES (...) from alternating column names and values; it returns lastrowid.
func (g *Gen) ins(t tx, table string, kv ...any) int64 {
	cols := make([]string, 0, len(kv)/2)
	vals := make([]any, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		cols = append(cols, `"`+kv[i].(string)+`"`)
		vals = append(vals, kv[i+1])
	}
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	res, err := t.Exec(q, vals...)
	if err != nil {
		panic(fmt.Errorf("%s: %w", table, err))
	}
	id, _ := res.LastInsertId()
	return id
}

func (g *Gen) exec(t tx, q string, args ...any) {
	if _, err := t.Exec(q, args...); err != nil {
		panic(fmt.Errorf("%s: %w", q, err))
	}
}

// one is the first column of the first row, or nil.
func (g *Gen) one(t tx, q string, args ...any) any {
	var v any
	if err := t.QueryRow(q, args...).Scan(&v); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		panic(fmt.Errorf("%s: %w", q, err))
	}
	return v
}

func (g *Gen) oneInt(t tx, q string, args ...any) int64 { return py.I(g.one(t, q, args...)) }

func (g *Gen) oneStr(t tx, q string, args ...any) string {
	switch v := g.one(t, q, args...).(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

// runOpts are the optional arguments of Gen.run (nil: drawn at random, like the Python defaults).
type runOpts struct {
	cost    *float64
	dur     *int64
	turns   *int64
	denials int64
	mr      any
}

func fp(f float64) *float64 { return &f }
func ip(i int64) *int64     { return &i }

// run records an agent run; cost, duration and turns are random unless given.
func (g *Gen) run(task string, ago float64, status, summary string, o runOpts) int64 {
	g.runID++
	rid := g.runID
	var cost float64
	if o.cost == nil {
		cost = round(g.r.uniform(0.02, 0.35), 4)
	} else {
		cost = *o.cost
	}
	var dur int64
	if o.dur == nil {
		dur = int64(g.r.randint(20, 240)) * 1000
	} else {
		dur = *o.dur
	}
	var turns int64
	if o.turns == nil {
		turns = int64(g.r.randint(4, 28))
	} else {
		turns = *o.turns
	}
	isErr := 0
	if status == "ERROR" {
		isErr = 1
	}
	g.ins(g.ops, "runs", "id", rid, "task", task, "started_at", g.t(ago), "duration_ms", dur, "status", status,
		"summary", summary, "cost_usd", cost, "turns", turns, "denials", o.denials, "is_error", isErr,
		"mr_url", o.mr, "log_path", fmt.Sprintf("/var/lib/argus/runs/%s-%d.jsonl", task, rid))
	return rid
}

func openDB(path string) (*sql.DB, tx) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	t, err := db.Begin()
	if err != nil {
		panic(err)
	}
	return db, t
}

func closeDB(db *sql.DB, t tx) {
	if err := t.Commit(); err != nil {
		panic(err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		panic(err)
	}
	if err := db.Close(); err != nil {
		panic(err)
	}
}

// build writes the whole data set into g.out (and auth.db into g.authDir).
func (g *Gen) build() {
	if err := os.MkdirAll(g.out, 0o755); err != nil {
		panic(err)
	}
	g.opsDB, g.ops = openDB(g.path("ops.db"))
	g.exec(g.ops, schema("schema_ops.sql"))
	g.brdDB, g.board = openDB(g.path("board", "board.db"))
	g.exec(g.board, schema("schema_board.sql"))
	for _, step := range []func(){g.inventory, g.scheduledRuns, g.incidents, g.qa, g.problems, g.alerts, g.capacity,
		g.hygiene, g.backups, g.certs, g.cve, g.cicd, g.boardData, g.proposals, g.reviewCases, g.ideas, g.fixer,
		g.gitlabIssues, g.kubeStatus, g.sourcesStatus, g.sourceCatalogue, g.credentials, g.today, g.stateFiles, g.settingsOut, g.queues, g.seo} {
		step()
	}
	closeDB(g.opsDB, g.ops)
	closeDB(g.brdDB, g.board)
	g.auth()
}

// Generate writes a complete data set into a fresh temporary directory inside out, then moves every file into out
// (atomic per file: the app never sees a half-written database). auth.db goes to authDir, only when it is missing
// there, so the demo sessions survive a regeneration.
func Generate(out, authDir string, now float64, seed int64) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("demo generate: %v", p)
		}
	}()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(out, ".next-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tmpAuth, err := os.MkdirTemp(out, ".auth-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpAuth)
	newGen(tmp, tmpAuth, now, seed).build()
	err = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(tmp, p)
		dst := filepath.Join(out, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return os.Rename(p, dst)
	})
	if err != nil {
		return err
	}
	if authDir != "" {
		target := filepath.Join(authDir, "auth.db")
		if _, serr := os.Stat(target); os.IsNotExist(serr) {
			if err := os.MkdirAll(authDir, 0o755); err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(tmpAuth, "auth.db"))
			if err != nil {
				return err
			}
			part := filepath.Join(authDir, ".auth.db.tmp") // another filesystem (a separate volume): copy, then rename
			if err := os.WriteFile(part, data, 0o644); err != nil {
				return err
			}
			if err := os.Rename(part, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// RefreshLive rewrites the files the pages call stale after a few minutes: the alert snapshot, the VM pulse and the
// CI/CD watcher's time.
func RefreshLive(out string, now float64) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("demo refresh: %v", p)
		}
	}()
	g := newGen(out, "", now, 0)
	g.alertsSnapshot()
	for i, v := range VMS {
		g.vmIDs[v.name] = i + 1
	}
	g.vmPulse()
	g.agentProgressLive() // the running demo planning job: its progress file and job start stay a few minutes old
	if db, derr := sql.Open("sqlite", filepath.Join(out, "board", "board.db")); derr == nil {
		db.Exec("UPDATE agent_tasks SET job_started=?, updated=? WHERE key='OPS-231' AND state='planning'", g.t(3*M+20), g.t(3*M+20))
		db.Close()
	}
	if data, rerr := os.ReadFile(filepath.Join(out, "cicd-status.json")); rerr == nil {
		if st, perr := py.ParseJSON(data); perr == nil {
			if m, ok := st.(*py.OMap); ok {
				w := m.Map("watch")
				if w == nil {
					w = om()
					m.Set("watch", w)
				}
				w.Set("at", int64(now)-120)
				g.writeJSON("cicd-status.json", m)
			}
		}
	}
	return nil
}

// Ready reports whether a generated data set and auth.db exist (the data container's health check).
func Ready(out, authDir string) bool {
	for _, p := range []string{filepath.Join(out, "ops.db"), filepath.Join(authDir, "auth.db")} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// Loop regenerates every `every` and refreshes the live files every minute in between; it never returns.
func Loop(out, authDir string, seed int64, every time.Duration, logf func(string, ...any)) {
	last := time.Now()
	for {
		time.Sleep(time.Minute)
		if time.Since(last) >= every {
			now := float64(time.Now().UnixNano()) / 1e9
			if err := Generate(out, authDir, now, seed); err != nil {
				logf("%v", err)
			}
			last = time.Now()
		} else if err := RefreshLive(out, float64(time.Now().UnixNano())/1e9); err != nil {
			logf("%v", err)
		}
	}
}

// --- JSON as Python's json.dumps(v, ensure_ascii=False[, indent=n]) ----------------------------------------------

func dumps(v any, indent int) string {
	var b strings.Builder
	writeJSON(&b, v, indent, 0)
	return b.String()
}

func writeJSON(b *strings.Builder, v any, indent, level int) {
	sep := ", "
	nl := func(l int) {}
	if indent >= 0 {
		sep = ","
		nl = func(l int) {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*l))
		}
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(py.FloatRepr(x))
	case string:
		writeString(b, x)
	case []string:
		l := make([]any, len(x))
		for i, s := range x {
			l[i] = s
		}
		writeJSON(b, l, indent, level)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, it := range x {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(level + 1)
			writeJSON(b, it, indent, level+1)
		}
		nl(level)
		b.WriteByte(']')
	case *py.OMap:
		if x.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteString(sep)
			}
			nl(level + 1)
			writeString(b, k)
			b.WriteString(": ")
			writeJSON(b, x.Get(k), indent, level+1)
		}
		nl(level)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("demo: cannot encode %T as JSON", v))
	}
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
