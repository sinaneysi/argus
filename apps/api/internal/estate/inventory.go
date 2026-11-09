package estate

// inventory.go: the VM inventory (the earlier `inventory.py collect`). Every VM that a Prometheus site sees through
// node_exporter, merged by node name; one ops.db row per VM (vms, with first_seen / last_seen / gone) and its history
// (vm_changes); inventory-status.json for the inventory page's scan bar.
//
// Merge key: the node name in lower case, "_" as "-", without a port and without a leading "<site>-". Environment:
// the series' env label (production, pre-production, stage, ...), else the first ARGUS_INVENTORY_ENV_PREFIXES rule
// that matches an address, else "unknown". Flags: prom-only, down, os-eol, disk-85.
//
// Not ported (host shell access, ADR-0019 / ADR-0020): the read-only SSH facts of each host (virtualisation, Docker,
// time sync, time zone) and the ssh-only VMs. The flags ssh-failed, ssh-skipped-db, not-monitored and no-ntp come
// only from those facts, so they are not set.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
	_ "modernc.org/sqlite"
)

// Options configure the estate jobs.
type Options struct {
	DB          string // ops.db
	StateDir    string // where the state files go (the folder of ops.db for the pages)
	Sites       []*Site
	Client      *Client
	ServiceMap  string      // service-map.json (default StateDir/service-map.json)
	EnvPrefixes [][2]string // {env, address prefix}, first match wins
	AppUnits    []string    // name prefixes of the application units (default "app-")
	Now         func() time.Time
	Log         io.Writer
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) logf(format string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", a...)
	}
}

func (o *Options) serviceMap() string {
	if o.ServiceMap != "" {
		return o.ServiceMap
	}
	return filepath.Join(o.StateDir, "service-map.json")
}

var (
	fsFilter = `fstype!~"tmpfs|overlay|squashfs|nsfs|ramfs|fuse.*|iso9660|devtmpfs|vfat",` +
		`mountpoint!~"/run.*|/boot.*|/snap/.*|/var/lib/docker/.+|/var/lib/kubelet/.+|/var/lib/containers/.+"`
	invQueries = [][2]string{
		{"uname", "last_over_time(node_uname_info[2d])"},
		{"os", "last_over_time(node_os_info[2d])"},
		{"up", "up"},
		{"scrape", "timestamp(up)"},
		{"cpu", `count by (instance, job)(node_cpu_seconds_total{mode="idle"})`},
		{"mem", "node_memory_MemTotal_bytes"},
		{"boot", "node_boot_time_seconds"},
		{"fs_size", "node_filesystem_size_bytes{" + fsFilter + "}"},
		{"fs_avail", "node_filesystem_avail_bytes{" + fsFilter + "}"},
	}
	envLabel = map[string]string{"production": "release", "prod": "release", "pre-production": "pre-release",
		"stage": "develop", "develop": "develop"}
	eolOS = []struct {
		id  string
		ver []string
	}{{"ubuntu", []string{"14.04", "16.04", "18.04", "20.04"}}, {"centos", []string{"6", "7", "8"}}, {"debian", []string{"9", "10"}}}
	tracked  = []string{"os", "kernel", "vcpu", "ram_bytes", "disk_total", "ips", "env", "monitored", "ssh_ok", "virt"}
	vmCols   = []string{"name", "ips", "env", "dc", "os", "os_id", "os_version", "kernel", "arch", "vcpu", "ram_bytes", "disk_total", "disk_used", "mounts", "boot_time", "virt", "hypervisor", "monitored", "up", "last_scrape", "prom_instance", "prom_job", "ssh_alias", "ssh_ok", "docker", "containers", "services_running", "domains", "ntp_sync", "timezone", "flags", "facts"}
	portRe   = regexp.MustCompile(`:\d+$`)
	dashesRe = regexp.MustCompile(`-{2,}`)
	ipInstRe = regexp.MustCompile(`^(\d{1,3}(?:\.\d{1,3}){3})(?::\d+)?$`)
)

// vmRec is one VM while it is being merged: Python-typed values by field, plus its addresses and mounts.
type vmRec struct {
	f      map[string]any
	ips    map[string]bool
	mounts *py.OMap // mountpoint -> {"size", "avail"}
}

func (v *vmRec) get(k string) any { return v.f[k] }

