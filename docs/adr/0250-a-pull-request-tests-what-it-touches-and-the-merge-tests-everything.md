# ADR 0250 — a pull request tests what it touches, and the merge tests everything

- **Status**: Proposed on 2026-11-15 — implemented in the same change (`scripts/ci/scope.sh`, `.github/workflows/bazel-ci.yml`, `.github/workflows/e2e-cross.yml`), for the owner to accept or refuse before it merges; revisable while v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md))
- **Date**: 2026-11-15
- **Deciders**: SDK maintainers; the owner's priority of 2026-11-14 ("une PR qui ne touche qu'un paquet ne fait plus tourner toute la CI")
- **Amends**: rule 12 of the root `CLAUDE.md` ("every test runs on every platform CI can execute, at full size": on a pull request, every test the change can reach; everything on the merge queue, `main`, the nightly run and a manual run); the rules of [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) as `scripts/CLAUDE.md` keeps them (a lane that skips names what it skips: here, on every run, in the job summary)
- **Related**: [ADR 0004](0004-sdk-bazel-build-system.md) (Bazel, the one Linux gate), [ADR 0018](0018-sdk-cross-platform-portability.md) (the build and runtime bars), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (a suite nothing runs is not a suite), [ADR 0094](0094-a-test-compiles-where-its-package-does.md), [ADR 0095](0095-windows-runs-every-test-and-gates.md), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md)
- **Area**: build
- **Need**: decision

## Context

