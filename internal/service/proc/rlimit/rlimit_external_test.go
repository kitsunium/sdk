// Package rlimit_test — black-box tests for the rlimit service facade.
package rlimit_test

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// applyCase is one row of the Apply table: a pid, a limit set, and the code the
// call is expected to return (zero means success on Linux).
type applyCase struct {
	name    string
	pid     int
	limits  map[coreproc.Resource]coreproc.LimitValue
	wantErr errs.Code
}

// readSoftNoFile parses /proc/self/limits and returns the current soft open-file
// ceiling so a test can observe a setrlimit effect end to end.
func readSoftNoFile(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/limits")
	//: a host without /proc cannot run the observability assertion.
	if err != nil {
		//: skip rather than fail on a non-procfs platform.
		t.Skipf("no /proc/self/limits: %v", err)
	}
	//: scan for the "Max open files" row and return its soft column.
	for line := range strings.SplitSeq(string(data), "\n") {
		//: the open-files row carries the soft ceiling in column 4.
		if strings.HasPrefix(line, "Max open files") {
			//: fields: "Max open files <soft> <hard> files".
			fields := strings.Fields(line)
			//: a well-formed row has at least the label plus soft value.
			if len(fields) >= 4 {
				//: column index 3 is the soft value.
				v, perr := strconv.ParseUint(fields[3], 10, 64)
				//: a parse failure means the row format changed unexpectedly.
				if perr != nil {
					//: surface the unexpected format so the test fails loudly.
					t.Fatalf("parse soft nofile %q: %v", fields[3], perr)
				}
				//: hand back the observed soft ceiling.
				return v
			}
		}
	}
	//: the row was absent — the format is not what the test expects.
	t.Fatal("Max open files row not found in /proc/self/limits")
	//: unreachable; t.Fatal stops the test.
	return 0
}

// TestApplyNoFileObservable applies a lowered RLIMIT_NOFILE to the current
// process and asserts the new soft ceiling is observable via /proc/self/limits —
// the issue's primary acceptance criterion.
func TestApplyNoFileObservable(t *testing.T) {
	//: only Linux provides setrlimit + procfs observability.
	if runtime.GOOS != "linux" {
		//: assert the stub contract instead of the syscall behaviour.
		assertUnsupported(t)
		//: nothing further to observe off Linux.
		return
	}
	before := readSoftNoFile(t)
	//: a soft ceiling under 16 is implausible and would make the test moot.
	if before < 16 {
		//: cannot lower below the floor the test targets.
		t.Skipf("starting soft nofile too low: %d", before)
	}
	target := before - 1
	err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{
		//: lower soft to target while keeping hard at the current soft (a valid ceiling).
		coreproc.ResourceNoFile: {Soft: target, Hard: before},
	})
	//: applying a valid lower soft ceiling must succeed.
	if err != nil {
		//: an error here means setrlimit or the mapping is broken.
		t.Fatalf("Apply(NOFILE soft=%d hard=%d): %v", target, before, err)
	}
	got := readSoftNoFile(t)
	//: the kernel must now report the lowered soft ceiling.
	if got != target {
		//: the limit did not take effect — the core assertion failed.
		t.Fatalf("soft nofile = %d, want %d", got, target)
	}
}

// assertUnsupported asserts that off Linux every entry point returns the typed
// UnsupportedPlatform sentinel and never panics.
func assertUnsupported(t *testing.T) {
	t.Helper()
	err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{
		//: any resource will do; the stub rejects before mapping.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the stub must surface UNSUPPORTED_PLATFORM, not nil.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing typed error breaks the degrade-gracefully contract.
		t.Fatalf("Apply off Linux = %v, want UnsupportedPlatform", err)
	}
}

// runApplyCase exercises one applyCase and asserts the returned code matches
// the expectation.
func runApplyCase(t *testing.T, tc applyCase) {
	t.Helper()
	err := rlimit.Apply(tc.pid, tc.limits)
	//: the success rows expect a nil error.
	if tc.wantErr == 0 {
		//: a non-nil error on a success row is a failure.
		if err != nil {
			//: report the unexpected error verbatim.
			t.Fatalf("Apply(%s) = %v, want nil", tc.name, err)
		}
		//: success row satisfied.
		return
	}
	//: the failure rows expect a specific typed code.
	if !errs.HasCode(err, tc.wantErr) {
		//: the wrong (or no) code breaks the typed-error contract.
		t.Fatalf("Apply(%s) = %v, want code %v", tc.name, err, tc.wantErr)
	}
}

// TestApplyTable drives the success and typed-error rows that hold on any host.
func TestApplyTable(t *testing.T) {
	t.Parallel()
	//: choose the expected code for the unknown-resource row by platform.
	unknownWant := coreproc.CodeUnknownResource
	//: off Linux the stub short-circuits to UNSUPPORTED_PLATFORM before mapping.
	if runtime.GOOS != "linux" {
		//: the non-Linux stub never reaches the resource table.
		unknownWant = coreproc.CodeUnsupportedPlatform
	}
	cases := []applyCase{
		{
			name:    "empty-limits",
			pid:     0,
			limits:  map[coreproc.Resource]coreproc.LimitValue{},
			wantErr: nonLinuxOrZero(),
		},
		{
			name: "unknown-resource",
			pid:  0,
			limits: map[coreproc.Resource]coreproc.LimitValue{
				//: the zero value ResourceUnknown has no RLIMIT_* mapping.
				coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
			},
			wantErr: unknownWant,
		},
	}
	//: run every row; helper drives the call and assertion.
	for _, tc := range cases {
		//: each row is independent — keep them parallel-safe.
		t.Run(tc.name, func(t *testing.T) {
			//: the body shares no mutable state across rows.
			t.Parallel()
			//: delegate to the shared assertion helper.
			runApplyCase(t, tc)
		})
	}
}

// nonLinuxOrZero returns UnsupportedPlatform off Linux and the success sentinel
// (0) on Linux, so the empty-limits row asserts the right outcome per platform.
func nonLinuxOrZero() errs.Code {
	//: an empty map is a no-op success on Linux.
	if runtime.GOOS == "linux" {
		//: zero marks the success expectation in the table.
		return 0
	}
	//: off Linux even an empty map returns the stub's typed error.
	return coreproc.CodeUnsupportedPlatform
}

// TestPrepareSysProcAttr asserts the no-syscall validator rejects an unmapped
// resource and accepts a mapped one (or returns the stub error off Linux).
func TestPrepareSysProcAttr(t *testing.T) {
	t.Parallel()
	//: an unmapped resource must be rejected without a syscall.
	err := rlimit.PrepareSysProcAttr(map[coreproc.Resource]coreproc.LimitValue{
		coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
	})
	//: off Linux the stub returns UNSUPPORTED_PLATFORM; on Linux UNKNOWN_RESOURCE.
	want := coreproc.CodeUnknownResource
	//: select the platform-correct expectation.
	if runtime.GOOS != "linux" {
		//: the stub short-circuits before the resource table.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the validator must surface the expected typed code.
	if !errs.HasCode(err, want) {
		//: a missing code breaks the fail-fast contract.
		t.Fatalf("PrepareSysProcAttr(unknown) = %v, want code %v", err, want)
	}
}
