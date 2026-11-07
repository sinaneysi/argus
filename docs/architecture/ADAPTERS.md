# Integration adapters

Status: proposal, 2026-10-22. Decision record: [ADR-0002](adr/0002-adapter-boundary.md).

## 1. Rules

1. **Adapters live in `bin/` (Python, stdlib only)** under `bin/argus/adapters/<kind>/<vendor>.py`. The
   web app (Python or Go) never calls an integration (OVERVIEW F6), so it needs no adapters.
2. **One interface per kind**, a `typing.Protocol` in `bin/argus/adapters/<kind>/__init__.py`, and
   `get(kind)` returns the adapter named by `<KIND>_ADAPTER`. A kind set to `none` returns a null adapter
   whose reads return empty results and whose writes raise `Disabled`; callers degrade (feature off).
3. **The model-facing tool surface does not change.** The model calls `scripts/obs.sh`,
   `scripts/gitlab-ro.sh`, `scripts/ssh-ro.sh`, `scripts/http-get.sh`. Those CLIs are already
   vendor-neutral in shape (`obs.sh <site> prom …`). Behind them, the script dispatches to the adapter.
   Prompts and `.claude/settings.json` therefore stay valid across vendors.
4. **Read and write are separate objects.** `XReader` is used by collectors and the model tools as
   argus with a read-only credential; `XWriter` is only instantiated inside root dispatchers or the
   action broker, with the write credential they alone can read. A process that has a reader cannot
   build a writer: the writer's constructor needs a credential file argus cannot open.
5. **Common behaviour** (in `adapters/_http.py`, already proven in `gitlab_ro.py`, `jira_client.py`,
   `outline_client.py`): https only, SSRF guard (allowed hosts, no private/link-local/metadata after DNS
   unless the host is in the allowlist, pinned address, no redirects), endpoint allowlist per adapter,
   timeouts, per-run call budget, shared hourly rate file, 429/5xx backoff with jitter, ETag cache, audit
   line per call (method, path template, status, ms — never a body or a header value), redaction of
   tokens in any error text.
6. **Errors:** `AuthError` (stop the run, credential status FAIL), `RateLimited`, `Unavailable` (retry
   later; Argus-internal, never a production page), `NotFound`, `Refused` (allowlist/SSRF), `Disabled`.

## 2. Monitoring (metrics, logs, alerts)

```python
class MonitoringReader(Protocol):
    def sites(self) -> list[str]                                   # OBS_SITES
    def query(self, site, promql, at=None) -> list[Series]          # instant
    def query_range(self, site, promql, start, end, step) -> list[Series]
    def alerts(self, site) -> list[Alert]                           # firing + pending
    def alert_rule(self, site, name) -> Rule | None
    def logs(self, site, logql, since, limit) -> list[LogLine]      # bounded: since ≤ 6h, limit ≤ 500
    def log_labels(self, site, label=None) -> list[str]
```

| adapter | vendor | notes |
|---|---|---|
| `grafana_proxy` | Grafana datasource proxy (Viewer token) | today's `obs.sh`; datasource UIDs discovered from `/api/frontend/settings` |
| `prometheus` | Prometheus + Loki direct | today's `prom-query.sh`, `logcli` |
| later `victoriametrics`, `mimir` | | same PromQL API, base URL differs |

**Alert ingest** is a separate interface because alerts arrive pushed:

```python
class AlertSource(Protocol):
    def events(self, since_token) -> tuple[list[AlertEvent], str]   # FIRING/RESOLVED, normalised
AlertEvent = {site, state, alertname, severity, instance, job, service, summary, value, labels, raw_ref}
```

| adapter | how | parser |
|---|---|---|
| `matrix_rooms` (today) | ops-watch long-polls rooms | per site `OBS_SITE_<ID>_ALERT_FORMAT`: `am-matrix-bold-v1` (`**FIRING [X]**`), `am-matrix-emoji-v1` (`🟠 FIRING [X]`), `kuma-v1` |
| `alertmanager_webhook` | a tiny root-owned receiver on loopback behind the edge, Alertmanager `webhook_configs` → `queue/` | structured JSON, no text parsing (preferred for new adopters) |

## 3. Chat

