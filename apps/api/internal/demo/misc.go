package demo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sinaneysi/argus/internal/py"
	"golang.org/x/crypto/scrypt"
)

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (g *Gen) fixer() {
	o := g.ops
	rCpn := g.oneInt(o, "SELECT run_id FROM triage_problems WHERE title LIKE 'staging checkout%'")
	rDev := g.oneInt(o, "SELECT run_id FROM triage_problems WHERE title LIKE 'dev-runner-01%'")
	for _, r := range []struct {
		rid, by                     string
		ago                         float64
		env                         string
		hosts                       []string
		prun                        int64
		title, st, outcome, summary string
	}{
		{"20261024103011-a1b2c3d4", "taylor.noe", 14 * H, "pre-release", sl("pre-stg-app-01-fix"), rCpn,
			"staging checkout: 500 on apply-coupon", "OK", "fixed",
			"Rolled coupon-service back to 3.8.2 on stg-app-01; apply-coupon answers 200; CPN-12 passes."},
		{"20261021080512-0f0e0d0c", "drew.foe", 3*D - 2*H, "develop", sl("dev-runner-01-fix"), rDev,
			"dev-runner-01 disk 88 %", "OK", "fixed", "Pruned 412 images (91 GB); weekly timer docker-prune.timer installed."},
	} {
		g.ins(o, "fix_runs", "id", r.rid, "requested_by", r.by, "requested_at", g.t(r.ago), "env", r.env, "hosts", jd(r.hosts), "problem_run", r.prun,
			"problem_idx", 0, "problem_title", r.title, "prompt_sha256", sha256hex(r.title), "prompt", "(the reviewed prompt)",
			"started_at", g.t(r.ago-60), "finished_at", g.t(r.ago-9*M), "status", r.st, "outcome", r.outcome, "summary", r.summary,
			"report", "## Report\n\n"+r.summary+"\n", "actions", jd(sl("backup taken", "change applied", "verified")),
			"backups", jd(sl("/var/backups/argus/"+r.rid)), "verification", jd(sl("health 200")), "rollback", jd(sl("redeploy the previous tag")),
			"cost_usd", 0.38, "turns", 19, "denials", 0, "unit_result", "success")
	}
	g.writeJSON("settings-out/fix-scope.json", om("set_up", true, "envs", sl("develop", "pre-release"), "missing", L(),
		"hosts", om("develop", sl("dev-swarm-01-fix", "dev-swarm-02-fix", "dev-runner-01-fix", "dev-db-01-fix"),
			"pre-release", sl("pre-stg-app-01-fix", "pre-stg-app-02-fix")),
		"ignored", sl("release")))
	g.writeJSON("settings-out/fix-status.json", om("runs", om()))
	g.writeText("settings-out/fix-preamble.md", "You are the fixer for Northwind Commerce's develop and staging hosts. "+
		"Never touch production. Back up before every change.\n")
}

// kubeStatus is Settings -> Kubernetes: one read-only cluster the agent may read, one refused (it can write).
func (g *Gen) kubeStatus() {
	test := func(ver string, ns int) *py.OMap {
		return om("result", "ok", "server_version", ver, "namespaces", ns, "at", g.t(3*H), "by", "alex.doe")
	}
	g.writeJSON("settings-out/kube-status.json", om(
		"kubectl", om("installed", true, "verified", true, "version", "v1.34.13", "path", "/usr/local/lib/argus/kubectl"),
		"clusters", om(
			"stage", om("env", "develop", "context", "stage-ro", "cluster", "stage", "user", "argus-ro",
				"server_host", "k8s-stage.northwind.example:6443", "namespace", "shop", "auth", "token", "insecure", false,
				"verdict", "read-only", "findings", L(), "enabled", true, "override", false, "why", "read-only",
				"set_by", "alex.doe", "set_at", g.t(2*D), "test", test("v1.30.4", 14)),
			"tools", om("env", "pre-release", "context", "tools-admin", "cluster", "tools", "user", "tools-admin",
				"server_host", "k8s-tools.northwind.example:6443", "namespace", "", "auth", "certificate", "insecure", false,
				"verdict", "writes", "findings", L("can create/delete/patch/update deployments.apps", "can exec into pods"),
				"enabled", false, "override", false, "why", "not read-only", "set_by", "sam.roe", "set_at", g.t(5*H),
				"test", test("v1.29.8", 6))),
		"results", om(), "at", g.t(3*H)))
}

