// Package kit — the sentences kit says, in every language it speaks.
package kit

import (
	"embed"
	"fmt"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/pkg/v1/app/i18n"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's words. Every diagnostic kit gives — a declaration it refuses, a
// setting it cannot read, a loop that stopped — is a sentence of its
// catalogues, locales/en.json and locales/fr.json, read by the SDK's i18n:
// in English for the terminal, the logs and an error, and in every language
// kit speaks for the Studio, which shows its reader's. A catalogue that
// fails to compile, or a key one language lacks, stops the process at the
// first sentence kit says — never at the package's load, which every run of
// a product pays, a status line's included: TestKitSpeaksEveryLanguage.

var (
	//go:embed locales/*.json
	localeFiles embed.FS

	// kitLanguages are the languages kit speaks, English first: its terminal's.
	kitLanguages = []string{"en", "fr"}

	// printers render kit's sentences, one per language, loaded at the
	// first sentence.
	printers = sync.OnceValue(func() map[string]*i18n.Printer {
		p, err := loadPrinters()
		if err != nil {
			panic("kit: its catalogues: " + err.Error())
		}
		return p
	})
)

// loadPrinters reads kit's sentences, one printer per language it speaks.
func loadPrinters() (map[string]*i18n.Printer, error) {
	english, err := i18n.ParseTag(kitLanguages[0])
	if err != nil {
		return nil, err
	}
	store, err := i18n.LoadFS(localeFiles, "locales", "json", english)
	if err != nil {
		return nil, err
	}
	englishKeys := map[i18n.Key]bool{}
	for _, k := range store.Keys(english) {
		englishKeys[k] = true
	}
	out := make(map[string]*i18n.Printer, len(kitLanguages))
	for _, lang := range kitLanguages {
		tag, err := i18n.ParseTag(lang)
		if err != nil {
			return nil, err
		}
		keys := store.Keys(tag)
		if keys == nil {
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a language has no catalogue", nil, errs.String("language", lang))
		}
		if missing := store.Missing(tag); len(missing) > 0 {
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a catalogue lacks keys: the framework says everything in every language", nil, errs.String("language", lang), errs.String("missing", fmt.Sprint(missing)))
		}
		if extra := slices.DeleteFunc(slices.Clone(keys), func(k i18n.Key) bool { return englishKeys[k] }); len(extra) > 0 {
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a catalogue has keys the English one lacks", nil, errs.String("language", lang), errs.String("extra", fmt.Sprint(extra)))
		}
		if out[lang], err = i18n.NewPrinter(store, tag); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// phrase is one of kit's sentences, in every language it speaks.
type phrase struct {
	// texts are by language; texts["en"] is what the terminal says.
	texts map[string]string
}

// String is the English sentence.
func (p phrase) String() string { return p.texts["en"] }

// in is the sentence in lang, English when kit does not speak it.
func (p phrase) in(lang string) string {
	if s, ok := p.texts[lang]; ok {
		return s
	}
	return p.String()
}

// empty reports whether p says nothing: the zero phrase.
func (p phrase) empty() bool { return len(p.texts) == 0 }

// say renders the catalogues' key with args, given as name, value pairs. A
// value that is itself a phrase is written in the same language as the
// sentence around it; any other is written as fmt.Sprint writes it.
func say(key string, args ...any) phrase {
	p := phrase{texts: make(map[string]string, len(kitLanguages))}
	a := make(i18n.Args, len(args)/2)
	for _, lang := range kitLanguages {
		for i := 0; i+1 < len(args); i += 2 {
			name := fmt.Sprint(args[i])
			switch v := args[i+1].(type) {
			case phrase:
				a[name] = v.in(lang)
			default:
				a[name] = fmt.Sprint(v)
			}
		}
		text, err := printers()[lang].Render(i18n.Key(key), a)
		if err != nil {
			// A key or an argument a call gives wrong: said, not hidden.
			text = fmt.Sprintf("%s %v", key, a)
		}
		p.texts[lang] = text
	}
	return p
}

// listOf joins phrases the way each language lists: "a, b and c".
func listOf(items []phrase) phrase {
	switch len(items) {
	case 0:
		return phrase{}
	case 1:
		return items[0]
	}
	out := say("list.and", "first", items[len(items)-2], "rest", items[len(items)-1])
	for i := len(items) - 3; i >= 0; i-- {
		out = say("list.comma", "first", items[i], "rest", out)
	}
	return out
}

// plain is a text kit does not translate — a file name, an identifier — as
// a phrase, the same in every language.
func plain(text string) phrase {
	p := phrase{texts: make(map[string]string, len(kitLanguages))}
	for _, lang := range kitLanguages {
		p.texts[lang] = text
	}
	return p
}
