// Package lifecycle — declares the sentinel *errs.Error port outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package lifecycle

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Add will be refused forever, and the fix is
// a code change at the call site, never a retry.
const exitConfig int = 78

var (
	// InvalidComponent is returned by Add for a component that could never
	// run. The "missing" field names which half is absent.
	InvalidComponent = errs.Define(CodeInvalidComponent, "INVALID_COMPONENT",
		"The lifecycle component is not runnable and was refused",
		"core/lifecycle: component has an empty name, a nil Start or a nil Stop; the field names which",
		errs.WithExitCode(exitConfig))

	// DuplicateComponent is returned by Add when the name is already taken.
	// Names are how a transition and an error identify a component, so two
	// components sharing one would make every report ambiguous.
	DuplicateComponent = errs.Define(CodeDuplicateComponent, "DUPLICATE_COMPONENT",
		"A component is already registered under that name",
		"core/lifecycle: component names are unique per Lifecycle; the field carries the name",
		errs.WithExitCode(exitConfig))

	// LifecycleRunning is returned by Add, and by a second Start, while the
	// Lifecycle is started. The order is frozen for the duration of a run:
	// appending to it mid-flight would give the new component a start
	// position it never had, and no defensible place in the reverse order.
	LifecycleRunning = errs.Define(CodeLifecycleRunning, "LIFECYCLE_RUNNING",
		"The lifecycle is already started",
		"core/lifecycle: the component order is frozen once Start begins; add before Start, or after Stop returns",
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
		"core/lifecycle: component panicked; the fields carry its name, the phase, and the panic value")
)
