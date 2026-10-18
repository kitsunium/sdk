package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/i18n"
)

// Every sentence kit says exists in every language, every sentence of the
// catalogues is said somewhere, and every call names exactly the values its
// sentence has in both languages: the keys the code names — say,
// valueProblem, a service's problem and warn, resolveSettings' problem — and
// the argument names after them are the catalogues'. A name the sentence
// lacks, or one it has that the call does not give, would ship the fallback
// text, which no test of the English alone would see.
//
// English is kit's own table (locales/en.json); the languages after it are
// framework/app/studio's (studioengine/locales), which the suite's binary
// imports (subsystems_test.go), as a product shown in the Studio does.
func TestKitSpeaksEveryLanguage(t *testing.T) {
	if _, err := loadEnglish(); err != nil {
		t.Fatal(err)
	}
	cfg := plug.Words.Load()
	if cfg == nil {
		t.Fatal("no catalogue after English is plugged in: the suite imports framework/app/studio")
	}
	if _, err := openOthers(cfg); err != nil {
		t.Fatal(err)
	}
	langs := kitLanguages()
	if len(langs) < 2 || langs[0] != "en" {
		t.Fatalf("the framework speaks %v: English first, then the Studio's languages", langs)
	}
	catalogues := map[string]map[string]string{}
	for _, lang := range langs {
		file := filepath.Join("studioengine", "locales", lang+".json")
		if lang == "en" {
			file = filepath.Join("locales", "en.json")
		}
		raw, rawErr := os.ReadFile(file)
		if rawErr != nil {
			t.Fatal(rawErr)
		}
		var c map[string]string
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		catalogues[lang] = c
	}
	files, filesErr := filepath.Glob("*.go")
	if filesErr != nil {
		t.Fatal(filesErr)
	}
	keyAt := map[string]int{"say": 0, "valueProblem": 0, "problem": 2, "warn": 2}
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, fErr := parser.ParseFile(fset, name, nil, 0)
		if fErr != nil {
			t.Fatal(fErr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch x := call.Fun.(type) {
			case *ast.Ident:
				fn = x.Name
			case *ast.SelectorExpr:
				fn = x.Sel.Name
			}
			at, ok := keyAt[fn]
			if !ok || len(call.Args) <= at {
				return true
			}
			lit, ok := call.Args[at].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true // a helper forwarding its own key
			}
			key, keyErr := strconv.Unquote(lit.Value)
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			where := fset.Position(lit.Pos()).String()
			used[key] = true
			given := map[string]bool{}
			rest := call.Args[at+1:]
			if len(rest)%2 != 0 {
				t.Errorf("%s: %q gives %d values after its key: name, value pairs", where, key, len(rest))
			}
			for i := 0; i < len(rest); i += 2 {
				nl, ok := rest[i].(*ast.BasicLit)
				if !ok || nl.Kind != token.STRING {
					t.Errorf("%s: %q names a value with something other than a literal", where, key)
					continue
				}
				arg, argErr := strconv.Unquote(nl.Value)
				if argErr != nil {
					t.Fatal(argErr)
				}
				given[arg] = true
			}
			for _, lang := range langs {
				text, ok := catalogues[lang][key]
				if !ok {
					t.Errorf("%s says %q, which the %s catalogue lacks", where, key, lang)
					continue
				}
				wants := placeholdersOf(text)
				for p := range wants {
					if !given[p] {
						t.Errorf("%s: %q in %s names {%s}, which the call does not give", where, key, lang, p)
					}
				}
				for g := range given {
					if !wants[g] {
						t.Errorf("%s: %q gives %q, which its %s sentence does not name", where, key, g, lang)
					}
				}
			}
			return true
		})
	}
	for key := range catalogues["en"] {
		if !used[key] && !slices.Contains([]string{"list.and", "list.comma"}, key) {
			t.Errorf("the catalogues hold %q, which the framework never says", key)
		}
	}
}

// placeholdersOf are the names a sentence of the SDK's i18n grammar takes:
// {name}, with {{ and }} for literal braces.
func placeholdersOf(text string) map[string]bool {
	out := map[string]bool{}
	for rest := text; rest != ""; {
		switch {
		case strings.HasPrefix(rest, "{{"), strings.HasPrefix(rest, "}}"):
			rest = rest[2:]
		case rest[0] == '{':
			name, after, closed := strings.Cut(rest[1:], "}")
			if !closed {
				return out
			}
			out[name] = true
			rest = after
		default:
			rest = rest[1:]
		}
	}
	return out
}

// A phrase is written in each language with its arguments, a phrase
// argument in the same language, and a list the way the language lists.
func TestAPhraseSpeaksEachLanguage(t *testing.T) {
	p := say("setting.positive", "key", "ttl", "waiters", listOf([]phrase{
		say("setting.waiter", "event", "expire", "workflow", "docs/workflow/life"),
		say("setting.waiter", "event", "purge", "workflow", "docs/workflow/bin"),
	}))
	if got := p.String(); got != `setting "ttl" must be a positive duration, for the timer expire of workflow docs/workflow/life and the timer purge of workflow docs/workflow/bin` {
		t.Errorf("en: %s", got)
	}
	if got := p.in("fr"); got != "le réglage « ttl » doit être une durée positive, pour le minuteur expire du workflow docs/workflow/life et le minuteur purge du workflow docs/workflow/bin" {
		t.Errorf("fr: %s", got)
	}
	if got := listOf([]phrase{plain("a"), plain("b"), plain("c")}).in("fr"); got != "a, b et c" {
		t.Errorf("a list of three: %s", got)
	}
	if got := p.in("de"); got != p.String() {
		t.Errorf("a language the framework does not speak reads English: %s", got)
	}
}

