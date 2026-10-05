package i18n

import (
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The subtag shapes the parser places by length; whether a subtag's
// characters are right for its position is corei18n.NewTag's to judge.
const (
	// maxTagBytes is the length of "fil-Hant-419", the longest canonical form
	// the subset can produce. A longer input is an extension or a variant,
	// refused whole before it is split.
	maxTagBytes int = len("fil-Hant-419")
	// maxSubtags is language + script + region.
	maxSubtags int = 3
	// scriptSubtagLen is the ISO 15924 script subtag length, always 4.
	scriptSubtagLen int = 4
	// alphaRegionLen is the ISO 3166-1 alpha-2 region length.
	alphaRegionLen int = 2
	// digitRegionLen is the UN M.49 numeric region length.
	digitRegionLen int = 3
)

// ParseTag canonicalises text into a [corei18n.TagValue], or returns
// [corei18n.InvalidTag].
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
//     tag; this package's negotiation is where ranges are read.
//
// Dropping any of them silently would change which language answers without
// changing anything a reader can see: "de-DE-u-co-phonebk" would quietly
// become "de-DE", and the difference would surface as a sort order nobody can
// explain. The domain does not implement collation, so the honest answer is to
// refuse the tag that asked for one.
//
// The parser lives here and not beside the value (ADR 0160: a wire format is a
// mechanism): it splits the text and places each subtag, and
// corei18n.NewTag — the one constructor of the value — checks each subtag's
// shape and canonicalises its case.
func ParseTag(text string) (tag corei18n.TagValue, err error) {
	//: the empty string is the zero tag written out; it names no language.
	if text == "" {
		//: refuse rather than return the zero tag, which would let an unset
		//: configuration field parse successfully.
		return corei18n.TagValue{}, errs.Wrap(corei18n.InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "empty"))
	}
	//: the canonical form of the subset never exceeds "fil-Hant-419".
	if len(text) > maxTagBytes {
		//: too long for the subset — an extension or a variant, refused whole.
		return corei18n.TagValue{}, errs.Wrap(corei18n.InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "outside the language[-Script][-REGION] subset"))
	}
	//: split on the one separator BCP 47 defines; "_" is refused by falling
	//: through to the subtag checks, which reject the underscore character.
	parts := strings.Split(text, "-")
	//: more than language, script and region is outside the subset.
	if len(parts) > maxSubtags {
		//: name what was found, never guess which subtag was meant.
		return corei18n.TagValue{}, errs.Wrap(corei18n.InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "more than three subtags"))
	}
	//: the subtags after the language, each in the one position it may hold.
	script, region, placed := placeSubtags(parts[1:])
	//: a variant, a singleton, a repeated or out-of-order subtag has none.
	if !placed {
		//: name what was found; never guess what was meant.
		return corei18n.TagValue{}, errs.Wrap(corei18n.InvalidTag, errs.WrapParams{}, errs.String("tag", text), errs.String("detail", "subtag is not a script or region in the subset"))
	}
	//: the value checks each subtag's characters and canonicalises its case.
	tag, err = corei18n.NewTag(parts[0], script, region)
	//: a subtag of the wrong shape for its position.
	if err != nil {
		//: InvalidTag, already carrying the detail; the written tag joins it.
		return corei18n.TagValue{}, errs.Wrap(err, errs.WrapParams{}, errs.String("tag", text))
	}
	//: a canonical tag.
	return tag, nil
}

// placeSubtags assigns the subtags after the language to the script and the
// region positions, in the only order BCP 47 permits: a four-character subtag
// is the script, and only before a region; a two- or three-character one is
// the region, and only once. Anything else — a variant, an extension
// singleton, a private-use marker, a repeated or an out-of-order subtag — has
// no position in the subset, and placed is false.
func placeSubtags(rest []string) (script, region string, placed bool) {
	//: nothing after the language is a complete tag on its own.
	for _, sub := range rest {
		//: one position per subtag, decided by its length and what came first.
		switch {
		//: the script comes first, and once.
		case len(sub) == scriptSubtagLen && script == "" && region == "":
			//: a script candidate.
			script = sub
		//: the region comes last, and once.
		case (len(sub) == alphaRegionLen || len(sub) == digitRegionLen) && region == "":
			//: a region candidate.
			region = sub
		//: no position left for it.
		default:
			//: refused by its shape and its place.
			return "", "", false
		}
	}
	//: every subtag has its position.
	return script, region, true
}
