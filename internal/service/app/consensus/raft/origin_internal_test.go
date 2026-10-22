package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// forwardAdmin is member from's forward of an engine entry of kind whose
// payload, of version v, names forged as its origin.
func forwardAdmin(r *Raft, from core.NodeID, kind core.EntryKind, v uint32, forged core.NodeID) core.MessageValue {
	data := wire.AppendAdminAt(nil, v, &wire.AdminValue{Operator: "forgejo-user:1", Origin: forged})
	m := msg(core.MsgForwardAdmin, from, 1, r.term)
	m.Context = 7
	m.Entries = []core.EntryValue{{Kind: kind, Version: v, BodySum: wire.BodySum(data), Data: data}}
	return m
}

// lastAdmin parses the engine entry the leader appended last.
func lastAdmin(r *Raft) (*core.EntryValue, wire.AdminValue, error) {
	e := r.log.at(r.log.lastIndex())
	a, err := wire.ParseAdminAt(e.Version, e.Data)
	return e, a, err
}

// checkOriginAuthenticated: once the cluster version is 2, the engine entry
// a member forwarded records that member, authenticated by the transport,
// whatever origin the forwarded payload named, and the leader's own one
// records the leader. Before it, the entry is of payload version 0, which a
// node of protocol 1 reads (ADR 0152 D8, #561).
func checkOriginAuthenticated(mut mutation) error {
	r := ackedLeader(mut)
	r.Step(r.now, forwardAdmin(r, 2, core.EntryNoSpaceRaise, 0, 0))
	if e, a, err := lastAdmin(r); err != nil || e.Kind != core.EntryNoSpaceRaise || e.Version != 0 || a.Origin != 0 {
		return fmt.Errorf("before the raise: entry %v version %d origin %d (%v), want version 0 and no origin", e.Kind, e.Version, a.Origin, err)
	}
	r.clusterVer = Protocol2
	fwd := forwardAdmin(r, 2, core.EntryNoSpaceRaise, wire.AdminVersion2, 3)
	fwd.Context = 8
	r.Step(r.now, fwd)
	e, a, err := lastAdmin(r)
	if err != nil || e.Version != wire.AdminVersion2 {
		return fmt.Errorf("at protocol 2: entry version %d (%v), want %d", e.Version, err, wire.AdminVersion2)
	}
	if a.Origin != 2 || a.Operator != "forgejo-user:1" {
		return fmt.Errorf("the entry member 2 forwarded records origin %d operator %q, want origin 2 and the operator verbatim", a.Origin, a.Operator)
	}
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:local"})
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryNoSpaceRaise, BodySum: wire.BodySum(data), Data: data}}); code != 0 {
		return fmt.Errorf("setup: local proposal refused: %v", code)
	}
	if _, a, err := lastAdmin(r); err != nil || a.Origin != 1 {
		return fmt.Errorf("the leader's own entry records origin %d (%v), want 1", a.Origin, err)
	}
	return nil
}

func TestAForwardedAdministrativeEntryRecordsItsOrigin(t *testing.T) {
	if err := checkOriginAuthenticated(0); err != nil {
		t.Fatal(err)
	}
	if err := checkOriginAuthenticated(mutOriginAsWritten); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// An admin payload version this node does not know stops the node when the
// entry commits, as an unknown kind does: never skipped (D18).
func TestAnUnknownAdminVersionStopsTheNode(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1)}, 0, false)
	data := wire.AppendAdmin(nil, &wire.AdminValue{})
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 2}, Kind: core.EntryNoSpaceRaise, Version: 9, Data: data, BodySum: wire.BodySum(data)}
	e.ChainSum = wire.ChainSum(f.log.chain(1), &e)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 1, 1, 2, []core.EntryValue{e}
	f.Step(t0, m)
	if u := f.Ready().Unsupported; u.Index != 2 {
		t.Fatalf("an admin payload of version 9 was applied: %+v, alarm %d", u, f.noSpace)
	}
}
