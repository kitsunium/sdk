// Package errs: parse.go provides ParseCode, the strict canonical parser
// for dotted-quad Code strings produced by Code.String(). The parser is
// intentionally NOT compatible with the Padded() form — that would make
// two textual representations round-trip to the same Code, violating the
// single-canonical-form invariant declared in ADR 0005 §3.2.
package errs

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
//
// Returns:
//   - c: the parsed Code on success.
//   - err: nil on success, *Error with CodeInvalidCodeString otherwise.
//     The returned error satisfies the typed-errors-only SDK rule.
func ParseCode(s string) (c Code, err error) {
	//: shortest valid form is "0.0.0.0" (7 bytes), longest "255.255.255.255" (15).
	if len(s) < 7 || len(s) > 15 {
		return 0, parseFailure(s, "length outside [7,15]")
	}

	//: scan segment by segment — single pass, no allocations.
	var octets [4]uint8
	var segStart int
	var segIdx int
	for i := 0; i <= len(s); i++ {
		//: treat the end-of-string as a virtual dot so the same branch handles
		//: the fourth segment without duplicated logic.
		atEnd := i == len(s)
		if !atEnd && s[i] != '.' {
			//: digit runs are validated when we finalise the segment.
			continue
		}
		//: we are at a dot (or end) — validate the segment we just finished.
		seg := s[segStart:i]
		if segIdx > 3 {
			return 0, parseFailure(s, "more than 4 segments")
		}
		v, ok := parseOctet(seg)
		if !ok {
			return 0, parseFailure(s, "segment "+seg+" invalid")
		}
		octets[segIdx] = v
		segIdx++
		segStart = i + 1
	}
	if segIdx != 4 {
		return 0, parseFailure(s, "expected 4 segments")
	}
	c = Pack(Major(octets[0]), Layer(octets[1]), PkgCode(octets[2]), Serial(octets[3]))
	//: reject the zero Code — "0.0.0.0" roundtrips from Code(0).String() but is
	//: the reserved "no code" sentinel; Define/Wrap refuse it so parsing it
	//: would yield a value that cannot be used in any sentinel match.
	if c == 0 {
		return 0, parseFailure(s, "0.0.0.0 is the reserved zero sentinel")
	}
	return c, nil
}

// parseOctet validates and returns a single octet per ParseCode rules.
// Extracted so the ParseCode main loop stays under the SDK's cyclomatic
// complexity ceiling.
//
// Params:
//   - seg: the raw segment between two dots (or end markers).
//
// Returns:
//   - v: the numeric value (0-255) on success.
//   - ok: true iff seg satisfies all segment rules.
func parseOctet(seg string) (v uint8, ok bool) {
	//: empty or too-long segment → reject (must be 1-3 digits).
	if len(seg) == 0 || len(seg) > 3 {
		return 0, false
	}
	//: v5 rule — no leading zero except the literal "0" itself.
	if len(seg) > 1 && seg[0] == '0' {
		return 0, false
	}
	//: accumulate digits and validate ASCII-digit membership.
	var n int
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	//: overflow check — a 3-digit number can still exceed 255 ("256"..."999").
	if n > 255 {
		return 0, false
	}
	return uint8(n), true
}

// parseFailure builds the *Error returned by ParseCode. Wrapping it in a
// helper keeps the call sites compact and centralises the Reason / Public
// messaging. During W1 the underlying Define still takes int; W2 flips it.
//
// Params:
//   - input: the malformed string (for diagnostics; truncated if very long).
//   - detail: a short phrase describing which rule failed.
//
// Returns:
//   - *Error: typed error satisfying the SDK's typed-errors-only rule.
func parseFailure(input, detail string) (err *Error) {
	//: trim input to avoid logging adversarial gigantic payloads.
	shown := input
	if len(shown) > 32 {
		shown = shown[:32] + "..."
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
