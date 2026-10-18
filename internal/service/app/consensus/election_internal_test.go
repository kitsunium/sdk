package consensus

import (
	"fmt"
	"math"
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

// The bound on an election after a leader kill is derived from the timing
// contract, round by round, rather than written down as a total.
//
// A round is one real vote, at a term of its own. The survivors heard the
// killed leader last at or before the kill, and each armed its election
// deadline then, at most ElectionTimeoutMax later; the loop sees a deadline
// fall due within one tick (HeartbeatInterval/5). A PreVote refused because
// its peer heard the leader later costs no round: the refuser's own deadline
// falls within the same ElectionTimeoutMax, and the first one, past its
// window by then, grants it. A round fails only by a split vote — both
// survivors campaigning on one tick, each voting for itself — and both
// re-arm within ElectionTimeoutMax. So an election the winner's term says
// took r rounds is due within r × (ElectionTimeoutMax + tick) of the kill,
// plus the interval at which cluster.leader polls.
//
// The number of rounds is a draw, and it is bounded too. On the memory
// network, inside the bubble, a message is delivered before the clock
// moves, so two survivors split only when their deadlines fall due on the
// same tick: at most 2·tick/(ElectionTimeoutMax−ElectionTimeoutMin) per
// round, 4 % with electionTiming (2,400 elections of this run measured
// 1.2 %). maxRounds allows consecutive splits until their odds fall under
// splitOddsFloor: seven rounds with electionTiming. The bound this test
// first had, one split at most, failed every ten to twenty runs: three
// rounds took 4.44 s, 4.58 s and 4.88 s against its 4.1 s.

// leaderPoll is the interval at which cluster.leader looks for a leader.
const leaderPoll = 5 * time.Millisecond

// splitOddsFloor is the odds below which a run of consecutive split votes
// is not expected in any election of any run of the suite.
const splitOddsFloor = 1e-7

// electionTick is how late the loop can see a deadline: its tick.
func electionTick() time.Duration { return electionTiming().HeartbeatInterval / 5 }

// electionBound is how long an election of rounds rounds may take after
// the leader's kill.
func electionBound(rounds uint64) time.Duration {
	return time.Duration(rounds)*(electionTiming().ElectionTimeoutMax+electionTick()) + leaderPoll
}

// maxRounds is the most rounds an election may take: one, and as many
// consecutive split votes as it takes for their odds to fall under
// splitOddsFloor.
func maxRounds() uint64 {
	tm := electionTiming()
	split := 2 * float64(electionTick()) / float64(tm.ElectionTimeoutMax-tm.ElectionTimeoutMin)
	return 1 + uint64(math.Ceil(math.Log(splitOddsFloor)/math.Log(split)))
}

// TestLeaderKilledAgainAfterKillsRestartsAndPartitionsElectsInTime replays,
// on the memory network and in a synctest bubble, the chaos run that saw
// the survivors of a second leader kill stay pre-candidates for 15 s: the
// leader killed and restarted, a follower killed and restarted, quorum lost
// and regained, a follower then the leader partitioned and healed, and the
// leader killed again. Every leader kill must be followed by a new leader
// within the rounds maxRounds allows, and within electionBound of the
// rounds it took, over many seeds.
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
	electedAfterKill(t, c, l, "first leader kill")
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
	electedAfterKill(t, c, c.leader(10*time.Second), "second leader kill")
}

// electedAfterKill kills the leader l, waits for a leader among the
// survivors, and fails when the election took more rounds than maxRounds,
// or longer than electionBound of the rounds it took.
func electedAfterKill(t *testing.T, c *cluster, l *testNode, what string) {
	t.Helper()
	before := l.node.Status().Term
	c.kill(l)
	start := time.Now()
	w := c.leader(30 * time.Second)
	took := time.Since(start)
	rounds := w.node.Status().Term - before
	if rounds > maxRounds() {
		t.Fatalf("%s: a leader after %d rounds, at most %d", what, rounds, maxRounds())
	}
	if took > electionBound(rounds) {
		t.Fatalf("%s: a leader after %v in %d rounds, bound %v", what, took, rounds, electionBound(rounds))
	}
}
