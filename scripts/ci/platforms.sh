#!/usr/bin/env bash
# scripts/ci/platforms.sh — the twelve GOOS/GOARCH cells the SDK is built for
# and judged on: one cell per line, `goos/goarch`, in the order the
# `cross-build` matrix of .github/workflows/bazel-ci.yml lists them.
#
# One table, read by everything that needs the cells:
#   - scripts/pre-commit/check-platforms.sh fails `make lint` and the `bazel`
#     job when this table and the cross-build matrix differ, in content or
#     order — the matrix stays written in the workflow, where a job's strategy
#     has to be, and this guard keeps the two one table;
#   - scripts/cross-platform-audit.sh, the local twin of cross-build, loops
#     over it;
#   - tools/genindex judges doc links (`make doclinks`) and writes docs/api
#     (`make api`) on these cells. It reads the here-document below WITHOUT
#     running this script: the lines between `cat <<'CELLS'` and `CELLS`, each
#     `goos/goarch`. Keep the table in that one here-document.
#
# illumos and solaris are two cells (ADR 0144): the go command compiles a
# _solaris.go file and the `solaris` build tag for both, an _illumos.go file
# for illumos alone, and runtime.GOOS tells them apart.
#
# A cell added here is added to the matrix in the same change, or the guard
# fails; the doc-link check and docs/api then judge it with no other edit.

set -euo pipefail

cat <<'CELLS'
linux/amd64
linux/arm64
linux/386
linux/arm
darwin/arm64
windows/amd64
freebsd/amd64
openbsd/amd64
netbsd/amd64
dragonfly/amd64
illumos/amd64
solaris/amd64
CELLS
