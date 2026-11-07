package app

// Port of app/views_cve.py: Security -> Vulnerabilities (/vulns), a thin read-only view of what bin/cve_match.py
// stored in ops.db (cve_findings, cve_targets, cve_fixes, cve_suppressions) and cve-status.json; the suppression
// request file (settings-in/cve-suppressions/, applied and audited by bin/cve_match.py); the VM card and the
// Inventory CVE column. No matching or scoring here.

import (
	"os"
	"path/filepath"
	"regexp"

	"strings"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/py"
)

var cvePri = []string{"P1", "P2", "P3", "P4"}
var cvePriCls = map[string]string{"P1": "fail", "P2": "fail", "P3": "warn", "P4": "ok"}
var cveClasses = []string{"production", "staging", "dev"}
var cveKinds = []string{"package", "kernel", "npm", "composer", "software", "runtime", "database", "image", "base-image", "os"}
var cveKindLabel = map[string]string{"package": "OS package", "kernel": "kernel", "npm": "npm dependency",
	"software": "server software", "runtime": "runtime", "database": "database", "image": "container image",
	"base-image": "CI base image", "os": "operating system", "composer": "Composer package (PHP)"}

var (
	cveGroupRe   = regexp.MustCompile(`^[A-Za-z0-9:._@/+ -]{1,80}\|[A-Za-z0-9:._@/+-]{1,214}\|(production|staging|dev)$`)
	cveHostRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{0,80}$`)
	cveVersionRe = regexp.MustCompile(`"([^"]+)"`)
	cveGlobRe    = regexp.MustCompile(`^[A-Za-z0-9*?._:/@+-]{0,120}$`)
	cveIDRe      = regexp.MustCompile(`^[0-9a-f]{12}$`)
	cveDateRe    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func cveKindOf(k string) string {
	if l, ok := cveKindLabel[k]; ok {
		return l
	}
	return k
}

func cvePriBadge(p string) string {
	cls, ok := cvePriCls[p]
	if !ok {
		cls = "warn"
	}
	if p == "" {
		p = "?"
	}
	return `<span class="badge ` + cls + `" title="priority">` + E(p) + `</span>`
}

type cveSel struct{ env, pri, kind, kev, eol, host, state, svc string }

func cveSelOf(qs py.QS) cveSel {
	var s cveSel
	if v := qs.Get("env"); In(v, cveClasses...) {
		s.env = v
	}
	if v := qs.Get("pri"); In(v, cvePri...) {
		s.pri = v
	}
	if v := qs.Get("kind"); In(v, cveKinds...) {
		s.kind = v
	}
	if qs.Get("kev") == "1" {
		s.kev = "1"
	}
	if qs.Get("eol") == "1" {
		s.eol = "1"
	}
	if v := qs.Get("host"); cveHostRe.MatchString(v) {
		s.host = py.Head(v, 80)
	}
	if qs.Get("state") == "suppressed" {
		s.state = "suppressed"
	}
	if v := py.Strip(qs.Get("svc")); cveSvcRe.MatchString(v) {
		s.svc = v
	}
	return s
}

// cveLink is link(sel, **kw) with an empty sel: the keys in the order given, empty values dropped.
func cveLink(kv ...string) string {
	var pairs [][2]string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			pairs = append(pairs, [2]string{kv[i], kv[i+1]})
		}
	}
	if len(pairs) == 0 {
		return "/vulns"
	}
	return "/vulns?" + py.Urlencode(pairs)
}

func cveGroups(s cveSel) []Row {
	st := s.state
	if st == "" {
		st = "open"
	}
	where, args := []string{"f.state=?"}, []any{st}
	for _, kc := range [][2]string{{s.env, "f.env_max_class"}, {s.pri, "f.priority"}, {s.kind, "f.kind"}} {
		if kc[0] != "" {
			where = append(where, kc[1]+"=?")
			args = append(args, kc[0])
		}
	}
	if s.kev != "" {
		where = append(where, "f.kev=1")
	}
	if s.eol != "" {
		where = append(where, "f.vuln_key LIKE 'EOL-%'")
	}
	if s.host != "" {
		where = append(where, "(EXISTS (SELECT 1 FROM cve_targets t WHERE t.finding_id=f.id AND t.target LIKE ?) OR "+
			"EXISTS (SELECT 1 FROM cve_affects a WHERE a.group_key=f.group_key AND a.host LIKE ?))")
		args = append(args, "%"+s.host+"%", "%"+s.host+"%")
	}
	if s.svc != "" {
		where = append(where, "EXISTS (SELECT 1 FROM cve_affects a WHERE a.group_key=f.group_key AND a.service=?)")
		args = append(args, s.svc)
	}
	return Q("SELECT f.group_key, f.kind, f.component_name, f.ecosystem, f.env_max_class, count(*) n, "+
		"max(f.score) score, min(f.priority) pri, sum(f.kev) kev, max(f.n_targets) targets, "+
		"group_concat(DISTINCT f.versions) versions, max(f.exposure='internet') internet, "+
		"min(f.sla_due) sla, max(f.fixed_version) fixed, sum(f.nofix) nofix "+
		"FROM cve_findings f WHERE "+strings.Join(where, " AND ")+" GROUP BY f.group_key "+
		"ORDER BY min(f.priority), max(f.score) DESC, sum(f.kev) DESC", args...)
}

