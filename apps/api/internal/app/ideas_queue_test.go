package app

// Ideas as a queue page: status tabs with counts that honour the filters, GET search / adviser / area / effort facets,
// sort and 25-row pagination in SQL, the old status/persona/area/effort links, explicit action labels, the unchanged
// POST forms (CSRF, reason required). Fictional fixture data only.

import (
	"database/sql"
	"fmt"
	"regexp"
	"testing"
)

var ideaRowRe = regexp.MustCompile(`<li id="i-(\d+)">`)

func ideaRowIDs(page string) []string {
	var out []string
	for _, m := range ideaRowRe.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

// ideasQueueSeed adds ids 10-39: 30 new ideas, alternating advisers, areas and efforts, on top of ideasSeed's five.
func ideasQueueSeed(t *testing.T, e *srvEnv) {
	t.Helper()
	db, err := sql.Open("sqlite", e.dir+"/ops.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := NowInt()
	ps, areas, efforts := []string{"devops", "qa", "noc"}, []string{"ci-cd", "security"}, []string{"L", "M", "S"}
	for i := 10; i < 40; i++ {
		if _, err := db.Exec(`INSERT INTO ideas (id, persona, title, area, problem, value, effort, status, created_at, updated_at)
			VALUES (?,?,?,?,'Seen 4 times on shop.northwind.example','1 h a week',?,'new',?,?)`,
			i, ps[i%3], fmt.Sprintf("Idea %02d for the northwind pipeline", i), areas[i%2], efforts[i%3], now-int64(i)*60, now); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`UPDATE ideas SET title='Cap 100% CPU_spikes alert' WHERE id=11`)
}
