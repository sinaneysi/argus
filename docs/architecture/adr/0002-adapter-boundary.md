# ADR-0002: The adapter boundary

Status: proposed, 2026-10-22.

## Context
Integrations: monitoring (Grafana proxy / Prometheus / Loki, Alertmanager via Matrix text), chat
(Matrix), tracker (Jira Server), SCM/CI (GitLab), SSO (Keycloak via OIDC), docs (Outline), object storage
(MinIO `mc`), vulnerability feeds (new). Vendor code is spread over call sites (`matrix.py`,
`jira_client.py`, `gitlab_ro.py`, `outline_client.py`, `obs.sh`); vendor semantics leak (room ids, Jira
status names, GitLab job names, two hand-parsed alert formats). The web app never calls integrations.

## Decision
- One `Protocol` per kind in `bin/argus/adapters/<kind>/`, one module per vendor, chosen by
  `<KIND>_ADAPTER`; `none` gives a null adapter and the feature degrades.
- Read and write are separate interfaces; writers exist only in root dispatchers and the action broker,
  because only they can open the write credentials.
- The model-facing CLI tools (`obs.sh`, `gitlab-ro.sh`, `ssh-ro.sh`, `http-get.sh`) keep their arguments;
  they dispatch to adapters. Prompts and the allowlist do not change per vendor.
- Shared HTTP behaviour (SSRF guard, allowlist, budgets, backoff, ETag, audit, redaction) is one module.
- Alert ingest prefers a structured Alertmanager webhook; Matrix-text parsing stays as an adapter with
  named formats for existing estates.
- OIDC is the only adapter inside the app (both Python and Go).
- Contract tests per kind run against fakes; an adapter is accepted when it passes them.

## Alternatives rejected
- **A plugin system with dynamic loading / entry points:** unnecessary for a handful of vendors; adds a
  supply-chain surface. Vendors are modules in the repository.
- **Moving integration calls into the Go app:** breaks the "app is a renderer" trust boundary (the app
  would need credentials).
- **Third-party SDKs (boto3, slack_sdk, jira):** violate the stdlib-only rule of root-run code.

## Consequences
- Slack, GitHub, Jira Cloud, plain S3 become one module plus a fake each.
- The Matrix-specific features (E2EE via pantalaimon, DM intake) stay Matrix-only until another chat
  adapter implements `ChatDM`; the feature toggle hides them otherwise.
