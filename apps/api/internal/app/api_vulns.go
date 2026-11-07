package app

// The Delivery section's Check a repo and Vulnerabilities pages as JSON: GET /api/v1/cicd/repo, /vulns, /vulns/group and
// POST /api/v1/cicd/repo/check, /vulns/suppress. Same queries, request files and roles as the Go pages they replace.

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

func deliveryPost(path string, body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/cicd/repo"}
	form := apiForm(body)
	if path == "/api/v1/cicd/repo/check" {
		return apiResult(RepoPostCheck(c, form, sess))
	}
	return apiResult(CvePostSuppress(c, form, sess))
}

// --- Check a repo ------------------------------------------------------------------------------------------------

func apiRepoVerdict(v string) apiPill {
	if x, ok := repoVerdict[v]; ok {
		return apiPill{Text: x[1], Kind: x[0]}
	}
	return apiPill{Text: py.OrS(v, "-"), Kind: "warn"}
}

func apiRepoIndex(qs py.QS, value, errText string) map[string]any {
	rows := Q("SELECT r.* FROM cicd_readiness r JOIN (SELECT coalesce(project_id, path) k, max(id) mid FROM cicd_readiness " +
		"GROUP BY k) m ON m.mid=r.id ORDER BY r.checked_at DESC, r.id DESC LIMIT 2000")
	verdict := func(r Row) string {
		if _, ok := repoVerdict[r.S("verdict")]; ok && r.Get("verdict") != nil {
			return r.S("verdict")
		}
		return "error"
	}
	n := map[string]int{"ready": 0, "warn": 0, "not_ready": 0, "error": 0}
	for _, r := range rows {
		n[verdict(r)]++
	}
	l := newListQ("/cicd/repo", qs, []string{"q", "verdict", "sort"}, map[string]string{"sort": "checked"}, func(k, v string) bool {
		if k == "verdict" {
			_, ok := repoVerdict[v]
			return ok
		}
		return k == "sort" && In(v, "checked", "blockers", "project")
	})
	l.anchor = "repos"
	words := l.words()
	vN := map[string]int{}
	var shown []Row
	name := func(r Row) string { return py.OrS(py.Or(r.Get("path"), r.Get("project_id")), "") }
	for _, r := range rows {
		if !listMatch(words, name(r)) {
			continue
		}
		vN[verdict(r)]++
		if l.get("verdict") == "" || verdict(r) == l.get("verdict") {
			shown = append(shown, r)
		}
	}
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := shown[i], shown[j]
		switch l.get("sort") {
		case "blockers":
			if a.I("blockers") != b.I("blockers") {
				return a.I("blockers") > b.I("blockers")
			}
			return a.I("warnings") > b.I("warnings")
		case "project":
			return name(a) < name(b)
		}
		return false
	})
	from, to := listPage(&l, len(shown))
	list := []map[string]any{}
	for _, r := range shown[from:to] {
		list = append(list, map[string]any{"project": py.OrS(name(r), "-"), "href": "/cicd/repo?" + py.UrlencodeKV("project", name(r)),
			"verdict": apiRepoVerdict(py.OrS(r.Get("verdict"), "")), "blockers": r.I("blockers"), "warnings": r.I("warnings"), "at": r.I("checked_at"),
			"by": strings.SplitN(py.OrS(r.Get("source"), ""), ":", 2)[0]})
	}
	var vals [][2]string
	for _, k := range []string{"not_ready", "warn", "ready", "error"} {
		vals = append(vals, [2]string{k, repoVerdict[k][1]})
	}
	tiles := []apiTile{}
	for _, kt := range [][2]string{{"ready", "Ready"}, {"warn", "With warnings"}, {"not_ready", "Not ready"}} {
		tiles = append(tiles, apiTile{Label: kt[1], Figure: Itoa(n[kt[0]]), Hot: kt[0] == "not_ready" && n[kt[0]] > 0})
	}
	return map[string]any{"mode": "index", "value": value, "error": errText, "placeholder": repoGitlabURL + "/group/project", "tiles": tiles, "checked": len(rows), "rows": list,
		"facets":  []apiFacet{apiFacetOf("verdict", "Readiness", l.get("verdict"), vals, vN)},
		"list":    apiListOf(l, len(shown), len(rows), "checked repos", [][3]string{{"checked", "Checked", ""}, {"blockers", "Blockers", ""}, {"project", "Project", ""}}),
		"engine":  CICD_ENGINE_PROJECT,
		"banners": []apiBanner{}}
}

