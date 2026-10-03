// Package trace — the one member of a collector's answer that is this signal's
// own: the count an ExportTraceServiceResponse reports rejected.
package trace

import "github.com/kitsunium/sdk/internal/service/internal/otlp"

// otlpPartialSuccess is ExportTracePartialSuccess. The shared sender decodes a
// 2xx body into it (otlp.DecodeRejected), and it is the only part of that
// decoding that differs between the two signals: the member is rejectedSpans
// here and rejectedDataPoints for metrics.
//
// errorMessage is declared so the shape matches the schema and encoding/json
// has somewhere to put it; its value is never read into an error, because it
// is unbounded remote-controlled text and an errs Field goes straight into
// structured logs.
type otlpPartialSuccess struct {
	RejectedSpans otlp.LenientInt64 `json:"rejectedSpans"`
	ErrorMessage  string            `json:"errorMessage"`
}

// RejectedCount implements otlp.RejectedCounter: how many spans the collector
// says it rejected, zero being "fully accepted".
func (p *otlpPartialSuccess) RejectedCount() int64 {
	//: the number or the decimal string, already decoded leniently.
	return int64(p.RejectedSpans)
}

// decodeRejected reads a 2xx body's rejected count through this signal's own
// partial-success message — the SignalSpec.DecodeRejected the shared sender
// calls. A fresh message per response: nothing is kept between exports.
func decodeRejected(payload []byte) int64 {
	//: the message the decode lands in.
	var message otlpPartialSuccess
	//: the shared lenient decode, with this signal's member name.
	return otlp.DecodeRejected(payload, &message)
}
