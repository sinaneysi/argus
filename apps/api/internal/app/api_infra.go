package app

// The Infrastructure section as JSON: GET /api/v1/inventory (+ /item), /capacity, /certs, /hygiene, /checks, and POST
// /api/v1/scan, /inventory/refresh, /inventory/analyse, /capacity/analyse. Same queries, request files and roles as
// the pages they replace. The Backups pages are in api_backups.go.

import (
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

type apiPill struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
}

var rePillClass = regexp.MustCompile(`class="(?:badge|tag)\s*([a-z]*)`)

// apiPillOf is a badge or tag of a Go helper as data.
func apiPillOf(html string) apiPill {
	p := apiPill{Text: Text(html)}
	if m := rePillClass.FindStringSubmatch(html); m != nil {
		p.Kind = m[1]
	}
	return p
}

func apiBadge(status any) apiPill { return apiPillOf(Badge(status)) }

// apiRefresh is c.Refresh as data: seconds until the page loads again and where to, nil when it does not.
func apiRefresh(c *Ctx) map[string]any {
	switch r := c.Refresh.(type) {
	case bool:
		if r {
			return map[string]any{"seconds": 5}
		}
	case int:
		return map[string]any{"seconds": r}
	case string:
		return map[string]any{"seconds": 2, "url": SafeNext(r)}
	}
	return nil
}

// apiScanBar is the scan control of a page: what a click asks for, and the line beside it.
func apiScanBar(active string) map[string]any {
	tasks := SCAN_TASKS[active]
	meta, busy, overdue := scanBarState(tasks)
	labels := []string{}
	for _, t := range tasks {
		labels = append(labels, SCAN_LABEL[t])
	}
	out := map[string]any{"tasks": strings.Join(tasks, ","), "back": active, "label": "Scan now", "busy": busy, "meta": meta,
		"title": "Run " + strings.Join(labels, " and ") + " again now", "overdue": ""}
	if active == "/operations" {
		out["tasks"], out["label"] = "all", "Scan all"
	}
	if overdue > 0 {
		out["overdue"], out["busy"] = "Scan asked "+Ago(overdue)+", not finished", false
	}
	return out
}

func apiBar(pct float64) float64 { return max(0, min(100, pct)) }

// --- Inventory ---------------------------------------------------------------------------------------------------

func apiInvHealth(x *inventoryRow) map[string]any {
	r := x.r
	if r.T("monitored") && !r.T("up") {
		return map[string]any{"word": "target down", "kind": "fail", "detail": "", "title": ""}
	}
	worst := inventoryWorst(x.states)
	if worst == "unknown" {
		return map[string]any{"word": "no data", "kind": "", "detail": "", "title": ""}
	}
	var bad, rs []string
	for _, rl := range inventoryRes {
		if s := x.states[rl[0]].s; s == "critical" || s == "pressure" {
			bad = append(bad, rl[1])
		}
		rs = append(rs, x.states[rl[0]].reasons...)
	}
	if len(bad) == 0 {
		return map[string]any{"word": "ok", "kind": "ok", "detail": "", "title": ""}
	}
	ws := py.Str(worst)
	return map[string]any{"word": ws, "kind": inventoryStateBdg[ws], "detail": strings.Join(bad, ", "), "title": py.Head(strings.Join(rs, "; "), 300)}
}

func apiInvRow(x *inventoryRow, cvec map[int64]cveCount, ws []string) map[string]any {
	r := x.r
	used := "-"
	if r.T("disk_total") {
		used = py.F(r.F("disk_used")*100/r.F("disk_total"), 0) + " %"
	}
	flags := []apiOption{}
	for _, f := range strings.Split(r.S("flags"), ",") {
		if f != "" && f != "disk-85" {
			d, _ := inventoryFlagDesc(f)
			flags = append(flags, apiOption{d, inventoryFlagName(f)})
		}
	}
	ips := inventoryIPs(r)
	ip, more := "-", 0
	if len(ips) > 0 {
		ip, more = ips[0], len(ips)-1
		for _, c := range ips {
			for _, w := range ws {
				if strings.Contains(strings.ToLower(c), w) {
					ip = c
				}
			}
		}
	}
	an := map[string]any(nil)
	if a := x.last; a != nil {
		switch {
		case a.Get("status") == "NO_ACCESS":
			an = map[string]any{"note": "no access"}
		case a.Get("status") == "REFUSED":
			an = map[string]any{"note": "not allowed"}
		case a.T("run_id"):
			who := "auto"
			if a.Get("requested_by") != "auto" {
				who = py.OrS(a.Get("requested_by"), "")
			}
			an = map[string]any{"href": "/incidents/problem?run=" + I2S(a.I("run_id")) + "&idx=0", "title": a.S("title"), "at": a.I("created"), "who": who}
		}
	}
	return map[string]any{"id": r.I("id"), "name": r.S("name"), "ip": ip, "ipMore": more, "hit": Text(x.why), "flags": flags, "env": r.S("env"), "dc": r.S("dc"),
		"health": apiInvHealth(x), "services": Text(inventoryServicesCell(x)), "servicesFail": strings.Contains(inventoryServicesCell(x), "ready fail"),
		"dead": r.I("dead_upstreams"), "cve": cveLine(cvec, r.I("id")), "analysis": an, "vcpu": py.OrS(r.Get("vcpu"), "-"),
		"ram": inventoryGB(r.Get("ram_bytes")), "used": used, "disk": inventoryGB(r.Get("disk_total")), "uptime": inventoryUptime(r.Get("boot_time"))}
}

// apiInventoryJSON is GET /api/v1/inventory: the list with the query parameters of /inventory; ?go=1 with one match
// names that VM's page ("goto"), as the sidebar search did.
func apiInventoryJSON(qs py.QS, _ *Session) (any, *apiErr) {
	if gl := qs.List("go"); len(gl) == 1 && gl[0] == "1" {
		if vid, ok := InventoryFindOne(qs); ok {
			return map[string]any{"goto": "/inventory/view?id=" + I2S(vid)}, nil
		}
	}
	il := inventoryCompute(qs)
	l := il.l
	tile := func(label string, v int64, sub, view string, hot bool) apiTile {
		t := apiTile{Label: label, Figure: I2S(v), Sub: sub, Hot: hot, Href: l.cleared().with("view", view).href()}
		if view != "" && l.get("view") == view {
			t.Href, t.Current, t.Sub = l.with("view", "").href(), true, "shown below · show all"
		}
		return t
	}
	tiles := []apiTile{tile("VMs", int64(len(il.allrows)), I2S(il.apps)+" production app hosts", "", false),
		tile("Critical now", il.crit, "a resource at its critical level", "critical", il.crit > 0),
		tile("Services not ready", il.notready, "down or restarting", "notready", il.notready > 0),
		tile("Dead upstreams", il.dead, "on "+I2S(il.deadvm)+" VMs: nginx routes there, nothing listens", "dead", il.dead > 0),
		{Label: "Analyses in 24 h", Figure: I2S(il.anN), Sub: I2S(il.anA) + " automatic", Href: "/runs"}}
	var envs, dcs, flags [][2]string
	for _, v := range inventoryEnvs {
		envs = append(envs, [2]string{v, v})
	}
	for _, v := range inventoryDCs {
		dcs = append(dcs, [2]string{v, v})
	}
	for _, f := range inventoryFlags {
		flags = append(flags, [2]string{f[0], inventoryFlagName(f[0])})
	}
	facets := []apiFacet{apiFacetOf("view", "Show", l.get("view"), inventoryViews, il.viewN), apiFacetOf("env", "Environment", l.get("env"), envs, il.envN),
		apiFacetOf("dc", "Datacenter", l.get("dc"), dcs, il.dcN), apiFacetOf("flag", "Flags", l.get("flag"), flags, il.flagN)}
	var sorts [][3]string
	for _, x := range inventorySorts {
		sorts = append(sorts, [3]string{x[0], x[1], x[1]})
	}
	from, to := listPage(&l, len(il.rows))
	cvec := CveVMCounts()
	rows := []map[string]any{}
	for _, x := range il.rows[from:to] {
		rows = append(rows, apiInvRow(x, cvec, il.ws))
	}
	var sqp [][2]string
	for _, p := range l.pairs() {
		if !In(p[0], "sort", "page") {
			sqp = append(sqp, p)
		}
	}
	csv := "/inventory.csv"
	if len(sqp) > 0 {
		csv += "?" + py.Urlencode(sqp)
	}
	pressure := "pressure from the last check"
	if inventoryPulse().Len() > 0 {
		pressure = "pressure re-measured every 5 min"
	}
	return map[string]any{"needs": il.nprob, "total": len(il.allrows), "servicesAt": il.vst.Get("at"), "pressure": pressure, "factsAt": il.st.Get("at"),
		"tiles": tiles, "facets": facets, "list": apiListOf(l, len(il.rows), len(il.allrows), "VMs", sorts), "rows": rows, "csv": csv,
		"scan": apiScanBar("/inventory"), "refresh": apiScanBusy("/inventory")}, nil
}

