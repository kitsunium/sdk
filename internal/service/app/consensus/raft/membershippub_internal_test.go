package raft

import (
	"fmt"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// checkMembershipPublishedOnChange commits commands under a stable
// configuration and requires no Ready to carry the admission set: it was
// handed to the driver at every commit, and every node paid a
// SetMembership, an address resolution and an Info line per entry (#531).
func checkMembershipPublishedOnChange(mut mutation) error {
	r := leaderOf(2, 0, nil, mut, false)
	for i := range 3 {
		d := []byte(fmt.Sprintf("k%d=v", i))
		if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}}); code != 0 {
			return fmt.Errorf("setup: proposal refused %v", code)
		}
		rd := r.Ready()
		last := r.log.lastIndex()
		r.StorageDone(rd.Seq, last, r.term)
		r.Ready()
		before := r.log.committed
		m := msg(core.MsgAppResp, 2, 1, r.term)
		m.Index = last
		r.Step(r.now, m)
		if r.log.committed <= before {
			return fmt.Errorf("setup: the commit did not advance past %d", before)
		}
		if rd := r.Ready(); rd.Membership != nil {
			return fmt.Errorf("the unchanged admission set was handed on at commit %d", r.log.committed)
		}
	}
	return nil
}
