package encoder

import (
	"strconv"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The record timestamp's format is timestampLayout, declared in timestamp.go
// beside the renderer that produces it and shared with the JSON encoder.

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

// quoteSafeFloor and quoteSafeCeiling bound the byte range strconv.AppendQuote
// copies into its output verbatim: an ASCII byte for which strconv.IsPrint
// reports true is exactly one in [0x20, 0x7E]. Outside that range AppendQuote
// either escapes the byte or decodes a multi-byte rune, so appendQuotedString
// hands the whole string back to it.
const (
	// quoteSafeFloor is the lowest byte AppendQuote emits unescaped (space).
	quoteSafeFloor byte = 0x20
	// quoteSafeCeiling is the highest such byte (tilde).
	quoteSafeCeiling byte = 0x7e
)

// The bytes AppendSanitized's fast path tests, the UTF-8 encodings of the
// runes it scrubs, and how many bytes each scrubbed shape spans.
const (
	// sanitizeFloor is the first byte that is not a C0 control (space).
	sanitizeFloor byte = 0x20
	// sanitizeDEL is DEL, the one ASCII control above the C0 range.
	sanitizeDEL byte = 0x7f
	// printableSpan is how many bytes printable ASCII spans from
	// sanitizeFloor: 0x20-0x7E, so b-sanitizeFloor < printableSpan tests it in
	// one unsigned comparison.
	printableSpan byte = sanitizeDEL - sanitizeFloor
	// utf8C1Lead leads the two-byte encodings of U+0080-U+00BF, the C1
	// controls U+0080-U+009F among them.
	utf8C1Lead byte = 0xc2
	// utf8C1Last is the last continuation byte of a C1 control (U+009F).
	utf8C1Last byte = 0x9f
	// utf8SeparatorLead leads the three-byte encoding of U+2000-U+2FFF.
	utf8SeparatorLead byte = 0xe2
	// utf8SeparatorMid is the second byte of U+2000-U+203F.
	utf8SeparatorMid byte = 0x80
	// utf8LineSeparatorLast is U+2028's last byte; U+2029's is the next.
	utf8LineSeparatorLast byte = 0xa8
	// utf8ParagraphSeparatorLast is U+2029's last byte.
	utf8ParagraphSeparatorLast byte = 0xa9
	// continuationFloor is the first UTF-8 continuation byte.
	continuationFloor byte = 0x80
	// asciiControlWidth is the bytes a C0 control or DEL spans.
	asciiControlWidth int = 1
	// c1ControlWidth is the bytes a C1 control spans in UTF-8.
	c1ControlWidth int = 2
	// separatorWidth is the bytes U+2028 or U+2029 spans in UTF-8.
	separatorWidth int = 3
)

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
	//: render the header in the documented "TIME LEVEL msg" order. The
	//: timestamp goes through appendTimestamp rather than AppendFormat: same
	//: bytes, 5-6× the speed, and it was 34.6 % of a whole emit — see
	//: timestamp.go and BENCH.md §5.2.
	dst = appendTimestamp(dst, r.Time)
	dst = append(dst, ' ')
	dst = append(dst, r.Level.String()...)
	dst = append(dst, ' ')
	//: strip framing-sensitive bytes from the Message so downstream sinks
	//: that use line-oriented framing (syslog RFC5424, plain file tail) do
	//: not see attacker-influenced CR/LF/NUL produce spoofed frames. This
	//: is defence-in-depth: sinks that need richer escaping still can.
	return AppendSanitized(dst, r.Message)
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

// AppendSanitized copies text onto dst replacing every C0 control byte
// (0x00-0x1F, '\n', '\r' and NUL among them), DEL (0x7F), every C1 control
// (U+0080-U+009F, NEL among them) and the two Unicode separators U+2028 and
// U+2029 with a single space each, so the content can neither inject a new
// frame in a line-framed downstream sink nor drive the terminal a console sink
// writes to — ESC [2K ESC [G erases a line and returns to its start, the
// effect of the CR the scrub removes (CWE-117, CWE-150). Every other byte,
// printable UTF-8 and invalid UTF-8 alike, is copied unchanged. It scrubs
// every attacker-influenceable textual field — the Message, group names, and
// attribute keys — not just the Message (V110).
//
// It is exported for one caller outside this package: the legacy
// service/observe/logger.TextHandler, which renders the same line shape without going
// through an Encoder. That handler once appended all three fields verbatim —
// the scrub existed, but only here — so a line break in any of them forged a
// log line. One function shared by both renderers cannot drift from itself.
func AppendSanitized(dst []byte, text string) []byte {
	//: small enough to inline, as the CR/LF/NUL scrub was (#444): printable
	//: ASCII, 0x20-0x7E, in ONE unsigned comparison per byte, and the whole
	//: text appended at once when that is all it holds — every ordinary key,
	//: group name and message.
	for i := range len(text) {
		//: anything else — a control, DEL, a byte of a multi-byte rune — is
		//: looked at by the scrub proper, from there.
		if text[i]-sanitizeFloor >= printableSpan {
			//: the whole text, rescanned there: one argument fewer keeps this
			//: function within the inlining budget.
			return appendScrubbed(dst, text)
		}
	}
	//: nothing to scrub.
	return append(dst, text...)
}

// appendScrubbed is AppendSanitized for a text holding at least one byte that
// is not printable ASCII: it copies the bytes that pass through in RUNS — one
// append per stretch, not one per byte — and one space per scrubbed byte or
// rune.
func appendScrubbed(dst []byte, text string) []byte {
	//: start is the first byte of the run not yet copied.
	start := 0
	//: walk text byte-by-byte; the bytes of a rune already scrubbed lie
	//: before start and are skipped.
	for i := range len(text) {
		b := text[i]
		//: printable ASCII extends the run, and a scrubbed rune's tail is gone.
		if b-sanitizeFloor < printableSpan || i < start {
			continue
		}
		width := 0
		//: a byte of a multi-byte rune that is neither C1's lead nor the
		//: separators' is copied without a call.
		if b < continuationFloor || b == utf8C1Lead || b == utf8SeparatorLead {
			width = scrubWidth(text, i)
		}
		//: nothing to scrub here: the byte extends the run.
		if width == 0 {
			continue
		}
		//: one space per scrubbed byte or rune.
		dst = append(dst, text[start:i]...)
		dst = append(dst, ' ')
		start = i + width
	}
	//: hand back the sanitized buffer with the last run appended.
	return append(dst, text[start:]...)
}

// scrubWidth reports how many bytes of text, from i, AppendSanitized replaces
// with one space: 1 for a C0 control or DEL, 2 for a C1 control, 3 for U+2028
// or U+2029, and 0 for anything else, which is copied.
func scrubWidth(text string, i int) int {
	b := text[i]
	//: a C0 control or DEL: one byte.
	if b < sanitizeFloor || b == sanitizeDEL {
		//: replaced alone.
		return asciiControlWidth
	}
	//: U+0080-U+009F.
	if isC1Control(text, i) {
		//: the two bytes of the rune.
		return c1ControlWidth
	}
	//: U+2028 and U+2029.
	if isUnicodeSeparator(text, i) {
		//: the three bytes of the rune.
		return separatorWidth
	}
	//: copied unchanged.
	return 0
}

// isC1Control reports whether text holds a C1 control at i: 0xC2 then
// 0x80-0x9F.
func isC1Control(text string, i int) bool {
	//: the lead, and a continuation byte at most utf8C1Last.
	return text[i] == utf8C1Lead && i+1 < len(text) &&
		text[i+1] >= continuationFloor && text[i+1] <= utf8C1Last
}

// isUnicodeSeparator reports whether text holds U+2028 or U+2029 at i:
// 0xE2 0x80 then 0xA8 or 0xA9.
func isUnicodeSeparator(text string, i int) bool {
	//: too short for the three bytes.
	if text[i] != utf8SeparatorLead || i+2 >= len(text) || text[i+1] != utf8SeparatorMid {
		//: not a separator.
		return false
	}
	//: the line or the paragraph separator.
	return text[i+2] == utf8LineSeparatorLast || text[i+2] == utf8ParagraphSeparatorLast
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
		dst = AppendSanitized(dst, g)
		dst = append(dst, groupSeparator)
	}
	//: render the attribute key followed by '=' and the typed value; the key
	//: runs through the same framing-byte scrub as Message and group names so
	//: no attacker-influenceable field can forge a frame boundary (V110).
	//: a top-level attribute may not spell one of the SDK's own fields; inside
	//: a group the prefix above already keeps it apart.
	if len(groups) == 0 && ReservesKey(a.Key) {
		//: rendered under attr.<key> instead.
		dst = append(dst, ReservedPrefix...)
	}
	dst = AppendSanitized(dst, a.Key)
	dst = append(dst, '=')
	//: hand back the buffer with the encoded attribute appended.
	return appendValueOnly(dst, a)
}

