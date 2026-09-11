// Package i18n — the language negotiator a server builds once and uses on
// every request.
package i18n

import (
	"slices"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Negotiator resolves an Accept-Language header to one of a fixed set of
// languages.
//
// It is built once, at wiring time, and used on every request. That split is
// the point: everything that can be wrong — an empty supported set, an unset
// default, a default that is not itself supported — is refused HERE, so
// [Negotiator.Negotiate] has no failure mode at all and never has to decide
// what to do about a header it dislikes.
type Negotiator struct {
	// supported is the set a negotiation may return, in the caller's order.
	supported []corei18n.TagValue
	// fallback is returned when nothing in the header matches.
	fallback corei18n.TagValue
}

// NewNegotiator returns a [Negotiator] over supported, falling back to
// fallback, or refuses.
//
// Refused: an empty supported set ([NegotiationEmpty] — ADR 0031's refuse
// half, since an empty set is an unfinished wiring rather than "accept
// anything"), a zero fallback ([CatalogInvalid] — the SDK does not pick a
// language), and a fallback outside the supported set ([CatalogInvalid] —
// otherwise every unmatched request resolves to a language the caller declared
// it does not serve).
func NewNegotiator(supported []corei18n.TagValue, fallback corei18n.TagValue) (negotiator *Negotiator, err error) {
	//: an empty set can only ever return the default.
	if len(supported) == 0 {
		//: refuse the wiring rather than hide it behind a working program.
		return nil, errs.Wrap(NegotiationEmpty, errs.WrapParams{})
	}
	//: a fallback is mandatory and is never guessed.
	if fallback.IsZero() {
		//: ADR 0031: refuse where any SDK-chosen value would be arbitrary.
		return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{}, errs.String("detail", "the negotiation fallback tag is unset"))
	}
	//: the fallback must be answerable.
	if !slices.Contains(supported, fallback) {
		//: otherwise the common path returns a language nobody supports.
		return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{},
			errs.String("tag", fallback.String()), errs.String("detail", "the negotiation fallback is not in the supported set"))
	}
	//: clone in — the caller's slice must not be able to change the set that
	//: every request is negotiated against.
	return &Negotiator{supported: slices.Clone(supported), fallback: fallback}, nil
}

// Supported returns the set this negotiator may return, as a copy.
func (n *Negotiator) Supported() []corei18n.TagValue {
	//: clone out, so a caller cannot reorder the set every request reads.
	return slices.Clone(n.supported)
}

// Negotiate resolves an RFC 9110 Accept-Language header to a supported tag.
// It NEVER fails and never returns the zero [corei18n.TagValue].
//
// # A malformed header is not an error
//
// The header is written by a stranger. RFC 4647 §3.4 prescribes exactly one
// response to a range it cannot use — skip it — and returning an error would
// invite a caller to fail the request, which is how a peculiar browser
// setting becomes a 400 nobody can reproduce. So an unparsable element is
// skipped, an unparsable q is skipped with its element, an empty header
// resolves to the fallback, and a header of pure garbage resolves to the
// fallback. Each of those is a named test. It is the same call ADR 0051 made
// for a malformed `traceparent`.
//
// # What is implemented, and what is refused BY NAME
//
//   - RFC 4647 §3.4 **Lookup** decides which tag is SELECTED. The range is
//     truncated one subtag at a time and the longest supported tag that
//     equals a truncation wins. This is exactly the algorithm, including its
//     consequence: a range of "en" does NOT select a supported "en-GB",
//     because Lookup truncates the range and never extends it. Name the
//     catalogue file after the base language and add a region variant only
//     when it really differs.
//   - RFC 4647 §3.3.1 **Basic Filtering** decides which tags a "q=0" element
//     REFUSES. "en;q=0" refuses "en", "en-GB" and "en-Latn-GB", because that
//     is what a client saying "not English" means. This is the one place the
//     two RFC 4647 mechanisms coexist, and they are on opposite sides of the
//     match on purpose.
//   - RFC 4647 §3.3.2 **Extended Filtering** is refused by name. Its wildcards
//     sit INSIDE a range ("de-*-DE"), no such range can match anything here,
//     and implementing it would mean a matcher with its own grammar for a
//     syntax no client sends.
//   - The wildcard range "*" is SKIPPED during selection, which is what §3.4
//     requires. "*;q=0" — "nothing but what I listed" — therefore changes
//     nothing: this function always returns a tag, because a page has to
//     render, and RFC 9110 §12.5.4 itself discourages answering 406 to an
//     Accept-Language a server cannot satisfy.
//   - An element with any parameter other than "q" is skipped whole. RFC 9110
//     defines no other parameter for this header, and guessing at one is how
//     an extension becomes a security surface.
func (n *Negotiator) Negotiate(header string) corei18n.TagValue {
	//: an absent header is the overwhelmingly common case on server-to-server
	//: traffic and must not cost a parse.
	if header == "" {
		//: the fallback.
		return n.fallback
	}
	//: a fixed buffer: the parse allocates nothing, so a request that carries
	//: a header costs the same as one that does not.
	var buf [maxRanges]element
	//: parse, skipping every element that cannot be used.
	elems := parseAcceptLanguage(header, buf[:0])
	//: nothing usable.
	if len(elems) == 0 {
		//: the fallback.
		return n.fallback
	}
	//: strongest preference first, header order breaking ties.
	sortByQuality(elems)
	//: select.
	return n.selectTag(elems)
}

// selectTag walks the sorted elements and returns the first supported tag a
// positive-weight range selects and no zero-weight range refuses.
func (n *Negotiator) selectTag(elems []element) corei18n.TagValue {
	//: in descending preference.
	for _, elem := range elems {
		//: a zero weight is a refusal, not a preference; it is consulted by
		//: refused() and never selects anything.
		if elem.quality == 0 {
			//: skip.
			continue
		}
		//: RFC 4647 §3.4: the wildcard is skipped during Lookup.
		if elem.text == wildcardRange {
			//: skip.
			continue
		}
		//: the longest supported tag this range's truncation reaches.
		best, ok := n.lookup(elem.text)
		//: no supported language under this range.
		if !ok {
			//: try the next preference.
			continue
		}
		//: an explicit "q=0" elsewhere in the header vetoes it.
		if refused(elems, best) {
			//: try the next preference.
			continue
		}
		//: the negotiated language.
		return best
	}
	//: nothing in the header could be served.
	return n.fallback
}
