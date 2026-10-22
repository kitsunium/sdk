package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// TestAChainRefusalIsReportedToTheDriver pins what D13 counts as
// consensus_chain_refusals_total: every append a follower refuses for a
// broken chain names, in the Ready, the leader it refused, so the driver can
// count it by leader. Before, the refusal reached Health only as the latest
// run at one index, and the counter the ADR names was never emitted.
func TestAChainRefusalIsReportedToTheDriver(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 0}, 0, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Entries = chainEntries(0, 1, 1)
	m.Entries[0].ChainSum++ // a chain the follower cannot verify
	f.Step(t0, m)
	rd := f.Ready()
	if len(rd.ChainRefusedBy) != 1 || rd.ChainRefusedBy[0] != 1 {
		t.Fatalf("ChainRefusedBy = %v, want [1]", rd.ChainRefusedBy)
	}
}

// checkRefusedLeaderIsNotHeard: once a follower refused a leader's chain
// MaxChainRefusals times, that leader's appends and heartbeats are neither
// answered nor taken as contact for the rest of the term, so the follower's
// election timer runs out and a refusing majority elects another leader
// (#508). Answered, each rejection was contact for the leader's CheckQuorum
// and the trigger of an immediate resend; taken as contact, they kept the
// refusing followers from ever campaigning.
func checkRefusedLeaderIsNotHeard(mut mutation) error {
	f := newTest(2, testState{term: 1, commit: 0}, mut, false)
	at := t0.Add(5 * time.Second)
	bad := msg(core.MsgApp, 1, 2, 1)
	bad.Entries = chainEntries(0, 1, 1)
	bad.Entries[0].BodySum++ // a payload that no longer matches its sum
	for range 3 {
		f.Step(at, bad)
	}
	f.Ready()
	for k := 1; k <= 4; k++ {
		now := at.Add(time.Duration(k) * 400 * time.Millisecond)
		f.Step(now, bad)
		f.Step(now, msg(core.MsgHeartbeat, 1, 2, 1))
		rd := f.Ready()
		for _, m := range append(rd.Messages, rd.AfterSync...) {
			if m.To == 1 && (m.Kind == core.MsgAppResp || m.Kind == core.MsgHeartbeatResp) {
				return fmt.Errorf("the refused leader was answered %v after %d refusals", m.Kind, 3+k)
			}
		}
	}
	// The last refusal was the last contact: the timer runs out within the
	// maximum election timeout of it.
	f.Tick(at.Add(testTiming().ElectionTimeoutMax + time.Millisecond))
	rd := f.Ready()
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.Kind == core.MsgPreVote {
			return nil
		}
	}
	return fmt.Errorf("a follower that refused its leader never campaigned (role %v, lead %d)", f.role, f.lead)
}

func TestARefusedLeaderIsNotHeard(t *testing.T) {
	if err := checkRefusedLeaderIsNotHeard(0); err != nil {
		t.Fatal(err)
	}
}

// TestAMajorityRefusingTheChainDeposesTheLeader is #508's reproduction on
// the simulator: the leader's entry stops matching its body sum once
// appended — a proposer that reuses its buffer, or memory corruption — so
// both followers refuse it. A new term must start within a few election
// timeouts, and the appends sent to a refusing follower stay below one per
// heartbeat instead of looping at the round-trip rate.
func TestAMajorityRefusingTheChainDeposesTheLeader(t *testing.T) {
	s := newSim(simOptions{seed: 3})
	s.run(s.after(3*time.Second), false)
	l := s.leader()
	if l == nil {
		t.Fatal("no leader")
	}
	term := l.r.Status().Term
	appends := map[core.NodeID]int{}
	s.onSend = func(m core.MessageValue) {
		if m.From == l.id && m.Kind == core.MsgApp && len(m.Entries) > 0 {
			appends[m.To]++
		}
	}
	data := []byte("poison=1")
	if !s.propose(l, data) {
		t.Fatal("the poisoned entry was refused")
	}
	data[len(data)-1] ^= 1 // the proposer's buffer, reused after Propose
	window := 4 * s.timing.ElectionTimeoutMax
	s.run(s.after(window), false)
	if s.err != nil {
		t.Fatal(s.err)
	}
	nl := s.leader()
	if nl == nil || nl.id == l.id || nl.r.Status().Term <= term {
		st := l.r.Status()
		t.Fatalf("both followers refuse leader %d's chain, yet after %v it is %v at term %d (was %d), commit %d; appends sent %v",
			l.id, window, st.Role, st.Term, term, st.Commit, appends)
	}
	limit := int(window / s.timing.HeartbeatInterval)
	for id, k := range appends {
		if k > limit {
			t.Errorf("follower %d was sent %d appends in %v, more than one per heartbeat (%d)", id, k, window, limit)
		}
	}
	t.Logf("leader %d deposed: %d leads term %d; appends to the refusing followers %v", l.id, nl.id, nl.r.Status().Term, appends)
}
