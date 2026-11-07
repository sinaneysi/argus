package demo

import (
	"fmt"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

type task struct {
	key, title, status, jstat string
	who                       any
	prio                      string
	env                       any
	host                      string
	sk, sid                   any
	labels                    []string
	col                       int
}

func (g *Gen) boardData() {
	b := g.board
	tasks := []task{
		{"OPS-231", "sms-otp: provider timeout and automatic failover", "progress", "In Progress", "alex.doe", "Highest", "release",
			"prod-pay-01", "incident", "141", sl("argus", "payments"), 1},
		{"OPS-232", "Restore shop-front :3001 env file on prod-shop-02", "todo", "To Do", "jamie.poe", "High", "release",
			"prod-shop-02", "rca", "52-1", sl("argus", "web"), 0},
		{"OPS-229", "Kernel reboot wave for CVE-2026-31337 (21 hosts)", "todo", "To Do", "robin.moe", "High", "release", "",
			"incident", "151", sl("security", "argus"), 0},
		{"OPS-226", "Loki: checkout LOG_LEVEL=info, retention 21 days", "review", "In Review", "sam.roe", "High", "release",
			"prod-mon-01", "disk", h12("east|prod-mon-01|/var/lib/loki"), sl("argus", "observability"), 2},
		{"OPS-225", "Staging: roll coupon-service back to 3.8.2", "fixer", "Ready for fixer", "taylor.noe", "Medium", "pre-release",
			"stg-app-01", "incident", "148", sl("argus", "argus-fixer"), 3},
		{"OPS-222", "Rebuild analytics-matomo on PHP 8.3", "todo", "To Do", "casey.loe", "High", "release", "prod-matomo-01",
			nil, nil, sl("security"), 0},
		{"OPS-221", "Checkout button misaligned on iPhone SE", "todo", "To Do", nil, "Low", nil, "", nil, nil, sl("qa-report", "frontend"), 0},
		{"OPS-219", "Prune docker images on dev-runner-01 weekly", "done", "Done", "drew.foe", "Low", "develop", "dev-runner-01",
			"incident", "149", sl("argus"), 4},
		{"OPS-214", "Search: blue/green re-index behind an alias", "progress", "In Progress", "jamie.poe", "Medium", "release",
			"prod-search-01", "rca", "49-1", sl("search"), 1},
		{"OPS-211", "Mongo backup user lost its role after the password rotation", "progress", "In Progress", "sam.roe", "High",
			"release", "prod-mongo-02", "backup", "1-1", sl("backup"), 1},
		{"OPS-208", "warehouse-api: end the stuck migration and retry the release deploy", "todo", "To Do", "morgan.koe", "High",
			"release", "prod-db-01", "cicd", "701", sl("cicd", "argus"), 0},
		{"OPS-205", "payment-gateway: mask and protect PAYMENT_API_KEY", "review", "In Review", "alex.doe", "Highest", "release", "",
			"cicd", "706", sl("security", "cicd"), 2},
		{"OPS-198", "Renew sellers.northwind.example (DNS-01 hook token)", "todo", "To Do", "casey.loe", "High", "release", "",
			nil, nil, sl("certificates"), 0},
		{"OPS-193", "Back up Redis cart and rate-limit data", "todo", "Backlog", nil, "Medium", "release", "prod-redis-01",
			"backup", "2-1", sl("backup"), 0},
		{"OPS-190", "Alert rule SmsProviderSlow and SmsOtpWorkersSaturated", "done", "Done", "alex.doe", "Medium", "release", "",
			"rca", "51-3", sl("observability"), 4},
		{"OPS-238", "Staging: move coupon-service health check to /healthz", "progress", "In Progress", "alex.doe", "Medium",
			"pre-release", "stg-app-01", nil, nil, sl("staging"), 1},
	}
	jira := "https://jira.northwind.example"
	orStr := func(v any, def string) any {
		if v == nil {
			return def
		}
		return v
	}
	for idx, t := range tasks {
		k := idx + 1
		ago := float64(k*7+2) * H
		if t.key == "OPS-238" {
			k = 30 // ids 16-18 are the GitLab tasks
		}
		fpv := ""
		if sid, ok := t.sid.(string); ok && t.sk == "incident" && (sid == "141" || sid == "148" || sid == "149") {
			n := 0
			fmt.Sscan(sid, &n)
			fpv = g.oneStr(g.ops, "SELECT p.fix_prompt FROM incident_events e JOIN triage_problems p ON p.run_id=e.run_id "+
				"AND p.idx=e.idx WHERE e.incident_id=? AND e.kind='finding' AND e.idx IS NOT NULL LIMIT 1", n)
		}
		hasSK := t.sk != nil
		desc, evidence := t.title+".", ""
		if hasSK {
			desc, evidence = t.title+".\n\nCreated from Argus.", "See the linked Argus page."
		}
		var srcURL any
		if t.sk == "incident" {
			srcURL = fmt.Sprintf("/incidents/view?id=%v", t.sid)
		}
		createdBy := orStr(t.who, "sam.roe")
		if contains(t.labels, "argus") {
			createdBy = "argus-auto"
		}
		g.ins(b, "tasks", "id", k, "jira_key", t.key, "title", t.title, "description", desc,
			"evidence", evidence, "fix_prompt", fpv, "status", t.status, "jira_status", t.jstat,
			"assignee", t.who, "assignee_name", nameOf(t.who), "priority", t.prio, "labels", jd(t.labels), "env", t.env, "host", t.host,
			"source_kind", t.sk, "source_id", t.sid, "source_url", srcURL,
			"created_by", createdBy, "requested_by", orStr(t.who, "sam.roe"),
			"created_at", g.t(ago+2*D), "updated_at", g.t(ago), "jira_updated", g.t(ago), "synced_at", g.t(2*M),
			"fix_request", nil, "fix_state", iif(t.key == "OPS-225", "requested", nil), "gone", 0, "jira_url", jira+"/browse/"+t.key)
		from := "the form"
		if hasSK {
			from = t.sk.(string)
		}
		g.ins(b, "events", "task_id", k, "at", g.t(ago+2*D), "kind", "created", "actor", "argus", "text", "created from "+from)
		if t.status != "todo" {
			g.ins(b, "events", "task_id", k, "at", g.t(ago), "kind", "status", "actor", orStr(t.who, "sam.roe"), "text", "To Do -> "+t.jstat)
		}
	}
	g.ins(b, "comments", "task_id", 1, "jira_id", "10431", "author", "Alex Doe", "body", "Fabrikam is primary for now (flag). 2.14.0 is on staging since Monday; I'll ask for the prod window tonight.",
		"created_at", g.t(1.5*H))
	g.ins(b, "comments", "task_id", 1, "jira_id", "10432", "author", "Sam Roe",
		"body", "Approved for 21:00 UTC, one host at a time please.", "created_at", g.t(1*H))
	g.ins(b, "comments", "task_id", 4, "jira_id", "10420", "author", "Sam Roe",
		"body", "Retention change is in the MR; checkout log level waits for the release train.", "created_at", g.t(9*H))
	col := func(name string, extra ...any) *py.OMap {
		return om(append([]any{"name", name, "statuses", L(om("name", name))}, extra...)...)
	}
	cols := L(col("Backlog"), col("To Do"), col("In Progress", "max", 6), col("In Review"), col("Ready for fixer"), col("Done"))
	colof := map[string]int{}
	for i, c := range cols {
		colof[c.(*py.OMap).S("name")] = i
	}
	for rank, t := range tasks {
		c, ok := colof[t.jstat]
		if !ok {
			c = 1
		}
		g.ins(b, "board_issues", "view", "board", "key", t.key, "rank", rank, "col", c, "summary", t.title, "status_id", fmt.Sprint(10000+rank),
			"status", t.jstat, "type", iif(t.sk == "incident", "Bug", "Task"), "priority", t.prio, "assignee", t.who, "assignee_name", nameOf(t.who),
			"labels", jd(t.labels), "updated", g.t(float64(rank*5+1)*H))
	}
	for rank, x := range [][3]string{
		{"OPS-235", "Quarterly restore drill: payments database", "sam.roe"},
		{"OPS-236", "Grafana: one dashboard per service tier", "drew.foe"},
		{"OPS-237", "Move dev-swarm to the new runner image", "morgan.koe"}} {
		prio := []string{"Medium", "Low", "Low"}[rank]
		g.ins(b, "board_issues", "view", "board", "key", x[0], "rank", 100+rank, "col", 0, "summary", x[1], "status_id", "1", "status", "Backlog",
			"type", "Task", "priority", prio, "assignee", x[2], "assignee_name", nameOf(x[2]), "labels", "[]", "updated", g.t(float64(rank+3)*D))
	}
	for _, k := range []string{"OPS-231", "OPS-229", "OPS-205"} {
		g.ins(b, "board_qf", "qf_id", 1, "key", k)
	}
	g.ins(b, "agent_tasks", "key", "OPS-225", "state", "waiting", "phase", "fix", "assignee", "taylor.noe", "argus_user", "taylor.noe",
		"plan_version", 1, "plan", jd(om("summary", "Roll staging coupon-service back to 3.8.2", "steps", sl("redeploy 3.8.2 on stg-app-01",
			"run QA case CPN-12"))),
		"intake_at", g.t(15*H), "intake_cost", 0.12, "approved_version", 1, "approved_by", "taylor.noe", "approved_at", g.t(14*H),
		"updated", g.t(14*H))
	g.ins(b, "agent_log", "key", "OPS-225", "at", g.t(15*H), "kind", "intake", "text", "plan v1: roll back coupon-service on staging")
	g.ins(b, "agent_log", "key", "OPS-225", "at", g.t(14*H), "kind", "approve", "text", "taylor.noe approved plan v1")
	g.agentExtra()
	g.ins(b, "state", "k", "proposal_settings", "v", jd(om("auto", om("qa", false, "page", true, "cve", false, "cicd", false),
		"expiry_days", 14, "reject_days", 7)))
	g.writeJSON("settings-out/board-status.json", om(
		"configured", true, "jira_url", jira, "project", "OPS", "board_id", 12, "bot", "argus-bot",
		"last_ok", g.t(2*M), "last_run", g.t(2*M), "users_connected", 6, "approvers", sl("sam.roe", "alex.doe"),
		"board", om("id", 12, "name", "OPS board", "type", "kanban", "columns", cols,
			"quick_filters", L(om("id", 1, "name", "Production now"), om("id", 2, "name", "Created by me"))),
		"board_snap_at", g.t(2*M), "settings", om("project", "OPS"), "requests", om(), "recent_failures", L(), "problems", L(),
		"agent", om("settings", om("enabled", true, "column", "In Progress", "chat", true, "secrets", false, "fixer_trigger", "off",
			"assignees", sl("alex.doe", "jamie.poe")),
			"tick", "2 planned, 1 paused (usage limit)", "debounce", 120, "rw_users", sl("alex.doe", "sam.roe"))))
	users := om()
	for _, p := range PEOPLE[:6] {
		users.Set(p.user, om("jira_name", p.user, "display", p.name, "ok", true, "at", g.t(D)))
	}
	g.writeJSON("settings-out/jira-users.json", om("users", users, "shared", om("jira_name", "argus-bot", "ok", true)))
}

func (g *Gen) agentExtra() {
	b := g.board
	plan := om("summary", "Point the coupon-service health check on staging at /healthz (the old /status route was removed in "+
		"3.9.0), redeploy it and confirm the edge marks it healthy.",
		"classification", om("environment", "pre-release", "kind", "change"), "confidence", "high",
		"estimate", om("effort", "30 min", "cost_usd", 0.42),
		"plan", L(om("mode", "READ", "text", "Read the compose file and the edge upstream for coupon-service", "targets", sl("stg-app-01")),
			om("mode", "READ", "text", "Confirm /healthz answers 200 on the running 3.9.0 container", "targets", sl("stg-app-01")),
			om("mode", "WRITE", "text", "Change the health check path to /healthz and redeploy coupon-service",
				"targets", sl("stg-app-01")),
			om("mode", "READ", "text", "Verify the edge sees the service healthy for 10 minutes", "targets", sl("stg-app-01"))),
		"questions", L(om("id", "q1", "text", "Is a 2-minute restart of coupon-service on staging fine during office hours?",
			"blocking", false)),
		"needs", L())
	g.ins(b, "agent_tasks", "key", "OPS-238", "state", "ready", "phase", "intake", "assignee", "alex.doe", "argus_user", "alex.doe",
		"plan_version", 2, "plan", jd(plan), "intake_at", g.t(40*M), "intake_cost", 0.31, "spent_usd", 0.46, "plan_entered_at", g.t(3*H),
		"updated", g.t(35*M))
	g.ins(b, "agent_log", "key", "OPS-238", "at", g.t(3*H), "kind", "intake", "text", "plan v1: health check path on staging")
	g.ins(b, "agent_log", "key", "OPS-238", "at", g.t(40*M), "kind", "intake", "text", "plan v2 after the chat: restart window asked")
	reset := strings.ReplaceAll(g.at(-3*H).UTC().Format("Jan 02, 03:04PM"), " 0", " ")
	g.ins(b, "agent_tasks", "key", "OPS-214", "state", "failed", "phase", "intake", "assignee", "jamie.poe", "argus_user", "jamie.poe",
		"plan_version", 0, "intake_cost", 0.0, "spent_usd", 0.0, "error", "claude-limit: resets "+reset+" (UTC)",
		"retry_at", g.t(-3*H-2*M), "limit_since", g.t(50*M), "plan_entered_at", g.t(2*H), "updated", g.t(50*M))
	g.ins(b, "agent_log", "key", "OPS-214", "at", g.t(50*M), "kind", "failed",
		"text", "Claude usage limit reached: resets "+reset+" (UTC); planning starts again after the reset")
	// Review validation cases (2026-11-02): a production plan (proposal only) with many targets, plan v3
	prod := om("summary", "Add the Redis cart and rate-limit data to the nightly backup: an RDB snapshot on each production "+
		"Redis node, copied to both backup hosts, kept 14 days.",
		"classification", om("environment", "release", "kind", "production"),
		"estimate", om("effort", "2 h", "cost_usd", 0.9),
		"plan", L(om("mode", "READ", "text", "Read the Redis persistence settings and the free disk on each node",
			"targets", sl("prod-redis-01", "prod-redis-02", "prod-redis-03")),
			om("mode", "WRITE", "text", "Enable a nightly BGSAVE at 02:30 and copy dump.rdb to the backup hosts",
				"targets", sl("prod-redis-01", "prod-redis-02", "prod-redis-03", "prod-backup-01", "prod-backup-02")),
			om("mode", "READ", "text", "Restore last night's snapshot into a scratch Redis and compare the key count",
				"targets", sl("prod-backup-01"))),
		"questions", L(), "needs", L())
	g.ins(b, "agent_tasks", "key", "OPS-193", "state", "ready", "phase", "intake", "assignee", "sam.roe", "argus_user", "sam.roe",
		"plan_version", 3, "plan", jd(prod), "intake_at", g.t(26*H), "intake_cost", 0.38, "spent_usd", 0.38, "plan_entered_at", g.t(2*D),
		"updated", g.t(26*H))
	g.ins(b, "agent_log", "key", "OPS-193", "at", g.t(26*H), "kind", "intake", "text", "plan v3: production, proposal only")
	g.agentProgressCases()
	type chatT struct {
		key, ak string
		who     any
		kind    string
		body    string
		ago     float64
	}
	chat := []chatT{
		{"OPS-238", "user", "alex.doe", "message", "Can you also check whether the edge still probes /status?", 2.5 * H},
		{"OPS-238", "argus", nil, "message", "Yes: the staging edge upstream for coupon-service still probes /status every " +
			"5 s and gets 404 since 3.9.0, so it marks the service down and falls back. The plan now changes the compose health " +
			"check and the edge probe together.", 2.4 * H},
		{"OPS-238", "user", "alex.doe", "message", "Good. Production has the same probe, please write that down for later.", 50 * M},
		{"OPS-238", "argus", nil, "message", "Production is read-only for me, so I proposed a separate task for the operator " +
			"with the probe change, its rollback and the check.", 45 * M},
		{"OPS-238", "system", nil, "proposal", "Proposed: Production edge: probe coupon-service on /healthz", 45 * M},
		{"new-5e1f0a2c", "user", "sam.roe", "message", "Why did the nightly search re-index take twice as long this week?", 5 * H},
		{"new-5e1f0a2c", "argus", nil, "message", "The index grew 38% after the spring catalogue import, and the job still " +
			"runs with 2 workers on prod-search-01. Raising it to 4 fits in the night window; the CPU there peaks at 35%.", 4.9 * H},
		{"new-5e1f0a2c", "user", "sam.roe", "message", "Define a task for that, please.", 4.8 * H},
		{"new-5e1f0a2c", "system", nil, "proposal", "Proposed: Search: run the nightly re-index with 4 workers", 4.7 * H},
	}
	seqs, turn := map[string]int{}, 0
	for _, c := range chat {
		seqs[c.key]++
		var turnID any
		if c.ak == "user" {
			turn++
			g.ins(b, "task_chat_turns", "id", turn, "key", c.key, "state", "done", "upto_seq", seqs[c.key], "requested_by", c.who,
				"started", g.t(c.ago-20), "finished", g.t(c.ago-90), "cost", 0.11, "error", nil)
		} else {
			turnID = turn
		}
		g.ins(b, "task_chat", "key", c.key, "seq", seqs[c.key], "author_kind", c.ak, "author", c.who, "kind", c.kind, "body", c.body,
			"turn_id", turnID, "created", g.t(c.ago))
	}
	for _, x := range []struct {
		key   string
		spent float64
	}{{"OPS-238", 0.22}, {"new-5e1f0a2c", 0.24}} {
		g.ins(b, "task_chat_state", "key", x.key, "summary", nil, "budget_usd", 2.0, "spent_usd", x.spent, "updated", g.t(45*M))
	}
	for k, x := range []struct {
		key        string
		seq        int
		title, env string
		ago        float64
	}{{"OPS-238", 5, "Production edge: probe coupon-service on /healthz", "release", 45 * M},
		{"new-5e1f0a2c", 4, "Search: run the nightly re-index with 4 workers", "release", 4.7 * H}} {
		isNew := strings.HasPrefix(x.key, "new-")
		url := fmt.Sprintf("/board/task?key=%s#m%d", x.key, x.seq)
		who, where := "alex.doe", x.key
		if isNew {
			url = fmt.Sprintf("/board/ask?key=%s#m%d", x.key, x.seq)
			who, where = "sam.roe", "Ask Argus"
		}
		g.ins(b, "proposals", "id", 101+k, "path", "chat", "source_kind", "chat", "source_id", fmt.Sprintf("%s.%d", x.key, x.seq),
			"source_url", url, "title", x.title, "description", x.title+".", "evidence", "From the task chat.", "fix_prompt", "", "env", x.env, "host", "",
			"priority", "Medium", "severity", "medium", "sev_rank", 2, "labels", jd(sl("argus", "chat")), "assignee_suggestion", nil,
			"why", "Asked in the chat ("+where+") by "+who,
			"board_col", 1, "hash", h12(x.title), "state", "pending", "created_at", g.t(x.ago), "updated_at", g.t(x.ago), "refreshed", 0)
	}
	type lessonT struct {
		title, body, svc string
		env              any
		kind             string
		tags             []string
		st, src          string
		uses             int
		usedAgo          float64
	}
	lessons := []lessonT{
		{"Restart one staging node at a time", "Staging runs a single instance per service behind the edge; restart one node, " +
			"wait for the edge to mark it healthy, then the next.", "coupon-service", "pre-release", "procedure", sl("restart"),
			"approved", "OPS-225", 9, 6 * H},
		{"Check the Loki retention before raising log levels", "A debug log level on checkout fills /var/lib/loki on " +
			"prod-mon-01 in about two days; change the retention first.", "loki", "release", "trap", sl("disk", "logging"),
			"approved", "OPS-226", 4, 1 * D},
		{"SMS provider failover is a feature flag, not a deploy", "sms-otp switches providers with the SMS_PRIMARY flag; " +
			"no redeploy is needed and the change is audited.", "sms-otp", "release", "fact", sl("payments"), "approved", "OPS-231", 2, 3 * D},
		{"Mongo backup user needs its role after a rotation", "Rotating the backup user's password drops its backup role " +
			"on prod-mongo-02; grant it again and run one test dump.", "mongo", "release", "trap", sl("backup"), "proposed", "OPS-211", 0, 0},
		{"Health check paths change with major releases", "Before a coupon-service minor release, compare the health " +
			"routes; 3.9.0 removed /status.", "coupon-service", nil, "procedure", sl("release"), "proposed", "OPS-238", 0, 0},
	}
	for idx, l := range lessons {
		k := idx + 1
		approved := l.st == "approved"
		proposedAgo := float64(k-3)*H + H
		var decidedBy, decidedAt, lastUsed any
		if approved {
			proposedAgo = float64(k+2) * D
			decidedBy, decidedAt = "sam.roe", g.t(float64(k+1)*D)
		}
		if l.uses != 0 {
			lastUsed = g.t(l.usedAgo)
		}
		g.ins(b, "lessons", "id", k, "title", l.title, "body", l.body, "service", l.svc, "env", l.env, "kind", l.kind, "tags", jd(l.tags), "norm_hash", h12(l.title),
			"state", l.st, "source_key", l.src, "evidence", "Task "+l.src+" run log", "proposed_at", g.t(proposedAgo),
			"decided_by", decidedBy, "decided_at", decidedAt, "uses", l.uses, "last_used", lastUsed, "unhelpful", 0)
	}
	g.ins(b, "state", "k", "lessons_settings", "v", jd(om("enabled", true)))
	g.writeJSON("settings-out/claude.json", om("enabled", true, "by", "sam.roe", "at", g.t(2*D)))
}

var priorityOf = map[string]string{"critical": "Highest", "high": "High", "low": "Low"}

func prio(sev string) string {
	if p, ok := priorityOf[sev]; ok {
		return p
	}
	return "Medium"
}

var sourceURLOf = map[string]string{"cve": "/vulns", "cicd": "/cicd", "qa": "/qa", "cred": "/certs"}

func srcURL(path string) string {
	if u, ok := sourceURLOf[path]; ok {
		return u
	}
	return "/incidents"
}

func (g *Gen) proposals() {
	type row struct {
		path, sk, sid, title, env, host, sev string
		rank                                 int
		state                                string
		ago                                  float64
	}
	rows := []row{
		{"cve", "cve", "linux|production", "Kernel reboot wave for CVE-2026-31337 on 21 production hosts", "release", "", "high", 3, "pending", 1 * D},
		{"cve", "cve", "php|production", "Rebuild analytics-matomo on PHP 8.3 (CVE-2026-24410, KEV)", "release", "prod-matomo-01", "critical", 4, "pending", 2 * D},
		{"cicd", "cicd", "703", "search: commit the lockfile on develop", "develop", "", "low", 1, "pending", 5 * H},
		{"cicd", "cicd", "709", "checkout: LOG_LEVEL=info on release", "release", "", "high", 3, "pending", 1.8 * D},
		{"qa", "qa", "63", "Product images slow on mobile (needs the page and device)", "release", "", "low", 1, "snoozed", 5 * H},
		{"page", "incident", "145", "www.northwind.example 502: restore prod-shop-02:3001", "release", "prod-shop-02", "high", 3, "accepted", 6 * H},
		{"page", "incident", "141", "sms-otp: provider timeout and failover", "release", "prod-pay-01", "critical", 4, "accepted", 4 * H},
		{"qa", "qa", "64", "Paid order not received by the warehouse (A-77812)", "release", "", "high", 3, "rejected", 2.4 * D},
	}
	for idx, r := range rows {
		k := idx + 1
		decided := r.state == "accepted" || r.state == "rejected"
		var snooze, decAt, decBy, reason, taskID any
		if r.state == "snoozed" {
			snooze = g.t(-1 * D)
		}
		if decided {
			decAt, decBy = g.t(r.ago-H), "sam.roe"
		}
		if r.state == "rejected" {
			reason = "duplicate of OPS-211 (mongo backup)"
		}
		if r.sid == "141" {
			taskID = 1
		} else if r.sid == "145" {
			taskID = 2
		}
		g.ins(g.board, "proposals", "id", k, "path", r.path, "source_kind", r.sk, "source_id", r.sid, "source_url", iif(r.path == "cve", "/vulns", "/incidents"),
			"title", r.title, "description", r.title+".", "evidence", "From Argus.", "fix_prompt", "", "env", r.env, "host", r.host,
			"priority", prio(r.sev), "severity", r.sev, "sev_rank", r.rank,
			"labels", jd(sl("argus", r.path)), "assignee_suggestion", PEOPLE[:5][g.r.choice(5)].user, "why", "matches the "+r.path+" intake rule",
			"board_col", 1, "hash", h12(r.title), "state", r.state, "snooze_until", snooze,
			"created_at", g.t(r.ago), "updated_at", g.t(r.ago-H), "refreshed", 1, "decided_at", decAt,
			"decided_by", decBy, "reason", reason, "task_id", taskID)
	}
	// the queue page (search, facets, 25 per page, bulk confirm): a fuller backlog of waiting proposals. Expiry is
	// created_at + 14 days (proposal_settings), so the ones 12-14 days old show "expires in ..h".
	type qrow struct {
		path, sk, sid, title, env, host, sev, state string
		ago                                         float64
	}
	queue := []qrow{
		{"cve", "cve", "openssl|production", "Patch OpenSSL 3.0.15 on the edge pool (CVE-2026-28801)", "release", "prod-edge-01", "high", "pending", 13.4 * D},
		{"cve", "cve", "nginx|production", "Upgrade nginx 1.26.3 on prod-edge-02 and prod-edge-03", "release", "prod-edge-02", "medium", "pending", 12.6 * D},
		{"cve", "cve", "node|staging", "Node 20.18 for the storefront build image (CVE-2026-27114)", "pre-release", "", "medium", "pending", 3 * D},
		{"cve", "cve", "redis|production", "Redis 7.2.7 on prod-db-03 (CVE-2026-22950)", "release", "prod-db-03", "high", "pending", 6 * D},
		{"cve", "cve", "glibc|develop", "Rebuild dev-runner-01 images on the patched glibc", "develop", "dev-runner-01", "low", "pending", 9 * D},
		{"cve", "cve", "imagemagick|production", "Disable the MVG coder on prod-media-01 (CVE-2026-25503)", "release", "prod-media-01", "critical", "pending", 13.8 * D},
		{"cicd", "cicd", "712", "orders: cache the Go module download between jobs", "develop", "", "low", "pending", 2 * D},
		{"cicd", "cicd", "715", "storefront: the release job runs without a lock", "release", "", "high", "pending", 11 * H},
		{"cicd", "cicd", "718", "payments: pin the builder image by digest", "release", "", "medium", "pending", 4 * D},
		{"cicd", "cicd", "721", "search: the smoke test stage is allowed to fail", "pre-release", "", "medium", "pending", 12.9 * D},
		{"cicd", "cicd", "724", "coupon-service: two deploy jobs race on pre-release", "pre-release", "", "high", "pending", 30 * H},
		{"cicd", "cicd", "727", "media: artifacts kept 30 days, 41 GB on the runner", "develop", "dev-runner-01", "low", "pending", 7 * D},
		{"qa", "qa", "66", "Coupon field accepts expired codes at checkout", "release", "", "high", "pending", 20 * H},
		{"qa", "qa", "67", "Order history page times out for accounts with 500+ orders", "release", "prod-api-02", "medium", "pending", 2.5 * D},
		{"qa", "qa", "68", "Search filter 'in stock' shows sold-out items", "pre-release", "", "medium", "pending", 5 * D},
		{"qa", "qa", "69", "Address form drops the unit number on save", "release", "", "low", "pending", 8 * D},
		{"qa", "qa", "70", "Gift card balance is not refreshed after a partial refund", "release", "prod-pay-02", "high", "pending", 12.2 * D},
		{"page", "incident", "148", "prod-db-02 replication lag above 30 s at the nightly batch", "release", "prod-db-02", "high", "pending", 9 * H},
		{"page", "incident", "149", "api.northwind.example p95 above 2 s for 12 minutes", "release", "prod-api-03", "medium", "pending", 1.5 * D},
		{"page", "incident", "150", "prod-backup-02 nightly dump finished 2 h late", "release", "prod-backup-02", "medium", "pending", 3.5 * D},
		{"page", "incident", "151", "prod-search-01 heap at 92 % after the re-index", "release", "prod-search-01", "critical", "pending", 3 * H},
		{"page", "incident", "152", "Monitoring gap: prod-misc-01 exporter down since 02:10", "release", "prod-misc-01", "low", "pending", 10 * D},
		{"cred", "cred", "tls|www", "TLS certificate for shop.northwind.example expires in 9 days", "release", "prod-edge-01", "high", "pending", 26 * H},
		{"cred", "cred", "token|ci", "CI deploy token of storefront expires in 12 days", "release", "", "medium", "pending", 4.5 * D},
		{"cred", "cred", "db|staging", "Rotate the staging reporting DB password (shared in a ticket)", "pre-release", "", "medium", "pending", 13.1 * D},
		{"cred", "cred", "ssh|develop", "Remove the departed contractor's key from dev-runner-01", "develop", "dev-runner-01", "low", "pending", 6.5 * D},
		{"cicd", "cicd", "730", "analytics: Matomo archive cron overlaps the backup window", "release", "prod-matomo-01", "low", "pending", 11.5 * D},
		{"qa", "qa", "71", "Wishlist share link opens a blank page on iOS", "release", "", "low", "pending", 36 * H},
		{"cve", "cve", "php|staging", "PHP 8.3.14 on the staging Matomo copy", "pre-release", "", "low", "snoozed", 4 * D},
		{"page", "incident", "146", "prod-shop-03 disk at 81 % (log rotation)", "release", "prod-shop-03", "medium", "snoozed", 2 * D},
		{"cicd", "cicd", "710", "storefront: flaky e2e test on the cart drawer", "develop", "", "low", "snoozed", 5.5 * D},
		{"page", "incident", "147", "Payment webhook retries pile up on prod-pay-02", "release", "prod-pay-02", "high", "accepted", 1.2 * D},
		{"cicd", "cicd", "706", "orders: run the migrations job before the deploy", "release", "", "medium", "accepted", 2.2 * D},
		{"qa", "qa", "65", "Promo banner overlaps the cart button on small screens", "release", "", "low", "rejected", 3.2 * D},
	}
	rank := map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}
	for idx, r := range queue {
		k := idx + 11
		decided := r.state == "accepted" || r.state == "rejected"
		var snooze, decAt, decBy, reason any
		if r.state == "snoozed" {
			snooze = g.t(-float64(k%3+1) * D)
		}
		if decided {
			decAt, decBy = g.t(r.ago-H), "alex.doe"
		}
		if r.state == "rejected" {
			reason = "cosmetic; tracked in the design backlog"
		}
		upd := r.ago*0.4 + H
		if r.ago < upd {
			upd = r.ago
		}
		g.ins(g.board, "proposals", "id", k, "path", r.path, "source_kind", r.sk, "source_id", r.sid,
			"source_url", srcURL(r.path),
			"title", r.title, "description", r.title+".", "evidence", "From Argus.", "fix_prompt", "", "env", r.env, "host", r.host,
			"priority", prio(r.sev), "severity", r.sev, "sev_rank", rank[r.sev],
			"labels", jd(sl("argus", r.path)), "assignee_suggestion", PEOPLE[k%6].user, "why", "matches the "+r.path+" intake rule",
			"board_col", 1, "hash", h12(r.title), "state", r.state, "snooze_until", snooze,
			"created_at", g.t(r.ago), "updated_at", g.t(upd), "refreshed", 1,
			"decided_at", decAt, "decided_by", decBy, "reason", reason, "task_id", nil)
	}
}

