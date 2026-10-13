package raft

import (
	"math/rand/v2"
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Config is what the step function needs from its driver. Every bound is
// already clamped and validated by the engine.
type Config struct {
	// ID is this node.
	ID core.NodeID
	// Group is the consensus group.
	Group core.GroupID
	// Log is the identity of the log, checked against an admin precondition.
	Log core.LogIdentity
	// Seed draws the randomized election timeouts.
	Seed uint64
	// Timing is the committed timing contract at start.
	Timing core.TimingContract
	// MaxInflight bounds the appends in flight per peer.
	MaxInflight int
	// MaxInflightBytes bounds their bytes.
	MaxInflightBytes int
	// MaxBatchBytes bounds one append.
	MaxBatchBytes int
	// MinVoters is the floor a Remove may not cross.
	MinVoters int
	// PromoteLag is how far behind the commit a learner may be promoted.
	PromoteLag uint64
	// Lease turns on lease reads.
	Lease bool
	// CanVote is false while the directory is joining or silent: such a
	// node neither votes nor acknowledges.
	CanVote bool
	// MaxChainRefusals is N of D4's "no silent loop".
	MaxChainRefusals int
	// NoTable is set while the node holds no usable snapshot: it votes and
	// stores the log but never campaigns, and asks for a snapshot (R7-18).
	NoTable bool
	// RecentEntriesBytes bounds the payloads held in memory; 0 holds them
	// all. Older ones are fetched from the store when a peer needs them.
	RecentEntriesBytes int64
	// mut is set by the package's own tests only.
	mut mutation
}

// Start is the durable state the driver loaded.
type Start struct {
	// HardState is the last hard state on disk.
	HardState core.HardState
	// Snapshot is the base the log continues, zero for none.
	Snapshot core.SnapshotMeta
	// Entries are the durable entries after the snapshot.
	Entries []core.Entry
	// Applied is where the state machine resumes, at least the snapshot.
	Applied uint64
	// Now is the start instant: the restart vote-refusal window opens here.
	Now time.Time
}

// ReadState answers a read: once applied reaches Index, the read is
// linearizable. A non-zero Code refuses it.
type ReadState struct {
	Context uint64
	Index   uint64
	Code    errs.Code
}

// TransferResult ends a leadership transfer this node started.
type TransferResult struct {
	To   core.NodeID
	Code errs.Code
}

// Ready is what the driver must do after a turn.
type Ready struct {
	// Seq names the write when there is one; completions carry it.
	Seq uint64
	// HardState is set when the term or the vote changed: it is durable
	// before anything in AfterSync leaves.
	HardState *core.HardState
	// Commit is the new commit index, written lazily, 0 when unchanged.
	Commit uint64
	// TruncateFrom removes every entry from that index on, before Entries.
	TruncateFrom uint64
	// Entries are appended after the truncation.
	Entries []core.Entry
	// Messages leave at once.
	Messages []core.Message
	// AfterSync leave once this write is durable.
	AfterSync []core.Message
	// Apply are committed, locally durable entries, in order.
	Apply []core.Entry
	// SendSnapshot names the peers that need a snapshot.
	SendSnapshot []core.NodeID
	// ReadStates answer reads.
	ReadStates []ReadState
	// Forwarded are answers to this node's forwarded proposals.
	Forwarded []core.Message
	// Transfers end transfers.
	Transfers []TransferResult
	// Compact drops the log's prefix through that index, which a durable
	// snapshot covers.
	Compact uint64
	// Fetch asks the driver to read entries the step function no longer
	// holds, off the event loop, and hand them back with Fetched.
	Fetch []FetchRequest
	// Membership is set when the admitted set changed.
	Membership []core.Member
}

// FetchRequest asks for the entries [Lo, Hi) of the store for a peer.
type FetchRequest struct {
	To     core.NodeID
	Lo, Hi uint64
}

// NeedsWrite reports whether the Ready asks the storage actor for anything.
func (rd *Ready) NeedsWrite() bool {
	return rd.HardState != nil || rd.TruncateFrom != 0 || rd.Compact != 0 || len(rd.Entries) != 0 || len(rd.AfterSync) != 0 || rd.Commit != 0
}

type confAt struct {
	index uint64
	m     core.Membership
}

type readReq struct {
	ctx   uint64
	index uint64
	from  core.NodeID // 0 for local
	round uint64
}

type round struct {
	id     uint64
	sentAt time.Time
	acks   map[core.NodeID]uint64 // the contract generation each voter applied
	conf   uint64
}

// Raft is the step function. It is not safe for concurrent use: one event
// loop owns it.
type Raft struct {
	cfg Config
	rng *rand.Rand
	mut mutation

	id   core.NodeID
	term uint64
	vote core.NodeID
	lead core.NodeID
	role core.Role

	log memLog

	base  core.Membership
	confs []confAt

	prs   map[core.NodeID]*progress
	votes map[core.NodeID]bool

	electionDeadline  time.Time
	heartbeatDue      time.Time
	lastLeaderContact time.Time
	now               time.Time

	// selfVoteSeq is the write that carries this candidate's own vote; its
	// vote counts once that write is durable (D5b).
	selfVoteSeq     uint64
	selfVoteDurable bool

	// termStart is the index of this leader's no-op.
	termStart uint64

	transferTo       core.NodeID
	transferSent     bool
	transferDeadline time.Time
	leaseDropped     bool
	leaseStart       time.Time
	// leaseLen is the lease the last confirmed round grants: computed
	// from the minimum timing contract over the acknowledging majority,
	// never from the leader's newer view (D9 d).
	leaseLen  time.Duration
	contracts map[uint64]core.TimingContract
	// campaignTransfer is the flag the current campaign carries.
	campaignIssuer core.NodeID
	campaignTerm   uint64

	nextRound    uint64
	rounds       []*round
	pendingReads []readReq
	waitingReads []readReq

	timing      core.TimingContract
	voteWindow  time.Duration
	clusterVer  uint32
	clusterGen  uint64
	noSpace     core.AlarmID
	refusedLead core.NodeID
	refusedAt   uint64
	refusedFrom core.NodeID
	refusals    int

	seq uint64
	rd  Ready
	hs  core.HardState
}

// New builds the step function over the loaded state.
func New(cfg Config, st Start) *Raft {
	r := &Raft{
		cfg:    cfg,
		mut:    cfg.mut,
		rng:    rand.New(rand.NewPCG(cfg.Seed, uint64(cfg.ID))),
		id:     cfg.ID,
		term:   st.HardState.Term,
		vote:   st.HardState.Vote,
		prs:    map[core.NodeID]*progress{},
		votes:  map[core.NodeID]bool{},
		timing: cfg.Timing,
		now:    st.Now,
	}
	sm := st.Snapshot
	r.log = memLog{snapIndex: sm.Position.Index, snapTerm: sm.Position.Term, snapChain: sm.ChainTip}
	r.base = sm.Membership.Clone()
	r.base.Index = sm.Position.Index
	if sm.Timing.Generation > r.timing.Generation {
		r.timing = sm.Timing
	}
	r.clusterVer, r.clusterGen, r.noSpace = sm.ClusterVersion, sm.ClusterGeneration, sm.NoSpace
	r.voteWindow = r.timing.ElectionTimeoutMin
	r.contracts = map[uint64]core.TimingContract{r.timing.Generation: r.timing}
	for _, e := range st.Entries {
		if e.Position.Index <= r.log.snapIndex {
			continue
		}
		r.log.ents = append(r.log.ents, e)
		r.log.window += int64(len(e.Data))
		r.noteAppended(&e)
	}
	r.log.stable = r.log.lastIndex()
	r.log.committed = min(max(st.HardState.Commit, r.log.snapIndex), r.log.lastIndex())
	r.log.applied = max(st.Applied, r.log.snapIndex)
	r.log.applied = min(r.log.applied, r.log.committed)
	r.applyCommitEffects(r.log.snapIndex+1, r.log.committed)
	r.hs = core.HardState{Term: r.term, Vote: r.vote, Commit: r.log.committed}
	r.role = core.RoleFollower
	if !r.isVoter(r.id) {
		r.role = core.RoleLearner
	}
	// A node that starts behaves as if it had just heard from a leader: it
	// refuses votes for one window (D5, from raft-sql-poc).
	r.lastLeaderContact = st.Now
	if r.mutated(mutNoRestartRefusal) {
		r.lastLeaderContact = time.Time{}
	}
	r.resetElection(st.Now)
	r.rd.Membership = r.admitted()
	return r
}

// ---- inputs ----

// Tick advances the timers to now.
func (r *Raft) Tick(now time.Time) {
	r.now = now
	if r.role == core.RoleLeader {
		r.tickLeader(now)
		return
	}
	if !now.Before(r.electionDeadline) {
		r.resetElection(now)
		if r.promotable() {
			r.campaign(true, 0, 0)
		}
	}
}

// StorageDone reports that the write seq is durable through (index, term).
func (r *Raft) StorageDone(seq, index, term uint64) {
	if r.selfVoteSeq != 0 && seq >= r.selfVoteSeq {
		r.selfVoteSeq = 0
		if r.role == core.RoleCandidate {
			r.selfVoteDurable = true
			r.votes[r.id] = true
			r.tallyVotes()
		}
	}
	if index == 0 || index <= r.log.stable {
		return
	}
	if !r.mutated(mutStaleCompletion) && !r.log.matchTerm(index, term) {
		return
	}
	r.log.stable = min(index, r.log.lastIndex())
	if r.role == core.RoleLeader {
		if pr := r.prs[r.id]; pr != nil {
			pr.maybeUpdate(r.log.stable)
		}
		if r.maybeCommit() {
			r.broadcastAppend(true)
		}
	}
}

// Applied reports how far the state machine applied.
func (r *Raft) Applied(index uint64) {
	_ = index // the driver keeps applied; the step function hands out through log.applied
}

// ReportUnreachable moves a peer the transport could not reach to probe.
func (r *Raft) ReportUnreachable(id core.NodeID) {
	if pr := r.prs[id]; r.role == core.RoleLeader && pr != nil && pr.state == stateReplicate {
		pr.becomeProbe()
	}
}

// SnapshotDone ends a snapshot transfer to a peer.
func (r *Raft) SnapshotDone(to core.NodeID, ok bool) {
	pr := r.prs[to]
	if r.role != core.RoleLeader || pr == nil || pr.state != stateSnapshot {
		return
	}
	if !ok {
		pr.pendingSnapshot = 0
	}
	pr.becomeProbe()
	pr.probeSent = true
}

// Compact forgets the entries through index, which a durable snapshot
// covers.
func (r *Raft) Compact(index uint64) {
	if index <= r.log.snapIndex || index > r.log.applied {
		return
	}
	t, _ := r.log.term(index)
	c := r.log.chain(index)
	m := r.confAt(index)
	r.log.compactTo(index, t, c)
	r.rd.Compact = max(r.rd.Compact, index)
	r.base = m.Clone()
	r.base.Index = index
	n := 0
	for n < len(r.confs) && r.confs[n].index <= index {
		n++
	}
	r.confs = append(r.confs[:0], r.confs[n:]...)
}

// NoTable reports whether the node still waits for a snapshot install.
func (r *Raft) NoTable() bool { return r.cfg.NoTable }

// SetCanVote changes whether the node may vote: a joining directory may once
// its admission is appended.
func (r *Raft) SetCanVote(v bool) { r.cfg.CanVote = v }

// SnapshotIndex returns the base of the in-memory log.
func (r *Raft) SnapshotIndex() uint64 { return r.log.snapIndex }

// Entry returns the entry at i, if the step function holds it.
func (r *Raft) Entry(i uint64) (core.Entry, bool) {
	if i <= r.log.snapIndex || i > r.log.lastIndex() || i <= r.log.evicted {
		return core.Entry{}, false
	}
	return *r.log.at(i), true
}

// ConfAt returns the membership valid at index i.
func (r *Raft) ConfAt(i uint64) core.Membership { return r.confAt(i).Clone() }

// ChainAt returns the chain value at i.
func (r *Raft) ChainAt(i uint64) uint32 { return r.log.chain(i) }

// TermAt returns the term at i.
func (r *Raft) TermAt(i uint64) (uint64, bool) { return r.log.term(i) }

// ---- status ----

// Status is the step function's part of a node's status.
type Status struct {
	Role      core.Role
	Term      uint64
	Lead      core.NodeID
	Commit    uint64
	Last      uint64
	Stable    uint64
	Timing    core.TimingContract
	Cluster   uint32
	ClusterG  uint64
	NoSpace   core.AlarmID
	Conf      core.Membership
	Committed core.Membership
	Transfer  core.NodeID
}

// Status returns the step function's view.
func (r *Raft) Status() Status {
	return Status{
		Role: r.role, Term: r.term, Lead: r.lead, Commit: r.log.committed,
		Last: r.log.lastIndex(), Stable: r.log.stable, Timing: r.timing,
		Cluster: r.clusterVer, ClusterG: r.clusterGen, NoSpace: r.noSpace,
		Conf: r.conf().Clone(), Committed: r.confAt(r.log.committed).Clone(),
		Transfer: r.transferTo,
	}
}

// Progress returns the leader's match index of a peer.
func (r *Raft) Progress(id core.NodeID) (match uint64, lastHeard time.Time, ok bool) {
	pr := r.prs[id]
	if r.role != core.RoleLeader || pr == nil {
		return 0, time.Time{}, false
	}
	return pr.match, pr.lastHeard, true
}

// LeaseValid reports whether the leader's lease holds at now (D9).
func (r *Raft) LeaseValid(now time.Time) bool {
	if !r.cfg.Lease || r.role != core.RoleLeader || r.leaseDropped || r.leaseStart.IsZero() {
		return false
	}
	if r.log.committed < r.termStart {
		return false
	}
	return now.Sub(r.leaseStart) < r.leaseLen
}

// DropLease drops the lease for the rest of the term: a loop turn longer than
// the drift budget (D9).
func (r *Raft) DropLease() { r.leaseDropped = true }

// HasReady reports whether a turn left work for the driver.
func (r *Raft) HasReady() bool {
	r.flushReads()
	rd := &r.rd
	if r.term != r.hs.Term || r.vote != r.hs.Vote || r.log.committed != r.hs.Commit {
		return true
	}
	if r.log.applied < min(r.log.committed, r.log.stable) && !r.cfg.NoTable {
		return true
	}
	return rd.TruncateFrom != 0 || rd.Compact != 0 || len(rd.Entries) != 0 || len(rd.Messages) != 0 || len(rd.AfterSync) != 0 ||
		len(rd.SendSnapshot) != 0 || len(rd.ReadStates) != 0 || len(rd.Forwarded) != 0 ||
		len(rd.Transfers) != 0 || rd.Membership != nil || len(rd.Fetch) != 0
}

// Ready returns the work of the turns since the last call and resets it.
func (r *Raft) Ready() Ready {
	r.flushReads()
	rd := r.rd
	r.rd = Ready{}
	hsChanged := r.term != r.hs.Term || r.vote != r.hs.Vote
	if hsChanged {
		hs := core.HardState{Term: r.term, Vote: r.vote, Commit: r.log.committed}
		rd.HardState = &hs
		// A request at a new term, or a reply that a new vote decided,
		// waits for the term and the vote to be durable (D5b).
		rd.AfterSync = append(rd.AfterSync, rd.Messages...)
		rd.Messages = nil
	}
	if r.log.committed != r.hs.Commit {
		rd.Commit = r.log.committed
	}
	r.hs = core.HardState{Term: r.term, Vote: r.vote, Commit: r.log.committed}
	// A node with no table applies nothing until an install (D7).
	if hi := min(r.log.committed, r.log.stable); r.log.applied < hi && !r.cfg.NoTable {
		rd.Apply = slices.Clone(r.log.ents[r.log.applied-r.log.snapIndex : hi-r.log.snapIndex])
		r.log.applied = hi
	}
	r.log.evict(r.cfg.RecentEntriesBytes, min(r.log.applied, r.log.stable))
	if rd.NeedsWrite() {
		r.seq++
		rd.Seq = r.seq
		if hsChanged && r.role == core.RoleCandidate && !r.selfVoteDurable {
			r.selfVoteSeq = rd.Seq
		}
	}
	return rd
}

// ---- helpers ----

func (r *Raft) conf() *core.Membership {
	if n := len(r.confs); n > 0 {
		return &r.confs[n-1].m
	}
	return &r.base
}

func (r *Raft) confAt(i uint64) *core.Membership {
	for k := len(r.confs) - 1; k >= 0; k-- {
		if r.confs[k].index <= i {
			return &r.confs[k].m
		}
	}
	return &r.base
}

func (r *Raft) isVoter(id core.NodeID) bool {
	m, ok := r.conf().Member(id)
	return ok && m.Voter
}

func (r *Raft) voters() []core.NodeID { return r.conf().Voters() }

func (r *Raft) quorum() int { return len(r.voters())/2 + 1 }

// promotable reports whether this node may campaign: a voter of the current
// configuration, whose directory may vote.
func (r *Raft) promotable() bool {
	m, ok := r.conf().Member(r.id)
	return ok && m.Voter && !m.Joining && r.cfg.CanVote && !r.cfg.NoTable
}

func (r *Raft) randomTimeout() time.Duration {
	lo, hi := r.timing.ElectionTimeoutMin, r.timing.ElectionTimeoutMax
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(r.rng.Int64N(int64(hi-lo)))
}

func (r *Raft) resetElection(now time.Time) { r.electionDeadline = now.Add(r.randomTimeout()) }

// admitted is the transport's admission set: the union of the latest
// appended configuration and the latest committed one (D4).
func (r *Raft) admitted() []core.Member {
	out := slices.Clone(r.conf().Members)
	for _, m := range r.confAt(r.log.committed).Members {
		if !slices.ContainsFunc(out, func(x core.Member) bool { return x.ID == m.ID }) {
			out = append(out, m)
		}
	}
	return out
}

func (r *Raft) send(m core.Message) {
	m.Version = 1
	m.Group = r.cfg.Group
	m.From = r.id
	if m.Term == 0 && m.Kind != core.MsgPreVote && m.Kind != core.MsgPreVoteResp {
		m.Term = r.term
	}
	if (m.Kind == core.MsgAppResp || m.Kind == core.MsgVoteResp) && !r.mutated(mutAckBeforeSync) {
		r.rd.AfterSync = append(r.rd.AfterSync, m)
		return
	}
	r.rd.Messages = append(r.rd.Messages, m)
}

// noteAppended records what an appended entry changes at append time: a
// membership takes effect when appended (D8), and every contract present
// widens the vote-refusal window (R3-07).
func (r *Raft) noteAppended(e *core.Entry) {
	switch e.Kind {
	case core.EntryMembership:
		a, err := wire.ParseAdmin(e.Data)
		if err != nil {
			return
		}
		m := a.Membership
		m.Index = e.Position.Index
		r.confs = append(r.confs, confAt{index: e.Position.Index, m: m})
		r.confChanged()
	case core.EntryTimingContract:
		if a, err := wire.ParseAdmin(e.Data); err == nil {
			c := a.Contract
			c.Generation = a.Pre.Generation + 1
			r.contracts[c.Generation] = c
			if c.ElectionTimeoutMin > r.voteWindow {
				r.voteWindow = c.ElectionTimeoutMin
			}
		}
	}
}

func (r *Raft) confChanged() {
	r.rd.Membership = r.admitted()
	if r.role == core.RoleLeader {
		conf := r.conf()
		for _, m := range conf.Members {
			if r.prs[m.ID] == nil {
				pr := &progress{next: r.log.lastIndex() + 1}
				pr.lastHeard = r.now
				r.prs[m.ID] = pr
			}
		}
		for id := range r.prs {
			if _, ok := conf.Member(id); !ok {
				delete(r.prs, id)
			}
		}
		// A configuration change restarts every read round (R2-15).
		for _, rd := range r.rounds {
			rd.conf = 0
		}
	} else if r.role == core.RoleLearner || r.role == core.RoleFollower {
		if r.isVoter(r.id) {
			r.role = core.RoleFollower
		} else {
			r.role = core.RoleLearner
		}
	}
}

// truncateFrom removes entries from i on and rolls the configuration back.
func (r *Raft) truncateFrom(i uint64) {
	r.log.truncateFrom(i)
	if r.rd.TruncateFrom == 0 || i < r.rd.TruncateFrom {
		// Pending unwritten entries at or past i are superseded.
		r.rd.TruncateFrom = i
	}
	n := len(r.rd.Entries)
	for n > 0 && r.rd.Entries[n-1].Position.Index >= i {
		n--
	}
	r.rd.Entries = r.rd.Entries[:n]
	k := len(r.confs)
	for k > 0 && r.confs[k-1].index >= i {
		k--
	}
	if k != len(r.confs) {
		r.confs = r.confs[:k]
		r.confChanged()
	}
}

func (r *Raft) appendEntries(es []core.Entry) {
	for i := range es {
		r.log.ents = append(r.log.ents, es[i])
		r.log.window += int64(len(es[i].Data))
		r.rd.Entries = append(r.rd.Entries, es[i])
		r.noteAppended(&r.log.ents[len(r.log.ents)-1])
	}
}

// applyCommitEffects applies the engine's own replicated state of the
// entries newly committed in [lo, hi].
func (r *Raft) applyCommitEffects(lo, hi uint64) {
	for i := max(lo, r.log.snapIndex+1); i <= hi && i <= r.log.lastIndex(); i++ {
		e := r.log.at(i)
		switch e.Kind {
		case core.EntryTimingContract, core.EntryClusterVersion, core.EntryNoSpaceRaise, core.EntryNoSpaceClear:
		default:
			continue
		}
		a, err := wire.ParseAdmin(e.Data)
		if err != nil {
			continue
		}
		switch e.Kind {
		case core.EntryTimingContract:
			if a.Pre.Generation == r.timing.Generation {
				c := a.Contract
				c.Generation = r.timing.Generation + 1
				r.timing = c
				r.contracts[c.Generation] = c
				if c.ElectionTimeoutMin > r.voteWindow {
					r.voteWindow = c.ElectionTimeoutMin
				}
			}
		case core.EntryClusterVersion:
			if a.Pre.Generation == r.clusterGen && a.ClusterVersion > r.clusterVer {
				r.clusterVer, r.clusterGen = a.ClusterVersion, r.clusterGen+1
			}
		case core.EntryNoSpaceRaise:
			if r.noSpace == 0 {
				r.noSpace = core.AlarmID(i)
			}
		case core.EntryNoSpaceClear:
			if a.Pre.Alarm == r.noSpace {
				r.noSpace = 0
			}
		}
	}
}

func (r *Raft) commitTo(i uint64) {
	if i > r.log.committed {
		old := r.log.committed
		r.log.committed = i
		r.applyCommitEffects(old+1, i)
		r.rd.Membership = r.admitted()
	}
}

// PeerStat is the leader's view of one peer, for Health (D13).
type PeerStat struct {
	ID        core.NodeID
	Voter     bool
	Match     uint64
	Next      uint64
	Inflight  int
	LastHeard time.Time
	Heard     bool
	Probing   bool
	Snapshot  bool
}

// PeerStats returns the leader's view of every peer, in configuration
// order; nil off the leader.
func (r *Raft) PeerStats() []PeerStat {
	if r.role != core.RoleLeader {
		return nil
	}
	var out []PeerStat
	for _, id := range r.peers() {
		pr := r.prs[id]
		out = append(out, PeerStat{ID: id, Voter: r.isVoter(id), Match: pr.match, Next: pr.next, Inflight: len(pr.inflight),
			LastHeard: pr.lastHeard, Heard: pr.heard, Probing: pr.state == stateProbe, Snapshot: pr.state == stateSnapshot})
	}
	return out
}

// FailureTolerance is how many voters can fail now with a majority left:
// the voters heard within the minimum election timeout, less a quorum. Only
// the leader knows it; elsewhere it is -1.
func (r *Raft) FailureTolerance(now time.Time) int {
	if r.role != core.RoleLeader {
		return -1
	}
	n := 0
	for _, id := range r.voters() {
		if id == r.id {
			n++
			continue
		}
		if pr := r.prs[id]; pr != nil && pr.heard && now.Sub(pr.lastHeard) < r.timing.ElectionTimeoutMin {
			n++
		}
	}
	return max(n-r.quorum(), -1)
}

// QuorumActive reports whether a majority answered the leader within the
// minimum election timeout.
func (r *Raft) QuorumActive(now time.Time) bool {
	return r.role == core.RoleLeader && r.quorumActive(now, r.timing.ElectionTimeoutMin)
}

// ChainRefusal names the leader and the index this follower refuses, and
// how many times, while it lasts (D4, R6-05).
func (r *Raft) ChainRefusal() (core.NodeID, uint64, int) {
	return r.refusedFrom, r.refusedAt, r.refusals
}
