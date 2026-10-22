package raft

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// longTiming is a generation 2 contract five times longer than testTiming.
func longTiming() core.TimingContractSpec {
	return core.TimingContractSpec{
		Generation: 2, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: 5 * time.Second, ElectionTimeoutMax: 10 * time.Second, MaxClockDrift: 0.01,
	}
}

// chainAfter chains es after prev, at index start on, setting each entry's
// position at the term it carries, its body sum and its chain value.
func chainAfter(prev uint32, start uint64, es ...core.EntryValue) []core.EntryValue {
	for i := range es {
		es[i].Position.Index = start + uint64(i)
		es[i].BodySum = wire.BodySum(es[i].Data)
		es[i].ChainSum = wire.ChainSum(prev, &es[i])
		prev = es[i].ChainSum
	}
	return es
}

// contractEntry is the entry of term proposing c against generation 1.
func contractEntry(term uint64, c core.TimingContractSpec) core.EntryValue {
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "op:timing", Pre: core.PreconditionSpec{Generation: 1}, Contract: c})
	return core.EntryValue{Position: core.PositionValue{Term: term}, Kind: core.EntryTimingContract, Data: data}
}

// commandEntry is a command of term.
func commandEntry(term uint64, s string) core.EntryValue {
	return core.EntryValue{Position: core.PositionValue{Term: term}, Kind: core.EntryCommand, Data: []byte(s)}
}

// contractLog is entry 1 of term 1, then the generation 2 contract and a
// command, both of term 2.
func contractLog() []core.EntryValue {
	first := chainEntries(0, 1, 1)
	return append(first, chainAfter(first[0].ChainSum, 2, contractEntry(2, longTiming()), commandEntry(2, "k=v"))...)
}

// heartbeatContract is the generation follower f reports to a heartbeat of
// leader 1 at term 2 with commit.
func heartbeatContract(f *Raft, at time.Time, commit uint64) (uint64, error) {
	hb := msg(core.MsgHeartbeat, 1, 2, 2)
	hb.Commit = commit
	f.Step(at, hb)
	rd := f.Ready()
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.Kind == core.MsgHeartbeatResp {
			return m.Contract, nil
		}
	}
	return 0, errors.New("the heartbeat was not answered")
}

// checkContractReportedOnceDurable: a follower reports to a heartbeat the
// contract generation its durable committed entries put in force, the one
// a restart rebuilds, never the one it applied in memory before the
// contract's entry was synced (#521). The leader sizes its lease on the
// reported generation's window.
func checkContractReportedOnceDurable(mut mutation) error {
	ents := contractLog()
	f := newTest(2, testState{term: 1, commit: 1, ents: ents[:1]}, mut, false)
	at := t0.Add(5 * time.Second)
	app := msg(core.MsgApp, 1, 2, 2)
	app.Index, app.LogTerm, app.Entries, app.Commit = 1, 1, ents[1:], 3
	f.Step(at, app)
	rd := f.Ready()
	if f.timing.Generation != 2 {
		return fmt.Errorf("setup: the committed contract is not applied in memory (generation %d)", f.timing.Generation)
	}
	g, err := heartbeatContract(f, at, 3)
	if err != nil {
		return err
	}
	if g != 1 {
		return fmt.Errorf("generation %d reported before the contract's entry is durable; a restart rebuilds generation 1", g)
	}
	f.StorageDone(rd.Seq, 3, 2)
	if g, err = heartbeatContract(f, at, 3); err != nil || g != 2 {
		return fmt.Errorf("generation %d reported once the contract's entry is durable (%v), want 2", g, err)
	}
	return nil
}

// checkNoCampaignInsideVoteWindow: a node restarted with a longer contract
// present in its log, its commit persisted below it, runs the older
// contract's timeouts but refuses votes for the longer window, and must not
// campaign inside it either: its candidacy is a vote for itself, inside a
// lease it may have acknowledged (#521).
func checkNoCampaignInsideVoteWindow(mut mutation) error {
	f := newTest(2, testState{term: 2, commit: 1, ents: contractLog()}, mut, false)
	if f.timing.Generation != 1 || f.voteWindow != longTiming().ElectionTimeoutMin {
		return fmt.Errorf("setup: generation %d, vote window %v", f.timing.Generation, f.voteWindow)
	}
	campaigned := func(at time.Time) bool {
		f.Tick(at)
		rd := f.Ready()
		for _, m := range append(rd.Messages, rd.AfterSync...) {
			if m.Kind == core.MsgPreVote {
				return true
			}
		}
		return false
	}
	if campaigned(t0.Add(testTiming().ElectionTimeoutMax + time.Millisecond)) {
		return fmt.Errorf("a restarted node campaigned inside its %v vote window", f.voteWindow)
	}
	for at := t0.Add(longTiming().ElectionTimeoutMin); at.Before(t0.Add(20 * time.Second)); at = at.Add(100 * time.Millisecond) {
		if campaigned(at) {
			return nil
		}
	}
	return errors.New("setup: the node never campaigned past its vote window")
}

