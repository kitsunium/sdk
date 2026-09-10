// Package i18n — Accept-Language negotiation: RFC 9110 parsing over RFC 4647
// matching.
package i18n

import (
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
)

// maxRanges bounds how many Accept-Language elements one header contributes.
//
// A header is written by a stranger and can be arbitrarily long; parsing all
// of it would let a client spend the server's time on its own behalf. Thirty
// two is far past what any real client sends — browsers send between one and
// six — and the elements beyond it are IGNORED rather than treated as an
// error, because failing a request over a peculiar header is the mistake this
// whole file is written to avoid.
const maxRanges int = 32

// defaultQuality is the weight of an element that carries no "q" parameter.
// RFC 9110 §12.4.2 fixes it at 1.
const defaultQuality int = 1000

// wildcardRange is the one range RFC 4647 §3.4 says to skip during Lookup.
const wildcardRange string = "*"

// element is one parsed Accept-Language entry.
type element struct {
	// text is the language range, with parameters and whitespace removed.
	text string
	// quality is the q parameter in thousandths, so the comparison is
	// integer: "q=0.8" is 800. A float would sort two headers that differ
	// only in trailing zeros differently on some inputs, for no benefit.
	quality int
	// order is the element's position in the header, which breaks quality
	// ties. RFC 9110 gives no tie rule; header order is the only information
	// available and is what every client intends.
	order int
}

// lookup implements RFC 4647 §3.4 over the supported set: the longest
// supported tag that equals a truncation of the range.
//
// Truncating the range and comparing is the same relation as asking whether
// the tag is a subtag-prefix of the range, so this needs no parsing and no
// allocation at all — which is why a negotiation costs no garbage per request.
func (n *Negotiator) lookup(rangeText string) (best corei18n.TagValue, ok bool) {
	//: the longest match wins, which is what progressive truncation means.
	longest := -1
	//: over the fixed supported set.
	for _, candidate := range n.supported {
		//: the canonical spelling the Tag already holds.
		text := candidate.String()
		//: a shorter match cannot beat one already found.
		if len(text) <= longest {
			//: skip.
			continue
		}
		//: does the range truncate down to this tag?
		if !subtagPrefixFold(rangeText, text) {
			//: no.
			continue
		}
		//: a longer match.
		best, longest = candidate, len(text)
	}
	//: found or not.
	return best, longest >= 0
}

// refused reports whether any zero-weight element refuses tag, by RFC 4647
// §3.3.1 basic filtering: the range is a subtag-prefix of the tag.
func refused(elems []element, tag corei18n.TagValue) bool {
	//: the canonical spelling.
	text := tag.String()
	//: only the zero-weight elements matter here.
	for _, elem := range elems {
		//: a preference, not a refusal.
		if elem.quality != 0 {
			//: skip.
			continue
		}
		//: "*;q=0" would refuse everything, which would leave nothing to
		//: render; it is deliberately not honoured — see Negotiate.
		if elem.text == wildcardRange {
			//: skip.
			continue
		}
		//: basic filtering: "en;q=0" refuses "en" and "en-GB" alike.
		if subtagPrefixFold(text, elem.text) {
			//: refused.
			return true
		}
	}
	//: acceptable.
	return false
}

// subtagPrefixFold reports whether prefix is whole, or is whole truncated at a
// subtag boundary, comparing ASCII case-insensitively.
//
// "en" is a subtag prefix of "en-GB" and of "EN"; it is not one of "eng",
// because "eng" is a different language and a prefix test that ignored the
// boundary would make it one.
func subtagPrefixFold(whole, prefix string) bool {
	//: a longer prefix cannot be one, and a shorter one must stop at a subtag
	//: boundary — "eng" is not "en" plus a subtag.
	if len(prefix) > len(whole) || (len(prefix) != len(whole) && whole[len(prefix)] != '-') {
		//: no.
		return false
	}
	//: ASCII case folding — BCP 47 subtags are ASCII and case insensitive.
	return strings.EqualFold(whole[:len(prefix)], prefix)
}

// sortByQuality orders elems by descending quality, keeping header order for
// ties.
//
// It is an insertion sort rather than sort.SliceStable because the input is at
// most maxRanges long and lives in the caller's stack array: handing it to
// sort.Interface would move it to the heap and put an allocation on the
// request path to sort six elements.
func sortByQuality(elems []element) {
	//: standard insertion sort, stable by construction.
	for i := 1; i < len(elems); i++ {
		//: the element being placed.
		current := elems[i]
		//: shift every strictly weaker element right.
		j := i - 1
		//: strictly, so equal qualities keep their header order.
		for j >= 0 && elems[j].quality < current.quality {
			//: shift.
			elems[j+1] = elems[j]
			//: continue left.
			j--
		}
		//: place it.
		elems[j+1] = current
	}
}
