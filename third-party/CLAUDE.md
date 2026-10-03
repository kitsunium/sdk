# third-party/ — one Go module per vendor

The SDK's integrations whose implementation is somebody else's library: a
logger writer, a codec, a compressor, a cryptographic scheme (ADR 0012). Each
vendor is a Go module of its own (ADR 0157), so a consumer that imports one
integration requires that module and the vendor behind it, and nothing of the
others. A consumer opts in with a blank import; the import path names the
package, `go get` names the module.

| Module | Packages | Vendor | Codes |
|---|---|---|---|
| `third-party/aws` | `writer/s3`, `writer/cloudwatch` | `github.com/aws/aws-sdk-go-v2` (+ `smithy-go`) | `0.3.35.*` s3, `0.3.25.*` cloudwatch |
| `third-party/codec/hcl` | the module root | `github.com/hashicorp/hcl/v2` | `0.3.37.*` |
| `third-party/codec/protobuf` | the module root | `google.golang.org/protobuf` | `0.3.38.*` |
| `third-party/db/writer/clickhouse` | the module root | `github.com/ClickHouse/clickhouse-go/v2` | `0.3.33.*` |
| `third-party/db/writer/mysql` | the module root | `github.com/go-sql-driver/mysql` | `0.3.32.*` |
| `third-party/db/writer/redis` | the module root | `github.com/redis/go-redis/v9` | `0.3.34.*` |
| `third-party/transform` | the module root (zstd + s2) | `github.com/klauspost/compress` | `0.3.63.*` |
| `third-party/x-crypto` | `argon2id`, `xchacha` | `golang.org/x/crypto` | none — the shared `core/crypto` sentinels |

`third-party/entitlement` is the exception: entitlement's ssh `Identity`
(ADR 0079) is still a package of the ROOT module, which nothing requires and
the release chain leaves untagged.

## Rules

- **A module is a vendor, not a package.** Two packages that share a vendor
  share a module — the s3 and cloudwatch writers both stand on the AWS SDK,
  argon2id and xchacha both on `x/crypto`, zstd and s2 on one compression
  library. Two modules for one vendor would double the release surface and
  save a consumer nothing; one module for two vendors would hand every consumer
  of one the other's graph, which is the defect ADR 0157 removes.
- **The import paths did not change.** `github.com/kitsunium/sdk/third-party/aws/writer/s3`
  is still the package; what changed is the module that hosts it.
- **Each module requires `internal/*` exactly, and replaces it in development.**
  Its `go.mod` replaces `internal/{kernel,core,service}` with this tree, as
  `framework/connectors/*` do; the release commit drops every intra-SDK
  `replace` and pins the requires at the version being cut.
- **Each module is in `go.work`**, so Bazel's `go_deps` resolves its vendor and
  the release chain tags it; git tracks its `go.mod`, so every census lane
  (ADR 0137) builds, vets, tests and scans it like any other module. Its root
  directory carries a `BUILD.bazel` with its `gazelle:prefix` —
  `third-party/aws` and `third-party/x-crypto` hold no package, and their
  `BUILD.bazel` exists only so `//third-party/aws:go.mod` and
  `//third-party/x-crypto:go.mod` load.
- **Released in lockstep** with the chain (ADR 0147 §9): the tag carries the
  module's directory, `third-party/aws/vX.Y.Z`, at the same version as
  `pkg/vX.Y.Z`. `compute-bumps.sh` emits the token `third-party` for a change
  here; any token cuts the whole chain once.
- **The layer rule is unchanged:** `third-party/` sits above `internal/` and
  `pkg/` and beside the framework — nothing in `internal/`, `pkg/` or
  `framework/` imports it, and it imports nothing of the framework
  (`scripts/check-layer-deps.sh`, ADR 0068, ADR 0147).

## Test

Each module is tested in its own directory, as a consumer builds it:

```sh
cd third-party/aws && GOWORK=off go test -race ./...   # standalone: its own go.mod and replaces
go test -race ./third-party/aws/...                     # workspace mode, from the repository root
bazel test //third-party/...                            # every vendor module under Bazel
```

A pattern never crosses a module boundary: from the root, `./third-party/...`
reaches only the root module's package (entitlement), and with `GOWORK=off`
`./third-party/aws/...` reaches nothing at all, because the root module no
longer contains it. Loop over `bash scripts/ci/go-modules.sh` to reach every
module (ADR 0137).

The opt-in suites behind a build tag keep the run procedure their package
`CLAUDE.md` gives (rule 12). The AWS writers' `localstack` suites stay in
`third-party/aws`: they need nothing beyond the AWS SDK. The database writers'
`integration` suites start their servers with testcontainers, so they live in
the auxiliary `e2e` module, `e2e/integration/writer/<engine>` — kept here, each
would put testcontainers in its module's `go.mod`, and so in the graph of every
consumer of that writer (ADR 0157). The SQL mechanisms' suite on real engines,
`third-party/db/sql` until then, is `e2e/integration/sql`.

## Do NOT

- Add a vendor to a module that already has one. A new vendor is a new module:
  a `go.mod`, a `go.work` line, a module-root `BUILD.bazel` with its prefix.
- Import a `third-party/` package from `internal/`, `pkg/` or `framework/`.
- Add a test that imports testcontainers, moby or a driver the module does not
  ship to a module here: tests count in a module's `go.mod`. It goes to
  `e2e/integration/`.
- Write a `third-party/<path>/v…` tag by hand: the release chain cuts every
  module of `go.work` at one version (`scripts/release/cut-tags.sh`).
