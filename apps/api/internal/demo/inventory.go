package demo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// vmStory is what is wrong with an interesting VM (the rest are healthy). The Python dict literals had duplicate
// keys for prod-db-02 ("io") and prod-matomo-01 ("cpu"); the later value won, so only the number is kept there.
type vmStory struct {
	states               map[string][2]string // family -> (state, why)
	restarting, degraded []string
	stopped              []string
	dead                 []int
	memUsed, diskUsed    int
	cpu, io              int
}

var VM_STORY = map[string]vmStory{
	"prod-pay-01":    {states: map[string][2]string{"memory": {"critical", "memory hit 95 % in the last 24 h (critical at 95 %), sms-otp worker pool holds 3.1 GB"}}, restarting: sl("sms-otp"), memUsed: 94},
	"prod-pay-02":    {states: map[string][2]string{"memory": {"pressure", "memory used 88 % (pressure at 85 %)"}}, memUsed: 88},
	"prod-shop-02":   {dead: []int{3001}, degraded: sl("shop-front")},
	"prod-search-01": {states: map[string][2]string{"memory": {"pressure", "opensearch heap 87 % of RAM"}}, memUsed: 87},
	"prod-db-02":     {io: 81},
	"prod-mon-01":    {states: map[string][2]string{"disk": {"critical", "/var/lib/loki 93 % used, full in 4.6 days"}}, diskUsed: 93},
	"prod-matomo-01": {cpu: 78},
	"dev-runner-01":  {states: map[string][2]string{"disk": {"pressure", "/ 88 % used (docker images)"}}, diskUsed: 88},
	"prod-wh-01":     {stopped: sl("shipping-rates")},
}

var VM_FLAGS = map[string]string{"dev-runner-01": "disk-85", "prod-mon-01": "disk-85", "stg-db-01": "ssh-skipped-db",
	"prod-db-01": "ssh-skipped-db", "prod-db-02": "ssh-skipped-db", "prod-db-03": "ssh-skipped-db",
	"prod-backup-02": "not-monitored", "prod-matomo-01": "os-eol", "dev-db-01": "no-ntp"}

var OS_OF = map[string][4]string{"prod-matomo-01": {"Ubuntu 20.04.6 LTS", "ubuntu", "20.04", "5.4.0-196-generic"}}
var DEFAULT_OS = [4]string{"Ubuntu 24.04.1 LTS", "ubuntu", "24.04", "6.8.0-45-generic"}

// OLD_KERNEL: the kernel reboot wave; these 21 production hosts run the old kernel.
const OLD_KERNEL = "6.8.0-41-generic"

var KERNEL_WAVE = func() map[string]bool {
	out := map[string]bool{}
	for _, v := range VMS {
		if v.env == "release" && v.role != "db" && v.name != "prod-matomo-01" && len(out) < 21 {
			out[v.name] = true
		}
	}
	return out
}()

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// series is _series(): n points `step` apart ending at `end`, with noise, a trend and an optional spike.
func (g *Gen) series(base, spread float64, n int, step, end int64, trend float64, spike *[2]float64) []any {
	pts := make([]any, 0, n)
	den := n - 1
	if den < 1 {
		den = 1
	}
	for i := 0; i < n; i++ {
		t := end - int64(n-1-i)*step
		v := base + trend*float64(i)/float64(den) + g.r.uniform(-spread, spread)
		if spike != nil && i >= n-int(spike[0]) {
			v += spike[1]
		}
		if !(v > 0.0) { // max(0.0, v)
			v = 0.0
		}
		pts = append(pts, L(t, round(v, 2)))
	}
	return pts
}