// reviewCases are waiting, expired and decided proposals for the Review inbox's validation cases (2026-11-02):
// substantive evidence on a critical production incident, a long title, and an expired proposal in Decided.
func (g *Gen) reviewCases() {
	ev := "13:58 deploy of checkout-api 4.12.0 on prod-api-01 (pipeline 8812).\n" +
		"14:05-14:20 HTTP 5xx 4.2 % of 18,400 requests (baseline 0.1 %); p95 1.9 s (baseline 0.4 s).\n" +
		"Log: 312 x \"upstream prematurely closed connection\" from payments-api since 14:04.\n" +
		"prod-api-02 and prod-api-03 run 4.11.3 and stay at 0.1 %."
	rows := []struct {
		id                                                       int
		path, sk, sid, title, desc, evidence, env, host, sev, st string
		rank                                                     int
		ago                                                      float64
	}{
		{60, "page", "incident", "153", "checkout-api 5xx at 4.2 % on prod-api-01 after the 13:58 deploy",
			"Only the host on the new release fails; the other two are healthy. Roll prod-api-01 back to 4.11.3 or hold the rollout.",
			ev, "release", "prod-api-01", "critical", "pending", 4, 50 * M},
		{61, "cicd", "cicd", "736", "coupon-service: the pre-release deploy job restarts all four replicas at once, drops in-flight " +
			"checkout sessions on stg-app-01 to stg-app-04 and leaves the edge health probe on the removed /status route",
			"", "From Argus.", "pre-release", "stg-app-01", "medium", "pending", 2, 5 * H},
		{62, "cve", "cve", "openssl|staging", "OpenSSL 3.0.15 on the staging build image (CVE-2026-28801)", "", "From Argus.",
			"pre-release", "", "low", "expired", 1, 15 * D},
	}
	for _, r := range rows {
		var decAt, decBy, reason any
		if r.st == "expired" {
			decAt, decBy, reason = g.t(r.ago-14*D), "argus", "expired"
		}
		g.ins(g.board, "proposals", "id", r.id, "path", r.path, "source_kind", r.sk, "source_id", r.sid, "source_url", srcURL(r.path),
			"title", r.title, "description", iif(r.desc == "", r.title+".", r.desc), "evidence", r.evidence, "fix_prompt", "", "env", r.env,
			"host", r.host, "priority", prio(r.sev), "severity", r.sev, "sev_rank", r.rank, "labels", jd(sl("argus", r.path)),
			"assignee_suggestion", nil, "why", "matches the "+r.path+" intake rule", "board_col", 1, "hash", h12(r.title), "state", r.st,
			"snooze_until", nil, "created_at", g.t(r.ago), "updated_at", g.t(r.ago), "refreshed", 0, "decided_at", decAt,
			"decided_by", decBy, "reason", reason, "task_id", nil)
	}
}

