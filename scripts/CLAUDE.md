<!-- updated: 2026-09-29T03:41:17Z -->
# scripts/

## Purpose

Repository tooling, not library code: the pre-commit guards, the release
scripts, the CI helpers, and the checks that keep each of them a real gate.
Exempt from rule 8's `README.md` requirement (root `CLAUDE.md`), because nothing
here is a Go package.

## Contents

| Path | What it is |
|---|---|
| `pre-commit/` | the guards `.githooks/pre-commit` runs in lexical order, each an executable `check-*.sh` taking the workspace path; `test-pre-commit-guards.bats` pins them |
| `release/` | `compute-bumps.sh` (WHETHER to release), `cut-tags.sh` (HOW BIG, from `release:*` labels — ADR 0135), `check-pr-size.sh`, their BATS suites and `lib/` |
| `ci/` | `go-modules.sh`, the module census every module-looping lane reads (ADR 0137), and `vuln-check.sh`, the govulncheck gate over it (ADR 0136) |
| `check-layer-deps.sh` | the layer firewall on the build graph: four `bazel query` expressions that must be empty (ADR 0068) |
| `ci-gates-check.sh` | a listed gate must have a `make` target, be `.PHONY`, and be invoked by `bazel-ci.yml` (ADR 0088) |
| `ci-scripts-test.sh`, `pre-commit-test.sh`, `hooks-test.sh` | run the BATS suites of `ci/`, `pre-commit/` and `.githooks/` |
| `cross-platform-audit.sh` | the local twin of `bazel-ci.yml`'s `cross-build`: every module of the census built and vetted (tests included) for every GOOS/GOARCH cell, printed as a matrix; needs bash 4 |
| `gen-error-codes.sh` | regenerates `docs/error-codes.yaml` (`make error-codes`) |
| `install-hooks.sh` | points `core.hooksPath` at `.githooks/`, once per clone |
| `test-commit-msg-hook.bats` | the commit-msg hook's suite (no AI attribution) |

## cross-platform-audit.sh and cross-build list the same cells

Twelve: linux on amd64, arm64, 386 and arm; darwin/arm64; windows/amd64; the
four BSDs on amd64; illumos/amd64 and solaris/amd64 (ADR 0144 — two GOOS values,
since `runtime.GOOS` tells them apart although the `solaris` build tag selects
both). Nothing checks that the two lists agree, so an edit to one is an edit to
the other.

## Do NOT

- Enumerate modules in a script that loops over them — read
  `ci/go-modules.sh` (ADR 0137).
- Add a suite nothing runs: a new `*.bats` beside the others is picked up by its
  runner's glob, and a new runner needs a `make` target listed in
  `ci-gates-check.sh` (ADR 0088).
