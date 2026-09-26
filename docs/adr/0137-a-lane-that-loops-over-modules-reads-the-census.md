# ADR 0137 — A lane that loops over modules reads the census, and names what it skips

- **Status**: Accepted; implemented in `scripts/ci/go-modules.sh` and read by the `cross-build` and `test-386` jobs of `.github/workflows/bazel-ci.yml`, the per-package job of `.github/workflows/e2e-cross.yml`, `scripts/cross-platform-audit.sh` and `scripts/ci/vuln-check.sh`.
- **Date**: 2026-09-26
- **Deciders**: kitsunium maintainers
- **Amends**: [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (the build bar's loop covers every module, not the six it named)
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (five modules in `go.work`, auxiliary ones outside it), [ADR 0018](0018-sdk-cross-platform-portability.md) (the build and runtime bars), [ADR 0136](0136-every-module-is-scanned-for-the-vulnerabilities-it-reaches.md) (the vulnerability scan loops over the same census)

## Context

The repository has eight Go modules: the five `go.work` holds (ADR 0001) and three kept outside it on purpose — `e2e`, `tools/genindex` and `tools/sdkguard`. The `go` command's `./...` stops at the first nested `go.mod`, so a lane that runs `go build`, `go vet` or `go test` per module reaches exactly the modules somebody wrote into its loop. Four such loops existed, and no two agreed:

| loop | modules it named |
|---|---|
| `bazel-ci.yml` `cross-build` | `internal/kernel internal/core internal/service pkg . e2e` |
| `bazel-ci.yml` `test-386` | `internal/kernel internal/core internal/service pkg` |
| `e2e-cross.yml`, every package on macOS and Windows | `internal/kernel internal/core internal/service pkg . e2e` |
| `scripts/cross-platform-audit.sh` | `internal/kernel internal/core internal/service pkg .` |

None named `tools/genindex` or `tools/sdkguard` ([#242](https://github.com/kitsunium/sdk/issues/242)): their tests — 477 passing, counted at every nesting level — had never run on 32 bits, and the build bar had never cross-compiled them. The root module's `third-party/` tests and `e2e`'s had never run on 32 bits either. Nothing looked wrong, because `ktn-linter` and `sdkguard` walk PATHS and did reach `tools/`; the tools that walk MODULES did not.

Measured before deciding, on linux/386 with go1.27.1: the four modules the 32-bit lane skipped — `tools/genindex`, `tools/sdkguard`, `e2e`, and the root module's eleven `third-party/` packages — all pass. So covering them costs no exclusion today.

## Decision

1. **The modules are a census, not a list.** `scripts/ci/go-modules.sh` prints every module directory whose `go.mod` git tracks, `.` for the root, sorted. A `go.mod` under `testdata/` is a fixture, not a module of this repository. An untracked `go.mod` is not reported, because CI checks out what git tracks. A census git cannot read, or one that comes back empty, is an error: a loop over nothing passes having built nothing.
2. **Every lane that loops over modules reads it**: `cross-build` (build + vet, ten GOOS/GOARCH cells), `test-386` (the 32-bit runtime), the per-package job of `e2e-cross.yml` (macOS and Windows), `scripts/cross-platform-audit.sh` (the local twin of `cross-build`) and `scripts/ci/vuln-check.sh` (ADR 0136). A module is in all of them the moment git tracks its `go.mod`; nobody edits a workflow to add one.
3. **A lane that must skip a module says so where it loops**, by name and with the lane that covers it instead — rule 12 of the root `CLAUDE.md`, applied to modules. None skips one today.
4. **The census is tested, and so is its use.** `scripts/ci/test-ci-scripts.bats`, run by `make ci-scripts-check` (in `GATES`, in the `shell-gates` job), pins the census in throwaway repositories and asserts that `cross-build`, `test-386` and the local audit read it rather than a list — the row that is red against the workflow as it was.

## Consequences / Semantics

- `test-386` runs eight modules instead of four; `cross-build` and the macOS/Windows package job run eight instead of six; the local audit eight instead of five.
- The root `CLAUDE.md`'s "counting go.mod files yields seven" had drifted to eight when `tools/sdkguard` arrived; it now names the census instead of a number.
- A module that genuinely cannot run somewhere (a platform its code does not support at all) is the one case that needs an edit, and the edit is an exclusion with a reason next to the loop — visible in review, unlike an omission.

## Breaking changes

None. No module, package or API changes; lanes run more.

## Alternatives considered

### Why not add `tools/genindex tools/sdkguard` to the two loops

It closes #242 and keeps the defect: the next module is again in no lane until somebody remembers, and the four loops still disagree with each other. [#242](https://github.com/kitsunium/sdk/issues/242) measured that adding `.` alone would have had the same blind spot.

### Why not `find . -name go.mod`

It reports untracked scratch modules on a developer's machine and whatever a build left behind; CI checks out what git tracks, and the census should answer for that tree.

### Why not `go work edit -json` or `go list -m`

`go.work` holds five of the eight modules by design (ADR 0001 — Bazel's `go_deps` cannot process the auxiliary ones), so the workspace is exactly the list that omits what this ADR is about.

## Deferred

- Nothing enforces that a NEW module-looping lane reads the census; the suite asserts the lanes that exist. The next one is caught in review by this ADR, or by the suite's row once it is added there.

## References

- `scripts/ci/go-modules.sh`, `scripts/ci/test-ci-scripts.bats`, `scripts/ci-scripts-test.sh`
- [ADR 0094](0094-a-test-compiles-where-its-package-does.md) — the build bar vets tests on every GOOS
- Issue [#242](https://github.com/kitsunium/sdk/issues/242) — tools/genindex and tools/sdkguard in no 32-bit lane, and why adding `.` would not have reached them
