package demo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	reT = regexp.MustCompile(`\{\{T(\d+(?:\.\d+)?)h\}\}`)
	reD = regexp.MustCompile(`\{\{D(\d+)\}\}`)
)

// render expands {{T5.2h}} (the UTC clock time 5.2 hours ago) and {{D3}} (the UTC date 3 days ago).
func (g *Gen) render(text string) string {
	text = reT.ReplaceAllStringFunc(text, func(m string) string {
		f, _ := strconv.ParseFloat(reT.FindStringSubmatch(m)[1], 64)
		return g.at(f * H).UTC().Format("15:04")
	})
	return reD.ReplaceAllStringFunc(text, func(m string) string {
		n, _ := strconv.Atoi(reD.FindStringSubmatch(m)[1])
		return g.at(float64(n) * D).UTC().Format("2006-01-02")
	})
}

func lazy(what, why, impact, fix, status string, source ...string) string {
	src := "report"
	if len(source) > 0 {
		src = source[0]
	}
	return jd(om("what", what, "why", why, "impact", impact, "fix", fix, "status", status, "source", src))
}

func (g *Gen) scheduledRuns() {
	for d := 13; d >= 0; d-- {
		st, sum := "OK", "OK: 24 certificates, nearest expiry in 41 days."
		if d < 6 {
			st, sum = "WARN", "WARN: 1 certificate expires within 14 days (sellers.northwind.example, 9 days); 23 OK."
		}
		g.run("cert-check", float64(d)*D+7*H, st, sum, runOpts{cost: fp(0.04), dur: ip(38000), turns: ip(6)})
	}
	for h := 2; h < 14*24; h += 6 {
		st := "OK"
		if h == 8 || h == 14 || h == 20 {
			st = "WARN"
		}
		var sum string
		if st == "WARN" {
			sum = "WARN: sms-otp provider timeouts on prod-pay-01/02 (Contoso SMS p99 31 s); checkout otp_timeout errors."
		} else {
			sum = []string{"OK: no new error pattern; 3 known noisy warnings (search slow query, cart retry, cdn 404).",
				"OK: error rates at baseline on both datacenters.",
				"OK: 1 transient upstream timeout on api.northwind.example (12 requests), not recurring."}[g.r.choice(3)]
		}
		cost := round(g.r.uniform(0.18, 0.42), 3)
		dur := int64(g.r.randint(90, 260)) * 1000
		g.run("log-rca", float64(h)*H+10*M, st, sum, runOpts{cost: &cost, dur: &dur})
	}
	for k := 0; k < 96; k++ {
		sum := []string{"OK: no new message in #qa.", "OK: 1 message, a known incident.", "OK: 2 messages, no new problem."}[g.r.choice(3)]
		cost := round(g.r.uniform(0.01, 0.06), 3)
		dur := int64(g.r.randint(8, 40)) * 1000
		turns := int64(g.r.randint(2, 6))
		g.run("chat-triage", float64(k)*30*M+4*M, "OK", sum, runOpts{cost: &cost, dur: &dur, turns: &turns})
	}
	for _, k := range []float64{40 * M, 10 * H, 34 * H} {
		g.run("rca-auto", k, "OK", "OK: nothing new to investigate deeply.", runOpts{cost: fp(0.02), dur: ip(12000), turns: ip(2)})
	}
}

// finding records a run with one triage problem; it returns the run id.
func (g *Gen) finding(task string, ago float64, title, sev, env, summary, rca string, sources []string, status string, idx int, suspicious int) int64 {
	if status == "" {
		status = "WARN"
	}
	cost := round(g.r.uniform(0.08, 0.6), 3)
	rid := g.run(task, ago, status, status+": "+title, runOpts{cost: &cost})
	g.ins(g.ops, "triage_problems", "run_id", rid, "idx", idx, "title", title, "severity", sev, "environment", env,
		"sources", jd(sources), "summary", summary, "rca", rca, "fix_prompt", "", "suspicious", suspicious)
	return rid
}

