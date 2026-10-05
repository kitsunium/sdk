package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// The subtag lengths BCP 47 fixes for the three positions this domain admits.
const (
	// minLanguageLen is the shortest ISO 639 language subtag ("fr").
	minLanguageLen int = 2
	// maxLanguageLen is the longest one this domain accepts ("fil"). BCP 47
	// also registers 4-letter (reserved) and 5-to-8-letter subtags; both are
	// refused — see [NewTag].
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
	// produce. It sizes the stack buffer NewTag canonicalises into.
	maxTagLen int = maxLanguageLen + separatorLen + scriptLen + separatorLen + regionDigitLen
	// asciiCaseBit is the single bit that separates 'a' from 'A'.
	asciiCaseBit byte = 'a' - 'A'
)

// NewTag assembles a [TagValue] from its subtags, or returns [InvalidTag].
//
// language is mandatory: 2 or 3 ASCII letters. script is empty or 4 ASCII
// letters; region is empty, 2 ASCII letters or 3 ASCII digits. That is exactly
// the `language[-Script][-REGION]` shape this domain admits, and the case is
// normalised — lowercase language, Titlecase script, UPPERCASE alpha region —
// because a tag differing only in case is the same language, and two tags for
// one language would split a catalogue in half.
//
// It reads no text. Splitting a written tag into its subtags, and refusing BY
// NAME everything BCP 47 or RFC 4647 can spell outside the subset — the POSIX
// spelling, extensions, variants, private use, ranges — is the parser's,
// internal/service/app/i18n's ParseTag (ADR 0160: a wire format is a
// mechanism). What is checked here is what makes a TagValue a tag at all,
// whoever assembles it, so no path into the type skips it.
func NewTag(language, script, region string) (tag TagValue, err error) {
	//: the language subtag is mandatory: 2 or 3 ASCII letters, nothing else.
	if !isAlpha(language) || len(language) < minLanguageLen || len(language) > maxLanguageLen {
		//: a 4-letter subtag here is a reserved BCP 47 form; longer is a
		//: registered one. Both are outside the subset.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("detail", "language subtag is not 2 or 3 letters"))
	}
	//: an optional script is exactly four letters.
	if script != "" && (len(script) != scriptLen || !isAlpha(script)) {
		//: name what was wrong; never guess what was meant.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("detail", "script subtag is not 4 letters"))
	}
	//: an optional region is two letters or three digits.
	if region != "" && !isRegion(region) {
		//: name what was wrong; never guess what was meant.
		return TagValue{}, errs.Wrap(InvalidTag, errs.WrapParams{}, errs.String("detail", "region subtag is not 2 letters or 3 digits"))
	}
	//: canonicalise into a stack buffer — one allocation, at the end.
	var buf [maxTagLen]byte
	//: lowercase is the canonical case for a language subtag.
	written := appendLower(buf[:0], language)
	//: record the lengths as the buffer grows, so the accessors can slice.
	built := TagValue{langN: uint8(len(language))} //nolint:gosec // bounded by maxLanguageLen above.
	//: Titlecase is the canonical case for a script subtag.
	if script != "" {
		//: written after its separator.
		written = appendTitle(append(written, '-'), script)
		//: the accessors slice it by this length.
		built.scriptN = uint8(scriptLen) //nolint:gosec // scriptLen is the constant 4.
	}
	//: uppercase for an alpha region; the digits are already canonical.
	if region != "" {
		//: written after its separator.
		written = appendUpper(append(written, '-'), region)
		//: the accessors slice it by this length.
		built.regionN = uint8(len(region)) //nolint:gosec // bounded by isRegion.
	}
	//: one allocation, here, for the canonical string every accessor slices.
	built.canonical = string(written)
	//: a canonical tag.
	return built, nil
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
// defines handled by an exact-tag entry in internal/service/app/i18n's table.
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
