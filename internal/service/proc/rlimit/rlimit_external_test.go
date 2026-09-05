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

// foreignPid is a pid chosen to be absent on any realistic host so the
// foreign-pid prlimit64 path fails deterministically (ESRCH/EPERM) and lets the
// test assert the failing-syscall annotation.
const foreignPid int = 0x7fff_fffe

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
// the issue's primary acceptance criterion. The /proc readback is Linux-specific;
// the portable getrlimit(2) readback in rlimit_unix_test.go proves the same
// native effect on every other Unix target.
func TestApplyNoFileObservable(t *testing.T) {
	//: /proc/self/limits exists only on Linux; other Unix targets prove the
	//: setrlimit effect via getrlimit(2) in rlimit_unix_test.go.
	if runtime.GOOS != "linux" {
		//: nothing to observe through procfs off Linux.
		t.Skip("/proc/self/limits observability is Linux-only")
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
	//: choose the expected code for the unknown-resource row by platform.
	unknownWant := coreproc.CodeUnknownResource
	//: a non-Unix target degrades to UNSUPPORTED_PLATFORM before the table.
	if !nativeRlimit {
		//: the non-Unix stub never reaches the resource table.
		unknownWant = coreproc.CodeUnsupportedPlatform
	}
	cases := []applyCase{
		{
			name:    "empty-limits",
			pid:     0,
			limits:  map[coreproc.Resource]coreproc.LimitValue{},
			wantErr: emptyLimitsWant(),
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

// emptyLimitsWant returns the success sentinel (0) where rlimits apply natively
// (every Unix target) and UnsupportedPlatform on a non-Unix target, so the
// empty-limits row asserts the right outcome per platform.
func emptyLimitsWant() errs.Code {
	//: an empty map is a no-op success wherever rlimit acts natively.
	if nativeRlimit {
		//: zero marks the success expectation in the table.
		return 0
	}
	//: off Unix even an empty map returns the stub's typed error.
	return coreproc.CodeUnsupportedPlatform
}

// fieldValue walks err's attached fields and returns the StringValue of the
// first one keyed key, or "" when absent.
func fieldValue(err error, key string) (val string) {
	//: scan every field merged along the wrap chain.
	for _, f := range errs.FieldsOf(err) {
		//: return the first match for the requested key.
		if f.Key() == key {
			//: hand back the recorded string annotation.
			return f.StringValue()
		}
	}
	//: no field carried the requested key.
	return ""
}

// TestForeignPidNamesPrlimit64 asserts that a failure on the foreign-pid path
// surfaces RLIMIT_FAILED annotated with syscall=prlimit64, so logs reflect the
// actual failing operation rather than always reading "setrlimit".
func TestForeignPidNamesPrlimit64(t *testing.T) {
	t.Parallel()
	//: the prlimit64 path and its annotation exist only on Linux.
	if runtime.GOOS != "linux" {
		//: nothing to assert about the syscall field off Linux.
		t.Skip("prlimit64 path is Linux-only")
	}
	err := rlimit.Apply(foreignPid, map[coreproc.Resource]coreproc.LimitValue{
		//: any mapped resource reaches the foreign-pid syscall before failing.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: an absent foreign pid must fail rather than silently succeed.
	if err == nil {
		//: a nil result means the foreign-pid path did not run as expected.
		t.Fatalf("Apply(foreign absent pid) = nil, want RLIMIT_FAILED")
	}
	//: the failure must carry the central RLIMIT_FAILED code.
	if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		//: a different code means the foreign-pid path did not classify correctly.
		t.Fatalf("Apply(foreign) = %v, want code RLIMIT_FAILED", err)
	}
	//: the recorded syscall must name the foreign-pid entry point.
	if got := fieldValue(err, "syscall"); got != "prlimit64" {
		//: a missing or "setrlimit" value is the misleading-message bug.
		t.Fatalf("syscall field = %q, want %q", got, "prlimit64")
	}
}

// TestPrepareSysProcAttr asserts the no-syscall validator rejects an unmapped
// resource and accepts a mapped one (or returns the stub error off Linux).
func TestPrepareSysProcAttr(t *testing.T) {
	t.Parallel()
	//: an unmapped resource must be rejected without a syscall.
	err := rlimit.PrepareSysProcAttr(map[coreproc.Resource]coreproc.LimitValue{
		coreproc.ResourceUnknown: {Soft: 1, Hard: 1},
	})
	//: on a non-Unix target the stub returns UNSUPPORTED_PLATFORM; natively the
	//: unmapped resource surfaces UNKNOWN_RESOURCE.
	want := coreproc.CodeUnknownResource
	//: select the platform-correct expectation.
	if !nativeRlimit {
		//: the stub short-circuits before the resource table.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the validator must surface the expected typed code.
	if !errs.HasCode(err, want) {
		//: a missing code breaks the fail-fast contract.
		t.Fatalf("PrepareSysProcAttr(unknown) = %v, want code %v", err, want)
	}
}
