// Package i18n — the six CLDR plural categories.
package i18n

import "github.com/kitsunium/sdk/internal/kernel/errs"

// Form is a CLDR plural category: one of the six names the Unicode plural
// rules assign a quantity to.
//
// [FormOther] is the ZERO value, and that is ADR 0031's "the zero value is the
// safe one" rather than an accident of ordering. CLDR guarantees every
// language defines `other`; it is the only category that is always present, so
// a [PluralRule] that forgets to set a result names the one form every message
// is required to carry — instead of naming `zero`, which most languages do not
// have and which would turn a forgotten return into a lookup miss.
type Form uint8

// The six categories, `other` first so it is the zero value.
const (
	// FormOther is the catch-all category. Every language defines it and
	// every message is required to carry it.
	FormOther Form = iota
	// FormZero is used by languages with a distinct form for nought — Arabic
	// and Latvian among them. Most languages do not have it.
	FormZero
	// FormOne is the singular of languages that have one.
	FormOne
	// FormTwo is the dual — Arabic, Slovenian, Welsh.
	FormTwo
	// FormFew is the small-plural of the Slavic and Semitic families.
	FormFew
	// FormMany is the large-plural, and in several Romance languages the
	// compact-decimal form ("1 million").
	FormMany
	// formCount bounds the enum. It is unexported: it is a size, not a
	// category, and exporting it would put a seventh value in the public
	// vocabulary of a specification that defines six.
	formCount
)

// formNames maps each category to the name CLDR spells it with — the same
// spelling a catalogue file uses.
var formNames = [formCount]string{
	FormOther: "other",
	FormZero:  "zero",
	FormOne:   "one",
	FormTwo:   "two",
	FormFew:   "few",
	FormMany:  "many",
}

// String returns the CLDR spelling of the category, or "invalid" for a value
// outside the six.
//
// "invalid" rather than a number, and never a silent "other": a corrupt Form
// reaching a log must not read as if it were a category, which is the same
// rule internal/core/authz applies to a corrupt Decision.
func (f Form) String() string {
	//: a value outside the enum is named as such.
	if !f.Valid() {
		//: never renders as a category.
		return "invalid"
	}
	//: the CLDR spelling.
	return formNames[f]
}

// Valid reports whether f is one of the six categories CLDR defines.
func (f Form) Valid() bool {
	//: the enum is dense from FormOther to FormMany.
	return f < formCount
}

// ParseForm returns the [Form] named by the CLDR spelling in name, or
// [InvalidForm].
//
// An unknown name is REFUSED rather than ignored. A catalogue entry that
// spells "otehr" and is dropped leaves the message with no `other` pattern at
// all — and `other` is the one form every message must carry, so the typo
// would surface as a missing translation in a language that was translated.
func ParseForm(name string) (form Form, err error) {
	//: linear over six entries beats a map here — no hashing, no allocation,
	//: and the table is in cache.
	for candidate, spelling := range formNames {
		//: exact match, no case folding: CLDR spells them lower case.
		if spelling == name {
			//: the category.
			return Form(candidate), nil //nolint:gosec // candidate indexes a 6-entry table.
		}
	}
	//: the name travels as a field; it is the developer's own catalogue text.
	return FormOther, errs.Wrap(InvalidForm, errs.WrapParams{}, errs.String("form", name))
}
