package consensus

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The pacing's sources, read by the wall-clock audit below: embedded so the
// audit reads them under Bazel too, where the package's sources are no
// runfiles.
var (
	//go:embed node_pace.go
	paceSource string
	//go:embed node_sendpin.go
	sendPinSource string
)

// #920: the pacing waits on the node's clock, never on package time's. A
// wait on the wall clock would make S/R hold only in real time, untestable
// under the manual clock, and a stream paced by time.Sleep would ignore its
// context.
func TestThePacingWaitsOnTheInjectedClockOnly(t *testing.T) {
	waits := []string{"Sleep", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker"}
	for name, src := range map[string]string{"node_pace.go": paceSource, "node_sendpin.go": sendPinSource} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && slices.Contains(waits, sel.Sel.Name) {
				t.Errorf("%s: time.%s waits on the wall clock", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

// pacedRead reads size bytes through a sentReader paced at rate on a
// manual clock, advancing the clock by step while the reader waits, and
// returns the simulated time the read took.
func pacedRead(t *testing.T, rate, size int64, step time.Duration) time.Duration {
	t.Helper()
	mc := clock.NewManualClock(time.Unix(1_000_000, 0))
	n := &Node{clk: mc, pace: newPacer(mc, rate)}
	sr := &sentReader{
		ctx: context.Background(), r: bytes.NewReader(make([]byte, size)), n: n,
		sent: new(atomic.Int64), moved: new(atomic.Int64), progress: new(atomic.Int64),
	}
	start := mc.Now()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, sr)
		done <- err
	}()
	for deadline := time.Now().Add(30 * time.Second); ; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("paced read: %v", err)
			}
			if got := sr.progress.Load(); got != size {
				t.Fatalf("progress %d, want %d", got, size)
			}
			return mc.Since(start)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the paced read never ended: %d of %d bytes", sr.progress.Load(), size)
		}
		if mc.Pending() > 0 {
			mc.Advance(step)
			continue
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// #920: a stream of S bytes paced at R bytes a second ends in S/R of the
// node's clock, give or take one chunk (a pacer's slice, R/10).
func TestAPacedStreamEndsInItsSizeOverItsRate(t *testing.T) {
	type tc struct {
		name       string
		rate, size int64
	}
	runCase := func(t *testing.T, c tc) {
		got := pacedRead(t, c.rate, c.size, time.Millisecond)
		want := time.Duration(float64(c.size) / float64(c.rate) * float64(time.Second))
		chunk := time.Duration(float64(max(c.rate/paceSlices, 1)) / float64(c.rate) * float64(time.Second))
		if got < want-chunk || got > want+chunk {
			t.Fatalf("%d bytes at %d B/s took %v of the clock, want %v ± %v", c.size, c.rate, got, want, chunk)
		}
	}
	for _, c := range []tc{
		{name: "quarter second", rate: 1 << 20, size: 256 << 10},
		{name: "two seconds", rate: 512 << 10, size: 1 << 20},
		{name: "not a multiple of the slice", rate: 100_000, size: 123_457},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// #920: a paced read cut by its stream's end returns at once, with the
// context's error, and never waits out its budget.
func TestAPacedReadEndsWithItsContext(t *testing.T) {
	mc := clock.NewManualClock(time.Unix(1_000_000, 0))
	n := &Node{clk: mc, pace: newPacer(mc, 1000)}
	ctx, cancel := context.WithCancel(context.Background())
	sr := &sentReader{
		ctx: ctx, r: bytes.NewReader(make([]byte, 10_000)), n: n,
		sent: new(atomic.Int64), moved: new(atomic.Int64), progress: new(atomic.Int64),
	}
	done := make(chan error, 1)
	go func() {
		_, err := sr.Read(make([]byte, 4096))
		done <- err
	}()
	mc.BlockUntil(1)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the paced read waited out its budget past its context's end")
	}
	if got := sr.progress.Load(); got != 100 {
		t.Fatalf("the read took %d bytes, want one slice of 100", got)
	}
}

// #920, D14: MaxSnapshotStreams clamps to 1, the rate is unlimited at 0, and a
// negative rate is refused.
func TestTheSnapshotStreamBoundsClampAndRefuse(t *testing.T) {
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 2, 0}, size: 1})
	cfg := c.config(c.nodes[0])
	got, err := withDefaults(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxSnapshotStreams != 1 || got.SnapshotStreamBytesPerSecond != 0 {
		t.Fatalf("defaults: %d streams at %d B/s, want 1 at 0", got.MaxSnapshotStreams, got.SnapshotStreamBytesPerSecond)
	}
	if newPacer(got.Clock, 0) != nil {
		t.Fatal("a rate of 0 is paced")
	}
	cfg.SnapshotStreamBytesPerSecond = -1
	if _, err := withDefaults(&cfg); !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
		t.Fatalf("a negative rate: %v, want CONSENSUS_MISCONFIGURED", err)
	}
}

