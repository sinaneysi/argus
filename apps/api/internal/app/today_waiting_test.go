package app

import "testing"

func TestTodayWaitingForHidesWhatAViewerCannotOpen(t *testing.T) {
	list := []map[string]any{{"title": "p", "href": "/review?kind=proposals"}, {"title": "i", "href": "/review?kind=ideas"}, {"title": "x", "href": "/problems/view?id=7"}}
	if got := todayWaitingFor(list, &Session{Role: "admin"}); len(got) != 3 {
		t.Fatalf("admin: %v", got)
	}
	got := todayWaitingFor(list, &Session{Role: "viewer"})
	if len(got) != 2 || got[0]["title"] != "i" {
		t.Fatalf("viewer: %v", got)
	}
}
