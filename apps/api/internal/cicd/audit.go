package cicd

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The daily audit (cicd-daily): the newest pipeline that ran on each env branch where none is recorded (seed),
// missing env branches, commits on a higher branch that never went through the lower one (drift), stale branches,
// pushes that started no pipeline, release deploys waiting at manual or missing, the default branch, the engine
// include ref, branch protection, and the variable audit (inferred, or full from the ci-vars-meta metadata).

var (
	hotfixRe = regexp.MustCompile(`(?i)hot-?fix|cherry picked from commit`)
	skipCIRe = regexp.MustCompile(`(?i)\[(skip ci|ci skip)\]`)
)

var seedStates = []string{"success", "failed", "canceled", "manual"}

// DriftCommits are the non-merge, non-hotfix commits of a compare result (commits in `to` that are not in `from`),
// less the sanctioned ones (allow matches the title; nil: none).
func DriftCommits(compare map[string]any, allow *regexp.Regexp) []map[string]string {
	var out []map[string]string
	for _, c := range maps(compare["commits"]) {
		if ps, _ := c["parent_ids"].([]any); len(ps) > 1 {
			continue
		}
		title := str(c["title"])
		if hotfixRe.MatchString(title+"\n"+str(c["message"])) || (allow != nil && allow.MatchString(title)) {
			continue
		}
		id := orS(str(c["short_id"]), str(c["id"]))
		out = append(out, map[string]string{"id": runeHead(id, 12), "title": runeHead(title, 140), "author": runeHead(str(c["author_name"]), 40)})
	}
	return out
}

func day(t int64) string { return time.Unix(t, 0).UTC().Format("2006-01-02") }

func (w *Watcher) branchFinding(p Row, key, sev, title, detail, ref, kind string, keys map[string]bool) error {
	if kind == "" {
		kind = "branch"
	}
	k := fmt.Sprintf("%s|%d|%s", kind, p.I("id"), key)
	keys[k] = true
	rule, _, _ := strings.Cut(key, "|")
	_, _, _, err := UpsertFinding(w.tx, kind, k, w.now, F{"project_id": p.I("id"), "project": p.S("path"), "env": EnvOf(ref), "ref": ref,
		"severity": sev, "klass": "branch", "rule": rule, "title": title, "detail": detail, "proposals": []Proposal{}}, false)
	return err
}

// Audit runs the branch audit over the watched projects (resuming where a spent budget stopped it) and the
// variable audit.
func (w *Watcher) Audit() error {
	projs, err := rows(w.tx, "SELECT * FROM cicd_projects WHERE consumer=1 ORDER BY id")
	if err != nil {
		return err
	}
	var pos int
	GetState(w.tx, "audit_pos", &pos)
	if len(projs) > 0 {
		pos %= len(projs)
	} else {
		pos = 0
	}
	order := append(append([]Row{}, projs[pos:]...), projs[:pos]...)
	done, seen := map[int64]bool{}, map[string]bool{}
	finished := true
	for i, p := range order {
		keys, err := w.auditProject(p)
		if errors.Is(err, ErrBudget) {
			if err := SetState(w.tx, "audit_pos", (pos+i)%len(projs)); err != nil {
				return err
			}
			fmt.Fprintf(w.Opt.Log, "audit: budget spent after %d projects; the rest next run\n", i)
			finished = false
			break
		} else if err != nil {
			return err
		}
		for k := range keys {
			seen[k] = true
		}
		done[p.I("id")] = true
		if err := w.commit(); err != nil {
			return err
		}
	}
	if finished {
		if err := SetState(w.tx, "audit_pos", 0); err != nil {
			return err
		}
	}
	open, err := rows(w.tx, "SELECT id, key, project_id FROM cicd_findings WHERE kind IN ('branch','release') AND status='open'")
	if err != nil {
		return err
	}
	for _, r := range open {
		if done[r.I("project_id")] && !seen[r.S("key")] {
			if err := w.exec("UPDATE cicd_findings SET status='resolved', resolved_at=?, resolve_note='no longer found by the audit' WHERE id=?",
				w.now, r.I("id")); err != nil {
				return err
			}
		}
	}
	if err := w.VarsAudit(); err != nil {
		return err
	}
	if err := SetState(w.tx, "audit_at", w.now); err != nil {
		return err
	}
	return w.commit()
}