func (g *Gen) find(task string, ago float64, title, sev, env, summary, rca string, sources ...string) int64 {
	return g.finding(task, ago, title, sev, env, summary, rca, sources, "WARN", 0, 0)
}

type incOpts struct {
	owner           any // string or nil
	hub             []any
	acked, resolved float64 // 0: not acknowledged / not resolved
	note            any
	alertSev        any
}

func (g *Gen) incident(keyName string, iid int, key, dc, subject, family, title, env, sev string, cf int, status string,
	firstAgo, lastAgo float64, occ int, sources []string, o incOpts) {
	var hubService, hubTier, hubCallers any
	if o.hub != nil {
		hubService, hubTier, hubCallers = o.hub[0], o.hub[1], o.hub[2]
	}
	ownerOr := func(def string) any {
		if o.owner != nil {
			return o.owner
		}
		return def
	}
	var ackedAt, ackedBy, resolvedAt, resolvedBy, clearSince, threadRoot, threadRoom any
	if o.acked != 0 {
		ackedAt, ackedBy = g.t(o.acked), o.owner
	}
	if o.resolved != 0 {
		resolvedAt, resolvedBy, clearSince = g.t(o.resolved), ownerOr("argus"), g.t(o.resolved)
	}
	if cf != 0 {
		threadRoot, threadRoom = "$evt"+h12(key), ROOM_NOTIFY
	}
	g.ins(g.ops, "incidents", "id", iid, "key", key, "dc", dc, "subject", subject, "family", family, "title", title, "env", env,
		"severity", sev, "alert_severity", o.alertSev, "customer_facing", cf, "status", status, "owner", o.owner,
		"first_seen", g.t(firstAgo), "last_seen", g.t(lastAgo), "occurrences", occ, "acked_at", ackedAt, "acked_by", ackedBy,
		"resolved_at", resolvedAt, "resolved_by", resolvedBy, "resolve_note", o.note, "last_firing", g.t(lastAgo),
		"clear_since", clearSince, "thread_root", threadRoot, "thread_room", threadRoom,
		"fingerprints", jd(L("fp-"+h12(key))), "hub_service", hubService, "hub_tier", hubTier, "hub_callers", hubCallers,
		"sources", jd(sources))
	g.inc[keyName] = iid
	if o.acked != 0 {
		g.ev(iid, o.acked, "ack", nil, nil, nil, o.owner, "taking it")
	}
	if o.resolved != 0 {
		kind, text := "auto-resolve", any("no matching alert for 6 h")
		if o.note != nil {
			kind, text = "resolve", o.note
		}
		g.ev(iid, o.resolved, kind, nil, nil, nil, ownerOr("argus"), text)
	}
}

// ev is an incident event; actor nil means "argus".
func (g *Gen) ev(iid int, ago float64, kind string, runID, idx, task, actor, text any) {
	if actor == nil {
		actor = "argus"
	}
	g.ins(g.ops, "incident_events", "incident_id", iid, "at", g.t(ago), "kind", kind, "run_id", runID, "idx", idx, "task", task,
		"actor", actor, "text", text)
}

type rcaOpts struct {
	source string
	cost   float64
	dur    int64
	by     string
}

func (g *Gen) rcaReport(rid int, iid int, title, slug string, ago float64, status, md string, o rcaOpts) int64 {
	if o.source == "" {
		o.source = "rca-deep"
	}
	path := fmt.Sprintf("reports/rca/%s-%s.md", g.day(ago), slug)
	run := g.run("rca-deep", ago+float64(o.dur), "OK", "OK: "+title, runOpts{cost: fp(o.cost), dur: ip(o.dur * 1000), turns: ip(46)})
	var mr any
	if status == "open" || status == "merged" {
		mr = fmt.Sprintf("%s/ops/argus/-/merge_requests/%d", GITLAB, 100+rid%900)
	}
	var inc any
	if iid != 0 {
		inc = iid
	}
	g.ins(g.ops, "rca_reports", "id", rid, "incident_id", inc, "title", title, "path", path, "mr_url", mr, "created", g.t(ago),
		"status", status, "content", md, "run_id", run, "source", o.source, "cost_usd", o.cost, "duration_s", o.dur, "redacted", 0,
		"requested_by", o.by)
	if status == "merged" {
		g.writeText("reports/rca/"+path[len("reports/rca/"):], md)
	}
	if iid != 0 {
		g.ev(iid, ago, "rca", run, nil, "rca-deep", o.by, fmt.Sprintf("RCA report #%d: %s", rid, title))
	}
	return run
}

