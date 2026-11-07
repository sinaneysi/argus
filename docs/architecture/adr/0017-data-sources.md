# ADR-0017: Data sources: a registry of every system Argus reads, many per type, and a routing knowledge base

Status: accepted, 2026-11-03. P1 ships with this ADR; P2 (discovery and the catalogue) is built, see "P2 (as built)". Builds on ADR-0016 D5 (the brain is an intent-level tool catalogue
over KnowledgeSource adapters): a data source is a configured instance of such an adapter, and P3 of this ADR is the
routing half of D5's tool server.

## Decisions taken with the operator

- **R1:** "a section named Data sources, like Grafana's, with all supported data sources; the user can add as many as
  they want (e.g. 10 Grafana and 2 GitLab)". A question about one service must not fan out to every instance, so a
  **routing knowledge base** maps services to sources: **Outline plus a DB copy**. Each service page in the wiki gets
  an editable "Data sources" table; Argus parses it into its DB; the agent reads the DB; a table that does not parse
  is an error on the page, never a silent skip.
- **R2:** built in phases: P1 registry + UI + adapters, P2 discovery and the wiki tables, P3 the agent's routing tool,
  P4 learning.

## Context

Before this ADR each external system was a one-off integration with its own page and secret location: GitLab
(read-only token for the agent; the GitLab issues tracker, ADR-0009/0010), Grafana per datacenter, Outline, and
Kubernetes clusters (Settings -> Kubernetes). A second Grafana meant a configuration edit plus code, and nothing told
the agent which GitLab holds the payment service.

## Decision

**D1. One registry, written by root.** A data source is `<type>/<name>` (name `^[a-z0-9][a-z0-9-]{0,30}$`, unique per
type) with env (`develop`, `pre-release`, `release`, or `shared` for one system serving every environment), url,
purpose, status, last check, check detail (no secret), created by/at and, for migrated entries, where the credential
still lives. Root's `argus sources apply` (run by `argus-sources.path` on `ARGUS_SETTINGS_IN/sources/`, the same
request-file flow as Settings -> Kubernetes) is the only writer of `ARGUS_SOURCES_DIR/registry.json` (0644, no
secret) and publishes `ARGUS_SETTINGS_OUT/sources-status.json` for the page; the registry rows are also written to the
`data_sources` table through the existing SQLite layer (the DB copy the agent and P3 read). `argus sources check`
(timer, every 6 hours) re-checks every source; `argus sources list` is the agent's catalogue.

**D2. Credentials per instance, root-only.** `ARGUS_SOURCES_DIR/<type>/<name>.cred`, 0600 root; 0640 root:<agent group>
only while the source is enabled for the agent (purpose `agent`, verified read-only), the same rule as kubeconfigs. A
credential goes app -> request file (0600, wiped and deleted on read) -> root, once. Never rendered back (the page shows
its last 4 characters), never logged, never in a result line or the status file.

**D3. Adapters per type (`internal/sources`), each with a health check and a read-only verdict that never writes.**
Status: `readonly`, `ok` (works; the purpose needs writes), `refused` (can write, purpose `agent`), `failed` (login
refused), `unreachable`, `unchecked`. P1 types:

| type | check |
|---|---|
| gitlab | `GET /api/v4/personal_access_tokens/self` (scopes, active) + `/user`. Read-only = scopes within `read_api`, `read_repository`, `read_registry`, `read_user`. |
| kubernetes | delegated to Settings -> Kubernetes (`internal/kube`: kubeconfig validation, `auth can-i`); the registry mirrors its clusters. |
| grafana | `GET /api/user` (not server admin) + `/api/user/orgs` (role must be Viewer; else the access-control permissions) + `/api/datasources` counts. |
| outline | `POST /api/auth.info` (the user's role decides). |
| elasticsearch | `GET /`, then `_security/_authenticate` + `_security/user/_has_privileges` (Elasticsearch) or `_plugins/_security/authinfo` (OpenSearch: read-only only when every role is a known read role). |
| sentry | `GET /api/0/` (`auth.scopes`: read-only = only `*:read`). |

Prometheus/Loki direct, Jira, MinIO, Splunk and Matrix are P2 types, listed on the page as "coming". Checks are https
only (no redirects, 10 s, 1 MB), pin the resolved address, and refuse loopback, link-local and metadata addresses.

**D4. Purpose.** `agent` = the agent reads through it; a write-capable login is refused (develop/pre-release may take an
explicit override). `tracker` = GitLab issues (needs `api`). `docs` = wiki publishing. **Release and shared entries never
take an override**; `tracker`/`docs` are allowed on `shared`, never on `release`.

**D5. Migration with a compatibility shim.** Root mirrors each existing single integration into the registry (managed
`legacy`, keyed by a legacy key, idempotent; it disappears when its old configuration does): the read-only GitLab
token, the GitLab issues tracker, every Grafana URL/token pair, Outline and each stored Kubernetes cluster. Root reads
those credentials where they already live and never writes there, so every job keeps working; the page says where to
change them until P3 moves jobs to the registry.

**D6. Agent visibility (P1).** `argus sources list [type]`: name, type, env, purpose, status, last check; never a
secret. Per-type tools take an instance where they exist (`argus tools kube <cluster>`).

**D7. Routing knowledge base (P2-P4).** P2: discovery per type (GitLab projects, k8s namespaces/deployments, Grafana/Loki
labels, Sentry projects, ES indices) builds a service catalogue and writes a "Data sources" table (type, instance,
selector, env, note) into each wiki service page; edits there are parsed back into the DB (`source_routes`); a table
that does not parse is shown as an error. P3: `sources.find(service|question)` in the D5 tool server: one match is
used, several are asked about or the top 2-3 tried, none fans out with time and cost limits; per-type tools take an
instance name. P4: a confirmed find becomes a proposed catalogue entry in Review.

**Demo.** `DEMO_MODE=1` ships several instances per type (three Grafanas, two GitLabs, a refused Kubernetes cluster,
...) and refuses every write: the form renders, root never runs.

## P2 (as built, 2026-11-03)

- **Discovery** (`internal/sources/discover`, `argus sources discover`, the agent's user, `argus-sources-discover.timer`
  hourly): only sources enabled for the agent (purpose `agent`, verified read-only). GitLab projects (path, id, default
  branch, last activity, archived, topics; `X-Next-Page` pagination, incremental `last_activity_after` between daily
  full sweeps), Kubernetes namespaces, workloads with app labels, services, ingress hosts (`kube.Tool.Inventory`: the
  same enabled-cluster gate and audit as `argus tools kube`), Grafana Loki label values of the app-naming labels that
  exist (Swarm task suffixes `.N.<id>` folded, session scopes and ids dropped), Prometheus `job` values, dashboards
  (uid, title, folder), Sentry projects (cursor pagination), Elasticsearch/OpenSearch index patterns (dated and
  rollover indices folded) and data streams. Names and metadata only. Every call goes through `sources.Get` (https,
  pinned address, no redirect) with budgets per source (150 calls, 240 s, 8 MB per answer, page/item caps). A capped
  or half-failed run is `partial` and prunes nothing; a complete full run prunes what is gone; a failed run keeps the
  last inventory. Tables `source_inventory`, `source_discovery_runs` in ARGUS_DB.
- **Catalogue** (`internal/sources/catalogue`, `argus sources catalogue`, also after each discovery): a deterministic
  matcher with a rule and a reason sentence per route (exact repository path 1.0; progressive name normalisation with
  `group-repo` before `repo`, generic names never alone, ambiguous names unmapped with "ambiguous: ..."; Kubernetes
  labels over object names; an ingress host among the service's domains 0.9; Loki/Prometheus/Sentry/index names;
  dashboard titles weak). Services come from `ARGUS_SOURCES_SERVICES` (services.json with optional extra prefixes
  and domains), else the CI/CD application projects. `source_routes` rules: discovered rows are `proposed`; a
  rejected route returns only when its evidence hash changes; `human` rows are never touched by a build; a confirmed
  route no longer seen is `stale`; `source_unmapped` and `source_catalogue_runs` hold the rest.
- **API and actions**: the JSON API `/api/v1/sources/catalogue` (`internal/app/api.go`, `sources_catalogue.go`):
  GET summary, routes (service/q/type/status filters, paging), unmapped, runs and the state of one action, for any
  signed-in user; POST `routes/confirm|reject|edit` and `discover` for admins with recent 2FA (sensitive policy) and
  the session's CSRF token (`X-CSRF-Token`, from `GET /api/v1/session`). The UI is the Next.js frontend's (the
  server-rendered page first shipped in PR #25 was removed). Actions are request files in `settings-in/source-actions/` applied by
  `argus sources actions` (the agent's user, `argus-sources-actions.path`), results in `source_action_results`, an
  audit row each in `source_route_audit`. The demo refuses every write.
- **Agent**: `argus sources routes <service>` (confirmed first, then proposed with confidence and reason) and
  `argus sources unmapped [type]`.
- **Not built: the Outline two-way table.** Argus has no Outline publisher in Go yet (the documentation publisher is
  a port requirement in docs/ROADMAP.md), so the catalogue lives in the DB and the page; the wiki section's rules are
  written down as a requirement of that port.

## P3 as built (2026-11-03)

- **D12. `argus sources find` (deterministic).** `internal/sources/find`, for the agent
  `argus sources find "<service or question>" [--type gitlab|k8s|loki|prometheus|sentry|es]... [--release] [--no-fanout] [--json] [--run id]`
  (aliases kubernetes/kube, prom, elasticsearch/opensearch, logs). The service comes from an exact service name, else
  from names in the text resolved by the catalogue's own rules (repository name with prefixes/suffixes dropped,
  group-repo, phrases longest first, a service domain from services.json). Generic and everyday words ("search",
  "order", "logs", "api") never match alone: such a word names a service only as the whole input or next to
  "service"/"repo"/"project"/"app". Several services are all reported (max 3), never guessed; no service means no
  fan-out. Targets per type from `source_routes`: confirmed first, then proposed with confidence >=
  `ARGUS_SOURCES_FIND_MIN_CONF` (0.6); ties: fresh before stale, then source/kind/locator. Rejected routes and
  instances not enabled for the agent (`data_sources`, else the status file) are never returned.
- **D13. Policy.** One confirmed target -> use it; several -> the top 2-3 (`ARGUS_SOURCES_FIND_TOP`, max 3) and say
  which; none -> a capped fan-out, only for the `--type` asked and only for one resolved service:
  `ARGUS_SOURCES_FANOUT_MAX_INSTANCES` 5, `_MAX_CALLS` 20, `_MAX_SECONDS` 60, `_MAX_OUTPUT` 6000 bytes (configuration,
  clamped). GET only through the discovery client (`discover.Options.Probe`: pinned address, no redirects, capped
  answers): GitLab project search, Loki label values of the app labels, Prometheus job values, Sentry project query,
  `_cat/indices/*name*`, the kube tool's inventory; hits go through the catalogue matcher. The report says "searched N
  of M instances", partial ones, skipped release ones, not-searched ones and the limit that stopped it; the command's
  help tells the agent to repeat it honestly. **Release instances are never scanned** unless the question is about
  release (`release`/`production`/`prod`, `main-*`, or `--release`) and root verified them read-only. Routed targets on
  release instances are returned: reading a known route is not a scan.
- **D14. Instance-aware tools.** Argus has one per-type read tool, `argus tools kube <cluster>`, and it already takes
  the instance; find prints it as the tool hint. GitLab, Grafana, Sentry and Elasticsearch have no read tool in Argus
  yet: find prints the locator for a person. No legacy environment shim (Argus has none).
- **D15. Learning hook (feeds P4).** A fan-out hit for a known service is inserted as `origin learned`, `status
  proposed`, confidence capped at 0.7, evidence `run <ARGUS_RUN_ID>: fan-out found it (...)`, audited in
  `source_route_audit` (`by_user sources-find`); never confirmed automatically; an existing row (any status, rejected
  included) is never touched. A catalogue build leaves learned rows alone.
- **D16. Audit.** One JSON line per find in `ARGUS_SOURCES_FIND_AUDIT_LOG`: time, a 12-hex hash of the question, the
  resolved services, the types, the chosen targets (type:source:status), per fan-out searched/of/hits/limits/
  skipped-release, learned count, run id. Never the question text or a credential.
- **API.** `GET /api/v1/sources/find?q=&type=&release=1` (admins): the same routing as JSON, read-only. The web process
  never fans out, never learns and writes no audit line; `fanout` is null and an unrouted type says "fan-out runs in the
  agent". The demo command routes but never fans out.

## P4 as built (2026-11-04)

- **D17. Route decisions.** `GET /api/v1/sources/review` (`why`, `type`, `q`) lists what waits for a person: learned
  routes, then discovery's strong proposals (confidence >= 0.9, not stale, at most 25, the rest stays in the catalogue).
  `GET /api/v1/sources/review/item?id=` adds who found it. `POST /api/v1/sources/review/act`
  (`confirm`, `reject` with an optional reason, `edit`) is the catalogue's flow: admins, recent 2FA, CSRF, one request
  file, refused in the demo. Anyone signed in reads. Handlers: `internal/app/sources_review.go`.
- **D18. Not in Argus yet.** Argus has no Outline publisher, so there is no write-back and no conflict reason, and it has
  no tier data in Go, so the order is reason, confidence, service. Both follow the Outline table when it is built.
- **D19. Reject reason.** Optional, up to 300 characters, kept in `source_route_audit`.
- **D20. Metrics.** `GET /api/v1/sources/metrics`: routes by type and status, the last 7 days of finds from
  `ARGUS_SOURCES_FIND_AUDIT_LOG` (each find once, by its best answer; `log` is `ok`, `missing` or `unreadable`, and an
  unreadable log is never shown as zero), and the top 5 unmapped items, ambiguous ones first.
- **D21. No credential migration.** Argus has no legacy environment shim. `sources.Legacy` only says where the mirror
  finds the existing integrations; it is not a fallback when a credential is resolved.

## Consequences

- One page answers "what can Argus read, and is each login read-only?"; adding the eleventh Grafana is a form.
- Root still owns every secret; the web app (nobody) and the agent never read a credential they do not need.
- Migrated credentials stay where they are until P3; the registry only points at them, so nothing drifts.

## Rejected

- **A secrets vault** for one host; the per-file root pattern already holds.
- **The web app as the registry writer**: it runs unprivileged and must not own what root acts on.
- **Moving migrated secrets in P1**: every job would change at once; P3 switches them one by one.
- **Fan-out of every question to every instance**: cost and time grow with each instance; routing is the point.