```python
class ChatReader(Protocol):
    def read_new(self, rooms, since_token) -> tuple[list[Message], str]   # untrusted text
    def permalink(self, room, event_id) -> str
class ChatNotifier(Protocol):
    def post(self, room, card: Card, *, loud: bool, thread=None) -> MessageRef   # Card = title, level, sentence, facts, link
    def reply_in_thread(self, room, parent, text, *, mention=None) -> MessageRef  # only QA follow-ups
class ChatDM(Protocol):
    def dm_events(self, since) -> list[DMEvent]; def dm_send(self, user, card) -> MessageRef
```

| adapter | status | notes |
|---|---|---|
| `matrix` | today (pantalaimon for E2EE) | `m.text` = loud, `m.notice` = silent; `m.mentions: {}`; refuses to post to watched rooms except `reply_in_thread` |
| `webhook` | today (`NOTIFY_WEBHOOK`) | notifier only |
| `slack` | later | notifier = `chat.postMessage` with blocks; loud = channel mention policy; reader = Events API (needs inbound) or `conversations.history` poll |
| `telegram` | later (notify.sh has a stub) | notifier only |

`notify_format.py` keeps building the `Card` from structured data; only rendering moves into the adapter
(HTML + plain text for Matrix, blocks for Slack).

## 4. Tracker

```python
class TrackerReader(Protocol):
    def projects(self) -> list[Project]
    def board(self, board_id) -> Board                        # columns, statuses, filter, quick filters
    def search(self, query, fields, since=None) -> list[Issue]
    def assignable(self, project, text) -> list[User]
    def transitions(self, issue) -> list[Transition]
class TrackerWriter(Protocol):                                 # root only, with the acting user's token
    def create(self, project, type, title, body, labels, assignee=None) -> IssueRef
    def comment(self, issue, body) -> None
    def transition(self, issue, transition_id) -> None
    def add_label(self, issue, label) -> None
```

| adapter | status |
|---|---|
| `jira_server` (DC/Server, PAT) | today (`jira_client.py`) |
| `jira_cloud` | later: same REST v2/v3 + agile, auth = email + API token |
| `gitlab_issues`, `github_issues` | later (boards = labels/milestones) |

Statuses and columns are never names in code: `JIRA_AGENT_COLUMN`, `JIRA_FIXER_STATUS`.

## 5. SCM / CI

```python
class ScmReader(Protocol):
    def projects(self, groups) -> list[Project]
    def file(self, project, path, ref) -> bytes | None
    def code_search(self, project, literal, ref) -> list[Hit]
    def commits(self, project, ref, since) -> list[Commit]
    def compare(self, project, a, b) -> Diff
    def branches(self, project) -> list[Branch]
    def pipelines(self, project, ref=None, status=None, since=None) -> list[Pipeline]
    def jobs(self, project, pipeline) -> list[Job]
    def job_trace_tail(self, project, job, lines) -> str          # redacted
    def deployments(self, project, env=None, since=None) -> list[Deployment]
    def lock_holder(self, project, resource_group) -> LockState
    def variables_meta(self, group_or_project) -> list[VarMeta]   # names + flags, never values
class ScmBot(Protocol):                                           # Argus's own repo only
    def push_branch(self, branch_prefix="agent/") -> None; def open_mr(self, branch, title, body) -> MrRef
    def commit_comment(self, sha, body) -> None
class CiWriter(Protocol):                                         # action broker only (ACCESS.md)
    def cancel_pipeline(self, project, pipeline) -> None
    def retry_job(self, project, job) -> None
    def play_job(self, project, job) -> None
    def set_variable(self, project, key, scope, value_ref) -> None   # value from a secret ref, never inline
```

| adapter | status |
|---|---|
| `gitlab` | today: `gitlab_ro.py` (reader), `open-mr.sh` (bot), `cicd_write.py` (writer, disabled) |
| `github` | later: REST + Actions (`workflow_runs`, `jobs`, `deployments`, `concurrency` instead of resource groups) |

CI conventions (engine project, deploy job names, lock resource group, env branches) are config, not
adapter code (CONFIG.md §3.7).

## 6. SSO

