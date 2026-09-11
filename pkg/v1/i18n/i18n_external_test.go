// Package i18n_test — the public facade as a consumer reaches it.
package i18n_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/i18n"

	_ "github.com/kitsunium/sdk/pkg/v1/codec" // register every SDK codec Format
)

// polishLocale is a complete Polish catalogue: Polish rules produce one, few,
// many and other, so all four must be present or the load is refused.
const polishLocale = `{
	"greeting": "Cześć, {name}",
	"cart.items": {
		"one": "{n} produkt",
		"few": "{n} produkty",
		"many": "{n} produktów",
		"other": "{n} produktu"
	}
}`

// englishLocale is the fallback.
const englishLocale = `{
	"greeting": "Hello, {name}",
	"cart.items": {"one": "{n} item", "other": "{n} items"},
	"checkout.title": "Checkout"
}`

// locales is the catalogue directory both tests load.
func locales() fstest.MapFS {
	return fstest.MapFS{
		"locales/en.json": {Data: []byte(englishLocale)},
		"locales/pl.json": {Data: []byte(polishLocale)},
	}
}

// tag parses a canonical tag or fails the test.
func tag(t *testing.T, text string) i18n.Tag {
	t.Helper()

	parsed, err := i18n.ParseTag(text)
	if err != nil {
		t.Fatalf("ParseTag(%q) = %v", text, err)
	}
	return parsed
}

func TestTheWholeThingFromACatalogueDirectory(t *testing.T) {
	t.Parallel()

	english, polish := tag(t, "en"), tag(t, "pl")

	store, err := i18n.LoadFS(locales(), "locales", "json", english)
	if err != nil {
		t.Fatalf("LoadFS = %v", err)
	}

	negotiator, err := i18n.NewNegotiator(store.Tags(), english)
	if err != nil {
		t.Fatalf("NewNegotiator = %v", err)
	}

	// A browser header, negotiated to a supported language.
	if got := negotiator.Negotiate("pl-PL,pl;q=0.9,en;q=0.5"); got != polish {
		t.Fatalf("Negotiate = %q, want %q", got, polish)
	}

	printer, err := i18n.NewPrinter(store, polish)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	// All four Polish categories, none of which English can express.
	for _, c := range []struct {
		n    int64
		text string
		want string
	}{
		{n: 1, text: "1", want: "1 produkt"},
		{n: 3, text: "3", want: "3 produkty"},
		{n: 5, text: "5", want: "5 produktów"},
		{n: 22, text: "22", want: "22 produkty"},
	} {
		got, renderErr := printer.RenderCount("cart.items", i18n.Int(c.n), i18n.Args{"n": c.text})
		if renderErr != nil {
			t.Fatalf("RenderCount(%d) = %v", c.n, renderErr)
		}
		if got != c.want {
			t.Errorf("RenderCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}

	// A key only English holds is served from the fallback AND pluralised by
	// English rules, because the English message is the one being rendered.
	got, err := printer.Render("checkout.title", nil)
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if got != "Checkout" {
		t.Errorf("Render = %q, want %q", got, "Checkout")
	}

	// The startup gate: which strings were never translated.
	gaps := store.Missing(polish)
	if len(gaps) != 1 || gaps[0] != "checkout.title" {
		t.Errorf("Missing(pl) = %v, want [checkout.title]", gaps)
	}
}

func TestAnIncompleteTranslationIsAStartupFailure(t *testing.T) {
	t.Parallel()

	// The decision the domain exists for, reachable from the public API: a
	// Polish catalogue with only one and other looks finished to whoever
	// wrote it, and the program does not start.
	dir := fstest.MapFS{
		"locales/en.json": {Data: []byte(englishLocale)},
		"locales/pl.json": {Data: []byte(`{
			"greeting": "Cześć, {name}",
			"cart.items": {"one": "{n} produkt", "other": "{n} produktu"}
		}`)},
	}

	_, err := i18n.LoadFS(dir, "locales", "json", tag(t, "en"))
	if err == nil {
		t.Fatal("LoadFS accepted a Polish catalogue with no few or many form")
	}
	if code, ok := errs.CodeOf(err); !ok || code.String() != "0.3.60.4" {
		t.Errorf("code = %v, want 0.3.60.4 TRANSLATION_INCOMPLETE", code)
	}
}

func TestAnUnsupportedLanguageIsRefusedRatherThanApproximated(t *testing.T) {
	t.Parallel()

	// Czech has one/few/many/other. Borrowing English's two categories would
	// render a wrong sentence on every Czech page with no symptom at all — so
	// the language is refused, and the program does not start.
	dir := fstest.MapFS{
		"locales/en.json": {Data: []byte(englishLocale)},
		"locales/cs.json": {Data: []byte(`{"greeting": "Ahoj, {name}"}`)},
	}

	_, err := i18n.LoadFS(dir, "locales", "json", tag(t, "en"))
	if err == nil {
		t.Fatal("LoadFS accepted a language with no reviewed CLDR rule")
	}
	if code, ok := errs.CodeOf(err); !ok || code.String() != "0.3.60.1" {
		t.Errorf("code = %v, want 0.3.60.1 UNSUPPORTED_LANGUAGE", code)
	}
	// The supported set travels as a log-only field, which pkg/v1/errs
	// deliberately does not expose — the refusal a consumer READS is the
	// Public sentence, and the diagnosis is for the operator's log.
	if public := errs.PublicOf(err); public == "" {
		t.Error("the refusal carries no Public sentence")
	}
}

func TestSupportedTagsIsReachableFromThePublicAPI(t *testing.T) {
	t.Parallel()

	tags := i18n.SupportedTags()
	if len(tags) == 0 {
		t.Fatal("SupportedTags() is empty")
	}

	// Rules resolves a region narrowing to its base language.
	if _, ok := i18n.Rules(tag(t, "fr-CA")); !ok {
		t.Error("Rules(fr-CA) = false, want French's rules")
	}
	if _, ok := i18n.Rules(tag(t, "cs")); ok {
		t.Error("Rules(cs) = true, want a refusal")
	}
}

func TestArgumentValuesAreSubstitutedAndNeverRescanned(t *testing.T) {
	t.Parallel()

	// A value is untrusted. If substitution were a second pass, a user whose
	// display name is "{admin_token}" would read an argument the caller never
	// meant to show them.
	store, err := i18n.LoadFS(locales(), "locales", "json", tag(t, "en"))
	if err != nil {
		t.Fatalf("LoadFS = %v", err)
	}
	printer, err := i18n.NewPrinter(store, tag(t, "en"))
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	got, err := printer.Render("greeting", i18n.Args{
		"name":        "{admin_token}",
		"admin_token": "sk-live-SECRET",
	})
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if got != "Hello, {admin_token}" {
		t.Errorf("Render = %q, want the value inserted literally", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Fatal("a substituted value was rescanned and disclosed a second argument")
	}
}

func TestAMissingKeyIsVisibleRatherThanBlank(t *testing.T) {
	t.Parallel()

	store, err := i18n.LoadFS(locales(), "locales", "json", tag(t, "en"))
	if err != nil {
		t.Fatalf("LoadFS = %v", err)
	}
	printer, err := i18n.NewPrinter(store, tag(t, "en"))
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	got, err := printer.Render("checkout.button.pay", nil)
	if err == nil {
		t.Fatal("Render accepted a key no language holds")
	}
	if got != "checkout.button.pay" {
		t.Errorf("Render = %q, want the key itself — a blank is a defect nobody reports", got)
	}
}
