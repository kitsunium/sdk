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

// TestSupported asserts Supported reflects the documented capability × platform
// matrix for the host GOOS, and that an out-of-range value is conservatively
// reported unsupported rather than indexing past the table.
func TestSupported(t *testing.T) {
	t.Parallel()
	g := runtime.GOOS
	unix := isUnixGOOS(g)
	win := g == "windows"
	type tc struct {
		name string
		cap  proc.Capability
		want bool
	}
	tests := []tc{
		{"ProcessSpawn", proc.CapProcessSpawn, unix || win},
		{"SignalRelay", proc.CapSignalRelay, unix || win},
		{"Reaper", proc.CapReaper, unix || win},
		{"Cgroup", proc.CapCgroup, g == "linux" || win},
		{"Rlimit", proc.CapRlimit, unix},
		{"UmaskNiceOOM", proc.CapUmaskNiceOOM, unix},
		{"SdNotify", proc.CapSdNotify, unix},
		{"SocketActivation", proc.CapSocketActivation, unix},
		{"an unknown capability", proc.Capability(0x7BAD), false},
		{"a negative capability", proc.Capability(-1), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each capability's support must match the matrix for this GOOS.
		if got := proc.Supported(c.cap); got != c.want {
			t.Errorf("Supported(%s) on %s = %v, want %v", c.cap, g, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMissingCapabilities returns the gaps and nothing else — a caller uses the
// result to decide what to disable, so an over-report costs a feature and an
// under-report costs a crash.
func TestMissingCapabilities(t *testing.T) {
	t.Parallel()
	bad := proc.Capability(0x7BAD)
	type tc struct {
		name string
		caps []proc.Capability
		want []proc.Capability
	}
	tests := []tc{
		//: ProcessSpawn is present on every tested lane (unix+windows); bad
		//: never is, on any platform.
		{"one supported and one unknown", []proc.Capability{proc.CapProcessSpawn, bad}, []proc.Capability{bad}},
		{"only unknowns", []proc.Capability{bad, proc.Capability(0x7BAE)}, []proc.Capability{bad, proc.Capability(0x7BAE)}},
		{"an all-supported set", []proc.Capability{proc.CapProcessSpawn, proc.CapSignalRelay}, nil},
		{"no capabilities at all", nil, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := proc.MissingCapabilities(c.caps...)
		if len(got) != len(c.want) {
			t.Fatalf("MissingCapabilities%v = %v, want %v", c.caps, got, c.want)
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Errorf("MissingCapabilities%v[%d] = %s, want %s", c.caps, i, got[i], w)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestMustSupport asserts the preflight is silent for capabilities the host
// has and panics with the typed UnsupportedPlatform error for those it lacks,
// so a recover() can classify the failure by code rather than by message text.
func TestMustSupport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		caps      []proc.Capability
		wantPanic bool
	}
	tests := []tc{
		//: CapProcessSpawn is supported on every CI lane (unix + windows).
		{"a capability the host has", []proc.Capability{proc.CapProcessSpawn}, false},
		{"no capabilities at all", nil, false},
		{"a single unsupported capability", []proc.Capability{proc.Capability(0x7BAD)}, true},
		{"an unsupported one beside a supported one", []proc.Capability{proc.CapProcessSpawn, proc.Capability(0x7BAD)}, true},
		{"several unsupported at once", []proc.Capability{proc.Capability(0x7BAD), proc.Capability(0x7BAE)}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if !c.wantPanic {
				//: a panic here would break the no-op-on-supported contract.
				if r != nil {
					t.Fatalf("MustSupport%v panicked: %v", c.caps, r)
				}
				return
			}
			//: MustSupport on an unsupported capability MUST panic.
			if r == nil {
				t.Fatalf("MustSupport%v did not panic", c.caps)
			}
			err, ok := r.(error)
			//: the panic value must be the typed error, never a bare string.
			if !ok {
				t.Fatalf("panic value %T is not an error", r)
			}
			wantCode, _ := perrs.CodeOf(proc.UnsupportedPlatform)
			//: the recovered error must carry the central code.
			if !perrs.HasCode(err, wantCode) {
				t.Fatalf("recovered %v, want code UNSUPPORTED_PLATFORM", err)
			}
		}()
		proc.MustSupport(c.caps...)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Capability_String pins that every declared capability renders as its
// stable name and that an out-of-range value degrades to its integer form
// rather than to an empty string a log reader could not act on.
func Test_Capability_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cap  proc.Capability
		want string
	}
	//: the table has eight entries; the ninth index is the first out of range.
	const pastTheTable proc.Capability = 8
	tests := []tc{
		{"the first capability", proc.CapProcessSpawn, "ProcessSpawn"},
		{"a middle capability", proc.CapCgroup, "Cgroup"},
		{"the last capability", proc.CapSocketActivation, "SocketActivation"},
		{"one past the table", pastTheTable, "Capability(8)"},
		{"a negative value", proc.Capability(-1), "Capability(-1)"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.cap.String(); got != c.want {
			t.Errorf("Capability(%d).String() = %q, want %q", int(c.cap), got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
