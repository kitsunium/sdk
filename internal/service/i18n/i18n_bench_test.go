// Package i18n_test — the render path is on every page of a translated
// application, so it is measured rather than assumed.
package i18n_test

import (
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// Result sinks. Assigning into a package-level var keeps the measured call
// from being folded away AND keeps every error the benchmark produces
// reachable, so nothing is discarded into a blank identifier.
var (
	sinkString string
	sinkErr    error
	sinkTag    corei18n.TagValue
	sinkForm   corei18n.Form
	sinkOK     bool
	sinkMsg    corei18n.MessageValue
	sinkStore  *svci18n.Store
	sinkPrint  *svci18n.Printer
)

// benchCatalogues is the catalogue every benchmark renders out of: English as
// the fallback, Polish for the four-category rule, Arabic for all six, and one
// language that deliberately does not hold every key so the fallback walk can
// be measured.
func benchCatalogues() map[corei18n.TagValue]svci18n.Catalogue {
	english := benchTag("en")
	polish := benchTag("pl")
	arabic := benchTag("ar")
	french := benchTag("fr")

	return map[corei18n.TagValue]svci18n.Catalogue{
		english: {
			"literal":   svci18n.Plain("Your cart is empty"),
			"one.arg":   svci18n.Plain("Welcome back, {name}"),
			"three.arg": svci18n.Plain("{name} moved {count} files to {folder}"),
			"cart.items": svci18n.PluralForms(map[string]string{
				"one": "{n} item", "other": "{n} items",
			}),
		},
		polish: {
			"literal": svci18n.Plain("Twój koszyk jest pusty"),
			"one.arg": svci18n.Plain("Witaj ponownie, {name}"),
			"cart.items": svci18n.PluralForms(map[string]string{
				"one": "{n} produkt", "few": "{n} produkty",
				"many": "{n} produktów", "other": "{n} produktu",
			}),
		},
		arabic: {
			"literal": svci18n.Plain("سلتك فارغة"),
			"cart.items": svci18n.PluralForms(map[string]string{
				"zero": "لا عناصر", "one": "عنصر واحد", "two": "عنصران",
				"few": "{n} عناصر", "many": "{n} عنصرا", "other": "{n} عنصر",
			}),
		},
		// French holds nothing, so every key rendered under it walks the
		// chain down to the English fallback.
		french: {"literal": svci18n.Plain("Votre panier est vide")},
	}
}

// benchTag parses a canonical tag, panicking on a typo in this file.
func benchTag(text string) corei18n.TagValue {
	tag, err := corei18n.ParseTag(text)
	if err != nil {
		panic("benchmark tag is not canonical: " + text)
	}
	return tag
}

// benchStore builds the shared store.
func benchStore(b *testing.B) *svci18n.Store {
	b.Helper()

	store, err := svci18n.NewStore(benchTag("en"), benchCatalogues())
	if err != nil {
		b.Fatalf("NewStore = %v", err)
	}
	return store
}

// benchPrinter builds a printer for one language.
func benchPrinter(b *testing.B, language string) *svci18n.Printer {
	b.Helper()

	printer, err := svci18n.NewPrinter(benchStore(b), benchTag(language))
	if err != nil {
		b.Fatalf("NewPrinter = %v", err)
	}
	return printer
}

// benchNegotiator builds the negotiator every negotiation benchmark uses.
func benchNegotiator(b *testing.B) *svci18n.Negotiator {
	b.Helper()

	negotiator, err := svci18n.NewNegotiator([]corei18n.TagValue{
		benchTag("en"), benchTag("fr"), benchTag("pl"), benchTag("ar"), benchTag("pt-PT"),
	}, benchTag("en"))
	if err != nil {
		b.Fatalf("NewNegotiator = %v", err)
	}
	return negotiator
}

func BenchmarkRenderLiteral(b *testing.B) {
	printer := benchPrinter(b, "en")

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.Render("literal", nil)
	}
}

func BenchmarkRenderOneArgument(b *testing.B) {
	printer := benchPrinter(b, "en")
	args := corei18n.Args{"name": "Ada"}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.Render("one.arg", args)
	}
}

