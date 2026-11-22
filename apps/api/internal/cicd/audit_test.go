package cicd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testdata/vars-parity.json: the earlier installation's variable checks on synthetic metadata. Listed differences:
// the target prefixes, group secret keys and production markers are configuration (CICD_TARGET_PREFIXES,
// CICD_GROUP_SECRET_KEYS, CICD_PROD_MARKERS); the detail texts are neutral.
func TestVarsParity(t *testing.T) {
	b, err := os.ReadFile("testdata/vars-parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Rows         []MetaRow
		Envs, Sample []string
		Check        [][]string
		Routing      [][]string
		RoutingEmpty [][]string `json:"routing_empty"`
		Groups       map[string][]MetaRow
		GroupChecks  [][]string `json:"group_checks"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{Opt: Options{TargetPrefixes: map[string][]string{"develop": {"develop"}, "pre-release": {"pre-", "pre_"}, "release": {"main-", "main_"}},
		GroupSecretKeys: []string{"SSH_PRIVATE_KEY*", "DEPLOYMENT_REPO_ACCESS_TOKEN"}}}
	flat := func(fs []VarFinding) [][]string {
		out := [][]string{}
		for _, f := range fs {
			out = append(out, []string{f.Rule, f.Env, f.Severity, f.Title, f.Key})
		}
		return out
	}
	norm := func(x [][]string) [][]string {
		if x == nil {
			return [][]string{}
		}
		return x
	}
	if got := flat(CheckProject(p.Rows, p.Envs, p.Sample)); !reflect.DeepEqual(got, norm(p.Check)) {
		t.Fatalf("check\n%v\nwant\n%v", got, p.Check)
	}
	if got := flat(w.RoutingChecks(p.Rows, p.Envs, map[string]bool{"develop": true, "pre-shop": true, "main-shop": true})); !reflect.DeepEqual(got, norm(p.Routing)) {
		t.Fatalf("routing\n%v\nwant\n%v", got, p.Routing)
	}
	if got := flat(w.RoutingChecks([]MetaRow{{Key: "EXECUTOR", Scope: "*"}}, p.Envs, nil)); !reflect.DeepEqual(got, norm(p.RoutingEmpty)) {
		t.Fatalf("routing empty\n%v\nwant\n%v", got, p.RoutingEmpty)
	}
	var gc [][]string
	for _, g := range w.GroupChecks(p.Groups) {
		gc = append(gc, []string{g.Group, g.Key, g.Scope, g.Severity})
	}
	if !reflect.DeepEqual(gc, p.GroupChecks) {
		t.Fatalf("groups %v want %v", gc, p.GroupChecks)
	}
}

func TestDriftParity(t *testing.T) {
	b, _ := os.ReadFile("testdata/parity.json")
	var p struct {
		Drift struct {
			Compare map[string]any
			Titles  []string
		}
	}
	json.Unmarshal(b, &p)
	var got []string
	for _, c := range DriftCommits(p.Drift.Compare, nil) {
		got = append(got, c["title"])
	}
	if !reflect.DeepEqual(got, p.Drift.Titles) {
		t.Fatalf("%v want %v", got, p.Drift.Titles)
	}
	allow := regexp.MustCompile(`^feat: x$`)
	if len(DriftCommits(p.Drift.Compare, allow)) != 0 {
		t.Fatal("an allowed commit counted as drift")
	}
}

func TestVariablesDropValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"key": "DOTENV", "value": "DB_URL=mongodb://prod-db-01:27017/x\nPORT=8080\n", "environment_scope": "develop", "variable_type": "file"},
			{"key": "TARGET_HOSTS", "value": "stage-shop", "environment_scope": "develop"},
			{"key": "API_TOKEN", "value": "supersecretvalue123", "environment_scope": "*", "masked": true}})
	}))
	defer srv.Close()
	g := &GitLab{Base: srv.URL, Token: "t", Budget: 5}
	ms := []Marker{{"prod-host", regexp.MustCompile(`(?i)\bprod-[a-z0-9-]+`)}}
	rs, err := g.Variables(context.Background(), "projects/1/variables", ms)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rs)
	for _, bad := range []string{"mongodb", "supersecretvalue123", "8080"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("value %q kept: %s", bad, b)
		}
	}
	if rs[0].DotProd[0] != "DB_URL" || !reflect.DeepEqual(rs[0].Dotenv, []string{"DB_URL", "PORT"}) || rs[1].Value != "stage-shop" || rs[2].Value != "" {
		t.Fatalf("%+v", rs)
	}
	dir := t.TempDir()
	if err := WriteMeta(filepath.Join(dir, "m", "meta.json"), Meta{Groups: map[string][]MetaRow{"g": {{Key: "K", Scope: "*", Value: "glpat-abcdefghijkl"}}}}); err != ErrSecretInMeta {
		t.Fatalf("a secret-looking meta was written: %v", err)
	}
}

// the audit against a fake GitLab: a missing branch, release drift, the seed of a branch without a recorded pipeline,
// no release deploy in 30 days; a second run with the branch present resolves its finding.
func TestAuditFlow(t *testing.T) {
	db := testDB(t)
	now := time.Now().Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/")
		send := func(v any) { json.NewEncoder(w).Encode(v) }
		switch {
		case p == "projects/5/repository/merge_base":
			send(map[string]any{"id": "base"})
		case p == "projects/5/repository/compare":
			send(map[string]any{"commits": []map[string]any{{"id": strings.Repeat("e", 40), "short_id": "eeee", "title": "feat: direct to release", "parent_ids": []string{"1"}, "author_name": "dev"}}})
		case p == "projects/5/pipelines" && r.URL.Query().Get("ref") == "develop" && r.URL.Query().Get("order_by") == "id":
			send([]map[string]any{{"id": 41, "status": "skipped"}, {"id": 40, "status": "success", "sha": "d1", "created_at": ISO(now - 3600), "updated_at": ISO(now - 3500)}})
		case p == "projects/5/pipelines":
			send([]map[string]any{{"id": 40, "created_at": ISO(now - 3600)}})
		default:
			send([]any{})
		}
	}))
	defer srv.Close()
	for _, q := range []string{
		"INSERT INTO cicd_projects (id, path, grp, default_branch, kind, consumer, includes, protected) VALUES (5, 'grp/app', 'grp', 'develop', 'app', 1, '{}', '{}')",
		"INSERT INTO cicd_branches VALUES (5, 'develop', 1, 'd1', 1, 'x'), (5, 'pre-release', 0, NULL, NULL, ''), (5, 'release', 1, 'r1', 1, 'y')"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		w, err := New(context.Background(), Options{DB: db, GitLab: &GitLab{Base: srv.URL, Token: "t", Budget: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Audit(); err != nil {
			t.Fatal(err)
		}
		w.Close()
	}
	run()
	got := map[string]string{}
	rs, _ := db.Query("SELECT rule, status FROM cicd_findings")
	for rs.Next() {
		var a, b string
		rs.Scan(&a, &b)
		got[a] = b
	}
	rs.Close()
	for _, want := range []string{"missing", "no-deploy-30d", "unprotected"} {
		if got[want] != "open" {
			t.Fatalf("finding %s: %v", want, got)
		}
	}
	var seeded int
	db.QueryRow("SELECT count(*) FROM cicd_pipelines WHERE id=40 AND triaged=1").Scan(&seeded)
	if seeded != 1 {
		t.Fatal("develop was not seeded with its newest pipeline that ran")
	}
	db.Exec("UPDATE cicd_branches SET present=1, sha='p1' WHERE branch='pre-release'")
	run()
	var st string
	db.QueryRow("SELECT status FROM cicd_findings WHERE rule='missing'").Scan(&st)
	if st != "resolved" {
		t.Fatalf("missing branch finding is %s after the branch appeared", st)
	}
	var drift int
	db.QueryRow("SELECT count(*) FROM cicd_findings WHERE rule='drift' AND ref='release'").Scan(&drift)
	if drift != 1 {
		t.Fatal("release drift not found")
	}
}
