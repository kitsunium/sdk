// Package server — every group option records its kind (ADR 0187).
package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// facadeFile is pkg/v1/net/server's generated facade, from this package's
// directory: go test runs a suite there, Bazel's runfiles keep the same
// layout (the go_test's data names the file), and so do the guests of
// e2e-cross, which run each binary from its package directory.
var facadeFile = filepath.Join("..", "..", "..", "..", "pkg", "v1", "net", "server", "gen_facade.go")

// groupOptionExempt are the constructors that record no kind: Listen adds an
// address, which no engine judges, and Loop is judged on its own by loopSet.
var groupOptionExempt = []string{"Listen", "Loop"}

// groupOptionFuncs returns the name of every top-level function of the files
// that returns exactly one GroupOption.
func groupOptionFuncs(t *testing.T, paths ...string) []string {
	t.Helper()
	var names []string
	fset := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		//: a file the walk cannot read is a blind spot, not a pass.
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "GroupOption" {
				names = append(names, fn.Name.Name)
			}
		}
	}
	sort.Strings(names)
	return slices.Compact(names)
}

// packageSources lists this package's production files.
func packageSources(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	//: an empty walk would pass having checked nothing.
	if len(out) == 0 {
		t.Fatal("no production file found in the package directory")
	}
	return out
}

// TestGroupOption_recordsItsKind walks every constructor that returns a
// GroupOption — the facade's, pkg/v1/net/server/gen_facade.go, and this
// package's own — and pins that each one records exactly its kind, given its
// zero value: a refusal reads the kinds a group was given, so a constructor
// that recorded none would let an option through a loop group unjudged
// (ADR 0187). A constructor the table does not name fails the test, so a new
// option cannot land without its kind.
func TestGroupOption_recordsItsKind(t *testing.T) {
	t.Parallel()
	type tc struct {
		opt  GroupOption
		kind corenet.OptionKind
	}
	table := map[string]tc{
		"ReadHeaderTimeout": {ReadHeaderTimeout(0), corenet.OptionReadHeaderTimeout},
		"MaxHeaderBytes":    {MaxHeaderBytes(0), corenet.OptionMaxHeaderBytes},
		"TLS":               {TLS(corenet.IdentityValue{}), corenet.OptionTLS},
		"ReadTimeout":       {ReadTimeout(0), corenet.OptionReadTimeout},
		"WriteTimeout":      {WriteTimeout(0), corenet.OptionWriteTimeout},
		"IdleTimeout":       {IdleTimeout(0), corenet.OptionIdleTimeout},
		"HandshakeTimeout":  {HandshakeTimeout(0), corenet.OptionHandshakeTimeout},
		"ReadBufferSize":    {ReadBufferSize(0), corenet.OptionReadBufferSize},
		"MaxPacketSize":     {MaxPacketSize(0), corenet.OptionMaxPacketSize},
		"BatchSize":         {BatchSize(0), corenet.OptionBatchSize},
		"MaxConns":          {MaxConns(0), corenet.OptionMaxConns},
		"Shards":            {Shards(0), corenet.OptionShards},
		"Adopt":             {Adopt(), corenet.OptionAdopt},
		"PreHandshake":      {PreHandshake(nil), corenet.OptionPreHandshake},
		"Plaintext":         {Plaintext(), corenet.OptionPlaintext},
		"Name":              {Name(""), corenet.OptionName},
		"Engine":            {Engine(corenet.EngineAuto), corenet.OptionEngine},
		"BatchReplies":      {BatchReplies(), corenet.OptionBatchReplies},
		"Protocols":         {Protocols(), corenet.OptionProtocols},
	}
	facade := groupOptionFuncs(t, facadeFile)
	//: the facade's fifteen, ReadHeaderTimeout through Adopt, and the three of
	//: ADR 0187; a shorter list means the walk read the wrong file.
	if len(facade) < 18 {
		t.Fatalf("the facade returns a GroupOption from %d functions, want at least 18: %v", len(facade), facade)
	}
	found := slices.Compact(slices.Sorted(slices.Values(append(facade, groupOptionFuncs(t, packageSources(t)...)...))))
	for _, name := range found {
		if slices.Contains(groupOptionExempt, name) {
			continue
		}
		c, ok := table[name]
		if !ok {
			t.Errorf("%s returns a GroupOption this test does not pin: give it an OptionKind and a row", name)
			continue
		}
		g := &StreamGroup{}
		c.opt(g)
		if got := g.kinds.list(); !slices.Equal(got, []corenet.OptionKind{c.kind}) {
			t.Errorf("%s recorded %v, want [%s]", name, got, c.kind)
		}
	}
	for name := range table {
		if !slices.Contains(found, name) {
			t.Errorf("the table names %s, which no walked file declares", name)
		}
	}
}

// TestStreamGroup_Use_recordsItsKind pins that Use records OptionMiddleware,
// even with no middleware: it is a method, not a GroupOption, and a loop group
// refuses the call itself.
func TestStreamGroup_Use_recordsItsKind(t *testing.T) {
	t.Parallel()
	g := (&StreamGroup{}).Use()
	if got := g.kinds.list(); !slices.Equal(got, []corenet.OptionKind{corenet.OptionMiddleware}) {
		t.Fatalf("Use() recorded %v, want [Use]", got)
	}
}

// Test_optionSet pins the set's order, its width against the kind block, and
// that a zero value counts as given.
func Test_optionSet(t *testing.T) {
	t.Parallel()
	//: every kind of the block fits the set's 64 bits.
	last := corenet.OptionNone
	for k := corenet.OptionNone; k.String() != "unknown"; k++ {
		last = k
	}
	if last >= 64 {
		t.Fatalf("the last kind, %s, is %d: past the set's 64 bits", last, uint8(last))
	}
	var s optionSet
	s.add(corenet.OptionEngine)
	s.add(corenet.OptionTLS)
	s.add(corenet.OptionTLS)
	if got, want := s.list(), []corenet.OptionKind{corenet.OptionTLS, corenet.OptionEngine}; !slices.Equal(got, want) {
		t.Fatalf("list = %v, want %v, in the block's order and once each", got, want)
	}
	if !s.has(corenet.OptionTLS) || s.has(corenet.OptionReadTimeout) {
		t.Fatalf("has: TLS %v, ReadTimeout %v; want true, false", s.has(corenet.OptionTLS), s.has(corenet.OptionReadTimeout))
	}
	var empty optionSet
	if got := empty.list(); len(got) != 0 {
		t.Fatalf("the empty set lists %v", got)
	}
}
