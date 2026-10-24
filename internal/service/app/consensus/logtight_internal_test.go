package consensus

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// extraLog is quotaLog with extra bytes on top of its entries', so a test
// brings a node's log into the tight band at once, short of the alarm.
type extraLog struct {
	*MemoryLog
	extra *atomic.Int64
}

func (l extraLog) logBytes() int64 {
	return quotaLog{MemoryLog: l.MemoryLog}.logBytes() + l.extra.Load()
}

func (extraLog) freeBytes() int64 { return -1 }

// cancelWatch is a memory transport that counts the snapshot streams whose
// context ended while they ran.
type cancelWatch struct {
	*MemoryTransport
	cancelled atomic.Int32
}

func (t *cancelWatch) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	stop := context.AfterFunc(ctx, func() { t.cancelled.Add(1) })
	defer stop()
	return t.MemoryTransport.Stream(ctx, to, offer, open)
}

// holdTCP is a TCP transport whose snapshot streams wait, while holding is
// on, until the test releases them, as holdTransport's over memory.
type holdTCP struct {
	*TCPTransport
	holding   atomic.Bool
	started   chan coreconsensus.InstallOfferValue
	release   chan struct{}
	cancelled atomic.Int32
}

func (t *holdTCP) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	if t.holding.Load() {
		select {
		case t.started <- offer:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-t.release:
		case <-ctx.Done():
			t.cancelled.Add(1)
			return ctx.Err()
		}
	}
	return t.TCPTransport.Stream(ctx, to, offer, open)
}

// #550's scenario (TestATransferInFlightPinsCompaction) under a quota of
// at most twice the reserve, which refuseCeilings accepts — 512 MiB under
// the default 256 MiB reserve. #616's tight band, within twice
// LogReserveBytes of MaxLogBytes, covered the whole of such a quota: no pin
// ever held, the stream in flight was cancelled at the first tick, and the
// leader compacted past the snapshot it was sending, as before #550
// (#622). The band now starts half way to the alarm at the earliest, and a
// transfer under a small log holds compaction as under a large quota.
func TestAPinHoldsCompactionUnderAQuotaOfTwoReservesOrLess(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 6}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		ht := &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		if old := trs[tn.id]; old != nil {
			ht.started, ht.release = old.started, old.release
			ht.holding.Store(old.holding.Load())
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		cfg.InitialTiming = steadyTiming()
		// 1 KiB an entry: the alarm rises at 424 entries held, the band
		// at 212, and #616's band at the first byte.
		cfg.Log = quotaLog{MemoryLog: tn.log}
		cfg.MaxLogBytes, cfg.LogReserveBytes = 1<<20, 600<<10
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	hearOnlyTheLeader(c, l, lag)
	c.start(lag)
	var offered coreconsensus.InstallOfferValue
	select {
	case offered = <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: no snapshot offered to the lagging node")
	}
	// Two cuts' worth of writes while the transfer is held.
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("r0-%d=v", i))
	}
	first, _ := l.log.Bounds()
	releaseHeld(t, lt, l)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, _ := lag.sm.get("r0-44"); v == "v" {
			lt.holding.Store(false)
			return
		}
		select {
		case <-lt.started:
			lt.holding.Store(false)
			t.Fatalf("under a 1 MiB quota and a 600 KiB reserve, the leader compacted past the snapshot in flight (its log starts at %d, the snapshot is at %d) and the lagging node needed another snapshot: the #550 defect",
				first, offered.Meta.Position.Index)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lagging node never caught up after the first transfer: %+v", lag.node.Status())
		}
	}
}