func apiScanBusy(active string) bool {
	_, busy, _ := scanBarState(SCAN_TASKS[active])
	return busy
}

func apiInvResources(doc *py.OMap, states map[string]inventoryState) ([]map[string]any, map[string]any) {
	now := py.AsMap(py.Or(doc.Get("now"), py.NewOMap()))
	memused := now.Get("mem_used_pct")
	if memused == nil && now.Get("mem_avail_pct") != nil {
		memused = 100 - py.Num(now.Get("mem_avail_pct"))
	}
	figs := map[string][2]string{"cpu": {inventoryFmt("pct", now.Get("cpu_busy")), "busy"}, "memory": {inventoryFmt("pct", memused), "used"},
		"io": {inventoryFmt("pct", now.Get("psi_io")), "waiting"}, "disk": {inventoryFmt("pct", now.Get("disk_used_max")), "fullest"},
		"network": {inventoryFmt("Bps", now.Get("net_rx")), "in"}}
	out := []map[string]any{}
	for _, rl := range inventoryRes {
		st := states[rl[0]]
		note := "within limits"
		if len(st.reasons) > 0 {
			note = st.reasons[0]
		} else if st.s == "unknown" {
			note = "no data"
		}
		state := ""
		if _, ok := inventoryStateBdg[py.Str(st.s)]; ok && st.s != nil {
			state = py.Str(st.s)
		}
		out = append(out, map[string]any{"label": rl[1], "figure": figs[rl[0]][0], "unit": If(figs[rl[0]][0] != "-", figs[rl[0]][1], ""),
			"state": state, "kind": inventoryStateBdg[state], "note": note, "title": strings.Join(st.reasons, "; ")})
	}
	sm := py.AsMap(py.Or(doc.Get("summary"), py.NewOMap()))
	tot := py.Or(sm.Get("services"), int64(0))
	var parts []string
	for _, k := range []string{"down", "restarting"} {
		if py.Truthy(sm.Get(k)) {
			parts = append(parts, py.Str(sm.Get(k))+" "+k)
		}
	}
	if py.Truthy(sm.Get("dead_upstreams")) {
		parts = append(parts, py.Str(sm.Get("dead_upstreams"))+" dead upstreams")
	}
	for _, k := range []string{"degraded", "stopped"} {
		if py.Truthy(sm.Get(k)) {
			parts = append(parts, py.Str(sm.Get(k))+" "+k)
		}
	}
	note := strings.Join(parts, ", ")
	if note == "" {
		note = If(py.Truthy(tot), "all healthy", "no service data")
	}
	ready := map[string]any{"figure": "-", "of": "", "note": note,
		"hot": py.Num(py.Or(sm.Get("down"), int64(0)))+py.Num(py.Or(sm.Get("restarting"), int64(0))) != 0}
	if py.Truthy(tot) {
		ready["figure"], ready["of"] = py.Str(sm.GetD("healthy", int64(0))), py.Str(tot)
	}
	return out, ready
}

func apiInvServices(doc *py.OMap) map[string]any {
	rows := []map[string]any{}
	idle := []string{}
	for _, s := range capacityMaps(py.Or(doc.Get("services"), []any{})) {
		if s.Get("verdict") == "idle" {
			idle = append(idle, py.Str(s.Get("unit")))
			continue
		}
		var ml []string
		if py.Truthy(s.Get("mem_peak")) {
			ml = append(ml, "peak "+inventoryMem(s.Get("mem_peak")))
		}
		if s.Get("anon") != nil {
			ml = append(ml, "process memory "+inventoryMem(s.Get("anon")))
		}
		kind := py.OrS(s.Get("kind"), "")
		rk := inventoryReadyBdg[py.Str(s.Get("verdict"))]
		if rk == "" || s.Get("verdict") == nil {
			rk = "warn"
		}
		restarts, up := "-", "-"
		if s.Get("restarts") != nil {
			restarts = py.Str(s.Get("restarts"))
		}
		if s.Get("active") == "active" {
			up = inventorySince(s.Get("since"))
		}
		ports, dead := []string{}, []string{}
		for i, p := range py.AsList(py.Or(s.Get("ports"), []any{})) {
			if i >= 6 {
				ports = append(ports, "+"+Itoa(len(py.AsList(s.Get("ports")))-6))
				break
			}
			ports = append(ports, ":"+I2S(py.I(p)))
		}
		for i, p := range py.AsList(py.Or(s.Get("dead_ports"), []any{})) {
			if i >= 4 {
				break
			}
			dead = append(dead, ":"+I2S(py.I(p))+" dead")
		}
		rows = append(rows, map[string]any{"unit": py.Str(s.Get("unit")), "kind": If(kind != "app", kind, ""), "why": strings.Join(inventoryStrs(py.Or(s.Get("why"), []any{})), "; "),
			"verdict": apiPill{Text: py.Str(s.Get("verdict")), Kind: rk}, "memory": inventoryMem(s.Get("mem")), "memoryNote": strings.Join(ml, " · "),
			"cpu": inventoryFmtCPU(s.Get("cpu_pct")), "restarts": restarts, "up": up, "ports": ports, "deadPorts": dead, "errors": py.Str(py.Or(s.Get("errors_1h"), int64(0))),
			"routes": append([]string{}, inventoryStrs(py.Or(s.Get("domains"), []any{}))...)})
	}
	src := "No read-only snapshot for this VM: states come from Prometheus (node_systemd_unit_state) where it reports them."
	if py.Truthy(doc.Get("ssh_at")) {
		src = "From the read-only snapshot: systemd and the unit's cgroup, which hidepid does not hide. Process memory = anonymous " +
			"memory (process heap and stacks); the rest is page cache and kernel memory. CPU is measured over 3 seconds " +
			"(100 % = one core). Errors: journal lines of the last hour with an error priority or an error word."
	}
	return map[string]any{"rows": rows, "idle": idle, "source": src}
}

func apiInvUpstreams(doc *py.OMap) (map[string]any, int) {
	dead, ok, repo := []map[string]any{}, []map[string]any{}, []map[string]any{}
	all := capacityMaps(py.Or(doc.Get("upstreams"), []any{}))
	for _, u := range all {
		l := u.Get("listening")
		p := apiPill{"dead", "fail", ""}
		switch {
		case l == nil:
			p = apiPill{"unknown", "warn", ""}
		case py.Truthy(l):
			p = apiPill{"listening", "ok", ""}
		case py.Truthy(u.Get("repo_only")):
			p = apiPill{"repo only", "warn", "only the nginx repo routes here; the live config does not"}
		}
		routes := []apiOption{}
		for i, x := range capacityMaps(py.Or(u.Get("refs"), []any{})) {
			if i >= 4 {
				break
			}
			routes = append(routes, apiOption{py.Str(x.Get("domain")) + py.OrS(x.Get("location"), ""), py.Str(x.Get("source"))})
		}
		more := int64(0)
		if py.Num(py.Or(u.Get("n_refs"), int64(0))) > 4 {
			more = py.I(u.Get("n_refs")) - 4
		}
		row := map[string]any{"port": py.I(u.Get("port")), "state": p, "unit": py.OrS(u.Get("unit"), "-"), "routes": routes, "more": more}
		switch {
		case py.Truthy(l):
			ok = append(ok, row)
		case py.Truthy(u.Get("repo_only")):
			repo = append(repo, row)
		default:
			dead = append(dead, row)
		}
	}
	n := 0
	for _, u := range all {
		if u.Get("listening") == false && !py.Truthy(u.Get("repo_only")) {
			n++
		}
	}
	return map[string]any{"count": len(all), "deadCount": n, "dead": dead, "ok": ok, "repo": repo}, n
}

