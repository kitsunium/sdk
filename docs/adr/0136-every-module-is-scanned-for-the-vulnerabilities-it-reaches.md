# ADR 0136 — Every module is scanned for the vulnerabilities it reaches, and a reachable one blocks the merge

- **Status**: Accepted; implemented in `scripts/ci/vuln-check.sh`, the `vuln-install` / `vuln-check` targets of the `Makefile`, the `bazel` job of `.github/workflows/bazel-ci.yml` and `.github/workflows/vuln-scan.yml`.
- **Date**: 2026-09-26
- **Deciders**: kitsunium maintainers
- **Amends**: [ADR 0004](0004-sdk-bazel-build-system.md) (the four govulncheck jobs it retired with the old pipeline come back, outside Bazel), [ADR 0008](0008-readme-from-code-generation.md) (its step 6 — govulncheck on the gomarkdoc binary — no longer runs, and never covered SDK code)
- **Related**: [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the modules scanned are the census), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (a gate is a name CI says out loud)

## Context

Nothing scanned the SDK for known vulnerabilities ([#210](https://github.com/kitsunium/sdk/issues/210)). Two removals, independent of each other, produced that:

1. **ADR 0004** replaced the old pipeline — a nine-job matrix including four `govulncheck` jobs over the runtime modules — with Bazel as the single build and test system. `bazel test` carries no vulnerability database, so the capability was removed, not replaced.
2. **ADR 0008** later added a narrower scan, `govulncheck -mode=binary` over the resolved `gomarkdoc` binary. It never covered SDK code, and `bazel-ci.yml` removed it for a reason it records and that stands: gomarkdoc v1.1.0 embeds `x/crypto` and `go-git` versions with historical CVEs in SSH and repository-write paths gomarkdoc never reaches, so the scan blocked every pull request on an upstream release.

Both ADRs still read as if a scan ran. `.github/workflows/CLAUDE.md` went further and made the absence a convention: *"CI does NOT run `go test`, `golangci-lint`, or `govulncheck` directly anymore (ADR 0004)"*.

What remained is Dependabot: alerts are enabled on the dependency graph, and they are **blind to reachability** — an alert says a vulnerable version is in the manifest, not that the SDK calls the vulnerable symbol. That distinction is the one that made retiring the gomarkdoc scan legitimate, and a manifest alert cannot make it. The last scan anybody ran was by hand, on 2026-08-21, and it is how CVE-2026-1710 (`moby/go-archive`, transitive through testcontainers) was found: nothing in CI would have.

Measured before deciding, with `govulncheck@v1.8.0` (database of 2026-09-24) on go1.27.1, every module in source mode: **no reachable vulnerability in any of the eight**. The root module imports two vulnerable packages and requires one vulnerable module whose symbols its code never calls — the case a manifest alert would raise and a reachability scan correctly does not fail on.

## Decision

1. **`make vuln-check` runs `govulncheck ./...` in source mode in every module of the census** (ADR 0137), one module at a time with `GOWORK=off`, so a finding names its module and each module is judged as it resolves on its own. Every module is scanned even after one fails.
2. **It fails on a REACHABLE vulnerability** — govulncheck's exit status 3 — **and on a scan that did not complete**, under its own name: no network to `vuln.go.dev`, a module that does not load. A scan that did not run is not a clean scan. A vulnerable package that is imported and never called, or a vulnerable module that is required and never reached, is reported and passes.
3. **Blocking, in the required `bazel` job.** A vulnerability published between two merges turns every pull request red until the dependency — or the Go toolchain — moves. That is intended, not a side effect: the SDK's `require` lines are the floor every consumer inherits under minimal version selection, so the fix is the SDK's to ship, and nothing else should merge ahead of it.
4. **The standard library is in the scan.** Its version is the toolchain's, and a reachable finding there is settled the same way the repository already moves Go: the `go` line of every module and `MODULE.bazel`'s `go_sdk` together — which is also what makes a consumer's build pick the fixed toolchain.
5. **The scanner is pinned once, in the Makefile** (`GOVULNCHECK_VERSION`). `make vuln-install` installs exactly that version — a module version is verified against the checksum database, so the pin is the integrity check — and `vuln-check.sh` refuses to run under any other: a lane that follows `latest` changes its verdict with no commit of this repository moving.
6. **Every day, too.** `.github/workflows/vuln-scan.yml` runs the same target on a daily schedule and on demand, so a publication surfaces within a day rather than on somebody's unrelated pull request. It is an alarm; the gate is the `bazel` job.
7. **The tooling boundary stays where it was.** `gomarkdoc` is a binary this repository installs, not a module it ships, and it stays out of the scan for the reason `bazel-ci.yml` records. Every module of this repository is in — including `tools/sdkguard`, which consumers run with `go run …@latest`, and `e2e`.

`vuln-check` is in `scripts/ci-gates-check.sh`'s `GATES`, next to `ci-scripts-check`, whose suite pins the verdicts above with a stub scanner: clean, reachable (exit 3), incomplete (exit 1), wrong version, missing binary.

## Consequences / Semantics

- `.github/workflows/CLAUDE.md`'s convention is rewritten: CI runs `govulncheck` directly, per module, outside Bazel — the one analysis Bazel cannot host, because it needs a database Bazel does not carry.
- The lane needs `vuln.go.dev`. Like any network dependency it can fail; it then fails loudly under "did not complete", never as clean. `make lint` stays offline — `vuln-check` is deliberately not part of it.
- A new module is scanned the moment git tracks its `go.mod` (ADR 0137); nobody edits this gate to add one.
- Cost: govulncheck over eight modules is a few minutes on a hosted runner, inside a job whose budget is two hours.

## Breaking changes

None. Nothing a consumer compiles changes. For contributors, one: a pull request can now go red because of a vulnerability published after it was opened, and the fix is a dependency or toolchain bump, not a change to the pull request.

## Alternatives considered

### Why not non-blocking on pull requests and blocking only on a schedule

Then the pull request that merges while a reachable vulnerability is known is green, and the release it triggers publishes a floor with the vulnerable version in it. The daily run is kept as an early warning; it is not a substitute for refusing the merge.

### Why not only fail on vulnerabilities a pull request introduces

It needs a second scan of the base tree on every run and a diff of two reports, and it answers a different question: "did this change make it worse" rather than "is what we are about to release vulnerable". The second is the one a consumer needs answered, and the one a release needs to be able to say no to.

### Why not exclude the standard library

A consumer's toolchain is their own, but the SDK's `go` line is the floor it can be selected at — raising it is how the SDK makes a consumer's build pick a fixed toolchain, and this repository already moves the line and `MODULE.bazel` together. A finding there is actionable here, so it is not noise.

### Why not scan all modules in one `govulncheck` over the workspace

`go.work` holds five of the eight modules (ADR 0001), and a workspace build resolves each module's dependencies against the others', which is not how a consumer resolves any of them. One module at a time with `GOWORK=off` is each module as it is published.

### Why not Dependabot alone, or a manifest scanner

Reachability is the distinction that matters, and the one they cannot make: an alert on a vulnerable package the SDK imports and never calls would block merges for nothing, which is exactly why the gomarkdoc binary scan was retired.

## Deferred

- **SARIF or code-scanning upload** of the findings. The job log names the module, the vulnerability, the fixed version and the call path already; a dashboard is a convenience, not a gate.
- **Automated dependency updates for `gomod`.** `.github/dependabot.yml` covers GitHub Actions only. A reachable finding now fails the gate, which is the enforcement; automation of the fix is a separate choice.

## References

- [ADR 0004](0004-sdk-bazel-build-system.md) — Bazel as the single build and test system (the retired scans)
- [ADR 0008](0008-readme-from-code-generation.md) — its step 6, the gomarkdoc binary scan, and why `bazel-ci.yml` removed it
- [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) — the census the scan loops over
- `scripts/ci/vuln-check.sh`, `scripts/ci/test-ci-scripts.bats` — the gate and its suite
- Issue [#210](https://github.com/kitsunium/sdk/issues/210) — no govulncheck step covered the runtime modules
