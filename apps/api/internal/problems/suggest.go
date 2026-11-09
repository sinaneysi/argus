package problems

// suggest.go: the deterministic problem-link suggester (no model). For every open problem and every issue of the
// last PROBLEMS_SUGGEST_LOOKBACK_DAYS (types in PROBLEMS_SOURCES) it matches features and writes a `suggested`
// problem_links row with a confidence and a one-line reason. Nothing is accepted unless PROBLEMS_AUTO_ACCEPT=1, and
// then only at or above PROBLEMS_AUTO_ACCEPT_CONFIDENCE and for the environment classes listed.
//
// Score = noisy-or 1 - prod(1 - w) of the matched feature weights, x 1.0 when the item was first seen within
// PROBLEMS_SUGGEST_WINDOW_MIN of the problem's active window, x 0.5 otherwise; capped at 0.99. Wide problems (more
// than PROBLEMS_FANOUT_MAX hosts) never match on a host.

import (
	"database/sql"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sinaneysi/argus/internal/py"
)

var weights = map[string]float64{
	"fingerprint": 0.60, "upstream": 0.50, "cve_group": 0.90, "component": 0.60, "ci_rule": 0.70,
	"host": 0.35, "domain": 0.30, "service": 0.25, "rca_text": 0.30,
}

var featLabel = map[string]string{"fingerprint": "same alert fingerprint", "upstream": "same upstream", "cve_group": "same CVE group",
	"component": "same component", "ci_rule": "same CI project and rule", "host": "same host",
	"domain": "same domain", "service": "same service", "rca_text": "named in the problem's RCA"}

const (
	timeBudget = 60 * time.Second
	rowCap     = 3000
)

var featureKeys = []string{"fps", "upstreams", "hosts", "domains", "services", "cve_groups", "components", "ci"}

var (
	ipPortRe = regexp.MustCompile(`^((?:\d{1,3}\.){3}\d{1,3}):(\d{2,5})\b`)
	ipv4Re   = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}`)
	ipFull   = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)
	schemeRe = regexp.MustCompile(`^[a-z]+://`)
	portEnd  = regexp.MustCompile(`:\d*$`)
)

// hostRegexps: host names (the configured prefixes, and FQDNs of the usual top-level domains) and name:port.
func hostRegexps(prefixes []string) (*regexp.Regexp, *regexp.Regexp) {
	var ps []string
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" {
			ps = append(ps, regexp.QuoteMeta(strings.ToLower(p))+`[a-z0-9-]+`)
		}
	}
	fqdn := `(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:ir|com|net|org|gold|io|test|example)`
	if len(ps) == 0 {
		return regexp.MustCompile(`(?i)\b(?:` + fqdn + `)\b`), nil
	}
	alt := strings.Join(ps, "|")
	return regexp.MustCompile(`(?i)\b(?:` + alt + `|` + fqdn + `)\b`), regexp.MustCompile(`(?i)\b((?:` + alt + `)):(\d{2,5})\b`)
}

type set map[string]bool

