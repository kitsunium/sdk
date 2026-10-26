//go:build linux

// Package server — UDP GRO on the recvmmsg reader (ADR 0192).
package server

import (
	"errors"
	stdnet "net"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// groReader opens a UDP socket with a GRO reader over it, and a GSO sender of
// segments of seg bytes aimed at it.
func groReader(t *testing.T, seg int) (*multiReader, *stdnet.UDPConn) {
	t.Helper()
	receiver, _ := udpPair(t)
	reader, err := newMultiReader(receiver, receiver)
	if err != nil {
		t.Fatalf("newMultiReader = %v", err)
	}
	if !reader.enableGRO() {
		t.Skip("this kernel refuses UDP_GRO (before 5.0)")
	}
	sender, err := stdnet.DialUDP("udp", nil, receiver.LocalAddr().(*stdnet.UDPAddr))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	raw, err := sender.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpSegment, seg)
	}); cerr != nil || serr != nil {
		t.Skipf("this kernel refuses UDP_SEGMENT: %v %v", cerr, serr)
	}
	return reader, sender
}

// readOneErr reads one batch within a second and returns what it returned.
func readOneErr(reader *multiReader, slots []datagram) (int, error) {
	if err := reader.pc.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return 0, err
	}
	return reader.readBatch(slots)
}

// readOne reads one batch within a second.
func readOne(t *testing.T, reader *multiReader, slots []datagram) int {
	t.Helper()
	if err := reader.pc.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := reader.readBatch(slots)
	if err != nil {
		t.Fatalf("readBatch = %d, %v", n, err)
	}
	return n
}

// Test_multiReader_readBatch_GRO pins the coalesced read: one GSO send arrives
// as one message whose segment size the control message carries, on every
// call — after a plain datagram left Controllen at zero too —, split into the
// datagrams it was sent as, the last one shorter, and never cut.
func Test_multiReader_readBatch_GRO(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// plainFirst sends and reads a datagram without GSO first.
		plainFirst bool
		// size is the GSO send's length.
		size int
		// want is how many datagrams it carries.
		want int
	}
	const seg = 1200
	tests := []tc{
		{name: "a burst of 54", size: 54 * seg, want: 54},
		{name: "a shorter last segment", size: 54*seg - 500, want: 54},
		{name: "after a plain datagram", plainFirst: true, size: 10 * seg, want: 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reader, sender := groReader(t, seg)
		slots := newSlots(4, reader.slotSize(seg))
		//: a plain datagram carries no control message, and the kernel writes
		//: Controllen back as zero.
		if c.plainFirst {
			if _, err := sender.Write(make([]byte, 100)); err != nil {
				t.Fatal(err)
			}
			readOne(t, reader, slots)
			if slots[0].seg != 0 || slots[0].n != 100 || slots[0].count() != 1 {
				t.Fatalf("plain datagram read as n=%d seg=%d", slots[0].n, slots[0].seg)
			}
		}
		payload := make([]byte, c.size)
		if _, err := sender.Write(payload); err != nil {
			t.Fatal(err)
		}

		n := readOne(t, reader, slots)

		if n != 1 || slots[0].cut {
			t.Fatalf("read %d messages, cut=%v, want one whole message", n, slots[0].cut)
		}
		if slots[0].seg != seg || slots[0].count() != c.want {
			t.Fatalf("seg=%d count=%d, want %d of %d", slots[0].seg, slots[0].count(), c.want, seg)
		}
		last, ok := slots[0].segment(c.want - 1)
		if !ok || len(last) != c.size-(c.want-1)*seg {
			t.Fatalf("last datagram is %d bytes, want %d", len(last), c.size-(c.want-1)*seg)
		}
		if _, ok := slots[0].segment(c.want); ok {
			t.Fatal("a datagram past the message's end")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_multiReader_fill_controlTruncated pins MSG_CTRUNC: a message whose
// control data had no room may have lost its segment size, so it is cut —
// dropped and counted — never handed on as one datagram.
func Test_multiReader_fill_controlTruncated(t *testing.T) {
	t.Parallel()
	reader, sender := groReader(t, 1200)
	//: room for a control header, none for its int.
	reader.controlSpace = syscall.CmsgLen(0)
	slots := newSlots(2, reader.slotSize(1200))
	if _, err := sender.Write(make([]byte, 4*1200)); err != nil {
		t.Fatal(err)
	}

	readOne(t, reader, slots)

	if !slots[0].cut || reader.headers[0].hdr.Flags&syscall.MSG_CTRUNC == 0 {
		t.Fatalf("cut=%v flags=%#x, want a cut message with MSG_CTRUNC", slots[0].cut, reader.headers[0].hdr.Flags)
	}
}

// Test_multiReader_slotSize pins the room: the ceiling without GRO, a whole
// coalesced message with it.
func Test_multiReader_slotSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// gro is whether GRO is on.
		gro bool
		// ceiling is the group's.
		ceiling int
		// want is the slot size.
		want int
	}
	tests := []tc{
		{name: "no GRO", ceiling: 1500, want: 1500},
		{name: "GRO, a small ceiling", gro: true, ceiling: 1500, want: groMessageSize},
		{name: "GRO, a larger ceiling", gro: true, ceiling: 70000, want: 70000},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := (&multiReader{gro: c.gro}).slotSize(c.ceiling); got != c.want {
				t.Fatalf("slotSize(%d) = %d, want %d", c.ceiling, got, c.want)
			}
		})
	}
}

