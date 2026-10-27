package http1_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// fakeOutbound is a lane's LoopOutbound with no worker behind it: it keeps
// every job sent, for the test to run and hand back when it chooses.
type fakeOutbound struct {
	// jobs are the jobs sent, in order.
	jobs []corenet.LoopJobValue
}

func (o *fakeOutbound) Send(_ corenet.LoopIO, job corenet.LoopJobValue) error {
	o.jobs = append(o.jobs, job)
	return nil
}

// pendH answers "/pend" through its worker, defers "/defer…" to EndBatch,
// answers "/big" at once with a body of size bytes, and anything else at once
// with its target. It records every response value it was handed while the
// batch still holds it: one handed twice is shared by two requests.
type pendH struct {
	// size is the length of "/big"'s body.
	size int
	// next numbers the jobs.
	next uint64
	// held are the deferred responses EndBatch fills, with their target.
	held    []*corenet.HTTP1ResponseValue
	targets []string
	// shared are the targets served with a response value the batch held.
	shared []string
}

func (h *pendH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	target := string(req.Target)
	if slices.Contains(h.held, res) {
		h.shared = append(h.shared, target)
	}
	switch {
	case target == "/pend":
		h.next++
		res.Pending, res.Job = true, h.next
	case strings.HasPrefix(target, "/defer"):
		res.Defer = true
		h.held = append(h.held, res)
		h.targets = append(h.targets, target)
	case target == "/big":
		res.Body = []byte(strings.Repeat("b", h.size))
	default:
		res.Body = []byte("now:" + target)
	}
}

func (h *pendH) EndBatch(int) {
	for i, res := range h.held {
		res.Status, res.Defer, res.Body = 201, false, []byte("later:"+h.targets[i])
	}
	h.held, h.targets = h.held[:0], h.targets[:0]
}

func (*pendH) WorkHTTP1(_ int, job uint64, res *corenet.HTTP1ResponseValue) {
	res.Body = []byte("work:" + strconv.FormatUint(job, 10))
}

// startPend builds an adapter for h on one lane, its outbound the fake, its
// holdback ceiling holdCap.
func startPend(h corenet.HTTP1Handler, holdCap int) (*http1.LoopHandler, *fakeOutbound) {
	a := http1.HandleLoop(h, http1.Parser{})
	a.OnStart(1)
	out := &fakeOutbound{}
	a.OnOutbound(corenet.LoopOutboundValue{Lanes: []corenet.LoopOutbound{out}, OutputCap: holdCap})
	return a, out
}

// finish runs every job sent so far through the adapter's worker and hands
// it back, as the lane's thread would.
func finish(a *http1.LoopHandler, io corenet.LoopIO, out *fakeOutbound) {
	w := a.Worker()
	for _, job := range out.jobs {
		w.Work(0, job)
		a.OnDone(io, job)
	}
	out.jobs = out.jobs[:0]
}

// TestHandleLoop_pendingPipelineKeepsBuffer pins the holdback ceiling under
// an open pipeline: each batch pipelines a pending request and an immediate
// one behind it, so a pending answer is always at the tail of what the
// connection holds. The answers already written must leave the count: only
// what is still held is bounded, and the connection stays open with every
// answer in order.
func TestHandleLoop_pendingPipelineKeepsBuffer(t *testing.T) {
	h := &pendH{}
	a, out := startPend(h, 512)
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Ring: 0, Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	const rounds = 40
	var want []string
	for i := range rounds {
		finish(a, io, out)
		req := get("/pend") + get("/i"+strconv.Itoa(i))
		if n := a.OnData(io, ref, []byte(req)); n != len(req) {
			t.Fatalf("round %d: OnData took %d of %d", i, n, len(req))
		}
		a.OnBatchEnd(io)
		if io.closed[ref] {
			t.Fatalf("round %d: the connection was closed with %d bytes written", i, len(io.out[ref]))
		}
		want = append(want, "200 OK work:"+strconv.Itoa(i+1), "200 OK now:/i"+strconv.Itoa(i))
	}
	finish(a, io, out)
	a.OnBatchEnd(io)
	if got := answers(t, io.out[ref], "GET"); !slices.Equal(got, want) {
		t.Fatalf("answers:\n got %q\nwant %q", got, want)
	}
}

// TestHandleLoop_droppedDeferNotShared pins a deferred answer held behind a
// pending one on a connection that closes during the batch — by passing the
// ceiling, or by OnClose — while the batcher still holds its response value:
// a request of another connection later in the same batch is never handed
// that value, and its answer is its own.
func TestHandleLoop_droppedDeferNotShared(t *testing.T) {
	for _, tc := range []struct {
		name string
		// close ends connection A after its deferred request.
		close func(a *http1.LoopHandler, io *fakeIO, ref corenet.LoopConnRef) string
	}{
		{"ceiling", func(*http1.LoopHandler, *fakeIO, corenet.LoopConnRef) string { return get("/big") }},
		{"OnClose", func(a *http1.LoopHandler, io *fakeIO, ref corenet.LoopConnRef) string {
			io.closed[ref] = true
			a.OnClose(io, ref, nil)
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &pendH{size: 1024}
			a, _ := startPend(h, 256)
			io := newFakeIO(0)
			refA := corenet.LoopConnRef{Ring: 0, Slot: 1, Gen: 1}
			refD := corenet.LoopConnRef{Ring: 0, Slot: 2, Gen: 1}
			a.OnOpen(io, refA)
			a.OnOpen(io, refD)
			reqA := get("/pend") + get("/deferB")
			a.OnData(io, refA, []byte(reqA))
			if more := tc.close(a, io, refA); more != "" {
				a.OnData(io, refA, []byte(more))
			}
			if !io.closed[refA] {
				t.Fatal("connection A is still open")
			}
			a.OnData(io, refD, []byte(get("/deferD")+get("/now")))
			a.OnBatchEnd(io)
			if len(h.shared) != 0 {
				t.Fatalf("served with a response value the batch still held: %q", h.shared)
			}
			want := []string{"201 Created later:/deferD", "200 OK now:/now"}
			if got := answers(t, io.out[refD], "GET"); !slices.Equal(got, want) {
				t.Fatalf("D's answers:\n got %q\nwant %q", got, want)
			}
		})
	}
}