func apiInvConsumers(doc *py.OMap, ram any) map[string]any {
	var svcs []*py.OMap
	for _, s := range capacityMaps(py.Or(doc.Get("services"), []any{})) {
		if py.Truthy(s.Get("mem")) {
			svcs = append(svcs, s)
		}
	}
	for _, t := range capacityMaps(py.Or(doc.Get("top_cgroups"), []any{})) {
		if py.Truthy(t.Get("mem")) {
			svcs = append(svcs, py.NewOMap("unit", t.Get("cgroup"), "mem", t.Get("mem"), "anon", t.Get("stat_anon"), "cpu_pct", t.Get("cpu_pct")))
		}
	}
	if len(svcs) == 0 {
		return nil
	}
	whole := py.Num(ram)
	if !py.Truthy(ram) {
		for _, x := range svcs {
			whole += py.Num(x.Get("mem"))
		}
	}
	byMem := append([]*py.OMap{}, svcs...)
	sortMaps(byMem, "mem")
	mem := []map[string]any{}
	for i, s := range byMem {
		if i >= 10 {
			break
		}
		share := 0.0
		if whole != 0 {
			share = py.Num(s.Get("mem")) * 100 / whole
		}
		note := ""
		if s.Get("anon") != nil {
			note = "process memory " + inventoryMem(s.Get("anon")) + " · page cache and kernel " + inventoryMem(py.Num(s.Get("mem"))-py.Num(s.Get("anon")))
		}
		mem = append(mem, map[string]any{"unit": py.Str(s.Get("unit")), "pct": apiBar(share), "figure": inventoryMem(s.Get("mem")), "share": py.F(share, 0) + " % of RAM", "note": note})
	}
	vcpu := py.Or(py.AsMap(py.Or(doc.Get("host"), py.NewOMap())).Get("nproc"), int64(0))
	den := py.Num(vcpu)
	if den == 0 {
		den = 1
	}
	var busy []*py.OMap
	for _, s := range svcs {
		if py.Truthy(s.Get("cpu_pct")) {
			busy = append(busy, s)
		}
	}
	sortMaps(busy, "cpu_pct")
	cpu := []map[string]any{}
	for i, s := range busy {
		if i >= 6 {
			break
		}
		share := ""
		if py.Truthy(vcpu) {
			share = py.F(py.Num(s.Get("cpu_pct"))/100, 1) + " of " + py.Str(vcpu) + " vCPU"
		}
		cpu = append(cpu, map[string]any{"unit": py.Str(s.Get("unit")), "pct": apiBar(py.Num(s.Get("cpu_pct")) / den), "figure": inventoryFmtCPU(s.Get("cpu_pct")), "share": share, "note": ""})
	}
	return map[string]any{"memory": mem, "cpu": cpu}
}

func sortMaps(l []*py.OMap, k string) {
	sort.SliceStable(l, func(i, j int) bool { return py.Num(l[i].Get(k)) > py.Num(l[j].Get(k)) })
}

func apiInvPressure(doc *py.OMap, states map[string]inventoryState) []map[string]any {
	series := py.AsMap(py.Or(doc.Get("series"), py.NewOMap()))
	thr := py.AsMap(py.Or(doc.Get("thresholds"), py.NewOMap()))
	out := []map[string]any{}
	if series.Len() == 0 {
		return out
	}
	names := map[string]string{}
	for _, rl := range inventoryRes {
		names[rl[0]] = rl[1]
	}
	for _, g := range inventoryGroups {
		st, ok := states[g.res]
		if !ok {
			st = inventoryState{"unknown", nil}
		}
		rows := []map[string]any{}
		for _, k := range g.keys {
			if !py.Truthy(series.Get(k)) {
				continue
			}
			sr := py.AsMap(series.Get(k))
			label, unit := inventoryLabels[k][0], inventoryLabels[k][1]
			var nowv, mx any
			for _, p := range py.AsList(py.Or(sr.Get("24h"), []any{})) {
				if l := py.AsList(p); len(l) >= 2 {
					nowv = l[1]
					if l[1] != nil && (mx == nil || py.Num(l[1]) > py.Num(mx)) {
						mx = l[1]
					}
				}
			}
			t := py.AsList(thr.Get(k))
			hot, limits := "", ""
			if len(t) > 1 {
				limits = "pressure at " + inventoryFmt(unit, t[0]) + ", critical at " + inventoryFmt(unit, t[1])
				if nowv != nil && py.Num(nowv) >= py.Num(t[1]) {
					hot = "fail"
				} else if nowv != nil && py.Num(nowv) >= py.Num(t[0]) {
					hot = "warn"
				}
			}
			row := map[string]any{"label": label, "limits": limits, "now": inventoryFmt(unit, nowv), "hot": hot, "max": inventoryFmt(unit, mx), "day": nil, "week": nil}
			if m, ok := inventoryMiniOf(sr.Get("24h"), unit, t, label+", 24 h"); ok {
				row["day"] = m
			}
			if m, ok := inventoryMiniOf(sr.Get("7d"), unit, t, label+", 7 days"); ok {
				row["week"] = m
			}
			rows = append(rows, row)
		}
		kind := inventoryStateBdg[py.Str(st.s)]
		out = append(out, map[string]any{"name": names[g.res], "state": If(kind != "", py.Str(st.s), ""), "kind": kind, "why": strings.Join(st.reasons, "; "), "rows": rows})
	}
	return out
}

func apiInvMounts(doc *py.OMap, vid int64) []map[string]any {
	trend := inventoryCapacityFS(vid)
	out := []map[string]any{}
	for _, m := range capacityMaps(py.Or(doc.Get("mounts"), []any{})) {
		var used any
		ino := "-"
		if py.Truthy(m.Get("size")) {
			used = 100 * (1 - py.Num(py.Or(m.Get("avail"), int64(0)))/py.Num(m.Get("size")))
		}
		if py.Truthy(m.Get("files")) {
			ino = py.F(100*(1-py.Num(py.Or(m.Get("ffree"), int64(0)))/py.Num(m.Get("files"))), 0) + " %"
		}
		u := py.Num(py.Or(used, int64(0)))
		kind := "ok"
		if u >= 95 {
			kind = "fail"
		} else if u >= 85 {
			kind = "warn"
		}
		t := trend[py.Str(m.Get("mount"))]
		days, growth := m.Get("days_to_full"), "—"
		if t != nil && t.Len() > 0 {
			days, growth = t.Get("days"), Text(CapacityGrowth(t))
		}
		if !py.IsNumber(days) {
			days = nil
		}
		out = append(out, map[string]any{"mount": py.Str(m.Get("mount")), "size": inventoryGB(m.Get("size")), "pct": apiBar(u), "kind": kind,
			"used": If(used != nil, py.F(u, 0)+" %", "-"), "inodes": ino, "growth": growth, "days": CapacityFmtDays(days)})
	}
	return out
}

