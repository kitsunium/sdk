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
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The facts a PeerHealthValue carries.
const (
	// PeerVoter is set for a voter, clear for a learner.
	PeerVoter PeerFlags = 1 << iota
	// PeerUnreachable is set when the transport failed to reach the peer
	// since it last answered, or it has not answered within the minimum
	// election timeout.
	PeerUnreachable
	// PeerSnapshot is set while a snapshot is being sent to the peer.
	PeerSnapshot
)

// The answers a HealthValue carries.
const (
	// HealthReady is the readiness answer: running, a leader known,
	// everything known committed applied, and not refusing the current
	// leader's chain (D4).
	HealthReady HealthFlags = 1 << iota
	// HealthHasLeader is set when a leader is known in the current term.
	HealthHasLeader
	// HealthQuorumReachable is set on a leader a majority answered within
	// the minimum election timeout; never elsewhere.
	HealthQuorumReachable
	// HealthStopped is set once the node stopped itself or was closed.
	HealthStopped
)

// maxSlotLen bounds a slot name, which the stamp stores behind a one-byte
// length.
const maxSlotLen int = 32

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

// drawIncarnation draws a random incarnation, which tells two lifetimes of
// one slot apart. crypto/rand.Read never returns an error on this toolchain
// (it aborts the process instead), so the discarded result hides nothing.
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
	st = coreconsensus.StampValue{
		Log: coreconsensus.LogIdentityValue{Cluster: cluster, Epoch: 1}, ID: drawID(),
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

// validSlot admits a slot name of 1 to maxSlotLen bytes of [a-z0-9-]: it
// is written in the stamp and in a certificate's URI SAN, so nothing a
// filesystem, a URL or a log line could misread gets in.
func validSlot(s string) bool {
	if len(s) == 0 || len(s) > maxSlotLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// foundingDigest is the digest of the founding member list, so that a rerun
// with another list is refused.
func foundingDigest(members []coreconsensus.MemberValue) [32]byte {
	return sha256.Sum256(wire.AppendMembership(nil, &coreconsensus.MembershipValue{Members: byID(members)}))
}

// byID is the founding list in ID order: the digest AND entry 1 are built
// from it, so founders given the same members in any order write the same
// entry (review M1).
func byID(members []coreconsensus.MemberValue) []coreconsensus.MemberValue {
	ms := slices.Clone(members)
	slices.SortFunc(ms, func(a, b coreconsensus.MemberValue) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return ms
}

// Bootstrap founds a cluster on one founding node's log, in phases that are
// each idempotent: a bootstrap-pending stamp naming the founding list, the
// founding configuration as entry 1, then the member stamp. Every founder
// runs it with the IDENTICAL list (D8, R2-24, R3-02, R9-04). It never runs
// inside NewNode.
func Bootstrap(ctx context.Context, cfg *Config, members []coreconsensus.MemberValue) error {
	// Every phase below is a durable write: a caller that gave up before the
	// first one gets its own cancellation back, and nothing is written.
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.Log == nil {
		return misconfigured("Log", "nil")
	}
	if err := checkFounders(members, cfg.MinVoters); err != nil {
		return err
	}
	stamp, err := founderStamp(cfg)
	if err != nil {
		return err
	}
	if err := checkSelfListed(members, &stamp); err != nil {
		return err
	}
	digest := foundingDigest(members)
	if done, err := advanceFounding(cfg.Log, &stamp, digest); done || err != nil {
		return err
	}
	if err := writeFoundingEntry(cfg, members); err != nil {
		return err
	}
	stamp.State = coreconsensus.StampMember
	if err := cfg.Log.WriteStamp(stamp); err != nil {
		return storageFailed("stamp", err)
	}
	return nil
}

// checkFounders refuses a founding list no cluster may start from: empty,
// with a zero or repeated id, with a member that carries no SPKI pin, with
// two members pinned to one key, or with fewer voters than MinVoters. It reads nothing from disk, so a bad
// list is refused before the stamp is touched.
func checkFounders(members []coreconsensus.MemberValue, minVoters int) error {
	if len(members) == 0 {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "no member"))
	}
	var noPin [32]byte
	seen := map[coreconsensus.NodeID]bool{}
	for _, m := range members {
		if m.ID == 0 || seen[m.ID] {
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a zero or repeated id"))
		}
		if m.SPKI == noPin {
			// An empty pin is refused: no member is admitted on its slot
			// alone (R9-04).
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a member without an SPKI pin"))
		}
		seen[m.ID] = true
	}
	if sharedKey(members) {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "two members under one key"))
	}
	if minVoters > countVoters(members) {
		return misconfigured("MinVoters", "above the founding voter count")
	}
	return nil
}

// countVoters is how many members of a list vote.
func countVoters(members []coreconsensus.MemberValue) int {
	voters := 0
	for _, m := range members {
		if m.Voter {
			voters++
		}
	}
	return voters
}

