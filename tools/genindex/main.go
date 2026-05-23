// Command genindex extracts a symbol search index from a Go module's
// public packages and emits it as JSON for the static docs site to
// consume client-side.
//
// The output schema is consumed by docs/site/src/components/Search.astro
// (via MiniSearch) to power the "Symbols" tab of the search modal.
//
// Pipeline: invoked from docs/site/package.json's `prebuild` step,
// runs strictly stdlib (no external deps — zero pollution of any go.sum)
// per tools/CLAUDE.md "single-purpose scripts" convention.
//
// Usage:
//
//	go run github.com/kitsunium/sdk/tools/genindex \
//	    -input ../pkg/v1 \
//	    -output ../docs/site/src/data/symbols.json \
//	    -url-base /v1/local
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// schemaVersion bumps when the JSON shape changes incompatibly.
// Search.astro pins to a known schema so an upgrade-skew surfaces
// at build time instead of silently breaking the UI.
const schemaVersion = 1

// Symbol is one row in the search index. Fields are intentionally
// short — gzipped JSON is what ships to the browser, and the index
// is reloaded on every page nav.
type Symbol struct {
	Kind         string   `json:"kind"`               // "func" | "type" | "const" | "var" | "method"
	Name         string   `json:"name"`               // bare identifier (e.g. "Marshal")
	Qualified    string   `json:"qualified"`          // "pkg.Name" form (e.g. "codec.Marshal")
	Package      string   `json:"package"`            // full import path
	PackageShort string   `json:"packageShort"`       // module-relative path (e.g. "codec")
	Signature    string   `json:"signature"`          // rendered Go signature (single line)
	Doc          string   `json:"doc,omitempty"`      // synopsis (first sentence)
	URL          string   `json:"url"`                // anchor URL on the docs site
	Receiver     string   `json:"receiver,omitempty"` // only for methods: receiver type name
	Examples     []string `json:"examples,omitempty"` // names of attached Example* funcs
	Deprecated   bool     `json:"deprecated,omitempty"`
	SourceURL    string   `json:"sourceUrl,omitempty"` // deep-link to the declaration on the source forge (GitHub blob URL)
}

// Index is the top-level JSON document.
type Index struct {
	Schema      int      `json:"schema"`
	GeneratedAt string   `json:"generatedAt"`
	Module      string   `json:"module"`
	Symbols     []Symbol `json:"symbols"`
}

func main() {
	var (
		input           = flag.String("input", "", "module root (e.g. ../pkg/v1)")
		output          = flag.String("output", "", "output JSON file (omit for stdout)")
		urlBase         = flag.String("url-base", "/v1/local", "URL prefix for symbol anchors")
		module          = flag.String("module", "github.com/kitsunium/sdk/pkg/v1", "Go module path of -input")
		repoRoot        = flag.String("repo-root", "", "filesystem path of the repo root (used to compute the source path; defaults to two levels above -input)")
		sourceURLPrefix = flag.String("source-url-prefix", "", "if set, build SourceURL = prefix + repo-relative-path#L<line> (e.g. https://github.com/kitsunium/sdk/blob/<sha>/)")
	)
	flag.Parse()

	if *input == "" {
		exitErr("genindex: -input is required")
	}

	// Default the repo root to <input>/../.. (e.g. /repo/pkg/v1 → /repo).
	if *repoRoot == "" {
		abs, err := filepath.Abs(*input)
		if err == nil {
			*repoRoot = filepath.Dir(filepath.Dir(abs))
		}
	}

	syms, err := collect(*input, *module, *urlBase, *repoRoot, *sourceURLPrefix)
	if err != nil {
		exitErr("genindex: %v", err)
	}

	idx := Index{
		Schema:      schemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Module:      *module,
		Symbols:     syms,
	}

	enc := func(w *os.File) {
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		if err := e.Encode(&idx); err != nil {
			exitErr("genindex: encode: %v", err)
		}
	}

	if *output == "" || *output == "-" {
		enc(os.Stdout)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		exitErr("genindex: mkdir: %v", err)
	}
	f, err := os.Create(*output)
	if err != nil {
		exitErr("genindex: create %s: %v", *output, err)
	}
	defer f.Close()
	enc(f)
	fmt.Fprintf(os.Stderr, "[genindex] wrote %s (%d symbols)\n", *output, len(syms))
}

