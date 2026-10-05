//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/cli .

// Package cli is the public facade for the SDK's command-line domain: the
// sub-commands, the generated help and the typed exit status that the stdlib's
// flag package deliberately does not have.
//
//	root := cli.Command{
//		Name:    "tool",
//		Summary: "does the thing",
//		Commands: []cli.Command{{
//			Name:    "serve",
//			Summary: "run the HTTP server",
//			Flags:   func(fs *flag.FlagSet) { fs.IntVar(&port, "port", 8080, "listen `port`") },
//			Run: func(ctx context.Context, in cli.Invocation) error {
//				return serve(ctx, port)
//			},
//		}},
//	}
//
//	func main() {
//		app, err := cli.New(cli.Config{}, root)
//		if err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(cli.Status(err))
//		}
//		err = app.Execute(context.Background(), os.Args[1:])
//		if err != nil {
//			fmt.Fprintln(os.Stderr, err)
//		}
//		os.Exit(cli.Status(err))
//	}
//
// # What it adds to package flag, and what it leaves alone
//
// It adds four things and nothing else: sub-commands to arbitrary depth, one
// help text generated from the declarations, a typed exit status, and a seam
// to the rest of the SDK.
//
// It leaves the whole of flag alone. A [Binder] receives the stdlib's own
// *[flag.FlagSet]: the flag syntax, the [flag.Value] interface, every
// FooVar helper you already use, the "-x=v / -x v" forms and the rendering of
// the defaults table are flag's, unchanged and unwrapped. There is no
// cobra, no pflag, no third-party dependency of any kind — an argument parser
// is a mechanism, not a connector to somebody else's system, and this SDK
// writes its mechanisms.
//
// # Nothing here can end your process
//
// flag's own answer to a bad argument is [flag.ExitOnError], which calls
// os.Exit from inside a library: it makes the parse untestable, it skips every
// deferred function in the program, and it decides on your behalf whether the
// process should still be alive. This domain uses [flag.ContinueOnError] and
// returns an error. There is no os.Exit, no log.Fatal and no panic in the
// path — pinned by an AST audit over the production sources AND the suite, not
// by a comment.
//
// # The exit status
//
// The status is carried by the SDK's existing error model and NOT by a second
// convention: a sentinel declares it with errs.WithExitCode and a caller reads
// it with errs.ExitCodeOf. [Status] is the single guard on top of that, and it
// exists for one sharp reason: errs.ExitCodeOf(nil) is 70, because it answers
// "what status does this error map to" and nil is not an error. Passing a
// successful Execute straight into it would exit 70 on every success.
//
//	| outcome                          | status | who chose it              |
//	|----------------------------------|--------|---------------------------|
//	| the command succeeded            | 0      | Status                    |
//	| -h / -help was asked for         | 0      | Execute returns nil       |
//	| the command line was wrong       | 64     | service/app/cli (EX_USAGE)    |
//	| the tree is wired wrong          | 78     | core/app/cli (EX_CONFIG)      |
//	| the command failed               | its own errs code, or 70   | the command   |
//
// The last row is the one worth stating: an [Action] returning an *errs.Error
// keeps its own code AND its own exit status all the way out, because the
// error is returned VERBATIM and never relabelled by the framework that called
// it.
//
// # Help
//
// The help is generated from the same declarations the resolver walks — the
// summaries, the sub-command list, and flag's own PrintDefaults over the set
// the Binder filled. It is never written by hand, because a help maintained
// beside the declarations eventually describes a flag that was renamed, and an
// operator acts on what the help says.
//
// Asking for it is not a failure: -h writes the help and Execute returns nil.
// A bad command line writes the SAME help and returns an EX_USAGE error, so
// the exit status is what distinguishes them and the text does not have to.
//
// # Where the output goes
//
// [Config].Output (a command's own output) and [Config].ErrOutput (help and
// usage) both default to os.Stderr, and neither defaults to os.Stdout. ADR
// 0030 makes stdout a protocol channel that no SDK default may claim; a tool
// that emits a document sets Output: os.Stdout in main, on one visible line,
// and its help still stays on stderr where it cannot corrupt the document.
//
// # What it composes rather than reimplements
//
//   - config — [FlagSource] turns the flags an operator ACTUALLY TYPED into
//     the top layer of a config load. It uses flag.FlagSet.Visit and never
//     VisitAll, so an unset flag's Go default never overrides a file. There is
//     no env reading, no file reading, no layering and no validation here:
//     that is the config domain, and it already exists.
//   - lifecycle — nothing. There is no Signals field and no shutdown budget:
//     signals are a process-wide side effect and lifecycle.Run already makes
//     them opt-in for that reason. A long-running command composes it inside
//     its own Action, where the context it needs already is.
//   - errs — the whole error model, including the exit status. This domain
//     defines no status convention of its own.
//
// # Deliberately absent
//
// No "did you mean …?": the help that has just been written already lists
// every name that would have worked, which is the complete answer and cannot
// be wrong. No prefix or abbreviation matching, and no case folding: both make
// what an existing command line MEANS depend on which siblings exist in
// today's release. No shell-completion generator, no colour, no interactive
// prompting, no environment-variable fallback per flag.
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
