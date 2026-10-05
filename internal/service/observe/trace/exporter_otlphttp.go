package trace

import (
	"time"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// OTLPTracesPath is the OTLP/HTTP path for the trace signal. It is a constant
// rather than something the exporter appends, so an endpoint stays a full URL a
// reviewer can read in one string: "http://collector:4318" + OTLPTracesPath.
const OTLPTracesPath string = "/v1/traces"

// DefaultOTLPTimeout is the OpenTelemetry protocol exporter specification's own
// default for OTEL_EXPORTER_OTLP_TIMEOUT. The clamp lands on the specification's
// number rather than on one this SDK invented.
const DefaultOTLPTimeout time.Duration = otlp.DefaultTimeout

// DefaultOTLPMaxResponseBytes caps the response body read at 1 MiB. The response
// is the one length a REMOTE party controls in this exchange; the REQUEST body
// is deliberately not capped, because its size is a property of the caller's own
// span volume, any SDK-chosen ceiling would be arbitrary (ADR 0031 §refuse), and
// the collector already answers 413 for one it will not take.
//
// It also bounds the drain that recycles the connection after the verdict —
// always this default rather than OTLPHTTPConfig.MaxResponseBytes, because
// that knob bounds what is read into memory and the drain keeps nothing.
const DefaultOTLPMaxResponseBytes int64 = otlp.DefaultMaxResponseBytes

// rejectedFieldKey is the errs Field key a partial success carries the
// rejected count under. It never carries remote-controlled text.
const rejectedFieldKey string = "rejected_spans"

// otlpSignal is this signal's vocabulary for the shared OTLP/HTTP sender: its
// own codes (0.3.50.5-8) and its own wording, byte for byte what this exporter
// returned before the transport was shared.
var otlpSignal = otlp.SignalSpec{
	EndpointInvalid: coretrace.OTLPEndpointInvalid,
	EndpointUnparsable: errs.WrapParams{
		Code:    coretrace.CodeOTLPEndpointInvalid,
		Reason:  "OTLP_ENDPOINT_INVALID",
		Public:  "The OTLP endpoint must be an absolute http(s) URL with a path",
		Private: "service/observe/trace: the configured OTLP endpoint could not be parsed as a URL",
	},
	RequestUnbuildable: errs.WrapParams{
		Code:    coretrace.CodeOTLPExportRejected,
		Reason:  "OTLP_EXPORT_REJECTED",
		Public:  "The trace collector rejected the export",
		Private: "service/observe/trace: the OTLP/HTTP request could not be built",
	},
	TransportFault: errs.WrapParams{
		Code:    coretrace.CodeOTLPExportUnavailable,
		Reason:  "OTLP_EXPORT_UNAVAILABLE",
		Public:  "The trace collector is unavailable",
		Private: "service/observe/trace: the OTLP/HTTP request failed before a response was read",
	},
	Rejected:       coretrace.OTLPExportRejected,
	Unavailable:    coretrace.OTLPExportUnavailable,
	PartialSuccess: coretrace.OTLPPartialSuccess,
	RejectedField:  rejectedFieldKey,
	DecodeRejected: decodeRejected,
}

// otlpHTTPExporter POSTs each batch to an OTLP collector as OTLP/JSON.
//
// It holds no mutable state, so unlike the writer-bound exporter it needs no
// mutex: the sender is safe for concurrent use, and every request builds its own
// body from its own batch.
type otlpHTTPExporter struct {
	// name is the exporter's name. It is NOT a registry key — this exporter is
	// never registered (see NewOTLPHTTPExporter) — it is what an error and a
	// diagnostic call it.
	name coretrace.ExporterName
	// sender is the shared transport, speaking this signal's vocabulary.
	sender *otlp.Sender
}

// NewOTLPHTTPExporter builds a SpanExporter that POSTs each batch to an OTLP
// collector as OTLP/JSON.
//
// # It is NOT registered, and that is a decision one step beyond ADR 0030
//
// ADR 0030 refuses to arm a WRITER on stdout from an import. Arming a NETWORK
// CLIENT from an import is strictly worse, and for a reason that is structural
// rather than a matter of degree: there is no endpoint that could be a correct
// default. `localhost:4318` is a guess, and a wrong guess turns every export into
// a POST at whatever answers on that address — inside a cluster, that is a real
// host belonging to somebody else. So this exporter is constructed explicitly, by
// a caller who names the collector, or not at all. A test pins its absence from
// AvailableExporters.
//
// # It does not retry
//
// The specification asks a client to honour Retry-After and otherwise back off
// exponentially. `internal/service/app/resilience` already ships that policy, and a
// backoff hidden inside Export would be a second one a caller cannot see, tune or
// cancel — Export has no context to cancel it with. What this exporter supplies
// instead is the CLASSIFICATION a retry policy needs, in the exact shape
// resilience.RetryConfig.Retryable wants:
//
//	resilience.NewRetry(resilience.RetryConfig{
//	    MaxAttempts: 3,
//	    BaseDelay:   time.Second,
//	    Retryable:   trace.OTLPRetryable,
//	})
//
// A construction failure is returned rather than deferred into an always-failing
// exporter: refusing at wiring time is strictly earlier than refusing at the
// first export.
func NewOTLPHTTPExporter(name coretrace.ExporterName, cfg OTLPHTTPConfig) (exporter coretrace.SpanExporter, err error) {
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

// otlpRetryable is OTLPRetryable's body: decl_gen.go writes OTLPRetryable, from the
// design, as one call of it.
func otlpRetryable(err error) bool {
	//: one code carries the transient verdict; HasCode walks the wrap trail.
	return errs.HasCode(err, coretrace.CodeOTLPExportUnavailable)
}

// Name implements core/observe/trace.SpanExporter.
func (e *otlpHTTPExporter) Name() coretrace.ExporterName {
	//: the configured name.
	return e.name
}

// Export encodes spans and POSTs them, returning a typed verdict.
//
// An encoding refusal returns before any socket is touched; everything after it
// — the request, the bounded read, the drain, the verdict — is the shared
// sender's, under this signal's codes.
//
// An EMPTY batch is a no-op rather than a POST. A request carrying zero spans
// costs a round trip to say nothing, and an export loop on a quiet service would
// make one every interval forever.
func (e *otlpHTTPExporter) Export(spans coretrace.SpansValue) error {
	//: nothing to ship.
	if spans.IsEmpty() {
		//: no socket touched.
		return nil
	}
	//: encode first — an unencodable batch never reaches the network.
	body, err := EncodeOTLPJSON(spans)
	//: the sentinel already carries the code, reason and public message.
	if err != nil {
		//: surface the typed refusal.
		return err
	}
	//: transport and classification.
	return e.sender.Post(body)
}
