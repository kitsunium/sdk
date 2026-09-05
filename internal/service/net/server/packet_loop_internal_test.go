// Package server — the datagram read loop.
package server

import (
	"context"
	"errors"
	stdnet "net"
	"slices"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// recordingHandler collects every datagram it is handed.
type recordingHandler struct {
	// mu guards the fields below, since the loop runs on its own goroutine.
	mu sync.Mutex
	// seen holds a copy of each payload, because the port's own Data is only
	// valid until ServePacket returns.
	seen []string
	// err is what the handler reports.
	err error
	// panics makes the handler panic instead of returning.
	panics bool
	// served is closed once the expected number of datagrams has arrived.
	served chan struct{}
	// want is how many datagrams close served.
	want int
}

// ServePacket implements corenet.PacketHandler.
func (h *recordingHandler) ServePacket(_ context.Context, p corenet.Packet) error {
	//: a panicking handler must not take the read loop down with it.
	if h.panics {
		panic("handler exploded")
	}
	h.mu.Lock()
	//: copy: the payload aliases the read buffer and is reused immediately.
	h.seen = append(h.seen, string(p.Data()))
	reached := len(h.seen) == h.want
	h.mu.Unlock()
	//: signal once, so a second datagram cannot close a closed channel.
	if reached && h.served != nil {
		close(h.served)
	}
	//: whatever this handler was configured to report.
	return h.err
}

// payloads returns a copy of what the handler has seen.
func (h *recordingHandler) payloads() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.seen)
}

