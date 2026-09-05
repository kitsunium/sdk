//go:build linux

// Package server — SO_REUSEPORT support on Linux.
package server

import (
	"syscall"
	"testing"
)

// Test_reusePortSupported pins that the platform CLAIM agrees with the kernel.
//
// The whole sharding decision hangs off it: resolveShards collapses to a single
// listener and reports a degradation whenever it is false, so a claim that
// disagrees with reality either silently disables sharding on a kernel that has
// it, or lets N listeners attempt a bind only the first can win. Asserting the
// claim against a real setsockopt is the only thing that can tell the two apart.
func Test_reusePortSupported(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain and typ select the socket family.
		domain int
		typ    int
	}
	tests := []tc{
		{name: "an IPv4 stream socket", domain: syscall.AF_INET, typ: syscall.SOCK_STREAM},
		{name: "an IPv4 datagram socket", domain: syscall.AF_INET, typ: syscall.SOCK_DGRAM},
		{name: "an IPv6 stream socket", domain: syscall.AF_INET6, typ: syscall.SOCK_STREAM},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fd, err := syscall.Socket(c.domain, c.typ, 0)
		if err != nil {
			t.Fatalf("socket: %v", err)
		}
		t.Cleanup(func() {
			if cerr := syscall.Close(fd); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		})

		claimed := reusePortSupported()
		accepted := setReusePort(uintptr(fd)) == nil

		//: a claim the kernel contradicts is worse than either answer on its own,
		//: because State reports the claim and the bind obeys the kernel.
		if claimed != accepted {
			t.Fatalf("reusePortSupported() = %v but the kernel %s the option — "+
				"State and the bind would disagree",
				claimed, map[bool]string{true: "accepted", false: "refused"}[accepted])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_setReusePort pins that the option actually reaches the kernel.
//
// A setsockopt that silently did nothing would leave every shard binding
// without the option, so only the first would succeed and the rest would fail
// at Start on an address that looks free. Reading the value back through
// getsockopt is the only thing that distinguishes "set" from "accepted and
// ignored".
func Test_setReusePort(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// domain and typ select the socket family.
		domain int
		typ    int
	}
	tests := []tc{
		{name: "an IPv4 stream socket", domain: syscall.AF_INET, typ: syscall.SOCK_STREAM},
		{name: "an IPv4 datagram socket", domain: syscall.AF_INET, typ: syscall.SOCK_DGRAM},
		{name: "an IPv6 stream socket", domain: syscall.AF_INET6, typ: syscall.SOCK_STREAM},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fd, err := syscall.Socket(c.domain, c.typ, 0)
		if err != nil {
			t.Fatalf("socket: %v", err)
		}
		t.Cleanup(func() {
			if cerr := syscall.Close(fd); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		})
		//: read back at the LITERAL 15 rather than at the package constant, so
		//: what is under test is that soReusePort names the option Linux calls
		//: SO_REUSEPORT in include/asm-generic/socket.h. Comparing the constant
		//: to itself would prove nothing, and a wrong value would only surface
		//: at bind time on a platform nobody tests locally.
		const kernelSOReusePort int = 15
		before, gerr := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, kernelSOReusePort)
		if gerr != nil {
			t.Fatalf("getsockopt before: %v", gerr)
		}
		if before != 0 {
			t.Fatalf("SO_REUSEPORT was already %d on a fresh socket", before)
		}

		if serr := setReusePort(uintptr(fd)); serr != nil {
			t.Fatalf("setReusePort: %v", serr)
		}

		after, gerr := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, kernelSOReusePort)
		if gerr != nil {
			t.Fatalf("getsockopt after: %v", gerr)
		}
		//: without this the shards past the first would fail to bind.
		if after == 0 {
			t.Fatal("SO_REUSEPORT is still unset after setReusePort — either the " +
				"option never reached the kernel, or soReusePort names a different " +
				"one and every shard past the first would fail to bind")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
