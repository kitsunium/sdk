package raft

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The checks pin the step function's behaviour on constructed schedules, a
// few messages each (D15: "constructed schedules, not seed sweeps alone").
// Each returns an error when the behaviour is wrong; TestMutationsAreCaught
// proves each one can fail.

var t0 = time.Unix(1_800_000_000, 0)

func testTiming() core.TimingContractSpec {
	return core.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
		ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01,
	}
}

func testConf(ids ...core.NodeID) core.MembershipValue {
	var m core.MembershipValue
	for _, id := range ids {
		m.Members = append(m.Members, core.MemberValue{ID: id, Voter: true, SPKI: [32]byte{byte(id)}})
	}
	return m
}

// chainEntries builds command entries at start.. with the given terms, chained
// from prev.
func chainEntries(prev uint32, start uint64, terms ...uint64) []core.EntryValue {
	out := make([]core.EntryValue, len(terms))
	for i, t := range terms {
		d := fmt.Appendf(nil, "e%d-t%d", start+uint64(i), t)
		e := core.EntryValue{Position: core.PositionValue{Term: t, Index: start + uint64(i)}, Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}
		e.ChainSum = wire.ChainSum(prev, &e)
		prev = e.ChainSum
		out[i] = e
	}
	return out
}

// testState is the durable state a test node starts from: the hard state's
// term and commit, and the log it already holds.
type testState struct {
	term   uint64
	commit uint64
	ents   []core.EntryValue
}

func newTest(id core.NodeID, st testState, mut mutation, lease bool) *Raft {
	term, commit, ents := st.term, st.commit, st.ents
	cfg := &Config{
		ID: id, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, Lease: lease, CanVote: true, MaxChainRefusals: 3, mut: mut,
	}
	return NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: term, Commit: commit},
		Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries:   ents,
		Now:       t0,
	})
}

func msg(kind core.MessageKind, from, to core.NodeID, term uint64) core.MessageValue {
	return core.MessageValue{Version: 1, Group: 1, Kind: kind, From: from, To: to, Term: term}
}

// leaderOf makes node 1 the leader of term over ents, its no-op durable.
func leaderOf(term uint64, commit uint64, ents []core.EntryValue, mut mutation, lease bool) *Raft {
	r := newTest(1, testState{term: term - 1, commit: commit, ents: ents}, mut, lease)
	r.now = t0.Add(5 * time.Second)
	r.term, r.vote = term, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), term)
	r.Ready()
	return r
}

func granted(rd ReadyValue, kind core.MessageKind) bool {
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.Kind == kind && !m.Reject {
			return true
		}
	}
	return false
}

func checkAckWaitsForSync(mut mutation) error {
	f := newTest(2, testState{term: 1, commit: 0}, mut, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Entries = chainEntries(0, 1, 1)
	f.Step(t0, m)
	rd := f.Ready()
	for _, x := range rd.Messages {
		if x.Kind == core.MsgAppResp {
			return errors.New("an append was acknowledged before its sync")
		}
	}
	return nil
}

func checkVoteKeptOnSameTermStepDown(mut mutation) error {
	r := newTest(3, testState{term: 1, commit: 0}, mut, false)
	now := t0.Add(3 * time.Second)
	r.Tick(now)
	r.Step(now, core.MessageValue{Version: 1, Group: 1, Kind: core.MsgPreVoteResp, From: 1, To: 3, Term: 2})
	if r.role != core.RoleCandidate {
		return fmt.Errorf("setup: role %v", r.role)
	}
	rd := r.Ready()
	r.StorageDone(rd.Seq, 0, 0)
	for _, from := range []core.NodeID{1, 2} {
		m := msg(core.MsgVoteResp, from, 3, 2)
		m.Reject = true
		r.Step(now, m)
	}
	r.Ready()
	m := msg(core.MsgVote, 2, 3, 2)
	r.Step(now, m)
	if granted(r.Ready(), core.MsgVoteResp) {
		return errors.New("a node that stepped down in the same term voted twice")
	}
	return nil
}

func staleFollower(mut mutation) *Raft {
	return newTest(2, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1, 1, 1)}, mut, false)
}

func checkCommitVerifiedPrefix(mut mutation) error {
	f := staleFollower(mut)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 1, 1, 3
	f.Step(t0, m)
	if f.log.committed != 1 {
		return fmt.Errorf("commit advanced to %d past the verified prefix 1", f.log.committed)
	}
	return nil
}

func checkAckVerifiedPrefix(mut mutation) error {
	f := staleFollower(mut)
	m := msg(core.MsgApp, 1, 2, 2)
	m.Index, m.LogTerm, m.Commit = 1, 1, 1
	f.Step(t0, m)
	for _, x := range f.Ready().AfterSync {
		if x.Kind == core.MsgAppResp && x.Index != 1 {
			return fmt.Errorf("acknowledged %d instead of the verified prefix 1", x.Index)
		}
	}
	return nil
}