// Test_Server_servePacket pins CONTAINMENT: a panicking handler must not take
// down the read loop, and with it every other peer of that socket.
//
// On the stream side a panic costs one connection. On a datagram socket there is
// only one loop for every peer, so the blast radius of the same mistake is the
// entire service — which is why the recover lives here rather than being left to
// the handler.
func Test_Server_servePacket(t *testing.T) {
	t.Parallel()
	failure := errors.New("malformed datagram")

	type tc struct {
		// name describes the case.
		name string
		// handlerErr is what the handler reports.
		handlerErr error
		// panics makes the handler panic instead of returning.
		panics bool
		// wantErr is whether the caller must be told the datagram failed.
		wantErr bool
	}
	tests := []tc{
		{name: "a handler that succeeds"},
		{name: "a handler that reports a failure", handlerErr: failure, wantErr: true},
		//: the panic is contained, so the loop sees a datagram that simply did
		//: not produce an error rather than a crash.
		{name: "a handler that panics", panics: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		handler := &recordingHandler{err: c.handlerErr, panics: c.panics}
		held := &packet{group: "sip", conn: &fakePacketConn{}}

		err := srv.servePacket(held, handler)

		//: reaching this line at all is the containment assertion.
		if (err != nil) != c.wantErr {
			t.Fatalf("servePacket = %v, want an error = %v", err, c.wantErr)
		}
		if c.wantErr && !errors.Is(err, c.handlerErr) {
			t.Fatalf("servePacket = %v, want %v", err, c.handlerErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_dispatch pins that an oversized datagram is DROPPED rather than
// handed on as a prefix.
//
// The kernel has already discarded the tail, so what remains reads exactly like
// a complete message: the handler has no way to tell it was cut, and a partial
// payload accepted as whole is a correctness bug rather than a capacity one. It
// is dropped and counted in State instead — there is nobody above a read loop to
// return an error to, so the counter IS the report.
func Test_Server_dispatch(t *testing.T) {
	t.Parallel()
	sender := &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 5060}

	type tc struct {
		// name describes the case.
		name string
		// ceiling is the group's datagram ceiling.
		ceiling int
		// sizes are the datagram lengths in the batch, in arrival order.
		sizes []int
		// wantServed is how many reach the handler.
		wantServed int
		// wantOversized is how many are dropped and counted.
		wantOversized int
	}
	tests := []tc{
		{name: "an empty batch", ceiling: 8, wantServed: 0},
		{name: "every datagram fits", ceiling: 8, sizes: []int{1, 4, 8}, wantServed: 3},
		{
			//: the probe byte was filled, so this one crossed the ceiling.
			name:    "one datagram past the ceiling",
			ceiling: 8, sizes: []int{9}, wantOversized: 1,
		},
		{
			//: a drop must not end the batch: the datagrams after it are from
			//: other peers and have done nothing wrong.
			name:    "an oversized datagram between two that fit",
			ceiling: 8, sizes: []int{4, 9, 4}, wantServed: 2, wantOversized: 1,
		},
		{name: "a datagram exactly at the ceiling", ceiling: 8, sizes: []int{8}, wantServed: 1},
		{name: "an empty datagram", ceiling: 8, sizes: []int{0}, wantServed: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := New()
		handler := &recordingHandler{}
		slots := newSlots(max(len(c.sizes), 1), c.ceiling)
		for i, size := range c.sizes {
			slots[i].n = size
			slots[i].addr = sender
		}
		held := &packet{conn: &fakePacketConn{}, group: "sip"}

		srv.dispatch(slots[:len(c.sizes)], held, handler, c.ceiling)

		state := srv.State()
		//: every datagram is counted, whether or not it was served — an operator
		//: reading a drop rate needs both halves.
		if state.TotalConns != uint64(len(c.sizes)) {
			t.Fatalf("TotalConns = %d, want %d", state.TotalConns, len(c.sizes))
		}
		if state.OversizedPackets != uint64(c.wantOversized) {
			t.Fatalf("OversizedPackets = %d, want %d — a truncated prefix served as "+
				"a whole message is invisible to the handler", state.OversizedPackets, c.wantOversized)
		}
		if got := len(handler.payloads()); got != c.wantServed {
			t.Fatalf("the handler saw %d datagrams, want %d", got, c.wantServed)
		}
		//: the references are dropped after the batch, so a slow next read
		//: cannot pin a read buffer through the reused value.
		if held.data != nil || held.from != nil {
			t.Errorf("the reused packet still pins %v from %v", held.data, held.from)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Server_readLoop pins the loop's two ends: it serves what arrives, and it
// STOPS when its socket closes.
//
// Goroutine lifecycle: one goroutine per case runs the loop under test. The case
// ends it by closing the socket and waits for the goroutine to publish its exit,
// so none outlives its case.
//
// Closing the socket is the only signal that reliably interrupts a blocking
// read, so a loop that treated the resulting error as transient would spin on a
// dead descriptor forever, holding the in-flight token the drain is waiting on.
// A transient failure, by contrast, must NOT end the loop: one malformed
// datagram cannot be allowed to stop the service for every other peer.
func Test_Server_readLoop(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// payloads are the datagrams sent to the socket.
		payloads []string
		// ceiling is the group's datagram ceiling.
		ceiling int
	}
	tests := []tc{
		{name: "one datagram", payloads: []string{"hello"}, ceiling: 64},
		{name: "several datagrams", payloads: []string{"a", "bb", "ccc"}, ceiling: 64},
		{name: "no traffic at all", ceiling: 64},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		receiver, sender := udpPair(t)
		srv := New()
		srv.runCtx = t.Context()
		handler := &recordingHandler{want: len(c.payloads), served: make(chan struct{})}
		bound := &boundPacketConn{group: "sip", pc: receiver}
		group := &PacketGroup{name: "sip", limits: corenet.LimitsValue{MaxPacketSize: c.ceiling}}

		stopped := make(chan struct{})
		srv.inFlight.Add(1)
		go func() {
			defer close(stopped)
			srv.readLoop(bound, group, handler)
		}()

		for _, payload := range c.payloads {
			if _, werr := sender.Write([]byte(payload)); werr != nil {
				t.Fatalf("send %q: %v", payload, werr)
			}
		}
		//: wait for the traffic rather than for a fixed delay.
		if len(c.payloads) > 0 {
			select {
			case <-handler.served:
			case <-time.After(serveDeadline):
				t.Fatalf("the loop served %v, want %v", handler.payloads(), c.payloads)
			}
		}

		//: closing the socket is the only signal that ends the loop.
		if cerr := receiver.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}

		select {
		case <-stopped:
		case <-time.After(serveDeadline):
			t.Fatal("the read loop is still running after its socket closed — it " +
				"would hold the in-flight token the drain waits on forever")
		}
		//: the loop releases its in-flight token on exit, so a Wait returns.
		waited := make(chan struct{})
		go func() {
			srv.inFlight.Wait()
			close(waited)
		}()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
			t.Fatal("the loop exited without releasing its in-flight token")
		}
		got := handler.payloads()
		if len(got) != len(c.payloads) {
			t.Fatalf("the loop served %v, want %v", got, c.payloads)
		}
		for i := range got {
			if got[i] != c.payloads[i] {
				t.Errorf("datagram %d was %q, want %q", i, got[i], c.payloads[i])
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
