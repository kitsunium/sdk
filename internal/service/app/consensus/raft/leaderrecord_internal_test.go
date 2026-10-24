package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// nodeOf is node id at term over a snapshot at index 10 of configuration
// conf, all of it committed, at cluster version cv.
func nodeOf(id core.NodeID, term uint64, conf core.MembershipValue, cv uint32, mut mutation) *Raft {
	cfg := &Config{
		ID: id, Group: 1, Seed: 1, Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
		MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, CanVote: true, MaxChainRefusals: 3, mut: mut,
	}
	conf.Index = 10
	r := NewRaft(cfg, &StartState{
		HardState: core.HardState{Term: term, Commit: 10},
		Snapshot: core.SnapshotMetaValue{
			Position: core.PositionValue{Term: term, Index: 10}, Membership: conf,
			Timing: testTiming(), ClusterVersion: cv, ClusterGeneration: uint64(cv),
		},
		Now: t0,
	})
	r.now = t0.Add(10 * time.Second)
	r.Ready()
	return r
}

// followerOf4 is member 2 of members 1 to 4 following leader 4 at term 5,
// which it heard just now.
func followerOf4(cv uint32) *Raft {
	b := nodeOf(2, 5, testConf(1, 2, 3, 4), cv, 0)
	hb := msg(core.MsgHeartbeat, 4, 2, 5)
	hb.Commit = 10
	b.Step(b.now, hb)
	b.Ready()
	return b
}

// recordFrom is the leader record member 2 sends 1 for leader 4, as
// followerOf4 answers a PreVote of member 1 behind its commit that
// announces protocol 2.
func recordFrom(b *Raft) (core.MessageValue, bool) {
	return recordFor(b, core.FlagProtocol2)
}

// recordFor is recordFrom for a PreVote carrying flags.
func recordFor(b *Raft, flags core.MessageFlags) (core.MessageValue, bool) {
	pv := msg(core.MsgPreVote, 1, 2, 6)
	pv.Index, pv.LogTerm, pv.Flags = 3, 2, flags
	b.Step(b.now, pv)
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord && m.To == 1 {
			return m, true
		}
	}
	return core.MessageValue{}, false
}

// staleVoter is member 1 of members 1 to 3, which knows no leader at term 3.
func staleVoter(mut mutation) *Raft {
	return nodeOf(1, 3, testConf(1, 2, 3), Protocol2, mut)
}

// admits reports whether r's admission set holds id.
func admits(r *Raft, id core.NodeID) bool {
	return slices.ContainsFunc(r.admitted(), func(m core.MemberValue) bool { return m.ID == id })
}

// checkLeaderRecordReplicationOnly: a leader a member vouched for is
// admitted for its appends and heartbeats only; its vote request is
// dropped unanswered (D4 as arbitrated for #542).
func checkLeaderRecordReplicationOnly(mut mutation) error {
	rec, ok := recordFrom(followerOf4(Protocol2))
	if !ok {
		return fmt.Errorf("setup: no leader record")
	}
	a := staleVoter(mut)
	a.Step(a.now, rec)
	if rd := a.Ready(); !admits(a, 4) || rd.LeaderHint.ID != 4 {
		return fmt.Errorf("setup: the vouched-for leader is not admitted")
	}
	v := msg(core.MsgVote, 4, 1, 6)
	v.Index, v.LogTerm = 50, 5
	a.Step(a.now, v)
	if rd := a.Ready(); granted(rd, core.MsgVoteResp) || a.Status().Term != 3 {
		return fmt.Errorf("a leader admitted by a record alone obtained a vote, or moved the term to %d", a.Status().Term)
	}
	hb := msg(core.MsgHeartbeat, 4, 1, 6)
	a.Step(a.now, hb)
	a.Ready()
	if st := a.Status(); st.Lead != 4 || st.Term != 6 {
		return fmt.Errorf("the heartbeat of the vouched-for leader was not followed: %+v", st)
	}
	return nil
}

