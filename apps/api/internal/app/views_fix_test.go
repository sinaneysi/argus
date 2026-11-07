package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const fixHostile = `<script>alert(1)</script><img src=x onerror=alert(2)>`

var (
	fixAdmin  = &Session{Username: "boss", Role: "admin", CSRF: "c0ffee"}
	fixViewer = &Session{Username: "eve", Role: "viewer", CSRF: "c0ffee"}
)

// fixTestEnv points the app at a fresh ops.db (the live schema of the fix group's tables) and settings dirs.
func fixTestEnv(t *testing.T) (string, *sql.DB) {
	t.Helper()
	d := t.TempDir()
	old := []string{DB, SET_IN, SET_OUT, REPORTS}
	t.Cleanup(func() { DB, SET_IN, SET_OUT, REPORTS = old[0], old[1], old[2], old[3] })
	DB, SET_IN, SET_OUT, REPORTS = filepath.Join(d, "ops.db"), filepath.Join(d, "in"), filepath.Join(d, "out"), filepath.Join(d, "rca")
	for _, x := range []string{SET_IN, SET_OUT, REPORTS, filepath.Join(SET_IN, "incident-actions")} {
		os.MkdirAll(x, 0o755)
	}
	schema, err := os.ReadFile("testdata/fix_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range strings.Split(string(schema), ";\n") {
		if strings.TrimSpace(s) != "" {
			if _, err := db.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := NowInt()
	mustExec(t, db, `INSERT INTO runs (id, task, started_at, status, summary) VALUES (7, 'chat-triage', ?, 'OK', ?)`, now-60, "# summary "+fixHostile)
	rca := "## Root cause\n" + fixHostile + "\n\n## How to fix it\n### Option 1: Restart `api` " + fixHostile + " (recommended)\n" +
		"- **Risk:** low\n\n## Prompt for a write-access agent\n```\nrestart api\n" + fixHostile + "\n```\n"
	mustExec(t, db, `INSERT INTO triage_problems (run_id, idx, title, severity, environment, sources, summary, rca, fix_prompt, suspicious)
		VALUES (7, 0, ?, 'high', 'develop', '["a"]', 'sum', ?, '', 0)`, "T "+fixHostile, rca)
	mustExec(t, db, `INSERT INTO incidents (id, key, dc, subject, title, env, severity, status, first_seen, last_seen, occurrences)
		VALUES (3, 'k', 'KUMA', 'api.example', ?, 'develop', 'high', 'open', ?, ?, 2)`, "I "+fixHostile, now-7200, now-60)
	mustExec(t, db, `INSERT INTO incident_events (incident_id, at, kind, run_id, idx, task) VALUES (3, ?, 'finding', 7, 0, 'chat-triage')`, now-60)
	mustExec(t, db, `INSERT INTO chat_messages (event_id, room_id, room_name, source, sender, sender_name, ts, body, run_id, task,
		disposition, reason, incident_id) VALUES ('$ev1:x', '!room:matrix.example', 'ops-qa', 'qa', '@a:x', ?, ?, ?, 7, 'chat-triage',
		'problem', ?, 3)`, "N "+fixHostile, now-120, "help "+fixHostile, "R "+fixHostile)
	mustExec(t, db, `INSERT INTO rca_reports (id, incident_id, title, path, created, status, content, source)
		VALUES (11, 3, ?, 'reports/rca/x.md', ?, 'open', ?, 'rca-deep')`, "RCA "+fixHostile, now-100, rca)
	return d, db
}

func fixSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func fixNoRawHostile(t *testing.T, name, page string) {
	t.Helper()
	if strings.Contains(page, "<script>") || strings.Contains(page, "<img src=x") {
		t.Errorf("%s: unescaped hostile text", name)
	}
}

func TestFixDownload(t *testing.T) {
	fixTestEnv(t)
	st, body, hdr := FixDownloadResponse(py.NewQS("run", "7", "idx", "0"))
	if st != 200 || !strings.HasPrefix(string(body), "restart api\n") || !strings.HasSuffix(string(body), "\n") ||
		hdr[0][1] != `attachment; filename="argus-fix-problem-7-0.md"` {
		t.Fatal(st, string(body), hdr)
	}
	if st, _, _ := FixDownloadResponse(py.NewQS("id", "11")); st != 200 {
		t.Fatal("rca download")
	}
	if st, _, _ := FixDownloadResponse(py.NewQS("incident", "3")); st != 200 {
		t.Fatal("incident download")
	}
	for _, bad := range []py.QS{py.NewQS("id", "1x"), py.NewQS("id", "1234567890"), py.NewQS("run", "7"),
		py.NewQS("run", "7", "idx", "1234"), py.NewQS("task", "../etc"), py.NewQS("task", "abc-1"), py.NewQS("id", "99"), {}} {
		if st, b, _ := FixDownloadResponse(bad); st != 404 || b != nil {
			t.Errorf("%v: %d", bad.Pairs(), st)
		}
	}
}

func readOnlyFile(t *testing.T, dir string) (string, map[string]any) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 1 {
		t.Fatalf("want one request file in %s, got %v", dir, names)
	}
	data, _ := os.ReadFile(filepath.Join(dir, names[0]))
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return string(data), m
}

func TestIncidentsPostAction(t *testing.T) {
	fixTestEnv(t)
	c := NewCtx(fixViewer, "/")
	cases := []struct {
		form py.QS
		sess *Session
		code int
	}{{py.NewQS("id", "3x", "action", "ack"), fixViewer, 400}, {py.NewQS("id", "3", "action", "drop"), fixViewer, 400},
		{py.NewQS("id", "3", "action", "resolve"), fixViewer, 403}, {py.NewQS("id", "3", "action", "reopen"), fixViewer, 403},
		{py.NewQS("id", "99", "action", "ack"), fixViewer, 404}}
	for _, x := range cases {
		if code, _ := IncidentsPostAction(c, x.form, x.sess); code != x.code {
			t.Errorf("%v: %d want %d", x.form.Pairs(), code, x.code)
		}
	}
	code, loc := IncidentsPostAction(c, py.NewQS("id", "3", "action", "resolve", "note", "  a\n b  "+strings.Repeat("x", 400)), fixAdmin)
	if code != 303 || loc != "/incidents/view?id=3&sent=resolve" {
		t.Fatal(code, loc)
	}
	raw, m := readOnlyFile(t, filepath.Join(SET_IN, "incident-actions"))
	if !strings.HasPrefix(raw, `{"id": 3, "action": "resolve", "by": "boss", "role": "admin", "note": "a b xx`) ||
		len([]rune(m["note"].(string))) != 300 {
		t.Fatal(raw)
	}
}

func TestRcaPostDeep(t *testing.T) {
	fixTestEnv(t)
	c := NewCtx(fixAdmin, "/")
	if code, _ := RcaPostDeep(c, py.NewQS("id", "3"), fixViewer); code != 403 {
		t.Fatal("viewer")
	}
	if code, _ := RcaPostDeep(c, py.NewQS("id", "1234567890"), fixAdmin); code != 400 {
		t.Fatal("long id")
	}
	if code, _ := RcaPostDeep(c, py.NewQS("id", "9"), fixAdmin); code != 404 {
		t.Fatal("missing")
	}
	if code, loc := RcaPostDeep(c, py.NewQS("id", "3"), fixAdmin); code != 303 || loc != "/incidents/view?id=3&sent=deep" {
		t.Fatal(code, loc)
	}
	raw, _ := readOnlyFile(t, filepath.Join(SET_IN, "requests"))
	if !strings.HasPrefix(raw, `{"by": "boss", "at": `) {
		t.Fatal(raw)
	}
	if code, msg := RcaPostDeep(c, py.NewQS("id", "3"), fixAdmin); code != 429 || !strings.Contains(msg, "requested") {
		t.Fatal("pending request", code, msg)
	}
	os.Remove(filepath.Join(SET_IN, "requests", "rca-deep-3.req"))
	os.WriteFile(filepath.Join(SET_OUT, "run-requests.json"), []byte(`{"rca_day": "`+qaToday()+`", "rca_count": 6}`), 0o644)
	if code, msg := RcaPostDeep(c, py.NewQS("id", "3"), fixAdmin); code != 429 || !strings.Contains(msg, "Daily limit") {
		t.Fatal("daily limit", code, msg)
	}
	started := I2S(NowInt() - 60)
	os.WriteFile(filepath.Join(SET_OUT, "run-requests.json"), []byte(`{"rca": {"3": {"started": `+started+`, "by": "auto"}}}`), 0o644)
	c2 := NewCtx(fixAdmin, "/")
	if code, msg := RcaPostDeep(c2, py.NewQS("id", "3"), fixAdmin); code != 429 || !strings.Contains(msg, "(automatic) running since") || c2.Refresh != true {
		t.Fatal("running", code, msg)
	}
}

func fixScope(t *testing.T) {
	os.WriteFile(filepath.Join(SET_OUT, "fix-scope.json"), []byte(`{"envs": ["develop"], "set_up": true, "error": "",
		"hosts": {"develop": ["dev-stage3-fix", "not an alias"]}}`), 0o644)
}

func TestFixerReview(t *testing.T) {
	fixTestEnv(t)
	c := NewCtx(fixAdmin, "/")
	if code, _, _ := FixerPostReview(c, py.NewQS("run", "7", "idx", "0"), fixViewer); code != 404 {
		t.Fatal("viewer")
	}
	// no scope published yet: not available
	if code, page, _ := FixerPostReview(c, py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", "x", "host", "dev-stage3-fix"), fixAdmin); code != 400 ||
		!strings.Contains(page, "Take action is not available for develop") {
		t.Fatal("no scope")
	}
	fixScope(t)
	try := func(form py.QS, want string) {
		t.Helper()
		code, page, loc := FixerPostReview(c, form, fixAdmin)
		if code != 400 || loc != "" || !strings.Contains(page, want) {
			t.Fatalf("%v: %d %q", form.Pairs(), code, loc)
		}
		fixNoRawHostile(t, "review", page)
	}
	try(py.NewQS("run", "7", "idx", "0", "prompt", "x", "host", "dev-stage3-fix"), `Tick "I reviewed this prompt" first.`)
	try(py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", " ", "host", "dev-stage3-fix"), "The prompt is empty.")
	try(py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", strings.Repeat("x", 17000), "host", "dev-stage3-fix"), "larger than 16 KB")
	try(py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", "fix prod-shop-01 now", "host", "dev-stage3-fix"),
		"The prompt names production: prod-shop-01.")
	try(py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", "x", "host", "pre-ai-01-fix"), "Pick at least one target host")
	code, _, loc := FixerPostReview(c, py.NewQS("run", "7", "idx", "0", "reviewed", "1", "prompt", "restart\r\napi", "host", "dev-stage3-fix",
		"host", "dev-stage3-fix"), fixAdmin)
	if code != 303 || !FixerIDRE.MatchString(strings.TrimPrefix(loc, "/admin/fix/run?id=")) {
		t.Fatal(code, loc)
	}
	raw, m := readOnlyFile(t, filepath.Join(SET_IN, "fix-requests"))
	if m["prompt"] != "restart\napi" || m["reviewed"] != true || len(m["hosts"].([]any)) != 1 ||
		m["prompt_sha256"] != "1c5d6aa6a7fdd6d1af0b98d8e2b4d38b4d1b8e1ccf2a9ae5dbe4b2bd0bca7d32"[:0]+fixSHA("restart\napi") {
		t.Fatal(raw)
	}
	if !strings.Contains(raw, `"problem": {"run": 7, "idx": 0, "title": "T <script>`) {
		t.Fatal("problem block", raw)
	}
	// the run page of the queued request refreshes itself

}

func TestFixerProblemActions(t *testing.T) {
	fixTestEnv(t)
	fixScope(t)
	admin, viewer := NewCtx(fixAdmin, "/"), NewCtx(fixViewer, "/")
	p := Row{"environment": "develop", "fix_prompt": "x", "run_id": int64(7), "idx": int64(0)}
	if got := FixerProblemActions(admin, p); !strings.Contains(got, `href="/admin/fix/review?run=7&amp;idx=0"`) {
		t.Fatal(got)
	}
	if FixerProblemActions(viewer, p) != "" {
		t.Fatal("viewer sees Take action")
	}
	if got := FixerProblemActions(admin, Row{"environment": "release", "fix_prompt": "x"}); !strings.Contains(got, "Argus never runs this") {
		t.Fatal("release")
	}
	if FixerProblemActions(admin, Row{"environment": "develop", "fix_prompt": ""}) != "" {
		t.Fatal("no prompt")
	}
}

func TestQAPermalinkAndSources(t *testing.T) {
	if got := QAPermalink("!abc:matrix.example", "$ev/1:x"); got != "https://matrix.to/#/!abc:matrix.example/$ev%2F1:x?via=matrix.example" {
		t.Fatal(got)
	}
	for _, bad := range [][2]string{{"room", "$e"}, {"!a:b", "e"}, {"!a b:c", "$e"}, {"!a:b", "$e\"><x"}} {
		if QAPermalink(bad[0], bad[1]) != "" {
			t.Errorf("accepted %v", bad)
		}
	}
	if s := QAIncSources(Row{"sources": `["kuma","bogus","qa"]`, "dc": "x"}); strings.Join(s, ",") != "kuma,qa" {
		t.Fatal(s)
	}
	if s := QAIncSources(Row{"sources": "not json", "dc": "east"}); s[0] != "alertmanager" {
		t.Fatal(s)
	}
	if got := qaShownBody("> <@a:x> quoted\n> more\n\nreal answer"); got != "real answer" {
		t.Fatal(got)
	}
}
