// Package lifecycle — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the engine's and the supervisor's
// verdicts on a run. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package lifecycle

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration — and a
// supervisor wired wrong — is a permanent wiring fault: the same call will be
// refused forever, and the fix is a code change at the call site, never a
// retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A budget that expired is a
// timing outcome, not a permanent fault: the same shutdown attempted again on
// a less loaded machine may well complete.
const exitTempFail int = 75

var (
	// InvalidComponent is returned by Add for a component that could never
	// run. The "missing" field names which half is absent.
	InvalidComponent = errs.Define(CodeInvalidComponent, "INVALID_COMPONENT",
		"The lifecycle component is not runnable and was refused",
		"core/app/lifecycle: component has an empty name, a nil Start or a nil Stop; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateComponent is returned by Add when the name is already taken.
	// Names are how a transition and an error identify a component, so two
	// components sharing one would make every report ambiguous.
	DuplicateComponent = errs.Define(CodeDuplicateComponent, "DUPLICATE_COMPONENT",
		"A component is already registered under that name",
		"core/app/lifecycle: component names are unique per Lifecycle; the field carries the name",
		errs.WithExitCode(exitConfig))

	// LifecycleRunning is returned by Add, and by a second Start, while the
	// Lifecycle is started. The order is frozen for the duration of a run:
	// appending to it mid-flight would give the new component a start
	// position it never had, and no defensible place in the reverse order.
	LifecycleRunning = errs.Define(CodeLifecycleRunning, "LIFECYCLE_RUNNING",
		"The lifecycle is already started",
		"core/app/lifecycle: the component order is frozen once Start begins; add before Start, or after Stop returns",
		errs.WithExitCode(exitConfig))

	// ComponentPanicked is the failure of a component whose Start or Stop
	// panicked. The Lifecycle recovers it rather than letting it reach the
	// runtime, because a panic escaping a Start would skip the unwind
	// entirely and leak every component already up — the exact failure this
	// domain exists to make impossible. The recovered value travels as a
	// field; it is never the wrap origin, so a panic carrying an *errs.Error
	// cannot hijack this code.
	ComponentPanicked = errs.Define(CodeComponentPanicked, "COMPONENT_PANICKED",
		"The lifecycle component panicked and was recovered",
		"core/app/lifecycle: component panicked; the fields carry its name, the phase, and the panic value")

	// The engine's and the supervisor's verdicts on a run, raised by
	// internal/service/app/lifecycle. They are declared here, with the
	// registration refusals, so that the domain's codes and sentinels are in one
	// place (ADR 0160).

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
		"service/app/lifecycle: Start aborted; the fields name the component and how many were unwound")

	// StopFailed is joined with the component's own error when its Stop
	// returns one. The shutdown continues: a component that cannot close
	// cleanly must not prevent the ones before it in the order from trying.
	StopFailed = errs.Define(CodeStopFailed, "STOP_FAILED",
		"A component reported an error while stopping",
		"service/app/lifecycle: Stop returned an error; the field names the component and the shutdown continued")

	// StopTimeout is returned for a component whose Stop had not returned
	// when its budget expired. The engine cancels the context it handed that
	// Stop — an announcement — and stops WAITING; it does not kill the
	// goroutine, which Go cannot do, and it closes nothing on the component's
	// behalf, which is what severed sockets under live handlers before ADR
	// 0043. Every component after it in the reverse order still gets its own
	// full budget.
	StopTimeout = errs.Define(CodeStopTimeout, "STOP_TIMEOUT",
		"A component did not stop within its budget and was abandoned",
		"service/app/lifecycle: the per-component stop budget expired; the goroutine was abandoned, not killed",
		errs.WithExitCode(exitTempFail))

	// UnwindFailed marks a failure that happened while cleaning up a partial
	// start. It is joined with — never substituted for — the start failure
	// that triggered the unwind: a teardown that also breaks is a second
	// defect, and reporting only it would hide the first.
	UnwindFailed = errs.Define(CodeUnwindFailed, "UNWIND_FAILED",
		"Cleaning up after a failed start did not complete cleanly",
		"service/app/lifecycle: one or more components failed to stop during the partial-start unwind")

	// ReadinessFailed is returned when the opt-in sd_notify datagram could
	// not be delivered. It is only ever reachable when the caller asked for
	// the notification: with NOTIFY_SOCKET unset every notifier call is a
	// documented no-op that returns nil, so an unsupervised binary never sees
	// this.
	ReadinessFailed = errs.Define(CodeReadinessFailed, "READINESS_FAILED",
		"The readiness notification could not be delivered",
		"service/app/lifecycle: sd_notify was requested and failed; the field names the state that was not delivered")

	// RunPanicked is the failure of a supervised run that panicked. The
	// supervisor recovers it on the run's goroutine, because a panic escaping
	// there would take the process down with every other goroutine in it;
	// the recovered value and the stack travel as FIELDS, never as the
	// origin, so a panic carrying an *errs.Error cannot hijack this code —
	// and never in the Public text, which a status page may show.
	RunPanicked = errs.Define(CodeRunPanicked, "RUN_PANICKED",
		"The supervised function panicked and was recovered",
		"service/app/lifecycle: a supervised run panicked; the fields carry the supervisor's name, the panic value and the stack, and the run is restarted after its backoff")

	// SupervisorMisconfigured refuses a supervisor that could never run.
	SupervisorMisconfigured = errs.Define(CodeSupervisorMisconfigured, "SUPERVISOR_MISCONFIGURED",
		"The supervisor cannot run as configured",
		"service/app/lifecycle: NewSupervisor needs a Name and a Run; the field names which is missing",
		errs.WithExitCode(exitConfig))

	// SupervisorRunning refuses a second Start while the first supervision
	// is still running: two loops over one function would be two owners of
	// whatever it drives.
	SupervisorRunning = errs.Define(CodeSupervisorRunning, "SUPERVISOR_RUNNING",
		"The supervisor is already running",
		"service/app/lifecycle: Start was called while a supervision is running; Stop it first",
		errs.WithExitCode(exitConfig))
)
