#!/usr/bin/env bash
# gen-error-codes.sh — write docs/error-codes.yaml, the human-readable list of
# the SDK's dotted-quad error codes (MM.LL.PP.SS, ADR 0005/0006), from docs/api.
#
# docs/api is the exported API read from the code with go/types on every cell
# (`make api`), and `make api-check` holds it to the code; it records every
# errs.Code constant with its dotted quad, whatever spelling declares it — a
# hex literal, an iota row, a re-export. tools/genindex -write-error-codes
# lists each constant a package declares under a name starting with Code (a
# re-export allocates nothing; the errs package's masks are of the code type
# and no code), sorted by value. The file is therefore the code's.
#
# It replaces a grep over the source, which listed what a regular expression
# matched rather than what the code declares: two codes a doc comment wrote
# (64.1.1.1 and 64.1.2.1, from pkg/v1/errs's example) and none of the six
# meta-codes 0.0.0.1-6 the errs package declares with iota.
#
# Run via `make error-codes` (`make api` runs it after writing docs/api). The
# guard scripts/pre-commit/check-error-codes-drift.sh fails CI and `make lint`
# when the committed file is not what this writes. The executable source of
# truth stays the AST audit in internal/kernel/errs/registry_external_test.go.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

# genindex is an auxiliary module outside go.work: GOWORK=off, run from its
# own directory, as every make recipe runs it.
cd "$root/tools/genindex"
GOWORK=off go run . -write-error-codes -repo-root "$root"
