# Argus 2: roadmap

Status: **proposal**, 2026-10-30. Decision record: [ADR-0016](../adr/0016-argus-2-platform.md). Design:
[architecture.md](architecture.md). Supersedes the order (not the content) of [docs/ROADMAP.md](../../ROADMAP.md): phases
A (generalisation), B (broker), C (CVE), D (problems) are folded in below, **implemented in Go**.

Estimates are engineer-days, agent-assisted, excluding review wait. Every phase ships alone, keeps `main` deployable
through `argus-deploy` (CI only), and has a rollback.

## 0. Starting point (measured on `main` `f0d119b`)

| area | today | size |
|---|---|---|
| Web app | Go, `apps/api` (module `argus/opsreport`), ~50 GET routes + admin/help/account, string-built HTML, strict CSP, no JS | 38.7k lines Go (non-test) |
| Jobs | Python stdlib, `bin/` 105 files, started by 113 units in `systemd/` | ~44k lines Python + 3.2k lines shell (`scripts/`) |
| Python web app | `app/` deprecated; 5 job libraries live there (`fix_handoff`, `lazy`, `backup_plan`, `backup_simple`, `backup_uri`) | 13.9k lines |
| Third-party Python at runtime | `cryptography` (AES-GCM in `task_secrets.py`), pantalaimon (Matrix E2EE proxy container) | — |
| Model | `claude -p` only (`claude_gate.py`, `run-task.sh`); Claude weekly limit stopped 30 of 79 runs | — |
| Designed, not built | config loader (R1), adapters (R8), action broker (B1–B6), installer (R9) | — |

In flight on other branches (do not touch their files): ADR-0014 OSS cleanup (`agent/oss-clean-a-2026-10-30`:
config loaders and examples, Go build tags, Persian to `locale/`, tests, prompts) and ADR-0015 rename
(`agent/argus-p0-2026-10-30` P0 strings; P1 identifiers incl. Go module path and `OPS_*`→`ARGUS_*`, 270+ files;
P2 units/paths/users on argus-01).

## 1. Sequencing rules

1. **No Argus 2 code before ADR-0015 P1 is merged.** P1 renames the Go module path, env names and 270+ files; any
   earlier Go MR would be rebased through all of it. Argus 2 docs (this set) can merge any time.
2. **Python freeze from Argus 2 P0:** bug and security fixes only in `bin/`, `scripts/`, `app/`. New behaviour is
   written in Go, in the module being ported ("port, then extend"; never extend Python and port later).
3. **Strangler per job, with shadow parity** (the method that ported the web app, applied to jobs):
   - a Go subcommand `argus <job>` reads the same inputs and writes to `ARGUS_SHADOW_DIR` (a copy of the target DB
     table or files) while the Python unit keeps running;
   - `tests/jobparity/` replays recorded inputs (HTTP fixtures recorded by a stdlib proxy, fixture DBs, request
     files) through both and diffs rows/files/requests; CI runs it on every MR of a port;
   - on argus-01 the shadow runs ≥ 3 days with zero unexplained diffs (`argus parity report <job>`);
   - cut over = the unit's `ExecStart` changes in the repo, deployed by CI; **rollback** = revert that line (the
     Python file stays in the tree until the phase's cleanup MR, 2 weeks after cut-over).
4. Every phase touching the UI follows DESIGN_SYSTEM.md + DESIGN.md and is reviewed from code (no screenshots).
5. ADR-0014: every new file gets a manifest decision in `tools/oss/manifest.txt` in the same MR; the leak gate and
   ratchet must stay green; new Go dependencies are added to the licence allowlist only if MIT/BSD/Apache/MPL-2.0.
6. Dev and CI tooling (`tests/parity`, `tools/oss`, `docs/architecture/audit`) may stay Python until rewritten;
   **"no Python" means nothing Python is installed or run on an Argus host.**

## 2. Phases

