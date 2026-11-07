# Deploying the public demo

The public demo at <https://argus.archidev.ir> runs `demo/compose.yml` on one small Ubuntu 24.04 VM. The VM has no
direct internet access, so images are built elsewhere and copied over. A TLS reverse proxy (Caddy) on the VM
terminates HTTPS for the name and routes by path to the two services (ADR-0018: one origin during the migration).

| item | value |
|---|---|
| directory | `/opt/argus` (`compose.yml` = `demo/compose.yml`) |
| compose project | `argus` (containers `argus-web-1`, `argus-api-1`, `argus-data-1`; volumes `argus_demo-data`, `argus_demo-auth`; network `argus_argus` 172.29.240.0/24) |
| images | `argus:web` (Next.js standalone on `node:20-bookworm-slim`, uid 65534), `argus:api` (scratch, the Go JSON API, uid 65534), `argus:data` (scratch, the `argus` binary, uid 65534, `network_mode: none`) |
| listen | web `127.0.0.1:3000`, api `127.0.0.1:8080` only |
| hardening | `read_only` (web: tmpfs for `/tmp` and `.next/cache`), `cap_drop: [ALL]`, `no-new-privileges`, memory and pid limits, data volume read-only in the api |
| demo users | `demo.viewer` / `demo.admin`, throwaway password on the sign-in page (`DEMO_PASSWORD`) |

The demo data comes from the generator (`argus demo generate`), not from the collectors. The data container has no
network, so `argus checks` stays off there (every `ARGUS_COLLECT_*` flag is 0 by default).

## Routing (Caddy, `/etc/caddy/Caddyfile`)

