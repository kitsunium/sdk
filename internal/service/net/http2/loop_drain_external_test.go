package http2_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// postIO is a batchIO that keeps what is posted to it, for the test to run
// on the lane's thread at the moment it chooses.
type postIO struct {
	*batchIO
	ops  []corenet.PostFunc
	args []uintptr
}

func (p *postIO) Post(op corenet.PostFunc, arg uintptr) error {
	p.ops, p.args = append(p.ops, op), append(p.args, arg)
	return nil
}

// runPosts runs what was posted, in order, as the loop does inside a batch.
func (p *postIO) runPosts() {
	ops, args := p.ops, p.args
	p.ops, p.args = nil, nil
	for i, op := range ops {
		op(p, args[i])
	}
}

// TestLoopHandler_drainAnnouncedMidBatchKeepsTheBatchsAnswers pins #1385 on
// the adapter: the drain's GOAWAY, run on the lane's thread in the batch that
// fed a request, after it was served and before OnBatchEnd wrote its answer,
// leaves the connection to the batch's end, which writes the answer and then
// closes it. The announcement closed it at once — its stream, answered in the
// protocol's state, no longer in flight — and the answer was refused.
func TestLoopHandler_drainAnnouncedMidBatchKeepsTheBatchsAnswers(t *testing.T) {
	type tc struct {
		name string
		// fedThisBatch feeds the request in the announcing batch; otherwise
		// the request is answered a batch before, and the announcement closes
		// an idle connection at once.
		fedThisBatch bool
	}
	runCase := func(t *testing.T, c tc) {
		hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
		a := http2.HandleLoop(hello, http1.Parser{})
		a.OnStart(1)
		io := &postIO{batchIO: newBatchIO()}
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		a.OnOpen(io, ref)
		req := h2Request()
		if !c.fedThisBatch {
			if n := a.OnData(io, ref, req); n != len(req) {
				t.Fatalf("OnData consumed %d of %d", n, len(req))
			}
			a.OnBatchEnd(io)
		}
		a.Draining()
		// The batch: the request fed, then the posted announcement, then its end.
		if c.fedThisBatch {
			if n := a.OnData(io, ref, req); n != len(req) {
				t.Fatalf("OnData consumed %d of %d", n, len(req))
			}
		}
		io.runPosts()
		if c.fedThisBatch && io.closed[ref] {
			t.Fatal("the drain's announcement closed the connection before the batch wrote its answer")
		}
		a.OnBatchEnd(io)
		if !hasFrame(io.out[ref], corehttp2.FrameHeaders, 1) || !hasFrame(io.out[ref], corehttp2.FrameData, 1) {
			t.Fatalf("the answer on stream 1 was not written: % x", io.out[ref])
		}
		if !hasFrame(io.out[ref], corehttp2.FrameGoAway, 0) {
			t.Fatalf("no GOAWAY was written: % x", io.out[ref])
		}
		if !io.closed[ref] {
			t.Fatal("the drained connection, its stream answered, was left open")
		}
		if a.InFlight(0, ref) {
			t.Fatal("the drained connection is still in flight")
		}
	}
	for _, c := range []tc{{name: "fed in the announcing batch", fedThisBatch: true}, {name: "idle", fedThisBatch: false}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
