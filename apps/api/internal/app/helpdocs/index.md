---
title: Help
summary: What Argus can do, where to find it and how to use it.
order: 0
---

Argus is the {{company}} DevOps assistant. It watches the estate, investigates problems and tells you what it
found and how to fix it. It reads; people decide. Start with [Getting started](/help/overview) if you are new.

## How do I… {#how-do-i}

| I want to… | Go to |
|---|---|
| Know what to do first today | [Today](/today), the day's plan, worst first |
| Make the decisions waiting for me | [Review](/review) · [Review help](/help/review) |
| See whether the daily checks are green | [Infrastructure → Checks](/operations) · [Checks help](/help/operations#checks) |
| Understand an incident and how to fix it | [Incidents](/incidents) · [Incidents help](/help/operations#incidents) |
| Ask Argus for a deeper investigation | [Deep RCA](/help/operations#deep-rca) (admins) |
| Let Argus fix something on develop or pre-release | [Take action](/help/operations#take-action) (admins) |
| Group several issues under one root cause | [Problems](/help/operations#problems) |
| See what happened to a QA report | [Conversations](/qa) · [Conversations help](/help/operations#qa-reports) |
| Find a server and see its health | [Find a VM](/help/overview#find-a-vm) · [Inventory](/help/infrastructure#inventory) |
| Know which disk fills up next | [Capacity](/help/infrastructure#capacity) |
| Add a database backup | [New backup](/help/infrastructure#backups) |
| Check if a repo can use the shared pipeline | [Check a repo](/help/delivery#check-a-repo) |
| Turn a Matrix message into a Jira task | [Matrix requests](/help/delivery#matrix-requests) |
| Approve or reject tasks Argus proposes | [Review inbox → Task proposals](/help/delivery#proposed-tasks) (admins) |
| See which vulnerabilities to fix first | [Vulnerabilities](/help/security#vulnerabilities) |
| See whether Google can crawl the shop | [SEO](/seo) · [SEO help](/help/marketing#seo) |
| Know what Argus may change | [Is it safe?](/help/safety) |
| Know what reaches Matrix and when | [Notifications](/help/notifications) |
| Look up a word | [Glossary](/help/glossary) |

## The three rules {#rules}

- **Production is read-only.** Argus never changes a release (production) host, database or deploy. It writes a
  proposal; a person runs it with the operator's approval for that one change.
- **Nothing reaches Jira without approval.** Argus proposes tasks; an admin accepts them.
- **Fixes on develop and pre-release are reviewed first.** An admin reads and edits the fix prompt before the fixer
  runs, and only in the environments the operator has allowed.

## About this help {#about}

These pages describe what Argus does today. A feature that is switched off by default, or still a preview, says so.
Use the search box above to find a word on every page, or open the [printable version](/help/all) to read or export
everything at once. Admins also see the settings of each feature, and a Settings page.
