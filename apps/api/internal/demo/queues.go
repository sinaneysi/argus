package demo

import (
	"fmt"
	"strings"

	"github.com/sinaneysi/argus/internal/py"
)

// queues fills the queue pages (search, facets, 25 rows a page): a fuller backlog for ideas, problems and suggestions,
// CI/CD failures, RCA reports, QA issues, lessons and decided proposals. It runs after every other step, so its
// random draws do not shift the data above.
func (g *Gen) queues() {
	g.queueIdeas()
	g.queueProblems()
	g.queueCicd()
	g.queueRCA()
	g.queueQA()
	g.queueLessons()
	g.queueDecided()
	g.queueReplies()
}

// queueReplies: two chat requests forwarded to Argus in Matrix that wait for the demo admin's OK (Review → Chat
// requests). Fictional people and projects only.
func (g *Gen) queueReplies() {
	choices := jd(L(om("name", "alex.doe", "display", "Alex Doe"), om("name", "jamie.poe", "display", "Jamie Poe"),
		om("name", "taylor.noe", "display", "Taylor Noe")))
	g.ins(g.board, "matrix_previews", "pid", "mx-7f3a21", "hash", h12("mx-7f3a21"), "by", "demo.admin", "by_mxid", "@demo.admin:"+"chat."+DOMAIN,
		"title", "Storefront search returns stale prices after the 09:00 import", "summary",
		"Two shoppers saw yesterday's prices on product pages.\nThe import finished at 09:04; the search index was not refreshed.",
		"requester", "Robin Vale — Sales (#storefront)", "project", "OPS", "fwd_name", "demo.admin", "assignee", "jamie.poe",
		"assignee_name", "Jamie Poe", "choices", choices, "state", "pending", "created", g.t(50*M), "updated", g.t(50*M))
	g.ins(g.board, "matrix_previews", "pid", "mx-91c0d4", "hash", h12("mx-91c0d4"), "by", "demo.admin", "by_mxid", "@demo.admin:"+"chat."+DOMAIN,
		"title", "Add the new warehouse VPN range to the staging allow list", "summary",
		"Warehouse scanners in the new hall cannot reach staging.\nRange from the network team: see the thread.",
		"requester", "Casey Lund (#ops-requests)", "project", "OPS", "fwd_name", "demo.admin", "assignee", nil,
		"assignee_name", nil, "choices", choices, "state", "pending", "created", g.t(3*H), "updated", g.t(3*H))
}

