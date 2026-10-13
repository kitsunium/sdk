package consensus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Node is one member of a consensus group: the event loop that owns the step
// function, its storage actor, its apply goroutine and its snapshot
// goroutines (D5b). A Node never closes what it was handed.
type Node struct {
	cfg   Config
	clk   clock.Timed
	r     *raft.Raft
	ident coreconsensus.LogIdentity
	stamp coreconsensus.Stamp

	inCtrl chan coreconsensus.Message
	inData chan coreconsensus.Message
	propc  chan *proposal
	execc  chan func()
	storec chan storeDone
	fetchq *fifo[raft.FetchRequest]
	fetchc chan fetchDone
	stopc  chan struct{}
	hard   atomic.Bool

	sq *fifo[storeReq]
	aq *fifo[applyItem]

	status atomic.Pointer[coreconsensus.Status]

	am          sync.Mutex
	acond       *sync.Cond
	applied     uint64
	appliedTerm uint64
	waiters     map[uint64]*proposal
	results     map[uint64]applyResult
	resultOrder []uint64
	keepResults int

	pendingBytes atomic.Int64

	// loop-owned
	forwards  map[uint64]*proposal
	reads     map[uint64]*readWait
	transfers []chan errs.Code
	subEpoch  uint64
	subCount  uint32
	sending   map[coreconsensus.NodeID]bool
	// unreachableAt is when the transport last failed to reach a peer.
	unreachableAt map[coreconsensus.NodeID]time.Time
	lastTerm      uint64
	lastLead      coreconsensus.NodeID
	lastRole      coreconsensus.Role
	readSeq       uint64
	canVote       bool
	noTable       bool
	snapState     snapState

	watchMu  sync.Mutex
	watchers map[chan coreconsensus.Status]struct{}

	stopMu      sync.Mutex
	stopReason  *coreconsensus.StopReason
	stopped     atomic.Bool
	closeOnce   sync.Once
	releaseOnce sync.Once
	wg          sync.WaitGroup
	loopDone    chan struct{}

	droppedFrom atomic.Int64
	longTurns   atomic.Int64
	joinMu      sync.Mutex
	lastJoin    map[string]time.Time
	joinRefused atomic.Int64
	// trustFrom is set by the package's own tests only: the engine then
	// trusts a frame's From over the transport's peer (R7-05 mutation).
	trustFrom bool
}

type applyResult struct {
	term uint64
	val  any
}

type proposal struct {
	entry coreconsensus.Entry
	admin bool
	stage atomic.Int32 // 0 unsent, 1 possibly accepted, 2 appended at pos
	pos   coreconsensus.Position
	local bool
	done  chan propResult
	// changedAt is when leadership changed while it was unresolved; only
	// the loop and, for waiters, holders of am touch it.
	changedAt time.Time
}

const (
	stageUnsent int32 = iota
	stagePossible
	stageAppended
)

type propResult struct {
	tok coreconsensus.Token
	val any
	err error
}

type readWait struct {
	done chan readResult
}

type readResult struct {
	index uint64
	code  errs.Code
}

type storeReq struct {
	rd raft.Ready
}

type storeDone struct {
	seq, index, term uint64
	after            [][]byte
	to               []coreconsensus.NodeID
}

func newSubEpoch() uint64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return uint64(binary.LittleEndian.Uint32(b[:])) << 32
}

// NewRaft opens a node over what an offline tool founded or initialised. It
// never bootstraps (D8). The start sequence is fixed (D14): the stamp, the
// latest snapshot whose checksums pass, the log to its end, then serve.
func NewRaft(cfg Config) (*Node, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if a, ok := cfg.Log.(dirHolder); ok {
		if err := a.acquire(); err != nil {
			return nil, err
		}
		n, err := newRaft(cfg)
		if err != nil {
			a.release()
		}
		return n, err
	}
	return newRaft(cfg)
}

// dirHolder is the shipped log's hold on its data directory: a DataDir
// refuses offline work and Close while a Node holds it (D11, R3-18).
type dirHolder interface {
	acquire() error
	release()
}

