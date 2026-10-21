# ADR 0185 — an export waits on disk, and the forwarder says what it lost

- **Status**: Accepted
- **Date**: 2026-10-21
- **Accepted**: 2026-10-21, decided autonomously by the SDK maintainers for
  kitsunium/sdk#493. It can be revisited while the SDK is v0
  ([ADR 0040](0040-changing-a-published-shape-while-v0.md)): it adds one new
  package and one sibling port per signal, and changes no published shape.
- **Deciders**: SDK maintainers
- **Extends**: [ADR 0054](0054-sdk-queue-domain.md) (the queue it stands on),
  [ADR 0048](0048-sdk-metrics-otlp-json.md) and
  [ADR 0051](0051-sdk-trace-domain.md) (the OTLP/HTTP exporters, which gain a
  sibling port), [ADR 0026](0026-sdk-resilience-domain.md) (the retry it runs)
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero
  values), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (siblings), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) and
  [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md)
  (where it may live), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md)
  (the injected clock), [ADR 0111](0111-a-mail-spool-retries-on-a-backoff-and-resends-only-after-a-crash.md)
  (the mail spool, its nearest relative), [ADR 0149](0149-a-product-reports-numbers-on-a-port-and-an-exporter-only-emits.md)
  (the framework's telemetry), [ADR 0183](0183-a-message-may-wait-for-its-instant-and-recurrence-stays-the-schedulers.md)
  (`RetryAfter`), kitsunium/sdk#479, kitsunium/sdk#493

## Context

A collector that is down loses the telemetry of every process that exports to
it. `trace.BatchSink` counts the spans it could not export (`Failed`) and
drops them; a metrics export loop drops its snapshot; the logger's
`nettransport` keeps a ring in memory. kitsunium/sdk#479 delivered the pieces
around the gap — `ExportContext` on both signals, `BatchSink`, the
classification `OTLPRetryable`, the collector's `Retry-After` carried as the
`retry_after_seconds` field that `resilience.RetryAfter` reads — and left the
gap itself, "store and forward", to an ADR.

The state of the art keeps the export on disk between the producer and the
network:

- the OpenTelemetry Collector's `exporterhelper`: a `sending_queue` that the
  `storage` extension (`file_storage`) makes persistent, consumed by
  `num_consumers` workers, each batch retried by `retry_on_failure`
  (exponential, `initial_interval` 5 s, `max_interval` 30 s,
  `max_elapsed_time` then dropped), a full queue refusing the new batch;
- Vector's disk buffer: a write-ahead log bounded by `max_size`, its
  `when_full` either `block` (the default) or `drop_newest`;
- Fluent Bit's `storage.type filesystem`, chunks on disk under
  `storage.total_limit_size`, the oldest discarded when it is reached.

The SDK already owns each half of that: `data/queue` is a durable,
at-least-once queue on a directory or a SQL table, with a lease the consumer
renews (`AutoExtend`), a stop that hands back what it did not finish
(`DrainTimeout`, kitsunium/sdk#491), a nack after a delay of its caller's
(`RetryAfter`, ADR 0183) and a dead-letter store; `app/resilience.NewRetry`
retries on an injected clock, waiting the delay a failure imposes
(`RetryConfig.Delay`, kitsunium/sdk#492). What is missing is the mechanism
between them: bounds, a policy when the bound is reached, the retry of one
batch, the verdict on a batch the collector refused, the counters an operator
reads, and the two adapters that put a signal's batch in and take it out.

## Decision

### D1 — Where it lives: `app/forward`, generic over bytes, beside the mail spool

The mechanism is a package of the `app` family: `internal/service/app/forward`
(the engine), `internal/core/app/forward` (its codes, `0.3.93.*`, declared in
the core at the engine's path — ADR 0160) and the facade
`pkg/v1/app/forward`. It is generic: a batch is `[]byte`, sent by a
`Sender func(ctx, batch []byte) error` its caller supplies. Two adapters in
the same package put a signal's batch in — `NewSpanExporter` (a trace
`ContextSpanExporter`) and `NewMetricExporter` (a metrics `ContextExporter`).

The placement is decided by the module graph, not by taste:

- **Not the kernel.** It needs a queue, and the kernel is stdlib-only and
  generic (rule 1); a queue is a domain.
- **Not `observe`.** The retry it runs is `app/resilience`'s, and the
  `pkg/v1/app` module already requires `pkg/v1/observe`: an `observe` package
  requiring `app` would close a cycle between two modules (ADR 0171 §7).
  Rewriting a retry inside `observe` would be the second retry policy the
  OTLP exporters refused to hide (ADR 0048).
- **Not `data`.** `pkg/v1/data` requires neither `observe` nor `app`; the
  adapters need the first and the retry the second.
- **`app`**, then, the family of the mechanisms above `data` and `observe`
  (ADR 0155): it requires both, and the mail spool (ADR 0111) — a durable
  outbox over the same queue, retried on a backoff, dead-lettered with its
  failure — is already there. The two are siblings and share nothing but the
  queue: a mail has an identity, a Message-ID and a transport; a batch has
  none of the three.

It is generic because nothing in it is telemetry: a webhook, an audit log
shipped to a SIEM or an event stream to a partner is the same loop — a bounded
backlog on disk, one sender, a verdict per answer. Only the two adapters know
a signal.

### D2 — The batch is one queue message, encoded as the OTLP/JSON request

One message per batch, never per span: a message costs one file or one row
and one fsync, so a span per message would multiply the cost of the durable
write by the batch size. The bytes are the OTLP/JSON request document the
signal's `EncodeOTLPJSON` already writes — exactly what the OTLP/HTTP exporter
posts. The consumer posts them unchanged, so a batch is encoded once, at the
producer, and a process that restarts with a different version re-sends what
the old one wrote without decoding it.

OTLP/protobuf stays deferred (ADR 0048). It would make the stored batch
unreadable without a schema, and the SDK has no protobuf encoder in the
standard library (ADR 0156).

To post a document already encoded, each signal's OTLP/HTTP exporter
implements a **sibling port** (ADR 0039): `trace.DocumentExporter` and
`metrics.DocumentExporter`, one method,
`ExportDocument(ctx, doc []byte) error`, with the classification of
`ExportContext` — same codes, same `retry_after_seconds`. The exporter's
constructor and `ContextSpanExporter` / `ContextExporter` are unchanged; a
caller reaches the sibling by type assertion, as every queue sibling is
reached. The shared OTLP sender stays the one place the HTTP exchange is
written (`observe/internal/otlp`).

### D3 — One queue per signal; ordering by one consumer, by default

A forwarder is one queue and one sender. Two signals are two forwarders on two
queues (two directories, or two tables): their collectors' paths differ, a
metrics backlog must not evict traces, and each gets its own bound.

`Config.Parallelism` is the number of consumers, clamped to 1. One consumer
keeps the queue's order while nothing fails, and is also the retry budget: a
collector that is down receives one request at a time from a process, however
large its backlog — never the backlog times the attempts. A caller whose
collector is the bottleneck raises it and gives up the order (`Consume`'s own
contract, ADR 0054 §4).

### D4 — The bound: batches AND bytes, a default for each, never unbounded

`Config.MaxBatches` (default `DefaultMaxBatches`, 10 000 — the Collector's
`queue_size` order of magnitude) and `Config.MaxBytes` (default
`DefaultMaxBytes`, 256 MiB — Vector's minimum disk buffer). Either reached is
full. Zero is the default and negative is refused (ADR 0031): an unbounded
store on disk fills the disk of the process it was meant to protect. A batch
larger than `MaxBytes` on its own is refused (`BatchTooLarge`) and counted
dropped: no policy can make room for it.

The forwarder counts what it holds — the batches it published and has not
yet acknowledged, dead-lettered or dropped. `New` starts the count from the
queue itself when the broker is a `queue.MessageReader` (all three SDK brokers
are), so a restarted process knows the backlog the last one left. The count is
the process's: a second process writing into the same queue is not seen, and a
message the broker dead-letters on its own (a consumer that kept dying on it,
`MaxDeliveries` lapsed leases) is counted until the next start. The dead-letter
store is outside the bound: it holds what the collector refused for good,
which is rare, evidence, and the operator's to purge (`DeadLetterManager`).

### D5 — When full: `DropOldest` (the zero), `DropNewest`, or `Block` for a bounded time

`Config.WhenFull` is a `FullPolicy`:

- **`DropOldest`, the zero.** The oldest batch the consumer is not sending is
  taken out of the queue (received and acknowledged) until the new one fits,
  each counted `Dropped`; the new one is kept. Telemetry is worth more the
  newer it is, and Fluent Bit does the same. It is a safe zero: it never
  blocks the producer, never loses the newest data, and is bounded. When no
  batch can be taken — the only one queued is the one being sent — it falls
  back to `DropNewest`.
- **`DropNewest`**: the new batch is refused with `Full` and counted
  `Dropped` (the Collector's behaviour, Vector's `drop_newest`). For an export
  whose history matters more than its present.
- **`Block`**: the producer waits for room up to `Config.BlockTimeout`
  (default `DefaultBlockTimeout`, 5 s; negative refused) on the forwarder's
  clock, then is refused with `Full` and counted `Dropped`. Vector's `block`
  waits forever; this one cannot, because a producer that waits forever is a
  `BatchSink` whose buffer then drops in memory, out of the forwarder's sight.
  Behind a `BatchSink` the wait is also bounded by its `ExportTimeout` — the
  context `Enqueue` is handed.

`Full` is a typed error (`FORWARD_FULL`, 503), so a `BatchSink` in front counts
the spans `Failed` as it did for a collector that was down — the difference
is that now the operator also reads `Dropped`.

### D6 — The retry: one batch, `resilience.NewRetry`, the collector's delay honoured, the age as budget

The consumer is `data/queue`'s `Consume`, one message at a time,
`AutoExtend` on — the lease is renewed while a batch is retried, so a long
outage keeps the batch leased by one consumer instead of letting it lapse to
another — and `HandlerIsIdempotent` asserted: a batch the collector accepted
just before a crash is sent again by the next process, which OTLP tolerates
(a duplicate span or point) and which no forwarder can prevent (ADR 0054 §1).

Each delivery runs `resilience.NewRetry` on the forwarder's clock:

- `Retryable` is the caller's classification — `trace.OTLPRetryable`,
  `metrics.OTLPRetryable` — and required: its nil has two opposite readings,
  "retry everything" and "retry nothing".
- `Delay` waits what the collector said (`resilience.RetryAfter`, the
  `Retry-After` of a 429 or a 503), in full — not cut to the backoff ceiling,
  because the specification says the client SHOULD honour it — but never past
  the batch's age limit. Otherwise it waits `Config.Backoff.Delay(attempt)`,
  default `DefaultRetryBase` 5 s growing to `DefaultRetryMax` 30 s, the
  Collector's `retry_on_failure` numbers.
- Each attempt is bounded by `Config.SendTimeout` (default
  `DefaultSendTimeout`, 10 s, the OTLP specification's
  `OTEL_EXPORTER_OTLP_TIMEOUT`) on the clock.
- The budget is `Config.MaxAge` (default `DefaultMaxAge`, 24 h; negative
  refused), counted from the instant the queue accepted the batch, which
  survives a restart. Attempts are otherwise unbounded: a collector down for
  an hour is retried for an hour, one request at a time (D3). A batch older
  than `MaxAge` when it is delivered is not sent; one whose retryable failure
  finds it past `MaxAge` is not retried. Either is acknowledged and counted
  `Expired` — dropped, not dead-lettered: a dead letter is a verdict on a
  batch, an expiry a verdict on the outage, and a long one would otherwise
  fill the unbounded dead-letter store with every batch it delayed.

A shared `resilience.RetryBudget` is not offered: with one consumer the
forwarder never multiplies the load, which is what a budget prevents.

### D7 — A batch the collector refused is dead-lettered, once

A failure the classification calls permanent — a 400, a 413, a partial
success, any answer the specification forbids replaying — returns
`queue.DoNotRetry`: `Consume` dead-letters it through the broker's
`Rejecter`, with the collector's verdict as its recorded reason, code and
public cause, and it is counted `DeadLettered`. It is never sent again by the
forwarder; an operator who fixed the cause replays it with the broker's
`DeadLetterManager`.

### D8 — The counters: what is held, sent, retried, lost, and the last delay imposed

`Forwarder.Stats()` returns a `StatsValue`: `Pending` and `PendingBytes`
(what is held, D4), `Enqueued`, `Sent`, `Retries`, `Dropped` (D5), `Expired`
(D6), `DeadLettered` (D7), and `LastRetryAfter` with `LastRetryAfterAt` —
the last delay a collector imposed and when. They are counters a caller reads
and exports the way it chooses: the forwarder depends on no metrics package
and writes nothing anywhere itself (ADR 0149's division: the product
reports on a port). They are what the framework's Studio shows: the export
backlog, the rejections, the last `Retry-After`.

### D9 — The stop: bounded, and nothing in flight is lost

`Run(ctx)` consumes until `ctx` ends. The batch being sent may finish within
`Config.DrainTimeout` (zero: stopped at once); one it cut short is handed back
uncounted (`Releaser`) and sent by the next process; every outcome after the
stop is bounded by `Config.SettleTimeout` — both are `Consume`'s, forwarded
(kitsunium/sdk#491). A caller's stop budget covers `DrainTimeout +
SettleTimeout`. Nothing is buffered in memory: a batch `Enqueue` accepted is
on disk, so a stop, a crash or a `kill -9` loses no batch the forwarder said
it kept.

## Consequences

- New package `pkg/v1/app/forward` (`New`, `Forwarder` with `Enqueue`, `Run`,
  `Stats`, `Config`, `FullPolicy` and its three values, `Stats`, `Sender`,
  `NewSpanExporter`, `NewMetricExporter`, the defaults, the sentinels
  `Misconfigured`, `Full`, `BatchTooLarge`); new code range `0.3.93.*`
  (`internal/core/app/forward`).
- `trace.DocumentExporter` and `metrics.DocumentExporter`: one sibling port
  each, implemented by the OTLP/HTTP exporter. Nothing else in `observe`
  changes.
- An export path behind a `BatchSink` costs one durable publish (an fsync on
  the file broker) instead of a network round trip, and the network is the
  consumer's.
- The suites run on a `ManualClock` with no sleep: a restart that finds its
  batches, a collector down then back, a 429 whose `Retry-After` is waited
  in full, a 400 dead-lettered, each full policy; on the memory, file and SQL
  brokers, and on SQLite, PostgreSQL and MySQL in `e2e/integration/sql`.

## Alternatives considered

- **An option of the OTLP exporters** (`OTLPHTTPConfig.Queue`). Rejected:
  the exporters are in `observe`, the queue's retry is in `app` (D1), and it
  would make every exporter carry a queue its caller may not want.
- **`observe/export/durable`** (kitsunium/sdk#493's first proposal).
  Rejected by the module cycle of D1.
- **A forwarder per signal, typed** (`forward.Spans`, `forward.Metrics`).
  Rejected: the mechanism is the same bytes for both, and generic it also
  serves an export that is not telemetry; the typing is in the two adapters.
- **Retrying by nacking with a delay** (`NackAfter`) instead of retrying in
  the handler. Rejected: a batch waiting its delay is invisible, so while the
  collector is down every batch is, and `DropOldest` would find nothing to
  drop exactly when it matters; it also lets every batch try the dead
  collector once per round. Retrying the head in the handler keeps the backlog
  visible and the load at one request.
- **Dead-lettering an expired batch.** Rejected by D6.
- **`Block` without a bound** (Vector's default). Rejected by D5 and ADR 0031.
- **A queue per process shared by the signals.** Rejected by D3.

## Deferred

- **The framework**: `framework/telemetry` (ADR 0149) gains an opt-in OTLP
  subsystem with a persistent queue per signal in the product's data
  directory or its database, its bounds and policy declared as settings, and
  the exporter replaceable; the Studio shows the export backlog, the
  rejections and the last `Retry-After` from `Stats`. Outside this ADR.
- **The logger's `nettransport`** over a forwarder (kitsunium/sdk#479 P3).
- **OTLP/protobuf** (ADR 0048).
- **A count shared between processes** writing into one queue (D4).

## References

- OpenTelemetry Collector, `exporter/exporterhelper/README.md`
  (`sending_queue`, `storage`, `retry_on_failure`).
- Vector, *Buffering model* (`when_full`, disk buffers).
- OTLP specification, §OTLP/HTTP Throttling and §Failures.
- Impl: `internal/core/app/forward`, `internal/service/app/forward`,
  `pkg/v1/app/forward`, `internal/core/observe/{trace,metrics}`
  (`DocumentExporter`), `internal/service/observe/{trace,metrics}`
  (`ExportDocument`).