// checkSnapshotAckIndex installs a snapshot on a node whose log matches the
// boundary but carries a stale suffix of another term: the install proves
// the prefix up to the snapshot only, so the leader must not count the
// suffix (review H1, the InstallSnapshot twin of checkAckVerifiedPrefix).
func checkSnapshotAckIndex(mut mutation) error {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, NoTable: true, mut: mut,
	}
	f := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 3},
		Snapshot:  core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries:   chainEntries(0, 1, 1, 1, 1, 2, 2),
		Now:       t0,
	})
	f.lead = 1
	l := leaderOf(3, 0, chainEntries(0, 1, 1, 1, 1, 3, 3), mut, false)
	f.InstallSnapshot(core.SnapshotMetaValue{
		Position: core.PositionValue{Term: 1, Index: 3}, ChainTip: l.log.chain(3),
		Membership: testConf(1, 2, 3), Timing: testTiming(),
	})
	var ack uint64
	rd := f.Ready()
	for _, x := range append(rd.AfterSync, rd.Messages...) {
		if x.Kind == core.MsgAppResp {
			ack = x.Index
		}
	}
	if ack != 3 {
		return fmt.Errorf("the install acknowledged %d instead of the snapshot index 3", ack)
	}
	l.prs[2].becomeSnapshot(3)
	m := msg(core.MsgAppResp, 2, 1, l.term)
	m.Index = ack
	l.Step(l.now, m)
	if l.log.committed > 3 {
		return fmt.Errorf("the leader committed %d over a follower that holds other entries", l.log.committed)
	}
	return nil
}

func checkFigure8(mut mutation) error {
	r := leaderOf(3, 0, chainEntries(0, 1, 1, 2), mut, false)
	m := msg(core.MsgAppResp, 2, 1, 3)
	m.Index = 2
	r.Step(r.now, m)
	if r.log.committed != 0 {
		return fmt.Errorf("an older term's entry was committed by counting (commit %d)", r.log.committed)
	}
	return nil
}

func voteFrom(kind core.MessageKind, from core.NodeID, term, lastTerm, lastIndex uint64) core.MessageValue {
	m := msg(kind, from, 2, term)
	m.LogTerm, m.Index = lastTerm, lastIndex
	return m
}

func checkVoteRestriction(mut mutation) error {
	f := newTest(2, testState{term: 2, commit: 0, ents: chainEntries(0, 1, 1, 2, 2)}, mut, false)
	f.Step(t0.Add(5*time.Second), voteFrom(core.MsgVote, 3, 3, 1, 10))
	if granted(f.Ready(), core.MsgVoteResp) {
		return errors.New("RequestVote granted to a candidate with a stale log")
	}
	return nil
}

func checkPreVoteRestriction(mut mutation) error {
	f := newTest(2, testState{term: 2, commit: 0, ents: chainEntries(0, 1, 1, 2, 2)}, mut, false)
	f.Step(t0.Add(5*time.Second), voteFrom(core.MsgPreVote, 3, 3, 1, 10))
	if granted(f.Ready(), core.MsgPreVoteResp) {
		return errors.New("PreVote granted to a candidate with a stale log")
	}
	return nil
}

// followerOfOne is node 2 at term 2, having just heard from leader 1.
func followerOfOne(mut mutation, ents []core.EntryValue) (*Raft, time.Time) {
	f := newTest(2, testState{term: 2, commit: 0, ents: ents}, mut, false)
	now := t0.Add(5 * time.Second)
	f.Step(now, msg(core.MsgHeartbeat, 1, 2, 2))
	f.Ready()
	return f, now.Add(10 * time.Millisecond)
}

func transferVote(term, issuerTerm, lastTerm, lastIndex uint64) core.MessageValue {
	m := voteFrom(core.MsgVote, 3, term, lastTerm, lastIndex)
	m.Flags = core.FlagTransfer
	m.TransferIssuer, m.TransferTerm = 1, issuerTerm
	return m
}

func checkRestrictionUnderTransfer(mut mutation) error {
	f, now := followerOfOne(mut, chainEntries(0, 1, 2, 2))
	f.Step(now, transferVote(3, 2, 1, 1))
	if granted(f.Ready(), core.MsgVoteResp) {
		return errors.New("the transfer flag lifted the election restriction")
	}
	return nil
}

func checkTransferFlagTerm(mut mutation) error {
	f, now := followerOfOne(mut, nil)
	f.Step(now, transferVote(4, 2, 0, 0))
	if granted(f.Ready(), core.MsgVoteResp) {
		return errors.New("a transfer flag was honoured at another term")
	}
	g, now := followerOfOne(0, nil)
	g.Step(now, transferVote(3, 2, 0, 0))
	if !granted(g.Ready(), core.MsgVoteResp) {
		return errors.New("a valid transfer flag was refused")
	}
	return nil
}

