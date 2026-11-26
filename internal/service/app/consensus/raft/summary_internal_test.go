package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Summary is Status without its two memberships (#912): the event loop
// reads it at every turn where it read Status, which clones both, so every
// other field must be Status's, whatever the node is — a follower that heard
// its leader's last index (#919), a leader, one whose term a transfer handed
// over (#676), a node that campaigns.
func TestSummaryIsStatusWithoutItsMemberships(t *testing.T) {
	type tc struct {
		name string
		// node is the step function read.
		node func(t *testing.T) *Raft
	}
	cases := []tc{
		{name: "a follower that heard nothing", node: func(*testing.T) *Raft {
			return newTest(2, testState{term: 1}, 0, false)
		}},
		{name: "a follower that heard its leader's last index", node: func(t *testing.T) *Raft {
			f := startOn(2, 0, snapAt(Protocol3))
			f.Step(t0, core.MessageValue{
				Version: 1, Group: 1, Kind: core.MsgHeartbeat, From: 1, To: 2, Term: 1, Commit: 10,
				RejectHint: 50, Flags: announcementOf(Protocol4),
			})
			if f.Status().LeaderLast != 50 {
				t.Fatal("setup: the follower did not hear the leader's last index")
			}
			return f
		}},
		{name: "a leader", node: func(*testing.T) *Raft { return leaderOf(2, 0, nil, 0, false) }},
		{name: "a leader a transfer elected", node: func(*testing.T) *Raft {
			l := leaderOf(2, 0, nil, 0, false)
			l.campaignIssuer = 3
			return l
		}},
		{name: "a node that campaigns", node: func(t *testing.T) *Raft {
			r := newTest(2, testState{term: 1}, 0, false)
			r.Tick(t0.Add(10 * time.Second))
			if r.role == core.RoleFollower {
				t.Fatal("setup: the node did not campaign")
			}
			return r
		}},
	}
	runCase := func(t *testing.T, c tc) {
		r := c.node(t)
		if got, want := r.Summary(), r.Status().SummaryValue; got != want {
			t.Fatalf("Summary %+v, Status's %+v", got, want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// SameConf answers what comparing the published configuration with
// Status's Conf answered (#912), so a driver clones the configuration again
// once it changed, and only then: any field of any member, the members'
// order, a retired id or key, the index that produced it. A nil and an
// empty list are the same, as they were.
func TestSameConfIsTheComparisonWithStatusConf(t *testing.T) {
	type tc struct {
		name string
		// edit changes the published copy; want is SameConf's answer.
		edit func(m *core.MembershipValue)
		want bool
	}
	cases := []tc{
		{name: "the copy Status returned", edit: func(*core.MembershipValue) {}, want: true},
		{name: "empty lists for nil ones", edit: func(m *core.MembershipValue) {
			m.Retired, m.RetiredKeys = []core.NodeID{}, [][32]byte{}
		}, want: true},
		{name: "another index", edit: func(m *core.MembershipValue) { m.Index++ }, want: false},
		{name: "a member's address", edit: func(m *core.MembershipValue) { m.Members[1].Address = "elsewhere:1" }, want: false},
		{name: "a member's vote", edit: func(m *core.MembershipValue) { m.Members[2].Voter = false }, want: false},
		{name: "a member's key", edit: func(m *core.MembershipValue) { m.Members[0].SPKI[0] ^= 1 }, want: false},
		{name: "a member more", edit: func(m *core.MembershipValue) {
			m.Members = append(m.Members, core.MemberValue{ID: 9, SPKI: [32]byte{9}})
		}, want: false},
		{name: "the members reordered", edit: func(m *core.MembershipValue) {
			m.Members[0], m.Members[1] = m.Members[1], m.Members[0]
		}, want: false},
		{name: "a retired id", edit: func(m *core.MembershipValue) { m.Retired = append(m.Retired, 9) }, want: false},
		{name: "a retired key", edit: func(m *core.MembershipValue) { m.RetiredKeys = append(m.RetiredKeys, [32]byte{9}) }, want: false},
	}
	runCase := func(t *testing.T, c tc) {
		r := leaderOf(2, 0, nil, 0, false)
		published := r.Status().Conf
		c.edit(&published)
		if got := r.SameConf(&published); got != c.want {
			t.Fatalf("SameConf %v, want %v", got, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A membership entry is in force once appended (D8): the configuration the
// driver published before it is no longer the one in force, and the one
// Status returns after it is, so the next turn publishes it (#912).
func TestSameConfSeesAMembershipEntryAppended(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), r.term)
	for _, id := range []core.NodeID{2, 3} {
		ack := msg(core.MsgAppResp, id, 1, r.term)
		ack.Index = r.log.lastIndex()
		r.Step(r.now, ack)
	}
	published := r.Status().Conf
	data := wire.AppendAdmin(nil, &wire.AdminValue{
		Change: wire.ChangeAddLearner, Membership: core.MembershipValue{Members: []core.MemberValue{{ID: 9, SPKI: [32]byte{9}}}},
	})
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}}); code != 0 {
		t.Fatalf("setup: the AddLearner was refused: %v", code)
	}
	if r.SameConf(&published) {
		t.Fatal("the configuration published before the AddLearner is still said to be in force")
	}
	if now := r.Status().Conf; !r.SameConf(&now) || len(now.Members) != len(published.Members)+1 {
		t.Fatalf("the configuration after the AddLearner, %d members, is not the one in force", len(now.Members))
	}
}
