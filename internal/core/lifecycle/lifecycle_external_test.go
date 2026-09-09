package lifecycle_test

import (
	"context"
	"testing"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestSentinelsCarryTheirAllocatedCode pins every sentinel to the dotted-quad
// value ADR 0050 allocated to this package. The registry audits check that the
// range is owned and that no two codes collide; neither of them checks that a
// given SENTINEL still carries the code its documentation names, which is what
// a consumer's errs.HasCode call actually depends on.
func TestSentinelsCarryTheirAllocatedCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *kerrs.Error
		code     kerrs.Code
		reason   string
	}{
		{"invalid component", corelc.InvalidComponent, corelc.CodeInvalidComponent, "INVALID_COMPONENT"},
		{"duplicate component", corelc.DuplicateComponent, corelc.CodeDuplicateComponent, "DUPLICATE_COMPONENT"},
		{"lifecycle running", corelc.LifecycleRunning, corelc.CodeLifecycleRunning, "LIFECYCLE_RUNNING"},
		{"component panicked", corelc.ComponentPanicked, corelc.CodeComponentPanicked, "COMPONENT_PANICKED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.Code(); got != tc.code {
				t.Errorf("code = %#x, want %#x", uint32(got), uint32(tc.code))
			}
			if got := tc.sentinel.Reason(); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// TestRegistrationRefusalsAreNotTransient pins the exit codes apart. A caller
// that treats lifecycle errors as one class needs the registration refusals to
// be distinguishable from a component's own failure: re-running Add with the
// same arguments will be refused identically, so EX_CONFIG (78) is the honest
// answer, while a panicking component is a software fault (EX_SOFTWARE, 70).
func TestRegistrationRefusalsAreNotTransient(t *testing.T) {
	t.Parallel()
	const exitConfig int = 78
	const exitSoftware int = 70
	tests := []struct {
		name     string
		sentinel *kerrs.Error
		exit     int
	}{
		{"invalid component", corelc.InvalidComponent, exitConfig},
		{"duplicate component", corelc.DuplicateComponent, exitConfig},
		{"lifecycle running", corelc.LifecycleRunning, exitConfig},
		{"component panicked", corelc.ComponentPanicked, exitSoftware},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.ExitCode(); got != tc.exit {
				t.Errorf("exit code = %d, want %d", got, tc.exit)
			}
		})
	}
}

// TestPortsAreFunctionsNotInterfaces is the executable half of the ADR 0039
// argument recorded in this package's doc comment. Start and Stop are named
// FUNC types precisely so they cannot grow a method and break every downstream
// implementer; a future contributor who "improves" either into an interface
// fails this assignment at compile time, before review.
func TestPortsAreFunctionsNotInterfaces(t *testing.T) {
	t.Parallel()
	up := corelc.Start(func(_ context.Context) error { return nil })
	down := corelc.Stop(func(_ context.Context) error { return nil })
	if err := up(context.Background()); err != nil {
		t.Errorf("Start returned %v, want nil", err)
	}
	if err := down(context.Background()); err != nil {
		t.Errorf("Stop returned %v, want nil", err)
	}
	//: a ComponentValue is assignable from the bare func literals too, which
	//: is the ergonomic the func ports exist for: no adapter at the call site.
	component := corelc.ComponentValue{
		Name:  "db",
		Start: func(_ context.Context) error { return nil },
		Stop:  func(_ context.Context) error { return nil },
	}
	if component.Start == nil || component.Stop == nil {
		t.Fatalf("ComponentValue did not accept bare func literals")
	}
}

// TestPhaseRendersItsTwoValuesAndNothingElse pins the closed set. A Phase is
// two things; a third value reaching a log line means something minted one
// this package never produces, and "unknown" says so instead of printing a
// number a reader would have to look up.
func TestPhaseRendersItsTwoValuesAndNothingElse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		phase corelc.Phase
		want  string
	}{
		{"start", corelc.PhaseStart, "start"},
		{"stop", corelc.PhaseStop, "stop"},
		{"the zero value is not a phase", corelc.Phase(0), "unknown"},
		{"nor is anything past the pair", corelc.Phase(9), "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.phase.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
