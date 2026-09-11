//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/cli .

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
//	| the command line was wrong       | 64     | service/cli (EX_USAGE)    |
//	| the tree is wired wrong          | 78     | core/cli (EX_CONFIG)      |
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
// [Config.Output] (a command's own output) and [Config.ErrOutput] (help and
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

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// Success is the process exit status of a command that did what it was asked.
const Success int = 0

// Action is what one command does — see [Command].
type Action = corecli.Action

// Binder declares one command's flags onto the stdlib set that parses them.
type Binder = corecli.Binder

// Executor resolves an argument vector against a command tree and runs it.
type Executor = corecli.Executor

// Command is one declared command: a name, what it says about itself, its
// flags, and EITHER an [Action] OR sub-commands.
type Command = corecli.CommandValue

// Invocation is one resolved command line as an [Action] sees it.
type Invocation = corecli.InvocationValue

// Config configures [New] — two writers, and deliberately nothing else.
type Config = svccli.Config

// FlagSourceValue is one invocation's typed flags as a config layer.
type FlagSourceValue = svccli.FlagSourceValue

var (
	// InvalidCommand refuses a declaration that could never be reached or
	// could never run: a blank name, a name that would be parsed as a flag, a
	// child with no summary, or neither an Action nor sub-commands.
	InvalidCommand = corecli.InvalidCommand
	// AmbiguousCommand refuses a command carrying BOTH an Action and
	// sub-commands, where adding a child would silently change what an
	// existing command line means.
	AmbiguousCommand = corecli.AmbiguousCommand
	// DuplicateCommand refuses two sub-commands of one group sharing a name.
	DuplicateCommand = corecli.DuplicateCommand
	// ReservedFlag refuses a Binder that bound -h or -help, the two names the
	// SDK answers itself.
	ReservedFlag = corecli.ReservedFlag

	// UnknownCommand is returned when a token names no sub-command of the
	// group it was typed under. EX_USAGE (64).
	UnknownCommand = svccli.UnknownCommand
	// MissingCommand is returned when a group is invoked with no sub-command.
	// EX_USAGE (64).
	MissingCommand = svccli.MissingCommand
	// InvalidFlags is returned when package flag refused the vector. EX_USAGE
	// (64).
	InvalidFlags = svccli.InvalidFlags
	// CommandPanicked is returned when an [Action] panicked. The recovered
	// value and the originating stack travel as fields; the status stays
	// EX_SOFTWARE (70).
	CommandPanicked = svccli.CommandPanicked
	// HelpWriteFailed is returned when -h asked for the help and the stream it
	// goes to did not take the page — a closed pipe, a full disk. The
	// stream's own error stays matchable beneath it, or rides in a field when
	// it is itself an SDK error, so the code and EX_IOERR (74) are always this
	// one's. A bad command line keeps its EX_USAGE even when its help could
	// not be written.
	HelpWriteFailed = svccli.HelpWriteFailed
)

// New validates a whole command tree and returns the [Executor] that runs it.
//
// The WHOLE tree is checked, not the branch an invocation happens to take, so
// a mis-wired `tool db migrate` fails on the first run of the binary rather
// than on the first run of the migration. Every refusal carries EX_CONFIG
// (78).
//
// Each [Binder] is CALLED once here, on a throwaway flag set, which is what
// proves it binds no reserved name — so a Binder must be safe to call more
// than once and must touch only the set it is given.
func New(cfg Config, root Command) (runner Executor, err error) {
	//: the service constructor owns the validation; this is the published name.
	return svccli.New(cfg, root)
}

// FlagSource turns the flags an operator ACTUALLY TYPED on one invocation into
// a config layer, for use as the LAST source of a config.Load or
// config.LoadSchema — the position that makes a flag win over a file and over
// the environment.
//
// It walks flag.FlagSet.Visit and never VisitAll: an unset flag contributes
// NOTHING, so a flag whose default is the Go zero cannot silently override the
// configuration file on every run. The flag name is the config key verbatim,
// with no case or separator transformation, because a rename rule is a second
// grammar whose failure mode is a key that quietly matches nothing.
func FlagSource(invocation Invocation) *FlagSourceValue {
	//: one seam, and the whole of what this domain contributes to config.
	return svccli.FlagSource(invocation)
}

// Status is the process exit status for what [Executor.Execute] returned: 0
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

// Execute is the one-line form of [New] followed by [Executor.Execute]. It
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