func cveVersionsText(g Row) string {
	var vs []string
	for _, chunk := range strings.Split(py.OrS(g.Get("versions"), ""), "],[") {
		for _, m := range cveVersionRe.FindAllStringSubmatch(chunk, -1) {
			if !In(m[1], vs...) {
				vs = append(vs, m[1])
			}
		}
	}
	if len(vs) > 3 {
		return strings.Join(vs[:3], ", ") + " +" + Itoa(len(vs)-3)
	}
	return strings.Join(vs, ", ")
}

func mapOr(v any) *py.OMap {
	if m := py.AsMap(v); m != nil {
		return m
	}
	return py.NewOMap()
}

// CveData is views_cve.DATA (ARGUS_STATE_DIR, default /var/lib/argus): where cve-status.json is.
var CveData = env("ARGUS_STATE_DIR", "/var/lib/argus")

func cveWhy(b any) string {
	v, err := py.ParseJSON([]byte(py.OrS(b, "{}")))
	if err != nil {
		return ""
	}
	m := py.AsMap(v)
	if m == nil {
		return ""
	}
	var parts []string
	for _, k := range m.Keys() {
		l := py.AsList(m.Get(k))
		if len(l) < 2 {
			continue
		}
		n := py.Num(l[0])
		plus := ""
		if n > 0 && k != "base" {
			plus = "+"
		}
		parts = append(parts, plus+py.G(n, 6)+" "+py.Str(l[1]))
	}
	return strings.Join(parts, " · ")
}

// CvePostSuppress is views_cve.post_suppress: admins write one request file; bin/cve_match.py validates, applies and
// audits it. The app never writes ops.db.
func CvePostSuppress(c *Ctx, form py.QS, sess *auth.Session) (int, string) {
	get := func(k string) string { return py.Strip(form.Get(k)) }
	if sess.Role != "admin" {
		return 403, "Only admins can suppress a vulnerability."
	}
	action, back := get("action"), get("back")
	if !In(action, "add", "remove") || (back != "" && !cveGroupRe.MatchString(back)) {
		return 400, "Invalid request."
	}
	req := py.NewOMap("action", action, "by", sess.Username, "role", sess.Role, "at", NowInt())
	if action == "remove" {
		if !cveIDRe.MatchString(get("id")) {
			return 400, "Invalid request."
		}
		req.Set("id", get("id"))
	} else {
		for _, k := range []string{"vuln_key", "component", "target", "env"} {
			if !cveGlobRe.MatchString(get(k)) {
				return 400, "Invalid " + k + "."
			}
			req.Set(k, get(k))
		}
		reason := strings.Join(py.Split(get("reason")), " ")
		if n := len([]rune(reason)); n < 20 || n > 500 {
			return 400, "The reason needs at least 20 characters."
		}
		if !cveDateRe.MatchString(get("expires")) {
			return 400, "An expiry date is required."
		}
		if get("vuln_key") == "" && get("component") == "" {
			return 400, "Name a vulnerability or a component."
		}
		req.Set("reason", reason).Set("expires", get("expires")).Set("owner", sess.Username).Set("rid", auth.TokenHex(6))
	}
	d := filepath.Join(SET_IN, "cve-suppressions")
	if err := os.MkdirAll(d, 0o755); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	name := I2S(NowInt()) + "-" + auth.TokenHex(4) + ".json"
	if err := WriteFileAtomic(filepath.Join(d, name), filepath.Join(d, "."+name+".tmp"), []byte(py.Dumps(req, -1))); err != nil {
		panic(&os.PathError{Op: "save", Path: d, Err: err})
	}
	logf("cve suppression %s requested by %s", action, sess.Username)
	if back != "" {
		return 303, cveLink("group", back, "sent", "suppress")
	}
	return 303, "/vulns"
}

// --- the VM card and the Inventory column ------------------------------------------------------------------

type cveCount struct{ p1, p2, n, kev int64 }

