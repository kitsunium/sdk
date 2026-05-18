// Package errs — provides ParseCode, the strict canonical parser
// for dotted-quad Code strings produced by Code.String(). The parser is
// intentionally NOT compatible with the Padded() form — that would make
// two textual representations round-trip to the same Code, violating the
// single-canonical-form invariant declared in ADR 0005 §3.2.
package errs

// Canonical-form length and shape limits for ParseCode.
const (
	// codeStringMinLen is the shortest valid canonical form: "0.0.0.0".
	codeStringMinLen int = 7
	// codeStringMaxLen is the longest valid canonical form: "255.255.255.255".
	codeStringMaxLen int = 15
	// codeSegmentCount is the exact number of dot-separated segments.
	codeSegmentCount int = 4
	// codeSegmentLastIdx is the highest valid segment index (codeSegmentCount-1).
	codeSegmentLastIdx int = 3
	// codeSegmentMaxDigits is the upper bound on a single segment's digit count.
	codeSegmentMaxDigits int = 3
)

// Per-segment numeric upper bound for ParseCode.
// The radix for digit accumulation is the package-wide decimalBase (field.go).
const (
	// octetMax is the maximum value a single segment may represent.
	octetMax int = 255
)

// Diagnostics envelope sizing — bounds the echoed input in error messages.
const (
	// inputEchoMax caps the prefix of malformed input we include in private
	// diagnostics, preventing adversarial gigantic payloads from bloating logs.
	inputEchoMax int = 32
)

// Fixed-position indices for the four-octet array — naming them removes the
// raw 0/1/2/3 literals from the Pack call site.
const (
	// octetIdxMajor is the index of the Major octet inside the parsed array.
	octetIdxMajor int = iota
	// octetIdxLayer is the index of the Layer octet inside the parsed array.
	octetIdxLayer
	// octetIdxPackage is the index of the Package octet inside the parsed array.
	octetIdxPackage
	// octetIdxSerial is the index of the Serial octet inside the parsed array.
	octetIdxSerial
)

// ParseCode parses the canonical "M.L.P.S" form produced by Code.String().
//
// Rules (any violation returns an *Error with CodeInvalidCodeString):
//   - Exactly 4 dot-separated segments.
//   - Each segment is 1 to 3 ASCII digits.
//   - No leading zero in any segment ("001", "01", "0001" → REJECTED).
//     This excludes the Padded() form ("001.001.001.001") as input, so
//     the canonical form is the only textual key that maps to a Code.
//   - Each segment's numeric value is in [0, 255].
//   - No leading/trailing whitespace, no sign character, no empty segment.
func ParseCode(s string) (c Code, err error) {
	//: enforce the canonical-form length envelope before any per-byte work.
	if len(s) < codeStringMinLen || len(s) > codeStringMaxLen {
		//: out-of-envelope strings cannot be canonical — bail immediately.
		return 0, parseFailure(s, "length outside ["+itoaDecimal(uint8(codeStringMinLen))+","+itoaDecimal(uint8(codeStringMaxLen))+"]")
	}
	//: single pass, no allocations — walk and finalise segments at dots.
	octets, perr := scanOctets(s)
	//: surface scanOctets' framed failure as-is when present.
	if perr != nil {
		//: scanOctets already framed the failure detail — surface it as-is.
		return 0, perr
	}
	//: assemble the four parsed octets into a packed Code value.
	c = Pack(
		Major(octets[octetIdxMajor]),
		Layer(octets[octetIdxLayer]),
		PkgCode(octets[octetIdxPackage]),
		Serial(octets[octetIdxSerial]),
	)
	//: reject the zero Code — "0.0.0.0" roundtrips from Code(0).String() but is
	//: the reserved "no code" sentinel; Define/Wrap refuse it so parsing it
	//: would yield a value that cannot be used in any sentinel match.
	if c == 0 {
		//: returning here keeps the error path explicit for the zero sentinel.
		return 0, parseFailure(s, "0.0.0.0 is the reserved zero sentinel")
	}
	//: success — surface the packed Code with a nil error.
	return c, nil
}