func (g *Gen) queueIdeas() {
	more := [][8]string{ // persona, area, effort, status, title, problem, proposal, value
		{"devops", "ci-cd", "S", "new", "Cache Go modules between CI jobs", "orders downloads every module on each job (3 min).",
			"A shared module cache keyed on go.sum in the CI template.", "Faster pipelines on every Go service."},
		{"devops", "ci-cd", "M", "new", "One deploy lock per environment", "Two coupon-service deploys raced on pre-release.",
			"A resource_group per environment in the shared CI template.", "No more half-deployed staging."},
		{"devops", "security", "M", "new", "Pin builder images by digest", "payments builds from a moving :latest tag.",
			"Resolve and pin the builder image digest; bump it by merge request.", "Reproducible builds."},
		{"devops", "security", "S", "review", "Mask and protect every *_KEY variable", "PAYMENT_API_KEY was visible in job logs.",
			"A nightly audit that flags unmasked or unprotected variables named *_KEY, *_TOKEN or *_SECRET.", "No secrets in logs."},
		{"devops", "backup", "M", "new", "Restore test for one backup set a week", "Nobody has restored opensearch-prod_snapshots this year.",
			"Pick one set a week, restore it on a scratch VM, record the time and the size.", "Backups proven, not assumed."},
		{"devops", "backup", "S", "accepted", "Alert when a backup set is two days stale", "mongo-prod_reviews went stale for 3 days unnoticed.",
			"A warning when the newest backup is older than 2 × its schedule.", "Stale backups caught in a day."},
		{"devops", "capacity", "S", "new", "Loki retention by label", "Debug logs of one service fill prod-mon-01.",
			"Shorter retention for level=debug streams.", "Disk stays flat when someone forgets a log level."},
		{"devops", "capacity", "L", "new", "Move search to its own VM pool", "Re-index competes with the API on prod-api-03.",
			"Two dedicated search VMs; the API pool keeps its headroom.", "Stable p95 during the nightly re-index."},
		{"devops", "other", "S", "built", "Document the SMS provider switch", "The failover flag was found only in a chat thread.",
			"A runbook page linked from the incident template.", "Faster mitigation next time."},
		{"qa", "qa", "M", "new", "Smoke test the checkout after every release deploy", "The 3-D Secure failure was found by customers.",
			"A scripted checkout with a test card right after deploy_production.", "Catches payment regressions in minutes."},
		{"qa", "qa", "S", "review", "Attach a HAR file template to #qa", "Slow-page reports rarely include timings.",
			"A pinned how-to for recording a HAR on mobile and desktop.", "Faster triage of slow pages."},
		{"qa", "ux", "S", "new", "Show the order number on the payment error page", "Support asks every caller for the order number.",
			"Print the order number and a support code on the error page.", "Shorter support calls."},
		{"qa", "ux", "M", "new", "Consistent empty states in the account area", "Three different 'nothing here' messages confuse testers.",
			"One empty-state component with a next step.", "Clearer pages, fewer false reports."},
		{"qa", "qa", "M", "accepted", "Staging data refresh every Monday", "Staging orders are months old; some flows cannot be tested.",
			"Refresh anonymised catalogue and order data every Monday 06:00.", "Realistic test data."},
		{"qa", "security", "S", "rejected", "Let QA read production logs", "QA waits for an operator to pull logs.",
			"Read-only Loki access for the QA team.", "Faster reproduction."},
		{"noc", "monitoring", "S", "new", "Group alerts by incident in the chat room", "One outage posted 31 alerts in #alerts.",
			"Thread every alert under its incident's first message.", "A readable alerts room during an outage."},
		{"noc", "monitoring", "M", "new", "Synthetic login check from both datacenters", "Login failures in west were seen only from east.",
			"Run the Kuma login check from a probe in each datacenter.", "Regional outages detected."},
		{"noc", "monitoring", "S", "review", "Silence alerts during planned maintenance", "The reboot wave paged twice at night.",
			"A maintenance window in the change calendar that silences the affected hosts.", "No pages for planned work."},
		{"noc", "capacity", "S", "new", "Trend disk growth on the dashboard", "Disks are noticed at 85 %, not when they start growing.",
			"A days-until-full panel per mount, sorted by the soonest.", "Weeks of warning instead of hours."},
		{"noc", "other", "M", "new", "On-call handover note every morning", "The night's events are lost between shifts.",
			"Argus drafts a five-line handover at 08:00 from the incident list.", "Nothing falls between shifts."},
		{"noc", "monitoring", "L", "new", "Status page for internal teams", "Sales and support ask in chat whether the shop is down.",
			"An internal status page fed from open customer-facing incidents.", "Fewer interruptions during outages."},
		{"devops", "ci-cd", "S", "new", "Fail the pipeline on a missing pre-release branch", "scheduler releases from develop straight to release.",
			"A CI check that blocks release when pre-release is missing.", "Every change passes staging."},
		{"qa", "qa", "S", "new", "Regression pack for search filters", "The 'in stock' filter showed sold-out items.",
			"Ten filter combinations in the staging smoke run.", "Filters stay correct across releases."},
		{"noc", "security", "S", "new", "Alert on SSH logins outside the bastion", "One login to prod-db-02 came from a laptop.",
			"Alert when sshd accepts a login from an address outside the bastion range.", "Unusual access seen at once."},
	}
	for idx, x := range more {
		k := idx + 10
		p, area, effort, status, title, problem, proposal, value := x[0], x[1], x[2], x[3], x[4], x[5], x[6], x[7]
		ago := float64(k*7+3) * H
		risk := "low"
		if effort == "L" {
			risk = "medium"
		}
		decider := PEOPLE[k%4].user
		g.insIdea(k, p, title, area, problem, jd(L(om("text", "Argus's own data", "link", "/incidents"))), proposal, value, effort, risk,
			status, ago, decider, "production logs stay with the operators", fmt.Sprintf("%s/ops/runbooks/-/merge_requests/%d", GITLAB, k))
		g.ins(g.ops, "idea_events", "idea_id", k, "persona", p, "at", g.t(ago), "kind", "new", "actor", "argus", "text", title)
		if status != "new" {
			g.ins(g.ops, "idea_events", "idea_id", k, "persona", p, "at", g.t(ago-H), "kind", status, "actor", decider, "text", title)
		}
	}
}

