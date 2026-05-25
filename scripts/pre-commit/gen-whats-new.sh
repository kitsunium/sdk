#!/usr/bin/env bash
# ============================================================================
# gen-whats-new.sh — pre-commit step that keeps the tracked "What's new"
# banner data (docs/site/src/data/whats-new-*.json) in sync with HEAD.
#
# It regenerates the JSON from `git log` (gh-free + fast, via
# docs/site/scripts/gen-whats-new.mjs) and stages it INTO the commit being
# made. Unlike the sibling check-*.sh guards (which validate and fail on
# drift), this one MUTATES + stages — the deliberate trade chosen for this
# file so the banner snapshot never lags by hand.
#
# Caveat (inherent to commit-time generation): the banner reflects HEAD = the
# PARENT of the commit being created. The new commit's SHA does not exist yet,
# so a feat/fix you just wrote lands in the banner one commit later. The
# DEPLOYED site is always fully fresh regardless, because `npm run prebuild`
# regenerates from current HEAD at build time; this hook only stops the
# committed snapshot from drifting.
# ============================================================================
set -euo pipefail

WORKSPACE="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
GEN="$WORKSPACE/docs/site/scripts/gen-whats-new.mjs"

# Nothing to do if the docs site / generator is absent (partial checkout).
[ -f "$GEN" ] || exit 0
# Don't block a commit just because node is unavailable in this environment.
command -v node >/dev/null 2>&1 || exit 0

node "$GEN"

# Stage only the regenerated banner data. A no-op when nothing changed.
git -C "$WORKSPACE" add docs/site/src/data/whats-new-*.json 2>/dev/null || true

exit 0
