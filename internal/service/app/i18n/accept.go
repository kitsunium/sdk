// Package i18n — the RFC 9110 Accept-Language header parser.
package i18n

import "strings"

// The three constants the qvalue grammar is written in.
const (
	// qualityScale is the number of fraction digits RFC 9110 §12.4.2 allows —
	// exactly three, which is why an integer in thousandths is exact rather
	// than an approximation of the float.
	qualityScale int = 3
	// digitBase is ten, because that is what a decimal DIGIT is worth.
	digitBase int = 10
	// qualityParam is the one parameter name this header defines.
	qualityParam string = "q"
	// integerPartLen is the width of a qvalue with no fraction at all: "0"
	// and "1" are complete qvalues.
	integerPartLen int = 1
	// decimalPointOffset is where the "." sits in "0.ddd", and
	// fractionOffset is where the digits start.
	decimalPointOffset int = 1
	fractionOffset     int = 2
)

// parseAcceptLanguage appends every usable element of header to dst and
// returns it. It never fails: an element it cannot use is skipped.
//
// The grammar is RFC 9110 §12.5.4:
//
//	Accept-Language = #( language-range [ weight ] )
//	weight          = OWS ";" OWS "q=" qvalue
//	qvalue          = ( "0" [ "." 0*3DIGIT ] ) / ( "1" [ "." 0*3"0" ] )
//
// Skipped, each deliberately and each with a test: an empty element (the
// trailing comma every hand-written header eventually grows), a range with a
// character outside the BCP 47 alphabet, a malformed qvalue ("q=2", "q=abc",
// "q=0.5000"), and an element carrying any parameter other than q. Skipping
// rather than failing is the whole posture — see [Negotiator.Negotiate].
func parseAcceptLanguage(header string, dst []element) []element {
	//: the header is a comma-separated list; position doubles as the tie
	//: breaker, so it is counted over the raw elements rather than the kept
	//: ones — two clients sending the same header must sort identically.
	position := 0
	//: strings.Cut rather than strings.Split. Measured: Split was 31.6 % of
	//: the allocated objects and 12.5 % of the CPU on the negotiation path
	//: (go tool pprof, strings.genSplit), because it builds a []string the
	//: loop reads once and drops. Cutting in place makes Negotiate allocate
	//: NOTHING at all, which is what puts it on a request path without a
	//: caveat. See BENCH.md §"What pprof showed".
	rest := header
	//: walk the list.
	for rest != "" {
		//: the bound is on what is KEPT, so a long header of unusable
		//: elements cannot push a usable one out.
		if len(dst) == maxRanges {
			//: stop parsing.
			break
		}
		//: the next element, and what follows it.
		raw, tail, more := strings.Cut(rest, ",")
		//: advance; when there is no comma left this is the final element.
		if more {
			//: continue after the comma.
			rest = tail
		} else {
			//: the last element.
			rest = ""
		}
		//: this element's position in the header.
		order := position
		//: advance regardless of whether the element is usable.
		position++
		//: parse it.
		elem, ok := parseElement(raw, order)
		//: an unusable element is skipped, never fatal.
		if !ok {
			//: next.
			continue
		}
		//: keep it.
		dst = append(dst, elem)
	}
	//: the usable elements, in header order.
	return dst
}

// parseElement parses one "range[;q=weight]" element.
func parseElement(raw string, order int) (elem element, ok bool) {
	//: the range and the parameters.
	head, params, hasParams := strings.Cut(raw, ";")
	//: OWS is legal around every token.
	text := strings.TrimSpace(head)
	//: an empty range — a doubled or trailing comma — carries nothing.
	if !validRange(text) {
		//: skip.
		return element{}, false
	}
	//: no parameter means the full weight RFC 9110 §12.4.2 fixes at 1.
	if !hasParams {
		//: keep it.
		return element{text: text, quality: defaultQuality, order: order}, true
	}
	//: exactly one parameter is defined for this header, and it is q.
	quality, valid := parseWeight(params)
	//: anything else — a second parameter, an unknown one, a malformed
	//: qvalue — takes the element with it.
	if !valid {
		//: skip.
		return element{}, false
	}
	//: a weighted element.
	return element{text: text, quality: quality, order: order}, true
}

