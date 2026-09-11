package cli_test

import (
	"context"
	"flag"
	"io"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// TestNewRefusesAMalformedDeclaration walks every clause the constructor
// enforces. Each is a decision recorded in ADR 0065 §D5, not a taste: the
// alternative to each refusal is a command line whose behaviour nobody
// declared.
func TestNewRefusesAMalformedDeclaration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		root corecli.CommandValue
		code kerrs.Code
	}{
		{
			name: "neither Run nor Commands is an inert command",
			root: corecli.CommandValue{Name: "tool"},
			code: corecli.CodeInvalidCommand,
		},
		{
			name: "both Run and Commands is ambiguous",
			root: corecli.CommandValue{
				Name: "tool", Run: noop, Commands: []corecli.CommandValue{leaf("sub", noop)},
			},
			code: corecli.CodeAmbiguousCommand,
		},
		{
			name: "a blank name names nothing",
			root: corecli.CommandValue{Name: "", Run: noop},
			code: corecli.CodeInvalidCommand,
		},
		{
			name: "a name beginning with - could never be typed",
			root: group("tool", corecli.CommandValue{Name: "-x", Summary: "s", Run: noop}),
			code: corecli.CodeInvalidCommand,
		},
		{
			name: "a name with a space is not one token",
			root: group("tool", corecli.CommandValue{Name: "two words", Summary: "s", Run: noop}),
			code: corecli.CodeInvalidCommand,
		},
		{
			name: "a listed child with no summary",
			root: group("tool", corecli.CommandValue{Name: "sub", Run: noop}),
			code: corecli.CodeInvalidCommand,
		},
		{
			name: "two children sharing a name",
			root: group("tool", leaf("sub", noop), leaf("sub", noop)),
			code: corecli.CodeDuplicateCommand,
		},
		{
			name: "a Binder claiming -h",
			root: corecli.CommandValue{
				Name: "tool", Run: noop,
				Flags: ownedIntBinder("h"),
			},
			code: corecli.CodeReservedFlag,
		},
		{
			name: "a Binder claiming -help",
			root: corecli.CommandValue{
				Name: "tool", Run: noop,
				Flags: ownedIntBinder("help"),
			},
			code: corecli.CodeReservedFlag,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var h harness
			app, err := svccli.New(h.config(), tc.root)
			if err == nil {
				t.Fatalf("New accepted a declaration it must refuse (app=%v)", app)
			}
			if !kerrs.HasCode(err, tc.code) {
				t.Errorf("code = %v, want %#x", err, uint32(tc.code))
			}
			if app != nil {
				t.Error("a refused declaration must not also hand back an executor")
			}
		})
	}
}

// TestNewValidatesTheWholeTreeNotThePathTaken is the decision that makes the
// refusals above worth having.
//
// A branch is validated at CONSTRUCTION, so a mis-wired `tool db migrate`
// fails on the first run of the binary. Validating lazily — on the path an
// invocation happens to take — would move that discovery to the first operator
// who typed the one command nobody had tried, which is a discovery made in
// production by somebody who cannot fix it.
func TestNewValidatesTheWholeTreeNotThePathTaken(t *testing.T) {
	t.Parallel()
	var h harness
	root := group("tool",
		leaf("ok", noop),
		//: three levels down, on a branch no test invocation would reach.
		group("db", group("schema", corecli.CommandValue{Name: "migrate", Summary: "s"})),
	)
	_, err := svccli.New(h.config(), root)
	if !kerrs.HasCode(err, corecli.CodeInvalidCommand) {
		t.Fatalf("a fault three levels deep must fail construction, got %v", err)
	}
	requireContains(t, kerrs.PrivateOf(err), "neither Run nor Commands", "the clause is named")
	fieldsCarryPath(t, err, "tool db schema migrate")
}

// TestARootNeedsNoSummary pins the one asymmetry in the shape rules: a summary
// is what a PARENT lists beside a child's name, and the root has no parent. A
// blanket requirement would demand a string nothing renders.
func TestARootNeedsNoSummary(t *testing.T) {
	t.Parallel()
	var h harness
	if _, err := svccli.New(h.config(), corecli.CommandValue{Name: "tool", Run: noop}); err != nil {
		t.Fatalf("a summary-less root is a valid declaration: %v", err)
	}
}

// TestBindersRunAtConstruction pins the mechanism the reserved-flag check
// rests on: a declaration cannot be inspected without executing it, so New
// calls each Binder once on a throwaway set. It is documented as a contract
// (a Binder must be safe to call more than once) precisely because it is
// observable, and a test that did not pin it would let somebody "optimise" the
// probe away and silently lose the -h collision check.
func TestBindersRunAtConstruction(t *testing.T) {
	t.Parallel()
	var h harness
	calls := 0
	var target int
	root := corecli.CommandValue{
		Name: "tool", Run: noop,
		Flags: func(flags *flag.FlagSet) {
			calls++
			flags.IntVar(&target, "n", 1, "an `n`")
		},
	}
	app, err := svccli.New(h.config(), root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if calls != 1 {
		t.Fatalf("the Binder ran %d times during New, want exactly 1", calls)
	}
	if err := app.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Errorf("the Binder ran %d times in total, want 2 (one probe, one parse)", calls)
	}
}

// ownedIntBinder returns a Binder that owns the variable it binds into, so two
// parallel subtests never share one — the race detector caught exactly that in
// an earlier revision of this file, which is itself the proof that New really
// does CALL the Binder rather than merely inspecting the declaration.
func ownedIntBinder(name string) corecli.Binder {
	target := new(int)
	return func(flags *flag.FlagSet) { flags.IntVar(target, name, 0, "an `n`") }
}

// fieldsCarryPath asserts that one of the error's fields spells the command
// path. The path is what turns a refusal into an actionable line — "which of
// my forty commands is wrong" is the only question the reader has.
func fieldsCarryPath(tb testing.TB, err error, want string) {
	tb.Helper()
	for _, field := range kerrs.FieldsOf(err) {
		if field.StringValue() == want {
			return
		}
	}
	tb.Errorf("no field carries the path %q; fields = %v", want, kerrs.FieldsOf(err))
}

// TestTheTreeIsFrozenAtNew pins that the executor walks the tree New
// validated, not the caller's. CommandValue is a value, but its Commands
// slices were the caller's arrays: renaming a child after New changed which
// command ran — unvalidated, and racing with any Execute in flight. Seen
// failing without the copy: "Execute(serve) = UNKNOWN_COMMAND after the
// caller renamed its own slice".
func TestTheTreeIsFrozenAtNew(t *testing.T) {
	t.Parallel()
	ran := false
	children := []corecli.CommandValue{leaf("serve", func(context.Context, corecli.InvocationValue) error {
		ran = true
		return nil
	})}
	root := corecli.CommandValue{Name: "tool", Summary: "s", Commands: children}
	app, err := svccli.New(svccli.Config{Output: io.Discard, ErrOutput: io.Discard}, root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	//: the caller edits its own array after the fact.
	children[0].Name = "renamed"
	if err := app.Execute(t.Context(), []string{"serve"}); err != nil || !ran {
		t.Fatalf("Execute(serve) = %v, ran = %v, after the caller renamed its own slice; want the validated tree", err, ran)
	}
}