func (w *Watcher) branches(pid int64) (map[string]Row, error) {
	rs, err := rows(w.tx, "SELECT * FROM cicd_branches WHERE project_id=?", pid)
	if err != nil {
		return nil, err
	}
	m := map[string]Row{}
	for _, r := range rs {
		m[r.S("branch")] = r
	}
	return m, nil
}

// SeedLatest records, for each present env branch with no pipeline that ran, the newest one that ran: history only
// (triaged=1: no triage, finding or message). One GET per branch, repeated only when a newer pipeline is recorded.
func (w *Watcher) SeedLatest(p Row, brs map[string]Row) (int, error) {
	pid := p.I("id")
	memo := map[string]int64{}
	GetState(w.tx, "seed", &memo)
	n := 0
	for _, env := range Envs {
		b := brs[env]
		if b == nil || b.I("present") == 0 {
			continue
		}
		ran, err := one(w.tx, "SELECT 1 x FROM cicd_pipelines WHERE project_id=? AND ref=? AND status!='skipped' LIMIT 1", pid, env)
		if err != nil {
			return n, err
		}
		if ran != nil {
			continue
		}
		nr, err := one(w.tx, "SELECT COALESCE(max(id), 0) m FROM cicd_pipelines WHERE project_id=? AND ref=?", pid, env)
		if err != nil {
			return n, err
		}
		key := fmt.Sprintf("%d|%s", pid, env)
		if v, ok := memo[key]; ok && v == nr.I("m") {
			continue
		}
		pls, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines", pid), Q("ref", env, "per_page", "20", "order_by", "id", "sort", "desc"))
		if err != nil {
			return n, err
		}
		memo[key] = nr.I("m")
		if err := SetState(w.tx, "seed", memo); err != nil {
			return n, err
		}
		var pl map[string]any
		for _, x := range pls {
			if str(x["status"]) != "skipped" {
				pl = x
				break
			}
		}
		if pl == nil || !inList(str(pl["status"]), seedStates) {
			continue
		}
		if err := w.exec("INSERT OR IGNORE INTO cicd_pipelines (id, project_id, ref, env, sha, status, source, created_at, updated_at, seen_at, triaged) "+
			"VALUES (?,?,?,?,?,?,?,?,?,?,1)", toI(pl["id"]), pid, env, env, nullS(str(pl["sha"])), str(pl["status"]), nullS(str(pl["source"])),
			nullI(TS(str(pl["created_at"]))), nullI(TS(str(pl["updated_at"]))), w.now); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		w.bump("seeded", n)
	}
	return n, nil
}

func (w *Watcher) maxCreated(pid int64, env string) (int64, error) {
	r, err := one(w.tx, "SELECT COALESCE(max(created_at), 0) m FROM cicd_pipelines WHERE project_id=? AND ref=?", pid, env)
	if err != nil {
		return 0, err
	}
	return r.I("m"), nil
}

