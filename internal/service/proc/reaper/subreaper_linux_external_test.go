//go:build linux

// Package reaper_test — arming subreaper mode on Linux.
package reaper_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcreaper "github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// TestSetChildSubreaper pins the arming contract.
//
// Subreaper mode is what lets a supervisor that is NOT pid 1 receive its
// orphaned descendants: without it they reparent to init and their SIGCHLD
// never arrives, so the supervisor's reaper sits idle while zombies accumulate
// somewhere it cannot see. prctl(PR_SET_CHILD_SUBREAPER) is idempotent, so
// arming it repeatedly must stay clean — a supervisor that re-arms on every
// config reload is doing the right thing.
func TestSetChildSubreaper(t *testing.T) {
	//: not parallel — prctl(PR_SET_CHILD_SUBREAPER) alters a process-wide
	//: attribute, and every later spawn in this binary inherits the effect.
	type tc struct {
		name  string
		calls int
	}
	tests := []tc{
		{"arming once", 1},
		{"arming repeatedly", 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for i := range c.calls {
			err := svcreaper.SetChildSubreaper()
			//: nil means armed. A failure must be the typed sentinel and never
			//: a bare errno, or a supervisor cannot tell "this kernel has no
			//: subreaper" from "the call was refused".
			if err == nil {
				continue
			}
			if !errs.HasCode(err, coreproc.CodeSubreaperFailed) &&
				!errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("call %d = %v, want SUBREAPER_FAILED or UNSUPPORTED_PLATFORM", i, err)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
