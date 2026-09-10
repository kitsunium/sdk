// Package encoder — implements TextEncoder, the default human-readable
// encoder that renders a record as "TIME LEVEL msg key=val key=val …\n".
// Lives in its own package so the format and the transport (Sink) can
// evolve independently.
package encoder

import (
	"strconv"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// timestampLayout is the RFC3339-with-milliseconds format used to render the
// RecordEvent.Time field into the textual output.
const timestampLayout string = "2006-01-02T15:04:05.000Z07:00"

// decimalBase is the radix passed to strconv.Append* for human-readable ints.
const decimalBase int = 10

// floatPrec is the -1 sentinel that asks strconv to emit the shortest
// round-trippable representation for a float64.
const floatPrec int = -1

// floatBitSize tells strconv.AppendFloat we are formatting a 64-bit float.
const floatBitSize int = 64

// floatFormat is the byte verb ('g') selecting strconv's exponent-or-decimal
// mode for float64 rendering.
const floatFormat byte = 'g'

// groupSeparator is the rune inserted between successive group names AND
// between a group prefix and an attribute key in textual output ("a.b.k=v").
const groupSeparator byte = '.'

// textEncoderName is the canonical identifier returned by TextEncoder.Name.
const textEncoderName string = "text"

// textEncoder renders records as a single plain-text line. Unexported so the
// public surface is the Encoder interface returned by NewText.
type textEncoder struct {
	// clk sources the timestamp when RecordEvent.Time is the zero value.
	clk clock.Clock
}

// NewText builds an Encoder bound to the supplied clock. Pass clock.System
// for production use; tests inject a fake clock. A nil clock falls back to
// the real wall clock so callers can pass clock.System or nil interchangeably.
// IFACE-PLUGIN: returns the Encoder contract so callers depend on the
// public surface; the concrete textEncoder is intentionally hidden.
func NewText(clk clock.Clock) Encoder {
	//: clock is mandatory — fall back to the real wall clock on nil.
	if clk == nil {
		//: defensive default avoids a nil-pointer panic in Append.
		clk = clock.System
	}
	//: hand back the encoder behind the public Encoder interface.
	return &textEncoder{clk: clk}
}

// Name returns the canonical encoder identifier.
func (e *textEncoder) Name() string {
	//: identifier is stable across the public API.
	return textEncoderName
}

// Append serialises r onto dst, prefixing every attribute key with the
// active group stack ("g1.g2.key=val"). Returns the (possibly re-allocated)
// buffer with the encoded line including the trailing newline.
func (e *textEncoder) Append(dst []byte, groups []string, r corelogger.RecordEvent) []byte {
	//: fall back to the encoder clock when the caller did not timestamp.
	if r.Time.IsZero() {
		//: use injected clock so tests can freeze time deterministically.
		r.Time = e.clk.Now()
	}
	//: render the header in the documented "TIME LEVEL msg" order.
	dst = appendHeader(dst, r)
	//: the trace context is a TOP-LEVEL field, so it lands between the header
	//: and the attributes and is never touched by the group prefix stack.
	dst = appendTraceContext(dst, r)
	//: append every record-bound attribute prefixed by the group stack.
	for _, a := range r.Attrs {
		//: render each record-local attr using the same encoding rules.
		dst = appendAttrWithGroups(dst, groups, a)
	}
	//: terminate the line so downstream consumers can split on '\n'.
	return append(dst, '\n')
}

// appendHeader writes the leading "TIME LEVEL msg" prefix into dst.
func appendHeader(dst []byte, r corelogger.RecordEvent) []byte {
	//: render the header in the documented "TIME LEVEL msg" order.
	dst = r.Time.AppendFormat(dst, timestampLayout)
	dst = append(dst, ' ')
	dst = append(dst, r.Level.String()...)
	dst = append(dst, ' ')
	//: strip framing-sensitive bytes from the Message so downstream sinks
	//: that use line-oriented framing (syslog RFC5424, plain file tail) do
	//: not see attacker-influenced CR/LF/NUL produce spoofed frames. This
	//: is defence-in-depth: sinks that need richer escaping still can.
	return appendSanitizedMessage(dst, r.Message)
}

// appendTraceContext writes the two top-level correlation fields
// "trace_id=<32 hex> span_id=<16 hex>" into dst, and writes NOTHING when tc is
// the invalid zero value.
//
// Emitting nothing is the whole point: an all-zero identifier is invalid under
// W3C Trace Context §3.2.2.3/§3.2.2.4, so rendering trace_id= or
// trace_id=000…0 would put a field no query can join on onto every line a
// service logs outside a request — which is most of them.
//
// The values are NOT quoted, unlike string attribute values. They are record
// fields rather than attributes, they are fixed-length lowercase hex and can
// therefore contain nothing that needs quoting, and an operator pasting an id
// from a tracing backend greps for `trace_id=<id>` exactly as it appears there.
func appendTraceContext(dst []byte, r corelogger.RecordEvent) []byte {
	//: the identity travels on the record because Encoder.Append receives no
	//: context.Context — that is the whole reason it is a field (ADR 0062).
	tc := r.TraceContext
	//: no span in scope — emit nothing rather than an unjoinable zero id.
	if !tc.IsValid() {
		//: the buffer is handed back untouched.
		return dst
	}
	//: " trace_id=" then the 32 hex digits, straight into the buffer.
	dst = append(dst, ' ')
	dst = append(dst, corelogger.TraceIDKey...)
	dst = append(dst, '=')
	dst = tc.AppendTraceIDHex(dst)
	//: " span_id=" then the 16 hex digits.
	dst = append(dst, ' ')
	dst = append(dst, corelogger.SpanIDKey...)
	dst = append(dst, '=')
	//: hand back the buffer carrying both correlation fields.
	return tc.AppendSpanIDHex(dst)
}

// appendSanitizedMessage copies msg onto dst replacing '\n', '\r', and NUL
// with a single space so the content can never inject a new frame in a
// line-framed downstream sink. Other control characters are preserved to
// keep the rendering faithful; only framing-sensitive bytes are stripped.
// It scrubs every attacker-influenceable textual field — the Message, group
// names, and attribute keys — not just the Message (V110).
func appendSanitizedMessage(dst []byte, msg string) []byte {
	//: walk msg byte-by-byte; ASCII control-char check is cheap and the
	//: allocation cost matches the existing append pattern in this file.
	for i := range len(msg) {
		b := msg[i]
		//: framing-sensitive bytes collapse to a single space per occurrence.
		if b == '\n' || b == '\r' || b == 0 {
			dst = append(dst, ' ')
			continue
		}
		//: every other byte passes through verbatim (UTF-8 multi-byte runes
		//: never contain bytes in 0x00..0x1F, so byte-level scan is safe).
		dst = append(dst, b)
	}
	//: hand back the sanitized buffer with framing-sensitive bytes scrubbed.
	return dst
}

// appendAttrWithGroups prepends the active group prefix stack to the
// attribute key before delegating value rendering to appendValueOnly.
func appendAttrWithGroups(dst []byte, groups []string, a corelogger.AttrValue) []byte {
	//: separator between message and first attr, and between consecutive attrs.
	dst = append(dst, ' ')
	//: walk the group stack to emit "g1.g2.…" before the attribute key.
	for _, g := range groups {
		//: scrub framing bytes from the group name — an attacker-influenced
		//: group segment must not inject a second line into a syslog/file frame
		//: any more than the Message can (V110).
		dst = appendSanitizedMessage(dst, g)
		dst = append(dst, groupSeparator)
	}
	//: render the attribute key followed by '=' and the typed value; the key
	//: runs through the same framing-byte scrub as Message and group names so
	//: no attacker-influenceable field can forge a frame boundary (V110).
	dst = appendSanitizedMessage(dst, a.Key)
	dst = append(dst, '=')
	//: hand back the buffer with the encoded attribute appended.
	return appendValueOnly(dst, a)
}

// appendValueOnly renders only the value part of an AttrValue.
func appendValueOnly(dst []byte, a corelogger.AttrValue) []byte {
	//: dispatch on the typed Kind discriminant — no boxing on the hot path.
	switch a.Value.Kind() {
	//: strings are quoted so whitespace in values remains visible.
	case corelogger.KindString:
		//: strconv.AppendQuote handles escaping consistently across encoders.
		return strconv.AppendQuote(dst, a.Value.String())
	//: int64 (also covers int / int32 widened by IntValue) renders base-10.
	case corelogger.KindInt64:
		//: strconv.AppendInt is the canonical alloc-free integer renderer.
		return strconv.AppendInt(dst, a.Value.Int64(), decimalBase)
	//: uint64 renders base-10 unquoted.
	case corelogger.KindUint64:
		//: strconv.AppendUint is the alloc-free unsigned integer renderer.
		return strconv.AppendUint(dst, a.Value.Uint64(), decimalBase)
	//: booleans render as "true" or "false".
	case corelogger.KindBool:
		//: strconv.AppendBool emits the canonical Go spelling.
		return strconv.AppendBool(dst, a.Value.Bool())
	//: floats use Go's default shortest round-trip format.
	case corelogger.KindFloat64:
		//: 'g' format with -1 precision matches Go's default fmt.Print rendering.
		return strconv.AppendFloat(dst, a.Value.Float64(), floatFormat, floatPrec, floatBitSize)
	//: durations render via the standard time.Duration String() form.
	case corelogger.KindDuration:
		//: AppendQuote keeps the textual form readable next to other quoted attrs.
		return strconv.AppendQuote(dst, a.Value.Duration().String())
	//: timestamps render as RFC3339-with-millis to match the header format.
	case corelogger.KindTime:
		//: AppendFormat reuses timestampLayout — the SAME layout as the line
		//: header — so a Time attr and the record timestamp share one shape.
		return a.Value.Time().AppendFormat(dst, timestampLayout)
	//: every other Kind degrades to '?' until structured encoders ship.
	default:
		//: '?' is the documented placeholder for unsupported variants.
		return append(dst, '?')
	}
}
