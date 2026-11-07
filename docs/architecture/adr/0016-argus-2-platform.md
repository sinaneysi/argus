# ADR-0016: Argus 2 — a Go-only platform with Day-2 operations, a pluggable brain, a provider router and a measured learning loop

Status: **accepted**, 2026-10-30 (operator decisions below). D2 and OD-1 (server-rendered Go UI) are **superseded by
[ADR-0018](0018-monorepo-next-go-api.md)** (2026-11-03: Next.js frontend, Go JSON API). Author: Sam (architecture). Baseline: GitLab `main` `f0d119b`.
Design set: [argus-2/README.md](../argus-2/README.md) (product, IA), [argus-2/architecture.md](../argus-2/architecture.md),
[argus-2/roadmap.md](../argus-2/roadmap.md). Builds on ADR-0001/0002/0003/0006/0007 (config, adapters, access,
environments, SSH certs), ADR-0008 (problems), ADR-0013 (task chat, secrets, lessons), ADR-0014 (open source),
ADR-0015 (rename). **Supersedes** ADR-0005 (Python/Go interplay) and ADR-0012 decisions 2, 4–8 (Next.js front);
keeps ADR-0012's monorepo layout and per-page view-model split.

## Context

The operator's goals (2026-10-30): Day-2 operations with actions; a brain over external knowledge sources;
code-aware investigation and fixes; self-improvement; reuse at any company by configuration; several AI providers
with fallback (the Claude weekly limit stopped 30 of 79 runs); UI clarity as the first quality; two-way GitLab/Jira
tasks; create-task chat with a temporary secret box; chat ingestion from Matrix, Mattermost and Rocket.Chat;
ideas agents. Added the same day: **Argus 2 must be written completely in Go; no Python at runtime.**

Facts that shape the design: the web app is Go (38.7k lines) and a pure renderer; the jobs are ~44k lines of
stdlib Python + 3.2k lines of shell behind 113 units; the only model runtime is `claude -p`; adapters, config loader
and action broker are designed but not built; ~60 GET pages in 8 nav groups with decisions spread over 5 pages;
the secret box is shipped but off because the edge → argus-01 hop is not trusted.

## Decisions

**D1. Go only.** One Go module, two binaries: `argus-web` (the app, built without writer packages) and `argus`
(multi-call: worker, checks, ingest, tools, broker, dispatchers, guard, secret-exec, eval, config). Root runs a
root-owned copy, never the clone (T1 unchanged). Python is ported job by job with shadow parity (roadmap §1 rule 3);
Python is frozen to fixes from P0; pantalaimon is replaced by `mautrix-go` + pure-Go `goolm` in the last phase.
*Rejected:* keeping Python jobs and Go web (two runtimes, two config loaders, duplicated clients — ADR-0005's cost
continues forever); a big-bang rewrite (no shippable state for months); per-job Go binaries (more artefacts to
install as root, no gain over subcommands).

**D2. Server-rendered Go UI; no Next.js.** `html/template` + `embed`, today's CSP, no client JavaScript; view
models per page and a `/api/v1` JSON API in Go. Live chat uses POST → 303 → thread with a 3 s meta refresh while a
turn is pending. *Rejected:* Next.js (Node runtime contradicts D1; 42–57 days of ADR-0012 spent on a framework
swap instead of the IA); `templ` (code generator, second template language); HTMX/nonce'd script (breaks the no-JS
contract for a small gain).

**D3. Information architecture v2: 7 sections + one Review inbox.** Today, Review, Operations, Incidents, Work,
Delivery & Estate, Argus; Settings and Help in the footer. Every human decision (task proposal, action approval,
lesson, idea, chat reply, problem link, knowledge promotion) is a row in Review. ~60 pages → ~30; old URLs 301 for
90 days. *Rejected:* restyling the current nav (does not remove the five decision places); one page per module
(recreates the sprawl).

**D4. One domain model, strangled in.** Source, Signal, Finding, Task, Action, Approval, Run, Agent, Knowledge
(+ Check, Job, Conversation, Eval case). Existing tables keep their writers; projection jobs upsert core rows
until each writer is ported. Evidence state in `ops.db` (argus), decisions and writes in `board.db` (root),
retrieval in `knowledge.db` (FTS5). *Rejected:* a schema migration first (big bang); PostgreSQL (operations cost
for one host); a vector DB (scale does not need it).

