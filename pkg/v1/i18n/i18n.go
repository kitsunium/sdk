//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/i18n .

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
// pkg/v1/view, whose contextual escaping knows where in the HTML the cursor
// is. Escaping here as well would double-escape every apostrophe in every
// French sentence in the catalogue.
//
// Message patterns are trusted input — they come from catalogue files the
// developer ships. Argument values are not, and are substituted literally and
// never rescanned. A pattern assembled from user input would let that user
// name a placeholder the caller happens to pass and read its value; this
// package cannot detect that and does not try.
package i18n

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/core/codec"
	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// The six CLDR plural categories, aliased from internal/core/i18n. FormOther
// is the zero Form, because it is the only category every language defines.
//
// They are declared one per const rather than as a group: their VALUES are the
// core package's, so an iota here would restate an ordering this package does
// not own and would silently diverge the day the core enum is reordered.

// FormOther is the catch-all. Every language defines it and every message must
// carry it.
const FormOther Form = corei18n.FormOther

// FormZero is the distinct nought of Arabic and Latvian.
const FormZero Form = corei18n.FormZero

// FormOne is the singular of languages that have one.
const FormOne Form = corei18n.FormOne

// FormTwo is the dual.
const FormTwo Form = corei18n.FormTwo

// FormFew is the small plural of the Slavic and Semitic families.
const FormFew Form = corei18n.FormFew

// FormMany is the large plural, and the compact-decimal form in several
// Romance languages.
const FormMany Form = corei18n.FormMany

// Tag is a language, optionally narrowed by a script and a region:
// `language[-Script][-REGION]` and deliberately nothing else. It is
// comparable, so it is a map key, and it holds its canonical spelling, so
// [Tag.String] allocates nothing.
type Tag = corei18n.TagValue

// Key names a message inside a catalogue. It is a developer identifier —
// "checkout.button.pay" — and it is what a render puts on screen when the
// translation is missing.
type Key = corei18n.Key

// Message is one compiled translation. Its placeholders are parsed when the
// catalogue is built, so a malformed pattern fails at startup and a render
// never parses.
type Message = corei18n.MessageValue

// Form is a CLDR plural category. [FormOther] is the zero value, because it is
// the only category every language defines.
type Form = corei18n.Form

// Count is a quantity described by the CLDR plural operands. It carries the
// DISPLAY precision, because a quantity of one shown with a decimal is a different category
// in English and a float64 cannot tell them apart.
type Count = corei18n.CountValue

// Args binds placeholder names to the text that replaces them. It is
// map[string]string on purpose — see the package comment.
type Args = corei18n.Args

// Catalog is the message-source port, frozen at two methods (ADR 0039).
// [Store] implements it.
type Catalog = corei18n.Catalog

// KeyLister is the ADR 0039 sibling that enumerates a catalog's keys, reached
// by type assertion. [Store.Missing] is what it exists for.
type KeyLister = corei18n.KeyLister

// Fallbacker is the ADR 0039 sibling that names a catalog's fallback language.
// A catalog with no fallback does not implement it, and the absence is the
// answer.
type Fallbacker = corei18n.Fallbacker

// PluralRule reports which CLDR category a quantity falls in for one language.
// It is a func type, which satisfies ADR 0039 structurally.
type PluralRule = corei18n.PluralRule

// Entry is one catalogue entry before it is compiled: a plain pattern
// ([Plain]) or one pattern per CLDR category ([PluralForms]).
type Entry = svci18n.EntryValue

// Catalogue is one language's entries, keyed by message key.
type Catalogue = svci18n.Catalogue

// Store is the concrete, immutable catalogue. It implements [Catalog],
// [KeyLister] and [Fallbacker].
type Store = svci18n.Store

// Printer renders messages in one language. Build one per language at startup,
// not one per request — see [NewPrinter].
type Printer = svci18n.Printer

// Negotiator resolves an Accept-Language header to one of a fixed set of
// languages. Everything that can be wrong is refused when it is built, so
// [Negotiator.Negotiate] has no failure mode.
type Negotiator = svci18n.Negotiator

// Plural is one language's CLDR plural specification: the categories it can
// produce, and the rule that picks between them.
type Plural = svci18n.PluralValue

