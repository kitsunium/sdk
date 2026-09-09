// Package lifecycle — declares the sentinel *errs.Error run outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package lifecycle

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitTempFail matches sysexits EX_TEMPFAIL (75). A budget that expired is a
// timing outcome, not a permanent fault: the same shutdown attempted again on
// a less loaded machine may well complete.
const exitTempFail int = 75

var (
	// StartFailed is joined with the component's own error when a Start
	// aborts the sequence. It is a SEPARATE error in an errors.Join rather
	// than a wrapper around the component's error on purpose: errs.Wrap would
	// hit the origin-wins rule (CLAUDE.md rule 6) and inherit the component's
	// code, so the caller could no longer ask "did startup fail?" without
	// knowing every code a component might produce. Side by side, both
	// errs.HasCode(err, CodeStartFailed) and the caller's own errors.Is
	// answer.
	StartFailed = errs.Define(CodeStartFailed, "START_FAILED",
		"A component failed to start and the started components were stopped",
		"service/lifecycle: Start aborted; the fields name the component and how many were unwound")

	// StopFailed is joined with the component's own error when its Stop
	// returns one. The shutdown continues: a component that cannot close
	// cleanly must not prevent the ones before it in the order from trying.
	StopFailed = errs.Define(CodeStopFailed, "STOP_FAILED",
		"A component reported an error while stopping",
		"service/lifecycle: Stop returned an error; the field names the component and the shutdown continued")

	// StopTimeout is returned for a component whose Stop had not returned
	// when its budget expired. The engine cancels the context it handed that
	// Stop — an announcement — and stops WAITING; it does not kill the
	// goroutine, which Go cannot do, and it closes nothing on the component's
	// behalf, which is what severed sockets under live handlers before ADR
	// 0043. Every component after it in the reverse order still gets its own
	// full budget.
	StopTimeout = errs.Define(CodeStopTimeout, "STOP_TIMEOUT",
		"A component did not stop within its budget and was abandoned",
		"service/lifecycle: the per-component stop budget expired; the goroutine was abandoned, not killed",
		errs.WithExitCode(exitTempFail))

	// UnwindFailed marks a failure that happened while cleaning up a partial
	// start. It is joined with — never substituted for — the start failure
	// that triggered the unwind: a teardown that also breaks is a second
	// defect, and reporting only it would hide the first.
	UnwindFailed = errs.Define(CodeUnwindFailed, "UNWIND_FAILED",
		"Cleaning up after a failed start did not complete cleanly",
		"service/lifecycle: one or more components failed to stop during the partial-start unwind")

	// ReadinessFailed is returned when the opt-in sd_notify datagram could
	// not be delivered. It is only ever reachable when the caller asked for
	// the notification: with NOTIFY_SOCKET unset every notifier call is a
	// documented no-op that returns nil, so an unsupervised binary never sees
	// this.
	ReadinessFailed = errs.Define(CodeReadinessFailed, "READINESS_FAILED",
		"The readiness notification could not be delivered",
		"service/lifecycle: sd_notify was requested and failed; the field names the state that was not delivered")
)
