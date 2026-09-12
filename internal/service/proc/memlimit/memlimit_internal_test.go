// Internal tests for the cgroup-derived Go soft memory limit.
package memlimit

import (
	"errors"
	"os"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// errNoFile stands in for a missing cgroup file, which is the normal state on
// any non-Linux host.
var errNoFile = errors.New("no such file")

// fakeFS returns a readFile stub serving contents by path, and os.ErrNotExist
// for anything absent — mirroring how a real host exposes only the hierarchy
// it actually mounts.
func fakeFS(contents map[string]string) func(string) ([]byte, error) {
	//: Return the computed result to the caller.
	return func(path string) ([]byte, error) {
		body, ok := contents[path]
		//: An unmapped path models an unmounted hierarchy.
		if !ok {
			//: Signal absence exactly as the OS would.
			return nil, errNoFile
		}

		//: Deliver the stubbed file contents.
		return []byte(body), nil
	}
}

// noEnv is a lookup stub reporting every variable as unset.
func noEnv(string) (string, bool) {
	//: Nothing is configured in this environment.
	return "", false
}

// TestApplyFrom covers every path that decides whether a limit is installed:
// the operator override, both cgroup versions, their "unlimited" spellings,
// an absent hierarchy, and caps too small to be worth enforcing.
//
// wantSource is asserted on every case, not only the applied ones: telling the
// three declining outcomes apart is the whole reason MemorySource exists, and a
// test that only checked Applied would pass with all three collapsed into one.
func TestApplyFrom(t *testing.T) {
	t.Parallel()

	const oneGiB int64 = 1 << 30

	tests := []struct {
		name          string
		env           func(string) (string, bool)
		files         map[string]string
		wantLimit     int64
		wantAllowance int64
		wantSource    coreproc.MemorySource
		wantApplied   bool
	}{
		{
			name:       "explicit GOMEMLIMIT is left alone",
			env:        func(string) (string, bool) { return "512MiB", true },
			files:      map[string]string{"/sys/fs/cgroup/memory.max": "1073741824"},
			wantSource: coreproc.MemorySourceOperator,
		},
		{
			name:          "cgroup v2 cap yields 90 percent",
			env:           noEnv,
			files:         map[string]string{"/sys/fs/cgroup/memory.max": "1073741824\n"},
			wantLimit:     oneGiB / 100 * 90,
			wantAllowance: oneGiB,
			wantSource:    coreproc.MemorySourceCgroup,
			wantApplied:   true,
		},
		{
			name:       "cgroup v2 max means unlimited",
			env:        noEnv,
			files:      map[string]string{"/sys/fs/cgroup/memory.max": "max\n"},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
		{
			name:          "cgroup v1 cap yields 90 percent",
			env:           noEnv,
			files:         map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824\n"},
			wantLimit:     oneGiB / 100 * 90,
			wantAllowance: oneGiB,
			wantSource:    coreproc.MemorySourceCgroup,
			wantApplied:   true,
		},
		{
			name:       "cgroup v1 sentinel means unlimited",
			env:        noEnv,
			files:      map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712\n"},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
		{
			name:       "no cgroup files at all",
			env:        noEnv,
			files:      map[string]string{},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
		{
			name:          "cap below the floor is rejected",
			env:           noEnv,
			files:         map[string]string{"/sys/fs/cgroup/memory.max": "16777216"},
			wantAllowance: 16777216,
			wantSource:    coreproc.MemorySourceBelowFloor,
		},
		{
			name:       "unparseable v2 content is ignored",
			env:        noEnv,
			files:      map[string]string{"/sys/fs/cgroup/memory.max": "not-a-number"},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
		{
			name:       "negative v1 content is ignored",
			env:        noEnv,
			files:      map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "-1"},
			wantSource: coreproc.MemorySourceUnconstrained,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var applied int64
			//: Record instead of mutating the real runtime limit, which
			//: would leak across parallel tests in this process.
			record := func(limit int64) int64 {
				applied = limit
				return 0
			}

			got := applyFrom(tt.env, fakeFS(tt.files), record)
			//: Whether a limit was installed is the primary contract.
			if got.Applied() != tt.wantApplied {
				t.Fatalf("Applied() = %t, want %t", got.Applied(), tt.wantApplied)
			}
			if got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.wantLimit)
			}
			//: WHY it declined is what a caller logs; collapsing the three
			//: declining outcomes into one would make this package unusable
			//: for diagnosis.
			if got.Source != tt.wantSource {
				t.Errorf("Source = %v, want %v", got.Source, tt.wantSource)
			}
			if got.Allowance != tt.wantAllowance {
				t.Errorf("Allowance = %d, want %d", got.Allowance, tt.wantAllowance)
			}
			//: The runtime must be touched only when a limit was derived.
			if tt.wantApplied && applied != tt.wantLimit {
				t.Errorf("SetMemoryLimit got %d, want %d", applied, tt.wantLimit)
			}
			if !tt.wantApplied && applied != 0 {
				t.Errorf("SetMemoryLimit called with %d, want no call", applied)
			}
		})
	}
}

// TestApplyFrom_NestedCgroup verifies the limit is read from the process's OWN
// cgroup, not the mount root.
//
// This is the shape systemd and any runtime without cgroup namespaces produce:
// the root reports "max" while the real cap lives several levels down. Reading
// only the root made Apply decline and silently disabled the feature exactly
// where it was needed.
func TestApplyFrom_NestedCgroup(t *testing.T) {
	t.Parallel()

	const oneGiB int64 = 1 << 30

	tests := []struct {
		name        string
		files       map[string]string
		wantLimit   int64
		wantApplied bool
	}{
		{
			name: "v2 cap on the process cgroup, unlimited root",
			files: map[string]string{
				procSelfCgroup: "0::/docker/abc123\n",
				"/sys/fs/cgroup/docker/abc123/memory.max": "1073741824",
				"/sys/fs/cgroup/docker/memory.max":        "max",
				"/sys/fs/cgroup/memory.max":               "max",
			},
			wantLimit:   oneGiB / 100 * 90,
			wantApplied: true,
		},
		{
			name: "restrictive ancestor wins over a looser own cgroup",
			files: map[string]string{
				procSelfCgroup:                           "0::/parent/child\n",
				"/sys/fs/cgroup/parent/child/memory.max": "4294967296",
				"/sys/fs/cgroup/parent/memory.max":       "1073741824",
				"/sys/fs/cgroup/memory.max":              "max",
			},
			wantLimit:   oneGiB / 100 * 90,
			wantApplied: true,
		},
		{
			name: "v1 cap on a nested memory controller",
			files: map[string]string{
				procSelfCgroup: "9:memory:/kubepods/pod99\n",
				"/sys/fs/cgroup/memory/kubepods/pod99/memory.limit_in_bytes": "1073741824",
			},
			wantLimit:   oneGiB / 100 * 90,
			wantApplied: true,
		},
		{
			name: "namespaced container reports root and still works",
			files: map[string]string{
				procSelfCgroup:              "0::/\n",
				"/sys/fs/cgroup/memory.max": "1073741824",
			},
			wantLimit:   oneGiB / 100 * 90,
			wantApplied: true,
		},
		{
			name: "v1 controller list must match memory exactly",
			files: map[string]string{
				procSelfCgroup: "5:memory+swap:/other\n",
				"/sys/fs/cgroup/memory/other/memory.limit_in_bytes": "1073741824",
			},
			wantApplied: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var applied int64
			record := func(limit int64) int64 {
				applied = limit
				return 0
			}

			got := applyFrom(noEnv, fakeFS(tt.files), record)
			//: Reading the process cgroup is what makes these cases work.
			if got.Applied() != tt.wantApplied {
				t.Fatalf("Applied() = %t, want %t", got.Applied(), tt.wantApplied)
			}
			if got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.wantLimit)
			}
			if tt.wantApplied && applied != tt.wantLimit {
				t.Errorf("SetMemoryLimit got %d, want %d", applied, tt.wantLimit)
			}
		})
	}
}

