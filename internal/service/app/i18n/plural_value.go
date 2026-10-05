package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"

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

// valid is PluralValue.Valid's body: decl_gen.go writes PluralValue.Valid, from the
// design, as one call of it.
func (p PluralValue) valid() bool {
	//: the zero PluralValue is what Rules returns for an unsupported language.
	return len(p.forms) != 0
}
