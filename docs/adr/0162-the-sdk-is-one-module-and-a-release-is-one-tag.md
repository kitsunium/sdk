# ADR 0162 — the SDK is one module, and a release is one tag

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers (the owner's decision of 2026-10-03)
- **Supersedes**: [ADR 0001](0001-sdk-go-multimodule-layout.md) — its module split. The four layers it drew stand, as directories of one module.
- **Amends**: [ADR 0007](0007-sdk-release-and-versioning.md) (§1 the tag shape, §4 what a release tags, §5 the tags the docs sync reads), [ADR 0009](0009-pkg-public-module-resolvability.md) (its single-module alternative is the decision; resolvability stands), [ADR 0017](0017-pkg-bare-module-path.md) (the public module is the root, not `…/pkg`; import paths unchanged), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (§1 the framework is a part of the SDK module, not a module; §9 the lockstep chain), [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) (§3 a vendor module is tagged when it changed, with no GitHub release of its own; §5 the root module is the SDK)
- **Related**: [ADR 0033](0033-consumer-rule-enforcement.md) (sdkguard's freshness probe), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the layer firewall, now the only check of the layer direction), [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) / [ADR 0089](0089-a-file-that-cannot-cut-a-release-cannot-size-one.md) / [ADR 0135](0135-a-release-is-sized-by-a-label-a-maintainer-set.md) (the range, the scope and the size of a release, unchanged), [ADR 0136](0136-every-module-is-scanned-for-the-vulnerabilities-it-reaches.md) / [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the census lanes), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (what made the merge free), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md)

## Context

ADR 0001 split the SDK into Go modules — `internal/kernel`, `internal/core`,
`internal/service` and the public one — so that "internal layers can ship
breaking changes with major bumps on the internal modules without touching
`pkg/v1`". ADR 0009 kept the split and tagged the whole chain at every
release, so that `pkg`'s go.mod resolves from the proxy; ADR 0147 added the
framework and its connectors to the chain, ADR 0157 the nine vendor modules,
all in lockstep. Measured on `main` at the commit this record was written
against:

- **346 tags**: 78 each for `pkg`, `internal/core`, `internal/kernel` and
  `internal/service`; 6 for `framework` and for each of its three database
  connectors; 1 for `framework/connectors/ssh` and for each of the nine
  vendor modules.
- **111 GitHub releases**: 77 for `pkg`, 6 for `framework` and for each of the
  three database connectors, 1 for the ssh connector and for each vendor
  module.
- **18 tags per release**: the last one, v0.17.0, put 18 tags on one release
  commit — the three internal modules, `pkg`, the framework, four connectors,
  nine vendor modules — and 15 GitHub releases; the releases before the vendor
  modules put 8.
- **234 tags of modules nobody may import.** Go's `internal/` rule refuses
  every import of `github.com/kitsunium/sdk/internal/...` from outside the
  repository's path, so the three internal modules were tagged 78 times only
  so that `pkg`'s go.mod would resolve.
- **0 external requirements.** Since ADR 0156 the five modules —
  `internal/kernel`, `internal/core`, `internal/service`, `pkg` and
  `framework` — require no module outside the SDK: their go.mod files name
  each other and nothing else.

The benefit ADR 0001 bought the split for never happened: every internal
module was released at the version `pkg` was, 78 times out of 78. What the
split cost is the list above, plus a consumer's go.mod naming five SDK modules
for one product (`pkg`, `framework` and the three internal ones, which Go's
module graph pruning lists as `// indirect`), eighteen go.mod files rewritten
per release, and four CI lanes and the vulnerability scan carrying a skip for
the empty root module ADR 0157 §5 left behind.

The reason ADR 0017 refused to make the root module public — it "would drag
the root module's heavy vendor deps onto every consumer" — is gone: the
vendors left the root for one module each (ADR 0157) and the codecs became
native (ADR 0156). ADR 0009 refused a single module because it "changes the
tag scheme … for no architectural gain"; the gain is this record's Context.

## Decision

### 1. One Go module, `github.com/kitsunium/sdk`

The module at the repository root holds `internal/kernel`, `internal/core`,
`internal/service`, `pkg` (`pkg/v1/...`) and `framework`. Import paths do not
change — `github.com/kitsunium/sdk/pkg/v1/...`,
`github.com/kitsunium/sdk/framework/...` — and neither does the `internal/`
firewall. A release is ONE tag `vX.Y.Z` on that module and ONE GitHub release.
The next release is **v0.18.0**: it continues `pkg`'s numbering, which was at
v0.17.0.

### 2. The modules that require a vendor stay modules, tagged when they change

The thirteen modules that require a vendor — `third-party/{aws, codec/hcl,
codec/protobuf, codec/yaml, db/writer/clickhouse, db/writer/mysql,
db/writer/redis, transform, x-crypto}` and `framework/connectors/{mysql,
postgres, sqlite, ssh}` — stay in the repository, each an isolated module, so
a consumer of one integration still takes that vendor's graph and no other
(ADR 0157). Each is tagged ONLY WHEN IT CHANGES, with the version of the SDK
release that carries the change: `<dir>/vX.Y.Z`. No GitHub release is made
for a vendor module; the SDK release's notes list the vendor tags it cut. The
first consolidated release changes every vendor go.mod, so it tags all
thirteen once; afterwards a typical release is the single root tag.

### 3. Old tags stay, and a consumer migrates with one command

The 346 tags stay: the module proxy has cached them, and deleting a tag is
irreversible for everyone who fetched it. No tombstone tag is cut either. A
consumer migrates with one command (below), documented here, in the README
and in the pull request.

### 4. How a release computes what it tags

- **Whether** (compute-bumps.sh): one token, `sdk`, when a path that can
  carry a consumer-visible change changed since the last release — under
  `pkg/`, `framework/` or `internal/` (the last through the rdeps rule, whose
  universe now includes `//third-party/...`, since the vendor modules import
  `internal/` directly), under a vendor module, or the root `go.mod`, `LICENSE`
  or `README.md`, which a consumer of the module inherits or reads on
  pkg.go.dev. Nothing else at the root counts: docs, scripts, lanes and the
  workspace ship in the module zip and no consumer compiles or reads them.
- **The version**: the newest `vX.Y.Z`, or — until the first one exists —
  the newest `pkg/vX.Y.Z`, bumped by the size a maintainer's label set
  (ADR 0135). The range the size is read over opens at that tag's first
  parent, as before (ADR 0085).
- **The first root tag is a bootstrap.** It is the first release of the SDK
  module's content, and the proxy and the checksum database keep a version
  forever — the path itself is known to them only at `v0.0.0`, a 2024 tag of an
  earlier history that the repository no longer holds — so it is held like ADR
  0009's very first release: an automatic run exits 3 and publishes nothing,
  and a maintainer's dispatch of SDK Release — which passes `--allow-bootstrap`
  — cuts it once the module zip and a clean-room `go get` are validated.
- **Which vendor modules**: each one `go.work` names beside `.`, measured from
  the FIRST PARENT of its own last tag — the release commit rewrote its go.mod,
  so measured from the tag every module would always look changed —, with the
  maintainer-only files of ADR 0089 left out. One with no tag is tagged.
- **What is published**: a detached release commit whose first parent is the
  main commit the release was cut from — even when it rewrites nothing, so the
  next range and every module's next measurement start where they must — on
  which each tagged vendor module's go.mod requires the SDK at `vX.Y.Z` and
  replaces nothing; the SDK's tag, then the vendor tags, pushed atomically. A
  vendor module that did not change keeps its last tag, whose go.mod requires
  the SDK release it was cut with; minimum version selection takes the newer
  SDK a consumer requires. Its code is the code CI builds against today's SDK,
  so the old tag works against the new SDK.

## Consumer migration

```sh
go get github.com/kitsunium/sdk@v0.18.0 \
  github.com/kitsunium/sdk/pkg@none \
  github.com/kitsunium/sdk/framework@none \
  github.com/kitsunium/sdk/internal/kernel@none \
  github.com/kitsunium/sdk/internal/core@none \
  github.com/kitsunium/sdk/internal/service@none
go mod tidy
```

and, in the same `go get`, `github.com/kitsunium/sdk/<dir>@v0.18.0` for each
vendor or connector module the go.mod requires. Import paths do not change.

The three internal modules are not optional. A go.mod on `pkg` v0.17.0 lists
them as `// indirect` after `go mod tidy` — module graph pruning lists every
module that provides a package of the build — and the command without them
leaves them in the build list beside the SDK module, which provides the same
packages. Measured in a consumer outside the repository: `ambiguous import:
found package github.com/kitsunium/sdk/internal/kernel/errs in multiple
modules`. With them, the go.mod is reduced to one requirement and the build
passes (§As built).

## Consequences

- A release is 1 tag and 1 GitHub release; v0.18.0 is 14 tags — the SDK and
  the thirteen vendor modules — and 1 GitHub release.
- A consumer's go.mod requires one SDK module. Measured: a program importing
  `pkg/v1` packages and `framework/kit` has `go list -m all` = itself and the
  SDK, and links no package outside the standard library and the SDK.
- The module zip is the repository minus the nested modules: 5 256 files,
  28.8 MiB uncompressed and 12.4 MB compressed at v0.18.0, of which 3.4 MiB is
  not Go — the ADRs, the docs site's sources, scripts, lanes. Before, a
  consumer of `pkg` and the framework downloaded five zips holding the same
  code without them, 25.3 MiB uncompressed at v0.17.0. pkg.go.dev now shows the
  repository's `README.md` on the module page, which `pkg` had none of.
- The layer direction kernel → core → service → pkg/v1 → framework is checked
  by the build graph alone (`scripts/check-layer-deps.sh`, ADR 0068). The
  module boundaries used to refuse a wrong-way import in a `GOWORK=off` build
  too — `internal/kernel`'s go.mod required nothing —; in workspace mode and
  under Bazel they never did, and the graph check runs in `make lint` and in
  the required CI job.
- **A new consumer must require the module before it tidies.** Go resolves an
  import that nothing in go.mod provides to the module with the LONGEST path
  that provides it at its latest version, and the retired `…/pkg` and
  `…/framework` keep providing `…/pkg/v1/*` and `…/framework/*` at v0.17.0 —
  measured against a proxy serving both: a fresh module importing them, after a
  bare `go mod tidy`, requires `…/pkg` and `…/framework` v0.17.0 and the three
  internal modules, not the SDK at v0.18.0. `go get github.com/kitsunium/sdk@latest`
  first makes the SDK module the one provider in the build list, and the
  install documentation says so. The cure Go offers — a last version of each
  retired module that provides no package, or retracts the others — is a
  tombstone tag, which §3 refuses; it stays the lever if new consumers keep
  landing on v0.17.0, and `sdkguard`'s notice catches the ones that do.
- The census lanes skip no module: the root is the SDK, so the skips ADR 0157
  §5 put in `cross-build`, `test-386`, `e2e-cross` and `vuln-check.sh` are
  gone, and govulncheck answering "no packages" for the root fails like any
  other module.
- `framework/kit` describes a build's kit and SDK from one module: both say
  `github.com/kitsunium/sdk`, at the same version. Positions in the SDK's own
  tests are relative to the repository root (`framework/internal/kit/...`),
  and the tests that needed a second Go module of the build simulate one,
  since the test binary links none.
- `tools/sdkguard`'s freshness probe reads `github.com/kitsunium/sdk`; a
  go.mod still on `…/pkg` is told the migration command once the SDK module
  has a release.
- The docs site's version list reads the root tags beside the `pkg/vX.Y.Z`
  history, as one list.

## Breaking changes

For a consumer, the requirement changes, not an import path: the command
above. Until v0.18.0 is published nothing changes for anyone. After it, a
go.mod that keeps `…/pkg` v0.17.0 keeps building at v0.17.0, and one that
requires both the SDK module and a module it replaced fails with an ambiguous
import — the command removes them together. A new consumer that runs
`go mod tidy` before requiring the SDK module gets the retired modules at
v0.17.0 (§Consequences): the install is `go get github.com/kitsunium/sdk@latest`.
In `framework/kit`'s graph, the build's `kit` and `sdk` both name
`github.com/kitsunium/sdk`.

## Alternatives considered

The owner was offered four ways to stop a release from tagging what did not
change.

- **One module, vendor modules tagged when they change — chosen.**
- **The vendor modules in a repository of their own.** No nested module in
  the SDK, and a vendor release that never touches the SDK's tags. Refused:
  every vendor module imports `internal/` packages of the SDK — measured, all
  nine under `third-party/`, and the ssh connector imports
  `internal/kernel/errs` — and Go allows an `internal/` import only from a
  package whose path shares the parent's prefix,
  `github.com/kitsunium/sdk/...`. Another repository would have to publish
  those mechanisms in `pkg/v1` first, and the two histories, CI lanes and
  release pipelines would drift apart.
- **One module for all the vendors.** One more tag per release instead of up
  to thirteen. Refused: a consumer of the S3 writer would take ClickHouse,
  Redis, HCL and the rest — exactly the 174-module graph ADR 0157 measured and
  undid.
- **Lockstep, as before, with one module for the SDK.** Fourteen tags per
  release, thirteen of them for modules that did not change, and a new version
  of every vendor module for a consumer's tooling to report. Refused: it keeps
  the noise this record exists to remove.

## As built

- **Modules.** `go.mod` at the root is the SDK; the go.mod and go.sum of
  `internal/kernel`, `internal/core`, `internal/service`, `pkg` and
  `framework` are deleted. `go.work` uses `.` and the thirteen vendor
  modules; `e2e`, `tools/genindex` and `tools/sdkguard` stay outside it. Every
  vendor module and `e2e` require `github.com/kitsunium/sdk` through a
  relative `replace` at `v0.0.0-00010101000000-000000000000`, what `go mod
  tidy` writes for a replaced, unpublished requirement; the release pins it.
  `GOWORK=off go mod tidy -diff` is clean in every module of the census.
- **Bazel.** `bazel mod tidy` replaces the five `com_github_kitsunium_sdk_*`
  repositories of `use_repo` by `com_github_kitsunium_sdk`, the vendor
  modules' requirement of the root, which no target references. The nested
  `# gazelle:prefix` lines of the merged directories are gone — the root's
  prefix yields the same import paths, and Gazelle regenerates no BUILD file —
  and `pkg/BUILD.bazel`, whose only purpose was letting `go_deps` load
  `//pkg:go.mod`, is deleted.
- **The framework's runtime.** `framework/internal/kit` reads the build's kit
  and SDK versions from `github.com/kitsunium/sdk`; its tests expect
  positions under `framework/internal/kit/`, and the tests of a module of
  another Go module make `github.com/kitsunium/sdk/pkg` one for their
  duration.
- **Lanes.** `setup-go` reads the root `go.mod`; the bazel job caches on
  every module's go.sum, the root having none; `make test-framework` runs
  `./framework/...` in the SDK module, then each connector module; the root
  skips are gone, and `scripts/ci/test-ci-scripts.bats` asserts it.
- **The release.** `scripts/release/lib/tag-format.sh` holds the shapes
  (`vX.Y.Z`, `third-party/...`, `framework/connectors/...`; `pkg/vX.Y.Z` read
  as history) and `vendor_modules`, which refuses a `go.work` that uses
  `./pkg`, `./framework` or an `internal/` directory again. `cut-tags.sh` runs
  its publication subshell under its own `set -e`: under `( … ) || rc=$?` bash
  ignored errexit, so a refused go.mod printed its refusal and was released
  anyway — seen red in the new suite before the fix. SDK Release makes one
  GitHub release and lists the vendor tags in its notes.
- **Measured**, in consumers outside the repository. One requiring
  `github.com/kitsunium/sdk` through a `replace` builds and links nothing but
  the standard library and the SDK. Then against a file proxy serving the
  v0.17.0 modules as published and the SDK module at v0.18.0 — its zip built
  from this tree by `golang.org/x/mod/zip`, 5 256 files, none refused —, with
  no `replace` at all: a consumer on `pkg` and `framework` v0.17.0 fails with an
  ambiguous import after `go get github.com/kitsunium/sdk@v0.18.0` alone, and
  after the command without the internal modules; with the command above it
  builds, its go.mod holding one requirement. A fresh module importing the
  packages resolves to `…/pkg` and `…/framework` v0.17.0 under a bare
  `go mod tidy`, and to the SDK module at v0.18.0 once
  `go get github.com/kitsunium/sdk@latest` ran first. A consumer of
  `framework/connectors/postgres` v0.17.0 migrates with the same command, the
  connector upgraded in it.

## References

- `scripts/release/` and its BATS suites — the release this record describes.
- `go.mod`, `go.work` — the SDK module and the workspace.
- [Go Modules Reference](https://go.dev/ref/mod) — §Module zip files (what a
  module's zip holds and leaves out: the nested modules), §Resolving a package
  to a module (the ambiguous import), §Module graph pruning (why a consumer's
  go.mod lists the internal modules).
- `go help internal` — an `internal/` import is allowed only below the
  parent of the `internal` directory.
