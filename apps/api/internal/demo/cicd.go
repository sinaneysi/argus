package demo

import (
	"fmt"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func check(id, title, sev, status, evidence, where, why string, fix ...string) *py.OMap {
	fixFile, fixText, source := "-", "", "platform/deployment README"
	if len(fix) > 0 {
		fixFile = fix[0]
	}
	if len(fix) > 1 {
		fixText = fix[1]
	}
	return om("status", status, "evidence", evidence, "where", where, "id", id, "title", title, "severity", sev,
		"why", why, "source", source, "fix_file", fixFile, "fix", fixText, "needs", "repo")
}

func repoReport(verdict, label, path, ref, sha string, checks []any, branches, pipelines *py.OMap, summary []string) *py.OMap {
	return om("verdict", verdict, "label", label, "summary", summary, "results", checks, "project", path, "ref", ref, "sha", sha,
		"ptype", "app", "branches", branches, "pipelines", pipelines, "executors", om("develop", "docker", "pre-release", "systemd",
			"release", "systemd"),
		"vars", om("mode", "inferred"), "cached", false)
}

type project struct {
	pid       int
	path, svc string
}

func (g *Gen) cicd() {
	o := g.ops
	pid0 := 300
	var projects []project
	for k, svc := range serviceNames() {
		s := SERVICES[svc]
		pid := pid0 + k
		path := s.group + "/" + svc
		projects = append(projects, project{pid, path, svc})
		g.ins(o, "cicd_projects", "id", pid, "path", path, "grp", s.group, "default_branch", "main",
			"last_activity", g.iso(float64(g.r.randint(1, 90))*H),
			"kind", "app", "consumer", 1, "include_ref", iif(k%7 != 0, "v4.2", "main"), "include_file", "cicd/app.gitlab-ci.yml",
			"includes", jd(om("develop", sl("v4.2", "cicd/app.gitlab-ci.yml"), "release", sl("v4.2", "cicd/app.gitlab-ci.yml"))),
			"ci_target", jd(om("pre-release", "stg-"+s.group, "release", "prod-"+s.group)), "executor", iif(k%3 != 0, "docker", "systemd"),
			"protected", jd(om("release", true, "pre-release", true, "develop", false)), "archived", 0, "scanned_at", g.t(3*H),
			"poll_cursor", g.iso(5*M), "polled_at", g.t(3*M))
		for _, br := range []string{"main", "develop", "pre-release", "release"} {
			if br == "pre-release" && (svc == "scheduler" || svc == "report-builder") {
				continue
			}
			committed := g.t(float64(g.r.randint(2, 200)) * H)
			title := []string{"Merge branch 'feature/checkout-retry'", "Bump dependencies", "Fix lint", "Release 2026.10.23"}[g.r.choice(4)]
			g.ins(o, "cicd_branches", "project_id", pid, "branch", br, "present", 1, "sha", h12(path+br)+h12(br)[:4],
				"committed_at", committed, "title", title)
		}
	}
	for _, p := range [][2]string{{"ui-kit", "web"}, {"api-client", "shop"}} {
		name, gp := p[0], p[1]
		pid0 += 100
		g.ins(o, "cicd_projects", "id", pid0, "path", gp+"/"+name, "grp", gp, "default_branch", "main", "kind", "package", "consumer", 1,
			"include_ref", "v4.2", "include_file", "cicd/package.gitlab-ci.yml", "archived", 0, "scanned_at", g.t(3*H))
		for _, br := range []string{"main", "develop", "release"} {
			g.ins(o, "cicd_branches", "project_id", pid0, "branch", br, "present", 1, "sha", h12(name+br), "committed_at", g.t(30*H), "title", "Release")
		}
	}
	plid := 88000
	special := map[string][2]string{"warehouse-api": {"release", "failed"}, "coupon-service": {"pre-release", "failed"},
		"shop-front": {"release", "manual"}, "search": {"develop", "failed"}, "loyalty": {"pre-release", "waiting_for_resource"},
		"report-builder": {"develop", "canceled"}, "recommendation": {"release", "manual"}}
	for _, p := range projects {
		for _, env := range []string{"develop", "pre-release", "release"} {
			if env == "pre-release" && (p.svc == "scheduler" || p.svc == "report-builder") {
				continue
			}
			n := 3
			if env == "release" {
				n = 2
			}
			for run := n - 1; run >= 0; run-- { // oldest first: a newer pipeline has a higher id
				plid++
				status := "success"
				if sp, ok := special[p.svc]; ok && run == 0 && sp[0] == env {
					status = sp[1]
				}
				a1 := g.r.randint(1, 26)
				a2 := g.r.randint(0, 59)
				ago := float64(run*30+a1)*H + float64(a2)*M
				if p.svc == "warehouse-api" && env == "release" && run == 0 {
					ago = 9 * H
				}
				if p.svc == "loyalty" && env == "pre-release" && run == 0 {
					ago = 24 * M
				}
				var finished any
				if status != "manual" && status != "waiting_for_resource" {
					finished = g.t(ago - 7*M)
				}
				user := PEOPLE[g.r.choice(len(PEOPLE))].user
				var prodDeploy any
				if env == "release" && status == "manual" {
					prodDeploy = "manual"
				}
				g.ins(o, "cicd_pipelines", "id", plid, "project_id", p.pid, "ref", env, "env", env, "sha", h12(fmt.Sprintf("%s%s%d", p.path, env, run)),
					"status", status, "source", "push", "created_at", g.t(ago), "updated_at", g.t(ago-6*M),
					"finished_at", finished, "user", user, "deploy_status", status,
					"downstream_id", nil, "prod_deploy", prodDeploy, "triaged", 1, "seen_at", g.t(3*M))
				if status == "success" && run == 0 {
					author := PEOPLE[g.r.choice(len(PEOPLE))].user
					var hosts []string
					for _, v := range VMS {
						if v.has(p.svc) && v.env == env && len(hosts) < 3 {
							hosts = append(hosts, v.name)
						}
					}
					if hosts == nil {
						hosts = []string{}
					}
					pre := "dev-"
					if env == "release" {
						pre = "prod-"
					} else if env == "pre-release" {
						pre = "stg-"
					}
					sf := p.svc == "shop-front"
					g.ins(o, "cicd_deploys", "project_id", p.pid, "project", p.path, "env", env, "sha", h12(p.path+env+"0"),
						"prev_sha", h12(p.path+env+"1"), "pipeline_id", plid, "at", g.t(ago-7*M), "author", author,
						"title", "Release "+g.day(ago), "hosts", jd(hosts),
						"target_group", pre+strings.Split(p.svc, "-")[0],
						"unit", "northwind-"+p.svc+".service", "verify_state", iif(sf, "fail", "pass"),
						"verify_pass", iif(sf, 0, 1),
						"verify_detail", iif(sf, "prod-shop-02: :3001 not listening", "health 200 on every host"),
						"verified_at", g.t(ago-9*M))
				}
			}
		}
	}
	pidof := map[string]int{}
	pathof := map[string]string{}
	for _, p := range projects {
		pidof[p.svc], pathof[p.svc] = p.pid, p.path
	}
	finding := func(fid int, kind, svc, env, sev, klass, rule, title, detail, evidence, proposal string, ago float64, job any, status string, resolved float64) {
		if status == "" {
			status = "open"
		}
		var pid any
		if id, ok := pidof[svc]; ok {
			pid = id
		}
		path := svc
		if p, ok := pathof[svc]; ok {
			path = p
		}
		author := PEOPLE[g.r.choice(len(PEOPLE))].user
		occ := g.r.randint(1, 4)
		var resAt, resNote, inc any
		if resolved != 0 {
			resAt, resNote = g.t(resolved), "passed on retry"
		}
		if svc == "warehouse-api" {
			inc = 150
		}
		g.ins(o, "cicd_findings", "id", fid, "kind", kind, "key", kind+"|"+svc+"|"+env+"|"+rule, "project_id", pid,
			"project", path, "env", env, "ref", env, "severity", sev, "klass", klass, "rule", rule, "title", title, "detail", detail,
			"evidence", evidence, "proposals", jd(L(om("text", proposal, "rule", rule,
				"api", "POST /projects/:id/pipelines/:pid/retry (execution disabled)"))),
			"pipeline_id", nil, "job_id", nil, "job_name", job, "sha", h12(svc+env), "author", author, "mr", nil,
			"occurrences", occ, "first_seen", g.t(ago+2*H), "last_seen", g.t(ago), "status", status,
			"resolved_at", resAt, "resolve_note", resNote, "incident_id", inc, "llm", 1, "notified", 1)
	}
	finding(701, "deploy", "warehouse-api", "release", "high", "lock", "migration-lock",
		"Release deploy of warehouse-api failed: migration lock held",
		"deploy_production: `alembic upgrade head` waited 300 s for the advisory lock held by a stuck migration from 10-22.",
		"ERROR: could not obtain lock on relation \"stock_moves\"\nJob deploy_production exited with code 1",
		"Ask the operator to end the stuck migration session (pid 48121) on prod-db-01, then retry deploy_production.",
		9*H, "deploy_production", "", 0)
	finding(702, "failure", "coupon-service", "pre-release", "medium", "code", "test-failure",
		"coupon-service tests fail on pre-release: percentage coupon division", "unit test test_percentage_min_order failed",
		"ZeroDivisionError: division by zero (coupons/rules.py:88)", "Fix the null min_order in 3.9.1 (see incident #148).", 19*H, "test", "", 0)
	finding(703, "failure", "search", "develop", "low", "lockfile", "lockfile-drift",
		"search build fails on develop: package-lock.json out of date", "npm ci: lockfile does not match package.json",
		"npm ERR! `npm ci` can only install packages when your package.json and package-lock.json are in sync",
		"Run `npm install` and commit the lockfile on develop.", 5*H, "build", "", 0)
	finding(704, "stuck", "loyalty", "pre-release", "medium", "lock", "lock-wait",
		"loyalty pre-release pipeline waits 24 min for the deploy lock",
		"waiting_for_resource on resource group deploy-pre-release, held by pipeline 88190 (shop/cart) which waits at manual.",
		"resource_group deploy-pre-release busy since "+g.iso(30*M), "Cancel the older pipeline 88190 or play its manual job.", 2*M, nil, "", 0)
	finding(705, "release", "shop-front", "release", "medium", "release", "waiting-manual",
		"shop-front release deploy waits at manual for 6 h", "deploy_production is manual on release; the operator plays it.",
		"pipeline status manual", "The operator plays deploy_production after the 502 fix (incident #145).", 6*H, "deploy_production", "", 0)
	finding(706, "survey", "payment-gateway", "release", "critical", "survey", "protected-vars",
		"payment-gateway: PAYMENT_API_KEY is not masked and not protected",
		"The variable is visible in job logs of unprotected branches.", "variables audit: masked=false protected=false",
		"Mark PAYMENT_API_KEY masked and protected; rotate the key.", 1*D, nil, "", 0)
	finding(707, "branch", "scheduler", "", "low", "branch", "missing-env-branch",
		"scheduler has no pre-release branch", "Releases go from develop straight to release.", "branches: main, develop, release",
		"Create pre-release from develop.", 4*D, nil, "", 0)
	finding(708, "branch", "report-builder", "release", "info", "branch", "stale",
		"report-builder release branch 41 days behind develop", "Last release 41 days ago.", "release @ 41 d", "Release or archive it.", 2*D, nil, "", 0)
	finding(709, "variable", "checkout", "release", "high", "variable", "debug-log-level",
		"checkout: LOG_LEVEL=debug on release", "Debug logging in production since 10-18 (see the loki storage analysis).",
		"CI variable LOG_LEVEL scope release = debug", "Set LOG_LEVEL=info for the release scope.", 1.8*D, nil, "", 0)
	finding(710, "failure", "cart", "release", "medium", "flaky", "flaky-test", "cart: flaky integration test on release",
		"test_concurrent_add passed on retry", "retry passed", "Quarantine the test.", 3*D, nil, "resolved", 2.9*D)
	g.exec(o, "INSERT INTO cicd_state (k, v) VALUES ('vars', ?), ('audit_at', ?)",
		jd(om("mode", "inferred", "at", g.t(5*H), "note", "names and flags only, never values")), fmt.Sprint(g.t(5*H)))
	// Check a repo: one Ready, one Warn, one Not ready
	commonOK := []any{
		check("ci-file", "The repo has a .gitlab-ci.yml", "blocker", "pass", "present", ".gitlab-ci.yml",
			"Without a CI file GitLab builds no pipeline."),
		check("ci-include", "The CI file includes platform/deployment (app template)", "blocker", "pass",
			"platform/deployment cicd/app.gitlab-ci.yml @ v4.2", ".gitlab-ci.yml:2", "The shared engine supplies build, deploy, lock and verify."),
		check("branches-exist", "The env branches exist (develop, pre-release, release)", "blocker", "pass",
			"develop, pre-release, release exist.", "branches", "The workflow rules run only on these names."),
		check("executor-var", "EXECUTOR is set per environment", "blocker", "pass", "EXECUTOR resolves for every env branch.",
			"CI variables", "Without EXECUTOR no build or deploy job matches."),
		check("target-hosts", "TARGET_HOSTS names the right inventory group per environment", "blocker", "pass",
			"pre-release -> stg-*, release -> prod-*.", "CI variables", "A wrong group deploys to the wrong hosts."),
		check("healthcheck", "A /health endpoint is declared for the verify step", "warning", "pass", "HEALTH_PATH=/health",
			"CI variables", "Verify checks every host after a deploy."),
	}
	envs := []string{"develop", "pre-release", "release"}
	brOK := func() *py.OMap {
		m := om()
		for _, e := range envs {
			m.Set(e, om("protected", e != "develop", "dev_push", false))
		}
		return m
	}
	plOK := func() *py.OMap {
		m := om()
		for _, e := range envs {
			m.Set(e, om("status", "success", "sha", "a1b2c3d4", "at", g.t(5*H)))
		}
		return m
	}
	brOk, plOk := brOK(), plOK()
	plWarn := plOk.Copy()
	plWarn.Set("develop", om("status", "failed", "sha", "9f8e7d6c", "at", g.t(5*H), "klass", "lockfile", "finding", "npm ci lockfile drift"))
	type rep struct {
		path           string
		pid            int
		verdict, label string
		checks         []any
		brs, pls       *py.OMap
		summ           []string
		nb, nw, npass  int
	}
	reports := []rep{
		{"orders/order-service", pidof["order-service"], "ready", "Ready", commonOK, brOk, plOk,
			sl("Ready: 0 blockers and 0 warnings of 6 checks (develop @ a1b2c3d4, app, docker/systemd).", "Nothing blocks the pipeline.",
				"Next: push to develop and watch the first pipeline."), 0, 0, 6},
		{"search/search", pidof["search"], "warn", "Ready with warnings", append(append([]any{}, commonOK[:5]...),
			check("healthcheck", "A /health endpoint is declared for the verify step", "warning", "fail", "HEALTH_PATH not set",
				"CI variables", "Without it verify only checks that the unit is active.", "GitLab -> Settings -> CI/CD -> Variables",
				"HEALTH_PATH  scope *  = /health"),
			check("lockfile", "package-lock.json matches package.json on develop", "warning", "fail", "npm ci would fail (drift)",
				"package-lock.json", "A drifted lockfile breaks the build job.", "package-lock.json", "npm install && git add package-lock.json")),
			brOk, plWarn,
			sl("Ready with warnings: 0 blockers and 2 warnings of 7 checks.", "The pipeline runs, but verify is weak and develop fails.",
				"Next: set HEALTH_PATH and commit the lockfile."), 0, 2, 5},
		{"platform/feature-flags", pidof["feature-flags"], "not_ready", "Not ready", L(
			commonOK[0],
			check("ci-include", "The CI file includes platform/deployment (app template)", "blocker", "fail",
				"no include: a hand-written deploy job with ssh", ".gitlab-ci.yml:14",
				"A repo outside the shared engine has no lock, no verify and no rollback.", ".gitlab-ci.yml",
				"include:\n  - project: platform/deployment\n    ref: v4.2\n    file: cicd/app.gitlab-ci.yml"),
			check("branches-exist", "The env branches exist (develop, pre-release, release)", "blocker", "fail",
				"only main and develop exist", "branches", "The workflow rules run only on these names.",
				"GitLab -> Repository -> Branches",
				"git switch -c pre-release origin/develop && git push origin pre-release\ngit switch -c release origin/pre-release && git push origin release"),
			check("executor-var", "EXECUTOR is set per environment", "blocker", "unknown", "cannot tell without the include",
				"CI variables", "Without EXECUTOR no build or deploy job matches."),
			check("protected-release", "The release branch is protected (Maintainers merge, nobody pushes)", "warning", "fail",
				"no release branch", "branches", "A protected release keeps unreviewed code out of production.")),
			om("develop", om("protected", false, "dev_push", true)), om("develop", om("status", "success", "sha", "4c4c4c4c", "at", g.t(2*D))),
			sl("Not ready: 2 blockers and 1 warning of 5 checks.", "This repo deploys by hand over ssh, outside the shared pipeline.",
				"Next: add the include and the env branches (the fixes below)."), 2, 1, 1},
	}
	for k, r := range reports {
		rp := repoReport(r.verdict, r.label, r.path, "develop", "a1b2c3d4", r.checks, r.brs, r.pls, r.summ)
		g.ins(o, "cicd_readiness", "project_id", r.pid, "path", r.path, "ref", "develop", "sha", "a1b2c3d4e5f6", "rules_version", 3, "verdict", r.verdict,
			"blockers", r.nb, "warnings", r.nw, "passed", r.npass, "unknown", iif(r.verdict == "not_ready", 1, 0), "facts", jd(om("ptype", "app")),
			"report", jd(rp), "checked_at", g.t(float64((k+1)*3)*H), "source", "web", "calls", 11, "error", "")
	}
	g.writeJSON("cicd-status.json", om("watch", om("at", g.t(2*M), "status", "OK", "write", "disabled (CICD_WRITE=0, no write token)", "note", ""),
		"scan", om("at", g.t(3*H), "status", "OK"), "audit", om("at", g.t(5*H), "status", "OK"),
		"daily", om("at", g.t(7*H), "status", "OK")))
}
