package metrics

import "github.com/kitsunium/sdk/internal/service/observe/internal/otlp"

// otlpPartialSuccess is ExportMetricsPartialSuccess. The shared sender decodes
// a 2xx body into it (otlp.DecodeRejected), and it is the only part of that
// decoding that differs between the two signals: the member is
// rejectedDataPoints here and rejectedSpans for traces.
//
// errorMessage is declared so the shape matches the schema and encoding/json
// has somewhere to put it; its value is never read into an error, because it
// is unbounded remote-controlled text and an errs Field goes straight into
// structured logs.
type otlpPartialSuccess struct {
	RejectedDataPoints otlp.LenientInt64 `json:"rejectedDataPoints"`
	ErrorMessage       string            `json:"errorMessage"`
}

// RejectedCount implements otlp.RejectedCounter: how many data points the
// collector says it rejected, zero being "fully accepted".
func (p *otlpPartialSuccess) RejectedCount() int64 {
	//: the number or the decimal string, already decoded leniently.
	return int64(p.RejectedDataPoints)
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
