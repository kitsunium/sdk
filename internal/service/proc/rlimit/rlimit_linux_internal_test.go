//go:build linux

// Package rlimit — white-box tests for the Linux implementation. The two kernel
// entry points behave differently enough that a caller has to be able to tell
// them apart from the error alone: setrlimit(2) for the calling process needs no
// privilege, prlimit64(2) for anyone else needs CAP_SYS_RESOURCE.
package rlimit

import (
	"maps"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// foreignPid is a pid chosen to be absent on any realistic host, so the
// prlimit64 path fails deterministically (ESRCH) and the syscall annotation can
// be asserted without depending on privileges.
const foreignPid int = 0x7fff_fffe

// fieldValue returns the StringValue of the first field keyed key, or "".
func fieldValue(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		//: the first match wins; fields are merged along the wrap chain.
		if f.Key() == key {
			return f.StringValue()
		}
	}
	//: no field carried the requested key.
	return ""
}

// currentNoFile reads the calling process's RLIMIT_NOFILE pair.
func currentNoFile(t *testing.T) syscall.Rlimit {
	t.Helper()
	var lim syscall.Rlimit
	//: a host that cannot read its own rlimit is broken, not unsupported.
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatalf("getrlimit(NOFILE): %v", err)
	}
	return lim
}

// Test_resolve pins the resource table. An unmapped resource must come back as
// the typed sentinel rather than as RLIMIT_0, which on Linux is RLIMIT_CPU — a
// ceiling that would silently kill the process it was applied to.
func Test_resolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      coreproc.Resource
		wantErr bool
	}
	tests := []tc{
		{name: "open files", in: coreproc.ResourceNoFile},
		{name: "processes", in: coreproc.ResourceNProc},
		{name: "core size", in: coreproc.ResourceCore},
		{name: "address space", in: coreproc.ResourceAS},
		{name: "cpu time", in: coreproc.ResourceCPU},
		{name: "file size", in: coreproc.ResourceFSize},
		{name: "data segment", in: coreproc.ResourceData},
		{name: "stack size", in: coreproc.ResourceStack},
		{name: "locked memory", in: coreproc.ResourceMemLock},
		{name: "the zero value", in: coreproc.ResourceUnknown, wantErr: true},
		{name: "a resource beyond the enum", in: coreproc.Resource(200), wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := resolve(c.in)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownResource) {
				t.Fatalf("resolve(%v) = %v, want UNKNOWN_RESOURCE", c.in, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolve(%v) = %v, want nil", c.in, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: every mapped resource must resolve to a DISTINCT constant, or two
	//: resources would silently share one kernel ceiling. The table is cloned
	//: first so the walk cannot observe a concurrent edit.
	table := maps.Clone(resourceToRLIMIT)
	seen := make(map[int]coreproc.Resource, len(table))
	for r := range table {
		rl, err := resolve(r)
		if err != nil {
			t.Errorf("resolve(%v) = %v for a table entry", r, err)
			continue
		}
		if prior, clash := seen[rl]; clash {
			t.Errorf("%v and %v both map to RLIMIT constant %d", prior, r, rl)
		}
		seen[rl] = r
	}
}

// Test_prepareLimits pins the validator: it must refuse on the FIRST unmapped
// resource and issue no syscall at all, since its whole purpose is to run before
// a process exists.
func Test_prepareLimits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		limits  map[coreproc.Resource]coreproc.LimitValue
		wantErr bool
	}
	tests := []tc{
		{name: "a nil set"},
		{name: "an empty set", limits: map[coreproc.Resource]coreproc.LimitValue{}},
		{
			name:   "every mapped resource",
			limits: everyMappedResource(),
		},
		{
			name:    "the zero-value resource alone",
			limits:  map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceUnknown: {}},
			wantErr: true,
		},
		{
			name: "one unmapped resource among mapped ones",
			limits: map[coreproc.Resource]coreproc.LimitValue{
				coreproc.ResourceNoFile:  {Soft: 1, Hard: 1},
				coreproc.ResourceUnknown: {},
			},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: snapshot the real ceiling so a validator that wrongly applied
		//: something would be caught.
		before := currentNoFile(t)

		err := prepareLimits(c.limits)

		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownResource) {
				t.Fatalf("prepareLimits(%s) = %v, want UNKNOWN_RESOURCE", c.name, err)
			}
		} else if err != nil {
			t.Fatalf("prepareLimits(%s) = %v, want nil", c.name, err)
		}

		after := currentNoFile(t)
		//: validation must never touch the kernel.
		if after != before {
			t.Errorf("prepareLimits changed RLIMIT_NOFILE from %+v to %+v", before, after)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// everyMappedResource builds a limit set naming every resource in the table, so
// a resource added to the map cannot slip past the validator test.
func everyMappedResource() map[coreproc.Resource]coreproc.LimitValue {
	out := make(map[coreproc.Resource]coreproc.LimitValue, len(resourceToRLIMIT))
	for r := range resourceToRLIMIT {
		out[r] = coreproc.LimitValue{Soft: 1, Hard: 1}
	}
	return out
}

// Test_applyLimits pins the observable effect and the routing.
//
// It is deliberately NOT parallel: lowering RLIMIT_NOFILE is a process-wide
// change, so a concurrent test opening files would see a ceiling it never asked
// for. The original value is restored on every exit path.
func Test_applyLimits(t *testing.T) {
	before := currentNoFile(t)
	t.Cleanup(func() {
		//: restore the process-global ceiling so a later test in the same
		//: binary does not inherit the lowered NOFILE.
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
			t.Logf("restoring RLIMIT_NOFILE: %v", err)
		}
	})
	//: a process running a Go test suite always has room to lower by one.
	if before.Cur < 2 {
		t.Fatalf("the starting soft NOFILE is %d — this host cannot run the suite", before.Cur)
	}

	type tc struct {
		name string
		//: how far below the current soft ceiling to aim.
		lower uint64
	}
	tests := []tc{
		{"one below the current ceiling", 1},
		{"two below the current ceiling", 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		target := before.Cur - c.lower

		err := applyLimits(0, map[coreproc.Resource]coreproc.LimitValue{
			//: lower soft to target, keeping hard where it was.
			coreproc.ResourceNoFile: {Soft: target, Hard: before.Max},
		})
		if err != nil {
			t.Fatalf("applyLimits(soft=%d hard=%d) = %v, want nil", target, before.Max, err)
		}

		//: the kernel must report the lowered ceiling — the proof the mapping
		//: and the syscall both did what they claimed.
		got := currentNoFile(t)
		if got.Cur != target {
			t.Errorf("soft NOFILE = %d, want %d", got.Cur, target)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_applyOne pins the routing decision and the annotation that follows from
// it. A caller reading a failure needs to know which syscall failed: setrlimit
// failing is a bad ceiling, prlimit64 failing is usually a missing capability or
// a process that already exited — different problems with different fixes.
func Test_applyOne(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		pid         int
		resource    coreproc.Resource
		wantErr     bool
		wantCode    errs.Code
		wantSyscall string
	}
	tests := []tc{
		{
			name:     "an unmapped resource never reaches a syscall",
			pid:      0,
			resource: coreproc.ResourceUnknown,
			wantErr:  true,
			wantCode: coreproc.CodeUnknownResource,
		},
		{
			//: an absent pid takes the foreign path and fails with ESRCH, which
			//: is what makes the annotation observable without privileges.
			name:        "a foreign pid names prlimit64",
			pid:         foreignPid,
			resource:    coreproc.ResourceNoFile,
			wantErr:     true,
			wantCode:    coreproc.CodeRlimitFailed,
			wantSyscall: syscallPrlimit64,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := applyOne(c.pid, c.resource, coreproc.LimitValue{Soft: 1024, Hard: 1024})
		if !c.wantErr {
			if err != nil {
				t.Fatalf("applyOne(%s) = %v, want nil", c.name, err)
			}
			return
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("applyOne(%s) = %v, want code %v", c.name, err, c.wantCode)
		}
		if c.wantSyscall == "" {
			return
		}
		//: a missing or wrong syscall field is the misleading-message bug this
		//: annotation exists to prevent.
		if got := fieldValue(err, "syscall"); got != c.wantSyscall {
			t.Errorf("the syscall field is %q, want %q", got, c.wantSyscall)
		}
		//: the target pid must ride along too, or a supervisor cannot say
		//: WHICH child it failed to constrain.
		if len(errs.FieldsOf(err)) < 3 {
			t.Errorf("fields = %v, want pid, resource and syscall", errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rlimitFailed pins the wrapper every failure path shares: one code, one
// exit status, and three annotations naming what was attempted on whom.
func Test_rlimitFailed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		pid         int
		resource    string
		syscallName string
	}
	tests := []tc{
		{"a self apply", 0, "nofile", syscallSetrlimit},
		{"a foreign apply", 4242, "nproc", syscallPrlimit64},
		{"an unnamed resource", 0, "", syscallSetrlimit},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := rlimitFailed(syscall.EPERM, c.pid, c.resource, c.syscallName)

		if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
			t.Fatalf("rlimitFailed = %v, want RLIMIT_FAILED", err)
		}
		//: EX_OSERR tells a supervisor the fault was the operating system's,
		//: not the operator's configuration.
		if got := errs.ExitCodeOf(err); got != exitOSErr {
			t.Errorf("exit code = %d, want %d", got, exitOSErr)
		}
		if got := fieldValue(err, "syscall"); got != c.syscallName {
			t.Errorf("the syscall field is %q, want %q", got, c.syscallName)
		}
		if got := fieldValue(err, "resource"); got != c.resource {
			t.Errorf("the resource field is %q, want %q", got, c.resource)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applySelf pins the calling-process path: it must succeed for a ceiling
// the kernel accepts and report a typed failure for one it does not.
//
// Not parallel: it changes a process-wide ceiling.
func Test_applySelf(t *testing.T) {
	before := currentNoFile(t)
	t.Cleanup(func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &before); err != nil {
			t.Logf("restoring RLIMIT_NOFILE: %v", err)
		}
	})

	type tc struct {
		name    string
		rlim    syscall.Rlimit
		wantErr bool
	}
	tests := []tc{
		{name: "the current ceiling", rlim: before},
		{name: "a lowered soft ceiling", rlim: syscall.Rlimit{Cur: before.Cur - 1, Max: before.Max}},
		{
			//: a soft ceiling above the hard one is refused by the kernel, and
			//: the refusal must arrive typed rather than as a bare errno.
			name:    "a soft ceiling above the hard one",
			rlim:    syscall.Rlimit{Cur: before.Max + 1, Max: before.Max},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rlim := new(syscall.Rlimit)
		*rlim = c.rlim
		err := applySelf(0, "nofile", syscall.RLIMIT_NOFILE, rlim)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
				t.Fatalf("applySelf(%s) = %v, want RLIMIT_FAILED", c.name, err)
			}
			if got := fieldValue(err, "syscall"); got != syscallSetrlimit {
				t.Errorf("the syscall field is %q, want %q", got, syscallSetrlimit)
			}
			return
		}
		if err != nil {
			t.Fatalf("applySelf(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// Test_applyForeign pins the other-process path. It is asserted against a pid
// that cannot exist, so the failure is deterministic on any host and needs no
// privilege — what is being pinned is the classification, not the kernel's
// permission model.
func Test_applyForeign(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		pid  int
	}
	tests := []tc{
		{"a pid that cannot exist", foreignPid},
		{"another pid that cannot exist", foreignPid - 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rlim := new(syscall.Rlimit)
		rlim.Cur, rlim.Max = 1024, 1024

		err := applyForeign(c.pid, "nofile", syscall.RLIMIT_NOFILE, rlim)

		if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
			t.Fatalf("applyForeign(%d) = %v, want RLIMIT_FAILED", c.pid, err)
		}
		//: the annotation must name prlimit64, or a log would claim setrlimit
		//: failed on a process this one never touched.
		if got := fieldValue(err, "syscall"); got != syscallPrlimit64 {
			t.Errorf("the syscall field is %q, want %q", got, syscallPrlimit64)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