func checkLiveLeaderRefusal(mut mutation) error {
	for _, kind := range []core.MessageKind{core.MsgVote, core.MsgPreVote} {
		f, now := followerOfOne(mut, nil)
		f.Step(now, voteFrom(kind, 3, 3, 0, 0))
		rd := f.Ready()
		if granted(rd, core.MsgVoteResp) || granted(rd, core.MsgPreVoteResp) {
			return fmt.Errorf("%v granted while a live leader was heard", kind)
		}
		if f.term != 2 {
			return fmt.Errorf("a refusal moved the term to %d", f.term)
		}
	}
	return nil
}

func checkHeartbeatClamped(mut mutation) error {
	r := leaderOf(2, 2, chainEntries(0, 1, 1, 1), mut, false)
	r.broadcastHeartbeat(r.now)
	for _, m := range r.Ready().Messages {
		if m.Kind == core.MsgHeartbeat && m.Commit > 0 {
			return fmt.Errorf("heartbeat to %d carried commit %d above its match 0", m.To, m.Commit)
		}
	}
	return nil
}

func checkRestartRefusal(mut mutation) error {
	f := newTest(2, testState{term: 2, commit: 0}, mut, false)
	f.Step(t0.Add(100*time.Millisecond), voteFrom(core.MsgVote, 3, 3, 0, 0))
	if granted(f.Ready(), core.MsgVoteResp) {
		return errors.New("a node voted inside its restart window")
	}
	return nil
}

func checkTimeoutNowTerm(mut mutation) error {
	f, now := followerOfOne(mut, nil)
	f.Step(now, msg(core.MsgTimeoutNow, 3, 2, 2))
	f.Step(now, msg(core.MsgTimeoutNow, 1, 2, 3))
	if f.role != core.RoleFollower || f.term > 3 || (f.term == 3 && f.vote == 2) {
		return fmt.Errorf("TimeoutNow accepted without the term and leader check (role %v term %d)", f.role, f.term)
	}
	return nil
}

func checkStaleCompletion(mut mutation) error {
	f := newTest(2, testState{term: 1, commit: 0}, mut, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Entries = chainEntries(0, 1, 1, 1, 1)
	f.Step(t0, m)
	rd1 := f.Ready()
	m2 := msg(core.MsgApp, 3, 2, 2)
	m2.Index, m2.LogTerm = 1, 1
	m2.Entries = chainEntries(m.Entries[0].ChainSum, 2, 2)
	f.Step(t0, m2)
	f.Ready()
	f.StorageDone(rd1.Seq, 3, 1)
	if f.log.stable >= 2 {
		return fmt.Errorf("a superseded completion counted index 2 as durable (stable %d)", f.log.stable)
	}
	return nil
}

func confirmedLeader(mut mutation) *Raft {
	r := leaderOf(2, 0, nil, mut, true)
	m := msg(core.MsgAppResp, 2, 1, 2)
	m.Index = r.log.lastIndex()
	r.Step(r.now, m)
	r.broadcastHeartbeat(r.now)
	hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
	hb.Context = r.nextRound
	r.Step(r.now, hb)
	r.Ready()
	return r
}

func checkLeaseDroppedAfterTransfer(mut mutation) error {
	r := confirmedLeader(mut)
	if !r.LeaseValid(r.now) {
		return errors.New("setup: no lease")
	}
	r.TransferLeadership(r.now, 3)
	if r.LeaseValid(r.now) {
		return errors.New("the lease survived a transfer attempt")
	}
	return nil
}

// D9 d: a follower that still applies an older, shorter contract bounds the
// lease, whatever the leader's newer view says.
func checkLeaseFromAckingMinimum(mut mutation) error {
	r := leaderOf(2, 0, nil, mut, true)
	m := msg(core.MsgAppResp, 2, 1, 2)
	m.Index = r.log.lastIndex()
	r.Step(r.now, m)
	long := r.timing
	long.Generation, long.ElectionTimeoutMin, long.ElectionTimeoutMax = 2, 10*time.Second, 20*time.Second
	r.contracts[2], r.timing = long, long
	r.broadcastHeartbeat(r.now)
	hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
	hb.Context, hb.Contract = r.nextRound, 1
	r.Step(r.now, hb)
	if !r.LeaseValid(r.now) {
		return errors.New("setup: no lease")
	}
	if r.LeaseValid(r.now.Add(2 * time.Second)) {
		return errors.New("the lease outlived the acknowledging majority's shorter contract")
	}
	return nil
}

func checkMembershipAfterNoOp(mut mutation) error {
	r := leaderOf(2, 0, nil, mut, false)
	nm := core.MemberValue{ID: 9, SPKI: [32]byte{9}}
	data := wire.AppendAdmin(nil, &wire.AdminValue{Change: wire.ChangeAddLearner, Membership: core.MembershipValue{Members: []core.MemberValue{nm}}})
	_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}})
	if code == 0 {
		return errors.New("a membership change was accepted before the term's no-op committed")
	}
	return nil
}