// A tight log lifts the hold of a stream's pin on compaction, and only
// that: #616 cancelled the stream with it, so while the log stayed in the
// band no snapshot stream outlived a tick, and under a quota of at most
// twice the reserve no follower behind the compacted log was ever installed
// (#622). Over TCP, where a cancelled stream ends at once: the leader holds
// its stream to the restarted follower while its writes bring its log into
// the band; it then compacts past the snapshot in flight, the stream goes
// on, and the follower catches up once it is released.
func TestAStreamSurvivesATightLogOverTCP(t *testing.T) {
	var mu sync.Mutex
	holds := map[*TCPTransport]*holdTCP{}
	tweak := func(c *Config) {
		c.SnapshotEvery, c.SnapshotTrailing = 20, 4
		c.SnapshotEveryBytes = 1 << 20
		if ml, ok := c.Log.(*MemoryLog); ok {
			c.Log = quotaLog{MemoryLog: ml}
		}
		// 1 KiB an entry: the band from 212 entries on, the alarm at 424;
		// #616's band at the first byte.
		c.MaxLogBytes, c.LogReserveBytes = 1<<20, 600<<10
		c.InitialTiming = steadyTiming()
		mu.Lock()
		defer mu.Unlock()
		tr := c.Transport.(*TCPTransport)
		if holds[tr] == nil {
			holds[tr] = &holdTCP{TCPTransport: tr, started: make(chan coreconsensus.InstallOfferValue), release: make(chan struct{})}
		}
		c.Transport = holds[tr]
	}
	ca, cid, ns := newTCPClusterWith(t, tweak)
	proposeOnTCPLeader(t, ns, "settle=1")
	l := tcpLeader(t, ns)
	var f *tcpNode
	for _, tn := range ns {
		if tn != l {
			f = tn
			break
		}
	}
	f.stop(t)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the stopped follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	mu.Lock()
	h := holds[l.tr]
	mu.Unlock()
	h.holding.Store(true)
	f.listen(t, ca)
	f.start(t, cid)
	var offered coreconsensus.InstallOfferValue
	select {
	case offered = <-h.started:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: no snapshot offered to the restarted follower")
	}
	held := offered.Meta.Position.Index
	// The pin holds until the log reaches the band, 212 entries on, and the
	// leader compacts at its next tick or cut: the writes go past it.
	var most uint64
	for i := range 300 {
		propose(t, l.node, fmt.Sprintf("b%03d=v", i))
		first, last := l.log.Bounds()
		most = max(most, last-first+1)
	}
	if n := h.cancelled.Load(); n != 0 {
		t.Fatalf("a tight log cancelled %d held snapshot streams: it should lift their hold on compaction only", n)
	}
	if most < 200 {
		t.Fatalf("the stream's pin did not hold compaction below the band: the leader's log held at most %d entries, the band starts at 212", most)
	}
	waitFor(t, "the leader compacted past the snapshot in flight once its log was tight", func() bool {
		first, _ := l.log.Bounds()
		return first > held
	})
	if n := h.cancelled.Load(); n != 0 {
		t.Fatalf("a tight log cancelled %d held snapshot streams: it should lift their hold on compaction only", n)
	}
	h.holding.Store(false)
	select {
	case h.release <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatalf("the held snapshot stream no longer waits for its release: %d cancelled, the leader's status %+v", h.cancelled.Load(), l.node.Status())
	}
	proposeOnTCPLeader(t, ns, "last=1")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if v, _ := f.sm.get("last"); v == "1" {
			break
		}
		if time.Now().After(deadline) {
			lf, ll := l.log.Bounds()
			t.Fatalf("the restarted follower never caught up: applied %d, leader commit %d, leader log [%d,%d], held streams cancelled %d",
				f.node.Status().Applied, l.node.Status().Commit, lf, ll, h.cancelled.Load())
		}
	}
	var scale uint8
	l.node.exec(func() { scale = l.node.pinScale[f.id] })
	if scale != 0 {
		t.Errorf("a pin lifted by a tight log grew its peer's bound %d times: it says nothing of the peer", scale)
	}
}

