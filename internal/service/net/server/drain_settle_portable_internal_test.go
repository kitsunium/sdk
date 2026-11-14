// Package server — the portable drain looks at the connections once
// OnBatchEnd has returned (#1385): a connection fed in every batch has every
// answer written before its close, hears OnDrain and closes within two looks.
package server

import (
	"bytes"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// portableAnswerH answers each request at OnBatchEnd, as the HTTP adapters
// do, and counts the answers refused and, as a LoopDrainer, OnDrain.
type portableAnswerH struct {
	portableLineH
	pending []corenet.LoopConnRef
	refused int
	drains  int
}

func (h *portableAnswerH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	h.pending = append(h.pending, ref)
	return h.portableLineH.OnData(io, ref, buf)
}

func (h *portableAnswerH) OnBatchEnd(io corenet.LoopIO) {
	for _, ref := range h.pending {
		if io.Write(ref, []byte("a\n")) != nil {
			h.refused++
		}
	}
	h.pending = h.pending[:0]
}

// portableAnswerDrainH is portableAnswerH as a LoopDrainer.
type portableAnswerDrainH struct{ portableAnswerH }

func (h *portableAnswerDrainH) OnDrain(corenet.LoopIO, corenet.LoopConnRef) { h.drains++ }

// TestPortableCloseSettled_judgesAfterTheBatchsAnswers runs the portable
// drain's batches on one connection fed in every batch, or once, its handler
// answering at OnBatchEnd: no answer is refused, every one is queued before
// the close is decided, and the close is decided within two looks — OnDrain
// at the first for a handler that drains, the close at the next; at the first
// for one that does not.
func TestPortableCloseSettled_judgesAfterTheBatchsAnswers(t *testing.T) {
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
		dh := &portableAnswerDrainH{}
		bh := &dh.portableAnswerH
		var h corenet.LoopHandler = bh
		if c.drainer {
			h = dh
		}
		pl := newOpenPortable(h, 1)
		pc := pl.conns[0]
		pl.g.drainReq.Store(true)
		fed, look := 0, 0
		for ; look < c.looks+2 && !pc.reasonSet; look++ {
			if c.fedEvery || look == 0 {
				pl.deliver(0, pc, []byte("q\n"))
				fed++
			}
			pl.batch()
		}
		if !pc.reasonSet || look > c.looks {
			t.Fatalf("after %d looks the close is decided: %v; want it within %d", look, pc.reasonSet, c.looks)
		}
		if bh.refused != 0 {
			t.Fatalf("%d answers refused", bh.refused)
		}
		pc.mu.Lock()
		got := bytes.Count(pc.out, []byte("a\n"))
		pc.mu.Unlock()
		if got != fed {
			t.Fatalf("%d answers queued for %d requests", got, fed)
		}
		if c.drainer && bh.drains != 1 {
			t.Fatalf("%d OnDrain, want one", bh.drains)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