func (g *Gen) queueProblems() {
	o := g.ops
	type row struct {
		id                  int
		title, st, sev, env string
		cf                  int
		owner               any
		rc, lbl             string
		ago                 float64
	}
	rows := []row{
		{10, "Coupon rules divide by a null minimum order", "fix_in_progress", "medium", "pre-release", 0, "jamie.poe",
			"coupon-service 3.9.0 reads min_order as 0 when it is null", "proven", 19 * H},
		{11, "Runner disks fill with old Docker images", "known_error", "low", "develop", 0, "drew.foe",
			"no prune job on the CI runners", "proven", 2 * D},
		{12, "Certificate renewals fail after a DNS API token rotation", "investigating", "medium", "release", 1, "casey.loe",
			"the certbot DNS-01 hook still uses the old token", "probable", 4 * D},
		{13, "Release deploys wait on stuck database migrations", "new", "high", "release", 0, nil, "", "", 8 * H},
		{14, "Debug log levels left on in production", "investigating", "medium", "release", 0, "robin.moe",
			"no check on LOG_LEVEL per scope", "probable", 1.8 * D},
		{15, "Login slow when the sessions table grows", "closed", "medium", "release", 1, "jamie.poe",
			"missing index on sessions.user_id", "proven", 9 * D},
		{16, "Orders queue backlog after a consumer deadlock", "resolved", "high", "release", 1, "alex.doe",
			"the notification consumer deadlocks on a full prefetch", "proven", 12 * D},
		{17, "Staging certificates expire without a warning", "closed", "low", "pre-release", 0, "casey.loe",
			"the staging wildcard was not in the cert-check list", "proven", 15 * D},
	}
	for _, x := range rows {
		ownerOr := x.owner
		if ownerOr == nil {
			ownerOr = "argus"
		}
		r := om("id", x.id, "title", x.title, "status", x.st, "origin", "manual", "root_cause", iif(x.rc != "", x.rc, nil),
			"root_cause_label", iif(x.lbl != "", x.lbl, nil), "severity", x.sev, "env", x.env, "customer_facing", x.cf, "owner", x.owner,
			"signature", "{}", "fanout", 0, "created_at", g.t(x.ago), "created_by", ownerOr, "updated_at", g.t(x.ago*0.5),
			"first_seen", g.t(x.ago+H))
		if x.st != "new" {
			r.Set("investigating_at", g.t(x.ago-H))
		}
		switch x.st {
		case "known_error", "fix_in_progress", "resolved", "closed":
			r.Set("known_error_at", g.t(x.ago*0.7))
		}
		if x.st == "resolved" || x.st == "closed" {
			r.Set("resolved_at", g.t(x.ago*0.5))
			r.Set("resolved_by", x.owner)
			r.Set("resolve_note", "fixed; see the linked incidents")
		}
		insMap(g, o, "problems", r)
		g.ins(o, "problem_events", "problem_id", x.id, "at", g.t(x.ago), "kind", "created", "actor", ownerOr, "text", x.title+" (manual)")
	}
	link := func(pid int, rt string, rid int, lt string, ago float64, state string, conf any, reason any) {
		if state == "" {
			state = "accepted"
		}
		acc, sug := state == "accepted", state == "suggested"
		confidence := conf
		if conf == nil {
			confidence = 1.0
		}
		var features, decBy, decAt any
		if sug {
			features = jd(sl("same service", "time window"))
		}
		if acc {
			decBy, decAt = "alex.doe", g.t(ago)
		}
		g.ins(o, "problem_links", "problem_id", pid, "ref_type", rt, "ref_id", fmt.Sprint(rid), "link_type", lt, "state", state,
			"is_primary", iif(acc && lt == "caused_by", 1, 0), "source", iif(acc, "human", "argus"),
			"confidence", confidence, "reason", reason, "features", features,
			"by", iif(sug, "argus", "alex.doe"), "at", g.t(ago), "decided_by", decBy, "decided_at", decAt)
		text := lt
		if sug {
			text = fmt.Sprintf("%s (%s) %v", lt, py.F(conf.(float64), 2), reason)
		}
		g.ins(o, "problem_events", "problem_id", pid, "at", g.t(ago), "kind", iif(acc, "link", "suggest"),
			"actor", iif(acc, "alex.doe", "argus"), "ref_type", rt, "ref_id", fmt.Sprint(rid), "text", text)
	}
	link(10, "incident", 148, "caused_by", 18*H, "", nil, nil)
	link(10, "qa_issue", 62, "caused_by", 18*H, "", nil, nil)
	link(11, "incident", 149, "caused_by", 1.9*D, "", nil, nil)
	link(12, "incident", 152, "caused_by", 3.9*D, "", nil, nil)
	link(13, "incident", 150, "caused_by", 7*H, "", nil, nil)
	link(15, "incident", 132, "caused_by", 8.8*D, "", nil, nil)
	link(16, "incident", 133, "caused_by", 11.9*D, "", nil, nil)
	link(17, "incident", 134, "caused_by", 14.8*D, "", nil, nil)
	// suggestions waiting for a person: several problems, link types and confidence bands
	for _, s := range []struct {
		pid       int
		rt        string
		rid       int
		lt        string
		ago, conf float64
		why       string
	}{
		{8, "incident", 142, "caused_by", 40 * M, 0.74, "same host prod-shop-02 within 20 minutes of the deploy"},
		{12, "incident", 134, "related", 3 * H, 0.66, "same family (cert) and the same renewal hook"},
		{14, "incident", 146, "caused_by", 2 * H, 0.91, "loki disk growth starts when LOG_LEVEL=debug went live"},
		{9, "incident", 143, "related", 5 * H, 0.61, "same production fleet, reboot pending on prod-pay-01"},
		{13, "qa_issue", 64, "related", 6 * H, 0.69, "warehouse-api errors in the same hour as the report"},
		{7, "qa_issue", 63, "related", 50 * M, 0.63, "mobile slowness reported during the SMS outage window"},
		{10, "incident", 131, "related", 9 * H, 0.58, "older replication lag on the coupon database host"},
		{11, "incident", 150, "related", 4 * H, 0.64, "runner disk at 88 % when the release job started"},
	} {
		link(s.pid, s.rt, s.rid, s.lt, s.ago, "suggested", s.conf, s.why)
	}
}

