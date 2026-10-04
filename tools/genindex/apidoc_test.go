// Package main — the document: records merged across cells, sorted, encoded
// the same on every machine, and the drift a check reports.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// twoCells are the cells the document tests read on.
var twoCells = []platform{{goos: "linux", goarch: "amd64"}, {goos: "windows", goarch: "amd64"}}

// Test_moduleAPI_document pins the merge: a record read the same on every
// cell is written once with no platforms; a name read differently on two
// cells is one record per form, each naming its cells; the records are in
// the document's order, a type before its methods.
func Test_moduleAPI_document(t *testing.T) {
	t.Parallel()
	m := newModuleAPI(listedModule{Path: "example.com/m"}, ".")
	same := apiSymbol{ID: "go:example.com/m.T", Kind: kindType, Package: "example.com/m", Name: "T", Canonical: "int"}
	method := apiSymbol{ID: "go:example.com/m.(*T).M", Kind: kindMethod, Package: "example.com/m", Name: "M", Recv: "T", Canonical: "func()"}
	linux := apiSymbol{ID: "go:example.com/m.Max", Kind: kindConst, Package: "example.com/m", Name: "Max", Canonical: "int", Value: "64"}
	windows := linux
	windows.Value = "32"
	for i := range twoCells {
		mustAdd(t, m.symbols, method, i)
		mustAdd(t, m.symbols, same, i)
	}
	mustAdd(t, m.symbols, windows, 1)
	mustAdd(t, m.symbols, linux, 0)
	mustAdd(t, m.packages, apiPackage{Path: "example.com/m", Name: "m", Dir: "."}, 0)
	mustAdd(t, m.packages, apiPackage{Path: "example.com/m", Name: "m", Dir: "."}, 1)

	doc := m.document(twoCells)

	got := make([]string, 0, len(doc.Symbols))
	for _, s := range doc.Symbols {
		got = append(got, s.ID+" "+s.Value+" "+strings.Join(s.Platforms, ","))
	}
	want := []string{
		"go:example.com/m.Max 64 linux/amd64",
		"go:example.com/m.Max 32 windows/amd64",
		"go:example.com/m.T  ",
		"go:example.com/m.(*T).M  ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("symbols = %q, want %q", got, want)
	}
	if len(doc.Packages) != 1 || doc.Packages[0].Platforms != nil {
		t.Fatalf("packages = %+v, want one record of every cell", doc.Packages)
	}
	if !slices.Equal(doc.Cells, []string{"linux/amd64", "windows/amd64"}) {
		t.Fatalf("cells = %q", doc.Cells)
	}
}

// mustAdd adds a record to a merge, failing the test on an encoding error.
func mustAdd[T any](t *testing.T, v *variants[T], rec T, cell int) {
	t.Helper()
	if err := v.add(rec, cell); err != nil {
		t.Fatalf("add: %v", err)
	}
}

// Test_documentName pins where a module's document is written: its path below
// the root module's parent, or its whole path outside it.
func Test_documentName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// root and module are the paths.
		root, module string
		// want is the name.
		want string
	}{
		{root: "github.com/kitsunium/sdk", module: "github.com/kitsunium/sdk", want: "sdk"},
		{root: "github.com/kitsunium/sdk", module: "github.com/kitsunium/sdk/third-party/aws", want: "sdk/third-party/aws"},
		{root: "github.com/kitsunium/sdk", module: "example.org/other", want: "example.org/other"},
		{root: "fixture", module: "fixture", want: "fixture"},
		{root: "", module: "example.com/m", want: "example.com/m"},
	}
	for _, c := range tests {
		if got := documentName(c.root, c.module); got != c.want {
			t.Fatalf("documentName(%q, %q) = %q, want %q", c.root, c.module, got, c.want)
		}
	}
}