type vmSet struct {
	keys []string
	m    map[string]*vmRec
}

func (s *vmSet) add(key, name string) *vmRec {
	if v, ok := s.m[key]; ok {
		return v
	}
	v := &vmRec{f: map[string]any{"key": key, "name": name}, ips: map[string]bool{}, mounts: py.NewOMap()}
	s.m[key] = v
	s.keys = append(s.keys, key)
	return v
}

// norm is the merge key of a node name.
func norm(name string, sites []*Site) string {
	k := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(portRe.ReplaceAllString(name, ""))), "_", "-")
	for _, s := range sites {
		p := strings.ToLower(s.Name) + "-"
		if strings.HasPrefix(k, p) && len(k) > len(p)+2 {
			k = k[len(p):]
		}
	}
	return dashesRe.ReplaceAllString(k, "-")
}

func ipOfInstance(inst string) string {
	if m := ipInstRe.FindStringSubmatch(inst); m != nil {
		return m[1]
	}
	return ""
}

func (o *Options) envOfIP(ip string) string {
	if ip == "" {
		return ""
	}
	for _, r := range o.EnvPrefixes {
		if strings.HasPrefix(ip, r[1]) {
			return r[0]
		}
	}
	return ""
}

func target(m map[string]string) [2]string { return [2]string{m["instance"], m["job"]} }

func lv(m map[string]string, k string) any {
	if s, ok := m[k]; ok {
		return s
	}
	return nil
}

// collectProm reads every site's node_exporter series.
func (o *Options) collectProm(ctx context.Context) *vmSet {
	vms := &vmSet{m: map[string]*vmRec{}}
	for _, site := range o.Sites {
		dc := site.Name
		res := map[string][]Sample{}
		for _, q := range invQueries {
			res[q[0]] = o.Client.Instant(ctx, site, q[1])
		}
		byTarget := map[[2]string]string{}
		for _, r := range res["uname"] {
			m := r.Metric
			nm := or(lv(m, "nodename"), lv(m, "instance"))
			if nm == nil {
				continue
			}
			key := norm(str(nm), o.Sites)
			if key == "" {
				continue
			}
			byTarget[target(m)] = key
			v := vms.add(key, str(nm))
			if truthy(v.get("dc")) && v.get("dc") != dc {
				v.f["also_dc"] = dc
			} else {
				v.f["dc"], v.f["prom_instance"], v.f["prom_job"] = dc, lv(m, "instance"), lv(m, "job")
			}
			v.f["kernel"] = or(lv(m, "release"), v.get("kernel"))
			v.f["arch"] = or(lv(m, "machine"), v.get("arch"))
			v.f["monitored"] = int64(1)
			v.f["env_label"] = or(v.get("env_label"), lv(m, "env"))
			if ip := ipOfInstance(m["instance"]); ip != "" {
				v.ips[ip] = true
			}
		}
		each := func(kind string, fn func(v *vmRec, m map[string]string, val any)) {
			for _, r := range res[kind] {
				if key, ok := byTarget[target(r.Metric)]; ok {
					fn(vms.m[key], r.Metric, r.Value)
				}
			}
		}
		each("os", func(v *vmRec, m map[string]string, _ any) {
			v.f["os"], v.f["os_id"], v.f["os_version"] = lv(m, "pretty_name"), lv(m, "id"), lv(m, "version_id")
		})
		each("up", func(v *vmRec, _ map[string]string, val any) {
			v.f["up"] = max(numOr0(v.get("up")), numOr0(val))
		})
		each("scrape", func(v *vmRec, _ map[string]string, val any) { v.f["last_scrape"] = numOr0(val) })
		each("cpu", func(v *vmRec, _ map[string]string, val any) { v.f["vcpu"] = numOr0(val) })
		each("mem", func(v *vmRec, _ map[string]string, val any) { v.f["ram_bytes"] = numOr0(val) })
		each("boot", func(v *vmRec, _ map[string]string, val any) { v.f["boot_time"] = numOr0(val) })
		mount := func(v *vmRec, mp any) *py.OMap {
			k := str(mp)
			if mm := v.mounts.Map(k); mm != nil {
				return mm
			}
			mm := py.NewOMap()
			v.mounts.Set(k, mm)
			return mm
		}
		each("fs_size", func(v *vmRec, m map[string]string, val any) { mount(v, lv(m, "mountpoint")).Set("size", numOr0(val)) })
		each("fs_avail", func(v *vmRec, m map[string]string, val any) { mount(v, lv(m, "mountpoint")).Set("avail", numOr0(val)) })
		for _, k := range vms.keys {
			v := vms.m[k]
			if v.get("dc") == dc {
				if _, ok := v.f["up"]; !ok || v.f["up"] == nil {
					v.f["up"] = int64(0)
				}
			}
		}
		// node exporters that are down for longer than the uname window: still VMs we know of
		for _, r := range res["up"] {
			mm := r.Metric
			if f, _ := num(r.Value); f != 0 {
				continue
			}
			if !strings.Contains(strings.ToLower(mm["job"]), "node") && !strings.HasSuffix(mm["instance"], ":9100") {
				continue
			}
			if _, ok := byTarget[target(mm)]; ok {
				continue
			}
			key := norm(mm["instance"], o.Sites)
			name := portRe.ReplaceAllString(mm["instance"], "")
			if mm["instance"] == "" {
				name = key
			}
			v := vms.add(key, name)
			v.f["dc"] = or(v.get("dc"), dc)
			v.f["prom_instance"], v.f["prom_job"], v.f["monitored"], v.f["up"] = lv(mm, "instance"), lv(mm, "job"), int64(1), int64(0)
		}
	}
	return vms
}

