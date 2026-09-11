// Package scheduler — declares the sentinel *errs.Error parser outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package scheduler

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). Every sentinel here rejects a
// schedule the caller wrote down: the fault is permanent, retrying the same
// expression will be refused identically, and the fix is an edit.
const exitConfig int = 78

var (
	// InvalidExpression is returned by Parse for a malformed or out-of-range
	// cron field.
	InvalidExpression = errs.Define(CodeInvalidExpression, "INVALID_EXPRESSION",
		"The cron expression is not valid",
		"service/scheduler: cron field malformed or out of range; the fields name the field, the item and the accepted range",
		errs.WithExitCode(exitConfig))

	// UnsupportedSyntax is returned by Parse for a construct this subset
	// refuses by name instead of interpreting. Refusing loudly is the whole
	// point: a parser that silently ignores a Quartz "L" would run a job on
	// the wrong day and report success.
	UnsupportedSyntax = errs.Define(CodeUnsupportedSyntax, "UNSUPPORTED_SYNTAX",
		"That cron syntax is outside the accepted subset",
		"service/scheduler: five-field POSIX cron plus @yearly/@monthly/@weekly/@daily/@hourly only; the fields name what was rejected",
		errs.WithExitCode(exitConfig))

	// UnreachableSchedule is returned by Parse for a valid expression that
	// matches no instant within the search horizon.
	UnreachableSchedule = errs.Define(CodeUnreachableSchedule, "UNREACHABLE_SCHEDULE",
		"The cron expression matches no date on the calendar",
		"service/scheduler: expression parsed but no instant matches within the horizon — e.g. day 31 of a 30-day month",
		errs.WithExitCode(exitConfig))

	// InvalidLocation is returned by ParseInLocation for a nil location.
	InvalidLocation = errs.Define(CodeInvalidLocation, "INVALID_LOCATION",
		"A time zone is required and none was given",
		"service/scheduler: ParseInLocation needs a non-nil *time.Location; use Parse for UTC",
		errs.WithExitCode(exitConfig))

	// InvalidInterval is returned by Every for a non-positive duration.
	InvalidInterval = errs.Define(CodeInvalidInterval, "INVALID_INTERVAL",
		"The interval must be greater than zero",
		"service/scheduler: Every needs a positive period; a non-positive one is due infinitely often",
		errs.WithExitCode(exitConfig))
)
