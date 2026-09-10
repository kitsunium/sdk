//go:build !race

// Package websocket — the allocation contract the package documents but nothing
// enforced.
//
// Both claims below are made in prose in three places — Receive's doc comment
// ("what makes a steady-state read allocate nothing"), initialWriteCapacity's
// ("a steady-state send allocates nothing") and the package CLAUDE.md — and
// until this file existed neither had a test. A documented allocation property
// with no gate is a claim, not a contract.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so AllocsPerRun under `-race` measures
// the detector. That makes this file invisible to the race suite, which is why
// //internal/service/net/websocket:websocket_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package websocket

import (
	"bufio"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// allocRuns is how many times AllocsPerRun exercises each claim. It is well
// past the point where a per-message allocation would round to zero: a single
// malloc on the path reports 1.0, not 1/allocRuns.
const allocRuns int = 200

// allocWarmup is how many messages are read or written before the measurement
// starts. The claim is about the STEADY state — the first message legitimately
// grows the reassembly buffer, and a test that measured it would be pinning the
// opposite of the contract.
const allocWarmup int = 8

// TestSteadyStateReceiveAllocatesNothing pins the reassembly-buffer reuse that
// Receive's doc comment sells as the reason the returned Data aliases the
// connection's own buffer.
//
// MUTATION-CHECKED. Changing trackFragment's `c.msg = c.msg[:0]` to
// `c.msg = nil` — which keeps every test in the suite green, because the
// messages still arrive intact — makes this fail with:
//
//	steady-state Receive allocated 1.0 times per message, want 0
//
// That is the whole point: dropping the capacity is invisible to every
// correctness test and visible only here.
func TestSteadyStateReceiveAllocatesNothing(t *testing.T) {
	conn, done := allocConn(t, benchClientFrame(corenet.WSBinary, true, benchPayload(4<<10)))
	defer done()
	//: the first messages grow the buffer; the contract is about the ones after.
	for range allocWarmup {
		allocReceive(t, conn)
	}
	got := testing.AllocsPerRun(allocRuns, func() {
		allocReceive(t, conn)
	})
	//: a single malloc on the read path reports 1.0 here, not a fraction.
	if got != 0 {
		t.Errorf("steady-state Receive allocated %.1f times per message, want 0", got)
	}
}

// TestSteadyStateSendAllocatesNothing pins the write-buffer reuse
// initialWriteCapacity's comment sells.
//
// MUTATION-CHECKED. Changing writeLocked's `corenet.AppendWSFrame(c.wbuf[:0],
// …)` to `corenet.AppendWSFrame(nil, …)` — which every other test in the suite
// accepts, because the bytes on the wire are byte-for-byte identical — makes
// this fail with:
//
//	steady-state Send allocated 2.0 times per message, want 0
//
// Two and not one, which is why the number here is the OBSERVED failure rather
// than the predicted one: appending into a nil slice allocates for the header
// byte and reallocates for the payload, so a reader who reasoned "one buffer,
// one allocation" would have written a figure the test never prints.
func TestSteadyStateSendAllocatesNothing(t *testing.T) {
	conn, done := allocConn(t, nil)
	defer done()
	message := corenet.WSMessageValue{Binary: true, Data: benchPayload(4 << 10)}
	//: the first sends grow the write buffer past its initial capacity.
	for range allocWarmup {
		allocSend(t, conn, message)
	}
	got := testing.AllocsPerRun(allocRuns, func() {
		allocSend(t, conn, message)
	})
	//: a single malloc on the write path reports 1.0 here, not a fraction.
	if got != 0 {
		t.Errorf("steady-state Send allocated %.1f times per message, want 0", got)
	}
}

// allocConn builds a connection over the endlessly repeating script and returns
// it with the teardown that joins its goroutine.
//
// It goes through NewConn rather than the benchmarks' benchFreshConn: an
// allocation claim about the production read path has to be measured on a
// connection assembled the production way, whatever the benchmarks do to
// isolate a signal.
func allocConn(t *testing.T, script []byte) (conn *Conn, done func()) {
	t.Helper()
	cfg, err := resolve([]Option{WithoutPing()})
	//: a refused option set would leave the test measuring nothing.
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	socket := &benchSocket{script: script, repeat: true}
	conn = NewConn(socket, bufio.NewReader(socket), "", &cfg, nil)
	//: the connection owns a goroutine, so the test joins it.
	return conn, func() {
		//: Close is the handler's call and is what joins the watcher.
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}
}

// allocReceive reads one message and fails the test if the connection ended.
func allocReceive(t *testing.T, conn *Conn) {
	t.Helper()
	message, err := conn.Receive()
	//: a connection that ended would make every later run allocate nothing for
	//: the wrong reason — the terminal check returns before the reader runs.
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	//: the message has to be observed, or the compiler is free to elide the
	//: buffer the whole claim is about.
	if len(message.Data) == 0 {
		t.Fatal("Receive returned an empty message")
	}
}

// allocSend writes one message and fails the test if the connection ended.
func allocSend(t *testing.T, conn *Conn, message corenet.WSMessageValue) {
	t.Helper()
	//: same reason as allocReceive: a terminated connection would report zero
	//: allocations because it never reached the write path.
	if err := conn.Send(message); err != nil {
		t.Fatalf("Send: %v", err)
	}
}
