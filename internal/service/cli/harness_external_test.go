package cli_test

import (
	"bytes"
	"context"
	"flag"
	"io"
	"strings"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// harness owns the two buffers every test in this package reads back, so no
// test in the suite writes to a process stream. That is not tidiness: a domain
// whose central claim is "nothing here reaches os.Stdout" (ADR 0030) cannot
// have a suite that reaches it.
type harness struct {
	out    bytes.Buffer
	errOut bytes.Buffer
}

// config returns the Config wiring both writers into this harness.
func (h *harness) config() svccli.Config {
	return svccli.Config{Output: &h.out, ErrOutput: &h.errOut}
}

// help returns everything written to the diagnostic stream so far.
func (h *harness) help() string { return h.errOut.String() }

// build constructs an executor over root and fails the test if the
// declaration was refused.
func (h *harness) build(tb testing.TB, root corecli.CommandValue) corecli.Executor {
	tb.Helper()
	app, err := svccli.New(h.config(), root)
	if err != nil {
		tb.Fatalf("New refused a tree this test expects to be valid: %v", err)
	}
	return app
}

// run executes args against root and returns the error verbatim.
func (h *harness) run(tb testing.TB, root corecli.CommandValue, args ...string) error {
	tb.Helper()
	return h.build(tb, root).Execute(context.Background(), args)
}

// newWithErrOutput builds an executor whose diagnostic stream is w, for the
// tests that need to observe HOW the help is written rather than what it says.
func newWithErrOutput(tb testing.TB, w io.Writer, root corecli.CommandValue) (corecli.Executor, error) {
	tb.Helper()
	return svccli.New(svccli.Config{Output: io.Discard, ErrOutput: w}, root)
}

// recorder is an Action that records the invocation it was handed.
type recorder struct {
	called     int
	invocation corecli.InvocationValue
	err        error
}

// action returns the Action this recorder stands behind.
func (r *recorder) action() corecli.Action {
	return func(_ context.Context, invocation corecli.InvocationValue) error {
		r.called++
		r.invocation = invocation
		return r.err
	}
}

// leaf builds a valid leaf command around an Action.
func leaf(name string, run corecli.Action) corecli.CommandValue {
	return corecli.CommandValue{Name: name, Summary: name + " summary", Run: run}
}

// group builds a valid group command around its children.
func group(name string, children ...corecli.CommandValue) corecli.CommandValue {
	return corecli.CommandValue{Name: name, Summary: name + " summary", Commands: children}
}

// noop is an Action that succeeds and does nothing.
func noop(context.Context, corecli.InvocationValue) error { return nil }

// intBinder returns a Binder declaring one int flag into target.
func intBinder(target *int, name string, def int) corecli.Binder {
	return func(flags *flag.FlagSet) { flags.IntVar(target, name, def, "an `n`") }
}

// requireContains fails the test when haystack does not contain needle.
func requireContains(tb testing.TB, haystack, needle, what string) {
	tb.Helper()
	if !strings.Contains(haystack, needle) {
		tb.Errorf("%s: %q not found in:\n%s", what, needle, haystack)
	}
}
