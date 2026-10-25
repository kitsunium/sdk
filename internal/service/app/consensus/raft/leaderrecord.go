package raft

import (
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// hintElectionTimeouts is how many maximum election timeouts a vouched-for
// leader stays admitted after its record or its last contact, with no
// configuration of this node listing it.
const hintElectionTimeouts time.Duration = 4

// askElectionTimeouts is how many maximum election timeouts a learner goes
// without hearing a leader before it asks its members for the record of the
// one they follow, and then between two questions (#639).
const askElectionTimeouts time.Duration = 2

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
// this node's commit (behindCommit), while this node follows a live leader,
// with that leader's record read from the committed configuration.
//
// Only to a PreVote that itself announces protocol 2, from a node that
// speaks it: a node of protocol 1 does not know the kind, and the two
// speak the highest version both announce (D18). The announcement is read
// on the PreVote answered, never on an older state of its sender. It used
// to wait for the cluster version 2, whose raise is refused while a member
// is not heard announcing protocol 2: the stale voter that needs the record
// is one the leader cannot hear, so it blocked the very raise that would
// send it (#638).
func (r *Raft) sendLeaderRecord(now time.Time, m core.MessageValue) {
	if !r.mayRecordTo(m) || r.role == core.RoleLeader || r.lead == 0 || r.lead == m.From ||
		r.lastLeaderContact.IsZero() || now.Sub(r.lastLeaderContact) >= r.voteWindow || !r.behindCommit(m) {
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

// behindCommit reports whether the log a PreVote m announces, by its last
// term and last index, cannot hold the entry at this node's commit: its
// sender lacks a committed entry, so it wins no election while a majority
// holds that entry, and the record only lets it hear the leader. It is
// behind when its last index is below the commit, as #542 judged it, or
// when its last term is older than the committed entry's: terms never
// decrease along a log, so a log holding that entry ends at its term or a
// later one, however far its index reaches (#675). Judged on the index
// alone, a voter back with an uncommitted tail of an older term, longer
// than the commit, or a learner's question over one (#639), was never
// answered, and on a quiet cluster stayed cut off from the leader for as
// long as it led: one vote lost, and one more failure stopped every commit.
//
// A log whose last term is the committed entry's, at or past its index,
// holds that entry, since the leader of that term only appended; one whose
// last term is later, at or past it, may hold it. Neither is sent a
// record, as before: the record is for a member behind the commit, never
// for one level with it or ahead.
func (r *Raft) behindCommit(m core.MessageValue) bool {
	if m.Index < r.log.committed {
		return true
	}
	if r.mutated(mutRecordByIndexOnly) {
		return false
	}
	t, ok := r.log.term(r.log.committed)
	return ok && m.LogTerm < t
}

// mayRecordTo reports whether the PreVote m may be answered with a leader
// record: both this node and m speak protocol 2.
func (r *Raft) mayRecordTo(m core.MessageValue) bool {
	switch {
	case r.mutated(mutRecordAtClusterVersion):
		return r.clusterVer >= Protocol2
	case r.mutated(mutRecordUnannounced):
		return true
	default:
		return r.speaks() >= Protocol2 && m.Flags&core.FlagProtocol2 != 0
	}
}

// askForLeader asks every other member of this node's configuration for
// the record of the leader it follows, when this node is a learner that
// heard no leader for askElectionTimeouts maximum election timeouts, at most
// once in that time (#639). The record answers a PreVote (#542), and a
// learner never campaigns: one down while a member was admitted and made
// leader came back with a configuration that does not list that leader, its
// transport refused the leader's key, and nothing vouched for it, so it was
// never fed while that leader led and kept every raise of the cluster
// version refused (#638).
//
// The question is a PreVote, the kind a follower answers with the record,
// asked and never campaigned: the learner does not become a candidate,
// counts no answer and moves no term of its own, and a PreVote binds no
// receiver, which raises no term on it and records nothing (R2-03). A
// follower of a live leader drops it, a leader that hears a majority does
// too, and a grant reaches a learner, which drops it. The Ready's
// LeaderUnheard tells the driver, which sends a joining node's JOIN again
// (#632).
func (r *Raft) askForLeader(now time.Time) {
	wait := askElectionTimeouts * r.timing.ElectionTimeoutMax
	if r.role != core.RoleLearner || r.speaks() < Protocol2 || r.mutated(mutLearnerNeverAsks) ||
		now.Sub(r.lastLeaderContact) < wait || (!r.askedAt.IsZero() && now.Sub(r.askedAt) < wait) {
		return
	}
	r.askedAt = now
	r.rd.LeaderUnheard = true
	if r.mutated(mutAskCampaigns) {
		r.campaign(true, 0, 0)
		return
	}
	for _, m := range r.conf().Members {
		if m.ID != r.id {
			r.send(core.MessageValue{
				Kind: core.MsgPreVote, To: m.ID, Term: r.term + 1,
				LogTerm: r.log.lastTerm(), Index: r.log.lastIndex(),
			})
		}
	}
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

// extendHint pushes the end of the provisional admission to its delay past
// now, on an append or a heartbeat of the vouched-for leader that this node
// took as contact at its own term (#618). The delay bounds a leader that is
// no longer heard. Counted from the record alone, it ended the admission
// while the leader's heartbeats still arrived: the transport cut every
// connection of the leader's key, the snapshot stream that would list it
// among them, and a node that hears its leader sends no PreVote that could
// bring a new record, so a voter whose install streamed longer than the
// delay was cut at every window and never caught up under writes. A record
// that already ended is never revived.
func (r *Raft) extendHint(now time.Time, from core.NodeID) {
	if r.hint.ID != 0 && from == r.hint.ID && !r.mutated(mutHintEndsWhileHeard) {
		r.hintUntil = now.Add(hintElectionTimeouts * r.timing.ElectionTimeoutMax)
	}
}

// endHintOnTimeout ends the provisional admission once its delay passed
// after the record or the leader's last contact, with no configuration of
// this node listing the leader.
func (r *Raft) endHintOnTimeout(now time.Time) {
	if r.hint.ID != 0 && !now.Before(r.hintUntil) {
		r.hint = core.MemberValue{}
		r.publishAdmitted()
	}
}