func (g *Gen) gitlabIssues() {
	grp := "northwind/ops"
	g.writeJSON("settings-out/gitlab-issues-status.json", om(
		"connected", true, "last_run", g.t(2*M), "warnings", L(), "allowed_hosts", sl("gitlab.northwind.example"),
		"connection", om("mode", "group", "url", GITLAB, "group_id", 50, "group_path", grp, "web_url", GITLAB+"/groups/"+grp,
			"board_id", 9, "board_name", "Ops", "bot", om("username", "group_50_bot", "bot", true),
			"connected_at", g.t(6*D), "by", "alex.doe", "default_project_id", 71,
			"default_project_path", grp+"/infra"),
		"projects", L(om("id", 71, "path", grp+"/infra", "short", "infra"),
			om("id", 72, "path", grp+"/ops-tasks", "short", "ops-tasks")),
		"lists", L(om("label", "Doing", "column", "progress"), om("label", "Review", "column", "review"),
			om("label", "Fixer", "column", "fixer")),
		"settings", om("default_tracker", "jira", "user_map", "alex=alex.doe\nsam=sam.roe")))
	for _, r := range []struct {
		tid, pid         int
		short            string
		iid              int
		title, status    string
		gstat, who, prio string
		env              string
		labels           []string
		ago              float64
	}{
		{16, 71, "infra", 45, "Rotate the Grafana admin password into the vault", "progress", "Doing", "sam.roe", "Medium",
			"release", sl("security"), 5 * H},
		{17, 72, "ops-tasks", 12, "Runner cache volume on dev-runner-01 to 100 GB", "todo", "To Do", "drew.foe", "Low",
			"develop", sl("cicd"), 26 * H},
		{18, 71, "infra", 47, "Staging: TLS 1.3 only on stg-edge-01", "review", "Review", "robin.moe", "Medium",
			"pre-release", sl("network"), 3 * H},
	} {
		url := fmt.Sprintf("%s/northwind/ops/%s/-/issues/%d", GITLAB, r.short, r.iid)
		g.ins(g.board, "tasks", "id", r.tid, "tracker", "gitlab", "gl_project", r.pid, "gl_iid", r.iid, "gl_url", url, "gl_ref", fmt.Sprintf("%s#%d", r.short, r.iid),
			"title", r.title, "description", r.title+".", "evidence", "", "fix_prompt", "", "status", r.status, "jira_status", r.gstat, "assignee", r.who,
			"assignee_name", nameOf(r.who), "priority", r.prio, "labels", jd(r.labels), "env", r.env, "host", "", "created_by", r.who,
			"requested_by", r.who, "created_at", g.t(r.ago+2*D), "updated_at", g.t(r.ago), "jira_updated", g.t(r.ago), "synced_at", g.t(2*M),
			"gone", 0)
		g.ins(g.board, "events", "task_id", r.tid, "at", g.t(r.ago+2*D), "kind", "created", "actor", r.who, "text", "created in GitLab")
	}
}

