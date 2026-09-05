//go:build linux

// Package server — the recvmmsg batched datagram reader.
package server

import (
	"errors"
	stdnet "net"
	"os"
	"syscall"
	"testing"
	"time"
)

// udpPair opens a bound receiver and a sender that can reach it.
func udpPair(t *testing.T) (receiver *stdnet.UDPConn, sender *stdnet.UDPConn) {
	t.Helper()
	raw, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	receiver, ok := raw.(*stdnet.UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned a %T, want *net.UDPConn", raw)
	}
	t.Cleanup(func() {
		if cerr := receiver.Close(); cerr != nil && !errors.Is(cerr, stdnet.ErrClosed) {
			t.Errorf("close receiver: %v", cerr)
		}
	})
	addr, ok := receiver.LocalAddr().(*stdnet.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr is a %T, want *net.UDPAddr", receiver.LocalAddr())
	}
	sender, derr := stdnet.DialUDP("udp", nil, addr)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	t.Cleanup(func() {
		if cerr := sender.Close(); cerr != nil {
			t.Errorf("close sender: %v", cerr)
		}
	})
	return receiver, sender
}

// Test_newMultiReader pins that the descriptor is taken at CONSTRUCTION.
//
// A reader built over a socket whose descriptor cannot be reached would fail on
// its first read instead — inside the accept loop, with nobody to report to —
// which is exactly why newDatagramSource asks here and degrades to the portable
// path when the answer is no.
func Test_newMultiReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// broken hands back a socket whose descriptor cannot be reached.
		broken bool
	}
	tests := []tc{
		{name: "a real UDP socket"},
		{name: "a socket whose descriptor cannot be reached", broken: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var pc stdnet.PacketConn
		var provider rawConnProvider = brokenRawConn{}
		if !c.broken {
			receiver, _ := udpPair(t)
			pc, provider = receiver, receiver
		}

		reader, err := newMultiReader(pc, provider)

		if c.broken {
			if err == nil {
				t.Fatal("newMultiReader accepted a socket with no reachable descriptor")
			}
			//: nothing is handed back, so newDatagramSource cannot install a
			//: reader that would fail on its first read.
			if reader != nil {
				t.Error("newMultiReader returned a reader beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("newMultiReader = %v, want nil", err)
		}
		if reader.raw == nil {
			t.Fatal("the reader holds no raw descriptor")
		}
		//: the arrays are sized lazily, on the first read, when the slot count
		//: is known — so a fresh reader is wired for nothing.
		if reader.wired != 0 {
			t.Errorf("a fresh reader is already wired for %d slots", reader.wired)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_multiReader_wire pins that wiring happens once per SLOT COUNT rather than
// once per read.
//
// The message array borrows each slot's buffer for the life of the socket, which
// is what makes a batched read cost no allocation at all. Rebuilding it per read
// would undo the entire reason for the batched path; never rebuilding it would
// leave the kernel writing into buffers that no longer exist.
func Test_multiReader_wire(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// first and second are the slot counts wired in order.
		first  int
		second int
		// wantRewired is whether the second call must rebuild the arrays.
		wantRewired bool
	}
	tests := []tc{
		{name: "the same batch twice", first: 8, second: 8},
		{name: "a larger batch", first: 8, second: 32, wantRewired: true},
		{name: "a smaller batch", first: 32, second: 8, wantRewired: true},
		{name: "a single slot", first: 1, second: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reader := &multiReader{}
		slots := newSlots(c.first, 1500)

		reader.wire(slots)

		if reader.wired != c.first {
			t.Fatalf("wired = %d after the first call, want %d", reader.wired, c.first)
		}
		if len(reader.headers) != c.first || len(reader.iovecs) != c.first || len(reader.names) != c.first {
			t.Fatalf("the arrays are %d/%d/%d long, want %d each",
				len(reader.headers), len(reader.iovecs), len(reader.names), c.first)
		}
		//: each header borrows its slot's buffer, or the kernel would write
		//: somewhere the read loop never looks.
		for i := range slots {
			if reader.iovecs[i].Base != &slots[i].buf[0] {
				t.Fatalf("iovec %d does not point at its slot's buffer", i)
			}
			if kernelLen(reader.iovecs[i].Len) != uint64(len(slots[i].buf)) {
				t.Fatalf("iovec %d is %d bytes, want %d",
					i, kernelLen(reader.iovecs[i].Len), len(slots[i].buf))
			}
			//: one iovec per message: a datagram is contiguous.
			if kernelLen(reader.headers[i].hdr.Iovlen) != 1 {
				t.Fatalf("header %d claims %d iovecs, want 1",
					i, kernelLen(reader.headers[i].hdr.Iovlen))
			}
			if reader.headers[i].hdr.Namelen != syscall.SizeofSockaddrAny {
				t.Fatalf("header %d has room for %d name bytes, want %d",
					i, reader.headers[i].hdr.Namelen, syscall.SizeofSockaddrAny)
			}
		}

		firstHeaders := reader.headers
		reader.wire(newSlots(c.second, 1500))

		//: rebuilding on every read would undo the whole point of batching.
		rewired := &reader.headers[0] != &firstHeaders[0]
		if rewired != c.wantRewired {
			t.Fatalf("the second wire rebuilt the arrays = %v, want %v", rewired, c.wantRewired)
		}
		if reader.wired != c.second {
			t.Errorf("wired = %d, want %d", reader.wired, c.second)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_multiReader_readBatch pins the batched read end to end against a real
// socket: what the kernel queued is what the handler sees, in order, with each
// datagram's own length and sender.
//
// It also pins that a CLOSED socket ends the read rather than spinning. The loop
// above it has nobody to hand an error to, so a reader that returned (0, nil)
// forever would burn a core silently.
func Test_multiReader_readBatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// payloads are the datagrams queued before the read.
		payloads []string
		// slots is the batch size the loop offers.
		slots int
		// closed closes the socket before reading.
		closed bool
	}
	tests := []tc{
		{name: "one datagram", payloads: []string{"hello"}, slots: 8},
		{name: "several datagrams in one syscall", payloads: []string{"a", "bb", "ccc"}, slots: 8},
		{name: "a batch of exactly one", payloads: []string{"only"}, slots: 1},
		{name: "an empty datagram", payloads: []string{""}, slots: 4},
		{name: "a closed socket", closed: true, slots: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		receiver, sender := udpPair(t)
		reader, err := newMultiReader(receiver, receiver)
		if err != nil {
			t.Fatalf("newMultiReader: %v", err)
		}
		for _, payload := range c.payloads {
			if _, werr := sender.Write([]byte(payload)); werr != nil {
				t.Fatalf("send %q: %v", payload, werr)
			}
		}
		//: a deadline turns a hung read into a failure rather than a stalled suite.
		if derr := receiver.SetReadDeadline(time.Now().Add(5 * time.Second)); derr != nil {
			t.Fatalf("set deadline: %v", derr)
		}
		if c.closed {
			if cerr := receiver.Close(); cerr != nil {
				t.Fatalf("close: %v", cerr)
			}
		}
		slots := newSlots(c.slots, 1500)

		n, rerr := reader.readBatch(slots)

		if c.closed {
			//: a closed socket ends the read loop instead of spinning on it.
			if rerr == nil {
				t.Fatalf("readBatch on a closed socket returned %d datagrams and no error", n)
			}
			return
		}
		if rerr != nil {
			t.Fatalf("readBatch = %v, want nil", rerr)
		}
		//: MSG_WAITFORONE returns as soon as one datagram is ready, so a short
		//: batch is correct; what must never happen is more than were sent.
		if n < 1 || n > len(c.payloads) {
			t.Fatalf("readBatch filled %d slots for %d queued datagrams", n, len(c.payloads))
		}
		for i := range n {
			//: each datagram's own length, not the buffer's.
			if got := string(slots[i].payload()); got != c.payloads[i] {
				t.Errorf("slot %d carries %q, want %q", i, got, c.payloads[i])
			}
			//: the sender must reach the handler, or a reply has nowhere to go.
			if slots[i].addr == nil {
				t.Errorf("slot %d reports no sender", i)
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

// Test_multiReader_readBatch_Deadline pins that the batched read stays under the
// netpoller.
//
// Driving recvmmsg inside RawConn.Read is what keeps the goroutine parked rather
// than spinning: returning false on EAGAIN hands control back to the runtime
// exactly as a blocking ReadFrom would. A reader that busy-looped would still
// pass every functional test above while burning a core per idle socket, and the
// only observable difference is that the socket's own deadline still fires.
func Test_multiReader_readBatch_Deadline(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// budget is how long the read is allowed to park.
		budget time.Duration
		// slots is the batch size the loop offers.
		slots int
	}
	tests := []tc{
		{name: "a short budget on a single slot", budget: 150 * time.Millisecond, slots: 1},
		{name: "a short budget on a full batch", budget: 150 * time.Millisecond, slots: 32},
		{name: "a longer budget", budget: 400 * time.Millisecond, slots: 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		receiver, _ := udpPair(t)
		reader, err := newMultiReader(receiver, receiver)
		if err != nil {
			t.Fatalf("newMultiReader: %v", err)
		}
		//: nothing is queued, so the read has to park.
		if derr := receiver.SetReadDeadline(time.Now().Add(c.budget)); derr != nil {
			t.Fatalf("set deadline: %v", derr)
		}

		started := time.Now()
		n, rerr := reader.readBatch(newSlots(c.slots, 1500))

		if rerr == nil {
			t.Fatalf("readBatch returned %d datagrams from an empty socket", n)
		}
		//: the runtime's own deadline, which only fires for a goroutine that is
		//: genuinely parked on the netpoller.
		if !errors.Is(rerr, os.ErrDeadlineExceeded) {
			t.Fatalf("readBatch = %v, want a deadline — the read is not parked on the netpoller", rerr)
		}
		//: a busy loop would come back well before the budget, having spun.
		if waited := time.Since(started); waited < c.budget/2 {
			t.Errorf("readBatch came back after %v, well before its %v deadline", waited, c.budget)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_multiReader_fill pins that only the slots the kernel ACTUALLY filled are
// updated. Copying results back for the whole array would hand the loop stale
// lengths and senders from the previous batch, and the loop dispatches on the
// count alone.
func Test_multiReader_fill(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// slots is the batch size.
		slots int
		// received is how many the kernel reported.
		received int
	}
	tests := []tc{
		{name: "a full batch", slots: 4, received: 4},
		{name: "a partial batch", slots: 8, received: 3},
		{name: "a single datagram", slots: 8, received: 1},
		{name: "nothing at all", slots: 8, received: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reader := &multiReader{}
		slots := newSlots(c.slots, 1500)
		reader.wire(slots)
		//: the kernel reports a length per message and a sender per name slot.
		for i := range reader.headers {
			reader.headers[i].length = uint32(i + 1)
			reader.names[i] = *inet4Sockaddr([4]byte{127, 0, 0, 1}, uint16(1000+i))
		}
		//: mark every slot so an untouched one stays visible.
		for i := range slots {
			slots[i].n = -1
		}

		reader.fill(slots, c.received)

		for i := range slots {
			//: past the reported count the slot is stale, and the loop must not
			//: look at it — dispatching on the count is what makes that safe.
			if i >= c.received {
				if slots[i].n != -1 {
					t.Errorf("slot %d was filled although the kernel reported only %d datagrams",
						i, c.received)
				}
				continue
			}
			if slots[i].n != i+1 {
				t.Errorf("slot %d has length %d, want %d", i, slots[i].n, i+1)
			}
			want := &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1).To4(), Port: 1000 + i}
			if slots[i].addr == nil || slots[i].addr.String() != want.String() {
				t.Errorf("slot %d reports sender %v, want %v", i, slots[i].addr, want)
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