// CveVMCounts is views_cve.vm_counts.
func CveVMCounts() map[int64]cveCount {
	out := map[int64]cveCount{}
	for _, r := range Q("SELECT vm_id, sum(priority='P1') p1, sum(priority='P2') p2, count(*) n, sum(kev) kev FROM (" +
		"SELECT t.vm_id, f.id, f.priority, f.kev FROM cve_targets t JOIN cve_findings f ON f.id=t.finding_id " +
		"WHERE f.state='open' AND t.vm_id IS NOT NULL UNION SELECT a.vm_id, f.id, f.priority, f.kev FROM cve_affects a " +
		"JOIN cve_targets t ON t.target=a.target JOIN cve_findings f ON f.id=t.finding_id AND f.group_key=a.group_key " +
		"WHERE f.state='open' AND a.kind='container' AND a.vm_id IS NOT NULL) GROUP BY vm_id") {
		out[r.I("vm_id")] = cveCount{r.I("p1"), r.I("p2"), r.I("n"), r.I("kev")}
	}
	return out
}

// CveVMCell is views_cve.vm_cell.
func CveVMCell(c cveCount, ok bool) string {
	if !ok {
		return `<span class="dim">-</span>`
	}
	lead := I2S(c.n)
	if c.p1 != 0 {
		lead = `<b class="failnum">` + I2S(c.p1) + ` P1</b>`
	} else if c.p2 != 0 {
		lead = `<b class="warnnum">` + I2S(c.p2) + ` P2</b>`
	}
	if c.p1 != 0 || c.p2 != 0 {
		lead += `<div class="dim small">` + I2S(c.n) + ` open` + If(c.kev != 0, " · "+I2S(c.kev)+" KEV", "") + `</div>`
	}
	return lead
}

// cveLine is the Inventory cell in words: "3 open · 1 KEV · worst P1".
func cveLine(m map[int64]cveCount, id int64) string {
	c, ok := m[id]
	if !ok {
		return "-"
	}
	s := I2S(c.n) + " open"
	if c.kev != 0 {
		s += " · " + I2S(c.kev) + " KEV"
	}
	if c.p1 != 0 {
		return s + " · worst P1"
	}
	if c.p2 != 0 {
		return s + " · worst P2"
	}
	return s
}

// CveVMCard is views_cve.vm_card.
// cveVMRows are a VM's open components, worst first, and the line about its package list.
func cveVMRows(vid int64) ([]Row, string) {
	rows := Q("WITH hit(finding_id, version) AS (SELECT finding_id, version FROM cve_targets WHERE vm_id=? "+
		"UNION ALL SELECT t.finding_id, t.version FROM cve_affects a JOIN cve_findings g ON g.group_key=a.group_key "+
		"JOIN cve_targets t ON t.finding_id=g.id AND t.target=a.target WHERE a.vm_id=? AND a.kind='container') "+
		"SELECT f.group_key, f.component_name, f.kind, min(f.priority) pri, count(DISTINCT f.id) n, "+
		"count(DISTINCT CASE WHEN f.kev=1 THEN f.id END) kev, max(f.fixed_version) fixed, max(t.version) version "+
		"FROM hit t JOIN cve_findings f ON f.id=t.finding_id WHERE f.state='open' "+
		"GROUP BY f.group_key ORDER BY min(f.priority), max(f.score) DESC", vid, vid)
	note := ""
	if h := Q("SELECT * FROM cve_hosts WHERE vm_id=?", vid); len(h) > 0 {
		if h[0].T("pkgs_at") {
			note = "Packages read " + Ago(h[0].Get("pkgs_at"))
		} else {
			e := h[0].S("error")
			if e == "" {
				e = "-"
			}
			note = "No package list: " + E(e)
		}
		if h[0].T("reboot_required") {
			note += " · a reboot is pending (a newer kernel or library is installed)"
		}
	}
	return rows, note
}

// cveFeedFailedLine is "feed queries failed: N (…)" from cve-status.json feed_problems, or "" when all queries
// came back whole. Partial = an NVD product with pages missing; unparsable = a cache file the matcher could not read.
func cveFeedFailedLine(st *py.OMap) string {
	fp := mapOr(st.Get("feed_problems"))
	failed, partial, bad := py.I(fp.GetD("failed", int64(0))), py.I(fp.GetD("partial", int64(0))), py.I(fp.GetD("unparsable", int64(0)))
	if failed+partial+bad == 0 {
		return ""
	}
	var more []string
	if partial > 0 {
		more = append(more, I2S(partial)+" partial")
	}
	if bad > 0 {
		more = append(more, I2S(bad)+" unparsable cache "+If(bad == 1, "file", "files"))
	}
	return "feed queries failed: " + I2S(failed) + If(len(more) > 0, " ("+strings.Join(more, ", ")+")", "")
}
