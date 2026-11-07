---
title: Estate and hygiene
summary: Inventory and the VM page, Capacity, Backups with New backup and Backup targets, and Certificates.
order: 4
---

The servers themselves: what runs where (Infrastructure → Inventory), how full it is, whether it is backed up
and whether its certificates are valid (the other Infrastructure pages).

## Inventory {#inventory}

Every VM Argus knows, from Prometheus and read-only host facts. No model is involved.
**Where:** Infrastructure → [Inventory](/inventory), or the **Find a VM** box in the sidebar.

### The list {#inventory-list}

- Narrow with the chip rows, each chip with its count: **Show** (needs a look, critical now, services not ready, dead
  upstreams, production app hosts; the figures above open the same lists), **Environment**, **Datacenter** and
  **Flags**. Sort with the links on the result line, or search with **Find a VM** (name, IP, environment, role,
  service, domain or mount). 50 VMs per page.
- **CSV** downloads the current list.
- The vulnerability column links to the VM's findings.

### The VM page {#inventory-vm}

- **Resource strip:** CPU, memory, disk, load and IO with a state badge and the reason, plus "Services ready N of M".
  Under it, **On this page** jumps to each section, and the VM's **open incidents** come first when there are any.
- **Pressure:** now, 24-hour maximum, small charts of the last 24 hours and 7 days, with the threshold drawn in.
- **Services:** the units on the VM, ready or not, restarts, and who uses the CPU and memory (from a read-only
  snapshot).
- **Upstreams and dead upstreams:** the nginx routes that point at this VM, and any port they route to that nothing
  listens on.
- **Filesystems:** used, growth per day and days to full.
- **Vulnerabilities and containers** on this VM, and its **change history** (folded).
- **Identity and Access** in the side rail.

### How do I… {#inventory-how}

- **Get fresh facts:** **Refresh** (anyone). The VM is collected again in about a minute; the page reloads until
  it is done.
- **Ask why a VM is under pressure:** **Analyse** (admins). A read-only analysis of up to 15 minutes: what uses
  the resource, whether that is needed or a leak, which services are not ready, and the fix options. It appears under
  **Analysis**, with Create task.

### What it will not do {#inventory-not}

- Analyse is refused on database hosts: Argus never logs in to them.
- A VM without a read-only access alias shows Prometheus data only.
- On production, every option in an analysis is a proposal for the operator.

## Capacity {#capacity}

Which disk, CPU or memory runs out next, for every VM in both data centres. Collected daily at 08:30, before the
morning brief. **Where:** Infrastructure → [Capacity](/capacity).

### What you see {#capacity-view}

- **Figures by level**, each a filter: Critical (full in under 7 days, or p95 at 97 % or more), High (under 14
  days, or p95 at 90 %), Watch (under 30 days), Under 10 % free (tight but not growing), OK, and Not monitored (no
  node_exporter data).
- **Needs attention:** filesystems at risk, each with **Analyse**, then hot VMs (CPU or memory p95 over 7 days).
- **All VMs:** worst first, filters for environment, data centre and level, sort by **Worst first** or
  **Fastest growing**. Each row shows its worst disk with **Growth / day** and **Full in**; "All N filesystems"
  opens every filesystem of that VM.

"Growth / day" is the average daily growth of used space over the last 7 days. "Full in" is free space divided by
that growth.

### Storage analysis {#capacity-analyse}

**Analyse** on an at-risk filesystem (admins) starts a read-only analysis of about 10 minutes, costing up to $1: what
fills the disk, what grows, and the options. The result appears in the Analysis column and can become a task.

## Backups {#backups}

Every backup set in object storage, checked daily at 07:00, plus the plans Argus helps you create.
**Where:** Infrastructure → [Backups](/backups).

### What you see {#backups-view}

- Failing sets stay open at the top; healthy ones fold into "Sets OK". A set's page shows its size history.
- **Backup plans** created with New backup, with **Restore tested** (when a restore was last proven).
- **Coverage:** backup families, sets without a known script, databases without a backup, unencrypted sets and
  high-risk defects, each with **Create task**.
- **Templates** links to the backup templates repository the plans are built from.

### Mark a failing set as OK (admins) {#backups-accept}

You checked a failing set and it is fine (a planned change, a size that is right, a retired source). Open the set
(**Mark as OK** next to it on Backups) and fill in:

- **Why** (at least 10 characters), shown to everyone next to the set and kept in the audit log.
- **For how long:** until the next passing check (default), for 1 to 30 days, or until the newest backup changes.
  Every choice ends after 30 days at most.

