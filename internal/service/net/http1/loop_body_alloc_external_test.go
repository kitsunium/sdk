//go:build !race

package http1_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// countingSink counts, and keeps nothing: an HTTP1BodyReader that owns its own
// buffers, as one under the 0-allocation budget does.
type countingSink struct{ chunks, served int }

func (c *countingSink) OnBody(_ *corenet.HTTP1RequestValue, _ []byte, _ bool) { c.chunks++ }

func (c *countingSink) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	c.served++
	res.Status = 200
}

// TestBodyReader_batchAllocatesNothing pins the budget a body reader keeps:
// once a connection has streamed a body, a batch of the next chunk and its
// answer allocates nothing, the head copy and the connection's request value
// being reused.
func TestBodyReader_batchAllocatesNothing(t *testing.T) {
	sink := &countingSink{}
	a := http1.HandleLoop(sink, http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	head := []byte(post("/up", 8))
	chunks := make([]byte, 8)
	batch := func() {
		a.OnData(io, ref, head)
		a.OnData(io, ref, chunks[:4])
		a.OnData(io, ref, chunks[4:])
		a.OnBatchEnd(io)
	}
	batch()
	if allocs := testing.AllocsPerRun(1000, batch); allocs != 0 {
		t.Fatalf("a body reader's batch: %v allocations, want 0", allocs)
	}
}
