// External tests for the public memlimit surface.
package memlimit_test

import (
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/memlimit"
)

// negativeProbe is the argument that makes debug.SetMemoryLimit report the
// current limit instead of assigning a new one.
const negativeProbe int64 = -1

// readCurrentLimit returns the runtime's current soft memory limit without
// changing it: debug.SetMemoryLimit(-1) is the documented read-only probe.
func readCurrentLimit() int64 {
	//: Return the computed result to the caller.
	return debug.SetMemoryLimit(negativeProbe)
}

// TestApply verifies Apply defers to an operator who set GOMEMLIMIT, and that
// declining leaves the runtime limit untouched.
//
// Silently overriding an explicit GOMEMLIMIT would contradict a deliberate
// choice with a cgroup-derived guess; leaving the runtime untouched when
// declining is what makes Apply safe to call unconditionally at startup on
// any host, including those without cgroups at all.
func TestApply(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "byte count", value: "536870912"},
		{name: "suffixed", value: "1GiB"},
		{name: "off", value: "off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Not parallel: Setenv and the runtime limit are process-wide.
			t.Setenv("GOMEMLIMIT", tt.value)
			before := readCurrentLimit()

			got := memlimit.Apply()
			//: An explicit GOMEMLIMIT must always win, so Apply declines.
			if got.Applied() {
				t.Errorf("Apply() applied %d bytes, want it to defer to GOMEMLIMIT", got.Limit)
			}
			if got.Limit != 0 {
				t.Errorf("Apply() Limit = %d, want 0", got.Limit)
			}
			//: The public surface must name the operator as the decider; a
			//: bare "declined" cannot be told from an uncapped host.
			if got.Source != memlimit.MemorySourceOperator {
				t.Errorf("Apply() Source = %v, want %v", got.Source, memlimit.MemorySourceOperator)
			}
			//: Declining must be observable as "nothing moved".
			if after := readCurrentLimit(); after != before {
				t.Errorf("runtime limit moved from %d to %d, want it untouched", before, after)
			}
		})
	}
}

// TestSourceString verifies every source renders a distinct, stable name, and
// that a value the package never mints reads as a defect rather than a number.
func TestSourceString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source memlimit.Source
		want   string
	}{
		{name: "operator", source: memlimit.MemorySourceOperator, want: "operator"},
		{name: "unconstrained", source: memlimit.MemorySourceUnconstrained, want: "unconstrained"},
		{name: "below floor", source: memlimit.MemorySourceBelowFloor, want: "below-floor"},
		{name: "cgroup", source: memlimit.MemorySourceCgroup, want: "cgroup"},
		{name: "the unminted zero", source: memlimit.Source(0), want: "unknown"},
		{name: "out of range", source: memlimit.Source(99), want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: A log line is the only consumer, so the rendering IS the contract.
			if got := tt.source.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLimitAppliedIsSourceDriven verifies Applied answers from Source alone, so
// a zero Limit on the applied path could never read as "nothing happened".
func TestLimitAppliedIsSourceDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit memlimit.Limit
		want  bool
	}{
		{name: "cgroup applied", limit: memlimit.Limit{Source: memlimit.MemorySourceCgroup}, want: true},
		{name: "operator declined", limit: memlimit.Limit{Source: memlimit.MemorySourceOperator}, want: false},
		{name: "unconstrained declined", limit: memlimit.Limit{Source: memlimit.MemorySourceUnconstrained}, want: false},
		{name: "below floor declined", limit: memlimit.Limit{Source: memlimit.MemorySourceBelowFloor, Allowance: 1}, want: false},
		{name: "the unminted zero value", limit: memlimit.Limit{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Applied must not be inferrable from Limit alone.
			if got := tt.limit.Applied(); got != tt.want {
				t.Errorf("Applied() = %t, want %t", got, tt.want)
			}
		})
	}
}
