package app

import (
	_ "embed" // locales/en.json, kit's English table
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's words. Every diagnostic kit gives — a declaration it refuses, a
// setting it cannot read, a loop that stopped — is a sentence of its
// catalogues. English is kit's own table, read from locales/en.json at the
// first sentence kit says: the terminal, the logs and an error read it,
// whatever a program imports. The languages after English are for the
// Studio, which shows its reader's: their catalogues and the SDK's i18n that
// compiles them are framework/app/studio's (plug.Words, ADR 0179 step 8,
// Q6), opened at the first sentence kit says once that package is imported.
// A catalogue that fails to compile, or a key a language lacks, stops the
// process at the first sentence kit says — never at the package's load,
// which every run of a product pays, a status line's included:
// TestKitSpeaksEveryLanguage.

var (
	//go:embed locales/en.json
	englishFile []byte

	// english is kit's English table, read at the first sentence.
	english = sync.OnceValue(func() *englishTable {
		t, err := loadEnglish()
		if err != nil {
			panic("kit: its catalogues: " + err.Error())
		}
		return t
	})

	// others are kit's catalogues after English, opened from the hook
	// framework/app/studio set; nil before the first sentence said with it.
	others atomic.Pointer[otherWords]

	// othersOpening holds a second opening of the catalogues after English
	// while one runs.
	othersOpening sync.Mutex

	// unsaid is what the zero phrase says: nothing, in every language.
	unsaid saying
)

// englishTable is kit's English: every sentence by key, as written and
// compiled.
type englishTable struct {
	// texts are the sentences as locales/en.json writes them.
	texts map[string]string
	// sentences are the same, compiled.
	sentences map[string]sentence
}

// sentence is one compiled sentence: literal text and placeholders, in
// order.
type sentence []sentencePart

// sentencePart is a run of literal text, or a placeholder when name is set.
type sentencePart struct {
	// text is the literal run.
	text string
	// name is the placeholder's, "" for a literal run.
	name string
}

// otherWords are kit's catalogues after English, opened from one value of
// the hook.
type otherWords struct {
	// from is the hook's value they were opened from.
	from *plug.WordsConfig
	// tr renders their sentences.
	tr plug.Translator
	// langs are the languages they speak, in the Studio's order.
	langs []string
}

// loadEnglish reads kit's English table and compiles every sentence of it.
func loadEnglish() (*englishTable, error) {
	var texts map[string]string
	if err := json.Unmarshal(englishFile, &texts); err != nil {
		return nil, failure(CodeCatalogue, "CATALOGUE_INVALID", "kit's English catalogue does not decode", err)
	}
	t := &englishTable{texts: texts, sentences: make(map[string]sentence, len(texts))}
	for key, text := range texts {
		s, problem := compileSentence(text)
		if problem != "" {
			return nil, failure(CodeCatalogue, "CATALOGUE_INVALID", "a sentence of kit's English catalogue does not compile", nil, errs.String("key", key), errs.String("detail", problem))
		}
		t.sentences[key] = s
	}
	return t, nil
}

// compileSentence reads text in the grammar of the SDK's i18n: literal text
// with named placeholders, {name}, a literal brace written {{ or }}. It
// returns what is wrong with text, "" when nothing is.
func compileSentence(text string) (sentence, string) {
	c := sentenceCompiler{text: text}
	for c.pos < len(text) {
		if !c.step() {
			return nil, c.problem
		}
	}
	c.flush()
	return c.out, c.problem
}

// sentenceCompiler reads one sentence, brace after brace.
type sentenceCompiler struct {
	// text is the sentence as written, pos how far it is read.
	text string
	pos  int
	// out are the parts read, lit the literal run not yet in out.
	out sentence
	lit strings.Builder
	// problem says what is wrong with text, once a step found it.
	problem string
}

// step reads the literal run up to the next brace and the construct the
// brace opens; false when it does not compile, problem saying why.
func (c *sentenceCompiler) step() bool {
	next := strings.IndexAny(c.text[c.pos:], "{}")
	if next < 0 {
		c.lit.WriteString(c.text[c.pos:])
		c.pos = len(c.text)
		return true
	}
	c.lit.WriteString(c.text[c.pos : c.pos+next])
	c.pos += next
	switch {
	case c.pos+1 < len(c.text) && c.text[c.pos+1] == c.text[c.pos]:
		// A doubled brace is the brace itself.
		c.lit.WriteByte(c.text[c.pos])
		c.pos += 2
		return true
	case c.text[c.pos] == '}':
		c.problem = fmt.Sprintf("unmatched closing brace at offset %d", c.pos)
		return false
	}
	return c.placeholder()
}

// placeholder reads the {name} at pos; false when it does not compile.
func (c *sentenceCompiler) placeholder() bool {
	end := strings.IndexByte(c.text[c.pos+1:], '}')
	if end < 0 {
		c.problem = fmt.Sprintf("unclosed placeholder at offset %d", c.pos)
		return false
	}
	name := c.text[c.pos+1 : c.pos+1+end]
	if !placeholderName(name) {
		c.problem = fmt.Sprintf("placeholder name %q is empty or outside [A-Za-z_][A-Za-z0-9_]*", name)
		return false
	}
	c.flush()
	c.out = append(c.out, sentencePart{name: name})
	c.pos += end + 2
	return true
}

// flush moves the literal run into the parts.
func (c *sentenceCompiler) flush() {
	if c.lit.Len() > 0 {
		c.out = append(c.out, sentencePart{text: c.lit.String()})
		c.lit.Reset()
	}
}

// placeholderName reports whether name matches [A-Za-z_][A-Za-z0-9_]*, the
// placeholder names of the SDK's i18n.
func placeholderName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// render is the English sentence of key with args; false for a key the
// table lacks or a placeholder args does not give.
func (t *englishTable) render(key string, args map[string]string) (string, bool) {
	s, ok := t.sentences[key]
	if !ok {
		return "", false
	}
	if len(s) == 1 && s[0].name == "" {
		return s[0].text, true
	}
	var b strings.Builder
	for _, part := range s {
		if part.name == "" {
			b.WriteString(part.text)
			continue
		}
		v, given := args[part.name]
		if !given {
			return "", false
		}
		b.WriteString(v)
	}
	return b.String(), true
}

// translator is kit's catalogues after English, opened once from the hook
// framework/app/studio set; nil when nothing set it, and kit speaks English
// alone. It is read at each sentence, not once: a sentence said before the
// Studio's package is initialised — a product's package-level declaration
// whose init Go runs first — must not keep kit English for the process.
func translator() *otherWords {
	cfg := plug.Words.Load()
	if cfg == nil {
		return nil
	}
	if o := others.Load(); o != nil && o.from == cfg {
		return o
	}
	othersOpening.Lock()
	defer othersOpening.Unlock()
	if o := others.Load(); o != nil && o.from == cfg {
		return o
	}
	o, err := openOthers(cfg)
	if err != nil {
		panic("kit: its catalogues: " + err.Error())
	}
	others.Store(o)
	return o
}

// openOthers opens the catalogues after English from cfg, against kit's
// English table: every language holds every key English holds, and no other.
func openOthers(cfg *plug.WordsConfig) (*otherWords, error) {
	en := english()
	tr, err := cfg.Open(maps.Clone(en.texts))
	if err != nil {
		return nil, err
	}
	o := &otherWords{from: cfg, tr: tr, langs: tr.Languages()}
	for _, lang := range o.langs {
		keys := tr.Keys(lang)
		if keys == nil {
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a language has no catalogue", nil, errs.String("language", lang))
		}
		var missing []string
		for key := range en.texts {
			if !slices.Contains(keys, key) {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a catalogue lacks keys: the framework says everything in every language", nil, errs.String("language", lang), errs.String("missing", fmt.Sprint(missing)))
		}
		if extra := slices.DeleteFunc(slices.Clone(keys), func(k string) bool { _, ok := en.texts[k]; return ok }); len(extra) > 0 {
			return nil, failure(CodeCatalogue, "CATALOGUE_INCOMPLETE", "a catalogue has keys the English one lacks", nil, errs.String("language", lang), errs.String("extra", fmt.Sprint(extra)))
		}
	}
	return o, nil
}

// kitLanguages are the languages kit speaks, English first — its
// terminal's —, then those of the catalogues the Studio plugged in.
func kitLanguages() []string {
	langs := []string{"en"}
	if o := translator(); o != nil {
		langs = append(langs, o.langs...)
	}
	return langs
}

// phrase is one of kit's sentences: in English, written when it is said,
// and in the languages after English, written when the Studio asks. It is
// one pointer, as small as the map of texts it replaced, so that a phrase
// passed and returned costs its callers no more code.
type phrase struct {
	// said is the sentence; nil for the zero phrase alone.
	said *saying
}

// saying is what a phrase said: its English, and what writes it in another
// language.
type saying struct {
	// english is what the terminal, the logs and an error say.
	english string
	// key is the catalogues' key the sentence was said with; "" for a text
	// kit does not translate, the same in every language.
	key string
	// args are the sentence's values by name, each written when it was
	// said: a string, or a phrase written in the language of the sentence
	// around it.
	args map[string]any
}

// String is the English sentence.
func (p phrase) String() string {
	if p.said == nil {
		return ""
	}
	return p.said.english
}

// in is the sentence in lang, English when kit does not speak it.
func (p phrase) in(lang string) string { return p.said.in(lang) }

// in is the sentence s said in lang, English when kit does not speak it;
// nothing for the zero phrase's, which is nil.
func (s *saying) in(lang string) string {
	if s == nil {
		s = &unsaid
	}
	if s.key == "" || lang == "en" {
		return s.english
	}
	o := translator()
	if o == nil || !slices.Contains(o.langs, lang) {
		return s.english
	}
	a := s.argsIn(lang)
	text, err := o.tr.Render(lang, s.key, a)
	if err != nil {
		// A key or an argument a call gives wrong: said, not hidden.
		return fmt.Sprintf("%s %v", s.key, a)
	}
	return text
}

// argsIn are s's values written in lang: a phrase in lang, a string as it
// was written.
func (s *saying) argsIn(lang string) map[string]string {
	a := make(map[string]string, len(s.args))
	for name, v := range s.args {
		switch v := v.(type) {
		case phrase:
			a[name] = v.in(lang)
		case string:
			a[name] = v
		}
	}
	return a
}

// spoken is the sentence in English, and in every language kit speaks by
// language: what a diagnostic carries, the second for the Studio; "" and nil
// for the zero phrase.
func (p phrase) spoken() (english string, texts map[string]string) {
	if p.said == nil {
		return "", nil
	}
	langs := kitLanguages()
	texts = make(map[string]string, len(langs))
	for _, lang := range langs {
		texts[lang] = p.said.in(lang)
	}
	return p.said.english, texts
}

// empty reports whether p says nothing: the zero phrase.
func (p phrase) empty() bool { return p.said == nil }

// say renders the catalogues' key with args, given as name, value pairs. A
// value that is itself a phrase is written in the same language as the
// sentence around it; any other is written as fmt.Sprint writes it, when it
// is said.
func say(key string, args ...any) phrase {
	s := &saying{key: key, args: make(map[string]any, len(args)/2)}
	for i := 0; i+1 < len(args); i += 2 {
		name := fmt.Sprint(args[i])
		switch v := args[i+1].(type) {
		case phrase:
			s.args[name] = v
		default:
			s.args[name] = fmt.Sprint(v)
		}
	}
	a := s.argsIn("en")
	text, ok := english().render(key, a)
	if !ok {
		// A key or an argument a call gives wrong: said, not hidden.
		text = fmt.Sprintf("%s %v", key, a)
	}
	s.english = text
	// The catalogues after English open at the first sentence once the
	// Studio is imported: one that fails stops the process here, as English
	// would.
	translator()
	return phrase{said: s}
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
	return phrase{said: &saying{english: text}}
}
