package app

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// views_capacity.py: /capacity and /hygiene (capacity.json and hygiene.json, written daily by bin/insights.sh).

// CapacityData is ARGUS_STATE_DIR.
var CapacityData = env("ARGUS_STATE_DIR", "/var/lib/argus")

type capacityLevel struct{ kind, word, tip string }

var capacityLevels = map[string]capacityLevel{"critical": {"fail", "critical", "full in under 7 days"},
	"warn": {"fail", "high", "full in under 14 days"}, "full": {"warn", "tight", "under 10 % free"},
	"watch": {"warn", "watch", "full in under 30 days"}, "ok": {"ok", "ok", "full in 30–60 days"}}

var capacityKinds = [][3]string{
	{"test", "Test alerts still firing", "Leftover notification tests. They fire at page/critical level and teach " +
		"the team to ignore pages."},
	{"stale", "Firing for more than 7 days", "Alerts nobody fixes or silences. “never resolved” = no RESOLVED " +
		"message for that alert in the counted window."},
	{"dead", "Dead scrape targets", "Targets with up == 0 for the whole 7-day window" + defIssueRef(defIssueDeadTargets) + "."},
	{"flapping", "Flapping", "Fire and resolve again and again (from ops-watch counters)."},
	{"noisy", "Noisy warnings", "Warnings firing several times a day that nobody acts on."}}

