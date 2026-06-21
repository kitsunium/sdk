//go:build freebsd

// Package cgroup_test — FreeBSD rctl backend contract. These cases exercise only
// the syscall-free surface (Create, Available, and the uniform degrade verbs
// SetPidsMax / SetIOMax / Freeze / Thaw), so they run on any FreeBSD host
// regardless of whether the kernel enables RACCT/RCTL. The rule-applying verbs
// (SetMemoryMax / SetCPUMax / Add / Kill / Delete) need a RACCT kernel and a
// live process, so they are covered by the e2e VM lane, not here.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestCreateSucceedsFreeBSD asserts the rctl backend hands back a usable group
// (Create allocates a tracker; it touches no kernel state).
func TestCreateSucceedsFreeBSD(t *testing.T) {
	t.Parallel()

	g, err := cgroup.Create("sdk-test-freebsd")
	//: Create is allocation-only on FreeBSD — it must never error.
	if err != nil {
		//: a creation error breaks the rctl group contract.
		t.Fatalf("Create on FreeBSD = %v, want nil", err)
	}
	//: a nil group would leave the caller with nothing to limit.
	if g == nil {
		//: a missing handle is a contract breach.
		t.Fatal("Create on FreeBSD returned nil group, want a handle")
	}
}

// TestAvailableTrueFreeBSD asserts the probe reports true — rctl is the native
// FreeBSD facility, present on a stock kernel.
func TestAvailableTrueFreeBSD(t *testing.T) {
	t.Parallel()

	//: rctl is the native facility — the probe reports it present.
	if !cgroup.Available() {
		//: false would wrongly hide the rctl backend.
		t.Fatal("Available on FreeBSD = false, want true")
	}
}

// TestDegradeVerbsFreeBSD asserts the non-mappable verbs return the uniform
// UnsupportedPlatform sentinel (they never reach a syscall).
func TestDegradeVerbsFreeBSD(t *testing.T) {
	t.Parallel()

	g, err := cgroup.Create("sdk-test-freebsd-degrade")
	//: a usable group is the precondition for the verb checks.
	if err != nil {
		//: bail if Create itself failed.
		t.Fatalf("Create = %v, want nil", err)
	}
	//: each non-mappable verb must surface UNSUPPORTED_PLATFORM.
	type verb struct {
		name string
		call func() error
	}
	verbs := []verb{
		{"SetPidsMax", func() error { return g.SetPidsMax(1) }},
		{"SetIOMax", func() error { return g.SetIOMax("8:0 rbps=1048576") }},
		{"Freeze", g.Freeze},
		{"Thaw", g.Thaw},
	}
	runCase := func(t *testing.T, v verb) {
		t.Helper()
		//: the verb must return the central UnsupportedPlatform sentinel.
		if err := v.call(); err != coreproc.UnsupportedPlatform {
			//: any other result breaks the honest-degrade contract.
			t.Errorf("%s on FreeBSD = %v, want UnsupportedPlatform", v.name, err)
		}
	}
	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) { runCase(t, v) })
	}
}
