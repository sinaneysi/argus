package app

// The Decided tab of Proposed tasks as a queue (search, outcome / severity / source / env facets, 25 rows a page,
// newest decision first) and the task board's filters on the shared facet row (.q-frow). Fictional data only.

import (
	"fmt"
	"regexp"
	"testing"
)

var decidedRowRe = regexp.MustCompile(`<tr><td class="nw" data-l="Decided">`)

func TestProposalsDecidedQueue(t *testing.T) {
	e := boardSetup(t)
	proposalsSeed(t, e) // id 33 is accepted today
	now := NowInt()
	outs := []string{"accepted", "rejected", "expired"}
	for i := 40; i < 70; i++ {
		if _, err := e.board.Exec(`INSERT INTO proposals (id, path, source_kind, source_id, title, env, severity, sev_rank, state,
			created_at, updated_at, decided_at, decided_by, reason) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			i, []string{"qa", "cicd"}[i%2], "qa", fmt.Sprint(i), fmt.Sprintf("Decided item %d on shop.northwind.example", i),
			[]string{"release", "develop"}[i%2], "medium", 2, outs[i%3], now-86400*3, now, now-int64(i)*60, "admin", "dup"); err != nil {
			t.Fatal(err)
		}
	}
	e.board.Exec(`INSERT INTO proposals (id, path, source_kind, source_id, title, state, created_at, updated_at, decided_at)
		VALUES (90, 'qa', 'qa', '90', 'Too old to show', 'rejected', ?, ?, ?)`, now-40*86400, now-40*86400, now-20*86400)
	e.board.Exec(`UPDATE proposals SET title='Rotate 100% of cert_bundle' WHERE id=41`)

	// search: % and _ literal

	// outcome facet, with counts that honour the source facet

	// the outcome facet belongs to Decided only

}