func apiRepoCheck(r *py.OMap, fix bool) map[string]any {
	st := r.Get("status")
	x, ok := repoStatus[py.OrS(st, "")]
	if _, isStr := st.(string); !ok || !isStr {
		x = [2]string{"warn", py.OrS(st, "?")}
	}
	sev := py.OrS(r.Get("severity"), "")
	out := map[string]any{"status": apiPill{Text: x[1], Kind: x[0]}, "title": py.OrS(r.Get("title"), ""), "id": py.OrS(r.Get("id"), ""), "severity": sev,
		"evidence": py.OrS(r.Get("evidence"), ""), "where": py.OrS(r.Get("where"), ""), "fix": nil}
	if fix && st == "fail" && py.Truthy(r.Get("fix")) {
		ff := py.OrS(r.Get("fix_file"), "")
		out["fix"] = map[string]any{"file": If(ff == "-", "", ff), "text": py.Str(r.Get("fix")), "why": py.OrS(r.Get("why"), ""), "source": py.OrS(r.Get("source"), "")}
	}
	return out
}

func apiRepoPipelines(rep *py.OMap) []map[string]any {
	pl, brs := py.AsMap(py.Or(rep.Get("pipelines"), py.NewOMap())), py.AsMap(py.Or(rep.Get("branches"), py.NewOMap()))
	out := []map[string]any{}
	for _, env := range cicdEnvs {
		b, p := py.AsMap(brs.Get(env)), py.AsMap(pl.Get(env))
		if b == nil {
			out = append(out, map[string]any{"env": env, "branch": false})
			continue
		}
		row := map[string]any{"env": env, "branch": true, "protected": py.Truthy(b.Get("protected")), "devPush": py.Truthy(b.Get("dev_push")), "status": nil, "extra": "", "sha": "-", "at": 0}
		if p.Len() > 0 {
			row["status"], row["sha"], row["at"] = apiCicdStatus(p.Get("status")), py.OrS(p.Get("sha"), "-"), py.I(p.Get("at"))
			if py.Truthy(p.Get("klass")) {
				k := py.Str(p.Get("klass"))
				row["extra"] = cicdGet(cicdClassLbl, k, k) + ": " + py.OrS(p.Get("finding"), "")
			}
		}
		out = append(out, row)
	}
	return out
}

