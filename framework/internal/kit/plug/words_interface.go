package plug

// kit's catalogues after English (ADR 0179 §3, step 8), which
// framework/internal/kit/studiokit implements over the SDK's i18n.

// Translator says kit's sentences in the languages after English, which
// only the Studio shows, in its reader's language.
type Translator interface {
	// Languages are the languages it speaks after English, in order.
	Languages() []string
	// Keys are the keys of lang's catalogue, sorted; nil for a language
	// it does not speak.
	Keys(lang string) []string
	// Render is the sentence of key in lang, its placeholders filled from
	// args by name; a key the catalogue lacks, or a name the sentence
	// takes that args does not give, is an error.
	Render(lang, key string, args map[string]string) (string, error)
}