func (g *Gen) queueCicd() {
	o := g.ops
	type pp struct {
		pid  int64
		path string
	}
	proj := map[string]pp{}
	rows, err := o.Query("SELECT id, path, substr(path, instr(path, '/') + 1) FROM cicd_projects")
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var id int64
		var path, svc string
		if err := rows.Scan(&id, &path, &svc); err != nil {
			panic(err)
		}
		proj[svc] = pp{id, path}
	}
	rows.Close()
	type row struct {
		kind, svc, env, sev, klass, rule, title, evid, prop string
		agoH, resH                                          float64
	}
	list := []row{
		{"failure", "orders", "develop", "low", "code", "build-error", "orders build fails: undefined field in OrderDTO",
			"./order.go:118: o.Discount undefined", "Rebase on develop; the field was renamed to DiscountTotal.", 3, 0},
		{"failure", "payments", "release", "high", "registry", "image-pull", "payments release job cannot pull the builder image",
			"manifest unknown: builder:1.22", "Pin the builder image by digest (proposal 718).", 11, 0},
		{"failure", "storefront", "pre-release", "medium", "flaky", "flaky-test", "storefront e2e: cart drawer test times out",
			"TimeoutError: waiting for selector .cart-drawer", "Quarantine the test until the animation wait is fixed.", 7, 0},
		{"deploy", "checkout", "release", "high", "health", "health-check", "checkout deploy: /health 503 on prod-api-02",
			"verify: prod-api-02 /health 503 after 60 s", "Check the env file of prod-api-02, then retry the job.", 15, 0},
		{"failure", "media", "develop", "low", "runner", "disk-full", "media build: no space left on dev-runner-01",
			"write /builds: no space left on device", "Prune old images on dev-runner-01 (idea: weekly prune).", 26, 0},
		{"deploy", "warehouse-api", "pre-release", "medium", "ssh", "ansible-unreachable", "warehouse-api: stg-wh-01 unreachable over SSH",
			"UNREACHABLE! ssh: connect to host 10.40.3.21 port 22: timed out", "Check the staging VM and its firewall rule.", 30, 0},
		{"failure", "search", "pre-release", "medium", "package", "package-version", "search: npm package version not found",
			"npm ERR! notarget No matching version for @nw/ui@2.4.1", "Publish @nw/ui 2.4.1 or pin 2.4.0.", 20, 0},
		{"failure", "cart", "develop", "low", "lockfile", "lockfile-drift", "cart: go.sum missing entries",
			"missing go.sum entry for module github.com/nw/money", "Run go mod tidy and commit go.sum.", 9, 0},
		{"deploy", "loyalty", "release", "medium", "deploy", "deploy-step", "loyalty deploy: systemd unit failed to restart",
			"Job for northwind-loyalty.service failed", "Read the unit journal on prod-app-04; the env file is missing a key.", 34, 0},
		{"failure", "notifications", "release", "medium", "variable", "missing-variable", "notifications: SMTP_HOST empty on release",
			"KeyError: 'SMTP_HOST'", "Set SMTP_HOST for the release scope.", 40, 0},
		{"failure", "coupon-service", "develop", "low", "code", "lint", "coupon-service lint fails on develop",
			"ruff: F401 unused import", "Remove the unused import.", 5, 0},
		{"deploy", "shop-front", "pre-release", "medium", "health", "health-check", "shop-front staging: :3001 not listening after deploy",
			"verify: stg-shop-01 :3001 connection refused", "Same cause as P-8: the second env file is empty.", 22, 0},
		{"failure", "scheduler", "release", "low", "canceled", "canceled", "scheduler release pipeline canceled by a newer push",
			"pipeline canceled (superseded)", "Nothing to do; the newer pipeline passed.", 50, 0},
		{"failure", "report-builder", "develop", "low", "unknown", "unknown", "report-builder: job exited with code 137",
			"Job killed: exit code 137", "Raise the job memory limit or split the test suite.", 60, 0},
		{"deploy", "orders", "release", "high", "lock", "migration-lock", "orders deploy: migration lock held for 5 minutes",
			"could not obtain lock on relation \"orders\"", "Ask the operator to end the stale migration session.", 70, 0},
		{"failure", "payments", "pre-release", "medium", "code", "test-failure", "payments: refund test fails on staging",
			"AssertionError: expected status refunded", "Fix the partial refund state transition.", 16, 0},
		{"failure", "storefront", "develop", "low", "code", "build-error", "storefront build: TypeScript error in ProductCard",
			"TS2339: Property 'priceLabel' does not exist", "Rename to formattedPrice.", 2, 0},
		{"deploy", "media", "release", "medium", "deploy", "deploy-step", "media deploy: rsync to prod-media-01 interrupted",
			"rsync error: unexplained error (code 255)", "Retry after the network check of prod-media-01.", 80, 0},
		{"failure", "checkout", "develop", "low", "flaky", "flaky-test", "checkout: flaky currency rounding test",
			"expected 10.01 got 10.00", "Use decimal arithmetic in the test helper.", 12, 0},
		{"failure", "search", "develop", "low", "runner", "runner-offline", "search: no runner picked the job for 30 minutes",
			"This job is stuck because the project doesn't have any runners online", "Bring dev-runner-02 back online.", 28, 0},
		{"failure", "warehouse-api", "develop", "low", "code", "test-failure", "warehouse-api: stock reservation test fails",
			"assert reserved == 3", "Fix the off-by-one in the reservation loop.", 36, 0},
		{"failure", "orders", "pre-release", "medium", "code", "test-failure", "orders: tax rounding test fails on staging",
			"expected 19.99 got 20.00", "Round half-even in the tax helper.", 13, 0},
		{"deploy", "notifications", "pre-release", "medium", "deploy", "deploy-step", "notifications: env file not written on stg-app-03",
			"template error: missing key SMTP_PORT", "Add SMTP_PORT to the pre-release variables.", 18, 0},
		{"failure", "media", "release", "medium", "registry", "image-push", "media: registry quota exceeded on push",
			"denied: quota exceeded", "Delete old media image tags (keep 20).", 24, 0},
		// resolved: the resolved tab and the history
		{"failure", "orders", "pre-release", "medium", "flaky", "flaky-test", "orders: flaky Kafka consumer test", "retry passed",
			"Quarantine the test.", 90, 88},
		{"deploy", "checkout", "pre-release", "medium", "health", "health-check", "checkout staging health check 502", "verify 502",
			"Restart after the config fix.", 100, 97},
		{"failure", "cart", "release", "high", "registry", "image-pull", "cart: registry timeout while pushing", "i/o timeout",
			"Retry; the registry was restarting.", 110, 109},
		{"failure", "notifications", "develop", "low", "lockfile", "lockfile-drift", "notifications: poetry.lock out of date",
			"poetry.lock is not consistent with pyproject.toml", "Run poetry lock.", 120, 118},
		{"deploy", "loyalty", "pre-release", "medium", "ssh", "ansible-unreachable", "loyalty: stg-app-02 unreachable",
			"UNREACHABLE!", "VM was rebooting; passed on retry.", 130, 129},
		{"failure", "payments", "develop", "low", "code", "build-error", "payments: missing import after a merge", "undefined: decimal",
			"Add the import.", 140, 138},
	}
	alias := map[string]string{"orders": "order-service", "payments": "payment-gateway", "storefront": "shop-front", "media": "image-resizer",
		"notifications": "notification"}
	for idx, r := range list {
		k := idx + 711
		svc := r.svc
		if a, ok := alias[svc]; ok {
			svc = a
		}
		p := proj[svc]
		job := "build"
		if r.kind == "deploy" {
			job = "deploy_" + strings.ReplaceAll(r.env, "-", "_")
		}
		var resAt, resNote any
		status := "open"
		if r.resH != 0 {
			status, resAt, resNote = "resolved", g.t(r.resH*H), "passed on retry"
		}
		g.ins(o, "cicd_findings", "id", k, "kind", r.kind, "key", fmt.Sprintf("%s|%s|%s|%s|%d", r.kind, svc, r.env, r.rule, k),
			"project_id", p.pid, "project", p.path, "env", r.env, "ref", r.env,
			"severity", r.sev, "klass", r.klass, "rule", r.rule, "title", r.title, "detail", r.title+".", "evidence", r.evid,
			"proposals", jd(L(om("text", r.prop, "rule", r.rule, "api", "POST /projects/:id/pipelines/:pid/retry (execution disabled)"))),
			"pipeline_id", nil, "job_id", nil, "job_name", job,
			"sha", h12(fmt.Sprintf("%s%s%d", svc, r.env, k)), "author", PEOPLE[k%8].user, "mr", nil, "occurrences", 1+k%4,
			"first_seen", g.t((r.agoH+2)*H), "last_seen", g.t(r.agoH*H), "status", status,
			"resolved_at", resAt, "resolve_note", resNote, "incident_id", nil, "llm", 1, "notified", 1)
	}
}

