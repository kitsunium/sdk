package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cutTransport is a memory transport whose first snapshot stream is cut
// short, by a cancelled context or by a body that ends early, and whose
// verdict it keeps. Every stream after the first goes through whole.
type cutTransport struct {
	*MemoryTransport
	cancel  bool
	started atomic.Int32
	mu      sync.Mutex
	verdict error
}

func (t *cutTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	//: only the first stream is cut: the learner must still catch up.
	if t.started.Add(1) != 1 {
		return t.MemoryTransport.Stream(ctx, to, offer, open)
	}
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	err := t.MemoryTransport.Stream(sctx, to, offer, func(off int64) (io.ReadCloser, error) {
		rc, err := open(off)
		if err != nil {
			return rc, err
		}
		//: a cancelled stream reads its context's error, as the node's paced reader does.
		if t.cancel {
			return &cancelBody{ReadCloser: rc, stop: stop, ctx: sctx}, nil
		}
		//: a truncated stream ends in a clean EOF, with nothing cancelled.
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(rc, 1), rc}, nil
	})
	t.mu.Lock()
	t.verdict = err
	t.mu.Unlock()
	return err
}

// cancelBody cancels its stream on the first read and fails it with the
// context's error.
type cancelBody struct {
	io.ReadCloser
	stop context.CancelFunc
	ctx  context.Context
}

func (b *cancelBody) Read([]byte) (int, error) {
	b.stop()
	return 0, b.ctx.Err()
}

// openCounter counts the snapshots a store opened.
type openCounter struct {
	coreconsensus.SnapshotStore
	opens atomic.Int32
}

func (s *openCounter) Open(id string) (coreconsensus.SnapshotHandle, error) {
	s.opens.Add(1)
	return s.SnapshotStore.Open(id)
}

// #1484: a snapshot stream cancelled on the memory transport ended as
// SNAPSHOT_CORRUPT, as a receiver's refusal does, and the sender read its
// whole snapshot back and logged that a peer found a sound one corrupt.
// A cancellation ends the stream as it does on TCP, with no verdict read;
// a stream cut short with nothing cancelled stays SNAPSHOT_CORRUPT.
func TestACancelledSnapshotStreamIsNotACorruptVerdict(t *testing.T) {
	type tc struct {
		name    string
		cancel  bool
		corrupt bool
	}
	runCase := func(t *testing.T, c tc) {
		trs := map[coreconsensus.NodeID]*cutTransport{}
		snaps := map[coreconsensus.NodeID]*openCounter{}
		logs := map[coreconsensus.NodeID]*attrLogger{}
		var mu sync.Mutex
		cl := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 4, 8}, every: 20, size: 3}
		cl.configure = func(tn *testNode, cfg *Config) {
			mu.Lock()
			defer mu.Unlock()
			ct := &cutTransport{MemoryTransport: cfg.Transport.(*MemoryTransport), cancel: c.cancel}
			oc := &openCounter{SnapshotStore: cfg.Snapshots}
			lg := &attrLogger{}
			trs[tn.id], snaps[tn.id], logs[tn.id] = ct, oc, lg
			cfg.Transport, cfg.Snapshots, cfg.Logger = ct, oc, lg
			cfg.InitialTiming = steadyTiming()
			cfg.InitialTiming.ElectionTimeoutMin, cfg.InitialTiming.ElectionTimeoutMax = 2*time.Second, 4*time.Second
		}
		newClusterWith(t, cl)
		l := cl.settledLeader(20 * time.Second)
		for i := range 200 {
			propose(t, l.node, fmt.Sprintf("a%d=v", i))
		}
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			//: the leader must have compacted, or the learner is fed entries and no snapshot.
			if first, _ := l.log.Bounds(); first > 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("setup: the leader never compacted its log")
			}
		}
		cutsSettled(t, l)
		mu.Lock()
		ct, oc, lg := trs[l.id], snaps[l.id], logs[l.id]
		mu.Unlock()
		a := cl.join(t, "slot-a")
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			//: the learner catches up on a second stream, after the cut one settled.
			if v, _ := a.sm.get("a199"); v == "v" && ct.started.Load() >= 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the learner never caught up: %+v, %d streams", a.node.Status(), ct.started.Load())
			}
		}
		ct.mu.Lock()
		verdict := ct.verdict
		ct.mu.Unlock()
		lg.mu.Lock()
		var found bool
		for _, line := range lg.lines {
			//: the journal line a corrupt verdict writes once the sender's copy verified.
			if line["msg"] == "consensus: a peer found a sound snapshot corrupt" {
				found = true
			}
		}
		lg.mu.Unlock()
		reads := int(oc.opens.Load() - ct.started.Load())
		if got := errs.HasCode(verdict, coreconsensus.CodeSnapshotCorrupt); got != c.corrupt {
			t.Fatalf("the first stream ended with %v: SNAPSHOT_CORRUPT %v, want %v", verdict, got, c.corrupt)
		}
		//: a cancelled stream must end as the context's cancellation, not as another refusal.
		if c.cancel && !errors.Is(verdict, context.Canceled) {
			t.Fatalf("the cancelled stream ended with %v, want context.Canceled", verdict)
		}
		if found != c.corrupt {
			t.Fatalf("the journal's \"found a sound snapshot corrupt\" is %v, want %v", found, c.corrupt)
		}
		if c.corrupt && reads < 1 {
			t.Fatalf("a corrupt verdict left the sender's snapshot unread: %d opens for %d streams", oc.opens.Load(), ct.started.Load())
		}
		if !c.corrupt && reads != 0 {
			t.Fatalf("the sender read its snapshot back after a cancelled stream: %d opens for %d streams", oc.opens.Load(), ct.started.Load())
		}
	}
	for _, c := range []tc{
		{name: "a cancelled stream ends as cancelled", cancel: true},
		{name: "a truncated stream stays corrupt", corrupt: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
