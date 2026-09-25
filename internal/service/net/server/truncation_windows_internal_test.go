//go:build windows

// Package server — the datagram a Windows read reports as too long, where
// every Unix kernel truncates it in silence.
package server

import (
	stdnet "net"
	"os"
	"syscall"
	"testing"
)

// Test_datagramTruncated_readsTheErrnoThroughNetsWrapping pins the
// classification portableReader relies on, in the shape the net package really
// returns it: an *OpError around an *os.SyscallError around the errno.
func Test_datagramTruncated_readsTheErrnoThroughNetsWrapping(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
		want bool
	}
	wrapped := func(errno syscall.Errno) error {
		return &stdnet.OpError{Op: "read", Net: "udp", Err: os.NewSyscallError("wsarecvfrom", errno)}
	}
	tests := []tc{
		{name: "a datagram longer than the buffer", err: wrapped(wsaeMsgSize), want: true},
		{name: "the bare errno", err: wsaeMsgSize, want: true},
		{name: "a reset, which is not a truncation", err: wrapped(syscall.Errno(10054))},
		{name: "a closed socket", err: stdnet.ErrClosed},
		{name: "no error at all", err: nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the errno is found through every wrapper, and only that errno.
		if got := datagramTruncated(c.err); got != c.want {
			t.Fatalf("datagramTruncated(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	//: one subtest per shape of error.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_portableReader_reportsATruncatedDatagramAsOversized pins what the
// reader does with the errno: one slot filled to the probe byte, which dispatch
// drops and COUNTS, instead of an error the read loop skips.
func Test_portableReader_reportsATruncatedDatagramAsOversized(t *testing.T) {
	t.Parallel()
	const ceiling int = 64
	slots := newSlots(1, ceiling)
	reader := &portableReader{pc: truncatingConn{}}
	n, err := reader.readBatch(slots)
	//: the errno became a datagram, not an error the read loop would skip.
	if err != nil || n != 1 {
		t.Fatalf("readBatch = (%d, %v), want one oversized datagram and no error", n, err)
	}
	//: filled past the ceiling, which is how dispatch recognises oversize.
	if slots[0].n <= ceiling {
		t.Fatalf("the slot holds %d bytes, want more than the %d-byte ceiling so dispatch drops it", slots[0].n, ceiling)
	}
	srv := New()
	srv.dispatch(slots[:n], &packet{group: "g"}, nil, ceiling)
	//: and the drop is counted, which is what Windows used to lose.
	if got := srv.oversized.Load(); got != 1 {
		t.Fatalf("oversized = %d, want 1 — the drop was not counted", got)
	}
}

// truncatingConn is a PacketConn whose every read fails the way Windows fails
// one for a datagram longer than the buffer.
type truncatingConn struct{ stdnet.PacketConn }

// ReadFrom implements net.PacketConn with the WSAEMSGSIZE failure.
func (truncatingConn) ReadFrom(p []byte) (int, stdnet.Addr, error) {
	return len(p), nil, &stdnet.OpError{Op: "read", Net: "udp", Err: os.NewSyscallError("wsarecvfrom", wsaeMsgSize)}
}
