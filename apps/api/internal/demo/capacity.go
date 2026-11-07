package demo

import (
	"fmt"
	"sort"

	"github.com/sinaneysi/argus/internal/py"
)

func fsLevel(days any, freePct float64) string {
	d, has := days.(float64)
	switch {
	case has && d < 7:
		return "critical"
	case has && d < 14:
		return "warn"
	case freePct < 10:
		return "full"
	case has && d < 30:
		return "watch"
	}
	return "ok"
}

// GROWTH: (vm, mount) -> growth per day in bytes (positive: filling).
var GROWTH = map[[2]string]float64{{"prod-mon-01", "/var/lib/loki"}: 6.8 * GBf, {"dev-runner-01", "/"}: 0.55 * GBf,
	{"prod-db-01", "/var/lib/postgresql"}: 3.9 * GBf, {"prod-mongo-01", "/var/lib/mongodb"}: 2.2 * GBf,
	{"prod-backup-01", "/data"}: 41 * GBf, {"prod-media-01", "/srv"}: 1.1 * GBf, {"prod-search-01", "/var/lib/data"}: 0.9 * GBf}

// growthChoices is [0, 0, 0.02, 0.05, -0.01]; the ints stay ints in Python (0 * GB is the int 0).
var growthChoices = []float64{0, 0, 0.02, 0.05, -0.01}

func daysKey(f *py.OMap) float64 {
	if d, ok := f.Get("days").(float64); ok {
		return d
	}
	return 1e9
}

func numF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

