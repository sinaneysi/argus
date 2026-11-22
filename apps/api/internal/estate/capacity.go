package estate

// capacity.go: the capacity of every VM (the earlier `capacity.py collect`). Disks: every filesystem's size, free
// space and its 7-day trend (deriv() of a 7d:1h subquery, done by Prometheus); days to full = free bytes / bytes lost
// per day. Compute: CPU busy and memory used (7-day average and p95), load per vCPU and the busiest disk's IO (p95),
// each from a 7d:1h subquery, so a query returns one value per series whatever the estate's size. Every VM is joined
// to the inventory (by node name, then address, then instance); an inventory VM without node_exporter data is listed
// as "not monitored". Writes capacity.json (the previous one kept as capacity-prev.json).

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	capHorizonDays = 60
	capFullPct     = 10
	capFilter      = `fstype!~"tmpfs|overlay|squashfs|nsfs|ramfs|fuse.*|iso9660|devtmpfs",` +
		`mountpoint!~"/run.*|/boot.*|/snap/.*|/var/lib/docker/.+|/var/lib/kubelet/.+|/var/lib/containers/.+"`
	capAvail  = "node_filesystem_avail_bytes{" + capFilter + "}"
	capSize   = "node_filesystem_size_bytes{" + capFilter + "}"
	capBy     = "by (instance, job)"
	capCPU    = `(1 - avg ` + capBy + ` (rate(node_cpu_seconds_total{mode="idle"}[5m])))`
	capMem    = "(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)"
	capLoad   = `(node_load1 / on (instance, job) count ` + capBy + ` (node_cpu_seconds_total{mode="idle"}))`
	capIO     = `max ` + capBy + ` (rate(node_disk_io_time_seconds_total{device!~"loop.*|ram.*|sr.*|fd.*|nbd.*"}[5m]))`
	capW      = "[7d:1h]"
	capMethod = "deriv(node_filesystem_avail_bytes[7d:1h]) per filesystem; CPU, memory, load, IO as avg / " +
		"p95 of 5-minute rates sampled hourly over 7 days ([7d:1h] subqueries); every site"
)

var (
	capQueries = [][2]string{
		{"uname", "last_over_time(node_uname_info[2d])"},
		{"size", capSize},
		{"avail", capAvail},
		{"deriv", "deriv(" + capAvail + "[7d:1h])"},
		{"cpu_avg", "100 * avg_over_time(" + capCPU + capW + ")"},
		{"cpu_p95", "100 * quantile_over_time(0.95, " + capCPU + capW + ")"},
		{"mem_avg", "100 * avg_over_time(" + capMem + capW + ")"},
		{"mem_p95", "100 * quantile_over_time(0.95, " + capMem + capW + ")"},
		{"load_avg", "avg_over_time(" + capLoad + capW + ")"},
		{"load_p95", "quantile_over_time(0.95, " + capLoad + capW + ")"},
		{"io_p95", "100 * quantile_over_time(0.95, (" + capIO + ")" + capW + ")"},
	}
	capVMMetrics = []string{"cpu_avg", "cpu_p95", "mem_avg", "mem_p95", "load_avg", "load_p95", "io_p95"}
	capRankKeys  = []string{"critical", "warn", "full", "watch", "unmonitored", "ok"}
	capRank      = map[string]int{"critical": 0, "warn": 1, "full": 2, "watch": 3, "unmonitored": 4, "ok": 5}
	capFSOrder   = map[string]int{"critical": 0, "warn": 1, "full": 2, "watch": 3, "ok": 4}
	capHot       = map[string][2]int64{"cpu_p95": {90, 97}, "mem_p95": {90, 97}}
	capPortRe    = regexp.MustCompile(`:\d*$`)
)

func capLevel(days, freePct any) string {
	d, dok := days.(float64)
	f, fok := freePct.(float64)
	switch {
	case dok && d < 7:
		return "critical"
	case dok && d < 14:
		return "warn"
	case fok && f < capFullPct:
		return "full"
	case dok && d < 30:
		return "watch"
	}
	return "ok"
}

func capHotLevel(name string, v any) string {
	f, ok := v.(float64)
	t, hot := capHot[name]
	if !ok || !hot {
		return "ok"
	}
	if f >= float64(t[1]) {
		return "critical"
	}
	if f >= float64(t[0]) {
		return "warn"
	}
	return "ok"
}

