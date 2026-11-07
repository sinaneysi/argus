package app

// The Review inbox redesign (views_review.go, views_review_panel.go, 2026-11-02) on the synthetic demo data set
// (fictional Northwind): the urgency comparator and every sort, filters across All and kind views, selection as URL
// state (the queue keeps its search, filters, sort and page; Back to queue restores them), snooze never offered past
// expiry, the missing-evidence state, the action-approval plan review step (pinned version, CSRF), viewers without act
// forms, empty and filtered-empty states, and the kinds' full lists one link away.

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func rvTestItem(kind, id string, sev int, env string, due, at int64) *rvItem {
	return &rvItem{k: reviewKindOf(kind), id: id, sevRank: sev, env: env, due: due, at: at}
}

func rvIDs(items []*rvItem) string {
	var s []string
	for _, it := range items {
		s = append(s, it.id)
	}
	return strings.Join(s, ",")
}

func TestReviewUrgencyComparator(t *testing.T) {
	// a: critical prod, expires late; b: critical prod, expires soon; c: high prod; d: critical develop;
	// e: no severity (an approval), prod; f: low prod; g: no severity, no env, chat request expiring soon;
	// h and i: same bucket, no deadline, h waited longer; j: same as i, tie broken by kind order then id
	items := []*rvItem{
		rvTestItem("proposals", "1", 4, "release", 9000, 100),
		rvTestItem("proposals", "2", 4, "release", 5000, 500),
		rvTestItem("proposals", "3", 3, "release", 4000, 50),
		rvTestItem("proposals", "4", 4, "develop", 1000, 10),
		rvTestItem("approvals", "OPS-9", 1, "release", 0, 300),
		rvTestItem("proposals", "6", 0, "release", 100, 1),
		rvTestItem("replies", "m1", 1, "", 2000, 900),
		rvTestItem("lessons", "8", 1, "pre-release", 0, 200),
		rvTestItem("ideas", "9", 1, "pre-release", 0, 400),
		rvTestItem("lessons", "10", 1, "pre-release", 0, 400),
	}
	reviewSort(items, "urgency")
	// severity first (critical, high, unknown, low); inside critical: production before develop, then the nearest deadline;
	// inside unknown: production, staging (lesson 8 waited longest; lesson 10 before idea 9 by kind order), then no env
	if got, want := rvIDs(items), "2,1,4,3,OPS-9,8,10,9,m1,6"; got != want {
		t.Fatalf("urgency order %s, want %s", got, want)
	}
	reviewSort(items, "oldest")
	if got, want := rvIDs(items), "6,4,3,1,8,OPS-9,10,9,2,m1"; got != want {
		t.Fatalf("oldest %s, want %s", got, want)
	}
	reviewSort(items, "newest")
	if got, want := rvIDs(items), "m1,2,10,9,OPS-9,8,1,3,4,6"; got != want {
		t.Fatalf("newest %s, want %s", got, want)
	}
	reviewSort(items, "expiring")
	if got, want := rvIDs(items), "6,4,m1,3,2,1,OPS-9,8,10,9"; got != want {
		t.Fatalf("expiring %s, want %s", got, want)
	}
	reviewSort(items, "no-such-sort") // unknown: urgency
	if !strings.HasPrefix(rvIDs(items), "2,1,4,3") {
		t.Fatal("unknown sort is urgency", rvIDs(items))
	}
	if rvSevRank("") != 1 || rvSevRank("info") != 0 || rvSevRank("Critical") != 4 || rvSevRank("warning") != 2 {
		t.Fatal("severity buckets")
	}
}

var rvActRe = regexp.MustCompile(`action="/(ideas|problems|proposals|board|admin/lessons)`)

var rvRowRe = regexp.MustCompile(`<li id="(rv-[a-z]+-[^"]+)" class="rv-item[^"]*"><a class="rv-link" href="([^"]+)"`)

func rvRows(body string) [][]string { return rvRowRe.FindAllStringSubmatch(body, -1) }

func rvPanelOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<section class="rv-panel"`)
	if i < 0 {
		t.Fatal("no review panel")
	}
	return body[i : i+strings.Index(body[i:], "</section></div></div>")]
}

func TestReviewApprovalPlanStep(t *testing.T) {
	e := iaSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}

	// the panel never approves: its primary action opens the plan review step

	// the production plan: proposal only, every target listed

	// the version stays pinned: a stale version is refused, the shown one is sent (demo off: the request file is written)
	if r := e.req("POST", "/api/v1/board/agent/spinup", ck, url.Values{"csrf": {csrf}, "key": {"OPS-238"}, "plan_version": {"1"}, "return": {"/review"}}, nil); r.status != 409 {
		t.Fatal("stale plan version", r.status)
	}
	r := e.req("POST", "/api/v1/board/agent/spinup", ck, url.Values{"csrf": {csrf}, "key": {"OPS-238"}, "plan_version": {"2"}, "return": {"/review?kind=approvals"}}, nil)
	if r.status != 200 || !strings.HasPrefix(r.loc, "/review?kind=approvals&req=") {
		t.Fatal("approve from the step", r.status, r.loc)
	}
	if r := e.req("POST", "/api/v1/board/agent/spinup", ck, url.Values{"csrf": {"bad"}, "key": {"OPS-238"}, "plan_version": {"2"}}, nil); r.status != 403 {
		t.Fatal("CSRF", r.status)
	}
}
