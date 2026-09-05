// Package main — the search-index generator.
package main

import (
	"encoding/json"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// exitProbeEnv marks the re-executed half of the fatal-exit test.
const exitProbeEnv string = "GENINDEX_EXIT_PROBE"

// fixtureSource is a small package exercising every declaration kind the index
// has a row builder for.
const fixtureSource string = `package fixture

// Exported does a thing. And then says more.
func Exported(a int) error { return nil }

// Widget is a thing.
//
// Deprecated: use Gadget.
type Widget struct{}

// Do performs the thing.
func (w Widget) Do() {}

// Limit is the ceiling.
const Limit = 10

// A and B are declared together.
const (
	A = 1
	B = 2
)

// Registry is the live one.
var Registry = map[string]int{}

func unexported() {}
`

// parseFixture parses a one-file package written to a temporary directory and
// returns its documented form, so the row builders can be exercised against a
// real go/doc tree rather than a hand-built one.
func parseFixture(t *testing.T, src string) (*doc.Package, *token.FileSet) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("staging the fixture: %v", err)
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, keepGoFiles, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	for _, pkg := range pkgs {
		files := slices.Collect(maps.Values(pkg.Files))
		dp, derr := doc.NewFromFiles(fset, files, "example.com/fixture")
		if derr != nil {
			t.Fatalf("documenting the fixture: %v", derr)
		}
		return dp, fset
	}
	t.Fatal("the fixture produced no package")
	return nil, nil
}

