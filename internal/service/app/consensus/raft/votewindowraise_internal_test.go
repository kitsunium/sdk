package raft

import (
	"fmt"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// silentFor keeps leader r in its term for d while member 2 answers every
// heartbeat announcing protocol v and member 3 says nothing, as a voter
// that went down after it announced v once in the term.
func silentFor(r *Raft, d time.Duration, v uint32) {
	start := r.now
	for r.now.Sub(start) < d {
		r.now = r.now.Add(r.timing.HeartbeatInterval)
		r.Tick(r.now)
		r.Step(r.now, announcing(r, 2, v))
		r.Ready()
	}
}

// checkRaiseHeardRecently: the cluster version is raised only while every
// member of the configuration answers within the vote window, its
// announcement with it. A voter that announced the version once in the
// term and then went silent kept its announcement for the rest of the term:
// rolled back to the previous release while down, it met the raise only as
// a snapshot it could not read once the leader compacted past its log, and
// left replication without a word (#665).
func checkRaiseHeardRecently(mut mutation) error {
	for _, v := range []uint32{Protocol2, Protocol3} {
		r := ackedLeader(mut)
		r.Step(r.now, announcing(r, 2, v))
		r.Step(r.now, announcing(r, 3, v))
		r.Ready()
		silentFor(r, r.voteWindow, v)
		if r.role != core.RoleLeader {
			return fmt.Errorf("setup: the leader stepped down")
		}
		if code := proposeRaise(r, v); code == 0 {
			return fmt.Errorf("raised to %d while member 3 was silent for %v, the vote window %v", v, r.now.Sub(r.prs[3].lastHeard), r.voteWindow)
		}
		r.Step(r.now, announcing(r, 3, v))
		if code := proposeRaise(r, v); code != 0 {
			return fmt.Errorf("setup: the raise to %d was refused once member 3 answered again: %v", v, code)
		}
	}
	return nil
}

// The issue's precondition, turned around: a voter that announced protocol
// 3 once in the term and then stayed silent for ten minutes no longer
// carries the raise to 3, which waits for it to answer again (#665).
func TestTheRaiseTo3WaitsForAVoterSilentForTenMinutes(t *testing.T) {
	r := ackedLeader(0)
	r.Step(r.now, announcing(r, 2, Protocol3))
	r.Step(r.now, announcing(r, 3, Protocol3))
	r.Ready()
	silentFor(r, 10*time.Minute, Protocol3)
	if r.role != core.RoleLeader {
		t.Fatalf("setup: the leader stepped down")
	}
	silent := r.now.Sub(r.prs[3].lastHeard)
	if code := proposeRaise(r, Protocol3); code != codeVersion {
		t.Fatalf("voter 3 silent for %v: the raise to 3 answered %v, want %v", silent, code, codeVersion)
	}
	r.Step(r.now, announcing(r, 3, Protocol3))
	if code := proposeRaise(r, Protocol3); code != 0 {
		t.Fatalf("voter 3 answered again: the raise to 3 answered %v", code)
	}
}

func TestTheRaiseWaitsForEveryMemberToAnswerWithinTheVoteWindow(t *testing.T) {
	if err := checkRaiseHeardRecently(0); err != nil {
		t.Fatal(err)
	}
	if err := checkRaiseHeardRecently(mutRaiseOverSilence); err == nil {
		t.Fatal("the mutation was not caught")
	}
}
