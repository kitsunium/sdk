package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// exLeader is member 1 of members 1 to 3, the leader of term 2 cut off from
// the others with its commit at 5, that appended its clients' proposals
// 6..last at term 2 before CheckQuorum deposed it, then went down. Its
// configuration does not list member 4, admitted, promoted and made leader
// while it was away.
func exLeader(last uint64) *Raft {
	conf := testConf(1, 2, 3)
	conf.Index = 5
	terms := make([]uint64, 0, last-5)
	for i := uint64(6); i <= last; i++ {
		terms = append(terms, 2)
	}
	cfg := &Config{
		ID: 1, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3,
	}
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: 2, Vote: 1, Commit: 5},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: 2, Index: 5}, Membership: conf,
			Timing: testTiming(), ClusterVersion: Protocol3, ClusterGeneration: 2,
		},
		Entries: chainEntries(0, 6, terms...),
		Now:     t0,
	})
	r.Ready()
	return r
}

// stranded runs member 1 back from its outage against members 2 and 3,
// followers of leader 4 at term 5 with their commit at 10, for d: 2 and 3
// hear 4's heartbeat every HeartbeatInterval, and every frame between 1, 2
// and 3 is delivered at once. Leader 4's own frames to 1 are refused by
// 1's transport unless 1 admits 4. It reports whether 1 ever admitted 4,
// how many PreVotes 1 sent and how many leader records it was sent.
func stranded(t *testing.T, a *Raft, d time.Duration) (bool, int, int) {
	t.Helper()
	b := nodeOf(2, 5, testConf(1, 2, 3, 4), Protocol3, 0)
	c := nodeOf(3, 5, testConf(1, 2, 3, 4), Protocol3, 0)
	peers := map[core.NodeID]*Raft{2: b, 3: c}
	start := b.now
	a.now = start
	preVotes, records, admitted := 0, 0, false
	var queue []core.MessageValue
	drain := func(r *Raft) {
		rd := r.Ready()
		queue = append(queue, rd.Messages...)
		queue = append(queue, rd.AfterSync...)
	}
	for now := start; now.Before(start.Add(d)); now = now.Add(10 * time.Millisecond) {
		if now.Sub(start)%testTiming().HeartbeatInterval == 0 {
			for id, p := range peers {
				hb := msg(core.MsgHeartbeat, 4, id, 5)
				hb.Commit = 10
				p.Step(now, hb)
				p.Ready() // the answers go to 4
			}
		}
		a.Tick(now)
		drain(a)
		for len(queue) > 0 {
			m := queue[0]
			queue = queue[1:]
			switch {
			case m.From == 1 && peers[m.To] != nil:
				if m.Kind == core.MsgPreVote {
					preVotes++
				}
				peers[m.To].Step(now, m)
				drain(peers[m.To])
			case m.To == 1:
				if m.Kind == core.MsgLeaderRecord {
					records++
				}
				a.Step(now, m)
				drain(a)
			}
		}
		if admits(a, 4) {
			admitted = true
		}
	}
	return admitted, preVotes, records
}

// A stale voter whose log ends below its members' commit is sent leader 4's
// record and admits it (#542): the control. The same voter with an
// uncommitted tail of its old term that reaches past that commit is sent it
// too and admits 4 on a quiet cluster (#675): behindCommit judges the
// PreVote by its last term as well as its last index, and a tail of term 2
// at index 40 holds nothing the members committed at terms 3 to 5. Judged
// on the last index alone (m.Index >= r.log.committed), as before, the
// voter was sent no record, PreVote after PreVote, for as long as the
// cluster stayed quiet.
func TestAStaleVoterWithAnOldTailPastTheCommitIsSentTheRecord(t *testing.T) {
	const quiet = 60 * time.Second
	ok, n, recs := stranded(t, exLeader(8), quiet)
	t.Logf("control, a tail to 8 of term 2: admitted 4=%v, %d PreVotes, %d records", ok, n, recs)
	if !ok {
		t.Fatalf("control: a stale voter whose log ends at 8, below the commit 10, never admitted leader 4 (%d PreVotes)", n)
	}
	a := exLeader(40)
	ok, n, recs = stranded(t, a, quiet)
	st := a.Status()
	t.Logf("stale voter with a tail to 40 of term 2: admitted 4=%v after %v, %d PreVotes, %d records, term %d, role %v, lead %d",
		ok, quiet, n, recs, st.Term, st.Role, st.Lead)
	if !ok {
		t.Fatalf("a stale voter whose old-term tail ends at 40, past its members' commit 10, sent %d PreVotes in %v and was never sent leader 4's record: it never hears the leader while the cluster is quiet", n, quiet)
	}
}

// answersWithRecord reports whether member 2, a follower of the live leader
// 4 at term 5 with its commit at 10 of term 5, answers with 4's record a
// PreVote of member 1 that announces protocol 2 and a log ending at
// (logTerm, index).
func answersWithRecord(mut mutation, logTerm, index uint64) bool {
	b := nodeOf(2, 5, testConf(1, 2, 3, 4), Protocol2, mut)
	hb := msg(core.MsgHeartbeat, 4, 2, 5)
	hb.Commit = 10
	b.Step(b.now, hb)
	b.Ready()
	pv := msg(core.MsgPreVote, 1, 2, 6)
	pv.LogTerm, pv.Index, pv.Flags = logTerm, index, core.FlagProtocol2
	b.Step(b.now, pv)
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord && m.To == 1 {
			return true
		}
	}
	return false
}

