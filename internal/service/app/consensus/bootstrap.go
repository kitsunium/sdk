package consensus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// drawID draws a random NodeID, never 0.
func drawID() coreconsensus.NodeID {
	for {
		var b [8]byte
		_, _ = rand.Read(b[:])
		if id := coreconsensus.NodeID(binary.LittleEndian.Uint64(b[:])); id != 0 {
			return id
		}
	}
}

func drawIncarnation() coreconsensus.Incarnation {
	var inc coreconsensus.Incarnation
	_, _ = rand.Read(inc[:])
	return inc
}

// InitLog prepares a joining log purely offline: a random NodeID, a joining
// stamp, and the pair returned. It is idempotent: a log already joining for
// that cluster and slot returns its pair (D8, R3-02, R8-10).
func InitLog(log coreconsensus.LogStore, cluster coreconsensus.ClusterID, slot string) (coreconsensus.NodeID, coreconsensus.Incarnation, error) {
	st, err := log.Stamp()
	if err != nil {
		return 0, coreconsensus.Incarnation{}, storageFailed("stamp", err)
	}
	if st.State != 0 {
		if st.Log.Cluster == cluster && st.Slot == slot {
			return st.ID, st.Incarnation, nil
		}
		return 0, coreconsensus.Incarnation{}, origin(coreconsensus.DataDirMismatch, errs.String("why", "stamped for another cluster or slot"))
	}
	if !validSlot(slot) {
		return 0, coreconsensus.Incarnation{}, misconfigured("slot", "not [a-z0-9-]{1,32}")
	}
	st = coreconsensus.Stamp{
		Log: coreconsensus.LogIdentity{Cluster: cluster, Epoch: 1}, ID: drawID(),
		Incarnation: drawIncarnation(), Slot: slot, State: coreconsensus.StampJoining,
	}
	if err := log.WriteStamp(st); err != nil {
		return 0, coreconsensus.Incarnation{}, storageFailed("stamp", err)
	}
	return st.ID, st.Incarnation, nil
}

// RedrawLog draws a new NodeID, only while the stamp is joining and the log
// is empty (R8-10).
func RedrawLog(log coreconsensus.LogStore) (coreconsensus.NodeID, error) {
	st, err := log.Stamp()
	if err != nil {
		return 0, storageFailed("stamp", err)
	}
	if first, last := log.Bounds(); st.State != coreconsensus.StampJoining || last >= first {
		return 0, origin(coreconsensus.DataDirMismatch, errs.String("why", "not an empty joining directory"))
	}
	st.ID = drawID()
	if err := log.WriteStamp(st); err != nil {
		return 0, storageFailed("stamp", err)
	}
	return st.ID, nil
}

func validSlot(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// foundingDigest is the digest of the founding member list, so that a rerun
// with another list is refused.
func foundingDigest(members []coreconsensus.Member) [32]byte {
	ms := slices.Clone(members)
	slices.SortFunc(ms, func(a, b coreconsensus.Member) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return sha256.Sum256(wire.AppendMembership(nil, &coreconsensus.Membership{Members: ms}))
}

// Bootstrap founds a cluster on one founding node's log, in phases that are
// each idempotent: a bootstrap-pending stamp naming the founding list, the
// founding configuration as entry 1, then the member stamp. Every founder
// runs it with the IDENTICAL list (D8, R2-24, R3-02, R9-04). It never runs
// inside NewRaft.
func Bootstrap(_ context.Context, cfg Config, members []coreconsensus.Member) error {
	if cfg.Log == nil {
		return misconfigured("Log", "nil")
	}
	if len(members) == 0 {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "no member"))
	}
	seen := map[coreconsensus.NodeID]bool{}
	for _, m := range members {
		if m.ID == 0 || seen[m.ID] {
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a zero or repeated id"))
		}
		if m.SPKI == ([32]byte{}) {
			// An empty pin is refused: no member is admitted on its slot
			// alone (R9-04).
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a member without an SPKI pin"))
		}
		seen[m.ID] = true
	}
	voters := 0
	for _, m := range members {
		if m.Voter {
			voters++
		}
	}
	if cfg.MinVoters > voters {
		return misconfigured("MinVoters", "above the founding voter count")
	}
	st, err := cfg.Log.Stamp()
	if err != nil {
		return storageFailed("stamp", err)
	}
	if st.State == 0 {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "unstamped: Init first"))
	}
	if st.Log.Cluster != cfg.Cluster || (cfg.ID != 0 && cfg.ID != st.ID) {
		return origin(coreconsensus.DataDirMismatch, errs.String("why", "cluster or node id"))
	}
	self := slices.IndexFunc(members, func(m coreconsensus.Member) bool { return m.ID == st.ID })
	if self < 0 || members[self].Incarnation != st.Incarnation || members[self].Slot != st.Slot {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "this node is not in the list as stamped"))
	}
	digest := foundingDigest(members)
	switch st.State {
	case coreconsensus.StampMember:
		if st.Founding == digest {
			return nil // already founded with this list
		}
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "already founded with another list"))
	case coreconsensus.StampBootstrapPending:
		if st.Founding != digest {
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "bootstrap pending with another list"))
		}
	case coreconsensus.StampJoining:
		if first, last := cfg.Log.Bounds(); last >= first {
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "the log already holds entries"))
		}
		st.State, st.Founding = coreconsensus.StampBootstrapPending, digest
		if err := cfg.Log.WriteStamp(st); err != nil {
			return storageFailed("stamp", err)
		}
	}
	if first, last := cfg.Log.Bounds(); last < first {
		conf := coreconsensus.Membership{Members: slices.Clone(members)}
		for i := range conf.Members {
			conf.Members[i].Joining = false
		}
		timing := cfg.InitialTiming
		if timing.HeartbeatInterval == 0 {
			timing = TimingDefaults()
		}
		timing.Generation = 1
		data := wire.AppendAdmin(nil, &wire.Admin{Operator: "bootstrap", Change: wire.ChangeFounding, Membership: conf, Contract: timing})
		e := coreconsensus.Entry{
			Position: coreconsensus.Position{Term: 1, Index: 1}, Kind: coreconsensus.EntryMembership,
			BodySum: wire.BodySum(data), Data: data,
		}
		e.ChainSum = wire.ChainSum(0, &e)
		hs := coreconsensus.HardState{Term: 1, Commit: 1}
		if err := cfg.Log.Append([]coreconsensus.Entry{e}, &hs); err != nil {
			return storageFailed("append", err)
		}
		if err := cfg.Log.Sync(); err != nil {
			return storageFailed("sync", err)
		}
	}
	st.State = coreconsensus.StampMember
	if err := cfg.Log.WriteStamp(st); err != nil {
		return storageFailed("stamp", err)
	}
	return nil
}

