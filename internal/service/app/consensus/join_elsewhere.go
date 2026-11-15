package consensus

import (
	"strconv"
	"strings"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// ---- a JOIN a node cannot answer at once (#1422) ----

// joinApplyLag is how far, in entries, a voter's apply may trail its commit
// index, and its log the leader's last index as its last heartbeat carried
// it, for the voter still to forward a JOIN: at D16's 10 000 entries a
// second, about a tenth of a second before it applies the AddLearner it
// forwards (#1422).
const joinApplyLag uint64 = 1024

// joinNotLeader opens the reason of a JOIN refused NOT_LEADER that names
// the leader: its id, then " at " and its address when this node knows it
// (#1422). The JOIN_REFUSED frame carries a code and a reason alone, so the
// joiner reads the leader back from the reason (leaderOfJoinRefusal).
const joinNotLeader = "not the leader; leader "

// joinElsewhere reports whether this node cannot answer a JOIN promptly,
// with the NOT_LEADER reason that names the leader it knows (#1422). A node
// that does not lead forwards the AddLearner and answers once it applied
// that entry itself: a learner still waiting for its snapshot, which applies
// nothing, held the JOIN for its whole deadline and answered
// OUTCOME_UNKNOWN although the leader had committed the admission. Only
// the leader and a voter level with it (joinServes) answer; every other
// node refuses before it proposes anything. A stopped node is left to the
// path that answers its stop.
func (n *Node) joinElsewhere() (string, bool) {
	var lead coreconsensus.NodeID
	var addr string
	serves := true
	if !n.exec(func() {
		st := n.r.Status()
		if st.Role == coreconsensus.RoleLeader {
			return
		}
		lead = st.Lead
		if m, ok := st.Conf.Member(lead); ok {
			addr = m.Address
		}
		serves = n.joinServes(&st)
	}) || serves {
		return "", false
	}
	return notLeaderJoinReason(lead, addr), true
}

// joinServes reports whether a node that does not lead, whose step function
// reads st, answers a JOIN it forwards promptly: it follows a known leader
// as a voter that holds its state (joinVoter), and its apply and its log are
// level with the leader's (joinLevel) (#1422). A learner, a joiner, a node
// with no table, a candidate and a lagging voter do not.
func (n *Node) joinServes(st *raft.StatusValue) bool {
	return n.joinVoter(st) && n.joinLevel(st)
}

// joinVoter reports whether this node follows a leader other than itself as
// a voter of its configuration that may vote and holds a table: a learner,
// a joiner and a node with no table may wait for a snapshot.
func (n *Node) joinVoter(st *raft.StatusValue) bool {
	if st.Lead == 0 || st.Lead == n.stamp.ID || st.Role != coreconsensus.RoleFollower {
		// No leader to forward to, or a learner, or a campaign under way.
		return false
	}
	me, ok := st.Conf.Member(n.stamp.ID)
	return ok && me.Voter && !me.Joining && n.canVote && !n.r.NoTable()
}

// joinLevel reports whether this node's apply is not held back, and its
// applied index and its log are within joinApplyLag entries of its commit
// index and of the leader's last index as its last heartbeat carried it.
func (n *Node) joinLevel(st *raft.StatusValue) bool {
	if st.LeaderLast > st.Last+joinApplyLag || n.applyOverloaded() {
		// Still catching up with the leader's log, or apply held back.
		return false
	}
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	return applied+joinApplyLag >= st.Commit
}

// notLeaderJoinReason is the reason of a JOIN refused NOT_LEADER by a node
// that cannot answer it promptly: the leader's id and, when known and the
// reason's bound holds it, its address; with no leader known, it says so.
func notLeaderJoinReason(lead coreconsensus.NodeID, addr string) string {
	if lead == 0 {
		return "not the leader; no leader known"
	}
	why := joinNotLeader + lead.String()
	if at := why + " at " + addr; addr != "" && sanitizeReason(at) == at {
		// An address the reason's bound or its printable runes would cut
		// is not named: a joiner would dial what was left of it.
		why = at
	}
	return why
}

// leaderOfJoinRefusal reads the leader a JOIN refused NOT_LEADER names in
// its reason (notLeaderJoinReason): its id and, when named, its address.
// ok is false for any other refusal, and for a reason that names none.
func leaderOfJoinRefusal(err error) (lead coreconsensus.NodeID, addr string, ok bool) {
	if !errs.HasCode(err, coreconsensus.CodeNotLeader) {
		return 0, "", false
	}
	why, _ := JoinRefusalReason(err)
	return leaderOfJoinReason(why)
}

// leaderOfJoinReason parses notLeaderJoinReason's text.
func leaderOfJoinReason(why string) (lead coreconsensus.NodeID, addr string, ok bool) {
	rest, found := strings.CutPrefix(why, joinNotLeader)
	if !found {
		return 0, "", false
	}
	id, addr, _ := strings.Cut(rest, " at ")
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, "", false
	}
	return coreconsensus.NodeID(v), addr, true
}