func checkChainVerified(mut mutation) error {
	f := newTest(2, testState{term: 1, commit: 0}, mut, false)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Entries = chainEntries(0, 1, 1)
	m.Entries[0].ChainSum ^= 1
	f.Step(t0, m)
	if f.log.lastIndex() != 0 {
		return errors.New("an entry whose chain value does not match was appended")
	}
	return nil
}

// forwardAnswers returns every answer to the forward ctx in the turns since
// the last Ready.
func forwardAnswers(r *Raft, ctx uint64) []core.MessageValue {
	rd := r.Ready()
	var out []core.MessageValue
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if (m.Kind == core.MsgForwardResp || m.Kind == core.MsgForwardAdminResp) && m.Context == ctx {
			out = append(out, m)
		}
	}
	return out
}

// checkForwardDeduped delivers one forward twice to a leader: one entry is
// appended, and both deliveries are answered with its position (#401).
func checkForwardDeduped(mut mutation) error {
	r := leaderOf(2, 0, nil, mut, false)
	last := r.log.lastIndex()
	m := forwardCmd(2, 7, 10)
	r.Step(r.now, m)
	r.Step(r.now, m)
	as := forwardAnswers(r, 7)
	switch {
	case r.log.lastIndex() != last+1:
		return fmt.Errorf("one forward delivered twice appended %d entries", r.log.lastIndex()-last)
	case len(as) != 2 || as[0].Reject || as[0].Index != last+1 || !reflect.DeepEqual(as[0], as[1]):
		return fmt.Errorf("the two deliveries were answered %+v", as)
	}
	return nil
}

func checkSimSafety(mut mutation) error {
	for seed := uint64(1); seed <= 8; seed++ {
		s := newSim(simOptions{seed: seed, drop: 0.05, dup: 0.02, crashRate: 1, diskMax: 30 * time.Millisecond, mut: mut})
		for k := 0; k < 4 && s.err == nil; k++ {
			if k%2 == 0 {
				s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
			} else {
				s.heal()
			}
			s.run(s.after(4*time.Second), true)
		}
		if s.err != nil {
			return fmt.Errorf("seed %d: %w", seed, s.err)
		}
	}
	return nil
}

var checks = map[string]func(mutation) error{
	"ack waits for sync":            checkAckWaitsForSync,
	"vote kept on same-term down":   checkVoteKeptOnSameTermStepDown,
	"commit within verified prefix": checkCommitVerifiedPrefix,
	"ack the verified prefix":       checkAckVerifiedPrefix,
	"snapshot ack index":            checkSnapshotAckIndex,
	"no-table install base":         checkNoTableBase,
	"figure 8":                      checkFigure8,
	"vote restriction":              checkVoteRestriction,
	"prevote restriction":           checkPreVoteRestriction,
	"restriction under transfer":    checkRestrictionUnderTransfer,
	"transfer flag term":            checkTransferFlagTerm,
	"live leader refusal":           checkLiveLeaderRefusal,
	"heartbeat commit clamped":      checkHeartbeatClamped,
	"restart refusal":               checkRestartRefusal,
	"timeout-now term":              checkTimeoutNowTerm,
	"stale completion":              checkStaleCompletion,
	"lease dropped after transfer":  checkLeaseDroppedAfterTransfer,
	"membership after no-op":        checkMembershipAfterNoOp,
	"lease from acking minimum":     checkLeaseFromAckingMinimum,
	"chain verified":                checkChainVerified,
	"forward deduplicated":          checkForwardDeduped,
	"simulated safety":              checkSimSafety,
	"simulated linearizability":     checkSimLinearizable,
	"deposed leader read":           checkDeposedLeaderRead,
}

func TestChecksPassUnmutated(t *testing.T) {
	// One parallel subtest per check: they share no state, and the two
	// simulated ones are most of the target's serial CPU (#304, eachSeed).
	for name, c := range checks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := c(0); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}

// TestMutationsAreCaught reintroduces each cited bug and requires the checks
// to fail on it (D15: one mutation per cited bug; a checker never seen
// failing is not a checker).
func TestMutationsAreCaught(t *testing.T) {
	muts := map[string]mutation{
		"ack before sync":                 mutAckBeforeSync,
		"same-term vote clearing":         mutSameTermVoteClear,
		"commit beyond verified prefix":   mutCommitBeyondVerified,
		"acknowledging lastIndex":         mutAckLastIndex,
		"snapshot ack at lastIndex":       mutSnapshotAckLastIndex,
		"install below the log's base":    mutInstallBelowBase,
		"older term committed by count":   mutCommitOldTermByCount,
		"restriction dropped (vote)":      mutNoRestrictionVote,
		"restriction dropped (prevote)":   mutNoRestrictionPreVote,
		"transfer flag at any term":       mutTransferAnyTerm,
		"heartbeat commit unclamped":      mutHeartbeatUnclamped,
		"no refusal after restart":        mutNoRestartRefusal,
		"timeout-now without term check":  mutTimeoutNowAnyTerm,
		"stale completion counted":        mutStaleCompletion,
		"lease kept after transfer":       mutLeaseAfterTransfer,
		"membership before no-op":         mutMembershipBeforeNoOp,
		"live-leader refusal in prevote":  mutLiveLeaderPreVoteOnly,
		"restriction skipped on transfer": mutRestrictionUnderTransfer,
		"chain mismatch appended":         mutChainSkip,
		"lease from the leader's view":    mutLeaseLeaderView,
		"read without a round":            mutReadNoRound,
		"forward proposed twice":          mutForwardNoDedupe,
	}
	for name, m := range muts {
		caught := ""
		for cname, c := range checks {
			if cname == "simulated safety" {
				continue
			}
			if cname == "simulated linearizability" && m != mutReadNoRound {
				continue
			}
			if err := c(m); err != nil {
				caught = cname + ": " + err.Error()
				break
			}
		}
		if caught == "" {
			t.Errorf("mutation %q was not caught", name)
			continue
		}
		t.Logf("mutation %q caught by %s", name, caught)
	}
}

