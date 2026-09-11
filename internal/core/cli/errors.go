// Package cli — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cli

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel in this file is a
// refused DECLARATION: the same tree will be refused identically forever, the
// operator did nothing wrong, and the fix is a code change in main. That is
// not the same event as a mistyped command line, which carries EX_USAGE (64)
// from internal/service/cli.
const exitConfig int = 78

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
		"core/cli: blank or unreachable name, missing summary, or neither Run nor Commands; the fields name the path and the clause",
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
		"core/cli: Run and Commands are both set; adding a child would silently change what an existing command line means",
		errs.WithExitCode(exitConfig))

	// DuplicateCommand is returned by the constructor when two children of one
	// group share a name. The resolver stops at the first match, so the second
	// declaration would be unreachable code that looks reachable.
	DuplicateCommand = errs.Define(CodeDuplicateCommand, "DUPLICATE_COMMAND",
		"Two sub-commands are declared under the same name",
		"core/cli: sibling names are unique per group; the fields carry the parent path and the repeated name",
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
		"core/cli: a Binder bound h or help; the engine needs flag.ErrHelp to tell a help request from a usage error",
		errs.WithExitCode(exitConfig))
)