func (w *Watcher) auditProject(p Row) (map[string]bool, error) {
	keys := map[string]bool{}
	pid := p.I("id")
	brs, err := w.branches(pid)
	if err != nil {
		return nil, err
	}
	if _, err := w.SeedLatest(p, brs); err != nil {
		return nil, err
	}
	app := p.S("kind") == "app"
	present := func(env string) bool { b := brs[env]; return b != nil && b.I("present") == 1 }
	for _, env := range Envs {
		if !present(env) && (app || env != "pre-release") {
			sev := "low"
			if env == "release" && app {
				sev = "medium"
			}
			if err := w.branchFinding(p, "missing|"+env, sev, "branch "+env+" is missing",
				fmt.Sprintf("%s has no %s branch; the %s environment cannot build or deploy it.", p.S("path"), env, env), env, "", keys); err != nil {
				return nil, err
			}
		}
	}
	for _, pair := range [][2]string{{"pre-release", "release"}, {"develop", "pre-release"}} {
		lo, hi := pair[0], pair[1]
		a, b := brs[lo], brs[hi]
		if !present(lo) || !present(hi) || a.S("sha") == "" || b.S("sha") == "" || a.S("sha") == b.S("sha") {
			continue
		}
		mb, err := w.Opt.GitLab.Obj(w.ctx, fmt.Sprintf("projects/%d/repository/merge_base", pid), map[string][]string{"refs[]": {hi, lo}})
		if err != nil {
			return nil, err
		}
		if str(mb["id"]) == b.S("sha") {
			continue // hi is fully contained in lo
		}
		cmp, err := w.Opt.GitLab.Obj(w.ctx, fmt.Sprintf("projects/%d/repository/compare", pid), Q("from", lo, "to", hi, "straight", "false"))
		if err != nil {
			return nil, err
		}
		extra := DriftCommits(cmp, w.Opt.DriftAllow)
		if len(extra) == 0 {
			continue
		}
		var parts []string
		for i, c := range extra {
			if i < 5 {
				parts = append(parts, fmt.Sprintf("%s %s (%s)", c["id"], c["title"], c["author"]))
			}
		}
		more := ""
		if len(extra) > 5 {
			more = fmt.Sprintf("; +%d more", len(extra)-5)
		}
		sev := "low"
		if hi == "release" {
			sev = "high"
		}
		if err := w.branchFinding(p, "drift|"+hi, sev, fmt.Sprintf("%s has %d commit(s) not in %s", hi, len(extra), lo),
			fmt.Sprintf("Commits on %s that never went through %s (merge and hotfix commits excluded): %s%s. Bring them back into %s (MR %s → %s) so the next promotion does not drop them.",
				hi, lo, strings.Join(parts, "; "), more, lo, hi, lo), hi, "", keys); err != nil {
			return nil, err
		}
	}
	for _, env := range Envs {
		if !present(env) {
			continue
		}
		b := brs[env]
		last, err := w.maxCreated(pid, env)
		if err != nil {
			return nil, err
		}
		if last == 0 || w.now-last > 30*86400 {
			pls, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines", pid), Q("ref", env, "per_page", "1"))
			if err != nil {
				return nil, err
			}
			last = 0
			if len(pls) > 0 {
				last = TS(str(pls[0]["created_at"]))
			}
		}
		if last == 0 || w.now-last > 30*86400 {
			lp, lc := "never", "?"
			if last != 0 {
				lp = day(last)
			}
			if b.I("committed_at") != 0 {
				lc = day(b.I("committed_at"))
			}
			if err := w.branchFinding(p, "stale|"+env, "info", env+": no pipeline in 30 days",
				fmt.Sprintf("Last pipeline on %s: %s; last commit %s.", env, lp, lc), env, "", keys); err != nil {
				return nil, err
			}
		} else if b.S("sha") != "" && b.I("committed_at") < w.now-1800 {
			has, err := one(w.tx, "SELECT 1 x FROM cicd_pipelines WHERE project_id=? AND sha=?", pid, b.S("sha"))
			if err != nil {
				return nil, err
			}
			if has != nil || skipCIRe.MatchString(b.S("title")) {
				continue
			}
			pls, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines", pid), Q("sha", b.S("sha"), "per_page", "1"))
			if err != nil {
				return nil, err
			}
			if len(pls) > 0 {
				continue
			}
			c, err := w.Opt.GitLab.Obj(w.ctx, fmt.Sprintf("projects/%d/repository/commits/%s", pid, b.S("sha")), nil)
			if err != nil {
				return nil, err
			}
			if skipCIRe.MatchString(str(c["message"])) {
				continue
			}
			sha := runeHead(b.S("sha"), 8)
			if _, err := w.inferredFinding(p, env, "no-pipeline", fmt.Sprintf("the last push to %s (%s) started no pipeline", env, sha),
				fmt.Sprintf("No pipeline for %s \"%s\" and no [skip ci]: EXECUTOR@%s is probably missing (the pipeline would be empty).",
					sha, runeHead(b.S("title"), 80), env), "EXECUTOR"); err != nil {
				return nil, err
			}
		}
	}
	if app && present("release") {
		if err := w.releaseChecks(p, brs["release"], keys); err != nil {
			return nil, err
		}
	}
	if d := p.S("default_branch"); d != "" && d != "develop" && d != "main" && d != "master" {
		sev, extra := "low", ""
		if d == "release" {
			sev, extra = "medium", " — that is production"
		}
		if err := w.branchFinding(p, "default", sev, "default branch is "+d,
			"MRs open against "+d+" by default"+extra+"; the convention is develop.", EnvOf(d), "", keys); err != nil {
			return nil, err
		}
	}
	incs := map[string][]string{}
	json.Unmarshal([]byte(orS(p.S("includes"), "{}")), &incs)
	refSet := map[string]bool{}
	for _, v := range incs {
		if len(v) > 0 {
			refSet[v[0]] = true
		}
	}
	if len(refSet) > 0 && !(len(refSet) == 1 && refSet["main"]) {
		var refs, pairs, names []string
		for r := range refSet {
			refs = append(refs, r)
		}
		sort.Strings(refs)
		for k := range incs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			pairs = append(pairs, k+"="+incs[k][0])
		}
		if err := w.branchFinding(p, "include-ref", "low", "the CI engine is included at "+strings.Join(refs, ", "),
			"The CI include is not ref: main on every env branch ("+strings.Join(pairs, ", ")+
				"): pinned branches miss engine fixes, or test an unreleased engine branch.", "", "", keys); err != nil {
			return nil, err
		}
	}
	var prot map[string]map[string]any
	if json.Unmarshal([]byte(p.S("protected")), &prot) == nil && prot != nil && app {
		for _, env := range []string{"release", "pre-release"} {
			if !present(env) {
				continue
			}
			sev := "low"
			if env == "release" {
				sev = "medium"
			}
			pr, ok := prot[env]
			if !ok {
				if err := w.branchFinding(p, "unprotected|"+env, sev, env+" is not a protected branch",
					"Anyone with Developer access can push to "+env+".", env, "", keys); err != nil {
					return nil, err
				}
			} else if dp, _ := pr["dev_push"].(bool); dp {
				if err := w.branchFinding(p, "devpush|"+env, sev, "Developers can push to "+env,
					env+" is protected but allows Developer pushes.", env, "", keys); err != nil {
					return nil, err
				}
			}
		}
	}
	return keys, nil
}