func newRaft(cfg Config) (*Node, error) {
	if mc, ok := cfg.Log.(interface{ checkMarkers() error }); ok {
		if err := mc.checkMarkers(); err != nil {
			return nil, err
		}
	}
	st, err := cfg.Log.Stamp()
	if err != nil {
		return nil, storageFailed("stamp", err)
	}
	switch {
	case st.State == 0:
		return nil, origin(coreconsensus.DataDirMismatch, errs.String("why", "unstamped: Init or Bootstrap first"))
	case st.State == coreconsensus.StampBootstrapPending:
		return nil, origin(coreconsensus.BootstrapRefused, errs.String("why", "bootstrap pending: rerun Bootstrap"))
	case st.Log.Cluster != cfg.Cluster:
		return nil, origin(coreconsensus.DataDirMismatch, errs.String("why", "cluster"))
	case cfg.ID != 0 && st.ID != cfg.ID:
		return nil, origin(coreconsensus.DataDirMismatch, errs.String("why", "node id"))
	}
	if t, ok := cfg.Transport.(interface {
		Cluster() coreconsensus.ClusterID
	}); ok && t.Cluster() != cfg.Cluster {
		// The transport took its cluster from its certificate's SAN (R3-24).
		return nil, misconfigured("Cluster", "differs from the certificate's URI SAN")
	}
	cfg.ID = st.ID
	n := &Node{
		cfg: cfg, clk: cfg.Clock, ident: st.Log, stamp: st,
		inCtrl: make(chan coreconsensus.Message, 4096), inData: make(chan coreconsensus.Message, 4096),
		propc: make(chan *proposal, 4096), execc: make(chan func(), 1024), storec: make(chan storeDone, 1024),
		stopc: make(chan struct{}), sq: newFifo[storeReq](), fetchq: newFifo[raft.FetchRequest](), fetchc: make(chan fetchDone, 64), aq: newFifo[applyItem](),
		waiters: map[uint64]*proposal{}, results: map[uint64]applyResult{},
		forwards: map[uint64]*proposal{}, reads: map[uint64]*readWait{}, sending: map[coreconsensus.NodeID]bool{}, unreachableAt: map[coreconsensus.NodeID]time.Time{},
		watchers: map[chan coreconsensus.Status]struct{}{}, subEpoch: newSubEpoch(), loopDone: make(chan struct{}),
	}
	n.acond = sync.NewCond(&n.am)
	start, err := n.load()
	if err != nil {
		return nil, err
	}
	n.canVote = st.State == coreconsensus.StampMember
	seed := cfg.Seed
	if seed == 0 {
		seed = newSubEpoch() | uint64(st.ID)
	}
	n.r = raft.New(raft.Config{
		ID: st.ID, Group: cfg.Group, Log: st.Log, Seed: seed, Timing: cfg.InitialTiming,
		MaxInflight: cfg.MaxInflight, MaxInflightBytes: cfg.MaxInflightBytes, MaxBatchBytes: cfg.MaxBatchBytes,
		MinVoters: cfg.MinVoters, PromoteLag: cfg.PromoteLag, Lease: cfg.Reads == ReadLease,
		CanVote: n.canVote, MaxChainRefusals: 3, NoTable: n.noTable, RecentEntriesBytes: cfg.RecentEntriesBytes,
	}, start)
	n.publish()
	if ur, ok := cfg.Transport.(coreconsensus.UnreachableReporter); ok {
		ur.Unreachable(func(id coreconsensus.NodeID) {
			select {
			case n.execc <- func() {
				n.unreachableAt[id] = n.clk.Now()
				n.r.ReportUnreachable(id)
			}:
			default:
			}
		})
	}
	if err := cfg.Transport.Bind(cfg.Group, n.deliver, n.offer, n.install, n.join); err != nil {
		return nil, err
	}
	n.wg.Add(4)
	go n.reader()
	go n.storageActor()
	go n.applyLoop()
	go n.loop()
	return n, nil
}

