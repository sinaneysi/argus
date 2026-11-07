package app

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// views_inventory.py: /inventory, /inventory/view, /inventory.csv and the refresh / analyse requests.

var inventoryFlags = [][2]string{{"prom-only", "in Prometheus, no ssh-ro access"}, {"not-monitored", "ssh-ro access, not in Prometheus"},
	{"down", "Prometheus target down"}, {"os-eol", "OS end of life"}, {"disk-85", "a mount >= 85 % used"},
	{"no-ntp", "clock not NTP-synchronized"}, {"ssh-failed", "ssh-ro alias did not answer"},
	{"ssh-skipped-db", "DB host: no login (operator rule)"}}

// inventoryFlagLabel: the plain words a chip shows for a flag; the flag key stays in the URL and the CSV.
var inventoryFlagLabel = map[string]string{"prom-only": "No ssh access", "not-monitored": "Not in Prometheus",
	"down": "Target down", "os-eol": "OS end of life", "disk-85": "Disk ≥ 85 % used", "no-ntp": "Clock not synced",
	"ssh-failed": "ssh did not answer", "ssh-skipped-db": "DB host, no login"}

func inventoryFlagName(f string) string {
	if l, ok := inventoryFlagLabel[f]; ok {
		return l
	}
	return f
}

func inventoryFlagDesc(f string) (string, bool) {
	for _, x := range inventoryFlags {
		if x[0] == f {
			return x[1], true
		}
	}
	return "", false
}

var (
	inventoryViews    = [][2]string{{"problems", "Needs a look"}, {"critical", "Critical now"}, {"notready", "Services not ready"}, {"dead", "Dead upstreams"}, {"apps", "Production app hosts"}}
	inventorySorts    = [][2]string{{"worst", "Worst first"}, {"name", "Name"}, {"env", "Environment"}, {"vcpu", "vCPU"}, {"ram", "RAM"}, {"used", "Disk used"}, {"seen", "Last seen"}}
	inventoryEnvs     = []string{"release", "pre-release", "develop", "unknown"}
	inventoryDCs      = append(append([]string{}, OBS_SITES...), strings.Join(OBS_SITES, "+"), "unknown")
	inventoryRes      = [][2]string{{"cpu", "CPU"}, {"memory", "Memory"}, {"io", "IO"}, {"disk", "Disk"}, {"network", "Network"}}
	inventoryRank     = map[string]int{"critical": 2, "pressure": 1, "ok": 0, "unknown": -1}
	inventoryStateBdg = map[string]string{"critical": "fail", "pressure": "warn", "ok": "ok"}
	inventoryReadyBdg = map[string]string{"healthy": "ok", "degraded": "warn", "restarting": "warn", "stopped": "warn", "down": "fail", "idle": "ok"}
	inventoryGroups   = []struct {
		res  string
		keys []string
	}{{"cpu", []string{"cpu_busy", "psi_cpu", "load_per_cpu", "cpu_steal"}},
		{"memory", []string{"mem_used_pct", "mem_avail", "psi_mem", "psi_mem_full", "majfault", "swap_used", "oom"}},
		{"io", []string{"cpu_iowait", "psi_io", "psi_io_full", "disk_util", "disk_lat"}},
		{"network", []string{"net_rx", "net_tx", "net_err", "tcp_estab", "conntrack_pct", "fd_pct"}}}
	inventoryLabels = map[string][2]string{"cpu_busy": {"CPU busy", "pct"}, "cpu_iowait": {"CPU iowait", "pct"}, "cpu_steal": {"CPU steal", "pct"},
		"load_per_cpu": {"Load per vCPU", "ratio"}, "psi_cpu": {"Tasks waiting for CPU (PSI)", "pct"},
		"mem_used_pct": {"Memory used", "pct"}, "mem_avail": {"Memory available", "bytes"},
		"swap_used": {"Swap used", "bytes"}, "majfault": {"Major page faults", "rate"},
		"psi_mem": {"Tasks waiting for memory (PSI)", "pct"}, "psi_mem_full": {"All tasks stalled (PSI full)", "pct"},
		"oom": {"OOM kills per 15 min", "count"}, "psi_io": {"Tasks waiting for IO (PSI)", "pct"},
		"psi_io_full": {"All tasks stalled on IO", "pct"}, "disk_util": {"Busiest disk busy", "pct"},
		"disk_lat": {"Disk latency per IO", "ms"}, "net_rx": {"Network in", "Bps"}, "net_tx": {"Network out", "Bps"},
		"net_err": {"Errors and drops", "rate"}, "tcp_estab": {"TCP established", "count"},
		"conntrack_pct": {"Conntrack table used", "pct"}, "fd_pct": {"File handles used", "pct"}}
)

const (
	inventoryRefreshGap = 120
	inventoryAnalyseGap = 1800
)

func inventoryRankOf(s any) int {
	if v, ok := inventoryRank[py.Str(s)]; ok && s != nil {
		return v
	}
	return -1
}

// ---- formatting ----

func inventoryGB(n any) string {
	if !py.Truthy(n) {
		return "-"
	}
	return capacityCommaF(py.Num(n)/(1024*1024*1024), 0) + " GB"
}

func inventoryMem(n any) string {
	if n == nil {
		return "-"
	}
	v := py.Num(n)
	if v >= 1024*1024*1024 {
		return py.F(v/(1024*1024*1024), 1) + " GB"
	}
	if v >= 1024*1024 {
		return py.F(v/(1024*1024), 0) + " MB"
	}
	return py.F(v/1024, 0) + " KB"
}

