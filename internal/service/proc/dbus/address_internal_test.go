package dbus

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestSocketOf: the first unix entry with a path or an abstract name is the
// socket, its value unescaped; every other transport, a listening key, two
// sockets in one entry or a broken escape is not dialled — none usable is
// ADDRESS_INVALID.
func TestSocketOf(t *testing.T) {
	for address, want := range map[string]string{
		"unix:path=/run/dbus/system_bus_socket":       "/run/dbus/system_bus_socket",
		"unix:abstract=/tmp/dbus-x,guid=0123":         "@/tmp/dbus-x",
		"tcp:host=localhost,port=1;unix:path=/a%20b":  "/a b",
		"autolaunch:;unix:path=/x%2Cy":                "/x,y",
		"unix:tmpdir=/tmp;unix:path=/second":          "/second",
		"launchd:env=DBUS_LAUNCHD;unix:abstract=name": "@name",
	} {
		if got, err := socketOf(address); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", address, got, err, want)
		}
	}
	for _, address := range []string{
		"", "tcp:host=localhost,port=1", "unix:tmpdir=/tmp", "unix:runtime=yes", "unix:path=/a,abstract=b",
		"unix:path=", "unix:path=%zz", "unix:path=%2", "unixexec:path=/bin/sh", "nonce-tcp:host=x", "unix",
	} {
		if _, err := socketOf(address); !errs.HasCode(err, coredbus.CodeAddressInvalid) || field(err, "address") != address {
			t.Errorf("%q: %v, want ADDRESS_INVALID naming it", address, err)
		}
	}
}

// TestBusAddresses: the environment's addresses first; the system bus's
// well-known socket, the session bus under XDG_RUNTIME_DIR escaped, and no
// session bus at all without one — nothing is launched, nothing dialled.
func TestBusAddresses(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := SystemBusAddress(); got != "unix:path=/run/dbus/system_bus_socket" {
		t.Errorf("system bus %q", got)
	}
	if got := SessionBusAddress(); got != "" {
		t.Errorf("session bus without XDG_RUNTIME_DIR %q, want none", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000 x")
	if got := SessionBusAddress(); got != "unix:path=/run/user/1000%20x/bus" {
		t.Errorf("session bus %q", got)
	}
	if got, _ := socketOf(SessionBusAddress()); got != "/run/user/1000 x/bus" {
		t.Errorf("the session address reads back as %q", got)
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/elsewhere")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:abstract=s")
	if SystemBusAddress() != "unix:path=/elsewhere" || SessionBusAddress() != "unix:abstract=s" {
		t.Errorf("the environment's addresses: %q %q", SystemBusAddress(), SessionBusAddress())
	}
}

// TestValidNames: the specification's well-known names, and what is not one.
func TestValidNames(t *testing.T) {
	for _, n := range []string{"a.b", "org.freedesktop.login1", "org.example.my-service", "_a.b_1", "A.B-c.D9"} {
		if !validName(n) || checkName(n) != nil {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{
		"", "org", ":1.42", ".org.x", "org..x", "org.x.", "org.1x", "1org.x", "org.x$", "org x.y", "org.é",
		strings.Repeat("a.", 128),
	} {
		if err := checkName(n); validName(n) || !errs.HasCode(err, coredbus.CodeNameInvalid) || field(err, "name") != n {
			t.Errorf("%q: %v, want NAME_INVALID naming it", n, err)
		}
	}
}

// TestDialRefusals: off Linux NewConn is UNSUPPORTED_PLATFORM before it reads
// the address; on Linux an address without a unix socket is ADDRESS_INVALID
// and a socket nobody listens on BUS_UNREACHABLE — within the test's own
// directory, never a bus of this machine.
func TestDialRefusals(t *testing.T) {
	ctx := context.Background()
	if runtime.GOOS != osLinux {
		if _, err := NewConn(ctx, "unix:path=/nonexistent"); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("NewConn on %s: %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		return
	}
	if _, err := NewConn(ctx, "tcp:host=localhost,port=1"); !errs.HasCode(err, coredbus.CodeAddressInvalid) {
		t.Fatalf("a TCP address: %v, want ADDRESS_INVALID", err)
	}
	none := "unix:path=" + escapeValue(filepath.Join(t.TempDir(), "none"))
	if _, err := NewConn(ctx, none); !errs.HasCode(err, coredbus.CodeBusUnreachable) {
		t.Fatalf("no listener: %v, want BUS_UNREACHABLE", err)
	}
}
