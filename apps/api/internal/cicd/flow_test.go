package cicd

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sinaneysi/argus/internal/incidents"
	"github.com/sinaneysi/argus/internal/migrate"
	"github.com/sinaneysi/argus/internal/notify"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ops.db")
	if _, err := migrate.ApplyFile(context.Background(), p, "ops"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeGitLab answers GET only; any other method fails the test.
type fakeGitLab struct {
	t     *testing.T
	mu    sync.Mutex
	pipes []map[string]any
	ci    string
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		f.t.Errorf("write call %s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("PRIVATE-TOKEN") != "fake-token" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/")
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case p == "projects":
		send([]map[string]any{{"id": 5, "path_with_namespace": "grp/search-api", "default_branch": "develop", "last_activity_at": ISO(time.Now().Unix())}})
	case p == "projects/5/repository/branches":
		send([]map[string]any{{"name": "develop", "commit": map[string]any{"id": "b1", "committed_date": "2026-10-21T08:00:00Z", "title": "x"}},
			{"name": "release", "commit": map[string]any{"id": "b2", "committed_date": "2026-10-21T08:00:00Z", "title": "y"}}})
	case p == "projects/5/repository/files/.gitlab-ci.yml/raw":
		w.Write([]byte(f.ci))
	case p == "projects/5/protected_branches":
		send([]map[string]any{{"name": "release", "push_access_levels": []map[string]any{{"access_level": 40}}}})
	case p == "projects/5/pipelines":
		send(f.pipes)
	case p == "projects/5/pipelines/900/jobs":
		send([]map[string]any{{"id": 1, "name": "build", "status": "failed", "stage": "build", "allow_failure": false, "failure_reason": "script_failure"}})
	case strings.HasSuffix(p, "/bridges"):
		send([]any{})
	case p == "projects/5/jobs/1/trace":
		w.Write([]byte("npm ERR! `npm ci` can only install packages when your package.json and package-lock.json are in sync.\nnpm ERR! Missing: lodash@4.17.21 from lock file\nexport NPM_TOKEN=s3cr3tvalue1\n"))
	case strings.HasPrefix(p, "projects/5/repository/commits/"):
		if strings.HasSuffix(p, "merge_requests") {
			send([]map[string]any{{"iid": 42}})
		} else {
			send(map[string]any{"author_name": "Dev One", "title": "bump deps"})
		}
	default:
		w.WriteHeader(404)
		send(map[string]any{})
	}
}