// TestApplyFrom_EmptyGoMemLimitIsNotAnOverride verifies an empty GOMEMLIMIT
// does not disable the feature. The Go runtime ignores an empty value and
// stays unbounded, so treating mere presence as an override would leave the
// process with no limit at all.
func TestApplyFrom_EmptyGoMemLimitIsNotAnOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty string", value: ""},
		{name: "whitespace only", value: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := func(string) (string, bool) { return tt.value, true }
			files := map[string]string{
				procSelfCgroup:              "0::/\n",
				"/sys/fs/cgroup/memory.max": "1073741824",
			}

			got := applyFrom(env, fakeFS(files), func(int64) int64 { return 0 })
			//: An empty value is not a decision; the cgroup limit applies.
			if !got.Applied() {
				t.Error("empty GOMEMLIMIT was treated as an override, want the cgroup limit applied")
			}
		})
	}
}

// TestApplyFrom_HybridHierarchies verifies a real v1 cap is honoured even when
// the unified hierarchy reports no limit.
//
// On a hybrid host both hierarchies are mounted and both can bind. Stopping at
// the v2 "max" would ignore a v1 memory controller that genuinely bounds the
// process, so the tightest real cap across all candidates wins.
func TestApplyFrom_HybridHierarchies(t *testing.T) {
	t.Parallel()

	const oneGiB int64 = 1 << 30

	tests := []struct {
		name        string
		files       map[string]string
		wantLimit   int64
		wantApplied bool
	}{
		{
			name: "v2 unlimited, v1 caps the process",
			files: map[string]string{
				procSelfCgroup:                                "0::/\n9:memory:/\n",
				"/sys/fs/cgroup/memory.max":                   "max",
				"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824",
			},
			wantLimit:   oneGiB / 100 * 90,
			wantApplied: true,
		},
		{
			name: "both unlimited yields no limit",
			files: map[string]string{
				procSelfCgroup:                                "0::/\n9:memory:/\n",
				"/sys/fs/cgroup/memory.max":                   "max",
				"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712",
			},
			wantApplied: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := applyFrom(noEnv, fakeFS(tt.files), func(int64) int64 { return 0 })
			//: The tightest real cap across hierarchies is the binding one.
			if got.Applied() != tt.wantApplied {
				t.Fatalf("Applied() = %t, want %t", got.Applied(), tt.wantApplied)
			}
			if got.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.wantLimit)
			}
		})
	}
}

