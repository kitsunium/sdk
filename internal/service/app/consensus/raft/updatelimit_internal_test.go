package raft

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// limitChange is the payload of a ChangeSnapshotLimit naming target with
// limit, or, with change, of another change kind carrying the same member.
func limitChange(change uint8, target core.NodeID, limit int64) []byte {
	return wire.AppendAdmin(nil, &wire.AdminValue{
		Operator: "engine:snapshot-limit", Change: change, Target: target,
		Membership: core.MembershipValue{Members: []core.MemberValue{{ID: target, MaxSnapshotBytes: limit}}},
	})
}

// proposeLimit proposes, at leader r, target's limit, its origin r itself.
func proposeLimit(r *Raft, target core.NodeID, limit int64) error {
	d := limitChange(wire.ChangeSnapshotLimit, target, limit)
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(d), Data: d}}); code != 0 {
		return fmt.Errorf("refused: %v", code)
	}
	return nil
}

// forwardLimit has from forward to leader r, under ctx, target's limit, and
// returns r's answer.
func forwardLimit(r *Raft, from, target core.NodeID, limit int64, ctx uint64) (core.MessageValue, error) {
	d := limitChange(wire.ChangeSnapshotLimit, target, limit)
	m := msg(core.MsgForwardAdmin, from, 1, r.term)
	m.Context = ctx
	m.Entries = []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(d), Data: d}}
	r.Step(r.now, m)
	as := forwardAnswers(r, ctx)
	if len(as) != 1 {
		return core.MessageValue{}, fmt.Errorf("the forward %d was answered %d times", ctx, len(as))
	}
	return as[0], nil
}

// limitLeader is ackedLeader with voter 3 heard as well, so a change has an
// answering majority without either follower, and the no-space alarm
// committed: the cluster of #677, whose every write was refused NO_SPACE,
// still admits the limit that repairs it.
func limitLeader(mut mutation) *Raft {
	r := ackedLeader(mut)
	r.Step(r.now, msg(core.MsgHeartbeatResp, 3, 1, 2))
	r.Ready()
	r.noSpace = core.AlarmID(r.log.lastIndex())
	return r
}

// recordedLimit is the limit r's latest configuration records for id.
func recordedLimit(r *Raft, id core.NodeID) int64 {
	m, _ := r.conf().Member(id)
	return m.MaxSnapshotBytes
}

// checkLimitFromItsMemberOnly: only a member knows the limit it runs with,
// and a leader records one only from that member, the origin the transport
// authenticated. A leader recording another member's limit, its own word or
// a third member's, would hold every cut of every node to a limit the
// member never stated (#677).
func checkLimitFromItsMemberOnly(mut mutation) error {
	r := limitLeader(mut)
	last := r.log.lastIndex()
	if err := proposeLimit(r, 2, 1<<30); err == nil {
		return errors.New("the leader recorded the snapshot limit of member 2 on its own word")
	}
	if a, err := forwardLimit(r, 3, 2, 1<<30, 11); err != nil || !a.Reject || a.Code != codeMembershipInvalid {
		return fmt.Errorf("member 3 stating member 2's snapshot limit was answered %+v (%v), want MEMBERSHIP_INVALID", a, err)
	}
	if r.log.lastIndex() != last || recordedLimit(r, 2) != 0 {
		return fmt.Errorf("a refused limit was appended: last %d -> %d, recorded %d", last, r.log.lastIndex(), recordedLimit(r, 2))
	}
	if a, err := forwardLimit(r, 2, 2, 1<<30, 12); err != nil || a.Reject {
		return fmt.Errorf("member 2 stating its own snapshot limit under the no-space alarm was answered %+v (%v)", a, err)
	}
	if got := recordedLimit(r, 2); got != 1<<30 {
		return fmt.Errorf("member 2's limit is recorded as %d, want %d", got, 1<<30)
	}
	return nil
}

