package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// winCampaign makes candidate c, of term, a leader: its own vote durable,
// then node 3's grant.
func winCampaign(t *testing.T, c *Raft, now time.Time) {
	t.Helper()
	if c.role != core.RoleCandidate {
		t.Fatalf("setup: role %v, want a candidate", c.role)
	}
	rd := c.Ready()
	c.StorageDone(rd.Seq, c.log.lastIndex(), c.log.lastTerm())
	c.Step(now, msg(core.MsgVoteResp, 3, 2, c.term))
	if c.role != core.RoleLeader {
		t.Fatalf("setup: role %v after a quorum's grant, want the leader", c.role)
	}
}

// The status names the leader whose TimeoutNow made a leader campaign,
// while it leads, and no one for a term won by an election or once it
// steps down: the watch of PreferredLeaders pauses only after a transfer
// (#676).
func TestTheStatusNamesTheLeaderThatHandedTheTermOver(t *testing.T) {
	f, now := followerOfOne(0, nil)
	f.Step(now, msg(core.MsgTimeoutNow, 1, 2, 2))
	if got := f.Status().HandedBy; got != 0 {
		t.Fatalf("a candidate's status names %d as the leader that handed it the term", got)
	}
	winCampaign(t, f, now)
	if got := f.Status().HandedBy; got != 1 {
		t.Fatalf("a leader elected by node 1's TimeoutNow names %d, want 1", got)
	}
	f.Step(now, msg(core.MsgHeartbeat, 3, 2, f.term+1))
	if st := f.Status(); st.Role != core.RoleFollower || st.HandedBy != 0 {
		t.Fatalf("after stepping down: role %v, handed by %d", st.Role, st.HandedBy)
	}

	e, now := followerOfOne(0, nil)
	for e.role != core.RolePreCandidate {
		now = now.Add(100 * time.Millisecond)
		e.Tick(now)
	}
	e.Step(now, core.MessageValue{Version: 1, Group: 1, Kind: core.MsgPreVoteResp, From: 3, To: 2, Term: e.term + 1})
	winCampaign(t, e, now)
	if got := e.Status().HandedBy; got != 0 {
		t.Fatalf("a leader elected on its timer names %d as the leader that handed it the term", got)
	}
}