// Test_multiReader_handover pins the handover: what the socket queued is
// drained without waiting and still split — GRO on while it drains —, a parked
// read is woken for it, and once the queue is empty GRO is off and the drain
// ends the loop as a closed socket does.
func Test_multiReader_handover(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// parked hands over while a read waits on an empty socket.
		parked bool
	}
	tests := []tc{
		{name: "with a burst queued"},
		{name: "with a read parked", parked: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		reader, sender := groReader(t, 1200)
		slots := newSlots(4, reader.slotSize(1200))
		reader.wire(slots)
		if c.parked {
			done := make(chan error, 1)
			go func() {
				_, err := reader.readBatch(slots)
				done <- err
			}()
			time.Sleep(50 * time.Millisecond)
			if !reader.handover() {
				t.Fatal("handover reported nothing to drain on a GRO reader")
			}
			select {
			case err := <-done:
				if !errors.Is(err, stdnet.ErrClosed) {
					t.Fatalf("the parked read ended with %v, want net.ErrClosed", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the parked read was not woken by the handover")
			}
			return
		}
		if _, err := sender.Write(make([]byte, 20*1200)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)

		if !reader.handover() {
			t.Fatal("handover reported nothing to drain on a GRO reader")
		}

		n, err := reader.readBatch(slots)
		if err != nil || n != 1 || slots[0].count() != 20 {
			t.Fatalf("drain read %d messages (%d datagrams), %v, want the queued burst split", n, slots[0].count(), err)
		}
		if _, err := reader.readBatch(slots); !errors.Is(err, stdnet.ErrClosed) {
			t.Fatalf("the drain past the queue = %v, want net.ErrClosed", err)
		}
		//: off only once the queue is empty: udp_recvmsg writes the segment
		//: size only while GRO is on.
		var on int
		var gerr error
		_ = reader.raw.Control(func(fd uintptr) {
			on, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpGRO)
		})
		if gerr != nil || on != 0 {
			t.Fatalf("UDP_GRO = %d, %v after the drain, want 0", on, gerr)
		}
		//: a reader without GRO has nothing to hand over.
		if (&multiReader{}).handover() {
			t.Fatal("a reader without GRO started a drain")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_groSegment pins the control walk: the UDP_GRO size found among others,
// none in an empty or malformed buffer.
func Test_groSegment(t *testing.T) {
	t.Parallel()
	msg := func(level, typ int32, value int32) []byte {
		b := make([]byte, syscall.CmsgSpace(4))
		h := (*syscall.Cmsghdr)(unsafe.Pointer(&b[0]))
		h.Level, h.Type = level, typ
		h.SetLen(syscall.CmsgLen(4))
		*(*int32)(unsafe.Pointer(&b[syscall.CmsgLen(0)])) = value
		return b
	}
	type tc struct {
		// name describes the case.
		name string
		// control is the buffer.
		control []byte
		// want is the size found.
		want int
	}
	malformed := msg(syscall.IPPROTO_UDP, udpGRO, 1200)
	(*syscall.Cmsghdr)(unsafe.Pointer(&malformed[0])).SetLen(1)
	tests := []tc{
		{name: "none", want: 0},
		{name: "the GRO size", control: msg(syscall.IPPROTO_UDP, udpGRO, 1200), want: 1200},
		{name: "after another message", control: append(msg(syscall.IPPROTO_IP, 8, 1), msg(syscall.IPPROTO_UDP, udpGRO, 900)...), want: 900},
		{name: "another message only", control: msg(syscall.IPPROTO_IP, 8, 1), want: 0},
		{name: "a length shorter than its header", control: malformed, want: 0},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := groSegment(c.control); got != c.want {
				t.Fatalf("groSegment = %d, want %d", got, c.want)
			}
		})
	}
}

// Test_multiReader_stopGRO pins Close's switch: GRO off, no drain.
func Test_multiReader_stopGRO(t *testing.T) {
	t.Parallel()
	reader, sender := groReader(t, 1200)

	reader.stopGRO()

	var on int
	var gerr error
	_ = reader.raw.Control(func(fd uintptr) {
		on, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_UDP, udpGRO)
	})
	if gerr != nil || on != 0 || reader.handing.Load() {
		t.Fatalf("UDP_GRO = %d, %v, handing=%v, want 0 and no drain", on, gerr, reader.handing.Load())
	}
	//: a read that returns once GRO is being switched off may hold a message
	//: without its segment size: dropped, the loop ended.
	if _, err := sender.Write(make([]byte, 4*1200)); err != nil {
		t.Fatal(err)
	}
	if n, err := readOneErr(reader, newSlots(2, reader.slotSize(1200))); n != 0 || !errors.Is(err, stdnet.ErrClosed) {
		t.Fatalf("a read after stopGRO = %d, %v, want 0 and net.ErrClosed", n, err)
	}
	(&multiReader{}).stopGRO()
}