// Test_encodeDocument pins the bytes: indented by two spaces, HTML characters
// as they are — a channel type stays <-chan —, one trailing newline, and the
// same bytes every time.
func Test_encodeDocument(t *testing.T) {
	t.Parallel()
	doc := &apiDocument{
		Format: apiFormat, Module: "example.com/m", Dir: ".", Cells: []string{"linux/amd64"},
		Packages: []apiPackage{}, Symbols: []apiSymbol{{ID: "go:example.com/m.C", Kind: kindVar, Canonical: "<-chan struct{}", File: "m.go"}},
	}
	first, err := encodeDocument(doc)
	if err != nil {
		t.Fatalf("encodeDocument = %v", err)
	}
	second, err := encodeDocument(doc)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("two encodings of one document differ")
	}
	text := string(first)
	if !strings.Contains(text, `"canonical": "<-chan struct{}"`) || !strings.HasSuffix(text, "}\n") || strings.HasSuffix(text, "\n\n") {
		t.Fatalf("encoding = %s", text)
	}
	if !strings.HasPrefix(text, "{\n  \"format\": \"sdk.api/v1\",\n") {
		t.Fatalf("encoding does not open with the format, indented by two: %s", text[:min(len(text), 80)])
	}
}

// Test_symbolDrift pins the drift a check reports: each record added,
// changed or removed, by id and cells; a committed file that is no document
// said to be one; a long list bounded.
func Test_symbolDrift(t *testing.T) {
	t.Parallel()
	old := &apiDocument{Format: apiFormat, Symbols: []apiSymbol{
		{ID: "go:m.A", Doc: "old\n"},
		{ID: "go:m.Gone"},
		{ID: "go:m.W", Platforms: []string{"windows/amd64"}},
	}}
	committed, err := encodeDocument(old)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	now := &apiDocument{Format: apiFormat, Symbols: []apiSymbol{
		{ID: "go:m.A", Doc: "new\n"},
		{ID: "go:m.New"},
		{ID: "go:m.W", Platforms: []string{"windows/amd64"}},
	}}

	got := symbolDrift(committed, now)

	want := []string{"  changed go:m.A", "  added   go:m.New", "  removed go:m.Gone"}
	if !slices.Equal(got, want) {
		t.Fatalf("drift = %q, want %q", got, want)
	}
	if got := symbolDrift([]byte("not json"), now); len(got) != 1 || !strings.Contains(got[0], "no docs/api document") {
		t.Fatalf("drift of a file that is no document = %q", got)
	}
	lines := make([]string, maxDriftLines+5)
	if got := boundLines(lines); len(got) != maxDriftLines+1 || !strings.Contains(got[maxDriftLines], "and 5 more") {
		t.Fatalf("boundLines kept %d lines, last %q", len(got), got[len(got)-1])
	}
}

// Test_existingDocuments pins which files under docs/api are documents: every
// JSON file at any depth but the schema; an absent docs/api holds none.
func Test_existingDocuments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got, err := existingDocuments(root); err != nil || len(got) != 0 {
		t.Fatalf("existingDocuments of a tree without docs/api = %q, %v", got, err)
	}
	stage(t, filepath.Join(root, "docs", "api"), map[string]string{"schema.json": "{}", "sdk.json": "{}", "notes.md": ""})
	stage(t, filepath.Join(root, "docs", "api", "sdk", "third-party"), map[string]string{"aws.json": "{}"})
	got, err := existingDocuments(root)
	if err != nil || !slices.Equal(got, []string{"sdk", "sdk/third-party/aws"}) {
		t.Fatalf("existingDocuments = %q, %v", got, err)
	}
	var out bytes.Buffer
	if err := removeStale(root, []string{"sdk"}, &out); err != nil {
		t.Fatalf("removeStale = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "api", "sdk", "third-party", "aws.json")); !os.IsNotExist(err) {
		t.Fatalf("a document no module writes survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "api", "schema.json")); err != nil {
		t.Fatalf("the schema was removed: %v", err)
	}
}