func runOnce(t *testing.T, db *sql.DB, srv *httptest.Server, task string, msgs *[]notify.Message) *Watcher {
	t.Helper()
	dir := t.TempDir()
	w, err := New(context.Background(), Options{DB: db, GitLab: &GitLab{Base: srv.URL, Token: "fake-token", Budget: 200, Sleep: func(time.Duration) {}},
		Incidents: &incidents.Options{}, StateDir: dir, PublicURL: "https://argus.example.test",
		Notify: func(m notify.Message) error { *msgs = append(*msgs, m); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	switch task {
	case "scan":
		if _, err := w.Scan(false); err != nil {
			t.Fatal(err)
		}
	case "watch":
		if err := w.Watch(0); err != nil {
			t.Fatal(err)
		}
		if _, err := w.NotifyFindings(w.New, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStatus(task, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cicd-status.json")); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWatcherFlow(t *testing.T) {
	db := testDB(t)
	fg := &fakeGitLab{t: t, ci: "build:\n  script: make\n", pipes: []map[string]any{{"id": 900, "ref": "release", "status": "failed", "sha": strings.Repeat("a", 40),
		"source": "push", "created_at": "2026-10-21T08:00:00Z", "updated_at": "2026-10-21T08:05:00Z"}}}
	srv := httptest.NewServer(fg)
	defer srv.Close()
	var msgs []notify.Message
	runOnce(t, db, srv, "scan", &msgs)
	var kind string
	var consumer int
	if err := db.QueryRow("SELECT kind, consumer FROM cicd_projects WHERE id=5").Scan(&kind, &consumer); err != nil || kind != "app" || consumer != 1 {
		t.Fatalf("scan: %q %d %v", kind, consumer, err)
	}
	w := runOnce(t, db, srv, "watch", &msgs)
	var klass, env, author, mr, evidence string
	var fid int64
	var inc sql.NullInt64
	if err := db.QueryRow("SELECT id, klass, env, author, mr, evidence, incident_id FROM cicd_findings").Scan(&fid, &klass, &env, &author, &mr, &evidence, &inc); err != nil {
		t.Fatal(err)
	}
	if klass != "lockfile" || env != "release" || author != "Dev One" || mr != "!42" || !inc.Valid {
		t.Fatalf("finding %s %s %s %s %v", klass, env, author, mr, inc)
	}
	if strings.Contains(evidence, "s3cr3tvalue1") {
		t.Fatal("a secret reached the evidence")
	}
	if len(w.New) != 1 || len(msgs) != 1 || !strings.Contains(msgs[0].Link, "/cicd/finding?id=") {
		t.Fatalf("new %v msgs %v", w.New, msgs)
	}
	var dc, ienv string
	db.QueryRow("SELECT dc, env FROM incidents WHERE id=?", inc.Int64).Scan(&dc, &ienv)
	if dc != "CICD" || ienv != "release" {
		t.Fatalf("incident %s %s", dc, ienv)
	}
	// the same pipeline turns green on a retry: flaky, resolved, and so is the incident
	fg.mu.Lock()
	fg.pipes[0]["status"], fg.pipes[0]["updated_at"] = "success", "2026-10-21T08:20:00Z"
	fg.mu.Unlock()
	db.Exec("UPDATE cicd_projects SET poll_cursor=NULL")
	runOnce(t, db, srv, "watch", &msgs)
	var status string
	db.QueryRow("SELECT status, klass FROM cicd_findings WHERE id=?", fid).Scan(&status, &klass)
	var istatus string
	db.QueryRow("SELECT status FROM incidents WHERE id=?", inc.Int64).Scan(&istatus)
	if status != "resolved" || klass != "flaky" || istatus != "resolved" {
		t.Fatalf("after success: %s %s incident %s", status, klass, istatus)
	}
	if len(msgs) != 1 {
		t.Fatalf("a finding was posted twice: %d", len(msgs))
	}
}

func TestPreReleaseFailureIsNotAnIncident(t *testing.T) {
	db := testDB(t)
	fg := &fakeGitLab{t: t, ci: "build:\n  script: make\n", pipes: []map[string]any{{"id": 900, "ref": "pre-release", "status": "failed", "sha": "a1",
		"created_at": "2026-10-21T08:00:00Z", "updated_at": "2026-10-21T08:05:00Z"}}}
	srv := httptest.NewServer(fg)
	defer srv.Close()
	var msgs []notify.Message
	runOnce(t, db, srv, "scan", &msgs)
	runOnce(t, db, srv, "watch", &msgs)
	var env string
	var inc sql.NullInt64
	if err := db.QueryRow("SELECT env, incident_id FROM cicd_findings").Scan(&env, &inc); err != nil || env != "pre-release" || inc.Valid {
		t.Fatalf("%s %v %v", env, inc, err)
	}
}

func TestEngineIncludeDecidesConsumer(t *testing.T) {
	db := testDB(t)
	fg := &fakeGitLab{t: t, ci: "build:\n  script: make\n"}
	srv := httptest.NewServer(fg)
	defer srv.Close()
	w, _ := New(context.Background(), Options{DB: db, GitLab: &GitLab{Base: srv.URL, Token: "fake-token", Budget: 50}, EnginePath: "grp/engine", EngineID: 77})
	if _, err := w.Scan(false); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var consumer int
	var kind string
	db.QueryRow("SELECT consumer, kind FROM cicd_projects WHERE id=5").Scan(&consumer, &kind)
	if consumer != 0 || kind != "none" {
		t.Fatalf("a project without the engine include is watched: %d %s", consumer, kind)
	}
}