// load restores the latest snapshot whose checksums pass and reads the log
// after it.
func (n *Node) load() (raft.Start, error) {
	var base coreconsensus.SnapshotMeta
	metas, err := n.cfg.Snapshots.List()
	if err != nil {
		return raft.Start{}, storageFailed("snapshots", err)
	}
	var durIdx uint64
	if d, ok := n.cfg.Machine.(coreconsensus.DurableStateMachine); ok {
		li, pos, derr := d.AppliedPosition()
		if derr != nil {
			return raft.Start{}, derr
		}
		if li.Same(n.ident) || (pos.Index <= n.ident.KeptIndex && li.Cluster == n.ident.Predecessor) {
			durIdx = pos.Index
		}
	}
	for _, m := range metas {
		if err := n.restoreFrom(m, durIdx); err != nil {
			continue // fall back to the older one (D7)
		}
		base = m
		break
	}
	n.snapState.last = base.Position.Index
	hs, err := n.cfg.Log.HardState()
	if err != nil {
		return raft.Start{}, origin(coreconsensus.HardStateCorrupt, errs.String("cause", err.Error()))
	}
	first, last := n.cfg.Log.Bounds()
	if base.Position.Index == 0 && (len(metas) > 0 || first > 1) {
		// Every snapshot fails its checksums, or none is left where the
		// log needs one: the node starts with NO table. It keeps its term,
		// vote and commit index, votes and keeps its log, never campaigns,
		// applies nothing and asks the leader for an install (D7, R6-13,
		// R7-18).
		n.noTable = true
		base = noTableBase(n.cfg.Log, metas, first)
	}
	s := base.Position.Index
	if first > s+1 {
		return raft.Start{}, origin(coreconsensus.LogCorrupt, errs.String("why", "gap after the snapshot"))
	}
	if s >= first && s <= last {
		if t, terr := n.cfg.Log.Term(s); terr != nil || t != base.Position.Term {
			// The suffix is kept only when the boundary entry matches (D7).
			if err := n.cfg.Log.TruncateSuffix(s + 1); err != nil {
				return raft.Start{}, storageFailed("truncate", err)
			}
			last = s
		}
	}
	var ents []coreconsensus.Entry
	if last > s && last >= first {
		ents, err = n.cfg.Log.Entries(max(first, s+1), last+1, int(^uint(0)>>1), nil)
		if err != nil {
			return raft.Start{}, err
		}
		for i := range ents {
			ents[i].Data = bytes.Clone(ents[i].Data)
		}
	}
	applied := max(s, durIdx)
	n.applied = applied
	if applied == s {
		n.appliedTerm = base.Position.Term
	}
	if n.noTable && durIdx < s {
		n.applied, n.appliedTerm = 0, 0 // nothing is applied until an install
	}
	return raft.Start{HardState: hs, Snapshot: base, Entries: ents, Applied: applied, Now: n.clk.Now()}, nil
}

// noTableBase is the base a node with no usable snapshot starts from: the
// log's compaction point, with the configuration and chain tip the newest
// snapshot's metadata records, which stays readable when its bytes do not.
func noTableBase(log coreconsensus.LogStore, metas []coreconsensus.SnapshotMeta, first uint64) coreconsensus.SnapshotMeta {
	var b coreconsensus.SnapshotMeta
	if len(metas) > 0 {
		b = metas[0]
		for _, m := range metas {
			if m.Position.Index == first-1 {
				b = m
			}
		}
	}
	b.Position.Index = first - 1
	b.Position.Term, _ = log.Term(first - 1)
	return b
}

// ---- transport callbacks ----

func (n *Node) deliver(from coreconsensus.AuthenticatedPeer, frame []byte) {
	if n.stopped.Load() {
		return
	}
	var m coreconsensus.Message
	if err := wire.ParseMessage(bytes.Clone(frame), &m); err != nil {
		return
	}
	if m.From != from.ID && !n.trustFrom {
		// The engine trusts the transport's authenticated peer, never the
		// frame's From (R7-05).
		n.droppedFrom.Add(1)
		return
	}
	q := n.inData
	if m.Kind.Lane() == coreconsensus.LaneControl {
		q = n.inCtrl
	}
	select {
	case q <- m:
	default:
	}
}

// exec runs f on the event loop and waits for it, or for the node to stop.
func (n *Node) exec(f func()) bool {
	done := make(chan struct{})
	select {
	case n.execc <- func() { f(); close(done) }:
	case <-n.stopc:
		return false
	}
	select {
	case <-done:
		return true
	case <-n.loopDone:
		return false
	}
}

// ---- the event loop ----