// serviceDomains: {ip: domains it is a backend for} and {edge ip: (tree, number of domains)} from the service map.
func serviceDomains(path string) (map[string]map[string]bool, map[string][2]any) {
	back, edges := map[string]map[string]bool{}, map[string][2]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		return back, edges
	}
	v, err := py.ParseJSON(b)
	doc := py.AsMap(v)
	if err != nil || doc == nil {
		return back, edges
	}
	if bk := doc.Map("backends"); bk != nil {
		for _, addr := range bk.Keys() {
			ip := addr
			if i := strings.LastIndex(addr, ":"); i >= 0 {
				ip = addr[:i]
			}
			for _, u := range py.AsList(bk.Get(addr)) {
				if um := py.AsMap(u); um != nil {
					if back[ip] == nil {
						back[ip] = map[string]bool{}
					}
					back[ip][cut(str(or(um.Get("domain"), "")), 80)] = true
				}
			}
		}
	}
	doms := doc.Map("domains")
	if trees := doc.Map("trees"); trees != nil {
		for _, tree := range trees.Keys() {
			n := 0
			if doms != nil {
				for _, d := range doms.Keys() {
					if strings.Contains(cut(py.Dumps(doms.Get(d), -1), 2000), tree) {
						n++
					}
				}
			}
			for _, e := range py.AsList(py.AsMap(trees.Get(tree)).Get("edges")) {
				if em := py.AsMap(e); em != nil && truthy(em.Get("ip")) {
					edges[str(em.Get("ip"))] = [2]any{tree, int64(n)}
				}
			}
		}
	}
	return back, edges
}

func isEOL(osID, ver string) bool {
	for _, e := range eolOS {
		if osID != e.id {
			continue
		}
		major, _, _ := strings.Cut(ver, ".")
		for _, x := range e.ver {
			if ver == x || major == x {
				return true
			}
		}
	}
	return false
}

