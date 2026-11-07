# RCA: www.northwind.example 502 on a quarter of requests — one shop-front instance not listening

## Lazy reading
- **What broke:** About 25 % of requests to `www.northwind.example` answer 502 since {{T7h}} UTC.
- **Why:** The second shop-front instance on `prod-shop-02` (:3001) did not start after the 10-23 deploy (proven).
- **Impact:** 502 on roughly one page view in four for customers routed to prod-shop-02; checkout unaffected.
- **Fix:** Start `northwind-shop-front@3001` with the fixed env file (Option 1) — the operator approves (production).
- **Status:** needs operator

## Summary

The nginx upstream `shop_front` on `prod-edge-01` and `prod-edge-02` balances over four backends:
`prod-shop-01:3000`, `prod-shop-01:3001`, `prod-shop-02:3000`, `prod-shop-02:3001`. Since the deploy at {{T7h}}
UTC, `prod-shop-02:3001` refuses connections: `northwind-shop-front@3001` exited with
`Error: NEXT_PUBLIC_API_BASE is not set` and systemd gave up after 5 restarts. nginx marks the backend down for
10 s after each failure and then retries it, so roughly every fourth request still lands on the dead port.

## Evidence

| # | finding | proven/probable | source |
|---|---|---|---|
| 1 | `connect() failed (111: Connection refused) while connecting to upstream 10.20.30.22:3001` 1,920 times/h | proven | nginx error log on prod-edge-01 |
| 2 | `northwind-shop-front@3001` failed, `start-limit-hit` | proven | `systemctl status` on prod-shop-02 |
| 3 | `/etc/northwind/shop-front-3001.env` lacks `NEXT_PUBLIC_API_BASE` (the 3000 file has it) | proven | `diff` of the two env files |
| 4 | The deploy template writes the env file per instance from the CI variable `SHOP_FRONT_ENV_3001` (empty) | probable | `web/shop-front` `.gitlab-ci.yml` |

## How to fix it

### Option 1: Restore the env file and start the instance (recommended)
- **Kind:** mitigation
- **What:** copy `NEXT_PUBLIC_API_BASE` from `shop-front-3000.env` into `shop-front-3001.env` on `prod-shop-02`, `systemctl reset-failed` and start `northwind-shop-front@3001`
- **Why it works:** the instance starts and listens on :3001 again; nginx stops getting refused connections
- **Effort:** 10 min
- **Risk:** low
- **Blast radius:** shop-front on prod-shop-02 only
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 2: Fill the CI variable and redeploy
- **Kind:** permanent fix
- **What:** set the CI variable `SHOP_FRONT_ENV_3001` (environment scope `production`) like `SHOP_FRONT_ENV_3000`; the next release writes the file correctly
- **Why it works:** the deploy no longer produces an incomplete env file
- **Effort:** 20 min
- **Risk:** low
- **Blast radius:** the next shop-front release
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 3: Health-gate the deploy
- **Kind:** prevention
- **What:** in `web/shop-front` CI, fail the deploy when any instance's `/health` is not 200 within 60 s
- **Why it works:** a broken instance stops the rollout instead of serving 502
- **Effort:** 1 h
- **Risk:** low
- **Blast radius:** the CI pipeline
- **Approval:** operator OK

## Prompt for a write-access agent

~~~~markdown
PRODUCTION CHANGE — needs the operator's explicit approval before any step.
# Restore shop-front instance :3001 on prod-shop-02
## Goal
`curl -fsS http://10.20.30.22:3001/health` answers 200 and nginx logs no more refused connections to it.
## Environment and targets
- Environment: production; host prod-shop-02 (10.20.30.22); unit `northwind-shop-front@3001.service`
- Files: `/etc/northwind/shop-front-3000.env` (good), `/etc/northwind/shop-front-3001.env` (missing a key)
## Change, step by step
### Step 1
- Backup: `cp -a /etc/northwind/shop-front-3001.env{,.bak-$(date +%s)}`
- Change: add the `NEXT_PUBLIC_API_BASE=` line from the 3000 file
- Check: `systemctl reset-failed northwind-shop-front@3001 && systemctl start northwind-shop-front@3001`
- Rollback: restore the backup, stop the unit.
## Verify
- `/health` 200 on :3001; `grep -c 10.20.30.22:3001 /var/log/nginx/error.log` stops growing on prod-edge-01.
~~~~