// TestSimulatorCatchesAckBeforeSync is the negative control of the
// simulator's own oracles: the bug raft-sql-poc measured must be found by
// the seeded crash schedules alone, without the constructed check.
func TestSimulatorCatchesAckBeforeSync(t *testing.T) {
	if checkSimSafety(mutAckBeforeSync|mutCommitOldTermByCount) == nil {
		t.Fatal("the simulator's oracles found nothing with acks before sync and Figure 8")
	}
}

// R3-09: a silent node grants no vote and acknowledges no append until a
// leader of a term at least its own rewrote its log past the truncation
// point and committed an entry of its term.
func TestSilentNodeStaysSilentUntilTheConditionHolds(t *testing.T) {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, SilentTerm: 3, SilentIndex: 2,
	}
	f := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 3}, Snapshot: core.SnapshotMetaValue{Membership: testConf(1, 2, 3)},
		Entries: chainEntries(0, 1, 1), Now: t0,
	})
	f.Step(t0.Add(5*time.Second), voteFrom(core.MsgVote, 3, 4, 9, 9))
	if granted(f.Ready(), core.MsgVoteResp) {
		t.Fatal("a silent node voted")
	}
	ents := chainEntries(0, 1, 1, 4, 4)
	m := msg(core.MsgApp, 1, 2, 4)
	m.Entries, m.Commit = ents[1:2], 1
	m.Index, m.LogTerm = 1, 1
	f.Step(t0.Add(5*time.Second), m)
	rd := f.Ready()
	for _, x := range append(rd.AfterSync, rd.Messages...) {
		if x.Kind == core.MsgAppResp && !x.Reject {
			t.Fatal("a silent node acknowledged before the leader committed in its term")
		}
	}
	m = msg(core.MsgApp, 1, 2, 4)
	m.Index, m.LogTerm, m.Entries, m.Commit = 2, 4, ents[2:3], 3
	f.Step(t0.Add(5*time.Second), m)
	rd = f.Ready()
	if !rd.SilenceEnded || f.cfg.SilentIndex != 0 {
		t.Fatal("the silence condition held and the node stayed silent")
	}
	if !granted(rd, core.MsgAppResp) {
		t.Fatal("no acknowledgement once silence ended")
	}
}

// forwardCmd is a MsgForward from node 2 carrying one command of size bytes.
func forwardCmd(term uint64, ctx uint64, size int) core.MessageValue {
	d := make([]byte, size)
	m := msg(core.MsgForward, 2, 1, term)
	m.Context = ctx
	m.Entries = []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}}
	return m
}

// forwardAnswer is the leader's answer to the forward ctx.
func forwardAnswer(r *Raft, ctx uint64) (core.MessageValue, bool) {
	rd := r.Ready()
	for _, m := range append(rd.Messages, rd.AfterSync...) {
		if m.Kind == core.MsgForwardResp && m.Context == ctx {
			return m, true
		}
	}
	return core.MessageValue{}, false
}

// A forwarded command meets the bounds a local one meets: the entry size,
// the uncommitted bytes and the apply backpressure (review M2).
func TestForwardedCommandsAreBounded(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	r.cfg.MaxEntryBytes, r.cfg.MaxForwardBytes = 100, 250
	last := r.log.lastIndex()
	r.Step(r.now, forwardCmd(2, 1, 101))
	if a, ok := forwardAnswer(r, 1); !ok || !a.Reject || a.Code != core.CodeEntryTooLarge {
		t.Fatalf("oversized forward: %+v", a)
	}
	if r.log.lastIndex() != last {
		t.Fatal("an oversized forwarded entry was appended")
	}
	for ctx := uint64(2); ctx <= 3; ctx++ {
		r.Step(r.now, forwardCmd(2, ctx, 100))
		if a, ok := forwardAnswer(r, ctx); !ok || a.Reject {
			t.Fatalf("forward %d: %+v", ctx, a)
		}
	}
	r.Step(r.now, forwardCmd(2, 4, 100))
	if a, ok := forwardAnswer(r, 4); !ok || !a.Reject || a.Code != core.CodeOverloaded {
		t.Fatalf("forward past the uncommitted bound: %+v", a)
	}
	// Once they commit, the bound frees.
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), r.term)
	ack := msg(core.MsgAppResp, 2, 1, r.term)
	ack.Index = r.log.lastIndex()
	r.Step(r.now, ack)
	r.Step(r.now, forwardCmd(2, 5, 100))
	if a, ok := forwardAnswer(r, 5); !ok || a.Reject {
		t.Fatalf("forward after the commit: %+v", a)
	}
	r.SetOverloaded(true)
	r.Step(r.now, forwardCmd(2, 6, 10))
	if a, ok := forwardAnswer(r, 6); !ok || !a.Reject || a.Code != core.CodeOverloaded {
		t.Fatalf("forward under apply backpressure: %+v", a)
	}
}

