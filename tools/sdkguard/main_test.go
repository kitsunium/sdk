package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write drops src into a fresh directory and returns that directory.
func write(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// idsOf renders the findings as rule IDs for compact assertions.
func idsOf(findings []findingEntity) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Rule)
	}
	return out
}

// scan runs every rule over one fixture file.
func scan(t *testing.T, src string) []findingEntity {
	t.Helper()
	dir := write(t, "fixture.go", src)
	found, err := scanDir(dir, allRules, false)
	if err != nil {
		t.Fatalf("scanDir: %v", err)
	}
	return found
}

// ctxFor parses src and builds the context the rules read, so a test can reach
// the resolution helpers without going through a whole scan.
func ctxFor(t *testing.T, src string) (*fileCtx, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byPath, dots := importsOf(file)
	ctx := &fileCtx{
		fset:       fset,
		byPath:     byPath,
		dotImports: dots,
		suppressed: suppressionsOf(fset, file),
	}
	ctx.bridgeBound = ctx.bridgeBoundNames(file)
	ctx.shadowed = shadowedNames(file, byPath)
	return ctx, file
}

// firstNode returns the first node in file for which want reports true.
func firstNode(file *ast.File, want func(ast.Node) bool) ast.Node {
	var found ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil || n == nil {
			return false
		}
		if want(n) {
			found = n
		}
		return true
	})
	return found
}

