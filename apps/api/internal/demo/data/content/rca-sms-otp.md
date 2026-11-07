# RCA: payment OTP SMS not delivered — sms-otp blocked on Contoso SMS timeouts

## Lazy reading
- **What broke:** Payment and login OTP SMS were not delivered for 2 h 40 min from {{T5.2h}} UTC.
- **Why:** Contoso SMS answered in 30 s or not at all; sms-otp has no timeout, so all 64 workers hung (proven).
- **Impact:** 2,140 card payments failed at the 3-D Secure step; about 31 % of checkouts in that window.
- **Fix:** Set a 3 s provider timeout with failover to Fabrikam Messaging (Option 1) — the operator approves (production).
- **Status:** mitigated

## Summary

From {{T5.2h}} UTC the SMS provider **Contoso SMS** (primary) slowed down: its `POST /v2/messages` answered after
25–31 s or the TCP connection stayed open without an answer. `sms-otp` on `prod-pay-01` and `prod-pay-02` calls the
provider synchronously from a fixed pool of 64 workers and has **no client timeout** (`SMS_PROVIDER_TIMEOUT` is
unset, the HTTP client default is infinite). Within 6 minutes every worker was waiting on Contoso, the queue grew to
11,800 messages and the OTP for 3-D Secure card payments and for login stopped arriving. `payment-gateway` waits 90 s
for the OTP step and then fails the payment, so checkouts failed with "Payment could not be confirmed".

At {{T2.5h}} UTC Alex Doe switched the provider by hand to **Fabrikam Messaging** (secondary) through the feature flag
`sms.provider=fabrikam` and restarted `sms-otp` on both hosts. The queue drained in 9 minutes; OTPs arrive again
(median 4 s). The underlying defect — no timeout, no automatic failover — is still in production.

## Evidence

| # | finding | proven/probable | source |
|---|---|---|---|
| 1 | Contoso `POST /v2/messages` p99 31.2 s from {{T5.2h}} (was 380 ms) | proven | Loki `{app="sms-otp"} |= "provider=contoso"` |
| 2 | 64 of 64 workers busy, queue 11,800 at the peak | proven | metric `sms_otp_workers_busy`, `sms_otp_queue_depth` |
| 3 | `SMS_PROVIDER_TIMEOUT` not set on either host | proven | `systemctl show northwind-sms-otp -p Environment` |
| 4 | 2,140 payments failed with `otp_timeout` in the window | proven | `payment-gateway` logs, `reason=otp_timeout` |
| 5 | Contoso status page: "degraded API latency in eu-1" from {{T5.3h}} | probable | provider status page, read by Sam Roe |
| 6 | 4 OOM kills of `sms-otp` on prod-pay-01 (3.2 GB) while the queue held 11,800 bodies | proven | `journalctl -k`, cgroup memory events |

Ruled out: the network path to the provider (TLS handshake 40 ms, no packet loss from `prod-pay-01`), the 10-22
deploy of `payment-gateway` (rolled back on staging, same behaviour only with a slow provider), DNS.

## Timeline (UTC)

- {{T5.3h}} Contoso SMS reports degraded latency.
- {{T5.2h}} `sms-otp` p99 provider latency jumps from 0.4 s to 31 s; workers start to pile up.
- {{T5.1h}} Kuma: `OTP delivery (synthetic)` down. QA reports "no OTP code" in #qa.
- {{T4.9h}} Alertmanager `SmsOtpQueueBacklog` (critical) on `prod-pay-01`; Argus opens incident and pages.
- {{T4.4h}} First OOM kill of `sms-otp` on `prod-pay-01`; systemd restarts it, the queue refills.
- {{T2.5h}} Provider switched to Fabrikam by feature flag; `sms-otp` restarted on both hosts.
- {{T2.3h}} Queue empty, OTP median 4 s. Payment failures back to the baseline (0.3 %).

## Root cause

`sms-otp` treats the provider as always fast. With no request timeout and a fixed worker pool, one slow dependency
blocks the whole service; there is no circuit breaker and the failover to the secondary provider exists only as a
manual feature flag. The queue keeps full message bodies in memory, so a long backlog also drives the process into
OOM kills, which drop in-flight messages.

## How to fix it

