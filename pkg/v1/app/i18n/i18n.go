//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/i18n .

// Package i18n is the public facade for the SDK's message-translation domain:
// a catalogue of translated messages, CLDR plural forms that are right in
// Polish and Arabic and not only in English, and Accept-Language negotiation
// that never fails a request.
//
//	english, err := i18n.ParseTag("en")
//	if err != nil {
//		return err
//	}
//
//	store, err := i18n.LoadFS(os.DirFS("locales"), ".", "json", english)
//	if err != nil {
//		return err // a missing plural form is a startup failure, not a wrong page
//	}
//
//	polish, err := i18n.ParseTag("pl")
//	if err != nil {
//		return err
//	}
//
//	printer, err := i18n.NewPrinter(store, polish)
//	if err != nil {
//		return err
//	}
//
//	text, err := printer.RenderCount("cart.items", i18n.Int(5), i18n.Args{"n": "5"})
//	// "5 produktów" — `many`, which English does not have
//
// # The decision this package exists to get right
//
// Polish has four plural categories, Russian four, Arabic six. English has
// two. An i18n library that resolves an unknown language by falling back to
// English's rules renders a grammatically wrong sentence on every page of the
// Polish site, and nothing observes it: the page renders, the tests pass,
// nothing is logged, and the only people who can see it have no way to report
// it that reaches the right file.
//
// So this package REFUSES what it cannot do correctly. A language with no
// reviewed CLDR rule is [UnsupportedLanguage] at construction, naming the
// language and the supported set beside it. A counted message missing a
// category its language can produce is [TranslationIncomplete] at
// construction, naming the file, the key and the form. Both are startup
// failures, where a human is present and the fix is an edit.
//
// # Supported languages
//
// Thirteen entries, transcribed by hand from the CLDR cardinal chart with each
// clause cited in the code: `ar`, `de`, `en`, `es`, `fr`, `it`, `ja`, `nl`,
// `pl`, `pt`, `pt-PT`, `ru`, `zh`. Between them they cover every one of the six
// CLDR categories and every rule shape — no distinction (ja, zh), one/other
// (de, en, nl), one/many/other (es, fr, it, pt), one/few/many/other (pl, ru)
// and all six (ar). A region narrowing CLDR does not distinguish inherits its
// language: "fr-CA", "de-AT" and "zh-Hant" all work. "pt-PT" has its own
// entry, because CLDR gives European Portuguese a different singular from
// Brazilian.
//
// [SupportedTags] is the list at runtime. Adding a language is a rule, its
// CLDR citation and its boundary-value test, in one change.
//
// # What this package deliberately is not
//
// It renders a message. It does NOT format numbers, dates, times, currencies
// or units, does not collate, case-map, normalise or transliterate, and has no
// opinion about text direction. Those are the rest of CLDR, and shipping a
// half-correct version of any of them is worse than not shipping it: [Args] is
// map[string]string, so a caller who wants "1 234,50 €" formats it and
// discovers at the call site that this is their job.
//
// It is also not a template engine. The syntax is literal text with named
// placeholders — "Welcome back, {name}" — and a literal brace is "{{".
// Positional arguments, format specifiers, ICU MessageFormat's nested plural
// and select constructs, and filter calls are each refused by name, because
// each of them is a parser living inside a translation file.
//
// # Escaping is not done here
//
// A rendered message is a plain string. Putting it in a page goes through
// pkg/v1/app/view, whose contextual escaping knows where in the HTML the cursor
// is. Escaping here as well would double-escape every apostrophe in every
// French sentence in the catalogue.
//
// Message patterns are trusted input — they come from catalogue files the
// developer ships. Argument values are not, and are substituted literally and
// never rescanned. A pattern assembled from user input would let that user
// name a placeholder the caller happens to pass and read its value; this
// package cannot detect that and does not try.
package i18n