// A refusal is replayed too: a forward refused as OVERLOADED, its duplicate
// delivered once the bound freed, is refused again and never appended, or
// the follower, told nothing was appended, would retry a command already in
// the log (#401).
func TestADuplicatedForwardReplaysItsRefusal(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	r.cfg.MaxForwardBytes = 50
	last := r.log.lastIndex()
	m := forwardCmd(2, 8, 100)
	r.Step(r.now, m)
	r.cfg.MaxForwardBytes = 1 << 20
	r.Step(r.now, m)
	as := forwardAnswers(r, 8)
	if len(as) != 2 || !as[0].Reject || as[0].Code != core.CodeOverloaded || !reflect.DeepEqual(as[0], as[1]) {
		t.Fatalf("answers %+v", as)
	}
	if r.log.lastIndex() != last {
		t.Fatal("the duplicate of a refused forward was appended")
	}
	// A new submission id is a new submission: it is proposed.
	r.Step(r.now, forwardCmd(2, 9, 100))
	if as := forwardAnswers(r, 9); len(as) != 1 || as[0].Reject || r.log.lastIndex() != last+1 {
		t.Fatalf("a new submission: %+v", as)
	}
}

// The answers kept are bounded: past maxFwdAnswers the oldest is evicted
// first, the newest kept.
func TestForwardAnswersAreBounded(t *testing.T) {
	f := newTest(2, testState{term: 1}, 0, false)
	const extra = 5
	for ctx := uint64(1); ctx <= uint64(maxFwdAnswers+extra); ctx++ {
		f.Step(t0, forwardCmd(1, ctx, 1))
	}
	tab := &f.fwdAnswers
	if len(tab.byKey) != maxFwdAnswers || len(tab.order) != maxFwdAnswers {
		t.Fatalf("kept %d answers in a ring of %d, bound %d", len(tab.byKey), len(tab.order), maxFwdAnswers)
	}
	for ctx := uint64(1); ctx <= uint64(maxFwdAnswers+extra); ctx++ {
		_, kept := tab.lookup(fwdKey{from: 2, ctx: ctx})
		if kept != (ctx > extra) {
			t.Fatalf("answer %d kept %v", ctx, kept)
		}
	}
}

// The table outlives a term: the leader of term 2 that leads again at
// term 3 answers a duplicate of a term-2 forward with the term-2 answer and
// proposes nothing (#401).
func TestADuplicateAfterANewTermIsNotProposedAgain(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	r.Step(r.now, forwardCmd(2, 9, 10))
	first := forwardAnswers(r, 9)
	if len(first) != 1 || first[0].Reject || first[0].LogTerm != 2 {
		t.Fatalf("term 2: %+v", first)
	}
	r.term, r.vote = 3, 1
	r.becomeLeader()
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), 3)
	r.Ready()
	last := r.log.lastIndex()
	r.Step(r.now, forwardCmd(3, 9, 10))
	as := forwardAnswers(r, 9)
	if len(as) != 1 || as[0].Reject || as[0].Index != first[0].Index || as[0].LogTerm != 2 {
		t.Fatalf("term 3: %+v, want the term-2 answer %+v", as, first[0])
	}
	if r.log.lastIndex() != last {
		t.Fatal("a duplicate was proposed again after a new term")
	}
}

// An entry kind this version does not know is never appended, acknowledged
// or skipped: the follower appends the entries before it and asks the
// driver to stop with VERSION_UNSUPPORTED (review M3).
func TestUnknownEntryKindStopsTheFollower(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1)}, 0, false)
	es := chainEntries(f.log.chain(1), 2, 1, 1)
	es[1].Kind = 0x80
	es[1].ChainSum = wire.ChainSum(es[0].ChainSum, &es[1])
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 1, 1, 3, es
	f.Step(t0, m)
	rd := f.Ready()
	if rd.Unsupported.Index != 3 || rd.Unsupported.Kind != 0x80 {
		t.Fatalf("unsupported: %+v", rd.Unsupported)
	}
	if f.log.lastIndex() != 2 {
		t.Fatalf("last %d, want 2: the unknown entry was appended", f.log.lastIndex())
	}
	for _, x := range append(rd.AfterSync, rd.Messages...) {
		if x.Kind == core.MsgAppResp && x.Index > 2 {
			t.Fatalf("acknowledged %d past the known entries", x.Index)
		}
	}
	if f.log.committed > 2 {
		t.Fatalf("committed %d past the known entries", f.log.committed)
	}
}

