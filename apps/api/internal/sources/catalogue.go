package sources

// catalogue.go: the agent's side, no secret read or printed.
//
//	Record  copies root's sources-status.json into the data_sources table of ARGUS_DB (the DB copy the agent and the
//	        P3 routing tool read). Root never writes ARGUS_DB (the collectors' and the agent's database), so
//	        `argus sources record` runs as the agent's user after each apply and check (argus-sources-record.service).
//	List    the catalogue for the agent: type, name, env, purpose, status, enabled, last check, what it is used by;
//	        reads ARGUS_DB, falls back to the status file.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const dbSchema = `CREATE TABLE IF NOT EXISTS data_sources (
  key TEXT PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL, env TEXT, url TEXT, purpose TEXT, status TEXT, detail TEXT,
  identity TEXT, enabled INTEGER DEFAULT 0, managed TEXT, used_by TEXT, cred_where TEXT, last_check INTEGER,
  created_by TEXT, created_at INTEGER, recorded_at INTEGER)`

var statusWords = map[string]string{"readonly": "read-only verified", "ok": "ok", "refused": "refused (can write)",
	"failed": "login failed", "unreachable": "unreachable", "unchecked": "not checked"}

// Row is one data_sources row.
type Row struct {
	Key, Type, Name, Env, URL, Purpose, Status, Detail, Identity, Managed, UsedBy, CredWhere, CreatedBy string
	Enabled                                                                                             bool
	LastCheck, CreatedAt                                                                                int64
}

// LoadStatus is the sources of a status file (nil when missing).
func LoadStatus(path string) map[string]*Entry {
	var st Status
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &st)
	}
	return st.Sources
}

func rowsOf(sources map[string]*Entry) []Row {
	var out []Row
	for k, e := range sources {
		if e == nil || !strings.Contains(k, "/") {
			continue
		}
		m := e.Managed
		if m == "" {
			m = "registry"
		}
		out = append(out, Row{Key: k, Type: e.Type, Name: e.Name, Env: e.Env, URL: e.URL, Purpose: e.Purpose, Status: e.Status,
			Detail: e.Detail, Identity: e.Identity, Managed: m, UsedBy: e.UsedBy, CredWhere: e.CredWhere, CreatedBy: e.CreatedBy,
			Enabled: e.Enabled, LastCheck: e.LastCheck, CreatedAt: e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Record replaces the data_sources rows with the status file's sources; it returns how many.
func Record(statusPath, db string) (int, error) {
	if !filepath.IsAbs(db) {
		return 0, fmt.Errorf("database path must be absolute: %q", db)
	}
	rows := rowsOf(LoadStatus(statusPath))
	c, err := sql.Open("sqlite", "file:"+db+"?_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		return 0, err
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	if _, err := c.Exec(dbSchema); err != nil {
		return 0, err
	}
	tx, err := c.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM data_sources"); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	for _, r := range rows {
		en := 0
		if r.Enabled {
			en = 1
		}
		if _, err := tx.Exec(`INSERT INTO data_sources (key, type, name, env, url, purpose, status, detail, identity, enabled,
  managed, used_by, cred_where, last_check, created_by, created_at, recorded_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Key, r.Type, r.Name, r.Env, r.URL, r.Purpose, r.Status, Mask(r.Detail), Mask(r.Identity), en, r.Managed, r.UsedBy,
			r.CredWhere, r.LastCheck, r.CreatedBy, r.CreatedAt, now); err != nil {
			return 0, err
		}
	}
	return len(rows), tx.Commit()
}

func readRows(db, statusPath string) []Row {
	if db != "" {
		if _, err := os.Stat(db); err == nil {
			if c, err := sql.Open("sqlite", "file:"+db+"?mode=ro&_pragma=busy_timeout(10000)"); err == nil {
				defer c.Close()
				q, err := c.Query(`SELECT key, type, name, COALESCE(env,''), COALESCE(purpose,''), COALESCE(status,''), enabled,
  COALESCE(used_by,''), COALESCE(last_check,0) FROM data_sources ORDER BY type, name`)
				if err == nil {
					var out []Row
					for q.Next() {
						var r Row
						var en int
						if q.Scan(&r.Key, &r.Type, &r.Name, &r.Env, &r.Purpose, &r.Status, &en, &r.UsedBy, &r.LastCheck) == nil {
							r.Enabled = en != 0
							out = append(out, r)
						}
					}
					q.Close()
					if len(out) > 0 {
						return out
					}
				}
			}
		}
	}
	return rowsOf(LoadStatus(statusPath))
}

func ago(ts int64, now time.Time) string {
	if ts <= 0 {
		return "never"
	}
	d := now.Unix() - ts
	if d < 0 {
		d = 0
	}
	switch {
	case d < 3600:
		return fmt.Sprintf("%d min ago", d/60)
	case d < 86400:
		return fmt.Sprintf("%d h ago", d/3600)
	}
	return fmt.Sprintf("%d d ago", d/86400)
}

// List prints the catalogue (one type when typ is set). Never a secret: the rows hold none.
func List(typ, db, statusPath string, out io.Writer) {
	var rows []Row
	for _, r := range readRows(db, statusPath) {
		if typ == "" || r.Type == typ {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		of := ""
		if typ != "" {
			of = " of type " + typ
		}
		fmt.Fprintf(out, "no data sources%s (Settings -> Data sources)\n", of)
		return
	}
	now := time.Now()
	fmt.Fprintln(out, "type\tname\tenv\tpurpose\tstatus\tagent\tlast check\tused by / tool")
	for _, r := range rows {
		st := statusWords[r.Status]
		if st == "" {
			st = dash(r.Status)
		}
		agent := "no"
		if r.Enabled {
			agent = "yes"
		}
		fmt.Fprintln(out, strings.Join([]string{r.Type, r.Name, dash(r.Env), dash(r.Purpose), st, agent, ago(r.LastCheck, now), dash(r.UsedBy)}, "\t"))
	}
	fmt.Fprintln(out, "\nOnly sources with agent=yes are read-only verified for you. Tools that take an instance: "+
		"argus tools kube <kubernetes name>.")
}
