# Argus

Argus is an unattended DevOps assistant. It collects evidence deterministically (alerts, logs, metrics, CI
pipelines, backups, certificates, CVE feeds, read-only host checks), asks a language model to explain it, and gives
people **one page per problem with the exact fix**. It is read-only by default; anything that changes a system goes
through a human approval first.

**Live demo:** <https://argus.archidev.ir> (synthetic data of a fictional company, one-click demo users,
every write refused).

> Status: early. This repository holds the Go web app, the design system and the public demo. The evidence
> collectors and dispatchers are being ported to Go (see [docs/ROADMAP.md](docs/ROADMAP.md) and
> [ADR-0016](docs/architecture/adr/0016-argus-2-platform.md)); until then the app renders the data a collector
> writes into its SQLite databases and state files, and the demo generator provides that data.

## What it shows

- **Today:** one list of what needs a human now, across every source.
- **Incidents and RCA:** an alert that stays firing becomes an incident page with the evidence, a root-cause
  analysis and a fix prompt.
- **CI/CD:** pipeline watch, daily summaries, triage of failed jobs, repository checks.
- **Backups:** newest object, age and size of every backup set; backup plans.
- **Certificates, CVE, capacity and inventory.**
- **Board and tasks:** a work board, proposals that a human accepts or rejects, and an approval queue for actions.

## Layout

| path | what |
|---|---|
| `apps/api/cmd/argus-api` | the JSON API (`argus-api`, also `healthcheck`, `user ...`, `mfa ...` subcommands) |
| `apps/api/cmd/argus` | the multi-call binary of Argus 2: `version`, `config check`, `demo generate`, `demo ready` |
| `apps/api/internal/config` | the configuration loader (`ARGUS_*` keys, old `OPS_*` names as aliases) |
| `apps/api/internal/demo` | the demo data generator (fictional Northwind Commerce) |
| `apps/api/internal/app` | pages, view models, the demo mode (`demo.go`), site configuration (`site.go`, `site_defaults.go`) |
| `apps/api/internal/auth`, `apps/api/internal/oidc` | local accounts, sessions, TOTP 2FA, OpenID Connect sign-in |
| `apps/api/internal/brand` | the product name in one place |
| `apps/web` | the Next.js frontend (App Router, TypeScript): sign-in, the shell, Today, Review inbox; reads `apps/api`'s JSON API |
| `packages/design` | the design system (`DESIGN_SYSTEM.md`, `DESIGN.md`) and the static assets (CSS, fonts, icons) |
| `demo/` | the public demo: Dockerfile, compose, crawler and the parity check of the generator |
| `apps/api/config/` | `argus.example.env`, `schema.json`, a commented example configuration |
| `apps/api/examples/` | systemd units for the CLI/worker jobs |
| `docs/` | architecture, ADRs, roadmap, demo deployment |
| `apps/api/vendor/` | vendored Go modules (licences in `THIRD_PARTY_LICENSES`) |

## Security model

- **Read-only by default.** Collectors use read-only credentials; the web app is a renderer over local data.
- **Strict CSP.** Pages are rendered on the server (Next.js server components); scripts
  only from the origin with a per-request nonce, no inline handlers or styles, `frame-ancestors 'none'`; forms work
  without client JavaScript.
- **Writes need a human.** Any change is proposed, shown with its rollback, and runs only after a person approves it.
- **Secrets stay out of the tree.** Secrets are read from one file each; the admin page shows only whether a secret
  works, never its value.

See `docs/architecture/ACCESS.md` and `docs/architecture/OVERVIEW.md` for the design.

## Quickstart (demo, synthetic data)

```sh
docker compose -f demo/compose.yml up --build -d
# open http://127.0.0.1:3000 and use "Sign in as viewer" or "Sign in as admin"
```

Details in [demo/README.md](demo/README.md); the public instance is deployed as in
[docs/DEPLOY-DEMO.md](docs/DEPLOY-DEMO.md).

## Building

```sh
cd apps/api
go vet ./...
go test ./...
go build -o argus-api ./cmd/argus-api              # normal build
go build -tags demo -o argus-api ./cmd/argus-api   # demo build (DEMO_MODE support compiled in)
go build -o argus ./cmd/argus                      # the multi-call binary (argus -h)
```

The web app (Node 20+, npm workspaces from the repository root):

```sh
npm ci
npm run lint && npm run typecheck && npm test && npm run build
API_URL=http://127.0.0.1:8080 npm run dev      # http://127.0.0.1:3000, against a running argus-api
```

The Go module lives in `apps/api` (see [ADR-0018](docs/architecture/adr/0018-monorepo-next-go-api.md)).
Go 1.22; modules are vendored (`GOFLAGS=-mod=vendor` is the default with a `vendor/` directory). Refresh them with
`apps/api/vendor.sh` after a `go.mod` change.

## Configuration

Argus is adopted by configuration only: every company-specific value (company name, GitLab URL, chat server,
time zone, environments, sites) comes from environment variables, with neutral defaults in
`apps/api/internal/app/site_defaults.go`. `demo/compose.yml` is a complete example; `docs/architecture/CONFIG.md` describes
the schema. `argus config check` validates a configuration (the file `/etc/argus/argus.env`, see
`apps/api/config/argus.example.env`, plus the environment) and prints it with secrets masked.

## Contributing and security

- How to contribute: [CONTRIBUTING.md](CONTRIBUTING.md).
- How to report a vulnerability: [SECURITY.md](SECURITY.md).

## Licence

Apache License 2.0, see [LICENSE](LICENSE) and [NOTICE](NOTICE).
