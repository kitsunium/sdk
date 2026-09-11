# ADR 0004 — Bazel 9 as the single build/test system

**Status**: Accepted
**Date**: 2026-04-20
**Deciders**: @kodflow
**Supersedes**: none
**Related**: ADR 0001 (multi-module layout), ADR 0002 (errs registry), context `.claude/contexts/bazel-9-go-sdk.md`
**Amended by**: [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) — §Layer firewall's mechanism: Gazelle gives every `internal/` package `//:__subpackages__` visibility, so the direction is asserted on the build graph instead

## Context

Before this MR the SDK drove build/test/lint through three disconnected
tools:

- `make sdk-*` targets → shell wrappers around `go test`, `go mod tidy`,
  `golangci-lint`.
- `.github/workflows/sdk-ci.yml` → 9-job matrix (4 × `go test` + 1 lint
  + 4 × govulncheck).
- `.golangci.yml` `depguard` ruleset → enforced the kernel → core →
  service → pkg/v1 dependency firewall.

Three shortcomings surfaced during the codec MR (#5) and its follow-up
(#6):

1. `go test` is not byte-reproducible — coverage thresholds fluctuated
   across Go patch releases and across runner OS generations.
2. The CI matrix artefact-name `coverage-${{ matrix.module }}` carried
   a `/` (forbidden by GitHub Actions), requiring an explicit
   sanitisation step. The shape of the matrix leaked into the CI YAML.
3. Future cross-language work (`rules_oci` for example-consumer
   images, `rules_pkg` for tarballs) would have required a Bazel
   conversion anyway.

## Decision

Adopt Bazel 9 as the **single** build/test system for the SDK, local
and CI. `go.mod` / `go.work` / `go.sum` stay on disk so gopls and
IDE tooling keep resolving imports natively.

### Key dependencies

| Component | Version | Source of truth |
|---|---|---|
| Bazel | `9.0.2` | `.bazelversion` |
| rules_go | `0.60.0` | `MODULE.bazel` |
| bazel-gazelle | `0.50.0` | `MODULE.bazel` |
| Go toolchain | `1.26.2` (pinned) | `MODULE.bazel` via `go_sdk.download(version = "1.26.2")` |

### MODULE.bazel

Bzlmod only (Bazel 9 dropped WORKSPACE). `go_deps.from_file(go_work =
"//:go.work")` pulls the dependency list from the four module-local
`go.mod` files; `bazel mod tidy` auto-populates the `use_repo` block.

### Layer firewall

The dependency direction kernel → core → service → pkg/v1 is encoded
as Bazel `package_group` + `visibility` in each layer's root
`BUILD.bazel`:

- `//internal/kernel:kernel_consumers` = kernel + core + service + pkg/v1
- `//internal/core:core_consumers` = core + service + pkg/v1
- `//internal/service:service_consumers` = service + pkg/v1
- `//pkg/v1` = `//visibility:public`

`# gazelle:default_visibility` directives cause Gazelle to auto-wire
each generated `go_library` to its layer's consumer group. Any rogue
import (e.g. kernel → service) fails `bazel build` at the visibility
check — strictly stronger than a `depguard` lint warning because it
fires inside the build graph.

### CI

Single `.github/workflows/bazel-ci.yml` job using
`bazel-contrib/setup-bazel@0.15.0` with disk-cache +
repository-cache keyed on `.bazelrc` + `MODULE.bazel` +
`**/go.{mod,sum}`. Steps: drift check
(`bazel mod tidy && bazel run //:gazelle && git diff --exit-code`),
`bazel build --config=ci //...`, `bazel test --config=ci //...`,
`bazel coverage --combined_report=lcov //...`.

### Makefile

Every `sdk-*` target is now a thin wrapper that shells to `bazel`.
Kept for developer ergonomics and to match the discipline set by
`/workspace/CLAUDE.md`; no business logic.

### .golangci.yml

`depguard` dropped (superseded by Bazel visibility). The other seven
linters (errcheck / govet / ineffassign / staticcheck / unused /
revive / gosec) stay as a code-quality second opinion — they catch
bugs Bazel's build graph doesn't.

## Consequences

### Positive

- **Hermetic builds**: `bazel build` is byte-reproducible across
  environments; the Go SDK version is pinned, not inferred from
  `$PATH`.
- **Incremental testing**: `bazel test //...` reruns only the targets
  whose inputs changed — measurable win on the 20-test suite.
- **Single CI job**: 9 jobs → 1. Simpler YAML, no artefact-name
  footguns.
- **Firewall enforced by the build graph**: layer violations now break
  `bazel build`, not just `golangci-lint run`.
- **Cross-language runway**: `rules_oci` / `rules_pkg` / `rules_shell`
  land cleanly in M3.

### Negative

- **Learning curve**: contributors have to learn `bazel test
  //path:target` (covered in root `CLAUDE.md` "How to work" table).
- **rules_go v0.60.0 is not officially tested against Go 1.26**. The
  SDK's `go.work` declares `go 1.26`; we pin the SDK download to
  `1.26.2` in `MODULE.bazel`. If rules_go ever refuses 1.26, fall back
  to `go_sdk.download(version = "1.25.7")` and downgrade `go.work`
  until v0.61+ adds 1.26 to the test matrix.
- **AST audit needs sandbox shim**: `internal/kernel/errs`'s
  `TestAudit*` tests walk the source tree looking for `go.work`. Under
  Bazel's sandbox the CWD relationship is stripped, so the test
  prefers `TEST_SRCDIR + TEST_WORKSPACE` when set, and the test target
  ships `//:audit_sources` as runfiles (one `audit_srcs` filegroup per
  package, aggregated at the root).

### Neutral

- `go.work` / `go.mod` / `go.sum` remain the authoritative
  dependency declarations; Bazel reads them via
  `go_deps.from_file(go_work = ...)`.
- gopls / VS Code Go extension / `go test ./...` still work locally
  outside Bazel — we don't block either path, we just don't invoke
  them from CI.

## Alternatives rejected

- **Buck2**: smaller Go ecosystem, no Gazelle equivalent, higher
  uncertainty for a single-language SDK.
- **Pants**: Python-first tooling; Go support is immature.
- **Keep the go-native toolchain**: misses hermeticity + incremental
  testing; CI matrix overhead grows linearly with new modules.

## Rollback

The whole migration lands in seven commits on a single branch
(`feat/bazel-m1`). Revert all seven → the old `sdk-ci.yml` and
Makefile targets come back from git history; `.golangci.yml`
depguard block is restored. No runtime artefacts change (no published
module version bump from this MR).

## References

- `.claude/contexts/bazel-9-go-sdk.md`
- Bazel 9 release: https://bazel.build/release
- rules_go v0.60.0: https://github.com/bazel-contrib/rules_go/releases
- gazelle v0.50.0: https://github.com/bazel-contrib/bazel-gazelle/releases
- `bazel-contrib/setup-bazel` action:
  https://github.com/bazel-contrib/setup-bazel
