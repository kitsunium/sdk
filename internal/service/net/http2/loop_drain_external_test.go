package http2_test

import (
	"slices"
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

// frameTypes is the type of each frame of out, in order.
func frameTypes(out []byte) []corehttp2.FrameType {
	var types []corehttp2.FrameType
	for len(out) >= 9 {
		n := int(out[0])<<16 | int(out[1])<<8 | int(out[2])
		types = append(types, corehttp2.FrameType(out[3]))
		if len(out) < 9+n {
			break
		}
		out = out[9+n:]
	}
	return types
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
		// The server's SETTINGS is the connection's first frame (RFC 9113
		// §3.4), and the GOAWAY follows the batch's answers.
		order := frameTypes(io.out[ref])
		if len(order) == 0 || order[0] != corehttp2.FrameSettings {
			t.Fatalf("the connection's frames %v, want SETTINGS first", order)
		}
		if slices.Index(order, corehttp2.FrameGoAway) < slices.Index(order, corehttp2.FrameData) {
			t.Fatalf("the connection's frames %v, want the GOAWAY behind the answer", order)
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

// workerH answers every request pending, its answer filled by the lane's
// worker.
type workerH struct{}

func (workerH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Pending, res.Job = true, 7
}

func (workerH) WorkHTTP1(_ int, _ uint64, res *corenet.HTTP1ResponseValue) {
	res.Body = []byte("ok")
}

// sentJobs is a lane's outbound that keeps the jobs sent to it.
type sentJobs struct{ jobs []corenet.LoopJobValue }

func (o *sentJobs) Send(_ corenet.LoopIO, job corenet.LoopJobValue) error {
	o.jobs = append(o.jobs, job)
	return nil
}

// TestLoopHandler_aWorkersAnswerInTheDrainingBatchFollowsItsOutput pins
// #1385 on a worker's answer: in the batch that fed a request and queued the
// drain's GOAWAY behind the connection's SETTINGS, the worker's answer comes
// back. Written and closed at once, it went out ahead of the SETTINGS and the
// GOAWAY, and its close refused them at the batch's end; it now joins the
// batch's output, and the batch's end writes it in order — SETTINGS first —,
// then closes.
func TestLoopHandler_aWorkersAnswerInTheDrainingBatchFollowsItsOutput(t *testing.T) {
	type tc struct {
		name string
		// draining announces the drain in the batch.
		draining bool
	}
	runCase := func(t *testing.T, c tc) {
		a := http2.HandleLoop(workerH{}, http1.Parser{})
		a.OnStart(1)
		out := &sentJobs{}
		a.OnOutbound(corenet.LoopOutboundValue{Lanes: []corenet.LoopOutbound{out}, OutputCap: 1 << 20})
		io := &postIO{batchIO: newBatchIO()}
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		a.OnOpen(io, ref)
		req := h2Request()
		if c.draining {
			a.Draining()
		}
		// The batch: the request fed and offloaded, the drain's announcement,
		// the worker's answer back, then the batch's end.
		if n := a.OnData(io, ref, req); n != len(req) {
			t.Fatalf("OnData consumed %d of %d", n, len(req))
		}
		io.runPosts()
		if len(out.jobs) != 1 {
			t.Fatalf("%d jobs sent to the worker, want 1", len(out.jobs))
		}
		a.Worker().Work(0, out.jobs[0])
		a.OnDone(io, out.jobs[0])
		a.OnBatchEnd(io)
		order := frameTypes(io.out[ref])
		if len(order) == 0 || order[0] != corehttp2.FrameSettings {
			t.Fatalf("the connection's frames %v, want SETTINGS first", order)
		}
		if !hasFrame(io.out[ref], corehttp2.FrameHeaders, 1) || !hasFrame(io.out[ref], corehttp2.FrameData, 1) {
			t.Fatalf("the worker's answer on stream 1 was not written: %v", order)
		}
		if !c.draining {
			return
		}
		// The GOAWAY, announced before the answer came back, names stream 1
		// as taken: the answer may follow it.
		if !hasFrame(io.out[ref], corehttp2.FrameGoAway, 0) {
			t.Fatalf("the connection's frames %v, want a GOAWAY", order)
		}
		if !io.closed[ref] {
			t.Fatal("the drained connection, its stream answered, was left open")
		}
	}
	for _, c := range []tc{{name: "draining", draining: true}, {name: "serving"}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
