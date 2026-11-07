package app

// Problems on fixture data: the feature flag (off = no nav item, no panel, 404), the list, the problem page, the
// banner and panel on an incident and an RCA, escaping, the request files of POST /problems/act (fields, JSON types,
// file name), roles (viewer link = suggestion, viewer accept refused), CSRF, and the missing request directory.

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

const probTestSchema = `
CREATE TABLE problems (id INTEGER PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'new', origin TEXT NOT NULL,
 root_cause TEXT, root_cause_label TEXT, workaround TEXT, severity TEXT, env TEXT, customer_facing INTEGER DEFAULT 0, owner TEXT,
 signature TEXT, fanout INTEGER DEFAULT 0, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT,
 fix_ref TEXT, fix_at INTEGER, lazy TEXT, verify_state TEXT, verify_since INTEGER, verify_detail TEXT, thread_root TEXT,
 thread_room TEXT, merged_into INTEGER, recurrences INTEGER DEFAULT 0, created_at INTEGER NOT NULL, created_by TEXT NOT NULL,
 updated_at INTEGER NOT NULL, first_seen INTEGER, investigating_at INTEGER, known_error_at INTEGER, fix_started_at INTEGER,
 resolved_at INTEGER, resolved_by TEXT, resolve_note TEXT, closed_at INTEGER);
CREATE TABLE problem_links (id INTEGER PRIMARY KEY, problem_id INTEGER NOT NULL, ref_type TEXT NOT NULL, ref_id TEXT NOT NULL,
 link_type TEXT NOT NULL, state TEXT NOT NULL, is_primary INTEGER DEFAULT 0, source TEXT NOT NULL, confidence REAL, reason TEXT,
 features TEXT, "by" TEXT NOT NULL, at INTEGER NOT NULL, decided_by TEXT, decided_at INTEGER, note TEXT);
CREATE TABLE problem_events (problem_id INTEGER NOT NULL, at INTEGER NOT NULL, kind TEXT NOT NULL, actor TEXT, ref_type TEXT,
 ref_id TEXT, text TEXT);
CREATE TABLE incidents (id INTEGER PRIMARY KEY, key TEXT NOT NULL, dc TEXT, subject TEXT, family TEXT, title TEXT, env TEXT,
 severity TEXT, alert_severity TEXT, customer_facing INTEGER DEFAULT 0, status TEXT NOT NULL DEFAULT 'open', owner TEXT,
 first_seen INTEGER, last_seen INTEGER, occurrences INTEGER DEFAULT 0, acked_at INTEGER, acked_by TEXT, resolved_at INTEGER,
 resolved_by TEXT, resolve_note TEXT, last_firing INTEGER, clear_since INTEGER, thread_root TEXT, fingerprints TEXT);
CREATE TABLE incident_events (incident_id INTEGER NOT NULL, at INTEGER NOT NULL, kind TEXT NOT NULL, run_id INTEGER, idx INTEGER,
 task TEXT, actor TEXT, text TEXT);
CREATE TABLE rca_reports (id INTEGER PRIMARY KEY, incident_id INTEGER, title TEXT NOT NULL, path TEXT NOT NULL UNIQUE, mr_url TEXT,
 created INTEGER NOT NULL, status TEXT NOT NULL, content TEXT, run_id INTEGER, source TEXT, cost_usd REAL, duration_s INTEGER,
 redacted INTEGER DEFAULT 0);
`

