// Package i18n — the language tag, and the BCP 47 subset it admits.
package i18n

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The subtag lengths BCP 47 fixes for the three positions this domain admits.
const (
	// minLanguageLen is the shortest ISO 639 language subtag ("fr").
	minLanguageLen int = 2
	// maxLanguageLen is the longest one this domain accepts ("fil"). BCP 47
	// also registers 4-letter (reserved) and 5-to-8-letter subtags; both are
	// refused by name — see [ParseTag].
	maxLanguageLen int = 3
	// scriptLen is the ISO 15924 script subtag length, always 4 ("Hant").
	scriptLen int = 4
	// regionAlphaLen is the ISO 3166-1 alpha-2 region length ("CA").
	regionAlphaLen int = 2
	// regionDigitLen is the UN M.49 numeric region length ("419").
	regionDigitLen int = 3
	// separatorLen is the one byte a subtag separator occupies.
	separatorLen int = 1
	// maxTagLen is "fil-Hant-419": the longest canonical form the subset can
	// produce. It sizes the stack buffer ParseTag canonicalises into.
	maxTagLen int = maxLanguageLen + separatorLen + scriptLen + separatorLen + regionDigitLen
	// maxSubtags is language + script + region.
	maxSubtags int = 3
	// asciiCaseBit is the single bit that separates 'a' from 'A'.
	asciiCaseBit byte = 'a' - 'A'
)

// TagValue is a language, optionally narrowed by a script and a region:
// exactly the `language[-Script][-REGION]` shape of BCP 47 and deliberately
// nothing else. pkg/v1/i18n publishes it as `Tag`.
//
// It is a comparable struct, so it is a map key and an == operand, and it
// holds its canonical spelling, so [TagValue.String] allocates nothing. The
// zero TagValue is the absence of a tag: [TagValue.IsZero] reports it and
// [TagValue.String] renders "". ADR 0031 is honoured by REFUSAL rather than by
// a default — no constructor in this domain reads a zero tag as "use English",
// because the SDK choosing a language for an application is exactly the silent
// wrong answer the domain exists to prevent.
type TagValue struct {
	// canonical is the canonical form: lowercase language, Titlecase script,
	// UPPERCASE alpha region. The three accessors slice it, so they cost
	// nothing and cannot disagree with it.
	canonical string
	// langN, scriptN and regionN are the subtag lengths inside canonical. A
	// zero scriptN or regionN means the subtag is absent.
	langN, scriptN, regionN uint8
}

// ParseTag canonicalises text into a [TagValue], or returns [InvalidTag].
//
// # The subset, and everything refused BY NAME
//
// Accepted: a 2- or 3-letter language, an optional 4-letter script, and an
// optional 2-letter or 3-digit region, separated by "-". Case is normalised —
// "FR-latn-ca" and "fr-Latn-CA" are the same tag — because a tag differing
// only in case is the same language and two tags for one language would split
// a catalogue in half.
//
// Refused, each by name rather than parsed and dropped:
//
//   - The POSIX locale spelling ("fr_FR", "en_US.UTF-8"). Accepting a second
//     separator would mint a second tag for one language, and the two would
//     index different halves of the same catalogue.
//   - Extended language subtags ("zh-cmn-Hans"), deprecated by BCP 47 itself.
//   - Variants ("de-CH-1901", "sl-rozaj"), 5-to-8-character subtags.
//   - Extension sequences ("de-DE-u-co-phonebk", "-t-", any singleton).
//   - Private use ("x-pig-latin", "en-x-custom").
//   - Grandfathered and irregular tags ("i-klingon", "zh-min-nan").
//   - The language ranges of RFC 4647 — "*" and "de-*-DE". A range is not a
//     tag; internal/service/i18n's negotiation is where ranges are read.
//
// Dropping any of them silently would change which language answers without
// changing anything a reader can see: "de-DE-u-co-phonebk" would quietly
// become "de-DE", and the difference would surface as a sort order nobody can
// explain. The domain does not implement collation, so the honest answer is to
// refuse the tag that asked for one.
func ParseTag(text string) (tag TagValue, err error) {
	//: the empty string is the zero tag written out; it names no language.
	if text == "" {
		//: refuse rather than return the zero tag, which would let an unset
		//: configuration field parse successfully.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "empty"))
	}
	//: the canonical form of the subset never exceeds "fil-Hant-419".
	if len(text) > maxTagLen {
		//: too long for the subset — an extension or a variant, refused whole.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "outside the language[-Script][-REGION] subset"))
	}
	//: split on the one separator BCP 47 defines; "_" is refused by falling
	//: through to the subtag checks, which reject the underscore character.
	parts := strings.Split(text, "-")
	//: more than language, script and region is outside the subset.
	if len(parts) > maxSubtags {
		//: name what was found, never guess which subtag was meant.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "more than three subtags"))
	}
	//: canonicalise into a stack buffer — one allocation, at the end.
	return buildTag(text, parts)
}