func (g *Gen) queueRCA() {
	type row struct {
		rid, iid    int
		title, slug string
		days        float64
		st, src     string
		cost        float64
	}
	for _, r := range []row{
		{40, 131, "prod-db-02 replication lag — vacuum on orders blocked the replica", "replication-lag-vacuum", 4.2, "merged", "rca-deep", 0.92},
		{41, 132, "admin login slow — sequential scan on sessions", "admin-login-sessions-index", 8.7, "merged", "rca-deep", 0.66},
		{42, 133, "Orders queue backlog — notification consumer deadlock", "orders-queue-consumer-deadlock", 11.8, "merged", "rca-deep", 1.04},
		{43, 134, "Staging wildcard certificate expired — hook not in the check list", "staging-wildcard-expired", 14.7, "merged", "alert-rca", 0.21},
		{44, 146, "prod-mon-01 loki disk growth — debug logs from checkout", "loki-disk-debug-logs", 1.6, "open", "alert-rca", 0.34},
		{45, 149, "dev-runner-01 disk at 88 % — no image prune", "runner-disk-images", 0.9, "local", "alert-rca", 0.18},
		{46, 150, "warehouse-api release deploy — stuck migration session", "warehouse-migration-lock", 0.35, "open", "rca-deep", 0.88},
		{47, 148, "Staging coupon 500 — null minimum order", "staging-coupon-null-min-order", 0.75, "closed", "rca-deep", 0.57},
	} {
		parts := strings.Split(r.title, " — ")
		last := parts[len(parts)-1]
		status := "mitigated"
		if r.st == "merged" {
			status = "fixed"
		}
		md := "## Lazy reading\n- **What broke:** " + parts[0] + "\n- **Why:** " + last +
			"\n- **Status:** " + status + "\n\n## Evidence\n- Argus's own alerts and the " +
			"incident timeline.\n\n## Root cause\n" + py.Capitalize(last) + ".\n\n## How to fix it\n\n" +
			"### Option 1: remove the cause (recommended)\n- **Effort:** S\n- **Risk:** low\n"
		by := "argus"
		if r.src == "rca-deep" {
			by = PEOPLE[r.rid%8].user
		}
		g.rcaReport(r.rid, r.iid, r.title, r.slug, r.days*D, r.st, md, rcaOpts{source: r.src, cost: r.cost, dur: int64(300 + r.rid*20), by: by})
	}
}

