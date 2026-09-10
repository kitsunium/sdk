// Package i18n_test — the renderer, and the decision the domain exists for.
package i18n_test

import (
	"strings"
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// twoLanguageStore builds a store with a complete English catalogue and a
// Polish one that is missing "cart.items" entirely.
func twoLanguageStore(t *testing.T) (*svci18n.Store, corei18n.TagValue, corei18n.TagValue) {
	t.Helper()

	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish:  {"greeting": svci18n.Plain("Cześć, {name}")},
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	return store, english, polish
}

func TestThePluralRulesFollowTheMessageAndNotTheRequest(t *testing.T) {
	t.Parallel()

	// THE test of this domain. A Polish request, a key only English has, and
	// a quantity Polish rules call `many`. The English message carries `one`
	// and `other` and nothing else.
	//
	// Selecting with the REQUESTED language's rules asks that message for
	// `many` and gets PluralFormMissing — or, in the library that "handles"
	// it, silently gets the `other` form under a category it never declared.
	// Selecting with the ANSWERING language's rules is the only combination
	// that renders a correct English sentence.
	store, _, polish := twoLanguageStore(t)

	printer, err := svci18n.NewPrinter(store, polish)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	got, err := printer.RenderCount("cart.items", corei18n.Int(5), corei18n.Args{"n": "5"})
	if err != nil {
		t.Fatalf("RenderCount fell back and failed: %v", err)
	}
	if want := "5 items"; got != want {
		t.Errorf("RenderCount = %q, want %q — the English message must be pluralised by English rules", got, want)
	}

	// And the singular still works through the same fallback.
	got, err = printer.RenderCount("cart.items", corei18n.Int(1), corei18n.Args{"n": "1"})
	if err != nil {
		t.Fatalf("RenderCount = %v", err)
	}
	if want := "1 item"; got != want {
		t.Errorf("RenderCount = %q, want %q", got, want)
	}
}

func TestARequestedLanguageThatHasTheKeyUsesItsOwnRules(t *testing.T) {
	t.Parallel()

	// The other side of the same decision: when Polish DOES hold the key, all
	// four Polish categories must be reachable.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish:  polishCatalogue(),
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	printer, err := svci18n.NewPrinter(store, polish)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	for _, c := range []struct {
		n    int64
		want string
	}{
		{n: 1, want: "1 produkt"},
		{n: 3, want: "3 produkty"},   // few
		{n: 5, want: "5 produktów"},  // many
		{n: 22, want: "22 produkty"}, // few again, which English cannot express
	} {
		got, renderErr := printer.RenderCount("cart.items", corei18n.Int(c.n), corei18n.Args{"n": itoa(c.n)})
		if renderErr != nil {
			t.Fatalf("RenderCount(%d) = %v", c.n, renderErr)
		}
		if got != c.want {
			t.Errorf("RenderCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// itoa renders a small non-negative integer without importing strconv into the
// assertion, so the test's own formatting cannot be mistaken for the SDK's
// (which deliberately does not format numbers at all).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestAMissingKeyRendersTheKeyAndReportsIt(t *testing.T) {
	t.Parallel()

	// A blank is a defect nobody reports. A key on screen is one everybody
	// does — and it discloses nothing, because a key is a developer
	// identifier already visible in the client bundle.
	store, english, _ := twoLanguageStore(t)

	printer, err := svci18n.NewPrinter(store, english)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	got, err := printer.Render("checkout.button.pay", nil)
	if !errs.HasCode(err, corei18n.CodeMessageNotFound) {
		t.Fatalf("Render = %v, want CodeMessageNotFound", err)
	}
	if got != "checkout.button.pay" {
		t.Errorf("Render = %q, want the key itself", got)
	}

	// The same for a counted render.
	got, err = printer.RenderCount("checkout.button.pay", corei18n.Int(2), nil)
	if !errs.HasCode(err, corei18n.CodeMessageNotFound) {
		t.Fatalf("RenderCount = %v, want CodeMessageNotFound", err)
	}
	if got != "checkout.button.pay" {
		t.Errorf("RenderCount = %q, want the key itself", got)
	}

	// The languages that were tried travel as a field, so an operator can
	// tell a missing translation from a misconfigured chain.
	fields := map[string]string{}
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	if fields["key"] != "checkout.button.pay" {
		t.Errorf("key field = %q", fields["key"])
	}
	if !strings.Contains(fields["tried"], "en") {
		t.Errorf("tried field = %q, want it to name the chain", fields["tried"])
	}
}

func TestTheChainClimbsParentsThenTheFallback(t *testing.T) {
	t.Parallel()

	// "fr-CA" resolves to "fr" and then to the fallback, and a language
	// reached twice is looked up once.
	english, french, canadian := mustTag(t, "en"), mustTag(t, "fr"), mustTag(t, "fr-CA")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english:  englishCatalogue(),
		french:   {"greeting": svci18n.Plain("Bonjour, {name}")},
		canadian: {"greeting": svci18n.Plain("Allo, {name}")},
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}

	printer, err := svci18n.NewPrinter(store, canadian)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}
	if got := printer.Tag(); got != canadian {
		t.Errorf("Tag() = %q, want %q — the language REQUESTED", got, canadian)
	}

	// Most specific wins.
	got, err := printer.Render("greeting", corei18n.Args{"name": "Ada"})
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if want := "Allo, Ada"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}

	// And a key only the fallback has still resolves, through the parent.
	got, err = printer.RenderCount("cart.items", corei18n.Int(2), corei18n.Args{"n": "2"})
	if err != nil {
		t.Fatalf("RenderCount = %v", err)
	}
	if want := "2 items"; got != want {
		t.Errorf("RenderCount = %q, want %q", got, want)
	}
}

func TestNewPrinterRefusesAnUnusableLanguage(t *testing.T) {
	t.Parallel()

	store, _, _ := twoLanguageStore(t)

	var unset corei18n.TagValue
	if _, err := svci18n.NewPrinter(store, unset); !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Errorf("NewPrinter with an unset tag = %v, want CodeCatalogInvalid", err)
	}

	// A language with no reviewed rule is refused at construction, so the
	// render path has no language it cannot plural.
	if _, err := svci18n.NewPrinter(store, mustTag(t, "cs")); !errs.HasCode(err, svci18n.CodeUnsupportedLanguage) {
		t.Errorf("NewPrinter(cs) = %v, want CodeUnsupportedLanguage", err)
	}
}

