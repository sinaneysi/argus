# ADR-0001: Adoption by configuration only

Status: proposed, 2026-10-22.

## Context
Argus was built for one estate. 1,613 company-specific literals sit in code, prompts, units and
operator scripts (HARDCODE-AUDIT.md); 14,086 more in `config/` data files. Some integrations are already
configurable (`GRAFANA_<SITE>_*`, `OIDC_*`, `MATRIX_*`, many `CICD_*`), others are not (site list,
environments, CI conventions, Jira statuses, locale). The operator wants another company to adopt Argus
"by changing only the .env".

## Decision
- One non-secret `argus.env` (`KEY=value`), one secret per file under `secrets/` (passed to units with
  `LoadCredential=`), and JSON data files under `data/` for lists (environments, tiers, customer-facing
  names, backup sets). JSON, not YAML: stdlib in Python, no dependency in Go.
- A single `config/schema.json` describes every key (type, default, required-when, validation, secret,
  module). The Python loader, the Go loader, `argus-config check|show|render` and the CONFIG.md tables
  all derive from it.
- One `FEATURE_<MODULE>` toggle per module; a module that is on and misconfigured refuses to start with a
  one-line message naming the key and the file; other modules keep running.
- Existing key names stay canonical. New keys are added only where none exists.
- Data files move out of the product repository to `ARGUS_DATA_DIR`; `config/examples/` ships Acme
  examples.
- Prompts get company facts from config/data at run time (a rendered preamble), never in their text.

## Alternatives rejected
- **One big YAML/TOML file:** nicer for lists, but needs a parser dependency on both sides and breaks
  `EnvironmentFile=` reuse in units.
- **Database-stored settings for everything:** hides configuration from review and from the state backup
  diff; the admin page keeps doing it only for the few runtime knobs it already owns.
- **Per-company fork / branch:** the current state; every fix must be cherry-picked; rejected.

## Consequences
- Each literal replacement is a small, testable change with today's value as the Northwind setting.
- `argus-config check` becomes the first `ExecStartPre` of every unit — a typo stops one module loudly
  instead of silently changing behaviour.
- Docs tables can be generated and cannot drift from the schema.
