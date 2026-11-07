package app

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

func cicdSeed(t *testing.T, e *inventoryEnv) {
	e.exec(t, `CREATE TABLE cicd_projects (id INTEGER PRIMARY KEY, path TEXT NOT NULL, grp TEXT, default_branch TEXT, kind TEXT,
	  consumer INTEGER DEFAULT 0, include_ref TEXT, includes TEXT, executor TEXT, archived INTEGER DEFAULT 0, scanned_at INTEGER)`)
	e.exec(t, `CREATE TABLE cicd_branches (project_id INTEGER, branch TEXT, present INTEGER, PRIMARY KEY (project_id, branch))`)
	e.exec(t, `CREATE TABLE cicd_pipelines (id INTEGER PRIMARY KEY, project_id INTEGER, ref TEXT, env TEXT, sha TEXT, status TEXT,
	  created_at INTEGER, deploy_status TEXT)`)
	e.exec(t, `CREATE TABLE cicd_deploys (id INTEGER PRIMARY KEY, project_id INTEGER, env TEXT, sha TEXT, at INTEGER, author TEXT,
	  hosts TEXT, verify_state TEXT)`)
	e.exec(t, `CREATE TABLE cicd_findings (id INTEGER PRIMARY KEY, kind TEXT, project_id INTEGER, project TEXT, env TEXT, ref TEXT,
	  severity TEXT, klass TEXT, rule TEXT, title TEXT, detail TEXT, evidence TEXT, proposals TEXT, pipeline_id INTEGER, job_id INTEGER,
	  job_name TEXT, sha TEXT, author TEXT, mr TEXT, occurrences INTEGER DEFAULT 1, first_seen INTEGER, last_seen INTEGER,
	  status TEXT DEFAULT 'open', resolved_at INTEGER, resolve_note TEXT, incident_id INTEGER, llm INTEGER DEFAULT 0)`)
	e.exec(t, `CREATE TABLE cicd_state (k TEXT PRIMARY KEY, v TEXT)`)
	now := NowInt()
	for i, p := range []string{"g/a", "g/b", "g/c", "g/pkg"} {
		kind := "app"
		if p == "g/pkg" {
			kind = "package"
		}
		e.exec(t, "INSERT INTO cicd_projects (id, path, grp, kind, consumer) VALUES (?,?,?,?,1)", i+1, p, "g", kind)
	}
	for _, b := range [][2]any{{1, "develop"}, {2, "develop"}, {3, "develop"}, {1, "release"}} {
		e.exec(t, "INSERT INTO cicd_branches VALUES (?,?,1)", b[0], b[1])
	}
	// a: newest ran = failed (a skipped one after it is ignored); b: only skipped -> canceled/skipped_only; c: nothing -> nopipeline
	e.exec(t, "INSERT INTO cicd_pipelines VALUES (1, 1, 'develop', 'develop', 'abc', 'failed', ?, NULL)", now-100)
	e.exec(t, "INSERT INTO cicd_pipelines VALUES (2, 1, 'develop', 'develop', 'abd', 'skipped', ?, NULL)", now-50)
	e.exec(t, "INSERT INTO cicd_pipelines VALUES (3, 2, 'develop', 'develop', 'abe', 'skipped', ?, NULL)", now-50)
	e.exec(t, "INSERT INTO cicd_pipelines VALUES (4, 1, 'release', 'release', 'abf', 'manual', ?, NULL)", now-50)
	e.exec(t, `INSERT INTO cicd_findings (id, kind, project_id, project, env, severity, klass, title, detail, proposals, last_seen)
	  VALUES (7, 'failure', 1, 'g/a', 'release', 'high', 'code', ?, ?, '[{"text": "fix <it>", "method": "POST", "path": "projects/1/retry", "rule": "r"}]', ?)`,
		inventoryHostile, inventoryHostile, now)
}

func TestCicdSummaryCounts(t *testing.T) {
	e := inventorySetup(t)
	if CSSummarize("app", 0) != nil || CicdSummaryJSON(py.QS{}) != `{"error": "no CI/CD data yet"}`+"\n" {
		t.Fatal("no tables")
	}
	cicdSeed(t, e)
	s := CSSummarize("app", 0)
	dev, rel := s.Envs["develop"], s.Envs["release"]
	if dev["failed"] != 1 || dev["canceled"] != 1 || dev["skipped_only"] != 1 || dev["nopipeline"] != 1 || dev["branches"] != 3 || dev["total"] != 3 {
		t.Fatal(dev)
	}
	if rel["waiting"] != 1 || rel["nobranch"] != 2 || rel["branches"] != 1 {
		t.Fatal(rel)
	}
	if CSLabelOf("waiting", "release") != "Waiting for manual deploy" || CSLabelOf("nobranch", "develop") != "No develop branch" {
		t.Fatal("labels")
	}
	j := CicdSummaryJSON(py.NewQS("kind", "package"))
	if !strings.HasPrefix(j, "{\n \"kind\": \"package\",\n \"window_days\": 30,") || strings.Contains(j, "g/") {
		t.Fatal(j)
	}
}

func TestCicdPagesEscapeAndPrefill(t *testing.T) {
	e := inventorySetup(t)
	cicdSeed(t, e)

	pf := CicdPrefill("7")
	if pf == nil || pf.S("priority") != "High" || !strings.Contains(pf.S("fix_prompt"), "API: POST /api/v4/projects/1/retry") {
		t.Fatal(pf)
	}
	if CicdPrefill("x") != nil || CicdPrefill("99") != nil {
		t.Fatal("prefill of nothing")
	}

}
