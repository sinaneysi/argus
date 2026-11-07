package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

var (
	reOption = regexp.MustCompile(`(?m)^### Option (\d+): (.+)$`)
	reField  = regexp.MustCompile(`^- \*\*([^*:]+):\*\* (.*)$`)
	rePrompt = regexp.MustCompile(`(?s)~~~~markdown\n(.*?)\n~~~~`)
)

// parseHandoff is the subset of the fix handoff parser this demo's own markdown needs.
func parseHandoff(md, env string) ([]any, string) {
	keys := map[string]string{"kind": "kind", "what": "what", "why it works": "why", "effort": "effort", "risk": "risk",
		"blast radius": "blast_radius", "approval": "approval"}
	opts := []any{}
	for i, m := range reOption.FindAllStringSubmatchIndex(md, -1) {
		title := py.Strip(md[m[4]:m[5]])
		rec := strings.HasSuffix(title, "(recommended)")
		title = py.Strip(strings.ReplaceAll(title, "(recommended)", ""))
		mend := m[1]
		stop := len(md)
		if e := strings.Index(md[mend:], "\n### "); e >= 0 && mend+e < stop {
			stop = mend + e
		}
		if e := strings.Index(md[mend:], "\n## "); e >= 0 && mend+e < stop {
			stop = mend + e
		}
		o := om("n", i+1, "title", title)
		for _, line := range py.SplitLines(md[mend:stop]) {
			if f := reField.FindStringSubmatch(line); f != nil {
				if k, ok := keys[strings.ToLower(f[1])]; ok {
					o.Set(k, py.Strip(f[2]))
				}
			}
		}
		for _, k := range []string{"kind", "what", "why", "effort", "risk", "blast_radius", "approval"} {
			if !o.Has(k) {
				o.Set(k, "")
			}
		}
		a := strings.ToUpper(o.S("approval"))
		ak := "fixer"
		if env == "release" || strings.Contains(a, "PRODUCTION") {
			ak = "production"
		} else if strings.Contains(a, "OPERATOR") {
			ak = "operator"
		}
		o.Set("approval_kind", ak)
		o.Set("recommended", rec)
		opts = append(opts, o)
	}
	prompt := ""
	if p := rePrompt.FindStringSubmatch(md); p != nil {
		prompt = p[1] + "\n"
	}
	return opts, prompt
}