func inventoryFmt(unit string, vv any) string {
	if vv == nil {
		return "-"
	}
	v := py.Num(vv)
	switch unit {
	case "pct":
		if v < 10 {
			return py.F(v, 1) + " %"
		}
		return py.F(v, 0) + " %"
	case "bytes":
		return inventoryMem(vv)
	case "Bps":
		return inventoryMem(vv) + "/s"
	case "ratio":
		return py.F(v, 2)
	case "rate":
		if v < 10 {
			return py.F(v, 1) + "/s"
		}
		return capacityCommaF(v, 0) + "/s"
	case "ms":
		return py.F(v, 0) + " ms"
	}
	return capacityCommaF(v, 0)
}

func inventoryFmtCPU(vv any) string {
	if vv == nil {
		return "-"
	}
	v := py.Num(vv)
	if v >= 10 {
		return py.F(v, 0) + " %"
	}
	return py.F(v, 1) + " %"
}

func inventoryUptime(boot any) string {
	if !py.Truthy(boot) {
		return "-"
	}
	d := (Now() - py.Num(boot)) / 86400
	if d >= 1 {
		return py.F(d, 0) + " d"
	}
	return py.F(d*24, 0) + " h"
}

func inventorySince(ts any) string {
	if !py.Truthy(ts) {
		return "-"
	}
	s := Now() - py.Num(ts)
	if s < 3600 {
		m := int64(math.Floor(s / 60))
		if m < 1 {
			m = 1
		}
		return I2S(m) + " min"
	}
	if s < 86400*2 {
		return py.F(s/3600, 0) + " h"
	}
	return py.F(s/86400, 0) + " d"
}

func inventoryDim(text string, cls ...string) string {
	c := "dim small"
	if len(cls) > 0 {
		c = cls[0]
	}
	if text == "" {
		return ""
	}
	return `<div class="` + c + `">` + E(text) + `</div>`
}

func inventoryDataDir() string { return filepath.Dir(DB) }

func inventoryVMDoc(vid int64) *py.OMap {
	d := py.AsMap(ReadJSON(filepath.Join(inventoryDataDir(), "vm", I2S(vid)+".json"), py.NewOMap()))
	if d == nil || d.Len() == 0 {
		return py.NewOMap()
	}
	return d
}

func inventoryPulse() *py.OMap {
	p := py.AsMap(ReadJSON(filepath.Join(inventoryDataDir(), "vm-pulse.json"), py.NewOMap()))
	if p == nil {
		p = py.NewOMap()
	}
	if Now()-py.Num(py.Or(p.Get("at"), int64(0))) < 900 {
		return p
	}
	return py.NewOMap()
}

type inventoryState struct {
	s       any
	reasons []string
}

func inventoryStrs(v any) []string {
	var out []string
	for _, x := range py.AsList(v) {
		out = append(out, py.Str(x))
	}
	return out
}

// inventoryMergedStates is merged_states(st, pv): st is a row (or nil), pv a pulse entry (or nil).
func inventoryMergedStates(st Row, pv *py.OMap) map[string]inventoryState {
	reasons := py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(st.Get("reasons"), "{}"))); err == nil {
		if m := py.AsMap(v); m != nil {
			reasons = m
		}
	}
	out := map[string]inventoryState{}
	for _, rl := range inventoryRes {
		r := rl[0]
		var s any = py.Or(st.Get(r), "unknown")
		rs := inventoryStrs(py.Or(reasons.Get(r), []any{}))
		p := pv.Get(r)
		if py.Truthy(p) && inventoryRankOf(p) > inventoryRankOf(s) {
			s = p
			if x := pv.Get("reasons"); py.Truthy(x) {
				rs = inventoryStrs(x)
			}
		}
		out[r] = inventoryState{s, rs}
	}
	return out
}

// ---- list ----

type inventoryRow struct {
	r      Row
	states map[string]inventoryState
	failed []string
	last   Row
	score  int64
	why    string
	fields []VMSearchField
	fdone  bool
}

func inventoryWhere(qs py.QS) (string, []any, *py.OMap) {
	conds, args, sel := []string{"v.gone=0"}, []any{}, py.NewOMap()
	var flagKeys, viewKeys []string
	for _, f := range inventoryFlags {
		flagKeys = append(flagKeys, f[0])
	}
	for _, v := range inventoryViews {
		viewKeys = append(viewKeys, v[0])
	}
	for _, ka := range []struct {
		k       string
		allowed []string
	}{{"env", inventoryEnvs}, {"dc", inventoryDCs}, {"flag", flagKeys}, {"view", viewKeys}} {
		val := qs.Get(ka.k)
		if !In(val, ka.allowed...) {
			continue
		}
		sel.Set(ka.k, val)
		switch {
		case ka.k == "flag":
			conds = append(conds, "(',' || v.flags || ',') LIKE ?")
			args = append(args, "%,"+val+",%")
		case ka.k == "view" && val == "apps":
			conds = append(conds, "v.env='release' AND coalesce(s.svc_app, 0) > 0")
		case ka.k == "env" || ka.k == "dc":
			conds = append(conds, "v."+ka.k+"=?")
			args = append(args, val)
		}
	}
	return strings.Join(conds, " AND "), args, sel
}

func inventoryHasState() bool {
	return len(Q("SELECT 1 FROM sqlite_master WHERE type='table' AND name='vm_state'")) > 0
}

