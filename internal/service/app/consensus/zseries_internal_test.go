package consensus

import (
	"strconv"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// oneWay is the delay every node's frames take, through removeleader's
// delayedTransport: the round trip between two nodes is twice it.
const oneWay = 15 * time.Millisecond

// hasAttr reports whether attrs carry key with value v, as its text.
func hasAttr(attrs []coreotel.AttrValue, key, v string) bool {
	//: the attribute among the series' set.
	for _, a := range attrs {
		//: key and value both match.
		if a.Key == key && string(a.AppendText(nil)) == v {
			return true
		}
	}
	return false
}

// D13 (#914): the three series observe.go lacked, published with the
// labels D13 names: consensus_peer_rtt_seconds a histogram by peer whose
// observations are the injected round trip, consensus_prevotes_lost_total
// a counter and consensus_snapshot_pins a gauge, each by group.
func TestTheD13SeriesArePublishedWithTheirLabels(t *testing.T) {
	var mu sync.Mutex
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	meters := map[coreconsensus.NodeID]coremetrics.FullMeter{}
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 1, 4}, timing: steadyTiming(),
		configure: func(tn *testNode, cfg *Config) {
			m := svcmetrics.NewMeter()
			mu.Lock()
			meters[tn.id] = m
			mu.Unlock()
			cfg.Meter = m
			cfg.Transport = delayed(cfg.Transport, oneWay, done)
		},
	})
	l := c.settledLeader(10 * time.Second)
	mu.Lock()
	meter := meters[l.id]
	mu.Unlock()
	group := strconv.FormatInt(int64(l.node.cfg.Group), 10)
	var peers []string
	//: every other node is a peer of the leader.
	for _, tn := range c.nodes {
		//: the leader is no peer of its own.
		if tn.id != l.id {
			peers = append(peers, tn.id.String())
		}
	}
	rtt := func(snap coremetrics.SnapshotValue, peer string) (coremetrics.HistogramValue, bool) {
		//: the peer's series of the histogram.
		for _, p := range snap.Histograms[mPeerRTT].Points {
			//: labelled by the peer and the group.
			if hasAttr(p.Attrs, "peer", peer) && hasAttr(p.Attrs, "group", group) {
				return p, p.Count >= 10
			}
		}
		return coremetrics.HistogramValue{}, false
	}
	waitFor(t, "ten round trips of each peer", func() bool {
		snap := meter.Collect()
		//: each peer has its samples.
		for _, peer := range peers {
			//: one short is not yet.
			if _, ok := rtt(snap, peer); !ok {
				return false
			}
		}
		return true
	})
	snap := meter.Collect()
	want := 2 * oneWay
	tick := time.Duration(l.node.tickEvery.Load())
	//: each peer's round trips are the injected one, within a tick and the
	//: scheduling of a loaded machine.
	for _, peer := range peers {
		h, _ := rtt(snap, peer)
		mean := time.Duration(h.Sum / float64(h.Count) * float64(time.Second))
		//: no observation below the injected round trip: the buckets under
		//: it are empty.
		for i, bound := range h.Bounds {
			//: a bucket wholly below the round trip holds nothing.
			if bound < want.Seconds() && h.Counts[i] != 0 {
				t.Errorf("peer %s: %d round trips at or under %vs, below the injected %v", peer, h.Counts[i], bound, want)
			}
		}
		if mean < want || mean > want+tick+10*time.Millisecond {
			t.Errorf("peer %s: mean round trip %v over %d samples, want %v within a tick (%v) and 10ms", peer, mean, h.Count, want, tick)
		}
	}
	lost, ok := snap.Sums[mPreVotesLost]
	if !ok || len(lost.Points) != 1 || !hasAttr(lost.Points[0].Attrs, "group", group) {
		t.Errorf("counter %s missing or unlabelled: %+v", mPreVotesLost, lost)
	}
	pins, ok := snap.Gauges[mSnapshotPins]
	if !ok || len(pins.Points) != 1 || !hasAttr(pins.Points[0].Attrs, "group", group) || pins.Points[0].Value != 0 {
		t.Errorf("gauge %s missing, unlabelled or not 0 with no transfer: %+v", mSnapshotPins, pins)
	}
}

// D13 (#914): the pins are counted, each one's age published by peer from
// its offer, and the age of a pin that ended reads 0, for the alert on a
// pin held past pinAfterStream maximum election timeouts.
func TestThePinGaugesCountAndAgeEachPinAndResetOneThatEnded(t *testing.T) {
	meter := svcmetrics.NewMeter()
	n := &Node{obs: newObs(&Config{Meter: meter, Group: 1}), sending: map[coreconsensus.NodeID]sendPin{}}
	now := time.Unix(1_700_000_000, 0)
	n.sending[2] = sendPin{index: 10, since: now.Add(-3 * time.Second).UnixNano()}
	n.sending[3] = sendPin{index: 10, since: now.Add(-25 * time.Second).UnixNano(), until: now.Add(time.Second)}
	n.observePins(now)
	gauge := func(name, peer string) (float64, bool) {
		//: the series of the peer, or the bare one.
		for _, p := range meter.Collect().Gauges[name].Points {
			//: the peer's own, or the one without a peer.
			if peer == "" || hasAttr(p.Attrs, "peer", peer) {
				return p.Value, true
			}
		}
		return 0, false
	}
	if v, _ := gauge(mSnapshotPins, ""); v != 2 {
		t.Fatalf("%s = %v, want 2", mSnapshotPins, v)
	}
	if v, _ := gauge(mSnapshotPinAge, "2"); v != 3 {
		t.Errorf("peer 2's pin age %v, want 3", v)
	}
	if v, _ := gauge(mSnapshotPinAge, "3"); v != 25 {
		t.Errorf("peer 3's pin age %v, want 25", v)
	}
	delete(n.sending, 3)
	n.observePins(now.Add(time.Second))
	if v, _ := gauge(mSnapshotPins, ""); v != 1 {
		t.Errorf("%s = %v after one pin ended, want 1", mSnapshotPins, v)
	}
	if v, ok := gauge(mSnapshotPinAge, "3"); !ok || v != 0 {
		t.Errorf("an ended pin's age reads %v (published %v), want 0", v, ok)
	}
	if v, _ := gauge(mSnapshotPinAge, "2"); v != 4 {
		t.Errorf("peer 2's pin age %v a second later, want 4", v)
	}
}
