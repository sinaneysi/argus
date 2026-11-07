package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

// srcRevSeed replaces the demo's routes with a small Northwind queue. Synthetic data only.
func srcRevSeed(t *testing.T) {
	t.Helper()
	catDemoDB(t)
	db, err := sql.Open("sqlite", DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"DELETE FROM source_routes", "DELETE FROM source_unmapped"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ins := `INSERT INTO source_routes (id, service, source, kind, locator, confidence, origin, status, evidence, stale, updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,1)`
	for _, r := range [][]any{
		{1, "shop/orders", "gitlab/main", "project", "northwind/shop/orders", 1.0, "discovered", "proposed", "exact repository path", 0},
		{2, "shop/billing", "gitlab/main", "project", "northwind/shop/billing", 0.95, "discovered", "proposed", "exact repository path", 0},
		{3, "web/old", "gitlab/main", "project", "northwind/web/old", 1.0, "discovered", "proposed", "exact repository path", 1},
		{4, "web/weak", "grafana/east", "loki", `{app="weak"}`, 0.75, "discovered", "proposed", "label matches", 0},
		{5, "web/front", "grafana/east", "loki", `{app="front"}`, 0.7, "learned", "proposed", "run 20261103-abc: fan-out found it", 0},
		{6, "web/done", "gitlab/main", "project", "northwind/web/done", 1.0, "discovered", "confirmed", "exact repository path", 0},
		{7, "web/no", "gitlab/main", "project", "northwind/web/no", 1.0, "discovered", "rejected", "exact repository path", 0},
	} {
		if _, err := db.Exec(ins, r...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO source_unmapped (source, kind, key, label, note) VALUES ('gitlab/main','project','northwind/tools/scratch','',''),
	  ('grafana/east','prom-job','node','','ambiguous: fits a/x, b/x')`); err != nil {
		t.Fatal(err)
	}
}

func srcRevIDs(t *testing.T, m map[string]any) string {
	t.Helper()
	var out []string
	for _, x := range m["items"].([]any) {
		out = append(out, fmt.Sprint(x.(map[string]any)["id"]))
	}
	return strings.Join(out, " ")
}

func TestSourcesReviewQueue(t *testing.T) {
	e, ck, _ := srcSetup(t)
	srcRevSeed(t)
	// learned first, then strong by confidence; stale, weak, decided routes stay out
	code, m := apiReq(t, e, "GET", srcRevAPI, ck, "", nil)
	if code != 200 || srcRevIDs(t, m) != "5 1 2" || m["total"] != float64(3) || m["capped"] != float64(0) {
		t.Fatalf("queue: %d %v", code, m)
	}
	first := m["items"].([]any)[0].(map[string]any)
	if first["why"] != "learned" || first["run_id"] != "20261103-abc" {
		t.Fatalf("learned item: %v", first)
	}
	for q, want := range map[string]string{"?why=learned": "5", "?why=discovered": "1 2", "?type=grafana": "5", "?q=billing": "2"} {
		if _, m := apiReq(t, e, "GET", srcRevAPI+q, ck, "", nil); srcRevIDs(t, m) != want {
			t.Fatalf("%s: %v", q, m)
		}
	}
	if code, _ := apiReq(t, e, "GET", srcRevAPI+"?why=nope", ck, "", nil); code != 400 {
		t.Fatal("bad filter")
	}
	// detail: the evidence and who found it; an item that left the queue is 404
	if code, m = apiReq(t, e, "GET", srcRevAPI+"/item?id=5", ck, "", nil); code != 200 || m["found_by"] != "an agent run's fan-out" {
		t.Fatalf("item: %d %v", code, m)
	}
	if code, m = apiReq(t, e, "GET", srcRevAPI+"/item?id=1", ck, "", nil); code != 200 || m["found_by"] != "discovery" {
		t.Fatalf("item: %d %v", code, m)
	}
	for _, q := range []string{"?id=3", "?id=6", "?id=999"} {
		if code, _ := apiReq(t, e, "GET", srcRevAPI+"/item"+q, ck, "", nil); code != 404 {
			t.Fatalf("%s: %d", q, code)
		}
	}
	if code, _ := apiReq(t, e, "GET", srcRevAPI+"/item?id=x", ck, "", nil); code != 400 {
		t.Fatal("bad id")
	}
}

func TestSourcesReviewCap(t *testing.T) {
	e, ck, _ := srcSetup(t)
	srcRevSeed(t)
	db, _ := sql.Open("sqlite", DB)
	defer db.Close()
	for i := 0; i < srcRevCap+3; i++ {
		if _, err := db.Exec(`INSERT INTO source_routes (service, source, kind, locator, confidence, origin, status, stale) VALUES
		  (?, 'gitlab/main', 'project', ?, 0.91, 'discovered', 'proposed', 0)`, fmt.Sprintf("bulk/s%02d", i), fmt.Sprintf("northwind/bulk/%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	_, m := apiReq(t, e, "GET", srcRevAPI, ck, "", nil)
	if m["total"] != float64(srcRevCap+1) || m["capped"] != float64(5) { // 1 learned + the cap; 2 seeded + cap+3 strong
		t.Fatalf("cap: %v %v", m["total"], m["capped"])
	}
	if !strings.HasPrefix(srcRevIDs(t, m), "5 1 2 ") {
		t.Fatal("the cap keeps the strongest")
	}
}

func TestSourcesReviewAct(t *testing.T) {
	e, ck, csrf := srcSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "source-actions"), 0o755)
	srcRevSeed(t)
	h := map[string]string{"X-CSRF-Token": csrf}
	read := func() map[string]any {
		t.Helper()
		f := catRequests(t)
		if len(f) != 1 {
			t.Fatalf("requests: %v", f)
		}
		b, _ := os.ReadFile(f[0])
		os.Remove(f[0])
		var x map[string]any
		json.Unmarshal(b, &x)
		return x
	}
	if code, m := apiReq(t, e, "POST", srcRevAct, ck, `{"action": "confirm", "id": 5}`, h); code != 202 || m["state"] != "queued" {
		t.Fatalf("confirm: %d %v", code, m)
	}
	if x := read(); x["action"] != "confirm" || x["id"] != float64(5) || x["user"] != "admin" || x["role"] != "admin" {
		t.Fatalf("request: %v", x)
	}
	apiReq(t, e, "POST", srcRevAct, ck, `{"action": "reject", "id": 5, "reason": "  an   archived fork "}`, h)
	if x := read(); x["action"] != "reject" || x["reason"] != "an archived fork" {
		t.Fatalf("reject: %v", x)
	}
	apiReq(t, e, "POST", srcRevAct, ck, `{"action": "edit", "id": 5, "locator": "{app=\"front2\"}", "note": "moved"}`, h)
	if x := read(); x["action"] != "edit" || x["locator"] != `{app="front2"}` || x["note"] != "moved" {
		t.Fatalf("edit: %v", x)
	}
	// refusals write nothing
	for _, c := range []struct {
		body string
		hdr  map[string]string
		code int
	}{
		{`{"action": "drop", "id": 5}`, h, 400}, {`{"id": 5}`, h, 400}, {`{"action": "discover", "source": "gitlab/main"}`, h, 400},
		{`{"action": "confirm", "id": "x"}`, h, 400}, {`{"action": "reject", "id": 5, "reason": "a|b"}`, h, 400},
		{`{"action": "reject", "id": 5, "reason": "` + strings.Repeat("x", 301) + `"}`, h, 400},
		{`{"action": "edit", "id": 5, "locator": ""}`, h, 400},
		{`{"action": "confirm", "id": 5}`, nil, 403}, {`{"action": "confirm", "id": 5}`, map[string]string{"X-CSRF-Token": "nope"}, 403},
	} {
		if code, m := apiReq(t, e, "POST", srcRevAct, ck, c.body, c.hdr); code != c.code {
			t.Fatalf("%s: %d %v", c.body, code, m)
		}
	}
	if code, _ := apiReq(t, e, "POST", srcRevAct, nil, `{"action": "confirm", "id": 5}`, nil); code != 401 {
		t.Fatal("anonymous")
	}
	// a viewer reads and cannot act
	vtok, vcsrf := e.login("view.er")
	vck := map[string]string{"opsr_session": vtok}
	if code, _ := apiReq(t, e, "GET", srcRevAPI, vck, "", nil); code != 200 {
		t.Fatal("viewer read")
	}
	if code, _ := apiReq(t, e, "POST", srcRevAct, vck, `{"action": "confirm", "id": 5}`, map[string]string{"X-CSRF-Token": vcsrf}); code != 403 {
		t.Fatal("viewer act")
	}
	// an admin without recent 2FA
	ntok, ncsrf := e.loginNoMFA("admin")
	old := auth.MFAActions
	auth.MFAActions = true
	defer func() { auth.MFAActions = old }()
	if code, _ := apiReq(t, e, "POST", srcRevAct, map[string]string{"opsr_session": ntok}, `{"action": "confirm", "id": 5}`, map[string]string{"X-CSRF-Token": ncsrf}); code != 403 {
		t.Fatal("no 2FA")
	}
	// the demo refuses
	oldDemo := DEMO
	DEMO = true
	defer func() { DEMO = oldDemo }()
	if code, m := apiReq(t, e, "POST", srcRevAct, ck, `{"action": "confirm", "id": 5}`, h); code != 403 || !strings.Contains(fmt.Sprint(m["error"]), "demo") {
		t.Fatalf("demo: %d %v", code, m)
	}
	if n := len(catRequests(t)); n != 0 {
		t.Fatalf("%d refused request(s) wrote a file", n)
	}
}

func TestSourcesMetrics(t *testing.T) {
	e, ck, _ := srcSetup(t)
	srcRevSeed(t)
	old := srcFindLog
	srcFindLog = filepath.Join(t.TempDir(), "sources-find.log")
	defer func() { srcFindLog = old }()
	// nothing asked yet
	code, m := apiReq(t, e, "GET", srcMetricsAPI, ck, "", nil)
	finds := m["finds"].(map[string]any)
	if code != 200 || finds["log"] != "missing" || finds["finds"] != float64(0) {
		t.Fatalf("no log: %d %v", code, m)
	}
	routes := fmt.Sprint(m["routes"])
	if !strings.Contains(routes, "confirmed:1") || !strings.Contains(routes, "rejected:1") || !strings.Contains(routes, "type:gitlab") {
		t.Fatalf("routes: %s", routes)
	}
	un := m["unmapped"].(map[string]any)
	top := un["top"].([]any)
	if un["total"] != float64(2) || top[0].(map[string]any)["key"] != "node" {
		t.Fatalf("unmapped: %v", un)
	}
	now := NowInt()
	lines := []string{
		fmt.Sprintf(`{"at": %d, "service": ["shop/orders"], "chosen": ["gitlab:gitlab/main:confirmed", "loki:grafana/east:proposed"], "fanout": {}, "learned": 0}`, now-60),
		fmt.Sprintf(`{"at": %d, "service": ["web/front"], "chosen": ["loki:grafana/east:proposed"], "fanout": {}, "learned": 0}`, now-60),
		fmt.Sprintf(`{"at": %d, "service": ["web/front"], "chosen": [], "fanout": {"web/front:gitlab": {"hits": 2}}, "learned": 1}`, now-60),
		fmt.Sprintf(`{"at": %d, "service": [], "chosen": [], "fanout": {}, "learned": 0}`, now-60),
		fmt.Sprintf(`{"at": %d, "service": ["shop/old"], "chosen": ["gitlab:gitlab/main:confirmed"], "fanout": {}, "learned": 0}`, now-8*86400), // too old
		`not json`,
	}
	os.WriteFile(srcFindLog, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	_, m = apiReq(t, e, "GET", srcMetricsAPI, ck, "", nil)
	finds = m["finds"].(map[string]any)
	for k, want := range map[string]float64{"finds": 4, "confirmed": 1, "proposed": 1, "fanout": 1, "none": 1, "fanout_searches": 1, "fanout_hits": 2, "learned": 1, "services": 2} {
		if finds[k] != want {
			t.Fatalf("%s: %v, want %v (%v)", k, finds[k], want, finds)
		}
	}
	// an unreadable log is said, never shown as zero finds
	os.Remove(srcFindLog)
	os.Mkdir(srcFindLog, 0o700)
	_, m = apiReq(t, e, "GET", srcMetricsAPI, ck, "", nil)
	if f := m["finds"].(map[string]any); f["log"] != "unreadable" {
		t.Fatalf("directory: %v", f)
	}
}
