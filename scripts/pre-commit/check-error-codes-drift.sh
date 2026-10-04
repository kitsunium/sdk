#!/usr/bin/env bash
# scripts/pre-commit/check-error-codes-drift.sh — error-code list drift gate.
#
# The committed docs/error-codes.yaml is the human-readable list of the SDK's
# dotted-quad error codes (ADR 0005/0006). scripts/gen-error-codes.sh writes it
# from docs/api (tools/genindex -write-error-codes), and `make api-check` holds
# docs/api to the code, so this guard closes the chain: it fails CI and
# `make lint` when the file is not what docs/api writes now — a code added,
# changed or removed in the code, `make api` run, and the file not
# regenerated — naming each entry listed and not declared, or declared and not
# listed (tools/genindex -check-error-codes). It writes nothing. The executable
# source of truth remains the AST audit
# (internal/kernel/errs/registry_external_test.go); this keeps the doc honest
# (CLAUDE.md rule 11).
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

if [ ! -f "$root/docs/error-codes.yaml" ]; then
	echo "✗ docs/error-codes.yaml missing — run 'make error-codes'." >&2
	exit 1
fi

# genindex is an auxiliary module outside go.work: GOWORK=off, from its own
# directory.
cd "$root/tools/genindex"
if ! GOWORK=off go run . -check-error-codes -repo-root "$root" >&2; then
	echo "✗ docs/error-codes.yaml is stale — run 'make error-codes' (or 'make api') and commit." >&2
	exit 1
fi