var (
	// InvalidTag is returned by [ParseTag] for a tag outside the
	// `language[-Script][-REGION]` subset. It is NOT what a peculiar
	// Accept-Language header produces: [Negotiator.Negotiate] skips an
	// element it cannot use and never fails.
	InvalidTag = corei18n.InvalidTag
	// InvalidKey is returned for an empty message key, or one carrying a
	// control character.
	InvalidKey = corei18n.InvalidKey
	// InvalidPattern is returned for a malformed pattern: an unclosed brace,
	// an unmatched closing brace, or a placeholder name outside
	// [A-Za-z_][A-Za-z0-9_]*.
	InvalidPattern = corei18n.InvalidPattern
	// ArgumentMissing is returned when [Args] does not carry a placeholder
	// the pattern names. It names the placeholder and never a value.
	ArgumentMissing = corei18n.ArgumentMissing
	// MessageNotFound is returned when no language in the chain holds the
	// key. The render still returns a non-empty string — the key itself.
	MessageNotFound = corei18n.MessageNotFound
	// PluralFormMissing is returned when a message is asked for a category it
	// does not carry. A catalogue built by [NewStore] cannot produce it.
	PluralFormMissing = corei18n.PluralFormMissing
	// InvalidCount is returned by [Decimal] for a NaN, an infinity, or a
	// fraction-digit count outside 0..9.
	InvalidCount = corei18n.InvalidCount
	// InvalidForm is returned by [ParseForm] for a name outside the six CLDR
	// categories.
	InvalidForm = corei18n.InvalidForm
	// UnsupportedLanguage is returned when a catalogue or a printer names a
	// language this SDK has no reviewed CLDR plural rule for. It is the
	// domain's central refusal — see the package comment.
	UnsupportedLanguage = svci18n.UnsupportedLanguage
	// CatalogInvalid is returned for a catalogue that cannot be compiled: an
	// unset or unserved fallback, a duplicate tag, or an entry of the wrong
	// shape.
	CatalogInvalid = svci18n.CatalogInvalid
	// CatalogLoadFailed is returned when a catalogue directory cannot be read
	// or its bytes cannot be decoded. The cause stays in the chain, so
	// errors.Is(err, fs.ErrNotExist) still answers.
	CatalogLoadFailed = svci18n.CatalogLoadFailed
	// TranslationIncomplete is returned for a counted message missing a
	// category its language can produce — the one catalogue defect a
	// translator cannot see by reading their own file.
	TranslationIncomplete = svci18n.TranslationIncomplete
	// NegotiationEmpty is returned by [NewNegotiator] for an empty supported
	// set, which is an unfinished wiring rather than "accept anything".
	NegotiationEmpty = svci18n.NegotiationEmpty
)

// ParseTag canonicalises text into a [Tag], or returns [InvalidTag].
//
// "FR-latn-ca" and "fr-Latn-CA" are the same Tag. The POSIX spelling
// ("fr_FR"), extensions, variants, private use and grandfathered tags are each
// refused by name rather than parsed and dropped — a dropped subtag changes
// which language answers without changing anything a reader can see.
func ParseTag(text string) (tag Tag, err error) {
	//: delegate to the core value constructor.
	return corei18n.ParseTag(text)
}

// ParseForm returns the [Form] named by a CLDR category spelling — "zero",
// "one", "two", "few", "many", "other" — or [InvalidForm].
func ParseForm(name string) (form Form, err error) {
	//: delegate to the core value constructor.
	return corei18n.ParseForm(name)
}

// Int returns the [Count] of an exact integer displayed with no fraction
// digits. The sign is discarded, because CLDR's operands are defined on the
// absolute value.
func Int(n int64) Count {
	//: delegate to the core value constructor.
	return corei18n.Int(n)
}

// Decimal returns the [Count] of value displayed with exactly fractionDigits
// fraction digits, or [InvalidCount].
//
// The second argument is the DISPLAY precision, not a property of the value:
// Decimal(1, 2) describes "1.00", which is `other` in English, while
// [Int](1) describes "1", which is `one`.
func Decimal(value float64, fractionDigits int) (count Count, err error) {
	//: delegate to the core value constructor.
	return corei18n.Decimal(value, fractionDigits)
}

