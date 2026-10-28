package raft

import (
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A joining node's transport admits, apart from its engine's set, the
// members its JOIN answer lists, as of the AddLearner it answers: a member
// both list is taken from the newer (#742). The Ready says what the engine's
// set is as of: its latest configuration, appended or the base's, or the
// commit a leader's record it took was read at, which is past that
// configuration (#724).
func TestTheAdmissionSetSaysWhatItIsAsOf(t *testing.T) {
	start := func(st *StartState) ReadyValue {
		st.Now = t0
		return NewRaft(&Config{
			ID: 3, Group: 1, Seed: 3, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
			MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, MaxChainRefusals: 3,
		}, st).Ready()
	}
	h := movedHistory(true)
	if rd := start(&StartState{
		HardState: core.HardState{Term: 1, Commit: 4}, Snapshot: core.SnapshotMetaValue{Membership: movedConf("a1", true)}, Entries: slices.Clone(h),
	}); len(rd.Membership) == 0 || rd.MembershipAt != 3 {
		t.Fatalf("a start whose latest configuration is the UpdateAddress at 3 hands its set as of %d (%d members)", rd.MembershipAt, len(rd.Membership))
	}
	if rd := start(&StartState{HardState: core.HardState{Term: 1, Commit: 4}, Snapshot: core.SnapshotMetaValue{
		Position: core.PositionValue{Term: 1, Index: 4}, Membership: movedConf("a1-moved", true),
	}}); len(rd.Membership) == 0 || rd.MembershipAt != 4 {
		t.Fatalf("a start on a snapshot at 4 hands its set as of %d (%d members)", rd.MembershipAt, len(rd.Membership))
	}
	r := movedMember(3, h[:2], 2, true, Protocol2, 0)
	now := t0.Add(10 * time.Second)
	hb := msg(core.MsgHeartbeat, 1, 3, 2)
	hb.Commit = 2
	r.Step(now, hb)
	r.Ready()
	r.ReportUnreachable(1)
	r.Tick(now)
	r.Ready()
	moved := core.MemberValue{ID: 1, SPKI: [32]byte{1}, Address: "a1-moved", Voter: true}
	r.Step(now, addrRecord(2, 2, 5, moved))
	rd := r.Ready()
	if !slices.ContainsFunc(rd.Membership, func(m core.MemberValue) bool { return m.ID == 1 && m.Address == "a1-moved" }) || rd.MembershipAt != 5 {
		t.Fatalf("with the leader's record read at 5 taken, the Ready hands %+v as of %d, want node 1 at a1-moved as of 5", rd.Membership, rd.MembershipAt)
	}
}
