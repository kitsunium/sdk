package raft

import (
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #755: #724's remedy takes a leader's address from a member's record only
// when the record was read at a commit past this node's latest
// configuration. A node back with an uncommitted tail of an older term
// that held a configuration entry past the cluster's commit refused every
// record until the cluster committed past that entry: on a quiet cluster,
// never. Such an entry, past the commit of a leader of a newer term, will
// never be committed (#675's argument on the sender's side), and it no
// longer outranks the record.

// tailEngineEntry is engineEntry at term.
func tailEngineEntry(prev uint32, index, term uint64, kind core.EntryKind, admin *wire.AdminValue) core.EntryValue {
	data := wire.AppendAdmin(nil, admin)
	e := core.EntryValue{Position: core.PositionValue{Term: term, Index: index}, Kind: kind, BodySum: wire.BodySum(data), Data: data}
	e.ChainSum = wire.ChainSum(prev, &e)
	return e
}

// tailMember starts node id over ents at hard state (term, commit), from
// the founding configuration naming node 1 at a1.
func tailMember(id core.NodeID, ents []core.EntryValue, term, commit uint64) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
	}
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: term, Commit: commit},
		Snapshot:  core.SnapshotMetaValue{Membership: movedConf("a1", false), ClusterVersion: Protocol2, ClusterGeneration: uint64(Protocol2)},
		Entries:   slices.Clone(ents), Now: t0,
	})
	r.Ready()
	return r
}

// tailCluster: entries 1 and 2 at term 1 everywhere. Nodes 1 and 2 hold
// the cluster's history: entry 3, the UpdateAddress that moved node 1 to
// a1-moved, and a command at 4, both at term 3, committed through 4. Node 3
// was down through it: it holds an uncommitted tail of term 2 from 3 to 8,
// five commands and, at 8 when conf is set, a configuration entry (a
// ChangeSnapshotLimit of node 2, still naming node 1 at a1), a sixth
// command otherwise. Node 1 then leads at term 4, and a message reaches its
// receiver only when its sender dials the address the receiver listens at.
func tailCluster(t *testing.T, conf bool) *unlistedNet {
	t.Helper()
	hist, stale := tailLogs(conf)
	n := newUnlistedNet(t, tailMember(1, hist, 3, 4), tailMember(2, hist, 3, 4), tailMember(3, stale, 2, 2))
	n.reach = func(m core.MessageValue) bool {
		if dialled(n.nodes[m.From], m.To) == movedAt[m.To] {
			return true
		}
		n.nodes[m.From].ReportUnreachable(m.To)
		return false
	}
	n.elect(1)
	if got := dialled(n.nodes[3], 1); got != "a1" {
		t.Fatalf("setup: node 3 dials node 1 at %q, want a1", got)
	}
	return n
}

// tailLogs is the history nodes 1 and 2 hold and the stale log node 3
// holds, as tailCluster describes them.
func tailLogs(conf bool) (hist, stale []core.EntryValue) {
	common := chainEntries(0, 1, 1, 1)
	upd := tailEngineEntry(common[1].ChainSum, 3, 3, core.EntryMembership, &wire.AdminValue{
		Operator: "op:move", Change: wire.ChangeUpdateAddress, Target: 1, Membership: movedConf("a1-moved", false),
	})
	hist = append(append(slices.Clone(common), upd), chainEntries(upd.ChainSum, 4, 3)...)
	tail := chainEntries(common[1].ChainSum, 3, 2, 2, 2, 2, 2)
	if conf {
		limit := movedConf("a1", false)
		limit.Members[1].MaxSnapshotBytes = 1 << 30
		tail = append(tail, tailEngineEntry(tail[4].ChainSum, 8, 2, core.EntryMembership, &wire.AdminValue{
			Operator: "engine:snapshot-limit", Change: wire.ChangeSnapshotLimit, Target: 2, Membership: limit,
		}))
	} else {
		tail = append(tail, chainEntries(tail[4].ChainSum, 8, 2)...)
	}
	return hist, append(slices.Clone(common), tail...)
}