func (g *Gen) credentials() {
	now := g.t(25 * M)
	item := func(id, name, kind, usedBy, where, status, level, band string, days any, detail string, howto []string, kw ...any) *py.OMap {
		var exp any
		if d, ok := days.(int); ok {
			exp = g.day(-float64(d) * D)
		}
		return om(append([]any{"id", id, "name", name, "kind_label", kind, "used_by", usedBy, "where", where, "status", status, "level", level, "band", band,
			"days_left", days, "expires_at", exp, "detail", detail, "checked_at", now, "howto", howto}, kw...)...)
	}
	g.writeJSON("settings-out/credentials.json", om("checked_at", now, "items", L(
		item("gitlab-ro", "GitLab read-only", "GitLab group access token", "CI/CD, Check a repo", "/etc/argus/gitlab-ro.token",
			"crit", "WARN", "crit", 6, "expires in 6 d", sl("Rotate it in GitLab (group northwind, Access tokens).",
				"Store it in /etc/argus/gitlab-ro.token"),
			"expiry_source", "api", "identity", "group_40_bot", "created_at", g.t(359*D), "created_source", "api", "last_used", g.t(4*M)),
		item("gitlab-issues", "GitLab issues", "GitLab group access token", "GitLab issues board sync",
			"/etc/argus/gitlab-issues.token", "ok", "OK", "ok", 171, "expires in 171 d", sl("Rotate it in GitLab."),
			"expiry_source", "api", "identity", "group_50_bot", "last_used", g.t(2*M)),
		item("jira", "Jira bot", "API token", "Board, Proposed tasks", "/etc/argus/jira.token", "warn", "WARN", "warn", 24,
			"expires in 24 d", sl("Create a new API token for argus-bot.", "Store it in /etc/argus/jira.token"),
			"expiry_source", "api", "identity", "argus-bot", "last_used", g.t(2*M)),
		item("matrix", "Matrix bot", "access token", "Chat triage, notifications", "/etc/argus/matrix.token", "ok", "OK", "never",
			nil, "never expires", sl("Log the bot in again to rotate."), "identity", "@argus:"+MATRIX, "last_used", g.t(M)),
		item("nvd", "NVD API key", "API key", "CVE feeds", "/etc/argus/nvd-api-key", "rotate", "WARN", "unknown", nil,
			"no expiry info; set 200 d ago, rotate by "+g.day(20*D)+" (overdue)", sl("Request a new key from NVD."),
			"rotate_due", g.day(20*D), "rotate_days", 180, "created_at", g.t(200*D), "created_source", "file"),
		item("backup-templates", "GitLab backup plans", "GitLab project access token", "New backup",
			"/etc/argus/backup-templates.token", "ok", "OK", "ok", 88, "expires in 88 d", sl("Rotate it in GitLab."),
			"expiry_source", "api", "identity", "project_88_bot", "last_used", g.t(3*D)))))
}