func apiInvAnalysis(c *Ctx, vid int64) map[string]any {
	reps := Q("SELECT r.*, p.title, p.summary, p.rca, p.severity, p.environment FROM vm_reports r LEFT JOIN "+
		"triage_problems p ON p.run_id = r.run_id AND p.idx = 0 WHERE r.vm_id=? ORDER BY r.created DESC LIMIT 20", vid)
	out := map[string]any{"latest": nil, "history": []map[string]any{}}
	var latest Row
	for _, r := range reps {
		if r.T("run_id") && r.T("rca") {
			latest = r
			break
		}
	}
	if latest != nil {
		rid := latest.I("run_id")
		env := py.Or(latest.Get("environment"), latest.Get("env"))
		who := "by " + py.OrS(latest.Get("requested_by"), "manual")
		if latest.Get("requested_by") == "auto" {
			who = "automatic: " + latest.Str("trigger")
		}
		more := []map[string]any{}
		for _, o := range Q("SELECT idx, title, severity FROM triage_problems WHERE run_id=? AND idx > 0 ORDER BY idx", rid) {
			more = append(more, map[string]any{"href": "/incidents/problem?run=" + I2S(rid) + "&idx=" + I2S(o.I("idx")), "title": o.Str("title"), "severity": o.S("severity")})
		}
		prob := Q("SELECT * FROM triage_problems WHERE run_id=? AND idx=0", rid)
		body := latest.S("rca")
		var lazy Lazy
		lzOK := false
		var fix map[string]any
		if len(prob) > 0 {
			if lazy, lzOK = LazyRow(prob[0], "rca"); lzOK {
				body = LazyStrip(body)
			}
			h := FixHandoffProblem(prob[0])
			pd := Row{}
			for k, v := range prob[0] {
				pd[k] = v
			}
			pd["fix_prompt"], pd["environment"] = h.Prompt, py.OrS(env, "")
			acts := ""
			if env != "release" {
				acts = FixerProblemActions(c, pd)
			}
			fix = apiFix(h, py.OrS(env, ""), "run="+I2S(rid)+"&idx=0", acts, "")
		}
		out["latest"] = map[string]any{"title": latest.S("title"), "severity": py.OrS(latest.Get("severity"), "?"), "severityKind": getD(sevKind, latest.S("severity"), "warn"),
			"at": latest.I("created"), "who": who, "mode": latest.S("mode"), "cost": "$" + py.F(py.Num(py.Or(latest.Get("cost_usd"), int64(0))), 2),
			"href": "/incidents/problem?run=" + I2S(rid) + "&idx=0", "prod": env == "release", "board": apiBoardBtn("vm", latest.I("id")), "lazy": apiLazy(lazy, lzOK),
			"summary": latest.S("summary"), "report": apiMd(body, If(fix != nil, "strip", "fold")), "folded": lzOK, "fix": fix, "more": more}
	}
	hist := []map[string]any{}
	for _, r := range reps {
		who, cost := "auto", "-"
		if r.Get("requested_by") != "auto" {
			who = py.OrS(r.Get("requested_by"), "")
		}
		if r.T("cost_usd") {
			cost = "$" + py.F(r.F("cost_usd"), 2)
		}
		row := map[string]any{"at": r.I("created"), "status": apiBadge(py.OrS(r.Get("status"), "?")), "report": If(r.Get("status") == "NO_ACCESS", "no read-only access", "not allowed for this VM"),
			"href": "", "who": who, "trigger": r.S("trigger"), "cost": cost}
		if r.T("run_id") {
			row["report"], row["href"] = py.OrS(r.Get("title"), "report"), "/incidents/problem?run="+I2S(r.I("run_id"))+"&idx=0"
		}
		hist = append(hist, row)
	}
	out["history"] = hist
	return out
}

func apiInvVulns(vid int64) map[string]any {
	rows, note := cveVMRows(vid)
	comps := []map[string]any{}
	for _, r := range rows {
		comps = append(comps, map[string]any{"priority": apiPillOf(cvePriBadge(r.S("pri"))), "href": cveLink("group", r.Str("group_key")), "name": r.Str("component_name"),
			"version": r.S("version"), "kev": r.T("kev"), "kind": cveKindOf(r.Str("kind")), "vulns": r.I("n"), "fixed": If(r.S("fixed") != "", r.S("fixed"), "no fix yet")})
	}
	svcs := []map[string]any{}
	for _, r := range cveVMServiceRows(vid) {
		list := []apiLink{}
		gs := strings.Split(r.S("groups"), ",")
		sort.Strings(gs)
		for i, g := range gs {
			if i >= 4 {
				list = append(list, apiLink{Text: "+" + Itoa(len(gs)-4)})
				break
			}
			name := g
			if p := strings.Split(g, "|"); len(p) == 3 {
				name = p[1]
			}
			list = append(list, apiLink{Text: name, Href: cveLink("group", g)})
		}
		units := strings.Split(r.S("units"), ",")
		for i, u := range units {
			units[i] = cveUnitText(cveAff{unit: u})
		}
		svc := r.Str("service")
		svcs = append(svcs, map[string]any{"priority": apiPillOf(cvePriBadge(r.S("pri"))), "service": svc, "href": If(svc != "all services", cveLink("svc", svc)+"#components", ""),
			"units": py.Head(strings.Join(units, ", "), 120), "vulns": r.I("n"), "kev": r.I("kev"), "exposure": map[int64]string{2: "customer-facing", 1: "internet", 0: "internal"}[r.I("exp")], "components": list})
	}
	cons := []map[string]any{}
	for _, r := range cveVMContainerRows(vid) {
		cons = append(cons, map[string]any{"name": r.Str("name"), "image": r.Str("image"), "declared": r.S("source") == "declared", "source": cveSourceText(r.S("source")),
			"inside": Text(cveAnalysisText(r.S("analysis"))), "vulns": r.I("n")})
	}
	return map[string]any{"note": Text(note), "components": comps, "services": svcs, "containers": cons}
}

// apiInvActions is the Refresh and Analyse control of a VM: what may start now and why not. Polling pages set c.Refresh.
func apiInvActions(c *Ctx, r Row, doc *py.OMap, sess *Session) map[string]any {
	vid := r.I("id")
	ok, msg, busy := inventoryReqState("refresh", vid, inventoryRefreshGap, 300)
	if busy {
		c.Refresh = true
	}
	out := map[string]any{"refresh": map[string]any{"can": ok, "busy": busy, "note": msg}, "collected": doc.Get("collected_at"), "analyse": nil}
	if sess.Role != "admin" {
		return out
	}
	aok, amsg, abusy := inventoryReqState("analyse", vid, inventoryAnalyseGap, 20*60)
	if abusy {
		c.Refresh = true
	}
	title := "What is under pressure, who uses it, needed or a leak, and why services are not ready. Read-only, about 10 minutes, up to $1.50."
	switch {
	case strings.Contains(r.S("flags"), "ssh-skipped-db"):
		aok, amsg = false, "not for DB hosts"
	case !((r.T("ssh_alias") && r.T("ssh_ok")) || r.T("monitored")):
		aok, amsg = false, "no read-only access"
	case aok && !(r.T("ssh_alias") && r.T("ssh_ok")):
		title += " Metrics only: this VM has no ssh-ro access."
	}
	out["analyse"] = map[string]any{"can": aok, "busy": abusy, "note": amsg, "title": title}
	return out
}

