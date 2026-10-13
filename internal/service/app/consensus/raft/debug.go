package raft

import "fmt"

// Debug renders the step function's state for a failing test's log.
func (r *Raft) Debug() string {
	s := fmt.Sprintf("refusals=%d@%d snapTerm=%d role=%v term=%d lead=%d snap=%d last=%d commit=%d stable=%d applied=%d", r.refusals, r.refusedAt, r.log.snapTerm, r.role, r.term, r.lead,
		r.log.snapIndex, r.log.lastIndex(), r.log.committed, r.log.stable, r.log.applied)
	for id, pr := range r.prs {
		s += fmt.Sprintf(" pr[%d]={match=%d next=%d state=%d probeSent=%v pend=%d infl=%d}", id, pr.match, pr.next, pr.state, pr.probeSent, pr.pendingSnapshot, len(pr.inflight))
	}
	return s + fmt.Sprintf(" silent=%d/%d", r.cfg.SilentTerm, r.cfg.SilentIndex)
}
