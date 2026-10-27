//go:build !race

// Package server — the cost of the lane's port to its worker (R3): Send
// allocates nothing once the lane's LoopIO is kept, which a parameter moved
// to the heap would cost on every call.
package server

import (
	"sync/atomic"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestLoopOutbound_sendAllocs pins Send at 0 allocations a call: the lane
// keeps a copy of its first LoopIO, so io itself never escapes.
func TestLoopOutbound_sendAllocs(t *testing.T) {
	var refused atomic.Uint64
	l := &outboundLane{jobs: newJobRing(4), done: newJobRing(4), wake: make(chan struct{}, 1), refused: &refused}
	var io corenet.LoopIO = probeIO{}
	if err := l.Send(io, corenet.LoopJobValue{}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	l.jobs.pop()
	allocs := testing.AllocsPerRun(1000, func() {
		if err := l.Send(io, corenet.LoopJobValue{Arg: 1}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		l.jobs.pop()
	})
	if allocs != 0 {
		t.Fatalf("Send: %v allocations a call, want 0", allocs)
	}
}
