<!-- updated: 2026-10-02T19:53:12Z -->
# scripts/

## Purpose

Repository tooling, not library code: the pre-commit guards, the release
scripts, the CI helpers, and the checks that keep each of them a real gate.
Exempt from rule 8's `README.md` requirement (root `CLAUDE.md`), because nothing
here is a Go package.

## Contents

| Path | What it is |
|---|---|
| `pre-commit/` | the guards `make lint` and `bazel-ci.yml` run (ADR 0153), each an executable `check-*.sh` taking the workspace path and defaulting to the repository root; `test-pre-commit-guards.bats` pins them, and keeps them runnable on a Mac as shipped |
| `release/` | `compute-bumps.sh` (WHETHER to release), `cut-tags.sh` (HOW BIG, from `release:*` labels — ADR 0135), `check-pr-size.sh`, their BATS suites (`test-*.bats` over `test-helpers.bash`, run by `release-scripts-test.sh`) and `lib/` (`tag-format.sh`, `release-scope.sh`, `release-size.sh`); also `test-sync-versions.mjs.test.js`, a Node test of `docs/site/scripts/lib/tag-format.mjs` that no lane runs — `docs/site`'s `npm test` globs `scripts/lib/*.test.mjs` only |
| `ci/` | `go-modules.sh`, the module census every module-looping lane reads (ADR 0137), and `vuln-check.sh`, the govulncheck gate over it (ADR 0136) |
| `check-layer-deps.sh` | the layer firewall on the build graph: seven `bazel query` expressions that must be empty (ADR 0068; the last three are the framework's, ADR 0147) |
| `ci-gates-check.sh` | a listed gate must have a `make` target, be `.PHONY`, and be invoked by `bazel-ci.yml` (ADR 0088) |
| `ci-scripts-test.sh`, `pre-commit-test.sh` | run the BATS suites of `ci/` and `pre-commit/` |
| `cross-platform-audit.sh` | the local twin of `bazel-ci.yml`'s `cross-build`: every module of the census built and vetted (tests included) for every GOOS/GOARCH cell, printed as a matrix; needs bash 4 |
| `gen-error-codes.sh` | regenerates `docs/error-codes.yaml` (`make error-codes`) |

## cross-platform-audit.sh and cross-build list the same cells

Twelve: linux on amd64, arm64, 386 and arm; darwin/arm64; windows/amd64; the
four BSDs on amd64; illumos/amd64 and solaris/amd64 (ADR 0144 — two GOOS values,
since `runtime.GOOS` tells them apart although the `solaris` build tag selects
both). Nothing checks that the two lists agree, so an edit to one is an edit to
the other.

## The guards run on the tools a Mac ships

Every `pre-commit/*.sh` runs under macOS's `/bin/bash` 3.2 and
the BSD `find`, `sed`, `sort` and `wc` in `/usr/bin`, as well as under bash 5
and GNU tools, with nothing installed (#260). So: no `readarray` or `mapfile`,
no associative array, no `${x,,}`, no `find -printf`; an array that may be
empty is not expanded under `set -u`, which bash 3.2 reports as unbound; and a
`wc -l` count loses the spaces BSD pads it with before it is printed. CI only
has bash 5 and GNU find, so `test-pre-commit-guards.bats` reads the sources for
the constructs that broke the guards before, rather than trusting a run there.
`gen-error-codes.sh` is held to the same rule, because a guard runs it.

## Do NOT

- Enumerate modules in a script that loops over them — read
  `ci/go-modules.sh` (ADR 0137).
- Add a suite nothing runs: a new `*.bats` beside the others is picked up by its
  runner's glob, and a new runner needs a `make` target listed in
  `ci-gates-check.sh` (ADR 0088).