func (g *Gen) inventory() {
	first := g.t(180 * D)
	for idx, v := range VMS {
		i := idx + 1
		name, env, dc, ip, vcpu, ram, disk, role, svcs := v.name, v.env, v.dc, v.ip, v.vcpu, v.ram, v.disk, v.role, v.svcs
		g.vmIDs[name] = i
		osv, ok := OS_OF[name]
		if !ok {
			osv = DEFAULT_OS
		}
		kernel := osv[3]
		if KERNEL_WAVE[name] {
			kernel = OLD_KERNEL
		}
		story := VM_STORY[name]
		usedPct := g.r.randint(22, 64) // story.get("disk_used", randint(...)) draws in any case
		if story.diskUsed != 0 {
			usedPct = story.diskUsed
		}
		rootGB := disk
		if disk > 40 {
			rootGB = 40
		}
		mounts := []mount{{"/", int64(rootGB) * GB, int64(float64(int64(rootGB)*GB*int64(usedPct)) / 100)}}
		if disk > 60 {
			dm := "/srv"
			switch role {
			case "db":
				dm = "/var/lib/mongodb"
				if contains(svcs, "postgresql") {
					dm = "/var/lib/postgresql"
				}
			case "infra":
				dm = "/data"
				if contains(svcs, "loki") {
					dm = "/var/lib/loki"
				}
			case "data":
				dm = "/var/lib/data"
			}
			pct := g.r.randint(30, 70)
			if story.diskUsed != 0 {
				pct = story.diskUsed
			}
			size := int64(disk-40) * GB
			mounts = append(mounts, mount{dm, size, int64(float64(size) * (float64(pct) / 100))})
		}
		var total, usedb int64
		mountList := []any{}
		for _, m := range mounts {
			total += m.size
			usedb += m.used
			mountList = append(mountList, om("mount", m.mount, "size", m.size, "used", m.used))
		}
		flags := VM_FLAGS[name]
		monitored := 1
		if flags == "not-monitored" {
			monitored = 0
		}
		doms := om()
		if role == "edge" {
			doms = om("edge_tree", iif(env == "release", "shop", env), "edge_domains", iif(env == "release", 14, 6))
		}
		backendSet := map[string]bool{}
		for _, s := range svcs {
			if sv, ok := SERVICES[s]; ok && sv.domain != "" {
				backendSet[sv.domain] = true
			}
		}
		backend := sortedKeys(backendSet)
		if len(backend) > 0 {
			if env == "release" {
				doms.Set("backend_for", backend)
			} else {
				pre := "dev."
				if env == "pre-release" {
					pre = "stg."
				}
				var out []string
				for _, d := range backend {
					out = append(out, strings.ReplaceAll(d, DOMAIN, pre+DOMAIN))
				}
				doms.Set("backend_for", out)
			}
		}
		if role == "db" {
			var roles []string
			for _, s := range svcs {
				roles = append(roles, s+iif(strings.HasSuffix(name, "01"), " primary", " replica").(string))
			}
			doms.Set("data_roles", roles)
		}
		dockerV := ""
		if contains(svcs, "docker") || role == "app" {
			dockerV = "27.3.1"
		}
		facts := om("cpu_model", "Intel Xeon Gold 6338 (virtual)", "nproc", vcpu, "docker", dockerV,
			"systemd", "255", "hostname", name, "machine_id", h12(name)+h12(name + "m")[:8])
		var boot int64
		if kernel != OLD_KERNEL {
			boot = g.t(float64(g.r.randint(9, 80)) * D)
		} else {
			boot = g.t(float64(g.r.randint(40, 120)) * D)
		}
		alias := ""
		if flags != "not-monitored" {
			alias = map[string]string{"release": "prod-", "pre-release": "stg-", "develop": "dev-"}[env] + strings.SplitN(name, "-", 2)[1] + "-ro"
		}
		sshOK := 1
		if strings.Contains(flags, "ssh-skipped-db") || alias == "" {
			sshOK = 0
		}
		g.ins(g.ops, "vms", "id", i, "key", name, "name", name, "ips", ip, "env", env, "dc", dc, "os", osv[0], "os_id", osv[1],
			"os_version", osv[2], "kernel", kernel, "arch", "x86_64", "vcpu", vcpu, "ram_bytes", int64(ram)*GB, "disk_total", total,
			"disk_used", usedb, "mounts", jd(mountList), "boot_time", boot, "virt", "kvm", "hypervisor", "proxmox-"+dc,
			"monitored", monitored, "up", monitored, "last_scrape", g.t(40), "prom_instance", iif(monitored == 1, ip+":9100", nil),
			"prom_job", "node-"+role, "ssh_alias", iif(alias != "", alias, nil), "ssh_ok", sshOK,
			"docker", iif(role == "app", 1, 0), "containers", iif(role == "app", len(svcs), 0),
			"services_running", len(svcs), "domains", jd(doms), "ntp_sync", iif(flags == "no-ntp", "no", "yes"),
			"timezone", "Etc/UTC", "flags", flags, "facts", jd(facts), "first_seen", first+int64(i)*3600, "last_seen", g.t(300), "gone", 0)
		if kernel == OLD_KERNEL {
			g.ins(g.ops, "vm_changes", "vm_id", i, "at", g.t(41*D), "field", "kernel", "old", "6.8.0-38-generic", "new", OLD_KERNEL)
		}
		if name == "prod-api-03" || name == "prod-shop-03" {
			g.ins(g.ops, "vm_changes", "vm_id", i, "at", g.t(12*D), "field", "ram_bytes", "old", fmt.Sprint(8*GB), "new", fmt.Sprint(16*GB))
		}
		if name == "prod-pay-01" {
			g.ins(g.ops, "vm_changes", "vm_id", i, "at", g.t(3*D), "field", "services_running", "old", "3", "new", "3")
		}
		g.vmDoc(i, v, story, mounts, alias)
	}
	sshOK := 0
	for _, v := range VMS {
		if f := VM_FLAGS[v.name]; f != "ssh-skipped-db" && f != "not-monitored" {
			sshOK++
		}
	}
	g.writeJSON("inventory-status.json", om("at", g.t(25*M), "status", "OK", "vms", len(VMS), "monitored", len(VMS)-1,
		"ssh_ok", sshOK, "duration_s", 74, "errors", L()))
	g.writeJSON("vmstate-status.json", om("at", g.t(4*M), "status", "OK", "vms", len(VMS), "ssh", 34, "prom", 40,
		"duration_s", 51, "errors", L()))
	g.vmPulse()
}

