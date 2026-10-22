package consensus

import (
	"bytes"
	"context"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// joinWithLimit has a new node join the TCP cluster ns, announcing limit,
// and returns the learner as the leader l admitted it.
func joinWithLimit(t *testing.T, ca *testCA, cid coreconsensus.ClusterID, ns []*tcpNode, l *tcpNode, limit int64) coreconsensus.MemberValue {
	t.Helper()
	j := &tcpNode{slot: "site-new", log: NewMemoryLog(), snaps: NewMemorySnapshots()}
	j.cert, j.spki = ca.issue(t, SAN(cid, j.slot), nil)
	j.listen(t, ca)
	id, _, err := InitLog(j.log, cid, j.slot)
	if err != nil {
		t.Fatal(err)
	}
	j.id = id
	j.start(t, cid)
	t.Cleanup(func() { j.stop(t) })
	st := must(j.log.Stamp())(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := coreconsensus.JoinConfig{Addresses: []string{l.addr}, Stamp: st, Address: j.addr, Pins: pinsOf(ns), MaxSnapshotBytes: limit}
	if _, err := j.tr.Join(ctx, cfg); err != nil {
		t.Fatalf("join: %v", err)
	}
	var jm coreconsensus.MemberValue
	waitFor(t, "the learner in the leader's membership", func() bool {
		var ok bool
		jm, ok = l.node.Membership().Member(id)
		return ok
	})
	return jm
}

// A joiner announces its MaxSnapshotBytes in the JOIN, and the members hold
// their cuts to it: nothing carried it, and a cut a new member could never
// install was still written (R2-30, #556, after #512).
func TestTCPJoinAnnouncesItsSnapshotLimit(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	propose(t, l.node, "a=1")
	jm := joinWithLimit(t, ca, cid, ns, l, 1<<20)
	if jm.MaxSnapshotBytes != 1<<20 {
		t.Fatalf("the learner's announced limit is %d, want %d", jm.MaxSnapshotBytes, 1<<20)
	}
	if h := l.node.Health(); h.SnapshotLimit != 1<<20 {
		t.Fatalf("the leader holds its cuts to %d, want the joiner's %d", h.SnapshotLimit, 1<<20)
	}
}

// A member that predates the versioned JOIN ends its connection unanswered;
// the joiner sends the same JOIN on the old lane and is admitted, its limit
// unannounced: the behaviour before the announcement (#556).
func TestTCPJoinFallsBackOnAMemberThatPredatesTheLane(t *testing.T) {
	ca, cid, ns := newTCPCluster(t)
	for _, tn := range ns {
		tn.tr.predatesJoinV2.Store(true)
	}
	l := tcpLeader(t, ns)
	propose(t, l.node, "a=1")
	jm := joinWithLimit(t, ca, cid, ns, l, 1<<20)
	if jm.MaxSnapshotBytes != 0 {
		t.Fatalf("an older member recorded a limit of %d it was never sent", jm.MaxSnapshotBytes)
	}
}

// The versioned JOIN round-trips, and one of another version, or with a
// limit no int64 holds, is refused rather than read as a JOIN it is not.
func TestAVersionedJoinIsReadOneWay(t *testing.T) {
	r := coreconsensus.JoinRequestValue{
		Cluster: coreconsensus.ClusterID{1}, ID: 7, Slot: "site-1", Address: "10.0.0.1:7000", MaxSnapshotBytes: 1 << 30,
	}
	got, ok := decodeJoinOn(laneJoinV2, encodeJoinV2(r))
	if !ok || got != r {
		t.Fatalf("round trip: %+v %v, want %+v", got, ok, r)
	}
	if got, ok := decodeJoinOn(laneJoin, encodeJoin(r)); !ok || got.MaxSnapshotBytes != 0 || got.Address != r.Address {
		t.Fatalf("the old lane: %+v %v", got, ok)
	}
	b := encodeJoinV2(r)
	b[0] = joinVersion2 + 1
	if _, ok := decodeJoinOn(laneJoinV2, b); ok {
		t.Fatal("a JOIN of an unknown version was read")
	}
	b = encodeJoinV2(r)
	copy(b[1:9], bytes.Repeat([]byte{0xff}, 8))
	if _, ok := decodeJoinOn(laneJoinV2, b); ok {
		t.Fatal("a limit no int64 holds was read")
	}
	if _, ok := decodeJoinOn(laneJoinV2, b[:joinV2Prefix-1]); ok {
		t.Fatal("a JOIN cut short was read")
	}
}

// Health reports the newest snapshot's size and the limit a cut is held
// to, and warns from four fifths of it, before the cuts are refused
// SNAPSHOT_TOO_LARGE (R2-30, #556).
func TestHealthWarnsBeforeTheSnapshotLimit(t *testing.T) {
	const limit = 8192
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 5, 6}, every: 5,
		configure: func(_ *testNode, cfg *Config) {
			cfg.MaxSnapshotBytes, cfg.SnapshotEveryBytes = limit, 1024
		},
	})
	l := c.settledLeader(5 * time.Second)
	value := string(bytes.Repeat([]byte("v"), 40))
	sawBelow := false
	for i := 0; ; i++ {
		proposeOnLeader(t, c, "k"+string(rune('a'+i%26))+string(rune('a'+i/26))+"="+value)
		cutsSettled(t, l)
		h := l.node.Health()
		if h.SnapshotLimit != limit {
			t.Fatalf("SnapshotLimit %d, want %d", h.SnapshotLimit, limit)
		}
		near := h.Flags.Has(HealthSnapshotNearLimit)
		if h.SnapshotBytes > 0 && h.SnapshotBytes < limit*4/5 {
			sawBelow = true
			if near {
				t.Fatalf("warned at %d of %d", h.SnapshotBytes, limit)
			}
		}
		if h.SnapshotBytes >= limit*4/5 {
			if !near {
				t.Fatalf("no warning at %d of %d", h.SnapshotBytes, limit)
			}
			if !sawBelow {
				t.Fatal("setup: no snapshot was seen below the threshold")
			}
			if !h.Flags.Has(HealthReady) {
				t.Fatal("the warning made the node unready")
			}
			return
		}
		if i > 400 {
			t.Fatalf("the snapshot never neared the limit: %d of %d", h.SnapshotBytes, limit)
		}
	}
}