func (g *Gen) queueQA() {
	o := g.ops
	riley, quinn, avery := QA_PEOPLE[0], QA_PEOPLE[1], QA_PEOPLE[2]
	type row struct {
		id       int
		inc      any
		title    string
		env, sev string
		act      string
		who      person
		agoH     float64
		answered bool
	}
	for _, r := range []row{
		{66, 144, "OTP check in Kuma went red during the outage", "release", "critical", "actionable", riley, 5.2, true},
		{67, 147, "Search very slow at night", "release", "medium", "actionable", quinn, 3.1 * 24, true},
		{68, 133, "Order confirmation emails delayed by an hour", "release", "high", "actionable", avery, 12 * 24, true},
		{69, 134, "Staging shows a certificate warning", "pre-release", "low", "actionable", quinn, 15 * 24, true},
		{70, nil, "Gift card balance wrong after a partial refund", "release", "high", "needs_info", avery, 3.5 * 24, false},
		{71, nil, "Wishlist share link opens a blank page", "release", "low", "needs_info", riley, 36, false},
		{72, 132, "Admin panel login takes very long", "release", "medium", "actionable", avery, 9 * 24, true},
		{73, nil, "Address form drops the unit number", "release", "low", "actionable", quinn, 8, true},
	} {
		ago := r.agoH * H
		actionable := r.act == "actionable"
		var q, missing, questions, askNote, qEvent, askedAt, answeredAt, actAt any
		missing, questions, askNote = "[]", "[]", "actionable"
		askStatus := "skipped"
		if !actionable {
			q = "Argus: which account or order, which device, and roughly when?"
			missing = jd(sl("account_or_order", "device_or_browser", "time"))
			questions = jd(sl("Which account or order?", "Which device?", "When?"))
			askStatus, askNote = "sent", nil
			qEvent = fmt.Sprintf("$askNw%dq", r.id)
			askedAt = g.t(ago - 10*M)
			if r.answered {
				answeredAt = g.t(ago - H)
			}
		} else {
			actAt = g.t(ago - 5*M)
		}
		it := om("id", r.id, "incident_id", r.inc, "title", r.title, "environment", r.env, "severity", r.sev, "actionability", r.act,
			"confirmed", iif(actionable, 1, 0), "suspicious", 0, "missing", missing, "questions", questions, "lang", "en",
			"reporter", r.who.user, "reporter_name", r.who.name, "room_id", ROOM_QA, "ask_status", askStatus,
			"ask_note", askNote, "question_text", q, "question_event", qEvent,
			"asked_at", askedAt, "answered_at", answeredAt, "actionable_at", actAt, "first_ts", g.t(ago+5*M), "last_ts", g.t(ago),
			"created_at", g.t(ago), "updated_at", g.t(ago-10*M))
		g.insIssue(it)
		reason := "needs more detail"
		if r.inc != nil {
			reason = fmt.Sprintf("linked to incident #%d", r.inc)
		}
		_ = o
		g.msg(ago+5*M, ROOM_QA, "#qa", "qa", r.who.user, r.who.name, r.title+".", "problem", reason, nil, "chat-triage", nil, r.inc, r.id)
	}
	for _, m := range []struct {
		ago             float64
		who             person
		body, disp, why string
	}{
		{30 * M, riley, "Regression run for release 10.28 is green on staging.", "not_a_problem", "a status update"},
		{3 * H, quinn, "Thanks, the coupon fix works on staging now.", "not_a_problem", "thanks"},
		{1.2 * D, avery, "Is anyone looking at the slow order history page?", "known", "same as proposal 67 (order history)"},
		{1.6 * D, riley, "Hello team, new test devices arrived.", "not_a_problem", "chat"},
		{2.8 * D, quinn, "Search 'in stock' filter shows sold-out items on staging.", "problem", "reproducible on staging"},
	} {
		g.msg(m.ago, ROOM_QA, "#qa", "qa", m.who.user, m.who.name, m.body, m.disp, m.why, nil, "chat-triage", nil, nil, nil)
	}
}

