// Package trace — the traceparent header: parsing it, and writing it back.
package trace

import (
	"encoding/hex"
	"strings"
)

// The traceparent field offsets, derived from the grammar rather than counted
// by hand:
//
//	value          = version "-" version-format
//	version        = 2HEXDIGLC
//	version-format = trace-id "-" parent-id "-" trace-flags
//	trace-id       = 32HEXDIGLC
//	parent-id      = 16HEXDIGLC
//	trace-flags    = 2HEXDIGLC
//
// which is 2 + 1 + 32 + 1 + 16 + 1 + 2 = 55 characters.
const (
	// TraceParentLen is the length of a version-00 traceparent, exactly.
	TraceParentLen int = 55
	// traceParentSep separates the four components.
	traceParentSep string = "-"
	// versionHexLen is the width of the version field.
	versionHexLen int = 2
	// flagsHexLen is the width of the trace-flags field.
	flagsHexLen int = 2
)

// The three field offsets inside the fixed 55-character prefix, derived from
// the widths above rather than written as literals so the grammar and the
// arithmetic cannot drift apart. Every version keeps these fields exactly where
// version 00 puts them (§3.2.4), which is what makes fixed-offset reading
// correct here and not merely convenient.
const (
	// traceIDOffset is where trace-id starts: past the version and its dash.
	traceIDOffset int = versionHexLen + 1
	// parentIDOffset is where parent-id starts.
	parentIDOffset int = traceIDOffset + TraceIDHexLen + 1
	// flagsOffset is where trace-flags starts.
	flagsOffset int = parentIDOffset + SpanIDHexLen + 1
)

// The two versions the specification names explicitly.
const (
	// VersionSupported is the version this SDK emits: "00", the only version
	// the current Recommendation defines.
	VersionSupported string = "00"
	// versionInvalid is "ff", which §3.2.2.1 declares invalid outright — it is
	// reserved so that a forward-version negotiation can never collide with it.
	versionInvalid string = "ff"
)

// TraceParentHeader and TraceStateHeader are the two header names W3C Trace
// Context defines. Both are lowercase: §3.2.1 asks a vendor to SEND the name in
// lowercase while accepting any case on receipt, which is what http.Header's
// canonicalisation already does for a Go receiver.
const (
	// TraceParentHeader carries the span context.
	TraceParentHeader string = "traceparent"
	// TraceStateHeader carries the vendor list.
	TraceStateHeader string = "tracestate"
)

// ParseTraceParent reads a traceparent header value into a span context.
//
// It implements §3.2.4 "Versioning of traceparent" rather than only the
// version-00 grammar, and the difference is the whole point of the section: a
// future version is FORWARD-COMPATIBLE by construction, because every version
// is required to keep the first 55 characters exactly where version 00 puts
// them. So:
//
//   - "ff" is refused by name — the specification declares it invalid.
//   - "00" must be EXACTLY 55 characters. The version-00 grammar has no
//     extension point, so trailing content is not "an unknown field", it is a
//     header this version cannot describe.
//   - A HIGHER version is parsed from its first 55 characters, and anything
//     beyond them must begin with a dash. Without that check a 56-character
//     header whose 56th byte is a hex digit would be read as a valid context
//     with a truncated flag field.
//   - Nothing past the flags is looked at: "vendors MUST NOT parse or assume
//     anything about unknown fields for this version."
//
// The returned context is marked Remote, because a traceparent by definition
// came from somewhere else.
//
// A refusal is never the caller's cue to fail the request. §4.3 is explicit
// that a vendor confronted with a malformed traceparent "creates a new
// traceparent header and deletes tracestate" — the header is written by a
// stranger, and a request rejected over it is a denial of service with extra
// steps. See Extract, which is the shape that behaviour belongs in.
func ParseTraceParent(header string) (context SpanContextValue, err error) {
	//: a header shorter than the fixed prefix cannot carry the four fields, in
	//: any version — the specification's own first test.
	if len(header) < TraceParentLen {
		//: nothing of the header is echoed; it is attacker-controlled.
		return SpanContextValue{}, InvalidTraceParent
	}
	//: the version governs how the rest is read, so it is decided first.
	version := header[:versionHexLen]
	//: three refusals with one effect: "ff" is invalid outright, a non-hex
	//: version is not a version at all, and whatever follows the fixed 55-char
	//: prefix must be legal for THIS version — version 00 has no extension
	//: point, and a higher one may only continue after a dash.
	if version == versionInvalid || !isLowerHex(version) || !hasValidTraceParentTail(version, header) {
		//: nothing of the header is echoed; it is attacker-controlled.
		return SpanContextValue{}, InvalidTraceParent
	}
	//: the first 55 characters are the same four fields in every version.
	return parseTraceParentPrefix(header[:TraceParentLen])
}