type idea struct {
	p, title, area, status, problem, proposal, value, effort, risk string
	ev                                                             []any
}

func ev1(text, link string) []any { return L(om("text", text, "link", link)) }

func (g *Gen) ideas() {
	items := []idea{
		{"devops", "Gate every deploy on per-instance health", "ci-cd", "new",
			"Deploys succeed while an instance fails to start (shop-front :3001, 10-23).",
			"Add a verify step that fails the pipeline when any instance's /health is not 200 within 60 s, and roll back that host.",
			"Stops a whole class of 502 incidents at deploy time.", "M", "low",
			ev1("incident #145: one shop-front instance down after deploy", "/incidents/view?id=145")},
		{"devops", "Provider timeouts as a CI lint", "ci-cd", "review",
			"Outbound HTTP clients without a timeout caused the OTP outage.",
			"A CI check that fails when an HTTP client in payments/* has no timeout configured.", "Prevents the next sms-otp.", "S", "low",
			ev1("RCA #51", "/rca/view?id=51")},
		{"devops", "Weekly docker prune on every runner", "capacity", "built",
			"dev-runner-01 filled up twice this month.", "A systemd timer on every runner: docker system prune older than 7 days.",
			"No more CI failures from a full runner disk.", "S", "low", ev1("incident #149", "/incidents/view?id=149")},
		{"qa", "Synthetic OTP test every 5 minutes", "qa", "accepted",
			"QA noticed the OTP outage 6 minutes after Kuma did; customers noticed before both.",
			"Send a real OTP to a test number every 5 minutes and alert when it does not arrive in 30 s.",
			"Detects provider trouble before customers do.", "S", "low", ev1("QA issue 61", "/qa")},
		{"qa", "Ask for the page and device in the first reply", "qa", "new",
			"Two QA issues this week waited a day for the page and device.",
			"A #qa message template (pinned) with the three fields Argus always asks for.", "Faster triage.", "S", "low",
			ev1("QA issues 63 and 65", "/qa")},
		{"qa", "Regression pack for coupons on staging", "qa", "new", "Percentage coupons broke on staging (3.9.0).",
			"Add CPN-1 to CPN-20 to the staging smoke run after every coupon-service deploy.", "Catches pricing regressions.", "M", "low",
			ev1("incident #148", "/incidents/view?id=148")},
		{"noc", "Page on provider latency, not on queue size", "monitoring", "new",
			"The backlog alert fired 18 minutes after the provider slowed down.",
			"Alert SmsProviderSlow (p99 > 2 s for 3 min) at page level; keep the backlog alert as a warning.", "Earlier pages.", "S", "low",
			ev1("RCA #51 timeline", "/rca/view?id=51")},
		{"noc", "Delete the leftover test alert", "monitoring", "new", "TestAlertDoNotPage has fired at critical for 13 days.",
			"Remove the rule; add a CI check that rejects rules named Test* in production.", "Less noise, more trust in pages.", "S", "low",
			ev1("Alert hygiene", "/hygiene")},
		{"noc", "One status page for customers", "monitoring", "rejected", "Support answered 14 calls during the OTP outage.",
			"A public status page fed from the incident list.", "Fewer support calls.", "L", "medium",
			ev1("QA message from Support", "/qa")},
	}
	for idx, it := range items {
		k := idx + 1
		ago := float64(k*9) * H
		g.insIdea(k, it.p, it.title, it.area, it.problem, jd(it.ev), it.proposal, it.value, it.effort, it.risk, it.status, ago,
			"sam.roe", "no budget for a status page this quarter", GITLAB+"/ops/runner-config/-/merge_requests/12")
		g.ins(g.ops, "idea_events", "idea_id", k, "persona", it.p, "at", g.t(ago), "kind", "new", "actor", "argus", "text", it.title)
	}
	g.writeJSON("ideas-status.json", om(
		"enabled", true, "running", false, "at", g.t(6*H), "last_run", g.t(6*H),
		"personas", om("devops", om("name", "Dana (DevOps)", "role", "senior DevOps / SRE", "days", sl("Mon", "Wed", "Fri")),
			"qa", om("name", "Quinn (QA)", "role", "QA lead", "days", sl("Tue", "Thu")),
			"noc", om("name", "Nico (NOC)", "role", "NOC engineer", "days", sl("Mon", "Thu")))))
}

