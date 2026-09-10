package cli_test

import (
	"context"
	"flag"
	"reflect"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestSentinelsCarryTheirAllocatedCode pins every sentinel to the dotted-quad
// value ADR 0065 allocated to this package. The registry audits check that the
// range is owned and that no two codes collide; neither of them checks that a
// given SENTINEL still carries the code its documentation names, which is what
// a consumer's errs.HasCode call actually depends on.
func TestSentinelsCarryTheirAllocatedCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *kerrs.Error
		code     kerrs.Code
		reason   string
	}{
		{"invalid command", corecli.InvalidCommand, corecli.CodeInvalidCommand, "INVALID_COMMAND"},
		{"ambiguous command", corecli.AmbiguousCommand, corecli.CodeAmbiguousCommand, "AMBIGUOUS_COMMAND"},
		{"duplicate command", corecli.DuplicateCommand, corecli.CodeDuplicateCommand, "DUPLICATE_COMMAND"},
		{"reserved flag", corecli.ReservedFlag, corecli.CodeReservedFlag, "RESERVED_FLAG"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.Code(); got != tc.code {
				t.Errorf("code = %#x, want %#x", uint32(got), uint32(tc.code))
			}
			if got := tc.sentinel.Reason(); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// TestDeclarationRefusalsCarryEXCONFIG pins the exit status of every sentinel
// this package owns.
//
// It matters because the domain's whole point is a status a caller can route
// on, and the two failure families must not collapse into one: a refused
// DECLARATION (this package) will be refused identically forever and the fix
// is a code change in main — EX_CONFIG, 78 — while a mistyped command line
// (internal/service/cli) is EX_USAGE, 64. A supervisor that restarts on one
// and reports on the other needs them to differ.
func TestDeclarationRefusalsCarryEXCONFIG(t *testing.T) {
	t.Parallel()
	const exitConfig int = 78
	sentinels := []*kerrs.Error{
		corecli.InvalidCommand,
		corecli.AmbiguousCommand,
		corecli.DuplicateCommand,
		corecli.ReservedFlag,
	}
	for _, sentinel := range sentinels {
		if got := sentinel.ExitCode(); got != exitConfig {
			t.Errorf("%s: exit = %d, want %d (EX_CONFIG)", sentinel.Reason(), got, exitConfig)
		}
	}
}

// TestPortsAreFunctionsNotInterfaces is the executable form of ADR 0039's rule
// as this domain satisfies it.
//
// A published interface cannot grow a method without breaking every downstream
// implementer at compile time, with no deprecation window. Action and Binder
// are FUNC types, which cannot grow one at all — the constraint is structural
// rather than remembered. pkg/v1/cli aliases both, so the shape is published
// and the guard is worth having.
func TestPortsAreFunctionsNotInterfaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  reflect.Type
	}{
		{"Action", reflect.TypeFor[corecli.Action]()},
		{"Binder", reflect.TypeFor[corecli.Binder]()},
	}
	for _, tc := range tests {
		if tc.typ.Kind() != reflect.Func {
			t.Errorf("%s is a %s; ADR 0039 wants a func so it can never grow a method", tc.name, tc.typ.Kind())
		}
	}
}

// TestExecutorIsFrozenAtOneMethod guards the other half of ADR 0039: Executor
// IS an interface, so every method it carries is a compile-time obligation on
// every downstream double. One is the whole contract; a second capability gets
// a sibling interface, never a widening.
func TestExecutorIsFrozenAtOneMethod(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[corecli.Executor]()
	if got := typ.NumMethod(); got != 1 {
		t.Fatalf("Executor has %d methods, want 1 (Execute); add a sibling interface instead", got)
	}
	if got := typ.Method(0).Name; got != "Execute" {
		t.Errorf("the one method is %q, want Execute", got)
	}
}

// TestIsGroupReadsTheDeclaration pins the leaf/group question to the presence
// of children and to nothing else — no cached flag, no constructor-set field —
// so it answers the same on a CommandValue a caller has just written and on
// one the engine has already validated.
func TestIsGroupReadsTheDeclaration(t *testing.T) {
	t.Parallel()
	leaf := corecli.CommandValue{
		Name:    "leaf",
		Summary: "a leaf",
		Run:     func(context.Context, corecli.InvocationValue) error { return nil },
	}
	if leaf.IsGroup() {
		t.Error("a command with a Run and no children is a leaf")
	}
	group := corecli.CommandValue{Name: "group", Summary: "a group", Commands: []corecli.CommandValue{leaf}}
	if !group.IsGroup() {
		t.Error("a command with children is a group")
	}
}

// TestLeafReturnsTheRunningCommandsSet pins InvocationValue.Leaf. It exists so
// a command reading its own flags does not index a slice whose length is a
// property of how deep it happens to sit in the tree — a caller writing
// Flags[len(Flags)-1] would be correct until somebody nests the command one
// level further, at which point it would still be correct and nobody would
// have checked.
func TestLeafReturnsTheRunningCommandsSet(t *testing.T) {
	t.Parallel()
	root := flag.NewFlagSet("tool", flag.ContinueOnError)
	leaf := flag.NewFlagSet("tool sub", flag.ContinueOnError)
	invocation := corecli.InvocationValue{Flags: []*flag.FlagSet{root, leaf}}
	if got := invocation.Leaf(); got != leaf {
		t.Errorf("Leaf() = %v, want the last set", got)
	}
	if got := (corecli.InvocationValue{}).Leaf(); got != nil {
		t.Errorf("Leaf() on a flagless invocation = %v, want nil", got)
	}
}
