# third-party/aws/writer/s3/

## Purpose

Registers the **"s3"** logger writer (ADR 0012). A consumer blank-imports this
package — `import _ "github.com/kitsunium/sdk/third-party/aws/writer/s3"` — to make
`logger.NewMulti(..., WriterSpec{Name: "s3", Config: logger.S3Config{…}})`
resolve. This is the **only** way the AWS SDK enters a build: the dep is declared
in the **root umbrella `go.mod`** (which hosts `third-party/*`), and nothing
requires the root module — so `pkg/v1` consumers keep a zero-AWS module graph.

## Contents

| File | Role |
|---|---|
| `s3.go`          | `Writer` singleton, `s3Factory` (`Name` / `Open`); composes the chain |
| `s3sink.go`      | `s3Sink` batching terminal sink + `uploadFunc` seam (AWS-free, unit-tested) |
| `client.go`      | `newUploadFunc` — returns the AWS PutObject closure (**only** AWS-importing file) |
| `cred_adapter.go`| `credAdapter` — bridges `writer.CredentialProvider` → `aws.CredentialsProvider` |
| `codes.go`, `errors.go` | sentinels — range 0.3.35.\* (`ClientInitFailed`, `PutFailed`) |

## Delivery model

`Open` composes **`levelgate(async(s3Sink))`**:

- `s3Sink` coalesces formatted records into one uploaded object per batch —
  flushed at `MaxBatchBytes` (default 1 MiB), on the `FlushEvery` ticker, or on
  Flush/Close. Object key = `Prefix + <unixnano>-<seq>.log`.
- `async` (middleware) gives the non-blocking ring + producer-side `OnDrop`.
- `levelgate` applies the per-writer `MinLevel` floor.

Upload failures seen by the background drainer/ticker are routed to
`S3Config.OnError`; `Flush`/`Close` propagate the error to the caller.

## Credentials

`S3Config.Credentials` (a `writer.CredentialProvider`) is **required** in v1 —
a nil provider returns `ClientInitFailed`. The default AWS credential chain is
intentionally not wired (it would pull the heavyweight `aws config` module).
`credAdapter` re-fetches credentials on each `Retrieve` so rotation works; the
redacting `CredentialValue` keeps secrets out of logs.

## Testing (three layers)

1. **Unit** — the batching logic is fully exercised offline by injecting a
   recording closure into the `uploadFunc` seam (`s3sink_internal_test.go`).
2. **Contract (hermetic, in CI)** — `TestUploadFuncContract` runs the **real**
   `client.PutObject` path using the AWS SDK's own in-process test seam: a
   smithy `Initialize` stub middleware (`Options.APIOptions`) captures the
   `PutObjectInput` and short-circuits before the network. This covers the
   closure that fakes can't reach — no Docker, no network.
3. **Integration (LocalStack, opt-in)** — `localstack_test.go`, behind the
   `//go:build localstack` tag, points `S3Config.Endpoint` at a LocalStack
   container, does a real `PutObject`, and reads the object back. Excluded from
   `bazel test //...`; run with `docker run localstack` + `go test -tags localstack`.

`S3Config.Endpoint` (optional, path-style) enables layer 3 and also serves
real S3-compatible backends (MinIO, GovCloud). Zero ktn-linter deviations.

## Do NOT

- Import this package from `pkg/v1/*` or the dep-light modules — it pulls AWS.
- Move the batching logic into `client.go` — keep it AWS-free behind `uploadFunc`.
- Wire the default credential chain without re-evaluating the dependency cost.

## Verification

```sh
# from the repo root (root umbrella module)
GOWORK=off go test -race -cover ./third-party/aws/writer/s3/...
```