// buildTag validates each subtag in turn and assembles the canonical form. It
// is separate from [ParseTag] so the shape checks and the assembly read as two
// steps rather than one nested branch.
func buildTag(original string, parts []string) (tag TagValue, err error) {
	//: a fixed buffer sized by the subset's longest canonical form.
	var buf [maxTagLen]byte
	//: the language subtag is mandatory and comes first.
	language := parts[0]
	//: 2 or 3 ASCII letters, and nothing else.
	if !isAlpha(language) || len(language) < minLanguageLen || len(language) > maxLanguageLen {
		//: a 4-letter subtag here is a reserved BCP 47 form; longer is a
		//: registered one. Both are outside the subset.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("tag", original), errs.String("detail", "language subtag is not 2 or 3 letters"))
	}
	//: lowercase is the canonical case for a language subtag.
	written := appendLower(buf[:0], language)
	//: record the lengths as the buffer grows, so the accessors can slice.
	built := TagValue{langN: uint8(len(language))} //nolint:gosec // bounded by maxLanguageLen above.
	//: the optional script and region follow, in that order.
	written, built, err = appendOptional(written, built, original, parts[1:])
	//: an unusable subtag stops the parse.
	if err != nil {
		//: InvalidTag, already carrying the detail.
		return TagValue{}, err
	}
	//: one allocation, here, for the canonical string every accessor slices.
	built.canonical = string(written)
	//: a canonical tag.
	return built, nil
}

// appendOptional consumes the script and region subtags, in the only order
// BCP 47 permits, and refuses anything else.
func appendOptional(dst []byte, built TagValue, original string, rest []string) (appended []byte, tag TagValue, err error) {
	//: nothing after the language is a complete tag on its own.
	for _, sub := range rest {
		//: a script subtag is exactly four letters and may come first.
		switch {
		//: Titlecase is the canonical case for a script subtag.
		case len(sub) == scriptLen && isAlpha(sub) && built.scriptN == 0 && built.regionN == 0:
			//: write it after the separator.
			dst = appendTitle(append(dst, '-'), sub)
			//: remember it so a second script is refused.
			built.scriptN = uint8(scriptLen) //nolint:gosec // scriptLen is the constant 4.
		//: uppercase for an alpha region; the digits are already canonical.
		case isRegion(sub) && built.regionN == 0:
			//: write it after the separator.
			dst = appendUpper(append(dst, '-'), sub)
			//: remember it so a second region is refused.
			built.regionN = uint8(len(sub)) //nolint:gosec // bounded by isRegion.
		//: a variant, an extension singleton, a private-use marker, a
		//: repeated subtag or an out-of-order one — refused by shape.
		default:
			//: name what was found; never guess what was meant.
			return nil, TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("tag", original), errs.String("detail", "subtag is not a script or region in the subset"))
		}
	}
	//: the assembled buffer and the recorded lengths.
	return dst, built, nil
}

// isRegion reports whether sub is an ISO 3166-1 alpha-2 or UN M.49 region.
func isRegion(sub string) bool {
	//: two letters, or three digits — the two forms BCP 47 admits.
	return (len(sub) == regionAlphaLen && isAlpha(sub)) || (len(sub) == regionDigitLen && isDigit(sub))
}

// isAlpha reports whether sub is non-empty and all ASCII letters.
func isAlpha(sub string) bool {
	//: an empty subtag (a doubled or trailing "-") is not alphabetic.
	if sub == "" {
		//: refuse.
		return false
	}
	//: ASCII only — BCP 47 subtags are ASCII by definition.
	for i := range len(sub) {
		//: reject anything outside A-Z and a-z, including "_" and digits.
		if (sub[i] < 'A' || sub[i] > 'Z') && (sub[i] < 'a' || sub[i] > 'z') {
			//: not a letter.
			return false
		}
	}
	//: all letters.
	return true
}

// isDigit reports whether sub is non-empty and all ASCII digits.
func isDigit(sub string) bool {
	//: an empty subtag is not numeric.
	if sub == "" {
		//: refuse.
		return false
	}
	//: ASCII digits only.
	for i := range len(sub) {
		//: reject anything outside 0-9.
		if sub[i] < '0' || sub[i] > '9' {
			//: not a digit.
			return false
		}
	}
	//: all digits.
	return true
}

