// Package cli — declares the sentinel *errs.Error outcomes of the domain: the
// refused declarations, and the engine's verdicts on a command line. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cli

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every declaration sentinel
// carries it: the same tree will be refused identically forever, the operator
// did nothing wrong, and the fix is a code change in main. That is not the
// same event as a mistyped command line, which carries exitUsage.
const exitConfig int = 78

// exitUsage matches sysexits EX_USAGE (64): the command line was wrong. It is
// deliberately a different status from the exitConfig a refused DECLARATION
// carries — one says the operator mistyped, the other says main is wired
// wrong, and a supervisor that restarts on one and not the other needs to be
// able to tell them apart.
const exitUsage int = 64

// exitIOErr matches sysexits EX_IOERR (74): an error occurred while doing I/O —
// here, on the stream the help is written to.
const exitIOErr int = 74

var (
	// InvalidCommand is returned by the constructor for a command that could
	// never be reached or could never run. The fields name the path and the
	// clause that failed.
	//
	// "Neither Run nor Commands" is the case worth naming: it is the inert
	// declaration ADR 0031 refuses. A name an operator can type that does
	// nothing looks configured, reports success, and is discovered only by
	// somebody wondering why the tool did not do the thing.
	InvalidCommand = errs.Define(CodeInvalidCommand, "INVALID_COMMAND",
		"The command declaration is not runnable and was refused",
		"core/app/cli: blank or unreachable name, missing summary, or neither Run nor Commands; the fields name the path and the clause",
		errs.WithExitCode(exitConfig))

	// AmbiguousCommand is returned by the constructor for a command carrying
	// both a Run and Commands.
	//
	// The refusal is not fastidiousness. With both, `tool db migrate` means
	// "run db with the positional argument migrate" — until somebody adds a
	// child named migrate, at which point the identical command line means
	// something else, in a release that changed no line of db's own code. A
	// declaration whose meaning depends on which siblings exist today is the
	// same class of failure as prefix matching, and it is refused for the same
	// reason.
	AmbiguousCommand = errs.Define(CodeAmbiguousCommand, "AMBIGUOUS_COMMAND",
		"A command may declare an action or sub-commands, not both",
		"core/app/cli: Run and Commands are both set; adding a child would silently change what an existing command line means",
		errs.WithExitCode(exitConfig))

	// DuplicateCommand is returned by the constructor when two children of one
	// group share a name. The resolver stops at the first match, so the second
	// declaration would be unreachable code that looks reachable.
	DuplicateCommand = errs.Define(CodeDuplicateCommand, "DUPLICATE_COMMAND",
		"Two sub-commands are declared under the same name",
		"core/app/cli: sibling names are unique per group; the fields carry the parent path and the repeated name",
		errs.WithExitCode(exitConfig))

	// ReservedFlag is returned by the constructor when a Binder bound "h" or
	// "help".
	//
	// The domain answers both names itself, and it must: package flag reports
	// an undefined -h as flag.ErrHelp rather than as a parse failure, and that
	// is the ONLY signal distinguishing "the operator asked for help" (exit 0)
	// from "the operator got it wrong" (exit 64). A command that binds -h
	// takes that signal away, and the engine would report a successful parse
	// where an operator expected a help page.
	ReservedFlag = errs.Define(CodeReservedFlag, "RESERVED_FLAG",
		"The flag names -h and -help are answered by the SDK",
		"core/app/cli: a Binder bound h or help; the engine needs flag.ErrHelp to tell a help request from a usage error",
		errs.WithExitCode(exitConfig))

	// The engine's verdicts on a command line, raised by internal/service/app/cli
	// as it resolves an argument vector and runs the command it names. They are
	// declared here, with the declaration refusals, so that the domain's codes and
	// sentinels are in one place (ADR 0160).

	// UnknownCommand is returned when a token does not name any child of the
	// group that was reached.
	//
	// There is deliberately no "did you mean …?". The group's help has already
	// been written, and it lists every name that WOULD have worked — which is
	// the complete answer, cannot be wrong, and costs no edit-distance table
	// to keep correct. A suggestion is a second answer that can differ from
	// the first.
	UnknownCommand = errs.Define(CodeUnknownCommand, "UNKNOWN_COMMAND",
		"That sub-command does not exist",
		"service/app/cli: the token names no child of this group; the fields carry the group path and the token",
		errs.WithExitCode(exitUsage))

	// MissingCommand is returned when a group is invoked with nothing after
	// its flags.
	//
	// It is a failure and not a quiet success on purpose: a group declares no
	// action, so `tool db` did nothing, and a script that read exit status 0
	// there would treat "I forgot the verb" as "the migration ran".
	MissingCommand = errs.Define(CodeMissingCommand, "MISSING_COMMAND",
		"This command needs a sub-command",
		"service/app/cli: a group has no action of its own; the field carries the group path and its help was written",
		errs.WithExitCode(exitUsage))

	// InvalidFlags is returned when package flag refuses the vector.
	//
	// flag's own message quotes the operator's value ("invalid value \"abc\"
	// for flag -n"), so it lives in a field and in Private and never in
	// Public, which is the wire-safe half.
	InvalidFlags = errs.Define(CodeInvalidFlags, "INVALID_FLAGS",
		"The flags for this command could not be parsed",
		"service/app/cli: flag.FlagSet.Parse refused the vector; the fields carry the command path and flag's own text",
		errs.WithExitCode(exitUsage))

	// CommandPanicked is the failure of an Action that panicked.
	//
	// It is recovered rather than left to the runtime for one reason that is
	// not "hiding the bug": a panicking Go program exits with status 2, which
	// sysexits gives no meaning and which several supervisors read as a usage
	// error. Nothing is lost — the recovered value AND the stack of the
	// goroutine that actually failed both travel as fields — and what changes
	// is that main, rather than the runtime, decides what the process does
	// next. It keeps the default EX_SOFTWARE (70): the command line was fine.
	CommandPanicked = errs.Define(CodeCommandPanicked, "COMMAND_PANICKED",
		"The command panicked and was recovered",
		"service/app/cli: the Action panicked; the fields carry the command path, the panic value and the originating stack")

	// HelpWriteFailed is returned when -h asked for the help and the
	// diagnostic stream did not take it.
	//
	// Only the -h path returns it. After a bad command line the usage error is
	// the verdict and the help is the actionable half of it, so a failed write
	// there does not replace the status the operator's mistake earned.
	HelpWriteFailed = errs.Define(CodeHelpWriteFailed, "HELP_WRITE_FAILED",
		"The help could not be written",
		"service/app/cli: the diagnostic stream refused the help -h asked for, or took part of it; the fields carry the command path",
		errs.WithExitCode(exitIOErr))
)
