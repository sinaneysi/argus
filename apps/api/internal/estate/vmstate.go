package estate

// vmstate.go: the VM state (the earlier `vmstate.py all|host|pulse`). For every VM of the inventory: the 24-hour and
// 7-day Prometheus series, the mounts, the systemd unit states, a verdict per resource (ok / pressure / critical with
// the reason) and the upstreams the service map and the live nginx map send to it. Writes vm/<id>.json, the ops.db
// table vm_state and vmstate-status.json; proxy-live.json keeps the live nginx maps that are less than 3 days old.
// The pulse (every 5 minutes) writes vm-pulse.json: the 15-minute pressure of every VM, its failed units and the
// candidates for an automatic analysis.
//
// Not ported (host shell access, ADR-0019 / ADR-0020): the per-host snapshot (unit memory and restarts, listening
// ports, journal errors, cgroups, containers), the live nginx map read on each edge, the unit memory samples and the
// evidence file of the model's VM analysis. Services come from node_systemd_unit_state ("state from Prometheus"),
// and an upstream's "listening" stays unknown.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

const (
	devF  = `device!~"lo|veth.*|docker.*|br-.*|cali.*|flannel.*|cni.*|vxlan.*|virbr.*|tun.*|tap.*"`
	diskF = `device!~"sr.*|loop.*|ram.*|fd.*|nbd.*"`
)