// collect walks every Go package under root, builds a doc.Package per
// directory via doc.NewFromFiles, and projects every exported symbol
// into the flat Symbol list. _test.go files are PARSED (so Example
// funcs are attached) but their own decls are skipped by go/doc.
func collect(root, modulePath, urlBase, repoRoot, sourceURLPrefix string) ([]Symbol, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	var out []Symbol
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		// Skip vendored / hidden / testdata directories.
		if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" || base == "node_modules" {
			return filepath.SkipDir
		}
		syms, dirErr := loadDir(path, root, modulePath, urlBase, repoRoot, sourceURLPrefix)
		if dirErr != nil {
			return fmt.Errorf("load %s: %w", path, dirErr)
		}
		out = append(out, syms...)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// loadDir parses a single directory. Returns nil (no error) if the
// directory contains no Go files OR only an unexported "main" or
// internal package we should not index.
func loadDir(dir, root, modulePath, urlBase, repoRoot, sourceURLPrefix string) ([]Symbol, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Include both .go and _test.go so Example funcs attach.
		return strings.HasSuffix(fi.Name(), ".go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// importPath of "dir" relative to "root" is appended to modulePath.
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	importPath := modulePath
	if rel != "." && rel != "" {
		importPath = modulePath + "/" + rel
	}
	// Internal packages are not visible to consumers; do not index them.
	if strings.Contains("/"+importPath+"/", "/internal/") {
		return nil, nil
	}

	var out []Symbol
	for name, pkg := range pkgs {
		// Skip _test packages; keep production package only.
		if strings.HasSuffix(name, "_test") {
			continue
		}
		// Collect the file list so doc.NewFromFiles can attach examples.
		var files []*ast.File
		for _, f := range pkg.Files {
			files = append(files, f)
		}
		//: omit doc.AllDecls — without it, doc.New only keeps exported
		//: identifiers (the public surface consumers see on pkg.go.dev).
		//: Internal helpers like unknownFormat / resolveStreaming are
		//: not indexable from a consumer's perspective.
		dp, err := doc.NewFromFiles(fset, files, importPath)
		if err != nil {
			return nil, err
		}
		out = append(out, emit(dp, fset, rel, urlBase, repoRoot, sourceURLPrefix)...)
	}
	return out, nil
}

// sourceURL builds the forge URL deep-link for an AST node when a
// source-URL prefix was supplied. Returns "" when the prefix is empty,
// when the node is nil, or when the file lies outside the repo root.
func sourceURL(prefix, repoRoot string, fset *token.FileSet, node ast.Node) string {
	if prefix == "" || node == nil || repoRoot == "" {
		return ""
	}
	pos := fset.Position(node.Pos())
	if !pos.IsValid() {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, pos.Filename)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.TrimSuffix(prefix, "/") + "/" + filepath.ToSlash(rel) + fmt.Sprintf("#L%d", pos.Line)
}

// emit projects every exported declaration into Symbol rows.
func emit(p *doc.Package, fset *token.FileSet, packageShort, urlBase, repoRoot, sourceURLPrefix string) []Symbol {
	pkgURL := strings.TrimSuffix(urlBase, "/")
	if packageShort != "." && packageShort != "" {
		pkgURL = pkgURL + "/" + packageShort
	}
	pkgURL += "/"
	pkgLabel := lastPathSegment(packageShort)

	var out []Symbol

	for _, f := range p.Funcs {
		out = append(out, Symbol{
			Kind:         "func",
			Name:         f.Name,
			Qualified:    pkgLabel + "." + f.Name,
			Package:      p.ImportPath,
			PackageShort: packageShort,
			Signature:    renderDecl(fset, f.Decl),
			Doc:          synopsis(f.Doc),
			URL:          pkgURL + "#" + f.Name,
			Examples:     exampleNames(f.Examples),
			Deprecated:   isDeprecated(f.Doc),
			SourceURL:    sourceURL(sourceURLPrefix, repoRoot, fset, f.Decl),
		})
	}

	for _, t := range p.Types {
		out = append(out, Symbol{
			Kind:         "type",
			Name:         t.Name,
			Qualified:    pkgLabel + "." + t.Name,
			Package:      p.ImportPath,
			PackageShort: packageShort,
			Signature:    renderDecl(fset, t.Decl),
			Doc:          synopsis(t.Doc),
			URL:          pkgURL + "#" + t.Name,
			Examples:     exampleNames(t.Examples),
			Deprecated:   isDeprecated(t.Doc),
			SourceURL:    sourceURL(sourceURLPrefix, repoRoot, fset, t.Decl),
		})
		// Methods attached to the type.
		for _, m := range t.Methods {
			out = append(out, Symbol{
				Kind:         "method",
				Name:         m.Name,
				Qualified:    pkgLabel + "." + t.Name + "." + m.Name,
				Package:      p.ImportPath,
				PackageShort: packageShort,
				Signature:    renderDecl(fset, m.Decl),
				Doc:          synopsis(m.Doc),
				URL:          pkgURL + "#" + t.Name + "." + m.Name,
				Receiver:     t.Name,
				Examples:     exampleNames(m.Examples),
				Deprecated:   isDeprecated(m.Doc),
				SourceURL:    sourceURL(sourceURLPrefix, repoRoot, fset, m.Decl),
			})
		}
		// Type-attached consts / vars.
		out = append(out, valueRows(t.Consts, "const", p, pkgLabel, packageShort, pkgURL, fset, repoRoot, sourceURLPrefix)...)
		out = append(out, valueRows(t.Vars, "var", p, pkgLabel, packageShort, pkgURL, fset, repoRoot, sourceURLPrefix)...)
	}

	out = append(out, valueRows(p.Consts, "const", p, pkgLabel, packageShort, pkgURL, fset, repoRoot, sourceURLPrefix)...)
	out = append(out, valueRows(p.Vars, "var", p, pkgLabel, packageShort, pkgURL, fset, repoRoot, sourceURLPrefix)...)

	return out
}

func valueRows(values []*doc.Value, kind string, p *doc.Package, pkgLabel, packageShort, pkgURL string, fset *token.FileSet, repoRoot, sourceURLPrefix string) []Symbol {
	var rows []Symbol
	for _, v := range values {
		// doc.Value can carry multiple Names declared in the same block
		// (e.g. `const ( A = 1; B = 2 )`). Emit one row per name; the
		// signature line is shared.
		sig := renderDecl(fset, v.Decl)
		dep := isDeprecated(v.Doc)
		doc := synopsis(v.Doc)
		src := sourceURL(sourceURLPrefix, repoRoot, fset, v.Decl)
		for _, n := range v.Names {
			rows = append(rows, Symbol{
				Kind:         kind,
				Name:         n,
				Qualified:    pkgLabel + "." + n,
				Package:      p.ImportPath,
				PackageShort: packageShort,
				Signature:    sig,
				Doc:          doc,
				URL:          pkgURL + "#" + n,
				Deprecated:   dep,
				SourceURL:    src,
			})
		}
	}
	return rows
}

// renderDecl produces a single-line signature like
// "func Marshal(format Format, v any) ([]byte, error)".
// Whitespace is collapsed for a tidy display string.
func renderDecl(fset *token.FileSet, node ast.Node) string {
	if node == nil {
		return ""
	}
	var sb strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}
	if err := cfg.Fprint(&sb, fset, node); err != nil {
		return ""
	}
	// Collapse multi-line declarations to one line so the index UI
	// can render the signature without overflowing horizontally.
	out := strings.Join(strings.Fields(sb.String()), " ")
	return out
}