// apiInventoryItemJSON is GET /api/v1/inventory/item?id=: everything on a VM's page.
func apiInventoryItemJSON(qs py.QS, sess *Session) (any, *apiErr) {
	vs := qs.Get("id")
	if !py.IsDigit(vs) || len(vs) > 9 {
		return nil, &apiErr{404, "no such VM"}
	}
	n, _ := py.ParseInt(vs)
	rr := Q("SELECT * FROM vms WHERE id=?", n)
	if len(rr) == 0 {
		return nil, &apiErr{404, "no such VM"}
	}
	r := rr[0]
	vid := r.I("id")
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/inventory/view"}
	doc, states, pv := inventoryLoad(r)
	facts := py.NewOMap()
	if v, err := py.ParseJSON([]byte(py.OrS(r.Get("facts"), "{}"))); err == nil {
		if m := py.AsMap(v); m != nil {
			facts = m
		}
	}
	banners := []apiBanner{}
	if m := map[string]string{"refresh": "Refresh requested. The VM is collected again in about a minute; this page reloads until it is done.",
		"analyse": "Analysis requested. It runs read-only for up to 15 minutes; the report appears under Analysis."}[qs.Get("sent")]; m != "" {
		banners = append(banners, apiBanner{Text: m, Kind: "info"})
	}
	if fu := inventoryStrs(pv.Get("failed_units")); len(fu) > 0 {
		banners = append(banners, apiBanner{Text: "Failed now (Prometheus, last 5 minutes): " + strings.Join(fu[:min(6, len(fu))], ", "), Kind: "fail"})
	}
	if py.Truthy(doc.Get("ssh_error")) && !py.Truthy(doc.Get("ssh_at")) && r.T("ssh_alias") {
		banners = append(banners, apiBanner{Text: "No read-only snapshot: " + py.Str(doc.Get("ssh_error")), Kind: "warn"})
	}
	ws := map[string]string{"ok": "resources within limits", "pressure": "a resource under pressure",
		"critical": "a resource at its critical level"}[py.Str(inventoryWorst(states))]
	if ws == "" {
		ws = "resources not measured"
	}
	ipl, ident, access := inventoryRecord(r, doc)
	kv := func(items []inventoryKV) []apiFact {
		out := []apiFact{}
		for _, it := range items {
			v := "-"
			if it.v != nil && it.v != "" {
				v = py.Str(it.v)
			}
			out = append(out, apiFact{Label: it.k, Value: v})
		}
		return out
	}
	incs := []map[string]any{}
	for _, i := range inventoryOpenIncidents(r, ipl) {
		incs = append(incs, map[string]any{"id": i.I("id"), "title": i.Str("title"), "status": i.Str("status"), "kind": getD(INC_KIND, i.Str("status"), "warn")})
	}
	changes := []map[string]any{}
	for _, ch := range Q("SELECT * FROM vm_changes WHERE vm_id=? ORDER BY at DESC LIMIT 60", vid) {
		changes = append(changes, map[string]any{"at": ch.I("at"), "field": ch.Str("field"), "old": ch.S("old"), "new": ch.S("new")})
	}
	raw := []apiOption{}
	for _, x := range inventoryRawFacts(facts) {
		raw = append(raw, apiOption{x[1], x[0]})
	}
	notes := []string{}
	for _, k := range []string{"ports_note", "journal_note"} {
		if py.Truthy(doc.Get(k)) {
			notes = append(notes, py.Str(doc.Get(k)))
		}
	}
	resources, ready := apiInvResources(doc, states)
	ups, nDead := apiInvUpstreams(doc)
	sm := py.AsMap(py.Or(doc.Get("summary"), py.NewOMap()))
	var prob map[string]any
	if ProblemsOn() {
		if rp := probQ1("SELECT id, name FROM vm_reports WHERE vm_id=? AND run_id IS NOT NULL ORDER BY id DESC LIMIT 1", vid); rp != nil {
			prob = apiProblemPanel(c, qs, "vm_report", I2S(rp.I("id")), "VM analysis "+rp.S("name"), "VM analysis", 0)
		}
	}
	serves := Text(inventoryServes(r, doc))
	roles := Text(inventoryDataRoles(r))
	actions := apiInvActions(c, r, doc, sess)
	jump := []apiOption{}
	if len(incs) > 0 {
		jump = append(jump, apiOption{"incidents", "Open incidents " + Itoa(len(incs))})
	}
	jump = append(jump, apiOption{"services", "Services " + py.Str(sm.GetD("services", int64(0)))},
		apiOption{"upstreams", "Upstreams " + If(nDead > 0, Itoa(nDead)+" dead", Itoa(ups["count"].(int)))}, apiOption{"analysis", "Analysis"}, apiOption{"vulns", "Vulnerabilities"},
		apiOption{"resources", "Resources"}, apiOption{"filesystems", "Filesystems"}, apiOption{"history", "Change history " + Itoa(len(changes))}, apiOption{"record", "Identity and access"})
	return map[string]any{"connect": ACCESS.SSH(r.S("name")), "connectVia": ACCESS.Label(), "id": vid, "name": py.OrS(r.Get("name"), "VM"), "env": r.S("env"), "dc": r.Str("dc"), "summary": inventoryShortSummary(sm), "state": ws,
		"actions": actions, "banners": banners, "resources": resources, "ready": ready, "jump": jump, "incidents": incs,
		"serviceCount": py.Str(sm.GetD("services", int64(0))), "services": apiInvServices(doc), "upstreams": ups, "serves": serves, "dataRoles": roles,
		"analysis": apiInvAnalysis(c, vid), "problem": prob, "vulns": apiInvVulns(vid), "consumers": apiInvConsumers(doc, r.Get("ram_bytes")),
		"pressure": apiInvPressure(doc, states), "mounts": apiInvMounts(doc, vid), "changes": changes, "raw": raw, "identity": kv(ident), "access": kv(access), "notes": notes,
		"refresh": apiRefresh(c)}, nil
}

// --- Capacity ----------------------------------------------------------------------------------------------------

func apiCapAnalysis(c *Ctx, f *py.OMap, sess *Session) map[string]any {
	fid := CapacityFSID(f)
	out := map[string]any{"fs": fid, "note": "", "report": nil, "can": false, "show": sess.Role == "admin", "msg": ""}
	r := Q("SELECT d.*, p.title FROM disk_reports d LEFT JOIN triage_problems p ON p.run_id=d.run_id AND p.idx=0 WHERE d.fs_id=? ORDER BY d.created DESC LIMIT 1", fid)
	if len(r) > 0 && r[0].Get("status") == "NO_ALIAS" {
		out["note"] = "no read-only access"
	} else if len(r) > 0 {
		x := r[0]
		who := "auto"
		if x.Get("requested_by") != "auto" {
			who = py.OrS(x.Get("requested_by"), "manual")
		}
		rep := map[string]any{"href": "/incidents/problem?run=" + I2S(x.I("run_id")) + "&idx=0", "title": x.S("title"), "at": x.I("created"), "who": who, "board": nil}
		if x.T("run_id") {
			rep["board"] = apiLink{Text: Text(BoardCreateButton("disk", x.I("id"), "Task", "")), Href: firstHref(BoardCreateButton("disk", x.I("id"), "Task", ""))}
		}
		out["report"] = rep
	}
	if sess.Role == "admin" {
		out["can"], out["msg"] = capacityDiskState(c, fid)
	}
	return out
}

func apiCapFS(c *Ctx, f *py.OMap, sess *Session) map[string]any {
	lv := f.Get("level")
	l, ok := capacityLevels[py.Str(lv)]
	if !ok || lv == nil {
		l = capacityLevel{"warn", py.OrS(lv, ""), ""}
	}
	var used any
	if f.Get("free_pct") != nil {
		used = 100 - py.Num(f.Get("free_pct"))
	}
	days := "-"
	if f.Get("days") != nil {
		days = py.G(py.Num(f.Get("days")), 6) + " d"
	}
	href := ""
	if py.Truthy(f.Get("vm_id")) {
		href = "/inventory/view?id=" + I2S(py.I(f.Get("vm_id")))
	}
	return map[string]any{"level": apiPill{l.word, l.kind, l.tip}, "new": py.Truthy(f.Get("new")), "name": py.Str(py.Or(f.Get("vm_name"), f.Get("host"))), "host": py.Str(f.Get("host")),
		"href": href, "env": py.OrS(f.Get("env"), ""), "dc": py.OrS(f.Get("dc"), ""), "mount": py.Str(f.Get("mount")), "used": apiCapMeter(used, l.kind, CapacityGB(f.Get("size"))+" in total"),
		"free": CapacityGB(f.Get("avail")), "growth": Text(CapacityGrowth(f)), "days": days, "analysis": apiCapAnalysis(c, f, sess)}
}

func apiCapMeter(used any, kind, title string) map[string]any {
	if used == nil {
		return nil
	}
	return map[string]any{"pct": apiBar(py.Num(used)), "figure": py.F(py.Num(used), 0) + " %", "kind": kind, "title": title}
}

func apiCapPct(v any, hot [2]float64) map[string]any {
	if v == nil {
		return map[string]any{"text": "-", "hot": ""}
	}
	return map[string]any{"text": py.F(py.Num(v), 0) + " %", "hot": strings.TrimSuffix(capacityHotCls(v, hot), "num")}
}

