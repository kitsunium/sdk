package consensus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"slices"

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
	st = coreconsensus.Stamp{Log: coreconsensus.LogIdentity{Cluster: cluster, Epoch: 1}, ID: drawID(),
		Incarnation: drawIncarnation(), Slot: slot, State: coreconsensus.StampJoining}
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
		e := coreconsensus.Entry{Position: coreconsensus.Position{Term: 1, Index: 1}, Kind: coreconsensus.EntryMembership,
			BodySum: wire.BodySum(data), Data: data}
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

// Health is a node's readiness, for the product's probes.
type Health struct {
	// Status is the node's view after its last turn.
	Status coreconsensus.Status
	// Ready is true while a leader is known and the node applied what it
	// knows committed.
	Ready bool
	// Stopped is true once the node stopped, closed or not.
	Stopped bool
	// StopReason says why the node stopped itself.
	StopReason coreconsensus.StopReason
	// DroppedFrames counts frames whose From differed from the
	// authenticated peer (R7-05).
	DroppedFrames int64
}

// Health returns the node's readiness.
func (n *Node) Health() Health {
	st := n.Status()
	h := Health{Status: st, Stopped: n.stopped.Load(), DroppedFrames: n.droppedFrom.Load()}
	if r, ok := n.StopReason(); ok {
		h.StopReason = r
	}
	h.Ready = !h.Stopped && st.Leader != 0 && st.Applied >= st.Commit
	return h
}