| phase | content | Go port in this phase (Python → Go package) | exit criteria | rollback | days | risk |
|---|---|---|---|---|---|---|
| **P0 Foundations** | `cmd/argus` multi-call binary; `internal/config` (loader, schema, `argus config check/show/render`, `ARGUS_*` with `OPS_*` aliases); `internal/store` migrations (`jobs`, `provider_state`, `source_status`); `tests/jobparity` harness + shadow convention; root-owned install path of `argus` in `deploy/manifest` | `claude_gate.py` + `claude-gate.sh` → `argus claude-gate` (first parity case); `secret_scan.py` → `internal/secretscan` (golden corpus, RE2 audit) | `config check` WARN-only in self-check; parity harness green on claude-gate and secretscan corpora | units keep Python paths; remove manifest line | 6–9 | low |
| **P1 Never stop on a limit** | `internal/llm`: providers `anthropic`, `openai_compat`; router; limit detection for Claude Code (exit text/JSON "usage limit", reset time) → `provider_state.exhausted_until`; fallback chain `claude-code:oauth` → `claude-code:apikey` (weekly USD cap) → `argus-loop` later → `defer` (re-queue); priority classes; Settings → AI providers (status, budgets, read-only) | `run-task.sh`, `claude-run.sh`, `record.py`, `lazy_store.py` → `argus run <task>` (`internal/agent/claudecode`, `internal/runs`) | a forced-limit test (fake `claude` binary) defers p4 and falls back p1/p2; zero runs recorded as failed for "limit" in 7 days | `ARGUS_ROUTER=off` (direct `claude -p` as today); units back to `run-task.sh` | 7–10 | medium (limit text format is not a stable API: fixture-tested, unknown → treat as exhausted 6 h) |
| **P2 Clarity: IA v2 + Review** | nav v2 behind `ARGUS_NAV_V2`; Today = `/` + `/today`; 301 map (`redirects.go`); **Review** inbox over existing tables (proposals, ideas, lessons, Matrix previews, problem suggestions) with one row template; new pages in `html/template` + `embed`, view model per page (ADR-0012 decision 3, Go-only) | `today.py`, `brief.py`, `proposals.py` → `internal/today`, `internal/review` | DESIGN_SYSTEM §11 checklist on Today, Review; parity of every decision POST (same request file); nav ≤ 7 groups | `ARGUS_NAV_V2=0`; redirects off | 8–12 | low |
| **P3 Day-2 Operations** | `checks.json` registry + check runner; findings table + projection; Operations overview + family pages with freshness/stale, tier badge, one action; actions use today's request flows (broker comes in P5) | `cert_scan.py`, `cert_report.py`, `cert-domains.py` (`openssl` CLI → `crypto/x509`), `scripts/backup-check.sh`, `backup_*.py`, `app/backup_*.py`, `cred_expiry.py`, `cred_proposals.py`, `cdn_watch.py`, `hygiene.py`, `capacity.py`, `readiness.py`, `disk_explain.py`, `vm_explain.py`, `overrides.py` → `internal/checks/<family>` (~5.4k lines) | each family ≥ 3 days shadow parity, then cut over; every check shows "stale" when it misses 2 runs | per family `ExecStart` revert | 12–16 | medium (backup plans write MRs: dry-run parity first) |
| **P4 Queue + core runtime** | `argus worker` (leases, priorities, concurrency); timers and `.path` units **enqueue** instead of running; alert ingest normalised (`AlertSource`: Matrix rooms + Alertmanager webhook) | `ops-watch.py`, `alerts-snapshot.sh`, `notify*.py`, `notify.sh`, `matrix.py` (send via pantalaimon REST), `selfcheck.*`, `cred-check.sh`, `kuma-push.sh`, `apply-settings.py`, `run-requests.py`, `insights.sh` → `internal/{worker,ingest/alerts,notify,selfcheck,dispatch}` (~6k lines) | ops-watch shadow sees the same queue items for 3 days; no unit runs model work outside the worker | per unit revert; `ARGUS_WORKER=off` | 10–14 | medium (ops-watch is always-on: run Go in shadow with its own sync token) |
| **P5 Action broker** | ACCESS.md B1–B4 in Go: `argus actd` (root), `policy.json`, approvals by plan hash, audit chain, `ACTIONS_MODE=dry-run` → `on`; tiers T0–T4 in the UI; Day-2 actions routed through it; first T2 capability `ci.pipeline.cancel` (develop) | `fix-requests.py`, `fix-run.sh`, `fix_*.py`, `fix-record.py`, `app/fix_handoff.py`, `app/lazy.py`, `secret_exec.py`, `scripts/secret-exec.sh`, `cicd_write.py`, `guardrails/*_guard.py` → `internal/actd`, `internal/actd/exec/{fixer,gitlab,local}`, `argus secret-exec`, `argus guard` | 30 days of request files replayed: same decisions as today; exhaustive policy tests (deny by default, unknown = production, T4 refusals) | `ACTIONS_MODE=off` = today's direct dispatch | 10–14 | high (root code; operator installs and reviews) |
| **P6 Brain** | `argus tools mcp` (intent tools, policy inside); KnowledgeSource adapters; new `elasticsearch`, `splunk`, `sentry`, `prometheus`/`loki` direct; `argus-loop` runtime; `knowledge.db` FTS5; read-only agents lose `Bash` once their tools exist; engineer agent sandbox (tmpfs clone, `docker --network none` tests, MR on `agent/*`) | `gitlab_ro.py`, `gitlab_client.py`, `scripts/{obs,prom-query,prom-alerts,loki-triage,gitlab-ro,http-get,list-mrs,open-mr,mc-ro}.sh`, `scripts/ssh-ro.sh` (→ `internal/sshro` on `x/crypto/ssh`), `outline_*.py`, `incidents.py`, `problems.py`, `problem_suggest.py`, `rca_reports.py`, `security_events.py`, `service-*.py`, `inventory.py`, `vmstate.py`, `data_platform.py` (~11k lines) | the same incident RCA set produces equal-or-better eval score with tools via MCP as with Bash; `ssh-ro` catalogue tests ported | per agent `tools=bash` fallback; per adapter disable | 16–22 | high (largest port; tool behaviour change) |
| **P7 Learning loop** | outcome capture (verdicts, check-green, recurrence), eval cases from accepted RCAs, `argus eval` (judge on a different provider), Argus → Quality page, Review kind "Knowledge change", route gate `MODEL_ROUTE_MIN_SCORE` | `lessons.py` → `internal/learning/lessons` (difflib ratio → `internal/textsim`, golden-tested) | ≥ 30 frozen cases; weekly eval runs under budget; a regression blocks promotion in a test | `EVAL=off`; lessons keep ADR-0013 behaviour | 10–14 | medium (judge bias: different provider + deterministic checks) |
| **P8 Work: two-way + new-task chat + secret box** | `Tracker` interfaces (Jira, GitLab issues), `trackers.json` routing, canonical states; New task (chat) page; secret box after the TLS hop (operator: edge `proxy_ssl_verify`, internal CA), `TASK_SECRETS=on` develop/pre only | `board_sync.py`, `board-notify.py`, `task_agent.py`, `task_chat.py`, `task_secrets.py` (`cryptography` AESGCM → `crypto/cipher`), `jira_*.py`, `gitlab_issues.py`, `matrix_requests.py`, `matrix_dm.py` → `internal/tasks`, `adapters/tracker/*`, `internal/secrets` (~7.9k lines) | 7 days shadow of board sync (no writes) then cut over; secret canary test: value never in DB, logs, transcript, provider request | `TASK_SECRETS=off`; per unit revert | 14–18 | high (two-way sync loops; root writer) |
| **P9 Conversations** | `ChatSource` adapters `matrix`, `mattermost`, `rocketchat`; `channels.json`; clustering; pre-emptive read-only investigation (budget `CHAT_PREINVESTIGATE_DAILY_USD`); Work → Conversations | `triage_collect.py`, `qa_issues.py`, `qa_images.py`, `chat_messages.py` → `internal/ingest/chat`, `internal/conversations` | injection safety cases pass; no tracker write without a Review accept | per channel `ingest=off` | 8–12 | medium (prompt injection: T0 tools only) |
| **P10 Ideas, Delivery, Security lenses** | ideas as Review kind, accepted idea → task, impact metric; CI/CD and CVE pages on findings | `ideas.py`, `cicd_*.py`, `ci_vars_meta.py`, `cve_*.py` + `cve_feeds/` → `internal/{ideas,cicd,cve}`; the SEO and design-review jobs are dropped, not ported (ADR-0016 OD-8) | parity per job | per unit revert | 12–16 | medium |
| **P11 Python out, Matrix native, installer** | replace pantalaimon with `mautrix-go` + pure-Go `goolm` (E2EE in `argus watch`); delete `bin/`, `scripts/`, `app/`; `argus install` (users, dirs, units for enabled modules, render, check); Acme install test in a clean VM in CI; OSS export of Argus 2 | `pan-login.sh`, `pantalaimon/` → `internal/adapters/chat/matrix` (E2EE) | no `python3` in any unit, container or root copy (CI test greps rendered units); Acme install green | keep pantalaimon container (`MATRIX_E2EE=pantalaimon`) | 8–12 | medium (E2EE key migration: new device, verify once) |

