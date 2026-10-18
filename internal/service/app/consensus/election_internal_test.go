package consensus

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// electionTiming is the timing of a deployed cluster: an election timeout
// of one to two seconds, ten heartbeats in the shortest.
func electionTiming() coreconsensus.TimingContractSpec {
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.05,
	}
}

// electionBound is how long the survivors of a killed leader may take to
// elect: one timeout for the round, one more for a split vote, and the
// tick's granularity.
func electionBound() time.Duration {
	return 2*electionTiming().ElectionTimeoutMax + 100*time.Millisecond
}

// TestLeaderKilledAgainAfterKillsRestartsAndPartitionsElectsInTime replays,
// on the memory network and in a synctest bubble, the chaos run that saw
// the survivors of a second leader kill stay pre-candidates for 15 s: the
// leader killed and restarted, a follower killed and restarted, quorum lost
// and regained, a follower then the leader partitioned and healed, and the
// leader killed again. Every leader kill must be followed by a new leader
// within electionBound, over many seeds.
func TestLeaderKilledAgainAfterKillsRestartsAndPartitionsElectsInTime(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) { chaosRun(t, seed) })
		})
	}
}

// chaosRun is one seed of the chaos run.
func chaosRun(t *testing.T, seed uint64) {
	net := NewMemoryNetwork()
	// Registered first, run last: once every node is closed, the delivery
	// goroutines end too, which the bubble requires.
	t.Cleanup(func() {
		for _, tr := range net.othersThan(0) {
			if err := tr.Close(); err != nil {
				t.Errorf("close transport: %v", err)
			}
		}
	})
	c := newClusterWith(t, &cluster{
		t: t, net: net, cid: coreconsensus.ClusterID{1, 2, 3},
		timing: electionTiming(), seed: seed * 1000,
	})
	settle := func() { time.Sleep(3 * time.Second) }
	l := c.settledLeader(10 * time.Second)
	proposeOnLeader(t, c, "a=1")

	// B1: the leader killed, then restarted.
	c.kill(l)
	electedWithin(t, c, "first leader kill")
	c.start(l)
	settle()

	// B3: a follower killed, then restarted.
	f := c.otherThan(c.leader(10 * time.Second))
	c.kill(f)
	proposeOnLeader(t, c, "b=2")
	c.start(f)
	settle()

	// B4: two of three killed, then one back.
	l = c.leader(10 * time.Second)
	f = c.otherThan(l)
	c.kill(f)
	c.kill(l)
	time.Sleep(5 * time.Second)
	c.start(f)
	c.leader(10 * time.Second)
	c.start(l)
	settle()

	// B5: a follower partitioned, then healed.
	f = c.otherThan(c.leader(10 * time.Second))
	c.net.Isolate(f.id, true)
	time.Sleep(5 * time.Second)
	c.net.Isolate(f.id, false)
	settle()

	// B6: the leader partitioned, then healed.
	l = c.leader(10 * time.Second)
	c.net.Isolate(l.id, true)
	time.Sleep(10 * time.Second)
	c.net.Isolate(l.id, false)
	settle()
	proposeOnLeader(t, c, "c=3")

	// B7: the leader killed again.
	c.kill(c.leader(10 * time.Second))
	electedWithin(t, c, "second leader kill")
}

// electedWithin waits for a leader among the running nodes and fails when
// it took longer than electionBound.
func electedWithin(t *testing.T, c *cluster, what string) {
	t.Helper()
	start := time.Now()
	c.leader(30 * time.Second)
	if took := time.Since(start); took > electionBound() {
		t.Fatalf("%s: a leader after %v, bound %v", what, took, electionBound())
	}
}