// checkLeaderRecordReachesAnOldTail: a follower of a live leader judges a
// PreVote behind its commit by the log's last term and last index, never
// its index alone (#675). A log shorter than the commit is behind whatever
// its last term (#542); one whose last term is older than the committed
// entry's cannot hold that entry, however far its index reaches. A log
// whose last term is the committed entry's, at or past its index, holds
// it, and one of a later term at or past it may: neither is sent the
// record, which is for a member behind the commit.
func checkLeaderRecordReachesAnOldTail(mut mutation) error {
	for _, c := range []struct {
		why            string
		logTerm, index uint64
		record         bool
	}{
		{"an uncommitted tail of term 2 to 40, past the commit 10 of term 5", 2, 40, true},
		{"a tail of term 4 to the commit's index", 4, 10, true},
		{"a log of term 2 ending at 3, below the commit (#542)", 2, 3, true},
		{"a log of a later term ending below the commit (#542)", 6, 8, true},
		{"a log level with the commit", 5, 10, false},
		{"a log of the commit's term past it", 5, 40, false},
		{"a log of a later term at the commit", 6, 10, false},
		{"a log of a later term past the commit", 6, 40, false},
	} {
		if got := answersWithRecord(mut, c.logTerm, c.index); got != c.record {
			return fmt.Errorf("%s (last term %d, last index %d): record sent %v, want %v", c.why, c.logTerm, c.index, got, c.record)
		}
	}
	return nil
}

func TestALeaderRecordJudgesTheVoterByItsLastTermAndIndex(t *testing.T) {
	if err := checkLeaderRecordReachesAnOldTail(0); err != nil {
		t.Fatal(err)
	}
	if err := checkLeaderRecordReachesAnOldTail(mutRecordByIndexOnly); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// staleLearner is member 4, a learner of voters 1 to 3, at term 2 over a
// snapshot at 5, which holds an uncommitted tail 6..40 of term 2 that its
// old leader replicated to it and to no voter, and which heard no leader
// since its start at t0.
func staleLearner(mut mutation) *Raft {
	conf := testConf(1, 2, 3, 4)
	conf.Members[3].Voter = false
	conf.Index = 5
	terms := make([]uint64, 35)
	for i := range terms {
		terms[i] = 2
	}
	r := NewRaft(&Config{
		ID: 4, Group: 1, Seed: 4, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, mut: mut,
	}, &StartState{
		HardState: core.HardState{Term: 2, Commit: 5},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: 2, Index: 5}, Membership: conf,
			Timing: testTiming(), ClusterVersion: Protocol2, ClusterGeneration: 2,
		},
		Entries: chainEntries(0, 6, terms...),
		Now:     t0,
	})
	r.Ready()
	return r
}

// checkLearnerWithAnOldTailIsSentTheRecord: a learner whose uncommitted
// tail of an older term reaches past its members' commit asks for the
// leader's record (#639) and is sent it, as a stale voter is (#675). Its
// question carried its last index, 40, past the commit 10, and was dropped
// for as long as the leader led.
func checkLearnerWithAnOldTailIsSentTheRecord(mut mutation) error {
	x := staleLearner(mut)
	at := t0.Add(askElectionTimeouts * testTiming().ElectionTimeoutMax)
	x.Tick(at)
	pv, ok := asked(x.Ready())[2]
	if !ok || pv.LogTerm != 2 || pv.Index != 40 {
		return fmt.Errorf("setup: the learner's question to 2 is %+v (asked %v)", pv, ok)
	}
	// Member 2 follows leader 5, which the learner's configuration does not
	// list, with its commit at 10 of term 5.
	bc := testConf(1, 2, 3, 4, 5)
	bc.Members[3].Voter = false
	b := nodeOf(2, 5, bc, Protocol2, mut)
	hb := msg(core.MsgHeartbeat, 5, 2, 5)
	hb.Commit = 10
	b.Step(b.now, hb)
	b.Ready()
	b.Step(b.now, pv)
	var rec core.MessageValue
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord && m.To == 4 {
			rec = m
		}
	}
	if rec.Kind != core.MsgLeaderRecord {
		return fmt.Errorf("a follower sent no record to the question of a learner whose tail of term 2 reaches 40, past the commit 10 of term 5")
	}
	x.Step(at, rec)
	if rd := x.Ready(); rd.LeaderHint.ID != 5 || !admits(x, 5) {
		return fmt.Errorf("the learner did not admit the leader its member vouched for (hint %d)", rd.LeaderHint.ID)
	}
	return nil
}

func TestALearnerWithAnOldTailPastTheCommitIsSentTheRecord(t *testing.T) {
	if err := checkLearnerWithAnOldTailIsSentTheRecord(0); err != nil {
		t.Fatal(err)
	}
	if err := checkLearnerWithAnOldTailIsSentTheRecord(mutRecordByIndexOnly); err == nil {
		t.Fatal("the mutation was not caught")
	}
}
