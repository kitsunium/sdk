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
| `third-party/codec/yaml` | the module root — the opt-in `"yaml-full"` Format, beside the SDK's native `"yaml"` subset (ADR 0156) | `gopkg.in/yaml.v3` | `0.3.77.*` |
| `third-party/db/writer/clickhouse` | the module root | `github.com/ClickHouse/clickhouse-go/v2` | `0.3.33.*` |
| `third-party/db/writer/mysql` | the module root | `github.com/go-sql-driver/mysql` | `0.3.32.*` |
| `third-party/db/writer/redis` | the module root | `github.com/redis/go-redis/v9` | `0.3.34.*` |
| `third-party/transform` | the module root (zstd + s2) | `github.com/klauspost/compress` | `0.3.63.*` |
| `third-party/x-crypto` | `argon2id`, `xchacha` | `golang.org/x/crypto` | none — the shared `core/crypto` sentinels |

Entitlement's ssh `Identity` (ADR 0079), `third-party/entitlement` until ADR
0158, is not here any more: it is the framework's connector module
`framework/connectors/ssh`, beside the database engines. The root module is the
SDK itself — `internal/`, `pkg/`, `framework/` — since ADR 0162.

## Rules

- **A module is a vendor, not a package.** Two packages that share a vendor
  share a module — the s3 and cloudwatch writers both stand on the AWS SDK,
  argon2id and xchacha both on `x/crypto`, zstd and s2 on one compression
  library. Two modules for one vendor would double the release surface and
  save a consumer nothing; one module for two vendors would hand every consumer
  of one the other's graph, which is the defect ADR 0157 removes.
- **The import paths did not change.** `github.com/kitsunium/sdk/third-party/aws/writer/s3`
  is still the package; what changed is the module that hosts it.
- **Each module requires the SDK module, and replaces it in development.**
  Its `go.mod` requires `github.com/kitsunium/sdk` — whose `internal/` packages
  it imports, which Go allows from a path under `github.com/kitsunium/sdk/` —
  and replaces it with this tree, as `framework/connectors/*` do; the release
  commit that tags the module drops the `replace` and pins the SDK at the
  version being cut (ADR 0162).
- **Each module is in `go.work`**, so Bazel's `go_deps` resolves its vendor and
  a release may tag it; git tracks its `go.mod`, so every census lane
  (ADR 0137) builds, vets, tests and scans it like any other module. Its root
  directory carries a `BUILD.bazel` with its `gazelle:prefix` —
  `third-party/aws` and `third-party/x-crypto` hold no package, and their
  `BUILD.bazel` exists only so `//third-party/aws:go.mod` and
  `//third-party/x-crypto:go.mod` load.
- **Tagged when it changes** (ADR 0162): a release tags a module here only
  when one of its files that a consumer sees changed since the module's own
  last tag — or when it has none —, at the version of the SDK release that
  carries the change: `third-party/aws/vX.Y.Z` beside the SDK's `vX.Y.Z`. A
  module that did not change keeps its last tag, whose go.mod requires the SDK
  release it was cut with. No GitHub release is made for it; the SDK release's
  notes list the vendor tags it cut. Up to v0.17.0 every module was tagged in
  lockstep with `pkg/vX.Y.Z` (ADR 0157 §3).
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
reaches nothing, in workspace mode or not — every directory there belongs to a
vendor module and none to the SDK module the root is —, and
`./third-party/aws/...` reaches that module's packages in workspace mode and
nothing with `GOWORK=off`. Loop over `bash scripts/ci/go-modules.sh` to reach
every module (ADR 0137).

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
- Write a `third-party/<path>/v…` tag by hand: a release tags the modules that
  changed, at its own version (`scripts/release/cut-tags.sh`, ADR 0162).
