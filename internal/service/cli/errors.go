// Package cli — declares the sentinel *errs.Error engine outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cli

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitUsage matches sysexits EX_USAGE (64): the command line was wrong. It is
// deliberately a different status from the EX_CONFIG (78) internal/core/cli
// returns for a refused DECLARATION — one says the operator mistyped, the
// other says main is wired wrong, and a supervisor that restarts on one and
// not the other needs to be able to tell them apart.
const exitUsage int = 64

var (
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
		"service/cli: the token names no child of this group; the fields carry the group path and the token",
		errs.WithExitCode(exitUsage))

	// MissingCommand is returned when a group is invoked with nothing after
	// its flags.
	//
	// It is a failure and not a quiet success on purpose: a group declares no
	// action, so `tool db` did nothing, and a script that read exit status 0
	// there would treat "I forgot the verb" as "the migration ran".
	MissingCommand = errs.Define(CodeMissingCommand, "MISSING_COMMAND",
		"This command needs a sub-command",
		"service/cli: a group has no action of its own; the field carries the group path and its help was written",
		errs.WithExitCode(exitUsage))

	// InvalidFlags is returned when package flag refuses the vector.
	//
	// flag's own message quotes the operator's value ("invalid value \"abc\"
	// for flag -n"), so it lives in a field and in Private and never in
	// Public, which is the wire-safe half.
	InvalidFlags = errs.Define(CodeInvalidFlags, "INVALID_FLAGS",
		"The flags for this command could not be parsed",
		"service/cli: flag.FlagSet.Parse refused the vector; the fields carry the command path and flag's own text",
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
		"service/cli: the Action panicked; the fields carry the command path, the panic value and the originating stack")
)