type mount struct {
	mount      string
	size, used int64
}

var sysPorts = map[string]int{"nginx": 443, "postgresql": 5432, "mongod": 27017, "redis": 6379, "rabbitmq": 5672,
	"opensearch": 9200, "prometheus": 9090, "loki": 3100, "grafana": 3000, "minio": 9000,
	"docker": 2376, "gitlab-runner": 9252, "swarm-manager": 2377, "swarm-worker": 7946}

// num keeps a Python value that is an int when it came from the story and a float when it was drawn.
func num(f float64, isInt bool) any {
	if isInt {
		return int64(f)
	}
	return f
}

func (g *Gen) vmDoc(vid int, v vm, story vmStory, mounts []mount, alias string) {
	now := g.now
	name, vcpu, ram, svcs, role := v.name, v.vcpu, v.ram, v.svcs, v.role
	families := []string{"cpu", "memory", "io", "disk", "network"}
	states := map[string]string{}
	reasons := om()
	for _, k := range families {
		states[k] = "ok"
		if st, ok := story.states[k]; ok {
			states[k] = st[0]
			reasons.Set(k, L(st[1]))
		}
	}
	services := []any{}
	healthy, down, restarting, degraded, stopped := 0, 0, 0, 0, 0
	upstreams := []any{}
	for _, s := range svcs {
		sv, isApp := SERVICES[s]
		port := sv.port
		if !isApp {
			port = 8000
			if p, ok := sysPorts[s]; ok {
				port = p
			}
		}
		verdict, why := "healthy", []any{}
		switch {
		case contains(story.restarting, s):
			verdict, why = "restarting", L("4 automatic restarts in the last hour (OOM kill at 3.2 GB)")
			restarting++
		case contains(story.degraded, s):
			verdict, why = "degraded", L("1 of 2 instances not listening (:3001)")
			degraded++
		case contains(story.stopped, s):
			verdict, why = "stopped", L("inactive since the 10-17 deploy; enabled")
			stopped++
		default:
			healthy++
		}
		var mem int64
		if s != "postgresql" && s != "mongod" && s != "opensearch" {
			mem = int64(g.r.uniform(0.2, 1.6) * GBf)
		} else {
			mem = int64(float64(int64(ram)*GB) * 0.55)
		}
		if s == "sms-otp" && name == "prod-pay-01" {
			mem = int64(3.1 * GBf)
		}
		unit := s + ".service"
		if isApp {
			unit = "northwind-" + s + ".service"
		}
		ports := []int{}
		if port != 0 {
			ports = []int{port}
		}
		dead := []int{}
		if s == "shop-front" {
			ports = []int{3000, 3001}
			for _, d := range story.dead {
				if d == 3001 {
					ports, dead = []int{3000}, []int{3001}
				}
			}
		}
		kind := "system"
		if isApp {
			kind = "app"
		} else if role == "db" {
			kind = "db"
		}
		var since int64
		if verdict != "restarting" {
			since = g.t(float64(g.r.randint(2, 30)) * D)
		} else {
			since = g.t(11 * M)
		}
		var cpuPct any = int64(0)
		if verdict != "stopped" {
			cpuPct = round(g.r.uniform(1, 45), 1)
		}
		var errs1h int64
		switch verdict {
		case "restarting":
			errs1h = 312
		case "degraded":
			errs1h = 88
		default:
			errs1h = []int64{0, 0, 0, 2, 5}[g.r.choice(5)]
		}
		domains := []any{}
		if isApp && sv.domain != "" {
			domains = L(sv.domain)
		}
		svc := om("unit", unit, "kind", kind, "verdict", verdict, "why", why, "active", iif(verdict == "stopped", "inactive", "active"),
			"since", since, "mem", iif(verdict != "stopped", mem, int64(0)), "mem_peak", int64(float64(mem)*1.25), "anon", int64(float64(mem)*0.7),
			"cpu_pct", cpuPct, "restarts", iif(verdict == "restarting", 4, 0), "ports", intList(ports), "dead_ports", intList(dead),
			"errors_1h", errs1h, "domains", domains)
		services = append(services, svc)
		if isApp && sv.domain != "" {
			for _, p := range append(append([]int{}, ports...), dead...) {
				isDead := false
				for _, d := range dead {
					if d == p {
						isDead = true
					}
				}
				upstreams = append(upstreams, om("port", p, "listening", !isDead, "unit", iif(!isDead, unit, nil),
					"refs", L(om("domain", sv.domain, "location", "/", "source", "live: prod-edge-01, prod-edge-02"),
						om("domain", sv.domain, "location", "/", "source", "repo: nginx-sites/shop.conf")),
					"n_refs", 2))
			}
		}
	}
	services = append(services, om("unit", "logrotate.service", "kind", "system", "verdict", "idle", "active", "inactive"))
	services = append(services, om("unit", "apt-daily.service", "kind", "system", "verdict", "idle", "active", "inactive"))
	nSvc := healthy + down + restarting + degraded + stopped
	deadUp := 0
	for _, u := range upstreams {
		if l, _ := u.(*py.OMap).Get("listening").(bool); !l {
			deadUp++
		}
	}
	// story.get(k, uniform(...)) draws in any case
	cpuNow, cpuInt := g.r.uniform(8, 35), false
	if story.cpu != 0 {
		cpuNow, cpuInt = float64(story.cpu), true
	}
	memNow, memInt := g.r.uniform(35, 70), false
	if story.memUsed != 0 {
		memNow, memInt = float64(story.memUsed), true
	}
	ioNow := g.r.uniform(2, 20)
	if story.io != 0 {
		ioNow = float64(story.io)
	}
	diskMax := 0.0
	for k, m := range mounts {
		p := 100 * float64(m.used) / float64(m.size)
		if k == 0 || p > diskMax {
			diskMax = p
		}
	}
	end := now - 600
	s24 := func(base, sp float64, spike *[2]float64) []any { return g.series(base, sp, 96, 900, end, 0, spike) }
	s7 := func(base, sp, trend float64) []any { return g.series(base, sp, 84, 7200, end, trend, nil) }
	pair := func(a, b []any) *py.OMap { return om("24h", a, "7d", b) }
	ramB := float64(int64(ram) * GB)
	var oomSpike *[2]float64
	if len(story.restarting) > 0 {
		oomSpike = &[2]float64{4, 1}
	}
	lat24, lat7 := 2.5, 2.2
	if ioNow > 70 {
		lat24, lat7 = 14, 11
	}
	series := om()
	series.Set("cpu_busy", pair(s24(cpuNow*0.8, 6, &[2]float64{8, cpuNow * 0.2}), s7(cpuNow*0.7, 8, 0)))
	series.Set("psi_cpu", pair(s24(cpuNow/8, 1.5, nil), s7(cpuNow/9, 1.5, 0)))
	series.Set("load_per_cpu", pair(s24(cpuNow/100, 0.08, nil), s7(cpuNow/110, 0.08, 0)))
	series.Set("cpu_steal", pair(s24(0.4, 0.3, nil), s7(0.4, 0.3, 0)))
	series.Set("mem_used_pct", pair(s24(memNow-4, 1.5, &[2]float64{12, 4}), s7(memNow-12, 2, 8)))
	series.Set("mem_avail", pair(s24(ramB*(100-memNow)/100, ramB*0.01, nil), s7(ramB*(100-memNow)/95, ramB*0.02, 0)))
	series.Set("psi_mem", pair(s24(pmax(0.1, (memNow-80)/2), 0.5, nil), s7(pmax(0.1, (memNow-85)/3), 0.4, 0)))
	series.Set("majfault", pair(s24(3, 2, nil), s7(3, 2, 0)))
	series.Set("oom", pair(s24(0, 0, oomSpike), s7(0, 0, 0)))
	series.Set("cpu_iowait", pair(s24(ioNow/6, 1, nil), s7(ioNow/7, 1, 0)))
	series.Set("psi_io", pair(s24(ioNow/4, 1.5, nil), s7(ioNow/5, 1.5, 0)))
	series.Set("disk_util", pair(s24(ioNow, 5, nil), s7(ioNow*0.9, 5, 0)))
	series.Set("disk_lat", pair(s24(lat24, 1.2, nil), s7(lat7, 1, 0)))
	series.Set("net_rx", pair(s24(6e6, 2e6, nil), s7(5.5e6, 2e6, 0)))
	series.Set("net_tx", pair(s24(4e6, 1.5e6, nil), s7(3.8e6, 1.5e6, 0)))
	series.Set("net_err", pair(s24(0.1, 0.1, nil), s7(0.1, 0.1, 0)))
	series.Set("tcp_estab", pair(s24(420, 80, nil), s7(400, 80, 0)))
	series.Set("conntrack_pct", pair(s24(6, 2, nil), s7(6, 2, 0)))
	series.Set("fd_pct", pair(s24(3, 1, nil), s7(3, 1, 0)))
	thresholds := om("cpu_busy", L(70, 90), "psi_cpu", L(20, 40), "load_per_cpu", L(1.0, 2.0), "cpu_steal", L(5, 15),
		"mem_used_pct", L(85, 95), "psi_mem", L(10, 25), "psi_mem_full", L(5, 15), "majfault", L(200, 1000),
		"oom", L(1, 3), "cpu_iowait", L(15, 30), "psi_io", L(20, 40), "psi_io_full", L(10, 25),
		"disk_util", L(70, 90), "disk_lat", L(10, 30), "conntrack_pct", L(70, 90), "fd_pct", L(70, 90))
	flags := VM_FLAGS[name]
	var sshAt any
	if alias != "" && !strings.Contains(flags, "ssh-skipped-db") {
		sshAt = g.t(4 * M)
	}
	hidepid := "0"
	if role == "app" {
		hidepid = "2"
	}
	mountDocs := []any{}
	vn := vmNow{cpu: round(cpuNow, 1), mem: round(memNow, 1), cpuIsInt: cpuInt, memIsInt: memInt} // doc["now"]
	for _, m := range mounts {
		mountDocs = append(mountDocs, om("mount", m.mount, "size", m.size, "avail", m.size-m.used, "files", 2621440, "ffree", 2400000))
		vn.mounts = append(vn.mounts, [2]int64{m.size, m.size - m.used})
	}
	verdicts := om()
	for _, k := range families {
		r := reasons.Get(k)
		if r == nil {
			r = L()
		}
		verdicts.Set(k, om("state", states[k], "reasons", r))
	}
	fv := float64(vcpu)
	psiIO := round(ioNow/4, 1)
	vn.psiIO = psiIO
	g.vmDocs[vid] = vn
	doc := om("vm_id", vid, "name", name, "collected_at", g.t(4*M), "ssh_at", sshAt,
		"prom_at", g.t(4*M), "hidepid", hidepid,
		"host", om("nproc", vcpu, "load", L(round(fv*cpuNow/100, 1), round(fv*cpuNow/110, 1), round(fv*cpuNow/120, 1))),
		"now", om("cpu_busy", num(round(cpuNow, 1), cpuInt), "mem_used_pct", num(round(memNow, 1), memInt), "psi_io", psiIO,
			"disk_used_max", round(diskMax, 1), "net_rx", 6.1e6),
		"summary", om("services", nSvc, "healthy", healthy, "down", down, "restarting", restarting, "degraded", degraded,
			"stopped", stopped, "dead_upstreams", deadUp),
		"services", services, "upstreams", upstreams,
		"top_cgroups", L(om("cgroup", "system.slice/containerd.service", "mem", int64(0.4*GBf), "stat_anon", int64(0.3*GBf), "cpu_pct", 2.1)),
		"series", series, "thresholds", thresholds, "mounts", mountDocs, "verdicts", verdicts)
	if sshAt == nil {
		doc.Set("ssh_error", iif(strings.Contains(flags, "ssh-skipped-db"), "DB host: no login (operator rule)", "no ssh-ro alias"))
		doc.Set("services", L())
		doc.Set("summary", om("text", "no service data (no read-only snapshot)"))
		nSvc, healthy = 0, 0
	}
	g.writeJSON(fmt.Sprintf("vm/%d.json", vid), doc)
	summary := fmt.Sprintf("%d services: %d healthy", nSvc, healthy)
	if restarting > 0 {
		summary += fmt.Sprintf(", %d restarting", restarting)
	}
	if degraded > 0 {
		summary += fmt.Sprintf(", %d degraded", degraded)
	}
	if deadUp > 0 {
		summary += fmt.Sprintf(" · %d dead upstream", deadUp)
	}
	appN := 0
	for _, s := range svcs {
		if _, ok := SERVICES[s]; ok {
			appN++
		}
	}
	g.ins(g.ops, "vm_state", "vm_id", vid, "at", g.t(4*M), "ssh_at", sshAt, "prom_at", g.t(4*M), "cpu", states["cpu"],
		"memory", states["memory"], "io", states["io"], "disk", states["disk"], "network", states["network"], "reasons", jd(reasons),
		"svc_total", nSvc, "svc_app", appN, "svc_healthy", healthy, "svc_degraded", degraded,
		"svc_restarting", restarting, "svc_down", down, "svc_stopped", stopped, "dead_upstreams", deadUp, "summary", summary,
		"ssh_error", doc.Get("ssh_error"))
}

// pmax is Python's max(a, b) for floats: the first argument wins a tie.
func pmax(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

func intList(xs []int) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (g *Gen) vmPulse() {
	pv := om(fmt.Sprint(g.vmIDs["prod-pay-01"]), om("memory", "critical", "reasons", L("memory used 94 % (5 min)"),
		"failed_units", L()))
	g.writeJSON("vm-pulse.json", om("at", g.t(60), "vms", pv))
}
