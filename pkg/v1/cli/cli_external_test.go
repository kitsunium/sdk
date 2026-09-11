package cli_test

import (
	"context"
	"errors"
	"flag"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/cli"
	"github.com/kitsunium/sdk/pkg/v1/config"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestStatusIsZeroForSuccess is the guard this package exists to provide, and
// the reason it is worth a named function rather than a doc line.
//
// errs.ExitCodeOf answers "what status does THIS ERROR map to", so it returns
// the EX_SOFTWARE default (70) for a nil it was never meant to be handed. That
// is correct for an accessor and catastrophic at a CLI boundary, where success
// is the common case: os.Exit(errs.ExitCodeOf(app.Execute(…))) would fail
// every successful run of every tool built on this domain.
//
// The assertion on ExitCodeOf(nil) is deliberate. It is not testing the errs
// package — it is pinning the premise this helper rests on, so that if errs
// ever changed its mind, the test that fails is the one whose reason is
// written down here.
func TestStatusIsZeroForSuccess(t *testing.T) {
	t.Parallel()
	const exitSoftware int = 70
	if got := errs.ExitCodeOf(nil); got != exitSoftware {
		t.Fatalf("errs.ExitCodeOf(nil) = %d, want %d — Status exists because of this", got, exitSoftware)
	}
	if got := cli.Status(nil); got != cli.Success {
		t.Errorf("Status(nil) = %d, want %d", got, cli.Success)
	}
}

// TestStatusDelegatesRatherThanInventing pins that this package adds no second
// exit-status convention: every status comes from the errs.WithExitCode the
// sentinel declared, read back through errs.ExitCodeOf.
func TestStatusDelegatesRatherThanInventing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"a declaration refusal is EX_CONFIG", cli.InvalidCommand, 78},
		{"an ambiguous declaration is EX_CONFIG", cli.AmbiguousCommand, 78},
		{"a duplicate name is EX_CONFIG", cli.DuplicateCommand, 78},
		{"a reserved flag is EX_CONFIG", cli.ReservedFlag, 78},
		{"an unknown command is EX_USAGE", cli.UnknownCommand, 64},
		{"a missing command is EX_USAGE", cli.MissingCommand, 64},
		{"bad flags are EX_USAGE", cli.InvalidFlags, 64},
		{"a panic is EX_SOFTWARE", cli.CommandPanicked, 70},
		{"an untyped error is EX_SOFTWARE", errors.New("plain"), 70},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cli.Status(tc.err); got != tc.want {
				t.Errorf("Status = %d, want %d", got, tc.want)
			}
			if got := errs.ExitCodeOf(tc.err); got != tc.want {
				t.Errorf("Status disagrees with errs.ExitCodeOf (%d vs %d)", tc.want, got)
			}
		})
	}
}

// TestTheFullShapeFromMain walks what a real main does: declare, construct,
// execute, exit. It is the example in the package doc, executed — so the
// example cannot drift from what the API accepts.
func TestTheFullShapeFromMain(t *testing.T) {
	t.Parallel()
	var out, help strings.Builder
	var port int
	var ran bool
	root := cli.Command{
		Name: "tool", Summary: "does the thing",
		Commands: []cli.Command{{
			Name: "serve", Summary: "run the HTTP server",
			Flags: func(flags *flag.FlagSet) { flags.IntVar(&port, "port", 8080, "listen `port`") },
			Run: func(_ context.Context, invocation cli.Invocation) error {
				ran = true
				_, writeErr := io.WriteString(invocation.Output, "listening")
				return writeErr
			},
		}},
	}
	cfg := cli.Config{Output: &out, ErrOutput: &help}
	err := cli.Execute(context.Background(), cfg, root, []string{"serve", "-port", "9000"})
	if status := cli.Status(err); status != cli.Success {
		t.Fatalf("status = %d (%v), want 0", status, err)
	}
	if !ran || port != 9000 {
		t.Fatalf("ran = %v, port = %d; want true and 9000", ran, port)
	}
	if out.String() != "listening" {
		t.Errorf("the command's output went to %q", out.String())
	}
	if help.Len() != 0 {
		t.Errorf("a successful run wrote a diagnostic:\n%s", help.String())
	}
}