// apiRepoJSON is /cicd/repo: the list of checked repos, or one repo's report.
func apiRepoJSON(qs py.QS, _ *Session) (any, *apiErr) {
	raw := qs.Get("project")
	if py.Strip(raw) == "" {
		return apiRepoIndex(qs, "", ""), nil
	}
	target, errText := RepoParseTarget(raw)
	if errText != "" {
		return apiRepoIndex(qs, raw, errText), nil
	}
	row := repoLatest(target)
	if row == nil && !repoIDRE.MatchString(target) {
		if p := Q("SELECT id FROM cicd_projects WHERE lower(path)=?", strings.ToLower(target)); len(p) > 0 {
			row = repoLatest(I2S(p[0].I("id")))
		}
	}
	banners := []apiBanner{}
	if qs.Get("sent") == "1" {
		banners = append(banners, apiBanner{Text: "Check requested. It reads the repo from GitLab in about half a minute; this page reloads until it is done.", Kind: "ok"})
	}
	can, msg, busy := repoReqState(target, row)
	out := map[string]any{"mode": "repo", "target": target, "name": repoLast(target), "path": target, "banners": banners, "placeholder": repoGitlabURL + "/group/project",
		"check": map[string]any{"can": can, "busy": busy, "msg": msg, "label": If(row != nil, "Re-check", "Check this repo")}, "refresh": busy, "report": nil}
	if row == nil {
		return out, nil
	}
	rep := repoReportOf(row)
	p := mapOr(rep.Get("project"))
	path := py.OrS(py.Or(p.Get("path"), row.Get("path")), target)
	verdict := py.OrS(row.Get("verdict"), "")
	summary := []string{}
	for i, x := range py.AsList(py.Or(rep.Get("summary"), []any{})) {
		if i < 3 {
			summary = append(summary, py.Str(x))
		}
	}
	report := map[string]any{"verdict": apiRepoVerdict(verdict), "blockers": row.I("blockers"), "warnings": row.I("warnings"), "passed": row.I("passed"), "summary": summary,
		"checkedAt": row.I("checked_at"), "error": ""}
	out["name"], out["path"], out["report"] = repoLast(path), path, report
	if verdict == "error" {
		report["error"] = py.OrS(row.Get("error"), "")
		return out, nil
	}
	open, rest := []map[string]any{}, []map[string]any{}
	for _, x := range py.AsList(py.Or(rep.Get("results"), []any{})) {
		r := py.AsMap(x)
		if r == nil {
			continue
		}
		if In(py.Str(r.Get("status")), "fail", "unknown") {
			open = append(open, apiRepoCheck(r, true))
		} else {
			rest = append(rest, apiRepoCheck(r, false))
		}
	}
	hm := mapOr(rep.Get("handoff"))
	var opts []*py.OMap
	for _, o := range py.AsList(py.Or(hm.Get("options"), []any{})) {
		if m := py.AsMap(o); m != nil {
			opts = append(opts, m)
		}
	}
	flags := []string{}
	for _, x := range py.AsList(py.Or(hm.Get("flags"), []any{})) {
		flags = append(flags, py.Str(x))
	}
	if len(opts) > 0 {
		h := Handoff{Options: opts, Prompt: py.OrS(hm.Get("prompt"), ""), State: py.OrS(hm.Get("state"), "missing"), Flags: flags}
		report["fix"] = apiFix(h, "develop", "repo="+I2S(row.I("id")), "", "Repo files go through a branch fix/ci-readiness and an MR; GitLab settings stay with the operator.")
	}
	kind := py.OrS(rep.Get("kind"), "-")
	if py.Truthy(rep.Get("ptype")) {
		kind += " · " + py.Str(rep.Get("ptype"))
	}
	exs := []string{}
	for _, x := range py.AsList(py.Or(rep.Get("executors"), []any{})) {
		exs = append(exs, py.Str(x))
	}
	calls := I2S(row.I("calls"))
	if py.Truthy(rep.Get("cached")) {
		calls += " (files from the cache: same sha)"
	}
	rail := []apiFact{{Label: "Project", Value: path, Href: repoGitlabURL + "/" + py.Quote(path), Mono: true}, {Label: "Kind", Value: kind},
		{Label: "Executors", Value: py.OrS(strings.Join(exs, ", "), "-")},
		{Label: "Checked ref", Value: py.OrS(row.Get("ref"), "-") + " @ " + py.Head(py.OrS(row.Get("sha"), ""), 10), Mono: true},
		{Label: "CI variables", Value: py.OrS(rep.Get("vars"), "-")}, {Label: "Rules", Value: "config/cicd-requirements.json v" + I2S(row.I("rules_version"))},
		{Label: "Checked", Value: Text(When(row.Get("checked_at"))) + " (" + Ago(row.Get("checked_at")) + ")"}, {Label: "GitLab calls", Value: calls}}
	cicd := apiFact{Label: "CI/CD", Value: "not a consumer yet"}
	if row.T("project_id") {
		if cid := Q("SELECT id FROM cicd_projects WHERE id=?", row.I("project_id")); len(cid) > 0 {
			cicd = apiFact{Label: "CI/CD", Value: "project page", Href: "/cicd/project?id=" + I2S(cid[0].I("id"))}
		}
	}
	report["open"], report["rest"], report["pipelines"], report["rail"] = open, rest, apiRepoPipelines(rep), append(rail, cicd)
	report["pipelinesFrom"] = If(rep.Get("pipelines_from") == "ops.db", "from Argus's CI/CD guardian", "read from GitLab")
	return out, nil
}

// --- Vulnerabilities ---------------------------------------------------------------------------------------------

func apiCveExposure(exp, via string) map[string]any {
	return map[string]any{"word": If(In(exp, "customer-facing", "internet"), exp, "internal"), "hot": exp == "customer-facing", "via": via}
}