// proxyStub serves a @v/list response, so no test touches the network.
func proxyStub(t *testing.T, versions ...string) probe {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/@v/list") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := w.Write([]byte(strings.Join(versions, "\n") + "\n")); err != nil {
			t.Errorf("stub write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return probe{proxy: srv.URL, client: srv.Client()}
}

// modDir writes a go.mod and returns its directory.
func modDir(t *testing.T, gomod string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// Each rule must fire on the construct it names. A rule that never fires is
// indistinguishable from a rule that does not exist.
func Test_checkSlogPipeline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{
			"a handler constructor builds a destination",
			"package p\nimport (\"log/slog\"; \"os\")\nvar _ = slog.NewTextHandler(os.Stderr, nil)\n", 1,
		},
		{
			"SetDefault redirects the process-wide logger",
			"package p\nimport \"log/slog\"\nfunc f(l *slog.Logger) { slog.SetDefault(l) }\n", 1,
		},
		{
			// The type and the attr constructors are vocabulary a bridge
			// consumer needs; only construction creates a destination.
			"the type alone is not a pipeline",
			"package p\nimport \"log/slog\"\ntype S struct{ L *slog.Logger }\n" +
				"var _ = slog.String(\"k\", \"v\")\n", 0,
		},
		{
			"a file that never imports slog is not scanned for it",
			"package p\nvar x = 1\n", 0,
		},
		{
			// A dot import binds no qualifier, so the rule reports that it
			// cannot see rather than passing quietly.
			"a dot import is reported as a blind spot",
			"package p\nimport (\n. \"log/slog\"\n\"os\"\n)\nvar _ = New(NewTextHandler(os.Stderr, nil))\n", 1,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		if got := checkSlogPipeline(ctx, file); len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The sanctioned composition must not fail the invariant it steers people
// towards. NewHandler returns (handler, error), so real code binds it first.
func Test_wrapsBridge(t *testing.T) {
	t.Parallel()
	const imports = "package p\nimport (\n\"log/slog\"\n" +
		"\"github.com/kitsunium/sdk/pkg/v1/logger\"\n" +
		"\"github.com/kitsunium/sdk/pkg/v1/logger/slogbridge\"\n)\n"
	type tc struct {
		name string
		src  string
		want bool
	}
	tests := []tc{
		{
			"a handler bound to a variable",
			imports + "func f(lg logger.Logger) *slog.Logger {\n" +
				"h, _ := slogbridge.NewHandler(lg)\nreturn slog.New(h)\n}\n", true,
		},
		{
			"an unrelated handler",
			imports + "func f(h slog.Handler) *slog.Logger { return slog.New(h) }\n", false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		call := firstNode(file, func(n ast.Node) bool { return ctx.isCall(n, slogPath, "New") })
		if call == nil {
			t.Fatal("no slog.New call in the fixture")
		}
		if got := ctx.wrapsBridge(call); got != c.want {
			t.Errorf("wrapsBridge = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// SDK002 replaces the two stdlib constructors with the SDK's typed model.
func Test_checkUntypedErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{"fmt.Errorf", "package p\nimport \"fmt\"\nvar _ = fmt.Errorf(\"boom\")\n", 1},
		{"errors.New", "package p\nimport \"errors\"\nvar _ = errors.New(\"boom\")\n", 1},
		{"both in one file", "package p\nimport (\"errors\"; \"fmt\")\n" +
			"var _ = errors.New(\"a\")\nvar _ = fmt.Errorf(\"b\")\n", 2},
		{"neither imported", "package p\nvar x = 1\n", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		if got := checkUntypedErrors(ctx, file); len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// SDK003 fires only where os.Stdout becomes a LOG destination. The field name
// alone concludes nothing: a Report writing its result to stdout is a CLI
// doing its job, which ADR 0030 permits.
func Test_checkStdoutDestination(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{
			"a logger Config Writer",
			"package p\nimport (\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writer: os.Stdout}\n", 1,
		},
		{
			"a fan-out slice inside a logging config",
			"package p\nimport (\"io\"\n\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writers: []io.Writer{os.Stderr, os.Stdout}}\n", 1,
		},
		{
			"an Output field on a non-logging struct",
			"package p\nimport (\"io\"\n\"os\"\n)\ntype Report struct{ Output io.Writer }\n" +
				"var _ = Report{Output: os.Stdout}\n", 0,
		},
		{
			"plain CLI output",
			"package p\nimport (\"encoding/json\"; \"fmt\"; \"os\")\n" +
				"func f() { fmt.Fprintln(os.Stdout, \"x\"); _ = json.NewEncoder(os.Stdout) }\n", 0,
		},
		{
			"stderr is the correct destination",
			"package p\nimport (\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n" +
				"var _ = logger.Config{Writer: os.Stderr}\n", 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		if got := checkStdoutDestination(ctx, file); len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The type gate is what keeps the field-name heuristic off every struct that
// happens to have an Output.
func Test_isLoggingConfig(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want bool
	}
	tests := []tc{
		{
			"a logger package struct",
			"package p\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n" +
				"var _ = logger.Config{}\n", true,
		},
		{
			"an slog package struct",
			"package p\nimport \"log/slog\"\nvar _ = slog.HandlerOptions{}\n", true,
		},
		{
			"a local struct",
			"package p\ntype Report struct{ Output int }\nvar _ = Report{}\n", false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		lit, _ := firstNode(file, func(n ast.Node) bool {
			_, ok := n.(*ast.CompositeLit)
			return ok
		}).(*ast.CompositeLit)
		if lit == nil {
			t.Fatal("no composite literal in the fixture")
		}
		if got := ctx.isLoggingConfig(lit); got != c.want {
			t.Errorf("isLoggingConfig = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stdoutInConfig reads only the destination-shaped fields of a logging struct.
func Test_stdoutInConfig(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	const head = "package p\nimport (\"os\"\n\"github.com/kitsunium/sdk/pkg/v1/logger\")\n"
	tests := []tc{
		{"a Writer field", head + "var _ = logger.Config{Writer: os.Stdout}\n", 1},
		{"a field that names no destination", head + "var _ = logger.Config{MinLevel: os.Stdout}\n", 0},
		{"stderr in the same field", head + "var _ = logger.Config{Writer: os.Stderr}\n", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		lit, _ := firstNode(file, func(n ast.Node) bool {
			_, ok := n.(*ast.CompositeLit)
			return ok
		}).(*ast.CompositeLit)
		if lit == nil {
			t.Fatal("no composite literal in the fixture")
		}
		if got := ctx.stdoutInConfig(lit); len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stdoutIn descends, so a fan-out slice is caught as surely as a bare
// reference.
func Test_stdoutIn(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{"a bare reference", "package p\nimport \"os\"\nvar v = os.Stdout\n", 1},
		{"inside a slice literal", "package p\nimport (\"io\"\n\"os\")\n" +
			"var v = []io.Writer{os.Stderr, os.Stdout}\n", 1},
		{"two references in one expression", "package p\nimport (\"io\"\n\"os\")\n" +
			"var v = []io.Writer{os.Stdout, os.Stdout}\n", 2},
		{"stderr only", "package p\nimport \"os\"\nvar v = os.Stderr\n", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		spec, _ := firstNode(file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			return ok && len(vs.Values) > 0
		}).(*ast.ValueSpec)
		if spec == nil {
			t.Fatal("no value spec in the fixture")
		}
		if got := ctx.stdoutIn(spec.Values[0], "a Writer field"); len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Reading logger.Version is fine; only writing it defeats the link-time stamp.
func Test_checkVersionAssignment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	const head = "package p\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n"
	tests := []tc{
		{"an assignment", head + "func f() { logger.Version = \"1.0\" }\n", 1},
		{"a read", head + "var v = logger.Version\n", 0},
		{
			// A local named `logger` makes the selector ambiguous, and
			// `logger` is a name consumers bind constantly.
			"a shadowed package name",
			head + "type myLog struct{ Version string }\n" +
				"func f() {\n\tlogger := myLog{}\n\tlogger.Version = \"1.0\"\n\t_ = logger\n}\n", 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		if got := checkVersionAssignment(ctx, file); len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// SDK005 watches the stdlib global logger, which carries no level and no
// framework_version.
func Test_checkLegacyLog(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{"log.Printf", "package p\nimport \"log\"\nfunc f() { log.Printf(\"x\") }\n", 1},
		{"log.Fatal bypasses defers too", "package p\nimport \"log\"\nfunc f() { log.Fatal(\"x\") }\n", 1},
		{"log.New builds its own", "package p\nimport (\"log\"; \"os\")\n" +
			"var _ = log.New(os.Stderr, \"\", 0)\n", 1},
		{"log not imported", "package p\nvar x = 1\n", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		if got := checkLegacyLog(ctx, file); len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The walk prunes the trees nobody can act on, and skips test files unless
// asked for them.
func Test_scanDir(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		file      string
		src       string
		withTests bool
		want      int
	}
	tests := []tc{
		{"an ordinary source file", "a.go", "package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", false, 1},
		{"a test file is skipped by default", "a_test.go", "package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", false, 0},
		{"a test file with -tests", "a_test.go", "package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", true, 1},
		{"a non-Go file", "a.txt", "log.Print()\n", false, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := write(t, c.file, c.src)
		got, err := scanDir(dir, allRules, c.withTests)
		if err != nil {
			t.Fatalf("scanDir: %v", err)
		}
		if len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	// An unreadable root is a tool error, not an empty result that would read
	// as "no violations".
	if _, err := scanDir(filepath.Join(t.TempDir(), "absent"), allRules, false); err == nil {
		t.Error("missing root accepted silently")
	}
}

// skipDir prunes the directories whose contents are not the consumer's to fix.
func Test_skipDir(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		dir  string
		want bool
	}
	tests := []tc{
		{"vendor", "vendor", true},
		{"testdata", "testdata", true},
		{"node_modules", "node_modules", true},
		{"a hidden directory", ".git", true},
		{"an ordinary package", "internal", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		full := filepath.Join(root, c.dir)
		if err := os.MkdirAll(full, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 {
			t.Fatalf("read dir: %v", err)
		}
		got := skipDir(full, entries[0]) != nil
		if got != c.want {
			t.Errorf("skipped = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// scanFile stays silent twice for different reasons: a file that does not
// parse, and one no build includes.
func Test_scanFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{"an ordinary file", "package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", 1},
		{"a file that does not parse", "package p\nfunc f( {\n", 0},
		{"a build-ignored file", "//go:build ignore\n\npackage p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", 0},
		{
			"a generated file",
			"// Code generated by stringer. DO NOT EDIT.\n\npackage p\nimport \"fmt\"\nvar _ = fmt.Errorf(\"x\")\n", 0,
		},
		{
			// A platform-constrained file IS part of a build; its rules apply
			// on the platform it targets.
			"a platform-constrained file",
			"//go:build linux\n\npackage p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", 1,
		},
		{
			"a justified suppression removes the finding",
			"package p\nimport \"log\"\nfunc f() { log.Print(\"x\") } //sdkguard:allow SDK005 vendor contract\n", 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := write(t, "fixture.go", c.src)
		got := scanFile(filepath.Join(dir, "fixture.go"), allRules)
		if len(got) != c.want {
			t.Errorf("findings = %d, want %d (%v)", len(got), c.want, idsOf(got))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The bridge handler is recognised whether it is inlined or bound first.
func Test_bridgeBoundNames(t *testing.T) {
	t.Parallel()
	const head = "package p\nimport (\n\"github.com/kitsunium/sdk/pkg/v1/logger\"\n" +
		"\"github.com/kitsunium/sdk/pkg/v1/logger/slogbridge\"\n)\n"
	type tc struct {
		name string
		src  string
		want []string
	}
	tests := []tc{
		{
			"a handler bound from NewHandler",
			head + "func f(lg logger.Logger) { h, _ := slogbridge.NewHandler(lg); _ = h }\n",
			[]string{"h"},
		},
		{
			"an ordinary assignment binds nothing",
			head + "func f(lg logger.Logger) { h := lg; _ = h }\n",
			nil,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		got := ctx.bridgeBoundNames(file)
		for _, name := range c.want {
			if !got[name] {
				t.Errorf("name %q not recorded; got %v", name, got)
			}
		}
		if c.want == nil && len(got) != 0 {
			t.Errorf("unexpected bindings: %v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A package whose local name the file also declares stops matching there: a
// rule that fires on correct code is the kind people switch off.
func Test_shadowedNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want string
	}
	const head = "package p\nimport \"github.com/kitsunium/sdk/pkg/v1/logger\"\n"
	tests := []tc{
		{"a short variable declaration", head + "func f() { logger := 1; _ = logger }\n", "logger"},
		{"a parameter", head + "func f(logger int) { _ = logger }\n", "logger"},
		{"a range binding", head + "func f(s []int) { for logger := range s { _ = logger } }\n", "logger"},
		{"a type declaration", head + "type logger int\n", "logger"},
		{"a func declaration", head + "func logger() {}\n", "logger"},
		{"a var block", head + "var logger int\n", "logger"},
		{"nothing shadows it", head + "var v = logger.LevelInfo\n", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		got := shadowedNames(file, ctx.byPath)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("unexpected shadows: %v", got)
			}
			return
		}
		if !got[c.want] {
			t.Errorf("name %q not recorded as shadowed; got %v", c.want, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// markDeclared dispatches to the two halves; each knows part of the grammar.
func Test_markDeclared(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want string
	}
	tests := []tc{
		{"a list-binding construct", "package p\nfunc f() { a := 1; _ = a }\n", "a"},
		{"a single-binding construct", "package p\ntype b int\n", "b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, file := ctxFor(t, c.src)
		seen := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			markDeclared(n, func(e ast.Expr) {
				if id, ok := e.(*ast.Ident); ok {
					seen[id.Name] = true
				}
			})
			return true
		})
		if !seen[c.want] {
			t.Errorf("name %q not marked; got %v", c.want, seen)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// markNameLists covers the constructs binding several names at once.
func Test_markNameLists(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want []string
	}
	tests := []tc{
		{"a multi-name :=", "package p\nfunc f() { a, b := 1, 2; _, _ = a, b }\n", []string{"a", "b"}},
		{"a var spec", "package p\nvar c, d int\n", []string{"c", "d"}},
		{"a parameter list", "package p\nfunc f(e, g int) { _, _ = e, g }\n", []string{"e", "g"}},
		{"a plain = binds nothing new", "package p\nvar h int\nfunc f() { h = 1 }\n", nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, file := ctxFor(t, c.src)
		seen := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			markNameLists(n, func(e ast.Expr) {
				if id, ok := e.(*ast.Ident); ok {
					seen[id.Name] = true
				}
			})
			return true
		})
		for _, want := range c.want {
			if !seen[want] {
				t.Errorf("name %q not marked; got %v", want, seen)
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

// markSingleNames covers the constructs binding one identifier.
func Test_markSingleNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want string
	}
	tests := []tc{
		{"a type spec", "package p\ntype tname int\n", "tname"},
		{"a range key", "package p\nfunc f(s []int) { for kname := range s { _ = kname } }\n", "kname"},
		{"a func decl", "package p\nfunc fname() {}\n", "fname"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, file := ctxFor(t, c.src)
		seen := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			markSingleNames(n, func(e ast.Expr) {
				if id, ok := e.(*ast.Ident); ok {
					seen[id.Name] = true
				}
			})
			return true
		})
		if !seen[c.want] {
			t.Errorf("name %q not marked; got %v", c.want, seen)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A file no build includes, or one a generator owns, is not the consumer's to
// fix — reporting it is noise they cannot action.
func Test_skipFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want bool
	}
	tests := []tc{
		{"build-ignored", "//go:build ignore\n\npackage p\n", true},
		{"generated", "// Code generated by x. DO NOT EDIT.\n\npackage p\n", true},
		{"platform-constrained", "//go:build linux\n\npackage p\n", false},
		{"ordinary", "// An ordinary file.\npackage p\n", false},
		{
			// Both markers only mean anything in the header.
			"a generated marker below the package clause",
			"package p\n\n// Code generated by x. DO NOT EDIT.\nvar v = 1\n", false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, file := ctxFor(t, c.src)
		if got := skipFile(file); got != c.want {
			t.Errorf("skipFile = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The constraint is evaluated with every tag satisfied except "ignore", so
// only a file excluded from EVERY build evaluates false.
func Test_excludedByBuildTag(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		text string
		want bool
	}
	tests := []tc{
		{"ignore", "//go:build ignore", true},
		{"ignore in a conjunction", "//go:build ignore && linux", true},
		{"a platform tag", "//go:build linux", false},
		{"a negated platform tag", "//go:build !windows", false},
		{"an ordinary comment", "// just a comment", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := excludedByBuildTag(c.text); got != c.want {
			t.Errorf("excludedByBuildTag(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// An aliased import must resolve exactly like a plain one, or a rule is one
// rename away from silence; a dot import resolves to nothing at all.
func Test_importsOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		src       string
		wantLocal string
		wantDot   bool
	}
	tests := []tc{
		{"a plain import", "package p\nimport \"log/slog\"\n", "slog", false},
		{"an aliased import", "package p\nimport sl \"log/slog\"\n", "sl", false},
		{"a blank import binds nothing", "package p\nimport _ \"log/slog\"\n", "", false},
		{"a dot import is recorded separately", "package p\nimport . \"log/slog\"\n", "", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, file := ctxFor(t, c.src)
		byPath, dots := importsOf(file)
		if got := byPath[slogPath]; got != c.wantLocal {
			t.Errorf("local name = %q, want %q", got, c.wantLocal)
		}
		if _, dotted := dots[slogPath]; dotted != c.wantDot {
			t.Errorf("dot-imported = %v, want %v", dotted, c.wantDot)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A directive without a reason is ignored on purpose: an exemption nobody had
// to justify is the kind that outlives the reason it was granted for.
func Test_parseSuppression(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		text   string
		wantID string
		wantOK bool
	}
	tests := []tc{
		{"a directive with a reason", "//sdkguard:allow SDK001 vendor contract", "SDK001", true},
		{"lowercase is accepted", "//sdkguard:allow sdk001 vendor contract", "SDK001", true},
		{"a bare directive does not suppress", "//sdkguard:allow SDK001", "", false},
		{"an ordinary comment", "// just a comment", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, ok := parseSuppression(c.text)
		if ok != c.wantOK || id != c.wantID {
			t.Errorf("parseSuppression = %q/%v, want %q/%v", id, ok, c.wantID, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// suppressionsOf keys the exemptions by the line the directive sits on.
func Test_suppressionsOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		src      string
		wantLine int
		wantRule string
	}
	tests := []tc{
		{
			"a trailing directive",
			"package p\nvar v = 1 //sdkguard:allow SDK001 reason\n", 2, "SDK001",
		},
		{
			"a directive on its own line",
			"package p\n//sdkguard:allow SDK002 reason\nvar v = 1\n", 2, "SDK002",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "f.go", c.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got := suppressionsOf(fset, file)
		if !got[c.wantLine][c.wantRule] {
			t.Errorf("no %s exemption on line %d; got %v", c.wantRule, c.wantLine, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A directive covers its own line or the one below it, matching how //nolint
// is already written.
func Test_isSuppressed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		line int
		rule string
		want bool
	}
	const src = "package p\n//sdkguard:allow SDK001 reason\nvar v = 1\n"
	tests := []tc{
		{"the directive's own line", src, 2, "SDK001", true},
		{"the line below it", src, 3, "SDK001", true},
		{"two lines below", src, 4, "SDK001", false},
		{"a different rule", src, 3, "SDK002", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, _ := ctxFor(t, c.src)
		finding := new(findingEntity)
		finding.Line, finding.Rule = c.line, c.rule
		if got := ctx.isSuppressed(finding); got != c.want {
			t.Errorf("isSuppressed = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A selector matches only when the file imports the path under that exact
// qualifier, and the qualifier is not also bound as a local.
func Test_isSelector(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want bool
	}
	tests := []tc{
		{"a plain import", "package p\nimport \"log/slog\"\nvar v = slog.LevelInfo\n", true},
		{"an aliased import", "package p\nimport sl \"log/slog\"\nvar v = sl.LevelInfo\n", true},
		{
			"a same-named local in a file that does not import it",
			"package p\ntype t struct{ LevelInfo int }\nfunc f(slog t) { _ = slog.LevelInfo }\n", false,
		},
		{
			"a shadowed package name",
			"package p\nimport \"log/slog\"\ntype t struct{ LevelInfo int }\n" +
				"func f() { slog := t{}; _ = slog.LevelInfo }\n", false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			if expr, ok := n.(ast.Expr); ok && ctx.isSelector(expr, slogPath, "LevelInfo") {
				found = true
			}
			return true
		})
		if found != c.want {
			t.Errorf("isSelector matched = %v, want %v", found, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// isCall narrows isSelector to the callee position: a bare reference is
// vocabulary, not construction.
func Test_isCall(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want bool
	}
	tests := []tc{
		{"a call", "package p\nimport \"log/slog\"\nvar v = slog.Default()\n", true},
		{"a bare reference", "package p\nimport \"log/slog\"\nvar v = slog.Default\n", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			if ctx.isCall(n, slogPath, "Default") {
				found = true
			}
			return true
		})
		if found != c.want {
			t.Errorf("isCall matched = %v, want %v", found, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// imports is the cheap gate a rule checks before walking the whole AST.
func Test_imports(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		path string
		want bool
	}
	tests := []tc{
		{"imported", "package p\nimport \"log/slog\"\n", slogPath, true},
		{"not imported", "package p\n", slogPath, false},
		{"a blank import is not callable", "package p\nimport _ \"log/slog\"\n", slogPath, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, _ := ctxFor(t, c.src)
		if got := ctx.imports(c.path); got != c.want {
			t.Errorf("imports(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// at flattens the position so the finding stays narrow enough to pass by value.
func Test_at(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		src      string
		wantLine int
	}
	tests := []tc{
		{"a node on the first line", "package p\n", 1},
		{"a node further down", "package p\n\n\nvar v = 1\n", 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, file := ctxFor(t, c.src)
		node := ast.Node(file)
		if c.wantLine > 1 {
			node = firstNode(file, func(n ast.Node) bool {
				_, ok := n.(*ast.ValueSpec)
				return ok
			})
		}
		got := ctx.at(node, "SDK001", "msg")
		if got.Line != c.wantLine {
			t.Errorf("line = %d, want %d", got.Line, c.wantLine)
		}
		if got.Rule != "SDK001" || got.Message != "msg" {
			t.Errorf("finding = %+v, want rule SDK001 / message msg", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A rule that cannot see is not a rule that found nothing, and only the first
// deserves a clean run.
func Test_blindSpot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		want int
	}
	tests := []tc{
		{"a dot import is reported", "package p\nimport . \"log/slog\"\n", 1},
		{"a plain import is not", "package p\nimport \"log/slog\"\n", 0},
		{"an absent import is not", "package p\n", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx, _ := ctxFor(t, c.src)
		got := ctx.blindSpot(slogPath, "SDK001")
		if len(got) != c.want {
			t.Errorf("findings = %d, want %d", len(got), c.want)
		}
		if len(got) > 0 && !strings.Contains(got[0].Message, "dot-imported") {
			t.Errorf("message does not name the cause: %q", got[0].Message)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The report order must be total, or two findings on one line would come out
// in an order that depends on the sort's internals rather than on the source.
func Test_compareFindings(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b findingEntity
		want int
	}
	tests := []tc{
		{"file decides first", findingEntity{File: "a.go"}, findingEntity{File: "b.go"}, -1},
		{"then the line", findingEntity{File: "a.go", Line: 1}, findingEntity{File: "a.go", Line: 2}, -1},
		{
			"then the column",
			findingEntity{File: "a.go", Line: 1, Column: 1},
			findingEntity{File: "a.go", Line: 1, Column: 2},
			-1,
		},
		{
			"then the rule",
			findingEntity{File: "a.go", Line: 1, Column: 1, Rule: "SDK001"},
			findingEntity{File: "a.go", Line: 1, Column: 1, Rule: "SDK002"},
			-1,
		},
		{
			"identical findings compare equal",
			findingEntity{File: "a.go", Line: 1, Column: 1, Rule: "SDK001"},
			findingEntity{File: "a.go", Line: 1, Column: 1, Rule: "SDK001"},
			0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := compareFindings(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got == 0) != (c.want == 0) {
			t.Errorf("compareFindings = %d, want sign of %d", got, c.want)
		}
		// The order must be antisymmetric, or a sort could loop.
		if back := compareFindings(c.b, c.a); (got < 0) != (back > 0) {
			t.Errorf("not antisymmetric: %d then %d", got, back)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// normalizeRoot must not turn a path into the working directory: trimming the
// trailing separator blindly made "sdkguard /" scan the cwd.
func Test_normalizeRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"empty means here", "", "."},
		{"ellipsis alone means here", "...", "."},
		{"dot-slash-ellipsis", "./...", "."},
		{"filesystem root survives", "/", "/"},
		{"recursive filesystem root survives", "/...", "/"},
		{"relative dir loses the trailing separator", "foo/...", "foo"},
		{"absolute dir", "/tmp/x/...", "/tmp/x"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := normalizeRoot(c.in); got != c.want {
			t.Errorf("normalizeRoot(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// scanRoots accepts the `./...` spelling and orders findings globally, so a
// diff of two runs is readable.
func Test_scanRoots(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		ellipsis  bool
		wantFirst string
		wantCount int
	}
	tests := []tc{
		{"the ./... spelling", true, "b.go", 2},
		{"a bare directory", false, "b.go", 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		nested := filepath.Join(dir, "sub")
		if err := os.MkdirAll(nested, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// b.go sorts before sub/a.go, so the order is not the walk's.
		if err := os.WriteFile(filepath.Join(dir, "b.go"),
			[]byte("package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.WriteFile(filepath.Join(nested, "a.go"),
			[]byte("package q\nimport \"fmt\"\nvar _ = fmt.Errorf(\"x\")\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		root := dir
		if c.ellipsis {
			root = dir + "/..."
		}
		got, err := scanRoots([]string{root}, allRules, false)
		if err != nil {
			t.Fatalf("scanRoots: %v", err)
		}
		if len(got) != c.wantCount {
			t.Fatalf("findings = %d, want %d (%v)", len(got), c.wantCount, idsOf(got))
		}
		if !strings.HasSuffix(got[0].File, c.wantFirst) {
			t.Errorf("first finding = %s, want %s", got[0].File, c.wantFirst)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	// An unreadable root is a tool error the caller must see.
	if _, err := scanRoots([]string{filepath.Join(t.TempDir(), "absent")}, allRules, false); err == nil {
		t.Error("missing root accepted silently")
	}
}

// An unknown level is a typo the caller must see, not a silent empty run.
func Test_filterLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		level   string
		want    int
		wantErr bool
	}
	tests := []tc{
		{"an empty level keeps them all", "", len(allRules), false},
		{"invariants only", LevelInvariant, 3, false},
		{"conventions only", LevelConvention, 2, false},
		{"case is normalised", "INVARIANT", 3, false},
		{"an unknown level is refused", "nonsense", 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := filterLevel(allRules, c.level)
		if c.wantErr {
			if err == nil {
				t.Error("unknown level accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("filterLevel: %v", err)
		}
		if len(got) != c.want {
			t.Errorf("rules = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// An unknown rule ID is a typo the caller must see, for the same reason.
func Test_selectRules(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		spec    string
		want    int
		wantErr bool
	}
	tests := []tc{
		{"an empty spec means every rule", "", len(allRules), false},
		{"one rule", "SDK001", 1, false},
		{"case is normalised", "sdk001", 1, false},
		{"several rules", "SDK001,SDK003", 2, false},
		{"whitespace is trimmed", " SDK001 , SDK003 ", 2, false},
		{"an unknown ID is refused", "SDK999", 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := selectRules(c.spec)
		if c.wantErr {
			if err == nil {
				t.Error("unknown rule accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("selectRules: %v", err)
		}
		if len(got) != c.want {
			t.Errorf("rules = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// mustSelect composes the two selection flags; both failures exit, so this
// covers the paths that return.
func Test_mustSelect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		rules string
		level string
		want  int
	}
	tests := []tc{
		{"no narrowing", "", "", len(allRules)},
		{"by level", "", LevelInvariant, 3},
		{"by ID", "SDK001", "", 1},
		{"by both", "SDK001,SDK002", LevelInvariant, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := mustSelect(c.rules, c.level); len(got) != c.want {
			t.Errorf("rules = %d, want %d", len(got), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The report format is what editors and CI annotators parse, so it is pinned.
func Test_report(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		findings []findingEntity
		want     []string
	}
	tests := []tc{
		{
			"one finding",
			[]findingEntity{{File: "a.go", Line: 3, Column: 7, Rule: "SDK001", Message: "msg"}},
			[]string{"a.go:3:7: SDK001: msg", "1 finding(s)"},
		},
		{
			"the summary counts them",
			[]findingEntity{
				{File: "a.go", Line: 1, Column: 1, Rule: "SDK001", Message: "one"},
				{File: "b.go", Line: 2, Column: 2, Rule: "SDK002", Message: "two"},
			},
			[]string{"a.go:1:1: SDK001: one", "b.go:2:2: SDK002: two", "2 finding(s)"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		report(&buf, c.findings)
		for _, want := range c.want {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("output %q lacks %q", buf.String(), want)
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

// -list documents the tool itself, so every rule must appear with its level.
func Test_printRules(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"the first rule", "SDK001"},
		{"the last rule", "SDK005"},
		{"the invariant level", LevelInvariant},
		{"the convention level", LevelConvention},
		{"an ADR reference", "ADR 0032"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		printRules(&buf)
		if !strings.Contains(buf.String(), c.want) {
			t.Errorf("table lacks %q:\n%s", c.want, buf.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Every rule must carry a level and a decision record, so a finding can always
// be traced back to what motivated it.
func Test_allRules(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func(t *testing.T, r ruleEntity)
	}
	tests := []tc{
		{"a level", func(t *testing.T, r ruleEntity) {
			t.Helper()
			if r.Level != LevelInvariant && r.Level != LevelConvention {
				t.Errorf("%s: level = %q", r.ID, r.Level)
			}
		}},
		{"a decision record", func(t *testing.T, r ruleEntity) {
			t.Helper()
			if !strings.Contains(r.Source, "ADR") && !strings.Contains(r.Source, "CLAUDE.md") {
				t.Errorf("%s: source %q names no decision record", r.ID, r.Source)
			}
		}},
		{"a check", func(t *testing.T, r ruleEntity) {
			t.Helper()
			if r.Check == nil {
				t.Errorf("%s: no check", r.ID)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for _, r := range allRules {
			c.check(t, r)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Only strict releases are candidates: suggesting a prerelease or a
// pseudo-version as "the latest" would be wrong advice.
func Test_parseSemver(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		ok   bool
	}
	tests := []tc{
		{"a release", "v0.1.24", true},
		{"zero", "v0.0.0", true},
		{"no v prefix", "0.1.24", false},
		{"a prerelease", "v1.0.0-rc1", false},
		{"a pseudo-version", "v0.0.0-20260101120000-abcdef123456", false},
		{"two components", "v1.2", false},
		{"negative", "v1.-2.0", false},
		{"non-numeric", "v1.x.0", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, _, _, ok := parseSemver(c.in); ok != c.ok {
			t.Errorf("parseSemver(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Ordering must be numeric, not lexical: v0.1.9 precedes v0.1.24, and getting
// that backwards would tell an up-to-date consumer to downgrade.
func Test_semverLess(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		a, b string
		want bool
	}
	tests := []tc{
		{"patch, double digits", "v0.1.9", "v0.1.24", true},
		{"patch, reversed", "v0.1.24", "v0.1.9", false},
		{"equal", "v1.2.3", "v1.2.3", false},
		{"minor wins over patch", "v0.1.99", "v0.2.0", true},
		{"major wins over minor", "v0.99.0", "v1.0.0", true},
		{"an unparseable a sorts first", "v0.0.0-2026-abcdef", "v0.1.0", true},
		{"nothing is newer than an unparseable b", "v0.1.0", "not-a-version", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// sortVersions puts the newest last, which is what lets versionNotice name it.
func Test_sortVersions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []string
		want []string
	}
	tests := []tc{
		{"already ordered", []string{"v0.1.9", "v0.1.24"}, []string{"v0.1.9", "v0.1.24"}},
		{"reversed", []string{"v0.1.24", "v0.1.9"}, []string{"v0.1.9", "v0.1.24"}},
		{"across components", []string{"v1.0.0", "v0.2.0", "v0.1.99"}, []string{"v0.1.99", "v0.2.0", "v1.0.0"}},
		{"empty", nil, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := slices.Clone(c.in)
		sortVersions(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("sortVersions = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// newerThan keeps only what sorts strictly after the requirement.
func Test_newerThan(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cur      string
		versions []string
		want     int
	}
	tests := []tc{
		{"two newer", "v0.1.9", []string{"v0.1.9", "v0.1.10", "v0.1.24"}, 2},
		{"on the latest", "v0.1.24", []string{"v0.1.9", "v0.1.24"}, 0},
		{"ahead of the proxy", "v0.2.0", []string{"v0.1.24"}, 0},
		{"nothing published", "v0.1.0", nil, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := newerThan(c.cur, c.versions); len(got) != c.want {
			t.Errorf("newerThan = %v, want %d entries", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The notice names the gap's kind because that is what conveys urgency, and
// the exact go get line because a nudge without a next step is just noise.
func Test_versionNotice(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		cur   string
		newer []string
		want  string
	}
	tests := []tc{
		{"the count", "v0.1.9", []string{"v0.1.10", "v0.1.24"}, "2 patch releases behind"},
		{"the latest", "v0.1.9", []string{"v0.1.24"}, "latest is v0.1.24"},
		{"the upgrade command", "v0.1.9", []string{"v0.1.24"}, "go get " + sdkModule + "@v0.1.24"},
		{"the way to silence it", "v0.1.9", []string{"v0.1.24"}, "-version-check=off"},
		{"the release policy", "v0.1.9", []string{"v0.1.24"}, "ADR 0007"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := versionNotice(c.cur, c.newer); !strings.Contains(got, c.want) {
			t.Errorf("notice lacks %q:\n%s", c.want, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// gapKind names the largest component that moved.
func Test_gapKind(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		cur, latest string
		want        string
	}
	tests := []tc{
		{"patches", "v0.1.9", "v0.1.24", "patch releases"},
		{"a minor gap", "v0.1.0", "v0.2.0", "crossing a minor version"},
		{"a major gap", "v0.9.0", "v1.0.0", "crossing a MAJOR version"},
		{"an unparseable current", "dev", "v0.1.0", "releases"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := gapKind(c.cur, c.latest); !strings.Contains(got, c.want) {
			t.Errorf("gapKind(%q, %q) = %q, want it to contain %q", c.cur, c.latest, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The go.mod reader must handle the shapes a real file uses. Reading a block
// replace as a require turned "pkg => ../sdk/pkg" into a requirement on the
// version "=>", which sorts below every real tag.
func Test_parseGoMod(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		gomod       string
		wantVersion string
		wantReplace bool
		wantFound   bool
	}
	tests := []tc{
		{
			"a require block",
			"module x\n\ngo 1.27\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.24\n)\n",
			"v0.1.24", false, true,
		},
		{
			"a single-line require",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.20\n",
			"v0.1.20", false, true,
		},
		{
			"a single-line replace",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.9\n" +
				"replace github.com/kitsunium/sdk/pkg => ../sdk/pkg\n",
			"v0.1.9", true, true,
		},
		{
			"a replace block",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.24\n)\n\n" +
				"replace (\n\tgithub.com/kitsunium/sdk/pkg => ../sdk/pkg\n)\n",
			"v0.1.24", true, true,
		},
		{
			"the SDK as the TARGET of a replace is not a replacement of it",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.9\n" +
				"replace example.com/fork => github.com/kitsunium/sdk/pkg v0.1.24\n",
			"v0.1.9", false, true,
		},
		{
			"an indirect marker",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg v0.1.9 // indirect\n)\n",
			"v0.1.9", false, true,
		},
		{
			"a commented-out directive is not a directive",
			"module x\n\nrequire github.com/kitsunium/sdk/pkg v0.1.24\n" +
				"// replace github.com/kitsunium/sdk/pkg => ../x\n",
			"v0.1.24", false, true,
		},
		{
			"a non-version second field is not a version",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/pkg => ../x\n)\n",
			"", false, false,
		},
		{"no SDK requirement", "module x\n\nrequire example.com/other v1.0.0\n", "", false, false},
		{
			"the internal modules are not the public one",
			"module x\n\nrequire (\n\tgithub.com/kitsunium/sdk/internal/core v0.1.24 // indirect\n)\n",
			"", false, false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ref, ok := parseGoMod(c.gomod)
		if ok != c.wantFound {
			t.Fatalf("found = %v, want %v", ok, c.wantFound)
		}
		if !ok {
			return
		}
		if ref.Version != c.wantVersion {
			t.Errorf("version = %q, want %q", ref.Version, c.wantVersion)
		}
		if ref.Replaced != c.wantReplace {
			t.Errorf("replaced = %v, want %v", ref.Replaced, c.wantReplace)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stripComment keeps a "// indirect" marker or a commented-out directive from
// being read as content.
func Test_stripComment(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"no comment", "\trequire x v1.0.0", "require x v1.0.0"},
		{"a trailing marker", "\trequire x v1.0.0 // indirect", "require x v1.0.0"},
		{"a whole-line comment", "// replace x => ../x", ""},
		{"an empty line", "   ", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// readGoModLine tracks which block is open across lines.
func Test_readGoModLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		section gomodSection
		line    string
		want    gomodSection
	}
	tests := []tc{
		{"a require block opens", sectionNone, "require (", sectionRequire},
		{"a replace block opens", sectionNone, "replace (", sectionReplace},
		{"a paren closes it", sectionRequire, ")", sectionNone},
		{"a content line keeps the context", sectionRequire, "example.com/x v1.0.0", sectionRequire},
		{"a single-line form leaves it alone", sectionNone, "require example.com/x v1.0.0", sectionNone},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var ref moduleRef
		if got := readGoModLine(&ref, c.section, c.line); got != c.want {
			t.Errorf("readGoModLine = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// readRequire records a version only from a well-formed SDK entry.
func Test_readRequire(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		entry string
		want  string
	}
	tests := []tc{
		{"the SDK", sdkModule + " v0.1.24", "v0.1.24"},
		{"another module", "example.com/x v1.0.0", ""},
		{"too few fields", sdkModule, ""},
		{"a second field that is not a version", sdkModule + " => ../x", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var ref moduleRef
		readRequire(&ref, c.entry)
		if ref.Version != c.want {
			t.Errorf("version = %q, want %q", ref.Version, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Direction matters: the SDK on the RIGHT of the arrow is not a replacement
// of it, and reading it as one would silence a consumer who is genuinely behind.
func Test_readReplace(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		entry string
		want  bool
	}
	tests := []tc{
		{"the SDK on the left", sdkModule + " => ../sdk/pkg", true},
		{"the SDK on the right", "example.com/fork => " + sdkModule + " v0.1.24", false},
		{"another module entirely", "example.com/x => ../x", false},
		{"no arrow at all", sdkModule + " v0.1.0", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var ref moduleRef
		readReplace(&ref, c.entry)
		if ref.Replaced != c.want {
			t.Errorf("replaced = %v, want %v", ref.Replaced, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// workspaceUses reads both go.work shapes.
func Test_workspaceUses(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		text string
		want []string
	}
	tests := []tc{
		{"a use block", "go 1.27\n\nuse (\n\t./m1\n\t./m2\n)\n", []string{"./m1", "./m2"}},
		{"a single-line use", "go 1.27\n\nuse ./m1\n", []string{"./m1"}},
		{"a commented entry is skipped", "use (\n\t// ./m1\n\t./m2\n)\n", []string{"./m2"}},
		{"no use directive", "go 1.27\n", nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := workspaceUses(c.text); !slices.Equal(got, c.want) {
			t.Errorf("workspaceUses = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// findUp walks up so the tool works from a package subdirectory the way every
// other Go tool does.
func Test_findUp(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		file  string
		depth int
		want  bool
	}
	tests := []tc{
		{"in the same directory", "go.mod", 0, true},
		{"two levels up", "go.mod", 2, true},
		{"a name that is not there", "absent.txt", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := modDir(t, "module x\n")
		start := root
		for range c.depth {
			start = filepath.Join(start, "sub")
		}
		if err := os.MkdirAll(start, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, found := findUp(start, c.file)
		if found != c.want {
			t.Fatalf("found = %v, want %v", found, c.want)
		}
		if found && !strings.HasSuffix(got, c.file) {
			t.Errorf("path = %q, want it to end in %q", got, c.file)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// findGoMod is findUp bound to the file the module system uses.
func Test_findGoMod(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		write bool
		want  bool
	}
	tests := []tc{
		{"a module root", true, true},
		{"a directory outside any module", false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		if c.write {
			dir = modDir(t, "module x\n")
		}
		// A temp dir has no go.mod above it on any sane machine, but the walk
		// reaches the filesystem root, so assert only on the positive case.
		got, found := findGoMod(dir)
		if c.want && (!found || !strings.HasSuffix(got, "go.mod")) {
			t.Errorf("findGoMod = %q/%v, want a go.mod", got, found)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// sdkRequirement locates the go.mod and hands its text to the parser.
func Test_sdkRequirement(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		gomod string
		depth int
		want  string
	}
	tests := []tc{
		{"in the module root", "module x\n\nrequire " + sdkModule + " v0.1.24\n", 0, "v0.1.24"},
		{"from a subdirectory", "module x\n\nrequire " + sdkModule + " v0.1.1\n", 2, "v0.1.1"},
		{"no SDK requirement", "module x\n", 0, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := modDir(t, c.gomod)
		start := root
		for range c.depth {
			start = filepath.Join(start, "sub")
		}
		if err := os.MkdirAll(start, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		ref, ok := sdkRequirement(start)
		if c.want == "" {
			if ok {
				t.Errorf("unexpected requirement %+v", ref)
			}
			return
		}
		if !ok || ref.Version != c.want {
			t.Errorf("version = %q/%v, want %q", ref.Version, ok, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A workspace root has no go.mod of its own, so the documented `sdkguard ./...`
// form from there would scan every module and warn about none of them.
func Test_workspaceRequirement(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		files   map[string]string
		want    string
		wantAny bool
	}
	tests := []tc{
		{
			// The OLDEST wins: warning about the newest would let a stale
			// module hide behind an up-to-date sibling.
			"the oldest module wins",
			map[string]string{
				"go.work":   "go 1.27\n\nuse (\n\t./m1\n\t./m2\n)\n",
				"m1/go.mod": "module m1\n\nrequire " + sdkModule + " v0.1.20\n",
				"m2/go.mod": "module m2\n\nrequire " + sdkModule + " v0.1.9\n",
			},
			"v0.1.9", true,
		},
		{
			"a replace anywhere silences the whole workspace",
			map[string]string{
				"go.work":   "go 1.27\n\nuse ./m1\n",
				"m1/go.mod": "module m1\n\nrequire " + sdkModule + " v0.1.9\nreplace " + sdkModule + " => ../sdk/pkg\n",
			},
			"", false,
		},
		{
			"a workspace whose modules do not use the SDK",
			map[string]string{
				"go.work":   "go 1.27\n\nuse ./m1\n",
				"m1/go.mod": "module m1\n",
			},
			"", false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		for rel, content := range c.files {
			full := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}
		ref, ok := workspaceRequirement(root)
		if ok != c.wantAny {
			t.Fatalf("found = %v, want %v (%+v)", ok, c.wantAny, ref)
		}
		if ok && ref.Version != c.want {
			t.Errorf("version = %q, want %q", ref.Version, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// isProxySeparator accepts both spellings of the GOPROXY fallback list.
func Test_isProxySeparator(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   rune
		want bool
	}
	tests := []tc{
		{"comma", ',', true},
		{"pipe", '|', true},
		{"a letter", 'a', false},
		{"a slash", '/', false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := isProxySeparator(c.in); got != c.want {
			t.Errorf("isProxySeparator(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// classifyProxy reads one entry the way the go command reads it.
func Test_classifyProxy(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		entry      string
		wantURL    string
		wantUsable bool
		wantStop   bool
	}
	tests := []tc{
		{"an https endpoint", "https://a.example", "https://a.example", true, false},
		{"a trailing slash is trimmed", "https://a.example/", "https://a.example", true, false},
		{"http is accepted too", "http://a.example", "http://a.example", true, false},
		{"off stops the scan", "off", "", false, true},
		{"direct is skipped", "direct", "", false, false},
		{"an empty entry is skipped", "", "", false, false},
		{"an unaddressable entry is skipped", "nonsense", "", false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		url, usable, stop := classifyProxy(c.entry)
		if url != c.wantURL || usable != c.wantUsable || stop != c.wantStop {
			t.Errorf("classifyProxy(%q) = %q/%v/%v, want %q/%v/%v",
				c.entry, url, usable, stop, c.wantURL, c.wantUsable, c.wantStop)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// GOPROXY is read the way the go command reads it, fallback list included:
// returning only the first entry made the documented support a fiction.
func Test_resolveProxies(t *testing.T) {
	type tc struct {
		name    string
		env     string
		want    []string
		enabled bool
	}
	tests := []tc{
		{"unset falls back to the default", "", []string{defaultProxy}, true},
		{"off disables the probe", "off", nil, false},
		{"direct alone disables it", "direct", nil, false},
		{
			"a comma list keeps every URL in order",
			"https://a.example,https://b.example,direct",
			[]string{"https://a.example", "https://b.example"},
			true,
		},
		{
			"a pipe list keeps every URL in order",
			"https://b.example|https://c.example",
			[]string{"https://b.example", "https://c.example"},
			true,
		},
		{"off ahead of a URL still disables", "off,https://e.example", nil, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		// No t.Parallel: this mutates a process-wide environment variable.
		t.Setenv("GOPROXY", c.env)
		got, enabled := resolveProxies()
		if enabled != c.enabled || !slices.Equal(got, c.want) {
			t.Errorf("resolveProxies() = %v/%v, want %v/%v", got, enabled, c.want, c.enabled)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// fetchVersions keeps only the strict releases a comparison can use.
func Test_fetchVersions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		published []string
		want      int
	}
	tests := []tc{
		{"two releases", []string{"v0.1.9", "v0.1.24"}, 2},
		{"a prerelease is dropped", []string{"v0.1.24", "v0.2.0-rc1"}, 1},
		{"a pseudo-version is dropped", []string{"v0.1.24", "v0.0.0-20260101120000-abcdef123456"}, 1},
		{"nothing published", nil, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stub := proxyStub(t, c.published...)
		got, err := fetchVersions(stub.client, stub.proxy, sdkModule)
		if err != nil {
			t.Fatalf("fetchVersions: %v", err)
		}
		if len(got) != c.want {
			t.Errorf("releases = %v, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	// An unreachable proxy is the caller's cue to try the next one.
	if _, err := fetchVersions(&http.Client{}, "http://127.0.0.1:9", sdkModule); err == nil {
		t.Error("unreachable proxy reported success")
	}
}

// versions walks the fallback list until one proxy answers.
func Test_versions(t *testing.T) {
	type tc struct {
		name     string
		proxyEnv string
		useStub  bool
		wantErr  bool
	}
	tests := []tc{
		{"an explicit proxy answers", "", true, false},
		{"a dead primary falls through to the backup", "dead-then-stub", false, false},
		{"GOPROXY=off makes no request at all", "off", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stub := proxyStub(t, "v0.1.24")
		p := probe{client: stub.client}
		switch {
		case c.useStub:
			p.proxy = stub.proxy
		case c.proxyEnv == "dead-then-stub":
			t.Setenv("GOPROXY", "http://127.0.0.1:9,"+stub.proxy)
		default:
			t.Setenv("GOPROXY", c.proxyEnv)
		}

		got, err := p.versions(sdkModule)
		if c.wantErr {
			if err == nil {
				t.Error("expected the probe to be disabled")
			}
			return
		}
		if err != nil {
			t.Fatalf("versions: %v", err)
		}
		if len(got) != 1 || got[0] != "v0.1.24" {
			t.Errorf("versions = %v, want [v0.1.24]", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// The end-to-end probe: outdated warns, current says nothing, and every
// failure path degrades to silence.
func Test_checkVersion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		required  string
		published []string
		wantStale bool
		wantText  string
	}
	tests := []tc{
		{"behind by patches", "v0.1.9", []string{"v0.1.9", "v0.1.10", "v0.1.24"}, true, "2 patch releases behind"},
		{"on the latest release", "v0.1.24", []string{"v0.1.9", "v0.1.24"}, false, ""},
		{"ahead of the proxy", "v0.2.0", []string{"v0.1.24"}, false, ""},
		{"a major gap says so", "v0.9.0", []string{"v0.9.0", "v1.0.0"}, true, "crossing a MAJOR version"},
		{"a minor gap says so", "v0.1.0", []string{"v0.1.0", "v0.2.0"}, true, "crossing a minor version"},
		{
			"prereleases are not candidates",
			"v0.1.24",
			[]string{"v0.1.24", "v0.2.0-rc1", "v0.0.0-20260101120000-abcdef123456"},
			false, "",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := modDir(t, "module x\n\nrequire "+sdkModule+" "+c.required+"\n")
		notice, stale := checkVersion(dir, proxyStub(t, c.published...))
		if stale != c.wantStale {
			t.Fatalf("stale = %v, want %v (notice: %q)", stale, c.wantStale, notice)
		}
		if c.wantText != "" && !strings.Contains(notice, c.wantText) {
			t.Errorf("notice %q does not contain %q", notice, c.wantText)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// A freshness nudge that breaks a build has failed at being a nudge, so every
// failure path returns silence.
func Test_checkVersionDegradesSilently(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		gomod string
		dead  bool
	}
	tests := []tc{
		{"no go.mod at all", "", false},
		{"a go.mod with no SDK requirement", "module x\n", false},
		{"a replaced module", "module x\n\nrequire " + sdkModule + " v0.1.0\nreplace " + sdkModule + " => ../x\n", false},
		{"an unreachable proxy", "module x\n\nrequire " + sdkModule + " v0.1.0\n", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		if c.gomod != "" {
			dir = modDir(t, c.gomod)
		}
		p := proxyStub(t, "v9.9.9")
		if c.dead {
			p = probe{proxy: "http://127.0.0.1:9", client: &http.Client{}}
		}
		if notice, stale := checkVersion(dir, p); stale {
			t.Errorf("warned when it should have stayed silent: %q", notice)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The mode flag is validated before any work, so a typo costs no network.
func Test_reportVersion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		mode    string
		wantErr bool
	}
	tests := []tc{
		{"off skips the probe", versionCheckOff, false},
		{"warn is accepted", versionCheckWarn, false},
		{"error is accepted", versionCheckError, false},
		{"anything else is refused", "bogus", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stale, err := reportVersion(t.TempDir(), c.mode)
		if c.wantErr {
			if err == nil {
				t.Error("unknown mode accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("reportVersion: %v", err)
		}
		// A temp dir holds no SDK requirement, so nothing is ever stale here.
		if stale {
			t.Error("reported stale for a directory with no requirement")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Every root gets a turn, so a stale requirement in the second module of a
// multi-module invocation is not silently ignored.
func Test_reportVersions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		mode    string
		roots   int
		wantErr bool
	}
	tests := []tc{
		{"one root", versionCheckOff, 1, false},
		{"several roots", versionCheckOff, 3, false},
		{"an unknown mode is refused", "bogus", 1, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		roots := make([]string, 0, c.roots)
		for range c.roots {
			roots = append(roots, t.TempDir())
		}
		stale, err := reportVersions(roots, c.mode)
		if c.wantErr {
			if err == nil {
				t.Error("unknown mode accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("reportVersions: %v", err)
		}
		if stale {
			t.Error("reported stale for directories with no requirement")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// inspect decides the exit code: findings always fail, and being behind only
// fails under -version-check=error.
func Test_inspect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  string
		mode string
		want bool
	}
	tests := []tc{
		{"a clean tree passes", "package p\nvar x = 1\n", versionCheckOff, false},
		{
			"a violation fails",
			"package p\nimport \"log\"\nfunc f() { log.Print(\"x\") }\n", versionCheckOff, true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := write(t, "fixture.go", c.src)
		// report writes to stderr; the test only reads the decision.
		if got := inspect([]string{dir}, allRules, false, c.mode); got != c.want {
			t.Errorf("inspect = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
