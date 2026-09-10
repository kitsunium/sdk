// Package i18n — one language's CLDR plural specification.
package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/i18n"

// PluralValue is the CLDR plural specification of ONE language: the categories its
// rules can produce, and the rule that picks between them.
//
// # Why this table is written by hand, and is short
//
// CLDR's plural data covers about 200 locales. Vendoring it means shipping
// tens of thousands of generated lines nobody on the team has read, to answer
// a question about languages the product does not have a single string in. So
// this table holds the languages the SDK will actually be asked for, each rule
// transcribed from the CLDR cardinal chart with its clause in the comment
// above it, each pinned by a table test that walks the boundary values.
//
// The consequence is stated plainly and is not a defect: a language absent
// from the table is REFUSED at catalogue construction, by name, with the
// supported set beside it. It does not silently borrow English's rules. See
// ADR 0063 §D1 and [UnsupportedLanguage].
//
// # The one operand that is not implemented
//
// Four of these rules — French, Spanish, Italian and Portuguese — have a
// `many` clause CLDR writes as "e = 0 and i != 0 and i % 1000000 = 0 and v = 0
// or e != 0..5". The operand e is the compact-decimal exponent, and this
// domain ships no compact-decimal formatter, so e is 0 for every quantity that
// can reach these rules. The clause is therefore implemented at e = 0: the
// first half exactly, the second half never firing. That is not an
// approximation of the rule, it is the rule restricted to the inputs the
// domain can produce — and it is written out at all four sites rather than
// once here, because a reader checking one rule against CLDR must not have to
// find this paragraph to know it is right.
type PluralValue struct {
	// forms is the set of categories this language's rules can produce,
	// ascending by Form value. It is what NewStore checks a plural message
	// against, so an incomplete translation is refused at load.
	forms []corei18n.Form
	// rule picks the category for a quantity. It is nil for a language with
	// no plural distinction at all — Japanese, Chinese — where a function
	// returning a constant would be that constant written the long way.
	rule corei18n.PluralRule
}

// Forms returns the CLDR categories this language can produce, ascending. The
// returned slice is shared and MUST NOT be modified: it is read on the load
// path by every catalogue in the process.
func (p PluralValue) Forms() []corei18n.Form {
	//: the stored set.
	return p.forms
}

// Select returns the CLDR category count falls in for this language.
//
// A language with no plural distinction carries no rule and every quantity is
// `other`; so does the zero PluralValue, which is ADR 0031's "the zero value
// is the safe one" — `other` is the one category every language defines and
// every message is required to carry.
func (p PluralValue) Select(count corei18n.CountValue) corei18n.Form {
	//: no rule means no distinction to make.
	if p.rule == nil {
		//: the safe answer, and the ADR 0031 reason FormOther is the zero.
		return corei18n.FormOther
	}
	//: the language's own rule.
	return p.rule(count)
}

// Valid reports whether p describes a language.
//
// It reads the category SET rather than the rule, because Japanese and Chinese
// legitimately carry no rule and are nonetheless perfectly valid entries. The
// zero PluralValue describes nothing, and it is the only value this reports
// false for.
func (p PluralValue) Valid() bool {
	//: the zero PluralValue is what Rules returns for an unsupported language.
	return len(p.forms) != 0
}