func capWorst(levels []string) string {
	best := ""
	for _, l := range levels {
		r, ok := capRank[l]
		if !ok {
			r = 9
		}
		if best == "" || r < rankOf(best) {
			best = l
		}
	}
	if best == "" {
		return "ok"
	}
	return best
}

func rankOf(l string) int {
	if r, ok := capRank[l]; ok {
		return r
	}
	return 9
}

func capHostOf(m map[string]string) string {
	h := m["hostname"]
	if h == "" {
		h = m["instance"]
	}
	if h == "" {
		h = "?"
	}
	return strings.ToLower(capPortRe.ReplaceAllString(h, ""))
}

func capNorm(name string) string {
	return strings.ReplaceAll(strings.ToLower(capPortRe.ReplaceAllString(name, "")), "_", "-")
}

// pyG is Python's format(v, "g").
func pyG(v any) string {
	f, _ := num(v)
	return strconv.FormatFloat(f, 'g', 6, 64)
}

type capVM struct {
	key, dc, instance, job, envLabel string
	fsKeys                           []string
	fs                               map[string]*py.OMap
	metrics                          map[string]float64
	inv                              vmRow
}

type capSample struct {
	m map[string]string
	v float64
	f bool // finite
}

// capQuery runs the queries of one site, one after the other (the 7-day ones are heavy); an empty answer is asked
// again once and then reported, never taken as data.
func (o *Options) capQuery(ctx context.Context, site *Site) (map[string][]capSample, []string) {
	res := map[string][]capSample{}
	var errs []string
	for _, q := range capQueries {
		var rows []capSample
		failed := ""
		for attempt := 1; attempt <= 2; attempt++ {
			ss, err := o.Client.InstantErr(ctx, site, q[1])
			if err != nil {
				if attempt == 2 {
					failed = cut(site.Name+" "+q[0]+": "+err.Error(), 240)
				}
				rows = nil
			} else {
				rows = rows[:0]
				for _, s := range ss {
					v, ok := num(s.Value)
					if s.Value == nil {
						continue
					}
					if _, isStr := s.Value.(string); isStr {
						if _, err := strconv.ParseFloat(strings.TrimSpace(s.Value.(string)), 64); err != nil {
							continue
						}
					}
					rows = append(rows, capSample{m: s.Metric, v: v, f: ok})
				}
			}
			if len(rows) > 0 {
				break
			}
		}
		if failed != "" {
			errs = append(errs, failed)
		} else if len(rows) == 0 && len(res["uname"]) > 0 {
			errs = append(errs, site.Name+" "+q[0]+": no series (query failed or timed out)")
		}
		res[q[0]] = rows
	}
	return res, errs
}

func loadCapInventory(db string) []vmRow {
	if _, err := os.Stat(db); err != nil {
		return nil
	}
	d, err := openReadOnly(db)
	if err != nil {
		return nil
	}
	defer d.Close()
	rows, err := queryRows(d, "SELECT id, key, name, ips, env, dc, prom_instance, prom_job, flags, domains, monitored, up FROM vms WHERE gone=0")
	if err != nil {
		return nil
	}
	for _, r := range rows {
		var roles []any
		if v, err := py.ParseJSON([]byte(or(r.s("domains"), "{}").(string))); err == nil {
			roles = py.AsList(py.AsMap(v).Get("data_roles"))
		}
		var rs []string
		for i, x := range roles {
			if i >= 2 {
				break
			}
			rs = append(rs, str(x))
		}
		r["role"] = strings.Join(rs, ", ")
		if r["role"] == "" {
			r["role"] = r.s("prom_job")
		}
	}
	return rows
}

func unmonitoredReason(flags string) string {
	set := map[string]bool{}
	for _, f := range strings.Split(flags, ",") {
		set[f] = true
	}
	switch {
	case set["not-monitored"]:
		return "no node_exporter target (Inventory has it from ssh-ro only)"
	case set["down"]:
		return "node_exporter target down"
	}
	return "no node_exporter data on any site"
}

func roundAny(v any, n int) any {
	if f, ok := v.(float64); ok {
		return round(f, n)
	}
	return nil
}