// synopsis extracts the first sentence per godoc conventions; doc.Synopsis
// (deprecated in newer Go) is replaced by doc.Package.Synopsis but a
// portable equivalent is implemented inline to avoid pulling in
// additional API surface.
func synopsis(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// godoc's "first sentence" = up to the first '.', '!', '?' followed
	// by whitespace OR end of string.
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '.' || c == '!' || c == '?' {
			next := i + 1
			if next == len(s) || s[next] == ' ' || s[next] == '\n' || s[next] == '\t' {
				return strings.TrimSpace(s[:next])
			}
		}
	}
	return strings.TrimSpace(s)
}

func isDeprecated(s string) bool {
	// godoc convention: a paragraph starting with "Deprecated:" marks
	// the identifier as deprecated.
	return strings.Contains(s, "\nDeprecated:") || strings.HasPrefix(s, "Deprecated:")
}

func exampleNames(exs []*doc.Example) []string {
	if len(exs) == 0 {
		return nil
	}
	out := make([]string, len(exs))
	for i, e := range exs {
		out[i] = e.Name
	}
	return out
}

func lastPathSegment(rel string) string {
	if rel == "." || rel == "" {
		return ""
	}
	idx := strings.LastIndex(rel, "/")
	if idx < 0 {
		return rel
	}
	return rel[idx+1:]
}

func exitErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
