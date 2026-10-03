# ADR 0157 — one module per vendor, released with the SDK

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0012](0012-logger-writer-registry.md) (the AWS writers in the root module; its rejected alternative, "a dedicated new module", is the one taken), [ADR 0015](0015-sdk-logger-writer-taxonomy-and-rotation.md) §D2 (the `vendor-root` tier, a writer whose vendor lives in the root module, has no placement left), [ADR 0022](0022-sdk-codec-hcl.md) and [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (HCL quarantined in the root module — the quarantine stands, its module changes), [ADR 0023](0023-sdk-schema-codecs.md) (the schema codecs in the root module), [ADR 0066](0066-third-party-compressors.md) (`third-party/transform` in the root module), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) §9 (the release chain gains the vendor modules)
- **Related**: [ADR 0007](0007-sdk-release-and-versioning.md) (lockstep tags), [ADR 0079](0079-the-entitlement-split-x-sys-was-never-in-the-mechanism.md) (a dependency split along the package it enters through), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the module census), [ADR 0139](0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md) / [ADR 0140](0140-sqlites-migration-lock-is-the-database-files-write-lock.md) / [ADR 0151](0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md) (the SQL integration suites), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principle 5), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (`yaml-full`), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) (the ssh identity leaves for the framework)

## Context

ADR 0012 put the AWS writers in the root module rather than a module of their
own, reasoning that the root was "already isolated, no-one-requires-it", so a
dedicated module would add a `go.mod` "for no benefit". ADR 0022, 0023 and
0066 followed it for HCL, Protobuf and the vendor compressors, and the
database writers, the x/crypto schemes, the ssh identity and the SQL
integration suites joined them.