// A cluster version above what this version speaks is refused at the
// leader, and stops a node that finds it committed: no member of protocol
// version 1 announces more (review M3).
func TestClusterVersionAboveTheProtocolIsRefused(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	data := wire.AppendAdmin(nil, &wire.AdminValue{Pre: core.PreconditionSpec{Log: r.cfg.Log}, ClusterVersion: ProtocolMax + 1})
	if _, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryClusterVersion, BodySum: wire.BodySum(data), Data: data}}); code != core.CodeVersionUnsupported {
		t.Fatalf("raise above the protocol: %v", code)
	}
	f := newTest(2, testState{term: 1, commit: 0, ents: chainEntries(0, 1, 1)}, 0, false)
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 2}, Kind: core.EntryClusterVersion, Data: data}
	e.BodySum = wire.BodySum(data)
	e.ChainSum = wire.ChainSum(f.log.chain(1), &e)
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 1, 1, 2, []core.EntryValue{e}
	f.Step(t0, m)
	if u := f.Ready().Unsupported; u.Index != 2 {
		t.Fatalf("a committed raise above the protocol: %+v", u)
	}
	if f.clusterVer > ProtocolMax {
		t.Fatalf("cluster version raised to %d", f.clusterVer)
	}
}

// A learner whose key a member already holds is refused: the transport
// admits a connection by its key and would let it speak as that member
// (review M5).
func TestAddLearnerRefusesAKeyAMemberHolds(t *testing.T) {
	r := leaderOf(2, 0, nil, 0, false)
	rd := r.Ready()
	r.StorageDone(rd.Seq, r.log.lastIndex(), r.term)
	for _, id := range []core.NodeID{2, 3} {
		ack := msg(core.MsgAppResp, id, 1, r.term)
		ack.Index = r.log.lastIndex()
		r.Step(r.now, ack)
	}
	if r.log.committed != r.log.lastIndex() {
		t.Fatalf("setup: no-op not committed (%d/%d)", r.log.committed, r.log.lastIndex())
	}
	add := func(nm core.MemberValue) errs.Code {
		data := wire.AppendAdmin(nil, &wire.AdminValue{Change: wire.ChangeAddLearner, Membership: core.MembershipValue{Members: []core.MemberValue{nm}}})
		_, code := r.Propose(r.now, []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}})
		return code
	}
	if code := add(core.MemberValue{ID: 9, SPKI: [32]byte{2}}); code != core.CodeMembershipInvalid {
		t.Fatalf("a learner under member 2's key: %v", code)
	}
	if code := add(core.MemberValue{ID: 9, SPKI: [32]byte{9}}); code != 0 {
		t.Fatalf("a learner under its own key: %v", code)
	}
}

// checkNoTableBase: a node with no table still refuses a snapshot older
// than its own log's base. Installing it would drop durable entries the
// node acknowledged, committed ones included, and leave its commit above
// its log. The simulator's snapshot schedule found it (seed 3).
func checkNoTableBase(mut mutation) error {
	cfg := &Config{
		ID: 2, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, NoTable: true, mut: mut,
	}
	all := chainEntries(0, 1, 1, 1, 1, 1, 1, 1)
	base := core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 4}, ChainTip: all[3].ChainSum, Membership: testConf(1, 2, 3)}
	f := NewRaft(cfg, &StartState{HardState: core.HardState{Term: 1, Commit: 6}, Snapshot: base, Entries: all[4:], Applied: 4, Now: t0})
	f.lead, f.term = 1, 1
	old := core.SnapshotMetaValue{Position: core.PositionValue{Term: 1, Index: 2}, ChainTip: all[1].ChainSum, Membership: testConf(1, 2, 3)}
	if code := f.CheckOffer(1, core.InstallOfferValue{Term: 1, Meta: old}, true); code == 0 || code == core.CodeAdminPreconditionFailed {
		return fmt.Errorf("an offer below the log's base was accepted (%v)", code)
	}
	f.InstallSnapshot(old)
	if f.log.lastIndex() < 6 || f.log.committed > f.log.lastIndex() {
		return fmt.Errorf("an older install dropped the log: last %d commit %d", f.log.lastIndex(), f.log.committed)
	}
	return nil
}

// engineEntry is the engine entry of kind at index, of term 1, chained on
// prev, carrying admin.
func engineEntry(prev uint32, index uint64, kind core.EntryKind, admin *wire.AdminValue) core.EntryValue {
	data := wire.AppendAdmin(nil, admin)
	e := core.EntryValue{Position: core.PositionValue{Term: 1, Index: index}, Kind: kind, BodySum: wire.BodySum(data), Data: data}
	e.ChainSum = wire.ChainSum(prev, &e)
	return e
}

