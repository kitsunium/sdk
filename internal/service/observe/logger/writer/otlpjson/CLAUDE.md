<!-- updated: 2026-11-03T15:00:00Z -->
# internal/service/observe/logger/writer/otlpjson/

## Purpose

The **third OpenTelemetry signal** (#867): the `"otlpjson"` writer factory
(ADR 0012, registered by name, no `init()`), its terminal sink, the OTLP/JSON
logs encoder and the OTLP/HTTP logs exporter. Traces and metrics already
left the process as OTLP/JSON (ADR 0048, ADR 0051) and waited on disk while
the collector was down (ADR 0185); logs now do too. Codes `0.3.104.*`,
declared in `internal/core/observe/logger/writer/otlpjson` (ADR 0160): this
package declares none.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/observe/logger/writer/otlpjson.yaml` (ADR 0167) |
| `writer.go` | `Writer` (the registered factory), `Config` (`Exporter` required; `Resource`, `Scope`, `MinLevel`, `BatchMax`, `BufferSize`, `OnDrop`, `OnError`), `DefaultBatchMax` (64), `factory.Open` — `levelgate(async(otlpSink))`, the drainer handing up to `BatchMax` records at once; no `Exporter` is the shared `WRITER_CONFIG_INVALID`, field named |
| `sink.go` | `otlpSink`: `Write` (a one-record document, a scratch reused), `WriteBatch` (one document for the backlog — delivered whole or none of it), `export` (encoded into the buffer reused under the mutex, handed to the exporter), `Flush` / `Close` (nothing held, nothing owned), `wrapCancelled` |
| `encode.go` | `EncodeOTLPJSON` and the append encoder: `appendRequest` (resourceLogs → resource, scopeLogs → scope, logRecords), `appendLogRecord` (field-number order: timeUnixNano, severityNumber, severityText, body, attributes, traceId, spanId), `severityNumber` (`level+9` held in 1…24), `appendAttrs` (a group flattened under `name.key`), `appendAnyValue` (the slog bridge's typing: uint64 past int64 a string, a duration its nanoseconds, a time its Unix nanoseconds, any other kind its rendered string) |
| `exporter.go` | `OTLPLogsPath`, `OTLPHTTPConfig` (a defined type over the shared `otlp.HTTPConfig`), `NewOTLPHTTPExporter` (a `DocumentExporter` and `EncodedDocumentExporter` over the shared sender), `OTLPRetryable`, `otlpSignal` (this signal's codes for every verdict), `decodeRejected` (`partialSuccess.rejectedLogRecords`) |
| `sink_compliance.go` | `otlpSink` is a `BatchSink`; the exporter both document ports |
| `encode_external_test.go` | the hand-written document for a record inside a span and one outside; the severity mapping; `Open`'s refusals; a backlog exported as documents, each refused record reported once |
| `sink_bench_test.go` / `BENCH.md` | a record's cost in a batch of 16 and alone: 0 allocations |

The end-to-end test through `app/forward` — a 429 with `Retry-After` waited
out on the forwarder's clock, the batch posted again once — is
`internal/service/app/forward/logs_external_test.go`.

## Why-this-shape

- **The record, not the encoded line.** A sink receives both; the LogRecord is
  built from the `RecordEvent` — the handler's bound attributes are merged in
  it — because OTLP wants typed attributes, a severity number and the trace
  pair as top-level fields, none of which survives the console encoding.
- **One document per batch, the exporter outside.** The writer encodes; who
  posts is `Config.Exporter`: the OTLP/HTTP exporter directly, or
  `forward.NewLogExporter` in front of a durable queue whose `Send` is the
  same exporter's `ExportDocument`. That is the shape the other two signals
  have (ADR 0185: one `DocumentExporter` per signal), the logs signal's
  encoding simply happening one step earlier.
- **Groups flattened, not `kvlistValue`.** The shared `otlp.AnyValue` carries
  the four scalars; dotted keys are what the semantic conventions name
  attributes with, and a backend indexes them without unnesting.

## Do NOT

- Retry inside the sink or the exporter: the forwarder's retry, or the
  caller's, is the only one.
- Retain a document past `ExportDocument`'s return in an exporter: the sink
  reuses the buffer.
- Echo a record's text into an error: a refusal carries the code and the
  collector's verdict, never the payload.

## Verification

```
cd internal && go test -race ./service/observe/logger/writer/otlpjson/ ./service/app/forward/
cd internal && go test -run '^$' -bench . -benchmem ./service/observe/logger/writer/otlpjson/
```