func TestACatalogWithNoFallbackSimplyHasAShorterChain(t *testing.T) {
	t.Parallel()

	// ADR 0039: the absence of the Fallbacker sibling IS the answer. There is
	// no zero Tag for the renderer to interpret.
	printer, err := svci18n.NewPrinter(oneLanguageCatalog{}, mustTag(t, "en"))
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	got, err := printer.Render("greeting", nil)
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if want := "Hello"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}

	// And a miss is a miss: there is nowhere else to look.
	if _, err := printer.Render("absent", nil); !errs.HasCode(err, corei18n.CodeMessageNotFound) {
		t.Errorf("Render = %v, want CodeMessageNotFound", err)
	}
}

// oneLanguageCatalog is a Catalog that implements neither ADR 0039 sibling.
type oneLanguageCatalog struct{}

func (oneLanguageCatalog) Lookup(tag corei18n.TagValue, key corei18n.Key) (corei18n.MessageValue, bool) {
	if tag.String() != "en" || key != "greeting" {
		return corei18n.MessageValue{}, false
	}
	message, err := corei18n.NewMessage("Hello")
	return message, err == nil
}

func (oneLanguageCatalog) Tags() []corei18n.TagValue { return nil }

func TestRenderNeverDisclosesAnArgumentValue(t *testing.T) {
	t.Parallel()

	// The same security property core/i18n states, checked once more where a
	// caller actually renders: the service layer adds tag, key and chain
	// fields to a refusal, and must not add a value along with them.
	store, english, _ := twoLanguageStore(t)

	printer, err := svci18n.NewPrinter(store, english)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	const leak = "sk-live-4f9a2c8e-EXFILTRATE-ME"

	// Missing key, with a secret in the args.
	_, notFound := printer.Render("absent.key", corei18n.Args{"name": leak})
	// Missing argument, with the secret under the wrong name.
	_, missingArg := printer.Render("greeting", corei18n.Args{"other": leak})

	for _, failure := range []error{notFound, missingArg} {
		if failure == nil {
			t.Fatal("expected both error paths to fire")
		}
		if strings.Contains(failure.Error(), leak) {
			t.Errorf("Error() disclosed an argument value: %q", failure.Error())
		}
		if strings.Contains(errs.PublicOf(failure), leak) || strings.Contains(errs.PrivateOf(failure), leak) {
			t.Error("Public or Private disclosed an argument value")
		}
		for _, field := range errs.FieldsOf(failure) {
			if strings.Contains(field.StringValue(), leak) {
				t.Errorf("field %q disclosed an argument value", field.Key())
			}
		}
	}
}

// TestOnePrinterIsSafeForConcurrentUse renders from one Printer on many
// goroutines at once, under -race, because that is how a server uses it.
//
// Goroutine lifecycle: exactly `workers` goroutines are started, each performs
// one render and sends exactly one string on a buffered channel sized to
// `workers`, then returns. Nothing blocks on the send, so no goroutine can
// outlive the test even if an assertion fails; the test then receives exactly
// `workers` values, so every goroutine has been observed to finish before it
// returns. There is no cancellation path because there is nothing to cancel —
// a render neither blocks nor takes a context.
func TestOnePrinterIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	// A Printer is built once and shared by every goroutine serving a
	// request; nothing in it may mutate.
	english, polish := mustTag(t, "en"), mustTag(t, "pl")

	store, err := svci18n.NewStore(english, map[corei18n.TagValue]svci18n.Catalogue{
		english: englishCatalogue(),
		polish:  polishCatalogue(),
	})
	if err != nil {
		t.Fatalf("NewStore = %v", err)
	}
	printer, err := svci18n.NewPrinter(store, polish)
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}

	const workers int = 32
	done := make(chan string, workers)
	for range workers {
		go func() {
			text, renderErr := printer.RenderCount("cart.items", corei18n.Int(5), corei18n.Args{"n": "5"})
			if renderErr != nil {
				done <- renderErr.Error()
				return
			}
			done <- text
		}()
	}
	for range workers {
		if got := <-done; got != "5 produktów" {
			t.Fatalf("concurrent render = %q, want %q", got, "5 produktów")
		}
	}
}
