# Argus public demo (sample data of the fictional Northwind Commerce). Three images from one file:
#   docker build -f demo/Dockerfile --target api  -t argus:api  .    # the Go JSON API (no pages, demo build, scratch
#   docker build -f demo/Dockerfile --target web  -t argus:web  .    # the Next.js frontend (standalone), non-root
#   docker build -f demo/Dockerfile --target data -t argus:data .    # the data generator (argus demo generate), scratch
# Normally through demo/compose.yml. Build context: the repository root (demo/Dockerfile.dockerignore keeps it small).
ARG GO_IMAGE=golang:1.22.12-alpine
ARG NODE_IMAGE=node:20-bookworm-slim

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY apps/api/go.mod apps/api/go.sum ./
COPY apps/api/vendor/ ./vendor/
COPY apps/api/cmd/ ./cmd/
COPY apps/api/internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor GOTOOLCHAIN=local \
    go build -tags demo -trimpath -ldflags="-s -w" -o /out/argus-api ./cmd/argus-api \
 && CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor GOTOOLCHAIN=local \
    go build -trimpath -ldflags="-s -w" -o /out/argus ./cmd/argus \
 && mkdir -p /out/tmp /out/data /out/auth && chmod 1777 /out/tmp && chown 65534:65534 /out/data /out/auth

# --- the API: one static binary, no shell, no CA bundle (it never connects anywhere)
FROM scratch AS api
COPY --from=build /out/argus-api /app/argus-api
COPY --from=build /out/tmp /tmp
COPY packages/design/static /app/static
ENV DEMO_MODE=1 ARGUS_BIND=0.0.0.0:8080 ARGUS_STATIC_DIR=/app/static \
    ARGUS_STATE_DIR=/data ARGUS_DB=/data/ops.db ARGUS_SETTINGS_IN=/data/settings-in ARGUS_SETTINGS_OUT=/data/settings-out \
    ARGUS_REPORTS_DIR=/data/reports/rca ARGUS_CRED_STATUS_FILE=/data/cred-status.json ARGUS_ALERTS_FILE=/data/alerts-firing.json \
    ARGUS_SELFCHECK_FILE=/data/selfcheck.json ARGUS_READINESS_FILE=/data/readiness.json ARGUS_BACKUP_REF_DIR=/data/backup-plans/ref \
    ARGUS_AUTH_DB=/auth/auth.db ARGUS_IMPORT_HTPASSWD= ARGUS_ADMINS= ARGUS_ADMIN_USER=demo.admin \
    FEATURE_TODAY=1 FEATURE_PROBLEMS=1 TZ=UTC
USER 65534:65534
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["/app/argus-api", "healthcheck", "http://127.0.0.1:8080/healthz"]
ENTRYPOINT ["/app/argus-api"]

# --- the data generator: writes /data (mounted read-only into the app) and /auth/auth.db; one static binary ----------
FROM scratch AS data
COPY --from=build /out/argus /app/argus
COPY --from=build /out/tmp /tmp
COPY --from=build --chown=65534:65534 /out/data /data
COPY --from=build --chown=65534:65534 /out/auth /auth
USER 65534:65534
HEALTHCHECK --interval=5s --timeout=3s --retries=30 CMD ["/app/argus", "demo", "ready", "-out", "/data", "-auth", "/auth"]
ENTRYPOINT ["/app/argus", "demo", "generate", "-out", "/data", "-auth", "/auth", "-loop"]

# --- the Next.js frontend: built here, run as the standalone server (node, non-root, read-only rootfs) --------------
FROM ${NODE_IMAGE} AS web-build
# NPM_REGISTRY: a mirror for build machines without direct access to registry.npmjs.org (npm maps the lockfile's
# registry.npmjs.org URLs onto it; the integrity hashes still apply)
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src
ENV NEXT_TELEMETRY_DISABLED=1 npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
COPY package.json package-lock.json .npmrc ./
COPY apps/web/package.json apps/web/
COPY packages/design/package.json packages/design/
RUN npm ci --registry="$NPM_REGISTRY"
COPY packages/design/ packages/design/
COPY apps/web/ apps/web/
RUN npm run build -w @argus/web \
 && mkdir -p /out && cp -r apps/web/.next/standalone/. /out/ \
 && cp -r apps/web/.next/static /out/apps/web/.next/static && cp -r apps/web/public /out/apps/web/public \
 && rm -rf /out/node_modules/sharp /out/node_modules/@img   # next's optional image optimiser (libvips, LGPL): unused, not shipped

FROM ${NODE_IMAGE} AS web
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0 API_URL=http://api:8080 TZ=UTC
COPY --from=web-build --chown=0:0 /out/ /app/
USER 65534:65534
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["node", "-e", "fetch('http://127.0.0.1:3000/healthz').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]
CMD ["node", "apps/web/server.js"]