func (w *Watcher) engineDeployJob(ds int64) (map[string]any, error) {
	if ds == 0 || w.Opt.EngineID == 0 {
		return nil, nil
	}
	dj, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines/%d/jobs", w.Opt.EngineID, ds), Q("per_page", "20"))
	if err != nil {
		return nil, err
	}
	for _, x := range dj {
		if inList(str(x["name"]), w.Opt.DeployJobNames) {
			return x, nil
		}
	}
	return nil, nil
}

// releaseChecks: the newest release pipeline waiting at manual for over 6 hours; no release deploy through CI in 30
// days; release HEAD not the last deployed commit. Report only: Argus never plays a deploy.
func (w *Watcher) releaseChecks(p Row, rb Row, keys map[string]bool) error {
	pid := p.I("id")
	newest, err := one(w.tx, "SELECT * FROM cicd_pipelines WHERE project_id=? AND ref='release' AND status!='skipped' ORDER BY id DESC LIMIT 1", pid)
	if err != nil {
		return err
	}
	created := int64(0)
	if newest != nil {
		created = newest.I("created_at")
		if created == 0 {
			created = w.now
		}
	}
	if newest != nil && (newest.S("status") == "running" || newest.S("status") == "manual") && created < w.now-6*3600 {
		b, err := w.bridge(pid, newest.I("id"))
		if err != nil {
			return err
		}
		dsm, _ := b["downstream_pipeline"].(map[string]any)
		ds := toI(dsm["id"])
		dep, err := w.engineDeployJob(ds)
		if err != nil {
			return err
		}
		st := orS(orS(str(dep["status"]), str(b["status"])), newest.S("status"))
		if st == "manual" {
			days := float64(w.now-created) / 86400
			name := "the deploy"
			if dep != nil {
				name = str(dep["name"])
			}
			dss := "?"
			if ds != 0 {
				dss = fmt.Sprint(ds)
			}
			if err := w.branchFinding(p, "waiting-manual", "info", fmt.Sprintf("release deploy waiting at manual for %.1f d", days),
				fmt.Sprintf("Pipeline %d (%s): %s in the CI engine's pipeline %s waits at manual. It holds this project's deploy lock. "+
					"The operator plays it (after the pre-deploy checks) or cancels the pipeline; Argus never plays it.",
					newest.I("id"), runeHead(newest.S("sha"), 8), name, dss), "release", "release", keys); err != nil {
				return err
			}
		}
	}
	last, err := one(w.tx, "SELECT * FROM cicd_deploys WHERE project_id=? AND env='release' ORDER BY at DESC LIMIT 1", pid)
	if err != nil {
		return err
	}
	lastSha, lastAt := "", int64(0)
	if last != nil {
		lastSha, lastAt = last.S("sha"), last.I("at")
	}
	if last == nil || w.now-last.I("at") > 30*86400 {
		pls, err := w.Opt.GitLab.List(w.ctx, fmt.Sprintf("projects/%d/pipelines", pid),
			Q("ref", "release", "status", "success", "updated_after", ISO(w.now-30*86400), "per_page", "3"))
		if err != nil {
			return err
		}
		for i, pl := range pls {
			if i >= 3 {
				break
			}
			b, err := w.bridge(pid, toI(pl["id"]))
			if err != nil {
				return err
			}
			if b != nil && str(b["status"]) == "success" {
				lastSha, lastAt = str(pl["sha"]), TS(str(b["finished_at"]))
				if lastAt == 0 {
					lastAt = TS(str(pl["updated_at"]))
				}
				break
			}
		}
	}
	if lastAt == 0 || w.now-lastAt > 30*86400 {
		return w.branchFinding(p, "no-deploy-30d", "info", "no release deploy through CI in 30 days",
			"No successful "+w.Opt.DeployBridge+" on release in 30 days: production still runs a unit deployed another way, "+
				"or nobody deployed.", "release", "release", keys)
	}
	if rb.S("sha") != "" && lastSha != "" && rb.S("sha") != lastSha && rb.I("committed_at") > lastAt+600 {
		return w.branchFinding(p, "head-not-deployed", "low", fmt.Sprintf("release HEAD %s is not deployed", runeHead(rb.S("sha"), 8)),
			fmt.Sprintf("Last CI deploy to release: %s on %s; release moved on %s (\"%s\"). Production runs an older commit than the branch says.",
				runeHead(lastSha, 8), day(lastAt), day(rb.I("committed_at")), runeHead(rb.S("title"), 80)), "release", "release", keys)
	}
	return nil
}