// generationTwoLeader is leader 1 at term 2 started from a snapshot cut
// after the generation 2 contract, its no-op committed by voter 3: it knows
// only generation 2.
func generationTwoLeader(mut mutation) *Raft {
	cfg := &Config{
		ID: 1, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, Lease: true, CanVote: true, MaxChainRefusals: 3, mut: mut,
	}
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 1, Commit: 2},
		Snapshot:  core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 2}, Membership: testConf(1, 2, 3), Timing: longTiming()},
		Now:       t0,
	})
	r.now = t0.Add(20 * time.Second)
	r.term, r.vote = 2, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), 2)
	ack := msg(core.MsgAppResp, 3, 1, 2)
	ack.Index = r.log.lastIndex()
	r.Step(r.now, ack)
	r.Ready()
	return r
}

// checkUnknownGenerationGrantsNoLease: a leader that knows only generation
// 2 is acknowledged by a follower still on generation 1, whose window is 1 s:
// the round confirms leadership but grants no lease, since the leader does
// not know how long that follower refuses votes. Sized as the shortest
// contract it knew, 5 s, it outlived the follower's refusal (#522).
func checkUnknownGenerationGrantsNoLease(mut mutation) error {
	r := generationTwoLeader(mut)
	r.broadcastHeartbeat(r.now)
	hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
	hb.Context, hb.Contract = r.nextRound, 1
	r.Step(r.now, hb)
	r.Ready()
	if r.LeaseValid(r.now.Add(testTiming().ElectionTimeoutMin + time.Millisecond)) {
		return fmt.Errorf("a lease of %v granted on an acknowledgement of a generation the leader does not know", r.leaseLen)
	}
	return nil
}

// checkOwnTimingDroppedOnInstall: a node holding its own InitialTiming as
// generation 1, which no entry confirmed, drops it once a snapshot names the
// cluster's contract: led by it, a lagging follower's generation 1 is
// unknown, never this node's guess at it (#522).
func checkOwnTimingDroppedOnInstall(mut mutation) error {
	f := newTest(2, testState{term: 2}, mut, false)
	f.lead = 1
	f.InstallSnapshot(core.SnapshotMetaValue{Position: core.PositionValue{Term: 2, Index: 5}, Membership: testConf(1, 2, 3), Timing: longTiming()})
	if c, ok := f.contracts[1]; ok {
		return fmt.Errorf("the node's own InitialTiming is still generation 1's (%v) after a snapshot named generation 2", c.ElectionTimeoutMin)
	}
	if _, ok := f.contracts[2]; !ok {
		return errors.New("setup: the snapshot's contract is not known")
	}
	return nil
}

// checkNoLeaseWithoutDrift: a contract in force whose drift budget is not in
// (0, 1) holds no lease. Bootstrap founded clusters on TimingDefaults, drift
// 0, and the lease was the whole election window with no long-turn guard
// (#523).
func checkNoLeaseWithoutDrift(mut mutation) error {
	for _, d := range []float64{0, math.NaN()} {
		r := confirmedLeader(mut)
		r.timing.MaxClockDrift = d
		if r.LeaseValid(r.now) {
			return fmt.Errorf("a lease held under a contract with drift %v", d)
		}
	}
	return nil
}

// checkFoundingContractCompleted: a founding contract written without its
// maximum, or with a drift outside [0, 1), is completed when it rules — a
// maximum of twice the minimum, no drift — the same way on every node. Run
// as written, a maximum of 0 refused every promotion (#523).
func checkFoundingContractCompleted(mut mutation) error {
	c := testTiming()
	c.ElectionTimeoutMax, c.MaxClockDrift = 0, math.NaN()
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "bootstrap", Change: wire.ChangeFounding, Membership: testConf(1, 2, 3), Contract: c})
	found := chainAfter(0, 1, core.EntryValue{Position: core.PositionValue{Term: 1}, Kind: core.EntryMembership, Data: data})
	f := newTest(2, testState{term: 1, commit: 1, ents: found}, mut, false)
	got := f.timing
	if got.ElectionTimeoutMax != 2*c.ElectionTimeoutMin || got.MaxClockDrift != 0 {
		return fmt.Errorf("the founding contract rules as %v..%v, drift %v; want a maximum of %v and no drift",
			got.ElectionTimeoutMin, got.ElectionTimeoutMax, got.MaxClockDrift, 2*c.ElectionTimeoutMin)
	}
	return nil
}

func TestTimingChecks(t *testing.T) {
	runCase := func(t *testing.T, c func(mutation) error) {
		if err := c(0); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]func(mutation) error{
		"contract reported once durable": checkContractReportedOnceDurable,
		"no campaign in the vote window": checkNoCampaignInsideVoteWindow,
		"unknown generation, no lease":   checkUnknownGenerationGrantsNoLease,
		"own timing dropped on install":  checkOwnTimingDroppedOnInstall,
		"no lease without drift":         checkNoLeaseWithoutDrift,
		"founding contract completed":    checkFoundingContractCompleted,
	} {
		t.Run(name, func(t *testing.T) { runCase(t, c) })
	}
}