// finish turns the merged records into row values.
func (o *Options) finish(vms *vmSet) {
	back, edges := serviceDomains(o.serviceMap())
	for _, k := range vms.keys {
		v := vms.m[k]
		ips := make([]string, 0, len(v.ips))
		for ip := range v.ips {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		v.f["ips"] = strings.Join(ips, ",")
		dset := map[string]bool{}
		for _, ip := range ips {
			for d := range back[ip] {
				if d != "" {
					dset[d] = true
				}
			}
		}
		doms := make([]string, 0, len(dset))
		for d := range dset {
			doms = append(doms, d)
		}
		sort.Strings(doms)
		if len(doms) > 60 {
			doms = doms[:60]
		}
		var edge *[2]any
		for _, ip := range ips {
			if e, ok := edges[ip]; ok {
				edge = &e
				break
			}
		}
		dl := make([]any, len(doms))
		for i, d := range doms {
			dl[i] = d
		}
		dm := py.NewOMap("backend_for", dl, "edge_tree", nil, "edge_domains", int64(0), "data_roles", []any{})
		if edge != nil {
			dm.Set("edge_tree", edge[0]).Set("edge_domains", edge[1])
		}
		v.f["domains"] = py.Dumps(dm, -1)
		env := envLabel[strings.ToLower(str(or(v.get("env_label"), "")))]
		if env == "" {
			for _, ip := range ips {
				if e := o.envOfIP(ip); e != "" {
					env = e
					break
				}
			}
		}
		if env == "" {
			env = "unknown"
		}
		v.f["env"] = env
		dc := str(or(v.get("dc"), "unknown"))
		if truthy(v.get("also_dc")) {
			dc += "+" + str(v.get("also_dc"))
		}
		v.f["dc"] = dc
		var tot, used int64
		mounts := []any{}
		mk := append([]string(nil), v.mounts.Keys()...)
		sort.Strings(mk)
		disk85 := false
		for _, mp := range v.mounts.Keys() {
			m := v.mounts.Map(mp)
			size, avail := numOr0(m.Get("size")), numOr0(m.Get("avail"))
			tot += size
			used += size - avail
			if size != 0 && float64(size-avail)/float64(size) >= 0.85 {
				disk85 = true
			}
		}
		for _, mp := range mk {
			m := v.mounts.Map(mp)
			size, avail := numOr0(m.Get("size")), numOr0(m.Get("avail"))
			if size != 0 {
				mounts = append(mounts, py.NewOMap("mount", mp, "size", m.Get("size"), "used", size-avail))
			}
		}
		v.f["disk_total"], v.f["disk_used"] = tot, used
		v.f["mounts"] = py.Dumps(mounts, -1)
		var flags []string
		if truthy(v.get("monitored")) && !truthy(v.get("ssh_alias")) {
			flags = append(flags, "prom-only")
		}
		if truthy(v.get("monitored")) && !truthy(v.get("up")) {
			flags = append(flags, "down")
		}
		if isEOL(strings.ToLower(str(or(v.get("os_id"), ""))), str(or(v.get("os_version"), ""))) {
			flags = append(flags, "os-eol")
		}
		if disk85 {
			flags = append(flags, "disk-85")
		}
		v.f["flags"] = strings.Join(flags, ",")
		v.f["facts"] = "{}"
		v.f["monitored"] = b2i(truthy(v.get("monitored")))
		v.f["ssh_ok"] = int64(0)
	}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// openDB opens ops.db for writing; the tables come from the migrations (argus migrate runs at the worker's start).
func openDB(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("database path must be absolute: %q", path)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(60000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// openReadOnly opens a database for reading only.
func openReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(10000)")
}

// schema is the earlier jobs' CREATE TABLE IF NOT EXISTS (the same as the ops baseline migration); it leaves an
// existing database untouched.
const schema = `
CREATE TABLE IF NOT EXISTS vms (
  id INTEGER PRIMARY KEY, key TEXT NOT NULL UNIQUE,
  name TEXT, ips TEXT, env TEXT, dc TEXT, os TEXT, os_id TEXT, os_version TEXT, kernel TEXT, arch TEXT,
  vcpu INTEGER, ram_bytes INTEGER, disk_total INTEGER, disk_used INTEGER, mounts TEXT,
  boot_time INTEGER, virt TEXT, hypervisor TEXT, monitored INTEGER, up INTEGER, last_scrape INTEGER,
  prom_instance TEXT, prom_job TEXT, ssh_alias TEXT, ssh_ok INTEGER, docker INTEGER, containers INTEGER,
  services_running INTEGER, domains TEXT, ntp_sync TEXT, timezone TEXT, flags TEXT, facts TEXT,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL, gone INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS vm_changes (
  vm_id INTEGER NOT NULL REFERENCES vms(id), at INTEGER NOT NULL, field TEXT NOT NULL, old TEXT, new TEXT);
CREATE INDEX IF NOT EXISTS vm_changes_vm ON vm_changes(vm_id, at);
CREATE TABLE IF NOT EXISTS vm_state (
  vm_id INTEGER PRIMARY KEY, at INTEGER, ssh_at INTEGER, prom_at INTEGER,
  cpu TEXT, memory TEXT, io TEXT, disk TEXT, network TEXT, reasons TEXT,
  svc_total INTEGER, svc_app INTEGER, svc_healthy INTEGER, svc_degraded INTEGER, svc_restarting INTEGER,
  svc_down INTEGER, svc_stopped INTEGER, dead_upstreams INTEGER, summary TEXT, ssh_error TEXT);
CREATE TABLE IF NOT EXISTS vm_unit_samples (
  vm_id INTEGER NOT NULL, at INTEGER NOT NULL, unit TEXT NOT NULL, active TEXT,
  mem INTEGER, anon INTEGER, peak INTEGER, cpu_usec INTEGER, cpu_pct REAL, restarts INTEGER, since INTEGER, oom_kill INTEGER);
CREATE INDEX IF NOT EXISTS vm_unit_samples_vm ON vm_unit_samples(vm_id, unit, at);
`

// sqlVal is a Python value as a SQLite parameter.
func sqlVal(v any) any {
	if b, ok := v.(bool); ok {
		return b2i(b)
	}
	return v
}

// dbStr is str(x) of a value read from SQLite or about to be written ("" for None in the comparison).
func dbStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	}
	return str(v)
}

func pyStrDB(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return str(v)
}

// mergeDuplicates folds rows whose keys normalise to the same key into one row (history and every vm_id moves to
// the row that is kept).
func mergeDuplicates(tx *sql.Tx, now int64, sites []*Site) (int, error) {
	type row struct {
		id, gone, lastSeen int64
		key                string
	}
	rs, err := tx.Query("SELECT id, key, gone, last_seen FROM vms")
	if err != nil {
		return 0, err
	}
	groups := map[string][]row{}
	var order []string
	for rs.Next() {
		var r row
		var ls sql.NullInt64
		if err := rs.Scan(&r.id, &r.key, &r.gone, &ls); err != nil {
			rs.Close()
			return 0, err
		}
		r.lastSeen = ls.Int64
		k := norm(r.key, sites)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	rs.Close()
	var refs []string
	tr, err := tx.Query("SELECT name FROM sqlite_master WHERE type='table' AND name != 'vms'")
	if err != nil {
		return 0, err
	}
	var tables []string
	for tr.Next() {
		var t string
		tr.Scan(&t)
		tables = append(tables, t)
	}
	tr.Close()
	for _, t := range tables {
		cr, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, t)
		if err != nil {
			return 0, err
		}
		for cr.Next() {
			var c string
			cr.Scan(&c)
			if c == "vm_id" {
				refs = append(refs, t)
			}
		}
		cr.Close()
	}
	merged := 0
	for _, canon := range order {
		g := groups[canon]
		sort.SliceStable(g, func(i, j int) bool {
			a, b := g[i], g[j]
			if (a.key != canon) != (b.key != canon) {
				return a.key == canon
			}
			if (a.gone != 0) != (b.gone != 0) {
				return a.gone == 0
			}
			if a.lastSeen != b.lastSeen {
				return a.lastSeen > b.lastSeen
			}
			return a.id < b.id
		})
		keep := g[0]
		for _, dup := range g[1:] {
			for _, t := range refs {
				q := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
				if _, err := tx.Exec("UPDATE OR IGNORE "+q+" SET vm_id=? WHERE vm_id=?", keep.id, dup.id); err != nil {
					return merged, err
				}
				if _, err := tx.Exec("DELETE FROM "+q+" WHERE vm_id=?", dup.id); err != nil {
					return merged, err
				}
			}
			if _, err := tx.Exec("DELETE FROM vms WHERE id=?", dup.id); err != nil {
				return merged, err
			}
			if _, err := tx.Exec("INSERT INTO vm_changes VALUES (?,?,?,?,?)", keep.id, now, "merged", dup.key, keep.key); err != nil {
				return merged, err
			}
			merged++
		}
		if keep.key != canon {
			if _, err := tx.Exec("UPDATE vms SET key=? WHERE id=?", canon, keep.id); err != nil {
				return merged, err
			}
		}
	}
	return merged, nil
}

// store writes the inventory rows and their history.
func (o *Options) store(vms *vmSet, now int64) error {
	db, err := openDB(o.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := mergeDuplicates(tx, now, o.Sites); err != nil {
		return err
	}
	type oldRow struct {
		id   int64
		gone int64
		f    map[string]any
	}
	rs, err := tx.Query("SELECT * FROM vms")
	if err != nil {
		return err
	}
	cols, _ := rs.Columns()
	old := map[string]*oldRow{}
	var oldKeys []string
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			rs.Close()
			return err
		}
		r := &oldRow{f: map[string]any{}}
		for i, c := range cols {
			r.f[c] = vals[i]
		}
		r.id, _ = r.f["id"].(int64)
		r.gone, _ = r.f["gone"].(int64)
		k := pyStrDB(r.f["key"])
		old[k] = r
		oldKeys = append(oldKeys, k)
	}
	rs.Close()
	change := func(id int64, field, a, b string) error {
		_, err := tx.Exec("INSERT INTO vm_changes VALUES (?,?,?,?,?)", id, now, field, a, b)
		return err
	}
	set := strings.Join(vmCols, "=?, ") + "=?"
	for _, key := range vms.keys {
		v := vms.m[key]
		vals := make([]any, 0, len(vmCols)+2)
		for _, c := range vmCols {
			vals = append(vals, sqlVal(v.f[c]))
		}
		if o2, ok := old[key]; ok {
			for _, f := range tracked {
				nv := v.f[f]
				if dbStr(o2.f[f]) != dbStr(nv) && nv != nil && nv != "" {
					if err := change(o2.id, f, cut(pyStrDB(o2.f[f]), 200), cut(str(nv), 200)); err != nil {
						return err
					}
				}
			}
			if o2.gone != 0 {
				if err := change(o2.id, "back", "gone", "seen"); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("UPDATE vms SET "+set+", last_seen=?, gone=0 WHERE id=?", append(vals, now, o2.id)...); err != nil {
				return err
			}
		} else {
			res, err := tx.Exec("INSERT INTO vms (key, "+strings.Join(vmCols, ", ")+", first_seen, last_seen) VALUES (?"+
				strings.Repeat(", ?", len(vmCols))+", ?, ?)", append(append([]any{key}, vals...), now, now)...)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			if err := change(id, "new", "", str(v.f["name"])); err != nil {
				return err
			}
		}
	}
	for _, key := range oldKeys {
		o2 := old[key]
		if _, ok := vms.m[key]; !ok && o2.gone == 0 {
			if _, err := tx.Exec("UPDATE vms SET gone=1 WHERE id=?", o2.id); err != nil {
				return err
			}
			if err := change(o2.id, "gone", "seen", "not seen"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Inventory runs one inventory collection and writes inventory-status.json.
func (o *Options) Inventory(ctx context.Context) (map[string]any, error) {
	if len(o.Sites) == 0 {
		return nil, ErrNoSite
	}
	t0 := o.now()
	now := t0.Unix()
	vms := o.collectProm(ctx)
	o.finish(vms)
	if err := o.store(vms, now); err != nil {
		return nil, err
	}
	counts, flags := py.NewOMap(), py.NewOMap()
	for _, k := range vms.keys {
		v := vms.m[k]
		ck := str(v.f["env"]) + "|" + str(v.f["dc"])
		counts.Set(ck, numOr0(counts.Get(ck))+1)
	}
	for _, k := range vms.keys {
		for _, f := range strings.Split(str(vms.m[k].f["flags"]), ",") {
			if f != "" {
				flags.Set(f, numOr0(flags.Get(f))+1)
			}
		}
	}
	status := "OK"
	if len(vms.keys) == 0 {
		status = "WARN"
	}
	st := py.NewOMap("at", now, "status", status, "vms", int64(len(vms.keys)), "counts", counts, "flags", flags,
		"db_hosts_skipped", []any{}, "seconds", int64(o.now().Sub(t0).Seconds()))
	if n := o.Client.Errors; n > 0 {
		st.Set("prom_errors", int64(n))
	}
	if err := writeAtomic(filepath.Join(o.StateDir, "inventory-status.json"), []byte(py.Dumps(st, -1))); err != nil {
		return nil, err
	}
	out := map[string]any{"vms": len(vms.keys), "status": status}
	return out, nil
}

// writeAtomic writes data through a temporary file and a rename (0644: the pages run as another user).
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Chmod(0o644)
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