// PeerHealth is the leader's view of one peer (D13).
type PeerHealth struct {
	// ID is the peer.
	ID coreconsensus.NodeID
	// Voter is false for a learner.
	Voter bool
	// Match is the highest index the peer holds durably, as acknowledged.
	Match uint64
	// Lag is how far Match is behind the leader's last index.
	Lag uint64
	// Inflight counts the appends in flight to it.
	Inflight int
	// LastContact is how long ago it last answered; negative when it never
	// answered in this term.
	LastContact time.Duration
	// Unreachable is set when the transport failed to reach it since it
	// last answered, or it has not answered within the minimum election
	// timeout.
	Unreachable bool
	// Snapshot is set while a snapshot is being sent to it.
	Snapshot bool
}

// Health is a node's verdict for an orchestrator's probe (D13).
type Health struct {
	// Status is the node's view after its last turn.
	Status coreconsensus.Status
	// Ready is true while a leader is known and the node applied what it
	// knows committed.
	Ready bool
	// HasLeader is true while a leader is known in the current term.
	HasLeader bool
	// QuorumReachable is true on a leader a majority answered within the
	// minimum election timeout; false elsewhere.
	QuorumReachable bool
	// FailureTolerance is how many voters can fail now with a majority
	// left. Only the leader knows it: -1 elsewhere.
	FailureTolerance int
	// ApplyLagEntries is commit minus applied.
	ApplyLagEntries uint64
	// Peers is the leader's view of every peer, nil elsewhere.
	Peers []PeerHealth
	// ChainRefusals counts identical chain refusals of this follower at
	// ChainRefusalIndex from ChainRefusalLeader; the node reports itself
	// unready while it refuses (D4, R6-05).
	ChainRefusals      int
	ChainRefusalIndex  uint64
	ChainRefusalLeader coreconsensus.NodeID
	// Stopped is true once the node stopped, closed or not.
	Stopped bool
	// StopReason says why the node stopped itself.
	StopReason coreconsensus.StopReason
	// DroppedFrames counts frames whose From differed from the
	// authenticated peer (R7-05).
	DroppedFrames int64
	// LongTurns counts the loop turns longer than the drift budget, each of
	// which dropped the lease (D9, R7-20).
	LongTurns int64
	// JoinsRateLimited counts JOINs refused by the one-per-slot rate.
	JoinsRateLimited int64
}

// Health returns the node's verdict, computed on the event loop.
func (n *Node) Health() Health {
	st := n.Status()
	h := Health{
		Status: st, Stopped: n.stopped.Load(), DroppedFrames: n.droppedFrom.Load(), FailureTolerance: -1,
		LongTurns: n.longTurns.Load(), JoinsRateLimited: n.joinRefused.Load(),
	}
	if r, ok := n.StopReason(); ok {
		h.StopReason = r
	}
	if st.Commit > st.Applied {
		h.ApplyLagEntries = st.Commit - st.Applied
	}
	h.HasLeader = st.Leader != 0 && !h.Stopped
	if !h.Stopped {
		n.exec(func() {
			now := n.clk.Now()
			h.QuorumReachable = n.r.QuorumActive(now)
			h.FailureTolerance = n.r.FailureTolerance(now)
			h.ChainRefusalLeader, h.ChainRefusalIndex, h.ChainRefusals = n.r.ChainRefusal()
			last := n.r.Status().Last
			window := n.r.Status().Timing.ElectionTimeoutMin
			for _, p := range n.r.PeerStats() {
				ph := PeerHealth{ID: p.ID, Voter: p.Voter, Match: p.Match, Inflight: p.Inflight, Snapshot: p.Snapshot, LastContact: -1}
				if last > p.Match {
					ph.Lag = last - p.Match
				}
				if p.Heard {
					ph.LastContact = now.Sub(p.LastHeard)
				}
				u, reported := n.unreachableAt[p.ID]
				ph.Unreachable = !p.Heard || ph.LastContact >= window || (reported && u.After(p.LastHeard))
				h.Peers = append(h.Peers, ph)
			}
		})
	}
	h.Ready = !h.Stopped && h.HasLeader && st.Applied >= st.Commit &&
		(h.ChainRefusals == 0 || h.ChainRefusalLeader != st.Leader)
	return h
}
