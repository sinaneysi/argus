# Argus for a real installation (ADR-0021 D1): two images from one file. Build context: the repository root.
#   docker build -f deploy/Dockerfile --target argus -t argus:latest     .   # argus-api + argus (worker, migrate, CLI)
#   docker build -f deploy/Dockerfile --target web   -t argus-web:latest .   # the Next.js frontend
# Normally through deploy/compose.yml: three services (api, worker, web), one volume at /var/lib/argus.
# Unlike demo/Dockerfile: no demo build tag, a CA bundle (Argus connects to the data sources), the one-volume layout.
ARG GO_IMAGE=golang:1.22.12-alpine
ARG NODE_IMAGE=node:20-bookworm-slim
# The Claude CLI of the cli model provider (internal/model): the native musl build from the npm registry, pinned by
# version and sha256 of the package. --build-arg CLAUDE_CODE_VERSION= builds the image without it (provider api only).
ARG CLAUDE_CODE_VERSION=2.1.311
ARG CLAUDE_CODE_SHA256_AMD64=e397b0accb2439a56913678d00ad4892eb746fc36b10258a85f0e2c3873ae88a
ARG CLAUDE_CODE_SHA256_ARM64=75a5940fb25bb1e86c44432cb42783931748b41eed3508eb50087146e4f93d53

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY apps/api/go.mod apps/api/go.sum ./
COPY apps/api/vendor/ ./vendor/
COPY apps/api/cmd/ ./cmd/
COPY apps/api/internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor GOTOOLCHAIN=local \
    go build -trimpath -ldflags="-s -w" -o /out/argus-api ./cmd/argus-api \
 && CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=vendor GOTOOLCHAIN=local \
    go build -trimpath -ldflags="-s -w" -o /out/argus ./cmd/argus \
 && mkdir -p /out/tmp /out/var/lib/argus && chmod 1777 /out/tmp && chown 65534:65534 /out/var/lib/argus

# --- the Claude CLI (and the musl loader it needs in the scratch image) ------------------------------------------------
FROM ${GO_IMAGE} AS claude
ARG CLAUDE_CODE_VERSION CLAUDE_CODE_SHA256_AMD64 CLAUDE_CODE_SHA256_ARM64 TARGETARCH
ARG NPM_REGISTRY=https://registry.npmjs.org
RUN mkdir -p /out/usr/local/bin /out/lib && if [ -n "$CLAUDE_CODE_VERSION" ]; then set -e; \
      case "${TARGETARCH:-amd64}" in amd64) a=x64; sum=$CLAUDE_CODE_SHA256_AMD64;; arm64) a=arm64; sum=$CLAUDE_CODE_SHA256_ARM64;; \
        *) echo "no Claude CLI for $TARGETARCH" >&2; exit 1;; esac; \
      p=claude-code-linux-$a-musl; \
      wget -q -O /tmp/c.tgz "$NPM_REGISTRY/@anthropic-ai/$p/-/$p-$CLAUDE_CODE_VERSION.tgz"; \
      echo "$sum  /tmp/c.tgz" | sha256sum -c -; \
      tar -xzf /tmp/c.tgz -C /tmp package/claude; install -m 0755 /tmp/package/claude /out/usr/local/bin/claude; \
      for f in /lib/ld-musl-*.so.1 /lib/libc.musl-*.so.1; do cp -L "$f" /out/lib/; done; \
    fi

# --- argus: both Go binaries (and the Claude CLI); the same image runs the api and the worker service ------------------------------------
FROM scratch AS argus
COPY --from=build /out/argus-api /app/argus-api
COPY --from=build /out/argus /app/argus
COPY --from=build /out/tmp /tmp
COPY --from=build --chown=65534:65534 /out/var/lib/argus /var/lib/argus
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=claude /out/ /
COPY packages/design/static /app/static
# the model calls run as the same non-root user; each gets a throwaway HOME in /tmp (tmpfs with a read-only root)
ENV ARGUS_LAYOUT=volume ARGUS_STATE_DIR=/var/lib/argus \
    ARGUS_BIND=0.0.0.0:8080 ARGUS_STATIC_DIR=/app/static TZ=UTC
USER 65534:65534
VOLUME /var/lib/argus
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["/app/argus-api", "healthcheck", "http://127.0.0.1:8080/healthz"]
ENTRYPOINT ["/app/argus-api"]

# --- argus-web: the Next.js frontend (standalone server, node, non-root) ---------------------------------------------
FROM ${NODE_IMAGE} AS web-build
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
 && rm -rf /out/node_modules/sharp /out/node_modules/@img

FROM ${NODE_IMAGE} AS web
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0 API_URL=http://api:8080 TZ=UTC
COPY --from=web-build --chown=0:0 /out/ /app/
USER 65534:65534
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD ["node", "-e", "fetch('http://127.0.0.1:3000/healthz').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]
CMD ["node", "apps/web/server.js"]
