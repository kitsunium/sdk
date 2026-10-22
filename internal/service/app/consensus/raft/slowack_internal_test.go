package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// sentTo counts, per index, the entries rd carries to peer to.
func sentTo(rd *ReadyValue, to core.NodeID, sent map[uint64]int) {
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.Kind != core.MsgApp || m.To != to {
			continue
		}
		for _, e := range m.Entries {
			sent[e.Position.Index]++
		}
	}
}

// slowAckLeader is a leader of term 2 whose peer 2 replicates from index
// 1, with the entries 2 to 5 in flight to it, each sent once.
func slowAckLeader(t testing.TB) (*Raft, map[uint64]int) {
	t.Helper()
	l := leaderOf(2, 0, nil, 0, false)
	for _, from := range []core.NodeID{2, 3} {
		ack := msg(core.MsgAppResp, from, 1, 2)
		ack.Index = 1
		l.Step(l.now, ack)
	}
	l.Ready()
	sent := map[uint64]int{}
	for i := range 4 {
		d := []byte{byte('a' + i)}
		if _, code := l.Propose(l.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}}); code != 0 {
			t.Fatalf("propose %d: %v", i, code)
		}
		rd := l.Ready()
		sentTo(&rd, 2, sent)
		l.StorageDone(rd.Seq, l.log.lastIndex(), 2)
	}
	if pr := l.prs[2]; pr.state != stateReplicate || pr.match != 1 || len(pr.inflight) != 4 {
		t.Fatalf("setup: peer 2 %+v, want replicate, match 1, 4 appends in flight", *pr)
	}
	return l, sent
}

// heartbeatAnswers delivers n answers of peer 2 to heartbeats, each one
// reporting it holds through held, and counts what the leader sends it.
func heartbeatAnswers(l *Raft, n int, held uint64, sent map[uint64]int) {
	for range n {
		l.now = l.now.Add(100 * time.Millisecond)
		hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
		hb.Index = held
		l.Step(l.now, hb)
		rd := l.Ready()
		sentTo(&rd, 2, sent)
	}
}

// #560: the lost-append detection on heartbeats took a slow
// acknowledgement for a lost append. A heartbeat answer leaves the
// follower at once, its append acknowledgement only after its fdatasync,
// so two answers with no match movement moved the peer to probe and the
// leader resent, then refilled, a window the follower already held. The
// follower now reports in its heartbeat answer the index it holds of this
// term, and the leader probes only when that is below the oldest append
// in flight.
func TestASlowAcknowledgementIsNotALostAppend(t *testing.T) {
	l, sent := slowAckLeader(t)
	heartbeatAnswers(l, 2, 5, sent)
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = 5
	l.Step(l.now, ack)
	rd := l.Ready()
	sentTo(&rd, 2, sent)
	for idx := uint64(2); idx <= 5; idx++ {
		if sent[idx] != 1 {
			t.Fatalf("entry %d sent %d times to a follower that held it all along (sent %v)", idx, sent[idx], sent)
		}
	}
	if pr := l.prs[2]; pr.state != stateReplicate || pr.match != 5 {
		t.Fatalf("peer 2 %+v, want replicate with match 5", *pr)
	}
}

// A heartbeat answer that reports nothing (0, a follower that predates
// the report) or an index below the oldest append in flight still finds
// a lost append, as before (R1-11).
func TestALostAppendIsStillProbedOnHeartbeats(t *testing.T) {
	for _, held := range []uint64{0, 1} {
		l, sent := slowAckLeader(t)
		heartbeatAnswers(l, 2, held, sent)
		if pr := l.prs[2]; pr.state != stateProbe || pr.next != 2 {
			t.Fatalf("report %d: peer 2 %+v, want probe from 2", held, *pr)
		}
		if sent[2] != 2 {
			t.Fatalf("report %d: entry 2 sent %d times, want resent once on the probe", held, sent[2])
		}
	}
}

// The follower reports its last index in its heartbeat answer when that
// entry is of the leader's term (it then holds the leader's log through
// it), and 0 otherwise.
func TestAHeartbeatAnswerReportsTheIndexHeldOfTheTerm(t *testing.T) {
	cases := []struct {
		name string
		ents []core.EntryValue
		term uint64
		want uint64
	}{
		{"entries of the term", chainEntries(0, 1, 1, 2, 2), 2, 3},
		{"no entry of the term", chainEntries(0, 1, 1, 1), 2, 0},
		{"empty log", nil, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTest(2, testState{term: tc.term, ents: tc.ents}, 0, false)
			f.Step(t0.Add(time.Second), msg(core.MsgHeartbeat, 1, 2, tc.term))
			rd := f.Ready()
			for _, m := range append(rd.Messages, rd.AfterSync...) {
				if m.Kind == core.MsgHeartbeatResp {
					if m.Index != tc.want {
						t.Fatalf("heartbeat answer reports %d, want %d", m.Index, tc.want)
					}
					return
				}
			}
			t.Fatal("no heartbeat answer")
		})
	}
}

// BenchmarkSlowAcknowledgementWindow measures the entries sent per distinct
// entry when two heartbeat answers come before a slow acknowledgement of a
// window of four appends (#560): 2 before the fix, 1 after.
func BenchmarkSlowAcknowledgementWindow(b *testing.B) {
	var sends, distinct int
	for b.Loop() {
		l, sent := slowAckLeader(b)
		heartbeatAnswers(l, 2, 5, sent)
		ack := msg(core.MsgAppResp, 2, 1, 2)
		ack.Index = 5
		l.Step(l.now, ack)
		rd := l.Ready()
		sentTo(&rd, 2, sent)
		for _, n := range sent {
			sends += n
		}
		distinct += len(sent)
	}
	b.ReportMetric(float64(sends)/float64(distinct), "sends/entry")
}
