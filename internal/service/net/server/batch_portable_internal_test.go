// Package server — the portable datagram reader.
package server

import (
	"errors"
	stdnet "net"
	"testing"
	"time"
)

// fakePacketConn is a PacketConn that hands back scripted datagrams.
type fakePacketConn struct {
	// payloads are delivered one per ReadFrom call.
	payloads []string
	// from is the sender reported for each datagram.
	from stdnet.Addr
	// err ends the reader once the payloads are exhausted, or immediately when
	// there are none.
	err error
	// reads counts how many times the socket was read.
	reads int
	// written records the last payload handed to WriteTo, and writtenTo the
	// address it was addressed to.
	written   []byte
	writtenTo stdnet.Addr
	// writeErr is what WriteTo reports.
	writeErr error
	// local is the address the socket reports as its own.
	local stdnet.Addr
}

// ReadFrom implements net.PacketConn.
func (f *fakePacketConn) ReadFrom(p []byte) (n int, addr stdnet.Addr, err error) {
	f.reads++
	//: no datagram left, so this is the call that reports the end.
	if len(f.payloads) == 0 {
		//: the socket is drained or broken.
		return 0, nil, f.err
	}
	next := f.payloads[0]
	f.payloads = f.payloads[1:]
	//: the kernel copies as much as the buffer holds and discards the rest.
	return copy(p, next), f.from, nil
}

// WriteTo implements net.PacketConn.
func (f *fakePacketConn) WriteTo(p []byte, addr stdnet.Addr) (n int, err error) {
	f.written = append([]byte(nil), p...)
	f.writtenTo = addr
	//: a configured failure stands in for a socket that has gone away.
	if f.writeErr != nil {
		//: nothing left the socket.
		return 0, f.writeErr
	}
	//: the whole datagram was handed to the kernel.
	return len(p), nil
}

// Close implements net.PacketConn.
func (f *fakePacketConn) Close() error {
	//: a memory-backed socket has nothing to release.
	return nil
}

// LocalAddr implements net.PacketConn.
func (f *fakePacketConn) LocalAddr() stdnet.Addr {
	//: the address the datagram arrived on, which Packet.To reports.
	return f.local
}

// SetDeadline implements net.PacketConn.
func (f *fakePacketConn) SetDeadline(time.Time) error {
	//: deadlines are the read loop's concern, not this reader's.
	return nil
}

// SetReadDeadline implements net.PacketConn.
func (f *fakePacketConn) SetReadDeadline(time.Time) error {
	//: deadlines are the read loop's concern, not this reader's.
	return nil
}

// SetWriteDeadline implements net.PacketConn.
func (f *fakePacketConn) SetWriteDeadline(time.Time) error {
	//: deadlines are the read loop's concern, not this reader's.
	return nil
}

// Test_portableReader_readBatch pins that the portable path fills exactly ONE
// slot per call, whatever the batch size.
//
// It deliberately does not loop to fill the batch: a second ReadFrom would block
// until another datagram arrived, turning a low-traffic socket into a stalled
// one. Batching is only a win when the kernel already has the datagrams queued,
// which is precisely what recvmmsg reports and ReadFrom cannot — so a reader
// that "helpfully" filled the batch here would trade latency for a throughput
// gain that does not exist.
func Test_portableReader_readBatch(t *testing.T) {
	t.Parallel()
	broken := errors.New("use of closed network connection")
	sender := &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 9999}

	type tc struct {
		// name describes the case.
		name string
		// payload is the datagram waiting on the socket.
		payload string
		// slots is the batch size the loop offers.
		slots int
		// size is the group's datagram ceiling.
		size int
		// readErr is what the socket reports instead of a datagram.
		readErr error
	}
	tests := []tc{
		{name: "one datagram into a single slot", payload: "hello", slots: 1, size: 1500},
		//: the batch is larger than what one ReadFrom can deliver, and stopping
		//: after one is the point.
		{name: "one datagram into a full batch", payload: "hello", slots: 32, size: 1500},
		{name: "an empty datagram", payload: "", slots: 8, size: 1500},
		{name: "a datagram exactly at the ceiling", payload: "12345", slots: 4, size: 5},
		{name: "an invalid read", slots: 8, size: 1500, readErr: broken},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakePacketConn{from: sender, err: c.readErr}
		if c.readErr == nil {
			socket.payloads = []string{c.payload}
		}
		reader := &portableReader{pc: socket}
		slots := newSlots(c.slots, c.size)

		n, err := reader.readBatch(slots)

		//: exactly one syscall per call, never a speculative second.
		if socket.reads != 1 {
			t.Fatalf("readBatch made %d ReadFrom calls, want 1 — a second one "+
				"would block until another datagram arrived", socket.reads)
		}
		if c.readErr != nil {
			if !errors.Is(err, c.readErr) {
				t.Fatalf("readBatch = %v, want %v", err, c.readErr)
			}
			//: a failed read fills nothing, so the loop cannot dispatch a slot
			//: that was never written.
			if n != 0 {
				t.Errorf("a failed read reported %d datagrams", n)
			}
			return
		}
		if err != nil {
			t.Fatalf("readBatch = %v, want nil", err)
		}
		if n != 1 {
			t.Fatalf("readBatch filled %d slots, want exactly 1", n)
		}
		if got := string(slots[0].payload()); got != c.payload {
			t.Errorf("slot 0 carries %q, want %q", got, c.payload)
		}
		//: the sender must reach the handler, or a reply has nowhere to go.
		if slots[0].addr != sender {
			t.Errorf("slot 0 reports sender %v, want %v", slots[0].addr, sender)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
