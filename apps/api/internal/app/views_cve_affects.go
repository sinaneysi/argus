package app

// Who a vulnerability affects: host + service (unit or container), from ops.db cve_affects and cve_service_risk
// (built by bin/cve_match.py with bin/cve_feeds/affects.py). Read-only; Go-only after the cutover (ADR-0005).

import (
	"regexp"

	"strings"
)

var cveSvcRe = regexp.MustCompile(`^[A-Za-z0-9 ()@._:/+=-]{0,120}$`)

type cveAff struct {
	target, host, env, service, unit, kind, exposure, via, version, how string
	vm                                                                  int64
}

func cveAffOf(r Row) cveAff {
	return cveAff{r.Str("target"), r.Str("host"), r.S("env"), r.Str("service"), r.Str("unit"), r.S("kind"),
		r.S("exposure"), r.S("via"), r.S("version"), r.S("how"), r.I("vm_id")}
}

const cveAffOrder = ` ORDER BY CASE exposure WHEN 'customer-facing' THEN 0 WHEN 'internet' THEN 1 ELSE 2 END, ` +
	`CASE WHEN unit IN ('-','*') THEN 1 ELSE 0 END, env DESC, service, host`

// cveAffectsByGroup loads the affects of the given component groups (at most 400 rows per group).
func cveAffectsByGroup(keys []string) map[string][]cveAff {
	out := map[string][]cveAff{}
	for i := 0; i < len(keys); i += 200 {
		j := i + 200
		if j > len(keys) {
			j = len(keys)
		}
		args := make([]any, 0, j-i)
		for _, k := range keys[i:j] {
			args = append(args, k)
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", j-i), ",")
		for _, r := range Q("SELECT * FROM cve_affects WHERE group_key IN ("+ph+")"+cveAffOrder, args...) {
			g := r.Str("group_key")
			if len(out[g]) < 400 {
				out[g] = append(out[g], cveAffOf(r))
			}
		}
	}
	return out
}

// cveAffCounts is (named services, hosts, hosts where every service is affected: kernel/OS).
func cveAffCounts(as []cveAff) (int, int, int) {
	svcs, hosts, wide := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range as {
		if a.host != "-" && a.host != "" {
			hosts[a.host] = true
		}
		switch {
		case a.unit == "*":
			wide[a.host] = true
		case a.unit != "-" && a.unit != "" && In(a.kind, "unit", "container", "project"):
			svcs[a.service] = true
		}
	}
	return len(svcs), len(hosts), len(wide)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return Itoa(n) + " " + many
}

// cveAffLabel is "Affects: 3 services on 2 hosts" (or all services on N hosts, or not mapped yet).
func cveAffLabel(as []cveAff) string {
	s, h, w := cveAffCounts(as)
	switch {
	case len(as) == 0:
		return "Affects: not mapped yet"
	case s == 0 && w > 0:
		return "Affects: all services on " + plural(h, "host", "hosts")
	case s == 0:
		return "Affects: no running service on " + plural(h, "host", "hosts")
	}
	return "Affects: " + plural(s, "service", "services") + " on " + plural(h, "host", "hosts") +
		If(w > 0, " (host-wide on "+Itoa(w)+")", "")
}

func cveUnitText(a cveAff) string {
	switch {
	case a.unit == "*":
		return "every service (host-wide)"
	case strings.HasPrefix(a.unit, "container:"):
		return "container " + strings.TrimPrefix(a.unit, "container:")
	}
	return a.unit
}

// cveVMServices is the VM card's "By service": each service on this VM with its open findings.
func cveVMServiceRows(vid int64) []Row {
	return Q("SELECT a.service, group_concat(DISTINCT a.unit) units, min(f.priority) pri, count(DISTINCT f.id) n, "+
		"count(DISTINCT CASE WHEN f.kev=1 THEN f.id END) kev, max(CASE a.exposure WHEN 'customer-facing' THEN 2 "+
		"WHEN 'internet' THEN 1 ELSE 0 END) exp, group_concat(DISTINCT f.group_key) groups "+
		"FROM cve_affects a JOIN cve_findings f ON f.group_key=a.group_key AND f.state='open' "+
		"JOIN cve_targets t ON t.finding_id=f.id AND t.target=a.target "+
		"WHERE a.vm_id=? AND a.unit NOT IN ('-','') GROUP BY a.service ORDER BY min(f.priority), count(DISTINCT f.id) DESC", vid)
}
