package scheduler_test

import (
	"context"
	"testing"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestSentinelsCarryTheirAllocatedCode pins every sentinel to the dotted-quad
// value ADR 0041 allocated to this package. The registry audits check that the
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
		{"invalid entry", coresched.InvalidEntry, coresched.CodeInvalidEntry, "INVALID_ENTRY"},
		{"duplicate job", coresched.DuplicateJob, coresched.CodeDuplicateJob, "DUPLICATE_JOB"},
		{"scheduler running", coresched.SchedulerRunning, coresched.CodeSchedulerRunning, "SCHEDULER_RUNNING"},
		{"job panicked", coresched.JobPanicked, coresched.CodeJobPanicked, "JOB_PANICKED"},
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
// that treats scheduler errors as one class needs the registration refusals to
// be distinguishable from a job's own failure: re-running Add with the same
// arguments will be refused identically, so EX_CONFIG (78) is the honest
// answer, while a panicking job is a software fault (EX_SOFTWARE, 70).
func TestRegistrationRefusalsAreNotTransient(t *testing.T) {
	t.Parallel()
	const exitConfig int = 78
	const exitSoftware int = 70
	tests := []struct {
		name     string
		sentinel *kerrs.Error
		exit     int
	}{
		{"invalid entry", coresched.InvalidEntry, exitConfig},
		{"duplicate job", coresched.DuplicateJob, exitConfig},
		{"scheduler running", coresched.SchedulerRunning, exitConfig},
		{"job panicked", coresched.JobPanicked, exitSoftware},
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
// argument recorded in this package's doc comment. Job and Schedule are named
// FUNC types precisely so they cannot grow a method and break every downstream
// implementer; a future contributor who "improves" either into an interface
// fails this assignment at compile time, before review.
func TestPortsAreFunctionsNotInterfaces(t *testing.T) {
	t.Parallel()
	job := coresched.Job(func(_ context.Context) error { return nil })
	schedule := coresched.Schedule(func(after time.Time) (time.Time, bool) {
		return after.Add(time.Minute), true
	})
	if err := job(context.Background()); err != nil {
		t.Errorf("job returned %v, want nil", err)
	}
	origin := time.Date(2031, time.March, 7, 4, 5, 0, 0, time.UTC)
	next, ok := schedule(origin)
	if !ok || !next.Equal(origin.Add(time.Minute)) {
		t.Errorf("schedule(origin) = %v, %v; want %v, true", next, ok, origin.Add(time.Minute))
	}
}