// hasValidTraceParentTail reports whether whatever follows the fixed 55-character
// prefix is legal for this version.
func hasValidTraceParentTail(version, header string) bool {
	//: version 00 is exactly 55 characters — there is no tail.
	if version == VersionSupported {
		//: any trailing byte makes it a header version 00 cannot describe.
		return len(header) == TraceParentLen
	}
	//: a higher version either ends at 55 or continues after a dash.
	return len(header) == TraceParentLen || strings.HasPrefix(header[TraceParentLen:], traceParentSep)
}

// parseTraceParentPrefix reads the four dash-separated fields of a 55-character
// traceparent prefix.
func parseTraceParentPrefix(prefix string) (context SpanContextValue, err error) {
	//: the three dashes sit at fixed positions in every version (§3.2.4), so
	//: checking them in place is what splitting used to check by field count —
	//: and it reads the fields without building a slice to hold them.
	//: strings.Split allocated a four-element []string on EVERY inbound
	//: request, which the memory profile put at 92.6 % of this function's
	//: allocated objects.
	if prefix[traceIDOffset-1] != '-' || prefix[parentIDOffset-1] != '-' || prefix[flagsOffset-1] != '-' {
		//: a misplaced dash is a header this grammar cannot describe.
		return SpanContextValue{}, InvalidTraceParent
	}
	//: an all-zero or non-hex trace-id is refused by ParseTraceID, which also
	//: enforces the width — so a dash landing inside the field is caught there.
	traceID, traceErr := ParseTraceID(prefix[traceIDOffset : traceIDOffset+TraceIDHexLen])
	//: surface the typed refusal.
	if traceErr != nil {
		//: refuse.
		return SpanContextValue{}, traceErr
	}
	//: an all-zero or non-hex parent-id is refused by ParseSpanID.
	spanID, spanErr := ParseSpanID(prefix[parentIDOffset : parentIDOffset+SpanIDHexLen])
	//: surface the typed refusal.
	if spanErr != nil {
		//: refuse.
		return SpanContextValue{}, spanErr
	}
	//: the flag byte is kept whole, undefined bits included — see
	//: TraceFlags.Sanitized for why masking happens on output instead.
	flags, flagsErr := parseTraceFlags(prefix[flagsOffset:])
	//: surface the typed refusal.
	if flagsErr != nil {
		//: refuse.
		return SpanContextValue{}, flagsErr
	}
	//: a traceparent always names a span that ran somewhere else.
	return SpanContextValue{TraceID: traceID, SpanID: spanID, Flags: flags, Remote: true}, nil
}

// parseTraceFlags reads the two-hex-digit trace-flags field.
func parseTraceFlags(text string) (flags TraceFlags, err error) {
	//: width and alphabet before any decode.
	if len(text) != flagsHexLen || !isLowerHex(text) {
		//: refuse.
		return 0, InvalidTraceParent
	}
	//: decode into a fixed array, as ParseTraceID does: DecodeString RETURNS a
	//: fresh slice, so it heap-allocated one byte per inbound request.
	var decoded [1]byte
	//: unreachable after the checks above, and still not ignored.
	if _, decodeErr := hex.Decode(decoded[:], []byte(text)); decodeErr != nil {
		//: refuse.
		return 0, InvalidTraceParent
	}
	//: the byte exactly as received.
	return TraceFlags(decoded[0]), nil
}

// FormatTraceParent renders a span context as a version-00 traceparent header
// value, reporting ok=false when the context names no joinable span.
//
// The pair is deliberate rather than an empty string: "there is no header for
// this context" is a real outcome, not a degenerate rendering. §3.2.2.3 and
// §3.2.2.4 make an all-zero identifier a value every conforming receiver MUST
// ignore, so emitting one would spend a header to say nothing and would make
// "we lost the context here" indistinguishable from "we never had one". A
// caller writes the header only when ok.
//
// The flag byte is SANITIZED here and nowhere else: §3.2.2.5.2 requires a vendor
// to set every undefined bit to zero, and this is the one moment this SDK is
// acting as a producer. A bit received from an upstream that defines it is
// preserved in the SpanContextValue and dropped on the way out, which is the
// only combination that satisfies both halves of the specification.
func FormatTraceParent(context SpanContextValue) (header string, ok bool) {
	//: an invalid context has no header at all.
	if !context.IsValid() {
		//: nothing to write, and the caller is told so.
		return "", false
	}
	//: exactly 55 bytes, so the builder never grows.
	var out strings.Builder
	out.Grow(TraceParentLen)
	//: version, trace-id, parent-id, trace-flags — in the grammar's order.
	out.WriteString(VersionSupported)
	out.WriteString(traceParentSep)
	out.WriteString(context.TraceID.String())
	out.WriteString(traceParentSep)
	out.WriteString(context.SpanID.String())
	out.WriteString(traceParentSep)
	//: the flag byte, masked down to the bits version 00 defines.
	out.WriteString(hex.EncodeToString([]byte{byte(context.Flags.Sanitized())}))
	//: the rendered header value.
	return out.String(), true
}