func apiCveAffects(as []cveAff, limit int) []map[string]any {
	out := []map[string]any{}
	for i, a := range as {
		if i >= limit {
			break
		}
		row := map[string]any{"host": a.host, "hostHref": "", "env": a.env, "service": a.service, "serviceHref": "", "unit": cveUnitText(a),
			"exposure": apiCveExposure(a.exposure, py.Head(a.via, 80)), "version": a.version, "how": py.Head(a.how, 90)}
		if a.vm != 0 {
			row["hostHref"] = "/inventory/view?id=" + I2S(a.vm)
		}
		if a.unit != "*" && a.unit != "-" && a.unit != "" {
			row["serviceHref"] = cveLink("svc", a.service) + "#components"
		}
		out = append(out, row)
	}
	return out
}

func apiCveGroup(g Row, aff map[string][]cveAff) map[string]any {
	as := aff[g.Str("group_key")]
	sla := g.I("sla")
	due := ""
	if sla != 0 {
		due = Strftime("2006-01-02", float64(sla))
	}
	return map[string]any{"href": cveLink("group", g.Str("group_key")), "pri": g.S("pri"), "priKind": cicdGet(cvePriCls, g.S("pri"), "warn"), "name": g.Str("component_name"),
		"versions": cveVersionsText(g), "kev": g.I("kev"), "internet": g.T("internet"), "kind": cveKindOf(g.Str("kind")), "ecosystem": g.S("ecosystem"),
		"class": g.Str("env_max_class"), "vulns": g.I("n"), "targets": g.I("targets"), "fixed": g.S("fixed"), "due": due, "late": sla != 0 && float64(sla) < Now(),
		"affects": cveAffLabel(as), "affected": apiCveAffects(as, 12), "moreAffected": max(0, len(as)-12), "moreHref": cveLink("group", g.Str("group_key")) + "#affected"}
}

