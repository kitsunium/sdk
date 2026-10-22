package app

import (
	"context"
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// The two signals an export forwards, as an [OTLPBatch] names them.
const (
	// OTLPTraces names the trace signal: an OTLP/JSON
	// ExportTraceServiceRequest.
	OTLPTraces string = iapp.OTLPTraces
	// OTLPMetrics names the metrics signal: an OTLP/JSON
	// ExportMetricsServiceRequest.
	OTLPMetrics string = iapp.OTLPMetrics
)

// What an export's full queue does with a new batch: the setting
// otlp-when-full spells it.
const (
	// OTLPDropOldest, the default: the oldest batches not being sent are
	// taken out, counted dropped, until the new one fits.
	OTLPDropOldest OTLPFullPolicy = iapp.OTLPDropOldest
	// OTLPDropNewest: the new batch is refused, counted dropped.
	OTLPDropNewest OTLPFullPolicy = iapp.OTLPDropNewest
	// OTLPBlock: the span or the snapshot waits for room a few seconds, then
	// is refused, counted dropped.
	OTLPBlock OTLPFullPolicy = iapp.OTLPBlock
)

// OTLPFullPolicy is what an export's full queue does with a new batch.
type OTLPFullPolicy = iapp.OTLPFullPolicy

// OTLPBatch is one batch as the product's own sender receives it
// ([OTLPSender]): its signal, [OTLPTraces] or [OTLPMetrics], and its
// OTLP/JSON document, which a collector takes as it is.
type OTLPBatch = iapp.OTLPBatchValue

// OTLPOption configures the app's export: [OTLPEndpoint], [OTLPBounds],
// [OTLPWhenFull], [OTLPMaxAge], [OTLPSendTimeout], [OTLPSender].
type OTLPOption = iapp.OpenTelemetryConfigurer

// OTLP declares that the app forwards its traces and the metrics the
// framework derives from them to an OpenTelemetry collector over OTLP/HTTP:
// every span the framework starts, and per node and operation its runs,
// failures and durations (app.node.runs, app.node.failures,
// app.node.duration), with what the export itself holds and lost
// (app.otlp.*). The export is on once its endpoint is set — the setting
// otlp-endpoint, [OTLPEndpoint] its default — or once [OTLPSender] replaces
// the network; a CLI command never exports. A program that declares it
// imports framework/app/otlp, or its start is refused, naming the import:
//
//	import _ "github.com/kitsunium/sdk/framework/app/otlp"
//
//	var App = app.NewApp("shop", catalog.Service).With(
//		app.OTLP(app.OTLPEndpoint("http://collector:4318")),
//	)
//
// Every value given here is the default of the setting that tunes it, which
// config/<env>.* and <PRODUCT>_OTLP_<KNOB> override. Given twice, the last
// wins.
//
//go:noinline
func OTLP(opts ...OTLPOption) AppOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLP(opts...)
}

// OTLPEndpoint is the collector's base URL — "http://collector:4318" —, each
// signal's path (/v1/traces, /v1/metrics) appended to it: the default of
// the setting otlp-endpoint. An absolute http or https URL with a host; the
// empty default leaves the export off.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPEndpoint(url string) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPEndpoint(url)
}

// OTLPBounds bounds each signal's queue: at most maxBatches batches and
// maxBytes bytes — the defaults of the settings otlp-max-batches (10000)
// and otlp-max-bytes (256 MiB). A batch larger than maxBytes on its own is
// dropped.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPBounds(maxBatches, maxBytes int) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPBounds(maxBatches, maxBytes)
}

// OTLPWhenFull is what a full queue does with a new batch — the default of
// the setting otlp-when-full: [OTLPDropOldest] (the default),
// [OTLPDropNewest] or [OTLPBlock].
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPWhenFull(p OTLPFullPolicy) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPWhenFull(p)
}

// OTLPMaxAge is how long after it was queued a batch may still be sent — the
// default of the setting otlp-max-age (24h): one older is dropped unsent,
// counted expired. A collector's Retry-After is waited in full, but never
// past it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPMaxAge(d time.Duration) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPMaxAge(d)
}

// OTLPSendTimeout bounds one attempt to send a batch — the default of the
// setting otlp-send-timeout (10s, OTEL_EXPORTER_OTLP_TIMEOUT's).
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPSendTimeout(d time.Duration) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPSendTimeout(d)
}

// OTLPSender replaces the network: each batch is handed to send instead of
// being posted to a collector — the product's own sender, a bridge to
// another transport. The queue, its bounds, its age and its retries stay
// the framework's. A failure send returns is tried again on the backoff —
// after the delay it carries when it is a resilience.RetryAfter's —, but for
// a refusal the caller must fix, an [Error] of a 4xx other than 408 and 429,
// which dead-letters the batch. The export is then on whatever otlp-endpoint
// says. A nil send keeps the network.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPSender(send func(ctx context.Context, batch OTLPBatch) error) OTLPOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OTLPSender(send)
}
