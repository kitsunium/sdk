package cli_test

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// TestResolutionWalksToArbitraryDepth pins the decision ADR 0065 §D3 took: the
// tree has no depth limit. One level would be a bound that every real tool
// breaks on its second release (`git remote add`), and the resolution loop
// costs exactly the same for three levels as for one — the bound would be a
// restriction the mechanism does not impose.
func TestResolutionWalksToArbitraryDepth(t *testing.T) {
	t.Parallel()
	var h harness
	rec := &recorder{}
	root := group("tool", group("db", group("schema", leaf("migrate", rec.action()))))
	if err := h.run(t, root, "db", "schema", "migrate", "--", "extra"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.called != 1 {
		t.Fatalf("the leaf ran %d times, want 1", rec.called)
	}
	wantPath := []string{"tool", "db", "schema", "migrate"}
	if !slices.Equal(rec.invocation.Path, wantPath) {
		t.Errorf("Path = %v, want %v", rec.invocation.Path, wantPath)
	}
	if !slices.Equal(rec.invocation.Args, []string{"extra"}) {
		t.Errorf("Args = %v, want [extra]", rec.invocation.Args)
	}
}

// TestFlagsBelongToTheCommandThatDeclaredThem pins the reason this domain
// needs no lookahead and no two-pass parse: package flag stops at the first
// non-flag argument, so that token is unambiguously the next command's name
// and everything before it belonged to the set that just stopped.
func TestFlagsBelongToTheCommandThatDeclaredThem(t *testing.T) {
	t.Parallel()
	var h harness
	var verbose, dryRun int
	rec := &recorder{}
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		Flags: intBinder(&verbose, "verbose", 0),
		Commands: []corecli.CommandValue{{
			Name: "migrate", Summary: "s",
			Flags: intBinder(&dryRun, "dry-run", 0),
			Run:   rec.action(),
		}},
	}
	if err := h.run(t, root, "-verbose", "2", "migrate", "-dry-run", "1", "rest"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if verbose != 2 || dryRun != 1 {
		t.Fatalf("verbose = %d, dry-run = %d; want 2 and 1", verbose, dryRun)
	}
	if len(rec.invocation.Flags) != 2 {
		t.Fatalf("Flags carries %d sets, want one per command on the path", len(rec.invocation.Flags))
	}
	if rec.invocation.Leaf() != rec.invocation.Flags[1] {
		t.Error("Leaf() must be the set of the command that is running")
	}
	if !slices.Equal(rec.invocation.Args, []string{"rest"}) {
		t.Errorf("Args = %v, want [rest]", rec.invocation.Args)
	}
}

// TestUnknownAndMissingSubCommandsAreUsageErrors pins the two ways a group can
// be typed wrong, their EX_USAGE status, and the fact that both write the
// group's help — which lists every name that would have worked. That listing
// is why ADR 0065 §D3 ships no edit-distance suggester: the complete answer is
// already on the screen and it cannot be wrong.
func TestUnknownAndMissingSubCommandsAreUsageErrors(t *testing.T) {
	t.Parallel()
	const exitUsage int = 64
	tests := []struct {
		name string
		args []string
		code kerrs.Code
	}{
		{"a token that names nothing", []string{"stat"}, svccli.CodeUnknownCommand},
		{"no sub-command at all", nil, svccli.CodeMissingCommand},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var h harness
			root := group("tool", leaf("status", noop), leaf("start", noop))
			err := h.run(t, root, tc.args...)
			if !kerrs.HasCode(err, tc.code) {
				t.Fatalf("err = %v, want %#x", err, uint32(tc.code))
			}
			if got := kerrs.ExitCodeOf(err); got != exitUsage {
				t.Errorf("exit = %d, want %d (EX_USAGE)", got, exitUsage)
			}
			requireContains(t, h.help(), "status", "the help lists every name that would have worked")
			requireContains(t, h.help(), "start", "the help lists every name that would have worked")
		})
	}
}