func (g *Gen) capacity() {
	rank := map[string]int{"critical": 0, "warn": 1, "full": 2, "watch": 3, "unmonitored": 4, "ok": 5}
	var vms, allfs []*py.OMap
	for idx, v := range VMS {
		i := idx + 1
		name, env, dc, ip, role := v.name, v.env, v.dc, v.ip, v.role
		doc := g.vmDocs[i]
		if VM_FLAGS[name] == "not-monitored" {
			vms = append(vms, om("key", name, "name", name, "inventory_id", i, "env", env, "dc", dc, "role", "node-"+role,
				"instance", "", "monitored", false, "level", "unmonitored",
				"why", sl("no node_exporter data in either DC"), "fs_count", 0, "disks", L(), "filesystems", L(),
				"max_growth", nil, "cpu_avg", nil, "cpu_p95", nil, "mem_avg", nil, "mem_p95", nil,
				"load_avg", nil, "load_p95", nil, "io_p95", nil))
			continue
		}
		var disks []*py.OMap
		mountNames := []string{"/"}
		for k := range doc.mounts {
			if k > 0 {
				mountNames = append(mountNames, g.mountName(i, k))
			}
		}
		for k, m := range doc.mounts {
			size, avail := m[0], m[1]
			mnt := mountNames[k]
			freePct := round(float64(avail)/float64(size)*100, 1)
			c := growthChoices[g.r.choice(5)] // drawn even when GROWTH has the mount
			growth, isInt := c*GBf, c == 0
			if gv, ok := GROWTH[[2]string{name, mnt}]; ok {
				growth, isInt = gv, false
			}
			_ = isInt
			var days any
			if growth > 0 {
				d := round(float64(avail)/growth, 1)
				if d <= 400 {
					days = d
				}
			}
			var loss any
			if growth > 0 {
				loss = roundi(growth)
			}
			dev := "/dev/vdb1"
			if mnt == "/" {
				dev = "/dev/vda1"
			}
			f := om("dc", dc, "host", ip+":9100", "mount", mnt, "device", dev,
				"env", env, "job", "node-"+role, "avail", avail, "size", size, "free_pct", freePct,
				"used_pct", round(100-freePct, 1), "days", days, "loss_per_day", loss,
				"growth_per_day", roundi(growth), "level", fsLevel(days, freePct), "vm", name, "vm_id", i, "vm_name", name)
			disks = append(disks, f)
			allfs = append(allfs, f)
		}
		sort.SliceStable(disks, func(a, b int) bool {
			x, y := disks[a], disks[b]
			if rank[x.S("level")] != rank[y.S("level")] {
				return rank[x.S("level")] < rank[y.S("level")]
			}
			if daysKey(x) != daysKey(y) {
				return daysKey(x) < daysKey(y)
			}
			return -numF(x.Get("used_pct")) < -numF(y.Get("used_pct"))
		})
		cpuP95 := round(doc.cpu*1.15, 1)
		memP95 := round(min99(doc.mem*1.04), 1)
		hot := func(v float64) string {
			if v >= 97 {
				return "critical"
			}
			if v >= 90 {
				return "warn"
			}
			return "ok"
		}
		lv := []string{}
		for _, f := range disks {
			lv = append(lv, f.S("level"))
		}
		lv = append(lv, hot(cpuP95), hot(memP95))
		level := lv[0]
		for _, x := range lv[1:] {
			if rank[x] < rank[level] {
				level = x
			}
		}
		why := []any{}
		for _, f := range disks {
			if f.S("level") == "ok" {
				continue
			}
			if d, ok := f.Get("days").(float64); ok && d < 30 {
				why = append(why, f.S("mount")+" full in "+py.G(d, 6)+" d")
			} else {
				why = append(why, f.S("mount")+" "+py.G(f.Get("free_pct").(float64), 6)+" % free")
			}
		}
		for _, p := range []struct {
			v   float64
			lbl string
		}{{cpuP95, "CPU"}, {memP95, "memory"}} {
			if hot(p.v) != "ok" {
				why = append(why, fmt.Sprintf("%s p95 %s %%", p.lbl, py.G(p.v, 6)))
			}
		}
		diskSub := []any{}
		for _, f := range disks {
			diskSub = append(diskSub, sub(f, "mount", "size", "used_pct", "free_pct", "days", "level"))
		}
		byMount := append([]*py.OMap{}, disks...)
		sort.SliceStable(byMount, func(a, b int) bool { return byMount[a].S("mount") < byMount[b].S("mount") })
		fsSub := []any{}
		var maxGrowth any
		for _, f := range byMount {
			fsSub = append(fsSub, sub(f, "mount", "size", "used_pct", "growth_per_day", "days", "level"))
		}
		for _, f := range disks {
			gp := f.Get("growth_per_day").(int64)
			if maxGrowth == nil || gp > maxGrowth.(int64) {
				maxGrowth = gp
			}
		}
		vms = append(vms, om("key", name, "name", name, "inventory_id", i, "env", env, "dc", dc, "role", "node-"+role,
			"instance", ip+":9100", "monitored", true, "level", level, "why", why, "fs_count", len(disks),
			"disks", diskSub, "filesystems", fsSub, "max_growth", maxGrowth,
			"cpu_avg", round(doc.cpu*0.7, 1), "cpu_p95", cpuP95,
			"mem_avg", round(doc.mem*0.95, 1), "mem_p95", memP95,
			"load_avg", round(doc.cpu/120, 2), "load_p95", round(doc.cpu/90, 2),
			"io_p95", round(doc.psiIO*4.2, 1)))
	}
	vmKey := func(r *py.OMap) (int, float64, float64, string) {
		days, used := 1e9, 0.0
		if l := r.List("disks"); len(l) > 0 {
			d := l[0].(*py.OMap)
			days = daysKey(d)
			used = numF(d.Get("used_pct"))
		}
		m := used
		for _, k := range []string{"cpu_p95", "mem_p95"} {
			if x := numF(r.Get(k)); x > m {
				m = x
			}
		}
		lvl, ok := rank[r.S("level")]
		if !ok {
			lvl = 9
		}
		return lvl, days, -m, r.S("name")
	}
	sort.SliceStable(vms, func(a, b int) bool {
		a1, a2, a3, a4 := vmKey(vms[a])
		b1, b2, b3, b4 := vmKey(vms[b])
		if a1 != b1 {
			return a1 < b1
		}
		if a2 != b2 {
			return a2 < b2
		}
		if a3 != b3 {
			return a3 < b3
		}
		return a4 < b4
	})
	order := map[string]int{"critical": 0, "warn": 1, "full": 2, "watch": 3, "ok": 4}
	sort.SliceStable(allfs, func(a, b int) bool {
		x, y := allfs[a], allfs[b]
		if order[x.S("level")] != order[y.S("level")] {
			return order[x.S("level")] < order[y.S("level")]
		}
		if daysKey(x) != daysKey(y) {
			return daysKey(x) < daysKey(y)
		}
		return x.Get("free_pct").(float64) < y.Get("free_pct").(float64)
	})
	byLevel := om()
	for _, k := range []string{"critical", "warn", "full", "watch", "unmonitored", "ok"} {
		byLevel.Set(k, 0)
	}
	monitored := 0
	vmList := []any{}
	for _, v := range vms {
		byLevel.Set(v.S("level"), byLevel.Get(v.S("level")).(int)+1)
		if b, _ := v.Get("monitored").(bool); b {
			monitored++
		}
		vmList = append(vmList, v)
	}
	fsByLevel := om()
	for _, k := range []string{"critical", "warn", "full", "watch", "ok"} {
		fsByLevel.Set(k, 0)
	}
	atRisk := []any{}
	for _, f := range allfs {
		fsByLevel.Set(f.S("level"), fsByLevel.Get(f.S("level")).(int)+1)
		d, has := f.Get("days").(float64)
		if (has && d < 60) || f.Get("free_pct").(float64) < 10 {
			x := om()
			for _, k := range f.Keys() {
				if k != "growth_per_day" {
					x.Set(k, f.Get(k))
				}
			}
			x.Set("new", f.S("vm") == "prod-mon-01")
			atRisk = append(atRisk, x)
		}
	}
	g.writeJSON("capacity.json", om(
		"generated_at", g.t(2*H+17*M), "window", "7d", "horizon_days", 60, "errors", L(),
		"method", "deriv(node_filesystem_avail_bytes[7d:1h]) per filesystem; CPU, memory, load, IO as avg / p95 of "+
			"5-minute rates sampled hourly over 7 days ([7d:1h] subqueries); both DCs",
		"filesystems", atRisk, "vms", vmList,
		"summary", om("vms", len(vms), "filesystems", len(allfs), "by_level", byLevel, "fs_by_level", fsByLevel,
			"inventory", len(VMS), "monitored", monitored,
			"unmonitored", byLevel.Get("unmonitored"), "not_in_inventory", 0,
			"hot", om("cpu_p95", L(90, 97), "mem_p95", L(90, 97)), "full_pct", 10)))
}

func min99(x float64) float64 {
	if x < 99 {
		return x
	}
	return 99
}

func sub(f *py.OMap, keys ...string) *py.OMap {
	o := om()
	for _, k := range keys {
		o.Set(k, f.Get(k))
	}
	return o
}

// mountName is the k-th mount of VM i (the data mount; the first is "/").
func (g *Gen) mountName(i, k int) string {
	v := VMS[i-1]
	switch v.role {
	case "db":
		if contains(v.svcs, "postgresql") {
			return "/var/lib/postgresql"
		}
		return "/var/lib/mongodb"
	case "infra":
		if contains(v.svcs, "loki") {
			return "/var/lib/loki"
		}
		return "/data"
	case "data":
		return "/var/lib/data"
	}
	return "/srv"
}