The set then shows **Accepted** with the real status underneath (for example `still FAIL(stale)`), who
accepted it, why, and until when. It is left out of the failing count on Checks and Backups, of Today, of the
morning brief, of the backup notification and the digest. The stored result is never changed.

It shows as failing again by itself when: the override ends; the set passed once and fails again (first scope); the
newest backup changed (third scope); or it fails **in another way** (say `FAIL(small)` after you accepted
`FAIL(stale)`). **Undo** next to the note removes it at once. Marking as OK needs a two-factor code from the last
10 minutes; Argus applies the request within a minute.

### New backup in four fields {#new-backup}

**Where:** Backups → [New backup](/backups/new). Anyone can open it and preview a plan; admins test and save.

1. **Connection string** (a password field): MongoDB, MySQL or MariaDB, PostgreSQL, Redis or SQL Server.
2. **Databases**: `all`, or names separated by spaces.
3. **Schedule**: every 30 minutes, hourly, twice a day, daily 03:00, or a cron expression. The next 5 runs are shown.
4. **Target**: a saved backup target.

Everything else is worked out for you and shown under **Advanced**: the engine, the host that runs the job (the
database host, found in the Inventory; for a replica set a secondary), the environment, the job name, encryption,
compression and retention.

Then:

- **Test connection** is required. It runs on the database host, checks sign-in and a ping only, and reports the
  version, the role and the exact GRANT if a privilege is missing. **Save** unlocks only when the test passed for
  that exact string.
- **Save** stores the plan, opens a merge request in the backup templates repository and, for production, a board
  task marked as a production change.
- **Install now** (admins, develop and pre-release) installs the job on the database host, takes **one backup
  now**, verifies it can be listed for restore, then starts the timer.
- **Production:** the plan page shows the operator's one install command instead. That bundle tests first and
  installs nothing if the test fails.
- **Record restore test** after a restore drill: result and date.

### What it will not do {#backups-not}

- Argus never connects to a production database and never installs on production.
- The connection string is never shown again, never put in the plan, the merge request, Jira, a log or a page
  (only `user:***@host`). It is kept on the server, readable by root only, until the job is installed, then
  deleted. To test again, paste it again.
- Fields that look like secrets in the wrong place are refused and not echoed.

:::admin
### Backup targets (admins) {#backup-targets}

Settings → [Backup targets](/admin/backup-targets): add each storage target once (name, MinIO or S3, endpoint,
bucket, prefix, keys, optional CA, default retention, environments). **Add and test** lists the prefix and writes and
deletes a test object; a target whose keys cannot delete is good (write-only). With MinIO admin keys each job gets
its own write-only user. The page shows the lifecycle rule to set for the retention. Keys go straight to root on the
server and are never shown again.

Operator prerequisites: the encryption public keys on the server, the first target, and a least-privilege backup
user per database (a failed test prints the exact GRANT).
:::

## Certificates {#certificates}

The TLS certificates of about 300 public names, checked daily at 07:30. **Where:** Infrastructure →
[Certificates](/certs).

- Names that serve the same certificate are one row: "wildcard, 18 days, used by 71 names".
- Days left decide the status: OK from 30 days, WARN under 30, FAIL under 14, and a page under 7 days or just
  expired. Certificates that expired more than 14 days ago are listed as "Expired long ago" (a stale vhost or a
  forgotten certificate).
- A name mismatch is WARN. A known, explained mismatch shows "Expected name mismatch" with the reason.
- Names that did not answer are listed as **Unreachable**, never as certificate failures.
- Pinned banners lead the page for certificates someone is tracking, until the served one is renewed.
- Healthy certificates, old expired ones and unreachable names are folded.

:::admin
### Settings for admins {#certificates-admin}

Settings → General → **Thresholds**: "Certificates: WARN below this many days" (default 30) and "FAIL below"
(default 14). The page threshold of 7 days is `CERT_PAGE_DAYS`. The name list is generated from the nginx
repository; run the generator again after vhost changes.
:::

## FAQ {#faq}

:::details A VM says "Not monitored". Is it down?
No. It means there is no node_exporter data for it in either data centre, so Argus cannot forecast it.
:::

:::details Why can I not test a backup on production?
Argus does not connect to production databases. The operator's install bundle tests the connection first on the
host itself.
:::

:::details Refresh on a VM page did nothing.
Refresh needs a read-only access alias for the VM. Without one, the page shows Prometheus data only.
:::
