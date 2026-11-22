package cicd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Envs are the environment branches, in promotion order.
var Envs = []string{"develop", "pre-release", "release"}

// EnvOf is the environment of a ref ("" for any other branch).
func EnvOf(ref string) string {
	for _, e := range Envs {
		if ref == e {
			return e
		}
	}
	return ""
}

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	Exec(string, ...any) (sql.Result, error)
}

// Row is one database row by column name.
type Row map[string]any

// S is a column as text.
func (r Row) S(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// I is a column as a number (0 when NULL).
func (r Row) I(k string) int64 { return toI(r[k]) }

// Null: the column is NULL.
func (r Row) Null(k string) bool { return r[k] == nil }

func toI(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		var n int64
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}

func rows(q querier, query string, args ...any) ([]Row, error) {
	rs, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []Row
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				r[c] = string(b)
			} else {
				r[c] = vals[i]
			}
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func one(q querier, query string, args ...any) (Row, error) {
	rs, err := rows(q, query, args...)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return rs[0], nil
}

// GetState reads one cicd_state value into v; false when it is missing or not valid JSON.
func GetState(q querier, k string, v any) bool {
	r, err := one(q, "SELECT v FROM cicd_state WHERE k=?", k)
	if err != nil || r == nil {
		return false
	}
	return json.Unmarshal([]byte(r.S("v")), v) == nil
}

// SetState writes one cicd_state value.
func SetState(q querier, k string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Exec("INSERT INTO cicd_state (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, string(b))
	return err
}

var tsRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.\d+)?([+-]\d\d:?\d\d)?`)

// TS is a GitLab ISO time in unix seconds; 0 when it does not parse.
func TS(iso string) int64 {
	s := strings.Replace(iso, "Z", "+00:00", 1)
	m := tsRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	tz := m[2]
	if tz == "" {
		tz = "+00:00"
	}
	if !strings.Contains(tz, ":") {
		tz = tz[:3] + ":" + tz[3:]
	}
	t, err := time.Parse("2006-01-02T15:04:05-07:00", m[1]+tz)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// ISO is unix seconds as GitLab's UTC form.
func ISO(t int64) string { return time.Unix(t, 0).UTC().Format("2006-01-02T15:04:05Z") }

// F is the fields of one finding.
type F map[string]any

var findingCols = []string{"project_id", "project", "env", "ref", "severity", "klass", "rule", "title", "detail", "evidence",
	"proposals", "pipeline_id", "job_id", "job_name", "sha", "author", "mr", "llm"}

func runeHead(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// UpsertFinding inserts or refreshes one finding by key: (id, new, reopened). bump false keeps the occurrence count.
func UpsertFinding(q querier, kind, key string, now int64, f F, bump bool) (int64, bool, bool, error) {
	old, err := one(q, "SELECT id, status, occurrences FROM cicd_findings WHERE key=?", key)
	if err != nil {
		return 0, false, false, err
	}
	var names []string
	var vals []any
	for _, c := range findingCols {
		v, ok := f[c]
		if !ok {
			continue
		}
		switch c {
		case "proposals":
			if _, isStr := v.(string); !isStr && v != nil {
				b, _ := pyDumps(v)
				v = b
			}
		case "title", "detail":
			if s, _ := v.(string); s != "" {
				v = runeHead(s, 2000)
			}
		case "evidence":
			if s, _ := v.(string); s != "" {
				v = runeHead(s, 8000)
			}
		}
		names = append(names, c)
		vals = append(vals, v)
	}
	if old == nil {
		cols := append([]string{"kind", "key", "first_seen", "last_seen", "status"}, names...)
		args := append([]any{kind, key, now, now, "open"}, vals...)
		res, err := q.Exec("INSERT INTO cicd_findings ("+strings.Join(cols, ", ")+") VALUES ("+
			strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")+")", args...)
		if err != nil {
			return 0, false, false, err
		}
		id, _ := res.LastInsertId()
		return id, true, false, nil
	}
	reopened := old.S("status") != "open"
	sets := ""
	for _, n := range names {
		sets += n + "=?, "
	}
	inc := 0
	if bump {
		inc = 1
	}
	_, err = q.Exec("UPDATE cicd_findings SET "+sets+"last_seen=?, status='open', resolved_at=NULL, resolve_note=NULL, "+
		"occurrences=occurrences+? WHERE id=?", append(vals, now, inc, old.I("id"))...)
	return old.I("id"), false, reopened, err
}

// ResolveFindings closes the open findings matching where; it returns them (id, incident_id).
func ResolveFindings(q querier, where string, args []any, note string, now int64) ([]Row, error) {
	rs, err := rows(q, "SELECT id, incident_id FROM cicd_findings WHERE status='open' AND "+where, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if _, err := q.Exec("UPDATE cicd_findings SET status='resolved', resolved_at=?, resolve_note=? WHERE id=?",
			now, runeHead(note, 300), r.I("id")); err != nil {
			return nil, err
		}
	}
	return rs, nil
}