// founderStamp reads the stamp Bootstrap advances, refusing a log that was
// never initialised or that belongs to another cluster or node.
func founderStamp(cfg *Config) (coreconsensus.StampValue, error) {
	stamp, err := cfg.Log.Stamp()
	if err != nil {
		return stamp, storageFailed("stamp", err)
	}
	if stamp.State == 0 {
		return stamp, origin(coreconsensus.BootstrapRefused, errs.String("why", "unstamped: Init first"))
	}
	if stamp.Log.Cluster != cfg.Cluster || (cfg.ID != 0 && cfg.ID != stamp.ID) {
		return stamp, origin(coreconsensus.DataDirMismatch, errs.String("why", "cluster or node id"))
	}
	return stamp, nil
}

// checkSelfListed refuses a founding list that does not name this node with
// the incarnation and slot it was stamped with: every founder must run
// Bootstrap with the same list, and that list must include itself.
func checkSelfListed(members []coreconsensus.MemberValue, stamp *coreconsensus.StampValue) error {
	self := slices.IndexFunc(members, func(m coreconsensus.MemberValue) bool { return m.ID == stamp.ID })
	if self < 0 || members[self].Incarnation != stamp.Incarnation || members[self].Slot != stamp.Slot {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "this node is not in the list as stamped"))
	}
	return nil
}

// advanceFounding runs the stamp phase of Bootstrap. It reports done when
// the node is already a member founded with this list, refuses a list that
// differs from the one already recorded, and moves an empty joining log to
// bootstrap-pending with the list's digest. A stamp state it does not
// define is refused, never founded.
func advanceFounding(log coreconsensus.LogStore, stamp *coreconsensus.StampValue, digest [32]byte) (bool, error) {
	switch stamp.State {
	case coreconsensus.StampMember:
		if stamp.Founding == digest {
			return true, nil // already founded with this list
		}
		return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "already founded with another list"))
	case coreconsensus.StampBootstrapPending:
		if stamp.Founding != digest {
			return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "bootstrap pending with another list"))
		}
	case coreconsensus.StampJoining:
		if first, last := log.Bounds(); last >= first {
			return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "the log already holds entries"))
		}
		stamp.State, stamp.Founding = coreconsensus.StampBootstrapPending, digest
		if err := log.WriteStamp(*stamp); err != nil {
			return false, storageFailed("stamp", err)
		}
	default:
		// State 0 was refused by founderStamp; any other value is one this
		// version does not define, and a directory it cannot read the state
		// of is never founded (a LogStore other than the shipped one may
		// hand back any byte).
		return false, origin(coreconsensus.DataDirMismatch, errs.String("why", "unknown stamp state"))
	}
	return false, nil
}

// writeFoundingEntry writes the founding configuration as entry 1, at term
// 1 and committed, then syncs it; a log that already holds an entry is left
// alone, which is what makes a rerun idempotent.
func writeFoundingEntry(cfg *Config, members []coreconsensus.MemberValue) error {
	if first, last := cfg.Log.Bounds(); last >= first {
		return nil
	}
	conf := coreconsensus.MembershipValue{Members: byID(members)}
	for i := range conf.Members {
		conf.Members[i].Joining = false
	}
	timing := cfg.InitialTiming
	if timing.HeartbeatInterval == 0 {
		timing = TimingDefaults()
	}
	timing.Generation = 1
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "bootstrap", Change: wire.ChangeFounding, Membership: conf, Contract: timing})
	e := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: 1, Index: 1}, Kind: coreconsensus.EntryMembership,
		BodySum: wire.BodySum(data), Data: data,
	}
	e.ChainSum = wire.ChainSum(0, &e)
	hs := new(coreconsensus.HardState{Term: 1, Commit: 1})
	if err := cfg.Log.Append([]coreconsensus.EntryValue{e}, hs); err != nil {
		return storageFailed("append", err)
	}
	if err := cfg.Log.Sync(); err != nil {
		return storageFailed("sync", err)
	}
	return nil
}

// PeerHealthValue is the leader's view of one peer (D13).
type PeerHealthValue struct {
	// ID is the peer.
	ID coreconsensus.NodeID
	// Flags are the peer's yes/no facts: PeerVoter, PeerUnreachable,
	// PeerSnapshot.
	Flags PeerFlags
	// Match is the highest index the peer holds durably, as acknowledged.
	Match uint64
	// Lag is how far Match is behind the leader's last index.
	Lag uint64
	// Inflight counts the appends in flight to it.
	Inflight int
	// LastContact is how long ago it last answered; negative when it never
	// answered in this term.
	LastContact time.Duration
}

// PeerFlags are a peer's yes/no facts in a health verdict, one bit each,
// read with Has.
type PeerFlags uint8

// Has reports whether every fact of x is set.
func (f PeerFlags) Has(x PeerFlags) bool { return f&x == x }

// HealthFlags are a verdict's yes/no answers, one bit each, read with Has.
type HealthFlags uint8

// Has reports whether every answer of x is set.
func (f HealthFlags) Has(x HealthFlags) bool { return f&x == x }

// set is f with x on or off.
func (f HealthFlags) set(x HealthFlags, on bool) HealthFlags {
	if on {
		return f | x
	}
	return f &^ x
}

