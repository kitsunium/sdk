package raft

import (
	"fmt"
	"testing"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #722: Promote made a voter of a learner that did not hold the entry that
// admits it, only match+PromoteLag >= commit being checked: while the
// commit was at most PromoteLag, a learner heard once at match 0 passed.
// The joiner's stamp stays joining until its log holds its admission
// committed and durable (#695), so it refused every vote and PreVote; once
// the leader failed, three live voters of four elected no one, and only a
// leader could tell the joiner its admission was committed. The file is
// named after the simulator's so that the shards of its seed parts stay
// where they are.

// proposePromote proposes the promotion of learner target at leader r.
func proposePromote(r *Raft, target core.NodeID) errs.Code {
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:promote", Change: wire.ChangePromote, Target: target})
	_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}})
	return code
}

// ackFrom has peer id acknowledge index to leader r, which hears it now.
func ackFrom(r *Raft, id core.NodeID, index uint64) {
	m := msg(core.MsgAppResp, id, 1, r.term)
	m.Index = index
	r.Step(r.now, m)
	r.Ready()
}

// promoteOnceAdmitted has learner 4, heard now and within PromoteLag of
// leader r's commit, acknowledge below its admission at admitted, then at
// it: Promote must be refused MEMBERSHIP_INVALID before and admitted after.
func promoteOnceAdmitted(r *Raft, admitted uint64) error {
	ackFrom(r, 4, admitted-1)
	if pr := r.prs[4]; pr == nil || pr.match+r.cfg.PromoteLag < r.log.committed {
		return fmt.Errorf("setup: learner 4 is not within PromoteLag of the commit %d: %+v", r.log.committed, pr)
	}
	if code := proposePromote(r, 4); code != codeMembershipInvalid {
		return fmt.Errorf("learner 4, admitted at %d, was promoted at match %d (code %v): a voter whose stamp is still joining, which refuses every vote", admitted, admitted-1, code)
	}
	ackFrom(r, 4, admitted)
	if code := proposePromote(r, 4); code != 0 {
		return fmt.Errorf("learner 4, holding its admission at %d, was refused its promotion: %v", admitted, code)
	}
	if m, _ := r.conf().Member(4); !m.Voter {
		return fmt.Errorf("the promotion appended no voter: %+v", r.conf())
	}
	return nil
}

// TestPromoteWaitsForTheLearnerToHoldItsAdmission: leader 1 of voters 1, 2
// and 3 adds learner 4 in an entry of its log, then hears 4 acknowledge the
// entry before it. Its admission is the first configuration that lists it.
func TestPromoteWaitsForTheLearnerToHoldItsAdmission(t *testing.T) {
	r := keyLeader(Protocol2, 0)
	if err := changeAndCommit(r, "add learner 4", func() error {
		if code := proposeAddLearner(r, 4, [32]byte{4}); code != 0 {
			return fmt.Errorf("refused: %v", code)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := promoteOnceAdmitted(r, r.conf().Index); err != nil {
		t.Fatal(err)
	}
}

// TestPromoteWaitsForTheLearnerToHoldTheBase: the snapshot base already
// lists learner 4, its admission compacted below it. The base is the
// first index the leader knows to hold it, so 4 must hold the base.
func TestPromoteWaitsForTheLearnerToHoldTheBase(t *testing.T) {
	conf := testConf(1, 2, 3, 4)
	conf.Members[3].Voter = false
	r := nodeOf(1, 5, conf, Protocol2, 0)
	r.term, r.vote = 6, 1
	r.becomeLeader()
	commitLast(r)
	if err := promoteOnceAdmitted(r, r.log.snapIndex); err != nil {
		t.Fatal(err)
	}
}

// TestALostProbeOfALearnerIsSentAgainInAQuietLog: leader 1 adds learner
// 4, and the probe it sends 4 as the admission commits is lost, as it is
// when the joiner's transport does not admit the leader yet; nothing is
// written after it. 4's heartbeat answer must have the leader probe it
// again, with nothing to append: resent only with entries, the probe
// waited for the next write, the learner was never fed in a quiet cluster,
// and its promotion, which waits for it to hold its admission, was refused
// for as long as nothing was written.
func TestALostProbeOfALearnerIsSentAgainInAQuietLog(t *testing.T) {
	r := keyLeader(Protocol2, 0)
	if err := changeAndCommit(r, "add learner 4", func() error {
		if code := proposeAddLearner(r, 4, [32]byte{4}); code != 0 {
			return fmt.Errorf("refused: %v", code)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	admitted := r.conf().Index
	// changeAndCommit's Ready carried the probe to 4: it is lost.
	r.Step(r.now, msg(core.MsgHeartbeatResp, 4, 1, r.term))
	rd := r.Ready()
	probed := false
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		probed = probed || (m.To == 4 && m.Kind == core.MsgApp)
	}
	if !probed {
		t.Fatalf("learner 4, admitted at %d and whose probe was lost, answered a heartbeat and was not probed again: nothing is written, so it is never fed", admitted)
	}
	if err := promoteOnceAdmitted(r, admitted); err != nil {
		t.Fatal(err)
	}
}