// TestAGroupWithNoSubCommandIsNotASuccess is the same case as above, stated as
// the property that motivates it. A group declares no action, so `tool db` did
// nothing — and a script reading exit status 0 there would treat "I forgot the
// verb" as "the migration ran".
func TestAGroupWithNoSubCommandIsNotASuccess(t *testing.T) {
	t.Parallel()
	var h harness
	if err := h.run(t, group("tool", leaf("migrate", noop))); err == nil {
		t.Fatal("a group invoked with no sub-command returned nil")
	}
}

// TestExactMatchOnly pins the absence of prefix matching and of case folding.
// Both are conveniences whose cost is that what an existing command line MEANS
// depends on which siblings exist in today's release: `tool st` resolves to
// status until somebody adds start, and then it resolves to nothing — in a
// release that changed no line of status's own code.
func TestExactMatchOnly(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"stat", "STATUS", "Status"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			var h harness
			err := h.run(t, group("tool", leaf("status", noop)), token)
			if !kerrs.HasCode(err, svccli.CodeUnknownCommand) {
				t.Errorf("%q resolved to status; names are compared by exact byte equality", token)
			}
		})
	}
}

// TestABadFlagIsAUsageErrorAndNeverFlagsOwnReport pins two things at once.
//
// The status is EX_USAGE. And package flag stayed SILENT: left at its default
// a FlagSet writes its own message and its own usage to os.Stderr before
// returning the error, so the failure would be reported twice, in two
// vocabularies, one of which the caller cannot intercept. flag's text is
// preserved — in a field and in Private, where it belongs, since it quotes the
// operator's value verbatim.
func TestABadFlagIsAUsageErrorAndNeverFlagsOwnReport(t *testing.T) {
	t.Parallel()
	const exitUsage int = 64
	var h harness
	var target int
	root := corecli.CommandValue{
		Name: "tool", Summary: "s", Flags: intBinder(&target, "n", 0), Run: noop,
	}
	err := h.run(t, root, "-n", "not-a-number")
	if !kerrs.HasCode(err, svccli.CodeInvalidFlags) {
		t.Fatalf("err = %v, want INVALID_FLAGS", err)
	}
	if got := kerrs.ExitCodeOf(err); got != exitUsage {
		t.Errorf("exit = %d, want %d (EX_USAGE)", got, exitUsage)
	}
	//: the operator's value is diagnostic-only. Public is the wire-safe half.
	if strings.Contains(kerrs.PublicOf(err), "not-a-number") {
		t.Error("Public quotes the operator's value; that is the log-only half")
	}
	requireContains(t, fieldText(err), "not-a-number", "flag's own text is preserved")
	//: the help went out; flag's own duplicate did not.
	requireContains(t, h.help(), "Flags:", "the engine wrote the help")
	if strings.Count(h.help(), "not-a-number") != 0 {
		t.Errorf("flag reported the failure itself:\n%s", h.help())
	}
}

// TestHelpIsNotAnError is the decision in one assertion: -h writes the help
// and returns nil, so `tool -h` exits 0. The same help after a bad command
// line does NOT, which is what the previous test pins — so the exit status is
// what distinguishes a question from a mistake, and the text does not have to.
func TestHelpIsNotAnError(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"-h", "--help", "-help"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			var h harness
			if err := h.run(t, group("tool", leaf("run", noop)), token); err != nil {
				t.Fatalf("%s returned %v; asking for help is not a failure", token, err)
			}
			requireContains(t, h.help(), "Commands:", "the help was written")
			if h.out.Len() != 0 {
				t.Errorf("help reached the command's output stream:\n%s", h.out.String())
			}
		})
	}
}