func BenchmarkRenderThreeArguments(b *testing.B) {
	printer := benchPrinter(b, "en")
	args := corei18n.Args{"name": "Ada", "count": "12", "folder": "Archive"}

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.Render("three.arg", args)
	}
}

func BenchmarkRenderCountEnglish(b *testing.B) {
	printer := benchPrinter(b, "en")
	args := corei18n.Args{"n": "5"}
	count := corei18n.Int(5)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.RenderCount("cart.items", count, args)
	}
}

func BenchmarkRenderCountPolish(b *testing.B) {
	printer := benchPrinter(b, "pl")
	args := corei18n.Args{"n": "22"}
	count := corei18n.Int(22)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.RenderCount("cart.items", count, args)
	}
}

func BenchmarkRenderCountArabic(b *testing.B) {
	printer := benchPrinter(b, "ar")
	args := corei18n.Args{"n": "42"}
	count := corei18n.Int(42)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.RenderCount("cart.items", count, args)
	}
}

func BenchmarkRenderThroughFallback(b *testing.B) {
	// French holds "literal" and nothing else, so this walks fr → en before
	// it finds the message. It is the cost of a partially translated
	// catalogue, which is every catalogue.
	printer := benchPrinter(b, "fr")
	args := corei18n.Args{"n": "5"}
	count := corei18n.Int(5)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.RenderCount("cart.items", count, args)
	}
}

func BenchmarkRenderMissingKey(b *testing.B) {
	// The error path builds a refusal carrying the chain, so it is the
	// slowest render by design. It is measured because a catalogue with a
	// systematic gap would otherwise pay it on every page without anyone
	// knowing what it costs.
	printer := benchPrinter(b, "pl")

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkString, sinkErr = printer.Render("absent.key", nil)
	}
}

func BenchmarkPluralSelectPolish(b *testing.B) {
	rules, ok := svci18n.Rules(benchTag("pl"))
	if !ok {
		b.Fatal("Rules(pl) = false")
	}
	count := corei18n.Int(22)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkForm = rules.Select(count)
	}
}

func BenchmarkPluralSelectArabic(b *testing.B) {
	rules, ok := svci18n.Rules(benchTag("ar"))
	if !ok {
		b.Fatal("Rules(ar) = false")
	}
	count := corei18n.Int(42)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkForm = rules.Select(count)
	}
}

func BenchmarkStoreLookup(b *testing.B) {
	store := benchStore(b)
	tag := benchTag("pl")

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkMsg, sinkOK = store.Lookup(tag, "cart.items")
	}
}

func BenchmarkNegotiateAbsentHeader(b *testing.B) {
	negotiator := benchNegotiator(b)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkTag = negotiator.Negotiate("")
	}
}

func BenchmarkNegotiateSingleRange(b *testing.B) {
	negotiator := benchNegotiator(b)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkTag = negotiator.Negotiate("fr")
	}
}

func BenchmarkNegotiateBrowserHeader(b *testing.B) {
	// What Firefox and Chrome actually send.
	negotiator := benchNegotiator(b)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkTag = negotiator.Negotiate("fr-CH,fr;q=0.9,en;q=0.8,de;q=0.7,*;q=0.5")
	}
}

func BenchmarkNegotiateNoMatch(b *testing.B) {
	// Every element is parsed and every one fails to match, which is the
	// worst case a well-formed header can produce.
	negotiator := benchNegotiator(b)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkTag = negotiator.Negotiate("cs;q=0.9,lt;q=0.8,lv;q=0.7,ga;q=0.6,cy;q=0.5")
	}
}

func BenchmarkParseTag(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkTag, sinkErr = corei18n.ParseTag("zh-Hant-TW")
	}
}

func BenchmarkNewPrinter(b *testing.B) {
	// The startup cost the render path exists to keep off the request path.
	store := benchStore(b)
	tag := benchTag("pl")

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkPrint, sinkErr = svci18n.NewPrinter(store, tag)
	}
}

func BenchmarkNewStore(b *testing.B) {
	// The load-time compile: every pattern parsed, every counted message
	// checked against its language's categories.
	catalogues := benchCatalogues()
	english := benchTag("en")

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		sinkStore, sinkErr = svci18n.NewStore(english, catalogues)
	}
}