func (n *Node) loop() {
	defer n.wg.Done()
	defer close(n.loopDone)
	tick := n.clk.NewTicker(n.tickInterval())
	defer tick.Stop()
	var props []*proposal
	for {
		var turn time.Time
		select {
		case <-n.stopc:
			n.failAllOnStop()
			return
		case <-tick.C():
			turn = n.clk.Now()
			n.r.Tick(n.clk.Now())
			n.sweep()
		case m := <-n.inCtrl:
			turn = n.clk.Now()
			n.r.Step(n.clk.Now(), m)
		case m := <-n.inData:
			turn = n.clk.Now()
			n.r.Step(n.clk.Now(), m)
		case d := <-n.storec:
			turn = n.clk.Now()
			n.r.StorageDone(d.seq, d.index, d.term)
			for i, f := range d.after {
				n.cfg.Transport.Send(d.to[i], f)
			}
		case p := <-n.propc:
			turn = n.clk.Now()
			props = append(props[:0], p)
		drain:
			for len(props) < 1024 {
				select {
				case p := <-n.propc:
					props = append(props, p)
				default:
					break drain
				}
			}
			n.handleProposals(props)
		case f := <-n.execc:
			turn = n.clk.Now()
			f()
		case f := <-n.fetchc:
			turn = n.clk.Now()
			n.r.Fetched(f.req.To, f.req.Lo, f.ents, f.compacted)
		}
		// Control first: a flood of entries never starves a vote (D4).
	ctrl:
		for k := 0; k < 256; k++ {
			select {
			case m := <-n.inCtrl:
				n.r.Step(n.clk.Now(), m)
			default:
				break ctrl
			}
		}
		n.handleReady()
		n.checkTurn(turn)
	}
}

// checkTurn drops the lease for the rest of the term after a turn longer
// than the drift budget, ElectionTimeoutMin × MaxClockDrift: a pause of
// the process is exactly that (D9, R7-20, R8-37).
func (n *Node) checkTurn(turn time.Time) {
	if n.cfg.Reads != ReadLease {
		return
	}
	t := n.r.Status().Timing
	budget := time.Duration(float64(t.ElectionTimeoutMin) * t.MaxClockDrift)
	if d := n.clk.Since(turn); budget > 0 && d > budget {
		n.longTurns.Add(1)
		n.r.DropLease()
	}
}

// sweep answers OUTCOME_UNKNOWN to proposals whose leader changed more than
// two maximum election timeouts ago and that are still not resolved: the
// answer was lost, the entry may still commit.
func (n *Node) sweep() {
	grace := 2 * n.r.Status().Timing.ElectionTimeoutMax
	now := n.clk.Now()
	for ctx, p := range n.forwards {
		if !p.changedAt.IsZero() && now.Sub(p.changedAt) > grace {
			n.dropForward(ctx)
			p.done <- propResult{err: outcomeUnknown("leader changed before the answer")}
		}
	}
	commit := n.r.Status().Commit
	n.am.Lock()
	for idx, p := range n.waiters {
		if idx > commit && !p.changedAt.IsZero() && now.Sub(p.changedAt) > grace {
			delete(n.waiters, idx)
			if p.local {
				n.pendingBytes.Add(-int64(len(p.entry.Data)))
			}
			p.done <- propResult{err: outcomeUnknown("leadership changed before commit")}
		}
	}
	n.am.Unlock()
}

func (n *Node) dropForward(ctx uint64) {
	delete(n.forwards, ctx)
	n.am.Lock()
	n.keepResults--
	n.am.Unlock()
}

func (n *Node) tickInterval() time.Duration {
	return max(n.cfg.InitialTiming.HeartbeatInterval/5, time.Millisecond)
}

func (n *Node) handleReady() {
	for n.r.HasReady() {
		rd := n.r.Ready()
		for _, m := range rd.Messages {
			n.cfg.Transport.Send(m.To, wire.AppendMessage(nil, &m))
		}
		if len(rd.Membership) > 0 {
			n.cfg.Transport.SetMembership(n.cfg.Group, rd.Membership)
		}
		if rd.NeedsWrite() {
			n.sq.push(storeReq{rd: rd})
		}
		if len(rd.Apply) > 0 {
			n.aq.push(applyItem{entries: rd.Apply})
		}
		for _, f := range rd.Fetch {
			n.fetchq.push(f)
		}
		for _, to := range rd.SendSnapshot {
			n.sendSnapshot(to)
		}
		for _, rs := range rd.ReadStates {
			if w, ok := n.reads[rs.Context]; ok {
				delete(n.reads, rs.Context)
				w.done <- readResult{index: rs.Index, code: rs.Code}
			}
		}
		for _, m := range rd.Forwarded {
			n.forwardAnswered(m)
		}
		for _, t := range rd.Transfers {
			for _, w := range n.transfers {
				w <- t.Code
			}
			n.transfers = nil
		}
		n.checkAdmission()
	}
	n.leadershipChanged()
	n.publish()
}

