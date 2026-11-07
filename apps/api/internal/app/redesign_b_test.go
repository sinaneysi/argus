package app

// Redesign B (Today, Overview, Act-now, Inventory, navigation): one primary next action per Today card with every
// form kept, Act-now limited to two items and a link, the Overview header split into finding and waiting work, the
// inventory table says what is off to the right, and one nav group open at a time. Presentation only.

import (
	"strings"
	"testing"
)

func todayCards(t *testing.T, page string) []string {
	t.Helper()
	i := strings.Index(page, `<ol class="td-list" aria-label="Today's tasks, in order">`)
	if i < 0 {
		t.Fatal("no plan list")
	}
	list := page[i:]
	list = list[:strings.Index(list, "</ol>")+5]
	return strings.Split(list, `<li class="td-item`)[1:]
}

func TestRedesignBProposalsNavCount(t *testing.T) {
	e := boardSetup(t)
	if _, err := e.board.Exec(`CREATE TABLE IF NOT EXISTS proposals (id INTEGER PRIMARY KEY, state TEXT, created_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	day := proposalDayStart()
	for i := 0; i < 5; i++ {
		e.board.Exec(`INSERT INTO proposals (state, created_at) VALUES ('pending', ?)`, day-86400)
	}
	e.board.Exec(`INSERT INTO proposals (state, created_at) VALUES ('accepted', ?)`, day+60)

	e.board.Exec(`INSERT INTO proposals (state, created_at) VALUES ('pending', ?)`, day+60)

}