func apiCapVM(v capacityVM, hot map[string][2]float64) map[string]any {
	m := v.m
	bits := []string{}
	if py.Truthy(m.Get("dc")) {
		bits = append(bits, py.Str(m.Get("dc")))
	}
	if py.Truthy(m.Get("role")) {
		bits = append(bits, py.Str(m.Get("role")))
	}
	if py.Truthy(m.Get("monitored")) && !py.Truthy(m.Get("inventory_id")) {
		bits = append(bits, "not in the Inventory")
	}
	href := ""
	if py.Truthy(m.Get("inventory_id")) {
		href = "/inventory/view?id=" + I2S(py.I(m.Get("inventory_id")))
	}
	out := map[string]any{"name": py.Str(py.Or(py.Or(m.Get("name"), m.Get("key")), "?")), "href": href, "env": If(py.OrS(m.Get("env"), "") != "unknown", py.OrS(m.Get("env"), ""), ""),
		"bits": strings.Join(bits, " · "), "hit": Text(v.why), "level": apiPillOf(capacityVMBadge(m)), "monitored": py.Truthy(m.Get("monitored")), "why": "", "disk": nil, "cpu": nil, "memory": nil}
	if !py.Truthy(m.Get("monitored")) {
		out["why"] = strings.Join(inventoryStrs(py.Or(m.Get("why"), []any{})), "; ")
		return out
	}
	disks := py.AsList(py.Or(m.Get("disks"), []any{nil}))
	var d *py.OMap
	if len(disks) > 0 {
		d = py.AsMap(disks[0])
	}
	if d != nil && d.Len() > 0 {
		days := d.Get("days")
		var full *py.OMap
		fsl := []map[string]any{}
		for _, f := range capacityMaps(py.Or(m.Get("filesystems"), []any{})) {
			if full == nil && vmsearchEq(f.Get("mount"), d.Get("mount")) {
				full = f
			}
			fsl = append(fsl, map[string]any{"mount": py.Str(f.Get("mount")), "used": Text(capacityPct(f.Get("used_pct"), "")), "growth": Text(CapacityGrowth(f)), "days": CapacityFmtDays(f.Get("days"))})
		}
		sub := "not filling"
		if full != nil && full.Len() > 0 {
			g := Text(CapacityGrowth(full))
			if x := full.Get("growth_per_day"); x != nil && py.Num(x) >= 1 {
				g += " / day"
			}
			if days != nil {
				sub = g + " · full in " + CapacityFmtDays(days)
			} else {
				sub = g + " · not filling"
			}
		} else if days != nil && py.Num(days) < 365 {
			sub = "full in " + py.G(py.Num(days), 6) + " d"
		}
		if m.Get("io_p95") != nil {
			sub += " · IO p95 " + Text(capacityPct(m.Get("io_p95"), ""))
		}
		if len(fsl) < 2 {
			fsl = fsl[:0]
		}
		out["disk"] = map[string]any{"mount": py.Str(d.Get("mount")), "sub": sub, "used": apiCapMeter(d.Get("used_pct"), capacityLevelKind(d.Get("level"), "warn"), CapacityGB(d.Get("size"))+" in total"), "filesystems": fsl}
	}
	cs := []string{}
	if m.Get("cpu_avg") != nil {
		cs = append(cs, "avg "+py.F(py.Num(m.Get("cpu_avg")), 0)+" %")
	}
	if m.Get("load_p95") != nil {
		cs = append(cs, "load "+py.F(py.Num(m.Get("load_p95")), 2)+"/vCPU")
	}
	cpu := apiCapPct(m.Get("cpu_p95"), hot["cpu_p95"])
	cpu["sub"] = strings.Join(cs, " · ")
	mem := apiCapPct(m.Get("mem_p95"), hot["mem_p95"])
	mem["sub"] = ""
	if m.Get("mem_avg") != nil {
		mem["sub"] = "avg " + py.F(py.Num(m.Get("mem_avg")), 0) + " %"
	}
	out["cpu"], out["memory"] = cpu, mem
	return out
}

// apiCapacityJSON is GET /api/v1/capacity with the query parameters of /capacity.
func apiCapacityJSON(qs py.QS, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/capacity"}
	doc := capacityDoc()
	fs := capacityMaps(py.Or(doc.Get("filesystems"), []any{}))
	vms := capacityMaps(py.Or(doc.Get("vms"), []any{}))
	summ := py.AsMap(py.Or(doc.Get("summary"), py.NewOMap()))
	hot := capacityHot(summ)
	by := py.AsMap(py.Or(summ.Get("by_level"), py.NewOMap()))
	tiles := []apiTile{}
	for _, lv := range capacityVMLevels {
		n := py.I(by.GetD(lv[0], int64(0)))
		tiles = append(tiles, apiTile{Label: lv[1], Figure: I2S(n), Sub: lv[2], Href: "/capacity?vlevel=" + lv[0] + "#vms", Hot: (lv[0] == "critical" || lv[0] == "warn") && n != 0})
	}
	l := capacityQuery(qs)
	lvl := l.get("level")
	shown, nRisk := []map[string]any{}, 0
	for _, f := range fs {
		if f.Get("level") != "ok" {
			nRisk++
		}
		if lvl == "all" || f.Get("level") != "ok" {
			shown = append(shown, apiCapFS(c, f, sess))
		}
	}
	fsHref := func(v string) string {
		g := l.with("level", v)
		g.anchor = "fs"
		return g.href()
	}
	hotRows := []map[string]any{}
	for _, v := range vms {
		if !py.Truthy(v.Get("monitored")) {
			continue
		}
		for _, k := range []string{"cpu_p95", "mem_p95"} {
			if py.Num(py.Or(v.Get(k), int64(0))) >= hot[k][0] {
				hotRows = append(hotRows, apiCapVM(capacityVM{m: v}, hot))
				break
			}
		}
	}
	cv := capacityVMsOf(l, vms)
	var lvls [][2]string
	for _, x := range capacityVMLevels {
		lvls = append(lvls, [2]string{x[0], x[1]})
	}
	sorts := [][3]string{{"worst", "Worst first", ""}, {"growth", "Fastest growing", ""}}
	if l.get("q") != "" {
		sorts = sorts[:1]
	}
	from, to := listPage(&l, len(cv.list))
	rows := []map[string]any{}
	for _, v := range cv.list[from:to] {
		rows = append(rows, apiCapVM(v, hot))
	}
	clear := l.cleared()
	clear.v["level"] = l.get("level")
	list := apiListOf(l, len(cv.list), len(vms), "VMs", sorts)
	list["path"], list["sortKey"], list["sort"], list["clearHref"] = "/capacity#vms", "vsort", l.get("vsort"), clear.href()
	list["filtered"] = l.get("q") != "" || l.get("env") != "" || l.get("dc") != "" || l.get("vlevel") != ""
	if lvl != "soon" {
		list["hidden"] = map[string]string{"level": lvl}
	}
	sub := []string{}
	if doc.Len() > 0 {
		if summ.Len() > 0 {
			sub = append(sub, py.Str(summ.Get("vms"))+" VMs, "+py.Str(summ.Get("filesystems"))+" filesystems")
		}
		if py.Truthy(summ.Get("inventory")) {
			sub = append(sub, py.Str(summ.GetD("monitored", int64(0)))+" of "+py.Str(summ.GetD("inventory", int64(0)))+" Inventory VMs monitored")
		}
		sub = append(sub, "forecast "+Ago(doc.Get("generated_at")), "window "+py.Str(doc.GetD("window", "7d")))
	}
	banners := apiBannersOf(capacityStaleBanner(doc, "capacity", 30))
	if qs.Get("sent") == "analyse" {
		banners = append([]apiBanner{{Text: "Storage analysis requested. It runs read-only for up to 15 minutes; the result appears in the Analysis column.", Kind: "info"}}, banners...)
	}
	hotNote := "No VM is at " + py.F(hot["cpu_p95"][0], 0) + " % CPU or memory or above at p95 over 7 days."
	return map[string]any{"sub": sub, "banners": banners, "tiles": tiles, "hasVMs": len(vms) > 0,
		"show":        map[string]any{"level": lvl, "risk": nRisk, "all": len(fs), "riskHref": fsHref("soon"), "allHref": fsHref("all")},
		"filesystems": shown, "hot": hotRows, "hotNote": hotNote, "vmTotal": len(vms), "rows": rows, "list": list,
		"facets": []apiFacet{apiFacetOf("env", "Environment", l.get("env"), cv.vals("env"), cv.counts["env"]), apiFacetOf("dc", "DC", l.get("dc"), cv.vals("dc"), cv.counts["dc"]),
			apiFacetOf("vlevel", "Level", l.get("vlevel"), lvls, cv.counts["vlevel"])},
		"scan": apiScanBar("/capacity"), "refresh": apiRefreshOr(c, "/capacity")}, nil
}