var (
	dbHostRe   = regexp.MustCompile(`(?i)mongo|mysql|maria|redis|postgres|clickhouse|elastic|(^|-)db(-|\d|$)`)
	instanceRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,120}$`)
	unitPfxRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	infraRe    = regexp.MustCompile(`(?i)^(nginx|openresty|docker|containerd|haproxy|keepalived|redis|mongod|mysql|mariadb|postgres|` +
		`php[0-9.]*-fpm|rabbitmq|elasticsearch|logstash|kibana|minio|gitlab|kafka|zookeeper|clickhouse|` +
		`memcached|varnish|squid|named|bind9|pgbouncer|apache2|httpd|tomcat|supervisor|pm2|epmd|` +
		`grafana|prometheus|loki|alertmanager|matomo|n8n|coder|jenkins)`)
	agentRe = regexp.MustCompile(`(?i)^(node[_-]exporter|prometheus-node-exporter|filebeat|metricbeat|auditbeat|promtail|zabbix|atop|` +
		`fail2ban|telegraf|vector|fluent|wazuh|osquery|falcon|cadvisor|process-exporter|nginx-exporter|` +
		`blackbox|snmp|smart|netdata|qualys|tenable|chrony|ntp)`)
	targetRe  = regexp.MustCompile(`^(\d{1,3}(?:\.\d{1,3}){3}):(\d{1,5})$`)
	digitsRe  = regexp.MustCompile(`\d+`)
	portsRe   = regexp.MustCompile(`\(:([\d, :]+)\)`)
	resources = []string{"cpu", "memory", "io", "disk", "network"}
	rank      = map[string]int{"ok": 0, "unknown": 0, "pressure": 1, "critical": 2}
	// the series keys, in the earlier job's order
	seriesKeys = []string{"cpu_busy", "cpu_iowait", "cpu_steal", "load_per_cpu", "psi_cpu", "mem_used_pct", "mem_avail",
		"swap_used", "majfault", "psi_mem", "psi_mem_full", "oom", "psi_io", "psi_io_full", "disk_util", "disk_lat",
		"net_rx", "net_tx", "net_err", "conntrack_pct", "fd_pct", "tcp_estab"}
	windows = []struct {
		name        string
		since, step int64
		rate        string
	}{{"24h", 86400, 900, "15m"}, {"7d", 604800, 7200, "15m"}}
)

// threshold: resource, key, label, warn, crit, op ("<" lower is worse). warn and crit keep the earlier job's types.
type threshold struct {
	res, key, label string
	warn, crit      any
	op              string
}

var thresholds = []threshold{
	{"cpu", "cpu_busy", "CPU busy", int64(75), int64(90), ">"}, {"cpu", "psi_cpu", "tasks waiting for CPU (PSI)", int64(20), int64(50), ">"},
	{"cpu", "load_per_cpu", "load per vCPU", 1.2, 2.0, ">"}, {"cpu", "cpu_steal", "CPU steal (hypervisor)", int64(5), int64(10), ">"},
	{"memory", "mem_avail_pct", "memory available", int64(10), int64(5), "<"},
	{"memory", "psi_mem", "tasks waiting for memory (PSI)", int64(2), int64(10), ">"},
	{"memory", "psi_mem_full", "all tasks stalled on memory (PSI full)", int64(1), int64(5), ">"},
	{"memory", "majfault", "major page faults/s", int64(100), int64(500), ">"}, {"memory", "swap_used_pct", "swap used", int64(50), int64(80), ">"},
	{"memory", "oom", "OOM kills", int64(1), int64(1), ">="},
	{"io", "cpu_iowait", "CPU iowait", int64(10), int64(20), ">"}, {"io", "psi_io", "tasks waiting for IO (PSI)", int64(15), int64(40), ">"},
	{"io", "disk_util", "busiest disk busy", int64(70), int64(90), ">"}, {"io", "disk_lat", "disk latency per IO", int64(30), int64(100), ">"},
	{"disk", "disk_used_max", "fullest filesystem used", int64(85), int64(95), ">"},
	{"disk", "inodes_used_max", "inodes used", int64(85), int64(95), ">"},
	{"network", "net_err", "network errors+drops/s", int64(10), int64(100), ">"},
	{"network", "conntrack_pct", "conntrack table", int64(75), int64(90), ">"}, {"network", "fd_pct", "file handles", int64(75), int64(90), ">"},
}

// seriesThresholds are the thresholds per series for the page's charts.
func seriesThresholds() *py.OMap {
	out := py.NewOMap()
	in := map[string]bool{}
	for _, k := range seriesKeys {
		in[k] = true
	}
	for _, t := range thresholds {
		if t.op == ">" && in[t.key] {
			out.Set(t.key, []any{t.warn, t.crit})
		}
	}
	out.Set("mem_used_pct", []any{int64(90), int64(95)})
	return out
}

func (o *Options) appUnits() []string {
	var out []string
	for _, p := range o.AppUnits {
		if unitPfxRe.MatchString(p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{"app-"}
	}
	return out
}

func (o *Options) appRe() *regexp.Regexp {
	var q []string
	for _, p := range o.appUnits() {
		q = append(q, regexp.QuoteMeta(p))
	}
	return regexp.MustCompile("^(" + strings.Join(q, "|") + ")")
}

func (o *Options) unitNames() string {
	return `name=~"(` + strings.Join(o.appUnits(), "|") + `).*|nginx.service|docker.service|containerd.service|haproxy.service"`
}

// rangeQueries are the PromQL of every series; sel is "" (every VM) or `instance="x",`; w the rate window.
func rangeQueries(sel, w string) [][2]string {
	s, r := sel, strings.TrimRight(sel, ",")
	return [][2]string{
		{"cpu_busy", `100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{` + s + `mode="idle"}[` + w + `])))`},
		{"cpu_iowait", `100 * avg by (instance) (rate(node_cpu_seconds_total{` + s + `mode="iowait"}[` + w + `]))`},
		{"cpu_steal", `100 * avg by (instance) (rate(node_cpu_seconds_total{` + s + `mode="steal"}[` + w + `]))`},
		{"load_per_cpu", `max by (instance) (avg_over_time(node_load1{` + r + `}[` + w + `])) / count by (instance) (node_cpu_seconds_total{` + s + `mode="idle"})`},
		{"psi_cpu", `100 * max by (instance) (rate(node_pressure_cpu_waiting_seconds_total{` + r + `}[` + w + `]))`},
		{"mem_used_pct", `100 * (1 - max by (instance) (node_memory_MemAvailable_bytes{` + r + `}) / max by (instance) (node_memory_MemTotal_bytes{` + r + `}))`},
		{"mem_avail", `max by (instance) (node_memory_MemAvailable_bytes{` + r + `})`},
		{"swap_used", `max by (instance) (node_memory_SwapTotal_bytes{` + r + `} - node_memory_SwapFree_bytes{` + r + `})`},
		{"majfault", `max by (instance) (rate(node_vmstat_pgmajfault{` + r + `}[` + w + `]))`},
		{"psi_mem", `100 * max by (instance) (rate(node_pressure_memory_waiting_seconds_total{` + r + `}[` + w + `]))`},
		{"psi_mem_full", `100 * max by (instance) (rate(node_pressure_memory_stalled_seconds_total{` + r + `}[` + w + `]))`},
		{"oom", `max by (instance) (increase(node_vmstat_oom_kill{` + r + `}[` + w + `]))`},
		{"psi_io", `100 * max by (instance) (rate(node_pressure_io_waiting_seconds_total{` + r + `}[` + w + `]))`},
		{"psi_io_full", `100 * max by (instance) (rate(node_pressure_io_stalled_seconds_total{` + r + `}[` + w + `]))`},
		{"disk_util", `100 * max by (instance) (rate(node_disk_io_time_seconds_total{` + s + diskF + `}[` + w + `]))`},
		{"disk_lat", `1000 * max by (instance) ((rate(node_disk_read_time_seconds_total{` + s + diskF + `}[` + w + `]) + ` +
			`rate(node_disk_write_time_seconds_total{` + s + diskF + `}[` + w + `])) / ` +
			`((rate(node_disk_reads_completed_total{` + s + diskF + `}[` + w + `]) + ` +
			`rate(node_disk_writes_completed_total{` + s + diskF + `}[` + w + `])) > 0.5))`},
		{"net_rx", `sum by (instance) (rate(node_network_receive_bytes_total{` + s + devF + `}[` + w + `]))`},
		{"net_tx", `sum by (instance) (rate(node_network_transmit_bytes_total{` + s + devF + `}[` + w + `]))`},
		{"net_err", `sum by (instance) (rate({__name__=~"node_network_(receive|transmit)_(errs|drop)_total",` + s + devF + `}[` + w + `]))`},
		{"conntrack_pct", `100 * max by (instance) (node_nf_conntrack_entries{` + r + `} / node_nf_conntrack_entries_limit{` + r + `})`},
		{"fd_pct", `100 * max by (instance) (node_filefd_allocated{` + r + `} / node_filefd_maximum{` + r + `})`},
		{"tcp_estab", `max by (instance) (node_netstat_Tcp_CurrEstab{` + r + `})`},
	}
}

func (o *Options) instantQueries(sel string) [][2]string {
	s, r := sel, strings.TrimRight(sel, ",")
	return [][2]string{
		{"fs_size", "node_filesystem_size_bytes{" + s + fsFilter + "}"},
		{"fs_avail", "node_filesystem_avail_bytes{" + s + fsFilter + "}"},
		{"fs_files", "node_filesystem_files{" + s + fsFilter + "}"},
		{"fs_ffree", "node_filesystem_files_free{" + s + fsFilter + "}"},
		{"units", `node_systemd_unit_state{` + s + o.unitNames() + `,state=~"active|failed|activating|inactive"} == 1`},
		{"mem_total", "node_memory_MemTotal_bytes{" + r + "}"},
		{"swap_total", "node_memory_SwapTotal_bytes{" + r + "}"},
		{"vcpu", `count by (instance) (node_cpu_seconds_total{` + s + `mode="idle"})`},
	}
}

func selector(instance string) (string, bool) {
	if instance == "" || !instanceRe.MatchString(instance) {
		return "", false
	}
	return `instance="` + instance + `",`, true
}

func omap(m *py.OMap, k string) *py.OMap {
	if x := m.Map(k); x != nil {
		return x
	}
	x := py.NewOMap()
	m.Set(k, x)
	return x
}

// promRanges: {instance: {key: {"24h": [[t, v]], "7d": [...]}}} of one site.
func (o *Options) promRanges(ctx context.Context, site *Site, sel string) *py.OMap {
	out := py.NewOMap()
	end := o.now()
	for _, w := range windows {
		for _, q := range rangeQueries(sel, w.rate) {
			for _, r := range o.Client.Range(ctx, site, q[1], end, w.since, w.step) {
				inst := r.Metric["instance"]
				if inst == "" {
					continue
				}
				pts := []any{}
				for _, p := range r.Values {
					v, ok := num(p[1])
					if !ok {
						continue
					}
					t, _ := num(p[0])
					pts = append(pts, []any{int64(t), round(v, 4)})
				}
				omap(omap(out, inst), q[0]).Set(w.name, pts)
			}
		}
	}
	return out
}

// promInstant: {instance: {"mounts": {mp: {size, avail, files, ffree}}, "units": {name: state}, mem_total, ...}}.
func (o *Options) promInstant(ctx context.Context, site *Site, sel string) *py.OMap {
	res := map[string][]Sample{}
	for _, q := range o.instantQueries(sel) {
		res[q[0]] = o.Client.Instant(ctx, site, q[1])
	}
	by := py.NewOMap()
	for _, k := range []string{"fs_size", "fs_avail", "fs_files", "fs_ffree"} {
		for _, r := range res[k] {
			inst, mp := r.Metric["instance"], r.Metric["mountpoint"]
			if inst != "" && mp != "" {
				omap(omap(omap(by, inst), "mounts"), mp).Set(k[3:], fnum(r.Value))
			}
		}
	}
	for _, r := range res["units"] {
		if inst, name := r.Metric["instance"], r.Metric["name"]; inst != "" && name != "" {
			omap(omap(by, inst), "units").Set(name, lv(r.Metric, "state"))
		}
	}
	for _, k := range []string{"mem_total", "swap_total", "vcpu"} {
		for _, r := range res[k] {
			if inst := r.Metric["instance"]; inst != "" {
				omap(by, inst).Set(k, fnum(r.Value))
			}
		}
	}
	return by
}

// ---- verdicts ----

func last(series *py.OMap) any {
	pts := py.AsList(series.Get("24h"))
	if len(pts) == 0 {
		return nil
	}
	return py.AsList(pts[len(pts)-1])[1]
}

func fmtVal(key string, v any) string {
	if v == nil {
		return "-"
	}
	f, _ := num(v)
	switch key {
	case "load_per_cpu":
		return fmtF(f, 2)
	case "majfault", "net_err":
		return fmtF(f, 0) + "/s"
	case "disk_lat":
		return fmtF(f, 0) + " ms"
	case "oom":
		return fmtF(f, 0)
	}
	return fmtF(f, 0) + " %"
}

// nowValues are the current values for the thresholds: the last 15-minute average of each series.
func nowValues(prom, inst *py.OMap, mounts []*py.OMap) *py.OMap {
	v := py.NewOMap()
	for _, k := range seriesKeys {
		var s *py.OMap
		if prom != nil {
			s = prom.Map(k)
		}
		if s == nil {
			v.Set(k, nil)
		} else {
			v.Set(k, last(s))
		}
	}
	v.Set("oom", nil)
	if prom != nil {
		if pts := py.AsList(py.AsMap(prom.Get("oom")).Get("24h")); len(pts) > 0 {
			sum := 0.0
			for _, p := range pts {
				f, _ := num(py.AsList(p)[1])
				sum += f
			}
			v.Set("oom", sum)
		}
	}
	if inst != nil {
		if mt, ok := num(inst.Get("mem_total")); ok && mt != 0 {
			if ma, ok := num(v.Get("mem_avail")); ok {
				v.Set("mem_avail_pct", 100.0*ma/mt)
			}
		}
		if st, ok := num(inst.Get("swap_total")); ok && st != 0 {
			if su, ok := num(v.Get("swap_used")); ok {
				v.Set("swap_used_pct", 100.0*su/st)
			}
		}
	}
	maxOf := func(sizeKey, freeKey string) any {
		var best any
		for _, m := range mounts {
			size, ok := num(m.Get(sizeKey))
			if !ok || size == 0 {
				continue
			}
			free, _ := num(m.Get(freeKey))
			u := 100.0 * (1 - free/size)
			if bf, ok := best.(float64); !ok || u > bf {
				best = u
			}
		}
		return best
	}
	v.Set("disk_used_max", maxOf("size", "avail"))
	v.Set("inodes_used_max", maxOf("files", "ffree"))
	return v
}

// verdicts: {resource: {"state": ok|pressure|critical|unknown, "reasons": [...]}} from current values.
func verdicts(vals *py.OMap, oomLabel string) *py.OMap {
	res := py.NewOMap()
	for _, r := range resources {
		res.Set(r, py.NewOMap("state", "unknown", "reasons", []any{}))
	}
	for _, t := range thresholds {
		v, ok := num(vals.Get(t.key))
		if !ok {
			continue
		}
		if t.key == "disk_lat" {
			pi, _ := num(vals.Get("psi_io"))
			du, _ := num(vals.Get("disk_util"))
			if pi < 5 && du < 30 {
				continue
			}
		}
		rv := res.Map(t.res)
		if rv.S("state") == "unknown" {
			rv.Set("state", "ok")
		}
		w, _ := num(t.warn)
		c, _ := num(t.crit)
		lvl := ""
		switch t.op {
		case "<":
			if v < c {
				lvl = "critical"
			} else if v < w {
				lvl = "pressure"
			}
		case ">=":
			if v >= w {
				lvl = "pressure"
			}
		default:
			if v >= c {
				lvl = "critical"
			} else if v >= w {
				lvl = "pressure"
			}
		}
		if lvl == "" {
			continue
		}
		txt := t.label + " " + fmtVal(t.key, v)
		if t.key == "oom" {
			txt += " " + oomLabel
		}
		cmp, at := "≥", t.warn
		if t.op == "<" {
			cmp = "<"
		}
		if lvl == "critical" {
			at = t.crit
		}
		txt += " (" + lvl + " at " + cmp + " " + fmtVal(t.key, at) + ")"
		rv.Set("reasons", append(rv.List("reasons"), txt))
		if rank[lvl] > rank[rv.S("state")] {
			rv.Set("state", lvl)
		}
	}
	return res
}

// ---- services and upstreams ----

func (o *Options) kindOf(unit string) string {
	n := strings.ReplaceAll(unit, ".service", "")
	switch {
	case o.appRe().MatchString(n):
		return "app"
	case agentRe.MatchString(n):
		return "agent"
	case infraRe.MatchString(n):
		return "infra"
	}
	return "other"
}

// promServices is the service list from node_systemd_unit_state (state only).
func (o *Options) promServices(units *py.OMap) []any {
	out := []any{}
	if units == nil {
		return out
	}
	names := append([]string(nil), units.Keys()...)
	sort.Strings(names)
	for _, name := range names {
		state := str(units.Get(name))
		verdict := map[string]string{"active": "healthy", "failed": "down", "activating": "restarting", "inactive": "stopped"}[state]
		if verdict == "" {
			verdict = "degraded"
		}
		why := []any{}
		if verdict != "healthy" {
			why = []any{"from Prometheus (node_systemd_unit_state); no ssh-ro detail"}
		}
		out = append(out, py.NewOMap("unit", name, "kind", o.kindOf(name), "verdict", verdict, "active", units.Get(name), "sub", "",
			"why", why, "ports", []any{}, "domains", []any{}, "dead_ports", []any{}))
	}
	return out
}

// refList is an ordered {"ip:port": [ref]}.
type refList struct {
	keys []string
	m    map[string][]*py.OMap
}

func newRefs() *refList { return &refList{m: map[string][]*py.OMap{}} }

func (r *refList) add(addr string, ref *py.OMap) {
	if _, ok := r.m[addr]; !ok {
		r.keys = append(r.keys, addr)
	}
	r.m[addr] = append(r.m[addr], ref)
}

// proxyTargets: the live nginx map of one edge -> {"ip:port": [ref]}; 127.x means the edge itself.
func proxyTargets(edgeName any, edgeIPs []any, targets []any) *refList {
	out := newRefs()
	for _, t := range targets {
		tm := py.AsMap(t)
		if tm == nil {
			continue
		}
		m := targetRe.FindStringSubmatch(str(or(tm.Get("target"), "")))
		if m == nil {
			continue
		}
		local := strings.HasPrefix(m[1], "127.")
		ips := []any{m[1]}
		if local {
			ips = edgeIPs
		}
		var srv []string
		for _, s := range py.AsList(tm.Get("server")) {
			srv = append(srv, str(s))
		}
		dom := cut(strings.Join(srv, ","), 100)
		if dom == "" {
			dom = "-"
		}
		for _, ip := range ips {
			out.add(str(ip)+":"+m[2], py.NewOMap("domain", dom, "location", cut(str(or(tm.Get("loc"), "")), 100),
				"upstream", cut(str(or(tm.Get("upstream"), "")), 80), "source", "live nginx on "+str(edgeName), "local", local))
		}
	}
	return out
}

// upstreamRefs: the service map's backends and the live nginx maps. A tree whose edges were read live is superseded by
// the live config: its routes that are only in the repo are kept as "repo only".
func upstreamRefs(smap *py.OMap, live *refList, liveIPs map[string]bool) *refList {
	refs := newRefs()
	liveTrees := map[string]bool{}
	if trees := smap.Map("trees"); trees != nil {
		for _, t := range trees.Keys() {
			for _, e := range py.AsList(py.AsMap(trees.Get(t)).Get("edges")) {
				if ip := py.AsMap(e).Get("ip"); ip != nil && liveIPs[str(ip)] {
					liveTrees[t] = true
				}
			}
		}
	}
	liveKeys := map[[3]string]bool{}
	for _, a := range live.keys {
		for _, u := range live.m[a] {
			liveKeys[[3]string{a, str(u.Get("domain")), str(u.Get("location"))}] = true
		}
	}
	if bk := smap.Map("backends"); bk != nil {
		for _, addr := range bk.Keys() {
			for _, u := range py.AsList(bk.Get(addr)) {
				um := py.AsMap(u)
				if um == nil {
					continue
				}
				ref := py.NewOMap("domain", cut(str(or(um.Get("domain"), "")), 100), "location", cut(str(or(um.Get("location"), "")), 100),
					"upstream", cut(str(or(um.Get("upstream"), "")), 80), "source", "service map ("+cut(str(or(um.Get("tree"), "repo")), 40)+")")
				if tr, ok := um.Get("tree").(string); ok && liveTrees[tr] && !liveKeys[[3]string{addr, ref.S("domain"), ref.S("location")}] {
					ref.Set("repo_only", true)
					ref.Set("source", ref.S("source")+", not in the live nginx config")
				}
				refs.add(addr, ref)
			}
		}
	}
	for _, addr := range live.keys {
		for _, u := range live.m[addr] {
			dup := false
			for _, r := range refs.m[addr] {
				if r.S("domain") == u.S("domain") && r.S("location") == u.S("location") {
					dup = true
				}
			}
			if !dup {
				refs.add(addr, u)
			}
		}
	}
	return refs
}

// upstreams are the ports of this VM that the references send traffic to (listening unknown without a snapshot).
func upstreams(ips []string, refs *refList) []any {
	isIP := map[string]bool{}
	for _, ip := range ips {
		isIP[ip] = true
	}
	ups := map[int][]*py.OMap{}
	for _, addr := range refs.keys {
		i := strings.LastIndex(addr, ":")
		ip, port := "", addr
		if i >= 0 {
			ip, port = addr[:i], addr[i+1:]
		}
		p, err := strconv.Atoi(port)
		if !isIP[ip] || port == "" || err != nil || strings.Trim(port, "0123456789") != "" {
			continue
		}
		ups[p] = append(ups[p], refs.m[addr]...)
	}
	ports := make([]int, 0, len(ups))
	for p := range ups {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	out := []any{}
	for _, p := range ports {
		seen := map[[2]string]bool{}
		var uniq, live, repo []any
		for _, r := range ups[p] {
			k := [2]string{str(r.Get("domain")), str(r.Get("location"))}
			if seen[k] {
				continue
			}
			seen[k] = true
			uniq = append(uniq, r)
			if truthy(r.Get("repo_only")) {
				repo = append(repo, r)
			} else {
				live = append(live, r)
			}
		}
		all := append(append([]any{}, live...), repo...)
		if len(all) > 12 {
			all = all[:12]
		}
		out = append(out, py.NewOMap("port", int64(p), "listening", nil, "unit", "", "refs", all, "n_refs", int64(len(uniq)),
			"repo_only", len(live) == 0))
	}
	return out
}

func summaryText(s *py.OMap) string {
	if !truthy(s.Get("services")) && !truthy(s.Get("upstreams")) {
		return "no service data"
	}
	parts := []string{str(s.Get("healthy")) + " healthy"}
	for _, k := range []string{"degraded", "restarting", "down", "stopped"} {
		if truthy(s.Get(k)) {
			parts = append(parts, str(s.Get(k))+" "+k)
		}
	}
	return str(s.Get("services")) + " services: " + strings.Join(parts, ", ")
}

// ---- one VM ----

type vmRow map[string]any

func (v vmRow) s(k string) string {
	switch x := v[k].(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

func (o *Options) build(vm vmRow, prom, inst *py.OMap, sshErr string, refs *refList, capacity []*py.OMap, now int64) *py.OMap {
	var ips []string
	for _, x := range strings.Split(vm.s("ips"), ",") {
		if x != "" {
			ips = append(ips, x)
		}
	}
	var mounts []*py.OMap
	if inst != nil {
		if mm := inst.Map("mounts"); mm != nil {
			mps := append([]string(nil), mm.Keys()...)
			sort.Strings(mps)
			for _, mp := range mps {
				m := mm.Map(mp)
				if truthy(m.Get("size")) {
					mounts = append(mounts, py.NewOMap("mount", mp, "size", m.Get("size"), "avail", m.Get("avail"), "files", m.Get("files"), "ffree", m.Get("ffree")))
				}
			}
		}
	}
	if len(mounts) == 0 {
		if v, err := py.ParseJSON([]byte(or(vm.s("mounts"), "[]").(string))); err == nil {
			for _, x := range py.AsList(v) {
				m := py.AsMap(x)
				if m == nil || !m.Has("mount") {
					mounts = nil
					break
				}
				mounts = append(mounts, py.NewOMap("mount", m.Get("mount"), "size", m.Get("size"),
					"avail", numOr0(or(m.Get("size"), int64(0)))-numOr0(or(m.Get("used"), int64(0)))))
			}
		}
	}
	for _, m := range mounts {
		for _, c := range capacity {
			if strings.EqualFold(str(or(c.Get("host"), "")), vm.s("name")) && str(c.Get("mount")) == str(m.Get("mount")) {
				m.Set("days_to_full", c.Get("days"))
				m.Set("loss_per_day", c.Get("loss_per_day"))
				break
			}
		}
	}
	vals := nowValues(prom, inst, mounts)
	ver := verdicts(vals, "in 24 h")
	for _, m := range mounts {
		d, isNum := m.Get("days_to_full"), false
		switch d.(type) {
		case int64, float64:
			isNum = true
		}
		if f, _ := num(d); isNum && f < 14 {
			lvl := "pressure"
			if f < 7 {
				lvl = "critical"
			}
			dv := ver.Map("disk")
			dv.Set("reasons", append(dv.List("reasons"), str(m.Get("mount"))+" full in "+strconv.FormatFloat(f, 'g', 6, 64)+" days (forecast)"))
			if rank[lvl] > rank[dv.S("state")] {
				dv.Set("state", lvl)
			}
		}
	}
	var units *py.OMap
	if inst != nil {
		units = inst.Map("units")
	}
	services := o.promServices(units)
	ups := upstreams(ips, refs)
	summ := py.NewOMap()
	cnt := func(f func(*py.OMap) bool) int64 {
		n := int64(0)
		for _, s := range services {
			if sm := s.(*py.OMap); sm.S("kind") != "agent" && f(sm) {
				n++
			}
		}
		return n
	}
	verd := func(x string) func(*py.OMap) bool { return func(s *py.OMap) bool { return s.S("verdict") == x } }
	source := ""
	if len(services) > 0 {
		source = "prometheus"
	}
	summ.Set("services", cnt(func(*py.OMap) bool { return true })).Set("app", cnt(func(s *py.OMap) bool { return s.S("kind") == "app" })).
		Set("healthy", cnt(verd("healthy"))).Set("degraded", int64(0)).Set("restarting", cnt(verd("restarting"))).
		Set("down", cnt(verd("down"))).Set("stopped", cnt(verd("stopped"))).Set("upstreams", int64(len(ups))).
		Set("dead_upstreams", int64(0)).Set("dead_ports", []any{}).Set("source", source)
	if len(services) > 0 {
		summ.Set("text", summaryText(summ)+" (state from Prometheus)")
	} else {
		summ.Set("text", "no service data (no ssh-ro access)")
	}
	nowOut := py.NewOMap()
	for _, k := range vals.Keys() {
		x := vals.Get(k)
		if f, ok := x.(float64); ok {
			x = round(f, 3)
		}
		nowOut.Set(k, x)
	}
	var promAt any
	series := py.NewOMap()
	if prom != nil && prom.Len() > 0 {
		promAt, series = now, prom
	}
	ml := make([]any, len(mounts))
	for i, m := range mounts {
		ml[i] = m
	}
	return py.NewOMap("v", int64(1), "vm_id", vm["id"], "name", vm["name"], "env", vm["env"], "collected_at", now,
		"prom_at", promAt, "ssh_at", nil, "ssh_error", sshErr, "series", series, "now", nowOut, "mounts", ml,
		"verdicts", ver, "services", services, "upstreams", ups, "summary", summ, "top_cgroups", []any{},
		"slices", py.NewOMap(), "host", py.NewOMap(), "hidepid", nil, "ports_note", nil, "journal_note", nil,
		"kernel_ooms_24h", nil, "containers", nil, "containers_exited", nil, "listening", []any{}, "procs_visible", nil,
		"trimmed", nil, "thresholds", seriesThresholds())
}

func readJSONMap(path string) *py.OMap {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := py.ParseJSON(b)
	if err != nil {
		return nil
	}
	return py.AsMap(v)
}

func (o *Options) capacityRows() []*py.OMap {
	var out []*py.OMap
	if doc := readJSONMap(filepath.Join(o.StateDir, "capacity.json")); doc != nil {
		for _, r := range doc.List("filesystems") {
			if m := py.AsMap(r); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func loadVMs(db *sql.DB, vid int64) ([]vmRow, error) {
	q, args := "SELECT * FROM vms WHERE gone=0 ORDER BY id", []any{}
	if vid > 0 {
		q, args = "SELECT * FROM vms WHERE gone=0 AND id=?", []any{vid}
	}
	return queryRows(db, q, args...)
}

func queryRows(db *sql.DB, q string, args ...any) ([]vmRow, error) {
	rs, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []vmRow
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := vmRow{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (o *Options) site(dc string) *Site {
	first, _, _ := strings.Cut(dc, "+")
	for _, s := range o.Sites {
		if s.Name == first {
			return s
		}
	}
	return nil
}

// VMState collects the state of every VM (vid 0) or of one VM.
func (o *Options) VMState(ctx context.Context, vid int64) (*py.OMap, error) {
	if len(o.Sites) == 0 {
		return nil, ErrNoSite
	}
	t0 := o.now()
	now := t0.Unix()
	db, err := openDB(o.DB)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	vms, err := loadVMs(db, vid)
	if err != nil {
		return nil, err
	}
	if len(vms) == 0 {
		if vid > 0 {
			return nil, fmt.Errorf("vmstate: no VM %d", vid)
		}
		return nil, fmt.Errorf("vmstate: inventory is empty")
	}
	prom, inst := map[string]*py.OMap{}, map[string]*py.OMap{}
	var dcs []string
	for _, v := range vms {
		if s := o.site(v.s("dc")); truthy(v["monitored"]) && s != nil && !contains(dcs, s.Name) {
			dcs = append(dcs, s.Name)
		}
	}
	sort.Strings(dcs)
	for _, dc := range dcs {
		sel := ""
		if vid > 0 {
			s, ok := selector(vms[0].s("prom_instance"))
			if !ok {
				continue
			}
			sel = s
		}
		prom[dc] = o.promRanges(ctx, o.site(dc), sel)
		inst[dc] = o.promInstant(ctx, o.site(dc), sel)
	}
	// the live nginx maps the earlier installation read on the edges: kept while they are less than 3 days old
	proxyPath := filepath.Join(o.StateDir, "proxy-live.json")
	edges := py.NewOMap()
	if live := readJSONMap(proxyPath); live != nil && live.Map("edges") != nil {
		edges = live.Map("edges")
	}
	if vid == 0 {
		kept := py.NewOMap()
		for _, k := range edges.Keys() {
			e := py.AsMap(edges.Get(k))
			if at, _ := num(or(e.Get("at"), int64(0))); float64(now)-at < 3*86400 {
				kept.Set(k, edges.Get(k))
			}
		}
		edges = kept
	}
	if err := writeAtomic(proxyPath, []byte(py.DumpsCompact(py.NewOMap("at", now, "edges", edges), true))); err != nil {
		return nil, err
	}
	live := newRefs()
	liveIPs := map[string]bool{}
	for _, k := range edges.Keys() {
		e := py.AsMap(edges.Get(k))
		pt := proxyTargets(e.Get("name"), py.AsList(e.Get("ips")), py.AsList(e.Get("targets")))
		for _, a := range pt.keys {
			for _, r := range pt.m[a] {
				live.add(a, r)
			}
		}
		for _, ip := range py.AsList(e.Get("ips")) {
			liveIPs[str(ip)] = true
		}
	}
	smap := readJSONMap(o.serviceMap())
	if smap == nil {
		smap = py.NewOMap()
	}
	refs := upstreamRefs(smap, live, liveIPs)
	capacity := o.capacityRows()
	var nVMs, critical int64
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, v := range vms {
		var p, in *py.OMap
		if s := o.site(v.s("dc")); s != nil {
			i := v.s("prom_instance")
			if prom[s.Name] != nil {
				p = prom[s.Name].Map(i)
			}
			if inst[s.Name] != nil {
				in = inst[s.Name].Map(i)
			}
		}
		sshErr := ""
		if v.s("ssh_alias") != "" {
			if !canSSH(v) {
				sshErr = "skipped: DB host rule"
			} else {
				sshErr = "not run: host access through the access broker is not ported yet"
			}
		}
		doc := o.build(v, p, in, sshErr, refs, capacity, now)
		id, _ := v["id"].(int64)
		if err := writeAtomic(filepath.Join(o.StateDir, "vm", strconv.FormatInt(id, 10)+".json"), []byte(py.DumpsCompact(doc, true))); err != nil {
			return nil, err
		}
		if err := storeState(tx, id, doc); err != nil {
			return nil, err
		}
		nVMs++
		for _, r := range resources {
			if doc.Map("verdicts").Map(r).S("state") == "critical" {
				critical++
				break
			}
		}
	}
	if _, err := tx.Exec("DELETE FROM vm_unit_samples WHERE at < ?", now-60*86400); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	status := "OK"
	if nVMs == 0 {
		status = "WARN"
	}
	scope := "all"
	if vid > 0 {
		scope = "one"
	}
	st := py.NewOMap("vms", nVMs, "ssh_ok", int64(0), "ssh_failed", int64(0), "dead_upstreams", int64(0), "critical", critical,
		"at", now, "seconds", int64(o.now().Sub(t0).Seconds()), "status", status, "scope", scope)
	if vid == 0 {
		if err := writeAtomic(filepath.Join(o.StateDir, "vmstate-status.json"), []byte(py.DumpsCompact(st, true))); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func canSSH(v vmRow) bool {
	return v.s("ssh_alias") != "" && !dbHostRe.MatchString(v.s("ssh_alias")) && !dbHostRe.MatchString(v.s("name")) &&
		!strings.Contains(v.s("flags"), "ssh-skipped-db")
}

func storeState(tx *sql.Tx, vid int64, doc *py.OMap) error {
	v, s := doc.Map("verdicts"), doc.Map("summary")
	reasons := py.NewOMap()
	states := make([]any, 0, len(resources))
	for _, r := range resources {
		rv := v.Map(r)
		reasons.Set(r, or(rv.List("reasons"), []any{}))
		states = append(states, or(rv.Get("state"), "unknown"))
	}
	args := []any{vid, doc.Get("collected_at"), doc.Get("ssh_at"), doc.Get("prom_at")}
	args = append(args, states...)
	args = append(args, py.Dumps(reasons, -1), s.Get("services"), s.Get("app"), s.Get("healthy"), s.Get("degraded"),
		s.Get("restarting"), s.Get("down"), s.Get("stopped"), s.Get("dead_upstreams"), s.Get("text"), or(doc.Get("ssh_error"), ""))
	_, err := tx.Exec("INSERT OR REPLACE INTO vm_state VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", args...)
	return err
}

// ---- pulse ----

var pulseQueries = [][2]string{
	{"cpu_busy", `100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[15m])))`},
	{"cpu_iowait", `100 * avg by (instance) (rate(node_cpu_seconds_total{mode="iowait"}[15m]))`},
	{"psi_cpu", `100 * max by (instance) (rate(node_pressure_cpu_waiting_seconds_total[15m]))`},
	{"psi_mem", `100 * max by (instance) (rate(node_pressure_memory_waiting_seconds_total[15m]))`},
	{"psi_mem_full", `100 * max by (instance) (rate(node_pressure_memory_stalled_seconds_total[15m]))`},
	{"psi_io", `100 * max by (instance) (rate(node_pressure_io_waiting_seconds_total[15m]))`},
	{"mem_avail_pct", `100 * max by (instance) (max_over_time((node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)[15m:1m]))`},
	{"oom", `max by (instance) (increase(node_vmstat_oom_kill[1h]))`},
	{"disk_used_max", `100 * max by (instance) (1 - node_filesystem_avail_bytes{` + fsFilter + `} / node_filesystem_size_bytes{` + fsFilter + `})`},
	{"load_per_cpu", `max by (instance) (avg_over_time(node_load1[15m])) / count by (instance) (node_cpu_seconds_total{mode="idle"})`},
}

// Pulse writes vm-pulse.json: the 15-minute pressure of every VM, its failed units and the automatic analysis
// candidates (a signature without numbers, so the same problem is not analysed again for a week).
func (o *Options) Pulse(ctx context.Context) (*py.OMap, error) {
	if len(o.Sites) == 0 {
		return nil, ErrNoSite
	}
	now := o.now().Unix()
	db, err := openDB(o.DB)
	if err != nil {
		return nil, err
	}
	vms, err := loadVMs(db, 0)
	if err != nil {
		db.Close()
		return nil, err
	}
	states, err := queryRows(db, "SELECT * FROM vm_state")
	db.Close()
	if err != nil {
		return nil, err
	}
	state := map[int64]vmRow{}
	for _, r := range states {
		id, _ := r["vm_id"].(int64)
		state[id] = r
	}
	type key struct{ dc, inst string }
	vals := map[key]*py.OMap{}
	failed := map[key][]string{}
	failedQ := `node_systemd_unit_state{state="failed",` + o.unitNames() + `} == 1`
	for _, site := range o.Sites {
		for _, q := range pulseQueries {
			for _, r := range o.Client.Instant(ctx, site, q[1]) {
				if i := r.Metric["instance"]; i != "" {
					k := key{site.Name, i}
					if vals[k] == nil {
						vals[k] = py.NewOMap()
					}
					vals[k].Set(q[0], fnum(r.Value))
				}
			}
		}
		for _, r := range o.Client.Instant(ctx, site, failedQ) {
			if i, n := r.Metric["instance"], r.Metric["name"]; i != "" && n != "" {
				k := key{site.Name, i}
				failed[k] = append(failed[k], cut(n, 100))
			}
		}
	}
	out := py.NewOMap()
	type cand struct {
		m     *py.OMap
		level int64
	}
	var auto []cand
	for _, v := range vms {
		k := key{"", v.s("prom_instance")}
		if s := o.site(v.s("dc")); s != nil {
			k.dc = s.Name
		}
		id, _ := v["id"].(int64)
		st := state[id]
		fu := append([]string(nil), failed[k]...)
		sort.Strings(fu)
		if len(fu) > 20 {
			fu = fu[:20]
		}
		val := vals[k]
		if k.dc == "" {
			val = nil
		}
		var ver *py.OMap
		if val != nil && val.Len() > 0 {
			ver = verdicts(val, "in the last hour")
		}
		row := py.NewOMap()
		reasons := []any{}
		for _, r := range resources {
			if r == "network" {
				continue
			}
			s := "unknown"
			if ver != nil {
				s = ver.Map(r).S("state")
				reasons = append(reasons, ver.Map(r).List("reasons")...)
			}
			row.Set(r, s)
		}
		if len(reasons) > 8 {
			reasons = reasons[:8]
		}
		ful := make([]any, len(fu))
		for i, x := range fu {
			ful[i] = x
		}
		row.Set("reasons", reasons).Set("failed_units", ful)
		out.Set(strconv.FormatInt(id, 10), row)
		var why []string
		for _, r := range []string{"cpu", "memory", "io"} {
			if row.S(r) == "critical" {
				why = append(why, r+" critical for 15 min")
			}
		}
		if len(fu) > 0 {
			n := fu
			if len(n) > 4 {
				n = n[:4]
			}
			why = append(why, "down: "+strings.Join(n, ", "))
		}
		if truthy(st["svc_down"]) {
			why = append(why, str(st["svc_down"])+" service(s) down")
		}
		if truthy(st["dead_upstreams"]) {
			why = append(why, str(st["dead_upstreams"])+" dead upstream(s)")
		}
		if len(why) > 0 && !dbHostRe.MatchString(v.s("name")) && (v.s("ssh_alias") != "" || truthy(v["monitored"])) {
			sigs := make([]string, len(why))
			for i, w := range why {
				sigs[i] = digitsRe.ReplaceAllString(w, "N")
			}
			sort.Strings(sigs)
			sig := strings.Join(sigs, "|")
			if truthy(st["dead_upstreams"]) {
				var m []string
				if f := portsRe.FindStringSubmatch(st.s("summary")); f != nil {
					m = []string{f[1]}
				}
				sig += "|ports:" + strings.Join(m, ",")
			}
			level := int64(1)
			for _, w := range why {
				if strings.Contains(w, "critical") || strings.Contains(w, "down") {
					level = 2
				}
			}
			auto = append(auto, cand{py.NewOMap("id", id, "why", cut(strings.Join(why, "; "), 300), "sig", cut(sig, 300), "level", level), level})
		}
	}
	sort.SliceStable(auto, func(i, j int) bool { return auto[i].level > auto[j].level })
	al := []any{}
	for i, a := range auto {
		if i >= 50 {
			break
		}
		al = append(al, a.m)
	}
	if err := writeAtomic(filepath.Join(o.StateDir, "vm-pulse.json"), []byte(py.DumpsCompact(py.NewOMap("at", now, "vms", out, "auto", al), true))); err != nil {
		return nil, err
	}
	return py.NewOMap("at", now, "vms", int64(out.Len()), "auto", int64(len(auto))), nil
}
