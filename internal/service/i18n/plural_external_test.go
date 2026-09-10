// Package i18n_test — the CLDR rule table, checked at its boundaries.
package i18n_test

import (
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// supportedCount is the number of entries in the hand-written rule table. It
// is asserted rather than derived so ADDING a language is a deliberate change
// to this file — which is where the ADR says the CLDR citation and the
// boundary values must land in the same commit.
const supportedCount int = 13

// pluralCase is one CLDR expectation: a quantity, how it is displayed, and the
// category the language's rules must return for it.
type pluralCase struct {
	// value is the quantity.
	value int64
	// digits is the DISPLAY precision. A negative value means "use Int",
	// which is not the same as zero digits on a fractional value.
	digits int
	// fractional is the value when digits is non-negative and the quantity is
	// not a whole number.
	fractional float64
	// want is the CLDR category.
	want corei18n.Form
}

// count builds the [corei18n.CountValue] a case describes.
func (c pluralCase) count(t *testing.T) corei18n.CountValue {
	t.Helper()

	if c.digits < 0 {
		return corei18n.Int(c.value)
	}

	value := c.fractional
	if value == 0 {
		value = float64(c.value)
	}

	built, err := corei18n.Decimal(value, c.digits)
	if err != nil {
		t.Fatalf("Decimal(%v, %d) = %v", value, c.digits, err)
	}
	return built
}

// whole is a case on an exact integer with no fraction digits.
func whole(value int64, want corei18n.Form) pluralCase {
	return pluralCase{value: value, digits: -1, want: want}
}

// shown is a case on a value displayed with a given number of fraction digits.
func shown(value float64, digits int, want corei18n.Form) pluralCase {
	return pluralCase{fractional: value, digits: digits, want: want}
}

func TestCLDRRulesAtTheirBoundaries(t *testing.T) {
	t.Parallel()

	// Every expectation below is the CLDR cardinal chart. The cases are the
	// boundaries the clauses actually turn on — not a sample — because a rule
	// table is exactly the kind of code that passes a spot check and is wrong
	// at 21, 102 and 112.
	cases := map[string][]pluralCase{
		// one: i = 1 and v = 0; other.
		"en": {
			whole(0, corei18n.FormOther), whole(1, corei18n.FormOne), whole(2, corei18n.FormOther),
			shown(1, 1, corei18n.FormOther), // "1.0 files" is other in English
			shown(1.5, 1, corei18n.FormOther),
		},
		"de": {whole(0, corei18n.FormOther), whole(1, corei18n.FormOne), shown(1, 1, corei18n.FormOther)},
		"nl": {whole(0, corei18n.FormOther), whole(1, corei18n.FormOne), shown(1, 1, corei18n.FormOther)},

		// one: i = 0,1; many: i != 0 and i % 1000000 = 0 and v = 0; other.
		"fr": {
			whole(0, corei18n.FormOne), whole(1, corei18n.FormOne), whole(2, corei18n.FormOther),
			shown(0.5, 1, corei18n.FormOne), // i = 0, and the clause has no v constraint
			shown(1.5, 1, corei18n.FormOne),
			shown(2.5, 1, corei18n.FormOther),
			whole(1_000_000, corei18n.FormMany), whole(2_000_000, corei18n.FormMany),
			whole(1_000_001, corei18n.FormOther),
			shown(1_000_000, 1, corei18n.FormOther), // v != 0 defeats the many clause
		},

		// one: n = 1; many: …; other. Its singular is on n, not on i and v.
		"es": {
			whole(0, corei18n.FormOther), whole(1, corei18n.FormOne), whole(2, corei18n.FormOther),
			shown(1, 1, corei18n.FormOne), // "1,0" IS one in Spanish and other in English
			shown(1.5, 1, corei18n.FormOther),
			whole(1_000_000, corei18n.FormMany),
		},

		// one: i = 1 and v = 0; many: …; other.
		"it": {
			whole(0, corei18n.FormOther), whole(1, corei18n.FormOne),
			shown(1, 1, corei18n.FormOther),
			whole(1_000_000, corei18n.FormMany),
		},

		// one: i = 0..1; many: …; other.
		"pt": {
			whole(0, corei18n.FormOne), whole(1, corei18n.FormOne), whole(2, corei18n.FormOther),
			shown(1, 1, corei18n.FormOne),
			whole(1_000_000, corei18n.FormMany),
		},

		// one: i = 1 and v = 0; many: …; other. The zero is where it differs
		// from pt, which is why Rules resolves the exact tag first.
		"pt-PT": {
			whole(0, corei18n.FormOther), whole(1, corei18n.FormOne), whole(2, corei18n.FormOther),
			shown(1, 1, corei18n.FormOther),
			whole(1_000_000, corei18n.FormMany),
		},

		// one:  i = 1 and v = 0
		// few:  v = 0 and i % 10 = 2..4 and i % 100 != 12..14
		// many: v = 0 and i != 1 and i % 10 = 0..1
		//       or v = 0 and i % 10 = 5..9
		//       or v = 0 and i % 100 = 12..14
		"pl": {
			whole(0, corei18n.FormMany), whole(1, corei18n.FormOne),
			whole(2, corei18n.FormFew), whole(3, corei18n.FormFew), whole(4, corei18n.FormFew),
			whole(5, corei18n.FormMany), whole(9, corei18n.FormMany), whole(10, corei18n.FormMany),
			whole(11, corei18n.FormMany), whole(12, corei18n.FormMany), whole(14, corei18n.FormMany),
			whole(21, corei18n.FormMany), // one in Russian, many in Polish
			whole(22, corei18n.FormFew), whole(25, corei18n.FormMany),
			whole(101, corei18n.FormMany), whole(102, corei18n.FormFew), whole(112, corei18n.FormMany),
			shown(1.5, 1, corei18n.FormOther), // every pl clause requires v = 0
			shown(2, 1, corei18n.FormOther),
		},

		// one:  v = 0 and i % 10 = 1 and i % 100 != 11
		// few:  v = 0 and i % 10 = 2..4 and i % 100 != 12..14
		// many: v = 0 and i % 10 = 0
		//       or v = 0 and i % 10 = 5..9
		//       or v = 0 and i % 100 = 11..14
		"ru": {
			whole(0, corei18n.FormMany), whole(1, corei18n.FormOne),
			whole(2, corei18n.FormFew), whole(4, corei18n.FormFew), whole(5, corei18n.FormMany),
			whole(11, corei18n.FormMany), whole(12, corei18n.FormMany), whole(14, corei18n.FormMany),
			whole(21, corei18n.FormOne), // many in Polish, one in Russian
			whole(22, corei18n.FormFew), whole(25, corei18n.FormMany),
			whole(100, corei18n.FormMany), whole(101, corei18n.FormOne),
			whole(102, corei18n.FormFew), whole(111, corei18n.FormMany), whole(112, corei18n.FormMany),
			shown(1.5, 1, corei18n.FormOther),
		},

		// zero: n = 0; one: n = 1; two: n = 2;
		// few: n % 100 = 3..10; many: n % 100 = 11..99; other.
		"ar": {
			whole(0, corei18n.FormZero), whole(1, corei18n.FormOne), whole(2, corei18n.FormTwo),
			whole(3, corei18n.FormFew), whole(10, corei18n.FormFew),
			whole(11, corei18n.FormMany), whole(99, corei18n.FormMany),
			whole(100, corei18n.FormOther), whole(101, corei18n.FormOther), whole(102, corei18n.FormOther),
			whole(103, corei18n.FormFew), whole(111, corei18n.FormMany), whole(200, corei18n.FormOther),
			shown(3.5, 1, corei18n.FormOther), // ar clauses are on n, so a fraction matches none
		},

		// No plural distinction at all.
		"ja": {whole(0, corei18n.FormOther), whole(1, corei18n.FormOther), whole(5, corei18n.FormOther)},
		"zh": {whole(0, corei18n.FormOther), whole(1, corei18n.FormOther), whole(5, corei18n.FormOther)},
	}

	for language, expectations := range cases {
		t.Run(language, func(t *testing.T) {
			t.Parallel()

			tag, err := corei18n.ParseTag(language)
			if err != nil {
				t.Fatalf("ParseTag(%q) = %v", language, err)
			}
			rules, ok := svci18n.Rules(tag)
			if !ok {
				t.Fatalf("Rules(%q) reported no rule for a language in the supported set", language)
			}

			for _, expectation := range expectations {
				got := rules.Select(expectation.count(t))
				if got != expectation.want {
					t.Errorf("%s: value=%v fractional=%v digits=%d → %v, want %v",
						language, expectation.value, expectation.fractional, expectation.digits, got, expectation.want)
				}
			}
		})
	}
}

func TestEveryRuleOnlyEverReturnsACategoryItDeclares(t *testing.T) {
	t.Parallel()

	// The load-time completeness check reads Forms(); the render path reads
	// Select(). If the two ever disagree, a message that passed the check at
	// startup would be asked at render time for a category it does not carry.
	// This walks a wide range of quantities through every language.
	for _, tag := range svci18n.SupportedTags() {
		rules, ok := svci18n.Rules(tag)
		if !ok {
			t.Fatalf("Rules(%q) = false for a tag SupportedTags returned", tag)
		}

		declared := map[corei18n.Form]bool{}
		for _, form := range rules.Forms() {
			declared[form] = true
		}

		for n := int64(0); n <= 250; n++ {
			if got := rules.Select(corei18n.Int(n)); !declared[got] {
				t.Fatalf("%s: Select(%d) = %v, which Forms() does not declare", tag, n, got)
			}
			for digits := 1; digits <= 2; digits++ {
				value, err := corei18n.Decimal(float64(n)+0.5, digits)
				if err != nil {
					t.Fatalf("Decimal = %v", err)
				}
				if got := rules.Select(value); !declared[got] {
					t.Fatalf("%s: Select(%d.5 at %d digits) = %v, which Forms() does not declare", tag, n, digits, got)
				}
			}
		}

		// A million is the one boundary outside the 0..250 sweep that a
		// Romance rule turns on.
		if got := rules.Select(corei18n.Int(1_000_000)); !declared[got] {
			t.Fatalf("%s: Select(1000000) = %v, which Forms() does not declare", tag, got)
		}
	}
}

func TestEveryDeclaredCategoryIsActuallyReachable(t *testing.T) {
	t.Parallel()

	// The other half: a language that DECLARED `few` and never returns it
	// would force every translator to write a form the renderer can never
	// show, and the completeness check would be demanding work for nothing.
	for _, tag := range svci18n.SupportedTags() {
		rules, ok := svci18n.Rules(tag)
		if !ok {
			t.Fatalf("Rules(%q) = false", tag)
		}

		seen := map[corei18n.Form]bool{}
		for n := int64(0); n <= 2_000_000; n++ {
			seen[rules.Select(corei18n.Int(n))] = true
			if n == 250 {
				n = 999_998 // jump to the compact-decimal boundary
			}
		}
		// Decimals are swept too, and they are not decoration: in Polish and
		// Russian EVERY clause carries "v = 0", so `other` is unreachable for
		// an integer quantity and exists solely for "1,5 pliku". A sweep of
		// integers alone reports those two languages as declaring a category
		// nothing produces — which is how this loop found the fact.
		for n := 0; n <= 250; n++ {
			value, decErr := corei18n.Decimal(float64(n)+0.5, 1)
			if decErr != nil {
				t.Fatalf("Decimal = %v", decErr)
			}
			seen[rules.Select(value)] = true
		}

		for _, form := range rules.Forms() {
			if !seen[form] {
				t.Errorf("%s declares %v and no quantity in the sweep produces it", tag, form)
			}
		}
	}
}

func TestRulesResolveTheExactTagBeforeTheLanguage(t *testing.T) {
	t.Parallel()

	// A region narrowing CLDR does not distinguish inherits its language …
	for _, spelling := range []string{"fr-CA", "de-AT", "zh-Hant", "en-GB", "pt-BR"} {
		tag, err := corei18n.ParseTag(spelling)
		if err != nil {
			t.Fatalf("ParseTag(%q) = %v", spelling, err)
		}
		if _, ok := svci18n.Rules(tag); !ok {
			t.Errorf("Rules(%q) = false, want the base language's rules", spelling)
		}
	}

	// … and one it DOES distinguish gets its own. "0 ficheiros" is other in
	// Portugal and "0 arquivos" is one in Brazil.
	european, err := corei18n.ParseTag("pt-PT")
	if err != nil {
		t.Fatalf("ParseTag = %v", err)
	}
	brazilian, err := corei18n.ParseTag("pt-BR")
	if err != nil {
		t.Fatalf("ParseTag = %v", err)
	}

	europeanRules, _ := svci18n.Rules(european)
	brazilianRules, _ := svci18n.Rules(brazilian)

	if europeanRules.Select(corei18n.Int(0)) != corei18n.FormOther {
		t.Error("pt-PT: 0 must be other")
	}
	if brazilianRules.Select(corei18n.Int(0)) != corei18n.FormOne {
		t.Error("pt-BR: 0 must be one, inherited from pt")
	}
}

func TestAnUnsupportedLanguageIsRefusedRatherThanApproximated(t *testing.T) {
	t.Parallel()

	// Every one of these has plural rules English does not have, and every
	// one of them would render a wrong sentence under an English fallback.
	for _, spelling := range []string{"cs", "lt", "lv", "ga", "cy", "sl", "ro", "hr", "sk", "uk"} {
		tag, err := corei18n.ParseTag(spelling)
		if err != nil {
			t.Fatalf("ParseTag(%q) = %v", spelling, err)
		}
		if _, ok := svci18n.Rules(tag); ok {
			t.Errorf("Rules(%q) = true; the table must not have grown a language without a boundary test", spelling)
		}
	}

	var zero corei18n.TagValue
	if _, ok := svci18n.Rules(zero); ok {
		t.Error("Rules(zero Tag) = true; an unset tag names no language")
	}
}

func TestSupportedTagsIsTheDocumentedSet(t *testing.T) {
	t.Parallel()

	tags := svci18n.SupportedTags()
	if len(tags) != supportedCount {
		t.Fatalf("SupportedTags() has %d entries, want %d — update the ADR, the CLAUDE.md files and this constant in the same change", len(tags), supportedCount)
	}

	want := []string{"ar", "de", "en", "es", "fr", "it", "ja", "nl", "pl", "pt", "pt-PT", "ru", "zh"}
	for i, tag := range tags {
		if tag.String() != want[i] {
			t.Errorf("SupportedTags()[%d] = %q, want %q (the list must stay sorted)", i, tag, want[i])
		}
	}

	// Every one of the six CLDR categories must be exercised by the set, or
	// the table is not proving that the domain handles more than English.
	covered := map[corei18n.Form]bool{}
	for _, tag := range tags {
		rules, ok := svci18n.Rules(tag)
		if !ok {
			t.Fatalf("Rules(%q) = false", tag)
		}
		for _, form := range rules.Forms() {
			covered[form] = true
		}
	}
	for _, form := range []corei18n.Form{
		corei18n.FormZero, corei18n.FormOne, corei18n.FormTwo,
		corei18n.FormFew, corei18n.FormMany, corei18n.FormOther,
	} {
		if !covered[form] {
			t.Errorf("no supported language produces %v", form)
		}
	}
}

func TestTheZeroPluralAnswersOtherRatherThanPanicking(t *testing.T) {
	t.Parallel()

	// Plural is a value type a caller can declare. Its zero has no rule, and
	// FormOther is the one category every language and every message carries.
	var zero svci18n.PluralValue

	if zero.Valid() {
		t.Error("Valid() = true on the zero Plural")
	}
	if got := zero.Select(corei18n.Int(5)); got != corei18n.FormOther {
		t.Errorf("Select on the zero Plural = %v, want FormOther", got)
	}
}
