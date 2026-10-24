package server_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/net/server"
)

// echoLoop is a loop handler written against the public package alone: it
// echoes what it receives and honours IdleTimeout itself. It is what ADR 0186
// §D9 step 6 publishes the ports for, so a consumer can implement them before
// any group can serve one.
type echoLoop struct {
	// closed counts the connections closed, for the test to read.
	closed int
}

// Compile-time proof that the public aliases are the ports a loop serves.
var (
	_ server.LoopHandler        = (*echoLoop)(nil)
	_ server.LoopOptionHonourer = (*echoLoop)(nil)
)

// OnOpen accepts the connection; an echo needs no per-connection state.
func (*echoLoop) OnOpen(server.LoopIO, server.LoopConnRef) {}

// OnData writes back everything it was handed and consumes all of it.
func (*echoLoop) OnData(io server.LoopIO, ref server.LoopConnRef, buf []byte) int {
	//: a refused write closes rather than drop bytes the peer expects back.
	if err := io.Write(ref, buf); err != nil {
		//: the stale or full connection has nothing left to answer.
		_ = io.Close(ref)
	}
	//: everything handed over was echoed or abandoned with the connection.
	return len(buf)
}

// OnTimeout closes a connection whose deadline passed.
func (*echoLoop) OnTimeout(io server.LoopIO, ref server.LoopConnRef) {
	//: a stale reference is the only error, and it means already closed.
	_ = io.Close(ref)
}

// OnWritable has nothing to resume: OnData never pauses.
func (*echoLoop) OnWritable(server.LoopIO, server.LoopConnRef) {}

// OnBatchEnd has nothing batched to flush.
func (*echoLoop) OnBatchEnd(server.LoopIO) {}

// OnClose counts the closed connection.
func (h *echoLoop) OnClose(server.LoopIO, server.LoopConnRef, error) {
	//: counted on the loop's thread, the only one that calls it.
	h.closed++
}

// Honours claims IdleTimeout and nothing else.
func (*echoLoop) Honours(k server.OptionKind) bool {
	//: only the one option this handler applies itself.
	return k == server.OptionIdleTimeout
}

// fakeIO is a LoopIO with no engine behind it, which refuses every write as a
// closed connection would.
type fakeIO struct {
	// closes counts the Close calls.
	closes int
}

// Write refuses as on a closed connection.
func (*fakeIO) Write(server.LoopConnRef, []byte) error { return server.LoopStaleRef }

// Close records the call.
func (f *fakeIO) Close(server.LoopConnRef) error {
	//: counted so the test sees OnData's reaction to the refusal.
	f.closes++
	return nil
}

// SetDeadline accepts any deadline.
func (*fakeIO) SetDeadline(server.LoopConnRef, time.Duration) error { return nil }

// Pressure reports no pressure.
func (*fakeIO) Pressure(server.LoopConnRef) bool { return false }

// Post refuses, as a full queue would.
func (*fakeIO) Post(server.PostFunc, uintptr) error { return server.LoopPostQueueFull }

// TestLoopPortsArePublic pins ADR 0186 §D9 step 6: a consumer implements
// LoopHandler and LoopIO with the public package alone, OptionKind renders the
// constructor's name, and the loop sentinels match through the facade.
func TestLoopPortsArePublic(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// kind is the option asked about.
		kind server.OptionKind
		// honoured is what echoLoop answers for it.
		honoured bool
		// spelled is the kind's String.
		spelled string
	}
	tests := []tc{
		{name: "the option it applies", kind: server.OptionIdleTimeout, honoured: true, spelled: "IdleTimeout"},
		{name: "an option it does not", kind: server.OptionTLS, honoured: false, spelled: "TLS"},
		{name: "the zero kind", kind: server.OptionNone, honoured: false, spelled: "none"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := &echoLoop{}
		//: the capability answers per kind.
		if got := h.Honours(c.kind); got != c.honoured {
			t.Fatalf("Honours(%v) = %v, want %v", c.kind, got, c.honoured)
		}
		//: the kind is spelled as its constructor.
		if got := c.kind.String(); got != c.spelled {
			t.Fatalf("String() = %q, want %q", got, c.spelled)
		}
		io := &fakeIO{}
		ref := server.LoopConnRef{Ring: 0, Slot: 1, Gen: 1}
		//: a refused write is consumed and closes the connection.
		if n := h.OnData(io, ref, []byte("ping")); n != len("ping") || io.closes != 1 {
			t.Fatalf("OnData = %d with %d closes, want 4 with 1", n, io.closes)
		}
		h.OnClose(io, ref, nil)
		//: OnClose ran once.
		if h.closed != 1 {
			t.Fatalf("closed = %d, want 1", h.closed)
		}
		//: the sentinels a LoopIO returns match through the facade.
		if err := io.Post(nil, 0); !errors.Is(err, server.LoopPostQueueFull) {
			t.Fatalf("errors.Is(Post, LoopPostQueueFull) = false, got %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
