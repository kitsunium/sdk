package raft

import (
	"slices"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #773: #603's guard counted as a stale voter's voucher a member whose pin
// DenyPin withdrew. vouched asked only that the voucher stay in next at the
// address the stale voter knows; a member whose pin was denied stays there,
// under the zero pin. Once the change commits every member refuses it
// (#551): it hears no leader and answers no PreVote with a record. The
// removal of the last other member the stale voter knew was admitted on its
// count, and the voter came back hearing no one, refused by the leader's
// key, one voter short for as long as that leader led.

// The exact branch: a voucher counts only under the non-zero key the stale
// voter's configuration pins for it, and that next still pins; the leader
// as well.
func TestVouchedNeedsTheVoucherUnderTheKeyTheStaleVoterPins(t *testing.T) {
	type tc struct {
		name        string
		known, next core.MembershipValue
		leader      core.NodeID
		want        bool
		// why is what the stale voter's transport does with the voucher.
		why string
	}
	denied := func(m core.MembershipValue, ids ...core.NodeID) core.MembershipValue {
		m.Members = slices.Clone(m.Members)
		//: each member named loses its pin.
		for i := range m.Members {
			//: a named member's pin is withdrawn.
			if slices.Contains(ids, m.Members[i].ID) {
				m.Members[i].SPKI = [32]byte{}
			}
		}
		return m
	}
	runCase := func(t *testing.T, c tc) {
		//: vouched must answer as the stale voter's transport would.
		if got := vouched(&c.next, &c.known, 1, c.leader); got != c.want {
			t.Errorf("vouched = %v, want %v: %s", got, c.want, c.why)
		}
	}
	//: each configuration pair the stale voter 1 may meet.
	for _, c := range []tc{
		{
			name: "the voucher's pin denied in next", known: testConf(1, 2, 3), next: denied(testConf(1, 2, 4), 2), leader: 4,
			why: "voter 2 stays at its address, but under the zero pin every member refuses it: it vouches for no one",
		},
		{
			name: "the voucher's pin denied in the stale voter's own configuration", known: denied(testConf(1, 2, 3), 2), next: denied(testConf(1, 2, 4), 2), leader: 4,
			why: "voter 1 pins voter 2 to the zero key, which admits nobody",
		},
		{
			name: "the voucher under another key in next", known: testConf(1, 2, 3), next: func() core.MembershipValue {
				m := testConf(1, 2, 4)
				m.Members[1].SPKI = [32]byte{9}
				return m
			}(), leader: 4,
			why: "voter 1 admits voter 2 under the key it pins, not under another",
		},
		{
			name: "the leader's pin denied in the stale voter's configuration", known: denied(testConf(1, 2, 4), 4), next: denied(testConf(1, 4, 5), 4), leader: 4,
			why: "voter 1 refuses the leader's connections under the zero pin it holds for it",
		},
		{
			name: "the voucher still pinned", known: testConf(1, 2, 3), next: testConf(1, 2, 4), leader: 4, want: true,
			why: "voter 2 stays under the key voter 1 pins: it answers the PreVote with the leader's record",
		},
		{
			name: "another voucher pinned beside a denied one", known: testConf(1, 2, 3), next: denied(testConf(1, 2, 3, 4), 2), leader: 4, want: true,
			why: "voter 3 stays under its key: one voucher is enough",
		},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The issue's scenario on the memory network's admission model. Founders
// 1, 2 and 3; members 4, 5 and 6 join and are promoted while 3 is down, so
// 3 knows {1, 2, 3}; 4 leads. With 1's pin denied, the removal of 2, the
// last member 3 reaches under a key it pins, is refused while 3 is down:
// 3 comes back, asks 2 for the record of leader 4 (#542), catches up, and
// 2's removal is admitted then. With 1 pinned, 1 vouches for 3, 2's
// removal is admitted while 3 is down, and 3 catches up through 1.
func TestARemovalCountsNoMemberWhosePinWasDeniedAsAVoucher(t *testing.T) {
	type tc struct {
		name string
		// deny has leader 4 withdraw member 1's pin before the removal.
		deny bool
	}
	history := func() []core.EntryValue {
		h := unlistedHistory()
		conf := testConf(1, 2, 3, 4)
		//: members 5 and 6 join and are promoted, one after the other.
		for _, id := range []core.NodeID{5, 6} {
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
	runCase := func(t *testing.T, c tc) {
		h := history()
		commit := h[len(h)-1].Position.Index
		cv := Protocol2
		nodes := []*Raft{unlistedMember(1, h, commit, 0, cv, 0), unlistedMember(2, h, commit, 0, cv, 0), unlistedMember(3, h[:2], 0, 0, cv, 0)}
		//: members 4, 5 and 6 hold the whole history.
		for _, id := range []core.NodeID{4, 5, 6} {
			nodes = append(nodes, unlistedMember(id, h, commit, 0, cv, 0))
		}
		n := newUnlistedNet(t, nodes...)
		n.down[3] = true
		n.elect(4)
		r3, r4 := n.nodes[3], n.nodes[4]
		settle := func() { n.tick(2 * testTiming().ElectionTimeoutMax) }
		settle()
		//: the DenyPin is admitted on 2, 4, 5 and 6 and cuts 1 once committed.
		if c.deny {
			//: 2, 4, 5 and 6 answer: 4 of 6 without 1.
			if err := proposeDenyPin(r4, 1); err != nil {
				t.Fatalf("setup: DenyPin(1) %v", err)
			}
			settle()
			//: the withdrawn pin must be committed before the removal.
			if m, _ := r4.confAt(r4.log.committed).Member(1); m.SPKI != [32]byte{} {
				t.Fatalf("setup: the DenyPin of 1 did not commit:%v", n)
			}
			// Every member now refuses 1 (#551): it hears no one.
			n.down[1] = true
			settle()
		}
		err := proposeRemove(r4, 2)
		//: with 1's pin denied, 2 is the last member 3 reaches under a key it pins.
		if c.deny {
			//: on 1's count, 3 would come back hearing no one.
			if err == nil {
				t.Fatalf("the removal of 2 was admitted while 3 is down, on the count of 1, whose pin was denied: 3 knows {1, 2, 3} and would come back hearing no one")
			}
			n.down[3] = false
			n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
			//: 3 must catch up through 2.
			if r3.log.committed != r4.log.committed {
				t.Fatalf("3 never caught up through 2:%v", n)
			}
			err = proposeRemove(r4, 2)
		}
		//: 2's removal must go through once nobody depends on it.
		if err != nil {
			t.Fatalf("the removal of 2 was %v, though 3 reaches another member under a key it pins:%v", err, n)
		}
		settle()
		n.down[2] = true
		n.down[3] = false
		n.tick(4 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
		//: 3 must follow leader 4 once 2 is gone.
		if r3.log.lastIndex() != r4.log.lastIndex() || r3.log.committed != r4.log.committed || r3.lead != 4 {
			t.Fatalf("3 is cut off from leader 4 once 2 left:%v", n)
		}
	}
	//: with and without the DenyPin.
	for _, c := range []tc{
		{name: "the other voucher pinned"},
		{name: "the other voucher's pin denied", deny: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
