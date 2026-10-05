package cli

import (
	"context"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Success is the process exit status of a command that did what it was asked.
const Success int = 0

// status is Status's body: decl_gen.go writes Status, from the
// design, as one call of it.
func status(err error) int {
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