func (w *Watcher) inferredFinding(p Row, env, rule, title, detail, key string) (int64, error) {
	pr := w.proposal("set_variable", p.I("id"), env, key, detail, nil)
	pr.Text = fmt.Sprintf("Operator: check %s@%s (Argus cannot read the variables yet; this is inferred).", key, env)
	sev := "low"
	if env == "release" {
		sev = "medium"
	}
	id, _, _, err := UpsertFinding(w.tx, "variable", fmt.Sprintf("variable|%d|%s|%s", p.I("id"), env, rule), w.now, F{"project_id": p.I("id"),
		"project": p.S("path"), "env": env, "ref": env, "severity": sev, "klass": "variable", "rule": rule, "title": title,
		"detail": detail, "proposals": []Proposal{pr}}, false)
	return id, err
}

// Seed runs SeedLatest for every watched project.
func (w *Watcher) Seed() error {
	_, order, err := w.consumers()
	if err != nil {
		return err
	}
	for _, pid := range order {
		p, err := one(w.tx, "SELECT * FROM cicd_projects WHERE id=?", pid)
		if err != nil {
			return err
		}
		brs, err := w.branches(pid)
		if err != nil {
			return err
		}
		if _, err := w.SeedLatest(p, brs); err != nil {
			return err
		}
		if err := w.commit(); err != nil {
			return err
		}
	}
	return nil
}
