package cli_test

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// TestHelpNamesEveryDeclaredCommandAndFlag is CLAUDE.md rule 11 applied to the
// executable, in the only form that survives a refactor: rather than comparing
// the help against a golden string somebody would update to match a regression,
// it walks the DECLARATIONS and asserts each one appears.
//
// A help maintained beside the declarations eventually documents a flag that
// was renamed, and an operator acts on what the help says. Generating it is
// what makes "the help cannot lie" a property rather than a promise; this test
// is what makes the generation a property rather than a current fact.
func TestHelpNamesEveryDeclaredCommandAndFlag(t *testing.T) {
	t.Parallel()
	var h harness
	var host string
	var port int
	root := corecli.CommandValue{
		Name: "tool", Summary: "the tool", Description: "A longer description.",
		Commands: []corecli.CommandValue{
			{
				Name: "serve", Summary: "run the server",
				Flags: func(flags *flag.FlagSet) {
					flags.StringVar(&host, "host", "localhost", "bind `address`")
					flags.IntVar(&port, "port", 8080, "listen `port`")
				},
				Run: noop,
			},
			{Name: "migrate", Summary: "apply migrations", Run: noop},
		},
	}
	if err := h.run(t, root, "-h"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := h.help()
	for _, want := range []string{"the tool", "A longer description.", "Usage:", "tool <command>", "Commands:"} {
		requireContains(t, got, want, "the root's help")
	}
	//: every child, by name AND by summary, because a name with no summary is
	//: a name an operator has to guess at.
	for _, child := range root.Commands {
		requireContains(t, got, child.Name, "a declared sub-command is listed")
		requireContains(t, got, child.Summary, "a declared sub-command's summary is listed")
	}
	//: the root declares no flags, so it must not advertise a section.
	if strings.Contains(got, "Flags:") {
		t.Errorf("the root has no flags but its help has a Flags section:\n%s", got)
	}

	h.errOut.Reset()
	if err := h.run(t, root, "serve", "-h"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	serve := h.help()
	//: flag's own PrintDefaults renders the table, so the backquoted type name
	//: and the default both appear without this domain reimplementing either.
	for _, want := range []string{"-host address", "localhost", "-port port", "8080", "[flags]"} {
		requireContains(t, serve, want, "the leaf's flag table")
	}
	//: a leaf is not a group and must not invite a sub-command.
	if strings.Contains(serve, "<command>") {
		t.Errorf("a leaf's usage line offers a sub-command:\n%s", serve)
	}
}

// TestHelpIsWrittenInOnePiece pins that the page is assembled and flushed
// once. A help interleaved with another goroutine's output is unreadable, and
// a partially written one is worse: it stops mid-list, which reads as "these
// are all the commands there are".
func TestHelpIsWrittenInOnePiece(t *testing.T) {
	t.Parallel()
	counter := &writeCounter{}
	root := group("tool", leaf("a", noop), leaf("b", noop))
	app, err := newWithErrOutput(t, counter, root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Execute(t.Context(), []string{"-h"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if counter.calls != 1 {
		t.Errorf("the help took %d Write calls, want 1", counter.calls)
	}
}

// TestAHelpNobodyReceivedIsNotASuccess pins that -h is status 0 only when the
// diagnostic stream took the page. `tool -h > /dev/full` used to exit 0 with
// nothing written, and a script reading that status believed the help had been
// delivered. The writer's own error stays matchable beneath the verdict, and a
// writer that took part of the page without saying why is io.ErrShortWrite.
// Seen failing with the write's error discarded again: both rows reported
// "Execute = <nil>".
func TestAHelpNobodyReceivedIsNotASuccess(t *testing.T) {
	t.Parallel()
	closed := errors.New("the pipe is closed")
	type tc struct {
		name  string
		w     io.Writer
		cause error
	}
	tests := []tc{
		{"a stream that refuses the page", refusingWriter{err: closed}, closed},
		{"a stream that takes part of it silently", halfWriter{}, io.ErrShortWrite},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		app, err := newWithErrOutput(t, c.w, group("tool", leaf("a", noop)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		got := app.Execute(t.Context(), []string{"-h"})
		if !kerrs.HasCode(got, svccli.CodeHelpWriteFailed) {
			t.Fatalf("%s: Execute = %v, want HELP_WRITE_FAILED", c.name, got)
		}
		if !errors.Is(got, c.cause) {
			t.Errorf("%s: Execute = %v, want the stream's own error beneath it", c.name, got)
		}
		if code := kerrs.ExitCodeOf(got); code != 74 {
			t.Errorf("%s: exit status %d, want EX_IOERR (74)", c.name, code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAFailedHelpWriteKeepsTheUsageVerdict pins the other half: after a bad
// command line the help is the actionable half of the answer and the usage
// error is the verdict, so a stream that refuses the help does not turn
// EX_USAGE into EX_IOERR — and the missing page still travels on the verdict,
// as a field, rather than vanishing.
func TestAFailedHelpWriteKeepsTheUsageVerdict(t *testing.T) {
	t.Parallel()
	app, err := newWithErrOutput(t, refusingWriter{err: io.ErrClosedPipe}, group("tool", leaf("a", noop)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := app.Execute(t.Context(), []string{"nope"})
	if !kerrs.HasCode(got, svccli.CodeUnknownCommand) {
		t.Fatalf("Execute = %v, want UNKNOWN_COMMAND to stay the verdict", got)
	}
	//: the verdict stays, and the page nobody received is still visible.
	if fields := fieldText(got); !strings.Contains(fields, "help_write_error="+io.ErrClosedPipe.Error()) {
		t.Errorf("the failed help write left no trace on the verdict; fields:\n%s", fields)
	}
}

// refusingWriter refuses every write with err, as a closed pipe or a full disk
// does.
type refusingWriter struct {
	err error
}

// Write accepts nothing and reports the stream's error.
func (w refusingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

// halfWriter takes half of what it is given and reports no error, which
// io.Writer forbids and which a buggy stream still does.
type halfWriter struct{}

// Write takes half of p and reports success.
func (halfWriter) Write(p []byte) (int, error) {
	return len(p) / 2, nil
}

// TestSubCommandsAreListedInDeclarationOrder pins that the order is the
// author's and not the map iteration order a lookup table would have imposed.
// A help whose list re-orders between runs is a help nobody can diff.
func TestSubCommandsAreListedInDeclarationOrder(t *testing.T) {
	t.Parallel()
	var h harness
	root := group("tool", leaf("zulu", noop), leaf("alpha", noop), leaf("mike", noop))
	if err := h.run(t, root, "-h"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := h.help()
	zulu, alpha, mike := strings.Index(got, "zulu"), strings.Index(got, "alpha"), strings.Index(got, "mike")
	if zulu >= alpha || alpha >= mike {
		t.Errorf("listed out of declaration order (zulu=%d alpha=%d mike=%d):\n%s", zulu, alpha, mike, got)
	}
}

// TestFlagParsingStaysSilentAfterHelpWasRendered guards a defect this design
// invites: rendering the flag table means pointing the FlagSet's output at the
// help buffer, and a set left pointing there would report its NEXT parse
// failure into a buffer nobody reads — or, if that buffer is gone, into the
// caller's memory. The renderer puts it back to io.Discard.
func TestFlagParsingStaysSilentAfterHelpWasRendered(t *testing.T) {
	t.Parallel()
	var h harness
	var target int
	root := corecli.CommandValue{
		Name: "tool", Summary: "s", Flags: intBinder(&target, "n", 0), Run: noop,
	}
	app := h.build(t, root)
	if err := app.Execute(t.Context(), []string{"-h"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	before := h.help()
	h.errOut.Reset()
	if err := app.Execute(t.Context(), []string{"-n", "nope"}); err == nil {
		t.Fatal("a bad value must be a usage error")
	}
	after := h.help()
	//: the second page is the help again, not the help plus flag's own report.
	if strings.Contains(after, "nope") {
		t.Errorf("flag reported into the help stream after a render:\n%s", after)
	}
	if before != after {
		t.Errorf("the same command rendered two different help pages:\n%q\n%q", before, after)
	}
}

// writeCounter counts Write calls so a test can assert on the number of
// syscalls a help page would cost, not merely on its content.
type writeCounter struct {
	calls int
	body  strings.Builder
}

// Write records one call and keeps the bytes.
func (w *writeCounter) Write(p []byte) (int, error) {
	w.calls++
	return w.body.Write(p)
}
