#!/bin/sh
# Refresh vendor/ after a go.mod change: vendor every module, then keep only what linux/amd64 builds
# (the image is linux/amd64; the full vendor tree is 200 MB, this one is ~18 MB). Needs a Go module proxy
# (GOPROXY, default proxy.golang.org); the image build itself needs none (vendor/ is committed).
set -eu
cd "$(dirname "$0")"
GOFLAGS=-mod=mod GOPROXY=${GOPROXY:-https://proxy.golang.org,direct} go mod tidy
GOPROXY=${GOPROXY:-https://proxy.golang.org,direct} go mod vendor
find vendor -name '*.go' | grep -E '_(darwin|windows|freebsd|openbsd|netbsd|dragonfly|solaris|illumos|aix|plan9|android|ios|js|wasip1|zos)(_[a-z0-9]+)?\.go$|_linux_(386|arm|arm64|loong64|ppc64|ppc64le|riscv64|s390x|mips|mipsle|mips64|mips64le)\.go$' | xargs rm -f
find vendor -type d -empty -delete
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-mod=vendor go build -o /dev/null ./cmd/argus-api
du -sh vendor