### Option 1: Provider timeout and automatic failover (recommended)
- **Kind:** permanent fix
- **What:** set `SMS_PROVIDER_TIMEOUT=3s` and `SMS_FAILOVER=fabrikam` in `/etc/northwind/sms-otp.env` on `prod-pay-01` and `prod-pay-02`, deploy `payments/sms-otp` 2.14.0 (circuit breaker, already on staging)
- **Why it works:** a slow provider costs at most 3 s per message and trips the breaker after 20 failures; traffic moves to Fabrikam within a minute
- **Effort:** 1 h
- **Risk:** low: the release has run on staging for 6 days
- **Blast radius:** sms-otp on both payment hosts; OTP for payment and login for about 1 minute during the rolling restart
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 2: Keep Fabrikam as primary until Contoso confirms the fix
- **Kind:** mitigation
- **What:** leave the feature flag `sms.provider=fabrikam`; add a calendar reminder to switch back
- **Why it works:** the healthy provider carries the traffic; nothing else changes
- **Effort:** 5 min
- **Risk:** medium: Fabrikam costs 40 % more per message and has a 200 msg/s cap
- **Blast radius:** none now; a Fabrikam outage would repeat the incident
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 3: Alert on provider latency and worker saturation
- **Kind:** prevention
- **What:** alert rules `SmsProviderSlow` (p99 > 2 s for 3 min) and `SmsOtpWorkersSaturated` (> 90 % for 2 min) in `monitoring/rules/payments.yml`
- **Why it works:** pages 5 minutes into a provider slowdown instead of after the backlog
- **Effort:** 30 min
- **Risk:** low
- **Blast radius:** monitoring only
- **Approval:** operator OK

## Prompt for a write-access agent

~~~~markdown
PRODUCTION CHANGE — needs the operator's explicit approval before any step.
# sms-otp: provider timeout and automatic failover on prod-pay-01 / prod-pay-02
## Goal
`sms-otp` gives up on a slow SMS provider after 3 s and fails over to Fabrikam Messaging automatically; OTP delivery
survives a provider slowdown.
## Environment and targets
- Environment: production
- Hosts: prod-pay-01 (10.20.30.41), prod-pay-02 (10.20.30.42)
- Unit: `northwind-sms-otp.service`, env file `/etc/northwind/sms-otp.env`, port 8091
- Repository: `payments/sms-otp`, tag `2.14.0` (circuit breaker), CI deploy job `deploy_production` (manual)
## Background
- Root cause (proven): no provider timeout; 64 workers blocked on Contoso SMS for 2 h 40 min; 2,140 payments failed.
- Evidence: `sms_otp_workers_busy` 64/64; Contoso p99 31.2 s; `SMS_PROVIDER_TIMEOUT` unset.
- Timeline (UTC): {{T5.2h}} slowdown, {{T2.5h}} manual switch to Fabrikam, {{T2.3h}} recovered.
## Constraints and guardrails
- Production is read-only unless the operator approved this exact change: show each command, its backup and its
  rollback first, then wait.
- Never write to a production database. Never run a production deploy pipeline yourself; the operator plays the job.
- One host at a time; prod-pay-02 first.
## Pre-checks (run first; stop on any failure)
1. `curl -fsS http://10.20.30.42:8091/health` → `{"status":"ok"}`.
2. `grep -c . /etc/northwind/sms-otp.env` on both hosts → the file exists.
## Change, step by step
### Step 1: env file on prod-pay-02
- Backup: `cp -a /etc/northwind/sms-otp.env /etc/northwind/sms-otp.env.bak-$(date +%Y%m%d%H%M)`
- Change: append `SMS_PROVIDER_TIMEOUT=3s` and `SMS_FAILOVER=fabrikam`
- Check: `systemctl restart northwind-sms-otp && sleep 5 && curl -fsS localhost:8091/health`
- Rollback: restore the backup and restart the unit.
### Step 2: the same on prod-pay-01, then ask the operator to play `deploy_production` for 2.14.0
## Verify
- `sms_otp_provider_latency_seconds` p99 < 3 s; a test OTP to the QA phone arrives in < 10 s.
- `SmsOtpQueueBacklog` resolved; payment failure rate at the baseline.
## Prevention follow-up
- Alert rules `SmsProviderSlow` and `SmsOtpWorkersSaturated` (Option 3).
## Do not touch
- payment-gateway, fraud-check, the database.
## Report back
- What you changed (diffs), the backup paths, the verification output, anything still open.
~~~~
