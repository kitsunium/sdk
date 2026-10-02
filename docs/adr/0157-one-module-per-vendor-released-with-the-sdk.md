# ADR 0157 — one module per vendor, released with the SDK

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0012](0012-logger-writer-registry.md) (the AWS writers in the root module; its rejected alternative, "a dedicated new module", is the one taken), [ADR 0022](0022-sdk-codec-hcl.md) and [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (HCL quarantined in the root module — the quarantine stands, its module changes), [ADR 0023](0023-sdk-schema-codecs.md) (the schema codecs in the root module), [ADR 0066](0066-third-party-compressors.md) (`third-party/transform` in the root module), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) §9 (the release chain gains the vendor modules)
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

It requires `pkg` and the `internal/` modules it reaches, and nothing in the
SDK requires it. The layer firewall is unchanged: `third-party/` is above
everything but the framework (ADR 0068, 0147). The quarantine reasons of
ADR 0022/0034, 0023 and 0066 stand; only the module each one lives in changes.

### 3. Released with the SDK

Each vendor module is a workspace module, so it is in the release chain ADR
0147 §9 reads from `go.work` and is tagged in lockstep with it —
`third-party/<path>/vX.Y.Z` at the version `pkg` is cut at — with no script
edited. It is in the module census (ADR 0137) because git tracks its `go.mod`,
so every lane that loops over modules builds, vets, tests on 32 bits and scans
it.

### 4. Integration-only code lives in `e2e`

A package that holds only suites run against real engines —
`third-party/db/sql` — moves to the `e2e` module, the auxiliary module outside
`go.work` where the SDK is already exercised against real kernels. A vendor
module's own integration tests may stay beside its code behind the
`integration` tag; their requirements are then that module's and nobody
else's.

### 5. The root module carries no vendor

Once the ssh identity has left for the framework (ADR 0158) and the SQL suites
for `e2e`, the root module holds no package and requires no vendor. It stays
what it was before ADR 0012: the workspace's anchor beside `go.work`,
`MODULE.bazel` and the root `BUILD.bazel`, and a member of the census.

## Consequences / Semantics

- **Implemented by the reorganisation series**: the nine modules, their
  `go.work` entries and Bazel wiring, the move of `third-party/db/sql` to
  `e2e`, and the release automation that tags them. This record changes no
  code.
- A consumer of the S3 writer requires `github.com/kitsunium/sdk/third-party/aws`
  at a release tag, and its module graph gains the AWS SDK and the SDK's own
  modules — not ClickHouse, not Redis, not testcontainers.
- The ADRs that cite `third-party/db/sql` (0139, 0140, 0143, 0151) keep the path
  of their time; the suites they name live in `e2e`.
- More modules mean more `go.sum` files and longer module-looping lanes; the
  census makes that automatic rather than a list to keep.

## Breaking changes

An importer of a `third-party/` package now requires that package's module
instead of the root module — the import path is unchanged, the `require` line
changes. Nothing outside `third-party/` changes.

## Alternatives considered

- **Keep the root module and tag it.** Cheap, and a consumer could pin a
  release; it would still take nineteen vendors' requirements to use one.
- **One module per package.** Splits the AWS writers, which share one SDK, and
  the two x/crypto schemes, which share one module — two `go.mod` files over
  the same graph, released and scanned twice.
- **Move the integrations into `pkg` behind build tags.** Rejected by ADR 0012
  already: a tag still records the requirement in `go.mod`.

## Deferred

- Independent versions for a vendor module, released apart from the chain —
  the same deferral as ADR 0147's for the framework.

## References

- The root `go.mod` and `go list -m all` in the root module, on the tree this
  record was written against.
- `git tag` — no tag names the root module.
