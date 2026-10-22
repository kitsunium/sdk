package raft

import (
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// hintElectionTimeouts is how many maximum election timeouts a vouched-for
// leader stays admitted with no configuration of this node listing it.
const hintElectionTimeouts time.Duration = 4

// stepLeaderRecord runs the admission of a stale voter (ADR 0152 D4, #542)
// before anything else reads m, and reports whether m was consumed.
//
// A voter that was down while a member was admitted, promoted and handed
// the leadership comes back with a configuration that does not list it:
// its transport cut that leader's appends, the members it knows were
// followers and sent it nothing, and it stayed pre-candidate for as long as
// that leader led. A follower that hears its PreVote while it follows a
// live leader now answers with that leader's record, and the voter admits
// it for replication only.
//
// A MsgLeaderRecord is consumed here and moves no term. A message from a
// member admitted only by such a record is consumed, dropped, unless it is
// an append or a heartbeat: the record vouches for replication, never for a
// vote, a transfer, a read or a forward.
func (r *Raft) stepLeaderRecord(now time.Time, m core.MessageValue) bool {
	switch {
	case m.Kind == core.MsgLeaderRecord:
		r.takeLeaderRecord(now, m)
		return true
	case m.Kind == core.MsgPreVote:
		r.sendLeaderRecord(now, m)
		return false
	case r.hintedOnly(m.From):
		return m.Kind != core.MsgApp && m.Kind != core.MsgHeartbeat && !r.mutated(mutHintAnyKind)
	default:
		return false
	}
}

// sendLeaderRecord answers a PreVote from a member that campaigns behind
// this node's commit, while this node follows a live leader, with that
// leader's record read from the committed configuration. Only once the
// cluster version is 2: a node of protocol 1 does not know the kind.
func (r *Raft) sendLeaderRecord(now time.Time, m core.MessageValue) {
	if r.clusterVer < Protocol2 || r.role == core.RoleLeader || r.lead == 0 || r.lead == m.From ||
		r.lastLeaderContact.IsZero() || now.Sub(r.lastLeaderContact) >= r.voteWindow || m.Index >= r.log.committed {
		return
	}
	if _, ok := r.conf().Member(m.From); !ok {
		return
	}
	rec, ok := r.confAt(r.log.committed).Member(r.lead)
	if !ok {
		return
	}
	data := wire.AppendMembership(nil, &core.MembershipValue{Members: []core.MemberValue{rec}})
	r.send(core.MessageValue{
		Kind: core.MsgLeaderRecord, To: m.From, Index: r.log.committed,
		Entries: []core.EntryValue{{Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}},
	})
}

// takeLeaderRecord admits the leader a member vouched for, provisionally:
// only while this node knows no live leader, from a member of its own
// configuration, at a term at least its own, for a key that is pinned and
// an ID this node neither knows as a member nor retired. The transport
// still checks at the handshake that the key is the cluster CA's.
func (r *Raft) takeLeaderRecord(now time.Time, m core.MessageValue) {
	if !r.mayTakeRecord(now, m) {
		return
	}
	rec, ok := recordOf(&m)
	if !ok || rec.ID == r.id || r.knows(rec.ID) {
		return
	}
	r.hint, r.hintUntil = rec, now.Add(hintElectionTimeouts*r.timing.ElectionTimeoutMax)
	r.rd.LeaderHint = rec
	r.publishAdmitted()
}

// mayTakeRecord reports whether this node takes a record now: it knows no
// live leader and does not lead, and the record comes from a member of its
// configuration at a term at least its own.
func (r *Raft) mayTakeRecord(now time.Time, m core.MessageValue) bool {
	if r.role == core.RoleLeader || m.Term < r.term || r.hearsLeader(now) {
		return false
	}
	_, member := r.conf().Member(m.From)
	return member
}

// hearsLeader reports whether this node heard its leader within the
// minimum election timeout.
func (r *Raft) hearsLeader(now time.Time) bool {
	return r.lead != 0 && !r.lastLeaderContact.IsZero() && now.Sub(r.lastLeaderContact) < r.timing.ElectionTimeoutMin
}

// recordOf reads the one member record a MsgLeaderRecord carries, refused
// when the entry's body does not match its checksum, when it is not one
// record, or when the record has no ID or no pinned key.
func recordOf(m *core.MessageValue) (core.MemberValue, bool) {
	if len(m.Entries) != 1 || wire.BodySum(m.Entries[0].Data) != m.Entries[0].BodySum {
		return core.MemberValue{}, false
	}
	got, err := wire.ParseMembership(m.Entries[0].Data)
	if err != nil || len(got.Members) != 1 {
		return core.MemberValue{}, false
	}
	rec := got.Members[0]
	var unpinned [32]byte
	return rec, rec.ID != 0 && rec.SPKI != unpinned
}

// knows reports whether id is a member or a retired ID of the latest or the
// committed configuration: a record never replaces what this node's own
// log says of it.
func (r *Raft) knows(id core.NodeID) bool {
	for _, c := range []*core.MembershipValue{r.conf(), r.confAt(r.log.committed)} {
		if _, ok := c.Member(id); ok || slices.Contains(c.Retired, id) {
			return true
		}
	}
	return false
}

// hintedOnly reports whether id is admitted by a leader record alone.
func (r *Raft) hintedOnly(id core.NodeID) bool {
	return r.hint.ID != 0 && id == r.hint.ID && !r.knows(id)
}

// endHintOnConf ends the provisional admission once a configuration of
// this node lists the vouched-for leader, which admits it as any member, or
// retires it.
func (r *Raft) endHintOnConf() {
	if r.hint.ID != 0 && r.knows(r.hint.ID) {
		r.hint = core.MemberValue{}
	}
}

// endHintOnTimeout ends the provisional admission once its delay passed
// with no configuration of this node listing the leader.
func (r *Raft) endHintOnTimeout(now time.Time) {
	if r.hint.ID != 0 && !now.Before(r.hintUntil) {
		r.hint = core.MemberValue{}
		r.publishAdmitted()
	}
}