func inventoryRowsFor(qs py.QS) ([]*inventoryRow, *py.OMap) {
	w, args, sel := inventoryWhere(qs)
	var rows []Row
	if !inventoryHasState() {
		w = strings.ReplaceAll(w, "v.env='release' AND coalesce(s.svc_app, 0) > 0", "0")
		rows = Q("SELECT v.* FROM vms v WHERE "+w+" LIMIT 1000", args...)
	} else {
		rows = Q("SELECT v.*, s.at s_at, s.cpu, s.memory, s.io, s.disk, s.network, s.reasons, s.svc_total, s.svc_app, "+
			"s.svc_healthy, s.svc_degraded, s.svc_restarting, s.svc_down, s.svc_stopped, s.dead_upstreams, s.summary, "+
			"s.ssh_error FROM vms v LEFT JOIN vm_state s ON s.vm_id = v.id "+
			"WHERE "+w+" LIMIT 1000", args...)
	}
	last := map[int64]Row{}
	for _, r := range Q("SELECT r.vm_id, r.created, r.status, r.run_id, r.requested_by, p.title FROM vm_reports r " +
		"LEFT JOIN triage_problems p ON p.run_id = r.run_id AND p.idx = 0 " +
		"WHERE r.id IN (SELECT max(id) FROM vm_reports GROUP BY vm_id)") {
		last[r.I("vm_id")] = r
	}
	pv := py.AsMap(py.Or(inventoryPulse().Get("vms"), py.NewOMap()))
	var out []*inventoryRow
	for _, r := range rows {
		p := py.AsMap(pv.Get(r.Str("id")))
		x := &inventoryRow{r: r, states: inventoryMergedStates(r, p)}
		x.failed = inventoryStrs(py.Or(p.Get("failed_units"), []any{}))
		if l, ok := last[r.I("id")]; ok {
			x.last = l
		}
		x.score = inventoryScore(x)
		out = append(out, x)
	}
	if v := sel.S("view"); v != "" && v != "apps" {
		var f []*inventoryRow
		for _, x := range out {
			if inventoryViewMatch(x, v) {
				f = append(f, x)
			}
		}
		out = f
	}
	if q := VMSearchQuery(qs); q != "" {
		idx := VMSearchSearch(len(out), q, func(i int) []VMSearchField { return inventoryFields(out[i]) },
			func(i int) []any { return inventoryIDs(out[i]) })
		ws := VMSearchWords(q)
		shown := map[string]bool{"name": true, "IP": true, "env": true, "dc": true}
		var res []*inventoryRow
		for _, i := range idx {
			out[i].why = VMSearchWhy(inventoryFields(out[i]), ws, shown)
			res = append(res, out[i])
		}
		out = res
	}
	return out, sel
}

// inventoryQuery is the list's GET state (the same values inventoryWhere accepts), 50 VMs a page.
func inventoryQuery(qs py.QS, sort string) listQ {
	var views, flags []string
	for _, v := range inventoryViews {
		views = append(views, v[0])
	}
	for _, f := range inventoryFlags {
		flags = append(flags, f[0])
	}
	l := newListQ("/inventory", qs, []string{"q", "view", "env", "dc", "flag", "sort"}, map[string]string{"sort": "worst"},
		func(k, v string) bool {
			switch k {
			case "view":
				return In(v, views...)
			case "env":
				return In(v, inventoryEnvs...)
			case "dc":
				return In(v, inventoryDCs...)
			case "flag":
				return In(v, flags...)
			}
			return false
		})
	l.v["q"], l.v["sort"], l.size = VMSearchQuery(qs), sort, 50
	return l
}

// inventoryViewMatch is the in-memory part of the "Show" facet (the KPI figures open exactly these lists).
func inventoryViewMatch(x *inventoryRow, view string) bool {
	switch view {
	case "problems":
		return x.score > 0
	case "critical":
		for _, s := range x.states {
			if s.s == "critical" {
				return true
			}
		}
		return false
	case "notready":
		return x.r.I("svc_down")+x.r.I("svc_restarting") > 0
	case "dead":
		return x.r.T("dead_upstreams")
	case "apps":
		return x.r.Get("env") == "release" && x.r.F("svc_app") > 0
	}
	return true
}