Isolation from `pkg` held. Isolation between vendors never existed. Measured on
this tree: the root module requires 23 modules directly — 19 of them vendors:
the AWS SDK, ClickHouse, MySQL, PostgreSQL and Redis clients, HCL, Protobuf,
`klauspost/compress`, `x/crypto`, a SQLite driver, `moby` and five
testcontainers modules — and `go list -m all` there names 174. A program that
wants the S3 writer requires the root module and takes every one of those
requirements into its own module graph. And the root module has never been
tagged — `pkg`, the three `internal/` modules and the framework's have — so it
does so at a pseudo-version, with nothing a consumer can pin to a release.
ADR 0012 had expected the opposite ("releasing the AWS writers tags the root
module"); the release chain was built without it (ADR 0079: "the root module
is never tagged"), so no `third-party/` package has ever been published under
a version.

Two of the root's packages are not integrations at all: `third-party/db/sql`
holds no production code, only the suites that run the SQL mechanisms on real
engines, and `third-party/entitlement` is the ssh implementation of a port
whose domain is leaving the SDK (ADR 0158).

## Decision

### 1. A vendor is a module

Each vendor integration under `third-party/` is a Go module of its own:

| Module | Vendor dependency | Packages |
|---|---|---|
| `third-party/aws` | the AWS SDK | `writer/cloudwatch`, `writer/s3` |
| `third-party/db/writer/clickhouse` | `clickhouse-go` | the ClickHouse writer |
| `third-party/db/writer/mysql` | `go-sql-driver/mysql` | the MySQL writer |
| `third-party/db/writer/redis` | `go-redis` | the Redis writer |
| `third-party/codec/hcl` | `hashicorp/hcl/v2` | the HCL codec |
| `third-party/codec/protobuf` | `google.golang.org/protobuf` | the Protobuf codec |
| `third-party/codec/yaml` | `gopkg.in/yaml.v3` | the Format `yaml-full` (ADR 0156) |
| `third-party/transform` | `klauspost/compress` | zstd and s2 |
| `third-party/x-crypto` | `golang.org/x/crypto` | argon2id and XChaCha20-Poly1305 |

The unit is the vendor, not the package: two packages over one vendor library
share a module, and two vendors never do. A consumer of one integration takes
that vendor's requirements and nobody else's.

### 2. A vendor module sits where `third-party/` always sat

It requires the SDK modules it imports — the `internal/` ones, and `pkg` where
it imports a facade — and nothing in the SDK requires it. The layer firewall is unchanged: `third-party/` is above
everything but the framework (ADR 0068, 0147). The quarantine reasons of
ADR 0022/0034, 0023 and 0066 stand; only the module each one lives in changes.

### 3. Released with the SDK

Each vendor module is a workspace module, so it is in the release chain ADR
0147 §9 reads from `go.work` and is tagged in lockstep with it —
`third-party/<path>/vX.Y.Z` at the version `pkg` is cut at — with no script
edited. It is in the module census (ADR 0137) because git tracks its `go.mod`,
so every lane that loops over modules builds, vets, tests on 32 bits and scans
it.

What the release scripts learn is the tag's shape and a token, not a list:
`lib/tag-format.sh` accepts `third-party/<dir>/vX.Y.Z` (`THIRD_PARTY_TAG_REGEX`)
and `chain_modules` orders the vendor modules after the framework's, by name;
`compute-bumps.sh` emits the token `third-party` for a change under
`third-party/` (its rule 1c), which `cut-tags.sh` and the SDK Release dispatch
accept; and the vendor tags are listed among the consumer-facing ones, each of
which gets a GitHub Release. A path that can cut a release can size it (ADR
0089): `lib/release-scope.sh` counts `third-party/` paths — and `framework/`
ones, which ADR 0147 had left out, so `cut-tags.sh` walked past a
framework-only merge when it sized a range, and a `release:minor` label on one
shipped as a patch.

### 4. Suites against real engines live in `e2e`

A module's tests count in its `go.mod`: a writer module whose integration test
starts a container would put testcontainers, `moby` and their graph in the
module graph of every consumer of that writer. So the suites that run against
real engines — `third-party/db/sql`, which holds nothing else, and the database
writers' Docker-backed tests — move to the `e2e` module, the auxiliary module
outside `go.work` that nothing requires and where the SDK is already exercised
against real kernels. They keep their `integration` tag, so the lanes that
build and test `e2e` on every platform compile none of them, and a vendor
module requires its vendor and the SDK alone. `third-party/db/sql` becomes
`e2e/integration/sql` and each `third-party/db/writer/<engine>/*_integration_test.go`
becomes `e2e/integration/writer/<engine>`, moved unchanged; their run procedure
is `e2e/integration/CLAUDE.md` (rule 12). The AWS writers' `localstack` suites
stay in `third-party/aws`: they need nothing that module does not already
require.

### 5. The root module carries no vendor

Once the ssh identity has left for the framework (ADR 0158) and the SQL suites
for `e2e`, the root module holds no package and requires no vendor. It stays
what it was before ADR 0012: the workspace's anchor beside `go.work`,
`MODULE.bazel` and the root `BUILD.bazel`, and a member of the census.

## Consequences / Semantics

- **Implemented by the reorganisation series.** Its vendor-module step created
  eight of the nine modules — every one but `third-party/codec/yaml`, which
  arrives with the YAML subset (ADR 0156) — with their `go.work` entries and
  Bazel wiring (a module-root `BUILD.bazel` carrying its `gazelle:prefix`; for
  `third-party/aws` and `third-party/x-crypto`, which hold no package at their
  root, a `BUILD.bazel` whose only purpose is that `go_deps` can load their
  `go.mod`), moved the suites against real engines to `e2e`, and taught the
  release automation the new tags. Until the ssh identity leaves (ADR 0158),
  the root module still requires `x/crypto` for it.
- **What a consumer's module graph holds**, measured with
  `GOWORK=off go list -m all` in each module (the module itself and the SDK's
  `internal/*` included). The root module named 174 before the split. At the
  split, while `internal/service` still required the vendor codecs, and on the
  integrated tree once ADR 0156 had made them native:

  | Module | At the split | With the native codecs |
  |---|---|---|
  | `third-party/aws` | 30 | 21 |
  | `third-party/codec/hcl` | 37 | 31 |
  | `third-party/codec/protobuf` | 20 | 12 |
  | `third-party/db/writer/clickhouse` | 94 | 88 — the driver's own graph, which lists testcontainers it never builds |
  | `third-party/db/writer/mysql` | 20 | 11 |
  | `third-party/db/writer/redis` | 27 | 20 |
  | `third-party/transform` | 19 | 10 |
  | `third-party/x-crypto` | 23 | 14 |
  | the root (the ssh identity) | 24 | 15 |
  | `e2e` (auxiliary: the suites, testcontainers, `moby`, the drivers) | 159 | 147 |
- testcontainers and `moby` are required by `e2e` alone: they leave `go.work`,
  and `bazel mod tidy` drops them from `use_repo`.
- `cut-tags.sh` publishes eight more tags and eight more GitHub Releases per
  release — nine once `third-party/codec/yaml` exists.
- A pattern does not cross a module boundary. From the repository root,
  `go test ./third-party/aws/...` works in workspace mode; with `GOWORK=off` it
  reaches nothing, and each package's verification command runs from its
  module's directory.
- A consumer of the S3 writer requires `github.com/kitsunium/sdk/third-party/aws`
  at a release tag, and its module graph gains the AWS SDK and the SDK's own
  modules — not ClickHouse, not Redis, not testcontainers.
- The ADRs that cite `third-party/db/sql` (0139, 0140, 0143, 0151) keep the path
  of their time; the suites they name live in `e2e`.
- More modules mean more `go.sum` files and longer module-looping lanes; the
  census makes that automatic rather than a list to keep.
- ADR 0015's `vendor-root` tier — a writer whose vendor already lives in the
  root `go.mod` — has no placement left: every vendor writer is `third-party`,
  in a module of its own.

## Breaking changes

An importer of a `third-party/` package now requires that package's module
instead of the root module — the import path is unchanged, the `require` line
changes (`go get github.com/kitsunium/sdk/third-party/aws@<version>`). No
tagged release is affected: the root module never had one. Nothing outside
`third-party/` changes.

## Alternatives considered

- **Keep the root module and tag it.** Cheap, and a consumer could pin a
  release; it would still take nineteen vendors' requirements to use one.
- **One module per package.** Splits the AWS writers, which share one SDK, and
  the two x/crypto schemes, which share one module — two `go.mod` files over
  the same graph, released and scanned twice.
- **Move the integrations into `pkg` behind build tags.** Rejected by ADR 0012
  already: a tag still records the requirement in `go.mod`.
- **Keep the writers' integration tests in their modules.** testcontainers would
  sit in each writer module's `go.mod`, and therefore in its consumers' graph.
- **A dedicated, test-only auxiliary module for the suites.** Every file in it
  is behind the `integration` tag, so under the default tags it holds no
  package: `go vet ./...` and `go test ./...` exit 1 on "no packages", and each
  census lane (ADR 0137) would need a named skip — five places. `e2e` is
  auxiliary already, and its default build is unaffected by tagged files.

## Deferred

- Independent versions for a vendor module, released apart from the chain —
  the same deferral as ADR 0147's for the framework.
- A generated `README.md` per vendor module, for pkg.go.dev.

## Verification

```sh
bash scripts/ci/go-modules.sh            # the vendor modules are in the census
bash scripts/ci/go-modules.sh | while IFS= read -r m; do
  (cd "$m" && GOWORK=off go build ./... && GOWORK=off go vet ./...)
done
cd third-party/aws && GOWORK=off go list -m all | grep -c testcontainers   # 0
bazel build //... && bash scripts/check-layer-deps.sh
bats scripts/release/test-compute-bumps.bats scripts/release/test-cut-tags.bats
cd e2e && GOWORK=off go vet -tags integration ./integration/...
```

## References

- The root `go.mod` and `go list -m all` in the root module, on the tree this
  record was written against.
- `git tag` — no tag names the root module.
- `third-party/CLAUDE.md` — the modules, their rules, how each is tested.
- `e2e/integration/CLAUDE.md` — the suites against real engines and their run
  procedure.
- `scripts/release/lib/tag-format.sh` — `THIRD_PARTY_TAG_REGEX`, `chain_modules`;
  `scripts/release/lib/release-scope.sh` — `rs_releasable`.
