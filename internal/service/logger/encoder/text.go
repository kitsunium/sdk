// Package encoder: text.go implements TextEncoder — the default
// human-readable encoder that renders a record as
// "TIME LEVEL msg key=val key=val …\n". Extracted from the original
// service/logger.TextHandler in commit 7 so the format and the transport
// (Sink) live in separate packages.
package encoder

import (
	"strconv"
	"time"

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
//
// Params:
//   - clk: clock used as a fallback when the record carries a zero Time.
//
// Returns:
//   - Encoder: a ready-to-use text Encoder.
//
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
//
// Returns:
//   - string: always "text".
func (e *textEncoder) Name() string {
	//: identifier is stable across the public API.
	return textEncoderName
}

// Append serialises r onto dst, prefixing every attribute key with the
// active group stack ("g1.g2.key=val"). Returns the (possibly re-allocated)
// buffer with the encoded line including the trailing newline.
//
// Params:
//   - dst: caller-supplied buffer (typically borrowed from kernel/buffer).
//   - groups: active group prefix stack (outermost first); may be empty.
//   - r: record to render.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer with the encoded line.
func (e *textEncoder) Append(dst []byte, groups []string, r corelogger.RecordEvent) []byte {
	//: fall back to the encoder clock when the caller did not timestamp.
	if r.Time.IsZero() {
		//: use injected clock so tests can freeze time deterministically.
		r.Time = e.clk.Now()
	}
	//: render the header in the documented "TIME LEVEL msg" order.
	dst = appendHeader(dst, r)
	//: append every record-bound attribute prefixed by the group stack.
	for _, a := range r.Attrs {
		//: render each record-local attr using the same encoding rules.
		dst = appendAttrWithGroups(dst, groups, a)
	}
	//: terminate the line so downstream consumers can split on '\n'.
	return append(dst, '\n')
}

// appendHeader writes the leading "TIME LEVEL msg" prefix into dst.
//
// Params:
//   - dst: caller-supplied buffer; header bytes are appended onto it.
//   - r: record providing time / level / message.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer with the header bytes.
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

// appendSanitizedMessage copies msg onto dst replacing '\n', '\r', and NUL
// with a single space so Message content can never inject a new frame in a
// line-framed downstream sink. Other control characters are preserved to
// keep the rendering faithful; only framing-sensitive bytes are stripped.
//
// Params:
//   - dst: caller-supplied buffer; sanitized bytes are appended onto it.
//   - msg: the Record.Message string to sanitize.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer with msg appended.
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
//
// Params:
//   - dst: caller-supplied buffer; encoded bytes are appended onto it.
//   - groups: active group prefix stack (outermost first); may be empty.
//   - a: attribute whose Key and Value are rendered.
//
// Returns:
//   - []byte: the (possibly re-allocated) buffer after append operations.
func appendAttrWithGroups(dst []byte, groups []string, a corelogger.AttrValue) []byte {
	//: separator between message and first attr, and between consecutive attrs.
	dst = append(dst, ' ')
	//: walk the group stack to emit "g1.g2.…" before the attribute key.
	for _, g := range groups {
		//: append each group name followed by the canonical separator.
		dst = append(dst, g...)
		dst = append(dst, groupSeparator)
	}
	//: render the attribute key followed by '=' and the typed value.
	dst = append(dst, a.Key...)
	dst = append(dst, '=')
	//: hand back the buffer with the encoded attribute appended.
	return appendValueOnly(dst, a)
}

// appendValueOnly renders only the value part of an AttrValue.
//
// Params:
//   - dst: buffer to append to; returned grown.
//   - a: attribute whose Value is rendered.
//
// Returns:
//   - []byte: the potentially re-sliced buffer after append operations.
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
		//: AppendFormat reuses the same layout as the line header.
		return a.Value.Time().AppendFormat(dst, time.RFC3339Nano)
	//: every other Kind degrades to '?' until structured encoders ship.
	default:
		//: '?' is the documented placeholder for unsupported variants.
		return append(dst, '?')
	}
}
