// Package i18n — the hand-written CLDR plural rules, and the languages this
// SDK will speak.
package i18n

import (
	"slices"
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The numbers CLDR's clauses are written against. They are named rather than
// inlined because a rule table is exactly the kind of code where a stray digit
// is invisible: "i % 100 != 12..14" and "i % 100 != 11..14" differ by one
// character and by one language.
const (
	// million is the modulus CLDR's Romance `many` clause is written against.
	// It is the compact-decimal category — "1 million", "2 milhões" — and it
	// is the only place in this table where a clause about e (the compact
	// exponent) is implemented at e = 0. See [PluralValue] and ADR 0063 §D1.
	million uint64 = 1_000_000
	// tenModulus and hundredModulus are the two operands every Slavic and
	// Semitic clause is written over.
	tenModulus     uint64 = 10
	hundredModulus uint64 = 100
	// singularValue and dualValue are the exact quantities Arabic names.
	singularValue uint64 = 1
	dualValue     uint64 = 2
	// slavicFewLow..slavicFewHigh is "i % 10 = 2..4", the Polish and Russian
	// `few` clause.
	slavicFewLow  uint64 = 2
	slavicFewHigh uint64 = 4
	// slavicManyLow..slavicManyHigh is "i % 10 = 5..9", shared by both.
	slavicManyLow  uint64 = 5
	slavicManyHigh uint64 = 9
	// polishTeenLow..teenHigh is "i % 100 = 12..14" — the exception that
	// pulls 12, 13 and 14 out of `few` and into `many`.
	polishTeenLow uint64 = 12
	teenHigh      uint64 = 14
	// russianTeenLow is 11: Russian's `many` clause starts one lower than
	// Polish's, which is the whole difference between 11 in the two
	// languages.
	russianTeenLow uint64 = 11
	// arabicFewLow..arabicFewHigh is "n % 100 = 3..10".
	arabicFewLow  uint64 = 3
	arabicFewHigh uint64 = 10
	// arabicManyLow is the start of "n % 100 = 11..99"; its upper bound is
	// the modulus itself, so it needs no constant.
	arabicManyLow uint64 = 11
)

// The category sets, shared by the languages that agree on them. They are
// package-level so the table holds one slice header per family rather than one
// backing array per language.
var (
	// formsOtherOnly is the set of a language with no plural distinction at
	// all — Japanese, Chinese.
	formsOtherOnly = []corei18n.Form{corei18n.FormOther}
	// formsOneOther is the Germanic set.
	formsOneOther = []corei18n.Form{corei18n.FormOther, corei18n.FormOne}
	// formsOneManyOther is the Romance set, whose `many` is the
	// compact-decimal category.
	formsOneManyOther = []corei18n.Form{corei18n.FormOther, corei18n.FormOne, corei18n.FormMany}
	// formsSlavic is the Polish and Russian set.
	formsSlavic = []corei18n.Form{corei18n.FormOther, corei18n.FormOne, corei18n.FormFew, corei18n.FormMany}
	// formsArabic is the only set in this table that uses all six.
	formsArabic = []corei18n.Form{
		corei18n.FormOther, corei18n.FormZero, corei18n.FormOne,
		corei18n.FormTwo, corei18n.FormFew, corei18n.FormMany,
	}

	// rulesByTag is the supported set, keyed by CANONICAL TAG.
	//
	// Resolution is exact-tag first, then the bare language subtag, so "fr-CA"
	// finds "fr" and "pt-PT" finds its own entry rather than "pt"'s — CLDR
	// gives European Portuguese a different singular from Brazilian, and
	// collapsing them would be the same silent substitution the whole table
	// exists to refuse.
	//
	// The keys are the INPUT of a lookup — a tag a request or a filename
	// carried — not an enumeration the SDK owns, which is why they are
	// strings.
	rulesByTag = map[string]PluralValue{
		"ar":    {forms: formsArabic, rule: pluralArabic},
		"de":    {forms: formsOneOther, rule: pluralGermanic},
		"en":    {forms: formsOneOther, rule: pluralGermanic},
		"es":    {forms: formsOneManyOther, rule: pluralSpanish},
		"fr":    {forms: formsOneManyOther, rule: pluralFrench},
		"it":    {forms: formsOneManyOther, rule: pluralItalian},
		"ja":    {forms: formsOtherOnly},
		"nl":    {forms: formsOneOther, rule: pluralGermanic},
		"pl":    {forms: formsSlavic, rule: pluralPolish},
		"pt":    {forms: formsOneManyOther, rule: pluralPortuguese},
		"pt-PT": {forms: formsOneManyOther, rule: pluralEuropeanPortuguese},
		"ru":    {forms: formsSlavic, rule: pluralRussian},
		"zh":    {forms: formsOtherOnly},
	}
)

// Rules returns the CLDR plural specification for tag, and reports whether the
// SDK has one.
//
// It resolves the exact canonical tag first and the bare language subtag
// second, so a region or script narrowing that CLDR does not distinguish
// inherits its language's rules — "fr-CA", "de-AT" and "zh-Hant" all do — and
// one that CLDR DOES distinguish gets its own, which today is "pt-PT".
func Rules(tag corei18n.TagValue) (plural PluralValue, ok bool) {
	//: an unset tag names no language and cannot resolve to one.
	if tag.IsZero() {
		//: not supported, and deliberately not defaulted.
		return PluralValue{}, false
	}
	//: exact tag wins, so a CLDR region exception is never shadowed.
	if exact, found := rulesByTag[tag.String()]; found {
		//: the region-specific rule.
		return exact, true
	}
	//: otherwise the bare language subtag.
	base, found := rulesByTag[tag.Language()]
	//: present or refused; never substituted.
	return base, found
}

// SupportedTags returns every tag [Rules] resolves exactly, sorted.
//
// It is what [UnsupportedLanguage] names beside the refused tag, so the
// message a maintainer reads is "pl is not supported; ar de en es fr it ja nl
// pt pt-PT ru zh are" rather than a bare refusal.
func SupportedTags() []corei18n.TagValue {
	//: one entry per table key.
	tags := make([]corei18n.TagValue, 0, len(rulesByTag))
	//: the keys are canonical, so re-parsing them cannot fail — and if it
	//: ever could, the table itself would be wrong and the entry is dropped
	//: rather than reported, because this function has no error return and a
	//: table typo is caught by TestEverySupportedTagParses.
	for key := range rulesByTag {
		//: canonicalise the key into a Tag.
		tag, err := corei18n.ParseTag(key)
		//: a malformed key is a table defect the test catches.
		if err != nil {
			//: skip it rather than panic on a package variable.
			continue
		}
		//: keep it.
		tags = append(tags, tag)
	}
	//: a stable order, so an error message is byte-identical across runs.
	slices.SortFunc(tags, compareTags)
	//: the supported set.
	return tags
}

// supportedList renders [SupportedTags] as a space-separated string for an
// error field.
func supportedList() string {
	//: the sorted set.
	tags := SupportedTags()
	//: one string per tag, joined once.
	spellings := make([]string, 0, len(tags))
	//: the canonical spellings.
	for _, tag := range tags {
		//: as a reader would see them.
		spellings = append(spellings, tag.String())
	}
	//: space separated.
	return strings.Join(spellings, chainSeparator)
}

// requireRules resolves tag or returns [UnsupportedLanguage] naming it and the
// supported set.
func requireRules(tag corei18n.TagValue) (plural PluralValue, err error) {
	//: the table lookup.
	found, ok := Rules(tag)
	//: an unsupported language is refused, never approximated.
	if !ok {
		//: the tag and the whole supported set travel as fields.
		return PluralValue{}, errs.Wrap(UnsupportedLanguage, errs.WrapParams{},
			errs.String("tag", tag.String()), errs.String("supported", supportedList()))
	}
	//: the language's rules.
	return found, nil
}

// pluralGermanic is the rule shared by English, German and Dutch.
//
// CLDR (en, de, nl): one: i = 1 and v = 0; other.
//
// The "v = 0" half is why [corei18n.CountValue] carries a display precision: "1.0
// files" is `other` in English, and a rule that read only the value could not
// tell it from "1 file".
func pluralGermanic(count corei18n.CountValue) corei18n.Form {
	//: i = 1 and v = 0.
	if count.IntegerPart() == 1 && count.VisibleFractionDigits() == 0 {
		//: one.
		return corei18n.FormOne
	}
	//: other.
	return corei18n.FormOther
}

// pluralFrench is the rule of French.
//
// CLDR (fr): one: i = 0,1; many: e = 0 and i != 0 and i % 1000000 = 0 and
// v = 0 or e != 0..5; other.
//
// The `many` clause is implemented at e = 0: this domain ships no
// compact-decimal formatter, so the exponent operand is 0 for every quantity
// that can reach here and the "or e != 0..5" half can never fire.
func pluralFrench(count corei18n.CountValue) corei18n.Form {
	//: one: i = 0,1 — note there is no v constraint, so 0.5 and 1.5 are one.
	if count.IntegerPart() <= 1 {
		//: one.
		return corei18n.FormOne
	}
	//: many at e = 0: i != 0 and i % 1000000 = 0 and v = 0.
	if isRomanceMany(count) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// pluralSpanish is the rule of Spanish.
//
// CLDR (es): one: n = 1; many: e = 0 and i != 0 and i % 1000000 = 0 and v = 0
// or e != 0..5; other.
//
// Its singular is written on n rather than on i and v, so "1,0" IS `one` in
// Spanish where "1.0" is `other` in English. The two clauses look
// interchangeable and are not.
//
// The `many` clause is implemented at e = 0 — see [pluralFrench].
func pluralSpanish(count corei18n.CountValue) corei18n.Form {
	//: n = 1 — the value is one, whatever precision it is displayed at.
	if count.IsIntegerValued() && count.IntegerPart() == 1 {
		//: one.
		return corei18n.FormOne
	}
	//: many at e = 0.
	if isRomanceMany(count) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// pluralItalian is the rule of Italian.
//
// CLDR (it): one: i = 1 and v = 0; many: e = 0 and i != 0 and
// i % 1000000 = 0 and v = 0 or e != 0..5; other.
//
// The `many` clause is implemented at e = 0 — see [pluralFrench].
func pluralItalian(count corei18n.CountValue) corei18n.Form {
	//: i = 1 and v = 0, the Germanic singular.
	if count.IntegerPart() == 1 && count.VisibleFractionDigits() == 0 {
		//: one.
		return corei18n.FormOne
	}
	//: many at e = 0.
	if isRomanceMany(count) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// pluralPortuguese is the rule of Portuguese as CLDR spells `pt`.
//
// CLDR (pt): one: i = 0..1; many: e = 0 and i != 0 and i % 1000000 = 0 and
// v = 0 or e != 0..5; other.
//
// The `many` clause is implemented at e = 0 — see [pluralFrench].
func pluralPortuguese(count corei18n.CountValue) corei18n.Form {
	//: one: i = 0..1.
	if count.IntegerPart() <= 1 {
		//: one.
		return corei18n.FormOne
	}
	//: many at e = 0.
	if isRomanceMany(count) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// pluralEuropeanPortuguese is the rule of Portuguese as CLDR spells `pt_PT`.
//
// CLDR (pt_PT): one: i = 1 and v = 0; many: e = 0 and i != 0 and
// i % 1000000 = 0 and v = 0 or e != 0..5; other.
//
// It differs from [pluralPortuguese] in the singular alone — "0 ficheiros" is
// `other` in Portugal and `one` in Brazil — and it is the reason [Rules]
// resolves the exact tag before the language subtag. Folding the two would
// change the article in front of every zero on a European Portuguese page.
//
// The `many` clause is implemented at e = 0 — see [pluralFrench].
func pluralEuropeanPortuguese(count corei18n.CountValue) corei18n.Form {
	//: one: i = 1 and v = 0.
	if count.IntegerPart() == 1 && count.VisibleFractionDigits() == 0 {
		//: one.
		return corei18n.FormOne
	}
	//: many at e = 0.
	if isRomanceMany(count) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// isRomanceMany implements the compact-decimal `many` clause shared by fr, es,
// it and pt, restricted to e = 0: i != 0 and i % 1000000 = 0 and v = 0.
func isRomanceMany(count corei18n.CountValue) bool {
	//: a whole non-zero multiple of a million, displayed without decimals.
	return count.IntegerPart() != 0 &&
		count.IntegerPart()%million == 0 &&
		count.VisibleFractionDigits() == 0
}

// pluralPolish is the rule of Polish.
//
// CLDR (pl):
//
//	one:  i = 1 and v = 0
//	few:  v = 0 and i % 10 = 2..4 and i % 100 != 12..14
//	many: v = 0 and i != 1 and i % 10 = 0..1
//	      or v = 0 and i % 10 = 5..9
//	      or v = 0 and i % 100 = 12..14
//	other
//
// Every clause carries "v = 0", so any quantity with visible decimals is
// `other` — 1,5 pliku. This is the shape an English-rules fallback gets wrong
// on nearly every number: 2, 3 and 4 are `few`, 5 through 21 are `many`, and
// English has neither category.
func pluralPolish(count corei18n.CountValue) corei18n.Form {
	//: every Polish clause requires v = 0.
	if count.VisibleFractionDigits() != 0 {
		//: other.
		return corei18n.FormOther
	}
	//: i = 1.
	if count.IntegerPart() == 1 {
		//: one.
		return corei18n.FormOne
	}
	//: the ten- and hundred-modulus operands both clauses read.
	mod10, mod100 := count.IntegerPart()%tenModulus, count.IntegerPart()%hundredModulus
	//: few: i % 10 = 2..4 and i % 100 != 12..14.
	if isSlavicFew(mod10, mod100, polishTeenLow) {
		//: few.
		return corei18n.FormFew
	}
	//: many: i != 1 and i % 10 = 0..1, or i % 10 = 5..9, or i % 100 = 12..14.
	if mod10 < slavicFewLow || isSlavicManyTail(mod10, mod100, polishTeenLow) {
		//: many — i = 1 was already answered above, so i != 1 holds here.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// isSlavicFew implements "i % 10 = 2..4 and i % 100 != teenLow..14", the `few`
// clause Polish and Russian share except for where their teen exception
// starts.
func isSlavicFew(mod10, mod100, teenLow uint64) bool {
	//: the teen exception pulls 12..14 (and 11..14 in Russian) out of `few`.
	inTeens := mod100 >= teenLow && mod100 <= teenHigh
	//: i % 10 = 2..4, outside the teens.
	return mod10 >= slavicFewLow && mod10 <= slavicFewHigh && !inTeens
}

// isSlavicManyTail implements the two `many` sub-clauses Polish and Russian
// share: "i % 10 = 5..9" or "i % 100 = teenLow..14".
func isSlavicManyTail(mod10, mod100, teenLow uint64) bool {
	//: the five-to-nine tail.
	if mod10 >= slavicManyLow && mod10 <= slavicManyHigh {
		//: many.
		return true
	}
	//: the teen exception.
	return mod100 >= teenLow && mod100 <= teenHigh
}

// pluralRussian is the rule of Russian.
//
// CLDR (ru):
//
//	one:  v = 0 and i % 10 = 1 and i % 100 != 11
//	few:  v = 0 and i % 10 = 2..4 and i % 100 != 12..14
//	many: v = 0 and i % 10 = 0
//	      or v = 0 and i % 10 = 5..9
//	      or v = 0 and i % 100 = 11..14
//	other
//
// It differs from Polish where it looks identical: 21 is `one` in Russian and
// `many` in Polish, and 0 is `many` in both but by different clauses.
func pluralRussian(count corei18n.CountValue) corei18n.Form {
	//: every Russian clause requires v = 0.
	if count.VisibleFractionDigits() != 0 {
		//: other.
		return corei18n.FormOther
	}
	//: the two modulus operands.
	mod10, mod100 := count.IntegerPart()%tenModulus, count.IntegerPart()%hundredModulus
	//: one: i % 10 = 1 and i % 100 != 11 — so 21 and 101 are singular.
	if mod10 == singularValue && mod100 != russianTeenLow {
		//: one.
		return corei18n.FormOne
	}
	//: few: i % 10 = 2..4 and i % 100 != 12..14 — the teen exception starts at
	//: 12 for `few` in Russian too, which is why it is not russianTeenLow.
	if isSlavicFew(mod10, mod100, polishTeenLow) {
		//: few.
		return corei18n.FormFew
	}
	//: many: i % 10 = 0, or i % 10 = 5..9, or i % 100 = 11..14.
	if mod10 == 0 || isSlavicManyTail(mod10, mod100, russianTeenLow) {
		//: many.
		return corei18n.FormMany
	}
	//: other.
	return corei18n.FormOther
}

// pluralArabic is the rule of Arabic, and the only entry in this table that
// uses all six categories.
//
// CLDR (ar):
//
//	zero: n = 0
//	one:  n = 1
//	two:  n = 2
//	few:  n % 100 = 3..10
//	many: n % 100 = 11..99
//	other
//
// Its clauses are written on n rather than on i and v, so a quantity with a
// non-zero fraction matches none of them and is `other` — 3,5 is not `few`.
func pluralArabic(count corei18n.CountValue) corei18n.Form {
	//: every Arabic clause is written on n, which equals i only when the
	//: visible fraction digits carry no value.
	if !count.IsIntegerValued() {
		//: other.
		return corei18n.FormOther
	}
	//: the value.
	value := count.IntegerPart()
	//: zero, one and two are exact.
	switch value {
	//: n = 0 — Arabic is one of the few languages with a distinct nought.
	case 0:
		//: zero.
		return corei18n.FormZero
	//: n = 1.
	case singularValue:
		//: one.
		return corei18n.FormOne
	//: n = 2 — the dual.
	case dualValue:
		//: two.
		return corei18n.FormTwo
	}
	//: the hundred-modulus the remaining two clauses read.
	mod100 := value % hundredModulus
	//: few: n % 100 = 3..10.
	if mod100 >= arabicFewLow && mod100 <= arabicFewHigh {
		//: few.
		return corei18n.FormFew
	}
	//: many: n % 100 = 11..99, which is everything at or above 11 since the
	//: modulus caps it at 99.
	if mod100 >= arabicManyLow {
		//: many.
		return corei18n.FormMany
	}
	//: other — 100, 101 and 102 land here.
	return corei18n.FormOther
}
