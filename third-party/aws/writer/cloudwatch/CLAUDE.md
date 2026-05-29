# third-party/aws/writer/cloudwatch/

## Purpose

Registers the **"cloudwatch"** logger writer (ADR 0012). A consumer blank-imports
this package — `import _ "github.com/kitsunium/sdk/third-party/aws/writer/cloudwatch"` —
to make `logger.NewMulti(..., WriterSpec{Name: "cloudwatch", Config:
logger.CloudWatchConfig{…}})` resolve. Like the s3 writer, this is one of the
only two places the AWS SDK enters a build — declared in the **root umbrella
`go.mod`** that hosts `third-party/*`, which no other module requires (so
`pkg/v1` consumers keep a zero-AWS module graph).

## Contents

| File | Role |
|---|---|
| `cloudwatch.go`  | `Writer` singleton, `cwFactory` (`Name` / `Open`); composes the chain |
| `cwsink.go`      | `cwSink` batching terminal sink + `deliverFunc` seam (AWS-free, unit-tested) |
| `cw_event.go`    | `cwEvent` value type (timestamp + message) |
| `client.go`      | `newDeliverFunc` — returns the AWS PutLogEvents closure (**only** AWS-importing file) |
| `cred_adapter.go`| `credAdapter` — bridges `writer.CredentialProvider` → `aws.CredentialsProvider` |
| `codes.go`, `errors.go` | sentinels — range 0.3.25.\* (`ClientInitFailed`, `PutFailed`) |

## Delivery model

`Open` composes **`levelgate(async(cwSink))`**:

- `cwSink` turns each record into one `InputLogEvent` (carrying its
  `RecordEvent.Time`, falling back to now when zero) and coalesces events into a
  single `PutLogEvents` call — flushed at the event cap (default 1000, under the
  CloudWatch 10000 ceiling), on the `FlushEvery` ticker, or on Flush/Close.
- `async` (middleware) gives the non-blocking ring + producer-side `OnDrop`.
- `levelgate` applies the per-writer `MinLevel` floor.

Delivery failures seen by the background drainer/ticker route to
`CloudWatchConfig.OnError`; `Flush`/`Close` propagate the error to the caller.

## Credentials

`CloudWatchConfig.Credentials` is **required** in v1 — a nil provider returns
`ClientInitFailed`. `credAdapter` re-fetches credentials on each `Retrieve`.

## Testing (three layers)

1. **Unit** — the batching logic is exercised offline by injecting a recording
   closure into the `deliverFunc` seam (`cwsink_internal_test.go`).
2. **Contract (hermetic, in CI)** — `TestDeliverFuncContract` runs the **real**
   `client.PutLogEvents` path via the AWS SDK's smithy `Initialize` stub
   middleware: it captures the `PutLogEventsInput` (group/stream + every event's
   message and ms timestamp) and short-circuits before the network.
3. **Integration (LocalStack, opt-in)** — `localstack_test.go`, behind the
   `//go:build localstack` tag, points `CloudWatchConfig.Endpoint` at a
   LocalStack container, does a real `PutLogEvents`, and reads the events back.
   Excluded from `bazel test //...`.

`CloudWatchConfig.Endpoint` (optional) enables layer 3. Zero ktn-linter
deviations.

## Do NOT

- Import this package from `pkg/v1/*` or the dep-light modules — it pulls AWS.
- Move the batching logic into `client.go` — keep it behind `deliverFunc`.

## Verification

```
GOWORK=off go test -race -cover ./third-party/aws/writer/cloudwatch/...
```