// checkAdmission turns a joining directory into a member once its own
// admission is in the configuration.
func (n *Node) checkAdmission() {
	if n.canVote {
		return
	}
	st := n.r.Status()
	if m, ok := st.Committed.Member(n.stamp.ID); ok && m.Incarnation == n.stamp.Incarnation {
		s := n.stamp
		s.State = coreconsensus.StampMember
		if err := n.cfg.Log.WriteStamp(s); err != nil {
			n.stop(storageFailed("stamp", err), false)
			return
		}
		n.stamp = s
		n.canVote = true
		n.r.SetCanVote(true)
	}
}

// leadershipChanged answers what a change of leader or term made unknown.
func (n *Node) leadershipChanged() {
	st := n.r.Status()
	if st.Term == n.lastTerm && st.Lead == n.lastLead && st.Role == n.lastRole {
		return
	}
	n.lastTerm, n.lastLead, n.lastRole = st.Term, st.Lead, st.Role
	// Forwarded and appended proposals are NOT answered here: an entry at a
	// known position resolves when that index is applied (success or
	// ENTRY_SUPERSEDED), and a forward may still be answered by the leader
	// it went to. A leadership change only starts their grace period; the
	// tick sweep answers OUTCOME_UNKNOWN once it ends (D6).
	now := n.clk.Now()
	for _, p := range n.forwards {
		if p.changedAt.IsZero() {
			p.changedAt = now
		}
	}
	n.am.Lock()
	for idx, p := range n.waiters {
		if idx > st.Commit && p.changedAt.IsZero() {
			p.changedAt = now
		}
	}
	n.am.Unlock()
	for ctx, w := range n.reads {
		delete(n.reads, ctx)
		w.done <- readResult{code: coreconsensus.CodeNotLeader}
	}
	if st.Role == coreconsensus.RoleLeader && len(n.cfg.PreferredLeaders) > 0 {
		n.preferLeader(st)
	}
}

// preferLeader transfers to a preferred node after an election when this
// one is not preferred (D8).
func (n *Node) preferLeader(st raft.Status) {
	for _, id := range n.cfg.PreferredLeaders {
		if id == n.stamp.ID {
			return
		}
	}
	for _, id := range n.cfg.PreferredLeaders {
		if m, ok := st.Conf.Member(id); ok && m.Voter {
			if match, _, ok := n.r.Progress(id); ok && match >= st.Commit {
				n.r.TransferLeadership(n.clk.Now(), id)
				return
			}
		}
	}
}

func (n *Node) publish() {
	st := n.r.Status()
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	s := &coreconsensus.Status{
		ID: n.stamp.ID, Role: st.Role, Term: st.Term, Leader: st.Lead, Commit: st.Commit, Applied: applied,
		Log: n.ident, Timing: st.Timing, NoSpace: st.NoSpace,
		Versions: coreconsensus.Versions{ProtocolMin: 1, ProtocolMax: 1, Cluster: st.Cluster, ClusterGeneration: st.ClusterG},
	}
	if old := n.status.Load(); old != nil && *old == *s {
		return
	}
	n.status.Store(s)
	n.watchMu.Lock()
	for ch := range n.watchers {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- *s:
		default:
		}
	}
	n.watchMu.Unlock()
}

type fetchDone struct {
	req       raft.FetchRequest
	ents      []coreconsensus.Entry
	compacted bool
}

// reader serves the step function's reads below its in-memory window, on
// its own goroutine, never the storage actor's, whose queue holds writes
// only (D4, R8-06).
func (n *Node) reader() {
	defer n.wg.Done()
	var reqs []raft.FetchRequest
	for {
		select {
		case <-n.fetchq.signal:
		case <-n.stopc:
			return
		}
		reqs = n.fetchq.take(reqs)
		for _, f := range reqs {
			d := fetchDone{req: f}
			ents, err := n.cfg.Log.Entries(f.Lo, f.Hi, n.cfg.MaxBatchBytes, nil)
			switch {
			case errs.HasCode(err, coreconsensus.CodeCompacted):
				d.compacted = true
			case err != nil:
				// An impossible answer stops the node: a log that cannot be
				// read cannot be trusted to agree with itself (D4).
				n.stop(err, true)
				return
			}
			for i := range ents {
				ents[i].Data = bytes.Clone(ents[i].Data)
			}
			d.ents = ents
			select {
			case n.fetchc <- d:
			case <-n.stopc:
				return
			}
		}
	}
}

