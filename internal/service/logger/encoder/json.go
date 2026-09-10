// Package encoder — implements jsonEncoder, the structured single-line JSON
// encoder that renders a record as one encoding/json-compatible object per
// line: {"ts":…,"level":…,"msg":…,<flat attrs>}\n. Lives beside the text
// encoder so the format and the transport (Sink) evolve independently. The
// byte writer is hand-rolled (append-based, no reflection, no json.Marshal)
// so the hot path stays allocation-free.
package encoder

import (
	"math"
	"strconv"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The "ts" member's format is timestampLayout, declared in timestamp.go and
// shared with the text encoder. It used to be duplicated here as
// jsonTimestampLayout; the two strings were identical, and one constant cannot
// drift from itself.

// jsonEncoderName is the canonical identifier returned by jsonEncoder.Name.
const jsonEncoderName string = "json"

// jsonGroupSeparator joins the active group stack with the attribute key so a
// grouped attr renders flat as "g1.g2.key" — matching the text encoder's
// dotted convention rather than nesting objects.
const jsonGroupSeparator byte = '.'

// jsonHexDigits maps a nibble to its lowercase hexadecimal rune for the
// \u00XX escape of control bytes.
const jsonHexDigits string = "0123456789abcdef"

// jsonDecimalBase is the radix passed to strconv.Append* for JSON numbers.
const jsonDecimalBase int = 10

// jsonFloatPrec is the -1 sentinel asking strconv for the shortest
// round-trippable float64 representation.
const jsonFloatPrec int = -1

// jsonFloatBitSize tells strconv.AppendFloat we are formatting a 64-bit float.
const jsonFloatBitSize int = 64

// jsonFloatFormat is the byte verb ('g') selecting strconv's
// exponent-or-decimal float64 mode.
const jsonFloatFormat byte = 'g'

// jsonControlCeiling is the exclusive upper bound of the bytes the JSON grammar
// forbids unescaped inside a string literal (everything below U+0020).
const jsonControlCeiling byte = 0x20

// jsonNibbleShift is the bit-shift that isolates the high nibble of a byte for
// the \u00XX escape.
const jsonNibbleShift byte = 4

// jsonNibbleMask isolates the low nibble of a byte for the \u00XX escape.
const jsonNibbleMask byte = 0x0f

// jsonEncoder renders records as a single structured JSON object line.
// Unexported so the public surface is the Encoder interface returned by
// NewJSON.
type jsonEncoder struct {
	// clk sources the timestamp when RecordEvent.Time is the zero value.
	clk clock.Clock
}

// NewJSON builds an Encoder that renders records as single-line JSON. Pass
// clock.System for production use; tests inject a fake clock. A nil clock
// falls back to the real wall clock so callers can pass clock.System or nil
// interchangeably.
// IFACE-PLUGIN: returns the Encoder contract so callers depend on the
// public surface; the concrete jsonEncoder is intentionally hidden.
func NewJSON(clk clock.Clock) Encoder {
	//: clock is mandatory — fall back to the real wall clock on nil.
	if clk == nil {
		//: defensive default avoids a nil-pointer panic in Append.
		clk = clock.System
	}
	//: hand back the encoder behind the public Encoder interface.
	return &jsonEncoder{clk: clk}
}

// Name returns the canonical encoder identifier.
func (e *jsonEncoder) Name() string {
	//: identifier is stable across the public API.
	return jsonEncoderName
}

// Append serialises r onto dst as one JSON object line and returns the
// (possibly re-allocated) buffer including the trailing newline. groups is
// rendered as a dotted prefix on each attribute key ("g1.g2.key").
func (e *jsonEncoder) Append(dst []byte, groups []string, r corelogger.RecordEvent) []byte {
	//: fall back to the encoder clock when the caller did not timestamp.
	if r.Time.IsZero() {
		//: use injected clock so tests can freeze time deterministically.
		r.Time = e.clk.Now()
	}
	dst = append(dst, '{')
	dst = appendJSONHeader(dst, r)
	//: trace context is emitted as TOP-LEVEL keys of the object, right after
	//: the header and before the attributes — the shape the OpenTelemetry
	//: compatibility spec shows for non-OTLP JSON logs.
	dst = appendJSONTraceContext(dst, r)
	//: append every record-bound attribute prefixed by the group stack.
	for _, a := range r.Attrs {
		//: each attr becomes a comma-prefixed "key":value member.
		dst = append(dst, ',')
		dst = appendJSONAttr(dst, groups, a)
	}
	dst = append(dst, '}')
	//: terminate the line so downstream consumers can split on '\n'.
	return append(dst, '\n')
}

// appendJSONHeader writes the fixed-order "ts","level","msg" members into dst.
func appendJSONHeader(dst []byte, r corelogger.RecordEvent) []byte {
	dst = appendJSONString(dst, "ts")
	dst = append(dst, ':')
	dst = append(dst, '"')
	//: appendTimestamp renders timestampLayout — the one layout both encoders
	//: share — byte for byte, at 5-6× AppendFormat's speed. See timestamp.go
	//: and BENCH.md §5.2.
	dst = appendTimestamp(dst, r.Time)
	dst = append(dst, '"', ',')
	dst = appendJSONString(dst, "level")
	dst = append(dst, ':')
	dst = appendJSONString(dst, r.Level.String())
	dst = append(dst, ',')
	dst = appendJSONString(dst, "msg")
	dst = append(dst, ':')
	//: Message is escaped as a JSON string — no separate framing sanitiser is
	//: needed because JSON escaping already neutralises CR/LF/NUL.
	return appendJSONString(dst, r.Message)
}

// appendJSONTraceContext writes the two correlation members
// ,"trace_id":"<32 hex>","span_id":"<16 hex>" into dst, and writes NOTHING
// when tc is the invalid zero value.
//
// They are TOP-LEVEL keys of the log object rather than attributes, which is
// what "includes trace context fields as top-level keys within the JSON log
// object" prescribes (specification/compatibility/logging_trace_context.md).
// An attribute could not have satisfied it: WithGroup would render the key as
// "g1.trace_id" and no ingestion pipeline would recognise it.
//
// The absent case emits nothing at all — not "" and not the all-zero id, both
// of which are invalid under W3C Trace Context §3.2.2.3/§3.2.2.4 and would
// otherwise appear on every line a service logs outside a request.
func appendJSONTraceContext(dst []byte, r corelogger.RecordEvent) []byte {
	//: the identity travels on the record because Encoder.Append receives no
	//: context.Context — that is the whole reason it is a field (ADR 0062).
	tc := r.TraceContext
	//: no span in scope — the object carries no correlation members at all.
	if !tc.IsValid() {
		//: the buffer is handed back untouched.
		return dst
	}
	//: ,"trace_id":"<32 hex>" — the hex alphabet needs no JSON escaping.
	dst = append(dst, ',')
	dst = appendJSONString(dst, corelogger.TraceIDKey)
	dst = append(dst, ':', '"')
	dst = tc.AppendTraceIDHex(dst)
	dst = append(dst, '"')
	//: ,"span_id":"<16 hex>".
	dst = append(dst, ',')
	dst = appendJSONString(dst, corelogger.SpanIDKey)
	dst = append(dst, ':', '"')
	dst = tc.AppendSpanIDHex(dst)
	//: hand back the buffer carrying both correlation members.
	return append(dst, '"')
}

// appendJSONAttr writes one attribute as a "g1.g2.key":value JSON member.
func appendJSONAttr(dst []byte, groups []string, a corelogger.AttrValue) []byte {
	dst = append(dst, '"')
	//: walk the group stack to emit "g1.g2.…" before the attribute key.
	for _, g := range groups {
		//: append each group name followed by the dotted separator.
		dst = appendJSONEscaped(dst, g)
		dst = append(dst, jsonGroupSeparator)
	}
	dst = appendJSONEscaped(dst, a.Key)
	dst = append(dst, '"', ':')
	//: hand back the buffer with the encoded attribute value appended.
	return appendJSONValue(dst, a)
}

// appendJSONValue renders the value of a as its JSON-correct token, dispatching
// on the typed Kind discriminant so there is no boxing on the hot path.
func appendJSONValue(dst []byte, a corelogger.AttrValue) []byte {
	//: dispatch on the typed Kind discriminant — no boxing on the hot path.
	switch a.Value.Kind() {
	//: strings render as escaped JSON string literals.
	case corelogger.KindString:
		//: appendJSONString quotes and escapes per the JSON grammar.
		return appendJSONString(dst, a.Value.String())
	//: int64 (also covers int / int32) renders as a bare JSON number.
	case corelogger.KindInt64:
		//: strconv.AppendInt is the canonical alloc-free integer renderer.
		return strconv.AppendInt(dst, a.Value.Int64(), jsonDecimalBase)
	//: uint64 renders as a bare JSON number.
	case corelogger.KindUint64:
		//: strconv.AppendUint is the alloc-free unsigned integer renderer.
		return strconv.AppendUint(dst, a.Value.Uint64(), jsonDecimalBase)
	//: booleans render as the JSON true/false literal.
	case corelogger.KindBool:
		//: strconv.AppendBool emits the canonical JSON spelling.
		return strconv.AppendBool(dst, a.Value.Bool())
	//: floats render with Go's shortest round-trip format.
	case corelogger.KindFloat64:
		//: NaN/Inf are not valid JSON, so guard before emitting a bare number.
		return appendJSONFloat(dst, a.Value.Float64())
	//: durations render as a quoted string (matches the text encoder).
	case corelogger.KindDuration:
		//: AppendQuote-style escaping keeps the value a valid JSON string.
		return appendJSONString(dst, a.Value.Duration().String())
	//: timestamps render as a quoted RFC3339-with-millis string.
	case corelogger.KindTime:
		//: reuse the header renderer so all timestamps share one shape.
		dst = append(dst, '"')
		dst = appendTimestamp(dst, a.Value.Time())
		//: close the quoted timestamp token before handing the buffer back.
		return append(dst, '"')
	//: every other Kind degrades to a quoted "?" placeholder, kept valid JSON.
	default:
		//: "?" mirrors the text encoder's unsupported-variant placeholder.
		return appendJSONString(dst, "?")
	}
}

// appendJSONFloat renders f as a bare JSON number, degrading NaN and ±Inf to
// the null literal because the JSON grammar has no representation for them.
func appendJSONFloat(dst []byte, f float64) []byte {
	//: NaN and ±Inf have no JSON number representation.
	if math.IsNaN(f) || math.IsInf(f, 0) {
		//: null is the documented degraded result for non-finite floats.
		return append(dst, 'n', 'u', 'l', 'l')
	}
	//: finite floats use the shortest round-trip 'g' format.
	return strconv.AppendFloat(dst, f, jsonFloatFormat, jsonFloatPrec, jsonFloatBitSize)
}

// appendJSONString writes s as a quoted, JSON-escaped string token onto dst.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	dst = appendJSONEscaped(dst, s)
	//: close the quoted token before handing the buffer back.
	return append(dst, '"')
}

// appendJSONEscaped writes the JSON-escaped body of s onto dst without the
// surrounding quotes, so callers can compose multi-part keys before quoting.
func appendJSONEscaped(dst []byte, s string) []byte {
	//: escape each byte that the JSON grammar forbids inside a string literal.
	for i := range len(s) {
		c := s[i]
		//: route the byte through the matching escape form.
		switch {
		//: backslash and quote take their two-byte escapes.
		case c == '\\' || c == '"':
			dst = append(dst, '\\', c)
		//: newline takes the short \n escape.
		case c == '\n':
			dst = append(dst, '\\', 'n')
		//: carriage return takes the short \r escape.
		case c == '\r':
			dst = append(dst, '\\', 'r')
		//: tab takes the short \t escape.
		case c == '\t':
			dst = append(dst, '\\', 't')
		//: remaining control bytes need a \u00XX escape.
		case c < jsonControlCeiling:
			dst = append(dst, '\\', 'u', '0', '0', jsonHexDigits[c>>jsonNibbleShift], jsonHexDigits[c&jsonNibbleMask])
		//: printable bytes pass through unchanged.
		default:
			dst = append(dst, c)
		}
	}
	//: hand back the buffer with the escaped body appended.
	return dst
}