// pyRound0 is Python's round(x) (to an int, halves to even).
func pyRound0(f float64) int64 { return int64(math.RoundToEven(f)) }

func (o *Options) capBuild(results map[string]map[string][]capSample, inventory []vmRow) ([]*py.OMap, []*py.OMap, *py.OMap) {
	vms := map[string]*capVM{}
	var vmOrder []string
	type fsKey struct{ vm, mount string }
	fs := map[fsKey]*py.OMap{}
	var fsOrder []fsKey
	for _, site := range o.Sites {
		dc := site.Name
		r := results[dc]
		names := map[[2]string]string{}
		for _, s := range r["uname"] {
			if n := capNorm(s.m["nodename"]); n != "" {
				names[target(s.m)] = n
			}
		}
		vmFor := func(m map[string]string) *capVM {
			k := names[target(m)]
			if k == "" {
				h := m["hostname"]
				if h == "" {
					h = m["instance"]
				}
				k = capNorm(h)
			}
			if k == "" {
				return nil
			}
			env := m["env"]
			if env == "" {
				env = m["environment"]
			}
			v := vms[k]
			if v == nil {
				v = &capVM{key: k, dc: dc, instance: m["instance"], job: m["job"], envLabel: env, fs: map[string]*py.OMap{}, metrics: map[string]float64{}}
				vms[k] = v
				vmOrder = append(vmOrder, k)
			} else if v.dc != dc {
				return nil
			}
			if v.envLabel == "" {
				v.envLabel = env
			}
			return v
		}
		type row struct {
			metric map[string]string
			vm     *capVM
			vals   map[string]float64
		}
		rows := map[fsKey]*row{}
		var rowOrder []fsKey
		for _, name := range []string{"avail", "size", "deriv"} {
			for _, s := range r[name] {
				if !s.f {
					continue
				}
				v := vmFor(s.m)
				if v == nil {
					continue
				}
				mp := s.m["mountpoint"]
				if mp == "" {
					mp = "?"
				}
				k := fsKey{v.key, mp}
				rw := rows[k]
				if rw == nil {
					rw = &row{metric: map[string]string{}, vm: v, vals: map[string]float64{}}
					rows[k] = rw
					rowOrder = append(rowOrder, k)
				}
				rw.vals[name] = s.v
				for a, b := range s.m {
					rw.metric[a] = b
				}
			}
		}
		for _, k := range rowOrder {
			if _, ok := fs[k]; ok {
				continue
			}
			rw := rows[k]
			var avail, size, freePct, days, loss, growth any
			if a, ok := rw.vals["avail"]; ok {
				avail = a
			}
			if s, ok := rw.vals["size"]; ok {
				size = s
			}
			if avail != nil && size != nil && size.(float64) != 0 {
				freePct = avail.(float64) / size.(float64) * 100
			}
			if d, ok := rw.vals["deriv"]; ok {
				l := -d * 86400
				loss, growth = l, l
				if avail != nil && l > 0 {
					days = math.Max(0, avail.(float64)/l)
				}
				if l <= 0 {
					loss = nil
				}
			}
			env := envLabel[rw.vm.envLabel]
			if env == "" {
				env = rw.vm.envLabel
			}
			var lossOut, growthOut any
			if l, ok := loss.(float64); ok && l != 0 {
				lossOut = pyRound0(l)
			}
			if g, ok := growth.(float64); ok {
				growthOut = pyRound0(g)
			}
			var usedPct any
			if f, ok := freePct.(float64); ok {
				usedPct = round(100-f, 1)
			}
			f := py.NewOMap("dc", dc, "host", capHostOf(rw.metric), "mount", k.mount, "device", rw.metric["device"], "env", env,
				"job", rw.metric["job"], "avail", avail, "size", size, "free_pct", roundAny(freePct, 1), "used_pct", usedPct,
				"days", roundAny(days, 1), "loss_per_day", lossOut, "growth_per_day", growthOut, "level", capLevel(days, freePct), "vm", k.vm)
			fs[k] = f
			fsOrder = append(fsOrder, k)
			if _, ok := rw.vm.fs[k.mount]; !ok {
				rw.vm.fsKeys = append(rw.vm.fsKeys, k.mount)
			}
			rw.vm.fs[k.mount] = f
		}
		for _, name := range capVMMetrics {
			for _, s := range r[name] {
				if !s.f {
					continue
				}
				if v := vmFor(s.m); v != nil {
					if _, ok := v.metrics[name]; !ok {
						n := 1
						if strings.HasPrefix(name, "load") {
							n = 2
						}
						v.metrics[name] = round(s.v, n)
					}
				}
			}
		}
	}
	// join to the inventory; two Prometheus names of one inventory VM are one VM (the first site's wins)
	type ik struct{ kind, v string }
	idx := map[ik]vmRow{}
	for _, v := range inventory {
		keys := []ik{{"key", capNorm(v.s("key"))}, {"key", capNorm(v.s("name"))}, {"inst", capNorm(v.s("prom_instance"))}}
		for _, ip := range strings.Split(v.s("ips"), ",") {
			if ip = strings.TrimSpace(ip); ip != "" {
				keys = append(keys, ik{"ip", ip})
			}
		}
		for _, k := range keys {
			if _, ok := idx[k]; k.v != "" && !ok {
				idx[k] = v
			}
		}
	}
	match := func(key, instance string) vmRow {
		for _, k := range []ik{{"key", key}, {"ip", ipOfInstance(instance)}, {"ip", key}, {"inst", capNorm(instance)}} {
			if r, ok := idx[k]; ok {
				return r
			}
		}
		return nil
	}
	removed := map[fsKey]bool{}
	claimed := map[int64]*capVM{}
	var out []*capVM
	for _, k := range vmOrder {
		v := vms[k]
		inv := match(v.key, v.instance)
		if inv != nil {
			id, _ := inv["id"].(int64)
			if first, ok := claimed[id]; ok {
				for _, mount := range v.fsKeys {
					f := v.fs[mount]
					if _, has := first.fs[mount]; has {
						removed[fsKey{v.key, mount}] = true
					} else {
						first.fs[mount] = f
						first.fsKeys = append(first.fsKeys, mount)
						f.Set("vm", first.key)
					}
				}
				for _, name := range capVMMetrics {
					if x, ok := v.metrics[name]; ok {
						if _, has := first.metrics[name]; !has {
							first.metrics[name] = x
						}
					}
				}
				continue
			}
			claimed[id] = v
		}
		v.inv = inv
		out = append(out, v)
	}
	daysOr := func(f *py.OMap, def float64) float64 {
		if d, ok := f.Get("days").(float64); ok {
			return d
		}
		return def
	}
	fnOr0 := func(x any) float64 { f, _ := num(x); return f }
	metric := func(v *capVM, k string) any {
		if x, ok := v.metrics[k]; ok {
			return x
		}
		return nil
	}
	var rows []*py.OMap
	for _, v := range out {
		inv := v.inv
		if inv == nil {
			inv = vmRow{}
		}
		disks := make([]*py.OMap, 0, len(v.fsKeys))
		for _, m := range v.fsKeys {
			disks = append(disks, v.fs[m])
		}
		sort.SliceStable(disks, func(i, j int) bool {
			a, b := disks[i], disks[j]
			if ra, rb := capRank[a.S("level")], capRank[b.S("level")]; ra != rb {
				return ra < rb
			}
			if da, db := daysOr(a, 1e9), daysOr(b, 1e9); da != db {
				return da < db
			}
			return -fnOr0(a.Get("used_pct")) < -fnOr0(b.Get("used_pct"))
		})
		env := inv.s("env")
		if env == "" {
			env = envLabel[v.envLabel]
			if env == "" {
				env = v.envLabel
			}
		}
		if env == "" {
			env = "unknown"
		}
		name := inv.s("name")
		if name == "" {
			name = v.key
		}
		for _, f := range disks {
			f.Set("vm_id", inv["id"]).Set("vm_name", name)
			if e := inv.s("env"); e != "" && e != "unknown" {
				f.Set("env", e)
			}
		}
		var lv, why []string
		var whyAny []any
		for _, f := range disks {
			lv = append(lv, f.S("level"))
		}
		lv = append(lv, capHotLevel("cpu_p95", metric(v, "cpu_p95")), capHotLevel("mem_p95", metric(v, "mem_p95")))
		for _, f := range disks {
			if f.S("level") == "ok" {
				continue
			}
			if d, ok := f.Get("days").(float64); ok && d < 30 {
				why = append(why, f.S("mount")+" full in "+pyG(d)+" d")
			} else {
				why = append(why, f.S("mount")+" "+pyG(f.Get("free_pct"))+" % free")
			}
		}
		for _, x := range [][2]string{{"cpu_p95", "CPU"}, {"mem_p95", "memory"}} {
			if capHotLevel(x[0], metric(v, x[0])) != "ok" {
				why = append(why, x[1]+" p95 "+pyG(metric(v, x[0]))+" %")
			}
		}
		for _, w := range why {
			whyAny = append(whyAny, w)
		}
		if whyAny == nil {
			whyAny = []any{}
		}
		dl, fl := []any{}, []any{}
		byMount := append([]*py.OMap(nil), disks...)
		sort.SliceStable(byMount, func(i, j int) bool { return byMount[i].S("mount") < byMount[j].S("mount") })
		var maxGrowth any
		for _, f := range disks {
			d := py.NewOMap()
			for _, k := range []string{"mount", "size", "used_pct", "free_pct", "days", "level"} {
				d.Set(k, f.Get(k))
			}
			dl = append(dl, d)
			if g, ok := f.Get("growth_per_day").(int64); ok {
				if m, ok := maxGrowth.(int64); !ok || g > m {
					maxGrowth = g
				}
			}
		}
		for _, f := range byMount {
			d := py.NewOMap()
			for _, k := range []string{"mount", "size", "used_pct", "growth_per_day", "days", "level"} {
				d.Set(k, f.Get(k))
			}
			fl = append(fl, d)
		}
		role := inv.s("role")
		if role == "" {
			role = v.job
		}
		row := py.NewOMap("key", v.key, "name", name, "inventory_id", inv["id"], "env", env, "dc", v.dc, "role", role,
			"instance", v.instance, "monitored", true, "level", capWorst(lv), "why", whyAny, "fs_count", int64(len(disks)),
			"disks", dl, "filesystems", fl, "max_growth", maxGrowth)
		for _, k := range capVMMetrics {
			row.Set(k, metric(v, k))
		}
		rows = append(rows, row)
	}
	for _, inv := range inventory {
		id, _ := inv["id"].(int64)
		if _, ok := claimed[id]; ok {
			continue
		}
		dc, _, _ := strings.Cut(str(or(inv["dc"], "unknown")), "+")
		row := py.NewOMap("key", inv["key"], "name", or(inv["name"], inv["key"]), "inventory_id", inv["id"],
			"env", or(inv["env"], "unknown"), "dc", dc, "role", or(inv["role"], ""), "instance", or(inv["prom_instance"], ""),
			"monitored", false, "level", "unmonitored", "why", []any{unmonitoredReason(inv.s("flags"))}, "fs_count", int64(0),
			"disks", []any{}, "filesystems", []any{}, "max_growth", nil)
		for _, k := range capVMMetrics {
			row.Set(k, nil)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := rankOf(a.S("level")), rankOf(b.S("level")); ra != rb {
			return ra < rb
		}
		first := func(r *py.OMap) *py.OMap {
			if l := r.List("disks"); len(l) > 0 {
				return py.AsMap(l[0])
			}
			return py.NewOMap()
		}
		if da, db := daysOr(first(a), 1e9), daysOr(first(b), 1e9); da != db {
			return da < db
		}
		peak := func(r *py.OMap) float64 {
			return -math.Max(fnOr0(first(r).Get("used_pct")), math.Max(fnOr0(r.Get("cpu_p95")), fnOr0(r.Get("mem_p95"))))
		}
		if pa, pb := peak(a), peak(b); pa != pb {
			return pa < pb
		}
		return strings.ToLower(str(a.Get("name"))) < strings.ToLower(str(b.Get("name")))
	})
	var allfs []*py.OMap
	for _, k := range fsOrder {
		if !removed[k] {
			allfs = append(allfs, fs[k])
		}
	}
	sort.SliceStable(allfs, func(i, j int) bool {
		a, b := allfs[i], allfs[j]
		if oa, ob := capFSOrder[a.S("level")], capFSOrder[b.S("level")]; oa != ob {
			return oa < ob
		}
		if da, db := daysOr(a, 1e9), daysOr(b, 1e9); da != db {
			return da < db
		}
		fp := func(f *py.OMap) float64 {
			if x, ok := f.Get("free_pct").(float64); ok {
				return x
			}
			return 100
		}
		return fp(a) < fp(b)
	})
	byLevel := py.NewOMap()
	for _, k := range capRankKeys {
		byLevel.Set(k, int64(0))
	}
	var monitored, notInInv int64
	for _, r := range rows {
		byLevel.Set(r.S("level"), numOr0(byLevel.Get(r.S("level")))+1)
		if r.Get("monitored") == true {
			monitored++
			if r.Get("inventory_id") == nil {
				notInInv++
			}
		}
	}
	fsBy := py.NewOMap()
	for _, k := range []string{"critical", "warn", "full", "watch", "ok"} {
		fsBy.Set(k, int64(0))
	}
	for _, f := range allfs {
		fsBy.Set(f.S("level"), numOr0(fsBy.Get(f.S("level")))+1)
	}
	hot := py.NewOMap("cpu_p95", []any{int64(90), int64(97)}, "mem_p95", []any{int64(90), int64(97)})
	summary := py.NewOMap("vms", int64(len(rows)), "filesystems", int64(len(allfs)), "by_level", byLevel, "fs_by_level", fsBy,
		"inventory", int64(len(inventory)), "monitored", monitored, "unmonitored", byLevel.Get("unmonitored"),
		"not_in_inventory", notInInv, "hot", hot, "full_pct", int64(capFullPct))
	return allfs, rows, summary
}

