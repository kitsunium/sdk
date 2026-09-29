package kit

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Every sentence kit says exists in every language, every sentence of the
// catalogues is said somewhere, and every call names exactly the values its
// sentence has in both languages: the keys the code names — say,
// valueProblem, a service's problem and warn, resolveSettings' problem — and
// the argument names after them are the catalogues'. A name the sentence
// lacks, or one it has that the call does not give, would ship the fallback
// text, which no test of the English alone would see.
func TestKitSpeaksEveryLanguage(t *testing.T) {
	if _, err := loadPrinters(); err != nil {
		t.Fatal(err)
	}
	catalogues := map[string]map[string]string{}
	for _, lang := range kitLanguages {
		raw, rawErr := os.ReadFile(filepath.Join("locales", lang+".json"))
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
			for _, lang := range kitLanguages {
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
			t.Errorf("the catalogues hold %q, which kit never says", key)
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
		t.Errorf("a language kit does not speak reads English: %s", got)
	}
}
