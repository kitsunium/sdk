package trace

// The traceparent grammar, in bytes; the service package's string parser
// (service/observe/trace.ParseTraceParent) reads the same offsets.
const (
	// traceParentLen is a version-00 traceparent's exact length.
	traceParentLen int = 55
	// versionHexLen is the width of the version field.
	versionHexLen int = 2
	// versionInvalid is the version the specification declares invalid (§3.2.2.1).
	versionInvalid byte = 0xff
	// traceIDOffset is where trace-id starts.
	traceIDOffset int = versionHexLen + 1
	// parentIDOffset is where parent-id starts.
	parentIDOffset int = traceIDOffset + TraceIDHexLen + 1
	// flagsOffset is where trace-flags starts.
	flagsOffset int = parentIDOffset + SpanIDHexLen + 1
	// hexDigits renders a nibble in the lowercase hex of the grammar.
	hexDigits string = "0123456789abcdef"
	// badNibble is what nibble returns for a byte outside [0-9a-f].
	badNibble byte = 0xff
	// nibbleBits is the width of one hex digit.
	nibbleBits byte = 4
)

// ParseTraceParentBytes reads a traceparent header value from bytes, with the
// rules of service/observe/trace.ParseTraceParent and without converting it to
// a string, for the hot paths that hold a header as bytes: ok is false for
// anything else, never an error, because a malformed traceparent is a new
// trace (W3C Trace Context §4.3) and a request is never refused for it. The
// context returned is marked Remote.
func ParseTraceParentBytes(header []byte) (context SpanContextValue, ok bool) {
	//: shorter than the fixed prefix cannot carry the four fields, in any
	//: version; then the version and its tail, and the three dashes at their
	//: fixed positions, in every version.
	if len(header) < traceParentLen || !versionTailOK(header) ||
		header[traceIDOffset-1] != '-' || header[parentIDOffset-1] != '-' || header[flagsOffset-1] != '-' {
		return SpanContextValue{}, false
	}
	var flags [1]byte
	//: each field in place: width by construction, alphabet and non-zero checked.
	if !decodeHexID(context.TraceID[:], header[traceIDOffset:parentIDOffset-1]) ||
		!decodeHexID(context.SpanID[:], header[parentIDOffset:flagsOffset-1]) ||
		!decodeHex(flags[:], header[flagsOffset:traceParentLen]) {
		return SpanContextValue{}, false
	}
	context.Flags, context.Remote = TraceFlags(flags[0]), true
	//: a joinable span context from a stranger.
	return context, true
}

// versionTailOK reports whether header's version is one the grammar admits and
// whatever follows the fixed prefix is legal for it: version 00 has no extension
// point, and a higher one ends at 55 or continues after a dash (§3.2.4).
func versionTailOK(header []byte) bool {
	var version [1]byte
	//: a version must be two lowercase hex digits, and never ff.
	if !decodeHex(version[:], header[:versionHexLen]) || version[0] == versionInvalid {
		return false
	}
	//: version 00 is exactly 55 bytes.
	if version[0] == 0 {
		return len(header) == traceParentLen
	}
	//: a higher version may continue, after a dash.
	return len(header) == traceParentLen || header[traceParentLen] == '-'
}

// decodeHexID decodes hex into dst and refuses the all-zero value the
// specification makes invalid.
func decodeHexID(dst, hex []byte) bool {
	//: width, alphabet, and the bytes decoded.
	if len(hex) != 2*len(dst) || !decodeHex(dst, hex) {
		return false
	}
	//: non-zero: a zero byte somewhere is not all-zero.
	for _, b := range dst {
		if b != 0 {
			return true
		}
	}
	//: all zeroes, which §3.2.2.3 and §3.2.2.4 forbid.
	return false
}

// decodeHex decodes 2*len(dst) lowercase hex digits from src into dst and
// reports whether all of them were; dst is undefined on false.
func decodeHex(dst, src []byte) bool {
	//: the digits, paired.
	for i := range dst {
		high, low := nibble(src[2*i]), nibble(src[2*i+1])
		//: a byte outside [0-9a-f]: the grammar's lowercase is checked here.
		if high == badNibble || low == badNibble {
			return false
		}
		dst[i] = high<<nibbleBits | low
	}
	//: every digit read.
	return true
}

// nibble is the value of a lowercase hex digit, or badNibble.
func nibble(c byte) byte {
	switch {
	//: 0-9.
	case c >= '0' && c <= '9':
		return c - '0'
	//: a-f.
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	//: uppercase, a dash, anything else.
	default:
		return badNibble
	}
}

// AppendTraceParent appends a version-00 traceparent for context to dst, the
// flags sanitized as FormatTraceParent sanitizes them: the header a caller
// writes without an allocation. An invalid context appends nothing.
func AppendTraceParent(dst []byte, context SpanContextValue) []byte {
	//: no joinable span, no header.
	if !context.IsValid() {
		return dst
	}
	dst = append(dst, '0', '0', '-')
	dst = appendHex(dst, context.TraceID[:])
	dst = append(dst, '-')
	dst = appendHex(dst, context.SpanID[:])
	dst = append(dst, '-')
	flags := [1]byte{byte(context.Flags.Sanitized())}
	return appendHex(dst, flags[:])
}

// appendHex appends src as lowercase hex.
func appendHex(dst, src []byte) []byte {
	//: two digits a byte, in place.
	for _, b := range src {
		dst = append(dst, hexDigits[b>>nibbleBits], hexDigits[b&0x0f])
	}
	return dst
}
