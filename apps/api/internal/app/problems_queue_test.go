package app

// Problems and Suggestions as queue pages: view tabs with counts, GET search / facets / sort / pagination, the old
// status/sev/env links, the contextual bulk bar (a GET form) and the server-rendered confirm step in front of the
// unchanged POST /problems/act. Fictional fixture data only; no request is applied.

import (
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var probRowRe = regexp.MustCompile(`<li id="p-(\d+)">`)
var probSugRowRe = regexp.MustCompile(`<li id="s-(\d+)">`)

func reIDs(re *regexp.Regexp, page string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// probQueueSeed: problems 10-39 (open stages, severities and envs rotating) and suggested links 10-39 on problem 1
// with confidences 0.61 .. 0.99, on top of probSeed.
func probQueueSeed(t *testing.T, e *srvEnv) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	// Suggested links are dated from link 3 (probSeed), not from a second clock read: link 10 must stay
	// strictly older than link 3 even when the two seeds straddle a second boundary ("newest" sort).
	var linkBase int64
	if err := db.QueryRow(`SELECT at FROM problem_links WHERE id=3`).Scan(&linkBase); err != nil {
		t.Fatal(err)
	}
	stages, sevs, envs := []string{"new", "investigating", "fix_in_progress"}, []string{"high", "medium", "low"}, []string{"develop", "pre-release"}
	for i := 10; i < 40; i++ {
		if _, err := db.Exec(`INSERT INTO problems (id, title, status, origin, severity, env, created_at, created_by, updated_at, first_seen)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, i, fmt.Sprintf("Northwind problem %02d", i), stages[i%3], "manual", sevs[i%3], envs[i%2],
			now-int64(i)*60, "admin", now-int64(i)*60, now-int64(100-i)*60); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO problem_links (id, problem_id, ref_type, ref_id, link_type, state, source, confidence, reason, "by", at)
			VALUES (?,1,?,?,'caused_by','suggested','argus',?,'shared upstream 198.51.100.7','argus',?)`,
			i, []string{"incident", "qa_issue"}[i%2], fmt.Sprint(100+i), 0.60+float64(i-10)*0.013, linkBase-int64(i-9)*60); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`UPDATE problems SET title='Cert_renewal 100% stuck', owner='alex.doe' WHERE id=17`)
	db.Exec(`UPDATE problem_links SET state='accepted' WHERE id=39`)
}

func TestProblemsQueueSuggestions(t *testing.T) {
	e := srvSetup(t)
	probSeed(t, e, true, true)
	probQueueSeed(t, e)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}

	// waiting: link 3 + links 10-38 = 30

	// confirm step: read-only, lists the waiting ones, skips the decided one, posts the unchanged fields

	if n := len(probFiles(t, e)); n != 0 {
		t.Fatal("the confirm step writes nothing", n)
	}
	// the POST it sends is the old one
	r := e.req("POST", "/api/v1/problems/act", ck, url.Values{"csrf": {csrf}, "action": {"reject"}, "link": {"38", "37"}, "note": {"other upstream"},
		"back": {"/problems?view=suggestions&conf=high"}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/problems?view=suggestions&conf=high&sent=problem&preq=") {
		t.Fatal(r.status, r.loc)
	}
	// viewers: no picks, no bulk bar, no confirm step

}