// appendLower appends sub to dst, ASCII-lowercased.
func appendLower(dst []byte, sub string) []byte {
	//: subtags are ASCII, so byte arithmetic is the whole of the case fold.
	for i := range len(sub) {
		//: fold one byte.
		dst = append(dst, lowerByte(sub[i]))
	}
	//: the lowercased subtag.
	return dst
}

// appendUpper appends sub to dst, ASCII-uppercased.
func appendUpper(dst []byte, sub string) []byte {
	//: subtags are ASCII, so byte arithmetic is the whole of the case fold.
	for i := range len(sub) {
		//: fold one byte.
		dst = append(dst, upperByte(sub[i]))
	}
	//: the uppercased subtag.
	return dst
}

// appendTitle appends sub to dst with the first byte uppercased and the rest
// lowercased — the canonical case of an ISO 15924 script subtag.
func appendTitle(dst []byte, sub string) []byte {
	//: the first byte carries the capital.
	dst = append(dst, upperByte(sub[0]))
	//: the remaining bytes are lowercase.
	return appendLower(dst, sub[1:])
}

// lowerByte folds one ASCII byte to lower case.
func lowerByte(b byte) byte {
	//: only A-Z moves.
	if b >= 'A' && b <= 'Z' {
		//: the ASCII case bit.
		return b + asciiCaseBit
	}
	//: already lower, or not a letter.
	return b
}

// upperByte folds one ASCII byte to upper case.
func upperByte(b byte) byte {
	//: only a-z moves.
	if b >= 'a' && b <= 'z' {
		//: the ASCII case bit.
		return b - asciiCaseBit
	}
	//: already upper, or not a letter.
	return b
}

// String returns the canonical spelling, or "" for the zero tag. It allocates
// nothing: the canonical form is what the tag holds.
func (t TagValue) String() string {
	//: the zero tag names no language, and says so as the empty string rather
	//: than as a placeholder a log line could mistake for a language.
	if t.IsZero() {
		//: nothing to print.
		return ""
	}
	//: the stored canonical form is the answer.
	return t.canonical
}

// IsZero reports whether t names no language.
func (t TagValue) IsZero() bool {
	//: the canonical form of the zero tag is the empty string.
	return t.canonical == ""
}

// Language returns the lowercase language subtag, or "" for the zero tag.
//
// It is the key CLDR plural rules are resolved on — the rules of "fr-CA" are
// the rules of "fr" — with the handful of region-specific exceptions CLDR
// defines handled by an exact-tag entry in internal/service/i18n's table.
func (t TagValue) Language() string {
	//: the language subtag is the head of the canonical form.
	return t.canonical[:t.langN]
}

// Script returns the Titlecase script subtag, or "" when the tag carries none.
func (t TagValue) Script() string {
	//: absent is the empty string, not a zero-length slice of the canonical.
	if t.scriptN == 0 {
		//: no script.
		return ""
	}
	//: it sits immediately after the language and its separator.
	start := int(t.langN) + separatorLen
	//: slice it in place — no allocation.
	return t.canonical[start : start+int(t.scriptN)]
}

// Region returns the UPPERCASE alpha-2 or numeric region subtag, or "" when
// the tag carries none.
func (t TagValue) Region() string {
	//: absent is the empty string.
	if t.regionN == 0 {
		//: no region.
		return ""
	}
	//: the region is always last in the canonical form.
	return t.canonical[len(t.canonical)-int(t.regionN):]
}

// Parent removes the most specific subtag and reports whether one was removed:
// "fr-Latn-CA" → "fr-Latn" → "fr" → (zero tag, false).
//
// It is the truncation RFC 4647 §3.4 Lookup performs, and it is a value
// operation rather than a negotiation policy, so it lives here. It allocates
// nothing: the parent's canonical form is a prefix of the child's.
//
// RFC 4647 §3.4 also says a single-character subtag is skipped rather than
// used as a lookup key, because a lone "u" or "x" is an extension singleton
// and not a language. This subset admits no single-character subtag at all, so
// the rule is satisfied by the grammar and there is nothing to skip — stated
// here because a reader comparing this code to the RFC will look for it.
func (t TagValue) Parent() (parent TagValue, ok bool) {
	//: strip the region first — it is the most specific subtag.
	if t.regionN != 0 {
		//: the parent's canonical form is a prefix of this one.
		return TagValue{
			canonical: t.canonical[:len(t.canonical)-int(t.regionN)-separatorLen],
			langN:     t.langN,
			scriptN:   t.scriptN,
		}, true
	}
	//: then the script.
	if t.scriptN != 0 {
		//: language only.
		return TagValue{
			canonical: t.canonical[:len(t.canonical)-int(t.scriptN)-separatorLen],
			langN:     t.langN,
		}, true
	}
	//: a bare language has no parent; the zero tag is not one.
	return TagValue{}, false
}
