// Package main — the pin markers against the code, per cell.
package main

import (
	"slices"
	"strings"
	"testing"
)

// Test_parseMarker pins the marker's grammar: an id, a kind, a canonical
// signature that may hold spaces; anything else is malformed, by name.
func Test_parseMarker(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// text is the comment.
		text string
		// want is the marker, when it is accepted.
		want marker
		// wantErr is a fragment of the refusal.
		wantErr string
	}
	tests := []tc{
		{
			name: "a function with spaces in its signature",
			text: "// go:example.com/p.New[...] func func[K comparable](k K) *example.com/p.T[K]",
			want: marker{id: "go:example.com/p.New[...]", kind: kindFunc, canonical: "func[K comparable](k K) *example.com/p.T[K]"},
		},
		{name: "no kind", text: "// go:example.com/p.New", wantErr: "has no kind"},
		{name: "no signature", text: "// go:example.com/p.New func ", wantErr: "has no signature"},
		{name: "no id", text: "// go:example.com/p.new func func()", wantErr: "is no go: id"},
		{name: "no kind docs/api writes", text: "// go:example.com/p.New function func()", wantErr: "is no kind"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := parseMarker(c.text)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("parseMarker = %+v, %v; want an error containing %q", got, err, c.wantErr)
			}
			return
		}
		if err != nil || got != c.want {
			t.Fatalf("parseMarker = %+v, %v; want %+v", got, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_compareCell pins every finding a cell can give, each by its name: a
// symbol the code declares and no marker does (added without the design), a
// marker the code does not answer, a kind, a constraint, a tag or a signature
// that differs, a receiver turned pointer or value, a marker of another
// package, and an id one cell's pins declare twice.
func Test_compareCell(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// code is the code's symbols on the cell.
		code []apiSymbol
		// marks are the cell's markers.
		marks []marker
		// twice are the ids declared twice.
		twice []string
		// want are the findings' fragments, in order.
		want []string
	}
	sym := func(id, kind, canonical string) apiSymbol { return apiSymbol{ID: id, Kind: kind, Canonical: canonical} }
	mark := func(id, kind, canonical string) marker { return marker{id: id, kind: kind, canonical: canonical} }
	tests := []tc{
		{
			name:  "the code and the markers agree",
			code:  []apiSymbol{sym("go:example.com/p.F", kindFunc, "func()")},
			marks: []marker{mark("go:example.com/p.F", kindFunc, "func()")},
		},
		{
			name: "a symbol added without the design",
			code: []apiSymbol{sym("go:example.com/p.New", kindFunc, "func()")},
			want: []string{"undeclared: the code declares it and no pin marker does"},
		},
		{
			name:  "a symbol the design declares and the code lacks",
			marks: []marker{mark("go:example.com/p.Gone", kindFunc, "func()")},
			want:  []string{"missing: a pin marker declares it and the code does not"},
		},
		{
			name:  "a function turned variable",
			code:  []apiSymbol{sym("go:example.com/p.F", kindVar, "func()")},
			marks: []marker{mark("go:example.com/p.F", kindFunc, "func()")},
			want:  []string{"kind differs: the code has a var, the marker a func"},
		},
		{
			name:  "a constraint widened",
			code:  []apiSymbol{sym("go:example.com/p.G[...]", kindFunc, "func[T any](t T)")},
			marks: []marker{mark("go:example.com/p.G[...]", kindFunc, "func[T comparable](t T)")},
			want:  []string{"constraint differs: the code has func[T any](t T), the marker func[T comparable](t T)"},
		},
		{
			name:  "a struct tag changed",
			code:  []apiSymbol{sym("go:example.com/p.S", kindType, `struct{A int "json:\"a\""}`)},
			marks: []marker{mark("go:example.com/p.S", kindType, `struct{A int "json:\"b\""}`)},
			want:  []string{"tag differs"},
		},
		{
			name:  "a parameter changed",
			code:  []apiSymbol{sym("go:example.com/p.F", kindFunc, "func(n int)")},
			marks: []marker{mark("go:example.com/p.F", kindFunc, "func(n int64)")},
			want:  []string{"signature differs"},
		},
		{
			name:  "a pointer receiver turned value",
			code:  []apiSymbol{sym("go:example.com/p.T.M", kindMethod, "func()")},
			marks: []marker{mark("go:example.com/p.(*T).M", kindMethod, "func()")},
			want:  []string{"receiver differs: the code's method has a value receiver, the marker's a pointer one"},
		},
		{
			name:  "a value receiver turned pointer",
			code:  []apiSymbol{sym("go:example.com/p.(*T).M", kindMethod, "func()")},
			marks: []marker{mark("go:example.com/p.T.M", kindMethod, "func()")},
			want:  []string{"receiver differs: the code's method has a pointer receiver, the marker's a value one"},
		},
		{
			name:  "a marker of another package",
			marks: []marker{mark("go:example.com/other.X", kindVar, "int")},
			want:  []string{"foreign marker: it names a symbol of example.com/other"},
		},
		{
			name:  "an id one cell's pins declare twice",
			code:  []apiSymbol{sym("go:example.com/p.F", kindFunc, "func()")},
			marks: []marker{mark("go:example.com/p.F", kindFunc, "func()")},
			twice: []string{"go:example.com/p.F"},
			want:  []string{"declared twice by the pin markers one cell compiles"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		code := map[string]apiSymbol{}
		for _, s := range c.code {
			code[s.ID] = s
		}
		marks := map[string]marker{}
		for _, m := range c.marks {
			marks[m.id] = m
		}

		got := compareCell("example.com/p", code, marks, c.twice)

		if len(got) != len(c.want) {
			t.Fatalf("findings = %+v, want %d", got, len(c.want))
		}
		for i, f := range got {
			if !strings.Contains(f.what, c.want[i]) {
				t.Fatalf("finding %d = %q, want it to contain %q", i, f.what, c.want[i])
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

// Test_markerFindings_cells pins how the pins are read per cell: a pin file
// counts on the cells that compile it, a symbol the code has on one cell only
// is judged there only, and a finding names its cells unless it holds on all
// of them.
func Test_markerFindings_cells(t *testing.T) {
	t.Parallel()
	dir := stagePackage(t, map[string]string{
		"api_gen_test.go":   "package p_test\n\n// go:example.com/p.F func func()\n",
		"api_gen_1_test.go": "//go:build windows\n\npackage p_test\n\n// go:example.com/p.W func func()\n",
		"other_test.go":     "package p_test\n\n// go:example.com/p.Ignored func func()\n",
	})
	cells := []platform{{goos: "linux", goarch: "amd64"}, {goos: "windows", goarch: "amd64"}}
	f := apiSymbol{ID: "go:example.com/p.F", Kind: kindFunc, Canonical: "func()"}
	w := apiSymbol{ID: "go:example.com/p.W", Kind: kindFunc, Canonical: "func()"}
	n := apiSymbol{ID: "go:example.com/p.N", Kind: kindFunc, Canonical: "func()"}
	byCell := []map[string][]apiSymbol{
		{dir: {f, n}},
		{dir: {f, w, n}},
	}

	got := markerFindings(byCell, cells, []packageDir{{dir: dir, path: "example.com/p"}}, dir)

	want := []string{".: go:example.com/p.N: undeclared"}
	if len(got) != len(want) || !strings.HasPrefix(got[0], want[0]) || strings.Contains(got[0], "(on ") {
		t.Fatalf("findings = %q, want one undeclared N on every cell", got)
	}
	byCell[1][dir] = []apiSymbol{f, n}
	got = markerFindings(byCell, cells, []packageDir{{dir: dir, path: "example.com/p"}}, dir)
	if !slices.ContainsFunc(got, func(l string) bool {
		return strings.Contains(l, "go:example.com/p.W: missing") && strings.HasSuffix(l, "(on windows/amd64)")
	}) {
		t.Fatalf("findings = %q, want W missing on windows/amd64 alone", got)
	}
}

// Test_splitTypeParams pins how a canonical signature's type parameters are
// told from the brackets of a slice, an array or a map.
func Test_splitTypeParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		// sig is the signature.
		sig string
		// params and rest are the split.
		params, rest string
	}{
		{sig: "func[K comparable, V any](k K) V", params: "[K comparable, V any]", rest: "func(k K) V"},
		{sig: "[T interface{~[]byte}] struct{V T}", params: "[T interface{~[]byte}]", rest: " struct{V T}"},
		{sig: "[]int", rest: "[]int"},
		{sig: "[4]byte", rest: "[4]byte"},
		{sig: "map[string]int", rest: "map[string]int"},
		{sig: "func(b []byte)", rest: "func(b []byte)"},
	}
	for _, c := range tests {
		params, rest := splitTypeParams(c.sig)
		if params != c.params || rest != c.rest {
			t.Fatalf("splitTypeParams(%q) = %q, %q; want %q, %q", c.sig, params, rest, c.params, c.rest)
		}
	}
}
