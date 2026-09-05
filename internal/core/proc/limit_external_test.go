package proc_test

import (
	"math"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// LimitInfinity must be the all-ones value the kernel reads as RLIM_INFINITY.
// Writing it as ^uint64(0) rather than a literal is what keeps it correct on
// every width, and this pins that it did not drift to a merely large number —
// a limit one below infinity is a real ceiling the kernel would enforce.
func Test_LimitInfinity(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  uint64
		want uint64
	}
	tests := []tc{
		{"the sentinel is all ones", coreproc.LimitInfinity, math.MaxUint64},
		{"it is not merely large", coreproc.LimitInfinity, ^uint64(0)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.got != c.want {
			t.Errorf("LimitInfinity = %#x, want %#x", c.got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The pair carries no policy of its own: Soft is the enforced value and Hard
// the ceiling, and the type stores exactly what a caller set. The invariant
// Soft <= Hard is the caller's to keep — the value type does not silently
// clamp, because a clamp would hide a misconfiguration the kernel would have
// reported.
func Test_LimitValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		limit coreproc.LimitValue
		soft  uint64
		hard  uint64
	}
	tests := []tc{
		{"a bounded pair", coreproc.LimitValue{Soft: 1024, Hard: 4096}, 1024, 4096},
		{"soft equal to hard", coreproc.LimitValue{Soft: 512, Hard: 512}, 512, 512},
		{
			"an unlimited resource",
			coreproc.LimitValue{Soft: coreproc.LimitInfinity, Hard: coreproc.LimitInfinity},
			coreproc.LimitInfinity, coreproc.LimitInfinity,
		},
		{"the zero value forbids everything", coreproc.LimitValue{}, 0, 0},
		{
			// Stored verbatim: the type does not clamp, so the kernel is the
			// one that reports the mistake.
			"an inverted pair is stored as written",
			coreproc.LimitValue{Soft: 4096, Hard: 1024},
			4096, 1024,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.limit.Soft != c.soft {
			t.Errorf("Soft = %d, want %d", c.limit.Soft, c.soft)
		}
		if c.limit.Hard != c.hard {
			t.Errorf("Hard = %d, want %d", c.limit.Hard, c.hard)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