Next.js serves every page and everything unknown (its 404). The API serves `/api/*`, `/auth/*`, `/static/*` (fonts and
favicons, also used by the web app's CSS), and the downloads. Go renders no page; a POST outside `/api/*` never reaches it.

```
https://argus.archidev.ir {
	tls /etc/caddy/certs/wild-fullchain.pem /etc/caddy/certs/wild-privkey.pem
	header Strict-Transport-Security "max-age=31536000"
	@api path /api/* /auth/* /static/* /favicon.ico /apple-touch-icon.png /inventory.csv /cicd/summary.json /rca/prompt.md /ideas/prompt.md
	handle @api {
		reverse_proxy 127.0.0.1:8080 {
			header_up X-Real-IP {remote_host}
		}
	}
	handle {
		reverse_proxy 127.0.0.1:3000 {
			header_up X-Real-IP {remote_host}
		}
	}
}
```

The API trusts `X-Real-IP` only
from `ARGUS_TRUSTED_PROXIES` (loopback, the bridge gateway the edge arrives from, and web's fixed address); web
forwards the visitor's address to the API so the sign-in lockout counts per visitor. Change it like this:

```sh
cp /etc/caddy/Caddyfile /root/Caddyfile.bak-$(date +%Y%m%d-%H%M)
$EDITOR /etc/caddy/Caddyfile
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && systemctl reload caddy
# rollback: cp /root/Caddyfile.bak-<ts> /etc/caddy/Caddyfile && systemctl reload caddy
```

## Build and ship

```sh
# on a build machine, from the repository root
(cd apps/api && go vet ./... && go test ./... && go test -tags demo ./...)
npm ci && npm run lint && npm run typecheck && npm test && npm run build
docker build -f demo/Dockerfile --target api  -t argus:api  .
docker build -f demo/Dockerfile --target web  -t argus:web  .   # needs npm: add --network host behind a VPN that blocks the bridge, or --build-arg NPM_REGISTRY=<mirror>
docker build -f demo/Dockerfile --target data -t argus:data .
docker save argus:api argus:web argus:data | gzip > argus-images.tar.gz
scp argus-images.tar.gz demo/compose.yml root@DEMO_HOST:/root/
```

## Install or update on the VM

```sh
TS=$(date +%Y%m%d-%H%M)
for i in api web data; do docker image inspect argus:$i >/dev/null 2>&1 && docker tag argus:$i argus:$i-prev; done
docker load < /root/argus-images.tar.gz
cp /opt/argus/compose.yml /opt/argus/compose.yml.bak-$TS
install -m 0644 /root/compose.yml /opt/argus/compose.yml
cd /opt/argus && docker compose -p argus -f /opt/argus/compose.yml up -d --no-build --wait --remove-orphans
curl -fsS -o /dev/null http://127.0.0.1:3000/login && curl -fsS -o /dev/null http://127.0.0.1:8080/healthz
rm -f /root/argus-images.tar.gz /root/compose.yml
```

**Rollback:** restore the Caddyfile backup (above), `cp /opt/argus/compose.yml.bak-<ts> /opt/argus/compose.yml`,
`for i in api web data; do docker tag argus:$i-prev argus:$i; done` (before 2026-11-03 the API image was `argus:app`:
`docker tag argus:app-prev argus:app`), then `docker compose -p argus -f /opt/argus/compose.yml up -d --no-build
--force-recreate --remove-orphans`.

## Verify

```sh
python3 -I demo/smoke.py https://argus.archidev.ir    # the checks below, without a browser; or by hand:
curl -s https://argus.archidev.ir/login | grep -c /_next/                     # > 0: served by Next.js
curl -sI https://argus.archidev.ir/login | grep -i content-security-policy     # script-src 'self' 'nonce-...'
# sign in with the demo admin button -> 303 to /today; /today and /review render (Needs you, the queue);
# a Review act -> the "Disabled in the demo" banner; POST /api/v1/review/act -> 403 {"error":"Disabled in the demo"}
curl -s -o /dev/null -w '%{http_code}\n' https://argus.archidev.ir/account  # 307 to /login: served by Next
docker ps --format '{{.Names}} {{.Status}}' | grep argus                       # three containers healthy
V=$(docker volume inspect argus_demo-data --format '{{.Mountpoint}}')
grep -o '"fetched_at": [0-9]*' $V/alerts-firing.json                          # moves every minute (live refresh)
```
On the VM itself, add `--resolve argus.archidev.ir:443:127.0.0.1` to curl.

## Deploy log

- 2026-11-04: Go serves no pages (ADR-0018 done). Last pages removed (PR #69, `main` `8b21c49`, backups `...-0802`, 243 of 243), then no fallback to Go (PR #70): images `argus:api`, `argus:web` and `argus:data` built from `main` `a11a630`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0807`); previous images tagged `-prev`. Caddy now lists the API's paths in `@api` and sends everything else to web (backup of the old `@web` routing `/root/Caddyfile.bak-20261104-0806`).
  Checked with `demo/smoke.py https://argus.archidev.ir`, 248 of 248. The first smoke run stopped on a DNS failure on the build machine after 42 checks; the repeat passed.
- 2026-11-04: Infrastructure, Delivery, Work and Agent pages removed from Go (PRs #67, #68). Images built from `main`; `compose.yml` and Caddy unchanged; previous images tagged `-prev`. Checked with `demo/smoke.py https://argus.archidev.ir`, 243 of 243.
- 2026-11-04: Home and Incidents pages removed from Go (PR #66); old Go POST routes removed (PR #65, TS 0710). Images built from `main`; `compose.yml` and Caddy unchanged; previous images tagged `-prev`. Checked with `demo/smoke.py https://argus.archidev.ir`.
- 2026-11-04: write handlers answer plain refusals (PR #64); images `argus:api`, `argus:web` and `argus:data` built from `main` `1fdc842`.
  `compose.yml` and Caddy unchanged (backups `/opt/argus/compose.yml.bak-20261104-0650`, `/root/Caddyfile.bak-20261104-0650`); previous images tagged `-prev`. Checked with `demo/smoke.py https://argus.archidev.ir`, 243 of 243.
- 2026-11-04: Data sources catalogue page on Next.js (PR #62); images `argus:api`, `argus:web` and `argus:data` built from `main` `fac6866`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0629`); previous images tagged `-prev`. Caddy now also routes `/admin/sources/catalogue` to web
  (backup `/root/Caddyfile.bak-20261104-0629`). Checked with `demo/smoke.py https://argus.archidev.ir`, 243 of 243. Every page is now served by Next; the Go pages are still in the image.
- 2026-11-04: Account, Two-factor and login-time enrolment on Next.js (PR #61); images `argus:api`, `argus:web` and `argus:data` built from `main` `03bfe1d`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0609`); previous images tagged `-prev`. Caddy now also routes `/account` and `/account/2fa` to web
  (backup `/root/Caddyfile.bak-20261104-0609`). Checked with `demo/smoke.py https://argus.archidev.ir`, 240 of 240.
- 2026-11-04: Settings Integrations on Next.js (PR #60); images `argus:api`, `argus:web` and `argus:data` built from `main` `0f82600`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0547`); previous images tagged `-prev`. Caddy now also routes `/admin/outline` and `/admin/outline/page` to web
  (backup `/root/Caddyfile.bak-20261104-0547`). Checked with `demo/smoke.py https://argus.archidev.ir`, 229 of 229.
- 2026-11-04: Settings Backup targets and Credentials on Next.js (PR #59); images `argus:api`, `argus:web` and `argus:data` built from `main` `f926092`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0531`); previous images tagged `-prev`. Caddy now also routes `/admin/backup-targets` and `/admin/credentials` to web
  (backup `/root/Caddyfile.bak-20261104-0531`). Checked with `demo/smoke.py https://argus.archidev.ir`, 223 of 223.
- 2026-11-04: Settings Data sources on Next.js (PR #57); images `argus:api`, `argus:web` and `argus:data` built from `main` `9d242e1`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0501`); previous images tagged `-prev`. Caddy now also routes `/admin/sources` to web
  (backup `/root/Caddyfile.bak-20261104-0501`). Checked with `demo/smoke.py https://argus.archidev.ir`, 217 of 217.
- 2026-11-04: Settings Kubernetes on Next.js (PR #56); images `argus:api`, `argus:web` and `argus:data` built from `main` `f0988fe`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0452`); previous images tagged `-prev`. Caddy now also routes `/admin/kube` to web
  (backup `/root/Caddyfile.bak-20261104-0452`). Checked with `demo/smoke.py https://argus.archidev.ir`, 209 of 209.
- 2026-11-04: Settings Claude on Next.js (PR #55); images `argus:api`, `argus:web` and `argus:data` built from `main` `4d27eab`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0440`); previous images tagged `-prev`. Caddy now also routes `/admin/claude` to web
  (backup `/root/Caddyfile.bak-20261104-0440`). Checked with `demo/smoke.py https://argus.archidev.ir`, 202 of 203: the failure was the smoke sending a redirect's
  `#fragment` in the request path; with that fixed, the check passes on the live demo.
- 2026-11-04: Settings Jira sync and the "?" help link on Next.js (PR #54); images `argus:api`, `argus:web` and `argus:data` built from `main` `f2ebb9e`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0426`); previous images tagged `-prev`. Caddy now also routes `/admin/board` to web (backup `/root/Caddyfile.bak-20261104-0426`). Checked with `demo/smoke.py https://argus.archidev.ir`, 194 of 195:
  the one failure was the smoke's own form lookup for the hosts text area, fixed in the next PR.
- 2026-11-04: Settings General and Users and access on Next.js (PR #52); images `argus:api`, `argus:web` and `argus:data` built from `main` `69acae7`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0400`); previous images tagged `-prev`. Caddy now also routes `/admin` and `/admin/users` to web
  (backup `/root/Caddyfile.bak-20261104-0400`). Checked with `demo/smoke.py https://argus.archidev.ir`, 185 of 185.
- 2026-11-04: Review's full lists of lessons, links and chat requests on Next.js (PR #51); images `argus:api`, `argus:web` and `argus:data` built from `main` `fd5bd97`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0349`); previous images tagged `-prev`. Caddy's `@goreview` is gone: no `/review` URL is a Go page
  (backup `/root/Caddyfile.bak-20261104-0349`). Checked with `demo/smoke.py https://argus.archidev.ir`, 175 of 175.
- 2026-11-04: Help on Next.js (PR #50): index, search, every page and the printable version; images `argus:api`, `argus:web` and `argus:data` built from `main` `03a1ebe`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0327`); previous images tagged `-prev`. Caddy now also routes `/help` and `/help/*` to web
  (backup `/root/Caddyfile.bak-20261104-0327`). Checked with `demo/smoke.py https://argus.archidev.ir`, 163 of 163.
- 2026-11-04: Review's full lists of ideas and proposed tasks on Next.js (PR #47) and the sticky bulk bar (PR #48); images `argus:api`, `argus:web` and `argus:data` built from `main` `32b846c`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0304`); previous images tagged `-prev`. Caddy's `@goreview` now skips `kind=ideas` and `kind=proposals`
  (backup `/root/Caddyfile.bak-20261104-0304`). Checked with `demo/smoke.py https://argus.archidev.ir`, 146 of 146.
- 2026-11-04: the Agent section on Next.js (PR #45): runs, one run, Knowledge, Fix runs and Take action; images `argus:api`, `argus:web` and `argus:data` built from `main` `f6a7119`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0248`); previous images tagged `-prev`. Caddy now also routes `/runs`, `/runs/view`, `/knowledge`, `/admin/fix`,
  `/admin/fix/run` and `/admin/fix/review` to web (backup `/root/Caddyfile.bak-20261104-0248`). Checked with `demo/smoke.py https://argus.archidev.ir`, 131 of 131.
- 2026-11-04: Conversations and Ask Argus on Next.js (PR #43), which finishes the Work section; images `argus:api`, `argus:web` and `argus:data` built from `main` `56703f3`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0208`); previous images tagged `-prev`. Caddy now also routes `/board/ask` and `/qa` to web
  (backup `/root/Caddyfile.bak-20261104-0208`). Checked with `demo/smoke.py https://argus.archidev.ir`, 113 of 113.
- 2026-11-04: the Work section's board, task page and New task on Next.js (PR #42), images `argus:api`, `argus:web` and `argus:data` built from `main` `bf39cdf`.
  `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0152`); previous images tagged `-prev`. Caddy now also routes `/board`, `/board/task` and
  `/board/new` to web (backup `/root/Caddyfile.bak-20261104-0152`). Checked with `demo/smoke.py https://argus.archidev.ir`, 107 of 107.
- 2026-11-04: the Delivery section on Next.js (PR #40, ADR-0018 step 3) and the step-up page, images `argus:api`, `argus:web` and `argus:data`
  built from `main` `ad10c8d`. `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0109`); previous images tagged
  `argus:api-prev`, `argus:web-prev`, `argus:data-prev`. Caddy now also routes `/cicd`, `/cicd/failures`, `/cicd/stuck`, `/cicd/audit`,
  `/cicd/project`, `/cicd/finding`, `/cicd/repo`, `/vulns` and `/account/confirm` to web (backup `/root/Caddyfile.bak-20261104-0109`).
  Checked with `demo/smoke.py https://argus.archidev.ir`, 83 of 83:
  - Every ported page is served by Next with a CSP nonce and real demo data.
  - Re-check and Suppress show "Disabled in the demo"; `POST /api/v1/cicd/repo/check`, `/vulns/suppress` and `/account/stepup` answer 403.
  - `/cicd/summary.json` is still the API; `/board`, `/runs` and `/knowledge` are still the Go pages. All three containers are healthy.
  - By hand: signed out, the pages redirect to `/login`; a viewer gets no suppress form; the old `POST /cicd/repo/check` and `/vulns/suppress` answer 403.
- 2026-11-04: the Infrastructure section on Next.js (PR #38, ADR-0018 step 3), images `argus:api`, `argus:web` and `argus:data`
  built from `main` `6b52e2c`. `compose.yml` unchanged (backup `/opt/argus/compose.yml.bak-20261104-0017`); previous images
  tagged `argus:api-prev`, `argus:web-prev`, `argus:data-prev`. Caddy now also routes `/inventory`, `/inventory/view`,
  `/capacity`, `/certs`, `/backups`, `/backups/set`, `/backups/new`, `/backups/plan`, `/operations` and `/hygiene` to web (backup
  `/root/Caddyfile.bak-20261104-0017`). Checked with `demo/smoke.py https://argus.archidev.ir`, 66 of 66:
  - Every ported page is served by Next with a CSP nonce and real demo data; the search goes straight to one matching VM.
  - Scan all and a backup test show "Disabled in the demo", and the test does not echo the string; the eight new `POST /api/v1/...` writes answer 403.
  - `/inventory.csv` still downloads; `/cicd`, `/vulns`, `/board`, `/runs` and `/knowledge` are still the Go pages. All three containers are healthy.
  - By hand: signed out, the pages redirect to `/login`; a viewer gets Refresh but no Analyse, no Mark as OK, no storage Analyse and no Test connection; the old `POST /scan`, `/inventory/refresh`, `/capacity/analyse` and `/backups/override` answer 403.
- 2026-11-03: the Incidents section on Next.js (PR #35, ADR-0018 step 3), images `argus:api`, `argus:web` and `argus:data`
  built from `main` `bdc33fb` (the generator is unchanged). `compose.yml` unchanged (backup
  `/opt/argus/compose.yml.bak-20261103-2328`); previous images tagged `argus:api-prev`, `argus:web-prev`, `argus:data-prev`.
  Caddy now also routes `/incidents`, `/incidents/view`, `/incidents/run`, `/incidents/problem`, `/incidents/rca`,
  `/incidents/problems`, `/alerts`, `/problems`, `/problems/view`, `/rca` and `/rca/view` to web (backup
  `/root/Caddyfile.bak-20261103-2328`). Checked with `demo/smoke.py https://argus.archidev.ir`, 40 of 40:
  - Every ported page and `/today` (Open incidents, Checks, last 48 h) is served by Next with a CSP nonce and real demo data.
  - An incident act shows "Disabled in the demo"; `POST /api/v1/incidents/act`, `/incidents/deep` and `/problems/act` answer 403.
  - `/inventory`, `/cicd`, `/board` and `/runs` are still the Go pages. All three containers are healthy.
  - By hand: signed out, the pages redirect to `/login`; a viewer gets no bulk bar, Resolve or Investigate deeply;
    `/rca/prompt.md` downloads; the old `POST /incidents/action`, `/incidents/deep` and `/problems/act` answer 403.

- 2026-11-03: monorepo with a Next.js frontend on a Go JSON API (PRs #27, #33; ADR-0018). Images `argus:web` (new,
  347 MB on disk, about 80 MB compressed), `argus:api` (was `argus:app`) and `argus:data`, built from `main` `45bbc83`.
  `compose.yml` replaced (three services and the network `argus_argus` 172.29.240.0/24; backup
  `/opt/argus/compose.yml.bak-20261103-2055`). The old images were tagged `argus:app-prev` and `argus:data-prev`.
  Caddy now routes `/`, `/login`, `/today`, `/review` and `/_next/*` to web on `127.0.0.1:3000`, routes
  `/review?mode=full` and everything else to the API on `127.0.0.1:8080`, and sets `X-Real-IP` (backup
  `/root/Caddyfile.bak-20261103-2057`). Checked with `demo/smoke.py https://argus.archidev.ir`:
  - `/login` is served by Next and carries `/_next/` assets.
  - The CSP has a per-request nonce and `frame-ancestors 'none'`, every script carries the nonce, and there are no inline styles or handlers.
  - Demo admin sign-in answers 303 to `/today`.
  - `/today` (Needs you, the plan) and `/review` (the queue and the panel) render from real demo data.
  - A Review act shows "Disabled in the demo", and `POST /api/v1/review/act` answers 403 `{"error":"Disabled in the demo"}`.
  - `/incidents` and `/review?mode=full` are still the Go pages.
  - The API logs the visitor's address on sign-in.
  - All three containers are healthy.

- 2026-11-03: design tokens, mark and favicons (Calm console, PR #29), app image built from `main` `56d6ca9`
  (also carries the monorepo move, PR #27, and the Data sources catalogue as `/api/v1` JSON with the Go-rendered
  catalogue page dropped, PR #28); the data image is unchanged. `compose.yml` unchanged; previous images tagged
  `argus:app-prev` / `argus:data-prev`. Checked: `/login` 200 "Log in · Argus"; demo admin and viewer sign-in; every
  sidebar page 200 (25 admin, 24 viewer); `POST /board/move` 403 "Disabled in the demo"; `/static/favicon.svg`,
  `favicon-32.png`, `apple-touch-icon.png` and `/favicon.ico` 200 and identical to the repository; both containers
  healthy.
- 2026-11-03: Data sources P2: discovery, routing catalogue, Settings → Data sources → Catalogue (PR #25, ADR-0017 P2),
  app and data images built from `main` `0a537dd` (the data image adds a synthetic catalogue in `ops.db`: Northwind
  GitLab projects, stage Kubernetes objects and grafana/east Loki/Prometheus/dashboard names run through the real
  matcher; confirmed, proposed, rejected, stale and human-edited routes; discovery runs with one partial and one
  failed). `compose.yml` unchanged; previous images tagged `argus:app-prev` / `argus:data-prev`. Checked: `/login` 200;
  admin demo sign-in 303; `/admin/sources/catalogue` 200 with the status block (Check: elasticsearch/logs), Discovery
  (Partial, Discover now), routes (Confirmed, Proposed, Stale) and Unmapped (ambiguous dashboard); `POST
  /admin/sources/catalogue` 403 "Disabled in the demo"; `/admin/sources` 200; both containers healthy.
- 2026-11-03: Settings → Data sources and `argus sources` (PR #23, ADR-0017 P1), app and data images built from `main`
  `b614b13` (the data image adds a synthetic `sources-status.json`: three Grafanas (read-only, refused, unreachable),
  two GitLabs (read-only agent, tracker), two Kubernetes clusters (one refused), Outline, Sentry (login failed),
  Elasticsearch). `compose.yml` unchanged; previous images tagged `argus:app-prev` / `argus:data-prev`. Checked: admin
  demo sign-in 303; `/admin/sources` 200 with the status block and the per-type lists; `POST /admin/sources` 403
  "Disabled in the demo"; `/static/claude.css` 200; both containers healthy.
- 2026-11-03: Settings → Kubernetes and `argus tools kube` (PR #21), app and data images built from `main` `e21861c`
  (the data image adds a synthetic `kube-status.json`: one read-only cluster the agent may read, one refused because it
  can write). `compose.yml` unchanged; previous images tagged `argus:app-prev` / `argus:data-prev`. Checked: admin demo
  sign-in 303; `/admin/kube` 200 with the status block (Check), `Read-only` and `Can write` panels; `POST /admin/kube`
  403 "Disabled in the demo"; `/static/claude.css` 200; both containers healthy.
- 2026-11-03: task agent live progress, failure block and Plan again (PRs #18, #19), app and data images built from
  `main` `8db43f4` (the data image adds OPS-231, a running planning job kept a few minutes old by the live loop, and
  OPS-211, an intake that failed because Claude rejected the login). `compose.yml` unchanged; previous images tagged
  `argus:app-prev` / `argus:data-prev`. Checked: admin demo sign-in 303; `/board/task?key=OPS-231` shows the steps,
  "Now: reading Loki logs (east)" and a 10 s refresh; `/board/task?key=OPS-211` shows "Planning failed: Claude
  rejected the login." and Plan again; `POST /board/agent` (replan) 403 "Disabled in the demo"; `/static/board.css`
  200; both containers healthy.
- 2026-11-02: Settings → Claude redesign: one status line, then token, location guard and usage with the settings
  folded (PR #16), app image built from `main` `f9e2f2c` (the data image is unchanged). `compose.yml` unchanged;
  previous images tagged `argus:app-prev` / `argus:data-prev`. Checked: admin demo sign-in 303; `/admin/claude` 200
  with the status block and the three sections, `/static/claude.css` 200; `POST /admin/claude-token`,
  `/admin/claude-egress` and `/admin/claude` 403 "Disabled in the demo"; both containers healthy.
- 2026-11-02: Settings → Claude: Claude token panel and egress country guard (PR #13), app image built from `main`
  `b1f8acf` (the data image is unchanged). `compose.yml` unchanged; previous images tagged `argus:app-prev` /
  `argus:data-prev`. Checked: admin demo sign-in 303; `/admin/claude` 200 with the Claude token and egress guard
  panels; `POST /admin/claude-token` and `POST /admin/claude-egress` 403 "Disabled in the demo"; both containers healthy.
- 2026-11-02: new Argus mark and favicons (an eye whose upper lid peaks like an A; replaces the old S-shaped mark),
  app image built from `main` after the merge (the data image is unchanged). Previous images tagged `argus:app-prev` /
  `argus:data-prev`.
- 2026-11-02: Review inbox redesign: queue and review panel, urgency order, one toolbar, plan review step (PR #10),
  app and data images built from `main` after the merge (the data image adds the review validation cases: a critical
  production proposal with real evidence, a long title, an expired proposal, a production plan v3 with five targets).
  `compose.yml` unchanged; previous images tagged `argus:app-prev` / `argus:data-prev`. Checks: `/review` 200 as admin
  and viewer, the critical production proposal first, filters by URL, `sel=` panel with the queue intact, the plan
  review step with the pinned version, writes 403 "Disabled in the demo", both containers healthy. Local crawl of the
  branch build before the merge: viewer 3999 x 200 and 9 x 403 (admin pages), admin 4008 x 200, no 5xx or 404.

- 2026-10-31: Review "All" is one list across kinds, the longest waiting first (PR #9), app image built from `main`
  after the merge (the data image is unchanged). Previous images tagged `argus:app-prev` / `argus:data-prev`.
- 2026-10-31: sidebar links 36px tall on desktop, the same as the group rows (PR #8), images built from `main`
  after the merge. `compose.yml` unchanged; previous images tagged `argus:app-prev` / `argus:data-prev`.
- 2026-10-30: collapsible sidebar groups (Home, Incidents, Infrastructure, Delivery, Work, Agent) and no section tab
  strips (PR #7), images built from `main` `44cbfb1`. `compose.yml` unchanged (backup `compose.yml.bak-20261030-1226`);
  previous images tagged `argus:app-prev` / `argus:data-prev`. Checked: `/login` 200 "Log in · Argus"; admin demo
  sign-in 303 to `/today`; on `/today`, `/certs`, `/cicd/repo`, `/runs`, `/incidents`, `/board` the six groups in order
  with only the current one `open` and no tab strip; every sidebar link 200 (21 admin, 20 viewer); `POST
  /proposals/act` 403 "Disabled in the demo"; both containers healthy.
- 2026-10-30: information architecture v2 and the removal of SEO and design reviews (PR #5), images built from `main`
  `ff12f72`. `compose.yml` unchanged (backup `compose.yml.bak-20261030-1149`); previous images tagged
  `argus:app-prev` / `argus:data-prev`; the generator rewrote the data on start (no reset needed: the removed tables
  are simply no longer written or read). Checked: `/login` 200 "Log in · Argus"; admin demo sign-in 303 to `/today`;
  `/today`, `/review`, `/operations`, `/incidents`, `/board`, `/cicd`, `/runs` 200; `/proposals`, `/ideas`,
  `/admin/lessons` 301 to Review; `/` 303 to `/today`; `/seo`, `/admin/design` 404; the sidebar is exactly the seven
  sections, no Marketing, SEO or Design reviews; `POST /proposals/act` 403 "Disabled in the demo"; both containers
  healthy, uid 65534, read-only, all capabilities dropped, data container without network.
- 2026-10-30: `argus-web` on the config loader (PR #2) and the first Go collectors (PR #3), images built from `main`
  `ac7057f`. The app image's `ENV` now uses the `ARGUS_*` names; `compose.yml` unchanged. The collectors are in the
  data image's `argus` binary but stay off (no network in the data container). Previous images tagged
  `argus:app-prev` / `argus:data-prev`. Checked: `/login` 200 "Log in · Argus"; admin demo sign-in 303; `/`, `/today`,
  `/alerts`, `/incidents`, `/inventory`, `/capacity`, `/backups`, `/certs`, `/vulns`, `/cicd`, `/board`, `/proposals`,
  `/problems`, `/qa`, `/ideas`, `/rca`, `/admin` 200; `POST /board/move` 403 "Disabled in the demo"; both containers
  healthy, uid 65534, read-only, all capabilities dropped, data container without network; no config warning in the
  app log; live alert file refreshed every minute. Before the deploy, a crawl of 4000 pages gave the same HTML as the
  previous binary apart from timestamps.
- 2026-10-30: Argus 2 step 1 (`argus2/cli-and-go-demo`). `argus:data` is now the Go generator (scratch, 11 MB,
  was the 75 MB Python image); `argus:app` rebuilt from the same tree. `compose.yml` unchanged. The previous images
  are tagged `argus:app-prev` and `argus:data-prev`. Checked: `/login` 200 "Log in · Argus"; admin demo sign-in 303;
  `/`, `/today`, `/incidents`, `/inventory`, `/capacity`, `/backups`, `/certs`, `/vulns`, `/cicd`, `/board`,
  `/proposals`, `/problems`, `/qa`, `/ideas`, `/rca`, `/admin` 200; `POST /board/move` 403 "Disabled in the demo";
  both containers healthy, uid 65534, read-only, all capabilities dropped, data container without network; live
  files refreshed one minute after the start. A local crawl of every route gave the same result as the Python data
  (viewer 1490 x 200 and 10 x 403, admin 1500 x 200). Leak check: 0 hits (repository, generated data, data image).

## Rollback

- To the previous Argus images: `docker tag argus:app-prev argus:app && docker tag argus:data-prev argus:data &&
  docker compose -p argus -f /opt/argus/compose.yml up -d --no-build --force-recreate`.
  After the 2026-10-30 update this restores the Python data image; its data format is the same, so no reset is needed.
- To the demo installation this one replaced: its compose file and images are kept on the VM; stop this project
  (`docker compose -p argus -f /opt/argus/compose.yml down`) and start that one again with its own compose file
  (the exact commands are in the operator's runbook).

## Reset the demo data

`docker compose -p argus -f /opt/argus/compose.yml down -v && docker compose -p argus -f /opt/argus/compose.yml up -d --no-build`
(the generator recreates the data and the demo sessions).
