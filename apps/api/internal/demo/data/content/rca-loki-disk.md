# Storage analysis: /var/lib/loki on prod-mon-01 full in 4.6 days

## Lazy reading
- **What broke:** `/var/lib/loki` on `prod-mon-01` is 93 % used and grows 6.8 GB a day.
- **Why:** Debug logging left on in `checkout` since the 10-18 release tripled the log volume (proven).
- **Impact:** Loki stops ingesting in about 4.6 days; no logs for RCAs or alerts after that.
- **Fix:** Set `LOG_LEVEL=info` for checkout and lower Loki retention to 21 days (Option 1) — the operator approves.
- **Status:** open

## Who uses the space

| path | size | growth / day |
|---|---|---|
| `/var/lib/loki/chunks` | 412 GB | +6.5 GB |
| `/var/lib/loki/index` | 31 GB | +0.3 GB |
| `/var/lib/loki/wal` | 2.1 GB | flat |

The `{app="checkout"}` stream is 61 % of the ingested bytes since 10-18 (it was 14 % before). Its lines are
`level=debug` request dumps.

## How to fix it

### Option 1: Turn debug logging off and shorten retention (recommended)
- **Kind:** mitigation
- **What:** `LOG_LEVEL=info` in the `checkout` env on prod-api-01/02/03; Loki `retention_period: 504h` (was 720h)
- **Why it works:** cuts the growth to about 2 GB a day and frees 120 GB at the next compaction
- **Effort:** 30 min
- **Risk:** low
- **Blast radius:** checkout restarts one host at a time; older logs beyond 21 days are deleted
- **Approval:** PRODUCTION CHANGE: the operator approves

### Option 2: Grow the disk by 500 GB
- **Kind:** permanent fix
- **What:** extend the volume `vdb` on the hypervisor and `resize2fs`
- **Why it works:** months of headroom at today's rate
- **Effort:** 1 h
- **Risk:** medium
- **Blast radius:** prod-mon-01 (Loki pauses during the resize)
- **Approval:** PRODUCTION CHANGE: the operator approves

## Prompt for a write-access agent

~~~~markdown
PRODUCTION CHANGE — needs the operator's explicit approval before any step.
# Reduce Loki growth on prod-mon-01
## Goal
`/var/lib/loki` grows under 2.5 GB a day and stays under 85 % used.
## Change, step by step
### Step 1: checkout log level (prod-api-01, -02, -03, one at a time)
- Backup: copy `/etc/northwind/checkout.env` with a timestamp suffix
- Change: `LOG_LEVEL=info`; restart `northwind-checkout`
- Check: `/health` 200; Loki `{app="checkout"} |= "level=debug"` returns nothing new
- Rollback: restore the env file, restart
~~~~