Every pull request ran everything: `bazel test //...` race on, the allocation lane, the framework suites and `bazel coverage //...` in the required `bazel` job; every module on linux/386; the SIMD and garble lanes; and, on every branch push, `e2e-cross.yml` — macOS and Windows in three shards each, seven Linux legs as root under a real init (three of them booting a distribution's kernel under QEMU), the three BSDs and illumos and Solaris in guests. Measured on 2026-11-14: the `bazel` job of a pull request took 35 m 45 s (run 43642086197, #1431) and 15 m 05 s on a re-run (run 43642097269, #1395), 14 m 46 s on a push to `main` (run 43647820252); #1375, three files of one package's tests, ran it all the same. The organisation's hosted runners are shared by every lane of every pull request: jobs waited 2 to 12 minutes to start (`created_at` against `started_at` of those runs), so a pull request's checks were slower than its lanes.

The SDK holds two graphs that say what a change can reach. Bazel's, which Gazelle writes and the drift check holds to the code, and in which a test reads nothing but what its BUILD file declares; and the go command's, which `go list` reads for any GOOS, GOARCH, tag and experiment — the only graph of the platforms Bazel does not build for.

## Decision

1. **The plan.** `scripts/ci/scope.sh plan` decides what a run tests. A pull request (and, in `e2e-cross.yml`, a push to a branch, compared with its merge base with `main`) is *scoped*; a merge queue entry, a push to `main`, a merge queue branch, a tag, the nightly run and a manual run are *full*. A scoped plan falls back to everything (*fallback*) when a path changed that defines the build — `MODULE.bazel`, `MODULE.bazel.lock`, `.bazelrc`, `.bazelversion`, `.bazelignore`, the root `BUILD.bazel`, a `WORKSPACE`, any `go.mod` or `go.sum`, `go.work`, `go.work.sum`, `tools/`, `.gitattributes`, the scope script —, or the calling workflow's own files (`bazel-ci.yml` with `scripts/ci/` and the `Makefile`; `e2e-cross.yml` with `scripts/ci/`), or a path no rule accounts for, or when anything fails: a base the clone does not hold, git, a plan too large for a step's environment.
2. **What a change touches.** A non-test Go file reaches its package and every package that depends on it, tests included (`touched`). A test file, a `testdata/` file, a file beside the sources of a package that embeds none, reach that package's own tests (`tests`); beside a package that embeds files (`//go:embed`), any file reaches its dependents, and a native source beside a Go package (assembly, C, C++, Objective-C, a `.syso` object) is code the package compiles, so it reaches them too. A package whose `go list` answer cannot prove its graph — an error of its own, or a graph that lacks a package — narrows nothing: the run falls back to everything. A file a test reads outside its own package — the error-code audits and genindex's census read every Go source, genindex reads `docs/api` and the platforms table, `net/server` reads `design/net.yaml` and its facade, `framework/app` reads `framework/internal/app` and the common-password list, the consensus engine its core, the MySQL and PostgreSQL connectors the SQLite fixture — reaches the reading package's tests: a table in the script, which `scope.bats` holds to every cross-package `data` label of the BUILD files. Bazel's files reach no Go test; prose, the ADRs, the design, the docs portal, the workflows and the shell tooling reach none either — their gates run in full on every run.
3. **Bazel's lane.** On a scoped plan the `bazel` job runs target-determinator (bazel-contrib, pinned by version and the SHA-256 of its linux/amd64 binary) between the pull request's base and the merge commit, over `//... except` the manual targets: `bazel build` the affected targets, `bazel test` and `bazel coverage` the tests among them, the allocation lane the tests of `tools/alloc-lane-targets.txt` among them, `make test-framework` the framework packages `go list` reaches. Nothing affected, nothing built. The drift checks, the guards, `make lint-check` (`make api-check` with it), the vulnerability scan and the layer firewall always run whole: they are fast and they read the whole tree. A plan that failed, a full plan, a fallback, a target-determinator or a query that fails: `//...`, as before.
4. **The Go lanes.** `test-386`, `test-simd` and `garble-tiny` in `bazel-ci.yml`; the macOS and Windows shards, the Linux legs, the BSDs and illumos and Solaris in `e2e-cross.yml`: the `scope` job decides, per lane, whether a package it tests is reached, `go list` deciding with that lane's GOOS, GOARCH, tags and experiment — a Go program under a package's `testdata/` counts with the package that builds it. A lane nothing reaches is skipped; a lane reached tests, in each leg, the packages its own platform reaches (`scope.sh affected`), a guest leg with nothing in scope boots no guest. `cross-build` (the build bar), `shell-gates`, `lint-ktn`, `models` and `docs-site` are never scoped.
5. **A skip is explicit, and said.** Every lane reading the plan carries `if: ${{ !cancelled() && needs.scope.outputs.<lane> != 'skip' }}`: a plan that failed answers nothing, and the lane runs whole. The required `bazel` job needs `scope` for its plan, not as a gate, and reads the plan's result before its outputs. A `scope-summary` job in each workflow writes, for every lane, *ran in full*, *fallback to full*, *ran (scoped)* or *skipped by scope* with the packages judged out of its reach, and fails when a lane that had to run was skipped. `scope.bats` asserts the `if:` of every scoped lane, the fallbacks of every scoped step, the pins, and each path rule.
6. **The full suite still runs.** On the merge queue when the repository enables it, on every push to `main`, and every night (`bazel-ci.yml` at 02:17 UTC, `e2e-cross.yml` at 02:47 UTC) in a concurrency group of their own, so a nightly run never cancels the push run `sdk-release.yml` waits for. A manual dispatch runs the whole suite on any branch.
7. **An optional remote cache.** `scripts/ci/bazel-remote-cache.sh` appends the remote cache to the runner's `~/.bazelrc` when `BAZEL_REMOTE_CACHE_URL` names one, and does nothing otherwise. It writes only on a push to `main`, a merge queue entry and the nightly run, with a write credential the workflow hands to that step on those events alone; every other run reads only (`--remote_upload_local_results=false`), with a read credential or anonymously, or not at all. A fork's run receives no secret, and the server refuses an unauthenticated write: the server is the guarantee, the flag spares the attempt. A malformed configuration turns the cache off with a warning; a cache is never a reason for a run to fail.

## Consequences / Semantics

- A pull request that touches one leaf package builds and tests a few dozen Bazel targets instead of every one, and skips the 386, SIMD and garble lanes when nothing of theirs is reached. Measured on the change itself (a comment in `internal/service/data/codec/tlv`): 23 affected targets, 15 tests, 3 allocation tests; 18 Go packages on each of linux/386, darwin, windows and NetBSD; the SIMD and garble lanes skipped. The conformance program depends on most of `pkg/v1`, so the `e2e-cross.yml` lanes still run for most code changes — each leg compiling and running only what is in scope.
- A defect a scope misses reaches `main`, where the push run and the nightly run test everything; with the merge queue enabled it is caught before. The scope is exactly as good as the two graphs: an undeclared read of a file outside a test's package is invisible to both, and the readers table names those `go test` makes — a test that reads a new one must be added to it, which `scope.bats` enforces for every one Bazel declares.
- The job summary of every run says what was tested, so a green pull request is read with what it covered.
- target-determinator adds about a minute and a half to a scoped `bazel` job (two `cquery` of `//...`, measured locally at 60 to 80 s); the `scope` job runs beside the gates the `bazel` job already waits for.

## Breaking changes

None for a consumer. A pull request's checks run less; `main`'s and the queue's run what they ran.

## Alternatives considered

### Why not bazel-diff

It needs a JVM and hashes the graph twice through its own JAR; target-determinator is one static binary that runs `cquery` in a worktree of its own and lists the labels. Both were acceptable to the owner's brief.

### Why not `paths:` filters on the triggers

A required check must report on every pull request, and a workflow a `paths:` filter skips reports nothing: the merge waits forever. The decision is a job, as `net-kernel-gate.yml`'s `changes` is.

### Why not the Bazel graph for the Go lanes

Bazel here analyses for the host alone: a `select()` branch of another GOOS is not a configured edge, and the auxiliary modules (`e2e`) are not in the graph. `go list` answers for each platform the lane runs on.

## Deferred

- Scoping `cross-build`: it compiles every module on twelve cells in about three minutes each, and is the only lane of three of them.
- target-determinator's results cache, kept between runs, would make the base's `cquery` a hit.
- Enabling the merge queue is a repository setting; until it is, the full suite of a merge runs on `main` after it.

## References

- `scripts/ci/scope.sh`, `scripts/ci/scope.bats`, `scripts/ci/bazel-remote-cache.sh`, `scripts/ci/test-binaries.sh` (`TEST_BINARIES_ONLY`)
- `.github/workflows/bazel-ci.yml` (`scope`, `bazel`, `scope-summary`), `.github/workflows/e2e-cross.yml` (`scope`, `scope-summary`), `.github/workflows/CLAUDE.md`
- target-determinator v0.34.0 — https://github.com/bazel-contrib/target-determinator/releases/tag/v0.34.0
