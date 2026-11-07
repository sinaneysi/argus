package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func TestRepoParseTarget(t *testing.T) {
	ok := map[string]string{
		"https://gitlab.example.com/northwind/website/shop-front-ts":  "northwind/website/shop-front-ts",
		"https://gitlab.example.com/northwind/a/b/-/merge_requests/3": "northwind/a/b",
		"https://gitlab.example.com/northwind/a/b.git":                "northwind/a/b",
		"HTTPS://GITLAB.example.com:443/northwind/a/b.git?x=1#y":      "northwind/a/b",
		"git@gitlab.example.com:northwind/a.git":                      "northwind/a",
		"gitlab.example.com/northwind/a":                              "northwind/a",
		" northwind/a/b ":                                             "northwind/a/b",
		"1208":                                                        "1208",
		"https://gitlab.example.com/northwind%2Fa":                    "northwind/a",
		"https://:@gitlab.example.com/northwind/a":                    "northwind/a",
		"https://gitlab.example.com/northwind/a\tb/c":                 "northwind/ab/c",
	}
	for in, want := range ok {
		if got, err := RepoParseTarget(in); err != "" || got != want {
			t.Errorf("%q: got %q err %q", in, got, err)
		}
	}
	for _, in := range []string{"https://github.com/sinadoe/argus", "http://evil.example/northwind/a", "git@github.com:a/b.git",
		"https://oauth2:glpat-x@gitlab.example.com/a/b", "ftp://gitlab.example.com/a/b", "file:///etc/passwd", "a",
		"../../etc/passwd", "a/../b", "", "  ", strings.Repeat("x", 400), "northwind/a b", "northwind/<script>",
		"javascript:alert(1)", "evil.example/northwind/a", "123%0A", "https://[::1/a", "https://glä.example/a/b", "tést/a"} {
		if got, err := RepoParseTarget(in); err == "" {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	if _, err := RepoParseTarget("https://github.com/a/b"); err != "Only projects on gitlab.example.com can be checked (got github.com)." {
		t.Fatal(err)
	}
}

func repoSeed(t *testing.T, e *inventoryEnv) map[string]int64 {
	cicdSeed(t, e)
	e.exec(t, `CREATE TABLE cicd_readiness (id INTEGER PRIMARY KEY, project_id INTEGER, path TEXT, ref TEXT, sha TEXT,
	  rules_version INTEGER, verdict TEXT, blockers INTEGER, warnings INTEGER, passed INTEGER, unknown INTEGER, facts TEXT,
	  report TEXT, checked_at INTEGER, source TEXT, calls INTEGER, error TEXT DEFAULT '')`)
	raw, err := os.ReadFile(filepath.Join("testdata", "repocheck_reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reps map[string]map[string]any
	if err := json.Unmarshal(raw, &reps); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	pid := map[string]int64{"ready": 1, "not_ready": 2, "package": 4}
	for i, k := range []string{"ready", "not_ready", "package"} {
		rep := reps[k]
		if k == "not_ready" { // hostile text from repo files is escaped everywhere
			rep["summary"].([]any)[1] = inventoryHostile
			rep["results"].([]any)[0].(map[string]any)["evidence"] = inventoryHostile
		}
		p := rep["project"].(map[string]any)["path"].(string)
		b, _ := json.Marshal(rep)
		c := rep["counts"].(map[string]any)
		e.exec(t, `INSERT INTO cicd_readiness (id, project_id, path, ref, sha, rules_version, verdict, blockers, warnings, passed, unknown,
		  report, checked_at, source, calls, error) VALUES (?,?,?,?,?,1,?,?,?,?,?,?,?,'test',12,'')`, i+1, pid[k], p, rep["ref"], rep["sha"],
			rep["verdict"], c["blocker"], c["warning"], c["pass"], c["unknown"], string(b), NowInt())
		ids[k] = int64(i + 1)
	}
	e.exec(t, `INSERT INTO cicd_readiness (id, path, verdict, report, checked_at, source, error) VALUES (9, 'northwind/nope', 'error',
	  '{"verdict": "error", "summary": ["GitLab answered: no such project."], "results": []}', ?, 'test', 'GitLab answered: no such project.')`, NowInt())
	return ids
}

func TestRepoPages(t *testing.T) {
	e := inventorySetup(t)
	repoSeed(t, e)

	name, body, ok := fixDownload(py.NewQS("repo", "2"))
	if !ok || name != "argus-fix-repo-2.md" || !strings.Contains(body, "Never push to develop") {
		t.Fatal("download")
	}
	if _, _, ok := fixDownload(py.NewQS("repo", "x")); ok {
		t.Fatal("bad id")
	}
}

func TestRepoPostAndRateLimit(t *testing.T) {
	e := inventorySetup(t)
	repoSeed(t, e)
	s := inventoryViewer()
	code, where := RepoPostCheck(inventoryCtx(s, "/cicd/repo/check"), py.NewQS("project", "https://gitlab.example.com/northwind/new/svc"), s)
	if code != 303 || where != "/cicd/repo?project=northwind%2Fnew%2Fsvc&sent=1" {
		t.Fatal(code, where)
	}
	f := filepath.Join(e.dir, "in", "requests", "repo-check-"+repoHex("northwind/new/svc")+".req")
	b, err := os.ReadFile(f)
	if err != nil || !strings.Contains(string(b), `"by": "view.er"`) {
		t.Fatal("request file", err)
	}
	if code, _ := RepoPostCheck(inventoryCtx(s, "/"), py.NewQS("project", "northwind/new/svc"), s); code != 429 {
		t.Fatal("queued twice", code)
	}

	if code, _ := RepoPostCheck(inventoryCtx(s, "/"), py.NewQS("project", "https://github.com/a/b"), s); code != 400 {
		t.Fatal("other host", code)
	}
	os.Remove(f)
	e.write(t, "out/run-requests.json", `{"repocheck": {"`+repoHex("northwind/new/svc")+`": {"started": `+I2S(NowInt()-30)+`}}}`)
	if code, msg := RepoPostCheck(inventoryCtx(s, "/"), py.NewQS("project", "northwind/new/svc"), s); code != 429 || !strings.Contains(msg, "running since") {
		t.Fatal("running", code, msg)
	}
	e.write(t, "out/run-requests.json", `{"repocheck": {"`+repoHex("northwind/new/svc")+`": {"requested": `+I2S(NowInt())+`, "result": "skipped: daily repo check limit (50) reached"}}}`)

}

func TestRepoNoTable(t *testing.T) {
	inventorySetup(t)

	if len(RepoReadinessMap()) != 0 {
		t.Fatal("map")
	}
}
