// Package events — declares the sentinel *errs.Error dispatch outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package events

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A listener that halts without
// the authority to is a wiring fault: the same dispatch will refuse it
// identically forever, and the fix is a field at the registration site.
const exitConfig int = 78

var (
	// ListenerFailed is joined with the listener's own error when that
	// listener returns one. It is a SEPARATE error in an errors.Join rather
	// than a wrapper around the listener's error on purpose: errs.Wrap would
	// hit the origin-wins rule (CLAUDE.md rule 6) and inherit the listener's
	// code, so a caller could no longer ask "did any listener fail?" without
	// first knowing every code any listener might produce. Side by side, both
	// errs.HasCode(err, CodeListenerFailed) and the caller's own errors.Is
	// answer — the shape service/lifecycle already uses for the same reason.
	ListenerFailed = errs.Define(CodeListenerFailed, "LISTENER_FAILED",
		"An event listener reported an error",
		"service/events: listener returned an error and the dispatch continued; the fields name the listener and the event type")

	// HaltNotPermitted is returned for a listener that returned the Halt
	// control sentinel without SubscriptionValue.MayHalt.
	//
	// The dispatch is NOT stopped. Honouring the halt anyway would make the
	// permission decorative, and silently swallowing it would leave a
	// listener convinced it had vetoed an event that every one of its
	// siblings then saw — the more expensive of the two silences, because it
	// is invisible until the consequences diverge.
	HaltNotPermitted = errs.Define(CodeHaltNotPermitted, "HALT_NOT_PERMITTED",
		"A listener tried to stop the dispatch without the authority to",
		"service/events: listener returned Halt but its subscription has MayHalt false; propagation continued",
		errs.WithExitCode(exitConfig))
)