// outlinePages is Settings -> Integrations: Outline connected in preview mode, three pages built.
func (g *Gen) outlinePages() {
	g.writeJSON("settings-out/outline-status.json", om("connection", om("url", "https://docs.northwind.example", "user", "argus-bot", "team", "Northwind",
		"collection", "Services", "connected_at", g.t(9*D), "by", "alex.doe"), "last_run", om("at", g.t(5*H), "counts", om()),
		"pages", om("shop/checkout", om("state", "unchanged", "url", "https://docs.northwind.example/doc/checkout")),
		"settings", om("default_url", "https://docs.northwind.example", "mode", "preview", "scope", "pilot", "collection", "Services"),
		"allowed_hosts", sl("docs.northwind.example")))
	page := func(svc, title, prev string) {
		body := "## " + title + "\n\nOwned by the shop team. See the [runbook](https://docs.northwind.example/doc/runbook).\n\n- Tier: 1\n- Deploys through `platform/deployment`\n"
		g.ins(g.ops, "outline_pages", "service", svc, "grp", "shop", "title", title, "body", body, "body_hash", "9f3c"+svc[len(svc)-2:], "prev_body", iif(prev != "", prev, nil),
			"prev_hash", iif(prev != "", "71ab", nil), "rendered_at", g.t(5*H), "changed_at", g.t(5*H), "redacted", "{}")
	}
	page("_index", "Services overview", "")
	page("shop/checkout", "Checkout", "## Checkout\n\nOwned by the web team.\n\n- Tier: 1\n")
	page("shop/search", "Search", "")
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func (g *Gen) stateFiles() {
	g.writeJSON("selfcheck.json", om("status", "OK", "ts", g.t(3*M), "kuma", "ok (pushed 3 min ago)", "checks", L(
		om("name", "ops-watch", "group", "collectors", "status", "OK", "detail", "listening to 3 rooms"),
		om("name", "log-rca", "group", "model tasks", "status", "OK", "detail", "last run 2 h ago"),
		om("name", "alerts snapshot", "group", "collectors", "status", "OK", "detail", "20 s old"),
		om("name", "board sync", "group", "dispatchers", "status", "OK", "detail", "2 min ago"),
		om("name", "cve scan", "group", "collectors", "status", "OK", "detail", "3 h ago"),
		om("name", "disk /var/lib/argus", "group", "host", "status", "OK", "detail", "31 % used"))))
	g.writeJSON("cred-status.json", om("at", g.t(20*M), "creds", om(
		"gitlab-ro", om("ok", true, "detail", "Reporter on 31 projects", "expires", g.day(-200*D)),
		"grafana-east", om("ok", true, "detail", "viewer"), "grafana-west", om("ok", true, "detail", "viewer"),
		"minio-ro", om("ok", true, "detail", "list + read on backup"), "matrix", om("ok", true, "detail", "logged in"),
		"jira-bot", om("ok", true, "detail", "OPS project"))))
	g.writeJSON("matrix-dm-bot.json", om("bot", "@argus:"+MATRIX, "at", g.t(5*M), "ok", true))
	tasks := om()
	for _, t := range []string{"backup-check", "cert-check", "log-rca", "chat-triage", "insights", "inventory", "cve"} {
		tasks.Set(t, om("result", "ok", "requested", g.t(26*H), "started", g.t(26*H)))
	}
	g.writeJSON("settings-out/run-requests.json", om("tasks", tasks, "repocheck", om()))
	g.writeJSON("settings-out/applied.json", om("at", g.t(2*D), "by", "sam.roe", "ok", true, "changed", sl("notify.quiet_start")))
	g.outlinePages()
	for _, d := range []string{"today-actions", "problem-actions", "idea-actions", "incident-actions", "board-requests", "backup-requests",
		"requests", "fix-requests", "cve-suppressions"} {
		if err := os.MkdirAll(filepath.Join(g.out, "settings-in", d), 0o755); err != nil {
			panic(err)
		}
	}
	g.writeJSON("settings-in/settings.json", om())
}

// settingsOut stores the fix handoffs (options and prompts) on the rows that have an RCA.
func (g *Gen) settingsOut() {
	o := g.ops
	for _, x := range []struct {
		rid, iid int
		env      string
	}{{51, 141, "release"}, {52, 145, "release"}} {
		md := g.oneStr(o, "SELECT content FROM rca_reports WHERE id=?", x.rid)
		g.storeHandoff("rca_reports", "id=?", L(x.rid), md, x.env, "rca-deep", nil)
		g.storeHandoff("incidents", "id=?", L(x.iid), md, x.env, "rca-deep", fmt.Sprintf("rca:%d", x.rid))
	}
	md := g.oneStr(o, "SELECT content FROM rca_reports WHERE id=51")
	g.storeHandoff("problems", "id=7", nil, md, "release", "rca-deep", "rca:51")
	type tp struct {
		run, idx int64
		md, env  string
	}
	var list []tp
	rows, err := o.Query("SELECT run_id, idx, rca, environment FROM triage_problems")
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var x tp
		var md, env any
		if err := rows.Scan(&x.run, &x.idx, &md, &env); err != nil {
			panic(err)
		}
		x.md, _ = md.(string)
		x.env, _ = env.(string)
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if x.md != "" && bytes.Contains([]byte(x.md), []byte("## How to fix it")) {
			g.storeHandoff("triage_problems", "run_id=? AND idx=?", L(x.run, x.idx), x.md, x.env, "incident", nil)
		}
	}
}