func (g *Gen) queueLessons() {
	b := g.board
	type row struct {
		id                    int
		title, body, svc, env string
		kind                  string
		tags                  []string
		st, src               string
		review, sup           any
		days                  float64
	}
	for _, r := range []row{
		{6, "Prune runner images before a release day", "dev-runner-01 fills with images on busy days; prune the week-old " +
			"images the evening before.", "ci-runner", "develop", "procedure", sl("disk", "ci"), "proposed", "OPS-219", "new", nil, 0.4},
		{7, "Loki retention: change it before the log level", "Raise retention or lower the level first; debug logs fill " +
			"prod-mon-01 in two days.", "loki", "release", "trap", sl("disk", "logging"), "proposed", "OPS-222", "update", 2, 0.2},
		{8, "Restart staging nodes in any order", "Staging has two instances per service now.", "coupon-service", "pre-release",
			"procedure", sl("restart"), "rejected", "OPS-229", nil, nil, 4},
		{9, "Use the old SMS provider by default", "Fabrikam was the default before 10-01.", "sms-otp", "release", "fact",
			sl("payments"), "rejected", "OPS-232", nil, nil, 2.5},
		{10, "Matomo archive runs at 01:00", "The archive cron moved to 03:30 with the backup window change.", "matomo",
			"release", "fact", sl("cron"), "retired", "OPS-190", nil, nil, 20},
		{11, "Search re-index needs 8 threads", "Superseded: the re-index runs at 04:00 with 2 threads.", "search", "release",
			"procedure", sl("search"), "retired", "OPS-198", nil, nil, 25},
		{12, "Grant the backup role after rotating the Mongo password", "The rotation drops the role on prod-mongo-02.",
			"mongo", "release", "trap", sl("backup"), "approved", "OPS-214", "retire?", nil, 30},
	} {
		decided := r.st != "proposed"
		approved := r.st == "approved"
		var decBy, decAt, lastUsed any
		if decided {
			decBy, decAt = "sam.roe", g.t(r.days*D)
		}
		if approved {
			lastUsed = g.t(r.days * D)
		}
		g.ins(b, "lessons", "id", r.id, "title", r.title, "body", r.body, "service", r.svc, "env", r.env, "kind", r.kind, "tags", jd(r.tags),
			"norm_hash", h12(r.title), "state", r.st, "source_key", r.src, "evidence", "Task "+r.src+" run log", "proposed_at", g.t(r.days*D+H),
			"decided_by", decBy, "decided_at", decAt, "uses", iif(approved, 1, 0), "last_used", lastUsed,
			"unhelpful", iif(r.review == "retire?", 2, 0), "supersedes", r.sup, "review", r.review)
	}
}

