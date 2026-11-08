# Argus demo — Northwind Commerce (sample data)

A click-through demo of Argus on a **fictional** company, Northwind Commerce (a web shop with payments and a
warehouse). Every name, host, address, person and number is invented: hosts on RFC 1918 networks, domains under
`northwind.example` (RFC 2606), people such as Alex Doe and Sam Roe, Jira project `OPS`, Matrix rooms `#qa` and
`#alerts`. No collector, no model and no integration runs: the AI outputs are stored sample text.

## Run it

```sh
docker compose -f demo/compose.yml up --build            # open http://127.0.0.1:3000 (web; the API behind it)
DEMO_WEB_PORT=3091 DEMO_PORT=8091 docker compose -f demo/compose.yml up      # other ports
# a host whose VPN client blocks Docker's bridge network:
docker compose -f demo/compose.yml -f demo/compose.host.yml up --build
```

Sign in with the buttons on the login page:

| button | user | password (throwaway) | can see |
|---|---|---|---|
| Sign in as viewer (demo) | `demo.viewer` | `northwind-demo-2026` | every page except the admin ones |
| Sign in as admin (demo) | `demo.admin` | `northwind-demo-2026` | everything, including Settings, Review inbox, Fix runs |

**Reset:** `docker compose -f demo/compose.yml down -v && docker compose -f demo/compose.yml up` (data and sessions
are regenerated). The data container regenerates the data every 6 hours and refreshes the live files (alert
snapshot, VM pulse, CI/CD watcher) every minute, so every timestamp stays relative to now.

## What is different from a real installation (`DEMO_MODE=1`, config only, off by default)

- A sticky banner "Demo · sample data, not a real company" on every page; one-click demo sign-in.
- The web app (`apps/web`) serves sign-in, Today and the Review inbox and proxies every other page to the API's Go
  pages (ADR-0018); the public demo's edge routes them directly (docs/DEPLOY-DEMO.md).
- **Every write is refused server-side:** any POST except sign-in, sign-out and the theme switch answers "Disabled in
  the demo" (the JSON API: 403 `{"error":"Disabled in the demo"}`) before a handler runs; nothing is written. The data volume is mounted read-only.
- **No outbound network:** single sign-on is never configured (Keycloak hidden), and the app has no other client.
- The demo binary is built with `-tags demo`; the compiled-in defaults are neutral examples in every build
  (`apps/api/internal/app/site_defaults.go`), and the demo's names come from `demo/compose.yml`.
- The data container has no network at all (`network_mode: none`); the app image is `scratch` with no CA bundle.

## Files

| file | what |
|---|---|
| `../apps/api/internal/demo` | the generator, `argus demo generate` (Go, deterministic: `-seed`, `-now`); writes ops.db, board.db, auth.db and the state files. Its `data/` holds the product schemas and the sample RCA reports it embeds |
| `Dockerfile`, `compose.yml`, `compose.host.yml` | three images, all uid 65534 and read-only: `argus:web` (Next.js standalone on Node), `argus:api` (scratch, the Go JSON API) and `argus:data` (scratch, no network) |
| `parity.sh` | development check: compares `argus demo generate` with the original Python generator from the git history |
| `smoke.py` | development tool (Python, not shipped): signs in as the demo admin without JavaScript, renders Today and Review, checks the CSP nonce and the demo refusal |
| `layout-sweep.mjs` | development tool (Node, not shipped): signs in as the demo admin and checks every page at four widths, sidebar open and collapsed, for covered, squeezed or overflowing text, sideways scroll and anything past the sidebar edge (DESIGN.md §10); exit 1 on any finding |
| `crawl.py` | development tool (Python, not shipped): crawl every route as viewer and admin (GET only) and print a status summary |

Generate the data without Docker: `cd apps/api && go run ./cmd/argus demo generate -out /tmp/demo/data -auth /tmp/demo/auth`.
The generator was ported from Python to Go with identical output (`parity.sh`); nothing Python is shipped.

## Leak check

Before every publish the images, the data volumes and the HTML of every page a viewer and an admin reach are scanned
for data of the company Argus was first built at (names, domains, networks, people, issue keys, Persian text). The
scanner holds that blocklist, so it is kept out of this repository; the result is recorded in
[docs/DEPLOY-DEMO.md](../docs/DEPLOY-DEMO.md).

## Hosting it publicly

One small VM is enough: 1 vCPU, 1 GB RAM, 10 GB disk (the app uses about 30 MB RAM, the data about 5 MB). Put a
reverse proxy with TLS in front (Caddy or nginx) that sets `X-Real-IP` (the sign-in lockout is per user and IP) and
forwards to `127.0.0.1:8080`; the session cookie is `Secure`, so the demo must be served over HTTPS. Keep the
containers as in `compose.yml` (read-only, no capabilities). The public instance: [docs/DEPLOY-DEMO.md](../docs/DEPLOY-DEMO.md).

## Clean-install test (ADR-0021 D8)

`python3 -I demo/clean-install/run.py` builds `deploy/compose.yml`, starts it with an EMPTY volume and fake GitLab,
Jira, Alertmanager and Matrix (`demo/clean-install/stubs.py`, synthetic data), signs in as the first admin, crawls
every page and API GET endpoint, submits every safe action and classifies each as works / not-configured / broken.
It fails only on a regression against `docs/CLEAN-INSTALL.md` (the backlog); `--write` updates that file after a fix.
Needs Docker and about three minutes; the project `argus-clean` and its volume are removed at the end.
