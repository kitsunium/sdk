package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The OTLP export subsystem's contract (issue #483, ADR 0185): what the
// framework's core asks of the export its app.OTLP declares — the product's
// spans and the metrics the framework derives from them, each signal kept in
// a durable queue of its own and forwarded to a collector over OTLP/HTTP, or
// to the product's own sender. framework/app/otlp sets the hook and
// framework/internal/app/otlpengine implements it over the SDK's
// app/forward, data/queue, observe/trace and observe/metrics; the core
// imports none of the four through it. The values are here, the export in
// otlp_interface.go.

// The two signals an export forwards, as an OTLPBatchValue names them.
const (
	// OTLPTraces names the trace signal: an OTLP/JSON
	// ExportTraceServiceRequest.
	OTLPTraces string = "traces"
	// OTLPMetrics names the metrics signal: an OTLP/JSON
	// ExportMetricsServiceRequest.
	OTLPMetrics string = "metrics"
)

// The full policies an export's queue applies, as the setting spells them.
const (
	// OTLPDropOldest takes the oldest batches out until the new one fits.
	OTLPDropOldest string = "drop-oldest"
	// OTLPDropNewest refuses the new batch.
	OTLPDropNewest string = "drop-newest"
	// OTLPBlock makes the producer wait for room, up to a bound.
	OTLPBlock string = "block"
)

// OTLP is the OTLP export subsystem; nil until framework/app/otlp is
// imported. A product that declares app.OTLP without it is refused at the
// start (ADR 0179 §6).
var OTLP atomic.Pointer[OTLPDriverConfig]

// OTLPDriverConfig is what framework/app/otlp plugs into the framework: how
// an app's export opens.
type OTLPDriverConfig struct {
	// Open opens the export cfg describes: its two queues — read, the
	// backlog a previous process left counted —, its senders and its
	// meter. It sends nothing until OTLPExporter.Run.
	Open func(ctx context.Context, cfg *OTLPConfig) (OTLPExporter, error)
}

// OTLPConfig is what the framework asks of an export: who produces the
// telemetry, where it goes, where it waits, its bounds and the app's clock.
type OTLPConfig struct {
	// Clock is the app's clock: the queues stamp and age a batch on it, the
	// retries and each attempt's bound wait on it, the metrics are
	// collected on it.
	Clock Clock
	// Headers ride on every request to the collector — an Authorization, a
	// tenant —; they are the secret otlp-headers, never shown.
	Headers map[string]string
	// Send, when set, ships a batch in place of the OTLP/HTTP exporters:
	// the product's own sender. Retryable then classifies its failures.
	Send func(ctx context.Context, batch OTLPBatchValue) error
	// Retryable classifies a failure of Send: a later attempt may cure it.
	// Unused without Send: the OTLP/HTTP exporters classify their own.
	Retryable func(err error) bool
	// OnError hears what the export could not keep — a batch of spans or a
	// snapshot its queue refused — and what it could not close; the core
	// logs it. The forwarder counts every batch it loses on its own.
	OnError func(err error)
	// Service and Version name the producer: the resource's service.name
	// and the instrumentation scope's version.
	Service, Version string
	// Endpoint is the collector's base URL, "http://collector:4318": each
	// signal's path is appended to it. Unused with Send.
	Endpoint string
	// Dir is the directory the two queues lie in, one subdirectory per
	// signal; "" keeps them in memory, lost at a stop.
	Dir string
	// WhenFull is what a full queue does with a new batch: OTLPDropOldest,
	// OTLPDropNewest or OTLPBlock.
	WhenFull string
	// MaxBytes bounds the bytes each queue holds.
	MaxBytes int64
	// MaxBatches bounds the batches each queue holds.
	MaxBatches int
	// MaxAge is how long after it was queued a batch may still be sent.
	MaxAge time.Duration
	// SendTimeout bounds one attempt, and the hand-over of the spans the
	// batch sink exports to their queue.
	SendTimeout time.Duration
	// Interval is how often the metrics are collected and queued.
	Interval time.Duration
	// DrainTimeout is how long the batch being sent at a stop may still
	// run; SettleTimeout bounds each acknowledgement once the stop began,
	// and the flush of what the export still holds in memory.
	DrainTimeout, SettleTimeout time.Duration
}

// OTLPSinkValue is the export's span sink: the SDK's trace.SpanSink held
// as any, as a MailValue holds a mail — plug names no SDK type, and the core,
// which names the SDK's trace, takes it back.
type OTLPSinkValue struct {
	// Sink is the SDK's trace.SpanSink.
	Sink any
}

// OTLPMeterValue is the product's own meter on the export: the SDK's
// metrics.FullMeter held as any, as OTLPSinkValue holds the span sink.
type OTLPMeterValue struct {
	// Meter is the SDK's metrics.FullMeter.
	Meter any
}

// OTLPBatchValue is one batch as the product's own sender receives it: its
// signal and its OTLP/JSON document, posted to a collector as it is.
type OTLPBatchValue struct {
	// Signal is OTLPTraces or OTLPMetrics.
	Signal string
	// Document is the OTLP/JSON export request.
	Document []byte
}

// OTLPStatsValue is what one signal's queue holds, has sent and has lost,
// with its bounds.
type OTLPStatsValue struct {
	// LastRetryAfterAt is when the collector last imposed a delay; zero
	// when it never did.
	LastRetryAfterAt time.Time
	// Signal is OTLPTraces or OTLPMetrics.
	Signal string
	// PendingBytes and MaxBytes are the bytes the queue holds and may hold.
	PendingBytes, MaxBytes int64
	// LastRetryAfter is the last delay the collector imposed.
	LastRetryAfter time.Duration
	// Pending and MaxBatches are the batches the queue holds and may hold.
	Pending, MaxBatches int
	// Enqueued, Sent and Retries count the batches kept, sent and the
	// attempts made again.
	Enqueued, Sent, Retries uint64
	// Dropped, Expired and DeadLettered count the batches lost to a bound,
	// to their age and to a permanent refusal.
	Dropped, Expired, DeadLettered uint64
	// Durable reports whether the queue outlives the process: false in
	// memory — no directory, or a platform whose durable queue the SDK
	// refuses.
	Durable bool
}
