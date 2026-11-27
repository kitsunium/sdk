package consensus

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// byteLog is a memory log whose bytes the test sets, so a test moves the
// log toward the tight band at the write rate it chooses.
type byteLog struct {
	*MemoryLog
	bytes *atomic.Int64
}

func (l byteLog) logBytes() int64 { return l.bytes.Load() }

func (byteLog) freeBytes() int64 { return -1 }

// ratePhase is a stretch of a rate-short scenario: for dur, the stream
// moves at stream bytes a second and the log grows at write bytes a second;
// at its end the peer's PeerSnapshotRateShort is short, and rises have been
// counted and logged in all.
type ratePhase struct {
	dur           time.Duration
	stream, write int64
	short         bool
	rises         int
}

// #1486: a snapshot stream whose transfer, at the rate it moves, would end
// after the writes bring the log to the band where its pin stops holding
// compaction back raises PeerSnapshotRateShort in Health.Peers, counts
// consensus_snapshot_rate_short_total and logs the peer, both times and
// the rate once per rise; the flag falls once R is raised or the writes
// stop, and holds between the two thresholds of its hysteresis. On the
// base the scenario is #550's loop with no signal: the log turns tight, so
// the pin stops holding, while the stream still has most of its bytes to
// send. Driven on a manual clock at the loop's tick, the stream's progress
// and the log's bytes moved by the test.
func TestAStreamTooSlowForTheLogBudgetIsFlaggedCountedAndLogged(t *testing.T) {
	const (
		peer  = coreconsensus.NodeID(2)
		size  = 10 << 20
		tick  = 10 * time.Millisecond
		etMax = 100 * time.Millisecond
	)
	type tc struct {
		name   string
		phases []ratePhase
		// tight runs the last phase on until the log is tight, and
		// requires the stream still in flight then: the pin no longer
		// holds while the transfer goes on, #550's loop.
		tight bool
	}
	runCase := func(t *testing.T, c tc) {
		mc := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		meter := svcmetrics.NewMeter()
		lg := &attrLogger{}
		bytes := new(atomic.Int64)
		n := &Node{
			clk: mc, obs: newObs(&Config{Meter: meter, Logger: lg, Group: 1}),
			cfg:     Config{Log: byteLog{MemoryLog: NewMemoryLog(), bytes: bytes}, MaxLogBytes: 1 << 20, LogReserveBytes: 128 << 10},
			sending: map[coreconsensus.NodeID]sendPin{},
		}
		progress := new(atomic.Int64)
		n.sending[peer] = sendPin{index: 10, since: mc.Now().UnixNano(), sent: new(atomic.Int64), moved: new(atomic.Int64), progress: progress, size: size}
		short := func() bool {
			ph := n.peerHealth(&raft.PeerStatValue{ID: peer, Flags: raft.StatSnapshot}, mc.Now(), 0, time.Second)
			return ph.Flags.Has(PeerSnapshotRateShort)
		}
		var sentAcc, logAcc float64
		step := func(p ratePhase) {
			mc.Advance(tick)
			sentAcc += float64(p.stream) * tick.Seconds()
			logAcc += float64(p.write) * tick.Seconds()
			progress.Store(min(int64(sentAcc), size))
			bytes.Store(int64(logAcc))
			n.watchRateShort(mc.Now(), etMax)
		}
		rises := func() int {
			total := 0
			//: every point of the counter, by peer.
			for _, p := range meter.Collect().Sums[mSnapshotRateShort].Points {
				//: the peer's own series.
				if hasAttr(p.Attrs, "peer", peer.String()) {
					total += int(p.Value)
				}
			}
			return total
		}
		//: each phase in turn, judged at its end.
		for i, p := range c.phases {
			//: the phase's ticks.
			for range int(p.dur / tick) {
				step(p)
			}
			//: the flag in Health.Peers.
			if got := short(); got != p.short {
				t.Fatalf("phase %d (stream %d B/s, writes %d B/s): PeerSnapshotRateShort %v, want %v", i, p.stream, p.write, got, p.short)
			}
			//: the counter.
			if got := rises(); got != p.rises {
				t.Fatalf("phase %d: %s = %d, want %d", i, mSnapshotRateShort, got, p.rises)
			}
			//: one Warn line per rise.
			if got := len(lg.lines); got != p.rises {
				t.Fatalf("phase %d: %d Warn lines, want %d: %v", i, got, p.rises, lg.lines)
			}
		}
		//: each line names the peer, both times and the rate.
		for _, line := range lg.lines {
			//: the four attributes.
			if line["peer"] != strconv.FormatUint(uint64(peer), 10) || line["transfer"] == "" || line["fill"] == "" || line["rate"] == "" {
				t.Fatalf("the line does not name the peer, both times and the rate: %v", line)
			}
		}
		//: the scenario carried on until the pin stops holding.
		if c.tight {
			last := c.phases[len(c.phases)-1]
			//: until the log is tight, a bound on the clock's turns.
			for i := 0; !n.logTight(); i++ {
				//: the stream would have ended first.
				if i > 100_000 {
					t.Fatal("the log never turned tight")
				}
				step(last)
			}
			//: #550: the pin is lifted while the stream still has bytes to send.
			if sent := progress.Load(); sent >= size {
				t.Fatalf("the stream ended (%d of %d bytes) before the log turned tight: no loop to signal", sent, size)
			}
			t.Logf("the log turned tight with %d of %d bytes sent: the pin no longer holds, the peer will need another snapshot", progress.Load(), size)
		}
		// The stream ends: its pin is a streamed one, and the flag falls.
		n.sending[peer] = sendPin{index: 10, until: mc.Now().Add(time.Second)}
		//: no flag on a stream that ended.
		if short() {
			t.Fatal("PeerSnapshotRateShort holds once the stream ended")
		}
	}
	for _, c := range []tc{
		{
			// About 100 s to send, 39 s to fill: raised; R raised to 1 MiB/s
			// brings the transfer to about 9 s: lowered.
			name: "R raised",
			phases: []ratePhase{
				{dur: time.Second, stream: 100 << 10, write: 20 << 10, short: true, rises: 1},
				{dur: time.Second, stream: 1 << 20, write: 20 << 10, rises: 1},
			},
		},
		{
			// The writes stop: the band is never reached.
			name: "writes stopped",
			phases: []ratePhase{
				{dur: time.Second, stream: 100 << 10, write: 20 << 10, short: true, rises: 1},
				{dur: time.Second, stream: 100 << 10, rises: 1},
			},
		},
		{
			// About 34 s against 38 s, nine tenths: between the thresholds
			// the flag holds and nothing is counted again; past R a rise
			// is counted anew.
			name: "hysteresis",
			phases: []ratePhase{
				{dur: time.Second, stream: 100 << 10, write: 20 << 10, short: true, rises: 1},
				{dur: time.Second, stream: 290 << 10, write: 20 << 10, short: true, rises: 1},
				{dur: time.Second, stream: 1 << 20, write: 20 << 10, rises: 1},
				{dur: time.Second, stream: 100 << 10, write: 20 << 10, short: true, rises: 2},
			},
		},
		{
			// R enough: about 10 s against 39 s, nothing raised.
			name: "R enough",
			phases: []ratePhase{
				{dur: 2 * time.Second, stream: 1 << 20, write: 20 << 10},
			},
		},
		{
			// #550's loop on the base: the pin stops holding mid-stream.
			name:   "the pin lifts mid-stream",
			phases: []ratePhase{{dur: time.Second, stream: 100 << 10, write: 20 << 10, short: true, rises: 1}},
			tight:  true,
		},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// #1486: tightFrom, the fill the detection measures against, is exactly
// where logTight turns: a byte below it the log is not tight, at it the log
// is, under a quota of at most two reserves as above it.
func TestTightFromIsWhereTheLogTurnsTight(t *testing.T) {
	type tc struct {
		name     string
		max, res int64
	}
	runCase := func(t *testing.T, c tc) {
		bytes := new(atomic.Int64)
		n := &Node{cfg: Config{Log: byteLog{MemoryLog: NewMemoryLog(), bytes: bytes}, MaxLogBytes: c.max, LogReserveBytes: c.res}}
		from := n.tightFrom()
		bytes.Store(from - 1)
		//: a byte below the band.
		if n.logTight() {
			t.Fatalf("tight at %d, a byte below tightFrom %d", from-1, from)
		}
		bytes.Store(from)
		//: at the band.
		if !n.logTight() {
			t.Fatalf("not tight at tightFrom %d", from)
		}
	}
	for _, c := range []tc{
		{name: "quota of two reserves or less", max: 1 << 20, res: 600 << 10},
		{name: "odd half", max: 1<<20 + 1, res: 600 << 10},
		{name: "three reserves", max: 3 << 20, res: 1 << 20},
		{name: "large quota", max: 1 << 30, res: 1 << 20},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