// auth writes auth.db: the two demo users and four people (who cannot sign in), 2FA rows and an audit trail.
func (g *Gen) auth() {
	path := filepath.Join(g.authDir, "auth.db")
	if err := os.MkdirAll(g.authDir, 0o755); err != nil {
		panic(err)
	}
	os.Remove(path)
	db, t := openDB(path)
	g.exec(t, `
CREATE TABLE IF NOT EXISTS users (username TEXT PRIMARY KEY, hash TEXT NOT NULL, role TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0, must_change INTEGER NOT NULL DEFAULT 0, created_at INTEGER);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL,
  created INTEGER NOT NULL, last_seen INTEGER NOT NULL, csrf TEXT NOT NULL, id_token TEXT);
CREATE TABLE IF NOT EXISTS failures (key TEXT PRIMARY KEY, count INTEGER NOT NULL, until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_meta (username TEXT PRIMARY KEY, source TEXT NOT NULL DEFAULT 'local', sub TEXT,
  last_login INTEGER);
CREATE TABLE IF NOT EXISTS oidc_states (state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, verifier TEXT NOT NULL,
  next TEXT NOT NULL, created INTEGER NOT NULL);
`)
	rng := newPyRand(g.seed * 7919) // salts are seeded: the demo must be reproducible
	users := [][2]string{{DEMO_VIEWER, "viewer"}, {DEMO_ADMIN, "admin"}}
	for _, p := range PEOPLE[:4] {
		users = append(users, [2]string{p.user, "viewer"})
	}
	for _, u := range users {
		salt := make([]byte, 16)
		for i := range salt {
			salt[i] = byte(rng.getrandbits(8))
		}
		pw := "not-a-login-" + h12(fmt.Sprintf("%s%d", u[0], g.seed))
		if u[0] == DEMO_VIEWER || u[0] == DEMO_ADMIN {
			pw = DEMO_PASSWORD
		}
		dk, err := scrypt.Key([]byte(pw), salt, 1<<14, 8, 1, 32)
		if err != nil {
			panic(err)
		}
		g.exec(t, "INSERT INTO users VALUES (?,?,?,0,0,?)", u[0], fmt.Sprintf("scrypt$16384$8$1$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(dk)),
			u[1], g.t(30*D))
		g.exec(t, "INSERT INTO user_meta (username, source, last_login) VALUES (?, 'local', ?)", u[0], g.t(D))
	}
	// 2FA: two people have an authenticator (a placeholder box: these accounts cannot sign in), the audit trail
	g.exec(t, schema("schema_mfa.sql"))
	for _, m := range []struct {
		user string
		ago  float64
	}{{"alex.doe", 12 * D}, {"sam.roe", 3 * D}} {
		g.exec(t, "INSERT INTO mfa (username, secret_enc, enabled, enabled_at, last_used) VALUES (?, 'demo-placeholder', 1, ?, ?)",
			m.user, g.t(m.ago), g.t(5*H))
		for k := 0; k < 8; k++ {
			g.exec(t, "INSERT INTO mfa_recovery VALUES (?,?,?,NULL)", m.user, h12(fmt.Sprintf("%s|%d|%d", m.user, k, g.seed)), g.t(m.ago))
		}
	}
	for _, a := range []struct {
		ago                                            float64
		actor, action, target, detail, method, outcome string
	}{
		{12 * D, "alex.doe", "mfa.enrol", "alex.doe", "authenticator app", "password", "ok"},
		{3 * D, "sam.roe", "mfa.enrol", "sam.roe", "authenticator app", "password", "ok"},
		{2 * D, "sam.roe", "mfa.recovery.used", "sam.roe", "8 codes left", "recovery", "ok"},
		{26 * H, "jamie.poe", "login", "jamie.poe", "wrong password", "password", "failed"},
		{5 * H, "alex.doe", "login", "alex.doe", "", "totp", "ok"},
	} {
		g.exec(t, "INSERT INTO auth_audit (at, actor, action, target, detail, ip, method, outcome) VALUES (?,?,?,?,?,?,?,?)",
			g.t(a.ago), a.actor, a.action, a.target, a.detail, "10.20.10.25", a.method, a.outcome)
	}
	if err := t.Commit(); err != nil {
		panic(err)
	}
	db.Close()
}

// sourcesStatus is Settings -> Data sources (ADR-0017): several instances per type, every state the page shows. The
// GitLab tracker and the Kubernetes clusters are mirrored from their own pages (managed "legacy"); none holds a secret.
func (g *Gen) sourcesStatus() {
	src := func(typ, name, env, url, purpose, status, detail, identity string, enabled bool, ago float64, kv ...any) *py.OMap {
		e := om("type", typ, "name", name, "env", env, "url", url, "purpose", purpose, "status", status, "detail", detail,
			"identity", identity, "findings", L(), "facts", om(), "enabled", enabled, "override", false, "override_requested", false,
			"managed", "registry", "created_by", "alex.doe", "created_at", g.t(9*D))
		if ago > 0 {
			e.Set("last_check", g.t(ago))
			e.Set("checked_by", "timer")
		}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Set(kv[i].(string), kv[i+1])
		}
		return e
	}
	types := om()
	for _, t := range []struct {
		key, label string
		purposes   []string
		user       bool
		managed    string
	}{{"elasticsearch", "Elasticsearch / OpenSearch", sl("agent"), true, ""}, {"gitlab", "GitLab", sl("agent", "tracker"), false, ""},
		{"grafana", "Grafana", sl("agent"), false, ""}, {"kubernetes", "Kubernetes", sl("agent"), false, "/admin/kube"},
		{"outline", "Outline", sl("docs", "agent"), false, ""}, {"sentry", "Sentry", sl("agent"), false, ""}} {
		types.Set(t.key, om("label", t.label, "purposes", t.purposes, "username", t.user, "managed", t.managed))
	}
	g.writeJSON("settings-out/sources-status.json", om(
		"sources", om(
			"elasticsearch/logs", src("elasticsearch", "logs", "release", "https://logs.northwind.example:9200", "agent", "readonly",
				"read-only", "argus-reader", true, 2*H, "username", "argus-reader", "cred_last4", "r8Kq",
				"facts", om("flavour", "elasticsearch", "version", "8.14.3", "cluster", "nw-logs", "read_all_indices", true)),
			"gitlab/main", src("gitlab", "main", "shared", GITLAB, "agent", "readonly", "read-only: scopes read_api, read_repository",
				"argus-ro", true, 2*H, "cred_last4", "9fQe", "facts", om("scopes", sl("read_api", "read_repository"), "expires_at", "2027-06-30")),
			"gitlab/tracker", src("gitlab", "tracker", "shared", GITLAB, "tracker", "ok", "works as the issue tracker (scope api)",
				"group_50_bot", false, 2*H, "managed", "legacy", "legacy", "gitlab-tracker", "cred_ref", "etc:gitlab-issues.token",
				"cred_where", "Settings → Integrations → GitLab issues", "manage", "/admin/outline", "used_by", "board sync (GitLab issues)",
				"created_by", "migration"),
			"grafana/east", src("grafana", "east", "release", "https://grafana-east.northwind.example", "agent", "readonly",
				"read-only: role Viewer, 2 loki, 1 other, 3 prometheus", "sa-argus-viewer", true, 2*H, "cred_last4", "Vw2x",
				"facts", om("role", "Viewer", "datasources", om("loki", 2, "other", 1, "prometheus", 3))),
			"grafana/west", src("grafana", "west", "release", "https://grafana-west.northwind.example", "agent", "refused",
				"this login can write (role Editor); the agent needs a read-only one", "sa-argus", false, 2*H, "cred_last4", "Ed4t",
				"findings", L("role Editor"), "facts", om("role", "Editor")),
			"grafana/lab", src("grafana", "lab", "develop", "https://grafana.lab.northwind.example", "agent", "unreachable",
				"no answer within 10 s", "", false, 2*H, "cred_last4", "Lb7m"),
			"kubernetes/stage", src("kubernetes", "stage", "develop", "https://k8s-stage.northwind.example:6443", "agent", "readonly",
				"read-only: get, list and watch", "argus-ro", true, 3*H, "managed", "legacy", "legacy", "kube-stage", "cred_ref", "kube:stage",
				"cred_where", "Settings → Kubernetes", "manage", "/admin/kube", "used_by", "argus tools kube stage", "created_by", "alex.doe",
				"facts", om("context", "stage-ro")),
			"kubernetes/tools", src("kubernetes", "tools", "pre-release", "https://k8s-tools.northwind.example:6443", "agent", "refused",
				"this kubeconfig can write", "tools-admin", false, 3*H, "managed", "legacy", "legacy", "kube-tools", "cred_ref", "kube:tools",
				"cred_where", "Settings → Kubernetes", "manage", "/admin/kube", "used_by", "argus tools kube tools", "created_by", "sam.roe",
				"findings", L("can create/delete/patch/update deployments.apps", "can exec into pods"), "facts", om("context", "tools-admin")),
			"outline/wiki", src("outline", "wiki", "shared", "https://docs.northwind.example", "docs", "ok",
				"publishes as Argus (role member)", "Argus", false, 2*H, "cred_last4", "Ol3p", "facts", om("role", "member", "team", "Northwind")),
			"sentry/errors", src("sentry", "errors", "release", "https://sentry.northwind.example", "agent", "failed",
				"the login was refused (HTTP 401); replace the credential", "", false, 2*H, "cred_last4", "Sn0w")),
		"types", types, "results", om(), "at", g.t(2*H)))
}