// TestHelpForASubCommandIsThatSubCommandsHelp pins that -h is answered by
// whichever command's set consumed it, so `tool db -h` documents db and not
// the root.
func TestHelpForASubCommandIsThatSubCommandsHelp(t *testing.T) {
	t.Parallel()
	var h harness
	root := group("tool", group("db", leaf("migrate", noop)), leaf("serve", noop))
	if err := h.run(t, root, "db", "-h"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	requireContains(t, h.help(), "tool db <command>", "the usage line names the sub-command")
	if strings.Contains(h.help(), "serve") {
		t.Errorf("the root's siblings leaked into a sub-command's help:\n%s", h.help())
	}
}

// TestAnActionsErrorIsReturnedVerbatim is the property that makes an exit
// status routable.
//
// The engine does not wrap it. Wrapping would buy nothing — origin-wins
// (CLAUDE.md rule 6) already preserves an *errs.Error's code and status — and
// would cost two things: a plain stdlib error wrapped with empty WrapParams
// comes back as INVALID_WRAP_PARAMS, relabelling a command failure as an SDK
// defect, and any wrap breaks the caller's own errors.Is on its own sentinel.
func TestAnActionsErrorIsReturnedVerbatim(t *testing.T) {
	t.Parallel()
	t.Run("a plain stdlib error", func(t *testing.T) {
		t.Parallel()
		var h harness
		sentinel := errors.New("the disk is full")
		rec := &recorder{err: sentinel}
		err := h.run(t, leaf("tool", rec.action()))
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want the caller's own sentinel", err)
		}
		if err != sentinel { //nolint:errorlint // identity is the assertion.
			t.Errorf("err was wrapped; the caller's error must come back untouched")
		}
	})
	t.Run("an errs error keeps its own exit status", func(t *testing.T) {
		t.Parallel()
		const exitTempFail int = 75
		var h harness
		//: a downstream code in the third-party Major range ADR 0019 reserves.
		own := kerrs.Define(0x40_01_01_01, "UPSTREAM_UNAVAILABLE",
			"the upstream is unavailable", "private", kerrs.WithExitCode(exitTempFail))
		rec := &recorder{err: own}
		err := h.run(t, leaf("tool", rec.action()))
		if got := kerrs.ExitCodeOf(err); got != exitTempFail {
			t.Errorf("exit = %d, want %d — a command's own status is never overwritten", got, exitTempFail)
		}
	})
}

// TestAPanickingActionBecomesATypedFailure pins the one panic this domain
// recovers, and why.
//
// A panicking Go program exits with status 2, which sysexits gives no meaning
// and several supervisors read as a usage error. Recovering hides nothing —
// the value AND the stack of the goroutine that actually failed both travel as
// fields — and it moves the decision about what the process does next from the
// runtime to main.
func TestAPanickingActionBecomesATypedFailure(t *testing.T) {
	t.Parallel()
	const exitSoftware int = 70
	var h harness
	root := leaf("tool", func(context.Context, corecli.InvocationValue) error {
		panic("nil map write")
	})
	err := h.run(t, root)
	if !kerrs.HasCode(err, svccli.CodeCommandPanicked) {
		t.Fatalf("err = %v, want COMMAND_PANICKED", err)
	}
	if got := kerrs.ExitCodeOf(err); got != exitSoftware {
		t.Errorf("exit = %d, want %d (EX_SOFTWARE) — the command line was fine", got, exitSoftware)
	}
	joined := fieldText(err)
	requireContains(t, joined, "nil map write", "the recovered value travels as a field")
	requireContains(t, joined, "service/cli", "the ORIGINATING stack travels as a field")
}

// TestAPanicCarryingAnErrsErrorCannotHijackTheCode pins the reason the
// recovered value is a FIELD and never the wrap origin: origin-wins would let
// a panic(*errs.Error) replace COMMAND_PANICKED with a code of the panicking
// code's choosing, including one with exit status 0.
func TestAPanicCarryingAnErrsErrorCannotHijackTheCode(t *testing.T) {
	t.Parallel()
	var h harness
	hijack := kerrs.Define(0x40_01_02_01, "LOOKS_FINE", "looks fine", "private", kerrs.WithExitCode(0))
	root := leaf("tool", func(context.Context, corecli.InvocationValue) error { panic(hijack) })
	err := h.run(t, root)
	if !kerrs.HasCode(err, svccli.CodeCommandPanicked) {
		t.Fatalf("a panic value hijacked the code: %v", err)
	}
	if kerrs.ExitCodeOf(err) == 0 {
		t.Error("a panic must never produce a successful exit status")
	}
}

