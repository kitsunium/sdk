package service

import (
	"context"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// endedProc is a process whose Wait returns err at once.
type endedProc struct{ err error }

func (p endedProc) PID() int                                                   { return 4242 }
func (p endedProc) Wait() (coreproc.ExitValue, error)                          { return coreproc.ExitValue{Code: -1}, p.err }
func (p endedProc) Signal(coreproc.Signal) error                               { return nil }
func (p endedProc) SignalGroup(coreproc.Signal) error                          { return nil }
func (p endedProc) Stop(context.Context, time.Duration, coreproc.Signal) error { return nil }

// TestWaitEventStatusUnknown pins #729 in the engine: the end of a process
// proc/exec attached — STATUS_UNKNOWN — is read as the port's EXIT_UNKNOWN,
// an end without a status, never as a failed wait.
func TestWaitEventStatusUnknown(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		err  error
	}{
		{"proc/exec's STATUS_UNKNOWN", coreproc.StatusUnknown},
		{"the port's EXIT_UNKNOWN", coresvc.ExitUnknown},
	} {
		ev := waitEvent(endedProc{err: c.err})
		if !ev.has(flagUnknown) {
			t.Errorf("%s: the event carries no flagUnknown", c.name)
		}
		if ev.kind != evExit || ev.pid != 4242 {
			t.Errorf("%s: event %+v, want the exit of 4242", c.name, ev)
		}
	}
}

// TestNewAttacherCopiesScope pins that the scope NewAttacher keeps is its own.
func TestNewAttacherCopiesScope(t *testing.T) {
	t.Parallel()
	uids := []int{1000}
	a, ok := NewAttacher(coreproc.AttachValue{UIDs: uids}).(execAttacher)
	if !ok {
		t.Fatalf("NewAttacher returned %T", a)
	}
	uids[0] = 0
	if a.scope.UIDs[0] != 1000 {
		t.Errorf("the attacher's scope changed with the caller's slice: %v", a.scope.UIDs)
	}
}
