package app

// Port of the parts of app/backup_simple.py the web app uses: the four-field form's clean(), resolving the
// database host in the Inventory (with bounded DNS lookups), the default member, the schedule chips and their next
// runs, and missing_databases. derive/render stay in Python (root's bin/backup_plans.py).

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

// BSSchedule is one schedule chip: key, label, cron.
type BSSchedule struct{ Key, Label, Cron string }

// BSSchedules is backup_simple.SCHEDULES, in order.
var BSSchedules = []BSSchedule{{"30m", "Every 30 min", "*/30 * * * *"}, {"hourly", "Hourly", "0 * * * *"},
	{"12h", "2× daily", "0 3,15 * * *"}, {"daily", "Daily 03:00", "0 3 * * *"}}

// Python's re.match(r"^...$") also accepts one trailing newline; "\n?$" keeps that (RE2's $ is the text end).
var (
	BSTargetRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}\n?$`)
	// root's checks of a new target (bin/backup_exec.py ENDPOINT_RE, BUCKET_RE, KEY_RE), mirrored for the form
	backupEndpointRE = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$`)
	backupBucketRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	backupKeyRE      = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{3,128}$`)
	BSDraftRE        = regexp.MustCompile(`^[0-9a-f]{16}\n?$`)
)

// BSFields is backup_simple.FIELDS.
var BSFields = []string{"uri", "databases", "schedule", "cron", "target", "member", "name", "retention_days", "draft", "token",
	"sha", "csrf", "action"}

// BSEnvOfIP is backup_simple.env_of_ip(ip).
func BSEnvOfIP(ip string) string {
	if ip == "" {
		return ""
	}
	for _, p := range defEnvPrefixes { // first match wins (site_defaults*.go)
		if strings.HasPrefix(ip, p[0]) {
			return p[1]
		}
	}
	return ""
}

// bsIsIP is ipaddress.ip_address(h) succeeding.
func bsIsIP(h string) bool {
	_, err := netip.ParseAddr(h)
	return err == nil
}

// BSLookup is backup_simple._lookup(name): the IPv4/IPv6 addresses of a name within 2 s, sorted; [] on any error.
// Tests replace it.
var BSLookup = func(name string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, a := range addrs {
		s := a.IP.String()
		if a.Zone != "" {
			s += "%" + a.Zone
		}
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// BSResolved is resolve()'s result.
type BSResolved struct {
	Kind    string
	Members []Row
	Missing []string
	Error   string
}

func bsSameVM(a, b Row) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// BSResolve is backup_simple.resolve(parsed, vms, vips, lookup): the Inventory VMs that can run the job.
func BSResolve(parsed *BUParsed, vms []Row, vips *py.OMap, lookup func(string) []string) BSResolved {
	if lookup == nil {
		lookup = BSLookup
	}
	byIP, byName := map[string]Row{}, map[string]Row{}
	for _, v := range vms {
		for _, ip := range strings.Split(v.S("ips"), ",") {
			if s := py.Strip(ip); s != "" {
				byIP[s] = v
			}
		}
		byName[strings.ToLower(v.S("name"))] = v
	}
	if parsed.SRV {
		return BSResolved{Kind: "single", Missing: []string{parsed.Hosts[0].Host},
			Error: "mongodb+srv hides the members behind DNS: write the members instead " +
				"(mongodb://user:pass@host1,host2,host3/?replicaSet=...)"}
	}
	var members []Row
	var missing []string
	kind := "single"
	if len(parsed.Hosts) > 1 {
		kind = "replica-set"
	}
	get := func(m map[string]Row, k string) Row {
		if v, ok := m[k]; ok && len(v) > 0 {
			return v
		}
		return nil
	}
	for _, hh := range parsed.Hosts {
		h := hh.Host
		hl := strings.ToLower(h)
		v := get(byIP, h)
		if v == nil {
			v = get(byName, hl)
		}
		if v == nil {
			v = get(byName, strings.Split(hl, ".")[0])
		}
		if v == nil && !bsIsIP(h) {
			for _, ip := range lookup(h) {
				if v = get(byIP, ip); v != nil {
					break
				}
			}
		}
		if v == nil && len(parsed.Hosts) == 1 {
			vip := vips.Get(h)
			if !py.Truthy(vip) {
				vip = nil
				if !bsIsIP(h) {
					for _, ip := range lookup(h) {
						if vips.Has(ip) {
							vip = vips.Get(ip)
							break
						}
					}
				}
			}
			if py.Truthy(vip) {
				kind = "vip"
				for _, m := range py.AsList(py.AsMap(vip).GetD("members", []any{})) {
					ms := py.Str(m)
					if mv := get(byName, strings.ToLower(ms)); mv != nil {
						members = append(members, mv)
					} else {
						missing = append(missing, ms)
					}
				}
				continue
			}
		}
		if v != nil {
			dup := false
			for _, m := range members {
				if bsSameVM(m, v) {
					dup = true
				}
			}
			if !dup {
				members = append(members, v)
			}
		} else {
			missing = append(missing, h)
		}
	}
	errs := ""
	if len(missing) > 0 {
		verb := "are"
		if len(missing) == 1 {
			verb = "is"
		}
		errs = strings.Join(missing, ", ") + " " + verb + " not in the Inventory. The job runs on the " +
			"database host itself, so the host must be an Inventory VM (a VIP needs its members in " +
			"config/backup-vips.json)."
	}
	envs := map[string]bool{}
	for _, m := range members {
		e := m.S("env")
		if e == "" {
			e = BSEnvOfIP(strings.Split(m.S("ips"), ",")[0])
		}
		envs[e] = true
	}
	if errs == "" && len(envs) > 1 {
		var l []string
		for e := range envs {
			l = append(l, e)
		}
		sort.Strings(l)
		errs = "the members are in different environments (" + strings.Join(l, ", ") + ")"
	}
	return BSResolved{Kind: kind, Members: members, Missing: missing, Error: errs}
}

// BSDefaultMember is backup_simple.default_member(res, test): a secondary.
func BSDefaultMember(res BSResolved, test *py.OMap) string {
	var names []string
	for _, m := range res.Members {
		names = append(names, py.Str(m.Get("name")))
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	prim := py.Str(test.GetD("primary", ""))
	for i := len(names) - 1; i >= 0; i-- {
		if names[i] != prim {
			return names[i]
		}
	}
	return names[len(names)-1]
}

// BSScheduleOf is backup_simple.schedule(key, cron): (sched, label, expr) or a ValueError message.
func BSScheduleOf(key, cron string) (*BPSched, string, string, error) {
	for _, s := range BSSchedules {
		if s.Key == key {
			sc, err := BPParseCron(s.Cron)
			return sc, strings.ToLower(s.Label), s.Cron, err
		}
	}
	if key != "cron" {
		return nil, "", "", bpErr("choose a schedule")
	}
	expr := strings.Join(py.Split(cron), " ")
	if expr == "" {
		return nil, "", "", bpErr("enter a cron expression (5 fields: minute hour day-of-month month day-of-week)")
	}
	if py.Len(expr) > 100 {
		return nil, "", "", bpErr("the expression is too long")
	}
	sc, err := BPParseCron(expr)
	if err != nil {
		return nil, "", "", err
	}
	return sc, "cron " + expr, expr, nil
}

// BSNextRuns is backup_simple.next_runs(key, cron, now, n=5): "%a %d %b %H:%M" strings, [] on a bad schedule.
func BSNextRuns(key, cron string, now time.Time) []string {
	s, _, _, err := BSScheduleOf(key, cron)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range s.NextRuns(now, 5) {
		out = append(out, t.Format("Mon 02 Jan 15:04"))
	}
	return out
}

// BSClean is backup_simple.clean(form): {field: value} and the errors (unknown or secret-named fields, too long,
// secret-looking values); a value is never echoed in an error.
func BSClean(form py.QS) (map[string]string, []string) {
	v := map[string]string{}
	var errs []string
	for _, k := range form.Keys() {
		val := form.Get(k)
		k30 := py.Head(k, 30)
		if !In(k, BSFields...) {
			errs = append(errs, "Unknown field '"+k30+"'.")
			continue
		}
		if BPSecretName.MatchString(k) && k != "token" {
			errs = append(errs, "The field '"+k30+"' is not accepted.")
			continue
		}
		val = py.Strip(val)
		lim := 200
		if k == "uri" {
			lim = 2000
		}
		if py.Len(val) > lim {
			errs = append(errs, k+": too long.")
			continue
		}
		if !In(k, "uri", "csrf", "token", "sha") && BPSecretish(val) {
			errs = append(errs, k+": looks like a secret. The password belongs in the connection string only.")
			continue
		}
		v[k] = val
	}
	return v, errs
}

// BSMissingDatabases is backup_simple.missing_databases(dbs, test).
func BSMissingDatabases(dbs string, test *py.OMap) []string {
	var have []string
	for _, x := range strings.Split(py.OrS(test.Get("databases"), ""), ",") {
		if x != "" {
			have = append(have, x)
		}
	}
	if dbs == "all" || len(have) == 0 {
		return nil
	}
	var out []string
	for _, d := range strings.Split(dbs, " ") {
		if d != "" && !In(d, have...) {
			out = append(out, d)
		}
	}
	return out
}
