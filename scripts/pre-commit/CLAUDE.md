<!-- updated: 2026-10-03T13:06:04Z -->
# scripts/pre-commit/

## Purpose

The repository's guards: one executable `check-*.sh` per invariant the build
cannot see by itself, exit 0 when it holds and non-zero with the offenders
named. Each finds the repository root from its own path, so it runs from any
directory; seven of them also take the workspace path as their first argument
(all but `check-error-codes-drift.sh` and the two README checks). The
directory keeps its name from the git hook that used to run them: since
ADR 0153 removed the hook, `make lint` and the steps of `bazel-ci.yml`'s
`bazel` job run them. The tree's portability rule (macOS's `/bin/bash` 3.2 and
BSD tools, nothing installed) is in `scripts/CLAUDE.md`.

## Contents

| Guard | Fails when | Run by |
|---|---|---|
| `check-alloc-lane-coverage.sh` | a directory holding a `//go:build !race` test is not covered by `tools/alloc-lane-targets.txt` — such a test runs in no lane (rule 12) | `make lint` and a `bazel` job step |
| `check-audit-coverage.sh` | a package declaring an `errs.Define` or an `errs.Code` constant is missing from `//:audit_sources`, so the errs AST audits never judge it (rule 3, ADR 0020); a re-export allocates nothing and is not counted | `make lint` and a `bazel` job step |
| `check-bench-md.sh` | a directory holding a `*_bench_test.go` has no sibling `BENCH.md` (rule 9) — presence only, never freshness | `make lint` and a `bazel` job step |
| `check-core-symmetry.sh` | a production file under `internal/service` declares a code, a service domain has no core package at its path, or a core package declares a code with no engine at its path — a sub-contract beneath a domain whose ranges are all the core's own (layer 2) excepted (ADR 0160) | `make lint` and a `bazel` job step |
| `check-domain-docs.sh` | the root `CLAUDE.md`'s `core/` block stops naming exactly the Go packages under `internal/core`, a domain is described twice in its Purpose table, or one of the three ADR indexes stops naming exactly the ADRs on disk (rule 11) | `make lint` and a `bazel` job step |
| `check-error-codes-drift.sh` | `docs/error-codes.yaml` differs from what `scripts/gen-error-codes.sh` regenerates now (`make error-codes`) | `make lint` and a `bazel` job step |
| `check-pkg-docs.sh` | a directory under `internal/`, `pkg/` or `framework/` holding production Go has neither `CLAUDE.md` nor `README.md`, or a `pkg/v*/**` or `framework/**` package lacks either one (rule 8) | `make lint` and a `bazel` job step |
| `check-readme-drift.sh` | a generated `README.md` under `pkg/v1` or `framework` differs from what gomarkdoc emits from the doc comments now (rule 10, ADR 0008) | a `bazel` job step only |
| `check-readme-determinism.sh` | two gomarkdoc runs over `pkg/v1` into separate directories disagree — the drift gate would turn flaky (ADR 0008 §Consequences) | a `bazel` job step only |
| `check-ktn-phases-1-7.sh` | ktn-linter's active phases 1-7 report an issue anywhere in the SDK | by hand, before a commit; CI enforces the same phases through `make lint-ktn-check` |
| `test-pre-commit-guards.bats` | — the suite: the two guards that piped into an early-exiting reader (one failed closed, one failed open), `check-domain-docs.sh` reading a core grouped by family, `check-core-symmetry.sh` on fixture trees, and a source scan holding every guard to bash 3.2 and BSD `find` | `make pre-commit-check` (`scripts/pre-commit-test.sh`), in the `shell-gates` job |

The seven guards `make lint` runs, with `scripts/check-layer-deps.sh`, are the
`GUARDS` list of `scripts/ci-gates-check.sh`, which fails when one of them is
not both a line of the `lint` recipe and a `run:` step of the `bazel` job.

## Rules

- **Every `*.sh` here is a guard.** A test runner or a helper sits in
  `scripts/` (`pre-commit-test.sh`), so nothing here reads as a gate that is
  not one.
- **A new guard is wired in the commit that adds it**: a line of the `lint`
  recipe, a `run:` step of the `bazel` job, and an entry in
  `ci-gates-check.sh`'s `GUARDS` — or the manifest check fails.
- **Fail closed.** A step that cannot answer (a file grep cannot read, a find
  that errors, an empty tree) stops the guard with a message; a gate that
  passes on an empty answer is the gap it exists to close.
- **No `cmd | grep -q` under `pipefail`.** The early exit turns the producer's
  EPIPE into status 141, which these scripts once read as "no match" — in one
  guard a false failure, in another a package silently dropped from the scan.
  Read to EOF, or test a captured value.
- **A guard keys on what is on disk**, never on a number someone maintains.

## Verify

```sh
make lint                  # the seven guards, among the rest
make pre-commit-check      # the BATS suite; needs bats on PATH
bash scripts/pre-commit/check-domain-docs.sh   # any guard runs alone
```
