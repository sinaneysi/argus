package app

import (
	"path/filepath"
	"regexp"

	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// views_cicd.py: the CI/CD guardian pages (read-only).

// CicdData is ARGUS_STATE_DIR.
var CicdData = env("ARGUS_STATE_DIR", "/var/lib/argus")

var (
	cicdEnvs     = []string{"develop", "pre-release", "release"}
	cicdSevKind  = map[string]string{"critical": "fail", "high": "fail", "medium": "warn", "low": "ok", "info": "ok"}
	cicdClassLbl = map[string]string{"code": "code / build", "package": "package version", "lockfile": "lockfile drift", "variable": "variable",
		"registry": "registry", "runner": "runner / infra", "ssh": "ssh / ansible", "deploy": "deploy step",
		"health": "health check", "lock": "lock", "flaky": "flaky", "canceled": "canceled", "unknown": "unknown",
		"branch": "branch", "survey": "security / config", "release": "release deploy"}
	cicdStatusKind = map[string]string{"success": "ok", "failed": "fail", "canceled": "warn", "running": "ok", "manual": "warn",
		"waiting_for_resource": "warn", "pending": "warn", "created": "warn", "skipped": "ok"}

	cicdGroupRE  = regexp.MustCompile(`^[A-Za-z0-9/_.-]{1,80}\n?$`)
	cicdSevOrder = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}

	cicdBarOrder = []string{"success", "failed", "waiting", "canceled", "nopipeline", "nobranch"}
)

func cicdGet(m map[string]string, k, d string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return d
}

func cicdStatusDoc() *py.OMap {
	st := py.AsMap(ReadJSON(filepath.Join(CicdData, "cicd-status.json"), py.NewOMap()))
	if st == nil {
		return py.NewOMap()
	}
	return st
}

func cicdWriteNote() string {
	st := cicdStatusDoc()
	w := py.AsMap(py.Or(st.Get("watch"), nil)).Get("write")
	return py.OrS(w, "disabled (CICD_WRITE=0, no write token)")
}

func cicdStatusLine() ([]string, *py.OMap) {
	st := cicdStatusDoc()
	var bits []string
	for _, kl := range [][2]string{{"watch", "watched"}, {"scan", "estate scanned"}, {"audit", "audited"}} {
		s := py.AsMap(py.Or(st.Get(kl[0]), nil))
		if py.Truthy(s.Get("at")) {
			bits = append(bits, `<span>`+kl[1]+` `+Ago(s.Get("at"))+`</span>`)
		}
	}
	return bits, st
}

func cicdProposals(f Row) []*py.OMap {
	v, err := py.ParseJSON([]byte(py.OrS(f.Get("proposals"), "[]")))
	if err != nil {
		return nil
	}
	var out []*py.OMap
	for _, x := range py.AsList(v) {
		m := py.AsMap(x)
		if m == nil {
			m = py.NewOMap()
		}
		out = append(out, m)
	}
	return out
}

func cicdStuckMinutes() string {
	return env("CICD_STUCK_MINUTES", "10")
}

const cicdEnvRank = "CASE env WHEN 'release' THEN 0 WHEN 'pre-release' THEN 1 WHEN 'develop' THEN 2 ELSE 3 END"

func cicdSummaryLink(env, state, kind string) string {
	pairs := [][2]string{{"env", env}, {"state", state}}
	if kind != "app" {
		pairs = append(pairs, [2]string{"kind", kind})
	}
	return "/cicd?" + py.Urlencode(pairs) + "#matrix"
}

// CicdSummaryJSON is views_cicd.summary_json(qs).
func CicdSummaryJSON(qs py.QS) string {
	s := CSSummarize(qs.GetD("kind", "app"), 0)
	if s == nil {
		return py.Dumps(py.NewOMap("error", "no CI/CD data yet"), -1) + "\n"
	}
	return py.Dumps(CSAsJSON(s), 1) + "\n"
}

// --- the matrix -------------------------------------------------------------------------------------------

type cicdMatrix struct {
	projs []Row
	last  map[csKey]Row
	dep   map[csKey]Row
	brs   map[csKey]Row
	openF map[csKey][]Row
}

func cicdMatrixData() *cicdMatrix {
	m := &cicdMatrix{last: map[csKey]Row{}, dep: map[csKey]Row{}, brs: map[csKey]Row{}, openF: map[csKey][]Row{}}
	m.projs = Q("SELECT * FROM cicd_projects WHERE consumer=1 ORDER BY path")
	for _, r := range Q("SELECT p.* FROM cicd_pipelines p JOIN (SELECT project_id, ref, max(id) mid FROM cicd_pipelines " +
		"WHERE status != 'skipped' GROUP BY project_id, ref) m ON m.mid=p.id") {
		m.last[csKey{r.I("project_id"), r.Str("ref")}] = r
	}
	for _, r := range Q("SELECT d.* FROM cicd_deploys d JOIN (SELECT project_id, env, max(at) mat FROM cicd_deploys GROUP BY " +
		"project_id, env) m ON m.project_id=d.project_id AND m.env=d.env AND m.mat=d.at") {
		m.dep[csKey{r.I("project_id"), r.Str("env")}] = r
	}
	for _, r := range Q("SELECT * FROM cicd_branches") {
		m.brs[csKey{r.I("project_id"), r.Str("branch")}] = r
	}
	for _, r := range Q("SELECT project_id, env, kind, rule, severity FROM cicd_findings WHERE status='open'") {
		k := csKey{r.I("project_id"), r.S("env")}
		m.openF[k] = append(m.openF[k], r)
	}
	return m
}