func (g *Gen) queueDecided() {
	rank := map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}
	type row struct {
		path, title, env, sev, st string
		agoH                      float64
		why                       any
	}
	rows := []row{
		{"cve", "Upgrade curl on the edge pool (CVE-2026-21404)", "release", "medium", "accepted", 3, nil},
		{"qa", "Order history times out for large accounts", "release", "high", "accepted", 6, nil},
		{"cicd", "media: keep CI artifacts 7 days instead of 30", "develop", "low", "rejected", 5, "already handled by the runner prune"},
		{"page", "prod-misc-01 exporter flapping", "release", "low", "expired", 20, nil},
		{"cred", "Rotate the staging SMTP password", "pre-release", "low", "expired", 44, nil},
		{"cve", "Patch libxml2 on the staging Matomo copy", "pre-release", "low", "rejected", 70, "the staging copy is being removed"},
		{"cicd", "coupon-service: add a deploy lock on pre-release", "pre-release", "high", "accepted", 96, nil},
		{"page", "prod-shop-01 nginx worker restarts", "release", "medium", "expired", 150, nil},
	}
	base := int(g.oneInt(g.board, "SELECT max(id) FROM proposals")) + 1
	for idx, r := range rows {
		k := base + idx
		ago := r.agoH * H
		sk := r.path
		if r.path == "page" {
			sk = "incident"
		}
		created := ago + 2*D
		var decBy, reason any
		if r.st == "expired" {
			created = ago + 14*D
			reason = "not decided within 14 days"
		} else {
			decBy = PEOPLE[k%3].user
		}
		if r.st == "rejected" {
			reason = r.why
		}
		g.ins(g.board, "proposals", "id", k, "path", r.path, "source_kind", sk, "source_id", fmt.Sprint(k),
			"source_url", srcURL(r.path),
			"title", r.title, "description", r.title+".", "evidence", "From Argus.", "fix_prompt", "", "env", r.env, "host", "",
			"priority", prio(r.sev), "severity", r.sev, "sev_rank", rank[r.sev],
			"labels", jd(sl("argus", r.path)), "assignee_suggestion", PEOPLE[k%6].user, "why", "matches the "+r.path+" intake rule",
			"board_col", 1, "hash", h12(r.title), "state", r.st, "snooze_until", nil,
			"created_at", g.t(created), "updated_at", g.t(ago), "refreshed", 1, "decided_at", g.t(ago),
			"decided_by", decBy, "reason", reason, "task_id", nil)
	}
}
