---
title: Security
summary: Vulnerabilities - which known vulnerabilities affect the estate, how bad they are here, who is affected and how to fix them.
order: 5
---

## Vulnerabilities {#vulnerabilities}

Which known vulnerabilities (CVEs) affect our servers, containers and dependencies, how bad they are **here**,
which services they touch, and how to fix them. Read-only: Argus reports and proposes; it never installs anything.
**Where:** Delivery → [Vulnerabilities](/vulns). The scan runs daily at 05:10, before the morning brief.

### Where the data comes from {#vulns-sources}

- **What we run:** OS packages, the kernel and runtimes of each host (read-only), version labels from Prometheus,
  Dockerfiles and lockfiles in GitLab, and the container images that run in both data centres. Images are read from
  the registry and unpacked as files; they are never run.
- **What is known to be vulnerable:** OSV (Ubuntu, Debian, npm and others), NVD for vendor builds, CISA's
  Known Exploited Vulnerabilities list (KEV), FIRST's exploit prediction score (EPSS), and endoflife.date for
  versions that no longer get fixes (EOL).
- Ubuntu and Debian packages are compared with the distribution's own fixed versions, so a back-ported fix is not a
  false alarm.

### Priority P1 to P4 {#vulns-priority}

Each finding gets a score from 0 to 15: the CVSS base (or the distribution's priority), plus points for being in
KEV, a high EPSS, an internet-facing target, production and a tier-1 host, minus points for a development-only
dependency or when no fix exists yet.

| Priority | Means |
|---|---|
| P1 | Score 12 or more, or in KEV on production. Fix first. |
| P2 | Score 9 or more. |
| P3 | Score 6 or more. |
| P4 | Everything else. |

Every finding shows **why** it got its score, and an SLA due date counted from when it was first seen.

### What you see {#vulns-view}

- **By component:** one row per vulnerable component, with its worst priority, KEV count, versions and
  "Affects: N services on M hosts" (expand to see them). Filter by priority, environment class, kind, host and service.
- **Services most at risk:** the services with the worst open findings, and their exposure (public edge,
  customer-facing domain, or internal).
- **A component page** (`/vulns?group=…`): the Summary, the vulnerabilities, the targets, who is affected, and
  **How to fix it** with a prompt for a write-access agent (the exact package upgrades, kernel and reboot window,
  dependency bump, image tag or OS upgrade, with the restarts and a check per service).
- **Coverage and feeds:** which hosts have a package list, the containers found, **blind spots** (what Argus could
  not see, such as an image it could not analyse or a host without a package list), and the status of each feed
  query.
- On a **VM page**: its findings by service and its containers.

### Alerts and tasks {#vulns-alerts}

- A new P1 on production that can be fixed today (or is in KEV) opens an incident with source Security, at most a
  few a day. It resolves when the scan no longer finds it, never on a timer.
- Matrix pings only for KEV on an internet-facing production target. Other new P1s are a silent notice; the rest
  is the brief's CVE line.
- Fixable P1 and P2 groups on production, and every KEV hit, are proposed as tasks on
  [Proposed tasks](/help/delivery#proposed-tasks). Argus never creates the Jira issue itself.

### How do I… {#vulns-how}

- **Decide what to fix first:** sort by priority and start with "Fix first"; read the why line.
- **Hand it to someone:** copy or download the prompt from the component page, or accept the proposed task.
- **Accept a risk** (admins): **Suppress** on the component page, with a reason of at least 20 characters and an
  expiry of at most 180 days. You may limit it to one vulnerability, a target pattern or an environment class. It
  applies at the next scan and reopens when it expires.
- **Rescan:** **Scan now** at the top right.

### What it will not do {#vulns-not}

- Never installs, upgrades or restarts anything.
- Never runs a container image.
- A suppression without a reason or an expiry is refused.

:::admin
### Settings for admins {#vulns-admin}

- Switch: `FEATURE_CVE=1` (default 0). Sources: `CVE_INVENTORY_SOURCES`; feeds: `CVE_FEEDS`; mode online, mirror or
  offline (`CVE_FEEDS_MODE`, offline bundles for an air-gapped server).
- Priority weights, products and environment classes live in `config/cve.json`; SLA days in `CVE_SLA_DAYS`
  (P1 2, P2 7, P3 30, P4 90).
- Paging and digest thresholds: `CVE_PAGE_MIN_PRIORITY`, `CVE_DIGEST_MIN_PRIORITY`; new incidents per day:
  `CVE_MAX_NEW_INCIDENTS_DAY`; suppression limit: `CVE_SUPPRESSION_MAX_DAYS`.
- The only secret is an optional NVD API key, readable by root only.
:::

## FAQ {#faq}

:::details Why is a vulnerability on a staging host P4 when the same one is P2 on production?
The environment is part of the score: production adds points and development removes them.
:::

:::details A container I know runs is not listed.
Look at Coverage → blind spots. Some data centres do not export container labels, so Argus cannot see their images;
a declared container that was not observed is listed there too.
:::

:::details Can I suppress a finding forever?
No. Every suppression needs an expiry of at most 180 days, so it is reviewed again.
:::