// kit's English table says every sentence byte for byte as the SDK's i18n
// said it before ADR 0179's step 8 took it out of the core: the same text for
// every key with every placeholder given — values holding braces written as
// they are —, a refusal for a placeholder left out and for a key the table
// lacks, where say writes the same fallback, and the same verdict on a
// sentence that does not compile.
func TestKitsEnglishIsTheSDKsEnglish(t *testing.T) {
	table, err := loadEnglish()
	if err != nil {
		t.Fatal(err)
	}
	en, err := i18n.ParseTag("en")
	if err != nil {
		t.Fatal(err)
	}
	catalogue := i18n.Catalogue{}
	for key, text := range table.texts {
		catalogue[i18n.Key(key)] = i18n.Plain(text)
	}
	store, err := i18n.NewStore(en, map[i18n.Tag]i18n.Catalogue{en: catalogue})
	if err != nil {
		t.Fatal(err)
	}
	printer, err := i18n.NewPrinter(store, en)
	if err != nil {
		t.Fatal(err)
	}
	for key, text := range table.texts {
		names := placeholdersOf(text)
		args := map[string]string{}
		for name := range names {
			args[name] = "‹" + name + "› {x} }} é"
		}
		got, ok := table.render(key, args)
		want, wantErr := printer.Render(i18n.Key(key), i18n.Args(args))
		if !ok || wantErr != nil || got != want {
			t.Errorf("%q: the framework says %q (%v), the SDK's i18n %q (%v)", key, got, ok, want, wantErr)
		}
		for name := range names {
			short := maps.Clone(args)
			delete(short, name)
			if _, ok := table.render(key, short); ok {
				t.Errorf("%q: the framework renders it without {%s}", key, name)
			}
			if _, err := printer.Render(i18n.Key(key), i18n.Args(short)); err == nil {
				t.Errorf("%q: the SDK's i18n renders it without {%s}", key, name)
			}
		}
	}
	if _, ok := table.render("no.such.sentence", nil); ok {
		t.Error("the framework renders a key its table lacks")
	}
	if got, want := say("no.such.sentence", "a", 1).String(), "no.such.sentence map[a:1]"; got != want {
		t.Errorf("a key the table lacks is said %q, want %q", got, want)
	}
	for _, text := range []string{"", "plain", "a } b", "a { b", "{1x}", "{}", "{a b}", "{{ {a} }}", "{{{a}}}", "}}{{", "{_a1}", "{a}{b}", "x {é}"} {
		_, problem := compileSentence(text)
		_, err := i18n.NewMessage(text)
		if (problem == "") != (err == nil) {
			t.Errorf("%q: the framework's verdict %q, the SDK's i18n %v", text, problem, err)
		}
	}
}

// Without framework/app/studio kit speaks English alone: a diagnostic
// carries its English and nothing else, and a language after English reads
// English. A phrase said before the Studio's package plugged its catalogues
// in — a product's package-level declaration whose init Go runs first —
// speaks them once it has.
func TestKitSpeaksEnglishAloneWithoutTheStudio(t *testing.T) {
	saved := plug.Words.Load()
	t.Cleanup(func() { plug.Words.Store(saved) })
	plug.Words.Store(nil)
	p := say("setting.positive", "key", "ttl", "waiters", say("setting.waiter", "event", "expire", "workflow", "docs/workflow/life"))
	english := `setting "ttl" must be a positive duration, for the timer expire of workflow docs/workflow/life`
	if got := p.String(); got != english {
		t.Errorf("en: %s", got)
	}
	if got := kitLanguages(); !slices.Equal(got, []string{"en"}) {
		t.Errorf("the framework speaks %v without the Studio", got)
	}
	if msg, got := p.spoken(); msg != english || !maps.Equal(got, map[string]string{"en": english}) {
		t.Errorf("a diagnostic carries %v without the Studio", got)
	}
	if got := p.in("fr"); got != english {
		t.Errorf("fr without the Studio: %s", got)
	}
	plug.Words.Store(saved)
	if got := p.in("fr"); got != "le réglage «\u00a0ttl\u00a0» doit être une durée positive, pour le minuteur expire du workflow docs/workflow/life" {
		t.Errorf("fr once the Studio plugged its catalogues in: %s", got)
	}
}

// A catalogue after English that lacks a key English has stops the process
// at the first sentence kit says, as one that does not compile does.
func TestACatalogueThatLacksAKeyStopsTheFirstSentence(t *testing.T) {
	saved := plug.Words.Load()
	t.Cleanup(func() { plug.Words.Store(saved) })
	plug.Words.Store(&plug.WordsConfig{Open: func(english map[string]string) (plug.Translator, error) {
		delete(english, "list.and")
		return lackingTranslator{english}, nil
	}})
	defer func() {
		r := recover()
		if s, ok := r.(string); !ok || !strings.Contains(s, "CATALOGUE_INCOMPLETE") {
			t.Errorf("the first sentence with a catalogue lacking a key: %v", r)
		}
	}()
	say("list.and", "first", "a", "rest", "b")
}

// lackingTranslator speaks one language, xx, whose catalogue is the English
// it was given less what the test took out.
type lackingTranslator struct {
	// sentences are its catalogue.
	sentences map[string]string
}

// Languages are xx.
func (lackingTranslator) Languages() []string { return []string{"xx"} }

// Keys are its catalogue's.
func (l lackingTranslator) Keys(string) []string { return slices.Sorted(maps.Keys(l.sentences)) }

// Render is the sentence as written.
func (l lackingTranslator) Render(_, key string, _ map[string]string) (string, error) {
	return l.sentences[key], nil
}
