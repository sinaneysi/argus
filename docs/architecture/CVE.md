# Vulnerability (CVE) section

Status: proposal, 2026-10-22. Decision record: [ADR-0004](adr/0004-cve-data-sources.md). Fits
CONFIG.md §3.10 (keys), ADAPTERS.md §10 (feeds), ACCESS.md §3/§8.2 (mitigation).

**Contract for the agent building this section now.** Whatever is built must keep these boundaries:
(1) collectors and matching in `bin/` (Python, stdlib, no model in the pipeline); (2) data in ops.db
tables named below (additive, created on first use after an online backup copy); (3) every
company-specific value from config (`CVE_*`, `environments.json`, `SERVICE_TIERS_FILE`,
`CUSTOMER_FACING_FILE`, `SERVICE_MAP_FILE`); (4) feeds fetched by one root job with an egress allowlist,
matching offline from the cache; (5) alerts only through `incidents.py` + `notify.sh`; (6) no write on
any host — mitigation is an action *request* (ACCESS.md), never executed by the CVE code; (7) app pages
in **both** `app/` and `apps/api/` with a parity case, or Go-only after the cutover (ADR-0005).

## 1. Pipeline

```
 inventory collectors (argus, read-only)          feed fetcher (root, egress allowlist)
  os/kernel ── ssh-ro pkgs/kernel/osrel ──┐           osv · debian · ubuntu · nvd · kev · epss · eol
  images ──── ssh-ro docker / registry ───┤                │ ETag cache /var/lib/argus/cve/feeds
  dockerfiles ─ gitlab-ro file (engine)  ─┤                ▼
  lockfiles ─── gitlab-ro file @release ──┼─▶ sw_components ──▶ matcher (argus, no network) ──▶ cve_findings
  runtimes ──── ssh-ro unitinfo/node -v ──┤                                  │
  services ──── Prometheus *_version_info ┤                       scorer (exposure, env, tier, KEV, EPSS)
  gitlab ────── /api/v4/version ──────────┘                                  │
                                                         suppressions ─▶ dedupe ─▶ incidents / digest / Jira
                                                                                     │
                                                         "how to fix" ─▶ action request (package.upgrade…)
```

## 2. Inventory

All read-only, through the existing tools; new ssh-ro catalogue entries go through review like any
`scripts/ssh-ro.sh` change.

| source | how | component record |
|---|---|---|
| OS + packages | new ssh-ro `pkgs` (`dpkg-query -W -f '${Package}\t${Version}\t${source:Package}\t${source:Version}\n'` or `rpm -qa --qf`), `osrel` (`/etc/os-release`) | `pkg:deb/debian/<src>@<ver>?distro=debian-12` (purl, **source package** for Debian/Ubuntu matching) |
| kernel | ssh-ro `kernel` (`uname -r`, installed `linux-image-*`) | running and installed separately: a fixed kernel installed but not booted is still exposed |
| container images | where: cAdvisor `image`/`name` labels in each DC's Prometheus (production, no root, no login), ssh-ro `docker` (develop), `containers_declared` (config, shown as declared); what: the root analyser `bin/cve_images.py` reads each image from the registry (GET only, egress allowlist, layers streamed and digest-checked, never run, cached per digest) | OS packages `pkg:deb`/`pkg:apk` (OSV `Debian:<major>`, `Alpine:v<minor>`), runtimes from the image env (`php` → `cpe:2.3:a:php:php` + endoflife.date), apps (`matomo` CPE), `pkg:composer` (OSV Packagist), `pkg:npm`; affects = container → host (+ service) |
| web fingerprint | config `web_fingerprints`: a public URL's `X-Powered-By` on the backends the service map routes it to | the live runtime version (e.g. `php 8.4.20`) on those hosts; a differing analysed image is a blind spot `image-drift` |
| base images | `gitlab-ro.sh file <CICD_ENGINE_PROJECT> <Dockerfile>` and `FROM` lines of app repos | `pkg:docker/<name>@<tag>` |
| nginx | ssh-ro `nginx -v` on hosts with role `edge` | `cpe:2.3:a:f5:nginx:<v>` |
| GitLab | `GET /api/v4/version` (Reporter) | `cpe:2.3:a:gitlab:gitlab:<v>` (edition from response) |
| MongoDB / Redis / MariaDB / RabbitMQ | Prometheus `mongodb_version_info`, `redis_instance_info{redis_version}`, `mysql_version_info`, `rabbitmq_build_info` | CPE |
| Node runtimes | ssh-ro `unitinfo` ExecStart path + new `runtime` (`<path> --version` for whitelisted interpreter paths) | `pkg:generic/node@<v>` + `eol` |
| app dependencies | `package-lock.json` / `yarn.lock` / `go.sum` / `requirements*.txt` at `CVE_LOCKFILE_REFS` of each project in `CICD_GROUPS` | `pkg:npm/<name>@<v>`; `dev` dependencies flagged (lower score) |

