<!-- updated: 2026-10-03T13:06:04Z -->
# scripts/ci/

## Purpose

The two helpers every lane that loops over Go modules shares: the module
census (ADR 0137) and the govulncheck gate built on it (ADR 0136), with the
BATS suite that pins both. Tooling, not library code — the tree's general
rules (portability, the ADR 0088 and ADR 0137 rules, the Do NOT list) are in
`scripts/CLAUDE.md`.

## Contents

| File | What it does | Run by |
|---|---|---|
| `go-modules.sh` | the census: one directory per `go.mod` git tracks, `.` for the root, sorted; a `go.mod` under `testdata/` is a fixture and left out, an untracked one is not reported; exit 1 with the reason on stderr when git cannot answer or answers nothing | `bazel-ci.yml`'s `cross-build` and `test-386`, `e2e-cross.yml`, `make test-framework`, `scripts/cross-platform-audit.sh`, `vuln-check.sh` |
| `vuln-check.sh` | govulncheck in source mode, `GOWORK=off`, one module at a time — the whole census, or the modules named as arguments. govulncheck's exit 3 (a reachable vulnerable symbol) fails and names the module; any other non-zero fails as a scan that did not complete; the root `.` answering "no packages matched" is reported as holding no package (ADR 0157 §5), and any other module answering it still fails. With `GOVULNCHECK_VERSION` set the scanner must report exactly that version | `make vuln-check` — a step of `bazel-ci.yml`'s `bazel` job and of the daily `vuln-scan.yml` |
| `test-ci-scripts.bats` | the census in throwaway repositories (nesting, `testdata/`, untracked, empty, a failing git, this repository's tools), the lanes reading it, every govulncheck verdict through a stub scanner, and the guard half of `scripts/ci-gates-check.sh` | `make ci-scripts-check` (`scripts/ci-scripts-test.sh`), in `bazel-ci.yml`'s `shell-gates` job |

## Rules

- **A lane that loops over modules reads the census**, never a list of its own
  — four hand-written lists had drifted apart (#242). A module a lane must skip
  is named next to the loop with the lane that covers it instead; the only one
  today is the empty root `.`, skipped by every census loop while it holds no
  package.
- **An empty answer is an error.** A lane looping over nothing passes having
  built nothing, which is the defect the census exists to close.
- **The census answers for what git tracks**: CI checks out exactly that, so a
  module is in every lane the moment its `go.mod` is committed.
- **Every module is scanned even after one fails**, so one run reports them all;
  a scan that did not run is never reported as a clean scan.
- **The suite tests the scripts where they answer.** Each script answers for the
  repository it lives in, so the suite copies it into a fixture repository; the
  scanner is a stub answering from marker files, so no case needs the network.

## Verify

```sh
bash scripts/ci/go-modules.sh                # prints the census
make ci-scripts-check                        # needs bats on PATH
make vuln-install && make vuln-check         # needs vuln.go.dev
```