// appendQuotedString appends s as a double-quoted token, producing byte-for-
// byte what strconv.AppendQuote produces — but skipping it when it has nothing
// to do.
//
// AppendQuote is the single most expensive call in this encoder: a CPU profile
// of a four-string-attribute record attributed 70 % of the whole encode to it,
// and half of THAT to utf8.DecodeRuneInString plus strconv.IsPrint — a full
// Unicode printability analysis of every rune, run on every value, to discover
// that a log message made of ASCII needs no escaping at all. The scan below
// answers the same question with a byte-range comparison and then copies the
// string with one memmove. Measured in internal/service/observe/logger/encoder/BENCH.md.
//
// Correctness is not a judgement call: AppendQuote emits a byte verbatim
// exactly when the rune is ASCII, strconv.IsPrint reports true (which for
// ASCII is precisely [0x20, 0x7E]), and it is neither the delimiter '"' nor
// the escape '\\'. quoteSafe accepts that set and nothing else, so the fast
// path is a strict subset of the slow path's identity cases — pinned by
// TestAppendQuotedStringMatchesStrconv, which sweeps every byte value.
func appendQuotedString(dst []byte, s string) []byte {
	//: anything AppendQuote might transform goes to AppendQuote.
	if !quoteSafe(s) {
		//: the general path owns every escaping and multi-byte-rune case.
		return strconv.AppendQuote(dst, s)
	}
	//: verbatim copy between the two delimiters AppendQuote would have added.
	dst = append(dst, '"')
	dst = append(dst, s...)
	//: close the quoted token before handing the buffer back.
	return append(dst, '"')
}