func apiCveCoverage(st, cov, feeds *py.OMap) map[string]any {
	ci := func(m *py.OMap, k string) int64 { return py.I(m.GetD(k, int64(0))) }
	fh := mapOr(feeds.Get("hosts"))
	hk := append([]string(nil), fh.Keys()...)
	sort.Strings(hk)
	hosts := []map[string]any{}
	for _, h := range hk {
		v := mapOr(fh.Get(h))
		ok := py.Truthy(v.Get("ok"))
		e := py.OrS(v.Get("error"), "")
		if e == "" && !ok {
			e = py.Str(v.Get("status"))
		}
		hosts = append(hosts, map[string]any{"host": h, "state": If(ok, "reachable", "not reachable"), "requests": py.I(v.Get("n")), "error": e})
	}
	miss := []map[string]any{}
	for _, m := range Q("SELECT name, env, error FROM cve_hosts WHERE COALESCE(pkgs_n,0)=0 ORDER BY env, name") {
		miss = append(miss, map[string]any{"name": m.Str("name"), "env": m.S("env"), "why": m.S("error")})
	}
	km := mapOr(cov.Get("kinds"))
	kk := append([]string(nil), km.Keys()...)
	sort.Strings(kk)
	kinds := []string{}
	for _, k := range kk {
		kinds = append(kinds, k+" "+py.Str(km.Get(k)))
	}
	// feed queries
	qc := mapOr(feeds.Get("queries"))
	nvd := []map[string]any{}
	for _, v := range py.AsList(feeds.Get("nvd")) {
		m := mapOr(v)
		total := "?"
		if m.Get("total") != nil {
			total = I2S(py.I(m.Get("total")))
		}
		nvd = append(nvd, map[string]any{"product": py.OrS(m.Get("prefix"), ""), "state": py.OrS(m.Get("status"), ""), "cves": I2S(py.I(m.GetD("results", int64(0)))) + " / " + total,
			"pages": py.I(m.GetD("pages", int64(0))), "why": py.Head(py.OrS(m.Get("reason"), ""), 200)})
	}
	failed := []map[string]any{}
	for _, v := range py.AsList(mapOr(st.Get("feed_problems")).Get("list")) {
		m := mapOr(v)
		failed = append(failed, map[string]any{"kind": py.OrS(m.Get("kind"), ""), "subject": py.OrS(m.Get("subject"), ""), "why": py.Head(py.OrS(m.Get("reason"), ""), 240)})
	}
	var queries map[string]any
	if qc.Len() > 0 || len(nvd) > 0 || len(failed) > 0 {
		cs := []string{}
		for _, k := range []string{"ok", "cached", "stale", "failed"} {
			if n := py.I(qc.GetD(k, int64(0))); n > 0 {
				cs = append(cs, I2S(n)+" "+k)
			}
		}
		queries = map[string]any{"line": py.OrS(cveFeedFailedLine(st), "feed queries failed: 0"), "requests": py.OrS(strings.Join(cs, " · "), "-"), "nvd": nvd, "failed": failed}
	}
	// containers and blind spots
	cc := mapOr(cov.Get("containers"))
	conts := Q("SELECT * FROM cve_containers ORDER BY CASE source WHEN 'declared' THEN 1 ELSE 0 END, env DESC, host, name")
	blind := Q("SELECT * FROM cve_blind_spots")
	var containers map[string]any
	if len(conts) > 0 || len(blind) > 0 || cc.Len() > 0 {
		rows := []map[string]any{}
		for _, c := range conts {
			row := map[string]any{"host": c.Str("host"), "hostHref": "", "env": c.S("env"), "name": c.Str("name"), "image": c.Str("image"), "digest": py.Head(c.S("digest"), 19),
				"source": cveSourceText(c.S("source")), "inside": Text(cveAnalysisText(c.S("analysis")))}
			if c.I("vm_id") != 0 {
				row["hostHref"] = "/inventory/view?id=" + I2S(c.I("vm_id")) + "#vulns"
			}
			rows = append(rows, row)
		}
		byKind := map[string][]Row{}
		for _, b := range blind {
			byKind[b.Str("kind")] = append(byKind[b.Str("kind")], b)
		}
		spots, counts := []map[string]any{}, []string{}
		for _, k := range cveBlindOrder {
			if n := len(byKind[k]); n > 0 {
				counts = append(counts, cveBlindLabel[k]+" "+Itoa(n))
			}
			for _, b := range byKind[k] {
				row := map[string]any{"label": py.OrS(cveBlindLabel[k], k), "subject": b.Str("subject"), "host": b.S("host"), "hostHref": "", "why": py.Head(b.S("reason"), 240)}
				if b.I("vm_id") != 0 {
					row["hostHref"] = "/inventory/view?id=" + I2S(b.I("vm_id"))
				}
				spots = append(spots, row)
			}
		}
		note := I2S(ci(cc, "hosts_with_packages")) + " hosts have a package list · " + I2S(ci(cc, "hosts_with_containers")) + " hosts have container images (" +
			I2S(ci(cc, "hosts_with_containers_observed")) + " observed through cAdvisor or docker, the rest declared in config) · " + I2S(ci(cc, "images_analysed")) + " of " +
			I2S(ci(cc, "images")) + " images analysed" + If(ci(cc, "images_failed") > 0, " ("+I2S(ci(cc, "images_failed"))+" could not be)", "") +
			". Images are read from the registry by root (layers streamed, never run); a declared image is what config says runs there, not what was seen."
		containers = map[string]any{"note": note, "rows": rows, "blind": spots, "blindCounts": strings.Join(counts, " · ")}
	}
	return map[string]any{"scanned": ci(cov, "scanned"), "hosts": ci(cov, "hosts"), "withAccess": ci(cov, "with_access"), "kinds": py.OrS(strings.Join(kinds, ", "), "-"),
		"mode": py.OrS(feeds.Get("mode"), "-"), "feedHosts": hosts, "missing": miss, "queries": queries, "containers": containers}
}

