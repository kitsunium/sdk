//go:build unix

package server_test

import (
	stdnet "net"
	"net/netip"
	"os"
	"slices"
	"syscall"
	"testing"
)

// fdScanMax bounds the descriptors listeningFDsOn looks at, whatever the
// soft limit says.
const fdScanMax uint64 = 1 << 16

// listeningFDsOn lists this process's descriptors that are listening TCP
// sockets bound to addr, read from the descriptors themselves (getsockname
// and SO_ACCEPTCONN) where no table names a socket's process. A copy a fork
// holds is in the child's table, not this one; a socket a test of this
// process opens later has a number this list never held.
func listeningFDsOn(addr netip.AddrPort) []int {
	var lim syscall.Rlimit
	top := fdScanMax
	//: the soft limit bounds every descriptor number, when it can be read.
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil && uint64(lim.Cur) < top {
		top = uint64(lim.Cur)
	}
	var fds []int
	//: each possible descriptor number.
	for fd := range int(top) {
		//: a listening socket, bound to addr.
		if listensOn(fd, addr) {
			fds = append(fds, fd)
		}
	}
	return fds
}

// listensOn reports whether fd is a listening socket bound to addr.
func listensOn(fd int, addr netip.AddrPort) bool {
	sa, err := syscall.Getsockname(fd)
	//: not a socket, or not open.
	if err != nil {
		return false
	}
	var got netip.AddrPort
	//: by family.
	switch sa := sa.(type) {
	//: IPv4.
	case *syscall.SockaddrInet4:
		got = netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port))
	//: IPv6.
	case *syscall.SockaddrInet6:
		got = netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), uint16(sa.Port))
	//: a Unix socket or anything else is never a TCP listener on addr.
	default:
		return false
	}
	//: the same address, an IPv4 one possibly mapped.
	if got.Addr().Unmap() != addr.Addr().Unmap() || got.Port() != addr.Port() {
		return false
	}
	accepting, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	//: a connected socket on the address does not hold it for a bind.
	return err == nil && accepting != 0
}

// stillListening is the descriptors of before that still listen on addr:
// the server's own sockets that Close did not release. A third party that
// bound the freed port since has a descriptor of its own, which before never
// held, and is not counted.
func stillListening(before []int, addr netip.AddrPort) []int {
	var held []int
	//: each descriptor the server held.
	for _, fd := range before {
		//: still a listener on addr.
		if listensOn(fd, addr) {
			held = append(held, fd)
		}
	}
	return held
}

// TestReleasedAfterClose_judgesTheServersOwnSockets pins what the check of
// TestServer_Close reads (#1386): a descriptor of the server that outlives
// Close is found, and a socket another party binds on the freed port is not
// taken for one. Rebinding the port, the check the suite had off Linux,
// failed in the second case: the port was held, by someone else.
func TestReleasedAfterClose_judgesTheServersOwnSockets(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// dupBeforeClose keeps a duplicate of the listener's descriptor across
		// Close, as a descriptor the engine forgot to close would.
		dupBeforeClose bool
		// thirdParty binds the freed port after Close.
		thirdParty bool
		// wantHeld is how many of the server's descriptors still listen.
		wantHeld int
	}
	tests := []tc{
		{name: "everything closed", wantHeld: 0},
		{name: "a duplicate outlives the close", dupBeforeClose: true, wantHeld: 1},
		{name: "a third party takes the freed port", thirdParty: true, wantHeld: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ln := listenTCP(t)
		addr := ln.Addr().String()
		ap := netip.MustParseAddrPort(addr)
		var dup int
		//: the duplicate is taken before the table is read, like the engine's own.
		if c.dupBeforeClose {
			raw, err := ln.SyscallConn()
			//: no raw access to dup from.
			if err != nil {
				t.Fatalf("syscall conn: %v", err)
			}
			var derr error
			//: the duplicate, or why there is none.
			if cerr := raw.Control(func(fd uintptr) { dup, derr = syscall.Dup(int(fd)) }); cerr != nil || derr != nil {
				t.Fatalf("dup: %v / %v", cerr, derr)
			}
			syscall.CloseOnExec(dup)
			t.Cleanup(func() { swallowClose(dup) })
		}
		before := listeningFDsOn(ap)
		wantBefore := 1
		//: the duplicate is a second descriptor on the same socket.
		if c.dupBeforeClose {
			wantBefore = 2
		}
		//: the table must hold the server's sockets, or the check proves nothing.
		if len(before) != wantBefore {
			t.Fatalf("descriptors listening on %s before Close = %v, want %d", addr, before, wantBefore)
		}
		closeOrFail(t, ln)
		//: the port, drawn by another party before the check looks.
		if c.thirdParty {
			//: the third party's descriptor must not carry a number the check
			//: recorded, or the numbers cannot tell it from the server's. The
			//: lowest free number goes to whoever opens next, and a parallel
			//: case frees lower ones, so a single filler does not pin the
			//: freed number (linux/386, #1386): each bind that lands on a
			//: recorded number is closed and its number filled, until one
			//: does not.
			var other *stdnet.TCPListener
			//: bounded: each round fills one more number.
			for range 4096 {
				l, err := stdnet.Listen("tcp", addr)
				//: the port went elsewhere already; nothing to judge.
				if err != nil {
					t.Skipf("the freed port was drawn already: %v", err)
				}
				tcp, ok := l.(*stdnet.TCPListener)
				//: not a TCP listener.
				if !ok {
					t.Fatalf("expected a *net.TCPListener, got %T", l)
				}
				//: a number the check never recorded: the third party stands.
				if !slices.Contains(before, fdOf(t, tcp)) {
					other = tcp
					break
				}
				closeOrFail(t, tcp)
				filler, ferr := os.Open(os.DevNull)
				//: no descriptor to fill the number with.
				if ferr != nil {
					t.Fatalf("open %s: %v", os.DevNull, ferr)
				}
				defer closeOrFail(t, filler)
			}
			//: every try drew a recorded number.
			if other == nil {
				t.Fatalf("the third party's descriptor was always a recorded number")
			}
			defer closeOrFail(t, other)
			//: what a rebind answers, and what the check must not.
			if again, err := stdnet.Listen("tcp", addr); err == nil {
				closeOrFail(t, again)
				t.Fatalf("a second bind on %s succeeded beside the third party", addr)
			}
		}
		//: the server's descriptors alone are judged.
		if held := stillListening(before, ap); len(held) != c.wantHeld {
			t.Fatalf("descriptors still listening on %s = %v, want %d", addr, held, c.wantHeld)
		}
	}
	//: each case on its own: they share no port.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// swallowClose closes a descriptor the test duplicated; a failure leaves
// nothing to report at the end of a test.
func swallowClose(fd int) {
	//: closed once; an error here has nothing left to undo.
	_ = syscall.Close(fd)
}

// fdOf is the descriptor number of ln's socket.
func fdOf(t *testing.T, ln *stdnet.TCPListener) int {
	t.Helper()
	raw, err := ln.SyscallConn()
	//: no raw access.
	if err != nil {
		t.Fatalf("syscall conn: %v", err)
	}
	fd := -1
	//: the number, or why none.
	if cerr := raw.Control(func(f uintptr) { fd = int(f) }); cerr != nil {
		t.Fatalf("control: %v", cerr)
	}
	return fd
}