// apiRefreshOr is the page's refresh, or one while its scan runs.
func apiRefreshOr(c *Ctx, active string) map[string]any {
	if r := apiRefresh(c); r != nil {
		return r
	}
	if apiScanBusy(active) {
		return map[string]any{"seconds": 5}
	}
	return nil
}

// --- Alert hygiene -----------------------------------------------------------------------------------------------

func apiHygieneJSON(_ py.QS, _ *Session) (any, *apiErr) {
	doc := py.AsMap(ReadJSON(filepath.Join(CapacityData, "hygiene.json"), py.NewOMap()))
	if doc == nil {
		doc = py.NewOMap()
	}
	findings := capacityMaps(py.Or(doc.Get("findings"), []any{}))
	counts := py.AsMap(py.Or(doc.Get("counts"), py.NewOMap()))
	tiles := []apiTile{}
	for _, k := range capacityKinds[:4] {
		tiles = append(tiles, apiTile{Label: k[1], Figure: py.Str(counts.GetD(k[0], int64(0))), Hot: py.Truthy(counts.Get(k[0])) && (k[0] == "test" || k[0] == "stale")})
	}
	sections := []map[string]any{}
	for _, kk := range capacityKinds {
		var items []*py.OMap
		sugg := map[string]bool{}
		one := ""
		for _, f := range findings {
			if f.Get("kind") == kk[0] {
				items = append(items, f)
				one = py.OrS(f.Get("suggestion"), "")
				sugg[one] = true
			}
		}
		if len(items) == 0 {
			continue
		}
		same := len(sugg) == 1
		rate := kk[0] == "flapping" || kk[0] == "noisy"
		rows := []map[string]any{}
		for _, f := range items {
			row := map[string]any{"alert": py.Str(f.Get("alertname")), "severity": py.OrS(f.Get("severity"), ""), "dc": py.Str(f.Get("dc")), "instance": py.Str(py.Or(py.Or(f.Get("instance"), f.Get("job")), "")),
				"why": If(!same, py.OrS(f.Get("suggestion"), ""), ""), "neverResolved": !rate && py.Truthy(f.Get("never_resolved")), "detail": ""}
			if rate {
				row["when"] = py.Str(f.Get("per_day")) + " / day"
				row["detail"] = py.Str(f.Get("fired")) + " fired, " + py.Str(f.Get("resolved")) + " resolved in " + py.Str(f.Get("window_days")) + " d"
			} else {
				row["when"] = py.Str(f.Get("age_days")) + " d"
			}
			rows = append(rows, row)
		}
		sections = append(sections, map[string]any{"title": kk[1], "note": Text(kk[2]), "suggestion": If(same, one, ""), "whenLabel": If(rate, "Rate", "Firing for"), "rows": rows})
	}
	sd := []string{} // the days the counters cover, each once
	for _, x := range inventoryStrs(py.Or(doc.Get("stats_days"), []any{})) {
		if !slices.Contains(sd, x) {
			sd = append(sd, x)
		}
	}
	return map[string]any{"loaded": doc.Len() > 0, "at": doc.Get("generated_at"), "counters": If(len(sd) > 0, strings.Join(sd, ", "), "none"), "banners": apiBannersOf(capacityStaleBanner(doc, "hygiene", 30)),
		"tiles": tiles, "sections": sections, "scan": apiScanBar("/hygiene"), "refresh": apiScanBusy("/hygiene")}, nil
}

// --- Certificates ------------------------------------------------------------------------------------------------

func apiCertRow(g *certsGroup) map[string]any {
	var d any = g.r.Get("days_left")
	if d == nil {
		d = int64(0)
	}
	mism := map[string]bool{}
	for _, m := range g.mism {
		mism[m] = true
	}
	names := []map[string]any{}
	for _, n := range g.names {
		names = append(names, map[string]any{"name": n, "mismatch": mism[n]})
	}
	return map[string]any{"status": apiBadge(g.r.Get("status")), "label": g.r.Str("label"), "issuer": g.r.S("issuer"), "pct": apiBar(py.Num(d) / 90 * 100), "level": certsLevel(py.Num(d)),
		"days": py.Str(d), "expires": py.Head(g.r.S("not_after"), 10), "names": names, "mismatches": len(mism)}
}

func apiCertRows(l []*certsGroup) []map[string]any {
	out := []map[string]any{}
	for _, g := range l {
		out = append(out, apiCertRow(g))
	}
	return out
}

// apiCertsJSON is GET /api/v1/certs: grouped by certificate; "hosts" is the per-name list of a run without groups.
func apiCertsJSON(_ py.QS, _ *Session) (any, *apiErr) {
	r := LatestRun("cert-check")
	out := map[string]any{"ran": r != nil, "scan": apiScanBar("/certs"), "refresh": apiScanBusy("/certs"), "hosts": nil}
	if r == nil {
		return out, nil
	}
	out["at"], out["summary"] = r.I("started_at"), py.OrS(r.Get("summary"), "")
	g := certsLoad(r.Get("id"))
	if g == nil {
		hosts := []map[string]any{}
		for _, ce := range Q("SELECT * FROM cert_results WHERE run_id=? ORDER BY days_left", r.Get("id")) {
			dv := py.Num(py.Or(ce.Get("days_left"), int64(0)))
			lvl := "fail"
			if dv >= 30 {
				lvl = "ok"
			} else if dv >= 14 {
				lvl = "warn"
			}
			hosts = append(hosts, map[string]any{"status": apiBadge(ce.Get("status")), "host": ce.Str("host"), "pct": apiBar(dv / 90 * 100), "level": lvl, "days": py.Str(ce.Get("days_left")),
				"expires": ce.S("not_after"), "issuer": ce.S("issuer")})
		}
		out["hosts"], out["status"] = hosts, apiBadge(r.Get("status"))
		return out, nil
	}
	certs, live, stale, unreach, names, status, banners := certsHead(g)
	var hot, ok []*certsGroup
	for _, x := range live {
		if x.r.Get("status") != "OK" {
			hot = append(hot, x)
		} else {
			ok = append(ok, x)
		}
	}
	un := []apiOption{}
	for _, u := range unreach {
		un = append(un, apiOption{u.r.S("note"), u.r.Str("label")})
	}
	out["status"], out["names"], out["certificates"], out["banners"] = apiBadge(status), names, len(certs), apiBannersOf(banners)
	out["hot"], out["ok"], out["stale"], out["unreachable"], out["host"] = apiCertRows(hot), apiCertRows(ok), apiCertRows(stale), un, ARGUS_HOSTNAME
	return out, nil
}

// --- Checks ------------------------------------------------------------------------------------------------------

