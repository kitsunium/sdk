package consensus

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// valveTransport is a holdTransport whose frames to one peer wait, while
// its valve is shut, until the test lets through what waits: that peer
// catches up by appends only as fast as the test steps it, so a catch-up
// that outlasts the pin bound needs no clock.
type valveTransport struct {
	*holdTransport
	mu   sync.Mutex
	peer coreconsensus.NodeID
	shut bool
	q    [][]byte
}

// Send holds a frame to the peer while the valve is shut, and sends any
// other at once.
func (v *valveTransport) Send(to coreconsensus.NodeID, frame []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shut && to == v.peer {
		v.q = append(v.q, frame)
		return
	}
	v.MemoryTransport.Send(to, frame)
}

// close shuts the valve on frames to peer.
func (v *valveTransport) close(peer coreconsensus.NodeID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.peer, v.shut = peer, true
}

// flush sends, in order, what waits, and opens the valve when open is set.
// The memory transport never blocks, so it sends under the lock and a frame
// sent meanwhile never overtakes one that waited.
func (v *valveTransport) flush(open bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, f := range v.q {
		v.MemoryTransport.Send(v.peer, f)
	}
	v.q = nil
	v.shut = v.shut && !open
}

// catchUpEntry is entry i, about 200 bytes, so a batch of MaxBatchBytes
// carries a few entries and a catch-up takes several steps.
func catchUpEntry(prefix string, i int) string {
	return fmt.Sprintf("%s%03d=%s", prefix, i, strings.Repeat("v", 200))
}

// valveRun restarts a follower behind the leader's compacted log, holds the
// snapshot stream it is offered while 60 entries are written, then lets the
// stream end with the valve shut on the leader's frames to it: the follower
// sits at the snapshot, 60 entries behind, its pin streamed. It returns once
// that pin is set, the snapshot's index with it.
func valveRun(t *testing.T, id byte) (c *cluster, l, lag *testNode, v *valveTransport, index uint64) {
	t.Helper()
	valves := map[coreconsensus.NodeID]*valveTransport{}
	var mu sync.Mutex
	c = &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 7, 1, id}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		valves[tn.id] = &valveTransport{holdTransport: &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}}
		cfg.Transport = valves[tn.id]
		cfg.InitialTiming = steadyTiming()
		// One batch of a few entries in flight at a time: each step of the
		// valve moves the follower's match by one batch at most.
		cfg.MaxBatchBytes, cfg.MaxEntryBytes = 2048, 2048
		cfg.MaxInflight, cfg.MaxInflightBytes = 1, 2048
	}
	newClusterWith(t, c)
	l = c.settledLeader(10 * time.Second)
	lag = c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, catchUpEntry("a", i))
	}
	compactedPastItsEnd(t, l, lag)
	mu.Lock()
	v = valves[l.id]
	mu.Unlock()
	shut := func() { v.close(lag.id) }
	v.streamed.Store(&shut)
	v.holding.Store(true)
	t.Cleanup(func() {
		v.holding.Store(false)
		v.flush(true)
	})
	hearOnlyTheLeader(c, l, lag)
	c.start(lag)
	select {
	case <-v.started:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot offered to the lagging node")
	}
	// Three cuts' worth of entries while the snapshot streams: the lag its
	// install leaves it with.
	for i := range 60 {
		propose(t, l.node, catchUpEntry("b", i))
	}
	releaseHeld(t, v.holdTransport, l)
	var pin sendPin
	waitFor(t, "the streamed snapshot's pin", func() bool {
		l.node.exec(func() { pin = l.node.sending[lag.id] })
		return !pin.until.IsZero()
	})
	return c, l, lag, v, pin.index
}

// stepPast lets through the leader's frames to the follower, a queue at a
// time, until its match passes past, and returns that match.
func stepPast(t *testing.T, l, lag *testNode, v *valveTransport, past uint64) uint64 {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		v.flush(false)
		var match uint64
		l.node.exec(func() { match, _, _ = l.node.r.Progress(lag.id) })
		if match > past {
			return match
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: the follower's match is still %d, not past %d", match, past)
		}
	}
}