// capacityCommaF is f"{x:,.Nf}".
func capacityCommaF(x float64, prec int) string {
	s := py.F(x, prec)
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return s
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	ip, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range ip {
		if i > 0 && (len(ip)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// CapacityGB is gb(n) of views_capacity.
func CapacityGB(n any) string {
	if n == nil {
		return "-"
	}
	return capacityCommaF(py.Num(n)/(1024*1024*1024), 1) + " GB"
}

// CapacityGrowth is growth(f).
func CapacityGrowth(f *py.OMap) string {
	var n any
	if f.Has("growth_per_day") {
		n = f.Get("growth_per_day")
	} else {
		n = f.Get("loss_per_day")
	}
	if n != nil && math.Abs(py.Num(n)) < 1 {
		n = int64(0)
	}
	if n == nil {
		if f.Get("avail") != nil || f.Has("growth_per_day") {
			return `<span class="dim" title="not growing over the last 7 days">—</span>`
		}
		return "-"
	}
	v := py.Num(n)
	if v < 0 {
		return `<span class="dim">shrinking</span>`
	}
	if v == 0 {
		return `<span class="dim">—</span>`
	}
	if v < 1024*1024/2 {
		return E("+< 1 MB")
	}
	if v < 1024*1024*1024 {
		return E("+" + capacityCommaF(v/(1024*1024), 0) + " MB")
	}
	return E("+" + CapacityGB(v))
}

func capacityStaleBanner(doc *py.OMap, what string, hours float64) string {
	if doc.Len() == 0 {
		return Banner("No "+what+" data yet: is argus-insights.timer installed on "+ARGUS_HOSTNAME+"?", "fail")
	}
	age := Now() - py.Num(py.Or(doc.Get("generated_at"), int64(0)))
	errs := py.AsList(py.Or(doc.Get("errors"), []any{}))
	if age > hours*3600 || len(errs) > 0 {
		msg := py.Capitalize(what) + " data is " + strconv.FormatInt(int64(math.Floor(age/3600)), 10) + " h old"
		if len(errs) > 0 {
			var es []string
			for i, e := range errs {
				if i >= 3 {
					break
				}
				es = append(es, py.Str(e))
			}
			msg += "; errors: " + strings.Join(es, "; ")
		}
		return Banner(msg, "fail")
	}
	return ""
}

// CapacityFSID is fs_id(f).
func CapacityFSID(f *py.OMap) string {
	s := sha1.Sum([]byte(py.OrS(f.Get("dc"), "") + "|" + py.OrS(f.Get("host"), "") + "|" + py.OrS(f.Get("mount"), "")))
	return hex.EncodeToString(s[:])[:12]
}

func capacityDiskState(c *Ctx, fid string) (bool, string) {
	rr := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	st := py.AsMap(py.AsMap(rr.GetD("disk", py.NewOMap())).GetD(fid, py.NewOMap()))
	if st == nil {
		st = py.NewOMap()
	}
	now := Now()
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", "disk-explain-"+fid+".req")); err == nil {
		return false, "queued"
	}
	started := st.GetD("started", int64(0))
	sv := py.Num(started)
	if py.Truthy(started) && now-sv < 20*60 {
		last := Q("SELECT created FROM disk_reports WHERE fs_id=? ORDER BY created DESC LIMIT 1", fid)
		if len(last) == 0 || last[0].F("created") < sv {
			c.Refresh = true
			return false, "running since " + Ago(started)
		}
	}
	if py.Truthy(started) && now-sv < 3600 {
		return false, "1 per hour"
	}
	res := py.Str(st.GetD("result", ""))
	if strings.HasPrefix(res, "skipped") && now-py.Num(st.GetD("requested", int64(0))) < 600 {
		return true, strings.ReplaceAll(res, "skipped: ", "")
	}
	return true, ""
}

var capacityFidRE = regexp.MustCompile(`^[0-9a-f]{12}$`)

func capacityDoc() *py.OMap {
	d := py.AsMap(ReadJSON(filepath.Join(CapacityData, "capacity.json"), py.NewOMap()))
	if d == nil {
		return py.NewOMap()
	}
	return d
}

func capacityMaps(v any) []*py.OMap {
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

// CapacityPostAnalyse is post_analyse(form, sess).
func CapacityPostAnalyse(c *Ctx, form py.QS, sess *Session) (int, string) {
	fid := form.Get("fs")
	if sess.Role != "admin" {
		return 403, "Only admins can start a storage analysis."
	}
	if !capacityFidRE.MatchString(fid) {
		return 400, "Invalid request."
	}
	found := false
	for _, f := range capacityMaps(py.Or(capacityDoc().Get("filesystems"), []any{})) {
		if CapacityFSID(f) == fid {
			found = true
		}
	}
	if !found {
		return 404, "No such filesystem in the capacity forecast."
	}
	if ok, msg := capacityDiskState(c, fid); !ok {
		return 429, "Not started: " + msg + "."
	}
	d := filepath.Join(SET_IN, "requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(err)
	}
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, "disk-explain-"+fid+".req"), filepath.Join(d, ".disk-explain-"+fid+".tmp"), []byte(body)); err != nil {
		panic(err)
	}
	logf("storage analysis %s requested by %s", fid, sess.Username)
	return 303, "/capacity?sent=analyse"
}

var capacityVMLevels = [][3]string{{"critical", "Critical", "full < 7 d or p95 ≥ 97 %"},
	{"warn", "High", "full < 14 d or p95 ≥ 90 %"}, {"watch", "Watch", "full < 30 d"},
	{"full", "Under 10 % free", "VMs whose worst disk is tight"}, {"ok", "OK", "no risk within 30 d"},
	{"unmonitored", "Not monitored", "no node_exporter data"}}

var capacityVMKind = map[string]string{"critical": "fail", "warn": "fail", "full": "warn", "watch": "warn", "ok": "ok"}

func capacityPct(v any, cls string) string {
	if v == nil {
		return `<span class="dim">-</span>`
	}
	if cls != "" {
		return `<span class="` + cls + `">` + py.F(py.Num(v), 0) + ` %</span>`
	}
	return py.F(py.Num(v), 0) + " %"
}

func capacityHotCls(v any, hot [2]float64) string {
	if v == nil {
		return ""
	}
	x := py.Num(v)
	if x >= hot[1] {
		return "failnum"
	} else if x >= hot[0] {
		return "warnnum"
	}
	return ""
}

// CapacityFmtDays is fmt_days(d).
func CapacityFmtDays(d any) string {
	if d == nil {
		return "—"
	}
	x := py.Num(d)
	if x < 1 {
		return "< 1 d"
	}
	if x < 365 {
		return py.G(x, 6) + " d"
	}
	return "> 1 year"
}

type capacityFacts struct{ ips, roles, doms []string }

func capacityInventoryFacts() map[int64]capacityFacts {
	out := map[int64]capacityFacts{}
	for _, r := range Q("SELECT id, ips, domains FROM vms WHERE gone=0") {
		d := py.NewOMap()
		if v, err := py.ParseJSON([]byte(py.OrS(r.Get("domains"), "{}"))); err == nil {
			if m := py.AsMap(v); m != nil {
				d = m
			}
		}
		var f capacityFacts
		for _, x := range strings.Split(py.OrS(r.Get("ips"), ""), ",") {
			if x = py.Strip(x); x != "" {
				f.ips = append(f.ips, x)
			}
		}
		for _, x := range py.AsList(py.Or(d.Get("data_roles"), []any{})) {
			f.roles = append(f.roles, py.Str(x))
		}
		for _, x := range py.AsList(py.Or(d.Get("backend_for"), []any{})) {
			f.doms = append(f.doms, py.Str(x))
		}
		out[r.I("id")] = f
	}
	return out
}

var capacityPortRE = regexp.MustCompile(`:\p{Nd}*$`)

func capacityFactsOf(v *py.OMap, facts map[int64]capacityFacts) capacityFacts {
	id := v.Get("inventory_id")
	if !py.IsNumber(id) {
		return capacityFacts{}
	}
	return facts[py.I(id)]
}

func capacityVMFields(v *py.OMap, facts map[int64]capacityFacts) []VMSearchField {
	fc := capacityFactsOf(v, facts)
	inst := capacityPortRE.ReplaceAllString(py.OrS(v.Get("instance"), ""), "")
	f := []VMSearchField{{"name", v.Get("name")}, {"key", v.Get("key")}, {"instance", inst}, {"env", v.Get("env")},
		{"dc", v.Get("dc")}, {"role", v.Get("role")}}
	for _, ip := range fc.ips {
		f = append(f, VMSearchField{"IP", ip})
	}
	for _, x := range fc.roles {
		f = append(f, VMSearchField{"role", x})
	}
	for _, x := range fc.doms {
		f = append(f, VMSearchField{"domain", x})
	}
	mounts := py.Or(v.Get("filesystems"), py.Or(v.Get("disks"), []any{}))
	for _, x := range capacityMaps(mounts) {
		f = append(f, VMSearchField{"mount", x.Get("mount")})
	}
	return f
}

func capacityVMIDs(v *py.OMap, facts map[int64]capacityFacts) []any {
	ids := []any{v.Get("name"), v.Get("key"), capacityPortRE.ReplaceAllString(py.OrS(v.Get("instance"), ""), "")}
	for _, ip := range capacityFactsOf(v, facts).ips {
		ids = append(ids, ip)
	}
	return ids
}

type capacityVM struct {
	m   *py.OMap
	why string
}

// capacityAllVMs is Capacity → All VMs as a queue list (DESIGN.md "Queue page", in-memory lists): "Find a VM" (the
// VM search: name, IPs, env, DC, roles, services, domains, mounts; ranked, matches marked), facets Environment, DC
// and Level with counts that honour the search and the other facets, sort Worst first (the forecast's order) or
// Fastest growing, 25 VMs a page. The KPI figures link here with ?vlevel=…#vms.
// capacityVMList is the All VMs list of a query: the VMs that match, the facet counts and the facet values.
type capacityVMList struct {
	list   []capacityVM
	counts map[string]map[string]int
	vals   func(string) [][2]string
	ws     []string
}

func capacityVMsOf(l listQ, vms []*py.OMap) capacityVMList {
	q := l.get("q")
	ws := VMSearchWords(q)
	var pool []capacityVM // the VMs the search keeps, in its rank order (else the forecast's order)
	if q != "" {
		facts := capacityInventoryFacts()
		idx := VMSearchSearch(len(vms), q, func(i int) []VMSearchField { return capacityVMFields(vms[i], facts) },
			func(i int) []any { return capacityVMIDs(vms[i], facts) })
		shownF := map[string]bool{"name": true, "env": true, "dc": true, "role": true}
		for _, i := range idx {
			pool = append(pool, capacityVM{vms[i], VMSearchWhy(capacityVMFields(vms[i], facts), ws, shownF)})
		}
	} else {
		for _, v := range vms {
			pool = append(pool, capacityVM{m: v})
		}
	}
	field := map[string]string{"env": "env", "dc": "dc", "vlevel": "level"}
	match := func(v *py.OMap, skip string) bool {
		for _, k := range []string{"env", "dc", "vlevel"} {
			if k != skip && l.get(k) != "" && py.OrS(v.Get(field[k]), "") != l.get(k) {
				return false
			}
		}
		return true
	}
	counts := map[string]map[string]int{"env": {}, "dc": {}, "vlevel": {}}
	var list []capacityVM
	for _, v := range pool {
		for k, m := range counts {
			if match(v.m, k) {
				m[py.OrS(v.m.Get(field[k]), "")]++
			}
		}
		if match(v.m, "") {
			list = append(list, v)
		}
	}
	if q == "" && l.get("vsort") == "growth" {
		key := func(v capacityVM) float64 {
			if g := py.Num(py.Or(v.m.Get("max_growth"), int64(0))); g > 0 {
				return -g
			}
			return 0
		}
		sort.SliceStable(list, func(i, j int) bool { return key(list[i]) < key(list[j]) })
	}
	envR := map[string]int{"release": 0, "pre-release": 1, "develop": 2}
	vals := func(k string) [][2]string {
		var out [][2]string
		for v := range counts[k] {
			if v != "" {
				out = append(out, [2]string{v, v})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			ri, oki := envR[out[i][0]]
			rj, okj := envR[out[j][0]]
			if k == "env" && (oki || okj) && ri != rj {
				return oki && (!okj || ri < rj)
			}
			return out[i][0] < out[j][0]
		})
		return out
	}
	return capacityVMList{list, counts, vals, ws}
}

func capacityVMBadge(v *py.OMap) string {
	lvl := v.Get("level")
	if lvl == "unmonitored" {
		return `<span class="tag">not monitored</span>`
	}
	ls := py.Str(lvl)
	word := ls
	if l, ok := capacityLevels[ls]; ok && lvl != nil {
		word = l.word
	}
	k := capacityVMKind[ls]
	if k == "" || lvl == nil {
		k = "warn"
	}
	return `<span class="badge ` + k + `">` + E(word) + `</span>`
}

func capacityLevelKind(level any, def string) string {
	if l, ok := capacityLevels[py.Str(level)]; ok && level != nil {
		return l.kind
	}
	return def
}

func capacityHotRaw(summ *py.OMap) (map[string][2]float64, map[string][]any) {
	hot := map[string][2]float64{}
	hotSrc := py.AsMap(py.Or(summ.Get("hot"), nil))
	if hotSrc == nil {
		hotSrc = py.NewOMap("cpu_p95", []any{int64(90), int64(97)}, "mem_p95", []any{int64(90), int64(97)})
	}
	hotRaw := map[string][]any{}
	for _, k := range hotSrc.Keys() {
		l := py.AsList(hotSrc.Get(k))
		hotRaw[k] = l
		var t [2]float64
		if len(l) > 0 {
			t[0] = py.Num(l[0])
		}
		if len(l) > 1 {
			t[1] = py.Num(l[1])
		}
		hot[k] = t
	}
	return hot, hotRaw
}

// capacityHot is the p95 limits; a missing one is 90 and 97 %.
func capacityHot(summ *py.OMap) map[string][2]float64 {
	hot, _ := capacityHotRaw(summ)
	for _, k := range []string{"cpu_p95", "mem_p95"} {
		if _, ok := hot[k]; !ok {
			hot[k] = [2]float64{90, 97}
		}
	}
	return hot
}

// capacityQuery is the page's GET state: the filesystems' Show facet and the All VMs list.
func capacityQuery(qs py.QS) listQ {
	l := newListQ("/capacity", qs, []string{"q", "env", "dc", "vlevel", "vsort", "level"}, map[string]string{"vsort": "worst", "level": "soon"},
		func(k, v string) bool {
			switch k {
			case "vsort":
				return In(v, "worst", "growth")
			case "level":
				return In(v, "soon", "all")
			case "vlevel":
				for _, x := range capacityVMLevels {
					if x[0] == v {
						return true
					}
				}
				return false
			}
			return len(v) <= 40 && proposalLabelRe.MatchString(v)
		})
	l.anchor = "vms"
	return l
}
