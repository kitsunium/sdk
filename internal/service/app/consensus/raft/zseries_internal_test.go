package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// heartbeatRound ticks the leader l to its next heartbeat and returns the
// round it opened and when, read from the heartbeat to peer 2.
func heartbeatRound(t *testing.T, l *Raft, now time.Time) (uint64, time.Time) {
	t.Helper()
	//: tick until a heartbeat to peer 2 leaves, or a second passes.
	for range 100 {
		now = now.Add(10 * time.Millisecond)
		l.Tick(now)
		rd := l.Ready()
		//: the heartbeat names its round in Context.
		for _, m := range rd.Messages {
			//: only peer 2's heartbeat is read.
			if m.Kind == core.MsgHeartbeat && m.To == 2 {
				return m.Context, now
			}
		}
	}
	t.Fatal("setup: no heartbeat to peer 2 within a second")
	return 0, now
}

// peerStat is the leader's view of peer id.
func peerStat(t *testing.T, l *Raft, id core.NodeID) PeerStatValue {
	t.Helper()
	//: the stat of id among the leader's peers.
	for _, p := range l.PeerStats() {
		//: the one asked for.
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("peer %d missing from the leader's stats", id)
	return PeerStatValue{}
}

// D13 (#914): the round trip of a heartbeat round is measured on the
// peer's first answer to it, from the round's broadcast: the series
// consensus_peer_rtt_seconds{peer} is recorded from it. A duplicate, or an
// answer a later one overtook, measures nothing.
func TestAHeartbeatAnswerMeasuresItsRoundTrip(t *testing.T) {
	l := leaderOf(2, 0, nil, 0, false)
	if p := peerStat(t, l, 2); p.RTT != 0 || p.RTTRound != 0 {
		t.Fatalf("before any answer: RTT %v round %d, want none", p.RTT, p.RTTRound)
	}
	round, sent := heartbeatRound(t, l, l.now)
	answer := msg(core.MsgHeartbeatResp, 2, 1, 2)
	answer.Context = round
	l.Step(sent.Add(37*time.Millisecond), answer)
	if p := peerStat(t, l, 2); p.RTT != 37*time.Millisecond || p.RTTRound != round {
		t.Fatalf("after the answer: RTT %v round %d, want 37ms round %d", p.RTT, p.RTTRound, round)
	}
	l.Step(sent.Add(90*time.Millisecond), answer)
	if p := peerStat(t, l, 2); p.RTT != 37*time.Millisecond {
		t.Fatalf("a duplicate answer measured %v, want the first answer's 37ms kept", p.RTT)
	}
	older, at := heartbeatRound(t, l, sent)
	newer, at2 := heartbeatRound(t, l, at)
	answer.Context = newer
	l.Step(at2.Add(5*time.Millisecond), answer)
	answer.Context = older
	l.Step(at2.Add(8*time.Millisecond), answer)
	if p := peerStat(t, l, 2); p.RTTRound != newer || p.RTT != 5*time.Millisecond {
		t.Fatalf("an overtaken answer: RTT %v round %d, want 5ms round %d", p.RTT, p.RTTRound, newer)
	}
}

// D13 (#914): consensus_prevotes_lost_total counts the PreVote refusals a
// campaign is answered, once per voter: a duplicate refusal is not a
// second loss.
func TestAPreVoteRefusalCountsOncePerVoterAndCampaign(t *testing.T) {
	f, now := followerOfOne(0, nil)
	//: tick until the follower's PreVote campaign starts.
	for f.role != core.RolePreCandidate {
		now = now.Add(100 * time.Millisecond)
		f.Tick(now)
	}
	refusal := core.MessageValue{Version: 1, Group: 1, Kind: core.MsgPreVoteResp, From: 3, To: 2, Term: f.term, Reject: true}
	f.Step(now, refusal)
	f.Step(now, refusal)
	if got := f.Status().PreVotesLost; got != 1 {
		t.Fatalf("one voter's refusal, twice: %d lost, want 1", got)
	}
	grant := refusal
	grant.From, grant.Reject = 1, false
	if f.role != core.RolePreCandidate {
		t.Fatalf("setup: role %v after one refusal of three voters, want still a pre-candidate", f.role)
	}
	f.Step(now, grant)
	if got := f.Status().PreVotesLost; got != 1 {
		t.Fatalf("a grant counted as a loss: %d lost, want 1", got)
	}
}

// D13 (#914): in the simulator, whose links have known round trips, the
// leader measures each peer's within the jitter of the network model
// (2 ms each way) and one tick of the loop (10 ms).
func TestTheSimulatorMeasuresEachPeersRoundTrip(t *testing.T) {
	s := newSim(simOptions{seed: 914})
	s.run(s.after(5*time.Second), false)
	l := s.leader()
	if l == nil {
		t.Fatal("no leader after 5 s")
	}
	s.run(s.after(time.Second), false)
	stats := l.r.PeerStats()
	if len(stats) != len(s.ids)-1 {
		t.Fatalf("leader %d has %d peer stats, want %d", l.id, len(stats), len(s.ids)-1)
	}
	//: every peer's round trip is the model's, within the jitter and a tick.
	for _, p := range stats {
		want := rtt(l.id, p.ID)
		if p.RTTRound == 0 || p.RTT < want || p.RTT > want+4*time.Millisecond+10*time.Millisecond {
			t.Errorf("peer %d: RTT %v round %d, want %v within 14ms", p.ID, p.RTT, p.RTTRound, want)
		}
	}
}
