package proc_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
)

// TestResourceString asserts each Resource renders its systemd directive suffix
// and unknown values degrade to "unknown".
func Test_Resource_String(t *testing.T) {
	t.Parallel()

	type resCase struct {
		res  proc.Resource
		want string
	}
	cases := []resCase{
		{res: proc.ResourceNoFile, want: "nofile"},
		{res: proc.ResourceNProc, want: "nproc"},
		{res: proc.ResourceCore, want: "core"},
		{res: proc.ResourceMemLock, want: "memlock"},
		{res: proc.ResourceUnknown, want: "unknown"},
		{res: proc.Resource(9999), want: "unknown"},
	}

	runCase := func(t *testing.T, tc resCase) {
		t.Helper()
		//: the rendered name must match the directive suffix exactly.
		if got := tc.res.String(); got != tc.want {
			t.Fatalf("Resource(%d).String() = %q, want %q", tc.res, got, tc.want)
		}
	}

	for _, tc := range cases {
		//: subtest per resource isolates a regressed mapping.
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestResourceKnown asserts only defined, non-zero resources report Known.
func Test_Resource_Known(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		resource proc.Resource
		want     bool
	}
	tests := []tc{
		{"the reserved zero value", proc.ResourceUnknown, false},
		{"the first defined resource", proc.ResourceNoFile, true},
		{"a middle resource", proc.ResourceCPU, true},
		{"the last defined resource", proc.ResourceMemLock, true},
		{"one past the range", proc.ResourceMemLock + 1, false},
		{"a negative value", proc.Resource(-1), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.resource.Known(); got != c.want {
			t.Errorf("Known() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestLimitInfinity(t *testing.T) {
	t.Parallel()

	//: LimitInfinity must be the unsigned all-ones value (RLIM_INFINITY).
	if proc.LimitInfinity != ^uint64(0) {
		t.Fatalf("LimitInfinity = %d, want %d", proc.LimitInfinity, ^uint64(0))
	}
}
