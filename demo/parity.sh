#!/bin/sh
# Compare the Go demo generator (argus demo generate) with the original Python generator, which was removed after the
# port. The Python version is taken from the git history, so this needs a full clone and python3 (stdlib only).
#   demo/parity.sh                 # from the repository root
# It writes both data sets at the same time and seed (TZ=UTC) and runs TestParityWithPython, which compares every
# file: SQLite schemas and every row, JSON documents, Markdown bytes and PNG pixels.
set -eu
REF=${REF:-e63c5a0}          # the last commit with demo/generate.py
NOW=${NOW:-1793318400}       # 2026-10-30T00:00:00Z
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git archive "$REF" demo | tar -x -C "$tmp"
TZ=UTC python3 -I "$tmp/demo/generate.py" --out "$tmp/py/data" --auth "$tmp/py/auth" --now "$NOW" >/dev/null
TZ=UTC ARGUS_DEMO_PARITY_DIR="$tmp/py" ARGUS_DEMO_PARITY_NOW="$NOW" GOFLAGS=-mod=vendor GOTOOLCHAIN=local \
  sh -c 'cd apps/api && go test ./internal/demo -run TestParityWithPython -count=1 -v'
