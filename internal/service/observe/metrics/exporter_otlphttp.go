// Package metrics — OTLP/HTTP emitter: the exporter that POSTs an encoded
// snapshot to a collector. The transport itself — endpoint refusal, the default
// client and its own pool, the bounded reads, the classification — is the one
// both signals share (internal/service/observe/internal/otlp); what is this signal's is
// the path, the vocabulary every verdict is named in, and the encoder.
package metrics

import (
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// OTLPMetricsPath is the URL path OTLP/HTTP reserves for the metrics signal.
// The specification names it under §OTLP/HTTP Request; an endpoint is a full
// URL used as-is, so a caller spells the path themselves and this constant is
// what they spell it with.
const OTLPMetricsPath string = "/v1/metrics"

// DefaultOTLPTimeout bounds one export round trip when OTLPHTTPConfig leaves
// Timeout unset. Ten seconds is the value the OpenTelemetry protocol exporter
// specification itself defaults OTEL_EXPORTER_OTLP_TIMEOUT to, so it is a
// clamp onto the specification's own number rather than a figure this SDK
// invented (ADR 0031).
const DefaultOTLPTimeout time.Duration = otlp.DefaultTimeout

// DefaultOTLPMaxResponseBytes caps how much of a collector's response body is
// read. A conforming response is an ExportMetricsServiceResponse or a Status
// message — hundreds of bytes. The cap exists because the body is the one part
// of this exchange a REMOTE party controls its length of, and an exporter that
// read it whole would hand a hostile or broken collector a way to exhaust the
// process that is only trying to report its metrics.
//
// It also bounds the drain that recycles the connection after the verdict —
// always this default rather than OTLPHTTPConfig.MaxResponseBytes, because
// that knob bounds what is read into memory and the drain keeps nothing.
const DefaultOTLPMaxResponseBytes int64 = otlp.DefaultMaxResponseBytes

// rejectedFieldKey is the errs Field key a partial success carries the
// rejected count under. It never carries remote-controlled text.
const rejectedFieldKey string = "rejected_data_points"

// otlpSignal is this signal's vocabulary for the shared OTLP/HTTP sender: its
// own codes (0.3.45.7-10) and its own wording, byte for byte what this
// exporter returned before the transport was shared.
var otlpSignal = otlp.SignalSpec{
	EndpointInvalid: OTLPEndpointInvalid,
	EndpointUnparsable: errs.WrapParams{
		Code:    CodeOTLPEndpointInvalid,
		Reason:  "OTLP_ENDPOINT_INVALID",
		Public:  "The OTLP endpoint is not an absolute http or https URL with a path",
		Private: "service/observe/metrics: the configured OTLP endpoint could not be parsed as a URL",
	},
	RequestUnbuildable: errs.WrapParams{
		Code:    CodeOTLPExportRejected,
		Reason:  "OTLP_EXPORT_REJECTED",
		Public:  "The OTLP collector rejected the metrics payload",
		Private: "service/observe/metrics: the OTLP/HTTP request could not be built",
	},
	TransportFault: errs.WrapParams{
		Code:    CodeOTLPExportUnavailable,
		Reason:  "OTLP_EXPORT_UNAVAILABLE",
		Public:  "The OTLP collector is unreachable or overloaded",
		Private: "service/observe/metrics: the OTLP/HTTP request failed before a response was read",
	},
	Rejected:       OTLPExportRejected,
	Unavailable:    OTLPExportUnavailable,
	PartialSuccess: OTLPPartialSuccess,
	RejectedField:  rejectedFieldKey,
	DecodeRejected: decodeRejected,
}

// otlpHTTPExporter POSTs each snapshot to an OTLP collector as OTLP/JSON.
//
// It holds no mutable state, so unlike the writer-bound exporters it needs no
// mutex: the sender is safe for concurrent use, and every request builds its
// own body from its own snapshot.
type otlpHTTPExporter struct {
	// name is the exporter's registry-shaped name; it is never registered.
	name coremetrics.ExporterName
	// sender is the shared transport, speaking this signal's vocabulary.
	sender *otlp.Sender
}

// NewOTLPHTTPExporter returns an Exporter that encodes each snapshot as
// OTLP/JSON and POSTs it to cfg.Endpoint.
//
// It is the EMITTER half of this package's OTLP support; EncodeOTLPJSON is the
// encoder half and this constructor adds nothing to it but transport. The split
// is why an encoding bug and a network bug are never the same investigation.
//
// It is deliberately NOT registered. The registry is reached by importing a
// package, and arming a network client from an import is strictly worse than
// the stdout hazard ADR 0030 already refuses: there is no endpoint that could
// be a correct default, and a wrong one turns every Export into a POST at
// whatever answers on that address.
//
// It does NOT retry, and that is a decision rather than an omission. The
// specification asks a client to back off exponentially on a retryable status,
// this SDK already ships that policy in internal/service/resilience, and a
// backoff hidden inside Export would be a second one a caller cannot see, tune
// or cancel. What this exporter provides instead is the CLASSIFICATION a retry
// policy needs:
//
//	runner := resilience.NewRetry(resilience.RetryConfig{
//	    MaxAttempts: 3,
//	    BaseDelay:   time.Second,
//	    Retryable:   metrics.OTLPRetryable,
//	})
//
// A construction failure is returned rather than deferred into a Runner that
// always fails: nothing here publishes a signature that forces the deferral,
// and refusing at wiring time is strictly earlier than refusing at the first
// scrape.
func NewOTLPHTTPExporter(name coremetrics.ExporterName, cfg OTLPHTTPConfig) (exporter coremetrics.Exporter, err error) {
	//: the shared transport, refusing an unusable endpoint under this
	//: signal's code before anything is constructed.
	sender, senderErr := otlp.NewSender((*otlp.HTTPConfig)(&cfg), &otlpSignal)
	//: surface the typed refusal; nothing is constructed.
	if senderErr != nil {
		//: the refusal already carries this signal's code and wording.
		return nil, senderErr
	}
	//: bind the name the exporter answers to.
	return &otlpHTTPExporter{name: name, sender: sender}, nil
}

// OTLPRetryable reports whether err is an OTLP/HTTP failure the specification
// says may be replayed: a transport fault, or one of HTTP 429 / 502 / 503 /
// 504. Everything else — a rejected payload, a partial success, an
// unrepresentable snapshot — is false, because replaying identical bytes at a
// collector that already refused them only costs the retry budget.
//
// Its signature is exactly resilience.RetryConfig.Retryable's, so it drops
// straight into a retry policy without an adapter. That is the whole reason
// this exporter classifies instead of looping.
func OTLPRetryable(err error) bool {
	//: one code carries the transient verdict; HasCode walks the wrap trail.
	return errs.HasCode(err, CodeOTLPExportUnavailable)
}

// Name implements core/observe/metrics.Exporter.
func (e *otlpHTTPExporter) Name() coremetrics.ExporterName {
	//: the exporter's registry-shaped name (it is not registered).
	return e.name
}

// Export encodes snap and POSTs it, returning a typed verdict.
//
// An encoding refusal returns before any socket is touched; everything after
// it — the request, the bounded read, the drain, the verdict — is the shared
// sender's, under this signal's codes.
func (e *otlpHTTPExporter) Export(snap coremetrics.SnapshotValue) error {
	//: encode first — an unrepresentable snapshot never reaches the network.
	body, err := EncodeOTLPJSON(snap)
	//: the sentinel already carries the code, reason and public message.
	if err != nil {
		//: surface the typed refusal.
		return err
	}
	//: transport and classification.
	return e.sender.Post(body)
}