Table `sw_components(id, target_kind host|image|project|service, target, env, ecosystem, name,
version, purl, cpe, source, is_dev, first_seen, last_seen)`, upserted per collection; a component not
seen for 3 runs is closed (its findings resolve).

Budgets: one ssh-ro call per host per day for `pkgs`; GitLab calls inside the shared hourly cap
(`CICD_MAX_PER_HOUR`); lockfiles only for projects whose ref changed (commit SHA cached).

## 3. Feeds, cache and offline mode

| feed | refresh | size | for |
|---|---|---|---|
| OSV `all.zip` per ecosystem in use (Debian, Ubuntu, npm, PyPI, Go, Alpine) | daily, `If-Modified-Since` | 10–200 MB | primary advisory source, ranges with `fixed` versions |
| Debian security tracker JSON | daily | ~60 MB | release-specific fixed versions, `no-dsa`/`ignored` states |
| Ubuntu (OSV Ubuntu) | daily | | same for Ubuntu |
| NVD 2.0 | incremental by `lastModStartDate`, 6 h | | CPE matching for non-package software; CVSS v3.1/v4 |
| CISA KEV | daily | < 2 MB | `known_exploited`, `due_date` |
| FIRST EPSS | daily | ~10 MB | exploit probability + percentile |
| endoflife.date | weekly | small | OS, kernel, Node, nginx, MongoDB, Redis EOL |

- **Fetcher** `argus-cve-feeds` (root, `argus-cve-feeds.timer`): runs with an egress allowlist
  (`CVE_FEEDS_EGRESS_HOSTS` → systemd `IPAddressAllow` resolved at render time, or the proxy), verifies
  checksums/sizes, writes atomically to `/var/lib/argus/cve/feeds/<feed>/<date>/`, keeps 3 versions,
  publishes `feeds-status.json` (age per feed) for the self-check. Every answer is read whole (Content-Length checked,
  short reads retried with backoff 10/30/60 s) and validated (JSON parses) before it replaces the cached file; on failure
  the previous good file is kept (status `stale`). `feeds-status.json` carries per-query status (ok/cached/stale/failed)
  and per NVD product (ok/partial/failed, results of totalResults, pages of 500); the matcher turns failed queries and
  unparsable cache files into `feed-failed` / `feed-unparsable` blind spots and /vulns shows "feed queries failed: N".
- **Modes:** `online` (direct), `mirror` (`CVE_FEEDS_MIRROR_URL`, e.g. a argus-01 raw proxy — relevant for
  egress-restricted estates), `offline` (operator drops a signed tarball into `CVE_OFFLINE_BUNDLE_DIR`;
  `bin/cve_bundle.py make` builds it on a connected machine).
- **Matching never goes online.** A stale feed (> 3 days) is an Argus-internal WARN, never a production
  page, and findings show "feed data from <date>".

## 4. Matching

- **Distro packages:** match by source package and distro release against the distro tracker's fixed
  version, compared with dpkg/rpm version semantics (implement `dpkg --compare-versions` rules in
  Python; test vectors from dpkg). Upstream version ranges are **not** used for distro packages
  (backports would be false positives).
- **Language packages:** OSV ranges (`SEMVER`, `ECOSYSTEM`) on the lockfile version.
- **CPE software:** NVD configurations; confidence `medium`; vendor advisory wins when present.
- **Kernel:** distro tracker on the running version; plus "reboot pending" when installed > running.
- **EOL:** a component past EOL is one finding `EOL-<product>-<cycle>` (no CVE needed).
- Aliases (CVE, GHSA, DSA, USN, OSV ids) are merged into one `vuln_key` = the CVE id when present.

## 5. Risk score and priority