// #920: with MaxSnapshotStreams at 1, two learners that need a snapshot are
// streamed one after the other: the second waits in raft's snapshot state
// until the first stream ends, then is served. Both install, and the pin of
// each holds compaction while the writes go on: neither needs a second
// snapshot to catch up.
//
// The second learner's JOIN goes to the leader alone (#1412). Sent to the
// first learner, which the memory transport tried first one time in four,
// it was forwarded and committed at once, but answered only once that
// learner applied it, after the snapshot the test holds: the JOIN waited
// out its ten seconds, the held stream, still at its offer, fell due
// (streamDue, 10 maximum election timeouts) and was cancelled, and the
// waiting learner was offered its stream within the 500 ms the test looks.
// The look now says whether the first stream was still in flight, and
// requires it to be.
func TestTwoLearnersAreStreamedOneAfterTheOther(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 2, 1}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		ht := &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		cfg.InitialTiming = steadyTiming()
		cfg.MaxSnapshotStreams = 1
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := l.log.Bounds(); first > 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the leader never compacted its log")
		}
	}
	cutsSettled(t, l)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	a := c.join(t, "slot-a")
	b := c.joinThrough(t, "slot-b", l)
	awaitOffer(t, lt, l)
	first := streamsInFlight(l)
	waiting := streamWaiter(t, l)
	select {
	case <-lt.started:
		t.Fatalf("a second stream started while the first, to %v, was held: in flight %v, %d held streams cancelled, the leader's status %+v",
			first, streamsInFlight(l), lt.cancelled.Load(), l.node.Status())
	case <-time.After(500 * time.Millisecond):
	}
	if now := streamsInFlight(l); len(first) != 1 || !slices.Equal(now, first) || lt.cancelled.Load() != 0 {
		t.Fatalf("the first stream ended while the test held it: in flight %v at the offer, %v now, %d held streams cancelled, the leader's status %+v",
			first, now, lt.cancelled.Load(), l.node.Status())
	}
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("r0-%d=v", i))
	}
	releaseHeld(t, lt, l)
	awaitOffer(t, lt, l)
	var pinned bool
	l.node.exec(func() { _, pinned = l.node.sending[waiting] })
	if !pinned {
		t.Fatalf("the second stream, to %d, holds no pin", waiting)
	}
	// The stream in flight is in the leader's health: held before its
	// accept, nothing of it read yet, of its snapshot's size.
	for _, p := range l.node.Health().Peers {
		if p.ID == waiting && (p.SnapshotSize <= 0 || p.SnapshotSent != 0) {
			t.Fatalf("the stream to %d reads %d of %d bytes in the health, want 0 of its size", waiting, p.SnapshotSent, p.SnapshotSize)
		}
	}
	for i := range 45 {
		propose(t, l.node, fmt.Sprintf("r1-%d=v", i))
	}
	lt.holding.Store(false)
	releaseHeld(t, lt, l)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		va, _ := a.sm.get("r1-44")
		vb, _ := b.sm.get("r1-44")
		if va == "v" && vb == "v" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the learners never caught up: %+v, %+v, streams %d", a.node.Status(), b.node.Status(), lt.streams.Load())
		}
	}
	if got := lt.streams.Load(); got != 2 {
		t.Fatalf("%d snapshot streams, want 2: a learner needed another one to catch up", got)
	}
}

// awaitOffer waits for the leader's held transport to be offered a stream.
func awaitOffer(t *testing.T, lt *holdTransport, l *testNode) {
	t.Helper()
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatalf("no snapshot offered: leader %+v, streams %d", l.node.Status(), lt.streams.Load())
	}
}

// streamsInFlight are the peers leader l streams a snapshot to.
func streamsInFlight(l *testNode) []coreconsensus.NodeID {
	var to []coreconsensus.NodeID
	l.node.exec(func() { to = slices.Sorted(maps.Keys(l.node.sendStop)) })
	return to
}

// streamWaiter waits until a peer waits for a stream on leader l, and
// returns it.
func streamWaiter(t *testing.T, l *testNode) coreconsensus.NodeID {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var w []coreconsensus.NodeID
		var inFlight int
		l.node.exec(func() { w, inFlight = slices.Clone(l.node.streamWait), len(l.node.sendStop) })
		if inFlight > 1 {
			t.Fatalf("%d streams in flight under MaxSnapshotStreams 1", inFlight)
		}
		if len(w) == 1 {
			return w[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no learner waits for a stream: %v waiting, %d in flight", w, inFlight)
		}
	}
}
