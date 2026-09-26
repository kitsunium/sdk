// Package main — the doc-link check.
package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stagePackage writes the given files into a fresh directory and returns it.
func stagePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
	return dir
}

// deadTexts projects findings onto their link texts, in report order.
func deadTexts(dead []deadLink) []string {
	out := make([]string, 0, len(dead))
	for _, d := range dead {
		out = append(out, d.text)
	}
	return out
}

// Test_checkDocLinks pins the rule ADR 0138 is about. A pkg/v1 facade ALIASES
// its types, and go/doc collects methods and fields from the declarations of
// the package it documents — so a member link over an alias renders as literal
// text while the same link over a declared type works, and the fix, the alias
// linked on its own with the member after it, resolves.
func Test_checkDocLinks(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// files is the package staged under the root.
		files map[string]string
		// want are the dead links, in report order.
		want []string
	}
	tests := []tc{
		{
			name: "a member of an alias is dead, the alias on its own is not",
			files: map[string]string{"a.go": `// Package a documents [Alias], [Alias.Method] and [Alias].Method.
package a

import other "example.com/other"

// Alias re-exports a type declared elsewhere.
type Alias = other.Thing
`},
			want: []string{"[Alias.Method]"},
		},
		{
			//: the control: a rule refusing every member link would pass the case above.
			name: "a method and a field of a declared type resolve",
			files: map[string]string{"a.go": `// Package a documents [Real], [Real.Method], [Real.Field] and [*Real.Method].
package a

// Real is declared here.
type Real struct {
	// Field is a field.
	Field int
}

// Method is a method.
func (Real) Method() {}
`},
		},
		{
			name: "a name nothing declares is dead, wherever the comment is",
			files: map[string]string{"a.go": `// Package a documents [Missing].
package a

// Real links [Nowhere] from a field.
type Real struct {
	// Field links [Gone].
	Field int
}

// helper is unexported, and its comment still renders under go doc -u: [Lost].
func helper() {}
`},
			want: []string{"[Missing]", "[Nowhere]", "[Gone]", "[Lost]"},
		},
		{
			//: qualified links resolve through imports go/doc cannot see from here.
			name: "qualified, standard-library, lowercase and slice brackets are not judged",
			files: map[string]string{"a.go": `// Package a documents [other.Nothing], [json.Marshal], [lower] and []byte.
package a

import (
	"encoding/json"
	other "example.com/other"
)

var _ = json.Marshal
var _ = other.Thing
`},
		},
		{
			name: "a code block is verbatim and carries no link",
			files: map[string]string{"a.go": `// Package a shows code:
//
//	x := [Nothing]
package a
`},
		},
		{
			//: go/doc renders no test file, so a test file's comment links nothing.
			name: "a test file is not checked",
			files: map[string]string{
				"a.go":      "// Package a is fine.\npackage a\n",
				"a_test.go": "// Package a links [Missing] from a test.\npackage a\n",
			},
		},
		{
			//: testdata holds Go files that deliberately do not compile.
			name: "testdata and dot directories are not walked",
			files: map[string]string{
				"a.go":                 "// Package a is fine.\npackage a\n",
				"testdata/broken.go":   "package broken\n\nfunc {\n",
				".cache/b/b.go":        "// Package b links [Missing].\npackage b\n",
				"sub/c.go":             "// Package c links [Missing].\npackage c\n",
				"node_modules/d/d.go":  "// Package d links [Missing].\npackage d\n",
				"vendor/e/e.go":        "// Package e links [Missing].\npackage e\n",
				"sub/deeper/f/f.go":    "// Package f is fine: [F].\npackage f\n\n// F is declared.\nconst F = 1\n",
				"sub/deeper/f/g_ok.go": "package f\n",
			},
			want: []string{"[Missing]"},
		},
		{
			//: go/doc, gopls and pkg.go.dev publish none of these comments.
			name: "a parameter's and a function-local struct's comments are not documentation",
			files: map[string]string{"a.go": `// Package a is fine.
package a

// F is documented, and its parameter comments are not.
func F(
	// x links [Nowhere].
	x int,
) {
	type local struct {
		// Field links [Gone].
		Field int
	}
	_ = local{}
}
`},
		},
		{
			//: a file no platform compiles is rendered by none.
			name: "a file no platform builds is not judged",
			files: map[string]string{
				"a.go":   "// Package a is fine.\npackage a\n",
				"gen.go": "//go:build ignore\n\n// Package main links [Missing].\npackage main\n",
			},
		},
		{
			//: written twice in one comment: two findings, not four.
			name: "a link written twice is reported once per line",
			files: map[string]string{"a.go": `// Package a links [Wait] and
// [Wait] again, then [Go].
package a
`},
			want: []string{"[Wait]", "[Go]", "[Wait]"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := stagePackage(t, c.files)

		dead, err := checkDocLinks(root)
		if err != nil {
			t.Fatalf("checkDocLinks = %v, want nil", err)
		}

		if got := deadTexts(dead); !slices.Equal(got, c.want) {
			t.Fatalf("dead links = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_checkDocLinks_platforms pins that a comment is judged on each platform
// that compiles its file, against what the package declares THERE. One symbol
// table over the union of every file would accept a link to a symbol only a
// Linux file declares from a comment Windows renders too.
func Test_checkDocLinks_platforms(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// files is the package staged under the root.
		files map[string]string
		// want are the dead links as "text on" pairs, in report order.
		want []string
	}
	linuxOnly := "package a\n\n// OnlyLinux exists on Linux.\nconst OnlyLinux = 1\n"
	tests := []tc{
		{
			name: "a symbol only Linux declares, linked from a file every platform builds",
			files: map[string]string{
				"a.go":       "// Package a links [OnlyLinux].\npackage a\n",
				"a_linux.go": linuxOnly,
			},
			want: []string{"[OnlyLinux] darwin/arm64, windows/amd64, freebsd/amd64, openbsd/amd64, netbsd/amd64, dragonfly/amd64"},
		},
		{
			//: the file only builds where the symbol exists.
			name: "the same link from a Linux file resolves",
			files: map[string]string{
				"a.go":       "// Package a is fine.\npackage a\n",
				"a_linux.go": "package a\n\n// OnlyLinux exists on Linux; see [OnlyLinux].\nconst OnlyLinux = 1\n",
			},
		},
		{
			//: dead on every platform that builds the file: no platform list.
			name: "the same link from a Windows file is dead wherever that file builds",
			files: map[string]string{
				"a.go":         "// Package a is fine.\npackage a\n",
				"a_linux.go":   linuxOnly,
				"a_windows.go": "package a\n\n// W links [OnlyLinux] from Windows.\nconst W = 1\n",
			},
			want: []string{"[OnlyLinux] "},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := stagePackage(t, c.files)

		dead, err := checkDocLinks(root)
		if err != nil {
			t.Fatalf("checkDocLinks = %v, want nil", err)
		}

		got := make([]string, 0, len(dead))
		for _, d := range dead {
			got = append(got, d.text+" "+d.on)
		}
		if !slices.Equal(got, c.want) {
			t.Fatalf("dead links = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_checkDocLinks_errors pins that a check which could not read a package
// fails, rather than reporting the links of the packages it could read.
func Test_checkDocLinks_errors(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// files is staged under the root.
		files map[string]string
	}
	tests := []tc{
		{name: "a file that does not parse", files: map[string]string{"a.go": "package a\n\nfunc {\n"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := stagePackage(t, c.files)

		if _, err := checkDocLinks(root); err == nil {
			t.Fatal("checkDocLinks = nil, want an error")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_runDocLinkCheck pins the exit status and the report: a dead link fails
// the run and names the file, the line and ADR 0138's fix; a clean tree and an
// empty argument list are told apart.
func Test_runDocLinkCheck(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// files is staged under the root, or nil for no root argument at all.
		files map[string]string
		// wantStatus is the exit status.
		wantStatus int
		// wantOut are substrings the report must contain.
		wantOut []string
	}
	tests := []tc{
		{
			name:       "a dead link fails and says where and how",
			files:      map[string]string{"a.go": "// Package a links [Missing].\npackage a\n"},
			wantStatus: 1,
			wantOut:    []string{"a.go:1:1: [Missing] names no symbol this package declares", "ADR 0138"},
		},
		{
			name:       "a clean tree passes",
			files:      map[string]string{"a.go": "// Package a links [A].\npackage a\n\n// A is declared.\nconst A = 1\n"},
			wantStatus: 0,
			wantOut:    []string{"every same-package doc link resolves"},
		},
		{
			//: "nothing checked" must not read as "nothing wrong".
			name:       "no directory to walk is a usage error",
			wantStatus: 1,
			wantOut:    []string{"needs at least one directory"},
		},
		{
			name:       "a package that cannot be read fails",
			files:      map[string]string{"a.go": "package a\n\nfunc {\n"},
			wantStatus: 1,
			wantOut:    []string{"genindex:"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var roots []string
		if c.files != nil {
			roots = []string{stagePackage(t, c.files)}
		}
		var out bytes.Buffer

		status := runDocLinkCheck(roots, &out)

		if status != c.wantStatus {
			t.Fatalf("runDocLinkCheck = %d, want %d; report:\n%s", status, c.wantStatus, out.String())
		}
		for _, want := range c.wantOut {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("report does not contain %q:\n%s", want, out.String())
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

// Test_compareDeadLinks pins the report order: numeric, so a finding on line 10
// is listed after one on line 9 — a string sort would put it after line 1.
func Test_compareDeadLinks(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// a and b are compared.
		a, b deadLink
		// wantSign is the sign of compareDeadLinks(a, b).
		wantSign int
	}
	tests := []tc{
		{name: "line 9 before line 10", a: deadLink{file: "a.go", line: 9}, b: deadLink{file: "a.go", line: 10}, wantSign: -1},
		{name: "file before line", a: deadLink{file: "a.go", line: 90}, b: deadLink{file: "b.go", line: 1}, wantSign: -1},
		{name: "column", a: deadLink{file: "a.go", line: 1, col: 2}, b: deadLink{file: "a.go", line: 1, col: 1}, wantSign: 1},
		{name: "text last", a: deadLink{file: "a.go", line: 1, text: "[A]"}, b: deadLink{file: "a.go", line: 1, text: "[B]"}, wantSign: -1},
		{name: "equal", a: deadLink{file: "a.go", line: 1, text: "[A]"}, b: deadLink{file: "a.go", line: 1, text: "[A]"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := compareDeadLinks(c.a, c.b)
		if (got > 0) != (c.wantSign > 0) || (got < 0) != (c.wantSign < 0) {
			t.Fatalf("compareDeadLinks = %d, want sign %d", got, c.wantSign)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_isSourceFile pins which files are documentation go/doc renders.
func Test_isSourceFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name is the file name.
		name string
		// want is whether it is checked.
		want bool
	}
	tests := []tc{
		{name: "a.go", want: true},
		{name: "a_linux.go", want: true},
		{name: "a_test.go"},
		{name: "README.md"},
		{name: "go.mod"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isSourceFile(c.name); got != c.want {
			t.Fatalf("isSourceFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_sameScopeLinks pins the candidates: same-package forms only, in the order
// written, whatever block they sit in.
func Test_sameScopeLinks(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// text is the doc comment text.
		text string
		// want are the candidates as linkText renders them.
		want []string
	}
	tests := []tc{
		{name: "names and members", text: "See [A], [B.C] and [*D.E].\n", want: []string{"A", "B.C", "D.E"}},
		{name: "a list item", text: "Intro.\n\n  - item [I]\n", want: []string{"I"}},
		{name: "a heading stays plain", text: "Intro.\n\n# About [H]\n\nMore.\n"},
		{name: "a link definition's text", text: "See [the docs].\n\n[the docs]: https://example.com\n"},
		{name: "qualified and standard library", text: "See [pkg.Name] and [json.Marshal].\n"},
		{name: "a slice type", text: "Takes a []byte.\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got []string
		for _, link := range sameScopeLinks(c.text) {
			got = append(got, linkText(link))
		}
		if !slices.Equal(got, c.want) {
			t.Fatalf("sameScopeLinks = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_relFile pins the report's path form: relative to the root, in slashes,
// and unchanged when the root cannot relate it.
func Test_relFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// file is the position's file name.
		file string
		// root is the checked root.
		root string
		// want is the rendered position.
		want string
	}
	root := filepath.Join(string(filepath.Separator), "repo")
	tests := []tc{
		{name: "under the root", file: filepath.Join(root, "pkg", "a.go"), root: root, want: "pkg/a.go"},
		{name: "a relative name the root cannot relate", file: "a.go", root: root, want: "a.go"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := relFile(c.file, c.root); got != c.want {
			t.Fatalf("relFile = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_docComments pins which comments count as documentation: the package
// clause's, declarations', specs' and fields', and not a comment inside a body.
func Test_docComments(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// src is the file parsed.
		src string
		// want is how many doc comments it holds.
		want int
	}
	tests := []tc{
		{name: "package clause only", src: "// Package a.\npackage a\n", want: 1},
		{name: "a type, its field and a func", src: "package a\n\n// T.\ntype T struct {\n\t// F.\n\tF int\n}\n\n// G.\nfunc G() {\n\t// not documentation\n}\n", want: 3},
		{name: "a grouped declaration and its specs", src: "package a\n\n// Group.\nconst (\n\t// A.\n\tA = 1\n)\n", want: 2},
		{name: "a nested struct and an interface method", src: "package a\n\n// T.\ntype T struct {\n\t// F.\n\tF []struct {\n\t\t// G.\n\t\tG int\n\t}\n}\n\n// I.\ntype I interface {\n\t// M.\n\tM()\n}\n", want: 5},
		{name: "not a parameter's, nor a local struct's", src: "package a\n\n// F.\nfunc F(\n\t// p.\n\tp int,\n) {\n\ttype l struct {\n\t\t// f.\n\t\tf int\n\t}\n\t_ = l{}\n}\n", want: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		f, err := parser.ParseFile(token.NewFileSet(), "a.go", c.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if got := len(docComments(f)); got != c.want {
			t.Fatalf("docComments = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_linkPositions pins that a link is located on the line that writes it,
// in both spellings go/doc accepts.
func Test_linkPositions(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// src is the file parsed; its first comment group is searched.
		src string
		// text is the link searched for.
		text string
		// wantLines are the lines reported.
		wantLines []int
	}
	tests := []tc{
		{name: "one line", src: "// Package a.\n// See [X].\npackage a\n", text: "X", wantLines: []int{2}},
		{name: "the pointer spelling", src: "// Package a.\n// See [*X].\npackage a\n", text: "X", wantLines: []int{2}},
		{name: "twice on one line", src: "// See [X] and [X].\npackage a\n", text: "X", wantLines: []int{1, 1}},
		{
			//: never lose a finding because the line search missed it.
			name: "not found on any line falls back to the comment", src: "// Package a.\npackage a\n", text: "X", wantLines: []int{1},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "a.go", c.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		group := f.Comments[0]

		var lines []int
		for _, d := range linkPositions(fset, group, c.text, "") {
			lines = append(lines, d.line)
		}
		if !slices.Equal(lines, c.wantLines) {
			t.Fatalf("lines = %v, want %v", lines, c.wantLines)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