// cicdCellFlags are the states of one project on one env branch: its newest pipeline and its open findings.
func cicdCellFlags(pl Row, fs []Row, env string) map[string]bool {
	flags := map[string]bool{}
	if pl != nil && pl.Get("status") == "failed" {
		flags["failing"] = true
	}
	for _, f := range fs {
		kd, rule := f.Get("kind"), f.Get("rule")
		if kd == "stuck" {
			flags["stuck"] = true
		}
		if kd == "branch" && rule == "drift" {
			flags["drift"] = true
			if env == "release" {
				flags["reldrift"] = true
			}
		}
		if kd == "branch" && rule == "stale" {
			flags["stale"] = true
		}
		if kd == "failure" || kd == "deploy" || kd == "variable" {
			flags["finding"] = true
		}
	}
	return flags
}

func cicdJSON(v any) any {
	x, err := py.ParseJSON([]byte(py.Str(v)))
	if err != nil {
		panic("json: " + err.Error())
	}
	return x
}

func cicdHead(rs []Row, n int) []Row {
	if len(rs) > n {
		return rs[:n]
	}
	return rs
}

func cicdShortPath(p string) string {
	if i := strings.Index(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

var cicdAuditRules = [][2]string{{"drift", "Commits that skipped the lower env"}, {"head-not-deployed", "Release HEAD not deployed"},
	{"no-deploy-30d", "No release deploy in 30 days"}, {"missing", "Missing env branches"}, {"unprotected", "Unprotected branches"},
	{"devpush", "Developers can push"}, {"default", "Default branch"}, {"include-ref", "Include ref not main"},
	{"stale", "Stale env branches"}}

func cicdID(qs py.QS) (int64, bool) {
	pid := qs.Get("id")
	if !py.IsDigit(pid) || py.Len(pid) > 9 {
		return 0, false
	}
	n, ok := py.ParseInt(pid)
	return n, ok
}

// CicdPrefill is views_cicd.prefill(sid).
func CicdPrefill(sid string) *py.OMap {
	if !py.IsDigit(sid) {
		return nil
	}
	n, _ := py.ParseInt(sid)
	rr := Q("SELECT * FROM cicd_findings WHERE id=?", n)
	if len(rr) == 0 {
		return nil
	}
	f := rr[0]
	props := cicdProposals(f)
	var lines []string
	for _, p := range props {
		s := "- " + py.OrS(p.Get("text"), "")
		if py.Truthy(p.Get("path")) {
			s += "\n  API: " + py.Str(p.Get("method")) + " /api/v4/" + py.Str(p.Get("path"))
		}
		s += "\n  Rule: " + py.OrS(p.Get("rule"), "")
		lines = append(lines, s)
	}
	fix := strings.Join(lines, "\n")
	fp := ""
	for _, p := range props {
		if py.Truthy(p.Get("fix_prompt")) {
			fp = py.Str(p.Get("fix_prompt"))
			break
		}
	}
	if fp == "" {
		fp = "From Argus's CI/CD guardian (finding #" + f.Str("id") + "). Proposed fix:\n" + fix + "\n\n" +
			"Production: proposal only. Never play, retry or run a production deploy (" + "project " + CICD_ENGINE_PROJECT_ID + ")."
	}
	pr := map[string]string{"high": "High", "medium": "Medium"}[f.S("severity")]
	if pr == "" {
		pr = "Low"
	}
	return py.NewOMap(
		"title", py.Head(cicdShortPath(f.S("project"))+": "+f.Str("title"), 200),
		"env", f.S("env"), "host", "", "priority", pr,
		"description", "CI/CD finding #"+f.Str("id")+" ("+f.Str("kind")+", "+py.OrS(f.Get("klass"), "-")+") on "+f.Str("project")+" "+f.S("ref")+". "+
			"Commit "+py.Head(f.S("sha"), 10)+" by "+py.OrS(f.Get("author"), "?")+" "+f.S("mr")+".",
		"evidence", py.Head(f.S("detail")+"\n\n"+f.S("evidence"), 16000),
		"fix_prompt", fp,
		"url", "/cicd/finding?id="+I2S(f.I("id")), "what", "CI/CD finding #"+f.Str("id"))
}
