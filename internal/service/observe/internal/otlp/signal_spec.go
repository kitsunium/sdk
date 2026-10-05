package otlp

import "github.com/kitsunium/sdk/internal/kernel/errs"

// SignalSpec is one signal's vocabulary for the shared OTLP/HTTP sender: the
// sentinels each verdict wraps, the parameters each wrap of a FOREIGN cause
// uses, the field a partial success counts under, and how that signal's
// collector reports a partial success at all.
//
// It is the error-code injection seam this package is built around. The sender
// decides WHICH verdict an exchange earns — the specification's sections, the
// same for every signal — and the SignalSpec decides what that verdict is CALLED:
// the dotted-quad code, the reason, the public and private wording. A metrics
// export refused by a collector still reads OTLP_EXPORT_REJECTED 0.3.45.8 and
// "The OTLP collector rejected the metrics payload", a trace export
// 0.3.50.6 and "The trace collector rejected the export", exactly as before
// the transport was shared.
//
// Every field is required. A signal declares its SignalSpec once, as a
// package-level value, and hands a pointer to NewSender.
type SignalSpec struct {
	// EndpointInvalid refuses an endpoint that cannot address a collector:
	// empty, not http or https, no host, or no path beyond "/".
	EndpointInvalid *errs.Error
	// EndpointUnparsable wraps url.Parse's own failure. It carries the
	// EndpointInvalid code with the parse error on the trail, and never the
	// URL's text.
	EndpointUnparsable errs.WrapParams
	// RequestUnbuildable wraps http.NewRequest's failure — near-unreachable,
	// the endpoint having been validated at construction, and still typed.
	RequestUnbuildable errs.WrapParams
	// TransportFault wraps a client.Do failure: the request never got an
	// answer, which the specification says a client SHOULD retry. It must
	// carry the Unavailable code, so the signal's retry classifier sees it.
	TransportFault errs.WrapParams
	// Rejected is the permanent verdict: a status outside the retryable set,
	// or a 3xx the redirect refusal handed back.
	Rejected *errs.Error
	// Unavailable is the transient verdict: one of HTTP 429, 502, 503, 504.
	Unavailable *errs.Error
	// PartialSuccess is the verdict for a 2xx whose body reports rejected
	// items: data was lost, and the specification forbids replaying it.
	PartialSuccess *errs.Error
	// RejectedField is the errs Field key a PartialSuccess carries the
	// rejected count under: "rejected_data_points", "rejected_spans".
	RejectedField string
	// DecodeRejected reads the rejected count out of a 2xx body — the one
	// member that differs between ExportMetricsServiceResponse and
	// ExportTraceServiceResponse. A signal passes a function that hands
	// otlp.DecodeRejected a fresh pointer to its own partial-success message.
	DecodeRejected func(payload []byte) int64
}