// scanOctets walks s, finalises each dot-delimited segment via parseOctet,
// and returns the resulting four-octet array. Extracted from ParseCode to
// keep its cyclomatic complexity below the SDK ceiling.
func scanOctets(s string) (octets [codeSegmentCount]uint8, err error) {
	var segStart int
	var segIdx int
	//: treat end-of-string as a virtual dot so one branch handles all segments.
	for i := 0; i <= len(s); i++ {
		atEnd := i == len(s)
		//: keep accumulating digits until we hit a delimiter or the end.
		if !atEnd && s[i] != '.' {
			//: non-delimiter bytes are validated at segment finalisation time.
			continue
		}
		//: at a delimiter — finalise the segment that just ended.
		seg := s[segStart:i]
		//: a fifth (or later) segment means the input is over-long.
		if segIdx > codeSegmentLastIdx {
			//: a fifth (or later) segment means the input is over-long.
			return octets, parseFailure(s, "more than "+itoaDecimal(uint8(codeSegmentCount))+" segments")
		}
		octet, ok := parseOctet(seg)
		//: segment failed digit, leading-zero, or range rules.
		if !ok {
			//: segment failed digit, leading-zero, or range rules.
			return octets, parseFailure(s, "segment "+seg+" invalid")
		}
		octets[segIdx] = octet
		segIdx++
		segStart = i + 1
	}
	//: too few segments — under-long or missing delimiters.
	if segIdx != codeSegmentCount {
		//: too few segments — under-long or missing delimiters.
		return octets, parseFailure(s, "expected "+itoaDecimal(uint8(codeSegmentCount))+" segments")
	}
	//: success — every segment parsed cleanly.
	return octets, nil
}

// parseOctet validates and returns a single octet per ParseCode rules.
// Extracted so the ParseCode main loop stays under the SDK's cyclomatic
// complexity ceiling.
func parseOctet(seg string) (octet uint8, ok bool) {
	//: empty, oversize, or leading-zero segments are all canonical-form rejects.
	if len(seg) == 0 || len(seg) > codeSegmentMaxDigits || (len(seg) > 1 && seg[0] == '0') {
		//: collapse the three structural guards into one — same effect.
		return 0, false
	}
	value, vok := digitsToOctet(seg)
	//: digitsToOctet flagged either a non-digit byte or numeric overflow.
	if !vok {
		//: digitsToOctet flagged either a non-digit byte or numeric overflow.
		return 0, false
	}
	//: success — pass the decoded byte and the ok bit up the call stack.
	return value, true
}

// digitsToOctet reads seg as a base-10 unsigned integer and validates that
// every byte is an ASCII digit and that the result fits in a uint8.
// Extracted from parseOctet to keep its cyclomatic complexity below 9.
func digitsToOctet(seg string) (octet uint8, ok bool) {
	var n int
	//: accumulate digits left-to-right; range over len for the Go 1.22+ idiom.
	for i := range len(seg) {
		ch := seg[i]
		//: any non-digit byte invalidates the whole segment.
		if ch < '0' || ch > '9' {
			//: bail on first offender — no partial values escape.
			return 0, false
		}
		n = n*decimalBase + int(ch-'0')
	}
	//: a 3-digit number can still exceed octetMax ("256".."999").
	if n > octetMax {
		//: overflow — refuse the segment.
		return 0, false
	}
	//: success — narrowing the int to uint8 is safe past the bounds check.
	return uint8(n), true
}

// parseFailure builds the *Error returned by ParseCode. Wrapping it in a
// helper keeps the call sites compact and centralises the Reason / Public
// messaging.
func parseFailure(input, detail string) *Error {
	//: trim input to avoid logging adversarial gigantic payloads.
	shown := input
	//: keep the prefix and mark truncation explicitly when input is large.
	if len(shown) > inputEchoMax {
		//: keep the prefix and mark truncation explicitly.
		shown = shown[:inputEchoMax] + "..."
	}
	//: direct struct literal bypasses Define/validate (which runs its own
	//: rules). The parse failure is a runtime signal, not a sentinel
	//: registered at init — it MUST NOT trigger the validation machinery.
	return &Error{
		code:    CodeInvalidCodeString,
		reason:  "INVALID_CODE_STRING",
		public:  "invalid code string",
		private: "ParseCode(" + shown + "): " + detail,
	}
}