```
base      = CVSS v4 base, else v3.1, else 5.0 (unknown)                 0..10
exploit   = +3 if KEV · +2 if EPSS ≥ CVE_EPSS_HIGH · +1 if EPSS percentile ≥ 0.9
exposure  = +2 if target serves a CUSTOMER_FACING_FILE name (service map) or is an edge
            +1 if reachable from another environment's network (data platform hubs)
env       = production +2 · staging 0 · dev −2      (class from environments.json)
tier      = +1 if the service is tier 1 in SERVICE_TIERS_FILE
relevance = −3 dev dependency · −2 package installed but no running process uses it (kernel: running only)
fix       = −1 if no fixed version exists yet (still shown, "no fix yet")
score     = clamp(base + exploit + exposure + env + tier + relevance + fix, 0, 15)
priority  = P1 ≥ 12 or (KEV and production) · P2 ≥ 9 · P3 ≥ 6 · P4 otherwise
```

Weights are config (`CVE_WEIGHTS` map) with these defaults; every finding stores its score breakdown so
the page can say *why* it is P1. SLA = `CVE_SLA_DAYS[priority]` from first seen.

## 6. Findings, dedupe, suppression

- `cve_findings(id, vuln_key, aliases, component_name, ecosystem, fixed_version, targets JSON,
  env_max_class, priority, score, breakdown JSON, kev, epss, first_seen, last_seen, state
  open|suppressed|resolved, sla_due, incident_id, task_key)`.
- **Dedupe key:** `vuln_key + ecosystem + component name (no version) + env class`. One finding lists
  every target; a new target appends, it does not create a new finding or a new notification.
- **Suppression** (`cve_suppressions`): match on `vuln_key` and/or component glob and/or target glob
  and/or env; **reason required** (≥ 20 chars), **expiry required** (≤ `CVE_SUPPRESSION_MAX_DAYS`),
  owner, created_by; admins only, audited. Expiry reopens the finding and posts one digest line.
  Later: import OpenVEX statements (`not_affected` with justification) as suppressions.
- **Resolve:** the component version no longer matches (upgraded), the component disappeared, or the
  advisory was withdrawn.

## 7. Alerting and hand-off

- New or escalated **P1** on class production → incident through `incidents.py` with key
  `CVE|<component>|<vuln_key>` (family `cve`), severity `page` only if `CVE_PAGE_MIN_PRIORITY` allows
  and the target is customer-facing (same paging rule as A4); capped by `CVE_MAX_NEW_INCIDENTS_DAY`.
- P2/P3 → the daily brief (one line per component, "+N more"); P4 → page only.
- "Create task" on a finding → the tracker (existing board flow), prefilled with targets and fix.
- **How to fix** (deterministic, no model): per ecosystem a template —
  Debian/Ubuntu `apt-get install --only-upgrade <pkg>=<fixed>` + services that use the library
  (`needrestart -b` output from ssh-ro, later) + reboot if kernel; npm: the lockfile bump and an MR
  proposal on the service repo's develop branch; base image: the new tag in the Dockerfile; EOL: the
  supported cycle. The model is used only optionally to explain impact in plain language (tool-less,
  `$0.05` cap), never to decide the fix.

## 8. Mitigation (later, through ACCESS.md)

- The fix becomes an action request `package.upgrade` with `params.packages` = exactly the finding's
  `name=fixed_version` list and `evidence.finding_id`; the broker's validator refuses any version that
  is not the finding's fixed version, any kernel package unless `allow_kernel`, and any package not
  from the host's configured repositories (`apt-cache policy` in the dry run).
- Policy (ACCESS.md §8.2): develop automatic, pre-release one approver, release one approver from
  `prod-approvers` + maintenance window + canary + auto rollback; kernel reboots two-person.
- After execution the next inventory run on that host is triggered and the finding resolves on evidence,
  not on the executor's word.

## 9. UI (both apps)

`/vulns`: summary by priority and environment, "needs attention" (P1/P2 past 50 % of SLA), filters
(env, priority, ecosystem, KEV, has fix, suppressed), finding page (targets, breakdown, advisories,
fix, suppress form for admins, Create task, Request mitigation when `FEATURE_ACTIONS` allows it).
Coverage panel: hosts with package lists, hosts with container images (observed vs declared), images analysed, and an explicit **Blind spots** list (`cve_blind_spots`: no package list and no container list, cAdvisor without image labels or without a VM, images not analysed and why, DB hosts skipped by rule, declared images, running image older than the analysed one). The VM card lists its containers. Overview tile and brief line. No JavaScript, same DESIGN.md.

## 10. Tests

Fixtures for dpkg and apk version compare, image layers (dpkg status, apk db, composer.lock, whiteouts), cAdvisor parsing, OSV range evaluation, Debian tracker states, scoring breakdown,
dedupe/append, suppression expiry, offline bundle verification, no-network matcher (socket blocked in
the test), incident key stability.
