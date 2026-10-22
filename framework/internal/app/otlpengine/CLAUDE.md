<!-- updated: 2026-10-22T00:00:00Z -->
# framework/internal/app/otlpengine — the OTLP export

What `framework/app/otlp` plugs into the framework (`plug.OTLP`, issue #483,
ADR 0185): the export `app.OTLP` declares. The product's spans — every span
the framework starts, through the app's tracer's sink — and the metrics the
framework derives from them (`app.node.runs`, `app.node.failures`,
`app.node.duration`, per node and operation) and from its own export
(`app.otlp.*`, per signal) each wait in a queue of their own — the SDK's
`data/queue`, durable in `<data>/.otlp/<signal>`, in memory without a data
directory and where the SDK's durable queue refuses the platform — and are
forwarded by the SDK's `app/forward` to an OpenTelemetry collector over
OTLP/HTTP (each signal's `DocumentExporter` posting the stored document as it
is), or to the product's own sender. The framework's core imports none of
`app/forward`, `data/queue`, the OTLP exporters, the trace batch sink or the
meter: it opens the export with a `plug.OTLPConfig` and holds it as a
`plug.OTLPExporter`, and a program that does not import `framework/app/otlp`
links none of this package (`framework/app`'s
`TestTheSmallestProductLinksNoSubsystem`) and is refused at the start for an
export it declares (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `otlpengine.go` | `Enable`, which sets `plug.OTLP`; `open` — a signal each (`openSignal`: `senderOf`, `openQueue`, `forward.New`), then the trace `BatchSink` in front of the trace forwarder (`forward.NewSpanExporter`), the metric exporter in front of the metrics forwarder (`forward.NewMetricExporter`) and the meter; `openQueue` — the durable queue in the signal's subdirectory, memory without a directory or where the SDK answers `UnsupportedPlatform`; `senderOf` — the product's sender (its failures classified by the core's `Retryable`), else the signal's OTLP/HTTP exporter at the endpoint plus the signal's path, its `ExportDocument` and its `OTLPRetryable`; `fullPolicyOf`; `closeQueue` |
| `export.go` | `export`, the open export: `SpanSink` (the batch sink's), `Record` (one run, a failure, a duration, the instruments made at a node's first unit of work), `Stats` (each signal's `forward.Stats`, its bounds and whether it is durable), `Run` (the forwarders and the metrics' collection in one `concur/group`; at the end the batch sink shut down and one last snapshot, each within the settle on the app's clock, then the forwarders stopped within their drain, then the queues closed), `observeStats` (the export's own counts as observable instruments) |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK stamps and waits on it — a test's manual clock included |
| `otlpengine_compliance.go` | the compile-time assertions: the export is a `plug.OTLPExporter`, the clock the SDK's |
| `harness_external_test.go`, `otlpengine_external_test.go` | every behaviour against an `httptest` collector on a manual clock that reports each wait it arms — the driver advances past each wait shorter than ten minutes but the batch sink's export bound, never past an attempt's or a lease's, and never sleeps: a span sent with the endpoint's headers; a collector down then back (the same batch three times, each attempt a backoff after the one before); a 429's `Retry-After` waited in full and said by the stats; a 400 dead-lettered after one attempt; a backlog that survives a restart over the same directory (on a platform whose durable queue the SDK refuses, the memory fallback asserted, then a skip); the stop's flush of the batch sink and of one last snapshot, sent by the next process; the product's sender and its classification; an endpoint and a bound refused at the open |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug` and the SDK (`app/forward`, `data/queue`, `observe/trace`, `observe/metrics`, `concur/group`, `clock`, `errs`, `proc`), never the framework's core nor another engine: the export keeps its queues itself and needs neither the store nor the queue subsystem — `framework/app/otlp` imports no other registration package.
- What a batch goes through is the SDK forwarder's, unchanged (ADR 0185): the bounds and the full policy, the age, the retry on its backoff with a collector's `Retry-After` waited in full, a permanent refusal dead-lettered, the counts. This package adds no retry, no classification of a collector's answer, and decodes no stored batch.
- The export never stops the product it observes: a queue the platform refuses is kept in memory and said (`Durable` false), never refused.
- A span is not exported twice: the tracer's sink is the batch sink's, and the export runs no span of its own — the forwarder's work is outside the framework's tracing, so an export never feeds itself.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/otlpengine/ && GOWORK=off go test -race -count=100 ./internal/app/otlpengine/ && GOWORK=off go test -race ./internal/app/ -run 'OTLP|ReachTheCollector|LostBatch|SenderFailures'
```