// checkLimitAboveZero: a limit of 0 records no limit at all, which lifts
// every bound on what the member is offered, and a negative one is no
// limit: both are refused in place of a recorded one (#677).
func checkLimitAboveZero(mut mutation) error {
	r := limitLeader(mut)
	if a, err := forwardLimit(r, 2, 2, 4096, 19); err != nil || a.Reject {
		return fmt.Errorf("setup: member 2 stating its own limit was answered %+v (%v)", a, err)
	}
	commitLast(r)
	for i, limit := range []int64{0, -1} {
		if a, err := forwardLimit(r, 2, 2, limit, uint64(20+i)); err != nil || !a.Reject || a.Code != codeMembershipInvalid {
			return fmt.Errorf("a snapshot limit of %d was answered %+v (%v), want MEMBERSHIP_INVALID", limit, a, err)
		}
	}
	return nil
}

// checkLimitThatChanges: the limit already recorded is refused, so a member
// whose view of the configuration lags appends no entry for nothing; another
// one is recorded (#677).
func checkLimitThatChanges(mut mutation) error {
	r := limitLeader(mut)
	if err := proposeLimit(r, 1, 4096); err != nil {
		return fmt.Errorf("setup: the leader stating its own limit: %w", err)
	}
	commitLast(r)
	last := r.log.lastIndex()
	if r.log.committed != last {
		return fmt.Errorf("setup: the change did not commit (%d < %d)", r.log.committed, last)
	}
	if err := proposeLimit(r, 1, 4096); err == nil || r.log.lastIndex() != last {
		return fmt.Errorf("the limit already recorded was appended again (last %d -> %d)", last, r.log.lastIndex())
	}
	if err := proposeLimit(r, 1, 8192); err != nil || recordedLimit(r, 1) != 8192 {
		return fmt.Errorf("a changed limit was %v, recorded %d", err, recordedLimit(r, 1))
	}
	return nil
}

func TestASnapshotLimitIsRecordedFromItsMemberOnly(t *testing.T) {
	if err := checkLimitFromItsMemberOnly(0); err != nil {
		t.Fatal(err)
	}
}

func TestASnapshotLimitOfZeroIsRefused(t *testing.T) {
	if err := checkLimitAboveZero(0); err != nil {
		t.Fatal(err)
	}
}

func TestTheSnapshotLimitAlreadyRecordedIsRefused(t *testing.T) {
	if err := checkLimitThatChanges(0); err != nil {
		t.Fatal(err)
	}
}

// A release before #677 knows no ChangeSnapshotLimit, and the change keeps
// the layout of every other one (no protocol raise, ADR 0152 amendment of
// 2026-10-25). As a leader such a release refuses it MEMBERSHIP_INVALID in
// nextConf's default branch, as this one refuses a kind no release knows;
// as a follower it adopts the configuration the entry carries, which every
// reader takes from the payload whatever its change kind (noteAppended).
func TestAnOlderReleaseRefusesTheLimitAsLeaderAndAdoptsItAsFollower(t *testing.T) {
	r := limitLeader(0)
	unknown := limitChange(wire.ChangeSnapshotLimit+1, 1, 1<<30)
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(unknown), Data: unknown}}); code != codeMembershipInvalid {
		t.Fatalf("a change kind no release knows was answered %v, want MEMBERSHIP_INVALID", code)
	}
	if err := proposeLimit(r, 1, 1<<30); err != nil {
		t.Fatal(err)
	}
	// Member 2, empty, is sent the leader's whole log.
	app := msg(core.MsgApp, 1, 2, r.term)
	app.Entries = slices.Clone(r.log.ents)
	f := newTest(2, testState{term: 1}, 0, false)
	f.Step(r.now, app)
	f.Ready()
	if f.log.lastIndex() != r.log.lastIndex() {
		t.Fatalf("setup: the follower holds %d entries, the leader %d", f.log.lastIndex(), r.log.lastIndex())
	}
	if got := recordedLimit(f, 1); got != 1<<30 {
		t.Fatalf("the follower holds the leader's limit as %d once the change is appended, want %d", got, 1<<30)
	}
}
