package app

// Proposed tasks as a queue page: GET search / facets / sort / pagination in SQL, the contextual bulk bar (a GET form,
// no JavaScript), the server-rendered bulk confirm step, and the unchanged POST contract of /proposals/act.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const proposalsTestSchema = `CREATE TABLE IF NOT EXISTS proposals (
  id INTEGER PRIMARY KEY, path TEXT NOT NULL, source_kind TEXT NOT NULL, source_id TEXT NOT NULL, source_url TEXT,
  title TEXT NOT NULL, description TEXT, evidence TEXT, fix_prompt TEXT, env TEXT, host TEXT, priority TEXT,
  severity TEXT, sev_rank INTEGER DEFAULT 0, labels TEXT DEFAULT '[]', assignee_suggestion TEXT, why TEXT,
  board_col INTEGER, hash TEXT, state TEXT NOT NULL DEFAULT 'pending',
  snooze_until INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, refreshed INTEGER DEFAULT 0,
  decided_at INTEGER, decided_by TEXT, reason TEXT, task_id INTEGER)`

// proposalsSeed: 30 waiting (ids 1-30), 2 snoozed (31-32), 1 accepted (33). Fictional data only.
func proposalsSeed(t *testing.T, e *boardTestEnv) {
	t.Helper()
	if _, err := e.board.Exec(proposalsTestSchema); err != nil {
		t.Fatal(err)
	}
	sevs := []struct {
		s string
		r int
	}{{"critical", 4}, {"high", 3}, {"medium", 2}, {"low", 1}, {"", 0}}
	paths := []string{"qa", "cve", "cicd"}
	envs := []string{"release", "develop", ""}
	now := NowInt()
	ins := func(id int, state, title string, sev string, rank int, path, env string, created int64) {
		if _, err := e.board.Exec(`INSERT INTO proposals (id, path, source_kind, source_id, title, env, host, severity, sev_rank, why, state,
			created_at, updated_at, decided_at, snooze_until) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, path, path, fmt.Sprint(100+id), title, env, "app-0"+fmt.Sprint(id%9), sev, rank, "matches the "+path+" intake rule", state,
			created, created, map[bool]any{true: now, false: nil}[state == "accepted"], map[bool]any{true: now + 3600, false: nil}[state == "snoozed"]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 30; i++ {
		sv := sevs[i%5]
		ins(i, "pending", fmt.Sprintf("Fix item %02d on shop.northwind.example", i), sv.s, sv.r, paths[i%3], envs[i%3], now-int64(i)*3600)
	}
	e.board.Exec(`UPDATE proposals SET title='Rebuild analytics-matomo on PHP 8.3' WHERE id=7`)
	e.board.Exec(`UPDATE proposals SET created_at=? WHERE id=12`, now-13*86400) // the oldest: expires within 48 h
	ins(31, "snoozed", "Snoozed one", "high", 3, "qa", "release", now-7200)
	ins(32, "snoozed", "Snoozed two", "low", 1, "qa", "release", now-7200)
	ins(33, "accepted", "Already accepted", "high", 3, "cve", "release", now-86400)
}

var proposalRowRe = regexp.MustCompile(`<li id="p-(\d+)">`)

func proposalRowIDs(page string) []string {
	var out []string
	for _, m := range proposalRowRe.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestProposalsQueueBulkConfirm(t *testing.T) {
	e := boardSetup(t)
	proposalsSeed(t, e)

	// the confirm form posts the same request file the old bulk bar did
	code, loc := ProposalsPost(&Ctx{Sess: e.admin, Path: "/proposals/act"}, "/proposals/act",
		py.ParseQS("csrf="+e.admin.CSRF+"&return=%2Fproposals%3Fsev%3Dhigh&action=reject&id=6&id=1&reason=dup"), e.admin)
	if code != 303 || !strings.HasPrefix(loc, "/proposals?sev=high&req=") {
		t.Fatal("post", code, loc)
	}
	req, _ := boardReadRequest(t, loc)
	if req.Get("kind") != "proposal" || req.Get("action") != "reject" || req.Get("reason") != "dup" || len(py.AsList(req.Get("ids"))) != 2 {
		t.Fatal("payload", py.Dumps(req, -1))
	}
}