**Total: 121–169 engineer-days** (of which the Python → Go port is ~50–60), about 6–8 months next to operations
with one operator and agents. P1 and P2 deliver the two most visible fixes (no stopped runs; one decision inbox)
in the first ~3 weeks after ADR-0015 P1.

## 3. Python-only libraries and their Go replacements

Today's Python is almost all standard library, so most replacements are Go standard library.

| Python today | used for | Go replacement | licence |
|---|---|---|---|
| `urllib.request`, `http.client`, `ssl` | every HTTP client (GitLab, Jira, Grafana, Outline, Matrix, MinIO) | `net/http`, `crypto/tls`; one hardened client `internal/adapters/httpx` (SSRF guard, allowlists, budgets, ETag, redaction) | BSD-3 (Go) |
| `sqlite3` | ops.db, board.db | `modernc.org/sqlite` (pure Go, already vendored; FTS5 included) | BSD-3 |
| `json`, `re`, `hashlib`, `hmac`, `secrets`, `base64`, `html`, `ipaddress`, `tempfile`, `shutil`, `argparse`, `fnmatch` | everywhere | `encoding/json`, `regexp` (RE2: **no backreferences/lookaround**, audit `secret_scan` patterns in P0), `crypto/sha256`, `crypto/hmac`, `crypto/rand`, `encoding/base64`, `html/template`, `net/netip`, `os`, `flag`, `path.Match` (semantics checked by tests) | BSD-3 |
| `fcntl` locks, `subprocess`, `concurrent.futures` | single-instance locks, running `claude`/`git`/`docker` | `golang.org/x/sys/unix` Flock (vendored), `os/exec`, goroutines + `golang.org/x/sync/errgroup` | BSD-3 |
| `difflib.SequenceMatcher` | dedupe (lessons, ideas, proposals) | `internal/textsim` (≈100-line port of the ratio, golden tests against Python outputs) | own (Apache-2.0) |
| `cryptography` AESGCM | task secrets | `crypto/aes` + `crypto/cipher` (GCM) — the Go app already does this for TOTP | BSD-3 |
| `openssl x509` CLI | cert decoding (`cert_scan.py`) | `crypto/x509` | BSD-3 |
| `openssl dgst -sign` CLI | Google service-account JWT (`gsc_check.py`) | `crypto/rsa` + `crypto/sha256` (RS256 by hand, ~40 lines) | BSD-3 |
| `ssh` via `ssh-ro.sh` | read-only host commands, later capability certs (ADR-0007) | `golang.org/x/crypto/ssh` (vendored; supports certificates, ProxyJump by dialling through the jump client) | BSD-3 |
| Matrix: own `matrix.py` over pantalaimon | send/receive, E2EE through pantalaimon (Python container) | P4–P10: `net/http` to pantalaimon (same as today). P11: `maunium.net/go/mautrix` with build tag `goolm` (pure-Go Olm/Megolm, no cgo) | **MPL-2.0** (file-level copyleft; compatible with Apache-2.0 distribution; add to the leak-gate licence allowlist) |
| Jira / GitLab clients (own, urllib) | REST | own small clients on `httpx` (endpoint allowlists matter more than SDK coverage). Rejected: `gitlab.com/gitlab-org/api/client-go` (Apache-2.0, large surface, ~40k lines vendored), `andygrunwald/go-jira` (MIT, v2 never finished) | — |
| YAML | not used at runtime (config is JSON) | none. If a source needs YAML later: `go.yaml.in/yaml/v3` (maintained fork of `gopkg.in/yaml.v3`) | MIT + Apache-2.0 |
| MCP (new) | tool server for Claude Code | own JSON-RPC 2.0 over stdio (~300 lines, `internal/agent/tools/mcp`), no SDK | own |
| `zoneinfo` | `ARGUS_TZ` | `time.LoadLocation` + `time/tzdata` embedded | BSD-3 |
| Node `design_shots.mjs` | design screenshots | removed (standing rule: no screenshots) | — |