func probSeed(t *testing.T, e *srvEnv, enabled, dir bool) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	for _, s := range strings.Split(probTestSchema, ";") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err, s)
		}
	}
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO incidents (id, key, subject, family, title, env, severity, status, first_seen, last_seen, occurrences)
		  VALUES (1, 'A|www.example.com|http-5xx', 'www.example.com', 'http-5xx', 'www 502 <script>alert(1)</script>', 'release',
		  'critical', 'open', ?, ?, 3), (2, 'A|admin.example.com|http-5xx', 'admin.example.com', 'http-5xx', 'admin 502', 'release',
		  'high', 'open', ?, ?, 1)`, []any{now - 7200, now - 60, now - 7000, now - 120}},
		{`INSERT INTO rca_reports (id, incident_id, title, path, created, status, content, source) VALUES
		  (7, 1, 'Dead upstreams', 'r/7.md', ?, 'open', '# RCA', 'rca-deep'), (8, NULL, 'Free text <b>b</b>', 'r/8.md', ?, 'open', '# x', 'rca-deep')`,
			[]any{now - 3600, now - 1800}},
		{`INSERT INTO problems (id, title, status, origin, root_cause, root_cause_label, severity, env, created_at, created_by, updated_at,
		  first_seen) VALUES (1, 'Dead upstreams on 192.0.2.31 <img src=x onerror=alert(1)>', 'known_error', 'manual', 'units not listening',
		  'proven', 'critical', 'release', ?, 'admin', ?, ?), (2, 'Merged one', 'closed', 'manual', NULL, NULL, NULL, NULL, ?, 'admin', ?, NULL)`,
			[]any{now - 3000, now - 100, now - 7200, now - 2000, now - 1000}},
		{`UPDATE problems SET merged_into=1 WHERE id=2`, nil},
		{`INSERT INTO problem_links (id, problem_id, ref_type, ref_id, link_type, state, is_primary, source, confidence, reason, "by", at,
		  decided_by, decided_at) VALUES
		  (1, 1, 'incident', '1', 'caused_by', 'accepted', 1, 'human', 1.0, NULL, 'admin', ?, 'admin', ?),
		  (2, 1, 'rca', '7', 'explains', 'accepted', 0, 'human', 1.0, NULL, 'admin', ?, 'admin', ?),
		  (3, 1, 'incident', '2', 'caused_by', 'suggested', 0, 'argus', 0.86, 'same upstream 192.0.2.31:3000 <b>b</b>', 'argus', ?, NULL, NULL),
		  (4, 2, 'problem', '1', 'duplicate_of', 'accepted', 0, 'human', 1.0, NULL, 'admin', ?, 'admin', ?)`,
			[]any{now - 2900, now - 2900, now - 2900, now - 2900, now - 600, now - 1000, now - 1000}},
		{`INSERT INTO problem_events (problem_id, at, kind, actor, ref_type, ref_id, text) VALUES (1, ?, 'status', 'admin', NULL, NULL,
		  'investigating -> known_error'), (1, ?, 'link', 'admin', 'incident', '1', 'caused_by')`, []any{now - 2800, now - 2900}},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatal(err, s.q)
		}
	}
	if enabled {
		st := `{"enabled": true, "auto_accept": false, "min_confidence": 0.6, "reject_days": 30, "last_suggest": ` + I2S(now-300) + `}`
		if err := os.WriteFile(filepath.Join(e.dir, "problems.json"), []byte(st), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if dir {
		os.MkdirAll(filepath.Join(e.dir, "in", "problem-actions"), 0o755)
	}
}

func probMust(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q", w)
		}
	}
}

func probMustNot(t *testing.T, body string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(body, w) {
			t.Fatalf("unexpected %q", w)
		}
	}
}

var probFileRe = regexp.MustCompile(`^[0-9]{10}-[0-9a-f]{8}\.json$`)

func probFiles(t *testing.T, e *srvEnv) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(e.dir, "in", "problem-actions", "*.json"))
	var out []map[string]any
	for _, f := range files {
		if !probFileRe.MatchString(filepath.Base(f)) {
			t.Fatal("bad name", f)
		}
		b, _ := os.ReadFile(f)
		if len(b) > 4096 {
			t.Fatal("too large")
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
		os.Remove(f)
	}
	return out
}

func TestProblemsPosts(t *testing.T) {
	e := srvSetup(t)
	probSeed(t, e, true, true)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	r := e.v1Post("/api/v1/problems/act", ck, map[string]any{"csrf": csrf, "action": "link", "problem": "1",
		"ref": []any{"incident:2", "rca:8", "incident:2"}, "link_type": "caused_by", "note": " same \t cause ",
		"back": "/incidents/view?id=2#problem"}, nil)
	if r.status != 200 || !regexp.MustCompile(`^/incidents/view\?id=2&sent=problem&preq=[0-9]{10}-[0-9a-f]{8}#problem$`).MatchString(r.loc) {
		t.Fatal(r.status, r.loc)
	}
	got := probFiles(t, e)
	if len(got) != 1 || got[0]["action"] != "link" || got[0]["problem"] != float64(1) || got[0]["role"] != "admin" ||
		got[0]["by"] != "admin" || got[0]["note"] != "same cause" || got[0]["link_type"] != "caused_by" {
		t.Fatal(got)
	}
	if refs, _ := got[0]["refs"].([]any); len(refs) != 2 || refs[0] != "incident:2" || refs[1] != "rca:8" {
		t.Fatal(got[0]["refs"])
	}
	// the panel says it was sent while the file waits
	r = e.req("POST", "/api/v1/problems/act", ck, url.Values{"csrf": {csrf}, "action": {"accept"}, "link": {"3"}, "back": {"/problems?view=suggestions"}}, nil)
	if r.status != 200 {
		t.Fatal(r.status)
	}
	if g := probFiles(t, e); len(g) != 1 || g[0]["links"].([]any)[0] != float64(3) {
		t.Fatal(g)
	}
	cases := []struct {
		form url.Values
		code int
	}{
		{url.Values{"csrf": {"0"}, "action": {"accept"}, "link": {"3"}}, 403},
		{url.Values{"csrf": {csrf}, "action": {"nuke"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"link"}, "problem": {"99"}, "ref": {"incident:2"}}, 404},
		{url.Values{"csrf": {csrf}, "action": {"link"}, "problem": {"1"}, "ref": {"incident:x"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"link"}, "problem": {"1"}, "ref": {"rca:8"}, "link_type": {"fixed_by"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"unlink"}, "link": {"1"}, "note": {"no"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"create"}, "title": {"ab"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"create"}, "title": {"abc"}, "from": {"problem:1"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"status"}, "problem": {"2"}, "to": {"known_error"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"status"}, "problem": {"1"}, "to": {"new"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"resolve"}, "problem": {"1"}, "note": {""}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"merge"}, "problem": {"1"}, "into": {"1"}}, 400},
		{url.Values{"csrf": {csrf}, "action": {"edit"}, "problem": {"1"}, "owner": {"Bad Owner"}}, 400},
	}
	for _, c := range cases {
		if r := e.req("POST", "/api/v1/problems/act", ck, c.form, nil); r.status != c.code {
			t.Fatal(c.form, r.status)
		}
	}
	if g := probFiles(t, e); len(g) != 0 {
		t.Fatal("refused requests wrote files", g)
	}
	for _, f := range []url.Values{
		{"action": {"create"}, "title": {"From the RCA"}, "from": {"rca:8"}},
		{"action": {"status"}, "problem": {"1"}, "to": {"fix_in_progress"}, "note": {"task OPS-1"}},
		{"action": {"resolve"}, "problem": {"1"}, "note": {"fixed and verified"}, "issue": {"1"}},
		{"action": {"merge"}, "problem": {"2"}, "into": {"P-1"}},
		{"action": {"edit"}, "problem": {"1"}, "owner": {"admin"}},
		{"action": {"unlink"}, "link": {"1"}, "note": {"not this cause"}},
		{"action": {"primary"}, "link": {"1"}},
		{"action": {"reject"}, "link": {"3"}, "note": {"different cause"}},
	} {
		f.Set("csrf", csrf)
		if r := e.req("POST", "/api/v1/problems/act", ck, f, nil); r.status != 200 {
			t.Fatal(f, r.status)
		}
	}
	g := probFiles(t, e)
	if len(g) != 8 {
		t.Fatal(len(g))
	}
	seen := map[string]map[string]any{}
	for _, x := range g {
		seen[x["action"].(string)] = x
	}
	if seen["resolve"]["issues"].([]any)[0] != float64(1) || seen["merge"]["into"] != float64(1) || seen["create"]["from"] != "rca:8" ||
		seen["status"]["to"] != "fix_in_progress" || seen["primary"]["link"] != float64(1) {
		t.Fatal(seen)
	}
	// viewers: a link becomes a suggestion (role viewer), anything else is refused
	vt, vc := e.login("view.er")
	vk := map[string]string{auth.COOKIE: vt}
	if r := e.req("POST", "/api/v1/problems/act", vk, url.Values{"csrf": {vc}, "action": {"link"}, "problem": {"1"}, "ref": {"incident:2"}}, nil); r.status != 200 {
		t.Fatal(r.status)
	}
	if g := probFiles(t, e); len(g) != 1 || g[0]["role"] != "viewer" || g[0]["by"] != "view.er" {
		t.Fatal(g)
	}
	for _, a := range []string{"accept", "unlink", "create", "status"} {
		if r := e.req("POST", "/api/v1/problems/act", vk, url.Values{"csrf": {vc}, "action": {a}, "link": {"3"}, "note": {"xxxx"}}, nil); r.status != 403 {
			t.Fatal(a, r.status)
		}
	}
}