```python
class IdentityProvider(Protocol):
    def authorize_url(self, state, nonce, pkce) -> str
    def exchange(self, code, verifier) -> Tokens
    def verify(self, id_token, nonce) -> Claims                  # iss/aud/azp/exp/iat/nbf/nonce, RS256/ES256
    def roles(self, claims) -> set[str]                          # OIDC_ROLE_CLAIM + OIDC_ROLE_MAP
```

One adapter: generic **OIDC** (Keycloak, Entra ID, Okta, Authentik, Dex all fit). It lives in the app
(`app/oidc.py`, `apps/api/internal/oidc`), the only adapter that does; role mapping is new
(`OIDC_ROLE_MAP`).

## 7. Docs

```python
class DocsWriter(Protocol):                                       # root only
    def whoami(self) -> str
    def collection(self, name, create=False) -> CollectionRef
    def find(self, collection, title) -> DocRef | None
    def get(self, doc) -> Doc
    def upsert(self, collection, title, markdown, doc=None) -> DocRef   # owns only the argus:begin/end block
```

| adapter | status |
|---|---|
| `outline` | today (`outline_client.py`, eight methods) |
| `confluence`, `gitlab_wiki`, `markdown_repo` | later |

## 8. Object storage

```python
class ObjectStoreReader(Protocol):
    def list(self, alias, bucket, prefix, recursive=False, limit=1000) -> list[Obj]   # name, size, mtime
    def stat(self, alias, bucket, key) -> Obj | None
class ObjectStoreWriter(Protocol):                                # root, Argus's own backup prefix only
    def put(self, alias, bucket, key, path) -> None; def get(self, alias, bucket, key, path) -> None
```

| adapter | status |
|---|---|
| `minio_mc` | today (`mc ls/stat/du/find`, ro alias) |
| `s3` | later: SigV4 in stdlib (`hmac`, `hashlib`), path-style for MinIO; no boto3 |

## 9. Host access

Not a vendor integration, but the same shape, so the broker can treat it like the others:

```python
class HostReader(Protocol):   # scripts/ssh-ro.sh catalogue
    def run(self, alias, command, args) -> Output                 # fixed catalogue, validated args, capped, redacted
class HostExecutor(Protocol): # action broker only (ACCESS.md §6)
    def plan(self, target, capability, params) -> Plan            # dry run on the target, returns diff
    def apply(self, target, capability, params, plan_hash) -> Result
    def verify(self, target, capability, params) -> Check
    def rollback(self, target, capability, action_id) -> Result
```

## 10. Vulnerability feeds

```python
class VulnFeed(Protocol):
    name: str                                                     # osv, nvd, kev, epss, debian, ubuntu, eol
    def sync(self, mode, cache_dir) -> SyncResult                 # online | mirror | offline; ETag/If-Modified-Since
    def lookup(self, component: Component) -> list[Advisory]     # from the local cache only, never online at match time
```

| adapter | source | used for |
|---|---|---|
| `osv` | `osv-vulnerabilities` bucket, per-ecosystem `all.zip` (Debian, Ubuntu, Alpine, npm, PyPI, Go, Maven) | packages and lockfiles, primary |
| `debian` | security-tracker `json` | Debian fixed versions per release (backports) |
| `ubuntu` | Ubuntu OSV/USN data | Ubuntu fixed versions |
| `nvd` | NVD 2.0 API (`nvd-api-key`), incremental by `lastModStartDate` | CPE-only software: nginx, GitLab, MongoDB, Redis, MinIO; CVSS |
| `kev` | CISA KEV JSON | known exploited flag |
| `epss` | FIRST EPSS daily CSV | exploit probability |
| `eol` | endoflife.date API | runtime/OS end-of-life |

Details and matching rules: CVE.md.

## 11. How an adapter is chosen and tested

- `get("chat")` reads `CHAT_ADAPTER`; unknown name → `CONFIG ERROR` at `argus-config check`.
- Every adapter ships with a fake server in `tests/fakes/` (pattern: `tests/fake_jira.py`,
  `tests/fake_outline.py`) and a contract test suite per kind (`tests/contract/test_<kind>.py`) that every
  adapter of that kind must pass. A new vendor is accepted when the contract suite passes against its
  fake and once against a real sandbox.
