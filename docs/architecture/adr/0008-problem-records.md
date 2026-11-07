# ADR-0008: Problem records and typed links

Status: proposed, 2026-10-22. Design: [HLD-problems.md](../HLD-problems.md).

## Context
Argus turns signals into issues with per-source keys (`incidents` keyed `DC|subject|family`, `qa_issues`, CVE groups,
`cicd_findings`, capacity/VM findings). Every RCA, fix handoff, Lazy reading, Matrix thread and Jira proposal hangs off
exactly one issue. One root cause routinely produces many issues on different subjects (incident #2's dead upstreams on
`10.21.51.31` behind the www/admin.cdn.northwind.example 5xx incidents, the QA reports and the 502 alerts; one kernel KEV across 50
hosts). The operator wants to map several issues to the one RCA that explains them. There is no object to group them,
no many-to-many link, and no way to point an RCA at an existing root cause.

## Decision
1. **A Problem is a new first-class record** (`problems`, shown `P-<id>`) with its own lifecycle
   `new → investigating → known_error → fix_in_progress → resolved → closed`, a root cause (with its proof label), a
   workaround, and one fix handoff / Lazy reading. Issues keep their own tables, keys, dedupe and lifecycles unchanged.
2. **Links are polymorphic references, not copies**: `problem_links(problem_id, ref_type, ref_id, link_type, state,
   is_primary, source, confidence, reason, by, at, decided_by, decided_at)`. Link types `caused_by`, `explains`,
   `fixed_by`, `related_to`, `duplicate_of`, `blocks`, each allowed only for listed ref types; only accepted links have
   effects. An issue may have several causes; one is primary.
3. **Argus proposes, a human decides.** A deterministic suggester (no model) proposes links with a confidence and a
   reason; the deep RCA model says "Explains: P-n" or "new". Only facts are accepted automatically: the RCA's own target
   incident and the RCA itself. Everything else is `suggested` until an admin accepts it (viewers may suggest).
   Auto-accept exists, default off, ≥ 0.90 and limited to non-production environment classes unless the operator
   sets production explicitly.
4. **Effects follow the primary accepted `caused_by` link**: one Lazy reading / fix handoff, one banner, one Matrix
   thread (a first production page is never folded), one Jira proposal per problem through the Proposed tasks queue
   (never auto-created), a resolve that offers the linked issues after verification.
5. **Naming**: the record is "Problem"; the existing per-run "problem" (`triage_problems`, `/incidents/problem`) is
   relabelled "Finding" in the UI only. Code, prompt contract and routes of the old object keep their names.
6. **Ownership**: `bin/problems.py` (argus) is the only writer of the three new tables; the Go app renders and writes
   request files to `settings-in/problem-actions/`; the root board sync reads ops.db for proposals. Go-only pages.
7. **Migration is additive and backfilled**: each incident's deep RCAs become one problem with the RCAs `explains` and
   the incident `caused_by` (accepted); everything else enters as suggestions.

## Alternatives rejected
- **Make the incident the root-cause record (parent incident / incident merge).** Incidents are symptom-keyed and
  auto-resolve on alert silence; a root cause outlives its symptoms (a known error without firing alerts), and QA
  issues, CVE groups and capacity findings are not always incidents. Merging incidents would also break the
  `DC|subject|family` dedupe that the collectors rely on.
- **A unified `issues` table that every collector writes.** Big-bang: changes every collector, `incidents.py`,
  `qa_issues.py`, `cve_match.py`, `cicd_estate.py` and all Go pages at once. The strangler is a resolver per ref type.
- **Tags or labels on issues** ("rootcause:upstream-31"). No lifecycle, no workaround, no owner, no audit, no place for
  the RCA set and the fix; free-text drift.
- **Link via the RCA only (RCA has many incidents).** An RCA is a document at a point in time; there are several per
  cause, and the cause needs a status, workaround and fix that outlive any one report.
- **Model-only linking** (ask the model to cluster). Costs on every run, not reproducible, not explainable; a wrong
  link can fold a page. The model contributes one claim per deep RCA; the suggester's features are deterministic and
  their reason is shown.
- **Server-side SVG graph as the main view.** Unreadable beyond ~12 nodes (the KEV case has 50) and not accessible;
  a grouped list is the graph.

## Consequences
- The operator maps issues to a root cause in one place; Jira gets one task per cause; Matrix one thread per cause.
- Three additive tables, one new request directory and one path unit; two root copies change (request dir, board sync).
- A wrong accepted link has real effects (thread, proposal folding, resolve offer): mitigated by human acceptance,
  the page exception, `removed` links, and precision metrics before auto-accept is considered.
- New vocabulary on screens ("Finding") and a parity exception for the relabelled page.
- Later, when the action broker exists, problem requests become audited `local` actions with no approval; the
  `problem_events` table remains the domain audit.
