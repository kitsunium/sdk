//go:build unix

// Package reaper_test — the Unix reaper as a supervisor uses it.
package reaper_test

import (
	"os"
	"testing"

	svcreaper "github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// TestNew pins that the reaper comes back IDLE. Starting the SIGCHLD loop from
// the constructor would install a process-global handler in any program that
// merely built one — including a program that decided, after inspecting it, not
// to use it.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		opts []svcreaper.Option
	}
	tests := []tc{
		{"no options", nil},
		{"an observer", []svcreaper.Option{svcreaper.WithOnReap(func(int) {})}},
		{"a nil option", []svcreaper.Option{nil}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcreaper.New(c.opts...)
		if r == nil {
			t.Fatal("New returned no reaper")
		}
		//: an idle reaper still answers a sweep, which is what makes ReapOnce
		//: usable without ever calling Start.
		if _, err := r.ReapOnce(); err != nil {
			t.Errorf("ReapOnce on a fresh reaper = %v, want nil", err)
		}
		//: and a Stop before any Start is a harmless no-op.
		r.Stop()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestIsPID1 pins the init check. It is what a supervisor consults to decide
// whether it needs subreaper mode at all — as pid 1 it already receives every
// orphan — so a false positive would leave orphans unreaped in a container.
func TestIsPID1(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the running process"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := svcreaper.IsPID1()
		//: the answer must match the process's real pid, whatever it happens
		//: to be — a test binary run as pid 1 in a container is legitimate.
		if want := os.Getpid() == 1; got != want {
			t.Errorf("IsPID1() = %v for pid %d, want %v", got, os.Getpid(), want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