func apiChecksRows() ([]map[string]any, []string, []string) {
	out := []map[string]any{}
	var stale, bad []string
	for _, t := range TASKS {
		task, name, href := t[0], t[1], t[2]
		r := LatestRun(task)
		if r == nil || Now()-r.F("started_at") > float64(STALE_H*3600) {
			stale = append(stale, name)
		}
		var status any = "NO RUN"
		if r != nil {
			status = r.Get("status")
			if task == "backup-check" && status != "OK" && backupAllAccepted() {
				status = "OK"
			}
		}
		if r != nil && status != "OK" {
			bad = append(bad, name)
		}
		state := ScanState(task)
		busy := ScanBusy(state)
		row := map[string]any{"task": task, "name": name, "href": href, "status": apiBadge(status), "busy": busy, "last": "never run", "at": 0, "summary": "No run recorded yet.", "state": state,
			"title": "Run " + SCAN_LABEL[task] + " again now", "strip": runStripOf(task, Now())}
		if busy {
			row["last"] = "Scanning…"
		} else if r != nil {
			row["last"] = Ago(r.Get("started_at"))
		}
		if r != nil {
			row["at"], row["summary"] = r.I("started_at"), FirstLine(r.Get("summary"))
		}
		out = append(out, row)
	}
	return out, stale, bad
}

// apiChecksExceptions is the "Needs attention" list of the Checks page: failing backup sets and certificates, worst first.
func apiChecksExceptions(fails []Row) map[string]any {
	type item struct {
		rank int
		m    map[string]any
	}
	var items []item
	for _, x := range fails {
		name := x.Str("name")
		items = append(items, item{1, map[string]any{"pill": apiPill{Text: strings.ToLower(py.OrS(x.Get("status"), "failing")), Kind: "fail"}, "title": name,
			"href": "/backups/set?name=" + py.Quote(name), "kind": "Backup set", "meta": "newest backup " + Text(backupAgeCell(x))}})
	}
	if r := LatestRun("cert-check"); r != nil {
		for _, g := range Q("SELECT label, days_left, n_names, status FROM cert_groups WHERE run_id=? AND kind='cert' AND "+
			"(status='EXPIRED' OR days_left < 30) ORDER BY status='EXPIRED' DESC, days_left LIMIT 5", r.Get("id")) {
			lvl, word := certsLevel(g.F("days_left")), I2S(g.I("days_left"))+" days left"
			if g.Str("status") == "EXPIRED" {
				lvl, word = "fail", "expired"
			}
			items = append(items, item{If2(lvl == "fail", 1, 3), map[string]any{"pill": apiPill{Text: word, Kind: lvl}, "title": g.Str("label"), "href": "/certs",
				"kind": "Certificate", "meta": I2S(g.I("n_names")) + If(g.I("n_names") == 1, " name", " names")}})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	rows := []map[string]any{}
	for i, it := range items {
		if i >= 8 {
			break
		}
		rows = append(rows, it.m)
	}
	return map[string]any{"rows": rows, "total": len(items)}
}

func If2(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

func apiChecksCredentials(sess *Session) map[string]any {
	d := credDoc()
	if d == nil {
		return nil
	}
	admin := sess.Role == "admin"
	bad := []map[string]any{}
	n, nBad := 0, 0
	for _, it := range credItems(d) {
		if it.S("status") == "absent" {
			continue
		}
		n++
		if credAttention[it.S("status")] {
			nBad++
			if len(bad) < 3 {
				bad = append(bad, map[string]any{"name": it.S("name"), "href": If(admin, "/admin/credentials#cred-"+it.S("id"), ""), "detail": it.S("detail"), "expires": Text(credExpiresShort(it))})
			}
		}
	}
	line := ""
	if nBad == 0 {
		line = Itoa(n) + " credentials checked " + Ago(d.Get("checked_at"))
		if nx := d.Map("next"); nx != nil && nx.Get("days_left") != nil && py.Num(nx.Get("days_left")) > 0 {
			line += "; next expiry " + nx.S("name") + " in " + I2S(int64(py.Num(nx.Get("days_left")))) + " d"
		}
	}
	return map[string]any{"title": If(nBad > 0, "Credentials: "+Itoa(nBad)+" expiring soon", "Credentials: all OK"), "rows": bad, "more": max(0, nBad-3), "line": line, "href": If(admin, "/admin/credentials", "")}
}

func apiChecksSelfcheck() map[string]any {
	doc := py.AsMap(ReadJSON(SelfcheckPath, py.NewOMap()))
	if doc == nil {
		doc = py.NewOMap()
	}
	status, line := SelfcheckSummary(doc, 0)
	rank := map[string]int{"FAIL": 0, "WARN": 1}
	checks := capacityMaps(py.Or(doc.Get("checks"), []any{}))
	sort.SliceStable(checks, func(i, j int) bool {
		a, oka := rank[py.Str(checks[i].Get("status"))]
		b, okb := rank[py.Str(checks[j].Get("status"))]
		return If2(oka, a, 2) < If2(okb, b, 2)
	})
	bad, ok := []map[string]any{}, []map[string]any{}
	for _, m := range checks {
		row := map[string]any{"status": apiBadge(m.Get("status")), "name": py.Str(m.GetD("name", "")), "group": py.Str(m.GetD("group", "")), "detail": py.Str(m.GetD("detail", ""))}
		if m.Get("status") != "OK" {
			bad = append(bad, row)
		} else {
			ok = append(ok, row)
		}
	}
	return map[string]any{"status": apiBadge(status), "line": line, "bad": bad, "ok": ok, "at": Ago(doc.Get("ts")), "kuma": py.OrS(doc.Get("kuma"), "")}
}

// apiChecksJSON is GET /api/v1/checks: the page /operations.
func apiChecksJSON(_ py.QS, sess *Session) (any, *apiErr) {
	rows, stale, bad := apiChecksRows()
	fails := failingBackups()
	banners := apiBannersOf(staleBanner(stale) + ClaudeOffBanner())
	month := Q("SELECT COALESCE(SUM(cost_usd),0) c, COUNT(*) n FROM runs WHERE started_at > ?", Now()-30*86400)
	cost, runs := "$0", "0"
	if len(month) > 0 {
		cost, runs = "$"+py.F(month[0].F("c"), 2), py.Str(month[0].Get("n"))
	}
	var rd map[string]any
	if d := readinessData(); d != nil {
		list := []map[string]any{}
		for _, r := range d.rows {
			list = append(list, map[string]any{"status": apiBadge(r.st), "check": r.k, "detail": r.v})
		}
		rd = map[string]any{"title": d.title, "date": d.date, "when": d.when, "rows": list, "gaps": d.gaps}
	}
	busy := false
	for _, r := range rows {
		busy = busy || r["busy"] == true
	}
	return map[string]any{"ok": len(TASKS) - len(bad), "total": len(TASKS), "bad": bad, "failing": len(fails), "banners": banners, "exceptions": apiChecksExceptions(fails), "checks": rows,
		"cost": cost, "runs": runs, "credentials": apiChecksCredentials(sess), "readiness": rd, "selfcheck": apiChecksSelfcheck(), "scan": apiScanBar("/operations"), "refresh": busy}, nil
}

// --- writes ------------------------------------------------------------------------------------------------------

// infraPost is the section's POSTs: the page handlers, answered as JSON.
func infraPost(h *reqCtx, path string, body map[string]any, sess *Session) (any, *apiErr) {
	c := &Ctx{Sess: sess, Theme: "auto", Path: "/inventory"}
	form := apiForm(body)
	switch path {
	case "/api/v1/scan":
		n, err := ScanRequest(form, sess)
		if err != nil {
			logf("scan request failed: %v", err)
			return nil, &apiErr{500, "Not sent."}
		}
		logf("scan requested by %s: %s (%d)", sess.Username, py.Repr(py.Head(form.Get("tasks"), 60)), n)
		return map[string]any{"ok": true, "redirect": SafeNext(form.Get("back"))}, nil
	case "/api/v1/inventory/refresh", "/api/v1/inventory/analyse":
		return apiResult(InventoryPost(c, strings.TrimPrefix(path, "/api/v1"), form, sess))
	case "/api/v1/capacity/analyse":
		return apiResult(CapacityPostAnalyse(c, form, sess))
	case "/api/v1/backups/override":
		return apiResult(BackupOverridePost(c, form, sess, h.ip()))
	case "/api/v1/backups/restore", "/api/v1/backups/install":
		return apiResult(BackupPost(c, strings.TrimPrefix(path, "/api/v1"), form, sess))
	}
	return apiBackupNew(form, sess)
}
