---
title: Notifications
summary: What Argus sends to Matrix and when - pages, the 09:00 and 17:00 digest, silent notices, and the lines of the morning brief.
order: 8
---

Argus posts to **one** Matrix room, the Argus notify room. It reads other rooms ({{qa_room}}, Kuma, Alertmanager)
but never posts there, with one exception: the threaded follow-up questions in {{qa_room}}
([Conversations](/help/operations#qa-questions)).

## Three kinds of message {#kinds}

| Kind | When | How it arrives |
|---|---|---|
| **Page** | Production is down for customers, a backup set failed, a certificate expires within 7 days, a CI/CD failure blocks a release deploy, KEV on an internet-facing production target. | At once, as a message Element notifies for. |
| **Silent notice** | Things to know soon but not urgent: other release CI/CD findings, stuck pipelines, deploy regressions, new P1 vulnerabilities. | At once, as a notice (no sound by default). |
| **Digest** | Everything else. | Collected into one message at 09:00 and 17:00 {{tz}} time. |

Rules that always apply:

- Severity comes from the recorded data, not from the model's wording.
- Between 23:00 and 08:00 only a production page notifies.
- The same problem within 12 hours updates the same thread or card; it is not posted again.
- An incident that pages gets one Matrix thread; later updates, acknowledge and resolve go into it.
- Argus's own failures never page as production failures. Argus pages about itself only after its self-check has
  failed for more than an hour, once, and once more when it recovers.
- Messages never mention `@room` or anyone, carry no secrets, and link only to Argus pages or a merge request.

## The morning brief {#brief}

The 09:00 digest starts with the brief. Each line links to its page.

| Line | Says |
|---|---|
| Incidents | Open incidents by environment, new since the last brief, the 7-day time to resolve, the top open ones. |
| Certs | Names with fewer than 30 days left, one line per certificate, pinned ones first. |
| Backups | Failing backup sets. |
| Disks | Filesystems that fill within 14 days. |
| CI/CD | Failing pipelines by environment, stuck pipelines, deploys in 24 hours. |
| CI/CD green | Apps whose newest pipeline passed, per environment (the same counts as the CI/CD page). |
| CVE | Open vulnerabilities by priority, how many are fixable today, KEV and end-of-life counts. |
| Noisy | The most frequent alerts of the last 24 hours. |
| Hygiene | Saturdays only: stale, test, dead, flapping counts. |
| Readiness | Only in the weeks around the sales event: release deploys and the freeze reminder, capacity headroom of the key hosts, edge 429s, landing pages, open critical incidents. |
| Proposed | "N tasks waiting for your OK". |
| Ideas | "N new ideas", only when new ones arrived. |
| Argus | Its own health: runs, errors, denials and cost in 24 hours, stale jobs. |

The 17:00 digest carries the incident line and the Argus line only.

## What does not reach Matrix {#not-sent}

- Each proposed task, each new idea, or each QA message: they are counted in the brief instead.
- OK results of scheduled checks are one short line, or part of the digest.
- Viewer actions such as Scan now or Refresh.

:::admin
## Settings for admins {#admin}

`MATRIX_NOTIFY_ENABLED` turns all sending on or off. `NOTIFY_STYLE=C` gives the page-now, digest-later behaviour
above; `NOTIFY_DIGEST_HOURS` (default 9,17), `NOTIFY_QUIET` (default 23-8) and the grouping window `NOTIFY_GROUP_HOURS`
(12) tune it. The certificate page threshold is `CERT_PAGE_DAYS` (7); CVE paging and digest thresholds are
`CVE_PAGE_MIN_PRIORITY` and `CVE_DIGEST_MIN_PRIORITY`. Every settings change is also posted to the room.
:::

## FAQ {#faq}

:::details I did not get a ping for an incident.
Only production incidents on customer-facing names page. Others are in the digest, and an acknowledged incident's
updates stay in its thread.
:::

:::details Why did the digest not arrive?
Sending may be switched off by the operator, or the bot account may not be signed in. Argus health on Infrastructure →
Checks shows the Matrix parts.
:::
