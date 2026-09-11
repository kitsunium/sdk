// Package encoder provides concrete Encoder implementations (Text today;
// NDJSON and JSON to land in follow-up commits). The Encoder interface
// itself lives in internal/core/logger — this package is implementation-
// only, exposing the interface via a type alias so existing call sites
// that import "service/logger/encoder".Encoder keep compiling.
//
// Per ADR 0005 hexagonal layering: core/ holds ports, service/ holds
// adapters. Previously Encoder lived here, creating an asymmetry where
// Sink lived in core/ and Encoder lived in service/ even though both are
// ports of the Handler.
package encoder

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// ReservedPrefix is what a TOP-LEVEL attribute whose key is one the SDK writes
// itself is rendered under, so the two never collide on one line.
const ReservedPrefix string = "attr."

// Encoder is the format-side port now rooted in internal/core/logger.
// Kept as an alias here so consumers that import service/logger/encoder
// for the interface type keep compiling. New code should import the
// canonical type from core/logger directly.
type Encoder = corelogger.Encoder

// ReservesKey reports whether key is one the SDK emits as a top-level field of
// its own: trace_id and span_id, the correlation OpenTelemetry prescribes and
// ADR 0062 writes on every record emitted inside a span.
//
// Two members of one JSON object cannot both be "trace_id" — a decoder keeps
// one of them, and which one is the decoder's business — so an attribute
// carrying that key beside the SDK's field could replace the correlation on the
// way in, and could carry a value from anywhere. It is renamed rather than
// dropped: nothing a caller logged is lost, and nothing it logged can be
// mistaken for the span this line came from. Inside a group the key is already
// prefixed ("http.trace_id"), so it cannot collide and is left alone.
func ReservesKey(key string) bool {
	//: exactly the two keys ADR 0062 writes.
	return key == corelogger.TraceIDKey || key == corelogger.SpanIDKey
}