// apiVulnsJSON is /vulns; the page reads /vulns/group for a link with ?id= or ?group=.
func apiVulnsJSON(qs py.QS, _ *Session) (any, *apiErr) {
	sel := cveSelOf(qs)
	st := ReadJSONMap(filepath.Join(CveData, "cve-status.json"))
	if st == nil {
		st = py.NewOMap()
	}
	cnt, cov, feeds := mapOr(st.Get("counts")), mapOr(st.Get("coverage")), mapOr(st.Get("feeds"))
	ci := func(m *py.OMap, k string) int64 { return py.I(m.GetD(k, int64(0))) }
	var fixable []Row
	for _, g := range cveGroups(cveSel{}) {
		if g.I("n") > g.I("nofix") {
			fixable = append(fixable, g)
		}
	}
	fixable = cicdHead(fixable, 10)
	rows := cveGroups(sel)
	shown := Capped(rows, qs)
	var keys []string
	for _, g := range append(append([]Row{}, fixable...), shown...) {
		keys = append(keys, g.Str("group_key"))
	}
	aff := cveAffectsByGroup(keys)
	groups := func(rs []Row) []map[string]any {
		out := []map[string]any{}
		for _, g := range rs {
			out = append(out, apiCveGroup(g, aff))
		}
		return out
	}
	tile := func(label, fig, sub, href string, hot, on bool) apiTile {
		t := apiTile{Label: label, Figure: fig, Sub: sub, Href: href, Hot: hot, Current: on}
		if on {
			t.Href, t.Sub = "/vulns", "shown below · show all"
		}
		return t
	}
	cc := mapOr(cov.Get("containers"))
	tiles := []apiTile{tile("P1", I2S(ci(cnt, "P1")), I2S(ci(cnt, "P1_fix"))+" with a fix today", cveLink("pri", "P1"), ci(cnt, "P1") > 0, sel.pri == "P1"),
		tile("P2", I2S(ci(cnt, "P2")), "next", cveLink("pri", "P2"), ci(cnt, "P2") > 0, sel.pri == "P2"),
		tile("Known exploited", I2S(ci(cnt, "kev")), "in CISA KEV", cveLink("kev", "1"), ci(cnt, "kev") > 0, sel.kev != ""),
		tile("End of life", I2S(ci(cnt, "eol")), "unsupported releases", cveLink("eol", "1"), false, sel.eol != ""),
		tile("Coverage", I2S(ci(cov, "scanned"))+"/"+I2S(ci(cov, "hosts")), "hosts with a package list · "+
			If(cc.Len() > 0, I2S(ci(cc, "hosts_with_containers"))+" with container images · ", "")+I2S(ci(cov, "components"))+" components", "#coverage", false, false)}
	banners := []apiBanner{}
	errs := []string{}
	if m := py.AsMap(st.Get("errors")); m != nil {
		for _, k := range m.Keys() {
			errs = append(errs, k+": "+py.Str(m.Get(k)))
		}
	}
	if m := py.AsMap(feeds.Get("errors")); m != nil {
		for _, k := range m.Keys() {
			errs = append(errs, "fetch "+k+": "+py.Str(m.Get(k)))
		}
	}
	if len(errs) > 0 {
		banners = append(banners, apiBanner{Text: "Feed problems: " + strings.Join(errs[:min(4, len(errs))], "; "), Kind: "warn"})
	}
	if at := st.Get("at"); py.Truthy(at) && Now()-py.Num(at) > 3*86400 {
		banners = append(banners, apiBanner{Text: "The last vulnerability scan is " + I2S(int64((Now()-py.Num(at))/86400)) + " days old.", Kind: "warn"})
	}
	if fq := cveFeedFailedLine(st); fq != "" {
		banners = append(banners, apiBanner{Text: fq + " — their CVEs are not matched (see Coverage and feeds).", Kind: "warn"})
	}
	risk := []map[string]any{}
	for _, r := range Q("SELECT * FROM cve_service_risk WHERE p1+p2+kev > 0 ORDER BY p1 DESC, kev DESC, p2 DESC, hosts DESC, service LIMIT 10") {
		hs := []string{}
		if v, err := py.ParseJSON([]byte(py.OrS(r.Get("host_list"), "[]"))); err == nil {
			for _, x := range py.AsList(v) {
				hs = append(hs, py.Str(x))
			}
		}
		hostS := strings.Join(hs, ", ")
		if len(hs) > 3 {
			hostS = strings.Join(hs[:3], ", ") + " +" + Itoa(len(hs)-3)
		}
		row := map[string]any{"service": r.Str("service"), "href": cveLink("svc", r.Str("service")) + "#components", "on": sel.svc == r.Str("service"), "hosts": r.I("hosts"), "hostList": hostS,
			"p1": r.I("p1"), "p2": r.I("p2"), "kev": r.I("kev"), "exposure": apiCveExposure(r.S("exposure"), ""), "fix": nil}
		if r.T("top_group") {
			row["fix"] = apiLink{Text: r.Str("top_component"), Href: cveLink("group", r.Str("top_group"))}
		}
		risk = append(risk, row)
	}
	svcs := []string{}
	for _, r := range Q("SELECT service FROM cve_service_risk ORDER BY service") {
		svcs = append(svcs, r.Str("service"))
	}
	if sel.svc != "" && !In(sel.svc, svcs...) {
		svcs = append(svcs, sel.svc)
		sort.Strings(svcs)
	}
	empty := "No component matches these filters."
	if (sel == cveSel{}) && ci(cov, "scanned") == 0 {
		empty = "No host has a package list yet, so nothing can be matched. Findings appear after the next scan."
	} else if (sel == cveSel{}) {
		empty = "No open finding in the " + I2S(ci(cov, "scanned")) + " scanned hosts."
	}
	out := map[string]any{"scanned": st.Len() > 0, "host": ARGUS_HOSTNAME, "p1": ci(cnt, "P1"), "p2": ci(cnt, "P2"), "kev": ci(cnt, "kev"), "components": ci(cnt, "components"), "feedAt": "never",
		"banners": banners, "tiles": tiles, "fixFirst": groups(fixable), "risk": risk, "rows": groups(shown), "total": len(rows), "more": nil, "empty": empty, "filtered": sel != cveSel{},
		"sel":     map[string]string{"env": sel.env, "pri": sel.pri, "kind": sel.kind, "svc": sel.svc, "host": sel.host, "kev": sel.kev, "state": sel.state, "eol": sel.eol},
		"choices": map[string]any{"env": cveClasses, "pri": cvePri, "kind": cveKinds, "svc": svcs}, "coverage": apiCveCoverage(st, cov, feeds)}
	if py.Truthy(feeds.Get("at")) {
		out["feedAt"] = Text(When(feeds.Get("at")))
	}
	if len(rows) > len(shown) {
		pairs := qs.Pairs("env", "pri", "kind", "svc", "host", "kev", "eol", "state")
		out["more"] = apiLink{Text: "Show all " + Itoa(len(rows)) + " components", Href: "/vulns?" + py.Urlencode(append(pairs, [2]string{"all", "1"})) + "#components"}
	}
	return out, nil
}

