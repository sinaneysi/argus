---
title: Glossary
summary: The words Argus uses, in one line each.
order: 10
---

## Terms {#terms}

| Word | Meaning |
|---|---|
| Acknowledge | Say "someone owns this incident". Anyone can. |
| Admin | The role that can change things in Argus. See [Roles](/help/overview#roles). |
| Advisers | Kai (DevOps), Quinn (QA) and Nico (NOC), who propose [ideas](/help/agent#ideas). |
| Blind spot | Something the vulnerability scan could not see, listed so it is not mistaken for clean. |
| Brief | The first part of the 09:00 digest. See [Notifications](/help/notifications#brief). |
| Customer-facing | A public name customers use. Only production incidents on these page the team. |
| Deep RCA | A long, read-only investigation of one incident. See [Deep RCA](/help/operations#deep-rca). |
| Digest | One Matrix message at 09:00 and 17:00 that collects everything that did not page. |
| Disposition | What Argus decided about one chat message: problem, known, duplicate, not a problem, suspicious. |
| EOL | End of life: a version that no longer gets security fixes. |
| EPSS | A score for how likely a vulnerability is to be exploited soon. |
| Finding | One result of one run. Several findings make an incident. |
| Fixer | Argus's write path on develop and pre-release, after an admin's review. See [Take action](/help/operations#take-action). |
| Flapping | An alert that fires and resolves again and again. Counted, never investigated. |
| Hub service | A tier-1 service many others depend on. Raises an incident's severity one step. |
| Incident | One record per problem: all findings about the same host and symptom. |
| KEV | CISA's list of vulnerabilities known to be exploited. In KEV on production means P1. |
| Summary (Lazy reading) | The five-line card at the top of a report: what broke, why, impact, fix, fix status. |
| Lock | The deploy lock of the shared pipeline; one pipeline holds it at a time. |
| Page | An urgent message Element notifies for. |
| P1 to P4 | Vulnerability priority, P1 first. See [Priority](/help/security#vulns-priority). |
| Problem (P-123) | The root cause behind several incidents and other items. See [Problems](/help/operations#problems). |
| Proposal | A change or task Argus suggests and a person decides. |
| Proven, probable, unproven | How sure a claim in an RCA is. |
| RCA | Root-cause analysis: what broke and why, with fix options. |
| Readiness | Whether a repo can use the shared pipeline ([Check a repo](/help/delivery#check-a-repo)), or the sales-event report. |
| Argus-internal | A failure caused by Argus itself, never shown as a production failure. |
| Snooze | Hide a proposed task for 24 hours. |
| Suppress | Accept a vulnerability's risk for a time, with a reason and an expiry. |
| Viewer | The read role. |
| Write-access agent | Whoever carries out a fix prompt: a person with Claude Code, or the fixer. |