// parseWeight parses the parameter section of an element, which must be
// exactly one "q=" parameter.
func parseWeight(params string) (quality int, ok bool) {
	//: split the parameter.
	name, value, found := strings.Cut(params, "=")
	//: three ways an element is unusable, and all three take it with them
	//: rather than leaving the parser to guess: a SECOND parameter (none is
	//: defined for this header), a bare parameter with no value, and a
	//: parameter that is not the case-insensitive "q".
	if strings.Contains(params, ";") || !found || !strings.EqualFold(strings.TrimSpace(name), qualityParam) {
		//: skip.
		return 0, false
	}
	//: the qvalue grammar.
	return parseQuality(strings.TrimSpace(value))
}

// parseQuality parses an RFC 9110 qvalue into thousandths.
//
// The grammar is narrow and this implementation is exactly as narrow: "2" is
// refused, "0.5000" is refused (four fraction digits), and "1.5" is refused
// because the grammar allows only zeros after a leading 1. A wider parser
// would accept a header the sender's own stack would have refused, and the two
// would disagree about what the client asked for.
func parseQuality(text string) (quality int, ok bool) {
	//: an empty qvalue is malformed.
	if text == "" {
		//: skip.
		return 0, false
	}
	//: the integer part is 0 or 1 and nothing else.
	whole := text[0]
	//: refuse "2", "-1", ".5" and every other shape.
	if whole != '0' && whole != '1' {
		//: skip.
		return 0, false
	}
	//: "0" and "1" with no fraction are complete qvalues.
	if len(text) == integerPartLen {
		//: 0 or 1000.
		return wholeQuality(whole), true
	}
	//: anything after the integer part must begin with the decimal point.
	if text[decimalPointOffset] != '.' {
		//: skip.
		return 0, false
	}
	//: at most three fraction digits.
	return fractionQuality(whole, text[fractionOffset:])
}

// wholeQuality maps the integer part of a qvalue to thousandths.
func wholeQuality(whole byte) int {
	//: "1" is the full weight.
	if whole == '1' {
		//: 1000 thousandths.
		return defaultQuality
	}
	//: "0" is a refusal.
	return 0
}

// fractionQuality parses the fraction digits of a qvalue.
func fractionQuality(whole byte, frac string) (quality int, ok bool) {
	//: the grammar allows at most three.
	if len(frac) > qualityScale {
		//: skip.
		return 0, false
	}
	//: read them as an integer in thousandths.
	value, digitsOK := fractionDigits(frac)
	//: a non-digit is not a qvalue.
	if !digitsOK {
		//: skip.
		return 0, false
	}
	//: after a leading 1 the grammar allows only zeros, so "1.5" is refused
	//: and "1.000" is the full weight.
	if whole == '1' {
		//: refuse anything but zeros; otherwise the full weight.
		return defaultQuality, value == 0
	}
	//: "0.ddd" is what it says.
	return value, true
}

// fractionDigits reads up to qualityScale ASCII digits as an integer in
// thousandths, padding the absent positions with zero so "8" is 800.
func fractionDigits(frac string) (value int, ok bool) {
	//: exactly three positions, present or padded.
	for i := range qualityScale {
		//: an absent digit is a zero.
		digit := 0
		//: a present one must be a digit.
		if i < len(frac) {
			//: refuse a non-digit.
			if frac[i] < '0' || frac[i] > '9' {
				//: skip.
				return 0, false
			}
			//: its value.
			digit = int(frac[i] - '0')
		}
		//: shift and add.
		value = value*digitBase + digit
	}
	//: the thousandths.
	return value, true
}

// validRange reports whether text is a language range this parser will carry.
//
// The alphabet is ASCII letters, digits, "-" and "*" — the BCP 47 subtag
// alphabet plus RFC 4647's wildcard. Anything else is not a range any client
// meant to send, and carrying it would only give the matcher something that
// cannot match.
func validRange(text string) bool {
	//: an empty element carries no range.
	if text == "" {
		//: skip.
		return false
	}
	//: the alphabet.
	for i := range len(text) {
		//: letters, digits, hyphen and the wildcard.
		if !isRangeByte(text[i]) {
			//: skip.
			return false
		}
	}
	//: a range the matcher can work with.
	return true
}

// isRangeByte reports whether b may appear in a language range.
func isRangeByte(b byte) bool {
	//: ASCII letters, and digits for UN M.49 regions.
	letter := (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
	//: the subtag separator and RFC 4647's wildcard complete the alphabet.
	return letter || (b >= '0' && b <= '9') || b == '-' || b == '*'
}
