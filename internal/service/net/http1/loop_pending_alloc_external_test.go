//go:build !race

package http1_test

import (
	"bytes"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// churnBody is churnH's immediate answer, never built per request.
var churnBody = []byte("now")

// churnH answers "/pend" through its worker and anything else at once with
// churnBody, allocating nothing itself.
type churnH struct{}

func (churnH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	if bytes.Equal(req.Target, []byte("/pend")) {
		res.Pending, res.Job = true, 1
		return
	}
	res.Body = churnBody
}

func (churnH) WorkHTTP1(_ int, _ uint64, res *corenet.HTTP1ResponseValue) { res.Body = churnBody }

// TestHandleLoop_reopenKeepsBuffers pins a slot's churn: a connection that
// held answers behind a pending one closes, and the next connection of its
// slot reuses the held list and the buffer it left, so a lane whose buffers
// have grown allocates nothing per connection.
func TestHandleLoop_reopenKeepsBuffers(t *testing.T) {
	a, out := startPend(churnH{}, 1<<16)
	io := &fakeIO{loop: 0}
	req := []byte(get("/pend") + get("/x") + get("/y"))
	gen := uint32(0)
	cycle := func() {
		gen++
		ref := corenet.LoopConnRef{Ring: 0, Slot: 1, Gen: gen}
		a.OnOpen(io, ref)
		a.OnData(io, ref, req)
		a.OnBatchEnd(io)
		a.OnClose(io, ref, nil)
		finish(a, io, out)
	}
	cycle()
	if allocs := testing.AllocsPerRun(1000, cycle); allocs != 0 {
		t.Fatalf("a slot's churn: %v allocations a connection, want 0", allocs)
	}
}