// The #602 bound still ends a stream whose receiver never answers its
// verdict while the log is tight: the band lifts the pin's hold on
// compaction, and the stream is cancelled at its own bound, never before.
// The follower's apply goroutine hangs at the restore of the snapshot it is
// sent; the leader's log is brought into the band once the last byte was
// read. The leader keeps its leadership throughout: one that stepped down
// cancelled the stream for that reason, and testTimingFast's 100 ms
// CheckQuorum window stepped it down on a loaded macOS runner.
func TestTheVerdictBoundStillEndsAStreamOnATightLog(t *testing.T) {
	timing := testTimingFast()
	timing.ElectionTimeoutMin, timing.ElectionTimeoutMax = 400*time.Millisecond, 450*time.Millisecond
	var mu sync.Mutex
	extras := map[coreconsensus.NodeID]*atomic.Int64{}
	watches := map[coreconsensus.NodeID]*cancelWatch{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 7}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		if extras[tn.id] == nil {
			extras[tn.id] = new(atomic.Int64)
		}
		cfg.Log = extraLog{MemoryLog: tn.log, extra: extras[tn.id]}
		cfg.MaxLogBytes, cfg.LogReserveBytes = 1<<20, 600<<10
		watches[tn.id] = &cancelWatch{MemoryTransport: cfg.Transport.(*MemoryTransport)}
		cfg.Transport = watches[tn.id]
		cfg.InitialTiming = timing
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the isolated follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	f.sm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			f.sm.mu.Unlock()
		}
	}
	defer release()
	c.net.Isolate(f.id, false)
	var sentAt int64
	waitFor(t, "a snapshot streamed to the follower", func() bool {
		l.node.exec(func() {
			if pin, ok := l.node.sending[f.id]; ok && pin.until.IsZero() && pin.sent != nil {
				sentAt = pin.sent.Load()
			}
		})
		return sentAt != 0
	})
	mu.Lock()
	extra, w := extras[l.id], watches[l.id]
	mu.Unlock()
	// About 300 KiB in all: in the band, 212 KiB on, short of the alarm.
	extra.Store(300 << 10)
	defer extra.Store(0)
	tight := false
	l.node.exec(func() { tight = l.node.logTight() })
	if !tight {
		t.Fatal("setup: the leader's log is not tight")
	}
	bound := time.Duration(pinAfterStream) * timing.ElectionTimeoutMax
	term := l.node.Status().Term
	time.Sleep(100 * time.Millisecond)
	if time.Since(time.Unix(0, sentAt)) < bound-200*time.Millisecond {
		if n := w.cancelled.Load(); n != 0 {
			if st := l.node.Status(); st.Role != coreconsensus.RoleLeader || st.Term != term {
				t.Fatalf("setup: the leader lost its leadership during the measure (term %d -> %d, role %v), which cancels its streams", term, st.Term, st.Role)
			}
			t.Fatalf("a tight log cancelled the stream %v after its last byte, before its %v bound", time.Since(time.Unix(0, sentAt)), bound)
		}
	} else {
		t.Logf("the setup outlasted the bound: the stream's survival of the band is not checked")
	}
	for deadline := time.Unix(0, sentAt).Add(bound + 5*time.Second); w.cancelled.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("a stream whose receiver never answers its verdict was not cancelled %v after its last byte, its bound %v, with the log tight", time.Since(time.Unix(0, sentAt)), bound)
		}
	}
	// A leader that stepped down cancelled the stream for that reason, not
	// its bound: that is no pass.
	if st := l.node.Status(); st.Role != coreconsensus.RoleLeader || st.Term != term {
		t.Fatalf("setup: the leader lost its leadership before the stream was cancelled (term %d -> %d, role %v), which cancels its streams", term, st.Term, st.Role)
	}
	if since := time.Since(time.Unix(0, sentAt)); since < bound {
		t.Fatalf("the stream was cancelled %v after its last byte, before its %v bound, with the log tight", since, bound)
	}
	var scale uint8
	l.node.exec(func() { scale = l.node.pinScale[f.id] })
	if scale != 0 {
		t.Errorf("a pin that fell due with the log tight grew its peer's bound %d times: it held nothing back", scale)
	}
	release()
	extra.Store(0)
	c.waitApplied(l.node.Status().Applied, 10*time.Second)
}