// Test_synopsis pins godoc's "first sentence" rule, including the two ways it is
// easy to get wrong.
//
// A terminator only ends a sentence when whitespace or the end of the text
// follows it — otherwise "e.g." and "v1.2" would truncate a doc comment
// mid-word, and the search result would show half a sentence.
func Test_synopsis(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// doc is the comment text go/doc handed over.
		doc string
		// want is the synopsis the row must carry.
		want string
	}
	tests := []tc{
		{name: "no documentation", doc: "", want: ""},
		{name: "one sentence", doc: "Marshal encodes v.", want: "Marshal encodes v."},
		{
			name: "two sentences",
			doc:  "Marshal encodes v. It fails on a cycle.",
			want: "Marshal encodes v.",
		},
		{
			//: the terminator is followed by a newline, which still ends it.
			name: "a sentence ending at a line break",
			doc:  "Marshal encodes v.\nIt fails on a cycle.",
			want: "Marshal encodes v.",
		},
		{name: "no terminator at all", doc: "Marshal encodes v", want: "Marshal encodes v"},
		{name: "an exclamation", doc: "Careful! This panics.", want: "Careful!"},
		{name: "a question", doc: "Why? Because.", want: "Why?"},
		{
			//: a documented simplification. go/doc's own firstSentence skips an
			//: abbreviation by looking at what precedes the terminator; this is
			//: the portable equivalent, so "e.g." ends the synopsis early. The
			//: cost is a slightly short summary in a search result, which is why
			//: the simpler rule was kept rather than reimplementing go/doc's.
			name: "an abbreviation ends the synopsis early",
			doc:  "Marshal encodes v, e.g. a map. It fails on a cycle.",
			want: "Marshal encodes v, e.g.",
		},
		{
			name: "a version number mid-sentence",
			doc:  "Requires v1.2 or later. Older versions differ.",
			want: "Requires v1.2 or later.",
		},
		{name: "leading and trailing space", doc: "  Marshal encodes v.  ", want: "Marshal encodes v."},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := synopsis(c.doc); got != c.want {
			t.Fatalf("synopsis(%q) = %q, want %q", c.doc, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_isSentenceEnd pins the three terminators godoc recognises.
func Test_isSentenceEnd(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// c is the byte under test.
		c byte
		// want is whether it can close a sentence.
		want bool
	}
	tests := []tc{
		{name: "a full stop", c: '.', want: true},
		{name: "an exclamation", c: '!', want: true},
		{name: "a question mark", c: '?', want: true},
		{name: "a comma", c: ','},
		{name: "a colon", c: ':'},
		{name: "a letter", c: 'a'},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isSentenceEnd(c.c); got != c.want {
			t.Fatalf("isSentenceEnd(%q) = %v, want %v", c.c, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_isSpace pins what separates words in a doc comment. Getting it wrong
// makes "e.g." end a sentence, which truncates the synopsis mid-word.
func Test_isSpace(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// c is the byte under test.
		c byte
		// want is whether it separates words.
		want bool
	}
	tests := []tc{
		{name: "a space", c: ' ', want: true},
		{name: "a newline", c: '\n', want: true},
		{name: "a tab", c: '\t', want: true},
		{name: "a letter", c: 'g'},
		{name: "a full stop", c: '.'},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isSpace(c.c); got != c.want {
			t.Fatalf("isSpace(%q) = %v, want %v", c.c, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_isDeprecated pins that the marker has to START a paragraph.
//
// A doc comment that merely mentions the convention in prose — describing it,
// as this package's own does — must not mark a live symbol as dead, because the
// docs site strikes the row through.
func Test_isDeprecated(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// doc is the comment text.
		doc string
		// want is whether the identifier is marked obsolete.
		want bool
	}
	tests := []tc{
		{name: "no documentation"},
		{name: "an ordinary comment", doc: "Marshal encodes v."},
		{name: "the marker opens the doc", doc: "Deprecated: use Gadget.", want: true},
		{
			name: "the marker opens a later paragraph",
			doc:  "Widget is a thing.\n\nDeprecated: use Gadget.", want: true,
		},
		{
			//: prose about the convention is not a marker.
			name: "the word inside a sentence",
			doc:  "Marks the identifier Deprecated: when the doc says so.",
		},
		{name: "the word with no colon", doc: "Deprecated soon, but not yet."},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isDeprecated(c.doc); got != c.want {
			t.Fatalf("isDeprecated(%q) = %v, want %v", c.doc, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_lastPathSegment pins the package label, and in particular that the module
// ROOT has none.
//
// Without the second return the root's label would be the empty string, and
// every root symbol would read ".Name" — a qualified name that matches nothing a
// user would type.
func Test_lastPathSegment(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// rel is the module-relative package path.
		rel string
		// want is the label, and wantOK whether there is one at all.
		want   string
		wantOK bool
	}
	tests := []tc{
		{name: "a single segment", rel: "codec", want: "codec", wantOK: true},
		{name: "a nested package", rel: "logger/writer", want: "writer", wantOK: true},
		{name: "a deeply nested package", rel: "a/b/c/d", want: "d", wantOK: true},
		//: the module root, spelled either way.
		{name: "the root as a dot", rel: "."},
		{name: "the root as an empty string", rel: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := lastPathSegment(c.rel)

		if ok != c.wantOK {
			t.Fatalf("lastPathSegment(%q) reported ok = %v, want %v", c.rel, ok, c.wantOK)
		}
		if got != c.want {
			t.Fatalf("lastPathSegment(%q) = %q, want %q", c.rel, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packageURL pins the anchor base every symbol hangs off. A missing or
// doubled slash produces links that 404 on a static site, which nothing else in
// the pipeline checks.
func Test_packageURL(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// urlBase and packageShort are what the caller configured.
		urlBase      string
		packageShort string
		// want is the anchor base.
		want string
	}
	tests := []tc{
		{name: "a nested package", urlBase: "/v1/local", packageShort: "codec", want: "/v1/local/codec/"},
		{name: "a base with a trailing slash", urlBase: "/v1/local/", packageShort: "codec", want: "/v1/local/codec/"},
		{name: "the root as a dot", urlBase: "/v1/local", packageShort: ".", want: "/v1/local/"},
		{name: "the root as an empty string", urlBase: "/v1/local", packageShort: "", want: "/v1/local/"},
		{name: "a deeply nested package", urlBase: "/v1", packageShort: "a/b", want: "/v1/a/b/"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := packageURL(c.urlBase, c.packageShort)

		if got != c.want {
			t.Fatalf("packageURL(%q, %q) = %q, want %q", c.urlBase, c.packageShort, got, c.want)
		}
		//: the docs site serves package pages as directories, so a missing
		//: trailing slash makes every anchor on the page relative to the wrong one.
		if !strings.HasSuffix(got, "/") {
			t.Errorf("the anchor base %q does not end in a slash", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packageLabel pins the fallback for the module root, whose path carries no
// segment to take.
func Test_packageLabel(t *testing.T) {
	t.Parallel()
	dp, _ := parseFixture(t, fixtureSource)

	type tc struct {
		// name describes the case.
		name string
		// packageShort is the module-relative package path.
		packageShort string
		// want is the label every Qualified name is prefixed with.
		want string
	}
	tests := []tc{
		{name: "a nested package", packageShort: "codec", want: "codec"},
		//: the root falls back to the package's own name, so a root symbol reads
		//: "Name" rather than a leading-dot ".Name".
		{name: "the root as a dot", packageShort: ".", want: dp.Name},
		{name: "the root as an empty string", packageShort: "", want: dp.Name},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := packageLabel(dp, c.packageShort); got != c.want {
			t.Fatalf("packageLabel(%q) = %q, want %q", c.packageShort, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_renderDecl pins that a declaration is collapsed to ONE line.
//
// The index UI renders the signature inline, so a multi-line declaration —
// which is how any interface or a struct with fields is printed — would
// overflow the row horizontally.
func Test_renderDecl(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)

	type tc struct {
		// name describes the case.
		name string
		// pick selects the declaration under test.
		pick func() ast.Node
		// wantOK is whether a signature must be produced at all.
		wantOK bool
		// contains is a fragment the rendered signature must carry.
		contains string
	}
	tests := []tc{
		{
			name:   "a function",
			pick:   func() ast.Node { return dp.Funcs[0].Decl },
			wantOK: true, contains: "func Exported(a int) error",
		},
		{
			name:   "a type",
			pick:   func() ast.Node { return dp.Types[0].Decl },
			wantOK: true, contains: "Widget",
		},
		//: go/doc leaves Decl nil for a declaration it could not attach, and a
		//: fabricated signature would be worse than none.
		{name: "no declaration at all", pick: func() ast.Node { return nil }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := renderDecl(fset, c.pick())

		if ok != c.wantOK {
			t.Fatalf("renderDecl reported ok = %v, want %v", ok, c.wantOK)
		}
		if !c.wantOK {
			//: nothing to render means nothing is offered.
			if got != "" {
				t.Fatalf("renderDecl returned %q with ok = false", got)
			}
			return
		}
		//: one line, whatever the source looked like.
		if strings.Contains(got, "\n") {
			t.Fatalf("the signature spans several lines: %q", got)
		}
		if !strings.Contains(got, c.contains) {
			t.Fatalf("renderDecl = %q, want it to contain %q", got, c.contains)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_signatureOf pins that an unrenderable declaration yields an EMPTY
// signature rather than a fabricated one, so a row never claims a shape the
// source does not have.
func Test_signatureOf(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)

	type tc struct {
		// name describes the case.
		name string
		// pick selects the declaration under test.
		pick func() ast.Node
		// wantEmpty is whether the signature must be empty.
		wantEmpty bool
	}
	tests := []tc{
		{name: "a function", pick: func() ast.Node { return dp.Funcs[0].Decl }},
		{name: "no declaration at all", pick: func() ast.Node { return nil }, wantEmpty: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := signatureOf(fset, c.pick())

		if (got == "") != c.wantEmpty {
			t.Fatalf("signatureOf = %q, want empty = %v", got, c.wantEmpty)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_exampleNames pins that an identifier with NO examples carries no field at
// all — omitempty is what keeps the shipped JSON small, and it only fires on nil.
func Test_exampleNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// examples are what go/doc attached.
		examples []*doc.Example
		// want is the list of names the row must carry.
		want []string
	}
	tests := []tc{
		{name: "no examples at all"},
		{name: "one example", examples: []*doc.Example{{Name: ""}}, want: []string{""}},
		{
			name:     "several examples",
			examples: []*doc.Example{{Name: "Basic"}, {Name: "WithOptions"}},
			want:     []string{"Basic", "WithOptions"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := exampleNames(c.examples)

		//: nil rather than an empty slice, or omitempty keeps the field.
		if len(c.examples) == 0 {
			if got != nil {
				t.Fatalf("exampleNames(nil) = %v, want nil so omitempty drops the field", got)
			}
			return
		}
		if !slices.Equal(got, c.want) {
			t.Fatalf("exampleNames = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_sourceURL pins that a link is only built when it can be CORRECT.
//
// A link to a path outside the repository root would 404 on the forge, which is
// worse than no link: a reader clicking it learns nothing and assumes the docs
// are stale.
func Test_sourceURL(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)
	decl := dp.Funcs[0].Decl
	fixtureDir := filepath.Dir(fset.Position(decl.Pos()).Filename)

	type tc struct {
		// name describes the case.
		name string
		// prefix and repoRoot are what the caller configured.
		prefix   string
		repoRoot string
		// nilNode omits the declaration.
		nilNode bool
		// wantOK is whether a link must be built.
		wantOK bool
	}
	tests := []tc{
		{
			name:   "a declaration inside the repository",
			prefix: "https://example/blob/sha/", repoRoot: fixtureDir, wantOK: true,
		},
		{
			name:   "a prefix with no trailing slash",
			prefix: "https://example/blob/sha", repoRoot: fixtureDir, wantOK: true,
		},
		//: source links are opt-in.
		{name: "no prefix", repoRoot: fixtureDir},
		{name: "no repository root", prefix: "https://example/blob/sha/"},
		{name: "no declaration", prefix: "https://example/blob/sha/", repoRoot: fixtureDir, nilNode: true},
		{
			//: a path outside the root would 404 on the forge.
			name:   "a declaration outside the repository",
			prefix: "https://example/blob/sha/", repoRoot: filepath.Join(fixtureDir, "elsewhere"),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := &indexOptions{sourceURLPrefix: c.prefix, repoRoot: c.repoRoot}
		var node ast.Node = decl
		if c.nilNode {
			node = nil
		}

		got, ok := sourceURL(opts, fset, node)

		if ok != c.wantOK {
			t.Fatalf("sourceURL reported ok = %v, want %v (link %q)", ok, c.wantOK, got)
		}
		if !c.wantOK {
			//: no link at all rather than one that points nowhere.
			if got != "" {
				t.Fatalf("sourceURL returned %q with ok = false", got)
			}
			return
		}
		//: exactly one slash between the prefix and the path, whatever the
		//: caller's prefix looked like.
		if strings.Contains(strings.TrimPrefix(got, "https://"), "//") {
			t.Errorf("the link has a doubled slash: %q", got)
		}
		//: the line anchor is what makes the link point at the declaration
		//: rather than at the top of the file.
		if !strings.Contains(got, "#L") {
			t.Errorf("the link carries no line anchor: %q", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_sourceLinkOf pins that a declaration nothing can point at carries an
// empty link rather than a wrong one.
func Test_sourceLinkOf(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)
	decl := dp.Funcs[0].Decl

	type tc struct {
		// name describes the case.
		name string
		// prefix is the configured forge prefix.
		prefix string
		// wantEmpty is whether the link must be empty.
		wantEmpty bool
	}
	tests := []tc{
		{
			name:   "a linkable declaration",
			prefix: "https://example/blob/sha/",
		},
		{name: "source links disabled", wantEmpty: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := &indexOptions{
			sourceURLPrefix: c.prefix,
			repoRoot:        filepath.Dir(fset.Position(decl.Pos()).Filename),
		}

		got := sourceLinkOf(opts, fset, decl)

		if (got == "") != c.wantEmpty {
			t.Fatalf("sourceLinkOf = %q, want empty = %v", got, c.wantEmpty)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_emit pins that EVERY declaration kind produces a row, and that each row
// is addressable.
//
// A kind the projection forgets is invisible: the search box simply never
// suggests it, and nothing else in the pipeline notices. A block declaring
// several names is the case that gets dropped — indexing it once under its first
// name loses every other constant in the block.
func Test_emit(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)
	opts := &indexOptions{urlBase: "/v1/local"}

	type tc struct {
		// name describes the case.
		name string
		// packageShort is the module-relative package path.
		packageShort string
		// wantNames are the symbol names the projection must produce.
		wantNames []string
		// wantQualified is the qualified form one row must carry.
		wantQualified string
	}
	tests := []tc{
		{
			name:         "a nested package",
			packageShort: "fixture",
			//: a function, a type, its method, a lone const, both names of a
			//: const block, and a var.
			wantNames:     []string{"Exported", "Widget", "Do", "Limit", "A", "B", "Registry"},
			wantQualified: "fixture.Exported",
		},
		{
			//: the root falls back to the package's own name.
			name:         "the module root",
			packageShort: ".",
			wantNames:    []string{"Exported", "Widget", "Do", "Limit", "A", "B", "Registry"},
			//: no leading dot, which is what the fallback exists to prevent.
			wantQualified: "fixture.Exported",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rows := emit(dp, fset, c.packageShort, opts)

		seen := make(map[string]symbol, len(rows))
		for _, row := range rows {
			seen[row.Name] = row
			//: every row is addressable, or the search result goes nowhere.
			if row.URL == "" || !strings.Contains(row.URL, "#") {
				t.Errorf("row %q has no anchor: %q", row.Name, row.URL)
			}
			//: an unexported identifier is not part of the surface a consumer
			//: sees, so indexing one would offer a result nobody can use.
			if !strings.HasPrefix(row.Name, strings.ToUpper(row.Name[:1])) {
				t.Errorf("row %q is unexported", row.Name)
			}
		}
		for _, want := range c.wantNames {
			if _, found := seen[want]; !found {
				t.Errorf("%q produced no row — the search box would never suggest it", want)
			}
		}
		if got := seen["Exported"].Qualified; got != c.wantQualified {
			t.Errorf("Qualified = %q, want %q", got, c.wantQualified)
		}
		//: the method carries its receiver, so a search for the bare method name
		//: still shows which type it belongs to.
		if got := seen["Do"]; got.Receiver != "Widget" || got.Kind != "method" {
			t.Errorf("the method row is %+v, want a method on Widget", got)
		}
		//: the obsolescence marker survives the projection.
		if !seen["Widget"].Obsolete {
			t.Error("the deprecated type was not marked obsolete")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rowContext_valueRows pins the case a naive projection loses: one
// declaration block holding several names.
//
// `const ( A = 1; B = 2 )` is ONE doc.Value with two names. Emitting a row per
// Value rather than per name would index A and silently drop B, and nothing
// downstream would report a missing symbol.
func Test_rowContext_valueRows(t *testing.T) {
	t.Parallel()
	dp, fset := parseFixture(t, fixtureSource)
	ctx := &rowContext{
		pkg: dp, fset: fset, pkgLabel: "fixture",
		packageShort: "fixture", pkgURL: "/v1/local/fixture/", opts: &indexOptions{},
	}

	type tc struct {
		// name describes the case.
		name string
		// pick selects the value blocks under test.
		pick func() []*doc.Value
		// kind is the row kind they produce.
		kind string
		// wantNames are the names that must each get their own row.
		wantNames []string
	}
	tests := []tc{
		{
			name: "the package constants",
			pick: func() []*doc.Value { return dp.Consts }, kind: "const",
			//: Limit is its own block; A and B share one.
			wantNames: []string{"Limit", "A", "B"},
		},
		{
			name: "the package variables",
			pick: func() []*doc.Value { return dp.Vars }, kind: "var",
			wantNames: []string{"Registry"},
		},
		{name: "no values at all", pick: func() []*doc.Value { return nil }, kind: "const"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rows := ctx.valueRows(c.pick(), c.kind)

		if len(rows) != len(c.wantNames) {
			t.Fatalf("valueRows produced %d rows, want %d — a block declaring "+
				"several names must yield one row each", len(rows), len(c.wantNames))
		}
		seen := make(map[string]symbol, len(rows))
		for _, row := range rows {
			seen[row.Name] = row
			if row.Kind != c.kind {
				t.Errorf("row %q has kind %q, want %q", row.Name, row.Kind, c.kind)
			}
			//: each name is addressed by its own anchor, not the block's.
			if !strings.HasSuffix(row.URL, "#"+row.Name) {
				t.Errorf("row %q anchors at %q", row.Name, row.URL)
			}
		}
		for _, want := range c.wantNames {
			if _, found := seen[want]; !found {
				t.Errorf("%q produced no row", want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_skipDir pins the directories the walk never descends into. testdata in
// particular holds Go files that deliberately do not compile, so descending into
// it would abort the whole index.
func Test_skipDir(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// base is the directory name.
		base string
		// want is whether the subtree is pruned.
		want bool
	}
	tests := []tc{
		{name: "an ordinary package", base: "codec"},
		{name: "vendored code", base: "vendor", want: true},
		//: holds files that deliberately do not compile.
		{name: "test fixtures", base: "testdata", want: true},
		{name: "node modules", base: "node_modules", want: true},
		{name: "a version-control directory", base: ".git", want: true},
		{name: "any dot directory", base: ".cache", want: true},
		//: a package whose name merely contains one of the words is not skipped.
		{name: "a package named after one", base: "vendoring"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := skipDir(c.base); got != c.want {
			t.Fatalf("skipDir(%q) = %v, want %v", c.base, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_keepGoFiles pins that _test.go files are PARSED.
//
// They contribute no declarations of their own — go/doc drops them — but an
// Example function only attaches to the identifier it documents if the file it
// lives in was parsed. Excluding them silently drops every example from the
// index.
func Test_keepGoFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// filename is the directory entry.
		filename string
		// want is whether the parser reads it.
		want bool
	}
	tests := []tc{
		{name: "a source file", filename: "codec.go", want: true},
		//: parsed so Example functions attach to what they document.
		{name: "an internal test file", filename: "codec_internal_test.go", want: true},
		{name: "an external test file", filename: "codec_external_test.go", want: true},
		{name: "a markdown file", filename: "README.md"},
		{name: "a build file", filename: "BUILD.bazel"},
		{name: "a file merely containing go", filename: "gopher.txt"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		info, err := os.Stat(stageFile(t, c.filename))
		if err != nil {
			t.Fatalf("staging %s: %v", c.filename, err)
		}

		if got := keepGoFiles(info); got != c.want {
			t.Fatalf("keepGoFiles(%q) = %v, want %v", c.filename, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stageFile creates an empty file with the given name and returns its path.
func stageFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("staging %s: %v", name, err)
	}
	return path
}

// Test_relPath pins the slash form. Import paths and URLs both use forward
// slashes whatever the host does, so a Windows-style separator here would put a
// backslash in an import path and in every anchor on the page.
func Test_relPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	type tc struct {
		// name describes the case.
		name string
		// segments are appended to the root to form the directory.
		segments []string
		// want is the module-relative path.
		want string
	}
	tests := []tc{
		{name: "the root itself", want: "."},
		{name: "a single segment", segments: []string{"codec"}, want: "codec"},
		{name: "a nested package", segments: []string{"logger", "writer"}, want: "logger/writer"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := filepath.Join(append([]string{root}, c.segments...)...)

		got, err := relPath(dir, root)
		if err != nil {
			t.Fatalf("relPath = %v, want nil", err)
		}
		if got != c.want {
			t.Fatalf("relPath = %q, want %q", got, c.want)
		}
		//: forward slashes whatever the host separator is.
		if strings.Contains(got, "\\") {
			t.Errorf("the path carries a backslash: %q", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_importPathOf pins that the module ROOT carries the bare module path.
// Appending a "." segment there would make every root symbol's import path
// unimportable.
func Test_importPathOf(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	type tc struct {
		// name describes the case.
		name string
		// segments are appended to the root to form the directory.
		segments []string
		// want is the import path.
		want string
	}
	tests := []tc{
		{name: "the module root", want: "example.com/mod"},
		{name: "a single segment", segments: []string{"codec"}, want: "example.com/mod/codec"},
		{
			name:     "a nested package",
			segments: []string{"logger", "writer"}, want: "example.com/mod/logger/writer",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		opts := &indexOptions{root: root, modulePath: "example.com/mod"}
		dir := filepath.Join(append([]string{root}, c.segments...)...)

		got, err := importPathOf(dir, opts)
		if err != nil {
			t.Fatalf("importPathOf = %v, want nil", err)
		}
		if got != c.want {
			t.Fatalf("importPathOf = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_loadDir pins the two directories that must produce NO rows and no error:
// one with no Go files, and one whose import path is internal.
//
// An internal package is not visible to consumers, so a search result for one
// offers something nobody can import — and treating either as an error would
// abort the whole index over a directory that is simply not indexable.
func Test_loadDir(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// segments place the package under the module root.
		segments []string
		// source is the Go file staged there, or empty for none.
		source string
		// wantRows is whether the directory must contribute rows.
		wantRows bool
	}
	tests := []tc{
		{name: "an exported package", segments: []string{"fixture"}, source: fixtureSource, wantRows: true},
		//: not visible to consumers, so not indexable.
		{name: "an internal package", segments: []string{"internal", "fixture"}, source: fixtureSource},
		{name: "a package nested under internal", segments: []string{"a", "internal", "b"}, source: fixtureSource},
		{name: "a directory with no Go files", segments: []string{"docs"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		dir := filepath.Join(append([]string{root}, c.segments...)...)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("staging %s: %v", dir, err)
		}
		if c.source != "" {
			if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(c.source), 0o600); err != nil {
				t.Fatalf("staging the source: %v", err)
			}
		}
		opts := &indexOptions{root: root, modulePath: "example.com/mod", urlBase: "/v1"}

		rows, err := loadDir(dir, opts)
		if err != nil {
			t.Fatalf("loadDir = %v, want nil", err)
		}
		if (len(rows) > 0) != c.wantRows {
			t.Fatalf("loadDir produced %d rows, want any = %v", len(rows), c.wantRows)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packageSymbols pins that the EXTERNAL test package contributes nothing.
// It documents test helpers, which no consumer imports, and indexing them would
// fill the search box with names that exist nowhere in the public API.
func Test_packageSymbols(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// source is the Go file staged in the package directory.
		source string
		// wantRows is whether the package must contribute rows.
		wantRows bool
	}
	tests := []tc{
		{name: "a production package", source: fixtureSource, wantRows: true},
		{
			//: only unexported declarations, which are not part of the surface.
			name:   "a package with nothing exported",
			source: "package fixture\n\nfunc unexported() {}\n",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(c.source), 0o600); err != nil {
			t.Fatalf("staging the source: %v", err)
		}
		fset := token.NewFileSet()
		pkgs, perr := parser.ParseDir(fset, dir, keepGoFiles, parser.ParseComments)
		if perr != nil {
			t.Fatalf("parsing: %v", perr)
		}

		var rows []symbol
		for _, pkg := range pkgs {
			got, err := packageSymbols(pkg, fset, "example.com/fixture", "fixture", &indexOptions{urlBase: "/v1"})
			if err != nil {
				t.Fatalf("packageSymbols = %v, want nil", err)
			}
			rows = append(rows, got...)
		}

		if (len(rows) > 0) != c.wantRows {
			t.Fatalf("packageSymbols produced %d rows, want any = %v", len(rows), c.wantRows)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_collect pins the walk end to end: every package under the root
// contributes, and the pruned directories do not.
//
// A testdata directory is the case that matters — it holds Go files that
// deliberately do not compile, so descending into it would abort the whole index
// rather than skipping one directory.
func Test_collect(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// packages maps a directory path to the source staged in it.
		packages map[string]string
		// wantNames are symbol names the index must contain.
		wantNames []string
		// absentNames are names it must not.
		absentNames []string
	}
	tests := []tc{
		{
			name:      "one package",
			packages:  map[string]string{"fixture": fixtureSource},
			wantNames: []string{"Exported", "Widget", "Do"},
		},
		{
			name: "two packages",
			packages: map[string]string{
				"fixture": fixtureSource,
				"other":   "package other\n\n// Another does a thing.\nfunc Another() {}\n",
			},
			wantNames: []string{"Exported", "Another"},
		},
		{
			//: a directory that deliberately does not compile must be skipped
			//: rather than abort the index.
			name: "a testdata directory",
			packages: map[string]string{
				"fixture":  fixtureSource,
				"testdata": "package broken\n\nthis is not Go at all\n",
			},
			wantNames: []string{"Exported"},
		},
		{
			name: "an internal package",
			packages: map[string]string{
				"fixture":          fixtureSource,
				"internal/private": "package private\n\n// Hidden does a thing.\nfunc Hidden() {}\n",
			},
			wantNames:   []string{"Exported"},
			absentNames: []string{"Hidden"},
		},
		{name: "an empty module"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		for rel, source := range c.packages {
			dir := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatalf("staging %s: %v", dir, err)
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o600); err != nil {
				t.Fatalf("staging %s: %v", dir, err)
			}
		}
		opts := &indexOptions{root: root, modulePath: "example.com/mod", urlBase: "/v1"}

		rows, err := collect(opts)
		if err != nil {
			t.Fatalf("collect = %v, want nil", err)
		}
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			seen[row.Name] = true
		}
		for _, want := range c.wantNames {
			if !seen[want] {
				t.Errorf("%q is missing from the index", want)
			}
		}
		for _, absent := range c.absentNames {
			if seen[absent] {
				t.Errorf("%q reached the index — it is not importable by a consumer", absent)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_buildIndex pins the document the docs site loads: the schema Search.astro
// pins to, the module the symbols came from, and a timestamp that makes a stale
// index identifiable from the file alone.
func Test_buildIndex(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// module is the configured module path.
		module string
		// symbols are the extracted rows.
		symbols []symbol
	}
	tests := []tc{
		{name: "an empty module", module: "example.com/mod"},
		{
			name:   "a module with symbols",
			module: "example.com/mod",
			symbols: []symbol{
				{Kind: "func", Name: "Exported"},
				{Kind: "type", Name: "Widget"},
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		idx := buildIndex(&indexOptions{modulePath: c.module}, c.symbols)

		//: Search.astro pins to this, so an upgrade skew has to surface here
		//: rather than as an empty result set that looks like "nothing matched".
		if idx.Schema != schemaVersion {
			t.Fatalf("Schema = %d, want %d", idx.Schema, schemaVersion)
		}
		if idx.Module != c.module {
			t.Errorf("Module = %q, want %q", idx.Module, c.module)
		}
		if len(idx.Symbols) != len(c.symbols) {
			t.Errorf("the index carries %d symbols, want %d", len(idx.Symbols), len(c.symbols))
		}
		//: a stale index has to be identifiable without diffing it against the
		//: tree it was built from.
		if idx.GeneratedAt == "" {
			t.Error("the index carries no build timestamp")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_defaultRepoRoot pins the fallback the source links depend on: two levels
// above the module root, which is the layout the SDK's own pkg/v1 has.
func Test_defaultRepoRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// configured is an explicit -repo-root, if any.
		configured string
		// input is the module root.
		input string
		// want is the resolved repository root; empty means "derive it".
		want string
	}
	tests := []tc{
		//: an explicit root always wins, verbatim.
		{name: "an explicit root", configured: "/repo", input: "/repo/pkg/v1", want: "/repo"},
		{name: "an explicit relative root", configured: "../..", input: "pkg/v1", want: "../.."},
		{name: "derived from the input", input: "/repo/pkg/v1", want: "/repo"},
		{name: "derived from a shallow input", input: "/repo/a/b", want: "/repo"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := defaultRepoRoot(c.configured, c.input)

		if !ok {
			t.Fatalf("defaultRepoRoot(%q, %q) reported ok = false", c.configured, c.input)
		}
		if got != c.want {
			t.Fatalf("defaultRepoRoot(%q, %q) = %q, want %q", c.configured, c.input, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_encodeIndex pins that the document is written as INDENTED JSON, so a diff
// of the committed index is readable rather than one enormous line.
func Test_encodeIndex(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// symbols are the rows the document carries.
		symbols []symbol
	}
	tests := []tc{
		{name: "an empty index"},
		{name: "an index with a symbol", symbols: []symbol{{Kind: "func", Name: "Exported"}}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "symbols.json")
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("creating the target: %v", err)
		}
		idx := &index{Schema: schemaVersion, Module: "example.com/mod", Symbols: c.symbols}

		encErr := encodeIndex(f, idx)
		closeErr := f.Close()

		if encErr != nil || closeErr != nil {
			t.Fatalf("encodeIndex = %v / close = %v, want nil", encErr, closeErr)
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading it back: %v", rerr)
		}
		//: indented, so a diff of the committed index is readable.
		if !strings.Contains(string(raw), "\n  ") {
			t.Errorf("the document is not indented:\n%s", raw)
		}
		var round index
		//: and it has to parse back, or the docs site loads nothing.
		if uerr := json.Unmarshal(raw, &round); uerr != nil {
			t.Fatalf("the document does not parse back: %v", uerr)
		}
		if round.Schema != schemaVersion || len(round.Symbols) != len(c.symbols) {
			t.Errorf("round-tripped to %+v", round)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_writeIndex pins that the target directory is CREATED and that a failure
// is reported rather than swallowed.
//
// The docs site's data directory does not exist on a clean tree, so a writer
// that assumed it did would fail the first build after a checkout — and a
// silently missing index is a search box that finds nothing.
func Test_writeIndex(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// target names the output file relative to a fresh directory; the two
		// stdout spellings are used verbatim.
		target string
		// nested puts the target under a directory that does not exist yet.
		nested bool
		// wantErr is whether writing must fail.
		wantErr bool
	}
	tests := []tc{
		{name: "a file in an existing directory", target: "symbols.json"},
		//: the docs data directory does not exist on a clean tree.
		{name: "a file in a directory that does not exist", target: "symbols.json", nested: true},
		{name: "stdout by omission", target: ""},
		{name: "stdout by dash", target: "-"},
		//: a path whose parent is a FILE cannot be created.
		{name: "a target under a file", target: "occupied/symbols.json", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		idx := &index{Schema: schemaVersion, Module: "example.com/mod"}
		target := c.target
		//: the stdout spellings are passed through unchanged.
		if c.target != "" && c.target != "-" {
			dir := t.TempDir()
			if c.wantErr {
				if err := os.WriteFile(filepath.Join(dir, "occupied"), nil, 0o600); err != nil {
					t.Fatalf("staging the obstruction: %v", err)
				}
			}
			if c.nested {
				dir = filepath.Join(dir, "data", "generated")
			}
			target = filepath.Join(dir, c.target)
		}

		err := writeIndex(target, idx)

		if c.wantErr {
			if err == nil {
				t.Fatal("writeIndex reported success for a target it cannot create")
			}
			return
		}
		if err != nil {
			t.Fatalf("writeIndex = %v, want nil", err)
		}
		//: nothing to read back when it went to stdout.
		if target == "" || target == "-" {
			return
		}
		raw, rerr := os.ReadFile(target)
		if rerr != nil {
			t.Fatalf("the index was not written: %v", rerr)
		}
		var round index
		if uerr := json.Unmarshal(raw, &round); uerr != nil {
			t.Fatalf("the written index does not parse: %v", uerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_exitErr pins that a fatal message goes to STDERR and the process exits
// non-zero.
//
// Both halves matter: the non-zero status is what fails the docs build rather
// than shipping a broken search index, and stderr is what keeps the message out
// of the JSON when the index is being piped to stdout.
//
// It runs across a real exec because os.Exit cannot be observed from inside the
// process it ends. The child half is selected by an environment marker rather
// than by a second test function, so the suite carries no test that skips itself
// on every ordinary run.
func Test_exitErr(t *testing.T) {
	t.Parallel()
	//: the child half: exit with the message the parent asserts on.
	if os.Getenv(exitProbeEnv) != "" {
		exitErr("genindex: %s", os.Getenv(exitProbeEnv))
		return
	}
	type tc struct {
		// name describes the case.
		name string
		// message is what the child is asked to die with.
		message string
	}
	tests := []tc{
		{name: "a plain message", message: "probe"},
		{name: "a message with a path", message: "create /tmp/x: permission denied"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=Test_exitErr")
		cmd.Env = append(os.Environ(), exitProbeEnv+"="+c.message)

		out, err := cmd.CombinedOutput()

		//: a zero exit would let the docs build ship whatever it had.
		if err == nil {
			t.Fatalf("exitErr returned a zero status:\n%s", out)
		}
		//: and the reason has to be legible, or the build failure says nothing.
		if !strings.Contains(string(out), "genindex: "+c.message) {
			t.Fatalf("the fatal message did not reach stderr:\n%s", out)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// fixtureContext builds the per-package context the row builders read.
func fixtureContext(t *testing.T) (*rowContext, *doc.Package) {
	t.Helper()
	dp, fset := parseFixture(t, fixtureSource)
	return &rowContext{
		pkg: dp, fset: fset, pkgLabel: "fixture",
		packageShort: "fixture", pkgURL: "/v1/local/fixture/", opts: &indexOptions{},
	}, dp
}

// Test_rowContext_funcRow pins the row a package-level function produces: the
// bare name a user types, the qualified name a result displays, and the anchor
// the result links to.
func Test_rowContext_funcRow(t *testing.T) {
	t.Parallel()
	ctx, dp := fixtureContext(t)

	type tc struct {
		// name describes the case.
		name string
		// want is the identifier the row must be for.
		want string
	}
	tests := []tc{
		{name: "an exported function", want: "Exported"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		row := ctx.funcRow(dp.Funcs[0])

		if row.Kind != "func" || row.Name != c.want {
			t.Fatalf("row = %+v, want a func row for %q", row, c.want)
		}
		//: the qualified name is what a result displays and what a user reads to
		//: tell two same-named functions apart.
		if row.Qualified != "fixture."+c.want {
			t.Errorf("Qualified = %q, want %q", row.Qualified, "fixture."+c.want)
		}
		if row.URL != ctx.pkgURL+"#"+c.want {
			t.Errorf("URL = %q, want the package anchor for %q", row.URL, c.want)
		}
		//: the signature is what makes a result usable without opening the page.
		if !strings.HasPrefix(row.Signature, "func "+c.want) {
			t.Errorf("Signature = %q", row.Signature)
		}
		if row.Doc == "" {
			t.Error("the row carries no synopsis")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rowContext_typeRow pins the row an exported type produces, including that
// the obsolescence marker survives — the docs site strikes such a row through,
// so losing it advertises a dead symbol as live.
func Test_rowContext_typeRow(t *testing.T) {
	t.Parallel()
	ctx, dp := fixtureContext(t)

	type tc struct {
		// name describes the case.
		name string
		// want is the identifier the row must be for.
		want string
		// wantObsolete is whether it carries the godoc marker.
		wantObsolete bool
	}
	tests := []tc{
		{name: "a deprecated type", want: "Widget", wantObsolete: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		row := ctx.typeRow(dp.Types[0])

		if row.Kind != "type" || row.Name != c.want {
			t.Fatalf("row = %+v, want a type row for %q", row, c.want)
		}
		if row.Obsolete != c.wantObsolete {
			t.Errorf("Obsolete = %v, want %v — the docs site strikes the row through",
				row.Obsolete, c.wantObsolete)
		}
		if row.URL != ctx.pkgURL+"#"+c.want {
			t.Errorf("URL = %q, want the package anchor for %q", row.URL, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rowContext_methodRow pins the split a method row makes: Name stays BARE
// so a search for the method alone finds it, while Qualified and the anchor
// carry the receiver so the result says which type it belongs to.
func Test_rowContext_methodRow(t *testing.T) {
	t.Parallel()
	ctx, dp := fixtureContext(t)

	type tc struct {
		// name describes the case.
		name string
		// wantReceiver and wantMethod identify the row.
		wantReceiver string
		wantMethod   string
	}
	tests := []tc{
		{name: "a value method", wantReceiver: "Widget", wantMethod: "Do"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		typ := dp.Types[0]
		row := ctx.methodRow(typ, typ.Methods[0])

		if row.Kind != "method" {
			t.Fatalf("Kind = %q, want method", row.Kind)
		}
		//: bare, so searching for the method alone finds it.
		if row.Name != c.wantMethod {
			t.Errorf("Name = %q, want the bare %q", row.Name, c.wantMethod)
		}
		if row.Receiver != c.wantReceiver {
			t.Errorf("Receiver = %q, want %q", row.Receiver, c.wantReceiver)
		}
		want := "fixture." + c.wantReceiver + "." + c.wantMethod
		if row.Qualified != want {
			t.Errorf("Qualified = %q, want %q", row.Qualified, want)
		}
		//: the anchor is Type.Method, which is how the docs site names it.
		if row.URL != ctx.pkgURL+"#"+c.wantReceiver+"."+c.wantMethod {
			t.Errorf("URL = %q", row.URL)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