// TestExecuteReportsAConstructionRefusal pins that the one-line form does not
// swallow a wiring fault: a refused tree comes back with its EX_CONFIG status
// so a caller who only reads Status still exits 78 rather than 0.
func TestExecuteReportsAConstructionRefusal(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	//: neither an Action nor sub-commands: the inert command ADR 0031 refuses.
	err := cli.Execute(context.Background(), cli.Config{Output: &out, ErrOutput: &out},
		cli.Command{Name: "tool"}, nil)
	code, ok := errs.CodeOf(cli.InvalidCommand)
	if !ok || !errs.HasCode(err, code) {
		t.Fatalf("err = %v, want INVALID_COMMAND", err)
	}
	if got := cli.Status(err); got != 78 {
		t.Errorf("status = %d, want 78 (EX_CONFIG)", got)
	}
}

// TestHelpExitsZeroAndUsageDoesNot is the domain's central status distinction,
// asserted at the public edge because that is where a caller writes os.Exit.
func TestHelpExitsZeroAndUsageDoesNot(t *testing.T) {
	t.Parallel()
	root := cli.Command{
		Name: "tool", Summary: "s",
		Commands: []cli.Command{{Name: "run", Summary: "run it", Run: func(context.Context, cli.Invocation) error { return nil }}},
	}
	tests := []struct {
		name   string
		args   []string
		status int
	}{
		{"help was asked for", []string{"-h"}, 0},
		{"a sub-command help", []string{"run", "-h"}, 0},
		{"nothing was asked for", nil, 64},
		{"something that does not exist", []string{"walk"}, 64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, help strings.Builder
			cfg := cli.Config{Output: &out, ErrOutput: &help}
			got := cli.Status(cli.Execute(context.Background(), cfg, root, tc.args))
			if got != tc.status {
				t.Errorf("status = %d, want %d", got, tc.status)
			}
			//: every one of these four writes the SAME help. The status is what
			//: distinguishes a question from a mistake; the text does not have to.
			if !strings.Contains(help.String(), "Usage:") {
				t.Errorf("no help was written:\n%s", help.String())
			}
		})
	}
}

// TestFlagSourceIsTheLastLayerOfAConfigLoad executes the composition ADR 0065
// §D6 promises, end to end, through the PUBLIC config API — so the claim "cli
// feeds config" is a test rather than a paragraph.
//
// It also pins the property that makes it usable: a flag left unset does NOT
// override the layer beneath it.
func TestFlagSourceIsTheLastLayerOfAConfigLoad(t *testing.T) {
	t.Parallel()
	type settings struct {
		Port int    `json:"port"`
		Host string `json:"host"`
	}
	var loaded settings
	var out strings.Builder
	root := cli.Command{
		Name: "tool", Summary: "s",
		Flags: func(flags *flag.FlagSet) {
			flags.Int("port", 8080, "listen `port`")
			flags.String("host", "localhost", "bind `address`")
		},
		Run: func(_ context.Context, invocation cli.Invocation) error {
			//: the file layer first, the command line last: flags win.
			return config.Load(&loaded, staticSource{"port": 1234, "host": "from-file"}, cli.FlagSource(invocation))
		},
	}
	cfg := cli.Config{Output: &out, ErrOutput: &out}
	if err := cli.Execute(context.Background(), cfg, root, []string{"-port", "9000"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if loaded.Port != 9000 {
		t.Errorf("port = %d, want 9000 — a flag that WAS typed wins", loaded.Port)
	}
	//: the decision this whole adapter exists for. With VisitAll, host would
	//: be "localhost" here and the file would look ignored, silently, forever.
	if loaded.Host != "from-file" {
		t.Errorf("host = %q, want from-file — an unset flag contributes nothing", loaded.Host)
	}
}

// staticSource is a fixed configuration layer, standing in for a file so this
// test needs no filesystem.
type staticSource map[string]any

// Load returns the fixed layer, cloned so config's own merge cannot write back
// into the double.
func (s staticSource) Load() (values map[string]any, err error) {
	return maps.Clone(s), nil
}
