//go:build windows

// Temporary probe for #304 — removed before the pull request.
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

func probeSpec() coreproc.Spec {
	return coreproc.Spec{
		Path: os.Getenv("ComSpec"),
		Args: []string{"cmd", "/c", "exit 0"},
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceAS: {Soft: 512 << 20, Hard: 512 << 20},
		},
	}
}

// TestRepro304AssignAfterExit: assigning a child that has already exited.
func TestRepro304AssignAfterExit(t *testing.T) {
	for i := 0; i < 20; i++ {
		job, err := newJobLimit(probeSpec())
		if err != nil {
			t.Fatalf("newJobLimit: %v", err)
		}
		p, err := os.StartProcess(os.Getenv("ComSpec"), []string{"cmd", "/c", "exit 0"}, &os.ProcAttr{})
		if err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
		_ = p.WithHandle(func(h uintptr) {
			_, _ = syscall.WaitForSingleObject(syscall.Handle(h), syscall.INFINITE)
		})
		aErr := job.assign(p.Pid)
		var errno syscall.Errno
		errors.As(aErr, &errno)
		t.Logf("PROBE304 assign-after-exit #%d: err=%v errno=%d (%v)", i, aErr, uint32(errno), errno)
		_, _ = p.Wait()
		job.close()
	}
}

// TestRepro304StartLoop: Start with a mappable rlimit, many times, in parallel.
func TestRepro304StartLoop(t *testing.T) {
	const n = 2000
	type res struct {
		err error
	}
	out := make(chan res, n)
	sem := make(chan struct{}, 8)
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			p, err := Start(context.Background(), probeSpec())
			if err == nil {
				_, _ = p.Wait()
			}
			out <- res{err: err}
		}()
	}
	fails := map[string]int{}
	total := 0
	for i := 0; i < n; i++ {
		r := <-out
		if r.err != nil {
			total++
			var errno syscall.Errno
			errors.As(r.err, &errno)
			fails[fmt.Sprintf("errno=%d (%v)", uint32(errno), errno)]++
		}
	}
	t.Logf("PROBE304 start-loop: %d/%d failed: %v", total, n, fails)
}
