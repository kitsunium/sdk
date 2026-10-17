package studiokit

import (
	"embed"
	"encoding/json"
	"slices"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/i18n"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's sentences in the languages after English (ADR 0179 step 8, Q6). The
// Studio is their only reader — it shows a diagnostic in its reader's
// language —, so their catalogues and the SDK's i18n that compiles them are
// here: kit's core holds English alone, and a product that does not import
// framework/kit/studio links none of this. The catalogues are opened once,
// at the first sentence kit says once the hook is set, against kit's English
// table, the language each falls back to.

var (
	//go:embed locales/*.json
	localeFiles embed.FS

	// languages are the languages kit speaks after English, in the order the
	// Studio lists them: one catalogue each, locales/<language>.json.
	languages = []string{"fr"}

	// words is what Enable plugs into kit.
	words = plug.WordsConfig{Open: openWords}
)

// translator says kit's sentences in the languages after English: one
// printer each over one store, whose fallback is kit's English.
type translator struct {
	// store holds every catalogue, English the fallback.
	store *i18n.Store
	// tags are the languages' tags, by the name kit gives them.
	tags map[string]i18n.Tag
	// printers render one language each, by the name kit gives it.
	printers map[string]*i18n.Printer
}

// openWords compiles the catalogues of languages against english, kit's own
// sentences by key: a catalogue that does not decode, a sentence that does
// not compile, refuse the whole.
func openWords(english map[string]string) (plug.Translator, error) {
	fallback, err := i18n.ParseTag("en")
	if err != nil {
		return nil, err
	}
	catalogues := map[i18n.Tag]i18n.Catalogue{fallback: catalogueOf(english)}
	tr := &translator{tags: make(map[string]i18n.Tag, len(languages)), printers: make(map[string]*i18n.Printer, len(languages))}
	for _, lang := range languages {
		tag, tagErr := i18n.ParseTag(lang)
		if tagErr != nil {
			return nil, tagErr
		}
		sentences, readErr := catalogueFile(lang)
		if readErr != nil {
			return nil, readErr
		}
		catalogues[tag] = catalogueOf(sentences)
		tr.tags[lang] = tag
	}
	if tr.store, err = i18n.NewStore(fallback, catalogues); err != nil {
		return nil, err
	}
	for _, lang := range languages {
		if tr.printers[lang], err = i18n.NewPrinter(tr.store, tr.tags[lang]); err != nil {
			return nil, err
		}
	}
	return tr, nil
}

// catalogueFile reads lang's catalogue: a JSON object of sentences by key.
func catalogueFile(lang string) (map[string]string, error) {
	raw, err := localeFiles.ReadFile("locales/" + lang + ".json")
	if err != nil {
		return nil, errs.Wrap(i18n.CatalogLoadFailed, errs.WrapParams{}, errs.String("language", lang), errs.String("detail", err.Error()))
	}
	var sentences map[string]string
	if err := json.Unmarshal(raw, &sentences); err != nil {
		return nil, errs.Wrap(i18n.CatalogLoadFailed, errs.WrapParams{}, errs.String("language", lang), errs.String("detail", err.Error()))
	}
	return sentences, nil
}

// catalogueOf is sentences as one language's catalogue, every one uncounted.
func catalogueOf(sentences map[string]string) i18n.Catalogue {
	c := make(i18n.Catalogue, len(sentences))
	for key, text := range sentences {
		c[i18n.Key(key)] = i18n.Plain(text)
	}
	return c
}

// Languages are the languages after English, in the Studio's order.
func (t *translator) Languages() []string { return slices.Clone(languages) }

// Keys are the keys of lang's catalogue, sorted; nil for a language kit does
// not speak after English.
func (t *translator) Keys(lang string) []string {
	tag, ok := t.tags[lang]
	if !ok {
		return nil
	}
	keys := t.store.Keys(tag)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	slices.Sort(out)
	return out
}

// Render is key's sentence in lang with args.
func (t *translator) Render(lang, key string, args map[string]string) (string, error) {
	p, ok := t.printers[lang]
	if !ok {
		return key, errs.Wrap(i18n.UnsupportedLanguage, errs.WrapParams{}, errs.String("language", lang))
	}
	return p.Render(i18n.Key(key), i18n.Args(args))
}
