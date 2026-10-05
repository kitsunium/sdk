package cli

import (
	"context"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Success is the process exit status of a command that did what it was asked.
const Success int = 0

// Status is the process exit status for what [Executor].Execute returned: 0
// when err is nil, and errs.ExitCodeOf(err) otherwise.
//
// It is a guard on the SDK's existing convention and not a second one.
// errs.ExitCodeOf answers "what status does THIS ERROR map to", so it returns
// the EX_SOFTWARE default (70) for a nil it was never meant to be handed —
// which is correct for an accessor and catastrophic at a CLI boundary, where
// success is the common case. This is the one place in the SDK where nil has
// to mean 0, so this is where the guard lives.
//
//	os.Exit(cli.Status(app.Execute(ctx, os.Args[1:])))
func Status(err error) int {
	//: success is the only status this domain names itself.
	if err == nil {
		//: nil is not an error and must not map to EX_SOFTWARE.
		return Success
	}
	//: everything else is the sentinel's own errs.WithExitCode, or its default.
	return kerrs.ExitCodeOf(err)
}

// Execute is the one-line form of [New] followed by [Executor].Execute. It
// returns the same errors both would: a construction refusal carries EX_CONFIG
// (78), so a caller that only wants a status can pass the result straight to
// [Status].
func Execute(ctx context.Context, cfg Config, root Command, args []string) error {
	app, err := New(cfg, root)
	//: a refused tree is a wiring fault and never reaches an argument vector.
	if err != nil {
		//: propagate the core sentinel verbatim; it already names the path.
		return err
	}
	//: from here the outcome is the command's, the help's, or the operator's.
	return app.Execute(ctx, args)
}