Net new third-party Go modules over the whole programme: `golang.org/x/sync` (BSD-3) and, in P11, `mautrix` (MPL-2.0)
with its pure-Go deps. Everything else is already vendored.

## 4. Reuse vs rewrite

| reuse as is | port 1:1 (shadow parity) | rewrite (new design) | delete |
|---|---|---|---|
| Go web app: auth, OIDC, 2FA, sessions, CSRF, CSP, layout, queue/detail components, help | collectors and checks, notify, ops-watch, board sync, task agent, CI/CD, CVE, ideas, lessons, fixer dispatch | IA/nav, Review inbox, Today merge, Operations pages; provider router; tool server; broker; eval; chat ingestion adapters; new-task chat | Python `app/` web (after its 5 libraries are ported), `bin/`, `scripts/`, pantalaimon, Next.js plan (ADR-0012 §2/§4/§7) |
| prompts (`prompts/*.md`), `config/*.json` data, trust model T0–T8, request-file contract | `secret_scan` patterns (RE2-audited), `ssh-ro` catalogue | domain core tables (projection first) | duplicated legacy routes (301 for 90 days) |

## 5. ADR-0012 (Next.js) in the light of Go-only

**Recommendation: do not build the Next.js front.** Go-only rules out a Node runtime on the host, and nothing in the
operator's 11 goals needs client JavaScript that the server cannot do. Keep from ADR-0012: the monorepo layout (P1,
merged), **the per-page view-model split** (it is what makes pages testable and gives `/api/v1`), and the JSON API
(read endpoints for future clients and the chat-ops bot). Replace its decisions 2, 4, 5, 6, 7 with: Go
`html/template` + `embed` components (auto-escaping, replaces string concatenation page by page as pages are
touched), the existing CSP unchanged, live chat by POST → 303 → thread page with a 3-second `<meta refresh>` only
while a model turn is pending (the `refreshMeta` mechanism that exists). Rejected: `a-h/templ` (MIT, typed, but a
code generator in the build and a second template language); HTMX or a nonce'd script (breaks "no JavaScript" for a
small gain; can be revisited as a separate ADR if streaming chat becomes a measured need).

