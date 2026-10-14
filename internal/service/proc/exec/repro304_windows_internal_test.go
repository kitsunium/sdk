//go:build windows

// Temporary probe for #304 — removed before the pull request.
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

var probeOpenProcess = modKernel32.NewProc("OpenProcess")

func probeSpec() coreproc.Spec {
	return coreproc.Spec{
		Path: os.Getenv("ComSpec"),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceAS: {Soft: 512 << 20, Hard: 512 << 20},
		},
	}
}

func errnoOf(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("errno=%d (%v)", uint32(errno), errno)
	}
	return fmt.Sprintf("%v", err)
}

// oldAssign is main's assignment: OpenProcess by pid, then assign, after the
// child already runs.
func oldAssign(j *jobLimit, pid int) (string, error) {
	hp, _, errno := probeOpenProcess.Call(0x0100|0x0001, 0, uintptr(pid))
	if hp == 0 {
		return "OpenProcess", errno
	}
	defer procCloseHandle.Call(hp)
	r1, _, aerrno := procAssignProcessToJobObject.Call(uintptr(j.handle), hp)
	if r1 == 0 {
		return "AssignProcessToJobObject", aerrno
	}
	return "", nil
}

// TestRepro304AssignAfterExit: assigning a child that has already exited.
func TestRepro304AssignAfterExit(t *testing.T) {
	for i := 0; i < 10; i++ {
		job, err := newJobLimit(probeSpec())
		if err != nil {
			t.Logf("PROBE304 newJobLimit: %v", err)
			return
		}
		p, err := os.StartProcess(os.Getenv("ComSpec"), []string{"cmd", "/c", "exit 0"}, &os.ProcAttr{})
		if err != nil {
			t.Logf("PROBE304 StartProcess: %v", err)
			return
		}
		_ = p.WithHandle(func(h uintptr) {
			_, _ = syscall.WaitForSingleObject(syscall.Handle(h), syscall.INFINITE)
		})
		step, oErr := oldAssign(job, p.Pid)
		nErr := job.assign(p)
		t.Logf("PROBE304 after exit #%d: old path %s -> %s; handle path -> %s", i, step, errnoOf(oErr), errnoOf(nErr))
		_, _ = p.Wait()
		job.close()
	}
}

func loop(n int, one func() (string, error)) (int, map[string]int) {
	var mu sync.Mutex
	fails := map[string]int{}
	total := 0
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			step, err := one()
			if err != nil {
				mu.Lock()
				total++
				fails[step+" "+errnoOf(err)]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return total, fails
}

// TestRepro304AB: main's spawn-then-assign against the suspended spawn, same host.
func TestRepro304AB(t *testing.T) {
	const n = 3000
	stock, sf := loop(n, func() (string, error) {
		job, err := newJobLimit(probeSpec())
		if err != nil {
			return "newJobLimit", err
		}
		defer job.close()
		p, err := os.StartProcess(os.Getenv("ComSpec"), []string{"cmd", "/c", "exit 0"}, &os.ProcAttr{})
		if err != nil {
			return "StartProcess", err
		}
		step, aErr := oldAssign(job, p.Pid)
		_, _ = p.Wait()
		return step, aErr
	})
	fixed, ff := loop(n, func() (string, error) {
		p, err := Start(context.Background(), probeSpec())
		if err != nil {
			return "Start", err
		}
		exit, werr := p.Wait()
		if werr != nil {
			return "Wait", werr
		}
		if exit.Code != 0 {
			return "exit", fmt.Errorf("code %d", exit.Code)
		}
		return "", nil
	})
	t.Logf("PROBE304 A/B: main's path %d/%d failed %v; suspended path %d/%d failed %v", stock, n, sf, fixed, n, ff)
}
