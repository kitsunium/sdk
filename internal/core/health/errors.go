// Package health — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package health

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Add will be refused forever, and the fix is
// a code change at the call site, never a retry.
const exitConfig int = 78

var (
	// InvalidCheck is returned by every Add for a check that could never run.
	// The "missing" field names which half is absent.
	InvalidCheck = errs.Define(CodeInvalidCheck, "INVALID_CHECK",
		"The health check is not runnable and was refused",
		"core/health: check has an empty name or a nil body; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateCheck is returned by an Add whose name is already registered
	// on that probe. Names are how a result and an error identify a check, so
	// two checks sharing one would make every report ambiguous.
	//
	// The namespace is PER PROBE: the same name may appear once on readiness
	// and once on liveness, because they are different questions about
	// different evidence and an operator reading "cache" on both is reading
	// two answers, not a collision.
	DuplicateCheck = errs.Define(CodeDuplicateCheck, "DUPLICATE_CHECK",
		"A health check is already registered under that name",
		"core/health: check names are unique per probe; the fields carry the name and the probe",
		errs.WithExitCode(exitConfig))

	// UnknownProbe is the result carried by a report for a Probe value this
	// package never mints. The probe answers StatusUnhealthy rather than
	// guessing: picking liveness for a caller who did not say what they
	// wanted is how a dependency outage becomes a restart loop.
	UnknownProbe = errs.Define(CodeUnknownProbe, "UNKNOWN_PROBE",
		"The requested health probe does not exist",
		"core/health: probe must be ProbeStartup, ProbeReadiness or ProbeLiveness; the field carries the value",
		errs.WithExitCode(exitConfig))

	// CheckPanicked is the failure of a check whose body panicked. The engine
	// recovers it rather than letting it reach the runtime, because a panic
	// escaping a probe would take the whole process down over a health
	// question — the health endpoint becoming the outage.
	//
	// The recovered value travels as a FIELD, never as the wrap origin, so a
	// panic carrying an *errs.Error cannot hijack this code, and the panic
	// text — which routinely carries an address or a query — cannot reach an
	// HTTP body through the Public of an error it did not write.
	CheckPanicked = errs.Define(CodeCheckPanicked, "CHECK_PANICKED",
		"The health check panicked and was recovered",
		"core/health: check panicked; the fields carry its name, its probe, and the panic value")
)
