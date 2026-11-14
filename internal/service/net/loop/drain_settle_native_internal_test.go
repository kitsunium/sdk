//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package loop — the drain looks at the connections once OnBatchEnd has
// returned (#1385): a connection fed in every batch has every answer written
// before its close, hears OnDrain and closes within two looks.
package loop

import (
	"bytes"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// batchAnswerH answers each request at OnBatchEnd, as the HTTP adapters do,
// and, as a LoopDrainer when drainer is set, counts OnDrain.
type batchAnswerH struct {
	scriptH
	pending []corenet.LoopConnRef
	refused int
	drains  int
}

func (h *batchAnswerH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	h.pending = append(h.pending, ref)
	return h.scriptH.OnData(io, ref, buf)
}

func (h *batchAnswerH) OnBatchEnd(io corenet.LoopIO) {
	for _, ref := range h.pending {
		if io.Write(ref, []byte("a\n")) != nil {
			h.refused++
		}
	}
	h.pending = h.pending[:0]
}

// batchAnswerDrainH is batchAnswerH as a LoopDrainer.
type batchAnswerDrainH struct{ batchAnswerH }

func (h *batchAnswerDrainH) OnDrain(corenet.LoopIO, corenet.LoopConnRef) { h.drains++ }

// TestCloseSettled_judgesAfterTheBatchsAnswers runs the drain's batches on
// one connection fed in every batch, or once, its handler answering at
// OnBatchEnd: the look that follows OnBatchEnd never refuses an answer, every
// answer leaves before the close, and the connection closes within two looks
// — OnDrain at the first for a handler that drains, the close at the next;
// at the first for one that does not. Before, the look ran ahead of
// OnBatchEnd and either deferred a fed connection — for ever, fed in every
// batch — or, bounded to one deferral, marked it closing before its answer
// was written, which the write then refused.
func TestCloseSettled_judgesAfterTheBatchsAnswers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// drainer runs the handler as a LoopDrainer.
		drainer bool
		// fedEvery feeds the connection before every batch, not only the
		// first.
		fedEvery bool
		// looks is how many looks the connection may stay open through.
		looks int
	}
	cases := []tc{
		{name: "drainer/fed every batch", drainer: true, fedEvery: true, looks: 2},
		{name: "plain/fed every batch", fedEvery: true, looks: 1},
		{name: "drainer/fed once", drainer: true, looks: 2},
		{name: "plain/fed once", looks: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		dh := &batchAnswerDrainH{}
		bh := &dh.batchAnswerH
		var h corenet.LoopHandler = bh
		if c.drainer {
			h = dh
		}
		lp, r, d := newTestLoop(h, corenet.LoopValue{}, 0)
		s := accept(t, r)
		lp.draining = true
		fed, look := 0, 0
		for ; look < c.looks+2 && r.conns[s].state == stOpen; look++ {
			if c.fedEvery || look == 0 {
				r.deliver(s, []byte("q\n"))
				fed++
			}
			h.OnBatchEnd(lp)
			lp.settleDrain()
			lp.endBatch()
		}
		if r.conns[s].state == stOpen || look > c.looks {
			t.Fatalf("after %d looks the connection is in state %d, want it closed within %d", look, r.conns[s].state, c.looks)
		}
		if bh.refused != 0 {
			t.Fatalf("%d answers refused", bh.refused)
		}
		if got := bytes.Count(d.sent[s].Bytes(), []byte("a\n")); got != fed {
			t.Fatalf("%d answers sent for %d requests", got, fed)
		}
		if len(d.closed) != 1 || d.closed[0] != s {
			t.Fatalf("closed %v, want the connection's shutdown once its answers left", d.closed)
		}
		if c.drainer && bh.drains != 1 {
			t.Fatalf("%d OnDrain, want one", bh.drains)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
