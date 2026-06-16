// Package proc_test — black-box tests for the capability preflight.
package proc_test

import (
	"runtime"
	"testing"

	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
)

// isUnixGOOS mirrors the package's Unix-target set for the expected matrix.
func isUnixGOOS(goos string) bool {
	switch goos {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	default:
		return false
	}
}

// TestSupportedMatrix asserts Supported reflects the documented capability ×
// platform matrix for the host GOOS.
func TestSupportedMatrix(t *testing.T) {
	t.Parallel()
	g := runtime.GOOS
	unix := isUnixGOOS(g)
	win := g == "windows"
	cases := []struct {
		cap  proc.Capability
		want bool
	}{
		{proc.CapProcessSpawn, unix || win},
		{proc.CapSignalRelay, unix || win},
		{proc.CapReaper, unix || win},
		{proc.CapCgroup, g == "linux" || win},
		{proc.CapRlimit, unix},
		{proc.CapUmaskNiceOOM, unix},
		{proc.CapSdNotify, unix},
		{proc.CapSocketActivation, unix},
	}
	for _, tc := range cases {
		//: each capability's support must match the matrix for this GOOS.
		if got := proc.Supported(tc.cap); got != tc.want {
			t.Errorf("Supported(%s) on %s = %v, want %v", tc.cap, g, got, tc.want)
		}
	}
}

// TestSupportedRejectsUnknown asserts an out-of-range capability is reported
// unsupported on every platform.
func TestSupportedRejectsUnknown(t *testing.T) {
	t.Parallel()
	//: an unknown capability is conservatively unsupported.
	if proc.Supported(proc.Capability(0x7BAD)) {
		t.Fatal("Supported(unknown) = true, want false")
	}
}

// TestMissingCapabilities returns the gaps and nothing else.
func TestMissingCapabilities(t *testing.T) {
	t.Parallel()
	bad := proc.Capability(0x7BAD)
	missing := proc.MissingCapabilities(proc.CapProcessSpawn, bad)
	//: ProcessSpawn is present on every tested lane (unix+windows), bad never is.
	if len(missing) != 1 || missing[0] != bad {
		t.Fatalf("MissingCapabilities = %v, want [%s]", missing, bad)
	}
	//: an all-supported set yields no gaps.
	if got := proc.MissingCapabilities(proc.CapProcessSpawn, proc.CapSignalRelay); got != nil {
		t.Fatalf("MissingCapabilities(supported) = %v, want nil", got)
	}
}

// TestMustSupportNoOpWhenPresent asserts MustSupport does not panic for a
// capability present on the host.
func TestMustSupportNoOpWhenPresent(t *testing.T) {
	t.Parallel()
	//: CapProcessSpawn is supported on every CI lane (unix + windows).
	defer func() {
		//: a panic here would break the no-op-on-supported contract.
		if r := recover(); r != nil {
			t.Fatalf("MustSupport(present) panicked: %v", r)
		}
	}()
	proc.MustSupport(proc.CapProcessSpawn)
	//: no-args MustSupport is also a no-op.
	proc.MustSupport()
}

// TestMustSupportPanicsTypedError asserts a missing capability panics with the
// typed UnsupportedPlatform error (so a recover() can classify it by code).
func TestMustSupportPanicsTypedError(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		//: MustSupport on an unsupported capability MUST panic.
		if r == nil {
			t.Fatal("MustSupport(unsupported) did not panic")
		}
		err, ok := r.(error)
		//: the panic value must be the typed error, never a bare string.
		if !ok {
			t.Fatalf("panic value %T is not an error", r)
		}
		wantCode, _ := perrs.CodeOf(proc.UnsupportedPlatform)
		//: the recovered error must carry the central UNSUPPORTED_PLATFORM code.
		if !perrs.HasCode(err, wantCode) {
			t.Fatalf("recovered %v, want code UNSUPPORTED_PLATFORM", err)
		}
	}()
	//: an out-of-range capability is unsupported on every platform.
	proc.MustSupport(proc.Capability(0x7BAD))
}