// HealthValue is a node's verdict for an orchestrator's probe (D13).
type HealthValue struct {
	// Status is the node's view after its last turn.
	Status coreconsensus.StatusValue
	// Flags are the verdict's yes/no answers: HealthReady, HealthHasLeader,
	// HealthQuorumReachable, HealthStopped.
	Flags HealthFlags
	// FailureTolerance is how many voters can fail now with a majority
	// left. Only the leader knows it: -1 elsewhere.
	FailureTolerance int
	// ApplyLagEntries is commit minus applied.
	ApplyLagEntries uint64
	// Peers is the leader's view of every peer, nil elsewhere.
	Peers []PeerHealthValue
	// ChainRefusals counts identical chain refusals of this follower at
	// ChainRefusalIndex from ChainRefusalLeader; the node reports itself
	// unready while it refuses (D4, R6-05).
	ChainRefusals      int
	ChainRefusalIndex  uint64
	ChainRefusalLeader coreconsensus.NodeID
	// StopReason says why the node stopped itself.
	StopReason coreconsensus.StopReasonValue
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
func (n *Node) Health() HealthValue {
	st := n.Status()
	h := HealthValue{
		Status: st, Flags: HealthFlags(0).set(HealthStopped, n.stopped.Load()), DroppedFrames: n.droppedFrom.Load(), FailureTolerance: -1,
		LongTurns: n.longTurns.Load(), JoinsRateLimited: n.joinRefused.Load(),
	}
	if r, ok := n.StopReason(); ok {
		h.StopReason = r
	}
	if st.Commit > st.Applied {
		h.ApplyLagEntries = st.Commit - st.Applied
	}
	stopped := h.Flags.Has(HealthStopped)
	h.Flags = h.Flags.set(HealthHasLeader, st.Leader != 0 && !stopped)
	if !stopped {
		n.exec(func() { n.loopHealth(&h) })
	}
	h.Flags = h.Flags.set(HealthReady, isReady(&h, &st))
	return h
}

// isReady is the verdict's Ready: running, a leader known, everything known
// committed applied, and not refusing the current leader's chain (D4).
func isReady(h *HealthValue, st *coreconsensus.StatusValue) bool {
	return !h.Flags.Has(HealthStopped) && h.Flags.Has(HealthHasLeader) && st.Applied >= st.Commit &&
		(h.ChainRefusals == 0 || h.ChainRefusalLeader != st.Leader)
}

// loopHealth fills the part of a verdict only the event loop can read: the
// quorum, the failure tolerance, the chain refusal and, on a leader, every
// peer's view. It runs on the loop, through exec.
func (n *Node) loopHealth(h *HealthValue) {
	now := n.clk.Now()
	h.Flags = h.Flags.set(HealthQuorumReachable, n.r.QuorumActive(now))
	h.FailureTolerance = n.r.FailureTolerance(now)
	h.ChainRefusalLeader, h.ChainRefusalIndex, h.ChainRefusals = n.r.ChainRefusal()
	last := n.r.Status().Last
	window := n.r.Status().Timing.ElectionTimeoutMin
	for _, p := range n.r.PeerStats() {
		h.Peers = append(h.Peers, n.peerHealth(&p, now, last, window))
	}
}

// peerHealth is one peer's line of a verdict. A peer is unreachable when it
// never answered this term, when its last answer is older than the minimum
// election timeout, or when the transport reported it unreachable since.
func (n *Node) peerHealth(p *raft.PeerStatValue, now time.Time, last uint64, window time.Duration) PeerHealthValue {
	ph := PeerHealthValue{ID: p.ID, Match: p.Match, Inflight: p.Inflight, LastContact: -1}
	if p.Flags.Has(raft.StatVoter) {
		ph.Flags |= PeerVoter
	}
	if p.Flags.Has(raft.StatSnapshot) {
		ph.Flags |= PeerSnapshot
	}
	if last > p.Match {
		ph.Lag = last - p.Match
	}
	heard := p.Flags.Has(raft.StatHeard)
	if heard {
		ph.LastContact = now.Sub(p.LastHeard)
	}
	if n.peerUnreachable(p, heard, ph.LastContact, window) {
		ph.Flags |= PeerUnreachable
	}
	return ph
}

// peerUnreachable reports whether a peer counts as unreachable: it never
// answered this term, its last answer is older than window, or the
// transport reported it unreachable since.
func (n *Node) peerUnreachable(p *raft.PeerStatValue, heard bool, lastContact, window time.Duration) bool {
	u, reported := n.unreachableAt[p.ID]
	return !heard || lastContact >= window || (reported && u.After(p.LastHeard))
}

// sharedKey reports whether two members are pinned to one key. The
// transport admits a connection by its key: two members under one key
// could speak as each other (review M5).
func sharedKey(members []coreconsensus.MemberValue) bool {
	var noPin [32]byte
	seen := make(map[[32]byte]bool, len(members))
	for _, m := range members {
		if m.SPKI == noPin {
			continue
		}
		if seen[m.SPKI] {
			return true
		}
		seen[m.SPKI] = true
	}
	return false
}