// quoteSafe reports whether every byte of s is one strconv.AppendQuote would
// copy into its output unchanged. A byte outside the printable-ASCII range is
// rejected without inspecting it further, which also rejects every byte of
// every multi-byte UTF-8 rune (all of them are >= 0x80).
func quoteSafe(s string) bool {
	//: byte-wise rather than rune-wise: the accepted set is single-byte only,
	//: so a byte scan cannot mis-classify a multi-byte rune — it rejects it.
	for i := range len(s) {
		c := s[i]
		//: reject control bytes, DEL and anything non-ASCII.
		if c < quoteSafeFloor || c > quoteSafeCeiling {
			//: hand the string to strconv.AppendQuote.
			return false
		}
		//: reject the two bytes AppendQuote escapes inside the printable range.
		if c == '"' || c == '\\' {
			//: hand the string to strconv.AppendQuote.
			return false
		}
	}
	//: every byte survives AppendQuote unchanged.
	return true
}

// appendValueOnly renders only the value part of an AttrValue.
func appendValueOnly(dst []byte, a corelogger.AttrValue) []byte {
	//: dispatch on the typed Kind discriminant — no boxing on the hot path.
	switch a.Value.Kind() {
	//: strings are quoted so whitespace in values remains visible.
	case corelogger.KindString:
		//: same output as strconv.AppendQuote, without the Unicode analysis
		//: when the value is plain ASCII — see appendQuotedString.
		return appendQuotedString(dst, a.Value.String())
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
		//: quoted like a string so the textual form stays readable next to the
		//: other quoted attrs; a Duration's rendering is always plain ASCII, so
		//: this always takes appendQuotedString's fast path.
		return appendQuotedString(dst, a.Value.Duration().String())
	//: timestamps render as RFC3339-with-millis to match the header format.
	case corelogger.KindTime:
		//: the SAME renderer as the line header, so a Time attr and the record
		//: timestamp cannot drift apart in shape.
		return appendTimestamp(dst, a.Value.Time())
	//: every other Kind degrades to '?' until structured encoders ship.
	default:
		//: '?' is the documented placeholder for unsupported variants.
		return append(dst, '?')
	}
}