// ---- storage actor ----

// storageActor is the one writer of the log (D5b). Each request is appended
// and synced before its completion goes back to the loop, and only then do
// the replies that depend on it leave.
func (n *Node) storageActor() {
	defer n.wg.Done()
	var reqs []storeReq
	var synced uint64
	for {
		select {
		case <-n.sq.signal:
		case <-n.stopc:
			if n.hard.Load() {
				return
			}
			// The storage actor owns the final sync: on Close it writes and
			// syncs what it holds before it exits (D5b).
			for _, q := range n.sq.take(reqs) {
				if !n.write(q, &synced) {
					return
				}
			}
			return
		}
		reqs = n.sq.take(reqs)
		for _, q := range reqs {
			if n.hard.Load() {
				return
			}
			if !n.write(q, &synced) {
				return
			}
		}
	}
}

func (n *Node) write(q storeReq, synced *uint64) bool {
	rd := &q.rd
	log := n.cfg.Log
	if rd.TruncateFrom != 0 {
		if err := log.TruncateSuffix(rd.TruncateFrom); err != nil {
			n.stop(storageFailed("truncate", err), true)
			return false
		}
		*synced = min(*synced, rd.TruncateFrom-1)
	}
	var hs *coreconsensus.HardState
	last := *synced
	if k := len(rd.Entries); k > 0 {
		last = rd.Entries[k-1].Position.Index
	}
	if rd.HardState != nil || rd.Commit != 0 {
		h, err := log.HardState()
		if err != nil {
			n.stop(storageFailed("hard state", err), true)
			return false
		}
		if rd.HardState != nil {
			h.Term, h.Vote = rd.HardState.Term, rd.HardState.Vote
		}
		if rd.Commit != 0 {
			// A persisted commit never passes the last synced index; the
			// record rides this batch, synced with it (R7-13).
			h.Commit = max(h.Commit, min(rd.Commit, last))
		}
		hs = &h
	}
	if len(rd.Entries) > 0 || hs != nil {
		if err := log.Append(rd.Entries, hs); err != nil {
			n.stop(storageFailed("append", err), true)
			return false
		}
	}
	if rd.Compact != 0 {
		if err := log.CompactPrefix(rd.Compact); err != nil {
			n.stop(storageFailed("compact", err), true)
			return false
		}
	}
	if err := log.Sync(); err != nil {
		// A failed sync stops the node and is never retried (D4).
		n.stop(storageFailed("sync", err), true)
		return false
	}
	*synced = max(*synced, last)
	d := storeDone{seq: rd.Seq}
	if k := len(rd.Entries); k > 0 {
		d.index, d.term = rd.Entries[k-1].Position.Index, rd.Entries[k-1].Position.Term
	}
	for _, m := range rd.AfterSync {
		d.after = append(d.after, wire.AppendMessage(nil, &m))
		d.to = append(d.to, m.To)
	}
	select {
	case n.storec <- d:
	case <-n.stopc:
		return false
	}
	return true
}

// ---- stop ----

// stop records why the node stopped itself, writes its marker, and stops
// every goroutine without a final sync.
func (n *Node) stop(err error, storage bool) {
	n.stopMu.Lock()
	if n.stopReason != nil {
		n.stopMu.Unlock()
		return
	}
	reason := &coreconsensus.StopReason{Err: err}
	if m, ok := n.cfg.Log.(interface {
		markStop(code errs.Code, detail string) bool
	}); ok {
		code, _ := errs.CodeOf(err)
		if !m.markStop(code, err.Error()) && storage {
			reason.Unmarked = true
		}
	}
	n.stopReason = reason
	n.stopMu.Unlock()
	n.stopped.Store(true)
	n.hard.Store(true)
	n.closeOnce.Do(func() { close(n.stopc) })
}