func (g *Gen) insIdea(k int, p, title, area, problem, evidence, proposal, value, effort, risk, status string, ago float64,
	decider, rejectReason, builtRef string) {
	decided := status != "new"
	var decAt, decBy, reason, bRef, bAt, bBy, prompt, promptAt any
	if decided {
		decAt, decBy = g.t(ago-H), decider
	}
	if status == "rejected" {
		reason = rejectReason
	}
	if status == "built" {
		bRef, bAt, bBy = builtRef, g.t(ago-2*H), "drew.foe"
	}
	if status == "accepted" || status == "review" {
		prompt, promptAt = "# Build: "+title+"\n\n## Goal\n"+proposal+"\n", g.t(ago-H)
	}
	g.ins(g.ops, "ideas", "id", k, "persona", p, "title", title, "area", area, "problem", problem, "evidence", evidence, "proposal", proposal,
		"value", value, "effort", effort, "risk", risk, "prerequisites", "none", "success_metric", "fewer incidents of this kind",
		"evidence_hash", h12(title), "status", status, "run_id", nil, "created_at", g.t(ago), "updated_at", g.t(ago-H),
		"decided_at", decAt, "decided_by", decBy, "reason", reason, "built_ref", bRef, "built_at", bAt, "built_by", bBy,
		"build_prompt", prompt, "prompt_at", promptAt)
}