**D5. Brain = intent-level tool catalogue over KnowledgeSource adapters, served by Argus's own tool server.**
Tools like `logs.search`, `errors.top`, `code.read`, `deploys.recent` map to configured sources (Grafana,
Prometheus/Loki, Elasticsearch/OpenSearch, Splunk, Sentry, GitLab, Outline, Jira). The tool server enforces policy,
budgets, redaction and audit, and is exposed as MCP (stdio) to Claude Code and in-process to Argus's own loop.
Read-only agents lose `Bash`. *Rejected:* vendor tools exposed directly to the model (prompts become vendor-specific,
adopters fork prompts); enforcement only in the Claude Code hook (does not hold for other runtimes); third-party
MCP servers per vendor (each with its own auth model and write tools we cannot vet).

**D6. Provider router with fallback, deferral and an eval gate.** Two runtimes (`claude-code`, `argus-loop`),
provider adapters `anthropic`, `openai_compat` (OpenAI, Azure, Gemini compat, Ollama, vLLM), routes per task class,
per-provider budgets and `exhausted_until`, data classes per provider, priority classes (p1 urgent never waits;
p4 background never pays for fallback). A model may enter a route only with an eval score ≥ `MODEL_ROUTE_MIN_SCORE`
for that agent. *Rejected:* an LLM gateway (LiteLLM etc.: a large Python dependency, contradicts D1, does not solve
tool-loop portability); only Claude with retries (does not survive a weekly limit); free-for-all model choice
(quality silently drops).

**D7. Day-2 checks are data; actions go through the broker with five tiers.** `checks.json` defines family, targets,
schedule, thresholds, source, owner, runbook and allowed actions; runs are deterministic, the model only explains
failures; "stale" is a first-class state. Tiers: T0 read, T1 self, T2 non-prod (develop may be automatic by policy,
pre-release one approver), T3 production narrow capability (human approves the plan hash, every time), T4 never
(refused in code). The broker is ACCESS.md B1–B4, built in Go. *Rejected:* per-check custom scripts with their own
action buttons (today's pattern; no common audit or tier); model-chosen commands for production (approver approves a
prompt, not commands — OVERVIEW F3).

**D8. Learning loop = outcomes + approved knowledge + replay evals + gated promotion.** Accepted RCAs/plans become
frozen eval cases; weekly replay with a judge from a different provider plus deterministic checks and safety cases;
prompt versions, lesson batches and route changes are promoted from Review only without regression. Quality metrics
(acceptance, edit rate, recurrence, cost per accepted answer) are shown on Argus → Quality. *Rejected:* notes-only
memory (no measure of "smarter"); automatic lesson approval (prompt-injection poisoning); fine-tuning (cost, data
leaves the host, no provider portability).

**D9. One install per company; multi-company by configuration.** `argus.env` + JSON data files (`environments`,
`sources`, `trackers`, `channels`, `providers`, `checks`, `agents`, `policy`) validated by `argus config check`;
services refuse to start on missing required keys; onboarding pages test each connection. *Rejected:* a shared
multi-tenant SaaS (one process holding many companies' production access and secrets is an unacceptable blast
radius); forks per company.

**D10. Chat and tracker integration: agents investigate early, humans create tickets.** Chat sources are
untrusted; per-channel policy (`ingest`, `reply`, `pre_investigate`); pre-emptive investigation is T0 only under a
daily budget; the result is a Review row "Accept & create issue"; the tracker of record is chosen by
`TRACKER_ROUTES`. In New-task chat the human's **Create issue** click is the acceptance. *Rejected:* auto-creating
issues (standing operator rule; noise); replying in channels by default.

**D11. Secret box on after the hop is TLS.** The backend listener serves TLS with an internal CA, the edges verify
it, edge logs carry no bodies, the secret route refuses non-TLS arrivals (`TASK_SECRETS_REQUIRE_TLS_HOP=1`); then
`TASK_SECRETS=on` for develop and pre-release tasks (release stays refused, ADR-0013 D3). *Rejected:* client-side
encryption (needs JS), secrets via Matrix DM (persisted history).

