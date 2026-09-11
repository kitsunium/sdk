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

import (
	"strings"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// ReservedPrefix is what a TOP-LEVEL attribute whose key the SDK reserves is
// rendered under, so the two never collide on one line. The whole namespace
// under it is reserved with it — see [ReservesKey].
const ReservedPrefix string = "attr."

// Encoder is the format-side port now rooted in internal/core/logger.
// Kept as an alias here so consumers that import service/logger/encoder
// for the interface type keep compiling. New code should import the
// canonical type from core/logger directly.
type Encoder = corelogger.Encoder

// ReservesKey reports whether a TOP-LEVEL attribute key must be renamed under
// [ReservedPrefix]: the two the SDK emits as fields of its own — trace_id and
// span_id, the correlation OpenTelemetry prescribes and ADR 0062 writes on
// every record emitted inside a span — and anything already in the reserved
// namespace.
//
// Two members of one JSON object cannot both be "trace_id" — a decoder keeps
// one of them, and which one is the decoder's business — so an attribute
// carrying that key beside the SDK's field could replace the correlation on the
// way in, and could carry a value from anywhere. It is renamed rather than
// dropped: nothing a caller logged is lost, and nothing it logged can be
// mistaken for the span this line came from.
//
// The namespace is reserved WITH the two keys because the rename has to be
// injective, and prefixing alone is not: a caller logging both trace_id and a
// literal attr.trace_id would otherwise land both on "attr.trace_id" and the
// ambiguity would survive one name over. Prefixing anything already inside the
// namespace makes the mapping one-to-one — attr.trace_id becomes
// attr.attr.trace_id — so two distinct keys always render distinct.
//
// Inside a group the key is already prefixed ("http.trace_id"), so it cannot
// collide and is left alone.
func ReservesKey(key string) bool {
	//: the two keys ADR 0062 writes, plus the escape namespace they are
	//: renamed into, which must be escaped in turn to stay one-to-one.
	return key == corelogger.TraceIDKey || key == corelogger.SpanIDKey ||
		strings.HasPrefix(key, ReservedPrefix)
}