## 6. First three MRs (each starts from `main` after ADR-0015 P1 merges)

1. **`feat(argus2): cmd/argus, internal/config, claude-gate in Go`** — P0 slice. `cmd/argus` with subcommands
   `config check|show` and `claude-gate check|skipped-json`; `internal/config` reading the env file with `ARGUS_*`
   and `OPS_*` aliases; `tests/jobparity` with the claude-gate cases (missing/true/false/unreadable switch file);
   manifest installs the root copy; self-check calls `argus config check` WARN-only. *Check:* parity green, Go tests,
   leak gate. *Rollback:* revert; nothing consumes it yet.
2. **`feat(llm): provider router and Claude limit fallback`** — P1 slice. `internal/llm` (anthropic, openai_compat
   with a fake server in tests), `provider_state` migration, `argus run <task>` wrapping today's `claude -p` call
   with limit detection, `defer` for p4 jobs, optional `claude-code:apikey` fallback **off until the operator sets
   `PROVIDER_ANTHROPIC_APIKEY_WEEKLY_USD`**; only `ideas` and `log-rca` units switch first. *Check:* fake-`claude`
   limit fixture; 7 days with no limit failures for the two units. *Rollback:* units back to `run-task.sh`.
3. **`feat(ui): navigation v2, Today merge, redirects`** — P2 slice, Go only: `NAV_GROUPS_V2` behind
   `ARGUS_NAV_V2`, `/` renders Today, 301 map in `redirects.go`, SEO and design reviews removed (OD-8), help
   updated. *Check:* route crawl (every old URL 200 or 301 to a 200), DESIGN_SYSTEM §11 checklist from code, demo
   build + leak check. *Rollback:* `ARGUS_NAV_V2=0`.

Owners: MR 1–2 a dev agent; MR 3 the designer agent with the design skills (standing rule); the operator reviews and
starts pipelines.
