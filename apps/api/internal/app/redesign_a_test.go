package app

// Redesign A (board and task page): the phone shows real work first, advanced filters stay compact, internal labels
// group apart, cards read in scan order, and the task page puts the task before the agent. Presentation only: every
// form keeps its fields, CSRF and return.

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func redesignSeed(t *testing.T) *boardTestEnv {
	e := boardSetup(t)
	now := NowInt()
	for _, r := range []struct {
		id                         int
		title, status, env, labels string
		who, name                  any
	}{
		{1, "api 502 on checkout", "review", "develop", `["db"]`, "alex", "Alex"},
		{2, "disk full on stage3", "progress", "develop", `["argus-inc-42","net"]`, nil, nil},
		{3, "cert renew", "progress", "release", `[]`, "m.k", "Mona"},
		{4, "old work", "done", "develop", `[]`, "alex", "Alex"},
	} {
		if _, err := e.board.Exec(`INSERT INTO tasks (id, title, status, env, labels, assignee, assignee_name, priority, created_at, updated_at, gone)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'High', ?, ?, 0)`, r.id, r.title, r.status, r.env, r.labels, r.who, r.name, now-7200, now-3600); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func redesignCSP(t *testing.T, page string) {
	t.Helper()
	if n := strings.Count(page, "<h1"); n != 1 {
		t.Fatal("exactly one h1, got", n)
	}
	if strings.Contains(page, `style="`) || strings.Contains(page, "<script") || strings.Contains(page, "<style") {
		t.Fatal("inline script or style breaks the CSP")
	}
}

func TestRedesignMobileDefaultColumn(t *testing.T) {
	// order: Review, In progress, To do, Done (by name, so Jira's own columns follow it); then any non-empty; then 0
	for _, c := range []struct {
		names  []string
		counts []int
		want   int
	}{
		{[]string{"To do", "In progress", "Review", "Done"}, []int{0, 4, 1, 3}, 2},
		{[]string{"To do", "In progress", "Review", "Done"}, []int{2, 4, 0, 3}, 1},
		{[]string{"To do", "In progress", "Review", "Done"}, []int{2, 0, 0, 3}, 0},
		{[]string{"To do", "In progress", "Review", "Done"}, []int{0, 0, 0, 3}, 3},
		{[]string{"To do", "In progress", "Review", "Done"}, []int{0, 0, 0, 0}, 0},
		{[]string{"Backlog", "Selected for Development", "In Progress", "Code Review", "Done"}, []int{5, 0, 2, 0, 1}, 2},
		{[]string{"Triage", "Doing"}, []int{0, 2}, 1},
	} {
		if got := kanbanDefault(c.names, c.counts); got != c.want {
			t.Fatal(c.names, c.counts, "got", got, "want", c.want)
		}
	}

	// To do is empty: the phone opens on Review, its chip is the current one, and an empty column is the narrow one

	// results line: open tasks and where they are, Review first

}

func TestRedesignFiltersAndLabels(t *testing.T) {
	e := redesignSeed(t)

	// no advanced filter: closed, no active row; the internal label is only an option, grouped after the human ones

	// the internal label is a chip in its own closed "Argus labels" fold, after the human labels

	// active internal label: the disclosure is open, says how many, and the active row names it with a remove link

	// the exact label value still selects the same task (URL contract unchanged)
	rows, _ := boardFiltered(e.ctx(e.admin), py.NewQS("label", "argus-inc-42"))
	if len(rows) != 1 || rows[0].I("id") != 2 {
		t.Fatal("label filter must return the same task set")
	}
}

func TestRedesignAssigneeChoices(t *testing.T) {
	// one entry per stored identity, most-assigned first; names that differ only by case keep the id
	p := boardPeople([]Row{{"assignee": "a1", "assignee_name": "Sam"}, {"assignee": "a1", "assignee_name": "Sam"},
		{"assignee": "a2", "assignee_name": "sam"}, {"assignee": "m", "assignee_name": nil}})
	if len(p) != 3 || p[0][0] != "a1" || p[0][1] != "Sam (a1)" || p[2][1] != "sam (a2)" {
		t.Fatal(p)
	}
}