// apiVulnGroupJSON is one component group, by ?group= or by a finding's ?id=.
func apiVulnGroupJSON(qs py.QS, sess *Session) (any, *apiErr) {
	key := qs.Get("group")
	if fid := qs.Get("id"); key == "" && py.IsDigit(fid) {
		if n, ok := py.ParseInt(fid); ok {
			if r := Q("SELECT group_key FROM cve_findings WHERE id=?", n); len(r) > 0 {
				key = r[0].Str("group_key")
			}
		}
	}
	missing := &apiErr{404, "no such component"}
	if !cveGroupRe.MatchString(key) {
		return nil, missing
	}
	fs := Q("SELECT * FROM cve_findings WHERE group_key=? AND state IN ('open','suppressed') ORDER BY state, priority, score DESC", key)
	if len(fs) == 0 {
		return nil, missing
	}
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/vulns?" + py.UrlencodeKV("group", key)}
	admin := sess.Role == "admin"
	f0 := fs[0]
	envv := map[string]string{"production": "release", "staging": "pre-release"}[f0.Str("env_max_class")]
	if envv == "" {
		envv = "develop"
	}
	affAll := cveAffectsByGroup([]string{key})[key]
	affBy := map[string][]cveAff{}
	for _, a := range affAll {
		affBy[a.target] = append(affBy[a.target], a)
	}
	vulns := []map[string]any{}
	for _, f := range Capped(fs, qs) {
		var mine []cveAff
		if v, err := py.ParseJSON([]byte(py.OrS(f.Get("targets"), "[]"))); err == nil {
			for _, x := range py.AsList(v) {
				if m := py.AsMap(x); m != nil {
					mine = append(mine, affBy[py.Str(m.Get("target"))]...)
				}
			}
		}
		cid := f.Str("vuln_key")
		href := f.S("url")
		if href == "" && strings.HasPrefix(cid, "CVE-") {
			href = "https://nvd.nist.gov/vuln/detail/" + cid
		}
		sev := py.OrS(f.S("severity"), "-")
		if !f.IsNull("cvss") {
			sev += " · CVSS " + py.F(f.F("cvss"), 1)
		}
		if !f.IsNull("epss") {
			sev += " · EPSS " + py.F(f.F("epss"), 2)
		}
		fixed := f.S("fixed_version")
		if fixed == "" {
			fixed = If(f.T("nofix"), "no fix yet", "-")
		}
		due := "-"
		if f.T("sla_due") {
			due = py.Head(Text(When(f.Get("sla_due"))), 10)
		}
		vulns = append(vulns, map[string]any{"pri": f.S("priority"), "priKind": cicdGet(cvePriCls, f.S("priority"), "warn"), "score": py.G(f.F("score"), 6), "id": cid,
			"href": If(strings.HasPrefix(href, "https://"), href, ""), "kev": f.T("kev"), "suppressed": f.Str("state") == "suppressed", "title": py.Head(f.S("title"), 160),
			"why": cveWhy(f.Get("breakdown")), "affects": cveAffLabel(mine), "severity": sev, "fixed": fixed, "due": due})
	}
	seen := map[string]map[string]any{}
	order := []string{}
	for _, f := range fs {
		v, err := py.ParseJSON([]byte(py.OrS(f.Get("targets"), "[]")))
		if err != nil {
			continue
		}
		for _, x := range py.AsList(v) {
			m := py.AsMap(x)
			if m == nil {
				continue
			}
			t := py.Str(m.Get("target"))
			if _, ok := seen[t]; ok {
				continue
			}
			row := map[string]any{"target": t, "href": "", "version": py.OrS(m.Get("version"), ""), "env": py.OrS(m.Get("env"), "")}
			if vm := py.I(m.Get("vm_id")); vm != 0 {
				row["href"] = "/inventory/view?id=" + I2S(vm)
			}
			seen[t] = row
			order = append(order, t)
		}
	}
	sort.Strings(order)
	targets := []map[string]any{}
	for i, t := range order {
		if i < 200 {
			targets = append(targets, seen[t])
		}
	}
	var lazy Lazy
	lzOK := false
	var fix map[string]any
	if fx := Q("SELECT * FROM cve_fixes WHERE group_key=?", key); len(fx) > 0 {
		if lazy, lzOK = LazyParse(fx[0].S("md")); lzOK {
			lazy.Source = "report"
		}
		state := py.OrS(fx[0].S("fix_state"), "missing")
		fix = apiFix(Handoff{Options: FHLoadOptions(fx[0].Get("fix_options")), Prompt: fx[0].S("fix_prompt"), State: state, Flags: []string{}}, envv, "cve="+I2S(f0.I("id")), "", "")
	}
	rules := []map[string]any{}
	for _, r := range Q("SELECT * FROM cve_suppressions WHERE active=1 AND (component='' OR component=? OR ? GLOB component)", f0.Str("component_name"), f0.Str("component_name")) {
		rules = append(rules, map[string]any{"id": r.Str("id"), "vuln": py.OrS(r.S("vuln_key"), "*"), "component": py.OrS(r.S("component"), "*"), "target": r.S("target"), "env": r.S("env"),
			"expires": r.Str("expires"), "by": r.Str("created_by"), "reason": r.Str("reason")})
	}
	banners := []apiBanner{}
	if qs.Get("sent") == "suppress" {
		banners = append(banners, apiBanner{Text: "Suppression requested. It applies at the next scan (Scan now runs it).", Kind: "ok"})
	}
	nOpen := 0
	for _, f := range fs {
		if f.Str("state") == "open" {
			nOpen++
		}
	}
	name := f0.Str("component_name")
	out := map[string]any{"key": key, "name": name, "pri": f0.S("priority"), "priKind": cicdGet(cvePriCls, f0.S("priority"), "warn"), "kind": cveKindOf(f0.Str("kind")), "ecosystem": f0.Str("ecosystem"),
		"class": f0.Str("env_max_class"), "open": nOpen, "targetCount": len(order), "affects": strings.TrimPrefix(cveAffLabel(affAll), "Affects: "), "banners": banners,
		"summary": apiLazy(lazy, lzOK), "problem": apiProblemPanel(c, qs, "cve_group", key, name+" vulnerabilities", "CVE group", f0.I("incident_id")), "vulns": vulns, "more": nil,
		"affected": apiCveAffects(affAll, len(affAll)), "fix": fix, "targets": targets, "rules": rules, "canSuppress": admin, "suppress": nil}
	if admin {
		out["suppress"] = map[string]any{"vuln": f0.Str("vuln_key"), "component": name, "env": f0.Str("env_max_class"), "until": Strftime("2006-01-02", Now()+30*86400)}
	}
	if len(fs) > len(vulns) {
		out["more"] = apiLink{Text: "Show all " + Itoa(len(fs)) + " vulnerabilities", Href: "/vulns?" + py.Urlencode([][2]string{{"group", key}, {"all", "1"}})}
	}
	return out, nil
}
