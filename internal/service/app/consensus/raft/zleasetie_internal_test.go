package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The lease rests on the newest round a majority acknowledged (D9): it
// starts at that round's send instant and lasts the shortest window among
// the voters that acknowledged it. Two rounds may leave at one instant, the
// turn's: a read flushed at the instant of a round already confirmed. The
// lease moved only to a round sent strictly later, so the newer of the two
// was ignored: a first round acknowledged by a follower that holds nothing
// durably committed, which reports generation 0 and sizes no lease (#698),
// left a lease of 0 at that instant, and the read confirmed at the same
// instant by followers of generation 1 left the leader with no lease at all
// (#1390, TestLeasedBarriersOpenNoRound's setup on a stopped clock). The
// newer round now sets the lease, as a later one does: its own window, never
// the longer of the two, so a newer round of a shorter window shortens it.
func TestTheNewestRoundSetsTheLeaseAtItsOwnInstant(t *testing.T) {
	type tc struct {
		name string
		// first and second are the generations the follower confirming
		// the first round, then the one confirming the second, report.
		first, second uint64
		// valid is whether a lease holds at the instant both rounds left.
		valid bool
	}
	cases := []tc{
		{name: "a newer round of generation 1 after one of generation 0", first: 0, second: 1, valid: true},
		{name: "a newer round of generation 0 after one of generation 1", first: 1, second: 0, valid: false},
	}
	runCase := func(t *testing.T, c tc) {
		r := leaderOf(2, 0, nil, 0, true)
		for _, id := range []core.NodeID{2, 3} {
			m := msg(core.MsgAppResp, id, 1, 2)
			m.Index = r.log.lastIndex()
			r.Step(r.now, m)
		}
		r.Ready()
		at := r.now
		r.broadcastHeartbeat(at)
		hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
		hb.Context, hb.Contract = r.nextRound, c.first
		r.Step(at, hb)
		r.Ready()
		if got := r.LeaseValid(at); got != (c.first != 0) {
			t.Fatalf("setup: a round confirmed by generation %d: lease %v", c.first, got)
		}
		// A read at the same instant: under a lease of 0 it waits for a
		// round of its own, sent at that instant.
		r.ReadIndex(at, 7, true)
		r.Ready()
		hb = msg(core.MsgHeartbeatResp, 3, 1, 2)
		hb.Context, hb.Contract = r.nextRound, c.second
		r.Step(at, hb)
		rd := r.Ready()
		if len(rd.ReadStates) != 1 || rd.ReadStates[0].Context != 7 || rd.ReadStates[0].Code != 0 {
			t.Fatalf("setup: the read of the second round: %+v", rd.ReadStates)
		}
		if got := r.LeaseValid(at); got != c.valid {
			t.Fatalf("after a round of generation %d then one of generation %d at one instant: lease %v (%v), want %v",
				c.first, c.second, got, r.leaseLen, c.valid)
		}
		// The newer round's own window, shortened by the drift budget,
		// and not a moment more.
		if end := at.Add(testTiming().ElectionTimeoutMin); r.LeaseValid(end) {
			t.Fatalf("the lease outlives the window it rests on: %v", r.leaseLen)
		}
		if c.valid {
			want := time.Duration(float64(testTiming().ElectionTimeoutMin) * (1 - testTiming().MaxClockDrift))
			if r.leaseLen != want || r.leaseStart != at {
				t.Fatalf("lease %v from %v, want %v from the rounds' instant", r.leaseLen, r.leaseStart.Sub(at), want)
			}
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
