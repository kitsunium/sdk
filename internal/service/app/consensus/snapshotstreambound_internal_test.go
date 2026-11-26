package consensus

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recordingReader is the stream's source: it notes the clock at each read
// the transport makes of it, the moments a stream's moved mark is stored.
type recordingReader struct {
	r    io.Reader
	clk  clock.Timed
	mu   sync.Mutex
	last time.Time
	gap  time.Duration
}

// Read reads r, and keeps the longest time between two of its reads.
func (g *recordingReader) Read(p []byte) (int, error) {
	now := g.clk.Now()
	g.mu.Lock()
	if !g.last.IsZero() {
		g.gap = max(g.gap, now.Sub(g.last))
	}
	g.last = now
	g.mu.Unlock()
	return g.r.Read(p)
}

// #1485: with a pacer, the streams sharing it each wait for N slices of its
// rate between two reads, so a stream's moved mark ages up to N/paceSlices
// seconds; streamDue cuts a stream whose mark is older than pinBound, which
// is pinAfterStream maximum election timeouts at scale 0. The two meet at
// MaxSnapshotStreams = pinAfterStream*paceSlices*ElectionTimeoutMax/1s, and
// MaxSnapshotStreams is refused above it, with the code every refusal of a
// bound has, naming the bound.
func TestMaxSnapshotStreamsIsRefusedAboveTheBoundItsPacerSetsNewNode(t *testing.T) {
	type tc struct {
		name      string
		etMax     time.Duration
		rate      int64
		streams   int
		wantBound int
		refused   bool
	}
	runCase := func(t *testing.T, c tc) {
		cl := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 3, 0}, size: 1})
		cfg := cl.config(cl.nodes[0])
		cfg.InitialTiming.ElectionTimeoutMax = c.etMax
		cfg.InitialTiming.ElectionTimeoutMin = c.etMax / 2
		cfg.InitialTiming.HeartbeatInterval = c.etMax / 20
		cfg.SnapshotStreamBytesPerSecond, cfg.MaxSnapshotStreams = c.rate, c.streams
		node, err := NewNode(cfg)
		if node != nil {
			defer func() { _ = node.HardClose() }()
		}
		if !c.refused {
			if err != nil {
				t.Fatalf("%d streams at %v: refused: %v (%s)", c.streams, c.etMax, err, fieldOf(err, "why"))
			}
			return
		}
		//: the refusal is the one of every bound, and it names the field and the bound.
		if !errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured) {
			t.Fatalf("%d streams at %v: %v, want CONSENSUS_MISCONFIGURED", c.streams, c.etMax, err)
		}
		if fieldOf(err, "field") != "MaxSnapshotStreams" || !strings.Contains(fieldOf(err, "why"), strconv.Itoa(c.wantBound)) {
			t.Fatalf("refusal names %q: %q, want MaxSnapshotStreams and the bound %d",
				fieldOf(err, "field"), fieldOf(err, "why"), c.wantBound)
		}
	}
	for _, c := range []tc{
		{name: "default timing, over the issue's 200", etMax: 2 * time.Second, rate: 1 << 20, streams: 201, wantBound: 200, refused: true},
		{name: "default timing, at the bound", etMax: 2 * time.Second, rate: 1 << 20, streams: 200},
		{name: "200 ms, one over", etMax: 200 * time.Millisecond, rate: 1 << 20, streams: 21, wantBound: 20, refused: true},
		{name: "200 ms, at the bound", etMax: 200 * time.Millisecond, rate: 1 << 20, streams: 20},
		{name: "no pacer, no ageing, no bound", etMax: 200 * time.Millisecond, streams: 1000},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// #1485: at the bound, every stream of a shared pacer is read again before
// its moved mark is older than pinBound (the clock's step apart), so
// streamDue cuts none; twice the bound, the marks outlive it.
func TestStreamsAtTheBoundAreNeverStillLongEnoughToBeCut(t *testing.T) {
	const (
		rate = 10_000 // a slice is 1000 bytes, 100 ms of the pacer
		step = time.Millisecond
	)
	etMax := 100 * time.Millisecond
	bound := int(maxPacedStreams(etMax)) // 10
	pinBound := time.Duration(pinAfterStream) * etMax
	type tc struct {
		name    string
		streams int
		cut     bool
	}
	runCase := func(t *testing.T, c tc) {
		mc := clock.NewManualClock(time.Unix(1_000_000, 0))
		n := &Node{clk: mc, pace: newPacer(mc, rate)}
		readers := make([]*recordingReader, c.streams)
		var wg sync.WaitGroup
		var ended atomic.Int64
		for i := range readers {
			readers[i] = &recordingReader{r: bytes.NewReader(make([]byte, 20_000)), clk: mc}
			sr := &sentReader{
				ctx: context.Background(), r: readers[i], n: n,
				sent: new(atomic.Int64), moved: new(atomic.Int64), progress: new(atomic.Int64),
			}
			wg.Go(func() { _, _ = io.Copy(io.Discard, sr); ended.Add(1) })
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		for deadline := time.Now().Add(60 * time.Second); ; {
			select {
			case <-done:
				var gap time.Duration
				for _, r := range readers {
					gap = max(gap, r.gap)
				}
				//: a gap past the bound plus one step is a stream streamDue would cut.
				if cut := gap > pinBound+step; cut != c.cut {
					t.Fatalf("%d streams: longest gap %v against pinBound %v, cut=%v want %v", c.streams, gap, pinBound, cut, c.cut)
				}
				return
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d streams never ended", c.streams)
			}
			//: the clock moves only once every stream still reading waits on it, so no stream charges late.
			if mc.Pending() > 0 && int64(mc.Pending()) >= int64(c.streams)-ended.Load() {
				mc.Advance(step)
				continue
			}
			time.Sleep(50 * time.Microsecond)
		}
	}
	for _, c := range []tc{
		{name: "at the bound", streams: bound},
		{name: "twice the bound", streams: 2 * bound, cut: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
