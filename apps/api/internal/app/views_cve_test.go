package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

const cveHostile = `<img src=x onerror=alert(1)>`

func cveSetup(t *testing.T) *inventoryEnv {
	e := inventorySetup(t)
	e.vms(t)
	old := CveData
	CveData = e.dir
	t.Cleanup(func() { CveData = old })
	e.exec(t, `CREATE TABLE cve_findings (id INTEGER PRIMARY KEY, dkey TEXT, vuln_key TEXT, aliases TEXT, kind TEXT,
	  component_name TEXT, ecosystem TEXT, group_key TEXT, env_max_class TEXT, envs TEXT, versions TEXT, targets TEXT,
	  n_targets INTEGER, exposure TEXT, tier INTEGER, title TEXT, severity TEXT, cvss REAL, vector TEXT, url TEXT, published TEXT,
	  fixed_version TEXT, nofix INTEGER, kev INTEGER, kev_due TEXT, ransomware INTEGER, epss REAL, epss_pct REAL, score REAL,
	  priority TEXT, breakdown TEXT, first_seen INTEGER, last_seen INTEGER, state TEXT, sla_due INTEGER, suppressed_by TEXT,
	  notified INTEGER, incident_id INTEGER, task_key TEXT, resolved_at INTEGER)`)
	e.exec(t, `CREATE TABLE cve_targets (finding_id INTEGER, target TEXT, vm_id INTEGER, version TEXT, env TEXT)`)
	e.exec(t, `CREATE TABLE cve_fixes (group_key TEXT PRIMARY KEY, kind TEXT, name TEXT, ecosystem TEXT, env_class TEXT,
	  fixed_version TEXT, md TEXT, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_at INTEGER)`)
	e.exec(t, `CREATE TABLE cve_suppressions (id TEXT PRIMARY KEY, vuln_key TEXT, component TEXT, target TEXT, env TEXT,
	  reason TEXT, expires TEXT, owner TEXT, created_by TEXT, created_at INTEGER, active INTEGER, removed_by TEXT, removed_at INTEGER)`)
	e.exec(t, `CREATE TABLE cve_hosts (name TEXT PRIMARY KEY, vm_id INTEGER, alias TEXT, env TEXT, os TEXT, os_id TEXT,
	  os_version TEXT, kernel TEXT, reboot_required INTEGER, exposure TEXT, tier INTEGER, pkgs_n INTEGER, pkgs_at INTEGER,
	  runtime TEXT, status TEXT, error TEXT, seen INTEGER)`)
	g := "Ubuntu:24.04:LTS|openssl|production"
	ins := `INSERT INTO cve_findings (id, dkey, vuln_key, kind, component_name, ecosystem, group_key, env_max_class, envs,
	  versions, targets, n_targets, exposure, title, severity, cvss, epss, fixed_version, nofix, kev, score, priority, breakdown,
	  first_seen, last_seen, state, sla_due) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	tg := `[{"target": "app-01", "vm_id": 1, "version": "3.0.13-0ubuntu3.4", "env": "release"}]`
	e.exec(t, ins, 1, "a", "CVE-2024-0001", "package", "openssl", "Ubuntu:24.04:LTS", g, "production", `["release"]`,
		`["3.0.13-0ubuntu3.4"]`, tg, 1, "internet", cveHostile, "critical", 9.8, 0.97, "3.0.13-0ubuntu3.5", 0, 1, 15.0, "P1",
		`{"base": [9.8, "CVSS"], "kev": [3, "in CISA KEV"], "fix": [-1, "x"]}`, 1, 2, "open", 1)
	e.exec(t, ins, 2, "b", "CVE-2024-0002", "package", "openssl", "Ubuntu:24.04:LTS", g, "production", `["release"]`,
		`["3.0.13-0ubuntu3.4"]`, tg, 1, "internet", "no fix", "medium", nil, nil, "", 1, 0, 6.0, "P3", `{}`, 1, 2, "open", 4102444800)
	e.exec(t, ins, 3, "c", "EOL-nodejs-14", "runtime", "node", "cpe", "cpe|node|dev", "dev", `["develop"]`, `["14.21.3"]`,
		`[{"target": "`+cveHostile+`", "version": "14.21.3", "env": "develop"}]`, 1, "internal", "node 14 is end-of-life", "", nil,
		nil, "22", 0, 0, 7.0, "P3", `{"base": [7, "end of life"]}`, 1, 2, "open", 4102444800)
	e.exec(t, "INSERT INTO cve_targets VALUES (1, 'app-01', 1, '3.0.13-0ubuntu3.4', 'release'), (2, 'app-01', 1, '3.0.13-0ubuntu3.4', 'release')")
	e.exec(t, `INSERT INTO cve_fixes (group_key, fix_options, fix_prompt, fix_state) VALUES (?, ?, ?, 'ok')`, g,
		`[{"n": 1, "title": "Upgrade openssl", "kind": "permanent fix", "recommended": true}]`, "PRODUCTION CHANGE — x\n# Upgrade")
	e.exec(t, `INSERT INTO cve_suppressions (id, vuln_key, component, target, env, reason, expires, created_by, active)
	  VALUES ('0123456789ab', 'CVE-2024-0002', 'open*', '', '', ?, '2026-12-01', 'alex.doe', 1)`, cveHostile+" reason long enough")
	e.exec(t, `INSERT INTO cve_hosts (name, vm_id, pkgs_n, pkgs_at, reboot_required) VALUES ('app-01', 1, 500, 1, 1)`)
	e.exec(t, `CREATE TABLE cve_affects (group_key TEXT, target TEXT, host TEXT, vm_id INTEGER, env TEXT, service TEXT, unit TEXT,
	  kind TEXT, exposure TEXT, via TEXT, version TEXT, how TEXT, PRIMARY KEY (group_key, target, host, unit))`)
	e.exec(t, `CREATE TABLE cve_service_risk (service TEXT PRIMARY KEY, hosts INTEGER, host_list TEXT, envs TEXT, p1 INTEGER,
	  p2 INTEGER, kev INTEGER, findings INTEGER, groups_n INTEGER, top_group TEXT, top_component TEXT, exposure TEXT, at INTEGER)`)
	aff := `INSERT INTO cve_affects VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	e.exec(t, aff, g, "app-01", "app-01", 1, "release", "website/payment", "payment_multipack.service", "unit", "customer-facing",
		"www.shop.northwind.example /payment/", "3.0.13-0ubuntu3.4", "links libssl3t64")
	e.exec(t, aff, g, "app-01", "app-01", 1, "release", "nginx", "nginx.service", "unit", "internet", "public edge",
		"3.0.13-0ubuntu3.4", "links libssl3t64")
	e.exec(t, aff, g, "app-01", "app-01", 1, "release", cveHostile, "x.service", "unit", "internal", cveHostile,
		"3.0.13-0ubuntu3.4", cveHostile)
	e.exec(t, aff, "cpe|node|dev", cveHostile, "stage3", nil, "develop", "all services", "*", "host", "internal", "", "14.21.3", "kernel")
	e.exec(t, `INSERT INTO cve_service_risk VALUES ('website/payment', 1, '["app-01"]', '["release"]', 1, 0, 1, 2, 1, ?, 'openssl',
	  'customer-facing', 1), (?, 1, '["app-01"]', '["release"]', 0, 1, 0, 1, 1, ?, 'openssl', 'internal', 1),
	  ('quiet', 1, '[]', '[]', 0, 0, 0, 1, 1, '', '', 'internal', 1)`, g, cveHostile, g)
	e.write(t, "cve-status.json", `{"at": 1, "counts": {"P1": 1, "P1_fix": 1, "P2": 0, "P3": 2, "kev": 1, "eol": 1, "components": 2},
	  "coverage": {"hosts": 4, "scanned": 1, "with_access": 2, "components": 900, "kinds": {"package": 880}},
	  "feeds": {"at": 1, "mode": "online", "hosts": {"api.osv.dev": {"ok": true, "n": 3}, "x.example": {"ok": false, "status": 403, "n": 1}},
	  "errors": {"nvd": "`+"\\u003cb\\u003e"+`down"}}, "errors": {}}`)
	return e
}

