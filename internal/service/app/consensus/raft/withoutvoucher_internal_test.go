package raft

import (
	"slices"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #773, option (c): a DenyPin is admitted whatever it leaves a stopped voter
// with, and the leader says it instead. Founders 1, 2 and 3; members 4, 5, 6
// and 7 join and are promoted while 3 is down, so 3 knows {1, 2, 3}; 4
// leads. In the reverse order, 2's removal is admitted while 1, pinned,
// vouches for 3, then 1's pin is denied: 3 is left with no voucher. The
// DenyPin stays admitted; Isolated names 3 and the withdrawn pin, and so
// does every later refusal the guard decides because of 3 (GuardRefusal),
// a removal or a move. In the forward order, the DenyPin leaves 3 reached
// through 2, and the removal of 2 is refused for what it would itself do:
// no withdrawn pin is named.
func TestAStoppedVoterADenyPinLeftWithNoVoucherIsNamedWithItsCause(t *testing.T) {
	stranded := []IsolationValue{{ID: 3, Flags: IsolationStranded | IsolationPinDenied}}
	history := func() []core.EntryValue {
		h := unlistedHistory()
		conf := testConf(1, 2, 3, 4)
		//: members 5, 6 and 7 join and are promoted, one after the other.
		for _, id := range []core.NodeID{5, 6, 7} {
			learner := conf
			learner.Members = append(slices.Clone(conf.Members), core.MemberValue{ID: id, SPKI: [32]byte{byte(id)}})
			last := h[len(h)-1]
			add := engineEntry(last.ChainSum, last.Position.Index+1, core.EntryMembership, &wire.AdminValue{
				Operator: "op:add", Change: wire.ChangeAddLearner, Target: id, Membership: learner,
			})
			conf = testConf(append(slices.Clone(conf.Voters()), id)...)
			promote := engineEntry(add.ChainSum, add.Position.Index+1, core.EntryMembership, &wire.AdminValue{
				Operator: "op:promote", Change: wire.ChangePromote, Target: id, Membership: conf,
			})
			h = append(h, add, promote)
		}
		return h
	}
	start := func(t *testing.T) (*unlistedNet, *Raft, func()) {
		h := history()
		commit := h[len(h)-1].Position.Index
		nodes := []*Raft{unlistedMember(1, h, commit, 0, Protocol2, 0), unlistedMember(2, h, commit, 0, Protocol2, 0), unlistedMember(3, h[:2], 0, 0, Protocol2, 0)}
		//: members 4 to 7 hold the whole history.
		for _, id := range []core.NodeID{4, 5, 6, 7} {
			nodes = append(nodes, unlistedMember(id, h, commit, 0, Protocol2, 0))
		}
		n := newUnlistedNet(t, nodes...)
		n.down[3] = true
		n.elect(4)
		settle := func() { n.tick(2 * testTiming().ElectionTimeoutMax) }
		settle()
		return n, n.nodes[4], settle
	}
	// denyPin has r withdraw 1's pin, which every member then refuses (#551).
	denyPin := func(t *testing.T, n *unlistedNet, r *Raft, settle func()) {
		//: the DenyPin is admitted, whatever 3 is left with.
		if err := proposeDenyPin(r, 1); err != nil {
			t.Fatalf("DenyPin(1) = %v, want it admitted: a key's fence never waits for a voter that is down", err)
		}
		settle()
		n.down[1] = true
		settle()
	}
	t.Run("reverse order: the DenyPin leaves 3 no voucher", func(t *testing.T) {
		n, r4, settle := start(t)
		//: 1, pinned, vouches for 3.
		if err := proposeRemove(r4, 2); err != nil {
			t.Fatalf("setup: Remove(2) while 1 vouches for 3 = %v", err)
		}
		settle()
		//: 3 is reached through 1: nothing to say.
		if got := r4.Isolated(); len(got) != 0 {
			t.Fatalf("setup: Isolated = %v before 1's pin was denied", got)
		}
		denyPin(t, n, r4, settle)
		//: the leader must name 3, and the withdrawn pin.
		if got := r4.Isolated(); !slices.Equal(got, stranded) {
			t.Errorf("Isolated = %v, want %v", got, stranded)
		}
		//: a removal the guard refuses because of 3 names it.
		if err := proposeRemove(r4, 7); err == nil || !slices.Equal(r4.GuardRefusal(), stranded) {
			t.Errorf("Remove(7) = %v, GuardRefusal = %v: want refused, naming %v", err, r4.GuardRefusal(), stranded)
		}
		//: a move the guard refuses because of 3 names it.
		if code := proposeMove(r4, 5, "elsewhere"); code != codeMembershipInvalid || !slices.Equal(r4.GuardRefusal(), stranded) {
			t.Errorf("UpdateAddress(5) = %v, GuardRefusal = %v: want MEMBERSHIP_INVALID, naming %v", code, r4.GuardRefusal(), stranded)
		}
		// The way out: 3, down, is removed; no guard judges that removal.
		//: the dead voter's removal is admitted, and names nothing.
		if err := proposeRemove(r4, 3); err != nil || r4.GuardRefusal() != nil {
			t.Fatalf("Remove(3) = %v, GuardRefusal = %v: want it admitted", err, r4.GuardRefusal())
		}
		//: 3, gone, is named no more.
		if got := r4.Isolated(); slices.ContainsFunc(got, func(v IsolationValue) bool { return v.ID == 3 }) {
			t.Errorf("Isolated = %v once 3 was removed", got)
		}
	})
	t.Run("forward order: the removal would strand 3", func(t *testing.T) {
		n, r4, settle := start(t)
		denyPin(t, n, r4, settle)
		//: 2 still vouches for 3.
		if got := r4.Isolated(); len(got) != 0 {
			t.Errorf("Isolated = %v, while 2, pinned, vouches for 3", got)
		}
		want := []IsolationValue{{ID: 3, Flags: IsolationStranded}}
		//: the removal of 2 is refused for what it would itself do.
		if err := proposeRemove(r4, 2); err == nil || !slices.Equal(r4.GuardRefusal(), want) {
			t.Errorf("Remove(2) = %v, GuardRefusal = %v: want refused, naming %v and no withdrawn pin", err, r4.GuardRefusal(), want)
		}
		//: a refusal on another ground names nothing.
		if err := proposeRemove(r4, 99); err == nil || r4.GuardRefusal() != nil {
			t.Errorf("Remove(99) = %v, GuardRefusal = %v: want refused, the guard not involved", err, r4.GuardRefusal())
		}
	})
}