// TestApply_DoesNotPanicOnRealHost verifies the exported entry point is safe
// against the real filesystem, where the cgroup files are usually absent.
func TestApply_DoesNotPanicOnRealHost(t *testing.T) {
	tests := []struct{ name string }{{name: "real host lookup"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//: Not parallel: Setenv mutates process state.
			t.Setenv(goMemLimitEnv, "1GiB")

			got := Apply()
			//: With GOMEMLIMIT set, Apply must defer to the operator.
			if got.Applied() || got.Limit != 0 {
				t.Errorf("Apply() = %+v, want no limit when %s is set", got, goMemLimitEnv)
			}
			//: And it must SAY the operator decided, not merely decline.
			if got.Source != coreproc.MemorySourceOperator {
				t.Errorf("Source = %v, want %v", got.Source, coreproc.MemorySourceOperator)
			}
		})
	}
}

// TestReadCgroupAllowance_PropagatesReadErrors verifies an unreadable file on
// one hierarchy does not hide a usable cap on the other, so a permission error
// never silently disables the limit.
func TestReadCgroupAllowance_PropagatesReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "v2 unreadable, v1 usable"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			readFile := func(target string) ([]byte, error) {
				//: The process belongs to both hierarchies at the root.
				if target == procSelfCgroup {
					return []byte("0::/\n9:memory:/\n"), nil
				}
				//: Model a v2 file that exists but cannot be read.
				if strings.HasSuffix(target, cgroupV2LimitFile) {
					return nil, os.ErrPermission
				}

				//: Deliver a usable v1 cap.
				return []byte("2147483648"), nil
			}

			allowance, ok := readCgroupAllowance(readFile)
			//: The readable hierarchy must still be honoured.
			if !ok || allowance != 2147483648 {
				t.Errorf("readCgroupAllowance() = (%d, %t), want (2147483648, true)", allowance, ok)
			}
		})
	}
}