func TestCveGroupPage(t *testing.T) {
	cveSetup(t)

	name, prompt, ok := fixDownload(py.NewQS("cve", "1"))
	if !ok || name != "argus-fix-cve-1.md" || !strings.HasPrefix(prompt, "PRODUCTION CHANGE") {
		t.Fatal(name, prompt)
	}
}

func TestCveSuppressPost(t *testing.T) {
	e := cveSetup(t)
	ok := py.NewQS("action", "add", "vuln_key", "CVE-2024-0002", "component", "openssl", "reason", "  no fix from   Ubuntu yet, accepted ",
		"expires", "2026-12-01", "back", "Ubuntu:24.04:LTS|openssl|production")
	if c, _ := CvePostSuppress(nil, ok, inventoryViewer()); c != 403 {
		t.Fatal("viewer")
	}
	for k, v := range map[string]string{"reason": "short", "component": "a;b", "expires": "x", "back": "../x"} {
		f := py.NewQS()
		for _, kk := range ok.Keys() {
			f.Set(kk, ok.Get(kk))
		}
		f.Set(k, v)
		if c, _ := CvePostSuppress(nil, f, inventoryAdmin()); c != 400 {
			t.Fatalf("%s accepted", k)
		}
	}
	c, loc := CvePostSuppress(nil, ok, inventoryAdmin())
	if c != 303 || !strings.Contains(loc, "sent=suppress") {
		t.Fatal(c, loc)
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "in", "cve-suppressions", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), `"reason": "no fix from Ubuntu yet, accepted"`) || !strings.Contains(string(b), `"role": "admin"`) {
		t.Fatal(string(b))
	}
}

func TestCveVMCardAndCell(t *testing.T) {
	cveSetup(t)

	m := CveVMCounts()
	if cell := cveLine(m, 1); !strings.Contains(cell, "worst P1") || !strings.Contains(cell, "2 open · 1 KEV") {
		t.Fatal(cell)
	}
	if cveLine(m, 2) != "-" {
		t.Fatal("no findings")
	}

}

