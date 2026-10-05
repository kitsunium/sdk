#!/usr/bin/env bash
# scripts/pre-commit/check-readme-drift.sh — canonical README drift gate.
# Run by `make lint` and as a direct step of `.github/workflows/bazel-ci.yml`.
# Single source of truth for what "drift" means: every README.md under pkg/
# and framework/ must be byte for byte what tools/genindex -write-readmes
# writes now from the committed docs/api (ADR 0167, amending ADR 0008) — and
# docs/api is the code's (`make api-check`), whose docs are the design's (the
# pin markers' doc digests). A README of a package docs/api no longer records,
# which genindex wrote, is stale and named too.
#
# genindex is the SDK's own stdlib-only tool: no binary to install, no network,
# the same bytes on every machine. It replaces gomarkdoc, whose exit status
# was unreliable when one call checked several packages and whose source links
# depended on the checkout's git state.
#
# Exits non-zero on any drift, naming each README.
#
# Runs under macOS's /bin/bash 3.2 (scripts/CLAUDE.md).

set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

cd "$root/tools/genindex"
GOWORK=off go run . -check-readmes -repo-root "$root"
