package memlimit

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestApplyFrom_MemoryHigh pins that memory.high is read beside memory.max, at
// every depth, and that the tighter of the two binds the derivation and is
// named by Bound: the kernel throttles a group above memory.high long before
// memory.max kills it, so deriving from memory.max alone leaves the collector
// working above the throttle.
func TestApplyFrom_MemoryHigh(t *testing.T) {
	t.Parallel()
	const oneGiB int64 = 1 << 30
	type tc struct {
		name          string
		files         map[string]string
		wantAllowance int64
		wantSource    coreproc.MemorySource
		wantBound     coreproc.MemoryBound
	}
	tests := []tc{
		{
			name: "memory.high under memory.max binds",
			files: map[string]string{
				procSelfCgroup:                    "0::/app\n",
				"/sys/fs/cgroup/app/memory.max":   "2147483648",
				"/sys/fs/cgroup/app/memory.high":  "1073741824",
				"/sys/fs/cgroup/memory.max":       "max",
				"/sys/fs/cgroup/memory.high":      "max",
				"/sys/fs/cgroup/app/memory.other": "1",
			},
			wantAllowance: oneGiB, wantSource: coreproc.MemorySourceCgroup, wantBound: coreproc.MemoryBoundHigh,
		},
		{
			name: "memory.high at max leaves memory.max binding",
			files: map[string]string{
				procSelfCgroup:                   "0::/app\n",
				"/sys/fs/cgroup/app/memory.max":  "2147483648",
				"/sys/fs/cgroup/app/memory.high": "max",
			},
			wantAllowance: 2 * oneGiB, wantSource: coreproc.MemorySourceCgroup, wantBound: coreproc.MemoryBoundMax,
		},
		{
			name: "a memory.high on an ancestor only binds",
			files: map[string]string{
				procSelfCgroup:                          "0::/parent/app\n",
				"/sys/fs/cgroup/parent/app/memory.max":  "max",
				"/sys/fs/cgroup/parent/app/memory.high": "max",
				"/sys/fs/cgroup/parent/memory.max":      "4294967296",
				"/sys/fs/cgroup/parent/memory.high":     "1073741824",
			},
			wantAllowance: oneGiB, wantSource: coreproc.MemorySourceCgroup, wantBound: coreproc.MemoryBoundHigh,
		},
		{
			name: "a memory.max on an ancestor under a deeper memory.high binds",
			files: map[string]string{
				procSelfCgroup:                          "0::/parent/app\n",
				"/sys/fs/cgroup/parent/app/memory.high": "2147483648",
				"/sys/fs/cgroup/parent/memory.max":      "1073741824",
			},
			wantAllowance: oneGiB, wantSource: coreproc.MemorySourceCgroup, wantBound: coreproc.MemoryBoundMax,
		},
		{
			name: "a memory.high below the floor carries its allowance",
			files: map[string]string{
				procSelfCgroup:                   "0::/app\n",
				"/sys/fs/cgroup/app/memory.max":  "2147483648",
				"/sys/fs/cgroup/app/memory.high": "33554432",
			},
			wantAllowance: 32 << 20, wantSource: coreproc.MemorySourceBelowFloor, wantBound: coreproc.MemoryBoundHigh,
		},
		{
			name: "a v1 cap is the hard cap",
			files: map[string]string{
				procSelfCgroup: "9:memory:/pod\n",
				"/sys/fs/cgroup/memory/pod/memory.limit_in_bytes": "1073741824",
			},
			wantAllowance: oneGiB, wantSource: coreproc.MemorySourceCgroup, wantBound: coreproc.MemoryBoundMax,
		},
		{
			name:       "no cap names no bound",
			files:      map[string]string{procSelfCgroup: "0::/app\n", "/sys/fs/cgroup/app/memory.high": "max"},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var applied int64
		got := applyFrom(noEnv, fakeFS(c.files), func(limit int64) int64 { applied = limit; return 0 })
		//: the allowance, the outcome and the file that bound it.
		if got.Allowance != c.wantAllowance || got.Source != c.wantSource || got.Bound != c.wantBound {
			t.Fatalf("applyFrom = %+v, want allowance %d, source %v, bound %v",
				got, c.wantAllowance, c.wantSource, c.wantBound)
		}
		//: the derivation is the same 90 % of whichever file bound.
		if c.wantSource == coreproc.MemorySourceCgroup && applied != deriveLimit(c.wantAllowance) {
			t.Fatalf("SetMemoryLimit(%d), want %d", applied, deriveLimit(c.wantAllowance))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// TestMemoryBoundString pins the log spelling of each bound and of the
// unminted zero.
func TestMemoryBoundString(t *testing.T) {
	t.Parallel()
	//: the two minted values and the honest answer for the rest.
	for bound, want := range map[coreproc.MemoryBound]string{
		coreproc.MemoryBoundHigh: "high",
		coreproc.MemoryBoundMax:  "max",
		0:                        "unknown",
		9:                        "unknown",
	} {
		//: one spelling per value.
		if got := bound.String(); got != want {
			t.Errorf("MemoryBound(%d).String() = %q, want %q", bound, got, want)
		}
	}
}