// A snapshot cut at an index carries the engine state as the committed
// entries through that index left it. The engine applies its entries at
// commit, ahead of the applied index a cut is made at: the state as it
// stands now named a contract, a cluster version and an alarm committed
// past the cut, and a node that installed that snapshot and was truncated
// back carried a contract whose entry it never applied (#402).
func TestEngineAtIsTheStateTheCommittedEntriesThroughTheIndexLeft(t *testing.T) {
	f := newTest(2, testState{term: 1, commit: 3, ents: chainEntries(0, 1, 1, 1, 1)}, 0, false)
	before := f.EngineAt(3)
	next := testTiming()
	next.HeartbeatInterval = 50 * time.Millisecond
	e4 := engineEntry(f.log.chain(3), 4, core.EntryTimingContract, &wire.AdminValue{Pre: core.PreconditionSpec{Generation: 1}, Contract: next})
	e5 := engineEntry(e4.ChainSum, 5, core.EntryNoSpaceRaise, &wire.AdminValue{})
	e6 := engineEntry(e5.ChainSum, 6, core.EntryClusterVersion, &wire.AdminValue{ClusterVersion: ProtocolMax})
	m := msg(core.MsgApp, 1, 2, 1)
	m.Index, m.LogTerm, m.Commit, m.Entries = 3, 1, 6, []core.EntryValue{e4, e5, e6}
	f.Step(t0, m)
	if f.log.committed != 6 {
		t.Fatalf("setup: committed %d, want 6", f.log.committed)
	}
	if st := f.Status(); st.Timing.Generation != 2 || st.NoSpace != 5 || st.Cluster != ProtocolMax {
		t.Fatalf("setup: the engine entries did not apply: %+v", st)
	}
	if got := f.EngineAt(3); got != before || got.Timing.Generation != 1 || got.NoSpace != 0 || got.Cluster != 0 {
		t.Fatalf("EngineAt(3) = %+v, want the state before the engine entries %+v", got, before)
	}
	if got := f.EngineAt(4); got.Timing.Generation != 2 || got.Timing.HeartbeatInterval != next.HeartbeatInterval || got.NoSpace != 0 || got.Cluster != 0 {
		t.Fatalf("EngineAt(4) = %+v, want the contract alone", got)
	}
	if got := f.EngineAt(5); got.Timing.Generation != 2 || got.NoSpace != 5 || got.Cluster != 0 {
		t.Fatalf("EngineAt(5) = %+v, want the contract and the alarm", got)
	}
	if got := f.EngineAt(6); got != (EngineValue{Timing: f.timing, Cluster: f.clusterVer, ClusterG: f.clusterGen, NoSpace: f.noSpace}) {
		t.Fatalf("EngineAt(6) = %+v, want the state as it stands", got)
	}
	// A node that installs the snapshot cut at 3, and restarts on it with
	// its log truncated back, holds no contract it never applied.
	eng := f.EngineAt(3)
	meta := core.SnapshotMetaValue{
		Position: core.PositionValue{Term: 1, Index: 3}, ChainTip: f.log.chain(3), Membership: testConf(1, 2, 3),
		Timing: eng.Timing, ClusterVersion: eng.Cluster, ClusterGeneration: eng.ClusterG, NoSpace: eng.NoSpace,
	}
	g := newTest(3, testState{term: 1}, 0, false)
	g.InstallSnapshot(meta)
	if st := g.Status(); st.Timing.Generation != 1 || st.NoSpace != 0 || st.Cluster != 0 {
		t.Fatalf("an install of the cut at 3 carries %+v", st)
	}
	if got := g.EngineAt(3); got != g.engine() {
		t.Fatalf("after an install, EngineAt(3) = %+v, want the installed state %+v", got, g.engine())
	}
	cfg := g.cfg
	h := NewRaft(&cfg, &StartState{HardState: core.HardState{Term: 1, Commit: 3}, Snapshot: meta, Now: t0})
	if st := h.Status(); st.Timing.Generation != 1 || st.NoSpace != 0 || st.Cluster != 0 {
		t.Fatalf("a restart on the cut at 3 carries %+v", st)
	}
	// Compaction folds the records it passes into the base, and keeps the
	// answer for every index above it.
	f.log.applied = 6
	f.Compact(4)
	if got := f.EngineAt(4); got.Timing.Generation != 2 || got.NoSpace != 0 {
		t.Fatalf("after compacting through 4, EngineAt(4) = %+v", got)
	}
	if got := f.EngineAt(5); got.NoSpace != 5 || got.Cluster != 0 {
		t.Fatalf("after compacting through 4, EngineAt(5) = %+v", got)
	}
	if len(f.engines) != 2 {
		t.Fatalf("after compacting through 4, %d records kept, want 2", len(f.engines))
	}
}