func cveContainersSetup(t *testing.T) *inventoryEnv {
	e := cveSetup(t)
	e.exec(t, `CREATE TABLE cve_containers (host TEXT, vm_id INTEGER, env TEXT, dc TEXT, name TEXT, service TEXT, image TEXT,
	  source TEXT, instance TEXT, digest TEXT, analysis TEXT, evidence TEXT, seen INTEGER, PRIMARY KEY (host, name, image))`)
	e.exec(t, `CREATE TABLE cve_blind_spots (kind TEXT, subject TEXT, host TEXT, vm_id INTEGER, env TEXT, reason TEXT, seen INTEGER,
	  PRIMARY KEY (kind, subject))`)
	ins := `INSERT INTO cve_containers VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1)`
	e.exec(t, ins, "app-01", 2, "release", "dc-west", "matomo", "matomo", "hub.northwind.example/matomo:5.10-apache", "declared", "",
		"sha256:ca91275ffc6aa442e711bfa85ada0cff2cb6d26f0e07071fcae296b4d6cc1d71", "ok", cveHostile)
	e.exec(t, ins, "dc-west:cadvisor:8080", nil, "release", "dc-west", cveHostile, cveHostile, "prom/prometheus:v3.7.0",
		"cadvisor:dc-west", "cadvisor:8080", "", "failed", "")
	bs := `INSERT INTO cve_blind_spots VALUES (?,?,?,?,?,?,1)`
	e.exec(t, bs, "no-evidence", "SRV-NW-06-50-life-matomo-app1", "SRV-NW-06-50-life-matomo-app1", 2, "release", "no ssh-ro alias")
	e.exec(t, bs, "cadvisor-no-labels", "dc-west:life-matomo-app2", "x", nil, "release", cveHostile)
	e.exec(t, bs, "image-not-analysed", "prom/prometheus:v3.7.0", "dc-west:cadvisor:8080", nil, "release", "authentication required")
	g := "Debian:13|php|production"
	e.exec(t, `INSERT INTO cve_findings (id, dkey, vuln_key, kind, component_name, ecosystem, group_key, env_max_class, envs,
	  versions, targets, n_targets, exposure, title, severity, cvss, fixed_version, nofix, kev, score, priority, breakdown,
	  first_seen, last_seen, state, sla_due) VALUES (9, 'z', 'CVE-2026-1111', 'runtime', 'php', 'cpe', ?, 'production', '["release"]',
	  '["8.4.22"]', '[{"target": "hub.northwind.example/matomo:5.10-apache", "kind": "image"}]', 1, 'internet', 'php bug', 'high', 8.1,
	  '8.4.23', 0, 0, 12.0, 'P1', '{}', 1, 2, 'open', 4102444800)`, g)
	e.exec(t, "INSERT INTO cve_targets VALUES (9, 'hub.northwind.example/matomo:5.10-apache', NULL, '8.4.22', 'release')")
	e.exec(t, `INSERT INTO cve_affects VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, g, "hub.northwind.example/matomo:5.10-apache", "app-01", 2,
		"release", "matomo", "container:matomo", "container", "internet", "", "8.4.22", "runs this image (declared in config, not observed)")
	e.write(t, "cve-status.json", `{"at": 1, "counts": {"P1": 2}, "coverage": {"hosts": 4, "scanned": 1, "components": 900,
	  "containers": {"hosts_with_packages": 1, "hosts_with_containers": 1, "hosts_with_containers_observed": 0, "images": 2,
	  "images_analysed": 1, "images_failed": 1}}, "feeds": {}}`)
	return e
}

func TestCveVMCardContainers(t *testing.T) {
	cveContainersSetup(t)

	if c := cveLine(CveVMCounts(), 2); !strings.Contains(c, "worst P1") {
		t.Fatal("inventory column counts container findings:", c)
	}
}

// Failed feed queries are loud on the JSON too: the page must not read as whole when a feed was not read.
func TestAPIv1VulnsFeedQueriesFailedAreLoud(t *testing.T) {
	e := cveSetup(t)
	e.write(t, "cve-status.json", `{"at": 1, "counts": {"P1": 0}, "coverage": {"hosts": 1},
	  "feeds": {"at": 1, "mode": "online", "hosts": {}, "errors": {}, "queries": {"ok": 12, "cached": 3, "stale": 1, "failed": 2}},
	  "feed_problems": {"failed": 2, "partial": 1, "unparsable": 1, "list": [
	    {"kind": "feed-failed", "subject": "nvd: cpe:2.3:a:php:php", "reason": "partial: 500 of 749"}]},
	  "errors": {}}`)
	v, er := apiVulnsJSON(py.QS{}, inventoryViewer())
	if er != nil {
		t.Fatal(er.msg)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), "feed queries failed: 2 (1 partial, 1 unparsable cache file)") {
		t.Fatalf("%.300s", b)
	}
}
