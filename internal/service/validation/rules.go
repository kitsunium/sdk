// Package validation — the tag dialect: what a rule item may say, and what it
// is refused for saying.
package validation

import (
	"reflect"
	"strings"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// argSeparator splits a rule from its argument inside one tag item.
const argSeparator string = "="

// The accepted rule names. This list is the dialect; it is closed on purpose,
// and every entry is a rule the SDK undertakes to maintain forever.
const (
	tagRequired string = "required"
	tagMin      string = "min"
	tagMax      string = "max"
	tagMinLen   string = "minlen"
	tagMaxLen   string = "maxlen"
	tagMinCount string = "mincount"
	tagMaxCount string = "maxcount"
	tagOneOf    string = "oneof"
)

// refusedRules maps a construct this dialect refuses to the reason, phrased as
// what to do instead. A refusal BY NAME costs one line here and saves an
// investigation: "unknown rule" would leave the caller unsure whether they
// mistyped or asked for something that does not exist.
var refusedRules = map[string]string{
	//: a regexp contains commas far more often than not, and this tag is
	//: comma-separated. Accepting it would mean inventing an escape dialect —
	//: and a SILENTLY TRUNCATED pattern is precisely the class of failure that
	//: makes a validator worse than no validator.
	"pattern": "a regular expression cannot be written in a comma-separated tag; compose validation.Matches in code",
	"regex":   "a regular expression cannot be written in a comma-separated tag; compose validation.Matches in code",
	"regexp":  "a regular expression cannot be written in a comma-separated tag; compose validation.Matches in code",
	//: RFC 5322 addresses are not a regular language: quoted local parts,
	//: comments, domain literals and IDN all fall outside anything a pattern
	//: can decide. Every "email" rule in every library is a guess that rejects
	//: valid addresses and accepts unroutable ones. Deliverability is decided
	//: by sending mail to it, not by parsing it.
	"email": "the SDK does not claim to validate an email address; check that it is non-empty and send a confirmation, or compose validation.Matches with a pattern you own",
	//: a URL that parses is not a URL that is acceptable — scheme allow-lists,
	//: host resolution and SSRF are application decisions, and net/url already
	//: answers the parse question.
	"url": "the SDK does not claim to validate a URL; use net/url.Parse and decide the scheme and host policy in the application",
	//: a UUID is one of six versions with different shapes; pkg/v1/id already
	//: mints and parses the ones this SDK supports.
	"uuid": "use pkg/v1/id to mint and parse identifiers; a shape check on an opaque identifier belongs to whoever defines it",
	//: splitDive only consumes the first marker.
	diveRule: "only one dive is allowed per field; the rules after it apply to what the descent reaches",
}

// compileRule compiles one rule item against one field's static type. It runs
// at COMPILE time only: a rule the field's kind cannot answer is refused here,
// never discovered at the first request.
func compileRule(name string, typ reflect.Type, item string) (check fieldCheck, err error) {
	//: split "min=3" into rule and argument.
	rule, arg, hasArg := strings.Cut(item, argSeparator)
	//: a construct this dialect refuses on purpose is refused by name.
	if reason, refused := refusedRules[rule]; refused {
		//: the reason says what to do instead.
		return nil, rejectRule(name, rule, reason)
	}
	//: dispatch on the rule.
	switch rule {
	//: presence — the only rule that is kind-independent.
	case tagRequired:
		//: presence takes no argument.
		if hasArg {
			//: an argument here means the caller expected different semantics.
			return nil, rejectRule(name, rule, "required takes no argument")
		}
		//: kind-independent: every kind has a zero value.
		return requiredCheck(), nil
	//: an ordered bound; the argument is parsed at the field's own width.
	case tagMin, tagMax:
		//: an ordered bound on a number.
		return buildNumeric(name, typ, rule, arg, hasArg)
	//: a size measured in RUNES, which only a string has.
	case tagMinLen, tagMaxLen:
		//: a rune count on a string.
		return buildLength(name, typ, rule, arg, hasArg)
	//: a size measured in ELEMENTS, which only a slice or an array has.
	case tagMinCount, tagMaxCount:
		//: an element count on a slice or an array.
		return buildCount(name, typ, rule, arg, hasArg)
	//: membership in a closed set spelled in the tag itself.
	case tagOneOf:
		//: set membership.
		return buildOneOf(name, typ, arg, hasArg)
	//: everything else — the dialect is closed, so this is the end of it.
	default:
		//: an unknown rule is a typo or an expectation this dialect does not
		//: meet; either way it must not be ignored.
		return nil, rejectRule(name, rule, "unknown rule; the dialect is required, min, max, minlen, maxlen, mincount, maxcount, oneof, dive")
	}
}

// requiredCheck is the compiled presence rule. reflect.Value.IsZero answers it
// for every kind — a nil pointer, a nil slice, an empty string, a zero number.
//
// It therefore cannot distinguish "not supplied" from "supplied as zero", for
// the reason [Required] documents: a Go value type has no representation for
// the difference. Declare the field as a pointer when the difference matters.
func requiredCheck() fieldCheck {
	//: one closure, shared by every field carrying the rule.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: anything that differs from the zero value counts as supplied.
		if !fieldValue.IsZero() {
			//: accepted.
			return nil
		}
		//: absent.
		return one(path, ruleRequired, requiredMessage, CodeRequired)
	}
}