func (g *Gen) storeHandoff(table, where string, args []any, md, env, source string, ref any) {
	opts, prompt := parseHandoff(md, env)
	state := "missing"
	if len(opts) > 0 && prompt != "" {
		state = "ok"
	} else if len(opts) > 0 || prompt != "" {
		state = "partial"
	}
	cols := []any{"fix_options", jd(opts), "fix_state", state, "fix_flags", "[]"}
	if table != "triage_problems" {
		cols = append(cols, "fix_prompt", prompt, "fix_source", source, "fix_at", g.now-3600)
	} else {
		cols = append(cols, "fix_prompt", prompt)
	}
	if ref != nil && (table == "incidents" || table == "problems") {
		cols = append(cols, "fix_ref", ref)
	}
	var sets []string
	var vals []any
	for i := 0; i < len(cols); i += 2 {
		sets = append(sets, cols[i].(string)+"=?")
		vals = append(vals, cols[i+1])
	}
	g.exec(g.ops, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE "+where, append(vals, args...)...)
}

const KERNEL_MD = `# Linux kernel 6.8.0-41 on the production fleet

## Lazy reading
- **What broke:** {n} production hosts run kernel 6.8.0-41, vulnerable to CVE-2026-31337 (known exploited).
- **Why:** The fixed kernel 6.8.0-45 is installed by unattended-upgrades but the hosts were never rebooted (proven).
- **Impact:** A local user or a compromised container can gain root; {pub} of the hosts serve the public shop.
- **Fix:** A rolling reboot wave, one host per pool at a time (Option 1) — the operator approves (production).
- **Status:** needs operator

## How to fix it

### Option 1: Rolling reboot wave (recommended)
- **Kind:** permanent fix
- **What:** reboot the {n} hosts in four waves (edges, shop, api, payments), one host per load-balancer pool at a time, drained first
- **Why it works:** the installed 6.8.0-45 kernel is active after the reboot
- **Effort:** 3 h
- **Risk:** medium: a host that does not come back needs console access
- **Blast radius:** one backend per pool at a time; no customer-visible downtime when drained
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 2: Live patch until the window
- **Kind:** mitigation
- **What:** enable the kernel live patch for CVE-2026-31337 on the internet-facing hosts first
- **Why it works:** closes the exploited path without a reboot
- **Effort:** 1 h
- **Risk:** low
- **Blast radius:** none
- **Approval:** PRODUCTION CHANGE: the operator approves

## Prompt for a write-access agent

~~~~markdown
PRODUCTION CHANGE — needs the operator's explicit approval before any step.
# Kernel reboot wave for CVE-2026-31337
## Goal
Every production host runs kernel 6.8.0-45 or newer; no customer-visible downtime.
## Environment and targets
- Environment: production; hosts: {hosts}
## Change, step by step
### For each host (one per pool at a time)
- Drain: set the backend ` + "`drain`" + ` in the edge upstream, wait until active connections are 0
- Change: ` + "`sudo systemctl reboot`" + `
- Check: ` + "`uname -r`" + ` → 6.8.0-45-generic; ` + "`/health`" + ` of every service on the host 200
- Rollback: boot the previous kernel from the GRUB menu (console)
## Verify
- The Vulnerabilities page shows no kernel finding for production.
~~~~
`

const PHP_MD = `# PHP 8.1 (end of life) in the analytics-matomo container

## Lazy reading
- **What broke:** The Matomo container on ` + "`prod-matomo-01`" + ` runs PHP 8.1.2, end of life and exploitable (CVE-2026-24410, KEV).
- **Why:** The image ` + "`matomo:4.16-php8.1`" + ` was pinned in 2024 and never rebuilt (proven).
- **Impact:** ` + "`stats.northwind.example`" + ` is internet-facing; remote code execution through the PHP-CGI argument flaw.
- **Fix:** Rebuild on ` + "`matomo:5.1-php8.3-fpm`" + ` and redeploy (Option 1) — the operator approves (production).
- **Status:** open

## How to fix it

### Option 1: Rebuild the image on PHP 8.3 (recommended)
- **Kind:** permanent fix
- **What:** change the base image in ` + "`analytics/matomo`" + ` to ` + "`matomo:5.1-php8.3-fpm`" + `, run the Matomo upgrade, redeploy
- **Why it works:** PHP 8.3.14 has every fix; 8.1 gets none
- **Effort:** 4 h
- **Risk:** medium: plugins must be checked against Matomo 5
- **Blast radius:** analytics only (stats.northwind.example), not the shop
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 2: Close it to the internet meanwhile
- **Kind:** mitigation
- **What:** allow ` + "`stats.northwind.example`" + ` only from the office VPN and the edge tracking path ` + "`/matomo.php`" + `
- **Why it works:** removes the exposed CGI path from the internet
- **Effort:** 20 min
- **Risk:** low
- **Blast radius:** the Matomo UI for people outside the VPN
- **Approval:** PRODUCTION CHANGE: the operator approves

## Prompt for a write-access agent

~~~~markdown
PRODUCTION CHANGE — needs the operator's explicit approval before any step.
# Rebuild analytics-matomo on PHP 8.3
## Goal
The container on prod-matomo-01 runs PHP 8.3; ` + "`stats.northwind.example`" + ` answers 200 and tracks visits.
## Environment and targets
- Environment: production; host prod-matomo-01 (10.20.30.95); container ` + "`analytics-matomo`" + `
- Repository ` + "`analytics/matomo`" + `, Dockerfile ` + "`FROM matomo:4.16-php8.1`" + `
## Change, step by step
### Step 1
- Backup: database dump (the backup plan matomo-db), image tag noted
- Change: ` + "`FROM matomo:5.1-php8.3-fpm`" + `; CI builds and pushes the image
- Check: ` + "`docker exec analytics-matomo php -v`" + ` → 8.3.x
- Rollback: redeploy the previous image tag
~~~~
`

type tgt struct{ target, version, env string }

type cveOpts struct {
	kev      int
	epss     float64
	exposure string
	nofix    int
	aliases  []string
	firstAgo float64
	state    string
}

func (g *Gen) cve() {
	o := g.ops
	var prod []vm
	for _, v := range VMS {
		if v.env == "release" {
			prod = append(prod, v)
		}
	}
	var kernelHosts []vm
	for _, v := range prod {
		if g.oneStr(o, "SELECT kernel FROM vms WHERE name=?", v.name) == OLD_KERNEL {
			kernelHosts = append(kernelHosts, v)
		}
	}
	public := 0
	for _, v := range kernelHosts {
		if v.role == "edge" || v.has("shop-front") {
			public++
		}
	}
	fid := 0
	add := func(vkey, kind, comp, eco, cls string, versions []string, targets []tgt, title, sev string, cvss float64, fixed, pri string,
		score float64, c cveOpts) string {
		if c.epss == 0 {
			c.epss = 0.02
		}
		if c.exposure == "" {
			c.exposure = "internal"
		}
		if c.firstAgo == 0 {
			c.firstAgo = 6 * D
		}
		if c.state == "" {
			c.state = "open"
		}
		fid++
		gk := eco + "|" + comp + "|" + cls
		sla := map[string]float64{"P1": 2, "P2": 7, "P3": 30, "P4": 90}[pri]
		tl := []any{}
		envSet := map[string]bool{}
		for _, t := range targets {
			var vid any
			if id, ok := g.vmIDs[t.target]; ok {
				vid = id
			}
			tl = append(tl, om("target", t.target, "vm_id", vid, "version", t.version, "env", t.env))
			envSet[t.env] = true
		}
		aliases := c.aliases
		if aliases == nil {
			aliases = []string{}
		}
		tier := 2
		if c.exposure != "internal" {
			tier = 1
		}
		vector := "CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H"
		if cvss >= 9 {
			vector = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
		}
		var kevDue, incidentID any
		if c.kev != 0 {
			kevDue = g.day(c.firstAgo - 21*D)
		}
		if comp == "linux" && cls == "production" {
			incidentID = 151
		}
		epssPct := c.epss * 9
		if 0.999 < epssPct {
			epssPct = 0.999
		}
		g.ins(o, "cve_findings", "id", fid, "dkey", h12(vkey+gk), "vuln_key", vkey, "aliases", jd(aliases), "kind", kind,
			"component_name", comp, "ecosystem", eco, "group_key", gk, "env_max_class", cls, "envs", jd(sortedKeys(envSet)),
			"versions", jd(versions), "targets", jd(tl), "n_targets", len(tl), "exposure", c.exposure, "tier", tier,
			"title", title, "severity", sev, "cvss", cvss, "vector", vector, "url", "", "published", g.day(c.firstAgo+20*D),
			"fixed_version", fixed, "nofix", c.nofix, "kev", c.kev, "kev_due", kevDue, "ransomware", 0,
			"epss", c.epss, "epss_pct", epssPct, "score", score, "priority", pri,
			"breakdown", jd(om("base", L(round(cvss, 1), "CVSS"), "kev", L(iif(c.kev != 0, 25, 0), "known exploited"),
				"exposure", L(iif(c.exposure != "internal", 15, 0), c.exposure), "env", L(iif(cls == "production", 10, 0), cls))),
			"first_seen", g.t(c.firstAgo), "last_seen", g.t(3*H), "state", c.state, "sla_due", g.t(c.firstAgo-sla*D),
			"notified", 1, "incident_id", incidentID)
		for _, t := range tl {
			m := t.(*py.OMap)
			g.ins(o, "cve_targets", "finding_id", fid, "target", m.Get("target"), "vm_id", m.Get("vm_id"), "version", m.Get("version"), "env", m.Get("env"))
		}
		return gk
	}
	var kt []tgt
	for _, v := range kernelHosts {
		kt = append(kt, tgt{v.name, OLD_KERNEL, "release"})
	}
	gkKernel := add("CVE-2026-31337", "kernel", "linux", "Ubuntu:24.04:LTS", "production", sl(OLD_KERNEL), kt,
		"Linux kernel: use-after-free in the netfilter nf_tables module allows local privilege escalation",
		"high", 7.8, "6.8.0-45.45", "P1", 92.5, cveOpts{kev: 1, epss: 0.41, exposure: "customer-facing", firstAgo: 3 * D})
	add("CVE-2026-29871", "kernel", "linux", "Ubuntu:24.04:LTS", "production", sl(OLD_KERNEL), kt,
		"Linux kernel: io_uring race condition leads to a kernel memory leak", "medium", 5.5, "6.8.0-45.45", "P2", 48.0,
		cveOpts{exposure: "customer-facing", firstAgo: 3 * D})
	var stK []tgt
	for _, v := range VMS {
		if v.env == "pre-release" {
			stK = append(stK, tgt{v.name, "6.8.0-41-generic", "pre-release"})
		}
	}
	add("CVE-2026-31337", "kernel", "linux", "Ubuntu:24.04:LTS", "staging", sl("6.8.0-41-generic"), stK,
		"Linux kernel: use-after-free in nf_tables", "high", 7.8, "6.8.0-45.45", "P2", 61.0, cveOpts{kev: 1, epss: 0.41, firstAgo: 3 * D})
	mt := []tgt{{"prod-matomo-01", "8.1.2", "release"}}
	gkPHP := add("CVE-2026-24410", "runtime", "php", "container:analytics-matomo", "production", sl("8.1.2"), mt,
		"PHP-CGI argument injection allows remote code execution", "critical", 9.8, "8.1.29", "P1", 97.0,
		cveOpts{kev: 1, epss: 0.94, exposure: "internet", firstAgo: 2 * D})
	add("CVE-2026-24477", "runtime", "php", "container:analytics-matomo", "production", sl("8.1.2"), mt,
		"PHP: heap buffer overflow in mb_encode_mimeheader", "high", 8.1, "8.1.30", "P2", 70.0, cveOpts{exposure: "internet", firstAgo: 2 * D})
	add("EOL-php-8.1", "runtime", "php", "container:analytics-matomo", "production", sl("8.1.2"), mt,
		"PHP 8.1 reached end of life: no more security fixes", "high", 7.0, "8.3", "P2", 66.0, cveOpts{exposure: "internet", firstAgo: 2 * D})
	var ossl []tgt
	for _, v := range VMS {
		if v.env == "release" && len(ossl) < 30 {
			ossl = append(ossl, tgt{v.name, "3.0.13-0ubuntu3.3", v.env})
		}
	}
	add("CVE-2026-30112", "package", "openssl", "Ubuntu:24.04:LTS", "production", sl("3.0.13-0ubuntu3.3"), ossl,
		"OpenSSL: denial of service in X.509 policy processing", "medium", 5.9, "3.0.13-0ubuntu3.5", "P3", 31.0, cveOpts{firstAgo: 9 * D})
	nx := []tgt{{"prod-shop-01", "14.2.3", "release"}, {"prod-shop-02", "14.2.3", "release"}, {"prod-shop-03", "14.2.3", "release"}}
	add("CVE-2026-27015", "npm", "next", "npm", "production", sl("14.2.3"), nx,
		"Next.js: cache poisoning through a crafted header", "high", 7.5, "14.2.25", "P2", 63.0, cveOpts{exposure: "customer-facing",
			firstAgo: 5 * D})
	add("CVE-2026-25330", "database", "postgresql", "Ubuntu:24.04:LTS", "production", sl("16.3"),
		[]tgt{{"prod-db-01", "16.3", "release"}, {"prod-db-02", "16.3", "release"}, {"prod-db-03", "16.3", "release"}},
		"PostgreSQL: privilege escalation through a crafted extension script", "medium", 6.5, "16.6", "P3", 34.0, cveOpts{firstAgo: 11 * D})
	add("CVE-2026-22091", "software", "nginx", "Ubuntu:24.04:LTS", "production", sl("1.24.0"),
		[]tgt{{"prod-edge-01", "1.24.0", "release"}, {"prod-edge-02", "1.24.0", "release"}, {"prod-edge-03", "1.24.0", "release"}},
		"nginx: HTTP/3 worker crash on a crafted QUIC packet", "medium", 5.3, "", "P3", 28.0, cveOpts{exposure: "internet", nofix: 1, firstAgo: 8 * D})
	add("CVE-2026-20844", "image", "redis", "image:redis:7.2", "dev", sl("7.2.4"), []tgt{{"dev-swarm-01", "7.2.4", "develop"}},
		"Redis: Lua sandbox escape", "high", 7.0, "7.2.7", "P4", 12.0, cveOpts{firstAgo: 14 * D})
	// who is affected
	for _, v := range kernelHosts {
		g.ins(o, "cve_affects", "group_key", gkKernel, "target", v.name, "host", v.name, "vm_id", g.vmIDs[v.name], "env", "release",
			"service", "(host)", "unit", "*", "kind", "host", "exposure", iif(v.role == "edge", "customer-facing", "internal"),
			"via", "", "version", OLD_KERNEL, "how", "kernel: every process on the host; reboot")
		svcs := v.svcs
		if len(svcs) > 2 {
			svcs = svcs[:2]
		}
		for _, s := range svcs {
			if sv, ok := SERVICES[s]; ok {
				g.ins(o, "cve_affects", "group_key", gkKernel, "target", v.name, "host", v.name, "vm_id", g.vmIDs[v.name], "env", "release",
					"service", s, "unit", "northwind-"+s+".service", "kind", "unit",
					"exposure", iif(sv.domain != "", "customer-facing", "internal"), "via", sv.domain,
					"version", OLD_KERNEL, "how", "runs on the vulnerable kernel")
			}
		}
	}
	g.ins(o, "cve_affects", "group_key", gkPHP, "target", "prod-matomo-01", "host", "prod-matomo-01", "vm_id", g.vmIDs["prod-matomo-01"],
		"env", "release", "service", "analytics-matomo", "unit", "container:analytics-matomo", "kind", "container", "exposure", "internet",
		"via", "stats.northwind.example", "version", "8.1.2", "how", "php-fpm in the image matomo:4.16-php8.1")
	type riskT struct {
		svc         string
		n           int
		hl          []string
		p1, p2, kev int
		comp, exp   string
	}
	for _, r := range []riskT{
		{"analytics-matomo", 1, sl("prod-matomo-01"), 1, 2, 1, "php", "internet"},
		{"shop-front", 3, sl("prod-shop-01", "prod-shop-02", "prod-shop-03"), 1, 2, 1, "linux", "customer-facing"},
		{"checkout", 3, sl("prod-api-01", "prod-api-02", "prod-api-03"), 1, 1, 1, "linux", "customer-facing"},
		{"payment-gateway", 2, sl("prod-pay-01", "prod-pay-02"), 1, 1, 1, "linux", "customer-facing"},
		{"cart", 3, sl("prod-api-01", "prod-api-02", "prod-api-03"), 1, 1, 1, "linux", "customer-facing"},
	} {
		g.ins(o, "cve_service_risk", "service", r.svc, "hosts", r.n, "host_list", jd(r.hl), "envs", jd(sl("release")), "p1", r.p1, "p2", r.p2, "kev", r.kev,
			"findings", r.p1+r.p2, "groups_n", iif(r.svc != "analytics-matomo", 2, 1),
			"top_group", iif(r.comp == "php", gkPHP, gkKernel), "top_component", r.comp, "exposure", r.exp, "at", g.t(3*H))
	}
	// fixes (md + stored handoff)
	var hostNames []string
	for _, v := range kernelHosts {
		hostNames = append(hostNames, v.name)
	}
	kmd := strings.NewReplacer("{n}", py.Str(len(kernelHosts)), "{pub}", py.Str(public), "{hosts}", strings.Join(hostNames, ", ")).Replace(KERNEL_MD)
	for _, f := range []struct{ gk, md, name, eco, cls, fixed string }{
		{gkKernel, kmd, "linux", "Ubuntu:24.04:LTS", "production", "6.8.0-45.45"},
		{gkPHP, PHP_MD, "php", "container:analytics-matomo", "production", "8.3"},
	} {
		opts, prompt := parseHandoff(f.md, "release")
		g.ins(o, "cve_fixes", "group_key", f.gk, "kind", iif(f.name == "linux", "kernel", "runtime"), "name", f.name, "ecosystem", f.eco,
			"env_class", f.cls, "fixed_version", f.fixed, "md", f.md, "fix_options", jd(opts), "fix_prompt", prompt, "fix_state", "ok",
			"fix_flags", "[]", "fix_source", "cve-fix", "fix_at", g.t(2*H))
	}
	g.ins(o, "cve_suppressions", "id", h12("supp-nginx"), "vuln_key", "CVE-2026-22091", "component", "nginx", "target", "", "env", "production",
		"reason", "HTTP/3 is disabled on every edge (listen ... quic not configured); revisit when a fix ships",
		"expires", g.day(-40*D), "owner", "robin.moe", "created_by", "robin.moe", "created_at", g.t(6*D), "active", 1)
	// hosts
	for idx, v := range VMS {
		i := idx + 1
		kern := g.oneStr(o, "SELECT kernel FROM vms WHERE id=?", i)
		flag := VM_FLAGS[v.name]
		skipped := flag == "ssh-skipped-db" || flag == "not-monitored"
		var alias, errMsg any
		if !skipped {
			alias = v.name + "-ro"
		}
		if flag == "ssh-skipped-db" {
			errMsg = "DB host: no login (operator rule)"
		} else if skipped {
			errMsg = "no ssh-ro alias"
		}
		var pkgs any = 0
		if !skipped {
			pkgs = g.r.randint(480, 910)
		}
		g.ins(o, "cve_hosts", "name", v.name, "vm_id", i, "alias", alias, "env", v.env, "os", "Ubuntu",
			"os_id", "ubuntu", "os_version", iif(v.name == "prod-matomo-01", "20.04", "24.04"), "kernel", kern,
			"reboot_required", iif(kern == OLD_KERNEL, 1, 0), "exposure", iif(v.role == "edge", "internet", "internal"),
			"tier", iif(v.env == "release", 1, 3), "pkgs_n", pkgs, "pkgs_at", g.t(5*H),
			"runtime", iif(v.role == "app", "docker", ""), "status", iif(skipped, "skipped", "ok"),
			"error", errMsg, "seen", g.t(5*H))
	}
	conts := [][6]string{
		{"prod-matomo-01", "analytics-matomo", "analytics-matomo", "registry.northwind.example/analytics/matomo:4.16-php8.1", "cadvisor:east", "ok"},
		{"prod-shop-01", "shop-front", "shop-front", "registry.northwind.example/web/shop-front:2026.10.23", "cadvisor:east", "ok"},
		{"prod-shop-02", "shop-front", "shop-front", "registry.northwind.example/web/shop-front:2026.10.23", "cadvisor:east", "ok"},
		{"prod-misc-01", "recommendation", "recommendation", "registry.northwind.example/shop/recommendation:1.8.0", "cadvisor:east", "failed"},
		{"dev-swarm-01", "redis_dev", "redis", "redis:7.2", "ssh-ro docker", "ok"},
		{"dev-swarm-02", "shop-front_dev", "shop-front", "registry.northwind.example/web/shop-front:develop", "ssh-ro docker", "ok"},
		{"prod-media-01", "image-resizer", "image-resizer", "registry.northwind.example/media/image-resizer:3.2.1", "declared", "pending"},
	}
	for _, c := range conts {
		v, _ := vmByName(c[0])
		sum := sha256.Sum256([]byte(c[3]))
		g.ins(o, "cve_containers", "host", c[0], "vm_id", g.vmIDs[c[0]], "env", v.env, "dc", v.dc,
			"name", c[1], "service", c[2], "image", c[3], "source", c[4], "instance", c[0]+":8080",
			"digest", "sha256:"+hex.EncodeToString(sum[:]), "analysis", c[5],
			"evidence", iif(c[5] == "ok", "layers streamed by root; packages from /var/lib/dpkg/status", ""), "seen", g.t(5*H))
	}
	blind := [][5]string{
		{"db-rule", "prod-db-01", "prod-db-01", "release", "database hosts are never logged into (operator rule); packages unknown"},
		{"db-rule", "prod-db-02", "prod-db-02", "release", "database hosts are never logged into (operator rule)"},
		{"no-evidence", "prod-backup-02", "prod-backup-02", "release", "no ssh-ro alias and no node_exporter: nothing is known about its packages"},
		{"image-not-analysed", "registry.northwind.example/shop/recommendation:1.8.0", "prod-misc-01", "release",
			"registry answered 401: the analyser's pull token has no access to the shop group"},
		{"declared", "registry.northwind.example/media/image-resizer:3.2.1", "prod-media-01", "release",
			"declared in config but cAdvisor on prod-media-01 reports no container labels"},
		{"cadvisor-no-labels", "prod-media-01:8080", "prod-media-01", "release", "cAdvisor runs without --docker_only labels"},
		{"web-no-header", "https://sellers.northwind.example", "", "release", "the server hides its version (server_tokens off)"},
	}
	for _, b := range blind {
		var vid any
		if id, ok := g.vmIDs[b[2]]; ok {
			vid = id
		}
		g.ins(o, "cve_blind_spots", "kind", b[0], "subject", b[1], "host", b[2], "vm_id", vid, "env", b[3], "reason", b[4], "seen", g.t(5*H))
	}
	nP1 := g.oneInt(o, "SELECT count(DISTINCT group_key) FROM cve_findings WHERE priority='P1' AND state='open'")
	g.writeJSON("cve-status.json", om(
		"at", g.t(3*H), "status", "WARN",
		"counts", om("P1", nP1, "P1_fix", nP1, "P2", g.oneInt(o, "SELECT count(DISTINCT group_key) FROM cve_findings WHERE priority='P2'"),
			"kev", g.oneInt(o, "SELECT count(DISTINCT group_key) FROM cve_findings WHERE kev=1"), "eol", 1,
			"components", g.oneInt(o, "SELECT count(DISTINCT group_key) FROM cve_findings")),
		"coverage", om("scanned", 36, "hosts", len(VMS), "with_access", 36, "components", 21480,
			"kinds", om("package", 19800, "kernel", 3, "npm", 1240, "composer", 310, "runtime", 6, "image", 7),
			"containers", om("hosts_with_packages", 36, "hosts_with_containers", 6, "hosts_with_containers_observed", 5,
				"images", 7, "images_analysed", 5, "images_failed", 1)),
		"feeds", om("at", g.t(5.5*H), "mode", "online", "errors", om(),
			"hosts", om("api.osv.dev", om("ok", true, "n", 412), "www.cisa.gov", om("ok", true, "n", 1),
				"api.first.org", om("ok", true, "n", 9)),
			"queries", om(), "nvd", L()),
		"feed_problems", om("failed", 0, "partial", 0, "unparsable", 0, "list", L()), "errors", om()))
}