// TestTheEngineHoldsNothingPerInvocation pins the half of concurrency safety
// the SDK can actually promise, and demonstrates the pattern that gets the
// other half.
//
// The engine keeps no per-invocation state: every Execute builds its own flag
// sets and its own Invocation, so eight concurrent runs each see their own
// -n. What the SDK canNOT make safe is the destination a Binder writes to —
// fs.IntVar(&shared, …) targets the CALLER's variable, and two concurrent
// invocations write it, which the race detector reports against the caller's
// line. That limitation is stated on Binder, on Config and in ADR 0065 §D7
// rather than papered over with a mutex the SDK would be holding around
// somebody else's memory.
//
// The safe pattern is the one below, and it is why InvocationValue.Flags
// exists: bind into storage the Binder allocates per call, and read it back
// through the invocation.
func TestTheEngineHoldsNothingPerInvocation(t *testing.T) {
	t.Parallel()
	const runs int = 8
	var h harness
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		//: per-call storage: the Binder allocates, so nothing is shared.
		Flags: func(flags *flag.FlagSet) { flags.Int("n", 0, "an `n`") },
		Run: func(_ context.Context, invocation corecli.InvocationValue) error {
			got, _ := invocation.Leaf().Lookup("n").Value.(flag.Getter).Get().(int)
			//: the argument this invocation was given, not a sibling's.
			if strconv.Itoa(got) != invocation.Args[0] {
				t.Errorf("flag = %d but the positional says %s; invocations leaked", got, invocation.Args[0])
			}
			return nil
		},
	}
	app := h.build(t, root)
	done := make(chan error, runs)
	for i := range runs {
		go func() {
			value := strconv.Itoa(i)
			done <- app.Execute(context.Background(), []string{"-n", value, value})
		}()
	}
	for range runs {
		if err := <-done; err != nil {
			t.Errorf("concurrent Execute: %v", err)
		}
	}
}

// TestTheContextReachesTheAction pins that this domain adds no deadline of its
// own and hands the Action the context Execute was called with. A CLI's
// cancellation policy is main's, and a budget invented here would be one no
// caller asked for.
func TestTheContextReachesTheAction(t *testing.T) {
	t.Parallel()
	var h harness
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "carried")
	root := leaf("tool", func(inner context.Context, _ corecli.InvocationValue) error {
		if inner.Value(key{}) != "carried" {
			t.Error("the Action did not receive the caller's context")
		}
		return nil
	})
	if err := h.build(t, root).Execute(ctx, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestOutputDefaultsToStderrAndNeverStdout pins ADR 0030 for this domain: the
// zero Config resolves both writers to os.Stderr, so an SDK default can never
// put a help page — or a command's own output — into a protocol channel a
// consumer is parsing.
func TestOutputDefaultsToStderrAndNeverStdout(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	app, err := svccli.New(svccli.Config{}, leaf("tool", rec.action()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.invocation.Output == nil {
		t.Fatal("Invocation.Output is documented as never nil")
	}
	if rec.invocation.Output == io.Writer(os.Stdout) {
		t.Error("the zero Config resolved Output to os.Stdout; ADR 0030 forbids it")
	}
	if rec.invocation.Output != io.Writer(os.Stderr) {
		t.Errorf("Output = %v, want os.Stderr — the safe zero of ADR 0030", rec.invocation.Output)
	}
}

// fieldText joins every field's rendering so a test can assert on the set
// without depending on the order the engine attached them.
func fieldText(err error) string {
	parts := make([]string, 0, 4)
	for _, field := range kerrs.FieldsOf(err) {
		parts = append(parts, field.Key()+"="+field.StringValue())
	}
	return strings.Join(parts, "\n")
}