## Consequences

- Positive: one language, one config loader, one HTTP client, one audit path; policy holds for every model
  provider; runs no longer stop on a provider limit; one place for every decision; adopters configure, not fork.
- Negative: 121–169 engineer-days (≈ 50–60 of them porting); a long period with two job runtimes (bounded by the
  per-phase cleanup MRs); RE2 regex semantics and `difflib` ratios must be re-verified; MPL-2.0 enters with mautrix
  (P11); Claude Code remains an external Node program when chosen as a runtime (not Argus code).
- ADR-0005 and ADR-0012 (§2, §4–§8) become *superseded* when this ADR is accepted; ROADMAP.md phases A–D are executed
  in the order of argus-2/roadmap.md.

### Operator decisions (2026-10-30)
- **OD-1 — changed:** Go + small JS islands. Pages stay server-rendered Go templates; small, self-hosted, CSP-compatible JS islands (no inline script, `script-src 'self'`, no third-party CDN, no Node build at runtime) are allowed where they clearly help: live chat streaming, the secret box, and dense widgets. Every island must degrade to a working no-JS page. D2 is amended accordingly.
- **OD-2:** yes, Claude API-key fallback capped at $40/week, urgent and task work only; background jobs wait.
- **OD-3:** OpenAI/Gemini for redacted internal evidence; local Ollama for raw chat; no customer chat text to external providers.
- **OD-4 — changed:** yes, investigate read-only before anyone reads, AND reply in the message thread (short acknowledgement + finding). Tickets still only after a human accepts. Thread replies must pass the secret scan and never contain production identifiers beyond what the message already contained.
- **OD-5:** per-service routing (`TRACKER_ROUTES`), Jira default for team work, GitLab issues for code-only agent work.
- **OD-6 — changed:** edge→argus-01 TLS first; then the secret box is enabled for develop, pre-release AND production tasks. Production credentials: single-use, shortest TTL, never written to disk unencrypted, audit row per use, and the production write itself still needs the human-approved plan (OD-7).
- **OD-7:** no production autonomy beyond today.
- **OD-8 — changed:** remove SEO and design review from the product entirely (not ported, not an optional check pack); the code-based designer agent stays a development tool outside Argus. **2026-11-07, operator:** the SEO page comes back (Marketing → SEO, `/api/v1/seo`); it reads the tables the external SEO jobs write, and Argus still runs no SEO job.

## Decisions the operator must make

| # | question | recommendation |
|---|---|---|
| OD-1 | Accept D2: no Next.js, Go templates, no client JS (live chat via 3 s refresh)? | **Yes.** Revisit only with a measured need for streaming, in its own ADR. |
| OD-2 | Paid fallback when the Claude subscription hits its weekly limit: Claude API key with a weekly cap? | **Yes, $40/week cap**, used only by p1/p2 jobs; p4 jobs defer. |
| OD-3 | Which non-Anthropic providers, and what may they see? | **OpenAI (or Gemini) for `internal` redacted evidence; a local Ollama model for raw chat classification.** No customer chat text to external providers. |
| OD-4 | Chat ingestion autonomy: investigate read-only before anyone reads the message? | **Yes, T0 only, $3/day**, no channel replies except a thread ack in opted-in channels; tickets only after Accept. |
| OD-5 | Tracker of record when both GitLab issues and Jira exist? | **Per-service routing** (`TRACKER_ROUTES`), default Jira for team work, GitLab issues for code-only agent work. |
| OD-6 | Secret box hop: TLS edge → argus-01 with an internal CA (stage nginx repo change, tag + pin)? | **Yes**, then `TASK_SECRETS=on` for develop and pre-release only. |
| OD-7 | Production autonomy in Argus 2 scope? | **None beyond today:** every production write is a human-approved plan hash; revisit narrowly scoped auto-capabilities after 30 days of broker audit with zero policy violations. |
| OD-8 | Product cut list: SEO becomes an off-by-default check pack; design reviews leave the product nav (or are dropped instead of ported)? | **Yes**; drop design-review screenshot tooling, keep the code-based designer agent in development only. |