func TestALeaderRecordAdmitsForReplicationOnly(t *testing.T) {
	if err := checkLeaderRecordReplicationOnly(0); err != nil {
		t.Fatal(err)
	}
	if err := checkLeaderRecordReplicationOnly(mutHintAnyKind); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// A follower sends the record only to a PreVote that announces protocol 2
// (#638), only while it follows a live leader, and only to a member
// campaigning behind its commit; the record is the committed
// configuration's.
func TestALeaderRecordIsSentOnlyToAStaleMemberAtProtocol2(t *testing.T) {
	rec, ok := recordFrom(followerOf4(Protocol2))
	if !ok {
		t.Fatal("a follower of a live leader sent no record to a member behind its commit")
	}
	if rec.Term != 5 || len(rec.Entries) != 1 {
		t.Fatalf("record %+v", rec)
	}
	if err := checkLeaderRecordFollowsTheAnnouncement(0); err != nil {
		t.Fatal(err)
	}
	b := followerOf4(0)
	b.cfg.speaks = 1
	if _, ok := recordFrom(b); ok {
		t.Fatal("a node of protocol 1 sent a record")
	}
	b = followerOf4(Protocol2)
	b.now = b.now.Add(10 * time.Second)
	if _, ok := recordFrom(b); ok {
		t.Fatal("a record was sent with no live leader")
	}
	b = followerOf4(Protocol2)
	pv := msg(core.MsgPreVote, 1, 2, 6)
	pv.Index, pv.LogTerm, pv.Flags = 10, 5, core.FlagProtocol2
	b.Step(b.now, pv)
	for _, m := range b.Ready().Messages {
		if m.Kind == core.MsgLeaderRecord {
			t.Fatal("a record was sent to a member not behind the commit")
		}
	}
}

// checkLeaderRecordFollowsTheAnnouncement: the leader record is sent by
// the PreVote's own announcement of protocol 2, at any cluster version, and
// never to a PreVote without it (#638). Sent only at cluster version 2, it
// waited for a raise the leader refuses while the stale voter it would
// reach is unheard; sent without the announcement, it would reach a node
// of protocol 1, which does not know the kind.
func checkLeaderRecordFollowsTheAnnouncement(mut mutation) error {
	for _, cv := range []uint32{0, 1, Protocol2} {
		b := nodeOf(2, 5, testConf(1, 2, 3, 4), cv, mut)
		hb := msg(core.MsgHeartbeat, 4, 2, 5)
		hb.Commit = 10
		b.Step(b.now, hb)
		b.Ready()
		if _, ok := recordFor(b, core.FlagProtocol2); !ok {
			return fmt.Errorf("cluster version %d: no record to a PreVote announcing protocol 2", cv)
		}
		if _, ok := recordFor(b, 0); ok {
			return fmt.Errorf("cluster version %d: a record to a PreVote that does not announce protocol 2", cv)
		}
	}
	return nil
}

// A record is refused from a non-member, at an older term, for a retired
// or already known ID, for an unpinned key, or while a leader is live; an
// accepted one ends when a configuration lists the leader, when it retires
// it, or when its delay passes.
func TestALeaderRecordIsBoundedAndEnds(t *testing.T) {
	rec, _ := recordFrom(followerOf4(Protocol2))
	for name, mutate := range map[string]func(a *Raft, m *core.MessageValue){
		"from a non-member": func(_ *Raft, m *core.MessageValue) { m.From = 9 },
		"older term":        func(a *Raft, m *core.MessageValue) { a.term = m.Term + 1 },
		"retired id":        func(a *Raft, _ *core.MessageValue) { a.base.Retired = append(a.base.Retired, 4) },
		"live leader": func(a *Raft, _ *core.MessageValue) {
			a.lead, a.lastLeaderContact = 2, a.now
		},
	} {
		a := staleVoter(0)
		m := rec
		mutate(a, &m)
		a.Step(a.now, m)
		if admits(a, 4) {
			t.Fatalf("%s: the record was taken", name)
		}
	}
	a := staleVoter(0)
	a.Step(a.now, rec)
	a.Tick(a.now.Add(hintElectionTimeouts*a.timing.ElectionTimeoutMax - time.Millisecond))
	if !admits(a, 4) {
		t.Fatal("the record ended before its delay")
	}
	a.Tick(a.now.Add(time.Millisecond))
	if admits(a, 4) {
		t.Fatal("the record outlived its delay")
	}
	a = staleVoter(0)
	a.Step(a.now, rec)
	a.base.Retired = append(a.base.Retired, 4)
	a.confChanged()
	if admits(a, 4) || a.hint.ID != 0 {
		t.Fatal("the record outlived the leader's retirement")
	}
}

// vouchedFor4 is staleVoter(mut) once it took member 2's record of leader 4.
func vouchedFor4(mut mutation) (*Raft, error) {
	rec, ok := recordFrom(followerOf4(Protocol2))
	if !ok {
		return nil, fmt.Errorf("setup: no leader record")
	}
	a := staleVoter(mut)
	a.Step(a.now, rec)
	if rd := a.Ready(); !admits(a, 4) || rd.LeaderHint.ID != 4 {
		return nil, fmt.Errorf("setup: the vouched-for leader is not admitted")
	}
	return a, nil
}

// published reports whether the admission set r last handed the driver
// holds id: the set the transport admits keys by.
func published(r *Raft, id core.NodeID) bool {
	return slices.ContainsFunc(r.admittedPub, func(m core.MemberValue) bool { return m.ID == id })
}

// hear4 steps into a, every HeartbeatInterval from from until before until,
// a Tick and then a heartbeat of leader 4 at term 5, and returns the
// instant of the last heartbeat. It fails at the first instant a's
// admission set, or the one it handed the driver, lacks 4.
func hear4(a *Raft, from, until time.Time) (time.Time, error) {
	last, preVotes := from, 0
	for now := from; now.Before(until); now = now.Add(a.timing.HeartbeatInterval) {
		a.Tick(now)
		a.Step(now, msg(core.MsgHeartbeat, 4, 1, 5))
		for _, m := range a.Ready().Messages {
			if m.Kind == core.MsgPreVote {
				preVotes++
			}
		}
		if !admits(a, 4) || !published(a, 4) {
			st := a.Status()
			return now, fmt.Errorf("%v after the record, with a heartbeat of leader 4 every %v (lead %d, term %d, last contact %v ago), the voter dropped it from its admission set: the transport cuts the leader and its snapshot stream (record's delay %v, PreVotes sent %d)",
				now.Sub(from), a.timing.HeartbeatInterval, st.Lead, st.Term, now.Sub(a.lastLeaderContact),
				hintElectionTimeouts*a.timing.ElectionTimeoutMax, preVotes)
		}
		last = now
	}
	return last, nil
}

// checkVouchedLeaderAdmittedWhileHeard: a stale voter keeps the leader a
// member vouched for admitted for as long as it hears it, past the
// record's delay (#618). The record used to end its delay after it was
// taken while the leader's heartbeats still arrived: the transport cut the
// leader's connections, its snapshot stream among them, and a node that
// hears its leader sends no PreVote that could bring a new record. A voter
// whose install streamed longer than the delay was cut at every window and
// never caught up under writes.
func checkVouchedLeaderAdmittedWhileHeard(mut mutation) error {
	a, err := vouchedFor4(mut)
	if err != nil {
		return err
	}
	_, err = hear4(a, a.now, a.now.Add(hintElectionTimeouts*a.timing.ElectionTimeoutMax+2*time.Second))
	return err
}

func TestAVouchedLeaderStaysAdmittedWhileItIsHeard(t *testing.T) {
	if err := checkVouchedLeaderAdmittedWhileHeard(0); err != nil {
		t.Fatal(err)
	}
	if err := checkVouchedLeaderAdmittedWhileHeard(mutHintEndsWhileHeard); err == nil {
		t.Fatal("the mutation was not caught")
	}
}

// A record outlives its delay only while the vouched-for leader is heard:
// once that contact ends, the admission ends hintElectionTimeouts maximum
// election timeouts after the last append or heartbeat this node took from
// it, not before and not never (#618). A message this node does not take
// as that leader's contact extends nothing: a heartbeat of a leader it
// refused for the term (#508), one of an older term, or another leader's.
func TestAVouchedLeaderEndsItsDelayAfterTheLastContact(t *testing.T) {
	hb := func(from core.NodeID, term uint64) core.MessageValue { return msg(core.MsgHeartbeat, from, 1, term) }
	for name, c := range map[string]struct {
		// stop runs once the heartbeats end, at, and returns the instant of
		// the leader's last contact.
		stop func(a *Raft, at time.Time) (time.Time, error)
		// after runs at every HeartbeatInterval until the delay passes.
		after func(a *Raft, now time.Time)
	}{
		"silent leader": {
			stop:  func(_ *Raft, at time.Time) (time.Time, error) { return at, nil },
			after: func(*Raft, time.Time) {},
		},
		"leader refused for the term": {
			stop: func(a *Raft, at time.Time) (time.Time, error) {
				// Three appends whose body no longer matches its sum: the
				// third is still contact, then the leader is refused.
				at = at.Add(a.timing.HeartbeatInterval)
				bad := msg(core.MsgApp, 4, 1, 5)
				bad.Index, bad.LogTerm = 10, 3
				bad.Entries = chainEntries(0, 11, 5)
				bad.Entries[0].BodySum++
				for range a.cfg.MaxChainRefusals {
					a.Step(at, bad)
				}
				a.Ready()
				if !a.refuses(4) {
					return at, fmt.Errorf("setup: leader 4 is not refused after %d chain refusals", a.cfg.MaxChainRefusals)
				}
				return at, nil
			},
			after: func(a *Raft, now time.Time) { a.Step(now, hb(4, 5)) },
		},
		"leader of an older term": {
			stop: func(a *Raft, at time.Time) (time.Time, error) {
				a.Step(at.Add(a.timing.HeartbeatInterval), hb(2, 7))
				a.Ready()
				if st := a.Status(); st.Lead != 2 || st.Term != 7 {
					return at, fmt.Errorf("setup: the voter does not follow member 2 at term 7: %+v", st)
				}
				return at, nil
			},
			after: func(a *Raft, now time.Time) {
				a.Step(now, hb(4, 5))
				a.Step(now, hb(2, 7))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, err := vouchedFor4(0)
			if err != nil {
				t.Fatal(err)
			}
			delay := hintElectionTimeouts * a.timing.ElectionTimeoutMax
			last, err := hear4(a, a.now, a.now.Add(delay+2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if last, err = c.stop(a, last); err != nil {
				t.Fatal(err)
			}
			end := last.Add(delay)
			for now := last.Add(a.timing.HeartbeatInterval); now.Before(end); now = now.Add(a.timing.HeartbeatInterval) {
				a.Tick(now)
				c.after(a, now)
				a.Ready()
				if !admits(a, 4) || !published(a, 4) {
					t.Fatalf("the record ended %v after the leader's last contact, before its delay %v", now.Sub(last), delay)
				}
			}
			a.Tick(end.Add(-time.Millisecond))
			if !admits(a, 4) {
				t.Fatalf("the record ended before its delay %v after the leader's last contact", delay)
			}
			a.Tick(end)
			a.Ready()
			if admits(a, 4) || published(a, 4) || a.hint.ID != 0 {
				t.Fatalf("the record outlived its delay %v after the leader's last contact", delay)
			}
		})
	}
}