const SHORT_RCA = `## Lazy reading
- **What broke:** {what}
- **Why:** {why}
- **Impact:** {impact}
- **Fix:** {fix}
- **Status:** {status}

## Evidence
{evidence}

## Root cause
{cause}

## How to fix it

### Option 1: {opt} (recommended)
- **Kind:** {kind}
- **What:** {change}
- **Why it works:** {works}
- **Effort:** {effort}
- **Risk:** low
- **Blast radius:** {blast}
- **Approval:** {approval}

## Prompt for a write-access agent

~~~~markdown
{prod}# {opt}
## Goal
{goal}
## Environment and targets
- Environment: {envname}
- Hosts: {hosts}
## Change, step by step
### Step 1
- Backup: {backup}
- Change: {change}
- Check: {check}
- Rollback: {rollback}
## Verify
{verify}
~~~~
`

var APPROVAL = map[string]string{"release": "PRODUCTION CHANGE: the operator approves", "pre-release": "operator OK", "develop": "fixer can do this"}
var ENVNAME = map[string]string{"release": "production", "pre-release": "staging (pre-release)", "develop": "develop"}

// kw is the keyword arguments of short_rca, in pairs.
func shortRCA(env string, kw ...string) string {
	m := map[string]string{"approval": APPROVAL[env], "envname": ENVNAME[env], "prod": ""}
	for i := 0; i+1 < len(kw); i += 2 {
		m[kw[i]] = kw[i+1]
	}
	if env == "release" {
		m["prod"] = "PRODUCTION CHANGE — needs the operator's explicit approval before any step.\n"
	}
	var pairs []string
	for k, v := range m {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(SHORT_RCA)
}

func (g *Gen) incidents() {
	o := g.ops
	smsMD := g.render(content("rca-sms-otp.md"))
	shopMD := g.render(content("rca-shop-front-502.md"))
	lokiMD := g.render(content("rca-loki-disk.md"))
	searchMD := g.render(content("rca-search-latency-2026.md"))

	// 1. the payment-provider SMS/OTP outage (deep RCA, Lazy reading, How to fix, prompt)
	g.incident("sms", 141, "EAST|sms-otp|queue-backlog", "EAST", "sms-otp", "queue-backlog",
		"sms-otp: payment OTP SMS not delivered (queue backlog 11,800)", "release", "critical", 1, "acknowledged",
		4.9*H, 2.4*H, 9, sl("alertmanager", "kuma", "qa"), incOpts{owner: "alex.doe", hub: L("sms-otp", 1, 7),
			acked: 4.6 * H, alertSev: "critical"})
	rca1 := shortRCA("release", "what", "OTP SMS stopped arriving; sms-otp queue at 11,800 on prod-pay-01 and prod-pay-02.",
		"why", "Every sms-otp worker waits on Contoso SMS, which answers in 30 s (proven).",
		"impact", "Card payments fail at 3-D Secure; login OTP delayed for all customers.",
		"fix", "Switch the provider flag to Fabrikam now (Option 1) — the operator approves (production).",
		"status", "needs operator",
		"evidence", "- `sms_otp_workers_busy` = 64 of 64 on both hosts since "+g.render("{{T5.1h}}")+" UTC.\n"+
			"- Loki: `provider=contoso latency_ms=31204` repeated; no answer for 12 % of requests.\n"+
			"- `SMS_PROVIDER_TIMEOUT` is not set in `/etc/northwind/sms-otp.env`.",
		"cause", "A slow provider with no client timeout blocks the fixed worker pool.",
		"opt", "Fail over to Fabrikam Messaging by feature flag", "kind", "mitigation",
		"change", "set `sms.provider=fabrikam` in feature-flags and restart `northwind-sms-otp` on prod-pay-01/02",
		"works", "the healthy provider answers in 400 ms; the queue drains", "effort", "10 min",
		"blast", "OTP delivery for about 1 minute during the restart",
		"goal", "OTPs arrive in under 10 s and the queue is empty.", "hosts", "prod-pay-01 (10.20.30.41), prod-pay-02 (10.20.30.42)",
		"backup", "note the current flag value", "check", "`sms_otp_queue_depth` falls to 0 within 15 min",
		"rollback", "set the flag back to `contoso`", "verify", "A test OTP to the QA phone arrives within 10 s.")
	r1 := g.find("incident", 4.85*H, "sms-otp queue backlog: OTP SMS not delivered", "critical", "release",
		"The sms-otp queue on prod-pay-01 and prod-pay-02 grows; all 64 workers are blocked on the SMS provider.",
		rca1, "alertmanager: SmsOtpQueueBacklog prod-pay-01", "kuma: OTP delivery (synthetic) down")
	g.ev(141, 4.85*H, "finding", r1, 0, "incident", nil, nil)
	r1b := g.find("chat-triage", 4.7*H, "QA: payment OTP code never arrives", "critical", "release",
		"Two QA testers and support report that the payment OTP SMS does not arrive.", rca1,
		"#qa: Riley (QA) "+g.render("{{T4.8h}}"), "#qa: Avery (Support)")
	g.ev(141, 4.7*H, "finding", r1b, 0, "chat-triage", nil, nil)
	g.ev(141, 4.4*H, "owner", nil, nil, nil, "alex.doe", "owner: alex.doe")
	for _, a := range []float64{4.3, 3.9, 3.5, 3.1, 2.8, 2.4} {
		sum, word := "WARN: queue draining", "draining"
		if a > 2.6 {
			sum, word = "WARN: sms-otp queue backlog still growing", "growing"
		}
		rr := g.run("incident", a*H, "WARN", sum, runOpts{})
		g.ev(141, a*H, "finding", rr, nil, "incident", nil, "SmsOtpQueueBacklog firing ("+word+")")
	}
	g.ev(141, 2.5*H, "other", nil, nil, nil, "alex.doe", "mitigation: provider flag switched to Fabrikam, sms-otp restarted on both hosts")
	g.rcaReport(51, 141, "Payment OTP SMS not delivered — sms-otp blocked on Contoso SMS timeouts", "sms-otp-provider-timeouts",
		2.0*H, "open", smsMD, rcaOpts{cost: 1.12, dur: 1680, by: "alex.doe"})
	g.exec(o, "UPDATE incidents SET lazy=? WHERE id=141", lazy(
		"Payment and login OTP SMS were not delivered for 2 h 40 min.",
		"Contoso SMS answered in 30 s; sms-otp has no timeout, so all 64 workers hung (proven).",
		"2,140 card payments failed at 3-D Secure; about 31 % of checkouts.",
		"Provider timeout with automatic failover (Option 1) — the operator approves (production).",
		"mitigated", "report"))
	g.exec(o, "UPDATE incidents SET lazy=json_set(lazy, '$.ref', 'rca:51') WHERE id=141")

	// 2. checkout failing at 3-D Secure (same problem)
	g.incident("checkout", 142, "EAST|checkout.northwind.example|http-5xx", "EAST", "checkout.northwind.example",
		"payment-failures", "checkout.northwind.example: payment failure rate 31 % (otp_timeout)", "release", "critical",
		1, "open", 4.6*H, 2.3*H, 5, sl("alertmanager"), incOpts{hub: L("payment-gateway", 1, 4), alertSev: "page"})
	rca2 := shortRCA("release", "what", "Card payments fail with `otp_timeout` on checkout.northwind.example (31 %).",
		"why", "payment-gateway waits 90 s for the OTP step; the OTP SMS never arrives (proven).",
		"impact", "2,140 failed payments in 2 h 40 min; customers see \"Payment could not be confirmed\".",
		"fix", "Fix sms-otp first (incident #141); no change in payment-gateway — the operator approves.",
		"status", "mitigated", "evidence", "- `payment-gateway` logs: `reason=otp_timeout` 2,140 times.\n"+
			"- The failures start 4 minutes after the sms-otp backlog.",
		"cause", "A downstream symptom of the sms-otp provider timeouts.",
		"opt", "No change in payment-gateway", "kind", "mitigation", "change", "none; follow incident #141",
		"works", "the failures stop when OTPs arrive again", "effort", "0 min", "blast", "none",
		"goal", "Payment failure rate back to 0.3 %.", "hosts", "prod-pay-01, prod-pay-02",
		"backup", "-", "check", "failure rate", "rollback", "-", "verify", "`payment_failures_total{reason=\"otp_timeout\"}` flat.")
	r2 := g.find("incident", 4.55*H, "Payment failure rate 31 % on checkout (otp_timeout)", "critical", "release",
		"Card payments fail at the 3-D Secure OTP step.", rca2, "alertmanager: PaymentFailureRateHigh checkout")
	g.ev(142, 4.55*H, "finding", r2, 0, "incident", nil, nil)

	// 3. prod-pay-01 memory (OOM kills of sms-otp), same problem
	g.incident("paymem", 143, "EAST|prod-pay-01|memory", "EAST", "prod-pay-01", "memory",
		"prod-pay-01 memory 94 %: sms-otp OOM-killed 4 times", "release", "high", 0, "open", 4.4*H, 25*M, 6,
		sl("alertmanager", "auto"), incOpts{alertSev: "warning"})
	rca3 := shortRCA("release", "what", "prod-pay-01 at 94 % memory; sms-otp OOM-killed 4 times in an hour.",
		"why", "The sms-otp queue keeps 11,800 message bodies in memory (proven).",
		"impact", "Each OOM kill drops in-flight OTPs; payment-gateway on the same host slows down.",
		"fix", "Cap the queue at 2,000 and spill to RabbitMQ (Option 1) — the operator approves.",
		"status", "open", "evidence", "- cgroup `memory.events`: oom_kill 4.\n- `sms_otp_queue_depth` 11,800 at the peak.",
		"cause", "Unbounded in-memory queue during the provider slowdown.",
		"opt", "Bound the in-memory queue", "kind", "permanent fix",
		"change", "`SMS_QUEUE_MAX=2000` and `SMS_QUEUE_SPILL=amqp://prod-queue-01/sms` in sms-otp.env",
		"works", "memory stays flat; excess messages wait in RabbitMQ", "effort", "30 min",
		"blast", "sms-otp on prod-pay-01", "goal", "sms-otp RSS stays under 1 GB under backlog.",
		"hosts", "prod-pay-01 (10.20.30.41)", "backup", "copy the env file", "check", "RSS < 1 GB",
		"rollback", "restore the env file", "verify", "No OOM kill in 24 h.")
	r3 := g.find("vm-explain", 4.2*H, "prod-pay-01: memory pressure from the sms-otp backlog", "high", "release",
		"Memory 94 %, sms-otp holds 3.1 GB; 4 OOM kills.", rca3, "vm-pulse: memory critical 15 min")
	g.ev(143, 4.2*H, "finding", r3, 0, "vm-explain", nil, nil)
	g.ins(o, "vm_reports", "vm_id", g.vmIDs["prod-pay-01"], "name", "prod-pay-01", "env", "release", "alias", "prod-pay-01-ro", "mode", "full",
		"created", g.t(4.2*H), "requested_by", "auto", "trigger", "memory critical for 15 minutes", "run_id", r3, "status", "WARN", "cost_usd", 0.41)

	// 4. Kuma synthetic OTP check (resolved, part of the problem)
	g.incident("kuma", 144, "KUMA|OTP delivery (synthetic)|down", "KUMA", "OTP delivery (synthetic)", "down",
		"Kuma: OTP delivery (synthetic) down", "release", "critical", 1, "resolved", 5.1*H, 2.4*H, 3,
		sl("kuma"), incOpts{resolved: 2.2 * H})
	rk := g.find("incident", 5.05*H, "Kuma: OTP delivery (synthetic) down", "critical", "release",
		"The synthetic OTP check gets no SMS within 60 s.", rca1, "kuma: OTP delivery (synthetic) [Down]")
	g.ev(144, 5.05*H, "finding", rk, 0, "incident", nil, nil)

	// 5. shop-front dead upstream (deep RCA, VM page)
	g.incident("shop502", 145, "EAST|www.northwind.example|http-5xx", "EAST", "www.northwind.example", "http-5xx",
		"www.northwind.example 502 on 25 % of requests (prod-shop-02:3001 dead)", "release", "high", 1, "open",
		7*H, 20*M, 14, sl("alertmanager"), incOpts{hub: L("shop-front", 1, 3), alertSev: "critical"})
	r5 := g.find("incident", 6.9*H, "502 from nginx upstream shop_front: prod-shop-02:3001 refuses connections", "high",
		"release", "One of four shop-front backends is not listening since the 10-23 deploy.", shopMD,
		"alertmanager: Nginx5xxRateHigh prod-edge-01", "nginx error log prod-edge-01")
	g.ev(145, 6.9*H, "finding", r5, 0, "incident", nil, nil)
	g.rcaReport(52, 145, "www.northwind.example 502 — one shop-front instance not listening", "shop-front-dead-upstream",
		5.5*H, "open", shopMD, rcaOpts{cost: 0.71, dur: 940, by: "auto"})
	g.ins(o, "vm_reports", "vm_id", g.vmIDs["prod-shop-02"], "name", "prod-shop-02", "env", "release", "alias", "prod-shop-02-ro", "mode", "full",
		"created", g.t(6.0*H), "requested_by", "auto", "trigger", "dead upstream :3001", "run_id", r5, "status", "WARN", "cost_usd", 0.38)

	// 6. loki disk (storage analysis)
	g.incident("loki", 146, "EAST|prod-mon-01|disk", "EAST", "prod-mon-01", "disk",
		"prod-mon-01 /var/lib/loki full in 4.6 days (93 % used)", "release", "medium", 0, "open", 2*D, 2*H, 3,
		sl("auto"), incOpts{owner: "sam.roe"})
	r6 := g.find("disk-explain", 1.8*D, "/var/lib/loki on prod-mon-01 full in 4.6 days", "medium", "release",
		"Debug logging in checkout tripled the log volume since 10-18.", lokiMD, "capacity: full in 4.6 d")
	g.ev(146, 1.8*D, "finding", r6, 0, "disk-explain", nil, nil)
	g.ins(o, "disk_reports", "fs_id", h12("east|prod-mon-01|/var/lib/loki"), "dc", "east", "host", "10.20.30.101:9100",
		"mount", "/var/lib/loki", "alias", "prod-mon-01-ro", "env", "release", "created", g.t(1.8*D), "requested_by", "auto",
		"run_id", r6, "status", "WARN", "cost_usd", 0.33)

	// 7. resolved search latency (merged report file)
	g.incident("search", 147, "EAST|api.northwind.example|latency", "EAST", "api.northwind.example", "latency",
		"api.northwind.example search p95 2.4 s during the nightly re-index", "release", "medium", 1, "resolved",
		3*D, 3*D-50*M, 4, sl("alertmanager"), incOpts{owner: "jamie.poe", resolved: 2 * D,
			note: "re-index moved to 04:00 with 2 bulk threads; blue/green alias planned (OPS-214)"})
	r7 := g.find("log-rca", 3*D-30*M, "Search latency during the catalogue re-index", "medium", "release",
		"Bulk indexing competes with live queries on prod-search-01/02.", searchMD, "loki: search slow query")
	g.ev(147, 3*D-30*M, "finding", r7, 0, "log-rca", nil, nil)
	g.rcaReport(49, 147, "Search latency on api.northwind.example during the catalogue re-index", "search-latency-reindex",
		2.6*D, "merged", searchMD, rcaOpts{cost: 0.66, dur: 820, by: "jamie.poe"})

	// 8. staging: coupon 500 from QA (fixable by the fixer: pre-release)
	g.incident("coupon", 148, "QA|stg checkout apply-coupon|http-5xx", "", "checkout.stg.northwind.example", "http-5xx",
		"staging: 500 on /cart/apply-coupon for percentage coupons", "pre-release", "medium", 0, "open", 20*H, 3*H, 3,
		sl("qa"), incOpts{owner: "taylor.noe"})
	r8 := g.find("chat-triage", 19*H, "staging checkout: 500 on apply-coupon (percentage coupons)", "medium", "pre-release",
		"QA: percentage coupons return HTTP 500 on staging; fixed-amount coupons work.",
		shortRCA("pre-release", "what", "`POST /cart/apply-coupon` returns 500 for percentage coupons on staging.",
			"why", "coupon-service 3.9.0 divides by `min_order` which is null for percentage coupons (proven).",
			"impact", "QA blocked on the 10-28 promotion test plan; production not affected (3.8.2).",
			"fix", "Roll staging coupon-service back to 3.8.2 (Option 1) — operator OK.", "status", "open",
			"evidence", "- `ZeroDivisionError` in coupon-service logs on stg-app-01, 41 times.\n- Started with the 3.9.0 deploy.",
			"cause", "A regression in coupon-service 3.9.0.", "opt", "Roll coupon-service back to 3.8.2 on staging",
			"kind", "mitigation", "change", "redeploy `shop/coupon-service` tag 3.8.2 to staging",
			"works", "3.8.2 has no division on min_order", "effort", "15 min", "blast", "staging only",
			"goal", "apply-coupon answers 200 for percentage coupons on staging.", "hosts", "stg-app-01 (10.20.20.21)",
			"backup", "note the running tag", "check", "`curl -s -o /dev/null -w '%{http_code}' ...` → 200",
			"rollback", "redeploy 3.9.0", "verify", "QA test case CPN-12 passes."),
		"#qa: Quinn (QA) "+g.render("{{T20h}}"))
	g.ev(148, 19*H, "finding", r8, 0, "chat-triage", nil, nil)

	// 9. develop runner disk
	g.incident("runner", 149, "WEST|dev-runner-01|disk", "WEST", "dev-runner-01", "disk", "dev-runner-01 / at 88 % (docker images)",
		"develop", "low", 0, "open", 3*D, 6*H, 4, sl("alertmanager"), incOpts{})
	r9 := g.find("incident", 3*D, "dev-runner-01 disk 88 %: old docker images", "low", "develop",
		"412 dangling images and build cache fill the root filesystem.",
		shortRCA("develop", "what", "`/` on dev-runner-01 is 88 % used.", "why", "Docker build cache and 412 dangling images (proven).",
			"impact", "CI jobs on this runner fail when the disk is full; about 2 days left.",
			"fix", "Prune images older than 7 days and add a weekly timer (Option 1) — fixer can do this.",
			"status", "open", "evidence", "- `docker system df`: images 61 GB, build cache 38 GB.",
			"cause", "No image pruning on the runner.", "opt", "Prune docker images and cache weekly",
			"kind", "permanent fix", "change", "`docker system prune -af --filter until=168h` and a weekly systemd timer",
			"works", "frees about 90 GB and keeps it free", "effort", "15 min", "blast", "dev-runner-01 CI jobs (cache misses)",
			"goal", "`/` under 60 % used.", "hosts", "dev-runner-01 (10.30.10.31)", "backup", "-",
			"check", "`df -h /`", "rollback", "disable the timer", "verify", "`df -h /` under 60 %."),
		"alertmanager: NodeFilesystemAlmostFull dev-runner-01")
	g.ev(149, 3*D, "finding", r9, 0, "incident", nil, nil)

	// 10. CI/CD: release deploy failed
	g.incident("cideploy", 150, "CICD|warehouse/warehouse-api|deploy", "CICD", "warehouse/warehouse-api", "deploy",
		"Release deploy of warehouse-api failed: migration lock held", "release", "high", 0, "open", 9*H, 9*H, 1, sl("cicd"), incOpts{})
	g.ev(150, 9*H, "finding", nil, nil, "cicd", nil, "pipeline 88412 job deploy_production failed: could not obtain migration lock")

	// 11. security: kernel KEV
	g.incident("kernel", 151, "SECURITY|linux|cve", "", "linux", "cve",
		"CVE-2026-31337 (Linux kernel, KEV) on 21 production hosts: reboot wave needed", "release", "high", 0, "open",
		3*D, 2*H, 3, sl("security"), incOpts{owner: "robin.moe"})
	g.ev(151, 3*D, "finding", nil, nil, "cve", nil, "P1: kernel 6.8.0-41 vulnerable, fixed in 6.8.0-45; 21 hosts need a reboot")

	// 12. certificate
	g.incident("cert", 152, "AUTO|sellers.northwind.example|cert", "", "sellers.northwind.example", "cert",
		"TLS certificate for sellers.northwind.example expires in 9 days (renewal failing)", "release", "medium", 1,
		"acknowledged", 5*D, 7*H, 6, sl("auto"), incOpts{owner: "casey.loe", acked: 4 * D})
	g.ev(152, 5*D, "finding", nil, nil, "cert-check", nil, "certbot renewal failed: DNS-01 challenge, TXT record not found")

	// older, resolved
	type old struct {
		iid                                   int
		name, key, subj, fam, title, env, sev string
		ago, res                              float64
		own, note                             string
	}
	for _, x := range []old{
		{131, "lag", "EAST|prod-db-02|replication", "prod-db-02", "replication", "prod-db-02 replication lag 45 s",
			"release", "high", 4.5 * D, 4.3 * D, "sam.roe", "vacuum on orders finished; lag back to < 1 s"},
		{132, "adminslow", "EAST|admin.northwind.example|latency", "admin.northwind.example", "latency",
			"admin.northwind.example login slow (8 s)", "release", "medium", 9 * D, 8.8 * D, "jamie.poe",
			"missing index on sessions.user_id added"},
		{133, "queue", "EAST|prod-queue-01|queue", "prod-queue-01", "queue", "RabbitMQ orders queue backlog 40,000",
			"release", "high", 12 * D, 11.9 * D, "alex.doe", "notification consumer restarted after a deadlock"},
		{134, "stgcert", "AUTO|stg.northwind.example|cert", "*.stg.northwind.example", "cert",
			"staging wildcard certificate expired", "pre-release", "low", 15 * D, 14.8 * D, "casey.loe", "renewed by hand; certbot hook fixed"},
	} {
		cf := 0
		if x.env == "release" {
			cf = 1
		}
		g.incident(x.name, x.iid, x.key, strings.Split(x.key, "|")[0], x.subj, x.fam, x.title, x.env, x.sev, cf, "resolved",
			x.ago, x.ago-2*H, g.r.randint(2, 7), sl("alertmanager"), incOpts{owner: x.own, acked: x.ago - 15*M, resolved: x.res, note: x.note})
		rr := g.find("incident", x.ago-5*M, x.title, x.sev, x.env, x.title+".", "", "alertmanager")
		g.ev(x.iid, x.ago-5*M, "finding", rr, 0, "incident", nil, nil)
	}
}
