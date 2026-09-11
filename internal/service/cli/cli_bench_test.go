package cli_test

import (
	"context"
	"flag"
	"io"
	"slices"
	"strconv"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// benchFlagCount is how many flags a realistic command declares. Five is what
// a `serve` sub-command tends to carry: address, port, timeout, verbosity, and
// one path.
const benchFlagCount int = 5

// benchTreeWidth is how many siblings each level of the nested tree carries,
// so the linear name scan is measured against a realistic fan-out rather than
// against two.
const benchTreeWidth int = 8

// benchSink keeps a returned error alive so neither the compiler nor the
// linter treats the call as discardable. Benchmarks here are single-goroutine,
// so a package-level sink is safe in a way it would not be in the concurrency
// tests.
var benchSink error

// benchMapSink keeps a produced config layer alive for the same reason.
var benchMapSink map[string]any

// benchArgs is the vector every parse benchmark feeds in: three of the five
// flags supplied, two left at their defaults, plus one positional.
var benchArgs = []string{"-f0", "1", "-f2", "3", "-f4", "5", "positional"}

// benchBinder declares benchFlagCount int flags into storage it allocates
// itself, so the benchmark measures the domain and not a shared write.
func benchBinder(flags *flag.FlagSet) {
	for i := range benchFlagCount {
		flags.Int("f"+strconv.Itoa(i), 0, "an `n`")
	}
}

// benchLeaf is the command every parse benchmark ends at.
func benchLeaf(name string) corecli.CommandValue {
	return corecli.CommandValue{
		Name: name, Summary: "a benchmark command",
		Flags: benchBinder,
		Run:   func(context.Context, corecli.InvocationValue) error { return nil },
	}
}

// benchConfig discards both streams: a benchmark that wrote to a real stream
// would be measuring the terminal.
func benchConfig() svccli.Config {
	return svccli.Config{Output: io.Discard, ErrOutput: io.Discard}
}

// BenchmarkFlagParseBaseline is the control every other number is read
// against: package flag doing exactly the work this domain delegates to it —
// build a set, bind five flags, parse the same vector.
//
// Without it, "resolving a sub-command costs N ns" is a number with no unit.
// With it, the whole domain's overhead is one subtraction.
func BenchmarkFlagParseBaseline(b *testing.B) {
	for b.Loop() {
		set := flag.NewFlagSet("tool", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		benchBinder(set)
		benchSink = set.Parse(benchArgs)
	}
}

// BenchmarkExecuteFlatCommand is one leaf: bind, parse, dispatch. Against the
// baseline it prices everything this domain adds to a single-command tool.
func BenchmarkExecuteFlatCommand(b *testing.B) {
	app, err := svccli.New(benchConfig(), benchLeaf("tool"))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		benchSink = app.Execute(ctx, benchArgs)
	}
}

// BenchmarkExecuteNestedCommand is the same parse three levels down, behind
// two groups with eight siblings each. It prices what resolution costs: two
// extra flag sets, two linear scans of eight names, one deeper path.
func BenchmarkExecuteNestedCommand(b *testing.B) {
	app, err := svccli.New(benchConfig(), benchTree())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	args := append([]string{"db", "schema", "migrate"}, benchArgs...)
	b.ResetTimer()
	for b.Loop() {
		benchSink = app.Execute(ctx, args)
	}
}

// BenchmarkExecuteUnknownCommand prices the failure path, which renders a full
// help page before returning. It is the most expensive thing the domain does
// per invocation, and it happens at most once per process.
func BenchmarkExecuteUnknownCommand(b *testing.B) {
	app, err := svccli.New(benchConfig(), benchTree())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	args := []string{"nosuchthing"}
	b.ResetTimer()
	for b.Loop() {
		benchSink = app.Execute(ctx, args)
	}
}

// BenchmarkRenderHelp prices the generated help for a group of eight children
// that also declares five flags — the page an operator sees on -h.
func BenchmarkRenderHelp(b *testing.B) {
	app, err := svccli.New(benchConfig(), benchTree())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	args := []string{"-h"}
	b.ResetTimer()
	for b.Loop() {
		benchSink = app.Execute(ctx, args)
	}
}

// BenchmarkNew prices the whole-tree validation: 1 root + 2 groups + 24 leaves,
// every Binder called once on a throwaway set. It runs exactly once per
// process, which is the point of measuring it rather than assuming it.
func BenchmarkNew(b *testing.B) {
	tree := benchTree()
	cfg := benchConfig()
	b.ResetTimer()
	for b.Loop() {
		_, benchSink = svccli.New(cfg, tree)
	}
}

// BenchmarkFlagSource prices the config adapter over an invocation carrying
// three set flags across two sets.
func BenchmarkFlagSource(b *testing.B) {
	invocation := benchInvocation(b)
	b.ResetTimer()
	for b.Loop() {
		benchMapSink, benchSink = svccli.FlagSource(invocation).Load()
	}
}

// benchTree builds the three-level tree the nested benchmarks walk: a root
// with eight children, one of which is a group with eight children, one of
// which is a group with eight leaves.
func benchTree() corecli.CommandValue {
	leaves := make([]corecli.CommandValue, 0, benchTreeWidth)
	for i := range benchTreeWidth {
		leaves = append(leaves, benchLeaf("leaf"+strconv.Itoa(i)))
	}
	schema := corecli.CommandValue{
		Name: "schema", Summary: "schema commands",
		Commands: append(slices.Clone(leaves), benchLeaf("migrate")),
	}
	db := corecli.CommandValue{
		Name: "db", Summary: "database commands",
		Flags:    benchBinder,
		Commands: append(slices.Clone(leaves), schema),
	}
	return corecli.CommandValue{
		Name: "tool", Summary: "a benchmark tool",
		Flags:    benchBinder,
		Commands: append(slices.Clone(leaves), db),
	}
}

// benchInvocation runs the nested tree once and hands back the invocation the
// leaf saw, so BenchmarkFlagSource measures the adapter and not an Execute.
func benchInvocation(tb testing.TB) corecli.InvocationValue {
	tb.Helper()
	var captured corecli.InvocationValue
	root := benchTree()
	//: replace the target leaf's Run so the invocation can be captured.
	root.Commands[benchTreeWidth].Commands[benchTreeWidth].Commands[benchTreeWidth].Run = func(_ context.Context, in corecli.InvocationValue) error {
		captured = in
		return nil
	}
	app, err := svccli.New(benchConfig(), root)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	args := append([]string{"db", "-f1", "9", "schema", "migrate"}, benchArgs...)
	if err := app.Execute(context.Background(), args); err != nil {
		tb.Fatalf("Execute: %v", err)
	}
	return captured
}