func capAtRisk(f *py.OMap) bool {
	if d, ok := f.Get("days").(float64); ok && d < capHorizonDays {
		return true
	}
	fp, ok := f.Get("free_pct").(float64)
	return ok && fp < capFullPct
}

// Capacity writes capacity.json (the previous one kept as capacity-prev.json). `filesystems` is the at-risk set
// (full within 60 days or under 10 % free), `vms` one row per VM worst first, `summary` the counts.
func (o *Options) Capacity(ctx context.Context) (*py.OMap, error) {
	if len(o.Sites) == 0 {
		return nil, ErrNoSite
	}
	now := o.now().Unix()
	results := map[string]map[string][]capSample{}
	errors := []any{}
	for _, s := range o.Sites {
		res, errs := o.capQuery(ctx, s)
		results[s.Name] = res
		for _, e := range errs {
			errors = append(errors, e)
		}
	}
	inventory := loadCapInventory(o.DB)
	if len(inventory) == 0 {
		errors = append(errors, "inventory: no VMs in ops.db (run argus estate inventory); only Prometheus VMs are listed")
	}
	allfs, vms, summary := o.capBuild(results, inventory)
	out := filepath.Join(o.StateDir, "capacity.json")
	prev := readJSONMap(out)
	prevUnder := map[[2]string]bool{}
	for _, x := range prev.List("filesystems") {
		if m := py.AsMap(x); m != nil && (m.S("level") == "critical" || m.S("level") == "warn") {
			prevUnder[[2]string{str(m.Get("host")), str(m.Get("mount"))}] = true
		}
	}
	filesystems := []any{}
	for _, f := range allfs {
		if !capAtRisk(f) {
			continue
		}
		x := py.NewOMap()
		for _, k := range f.Keys() {
			if k != "growth_per_day" {
				x.Set(k, f.Get(k))
			}
		}
		lvl := x.S("level")
		x.Set("new", (lvl == "critical" || lvl == "warn") && prev != nil && !prevUnder[[2]string{str(x.Get("host")), str(x.Get("mount"))}])
		filesystems = append(filesystems, x)
	}
	vl := make([]any, len(vms))
	for i, v := range vms {
		vl[i] = v
	}
	doc := py.NewOMap("generated_at", now, "window", "7d", "horizon_days", int64(capHorizonDays), "errors", errors,
		"method", capMethod, "filesystems", filesystems, "vms", vl, "summary", summary)
	if prev != nil {
		if err := os.Rename(out, filepath.Join(o.StateDir, "capacity-prev.json")); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(out, []byte(py.DumpsCompact(doc))); err != nil {
		return nil, err
	}
	return summary, nil
}