// ValidateKey reports whether key is usable, and returns [InvalidKey] when it
// is not: empty, or carrying a control character.
func ValidateKey(key Key) error {
	//: delegate to the core validator.
	return corei18n.ValidateKey(key)
}

// NewMessage compiles an uncounted message, or returns [InvalidPattern].
func NewMessage(text string) (message Message, err error) {
	//: delegate to the core value constructor.
	return corei18n.NewMessage(text)
}

// NewPluralMessage compiles a counted message from one pattern per category,
// or returns [InvalidPattern] or [PluralFormMissing]. The map MUST contain
// [FormOther].
//
// It does not check the map against any language's rules — it does not know
// the language. [NewStore] does, and refuses there.
func NewPluralMessage(forms map[Form]string) (message Message, err error) {
	//: delegate to the core value constructor.
	return corei18n.NewPluralMessage(forms)
}

// Plain returns an uncounted [Entry].
func Plain(text string) Entry {
	//: delegate to the service constructor.
	return svci18n.Plain(text)
}

// PluralForms returns a counted [Entry] whose patterns are keyed by CLDR
// category name. A counted entry is checked at load against every category its
// language can produce.
func PluralForms(forms map[string]string) Entry {
	//: delegate to the service constructor.
	return svci18n.PluralForms(forms)
}

// NewStore compiles catalogues into a [Store], or refuses.
//
// Every check is here, and every failure is a startup failure:
// [UnsupportedLanguage] for a language with no reviewed rule,
// [TranslationIncomplete] for a counted message missing one of its language's
// categories, [CatalogInvalid] for an unset or unserved fallback, and the core
// refusals for a bad key, pattern or category name.
func NewStore(fallback Tag, catalogues map[Tag]Catalogue) (store *Store, err error) {
	//: delegate to the service constructor.
	return svci18n.NewStore(fallback, catalogues)
}

// LoadFS reads one catalogue file per language from dir and returns a [Store].
//
// The bytes are decoded by the codec domain, so a catalogue is JSON, YAML,
// TOML or any other registered format and this package contains no parser —
// blank-import pkg/v1/codec to register one. fsys is io/fs.FS, so an embed.FS,
// an os.DirFS and a pkg/v1/vfs filesystem all work here unchanged.
//
// The language comes from the file NAME: "en.json", "pt-PT.yaml".
// Subdirectories are ignored rather than walked, and two files that
// canonicalise to one tag are [CatalogInvalid].
func LoadFS(fsys fs.FS, dir string, format codec.Format, fallback Tag) (store *Store, err error) {
	//: delegate to the service loader.
	return svci18n.LoadFS(fsys, dir, format, fallback)
}

// NewPrinter returns a [Printer] rendering tag out of catalog, or refuses.
//
// It resolves the chain and allocates; [Printer.Render] walks it and does not.
// Build one per language at startup and index them by [Tag] — a request then
// costs a map lookup, not a construction.
func NewPrinter(catalog Catalog, tag Tag) (printer *Printer, err error) {
	//: delegate to the service constructor.
	return svci18n.NewPrinter(catalog, tag)
}

// NewNegotiator returns a [Negotiator] over supported, falling back to
// fallback, or refuses: an empty supported set is [NegotiationEmpty], an unset
// or unsupported fallback is [CatalogInvalid].
func NewNegotiator(supported []Tag, fallback Tag) (negotiator *Negotiator, err error) {
	//: delegate to the service constructor.
	return svci18n.NewNegotiator(supported, fallback)
}

// Rules returns the CLDR plural specification for tag, and reports whether the
// SDK has one. It resolves the exact tag first and the bare language second,
// so "pt-PT" gets its own rule and "fr-CA" inherits French's.
func Rules(tag Tag) (plural Plural, ok bool) {
	//: delegate to the service table.
	return svci18n.Rules(tag)
}

// SupportedTags returns every language this SDK has a reviewed CLDR plural
// rule for, sorted. It is what [UnsupportedLanguage] names beside a refused
// tag.
func SupportedTags() []Tag {
	//: delegate to the service table.
	return svci18n.SupportedTags()
}