// pinDue runs watchPins at the instant after the bound of the follower's
// pin, up to bounds times in a row on one turn of the loop, while the pin
// holds, and reports the follower's match and the leader's last index it
// last judged on, and whether the pin still holds after them.
func pinDue(l, lag *testNode, bounds int) (match, last uint64, kept bool) {
	l.node.exec(func() {
		for range bounds {
			pin, ok := l.node.sending[lag.id]
			if !ok {
				return
			}
			match, _, _ = l.node.r.Progress(lag.id)
			last = l.node.r.Status().Last
			l.node.watchPins(pin.until.Add(time.Nanosecond))
		}
		_, kept = l.node.sending[lag.id]
	})
	return match, last, kept
}

// #771: a streamed snapshot's pin ended a fixed pinBound after its stream,
// whatever its follower's progress. A follower that catches up by appends
// for longer than that, as one that installed a long transfer under a
// steady write rate does, was compacted past again, offered the newest
// snapshot, and fell behind by the same lag: never caught up while the load
// lasted. The pin is now renewed at its bound while the follower moved
// since and is no further behind than its install left it, and the
// follower catches up with no second snapshot. The valve stands for a slow
// link, and the bound is crossed by handing watchPins the instant after
// it: the leader's clock never needs to pass it.
func TestAStreamedPinOutlastsItsBoundWhileItsFollowerCatchesUp(t *testing.T) {
	_, l, lag, v, index := valveRun(t, 1)
	at := index
	for round := range 2 {
		stepPast(t, l, lag, v, at)
		match, last, kept := pinDue(l, lag, 1)
		if match <= at || match >= last {
			t.Fatalf("setup, bound %d: the follower is at %d, not between %d and the leader's last index %d", round, match, at, last)
		}
		if !kept {
			t.Fatalf("bound %d: the pin of a follower still catching up — at %d, from %d its install left it at, %d behind the leader's last index %d — ended at its bound", round, match, index, last-match, last)
		}
		at = match
	}
	v.flush(true)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		select {
		case <-v.started:
			f, _ := l.log.Bounds()
			t.Fatalf("the follower was offered a second snapshot: its status %+v, the leader's log starts at %d", lag.node.Status(), f)
		default:
		}
		if _, ok := lag.sm.get(fmt.Sprintf("b%03d", 59)); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the follower never caught up by appends: %+v", lag.node.Status())
		}
	}
	waitFor(t, "the caught-up follower's pin to end", func() bool {
		var pinned bool
		l.node.exec(func() { _, pinned = l.node.sending[lag.id] })
		return !pinned
	})
}

// #771, the bound kept: a renewed pin still ends at its next bound once its
// follower stopped moving, or fell further behind than its install left
// it — slower than the writes, it would hold the log growing until the log
// is tight, and only a snapshot brings it back.
func TestARenewedStreamedPinEndsOnceItsFollowerNoLongerCatchesUp(t *testing.T) {
	type tc struct {
		name   string
		id     byte
		behind bool
	}
	runCase := func(t *testing.T, c tc) {
		_, l, lag, v, index := valveRun(t, c.id)
		at := stepPast(t, l, lag, v, index)
		if _, _, kept := pinDue(l, lag, 1); !kept {
			t.Fatalf("setup: the pin of a follower at %d, past %d, ended at its first bound", at, index)
		}
		if c.behind {
			// Writes the follower is not sent, more than it caught up since
			// its install, then one step: it moved, and it is further behind
			// than its install left it.
			for k := range 60 {
				propose(t, l.node, catchUpEntry("c", k))
			}
			var match uint64
			l.node.exec(func() { match, _, _ = l.node.r.Progress(lag.id) })
			stepPast(t, l, lag, v, match)
		}
		// A late acknowledgement may still move the follower once after the
		// last renewal: let it land, then the pin is renewed at most once
		// more, and ends.
		time.Sleep(100 * time.Millisecond)
		match, last, kept := pinDue(l, lag, 2)
		if kept {
			t.Fatalf("the pin of a follower that no longer catches up — at %d, %d behind the leader's last index %d — held past two more bounds", match, last-match, last)
		}
	}
	for _, c := range []tc{{name: "stopped", id: 2}, {name: "falling behind", id: 3, behind: true}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
