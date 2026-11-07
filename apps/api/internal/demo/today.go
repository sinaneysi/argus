package demo

type todayItem struct {
	ref, kind, area, env, title, do, why, link, label string
	est                                               int
	score, u, i, c                                    float64
	slot, status                                      string
	flags                                             map[string]int
	prompt                                            string
}

func flagOr(m map[string]int, k string, def int) int {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func (g *Gen) today() {
	o := g.ops
	day := g.day(0)
	pid := g.ins(o, "today_plans", "day", day, "created_at", g.t(6*H), "updated_at", g.t(20*M), "version", 2, "budget_min", 240, "max_items", 6,
		"max_risky", 1, "planned_min", 215, "n_today", 6, "n_later", 8, "trigger", "timer", "by", "argus", "refined", 1, "cost_usd", 0.06,
		"note", "re-planned after incident #141")
	none := map[string]int{}
	items := []todayItem{
		{"incident:141", "incident", "incidents", "release", "Ship the sms-otp timeout and failover (OPS-231)",
			"Approve the 21:00 UTC window and watch the rolling restart of prod-pay-02, then prod-pay-01.",
			"The workaround is a manual flag; the next provider slowdown repeats the outage.", "/incidents/view?id=141", "Open incident",
			45, 9.4, 3.0, 3.0, 0.95, "today", "doing", map[string]int{"needs_window": 1, "needs_approval": 1}, "/rca/prompt.md?incident=141"},
		{"problem:7", "problem", "problems", "release", "Confirm the root cause of P-7 and link the Kuma check",
			"Accept the suggested link (incident #144) and set the problem's workaround to the feature flag.",
			"One problem, four issues: keeping them together closes them together.", "/problems/view?id=7", "Open problem",
			10, 7.1, 2.0, 3.0, 0.9, "today", "planned", none, ""},
		{"incident:145", "incident", "incidents", "release", "Restore shop-front :3001 on prod-shop-02",
			"Copy NEXT_PUBLIC_API_BASE into shop-front-3001.env and start the instance (Option 1 of the RCA).",
			"A quarter of www requests answer 502 right now.", "/incidents/view?id=145", "Open incident", 15, 8.8, 3.0, 2.5, 0.95,
			"today", "planned", map[string]int{"needs_approval": 1}, "/rca/prompt.md?incident=145"},
		{"cve:reboot:production", "cve", "vulns", "release", "Plan the kernel reboot wave (21 hosts, CVE-2026-31337 KEV)",
			"Book a window with the shop and payments owners; start with the edges.", "KEV with a 2-day SLA, already late.",
			"/vulns?group=" + "Ubuntu%3A24.04%3ALTS%7Clinux%7Cproduction", "Open vulnerability", 30, 7.9, 2.5, 2.5, 0.9, "today",
			"planned", map[string]int{"needs_window": 1}, ""},
		{"cert:sellers", "cert", "certs", "release", "Fix the DNS-01 token for sellers.northwind.example",
			"Rotate the DNS API token in the certbot hook and run certbot renew --cert-name sellers.", "Expires in 9 days; renewal fails daily.",
			"/certs", "Open certificates", 20, 6.2, 2.0, 2.0, 0.95, "today", "planned", none, ""},
		{"cicd:701", "cicd", "cicd", "release", "Unblock the warehouse-api release deploy",
			"Ask the DBA to end the stuck migration session on prod-db-01, then retry deploy_production.",
			"The release has waited 9 hours.", "/cicd/finding?id=701", "Open finding", 20, 5.9, 2.0, 2.0, 0.9, "today", "done",
			map[string]int{"own_action": 0}, ""},
		{"cicd:stuck:storefront", "cicd", "cicd", "pre-release", "Clear the stuck storefront staging pipeline",
			"Cancel the older pipeline that holds the staging lock, then retry deploy_staging.",
			"Two merge requests wait behind it.", "/cicd", "Open CI/CD", 10, 4.4, 1.5, 1.5, 0.9, "later", "planned", none, ""},
		{"capacity:prod-mon-01", "capacity", "capacity", "release", "Loki on prod-mon-01 full in 4.6 days",
			"Review OPS-226 and merge the retention change.", "Full in under 7 days.", "/capacity", "Open capacity", 15, 5.2, 2.0, 1.8,
			0.9, "later", "planned", none, ""},
		{"backups:mongo-prod_reviews", "backup", "backups", "release", "mongo-prod_reviews backup 52 h old",
			"Restore the backup role of the mongo backup user (OPS-211).", "No fresh backup for 2 days.", "/backups", "Open backups",
			15, 5.0, 2.0, 1.8, 0.9, "later", "planned", none, ""},
		{"vulns:php", "cve", "vulns", "release", "Rebuild analytics-matomo on PHP 8.3", "Start OPS-222 after the reboot wave.",
			"P1 and internet-facing.", "/vulns", "Open vulnerability", 60, 4.9, 2.0, 2.0, 0.85, "later", "planned", none, ""},
		{"qa:63", "qa", "qa", "release", "QA: product images slow on mobile", "Wait for Riley's answer.", "Needs info.",
			"/qa", "Open QA", 10, 2.1, 1.0, 1.0, 0.5, "later", "planned", none, ""},
		{"incident:148", "incident", "incidents", "pre-release", "Staging coupon 500", "The fixer run is done; ask QA to re-test CPN-12.",
			"Blocks the promotion test plan.", "/incidents/view?id=148", "Open incident", 5, 3.4, 1.5, 1.5, 0.9, "later", "planned", none, ""},
		{"hygiene:test", "hygiene", "hygiene", "release", "Delete the leftover test alert", "Remove TestAlertDoNotPage from the rules.",
			"It pages at critical for 13 days.", "/hygiene", "Open hygiene", 5, 3.0, 1.0, 1.5, 0.95, "later", "planned", none, ""},
		{"incident:149", "incident", "incidents", "develop", "dev-runner-01 disk", "Done by the fixer; check the timer runs Sunday.",
			"Low.", "/incidents/view?id=149", "Open incident", 5, 1.2, 0.5, 1.0, 0.9, "later", "planned", none, ""},
	}
	for rank, it := range items {
		var prompt, laterReason, started, done, actual any
		if it.prompt != "" {
			prompt = it.prompt
		}
		if it.slot == "later" {
			laterReason = "did not fit the 4 h budget"
		}
		if it.status == "doing" {
			started = g.t(35 * M)
		}
		if it.status == "done" {
			done, actual = g.t(90*M), 25
		}
		g.ins(o, "today_items", "plan_id", pid, "ref", it.ref, "kind", it.kind, "area", it.area, "env", it.env, "title", it.title, "do_this", it.do, "why_now", it.why,
			"details", jd(L(om("text", "Source: "+it.ref, "link", it.link))), "link", it.link, "link_label", it.label, "close_link", it.link,
			"prompt_url", prompt, "members", jd(sl(it.ref)), "sig", h12(it.ref), "needs_approval", flagOr(it.flags, "needs_approval", 0),
			"needs_window", flagOr(it.flags, "needs_window", 0), "own_action", flagOr(it.flags, "own_action", 1), "est_min", it.est, "est_base", it.est,
			"est_basis", "typical for "+it.kind+" items", "score", it.score, "urgency", it.u, "impact", it.i, "confidence", it.c, "slot", it.slot, "rank", rank+1,
			"later_reason", laterReason, "status", it.status,
			"carried", iif(it.ref == "cve:reboot:production", 1, 0), "started_at", started,
			"done_at", done, "actual_min", actual, "created_at", g.t(6*H), "updated_at", g.t(20*M))
	}
	g.writeJSON("today-status.json", om(
		"enabled", true, "plan_at", "07:30", "at", g.t(20*M), "day", day,
		"decisions", L(om("group", "Proposed tasks", "title", "36 proposals wait for a decision", "link", "/review?kind=proposals", "count", 36,
			"detail", "8 security, 4 credentials, 9 CI/CD"),
			om("group", "Problems", "title", "1 suggested link on P-7", "link", "/problems/view?id=7", "count", 1,
				"detail", "Kuma OTP check, confidence 88 %"),
			om("group", "Ideas", "title", "5 new ideas from the advisors", "link", "/review?kind=ideas", "count", 5,
				"detail", "DevOps, QA, NOC"))))
}