func inventoryIPs(r Row) []string {
	var out []string
	for _, x := range strings.Split(py.OrS(r.Get("ips"), ""), ",") {
		if x = py.Strip(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func inventoryFields(x *inventoryRow) []VMSearchField {
	if x.fdone {
		return x.fields
	}
	r := x.r
	dom := py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(r.Get("domains"), "{}"))); err == nil {
		if m := py.AsMap(v); m != nil {
			dom = m
		}
	}
	var mounts []any
	if v, err := py.ParseJSON([]byte(py.OrS(r.Get("mounts"), "[]"))); err == nil {
		for _, m := range py.AsList(v) {
			mounts = append(mounts, py.AsMap(m).Get("mount"))
		}
	}
	doc := inventoryVMDoc(r.I("id"))
	for _, m := range capacityMaps(py.Or(doc.Get("mounts"), []any{})) {
		mounts = append(mounts, m.Get("mount"))
	}
	f := []VMSearchField{{"name", r.Get("name")}, {"key", r.Get("key")}, {"env", r.Get("env")}, {"dc", r.Get("dc")}}
	for _, ip := range inventoryIPs(r) {
		f = append(f, VMSearchField{"IP", ip})
	}
	f = append(f, VMSearchField{"role", r.Get("prom_job")}, VMSearchField{"instance", r.Get("prom_instance")},
		VMSearchField{"alias", r.Get("ssh_alias")}, VMSearchField{"flag", r.Get("flags")})
	for _, x := range py.AsList(py.Or(dom.Get("data_roles"), []any{})) {
		f = append(f, VMSearchField{"role", py.Str(x)})
	}
	if py.Truthy(dom.Get("edge_tree")) {
		f = append(f, VMSearchField{"edge", dom.Get("edge_tree")})
	}
	for _, x := range py.AsList(py.Or(dom.Get("backend_for"), []any{})) {
		f = append(f, VMSearchField{"domain", py.Str(x)})
	}
	for _, s := range capacityMaps(py.Or(doc.Get("services"), []any{})) {
		if s.Get("verdict") != "idle" {
			f = append(f, VMSearchField{"service", s.Get("unit")})
		}
	}
	var seen []any
	for _, m := range mounts {
		dup := false
		for _, s := range seen {
			if vmsearchEq(s, m) {
				dup = true
			}
		}
		if dup {
			continue
		}
		seen = append(seen, m)
		if py.Truthy(m) {
			f = append(f, VMSearchField{"mount", m})
		}
	}
	x.fields, x.fdone = f, true
	return f
}

func inventoryIDs(x *inventoryRow) []any {
	ids := []any{x.r.Get("name"), x.r.Get("key")}
	for _, ip := range inventoryIPs(x.r) {
		ids = append(ids, ip)
	}
	return ids
}

// InventoryFindOne is views_inventory.find_one(qs).
func InventoryFindOne(qs py.QS) (int64, bool) {
	q := VMSearchQuery(qs)
	if q == "" {
		return 0, false
	}
	rows, _ := inventoryRowsFor(py.NewQS("q", q))
	if len(rows) == 1 {
		return rows[0].r.I("id"), true
	}
	var exact []*inventoryRow
	for _, x := range rows {
		if VMSearchRank(inventoryIDs(x), q) == 0 {
			exact = append(exact, x)
		}
	}
	if len(exact) == 1 {
		return exact[0].r.I("id"), true
	}
	return 0, false
}

func inventoryScore(x *inventoryRow) int64 {
	var crit, pres int64
	for _, s := range x.states {
		if s.s == "critical" {
			crit++
		} else if s.s == "pressure" {
			pres++
		}
	}
	r := x.r
	s := 100*crit + 25*pres
	s += 60*r.I("svc_down") + 60*int64(len(x.failed)) + 30*r.I("svc_restarting")
	du := r.I("dead_upstreams")
	if du > 5 {
		du = 5
	}
	s += 20*du + 8*r.I("svc_degraded") + 3*r.I("svc_stopped")
	if r.T("monitored") && !r.T("up") {
		s += 80
	}
	return s
}

func inventoryWorst(states map[string]inventoryState) any {
	var worst any
	first := true
	for _, rl := range inventoryRes {
		s := states[rl[0]].s
		if first || inventoryRankOf(s) > inventoryRankOf(worst) {
			worst, first = s, false
		}
	}
	return worst
}

func inventoryServicesCell(x *inventoryRow) string {
	r := x.r
	if r.Get("svc_total") == nil {
		return `<span class="dim">-</span>`
	}
	tot := r.I("svc_total")
	if tot == 0 {
		return `<span class="dim">` + If(!r.T("ssh_ok"), "no read-only SSH", "none found") + `</span>`
	}
	var parts []string
	for _, kl := range [][2]string{{"svc_down", "down"}, {"svc_restarting", "restarting"}, {"svc_degraded", "degraded"}, {"svc_stopped", "stopped"}} {
		if r.T(kl[0]) {
			parts = append(parts, r.Str(kl[0])+" "+kl[1])
		}
	}
	if len(x.failed) > 0 {
		f := x.failed
		if len(f) > 2 {
			f = f[:2]
		}
		parts = append([]string{"failed now: " + strings.Join(f, ", ")}, parts...)
	}
	cls := If(r.T("svc_down") || len(x.failed) > 0, " fail", "")
	return `<span class="ready` + cls + `"><b>` + py.OrS(r.Get("svc_healthy"), "0") + `</b> of ` + I2S(tot) + ` ready</span>` + inventoryDim(strings.Join(parts, ", "))
}

func inventoryEnvRank(v any) int {
	switch v {
	case "release":
		return 0
	case "pre-release":
		return 1
	case "develop":
		return 2
	}
	return 3
}

// inventoryList is the list page's state: the VMs of this query in order, every VM, the figures and the facet counts.
type inventoryList struct {
	rows, allrows                             []*inventoryRow
	sort, q                                   string
	ws                                        []string
	l                                         listQ
	st, vst                                   *py.OMap
	crit, notready, dead, deadvm, apps, nprob int64
	anN, anA                                  int64
	viewN, envN, dcN, flagN                   map[string]int
}

// inventoryCompute sorts and counts the list of a query.
func inventoryCompute(qs py.QS) *inventoryList {
	rows, _ := inventoryRowsFor(qs)
	sort_ := qs.GetD("sort", "worst")
	okSort := false
	for _, s := range inventorySorts {
		if s[0] == sort_ {
			okSort = true
		}
	}
	if !okSort {
		sort_ = "worst"
	}
	lowName := func(x *inventoryRow) string { return strings.ToLower(py.OrS(x.r.Get("name"), "")) }
	var less func(a, b *inventoryRow) bool
	switch sort_ {
	case "worst":
		less = func(a, b *inventoryRow) bool {
			if a.score != b.score {
				return a.score > b.score
			}
			ea, eb := inventoryEnvRank(a.r.Get("env")), inventoryEnvRank(b.r.Get("env"))
			if ea != eb {
				return ea < eb
			}
			return lowName(a) < lowName(b)
		}
	case "name":
		less = func(a, b *inventoryRow) bool { return lowName(a) < lowName(b) }
	case "env":
		less = func(a, b *inventoryRow) bool {
			ea, eb := a.r.S("env"), b.r.S("env")
			if ea != eb {
				return ea < eb
			}
			return strings.ToLower(a.r.Str("name")) < strings.ToLower(b.r.Str("name"))
		}
	case "vcpu":
		less = func(a, b *inventoryRow) bool { return a.r.F("vcpu") > b.r.F("vcpu") }
	case "ram":
		less = func(a, b *inventoryRow) bool { return a.r.F("ram_bytes") > b.r.F("ram_bytes") }
	case "used":
		u := func(x *inventoryRow) float64 {
			t := x.r.F("disk_total")
			if t == 0 {
				t = 1
			}
			return x.r.F("disk_used") / t
		}
		less = func(a, b *inventoryRow) bool { return u(a) > u(b) }
	case "seen":
		less = func(a, b *inventoryRow) bool { return a.r.F("last_seen") > b.r.F("last_seen") }
	}
	sort.SliceStable(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
	q := VMSearchQuery(qs)
	ws := VMSearchWords(q)
	if q != "" {
		rk := map[*inventoryRow]int{}
		for _, x := range rows {
			rk[x] = VMSearchRank(inventoryIDs(x), q)
		}
		sort.SliceStable(rows, func(i, j int) bool { return rk[rows[i]] < rk[rows[j]] })
	}
	allrows, _ := inventoryRowsFor(py.QS{})
	st := ReadJSONMap(filepath.Join(inventoryDataDir(), "inventory-status.json"))
	vst := ReadJSONMap(filepath.Join(inventoryDataDir(), "vmstate-status.json"))
	l := inventoryQuery(qs, sort_)
	var crit, notready, dead, deadvm, apps, nprob int64
	for _, x := range allrows {
		if inventoryViewMatch(x, "critical") {
			crit++
		}
		notready += x.r.I("svc_down") + x.r.I("svc_restarting")
		dead += x.r.I("dead_upstreams")
		if x.r.T("dead_upstreams") {
			deadvm++
		}
		if inventoryViewMatch(x, "apps") {
			apps++
		}
		if x.score > 0 {
			nprob++
		}
	}
	an := Q("SELECT count(*) n, sum(requested_by='auto') a FROM vm_reports WHERE created >= ? AND run_id IS NOT NULL", NowInt()-86400)
	var anN, anA int64
	if len(an) > 0 {
		anN, anA = an[0].I("n"), an[0].I("a")
	}
	// facet counts honour the other filters and the search, in memory over every VM
	match := func(x *inventoryRow, skip string) bool {
		if skip != "view" && l.get("view") != "" && !inventoryViewMatch(x, l.get("view")) {
			return false
		}
		if skip != "env" && l.get("env") != "" && x.r.S("env") != l.get("env") {
			return false
		}
		if skip != "dc" && l.get("dc") != "" && x.r.S("dc") != l.get("dc") {
			return false
		}
		if skip != "flag" && l.get("flag") != "" && !strings.Contains(","+x.r.S("flags")+",", ","+l.get("flag")+",") {
			return false
		}
		return len(ws) == 0 || VMSearchMatches(inventoryFields(x), ws)
	}
	viewN, envN, dcN, flagN := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, x := range allrows {
		if match(x, "view") {
			for _, v := range inventoryViews {
				if inventoryViewMatch(x, v[0]) {
					viewN[v[0]]++
				}
			}
		}
		if match(x, "env") {
			envN[x.r.S("env")]++
		}
		if match(x, "dc") {
			dcN[x.r.S("dc")]++
		}
		if match(x, "flag") {
			for _, f := range strings.Split(x.r.S("flags"), ",") {
				if f != "" {
					flagN[f]++
				}
			}
		}
	}
	return &inventoryList{rows: rows, allrows: allrows, sort: sort_, q: q, ws: ws, l: l, st: st, vst: vst, crit: crit, notready: notready,
		dead: dead, deadvm: deadvm, apps: apps, nprob: nprob, anN: anN, anA: anA, viewN: viewN, envN: envN, dcN: dcN, flagN: flagN}
}

// ---- one VM ----

func inventoryDoneAfter(kind string, vid int64, started float64) bool {
	if kind == "refresh" {
		return py.Num(py.Or(inventoryVMDoc(vid).Get("collected_at"), int64(0))) >= started
	}
	r := Q("SELECT max(created) c FROM vm_reports WHERE vm_id=?", vid)
	return len(r) > 0 && py.Num(py.Or(r[0].Get("c"), int64(0))) >= started
}

func inventoryReqState(kind string, vid int64, gap, running float64) (bool, string, bool) {
	sect := "vm"
	if kind == "refresh" {
		sect = "vmrefresh"
	}
	rr := ReadJSONMap(filepath.Join(SET_OUT, "run-requests.json"))
	st := py.AsMap(py.AsMap(rr.GetD(sect, py.NewOMap())).GetD(I2S(vid), py.NewOMap()))
	if st == nil {
		st = py.NewOMap()
	}
	now := Now()
	name := "vm-" + If(kind == "refresh", "refresh", "explain") + "-" + I2S(vid) + ".req"
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", name)); err == nil {
		return false, "queued", true
	}
	started := st.GetD("started", int64(0))
	sv := py.Num(started)
	if py.Truthy(started) && now-sv < running && !inventoryDoneAfter(kind, vid, sv) {
		return false, "running since " + Ago(started), true
	}
	if py.Truthy(started) && now-sv < gap {
		return false, "again in " + strconv.FormatInt(int64(math.Floor((gap-(now-sv))/60))+1, 10) + " min", false
	}
	res := py.Str(st.GetD("result", ""))
	if strings.HasPrefix(res, "skipped") && now-py.Num(st.GetD("requested", int64(0))) < 600 {
		return true, strings.ReplaceAll(res, "skipped: ", "Not started: "), false
	}
	return true, "", false
}

type inventoryPt struct{ t, v float64 }

// inventoryMiniShape is a small chart's paths in a 120 x 28 box; ok is false with fewer than two points.
type inventoryMiniShape struct {
	Area  string `json:"area"`
	Line  string `json:"line"`
	Rule  string `json:"rule,omitempty"` // the y of the pressure threshold
	Label string `json:"label"`
}

func inventoryMiniOf(points any, unit string, thr []any, label string) (inventoryMiniShape, bool) {
	var pts []inventoryPt
	for _, p := range py.AsList(points) {
		l := py.AsList(p)
		if len(l) < 2 || l[1] == nil {
			continue
		}
		pts = append(pts, inventoryPt{py.Num(l[0]), py.Num(l[1])})
	}
	if len(pts) < 2 {
		return inventoryMiniShape{}, false
	}
	w, h := 120.0, 28.0
	t0, t1 := pts[0].t, pts[len(pts)-1].t
	vmax := pts[0].v
	for _, p := range pts {
		if p.v > vmax {
			vmax = p.v
		}
	}
	alt := 0.0
	if len(thr) > 0 && py.Num(thr[0]) <= vmax*3 {
		alt = py.Num(thr[0]) * 1.15
	}
	top := math.Max(vmax, alt)
	if top == 0 {
		top = 1
	}
	span := t1 - t0
	if span == 0 {
		span = 1
	}
	var line []string
	for _, p := range pts {
		x := (p.t - t0) / span * w
		y := (h - 2) - p.v/top*(h-4)
		line = append(line, f1(x)+","+f1(y))
	}
	out := inventoryMiniShape{Area: "M0,28 L" + strings.Join(line, " L") + " L120,28 Z", Line: strings.Join(line, " ")}
	if len(thr) > 0 && py.Num(thr[0]) <= top {
		out.Rule = f1((h - 2) - py.Num(thr[0])/top*(h-4))
	}
	out.Label = label + ": latest " + inventoryFmt(unit, pts[len(pts)-1].v) + ", max " + inventoryFmt(unit, vmax) + ", " + When(t0) + " – " + When(t1)
	return out, true
}

func inventoryCapacityFS(vid int64) map[string]*py.OMap {
	out := map[string]*py.OMap{}
	for _, v := range capacityMaps(py.Or(capacityDoc().Get("vms"), []any{})) {
		if id := v.Get("inventory_id"); py.IsNumber(id) && py.Num(id) == float64(vid) {
			for _, f := range capacityMaps(py.Or(v.Get("filesystems"), []any{})) {
				out[py.Str(f.Get("mount"))] = f
			}
			break
		}
	}
	return out
}

func inventoryDomains(r Row) (*py.OMap, bool) {
	v, err := py.ParseJSON([]byte(py.OrS(r.Get("domains"), "{}")))
	if err != nil {
		return nil, false
	}
	return py.AsMap(v), true
}

func inventoryServes(r Row, doc *py.OMap) string {
	if py.Truthy(doc.Get("upstreams")) {
		return ""
	}
	dom, ok := inventoryDomains(r)
	if !ok || dom == nil {
		dom = py.NewOMap()
	}
	doms := inventoryStrs(py.Or(dom.Get("backend_for"), []any{}))
	edge := dom.Get("edge_tree")
	if len(doms) == 0 && !py.Truthy(edge) {
		return ""
	}
	txt := ""
	if len(doms) > 0 {
		d := doms
		if len(d) > 12 {
			d = d[:12]
		}
		txt = "The service map routes " + Itoa(len(doms)) + " domains here: " + strings.Join(d, ", ") + If(len(doms) > 12, "…", "")
	}
	if py.Truthy(edge) {
		txt += " This VM is an nginx edge of the tree " + py.Str(edge) + " (" + I2S(py.I(py.Or(dom.Get("edge_domains"), int64(0)))) + " domains)."
	}
	return `<p class="section-note">` + E(py.Strip(txt)) + `</p>`
}

func inventoryDataRoles(r Row) string {
	dom, ok := inventoryDomains(r)
	var roles []string
	if ok && dom != nil {
		roles = inventoryStrs(py.Or(dom.Get("data_roles"), []any{}))
	}
	if len(roles) == 0 {
		return ""
	}
	if len(roles) > 12 {
		roles = roles[:12]
	}
	return `<p class="section-note">Data platform: ` + E(strings.Join(roles, ", ")) + `. ` +
		`Source: config/data-platform.json; "(KB)" = from the knowledge base, unverified.</p>`
}

func inventoryShortSummary(sm *py.OMap) string {
	if !py.Truthy(sm.Get("services")) && !py.Truthy(sm.Get("dead_upstreams")) {
		return py.OrS(sm.Get("text"), "no service data")
	}
	parts := []string{py.Str(sm.GetD("healthy", int64(0))) + " healthy"}
	for _, k := range []string{"down", "restarting", "degraded", "stopped"} {
		if py.Truthy(sm.Get(k)) {
			parts = append(parts, py.Str(sm.Get(k))+" "+k)
		}
	}
	out := py.Str(sm.GetD("services", int64(0))) + " services: " + strings.Join(parts, ", ")
	if du := sm.Get("dead_upstreams"); py.Truthy(du) {
		out += " · " + py.Str(du) + " dead upstream" + If(py.Num(du) > 1, "s", "")
	}
	return out
}

// inventoryLoad is a VM's collected document (mounts from the inventory when it has none), its resource states and pulse.
func inventoryLoad(r Row) (*py.OMap, map[string]inventoryState, *py.OMap) {
	vid := r.I("id")
	doc := inventoryVMDoc(vid)
	if !py.Truthy(doc.Get("mounts")) {
		if v, err := py.ParseJSON([]byte(py.OrS(r.Get("mounts"), "[]"))); err == nil {
			if l, ok := v.([]any); ok {
				var ms []any
				for _, x := range l {
					m := py.AsMap(x)
					sz := m.Get("size")
					var avail any
					if _, f := py.Or(sz, int64(0)).(float64); f {
						avail = py.Num(py.Or(sz, int64(0))) - py.Num(py.Or(m.Get("used"), int64(0)))
					} else if _, f := py.Or(m.Get("used"), int64(0)).(float64); f {
						avail = py.Num(py.Or(sz, int64(0))) - py.Num(py.Or(m.Get("used"), int64(0)))
					} else {
						avail = py.I(sz) - py.I(m.Get("used"))
					}
					ms = append(ms, py.NewOMap("mount", m.Get("mount"), "size", sz, "avail", avail))
				}
				doc.Set("mounts", ms)
			}
		}
	}
	var states map[string]inventoryState
	var st []Row
	if inventoryHasState() {
		st = Q("SELECT * FROM vm_state WHERE vm_id=?", vid)
	}
	pvAll := py.AsMap(py.Or(inventoryPulse().Get("vms"), py.NewOMap()))
	if len(st) > 0 {
		states = inventoryMergedStates(st[0], py.AsMap(pvAll.Get(I2S(vid))))
	} else {
		states = map[string]inventoryState{}
		ver := py.AsMap(py.Or(doc.Get("verdicts"), py.NewOMap()))
		for _, rl := range inventoryRes {
			v := py.AsMap(ver.GetD(rl[0], py.NewOMap()))
			states[rl[0]] = inventoryState{v.GetD("state", "unknown"), inventoryStrs(py.Or(v.Get("reasons"), []any{}))}
		}
	}
	pv := py.AsMap(py.Or(pvAll.Get(I2S(vid)), py.NewOMap()))
	return doc, states, pv
}

type inventoryKV struct {
	k string
	v any
}

// inventoryRecord is the VM's addresses and its Identity and Access facts.
func inventoryRecord(r Row, doc *py.OMap) ([]string, []inventoryKV, []inventoryKV) {
	ram := r.Get("ram_bytes")
	var ipl []string
	for _, x := range strings.Split(r.S("ips"), ",") {
		if x != "" {
			ipl = append(ipl, x)
		}
	}
	ntp := py.OrS(r.Get("ntp_sync"), "-")
	if strings.ToLower(r.Str("ntp_sync")) == "yes" {
		ntp = "synchronized"
	}
	var virt []string
	for _, k := range []string{"virt", "hypervisor"} {
		if r.T(k) {
			virt = append(virt, r.Str(k))
		}
	}
	ident := []inventoryKV{{"IP addresses", If(len(ipl) > 0, strings.Join(ipl, ", "), "-")},
		{"Environment", EnvLabel(r.S("env"))}, {"Datacenter", r.Get("dc")}, {"OS", r.Get("os")}, {"Kernel", r.Get("kernel")},
		{"vCPU · RAM", py.OrS(r.Get("vcpu"), "-") + " · " + inventoryGB(ram)},
		{"Uptime", inventoryUptime(r.Get("boot_time")) + ", booted " + When(r.Get("boot_time"))},
		{"Virtualization", If(len(virt) > 0, strings.Join(virt, " · "), "-")},
		{"Time", py.OrS(r.Get("timezone"), "-") + ", NTP " + ntp},
		{"Disk", inventoryGB(r.Get("disk_total")) + ", " + inventoryGB(r.Get("disk_used")) + " used"}}
	host := py.AsMap(py.Or(doc.Get("host"), py.NewOMap()))
	hp := doc.Get("hidepid")
	ssh := py.OrS(r.Get("ssh_alias"), "no alias")
	if r.T("ssh_alias") {
		if r.T("ssh_ok") {
			ssh += " (ok)"
		} else if strings.Contains(r.S("flags"), "ssh-skipped-db") {
			ssh += " (skipped: DB host)"
		} else {
			ssh += " (no answer)"
		}
	}
	prom := py.OrS(r.Get("prom_instance"), "not monitored")
	if r.T("monitored") {
		prom += " (" + If(r.T("up"), "up", "down") + ")"
	}
	procs := "-"
	if py.Truthy(hp) && hp != "0" && hp != "off" {
		procs = "hidden by hidepid; per-unit numbers come from cgroups"
	} else if py.Truthy(doc.Get("ssh_at")) {
		procs = "visible"
	}
	var loads []string
	for _, x := range py.AsList(py.Or(host.Get("load"), []any{})) {
		loads = append(loads, py.F(py.Num(x), 1))
	}
	access := []inventoryKV{{"Prometheus", prom}, {"ssh-ro", ssh}, {"Processes", procs},
		{"Load 1 / 5 / 15", If(len(loads) > 0, strings.Join(loads, " / "), "-")},
		{"Flags", py.OrS(r.Get("flags"), "-")}, {"First seen", When(r.Get("first_seen"))},
		{"Collected", When(doc.Get("collected_at"))}}
	return ipl, ident, access
}

// inventoryOpenIncidents are the open incidents that name this VM or one of its addresses.
func inventoryOpenIncidents(r Row, ipl []string) []Row {
	name := strings.ToLower(r.S("name"))
	var out []Row
	for _, i := range Q("SELECT id, title, status, subject FROM incidents WHERE status IN ('open','acknowledged')") {
		subj := i.S("subject")
		match := name != "" && (strings.Contains(strings.ToLower(subj), name) || strings.Contains(strings.ToLower(i.S("title")), name))
		if !match && name != "" {
			for _, ip := range ipl {
				if strings.Contains(subj, ip) {
					match = true
				}
			}
		}
		if match {
			out = append(out, i)
		}
	}
	return out
}

// inventoryRawFacts are the ssh facts as sorted name and value pairs (mounts have their own table).
func inventoryRawFacts(facts *py.OMap) [][2]string {
	keys := append([]string(nil), facts.Keys()...)
	sort.Strings(keys)
	var out [][2]string
	for _, k := range keys {
		if k == "mounts" {
			continue
		}
		v := facts.Get(k)
		var s string
		switch v.(type) {
		case []any, *py.OMap:
			s = py.Dumps(v, -1)
		case nil:
			s = "-" // a missing fact, never Python's "None"
		default:
			s = py.Str(v)
		}
		out = append(out, [2]string{k, py.Head(s, 300)})
	}
	return out
}

// ---- requests ----

func inventoryRequest(kind string, form py.QS, sess *Session) (int, string) {
	vs := form.Get("id")
	if !py.IsDigit(vs) || py.Len(vs) > 9 {
		return 400, "Invalid request."
	}
	vid, _ := py.ParseInt(vs)
	if len(Q("SELECT 1 FROM vms WHERE id=? AND gone=0", vid)) == 0 {
		return 404, "No such VM in the inventory."
	}
	gap, running := float64(inventoryRefreshGap), 300.0
	if kind != "refresh" {
		gap, running = inventoryAnalyseGap, 1200
	}
	ok, msg, _ := inventoryReqState(kind, vid, gap, running)
	if !ok {
		return 429, "Not started: " + msg + "."
	}
	name := "vm-" + If(kind == "refresh", "refresh", "explain") + "-" + I2S(vid)
	d := filepath.Join(SET_IN, "requests")
	if err := os.MkdirAll(d, 0o777); err != nil {
		panic(err)
	}
	body := py.Dumps(py.NewOMap("by", sess.Username, "at", NowInt()), -1)
	if err := WriteFileAtomic(filepath.Join(d, name+".req"), filepath.Join(d, "."+name+".tmp"), []byte(body)); err != nil {
		panic(err)
	}
	logf("VM %s %s requested by %s", kind, vs, sess.Username)
	return 303, "/inventory/view?id=" + I2S(vid) + "&sent=" + kind
}

// InventoryPost is post_refresh / post_analyse by path.
func InventoryPost(c *Ctx, path string, form py.QS, sess *Session) (int, string) {
	if strings.HasSuffix(path, "refresh") {
		return inventoryRequest("refresh", form, sess)
	}
	if sess.Role != "admin" {
		return 403, "Only admins can start a VM analysis."
	}
	vs := form.Get("id")
	if py.IsDigit(vs) && py.Len(vs) <= 9 {
		n, _ := py.ParseInt(vs)
		if r := Q("SELECT flags FROM vms WHERE id=?", n); len(r) > 0 && strings.Contains(r[0].S("flags"), "ssh-skipped-db") {
			return 403, "Not for DB hosts: Argus never logs in to them."
		}
	}
	return inventoryRequest("analyse", form, sess)
}

var (
	inventoryCSVCols  = []string{"name", "ips", "env", "dc", "os", "kernel", "vcpu", "ram_bytes", "disk_total", "disk_used", "boot_time", "virt", "hypervisor", "monitored", "up", "ssh_alias", "ssh_ok", "docker", "containers", "services_running", "flags", "first_seen", "last_seen"}
	inventoryCSVState = []string{"cpu", "memory", "io", "disk", "network", "svc_total", "svc_healthy", "svc_down", "svc_restarting", "dead_upstreams", "summary"}
)

// inventoryCSVField is csv.writer's QUOTE_MINIMAL for one value.
func inventoryCSVField(v any) string {
	s := ""
	if v != nil {
		s = py.Str(v)
	}
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// InventoryCSV is csv_export(qs).
func InventoryCSV(qs py.QS) string {
	rows, _ := inventoryRowsFor(qs)
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].r, rows[j].r
		if a.S("env") != b.S("env") {
			return a.S("env") < b.S("env")
		}
		return strings.ToLower(a.S("name")) < strings.ToLower(b.S("name"))
	})
	var b strings.Builder
	head := append(append([]string{}, inventoryCSVCols...), inventoryCSVState...)
	for i, h := range head {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(inventoryCSVField(h))
	}
	b.WriteString("\r\n")
	for _, x := range rows {
		var vals []any
		for _, c := range inventoryCSVCols {
			vals = append(vals, x.r.Get(c))
		}
		for _, c := range inventoryCSVState {
			if st, ok := x.states[c]; ok {
				vals = append(vals, st.s)
			} else {
				vals = append(vals, x.r.Get(c))
			}
		}
		for i, v := range vals {
			if i > 0 {
				b.WriteByte(',')
			}
			if s, ok := v.(string); ok && (s == "" || strings.ContainsAny(s[:1], "=+-@")) {
				v = "'" + s
			}
			b.WriteString(inventoryCSVField(v))
		}
		b.WriteString("\r\n")
	}
	return b.String()
}
