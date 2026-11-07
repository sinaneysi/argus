package app

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sinaneysi/argus/internal/py"

	_ "modernc.org/sqlite" // pure-Go SQLite: a static, CGO-free binary
)

// Read-only SQLite access. The Python app opened a new connection per query (file:...?mode=ro). Here one pool
// per file is kept and reopened when the file is replaced (another inode), so a reader never holds on to a
// database a job swapped out, and a query costs no open/schema parse.

type roPool struct {
	db    *sql.DB
	ino   uint64
	dev   uint64
	until time.Time
}

var (
	poolMu sync.Mutex
	pools  = map[string]*roPool{}
)

func fileID(path string) (uint64, uint64, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino), uint64(s.Dev), true
	}
	return 0, 0, true
}

func roDB(path string) (*sql.DB, error) {
	ino, dev, ok := fileID(path)
	if !ok {
		return nil, fmt.Errorf("unable to open database file")
	}
	poolMu.Lock()
	defer poolMu.Unlock()
	if p := pools[path]; p != nil {
		if p.ino == ino && p.dev == dev {
			return p.db, nil
		}
		old := p.db
		go func() { time.Sleep(30 * time.Second); old.Close() }() // let running queries finish
		delete(pools, path)
	}
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=temp_store(2)&_txlock=deferred"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pools[path] = &roPool{db: db, ino: ino, dev: dev}
	return db, nil
}

// --- result cache ----------------------------------------------------------------------------------------
//
// The pages read the same rows again and again between two writes of a job. A result is kept with the file
// version it was read from (inode, size and mtime of the database and of its -wal file; every commit changes one
// of them) and served again only while that version is unchanged, so a cached answer is always what SQLite
// would return now. Callers get their own copy of every row.

type fileVer struct {
	ino, size, mtime, wsize, wmtime int64
}

func versionOf(path string) (fileVer, bool) {
	var v fileVer
	st, err := os.Stat(path)
	if err != nil {
		return v, false
	}
	v.size, v.mtime = st.Size(), st.ModTime().UnixNano()
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		v.ino = int64(s.Ino)
	}
	if w, err := os.Stat(path + "-wal"); err == nil {
		v.wsize, v.wmtime = w.Size(), w.ModTime().UnixNano()
	}
	return v, true
}

type cached struct {
	ver  fileVer
	rows []Row
}

var (
	cacheMu   sync.Mutex
	cache     = map[string]cached{}
	cacheRows int
	// NoCache turns the result cache off (tests that rewrite a database within one mtime tick).
	NoCache = env("ARGUS_NO_QUERY_CACHE", "0") == "1"
)

const maxCacheRows = 300000

func cacheKey(path, query string, args []any) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte(0)
	b.WriteString(query)
	for _, a := range args {
		fmt.Fprintf(&b, "\x00%T:%v", a, a)
	}
	return b.String()
}

func copyRows(rows []Row) []Row {
	if rows == nil {
		return nil
	}
	out := make([]Row, len(rows))
	for i, r := range rows {
		c := make(Row, len(r))
		for k, v := range r {
			c[k] = v
		}
		out[i] = c
	}
	return out
}

// QueryPath runs a read-only query on the SQLite file at path (answered from the cache while the file is
// unchanged).
func QueryPath(path, query string, args ...any) ([]Row, error) {
	if NoCache {
		return queryPath(path, query, args...)
	}
	ver, ok := versionOf(path)
	if !ok {
		return queryPath(path, query, args...)
	}
	key := cacheKey(path, query, args)
	cacheMu.Lock()
	if c, hit := cache[key]; hit && c.ver == ver {
		cacheMu.Unlock()
		return copyRows(c.rows), nil
	}
	cacheMu.Unlock()
	rows, err := queryPath(path, query, args...)
	if err != nil {
		return rows, err
	}
	if after, ok := versionOf(path); ok && after == ver { // nothing committed while we read
		cacheMu.Lock()
		if cacheRows+len(rows) > maxCacheRows {
			cache, cacheRows = map[string]cached{}, 0
		}
		if old, had := cache[key]; had {
			cacheRows -= len(old.rows)
		}
		cache[key] = cached{ver, rows}
		cacheRows += len(rows)
		cacheMu.Unlock()
		return copyRows(rows), nil
	}
	return rows, nil
}

func queryPath(path, query string, args ...any) ([]Row, error) {
	db, err := roDB(path)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(Row, len(cols))
		for i, c := range cols {
			if _, dup := r[c]; dup {
				continue // sqlite3.Row: the first column of that name
			}
			r[c] = normValue(vals[i])
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func normValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case int:
		return int64(x)
	case time.Time: // a column declared DATE/DATETIME: Python's sqlite3 gives the stored text
		return x.Format("2006-01-02 15:04:05")
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	}
	return v
}

// Q is q(sql, *args) on ops.db: [] on any SQLite error (a missing table on a fresh install).
func Q(query string, args ...any) []Row {
	rows, err := QueryPath(DB, query, args...)
	if err != nil {
		if debugSQL {
			log.Printf("sql error: %v in %.80s", err, query)
		}
		return nil
	}
	return rows
}

var debugSQL = env("ARGUS_DEBUG_SQL", "0") == "1"

// LatestRun is latest_run(task): the newest runs row, or the status file of the jobs without one.
func LatestRun(task string) Row {
	if task == "insights" || task == "inventory" || task == "cve" {
		st := ReadJSONMap(filepath.Join(filepath.Dir(DB), task+"-status.json"))
		at, ok := st.Get("at").(int64)
		if !ok {
			return nil
		}
		return Row{"started_at": at, "status": py.Head(py.Str(st.GetD("status", "")), 8)}
	}
	r := Q("SELECT * FROM runs WHERE task=? ORDER BY started_at DESC LIMIT 1", task)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}