func (s set) add(x string) { s[x] = true }
func (s set) union(o set) {
	for x := range o {
		s[x] = true
	}
}
func (s set) sorted() []string {
	out := make([]string, 0, len(s))
	for x := range s {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func (s set) inter(o set) []string {
	var out []string
	for x := range s {
		if o[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

type feats map[string]set

func emptyF() feats {
	f := feats{}
	for _, k := range featureKeys {
		f[k] = set{}
	}
	return f
}

func norm(h any) string {
	s := ""
	if py.Truthy(h) {
		s = py.Str(h)
	}
	s = strings.ToLower(py.Strip(s))
	s, _, _ = strings.Cut(schemeRe.ReplaceAllString(s, ""), "/")
	return py.Head(portEnd.ReplaceAllString(s, ""), 120)
}

func digitOrDot(b byte) bool { return b == '.' || (b >= '0' && b <= '9') }

// scanLB is finditer with the lookbehind (?<![\d.]) at each start (re is anchored with ^).
func scanLB(t string, re *regexp.Regexp, after func(rest string) bool) [][]string {
	var out [][]string
	for i := 0; i < len(t); {
		if i > 0 && digitOrDot(t[i-1]) {
			i++
			continue
		}
		m := re.FindStringSubmatch(t[i:])
		if m == nil || (after != nil && !after(t[i+len(m[0]):])) {
			i++
			continue
		}
		out = append(out, m)
		i += len(m[0])
	}
	return out
}

func (o *Options) skipHost(h string) bool {
	if h == "example.com" {
		return true
	}
	for _, s := range o.SkipHosts {
		if strings.EqualFold(s, h) {
			return true
		}
	}
	return false
}

// textFeatures: hosts (names, addresses), domains and upstreams (ip:port, name:port) named in text.
func (o *Options) textFeatures(text any, f feats) feats {
	if f == nil {
		f = emptyF()
	}
	t := ""
	if py.Truthy(text) {
		t = py.Head(py.Str(text), 60000)
	}
	for _, m := range scanLB(t, ipPortRe, nil) {
		f["upstreams"].add(m[1] + ":" + m[2])
		f["hosts"].add(m[1])
	}
	if o.namePort != nil {
		for _, m := range o.namePort.FindAllStringSubmatch(t, -1) {
			f["upstreams"].add(strings.ToLower(m[1]) + ":" + m[2])
		}
	}
	for _, m := range scanLB(t, ipv4Re, func(r string) bool { return r == "" || !digitOrDot(r[0]) }) {
		f["hosts"].add(m[0])
	}
	for _, m := range o.hostRe.FindAllString(t, -1) {
		h := norm(m)
		if h == "" || o.skipHost(h) || strings.HasSuffix(h, ".md") {
			continue
		}
		if strings.Contains(h, ".") {
			f["domains"].add(h)
		} else {
			f["hosts"].add(h)
		}
	}
	return f
}

func subjectFeatures(subject any, f feats) feats {
	s := norm(subject)
	if s == "" {
		return f
	}
	if ipFull.MatchString(s) || !strings.Contains(s, ".") {
		f["hosts"].add(s)
	} else {
		f["domains"].add(s)
	}
	return f
}

// --- service map -------------------------------------------------------------------------------------------------

func (o *Options) serviceMap() *py.OMap {
	if !o.smapRead {
		o.smapRead = true
		for _, p := range o.ServiceMaps {
			if b, err := os.ReadFile(p); err == nil {
				if v, err := py.ParseJSON(b); err == nil {
					o.smap, _ = v.(*py.OMap)
					break
				}
			}
		}
	}
	return o.smap
}

// servicesOf: the upstream names of the item's upstreams, and of its domains when a domain has at most 8 of them.
func (o *Options) servicesOf(f feats) set {
	out := set{}
	sm := o.serviceMap()
	if sm == nil {
		return out
	}
	backends := sm.Map("backends")
	for u := range f["upstreams"] {
		for _, b := range py.AsList(backends.Get(u)) {
			if bm, ok := b.(*py.OMap); ok && py.Truthy(bm.Get("upstream")) {
				out.add(py.Str(bm.Get("upstream")))
			}
		}
	}
	domains := sm.Map("domains")
	for d := range f["domains"] {
		names := set{}
		for _, blk := range py.AsList(domains.Map(d).Get("blocks")) {
			bm, _ := blk.(*py.OMap)
			for _, loc := range py.AsList(bm.Get("locations")) {
				if lm, ok := loc.(*py.OMap); ok && py.Truthy(lm.Get("upstream")) {
					names.add(py.Str(lm.Get("upstream")))
				}
			}
		}
		if len(names) > 0 && len(names) <= 8 {
			out.union(names)
		}
	}
	return out
}

// --- items (issues) --------------------------------------------------------------------------------------------------

type opt struct {
	v  int64
	ok bool
}

func optOf(v any) opt {
	if v == nil {
		return opt{}
	}
	return opt{py.I(v), true}
}

type item struct {
	rt, rid     string
	env         any
	first, last opt
	incidentID  int64
	subject     string
	f           feats
}

func (o *Options) incidentText(q querier, iid int64) string {
	var parts []string
	for _, r := range rowsQ(q, `SELECT p.title, p.summary, p.rca FROM incident_events e JOIN triage_problems p ON p.run_id=e.run_id AND p.idx=e.idx WHERE e.incident_id=? ORDER BY e.at DESC LIMIT 5`, iid) {
		parts = append(parts, r.s("title"), r.s("summary"), py.Head(r.s("rca"), 8000))
	}
	for _, r := range rowsQ(q, `SELECT content FROM rca_reports WHERE incident_id=? ORDER BY id DESC LIMIT 2`, iid) {
		parts = append(parts, py.Head(r.s("content"), 20000))
	}
	return strings.Join(parts, "\n")
}

var cveEnv = map[string]string{"production": "release", "staging": "pre-release", "dev": "develop"}

// items are the candidate issues; with oneRT set, only that item, whatever its age or state.
func (o *Options) items(q querier, now int64, oneRT, oneID string) []item {
	cfg := o.config()
	since := now - cfg.LookbackDays*86400
	src := cfg.Sources
	if oneRT != "" {
		src = []string{oneRT}
	}
	num := int64(-1)
	if oneRT != "" && py.IsDigit(oneID) {
		num = atoi(oneID)
	}
	var out []item
	if in("incident", src) {
		var rs []row
		if oneRT != "" {
			rs = rowsQ(q, `SELECT id, subject, title, env, first_seen, last_seen, fingerprints FROM incidents WHERE id=? LIMIT ?`, num, rowCap)
		} else {
			rs = rowsQ(q, `SELECT id, subject, title, env, first_seen, last_seen, fingerprints FROM incidents WHERE (status IN ('open','acknowledged') OR last_seen >= ?) ORDER BY last_seen DESC LIMIT ?`, since, rowCap)
		}
		for _, r := range rs {
			f := emptyF()
			subjectFeatures(r["subject"], f)
			o.textFeatures(r["title"], f)
			f["upstreams"].union(o.textFeatures(o.incidentText(q, r.i("id")), nil)["upstreams"])
			raw := r.s("fingerprints")
			if raw == "" {
				raw = "[]"
			}
			if v, err := py.ParseJSON([]byte(raw)); err == nil {
				for _, x := range py.AsList(v) {
					if py.Truthy(x) {
						f["fps"].add(py.Str(x))
					}
				}
			}
			out = append(out, item{rt: "incident", rid: r.s("id"), env: r["env"], first: optOf(r["first_seen"]), last: optOf(r["last_seen"]),
				incidentID: r.i("id"), subject: norm(r["subject"]), f: f})
		}
	}
	if in("qa_issue", src) {
		var rs []row
		if oneRT != "" {
			rs = rowsQ(q, `SELECT id, title, environment, first_ts, last_ts, incident_id, log FROM qa_issues WHERE id=? LIMIT ?`, num, rowCap)
		} else {
			rs = rowsQ(q, `SELECT id, title, environment, first_ts, last_ts, incident_id, log FROM qa_issues WHERE (last_ts >= ? OR created_at >= ?) LIMIT ?`, since, since, rowCap)
		}
		for _, r := range rs {
			f := o.textFeatures(r.s("title")+"\n"+py.Head(r.s("log"), 8000), nil)
			out = append(out, item{rt: "qa_issue", rid: r.s("id"), env: r["environment"], first: optOf(r["first_ts"]), last: optOf(r["last_ts"]),
				incidentID: r.i("incident_id"), f: f})
		}
	}
	if in("cve_group", src) {
		var rs []row
		if oneRT != "" {
			rs = rowsQ(q, `SELECT id, group_key, component_name, ecosystem, env_max_class, first_seen, last_seen, incident_id, targets FROM cve_findings WHERE group_key=? LIMIT ?`, oneID, rowCap*5)
		} else {
			rs = rowsQ(q, `SELECT id, group_key, component_name, ecosystem, env_max_class, first_seen, last_seen, incident_id, targets FROM cve_findings WHERE group_key IS NOT NULL AND (state IS NULL OR state NOT IN ('resolved','suppressed')) AND last_seen >= ? LIMIT ?`, since, rowCap*5)
		}
		var order []string
		groups := map[string]*item{}
		for _, r := range rs {
			gk := r.s("group_key")
			g := groups[gk]
			if g == nil {
				g = &item{rt: "cve_group", rid: gk, env: "", f: emptyF()}
				groups[gk] = g
				order = append(order, gk)
			}
			g.f["cve_groups"].add(gk)
			if r.has("component_name") {
				g.f["components"].add(strings.ToLower(r.s("component_name") + "|" + r.s("ecosystem")))
			}
			if e, ok := cveEnv[r.s("env_max_class")]; ok {
				g.env = e
			}
			if fs := py.I(r["first_seen"]); fs != 0 && (!g.first.ok || g.first.v == 0 || fs < g.first.v) {
				g.first = opt{fs, true}
			}
			mx := g.last.v
			if l := py.I(r["last_seen"]); l > mx {
				mx = l
			}
			g.last = opt{mx, mx != 0}
			if g.incidentID == 0 {
				g.incidentID = r.i("incident_id")
			}
			raw := r.s("targets")
			if raw == "" {
				raw = "[]"
			}
			if v, err := py.ParseJSON([]byte(raw)); err == nil {
				tg := py.AsList(v)
				if len(tg) > 200 {
					tg = tg[:200]
				}
				for _, t := range tg {
					var name any = t
					if m, ok := t.(*py.OMap); ok {
						name = m.Get("target")
					}
					if s, ok := name.(string); ok && s != "" {
						s, _, _ = strings.Cut(s, "/")
						s, _, _ = strings.Cut(s, " ")
						subjectFeatures(s, g.f)
					}
				}
			}
		}
		n := 0
		for _, gk := range order {
			if refTypes["cve_group"].re.MatchString(gk) && n < rowCap {
				out = append(out, *groups[gk])
				n++
			}
		}
	}
	if in("cicd_finding", src) {
		var rs []row
		if oneRT != "" {
			rs = rowsQ(q, `SELECT id, project_id, rule, klass, env, title, detail, first_seen, last_seen, incident_id FROM cicd_findings WHERE id=? LIMIT ?`, num, rowCap)
		} else {
			rs = rowsQ(q, `SELECT id, project_id, rule, klass, env, title, detail, first_seen, last_seen, incident_id FROM cicd_findings WHERE status='open' AND last_seen >= ? ORDER BY last_seen DESC LIMIT ?`, since, rowCap)
		}
		for _, r := range rs {
			f := emptyF()
			if r.has("project_id") {
				if r.has("rule") {
					f["ci"].add(r.s("project_id") + "|rule:" + r.s("rule"))
				}
				if r.has("klass") {
					f["ci"].add(r.s("project_id") + "|class:" + r.s("klass"))
				}
			}
			f["upstreams"].union(o.textFeatures(r.s("title")+"\n"+py.Head(r.s("detail"), 4000), nil)["upstreams"])
			out = append(out, item{rt: "cicd_finding", rid: r.s("id"), env: r["env"], first: optOf(r["first_seen"]), last: optOf(r["last_seen"]),
				incidentID: r.i("incident_id"), f: f})
		}
	}
	if in("capacity", src) {
		seen := set{}
		var rs []row
		if oneRT != "" {
			host, mount, _ := strings.Cut(oneID, "|")
			rs = rowsQ(q, `SELECT host, alias, mount, env, created FROM disk_reports WHERE (host=? OR alias=?) AND mount=? ORDER BY created DESC LIMIT 1`, host, host, mount)
			if len(rs) == 0 {
				rs = []row{{"host": host, "alias": "", "mount": mount, "env": "", "created": nil}}
			}
		} else {
			rs = rowsQ(q, `SELECT host, alias, mount, env, created FROM disk_reports WHERE created >= ? ORDER BY created DESC LIMIT ?`, since, rowCap)
		}
		for _, r := range rs {
			host := r.s("host")
			if host == "" {
				host = r.s("alias")
			}
			host = py.Strip(host)
			rid := host + "|" + r.s("mount")
			if seen[rid] || !refTypes["capacity"].re.MatchString(rid) {
				continue
			}
			seen.add(rid)
			out = append(out, item{rt: "capacity", rid: rid, env: r["env"], first: optOf(r["created"]), last: optOf(r["created"]),
				subject: norm(host), f: subjectFeatures(host, emptyF())})
		}
	}
	if in("vm_report", src) {
		var rs []row
		if oneRT != "" {
			rs = rowsQ(q, `SELECT id, name, env, created, run_id FROM vm_reports WHERE id=? LIMIT ?`, num, rowCap)
		} else {
			rs = rowsQ(q, `SELECT id, name, env, created, run_id FROM vm_reports WHERE created >= ? ORDER BY created DESC LIMIT ?`, since, rowCap)
		}
		for _, r := range rs {
			f := subjectFeatures(r["name"], emptyF())
			var parts []string
			for _, p := range rowsQ(q, `SELECT title, summary FROM triage_problems WHERE run_id=?`, r["run_id"]) {
				parts = append(parts, p.s("title")+" "+p.s("summary"))
			}
			f["upstreams"].union(o.textFeatures(strings.Join(parts, "\n"), nil)["upstreams"])
			out = append(out, item{rt: "vm_report", rid: r.s("id"), env: r["env"], first: optOf(r["created"]), last: optOf(r["created"]),
				subject: norm(r["name"]), f: f})
		}
	}
	if in("finding", src) {
		var rs []row
		if oneRT != "" {
			run, i, _ := strings.Cut(oneID, ":")
			rs = rowsQ(q, `SELECT p.run_id, p.idx, p.title, p.summary, p.environment, r.started_at FROM triage_problems p LEFT JOIN runs r ON r.id=p.run_id WHERE p.run_id=? AND p.idx=?`, atoi(run), atoi(i))
		} else {
			rs = rowsQ(q, `SELECT p.run_id, p.idx, p.title, p.summary, p.environment, r.started_at FROM triage_problems p JOIN runs r ON r.id=p.run_id WHERE r.started_at >= ? AND NOT EXISTS (SELECT 1 FROM incident_events e WHERE e.run_id=p.run_id AND e.idx=p.idx) ORDER BY r.started_at DESC LIMIT ?`, since, rowCap)
		}
		for _, r := range rs {
			if r["idx"] == nil {
				continue
			}
			f := o.textFeatures(r.s("title")+"\n"+r.s("summary"), nil)
			out = append(out, item{rt: "finding", rid: r.s("run_id") + ":" + r.s("idx"), env: r["environment"], first: optOf(r["started_at"]),
				last: optOf(r["started_at"]), f: f})
		}
	}
	for i := range out {
		out[i].f["services"].union(o.servicesOf(out[i].f))
	}
	return out
}

// --- problems ----------------------------------------------------------------------------------------------------------

// signature is the union of the features of the accepted caused_by items and the upstreams named by the
// explaining RCAs.
func (o *Options) signature(q querier, pid int64) feats {
	sig := emptyF()
	cache := map[[2]string]*item{}
	for _, lk := range rowsQ(q, `SELECT ref_type, ref_id FROM problem_links WHERE problem_id=? AND state='accepted' AND link_type='caused_by'`, pid) {
		k := [2]string{lk.s("ref_type"), lk.s("ref_id")}
		it, ok := cache[k]
		if !ok {
			if in(k[0], IssueTypes) {
				if got := o.items(q, o.now(), k[0], k[1]); len(got) > 0 {
					it = &got[0]
				}
			}
			cache[k] = it
		}
		if it != nil {
			for _, fk := range featureKeys {
				sig[fk].union(it.f[fk])
			}
		}
	}
	for _, r := range rowsQ(q, `SELECT r.content FROM problem_links l JOIN rca_reports r ON r.id=CAST(l.ref_id AS INTEGER) WHERE l.problem_id=? AND l.ref_type='rca' AND l.state='accepted'`, pid) {
		sig["upstreams"].union(o.textFeatures(r["content"], nil)["upstreams"])
	}
	return sig
}

type pctx struct {
	sig        feats
	rca        string
	start, end int64
	linked     map[[2]string]bool
	incidents  map[int64]bool
	wide       bool
}

func (o *Options) problemContext(q querier, p row, now int64) pctx {
	sig := emptyF()
	raw := p.s("signature")
	if raw == "" {
		raw = "{}"
	}
	any1 := false
	if v, err := py.ParseJSON([]byte(raw)); err == nil {
		if m, ok := v.(*py.OMap); ok {
			for _, k := range featureKeys {
				for _, x := range py.AsList(m.Get(k)) {
					sig[k].add(py.Str(x))
					any1 = true
				}
			}
		}
	}
	if !any1 {
		sig = o.signature(q, p.i("id"))
	}
	sig["services"].union(o.servicesOf(sig))
	var rca []string
	for _, r := range rowsQ(q, `SELECT r.content FROM problem_links l JOIN rca_reports r ON r.id=CAST(l.ref_id AS INTEGER) WHERE l.problem_id=? AND l.ref_type='rca' AND l.state='accepted'`, p.i("id")) {
		rca = append(rca, strings.ToLower(py.Head(r.s("content"), 200000)))
	}
	c := pctx{sig: sig, rca: strings.Join(rca, "\n"), linked: map[[2]string]bool{}, incidents: map[int64]bool{}}
	var firsts, lasts []int64
	for _, lk := range rowsQ(q, `SELECT ref_type, ref_id, link_type, state FROM problem_links WHERE problem_id=? AND state IN ('suggested','accepted')`, p.i("id")) {
		c.linked[[2]string{lk.s("ref_type"), lk.s("ref_id")}] = true
		if lk.s("state") == "accepted" && lk.s("link_type") == "caused_by" {
			d := o.describe(q, lk.s("ref_type"), lk.s("ref_id"))
			if d.firstSeen != 0 {
				firsts = append(firsts, d.firstSeen)
			}
			if d.lastSeen != 0 {
				lasts = append(lasts, d.lastSeen)
			}
			if lk.s("ref_type") == "incident" {
				c.incidents[atoi(lk.s("ref_id"))] = true
			}
		}
	}
	c.start = p.i("created_at")
	if len(firsts) > 0 {
		c.start = minOf(firsts)
	}
	switch {
	case in(p.s("status"), openStatuses):
		c.end = now
	case len(lasts) > 0:
		c.end = maxOf(lasts)
	case p.i("resolved_at") != 0:
		c.end = p.i("resolved_at")
	default:
		c.end = p.i("updated_at")
	}
	c.wide = int64(len(sig["hosts"])) > o.config().FanoutMax
	return c
}

func minOf(xs []int64) int64 {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(xs []int64) int64 {
	m := xs[0]
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

type match struct{ name, detail string }

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// namedAsHost is re.search(r"(?<![\w.-])" + h + r"(?![\w-])", text).
func namedAsHost(text, h string) bool {
	for off := 0; ; {
		i := strings.Index(text[off:], h)
		if i < 0 {
			return false
		}
		i += off
		ok := true
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			ok = !(isWord(r) || r == '.' || r == '-')
		}
		if j := i + len(h); ok && j < len(text) {
			r, _ := utf8.DecodeRuneInString(text[j:])
			ok = !(isWord(r) || r == '-')
		}
		if ok {
			return true
		}
		off = i + 1
	}
}

func round(x float64, n int) float64 { v, _ := strconv.ParseFloat(py.F(x, n), 64); return v }

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// score of one item against one problem: the score, the matched features, the gap to the active window (nil:
// unknown).
func (o *Options) score(it item, c pctx) (float64, []match, *int64) {
	sig, f := c.sig, it.f
	var m []match
	hit := func(name string, common []string) {
		if len(common) > 0 {
			m = append(m, match{name, common[0]})
		}
	}
	hit("fingerprint", f["fps"].inter(sig["fps"]))
	hit("upstream", f["upstreams"].inter(sig["upstreams"]))
	hit("cve_group", f["cve_groups"].inter(sig["cve_groups"]))
	hit("component", f["components"].inter(sig["components"]))
	hit("ci_rule", f["ci"].inter(sig["ci"]))
	if !c.wide {
		hit("host", f["hosts"].inter(sig["hosts"]))
	}
	hit("domain", f["domains"].inter(sig["domains"]))
	hit("service", f["services"].inter(sig["services"]))
	if c.rca != "" {
		var named []string
		for _, u := range f["upstreams"].sorted() {
			if strings.Contains(c.rca, u) {
				named = append(named, u)
			}
		}
		if it.subject != "" && strings.Contains(it.subject, ".") && strings.Contains(c.rca, it.subject) {
			named = append([]string{it.subject}, named...)
		}
		if !c.wide { // a wide problem's RCA lists its hosts; a host name alone is not evidence
			for _, h := range f["hosts"].sorted() {
				if py.Len(h) >= 7 && namedAsHost(c.rca, h) {
					named = append(named, h)
				}
			}
		}
		if len(named) > 0 {
			m = append(m, match{"rca_text", named[0]})
		}
	}
	if len(m) == 0 {
		return 0, nil, nil
	}
	prod := 1.0
	for _, x := range m {
		prod *= 1 - weights[x.name]
	}
	s := 1 - prod
	w := o.config().WindowMin * 60
	t := it.first
	if !(t.ok && t.v != 0) {
		t = it.last
	}
	var gap *int64
	end := c.end
	if end == 0 {
		end = c.start
	}
	if t.ok && t.v != 0 && c.start != 0 {
		var g int64
		if c.start-w <= t.v && t.v <= end+w {
			g = c.start - t.v
			if x := t.v - end; x > g {
				g = x
			}
			if g < 0 {
				g = 0
			}
		} else {
			s *= 0.5
			g = abs(t.v - c.start)
			if x := abs(t.v - end); x < g {
				g = x
			}
		}
		gap = &g
	} else if !t.ok {
		s *= 0.5
	}
	return math.Min(round(s, 4), 0.99), m, gap
}

func gapText(g *int64) string {
	switch {
	case g == nil:
		return "time unknown"
	case *g == 0:
		return "inside the problem's active window"
	case *g < 3600:
		return strconv.FormatInt(py.FloorDiv(*g, 60), 10) + " min apart"
	case *g < 2*86400:
		return py.F(float64(*g)/3600, 1) + " h apart"
	}
	return py.F(float64(*g)/86400, 0) + " d apart"
}

func reasonOf(m []match, g *int64) string {
	top := append([]match(nil), m...)
	sort.SliceStable(top, func(i, j int) bool { return weights[top[i].name] > weights[top[j].name] })
	if len(top) > 2 {
		top = top[:2]
	}
	parts := make([]string, len(top))
	for i, x := range top {
		parts[i] = featLabel[x.name] + " " + x.detail
	}
	return strings.Join(parts, "; ") + "; " + gapText(g)
}

// SuggestResult is what one suggester pass did.
type SuggestResult struct {
	Problems, Items, Suggested, AutoAccepted, SkippedRejected int
	High, Medium, Low                                         int
	Stopped                                                   string
}

func (r SuggestResult) omap() *py.OMap {
	return py.NewOMap("problems", int64(r.Problems), "items", int64(r.Items), "suggested", int64(r.Suggested),
		"auto_accepted", int64(r.AutoAccepted), "skipped_rejected", int64(r.SkippedRejected),
		"bands", py.NewOMap("high", int64(r.High), "medium", int64(r.Medium), "low", int64(r.Low)), "stopped", r.Stopped)
}

// suggest is one bounded pass inside the caller's transaction.
func (o *Options) suggest(tx *sql.Tx) (SuggestResult, error) {
	cfg, now, t0 := o.config(), o.now(), time.Now()
	var res SuggestResult
	probs := rowsQ(tx, `SELECT * FROM problems WHERE status IN ('new','investigating','known_error','fix_in_progress') OR (status='resolved' AND resolved_at >= ?) ORDER BY id`, now-cfg.ReopenDays*86400)
	res.Problems = len(probs)
	if len(probs) == 0 {
		return res, nil
	}
	ctxs := make([]pctx, len(probs))
	for i, p := range probs {
		ctxs[i] = o.problemContext(tx, p, now)
	}
	its := o.items(tx, now, "", "")
	res.Items = len(its)
	rejectSince := now - cfg.RejectDays*86400
	type cand struct {
		s   float64
		p   row
		m   []match
		gap *int64
	}
	for _, it := range its {
		if time.Since(t0) > timeBudget {
			res.Stopped = "time budget"
			break
		}
		if one(tx, `SELECT 1 x FROM problem_links WHERE ref_type=? AND ref_id=? AND link_type='caused_by' AND state='accepted' LIMIT 1`, it.rt, it.rid) != nil {
			continue // decided elsewhere
		}
		var cands []cand
		for i, p := range probs {
			c := ctxs[i]
			if c.linked[[2]string{it.rt, it.rid}] {
				continue
			}
			if it.incidentID != 0 && c.incidents[it.incidentID] {
				continue // derived through its incident: never stored twice
			}
			s, m, gap := o.score(it, c)
			if s < cfg.MinConfidence {
				continue
			}
			if rj := one(tx, `SELECT max(confidence) c FROM problem_links WHERE problem_id=? AND ref_type=? AND ref_id=? AND state='rejected' AND decided_at >= ?`, p.i("id"), it.rt, it.rid, rejectSince); rj != nil && rj["c"] != nil && s < py.Num(rj["c"])+0.2 {
				res.SkippedRejected++
				continue
			}
			cands = append(cands, cand{s, p, m, gap})
		}
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].s != cands[j].s {
				return cands[i].s > cands[j].s
			}
			return cands[i].p.i("id") < cands[j].p.i("id")
		})
		if int64(len(cands)) > cfg.MaxPerItem {
			cands = cands[:cfg.MaxPerItem]
		}
		for _, c := range cands {
			reason := reasonOf(c.m, c.gap)
			if c.p.s("status") == "resolved" {
				reason = "recurrence of P-" + c.p.s("id") + "? " + reason
			}
			names := make([]string, len(c.m))
			for i, x := range c.m {
				names[i] = x.name
			}
			conf := round(c.s, 2)
			lid, made, err := o.link(tx, c.p.i("id"), it.rt, it.rid, "caused_by", o.Actor,
				linkArgs{state: "suggested", source: o.Actor, confidence: &conf, reason: reason, features: names, noCheck: true}, now)
			if err != nil {
				if !refusal(err) {
					return res, err
				}
				o.Log.Info("suggestion skipped", "item", it.rt+":"+it.rid, "problem", c.p.i("id"), "error", err.Error())
				continue
			}
			if !made {
				continue
			}
			res.Suggested++
			switch {
			case c.s >= 0.9:
				res.High++
			case c.s >= 0.75:
				res.Medium++
			default:
				res.Low++
			}
			if cfg.AutoAccept && c.s >= cfg.AutoConfidence && in(EnvClass(it.env), cfg.AutoClasses) {
				if _, err := o.accept(tx, []int64{lid}, o.Actor, o.Actor+"-auto", now); err != nil {
					return res, err
				}
				res.AutoAccepted++
			}
		}
	}
	return res, nil
}

// Suggest is one suggester pass; off (FEATURE_PROBLEMS or PROBLEMS_SUGGEST off) it only writes the state file.
func (o *Options) Suggest(db *sql.DB) (SuggestResult, bool, error) {
	o.defaults()
	o.cfg, o.smap, o.smapRead = nil, nil, false
	cfg := o.config()
	lk, err := o.lock()
	if err != nil {
		return SuggestResult{}, false, err
	}
	defer lk.Close()
	if !cfg.Enabled || !cfg.Suggest {
		return SuggestResult{}, false, o.writeState(db, py.NewOMap("last_suggest_result", py.NewOMap("skipped", "off")))
	}
	t0 := time.Now()
	tx, err := db.Begin()
	if err != nil {
		return SuggestResult{}, true, err
	}
	res, err := o.suggest(tx)
	if err != nil {
		tx.Rollback()
		return res, true, err
	}
	if err := tx.Commit(); err != nil {
		return res, true, err
	}
	m := res.omap().Set("ms", time.Since(t0).Milliseconds())
	return res, true, o.writeState(db, py.NewOMap("last_suggest", o.now(), "last_suggest_result", m))
}
