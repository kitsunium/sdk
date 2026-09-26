// Package health — declares the engine's sentinel *errs.Error outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public here is written to be READ BY A STRANGER. A probe endpoint is
// routinely exposed more widely than whoever added it expected — a mesh
// sidecar, a load balancer health page, an uptime checker — so these strings
// name a condition and never a cause, a host, a port or a query.
package health

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Add will be refused forever.
const exitConfig int = 78

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). A failing check is a
// runtime condition about something the process depends on, not a defect in
// the process's own configuration.
const exitUnavailable int = 69

var (
	// CheckFailed is the code a PLAIN error from a check is wrapped with. An
	// *errs.Error cause keeps its own identity through origin-wins, so a
	// caller who already writes typed errors sees their own Public in the
	// probe body and everyone else sees this one.
	CheckFailed = errs.Define(CodeCheckFailed, "CHECK_FAILED",
		"A health check reported a failure",
		"service/health: check returned a non-nil error; the fields carry its name and probe",
		errs.WithExitCode(exitUnavailable))

	// CheckTimeout reports a check that had not answered when its budget
	// expired.
	//
	// It is a FAILURE and not an "unknown", and that is a decision (ADR 0060
	// §D4). A probe exists to answer a question within a bounded time, and
	// "I could not answer" is operationally the same as "no" for the caller
	// who has to decide whether to route traffic. An "unknown" that
	// aggregated as healthy would make a wedged dependency invisible — the
	// exact failure probes exist to catch — and one that aggregated as
	// unhealthy would just be a second spelling of this. What IS preserved is
	// the distinction: ResultValue.TimedOut separates "the dependency said
	// no" from "the dependency said nothing".
	CheckTimeout = errs.Define(CodeCheckTimeout, "CHECK_TIMEOUT",
		"A health check did not answer within its budget",
		"service/health: check exceeded its timeout; the fields carry its name, probe and budget",
		errs.WithExitCode(exitUnavailable))

	// StaleCacheWindow is returned by AddReadiness for a MaxAge above
	// [MaxCacheAge]. Refused rather than clamped: see MaxCacheAge.
	StaleCacheWindow = errs.Define(CodeStaleCacheWindow, "STALE_CACHE_WINDOW",
		"The health check cache window is longer than the SDK will vouch for",
		"service/health: MaxAge exceeds MaxCacheAge; the fields carry both",
		errs.WithExitCode(exitConfig))

	// StartupPending is the synthetic result of a readiness probe answered
	// while a startup check has yet to pass. No readiness check was run,
	// because "not yet started" already answers the routing question and
	// dialling a dependency to reconfirm it would only add load.
	StartupPending = errs.Define(CodeStartupPending, "STARTUP_PENDING",
		"The process is still starting and is not accepting traffic yet",
		"service/health: readiness short-circuited; a startup check has not passed",
		errs.WithExitCode(exitUnavailable))

	// Draining is the synthetic result of a readiness probe answered after
	// Drain. It is what lets an orchestrator withdraw the replica from
	// routing BEFORE the shutdown sequence starts closing anything.
	//
	// Liveness deliberately keeps answering normally while this is true: a
	// draining process that gets killed for failing liveness loses exactly
	// the in-flight work the drain existed to finish.
	Draining = errs.Define(CodeDraining, "DRAINING",
		"The process is shutting down and is no longer accepting traffic",
		"service/health: readiness short-circuited; Drain has been called and is one-way",
		errs.WithExitCode(exitUnavailable))

	// NotifyFailed is handed to Config.OnNotifyError when an opt-in sd_notify
	// datagram could not be delivered. With $NOTIFY_SOCKET unset the notifier
	// is a no-op that succeeds, so this means a socket was configured and did
	// not take the message.
	NotifyFailed = errs.Define(CodeNotifyFailed, "NOTIFY_FAILED",
		"The readiness notification could not be delivered",
		"service/health: sd_notify send failed; the field carries the state that was being sent")

	// AskMisconfigured refuses an Ask that could never get an answer: the
	// address has no host:port shape or no port in 1–65535, the path is not
	// absolute, or the timeout is negative. Nothing is dialled; the argument
	// field names which.
	AskMisconfigured = errs.Define(CodeAskMisconfigured, "ASK_MISCONFIGURED",
		"The readiness probe was given an address, path or timeout it cannot use",
		"service/health: Ask refused its configuration before dialling; the argument field names what",
		errs.WithExitCode(exitConfig))

	// AskUnreachable reports an Ask that got no answer because the connection
	// or the request failed. The transport's own error is the cause.
	AskUnreachable = errs.Define(CodeAskUnreachable, "ASK_UNREACHABLE",
		"The process could not be reached",
		"service/health: the connection or the request failed before any answer; the cause is the transport's",
		errs.WithExitCode(exitUnavailable))

	// AskTimeout reports an Ask with no answer when its budget ran out. A
	// caller whose own context ended first gets the same code, with the
	// context's error in the chain — the precedent CheckTimeout set for a
	// departed caller.
	AskTimeout = errs.Define(CodeAskTimeout, "ASK_TIMEOUT",
		"The process did not answer the readiness probe in time",
		"service/health: the budget or the caller's context ended before an answer arrived",
		errs.WithExitCode(exitUnavailable))

	// AskNotReady reports an Ask the process answered with a status other than
	// 200. The status field carries it; the body was drained and discarded and
	// no byte of it is in the error.
	AskNotReady = errs.Define(CodeAskNotReady, "ASK_NOT_READY",
		"The process answered that it is not ready",
		"service/health: the process answered a status other than 200; the status field carries it",
		errs.WithExitCode(exitUnavailable))
)
