// Package server — the datagram batch-read abstraction.
package server

import (
	"testing"
)

// Test_datagram_payload pins that only the PREFIX the read filled is handed on.
//
// Slots are reused for the life of the read loop, so a slot's buffer still holds
// whatever the previous datagram put there. Handing the whole buffer to the
// handler would append the tail of an older datagram — from a different peer —
// to every short one, which is a data-leak between senders rather than a
// cosmetic bug.
func Test_datagram_payload(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// size is the slot buffer's length.
		size int
		// n is how many bytes the last read placed in it.
		n int
	}
	tests := []tc{
		{name: "a full buffer", size: 8, n: 8},
		{name: "a short datagram in a reused buffer", size: 1024, n: 3},
		//: an empty datagram is legal on UDP and must not become the buffer's
		//: stale contents.
		{name: "an empty datagram", size: 1024, n: 0},
		{name: "a single byte", size: 64, n: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		slot := datagram{buf: make([]byte, c.size), n: c.n}
		//: fill the whole buffer, so anything past n is visibly stale.
		for i := range slot.buf {
			slot.buf[i] = 0xAA
		}

		got := slot.payload()

		if len(got) != c.n {
			t.Fatalf("payload has %d bytes for a %d-byte read — the handler would "+
				"see the tail of a previous datagram", len(got), c.n)
		}
		//: the payload aliases the slot rather than copying it, which is what
		//: keeps the read loop allocation-free.
		if c.n > 0 && &got[0] != &slot.buf[0] {
			t.Error("payload copied the slot buffer instead of aliasing it")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_newSlots pins the PROBE BYTE, which is what makes an oversized datagram
// detectable at all.
//
// The kernel copies as much as the buffer holds and discards the remainder, so a
// buffer sized exactly at the group's ceiling cannot tell a datagram that just
// fits from one that was cut down to fit — both report the same length. Reading
// one byte further makes the two distinguishable, which is the same reason the
// outbound body ceiling reads one byte past its own limit.
func Test_newSlots(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// count is the batch size.
		count int
		// size is the group's datagram ceiling.
		size int
	}
	tests := []tc{
		{name: "a single slot", count: 1, size: 1500},
		{name: "a full batch", count: 32, size: 1500},
		{name: "the domain defaults", count: defaultBatchSize, size: defaultMaxPacketSize},
		{name: "a tiny ceiling", count: 2, size: 1},
		{name: "no slots at all", count: 0, size: 1500},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		slots := newSlots(c.count, c.size)

		//: the length IS the batch size: the slice is indexed, never appended to.
		if len(slots) != c.count {
			t.Fatalf("newSlots(%d, %d) returned %d slots, want %d",
				c.count, c.size, len(slots), c.count)
		}
		for i := range slots {
			//: one byte PAST the ceiling, which is the whole difference between
			//: a datagram that just fits and one that was truncated to fit.
			if len(slots[i].buf) != c.size+1 {
				t.Fatalf("slot %d has a %d-byte buffer for a %d-byte ceiling, want %d — "+
					"an oversized datagram would be indistinguishable from one that fits",
					i, len(slots[i].buf), c.size, c.size+1)
			}
			//: each slot owns its own buffer, or two datagrams read in one batch
			//: would overwrite each other.
			if i > 0 && &slots[i].buf[0] == &slots[i-1].buf[0] {
				t.Fatalf("slots %d and %d share a buffer", i-1, i)
			}
			//: a fresh slot has read nothing yet.
			if slots[i].n != 0 || slots[i].addr != nil {
				t.Fatalf("slot %d arrives pre-filled: n=%d addr=%v", i, slots[i].n, slots[i].addr)
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
