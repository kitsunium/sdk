// Package i18n_test — the catalogue, and everything it refuses at load.
package i18n_test

import (
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// mustTag parses a canonical tag or fails the test.
func mustTag(t *testing.T, text string) corei18n.TagValue {
	t.Helper()

	tag, err := corei18n.ParseTag(text)
	if err != nil {
		t.Fatalf("ParseTag(%q) = %v", text, err)
	}
	return tag
}

// englishCatalogue is a minimal, complete English catalogue.
func englishCatalogue() svci18n.Catalogue {
	return svci18n.Catalogue{
		"greeting": svci18n.Plain("Hello, {name}"),
		"cart.items": svci18n.PluralForms(map[string]string{
			"one":   "{n} item",
			"other": "{n} items",
		}),
	}
}

// polishCatalogue is the same catalogue, complete for Polish's four
// categories.
func polishCatalogue() svci18n.Catalogue {
	return svci18n.Catalogue{
		"greeting": svci18n.Plain("Cześć, {name}"),
		"cart.items": svci18n.PluralForms(map[string]string{
			"one":   "{n} produkt",
			"few":   "{n} produkty",
			"many":  "{n} produktów",
			"other": "{n} produktu",
		}),
	}
}

func TestNewStoreCompilesACompleteCatalogue(t *testing.T) {
	t.Parallel()

	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish:  polishCatalogue(),
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}

	if got := store.Fallback(); got != english {
		t.Errorf("Fallback() = %q, want %q", got, english)
	}
	tags := store.Tags()
	if len(tags) != 2 || tags[0].String() != "en" || tags[1].String() != "pl" {
		t.Errorf("Tags() = %v, want [en pl] sorted", tags)
	}
	if _, ok := store.Lookup(polish, "greeting"); !ok {
		t.Error("Lookup(pl, greeting) missed")
	}
	// Lookup is EXACT: it does no fallback of its own, so the walk lives in
	// exactly one place.
	if _, ok := store.Lookup(mustTag(t, "de"), "greeting"); ok {
		t.Error("Lookup(de, greeting) hit; the port must not fall back")
	}
}

func TestNewStoreRefusesAnIncompletePluralTranslation(t *testing.T) {
	t.Parallel()

	// This is the defect a translator cannot see by reading their own file:
	// the Polish entry looks finished and is incomplete only against a rule
	// table stored somewhere else.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	_, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish: {
			"greeting": svci18n.Plain("Cześć, {name}"),
			"cart.items": svci18n.PluralForms(map[string]string{
				"one":   "{n} produkt",
				"other": "{n} produktu",
			}),
		},
	})
	if !errs.HasCode(err, svci18n.CodeTranslationIncomplete) {
		t.Fatalf("NewStore = %v, want CodeTranslationIncomplete", err)
	}

	// The three things needed to fix the file must all be in the fields.
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	if fields["tag"] != "pl" {
		t.Errorf("tag field = %q, want %q", fields["tag"], "pl")
	}
	if fields["key"] != "cart.items" {
		t.Errorf("key field = %q, want %q", fields["key"], "cart.items")
	}
	if fields["form"] != "few" {
		t.Errorf("form field = %q, want %q (the first missing category, in Form order)", fields["form"], "few")
	}
}

func TestAnUncountedEntryIsNeverCheckedForCompleteness(t *testing.T) {
	t.Parallel()

	// "Welcome back" needs no plural form in any language. Only an entry
	// written in the FORMS shape declares itself counted.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	_, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: {"greeting": svci18n.Plain("Hello")},
		polish:  {"greeting": svci18n.Plain("Cześć")},
	})
	if err != nil {
		t.Fatalf("NewStore = %v, want an uncounted entry to be accepted", err)
	}
}

func TestACountedEntryWithOnlyOtherIsStillChecked(t *testing.T) {
	t.Parallel()

	// The FORMS shape is the declaration. A translator who wrote
	// `{"other": …}` for Polish declared a counted message, and it is
	// incomplete.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	_, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish: {
			"greeting":   svci18n.Plain("Cześć"),
			"cart.items": svci18n.PluralForms(map[string]string{"other": "{n} produktu"}),
		},
	})
	if !errs.HasCode(err, svci18n.CodeTranslationIncomplete) {
		t.Fatalf("NewStore = %v, want CodeTranslationIncomplete", err)
	}
}

func TestNewStoreRefusesAnUnsupportedLanguageByName(t *testing.T) {
	t.Parallel()

	english, czech := mustTag(t, "en"), mustTag(t, "cs")

	_, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		czech:   {"greeting": svci18n.Plain("Ahoj")},
	})
	if !errs.HasCode(err, svci18n.CodeUnsupportedLanguage) {
		t.Fatalf("NewStore = %v, want CodeUnsupportedLanguage", err)
	}

	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	if fields["tag"] != "cs" {
		t.Errorf("tag field = %q, want %q", fields["tag"], "cs")
	}
	// The supported set travels beside the refusal, so the message a
	// maintainer reads is actionable rather than a bare no.
	if fields["supported"] == "" {
		t.Error("the refusal did not name the supported set")
	}
}