// TestTheMemberBehindAMovedLeaderTakesItsAddressOverAnOlderTermsTail: node
// 3 takes the record of node 1's address although its divergent tail of
// an older term ends in a configuration entry past the cluster's commit,
// catches up on a quiet cluster, and once node 2 fails, nodes 1 and 3
// still have a leader. A tail of commands alone is the control.
func TestTheMemberBehindAMovedLeaderTakesItsAddressOverAnOlderTermsTail(t *testing.T) {
	for _, conf := range []bool{false, true} {
		name := "tail-of-commands"
		if conf {
			name = "tail-with-a-configuration-entry"
		}
		t.Run(name, func(t *testing.T) {
			n := tailCluster(t, conf)
			r1, r3 := n.nodes[1], n.nodes[3]
			confAt := r3.conf().Index
			n.tick(8 * askElectionTimeouts * testTiming().ElectionTimeoutMax)
			if r3.log.lastIndex() != r1.log.lastIndex() || r3.log.committed != r1.log.committed {
				t.Fatalf("node 3 never caught up with leader 1 on a quiet cluster (last %d, commit %d; leader last %d, commit %d): its tail's configuration at %d outranked the record, and it dials %q:%v",
					r3.log.lastIndex(), r3.log.committed, r1.log.lastIndex(), r1.log.committed, confAt, dialled(r3, 1), n)
			}
			if got := dialled(r3, 1); got != "a1-moved" {
				t.Fatalf("node 3 caught up dialling node 1 at %q, want a1-moved", got)
			}
			if r1.FailureTolerance(n.now) != 1 {
				t.Fatalf("leader 1 does not hear node 3: failure tolerance %d", r1.FailureTolerance(n.now))
			}
			n.down[2] = true
			n.tick(10 * testTiming().ElectionTimeoutMax)
			if l := n.leader(); l == 0 || !n.nodes[l].QuorumActive(n.now) {
				t.Fatalf("no leader with nodes 1 and 3 up, two voters of three:%v", n)
			}
		})
	}
}

// TestTheTailOfTheLeadersTermStillOutranksTheRecord: node 3's latest
// configuration, uncommitted at 8, outranks a record read at 5 when it is
// of the term node 3 follows its leader in, one that leader wrote (#724);
// under a leader of a later term it is a divergent tail, and the record is
// taken. Either way the record ends once node 3's commit reaches it.
func TestTheTailOfTheLeadersTermStillOutranksTheRecord(t *testing.T) {
	_, stale := tailLogs(true)
	moved := core.MemberValue{ID: 1, SPKI: [32]byte{1}, Address: "a1-moved", Voter: true}
	for _, x := range []struct {
		name  string
		term  uint64
		taken bool
	}{
		{"of the leader's term", 2, false},
		{"of an older term", 4, true},
	} {
		t.Run(x.name, func(t *testing.T) {
			r := tailMember(3, stale, 2, 2)
			now := t0.Add(10 * time.Second)
			hb := msg(core.MsgHeartbeat, 1, 3, x.term)
			hb.Commit = 2
			r.Step(now, hb)
			r.Ready()
			r.ReportUnreachable(1)
			r.Tick(now)
			r.Ready()
			if r.lead != 1 || r.term != x.term || r.conf().Index != 8 {
				t.Fatalf("setup: node 3 follows %d at term %d with its configuration at %d, want 1 at %d and 8", r.lead, r.term, r.conf().Index, x.term)
			}
			r.Step(now, addrRecord(2, x.term, 5, moved))
			rd := r.Ready()
			if got := dialled(r, 1) == "a1-moved" && rd.LeaderAddress.Address == "a1-moved"; got != x.taken {
				t.Fatalf("a record read at 5 over a configuration at 8 %s: taken %v, want %v (dials %q)", x.name, got, x.taken, dialled(r, 1))
			}
			if !x.taken {
				return
			}
			// Leader 1 truncates the tail and commits past the record's
			// index: the record ends.
			hist, _ := tailLogs(true)
			app := msg(core.MsgApp, 1, 3, x.term)
			app.Index, app.LogTerm, app.Commit = 2, 1, 5
			app.Entries = append(slices.Clone(hist[2:]), chainEntries(hist[3].ChainSum, 5, x.term)...)
			r.Step(now, app)
			r.Ready()
			if r.leadAddr.ID != 0 || dialled(r, 1) != "a1-moved" || r.log.committed != 5 || r.conf().Index != 3 {
				t.Fatalf("the record outlived the truncated tail: record %+v, dials %q, commit %d, configuration at %d", r.leadAddr, dialled(r, 1), r.log.committed, r.conf().Index)
			}
		})
	}
}
