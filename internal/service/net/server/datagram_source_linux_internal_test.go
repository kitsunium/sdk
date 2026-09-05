//go:build linux

// Package server — batch-reader selection on Linux.
package server

import (
	stdnet "net"
	"syscall"
	"testing"
)

// wrappedConn is a PacketConn that deliberately exposes no SyscallConn.
type wrappedConn struct {
	stdnet.PacketConn
}

// brokenRawConn exposes a SyscallConn that refuses, which is what a socket
// already closed underneath the reader looks like.
type brokenRawConn struct {
	stdnet.PacketConn
}

// brokenRawConn is a rawConnProvider, which is the whole point of the double.
var _ rawConnProvider = brokenRawConn{}

// SyscallConn implements rawConnProvider by failing.
func (brokenRawConn) SyscallConn() (syscall.RawConn, error) {
	//: the descriptor cannot be reached, so there is nothing to batch on.
	return nil, syscall.EBADF
}

// socketKind names the three shapes of PacketConn the selection has to handle.
type socketKind int

const (
	// socketReal is a genuine UDP socket, which carries a descriptor.
	socketReal socketKind = iota
	// socketNoDescriptor is a wrapper that exposes no SyscallConn at all.
	socketNoDescriptor
	// socketBrokenDescriptor exposes one that refuses.
	socketBrokenDescriptor
)

// packetConnOf builds the socket a case asks for.
func packetConnOf(t *testing.T, kind socketKind) stdnet.PacketConn {
	t.Helper()
	//: only the real socket needs cleaning up.
	if kind != socketReal {
		//: a double, which is exactly what the fallback paths are for.
		return map[socketKind]stdnet.PacketConn{
			socketNoDescriptor:     wrappedConn{},
			socketBrokenDescriptor: brokenRawConn{},
		}[kind]
	}
	pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		//: a close failure here would leak a socket into the rest of the suite.
		if cerr := pc.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return pc
}

// Test_newDatagramSource pins the platform selection itself.
//
// Every behavioural datagram test would pass identically on the portable
// fallback, so none of them prove the batched path is actually in use. If a
// refactor broke the type assertion here, the engine would quietly degrade to
// one syscall per datagram and every other test would stay green — this is the
// only one that would fail.
//
// The fallbacks matter just as much. A PacketConn with no raw descriptor — a
// test double, or a wrapper — must still be SERVED rather than rejected, and so
// must one whose descriptor cannot be reached: recvmmsg needs a real file
// descriptor, and falling back keeps such a socket working instead of failing at
// the first read.
func Test_newDatagramSource(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// socket selects which kind of PacketConn the engine is handed.
		socket socketKind
		// wantBatched is whether the batched reader must be selected.
		wantBatched bool
	}
	tests := []tc{
		{name: "a real UDP socket", socket: socketReal, wantBatched: true},
		{name: "a wrapper with no descriptor", socket: socketNoDescriptor},
		{name: "a socket whose descriptor cannot be reached", socket: socketBrokenDescriptor},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pc := packetConnOf(t, c.socket)

		reader := newDatagramSource(pc)

		if c.wantBatched {
			//: a portable reader on linux means the batched path silently vanished.
			if _, ok := reader.(*multiReader); !ok {
				t.Fatalf("newDatagramSource returned %T on linux, want *multiReader — "+
					"the engine has silently degraded to one syscall per datagram", reader)
			}
			return
		}
		//: the portable reader is the floor every socket can use.
		if _, ok := reader.(*portableReader); !ok {
			t.Fatalf("newDatagramSource returned %T for a descriptor-less conn, "+
				"want *portableReader", reader)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_batchAvailable pins the claim State reports.
//
// The degradation shown to an operator is derived from this, so it has to agree
// with what newDatagramSource actually selects — a State that says "batched" on
// a build with no recvmmsg is worse than one that says nothing at all, because
// it is the report an operator uses to decide the syscall count is not the
// problem.
func Test_batchAvailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// socket selects which kind of PacketConn the engine is handed.
		socket socketKind
		// wantAgrees is whether the reader selected for that socket must match
		// the platform claim.
		wantAgrees bool
	}
	tests := []tc{
		{name: "a real UDP socket", socket: socketReal, wantAgrees: true},
		//: the fallbacks are a property of the SOCKET, not of the platform, so
		//: the claim stays true even where the batched reader is not selected.
		{name: "a wrapper with no descriptor", socket: socketNoDescriptor},
		{name: "a socket whose descriptor cannot be reached", socket: socketBrokenDescriptor},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the build tag on this file is what asserts we are on Linux, which has
		//: recvmmsg.
		if !batchAvailable() {
			t.Fatal("batchAvailable reports false on linux")
		}

		_, batched := newDatagramSource(packetConnOf(t, c.socket)).(*multiReader)

		if batched != c.wantAgrees {
			t.Fatalf("batchAvailable() is true but a %v socket got batched = %v — "+
				"State would report a capability the engine does not use",
				c.name, batched)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
