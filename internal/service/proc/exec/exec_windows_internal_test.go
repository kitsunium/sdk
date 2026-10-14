//go:build windows

// Package exec — the order a confined Windows child is spawned in (#304).
package exec

import (
	"errors"
	"os"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// waitTimeout is WAIT_TIMEOUT (winerror.h): the object was not signalled.
const waitTimeout uint32 = 0x00000102

// confinedExitSpec is a child that exits at once, under a mappable rlimit.
func confinedExitSpec(t *testing.T) coreproc.Spec {
	t.Helper()
	return coreproc.Spec{
		Path: comspecPath(t),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			//: a generous 512 MiB address-space cap the child stays well under.
			coreproc.ResourceAS: {Soft: 512 << 20, Hard: 512 << 20},
		},
	}
}

// comspecPath returns cmd.exe's path, or skips where it is missing.
func comspecPath(t *testing.T) string {
	t.Helper()
	cs := os.Getenv("ComSpec")
	//: ComSpec is set on every Windows host; fall back to the canonical path.
	if cs == "" {
		cs = `C:\Windows\System32\cmd.exe`
	}
	//: a host without cmd.exe cannot run the spawn checks.
	if _, err := os.Stat(cs); err != nil {
		t.Skipf("cmd.exe not found: %v", err)
	}
	return cs
}

// exited reports whether p has exited within ms milliseconds.
func exited(t *testing.T, p *os.Process, ms uint32) bool {
	t.Helper()
	var event uint32
	var werr error
	hErr := p.WithHandle(func(h uintptr) {
		event, werr = syscall.WaitForSingleObject(syscall.Handle(h), ms)
	})
	if hErr != nil || werr != nil {
		t.Fatalf("WaitForSingleObject: %v %v", hErr, werr)
	}
	return event != waitTimeout
}

// Test_confineRunsTheChildOnlyOnceAssigned pins the order a confined child is
// spawned in: created suspended, it runs no instruction — so cannot exit —
// before confine assigns it to its job and resumes it, and then runs to its
// own exit.
//
// Before #304 the child ran from CreateProcess on and was assigned afterwards:
// a `cmd /c exit 0` that was gone by then made AssignProcessToJobObject fail
// with ERROR_ACCESS_DENIED, and Start returned RLIMIT_FAILED for a spawn that
// had done nothing wrong (TestWindowsRlimitMemoryMapped, run 38497960840).
func Test_confineRunsTheChildOnlyOnceAssigned(t *testing.T) {
	t.Parallel()
	spec := confinedExitSpec(t)
	job, err := newJobLimit(spec)
	if err != nil {
		t.Fatalf("newJobLimit: %v", err)
	}
	sio, err := buildStdio(spec)
	if err != nil {
		job.close()
		t.Fatalf("buildStdio: %v", err)
	}
	p, err := spawnWindows(spec, sio, true)
	if err != nil {
		sio.closeAll()
		job.close()
		t.Fatalf("spawnWindows: %v", err)
	}

	//: suspended, the child cannot have exited, however long it is given.
	if exited(t, p, 500) {
		t.Fatal("the child ran before it was confined")
	}
	if cErr := confine(job, p); cErr != nil {
		sio.closeAll()
		t.Fatalf("confine: %v", cErr)
	}
	sio.afterStart()
	h := newHandle(p, spec.Setpgid, sio, job)
	exit, werr := h.Wait()

	if werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	//: resumed, the confined child runs to its own exit.
	if exit.Code != 0 {
		t.Fatalf("exit Code = %d, want 0", exit.Code)
	}
}

// Test_anExitedChildIsNotAssigned pins the Windows behaviour the order above
// answers: a process that has already exited cannot be assigned to a job, so a
// child must never be able to exit before it is confined.
func Test_anExitedChildIsNotAssigned(t *testing.T) {
	t.Parallel()
	spec := confinedExitSpec(t)
	job, err := newJobLimit(spec)
	if err != nil {
		t.Fatalf("newJobLimit: %v", err)
	}
	defer job.close()
	p, err := os.StartProcess(spec.Path, spec.Args, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	defer func() { _, _ = p.Wait() }()
	//: the child is gone, its handle still held.
	if !exited(t, p, syscall.INFINITE) {
		t.Fatal("the child never exited")
	}

	aErr := job.assign(p)

	if !errors.Is(aErr, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("assign(exited child) = %v, want ERROR_ACCESS_DENIED", aErr)
	}
}