func TestNewStoreRefusesAFallbackItCannotServe(t *testing.T) {
	t.Parallel()

	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	// ADR 0031's refuse half: the SDK does not pick a language.
	var unset corei18n.TagValue
	if _, err := svci18n.NewStore(unset, map[corei18n.TagValue]svci18n.Catalogue{english: englishCatalogue()}); !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Errorf("NewStore with an unset fallback = %v, want CodeCatalogInvalid", err)
	}

	// A fallback with no catalogue turns every miss into a returned key, and
	// the wiring fault would look exactly like a missing translation.
	if _, err := svci18n.NewStore(polish, map[corei18n.TagValue]svci18n.Catalogue{english: englishCatalogue()}); !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Errorf("NewStore with an unserved fallback = %v, want CodeCatalogInvalid", err)
	}
}

func TestNewStoreRefusesABadKeyPatternOrCategoryName(t *testing.T) {
	t.Parallel()

	english := mustTag(t, "en")

	cases := map[string]struct {
		catalogue svci18n.Catalogue
		code      errs.Code
	}{
		"empty key": {
			catalogue: svci18n.Catalogue{"": svci18n.Plain("Hello")},
			code:      corei18n.CodeInvalidKey,
		},
		"unclosed placeholder": {
			catalogue: svci18n.Catalogue{"greeting": svci18n.Plain("Hello, {name")},
			code:      corei18n.CodeInvalidPattern,
		},
		"unknown category name": {
			catalogue: svci18n.Catalogue{"items": svci18n.PluralForms(map[string]string{"otehr": "x", "other": "y"})},
			code:      corei18n.CodeInvalidForm,
		},
		"counted entry with no other": {
			catalogue: svci18n.Catalogue{"items": svci18n.PluralForms(map[string]string{"one": "x"})},
			code:      corei18n.CodePluralFormMissing,
		},
		"empty counted entry": {
			catalogue: svci18n.Catalogue{"items": svci18n.PluralForms(nil)},
			code:      corei18n.CodePluralFormMissing,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{english: c.catalogue})
			if !errs.HasCode(err, c.code) {
				t.Errorf("NewStore = %v, want %v", err, c.code)
			}
		})
	}
}

func TestMissingIsTheStartupGateForUntranslatedKeys(t *testing.T) {
	t.Parallel()

	// The SDK's answer to "which strings were never translated" is a query a
	// caller's own test runs, not a render-time behaviour — at render time a
	// missing translation has already happened.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish:  {"greeting": svci18n.Plain("Cześć, {name}")},
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}

	gaps := store.Missing(polish)
	if len(gaps) != 1 || gaps[0] != "cart.items" {
		t.Errorf("Missing(pl) = %v, want [cart.items]", gaps)
	}
	if gaps := store.Missing(english); len(gaps) != 0 {
		t.Errorf("Missing(en) = %v, want empty for the fallback itself", gaps)
	}
	// An unknown language is missing everything, which is the honest answer.
	if gaps := store.Missing(mustTag(t, "de")); len(gaps) != 2 {
		t.Errorf("Missing(de) = %v, want both keys", gaps)
	}
}

func TestKeysAndTagsAreClonedOut(t *testing.T) {
	t.Parallel()

	english := mustTag(t, "en")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{english: englishCatalogue()})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}

	// One Store is read by every renderer in the process; a caller that
	// sorted the returned slice in place would reorder them all.
	tags := store.Tags()
	tags[0] = corei18n.TagValue{}
	if again := store.Tags(); again[0] != english {
		t.Error("Tags() handed out its internal slice")
	}

	keys := store.Keys(english)
	if len(keys) != 2 || keys[0] != "cart.items" || keys[1] != "greeting" {
		t.Fatalf("Keys(en) = %v, want [cart.items greeting] sorted", keys)
	}
	keys[0] = ""
	if again := store.Keys(english); again[0] != "cart.items" {
		t.Error("Keys() handed out its internal slice")
	}

	// nil, not an empty slice: an unknown language has no keys, and an empty
	// slice reads as "translated, with nothing in it".
	if got := store.Keys(mustTag(t, "de")); got != nil {
		t.Errorf("Keys(de) = %v, want nil", got)
	}
}

func TestStoreSatisfiesThePortAndBothSiblings(t *testing.T) {
	t.Parallel()

	english := mustTag(t, "en")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{english: englishCatalogue()})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}

	catalog := corei18n.Catalog(store)
	if _, ok := catalog.(corei18n.KeyLister); !ok {
		t.Error("Store does not implement the KeyLister sibling")
	}
	if _, ok := catalog.(corei18n.Fallbacker); !ok {
		t.Error("Store does not implement the Fallbacker sibling")
	}
}

// ADR 0039: pkg/v1/i18n aliases Catalog, Go interfaces are structural, and a
// third method would break every downstream two-method double at compile time
// with no deprecation window. This assertion IS that test — it lives at
// package level so the build fails before any test runs.
var _ corei18n.Catalog = twoMethodDouble{}

// twoMethodDouble implements exactly the two methods Catalog declares. If the
// port grows a third, this file stops compiling — which is the point.
type twoMethodDouble struct{}

func (twoMethodDouble) Lookup(_ corei18n.TagValue, _ corei18n.Key) (corei18n.MessageValue, bool) {
	return corei18n.MessageValue{}, false
}

func (twoMethodDouble) Tags() []corei18n.TagValue { return nil }