// agentProgressCases: a planning job that is running (its steps, tool calls and last activity on the task page) and an
// intake that failed because Claude rejected the login (the failure block and Plan again). The live loop keeps the
// running one fresh (RefreshLive -> agentProgressLive).
func (g *Gen) agentProgressCases() {
	b := g.board
	g.ins(b, "agent_tasks", "key", "OPS-231", "state", "planning", "phase", "intake", "assignee", "alex.doe", "argus_user", "alex.doe",
		"plan_version", 0, "job_started", g.t(3*M+20), "spent_usd", 0.0, "plan_entered_at", g.t(40*M), "updated", g.t(3*M+20))
	g.ins(b, "agent_log", "key", "OPS-231", "at", g.t(3*M+20), "kind", "intake", "text", "Reading the task and planning (read-only)")
	g.agentProgressLive()
	g.ins(b, "agent_tasks", "key", "OPS-211", "state", "failed", "phase", "intake", "assignee", "sam.roe", "argus_user", "sam.roe",
		"plan_version", 0, "intake_cost", 0.0, "spent_usd", 0.0, "plan_entered_at", g.t(5*H), "updated", g.t(70*M),
		"error", "intake: Your organization has disabled Claude subscription access for Claude Code",
		"error_detail", jd(om("phase", "intake", "class", "auth", "raw", "Your organization has disabled Claude subscription access for "+
			"Claude Code \u00b7 Use an Anthropic API key instead, or ask your admin to enable it", "is_error", true, "exit_code", 1,
			"run_id", "", "at", g.t(70*M))))
	g.ins(b, "agent_log", "key", "OPS-211", "at", g.t(72*M), "kind", "intake", "text", "Reading the task and planning (read-only)")
	g.ins(b, "agent_log", "key", "OPS-211", "at", g.t(70*M), "kind", "failed",
		"text", "Intake failed: Your organization has disabled Claude subscription access for Claude Code")
}

// agentProgressLive writes the running demo job's progress file (bin side: the task-agent runner), relative to now.
func (g *Gen) agentProgressLive() {
	g.writeJSON("task-agent/out/OPS-231.intake.progress.json", om("phase", "intake", "started", g.t(3*M+15), "tools", 9,
		"steps", sl("queued", "reading", "gathering", "writing", "recording"), "step", "gathering",
		"activity", "reading Loki logs (east)", "updated", g.t(6), "last_tool", "Bash"))
}