// StopReason returns why the node stopped itself, if it did.
func (n *Node) StopReason() (coreconsensus.StopReason, bool) {
	n.stopMu.Lock()
	defer n.stopMu.Unlock()
	if n.stopReason == nil {
		return coreconsensus.StopReason{}, false
	}
	return *n.stopReason, true
}

func (n *Node) failAllOnStop() {
	err := n.stoppedErr()
	for ctx, p := range n.forwards {
		n.dropForward(ctx)
		if p.stage.Load() == stageUnsent {
			p.done <- propResult{err: err}
		} else {
			p.done <- propResult{err: outcomeUnknown("node stopped")}
		}
	}
	for ctx, w := range n.reads {
		delete(n.reads, ctx)
		w.done <- readResult{code: coreconsensus.CodeNodeStopped}
	}
	for {
		select {
		case p := <-n.propc:
			p.done <- propResult{err: err}
		default:
			n.am.Lock()
			for idx, p := range n.waiters {
				delete(n.waiters, idx)
				p.done <- propResult{err: outcomeUnknown("node stopped")}
			}
			n.acond.Broadcast()
			n.am.Unlock()
			return
		}
	}
}

func (n *Node) stoppedErr() error {
	if r, ok := n.StopReason(); ok {
		return origin(coreconsensus.NodeStopped, errs.String("cause", r.Err.Error()))
	}
	return origin(coreconsensus.NodeStopped)
}

// Close transfers leadership first, then stops every goroutine after the
// storage actor's final sync (R2-38). It is idempotent.
func (n *Node) Close(ctx context.Context) error {
	if !n.stopped.Load() {
		st := n.Status()
		if st.Role == coreconsensus.RoleLeader {
			n.handOff(ctx)
		}
	}
	n.stopped.Store(true)
	n.closeOnce.Do(func() { close(n.stopc) })
	n.wg.Wait()
	n.am.Lock()
	n.acond.Broadcast()
	n.am.Unlock()
	n.releaseDir()
	return nil
}

func (n *Node) releaseDir() {
	n.releaseOnce.Do(func() {
		if a, ok := n.cfg.Log.(dirHolder); ok {
			a.release()
		}
	})
}

// handOff transfers leadership to the most caught-up voter, best effort.
func (n *Node) handOff(ctx context.Context) {
	var to coreconsensus.NodeID
	var best uint64
	n.exec(func() {
		st := n.r.Status()
		for _, m := range st.Conf.Members {
			if !m.Voter || m.ID == n.stamp.ID {
				continue
			}
			if match, _, ok := n.r.Progress(m.ID); ok && (to == 0 || match > best) {
				to, best = m.ID, match
			}
		}
	})
	if to == 0 {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, n.cfg.InitialTiming.ElectionTimeoutMax)
	defer cancel()
	_ = n.transferLocal(tctx, to)
}

// HardClose stops now: no transfer, no final sync, bounded by
// HardCloseTimeout (R7-11).
func (n *Node) HardClose() error {
	n.hard.Store(true)
	n.stopped.Store(true)
	n.closeOnce.Do(func() { close(n.stopc) })
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	t := n.clk.NewTimer(n.cfg.HardCloseTimeout)
	defer t.Stop()
	select {
	case <-done:
		n.releaseDir()
	case <-t.C():
		// A goroutine stuck in fdatasync is abandoned, and the directory
		// stays held until the process exits (R7-11).
	}
	n.am.Lock()
	n.acond.Broadcast()
	n.am.Unlock()
	return nil
}

// Status returns the node's view after its last turn.
func (n *Node) Status() coreconsensus.Status {
	if s := n.status.Load(); s != nil {
		return *s
	}
	return coreconsensus.Status{}
}

// Watch implements LeadershipWatcher.
func (n *Node) Watch(ctx context.Context) <-chan coreconsensus.Status {
	ch := make(chan coreconsensus.Status, 1)
	ch <- n.Status()
	n.watchMu.Lock()
	n.watchers[ch] = struct{}{}
	n.watchMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-n.loopDone:
		}
		n.watchMu.Lock()
		delete(n.watchers, ch)
		close(ch)
		n.watchMu.Unlock()
	}()
	return ch
}

// Membership returns the configuration the node last appended.
func (n *Node) Membership() coreconsensus.Membership {
	var m coreconsensus.Membership
	if !n.exec(func() { m = n.r.Status().Conf }) {
		return coreconsensus.Membership{}
	}
	return m
}
