//go:build unix

package reaper

import (
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestWithOnExitReportsAnOrphansEnd is #1136's acceptance through the
// reaper: a child nobody claims, exiting 42, and one this test kills with
// SIGKILL are each reported once, unowned, with their exit in Wait's words —
// where before the sweep counted them and dropped the status. Not parallel:
// wait4(-1) collects every child of the test binary, and this test's sweeps
// must not take a parallel test's children.
func TestWithOnExitReportsAnOrphansEnd(t *testing.T) {
	attr := &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}}
	exited, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 42"}, attr)
	//: the orphan-to-be: nobody claims it.
	if err != nil {
		t.Skipf("no /bin/sh to spawn: %v", err)
	}
	killed, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "sleep 30"}, attr)
	//: a child of this test, killed by this test.
	if err != nil {
		t.Fatalf("starting the child to kill: %v", err)
	}
	//: this test's own child, never another process.
	if err := killed.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing the test's own child: %v", err)
	}
	events := make(map[int][]ExitEvent, 2)
	r, ok := New(WithOnExit(func(e ExitEvent) { events[e.PID] = append(events[e.PID], e) })).(*unixReaper)
	//: the Unix reaper.
	if !ok {
		t.Fatal("New did not return the Unix reaper")
	}
	deadline := time.Now().Add(reapDeadline)
	//: sweep until both are reported, as the loop does on each SIGCHLD.
	for (len(events[exited.Pid]) == 0 || len(events[killed.Pid]) == 0) && time.Now().Before(deadline) {
		//: SIGKILL again until it is collected: this test's own child, and a
		//: kill landing in the child's exec window can be lost (seen on NetBSD,
		//: where the process then ran its full sleep).
		if len(events[killed.Pid]) == 0 {
			_ = killed.Signal(syscall.SIGKILL)
		}
		//: a sweep's error is not this test's subject; the deadline is.
		if _, rerr := r.ReapOnce(); rerr != nil {
			t.Fatalf("ReapOnce: %v", rerr)
		}
		time.Sleep(reapPoll)
	}
	//: once each, unowned, in Wait's words.
	if got := events[exited.Pid]; len(got) != 1 || got[0].Owned || got[0].Exit.Code != 42 || got[0].Exit.Signaled {
		t.Fatalf("the exiting orphan reported %+v, want one unowned exit 42", got)
	}
	got := events[killed.Pid]
	if len(got) != 1 || got[0].Owned || !got[0].Exit.Signaled || got[0].Exit.Signal != coreproc.Signal(syscall.SIGKILL) || got[0].Exit.Code != -1 {
		t.Fatalf("the killed orphan reported %+v, want one unowned SIGKILL", got)
	}
}

// TestWithoutOnExitNothingIsBound pins the no-observer path: childwait is
// handed nil, so a sweep costs exactly what it cost before the option.
func TestWithoutOnExitNothingIsBound(t *testing.T) {
	t.Parallel()
	//: no observer, no bound method.
	if r, ok := New().(*unixReaper); !ok || r.observe != nil {
		t.Fatal("a reaper without WithOnExit binds an observer")
	}
	//: an observer, one bound method for the reaper's life.
	if r, ok := New(WithOnExit(func(ExitEvent) {})).(*unixReaper); !ok || r.observe == nil {
		t.Fatal("a reaper with WithOnExit binds no observer")
	}
}
