# ADR-0018: Monorepo, a Next.js frontend and a Go JSON API

Status: accepted, 2026-11-03. Supersedes ADR-0016 D2 and OD-1 (server-rendered Go pages, no Next.js) for the
user interface. Everything else in ADR-0016 stays.

## Context

The operator decided on 2026-11-03 to stop rendering pages in Go: "make Argus a monorepo, separate front and
backend, use Next for the front, don't use Go for the front, do not over-engineer". The Go code holds the data
access, the access policy and the collectors; those are worth keeping. The page templates are what changes.

## Decision

One repository, npm workspaces for the JavaScript side, one Go module for the backend.

```
apps/web/         Next.js (App Router, TypeScript strict): every page
apps/api/         the Go module (module path unchanged): cmd/argus-api = JSON API server, cmd/argus = CLI/worker
packages/design/  design tokens (CSS custom properties, light/dark), fonts, marks, DESIGN.md; consumed by apps/web
demo/             demo images and compose: web + api + data
docs/             architecture and ADRs
```

- **One Go API service.** The data functions stay where they are; `/api/v1/...` handlers sit next to the old page
  handlers and reuse them. No second backend until one is measured to be needed.
- **Auth.** The Go API owns sessions. Login keeps today's steps (password, then TOTP); the session cookie stays
  `HttpOnly; Secure; SameSite=Lax`. CSRF is a double submit: a readable `argus_csrf` cookie echoed in the
  `X-CSRF-Token` header (or a `csrf` form field) on every write; the session's own token (`GET /api/v1/session`)
  is accepted as well, for API clients. `GET /api/v1/me` returns the user, role and
  permissions. The same policy table (`POST_ROUTES`: role, MFA, demo refusal) guards API writes; in the demo a
  write answers `403 {"error":"Disabled in the demo"}`.
- **Next renders on the server.** Server components call the API with the visitor's cookie and address forwarded
  (`API_URL`, never exposed to the browser). Client components only where interaction needs them (none so far).
  Forms are server actions, which forward to the API with the CSRF header and hand its cookies back; they work
  without client JavaScript (progressive enhancement), and Next checks their Origin.
- **Security rules stay.** CSP with a per-request nonce (`script-src 'self' 'nonce-…'`), `frame-ancestors 'none'`,
  no inline event handlers, no third-party origins, no secrets in client bundles, self-hosted fonts.
- **Routing during migration: one origin.** The edge sends `/api/*` to the API, ported pages and `/_next/*` to
  web, and everything else to the old Go pages until they are ported. The web app also proxies any path it has no
  route for to the API (a Next fallback rewrite), so one port serves the whole product locally.

## Migration order

1. Move the Go module into `apps/api` with `git mv` (history kept). No behaviour change.
2. JSON API for auth, navigation counts, Today, Review, data status; Next pages for login, the shell, Today and
   Review. Demo runs web + api + data.
3. Port the remaining sections one at a time (Incidents, Infrastructure, Delivery, Work, Agent, Settings), each
   with its API endpoints, then delete the Go templates and the old routes.

Done 2026-11-04: every page is Next.js and Go renders none. The Go app serves `/api/*`, `/auth/*` (a redirect in every
outcome, the reason goes to `/login` as a code), `/static/*` (fonts and icons), `/healthz` and the downloads
`/inventory.csv`, `/cicd/summary.json`, `/rca/prompt.md`, `/ideas/prompt.md`. Some data functions still build an HTML
fragment that the API reads back as text: a later clean-up, no page depends on it.

## Rejected

- **Micro-frontends / several web apps:** one team, one product; the cost buys nothing.
- **Several backends now:** the data layer is shared; splitting it would duplicate the policy table.
- **Turborepo / Nx:** npm workspaces are enough for two apps and one package.
- **Client-side SPA:** server rendering keeps the strict CSP and works without client JavaScript.
