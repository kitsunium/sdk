package raft

import (
	"bytes"
	"maps"
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The client sessions of ADR 0241 (#918). A session command carries its
// session, its sequence and the command; the table records, per session, the
// last sequence applied, its body sum and its position. The same fold runs
// in two places over the same committed entries: the step function's, at
// commit, answers a retry before anything is appended, and the driver's, at
// apply, decides what the state machine is handed. Only the second is a
// safety: a retry the first misses is appended and the apply skips it.

// sessionOpen is the sequence of the entry that opens a session: no command
// carries it.
const sessionOpen uint64 = 0

// SessionTable is the replicated record of the client sessions (ADR 0241): a
// pure fold of the committed session commands in log order, the same on
// every node, which the step function reads to answer a retry before it
// appends and the driver to apply a command at most once. It is not safe
// for concurrent use.
type SessionTable struct {
	byID map[core.SessionID]core.SessionValue
}

// NewSessionTable returns the session table a snapshot recorded, vs, empty
// for none.
func NewSessionTable(vs []core.SessionValue) *SessionTable {
	t := &SessionTable{byID: make(map[core.SessionID]core.SessionValue, len(vs))}
	for _, v := range vs {
		t.byID[v.ID] = v
	}
	return t
}

// Apply folds the committed session command e and says what it is:
// its own position and no code for a command to apply or a session
// opened, the recorded position for a copy of the command applied
// there, SESSION_DIGEST_MISMATCH or SESSION_EXPIRED for one never
// applied, VERSION_UNSUPPORTED for a payload that does not parse.
func (t *SessionTable) Apply(e *core.EntryValue) (core.PositionValue, errs.Code) {
	s, seq, _, err := wire.ParseSession(e.Data)
	if err != nil {
		return core.PositionValue{}, codeVersion
	}
	idx := e.Position.Index
	v, ok := t.byID[s]
	if seq == sessionOpen {
		if !ok {
			v = core.SessionValue{ID: s, BodySum: e.BodySum, Position: e.Position}
			t.evictFor()
		}
		v.Last = idx
		t.byID[s] = v
		return e.Position, 0
	}
	pos, code, known := recorded(v, ok, seq, e.BodySum)
	switch {
	case !known:
		v.Seq, v.BodySum, v.Position = seq, e.BodySum, e.Position
		pos = e.Position
	case code != 0:
		// A refused command changes nothing: it never touched the session.
		return pos, code
	}
	v.Last = idx
	t.byID[s] = v
	return pos, 0
}

// recorded is what the session record v, held when ok, says of command seq
// of body sum sum: known is false for a command it has not seen, which is
// applied; otherwise the position of the copy applied, or the refusal.
func recorded(v core.SessionValue, ok bool, seq uint64, sum uint32) (core.PositionValue, errs.Code, bool) {
	switch {
	case !ok || seq < v.Seq || seq == sessionOpen:
		return core.PositionValue{}, core.CodeSessionExpired, true
	case seq > v.Seq:
		return core.PositionValue{}, 0, false
	case sum != v.BodySum:
		return core.PositionValue{}, core.CodeSessionDigestMismatch, true
	default:
		return v.Position, 0, true
	}
}

// evictFor makes room for one more session: at MaxSessions, the session
// whose last entry is the oldest goes. Each index is the last of one session
// at most, so the choice is the same on every node whatever the map's order.
func (t *SessionTable) evictFor() {
	if len(t.byID) < MaxSessions {
		return
	}
	var oldest core.SessionID
	first := true
	var at uint64
	for id, v := range t.byID {
		if first || v.Last < at {
			oldest, at, first = id, v.Last, false
		}
	}
	delete(t.byID, oldest)
}

// Values returns the sessions recorded, ordered by ID, for a snapshot.
func (t *SessionTable) Values() []core.SessionValue {
	if len(t.byID) == 0 {
		return nil
	}
	out := slices.AppendSeq(make([]core.SessionValue, 0, len(t.byID)), maps.Values(t.byID))
	slices.SortFunc(out, func(a, b core.SessionValue) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	return out
}

// ProposeSession appends one session command at the leader, unless the
// session already holds its sequence: a copy of the same payload is
// answered with the position of the one appended or committed, and
// nothing is appended; another payload is SESSION_DIGEST_MISMATCH, an
// older sequence or a session not open SESSION_EXPIRED. It is refused
// PAYLOAD_VERSION_REFUSED below cluster version 4 (#918).
func (r *Raft) ProposeSession(now time.Time, e core.EntryValue) (core.PositionValue, errs.Code) {
	return r.proposeSession(now, e, r.id)
}

// proposeSession is ProposeSession for a command received from origin: this
// leader, or the member that forwarded it.
func (r *Raft) proposeSession(now time.Time, e core.EntryValue, origin core.NodeID) (core.PositionValue, errs.Code) {
	r.now = now
	if r.role != core.RoleLeader || r.transferTo != 0 {
		return core.PositionValue{}, codeNotLeader
	}
	if e.Kind != core.EntrySessionCommand {
		return core.PositionValue{}, codeVersion
	}
	if !r.atProtocol4() {
		return core.PositionValue{}, core.CodePayloadVersionRefused
	}
	s, seq, _, err := wire.ParseSession(e.Data)
	if err != nil {
		return core.PositionValue{}, codeVersion
	}
	if pos, code, known := r.sessionKnown(s, seq, e.BodySum); known && !r.cfg.sessionUnchecked {
		return pos, code
	}
	pos, code := r.propose(now, []core.EntryValue{e}, origin)
	if code != 0 {
		return core.PositionValue{}, code
	}
	return pos[0], 0
}

// sessionKnown is what this leader already knows of command seq of session
// s: the latest entry of s its log holds past the commit, or else the
// session as the committed entries left it. A leader never truncates its own
// log, so a copy appended in its term is the one that commits, or none does.
func (r *Raft) sessionKnown(s core.SessionID, seq uint64, sum uint32) (core.PositionValue, errs.Code, bool) {
	if pos, code, known, found := r.sessionPending(s, seq, sum); found {
		return pos, code, known
	}
	v, ok := r.sessions.byID[s]
	if seq == sessionOpen {
		if ok {
			return v.Position, 0, true
		}
		return core.PositionValue{}, 0, false
	}
	return recorded(v, ok, seq, sum)
}

// sessionPending is the first half of sessionKnown: the answer the latest
// entry of session s past the commit gives command seq, found false when the
// uncommitted tail holds none and the committed table decides instead.
func (r *Raft) sessionPending(s core.SessionID, seq uint64, sum uint32) (pos core.PositionValue, code errs.Code, known, found bool) {
	for i := r.log.lastIndex(); i > max(r.log.committed, r.log.snapIndex); i-- {
		e := r.log.at(i)
		if e.Kind != core.EntrySessionCommand {
			continue
		}
		es, eseq, _, err := wire.ParseSession(e.Data)
		if err != nil || es != s {
			continue
		}
		if eseq != sessionOpen {
			v := core.SessionValue{ID: s, Seq: eseq, BodySum: e.BodySum, Position: e.Position}
			pos, code, known = recorded(v, true, seq, sum)
			return pos, code, known, true
		}
		if seq == sessionOpen {
			return e.Position, 0, true, true
		}
		// Opened past the commit: a command of it is new.
		return core.PositionValue{}, 0, false, true
	}
	return core.PositionValue{}, 0, false, false
}

// atProtocol4 reports whether the committed cluster version admits the
// formats of protocol 4: the session command, and the metadata that
// records the sessions. Every member announced it before the raise
// committed (D18, #918).
func (r *Raft) atProtocol4() bool {
	return r.clusterVer >= Protocol4
}
