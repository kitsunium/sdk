package consensus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A proposal's stage says what a cancelled caller may be told: only an
// unsent proposal is certainly not in the log.
const (
	// stageUnsent is a proposal the loop has not handed to raft: a
	// cancellation is ctx.Err(), nothing was appended.
	stageUnsent int32 = iota
	// stagePossible is a proposal raft may have accepted, or forwarded to
	// the leader: its outcome is unknown until answered.
	stagePossible
	// stageAppended is a proposal appended at a known position: it is
	// resolved when that index is applied.
	stageAppended
)

const (
	// inboxDepth bounds the inbound message lanes and the proposal queue:
	// a burst a few heartbeats long is absorbed, a flood is dropped at the
	// lane (and refused at Propose) instead of growing without bound.
	inboxDepth int = 4096
	// loopBacklog bounds the closures and storage completions queued for
	// the event loop; both are produced at most a few per turn.
	loopBacklog int = 1024
	// fetchBacklog bounds the log reads answered back to the loop; reads
	// are issued per lagging peer, so a few dozen cover a cluster.
	fetchBacklog int = 64
	// maxChainRefusals is the N of D4's "no silent loop": how many
	// identical chain refusals of one leader make a follower report itself
	// unready (R6-05), enough to ride out a transient, few enough that an
	// operator hears of a real divergence.
	maxChainRefusals int = 3
	// maxBatchedProposals caps how many queued proposals one turn drains
	// into a batch, so one turn never starves the other inputs.
	maxBatchedProposals int = 1024
	// maxCtrlPerTurn caps the control messages drained after each turn,
	// so control comes first without starving the data lane (D4).
	maxCtrlPerTurn int = 256
)

// Node is one member of a consensus group: the event loop that owns the step
// function, its storage actor, its apply goroutine and its snapshot
// goroutines (D5b). A Node never closes what it was handed.
type Node struct {
	cfg   Config
	clk   clock.Timed
	r     *raft.Raft
	ident coreconsensus.LogIdentityValue
	stamp coreconsensus.StampValue

	inCtrl chan coreconsensus.MessageValue
	inData chan coreconsensus.MessageValue
	propc  chan *proposal
	execc  chan func()
	storec chan storeDone
	fetchq *fifo[raft.FetchSpec]
	fetchc chan fetchDone
	stopc  chan struct{}
	hard   atomic.Bool

	sq *fifo[storeReq]
	aq *fifo[applyItem]

	status atomic.Pointer[coreconsensus.StatusValue]

	am          sync.Mutex
	acond       *sync.Cond
	applied     uint64
	appliedTerm uint64
	waiters     map[uint64]*proposal
	results     map[uint64]applyResult
	resultOrder []uint64
	keepResults int

	pendingBytes atomic.Int64
	// tickEvery is the loop's current tick, for the tests.
	tickEvery atomic.Int64
	// applyLag is the payload bytes handed to the apply goroutine and not
	// yet applied: the gap between commit and applied (D6, R1-14).
	applyLag atomic.Int64

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
	watchers map[chan coreconsensus.StatusValue]struct{}

	stopMu      sync.Mutex
	stopReason  *coreconsensus.StopReasonValue
	stopped     atomic.Bool
	closeOnce   sync.Once
	releaseOnce sync.Once
	wg          sync.WaitGroup
	loopDone    chan struct{}
	// loopDoneOnce closes loopDone, which only the loop does, on its way out.
	loopDoneOnce sync.Once

	obs         *obs
	pendingProp atomic.Int64
	droppedFrom atomic.Int64
	longTurns   atomic.Int64
	joinMu      sync.Mutex
	lastJoin    map[string]time.Time
	joinRefused atomic.Int64
	// trustFrom is set by the package's own tests only: the engine then
	// trusts a frame's From over the transport's peer (R7-05 mutation).
	trustFrom bool
	// crashAfterPublish is set by the package's own tests only.
	crashAfterPublish atomic.Bool
}

// snapState tracks the cuts: at most one in flight, coalesced (D7).
type snapState struct {
	mu       sync.Mutex
	last     uint64
	inFlight bool
	spools   map[string]coreconsensus.SnapshotSink
}

type applyResult struct {
	term uint64
	val  any
}

type proposal struct {
	entry coreconsensus.EntryValue
	admin bool
	stage atomic.Int32 // 0 unsent, 1 possibly accepted, 2 appended at pos
	pos   coreconsensus.PositionValue
	local bool
	done  chan propResult
	// changedAt is when leadership changed while it was unresolved; only
	// the loop and, for waiters, holders of am touch it.
	changedAt time.Time
}

type propResult struct {
	tok coreconsensus.TokenValue
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
	rd raft.ReadyValue
}

type storeDone struct {
	seq, index, term uint64
	after            [][]byte
	to               []coreconsensus.NodeID
}

// newSubEpoch draws a random epoch in the high 32 bits of a forward
// submission id, so ids never repeat across restarts (R7-23, R9-17); it
// also seeds the election timeouts when the caller set no Seed.
func newSubEpoch() uint64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return uint64(binary.LittleEndian.Uint32(b[:])) << 32
}

// NewRaft opens a node over what an offline tool founded or initialised. It
// never bootstraps (D8). The start sequence is fixed (D14): the stamp, the
// latest snapshot whose checksums pass, the log to its end, then serve.
func NewRaft(cfg Config) (*Node, error) {
	cfg, err := withDefaults(&cfg)
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

// newRaft opens the node once NewRaft holds the data directory: it checks
// the stamp, loads the snapshot and the log, binds the transport, then
// launches the node's four background goroutines (reader, storage actor,
// apply, event loop). Each is counted in n.wg and ends once stopc closes,
// through Close, HardClose or a self-stop; nothing is launched on an error.
func newRaft(cfg Config) (*Node, error) {
	stamp, err := startStamp(&cfg)
	if err != nil {
		return nil, err
	}
	cfg.ID = stamp.ID
	n := newNode(&cfg, &stamp)
	n.obs = newObs(&cfg)
	start, err := n.load()
	if err != nil {
		return nil, err
	}
	n.canVote = stamp.State == coreconsensus.StampMember
	silentTerm, silentIndex, err := silenceOf(cfg.Log, n.obs)
	if err != nil {
		return nil, err
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = newSubEpoch() | uint64(stamp.ID)
	}
	n.r = raft.New(raft.Config{
		ID: stamp.ID, Group: cfg.Group, Log: stamp.Log, Seed: seed, Timing: cfg.InitialTiming,
		MaxInflight: cfg.MaxInflight, MaxInflightBytes: cfg.MaxInflightBytes, MaxBatchBytes: cfg.MaxBatchBytes,
		MinVoters: cfg.MinVoters, PromoteLag: cfg.PromoteLag, Lease: cfg.Reads == ReadLease,
		CanVote: n.canVote, MaxChainRefusals: maxChainRefusals, NoTable: n.noTable, RecentEntriesBytes: cfg.RecentEntriesBytes,
		SilentTerm: silentTerm, SilentIndex: silentIndex,
		MaxEntryBytes: cfg.MaxEntryBytes, MaxForwardBytes: cfg.MaxPendingBytes,
	}, start)
	n.publish()
	if err := n.bindTransport(); err != nil {
		return nil, err
	}
	n.wg.Add(4)
	go n.reader()
	go n.storageActor()
	go n.applyLoop()
	go n.loop()
	return n, nil
}

// startStamp reads the stamp a node may start on, refusing a directory
// with a stop marker, one never stamped, one whose bootstrap is pending,
// one of another cluster or node, and a transport whose certificate names
// another cluster (R3-24).
func startStamp(cfg *Config) (coreconsensus.StampValue, error) {
	if mc, ok := cfg.Log.(interface{ checkMarkers() error }); ok {
		if err := mc.checkMarkers(); err != nil {
			return coreconsensus.StampValue{}, err
		}
	}
	stamp, err := cfg.Log.Stamp()
	if err != nil {
		return stamp, storageFailed("stamp", err)
	}
	if err := refuseStamp(cfg, &stamp); err != nil {
		return stamp, err
	}
	if t, ok := cfg.Transport.(interface {
		Cluster() coreconsensus.ClusterID
	}); ok && t.Cluster() != cfg.Cluster {
		// The transport took its cluster from its certificate's SAN (R3-24).
		return stamp, misconfigured("Cluster", "differs from the certificate's URI SAN")
	}
	return stamp, nil
}

// refuseStamp is the stamp half of startStamp: a node starts only on a
// founded or joining directory of its own cluster and, when cfg names one,
// its own id.
func refuseStamp(cfg *Config, stamp *coreconsensus.StampValue) error {
	switch {
	case stamp.State == 0:
		return origin(coreconsensus.DataDirMismatch, errs.String("why", "unstamped: Init or Bootstrap first"))
	case stamp.State == coreconsensus.StampBootstrapPending:
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "bootstrap pending: rerun Bootstrap"))
	case stamp.Log.Cluster != cfg.Cluster:
		return origin(coreconsensus.DataDirMismatch, errs.String("why", "cluster"))
	case cfg.ID != 0 && stamp.ID != cfg.ID:
		return origin(coreconsensus.DataDirMismatch, errs.String("why", "node id"))
	}
	return nil
}

// newNode allocates a node's queues and maps; it starts nothing.
func newNode(cfg *Config, stamp *coreconsensus.StampValue) *Node {
	n := &Node{
		cfg: *cfg, clk: cfg.Clock, ident: stamp.Log, stamp: *stamp,
		inCtrl: make(chan coreconsensus.MessageValue, inboxDepth), inData: make(chan coreconsensus.MessageValue, inboxDepth),
		propc: make(chan *proposal, inboxDepth), execc: make(chan func(), loopBacklog), storec: make(chan storeDone, loopBacklog),
		stopc: make(chan struct{}), sq: newFifo[storeReq](), fetchq: newFifo[raft.FetchSpec](), fetchc: make(chan fetchDone, fetchBacklog), aq: newFifo[applyItem](),
		waiters: map[uint64]*proposal{}, results: map[uint64]applyResult{},
		forwards: map[uint64]*proposal{}, reads: map[uint64]*readWait{}, sending: map[coreconsensus.NodeID]bool{}, unreachableAt: map[coreconsensus.NodeID]time.Time{},
		watchers: map[chan coreconsensus.StatusValue]struct{}{}, subEpoch: newSubEpoch(), loopDone: make(chan struct{}),
	}
	n.acond = sync.NewCond(&n.am)
	return n
}

// silenceOf reads the silence condition a recovered log persists, the term
// and index raft stays silent until; a store that keeps none answers zeros,
// which is no silence. A condition that cannot be read stops the start.
//
// A condition that exists but cannot be read keeps the node silent past the
// end of its log, the safe side, and is reported: an operator otherwise
// finds a node that never votes and no line saying why.
func silenceOf(log coreconsensus.LogStore, o *obs) (term, index uint64, err error) {
	if s, ok := log.(interface {
		silence() (term, index uint64, unreadable, err error)
	}); ok {
		var unreadable error
		if term, index, unreadable, err = s.silence(); err != nil {
			return 0, 0, storageFailed("silence condition", err)
		}
		if unreadable != nil {
			o.warn("consensus: silence condition unreadable, the node stays silent past the end of its log", lstr("error", unreadable.Error()))
		}
	}
	return term, index, nil
}

// bindTransport hands the transport the node's callbacks. An unreachable
// report is queued to the loop and dropped when the loop is saturated, as
// it is advisory; the JOIN handler with reasons is bound when supported.
func (n *Node) bindTransport() error {
	cfg := &n.cfg
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
		return err
	}
	if jb, ok := cfg.Transport.(coreconsensus.JoinReasonBinder); ok {
		jb.BindJoinReason(cfg.Group, n.joinReason)
	}
	return nil
}

// load restores the latest snapshot whose checksums pass and reads the log
// after it.
func (n *Node) load() (raft.StartState, error) {
	metas, err := n.cfg.Snapshots.List()
	if err != nil {
		return raft.StartState{}, storageFailed("snapshots", err)
	}
	durIdx, err := n.durableIndex()
	if err != nil {
		return raft.StartState{}, err
	}
	base := n.restoreLatest(metas, durIdx)
	n.snapState.last = base.Position.Index
	hs, err := n.cfg.Log.HardState()
	if err != nil {
		return raft.StartState{}, origin(coreconsensus.HardStateCorrupt, errs.String("cause", err.Error()))
	}
	first, last := n.cfg.Log.Bounds()
	if base, err = n.baseOrNoTable(base, metas, first); err != nil {
		return raft.StartState{}, err
	}
	snapIdx := base.Position.Index
	if first, last, err = n.finishInstall(&base, first, last); err != nil {
		return raft.StartState{}, err
	}
	ents, err := n.readTail(first, last, snapIdx)
	if err != nil {
		return raft.StartState{}, err
	}
	applied := n.startApplied(&base, durIdx)
	return raft.StartState{HardState: hs, Snapshot: base, Entries: ents, Applied: applied, Now: n.clk.Now()}, nil
}

// durableIndex is the index a DurableStateMachine already holds applied,
// counted only when it was applied in this log's lineage, or in the
// predecessor's below the kept index; any other machine answers 0.
func (n *Node) durableIndex() (uint64, error) {
	d, ok := n.cfg.Machine.(coreconsensus.DurableStateMachine)
	if !ok {
		return 0, nil
	}
	li, pos, err := d.AppliedPosition()
	if err != nil {
		return 0, err
	}
	if li.Same(n.ident) || (pos.Index <= n.ident.KeptIndex && li.Cluster == n.ident.Predecessor) {
		return pos.Index, nil
	}
	return 0, nil
}

// restoreLatest restores from the newest snapshot that verifies, falling
// back to the older one when it does not (D7); none restores is the zero
// base.
func (n *Node) restoreLatest(metas []coreconsensus.SnapshotMetaValue, durIdx uint64) coreconsensus.SnapshotMetaValue {
	for _, m := range metas {
		if err := n.restoreFrom(m, durIdx); err != nil {
			continue // fall back to the older one (D7)
		}
		return m
	}
	return coreconsensus.SnapshotMetaValue{}
}

// baseOrNoTable keeps a restored base, or starts the node with no table
// when a snapshot was needed and none restored.
func (n *Node) baseOrNoTable(base coreconsensus.SnapshotMetaValue, metas []coreconsensus.SnapshotMetaValue, first uint64) (coreconsensus.SnapshotMetaValue, error) {
	if base.Position.Index != 0 || (len(metas) == 0 && first <= 1) {
		return base, nil
	}
	// Every snapshot fails its checksums, or none is left where the
	// log needs one: the node starts with NO table. It keeps its term,
	// vote and commit index, votes and keeps its log, never campaigns,
	// applies nothing and asks the leader for an install (D7, R6-13,
	// R7-18).
	n.noTable = true
	base, err := noTableBase(n.cfg.Log, metas, first)
	if err != nil {
		return base, storageFailed("term", err)
	}
	return base, nil
}

// finishInstall refuses a log that starts past the base, then completes an
// install interrupted after its snapshot was published, and returns the
// log's bounds as they now stand.
func (n *Node) finishInstall(base *coreconsensus.SnapshotMetaValue, first, last uint64) (newFirst, newLast uint64, err error) {
	snapIdx := base.Position.Index
	if first > snapIdx+1 {
		return first, last, origin(coreconsensus.LogCorrupt, errs.String("why", "gap after the snapshot"))
	}
	// A published snapshot is the commit point of an install (D7): a crash
	// after it, before the log was moved onto it, is finished here. The
	// suffix is kept only when the boundary entry matches; otherwise the
	// whole log is replaced, and a log that ends below the snapshot is
	// compacted to it, so the next append continues at snapIdx+1.
	if snapIdx < first {
		return first, last, nil
	}
	match := false
	if snapIdx <= last {
		t, terr := n.cfg.Log.Term(snapIdx)
		match = terr == nil && t == base.Position.Term
	}
	if !match {
		if err := n.cfg.Log.TruncateSuffix(first); err != nil {
			return first, last, storageFailed("truncate", err)
		}
	}
	if match && snapIdx <= last {
		return first, last, nil
	}
	if err := n.cfg.Log.CompactPrefix(snapIdx); err != nil {
		return first, last, storageFailed("compact", err)
	}
	if err := n.cfg.Log.Sync(); err != nil {
		return first, last, storageFailed("sync", err)
	}
	newFirst, newLast = n.cfg.Log.Bounds()
	return newFirst, newLast, nil
}

// readTail reads the log after the base into memory, each entry's payload
// copied so the step function never holds a store's buffer.
func (n *Node) readTail(first, last, snapIdx uint64) ([]coreconsensus.EntryValue, error) {
	if last <= snapIdx || last < first {
		return nil, nil
	}
	ents, err := n.cfg.Log.Entries(max(first, snapIdx+1), last+1, int(^uint(0)>>1), nil)
	if err != nil {
		return nil, err
	}
	for i := range ents {
		ents[i].Data = bytes.Clone(ents[i].Data)
	}
	return ents, nil
}

// startApplied sets where apply resumes: past the base, or past what a
// durable machine already holds. It returns the applied index raft starts
// from; a node with no table records nothing applied until an install.
func (n *Node) startApplied(base *coreconsensus.SnapshotMetaValue, durIdx uint64) uint64 {
	snapIdx := base.Position.Index
	applied := max(snapIdx, durIdx)
	n.applied = applied
	if applied == snapIdx {
		n.appliedTerm = base.Position.Term
	}
	if n.noTable && durIdx < snapIdx {
		n.applied, n.appliedTerm = 0, 0 // nothing is applied until an install
	}
	return applied
}

// noTableBase is the base a node with no usable snapshot starts from: the
// log's compaction point, with the configuration and chain tip the newest
// snapshot's metadata records, which stays readable when its bytes do not.
//
// The term at first-1 is the log's own compaction term, which every LogStore
// answers; a store that cannot is one whose base this node must not guess,
// since a wrong term there is a log-matching check answered falsely.
func noTableBase(log coreconsensus.LogStore, metas []coreconsensus.SnapshotMetaValue, first uint64) (coreconsensus.SnapshotMetaValue, error) {
	var b coreconsensus.SnapshotMetaValue
	if len(metas) > 0 {
		b = metas[0]
		for _, m := range metas {
			if m.Position.Index == first-1 {
				b = m
			}
		}
	}
	b.Position.Index = first - 1
	term, err := log.Term(first - 1)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	b.Position.Term = term
	return b, nil
}

// ---- transport callbacks ----

// deliver is the transport's inbound callback: it parses a frame, drops it
// when its From is not the authenticated peer (R7-05), and queues it on its
// lane without ever blocking the transport; a full lane drops the frame,
// which raft's retransmission absorbs.
func (n *Node) deliver(from coreconsensus.AuthenticatedPeerValue, frame []byte) {
	if n.stopped.Load() {
		return
	}
	var m coreconsensus.MessageValue
	if err := wire.ParseMessage(bytes.Clone(frame), &m); err != nil {
		return
	}
	if m.From != from.ID && !n.trustFrom {
		// The engine trusts the transport's authenticated peer, never the
		// frame's From (R7-05).
		n.droppedFrom.Add(1)
		n.obs.inc(mFramesDropped, coreotel.String("reason", "from_mismatch"))
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

// loop is the event loop, the only goroutine that touches raft: one input
// per turn, then the control lane drained first, then the turn's Ready
// handled. It ends when stopc closes, after failing every caller still
// waiting on it, and closes loopDone on the way out.
func (n *Node) loop() {
	defer n.wg.Done()
	defer n.loopDoneOnce.Do(func() { close(n.loopDone) })
	every := n.tickInterval()
	n.tickEvery.Store(int64(every))
	tick := n.clk.NewTicker(every)
	defer tick.Stop()
	var props []*proposal
	for {
		turn, open := n.turn(tick, &props)
		if !open {
			n.failAllOnStop()
			return
		}
		n.drainControl()
		n.handleReady()
		n.checkTurn(turn)
	}
}

// turn waits for one input of the event loop and steps it, returning when
// the turn started. It reports false on stop. The inputs are never closed;
// were one closed, its handler reports false and the loop ends as it does
// on stop instead of spinning on the closed channel.
func (n *Node) turn(tick clock.Ticker, props *[]*proposal) (time.Time, bool) {
	select {
	case <-n.stopc:
		return time.Time{}, false
	case _, ok := <-tick.C():
		return n.onTick(tick, ok)
	case m, ok := <-n.inCtrl:
		return n.onMessage(&m, ok)
	case m, ok := <-n.inData:
		return n.onMessage(&m, ok)
	case d, ok := <-n.storec:
		return n.onStored(&d, ok)
	case p, ok := <-n.propc:
		return n.onProposal(p, ok, props)
	case f, ok := <-n.execc:
		return n.onExec(f, ok)
	case f, ok := <-n.fetchc:
		return n.onFetched(&f, ok)
	}
}

// onTick advances raft's clock, follows a committed contract that changed
// the heartbeat, answers the proposals whose grace ended, and publishes the
// periodic gauges.
func (n *Node) onTick(tick clock.Ticker, ok bool) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	n.r.Tick(n.clk.Now())
	if d := n.tickInterval(); d != time.Duration(n.tickEvery.Load()) {
		// A committed contract changed the heartbeat (review of PR
		// #301, L5).
		n.tickEvery.Store(int64(d))
		tick.Reset(d)
	}
	n.sweep()
	n.observePeers()
	n.observeResources()
	return turn, true
}

// onMessage steps one inbound message, of either lane, after telling raft
// whether the apply backlog is over MaxApplyLagBytes.
func (n *Node) onMessage(m *coreconsensus.MessageValue, ok bool) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	n.r.SetOverloaded(n.applyLag.Load() >= n.cfg.MaxApplyLagBytes)
	n.r.Step(n.clk.Now(), *m)
	return turn, true
}

// onStored takes a completion of the storage actor.
func (n *Node) onStored(d *storeDone, ok bool) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	n.storageDone(d)
	return turn, true
}

// onProposal batches the proposal that woke the loop with those queued
// behind it.
func (n *Node) onProposal(p *proposal, ok bool, props *[]*proposal) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	n.batchProposals(p, props)
	return turn, true
}

// onExec runs a closure exec queued for the loop.
func (n *Node) onExec(f func(), ok bool) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	f()
	return turn, true
}

// onFetched hands raft the entries the reader fetched below its window.
func (n *Node) onFetched(f *fetchDone, ok bool) (time.Time, bool) {
	if !ok {
		return time.Time{}, false
	}
	turn := n.clk.Now()
	n.r.Fetched(f.req.To, f.req.Lo, f.ents, f.compacted)
	return turn, true
}

// storageDone tells raft a write is durable, then sends the replies that
// waited on it: only now may they leave (D5b).
func (n *Node) storageDone(d *storeDone) {
	n.r.StorageDone(d.seq, d.index, d.term)
	for i, f := range d.after {
		n.cfg.Transport.Send(d.to[i], f)
	}
}

// batchProposals hands raft the proposal that woke the loop together with
// those already queued behind it, up to maxBatchedProposals, as one batch.
func (n *Node) batchProposals(first *proposal, props *[]*proposal) {
	batch := append((*props)[:0], first)
	for len(batch) < maxBatchedProposals {
		p, ok := n.queuedProposal()
		if !ok {
			break
		}
		batch = append(batch, p)
	}
	*props = batch
	n.handleProposals(batch)
}

// queuedProposal takes a proposal already queued, without waiting. The
// queue is never closed, so a closed one reads as empty.
func (n *Node) queuedProposal() (*proposal, bool) {
	select {
	case p, ok := <-n.propc:
		return p, ok
	default:
		return nil, false
	}
}

// drainControl steps the control messages queued after a turn, up to
// maxCtrlPerTurn: a flood of entries never starves a vote (D4).
func (n *Node) drainControl() {
	for range maxCtrlPerTurn {
		m, ok := n.queuedControl()
		if !ok {
			return
		}
		n.r.Step(n.clk.Now(), m)
	}
}

// queuedControl takes a control message already queued, without waiting.
// The lane is never closed, so a closed one reads as empty.
func (n *Node) queuedControl() (coreconsensus.MessageValue, bool) {
	select {
	case m, ok := <-n.inCtrl:
		return m, ok
	default:
		return coreconsensus.MessageValue{}, false
	}
}

// checkTurn drops the lease for the rest of the term after a turn longer
// than the drift budget, ElectionTimeoutMin × MaxClockDrift: a pause of
// the process is exactly that (D9, R7-20, R8-37).
func (n *Node) checkTurn(turn time.Time) {
	d := n.clk.Since(turn)
	n.obs.record(mLoopTurn, d.Seconds())
	if n.cfg.Reads != ReadLease {
		return
	}
	t := n.r.Status().Timing
	budget := time.Duration(float64(t.ElectionTimeoutMin) * t.MaxClockDrift)
	if budget > 0 && d > budget {
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
	n.sweepWaiters(now, grace)
}

// sweepWaiters is sweep's half for appended proposals: one still above the
// commit index when its grace ends is answered OUTCOME_UNKNOWN and its
// pending bytes are released.
func (n *Node) sweepWaiters(now time.Time, grace time.Duration) {
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

// dropForward forgets a forwarded proposal and the applied result kept for
// its answer.
func (n *Node) dropForward(ctx uint64) {
	delete(n.forwards, ctx)
	n.am.Lock()
	n.keepResults--
	n.am.Unlock()
}

// tickInterval is a fifth of the committed contract's heartbeat, so raft's
// timers fire within a fifth of a heartbeat of their due time, and never
// below a millisecond.
func (n *Node) tickInterval() time.Duration {
	return max(n.r.Status().Timing.HeartbeatInterval/5, time.Millisecond)
}

// handleReady drains every Ready raft has, in order, then reacts to a
// change of leadership and publishes the status. It runs on the loop, and
// returns at once when a Ready stopped the node.
func (n *Node) handleReady() {
	for n.r.HasReady() {
		rd := new(n.r.Ready())
		if n.stopOnUnsupported(rd) {
			return
		}
		n.handleOne(rd)
	}
	n.leadershipChanged()
	n.publish()
}

// stopOnUnsupported stops the node with VERSION_UNSUPPORTED when a Ready
// names an entry whose kind this version does not know, and reports whether
// it did; that Ready is then not carried out at all, and handleReady
// returns without publishing.
func (n *Node) stopOnUnsupported(rd *raft.ReadyValue) bool {
	u := rd.Unsupported
	if u.Index == 0 {
		return false
	}
	n.stop(unsupportedKind(u.Kind, u.Index), false)
	return true
}

// handleOne carries out one Ready: messages sent, membership and silence
// applied, writes and entries queued to the storage actor and the apply
// goroutine, fetches and snapshot sends started, waiters answered.
func (n *Node) handleOne(rd *raft.ReadyValue) {
	for _, m := range rd.Messages {
		n.cfg.Transport.Send(m.To, wire.AppendMessage(nil, &m))
	}
	if len(rd.Membership) > 0 {
		n.cfg.Transport.SetMembership(n.cfg.Group, rd.Membership)
		n.obs.info("consensus: admitted members changed", lattr("members", uint64(len(rd.Membership))))
	}
	if rd.SilenceEnded {
		n.endSilence()
	}
	if rd.TruncateFrom != 0 {
		n.obs.info("consensus: log truncated", lattr("from", rd.TruncateFrom))
	}
	if rd.NeedsWrite() {
		n.sq.push(storeReq{rd: *rd})
	}
	n.queueApply(rd.Apply)
	for _, f := range rd.Fetch {
		n.fetchq.push(f)
	}
	for _, to := range rd.SendSnapshot {
		n.sendSnapshot(to)
	}
	n.answerReady(rd)
	n.checkAdmission()
}

// endSilence persists the end of the silence condition; a store that
// cannot record it stops the node, since a restart would be silent again.
func (n *Node) endSilence() {
	if s, ok := n.cfg.Log.(interface{ endSilence() error }); ok {
		if err := s.endSilence(); err != nil {
			n.stop(storageFailed("end silence", err), true)
		}
	}
	n.obs.info("consensus: silence condition met, the node votes and acknowledges again")
}

// queueApply hands committed entries to the apply goroutine, counting
// their payload into the apply lag first.
func (n *Node) queueApply(entries []coreconsensus.EntryValue) {
	if len(entries) == 0 {
		return
	}
	var b int64
	for i := range entries {
		b += int64(len(entries[i].Data))
	}
	n.obs.setGauge(mApplyLagBytes, float64(n.applyLag.Add(b)))
	n.aq.push(applyItem{entries: entries})
}

// answerReady answers the reads, forwards and transfers a Ready resolved.
func (n *Node) answerReady(rd *raft.ReadyValue) {
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
	n.startGrace(st.Commit)
	for ctx, w := range n.reads {
		delete(n.reads, ctx)
		w.done <- readResult{code: coreconsensus.CodeNotLeader}
	}
	if st.Role == coreconsensus.RoleLeader && len(n.cfg.PreferredLeaders) > 0 {
		n.preferLeader(st)
	}
}

// startGrace starts the grace period of every unresolved proposal.
// Forwarded and appended proposals are NOT answered on a leadership change:
// an entry at a known position resolves when that index is applied
// (success or ENTRY_SUPERSEDED), and a forward may still be answered by the
// leader it went to. The change only starts their grace period; the tick
// sweep answers OUTCOME_UNKNOWN once it ends (D6).
func (n *Node) startGrace(commit uint64) {
	now := n.clk.Now()
	for _, p := range n.forwards {
		if p.changedAt.IsZero() {
			p.changedAt = now
		}
	}
	n.am.Lock()
	for idx, p := range n.waiters {
		if idx > commit && p.changedAt.IsZero() {
			p.changedAt = now
		}
	}
	n.am.Unlock()
}

// preferLeader transfers to a preferred node after an election when this
// one is not preferred (D8).
func (n *Node) preferLeader(st raft.StatusValue) {
	if slices.Contains(n.cfg.PreferredLeaders, n.stamp.ID) {
		return
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

// publish stores the status a turn produced and wakes every watcher with
// it, replacing a value the watcher has not read yet so a slow reader sees
// the newest status, never a backlog; an unchanged status publishes nothing.
func (n *Node) publish() {
	st := n.r.Status()
	n.am.Lock()
	applied := n.applied
	n.am.Unlock()
	s := &coreconsensus.StatusValue{
		ID: n.stamp.ID, Role: st.Role, Term: st.Term, Leader: st.Lead, Commit: st.Commit, Applied: applied,
		Log: n.ident, Timing: st.Timing, NoSpace: st.NoSpace,
		Versions: coreconsensus.VersionsValue{ProtocolMin: 1, ProtocolMax: raft.ProtocolMax, Cluster: st.Cluster, ClusterGeneration: st.ClusterG},
	}
	old := n.status.Load()
	if old != nil && *old == *s {
		return
	}
	n.status.Store(s)
	n.observeStatus(old, s)
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
	req       raft.FetchSpec
	ents      []coreconsensus.EntryValue
	compacted bool
}

// reader serves the step function's reads below its in-memory window, on
// its own goroutine, never the storage actor's, whose queue holds writes
// only (D4, R8-06).
func (n *Node) reader() {
	defer n.wg.Done()
	var reqs []raft.FetchSpec
	for {
		select {
		case _, ok := <-n.fetchq.signal:
			// The fifo never closes its signal channel; were it closed, the
			// reader ends as it does on stop rather than spinning on it.
			if !ok {
				return
			}
		case <-n.stopc:
			return
		}
		reqs = n.fetchq.take(reqs)
		for _, f := range reqs {
			if !n.fetch(f) {
				return
			}
		}
	}
}

// fetch reads one range for raft and hands it to the loop; a compacted
// range is reported as such. It returns false when the reader must end:
// the read failed and stopped the node, or the node stopped meanwhile.
func (n *Node) fetch(f raft.FetchSpec) bool {
	d := fetchDone{req: f}
	ents, err := n.cfg.Log.Entries(f.Lo, f.Hi, n.cfg.MaxBatchBytes, nil)
	switch {
	case errs.HasCode(err, coreconsensus.CodeCompacted):
		d.compacted = true
	case err != nil:
		// An impossible answer stops the node: a log that cannot be
		// read cannot be trusted to agree with itself (D4).
		n.stop(err, true)
		return false
	}
	for i := range ents {
		ents[i].Data = bytes.Clone(ents[i].Data)
	}
	d.ents = ents
	select {
	case n.fetchc <- d:
		return true
	case <-n.stopc:
		return false
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
		case _, ok := <-n.sq.signal:
			// The fifo never closes its signal channel; were it closed, the
			// actor ends as a hard stop does rather than spinning on it.
			if !ok {
				return
			}
		case <-n.stopc:
			n.finalWrites(reqs, &synced)
			return
		}
		reqs = n.sq.take(reqs)
		if !n.writeAll(reqs, &synced) {
			return
		}
	}
}

// finalWrites is the storage actor's exit on stop. The actor owns the
// final sync: on Close it writes and syncs what it holds before it exits
// (D5b); on a hard stop it writes nothing more.
func (n *Node) finalWrites(reqs []storeReq, synced *uint64) {
	if n.hard.Load() {
		return
	}
	for _, q := range n.sq.take(reqs) {
		if !n.write(&q, synced) {
			return
		}
	}
}

// writeAll writes a batch of requests in order, and returns false when the
// actor must end: a hard stop arrived, or a write stopped the node.
func (n *Node) writeAll(reqs []storeReq, synced *uint64) bool {
	for _, q := range reqs {
		if n.hard.Load() {
			return false
		}
		if !n.write(&q, synced) {
			return false
		}
	}
	return true
}

// write persists one Ready, truncation, entries with their hard state,
// then compaction, syncs it, and only then hands the loop its completion
// with the replies that waited on it. Any storage failure stops the node
// and is never retried (D4); it returns false then, or when the node
// stopped before the completion was taken.
func (n *Node) write(q *storeReq, synced *uint64) bool {
	rd := &q.rd
	if !n.truncateFor(rd, synced) {
		return false
	}
	last := *synced
	if k := len(rd.Entries); k > 0 {
		last = rd.Entries[k-1].Position.Index
	}
	if !n.appendFor(rd, last) {
		return false
	}
	if rd.Compact != 0 {
		if err := n.cfg.Log.CompactPrefix(rd.Compact); err != nil {
			n.stop(storageFailed("compact", err), true)
			return false
		}
	}
	if !n.syncFor(rd) {
		return false
	}
	*synced = max(*synced, last)
	return n.completeWrite(rd)
}

// truncateFor removes the log suffix a Ready overwrites, and lowers the
// synced index with it, since what was synced there no longer exists.
func (n *Node) truncateFor(rd *raft.ReadyValue, synced *uint64) bool {
	if rd.TruncateFrom == 0 {
		return true
	}
	if err := n.cfg.Log.TruncateSuffix(rd.TruncateFrom); err != nil {
		n.stop(storageFailed("truncate", err), true)
		return false
	}
	*synced = min(*synced, rd.TruncateFrom-1)
	return true
}

// appendFor appends a Ready's entries together with the hard state they
// change, in one record, so the two are synced together.
func (n *Node) appendFor(rd *raft.ReadyValue, last uint64) bool {
	hs, ok := n.hardStateFor(rd, last)
	if !ok {
		return false
	}
	if len(rd.Entries) > 0 || hs != nil {
		if err := n.cfg.Log.Append(rd.Entries, hs); err != nil {
			n.stop(storageFailed("append", err), true)
			return false
		}
	}
	return true
}

// hardStateFor is the hard state a Ready changes, or nil when it changes
// none: the term and vote raft set, and a commit capped at the last index
// this batch makes durable (R7-13).
func (n *Node) hardStateFor(rd *raft.ReadyValue, last uint64) (*coreconsensus.HardState, bool) {
	if rd.HardState == nil && rd.Commit == 0 {
		return nil, true
	}
	h, err := n.cfg.Log.HardState()
	if err != nil {
		n.stop(storageFailed("hard state", err), true)
		return nil, false
	}
	if rd.HardState != nil {
		h.Term, h.Vote = rd.HardState.Term, rd.HardState.Vote
	}
	if rd.Commit != 0 {
		// A persisted commit never passes the last synced index; the
		// record rides this batch, synced with it (R7-13).
		h.Commit = max(h.Commit, min(rd.Commit, last))
	}
	return &h, true
}

// syncFor syncs the log and records the latency and the batch's size. A
// failed sync stops the node and is never retried (D4).
func (n *Node) syncFor(rd *raft.ReadyValue) bool {
	syncStart := n.clk.Now()
	err := n.cfg.Log.Sync()
	n.obs.record(mFsyncLatency, n.clk.Since(syncStart).Seconds())
	if len(rd.Entries) > 0 {
		size := 0
		for i := range rd.Entries {
			size += len(rd.Entries[i].Data)
		}
		n.obs.record(mBatchEntries, float64(len(rd.Entries)))
		n.obs.record(mBatchBytes, float64(size))
	}
	if err != nil {
		// A failed sync stops the node and is never retried (D4).
		n.stop(storageFailed("sync", err), true)
		return false
	}
	return true
}

// completeWrite hands the loop the completion of a synced Ready with the
// replies that waited on it; false when the node stopped first.
func (n *Node) completeWrite(rd *raft.ReadyValue) bool {
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
	reason := &coreconsensus.StopReasonValue{Err: err}
	if m, ok := n.cfg.Log.(interface {
		markStopEach(code errs.Code, detail string) (dirErr, runErr error)
	}); ok {
		code, _ := errs.CodeOf(err)
		dirErr, runErr := m.markStopEach(code, err.Error())
		if dirErr != nil && (runErr != nil || code != coreconsensus.CodeStorageFailed) && storage {
			reason.Unmarked = true
		}
		n.reportUnwrittenMarkers(dirErr, runErr)
	}
	n.stopReason = reason
	n.stopMu.Unlock()
	code, _ := errs.CodeOf(err)
	n.obs.setGauge(mStopped, 1, coreotel.String("reason", reasonOf(code)))
	n.obs.warn("consensus: node stopped", lstr("reason", reasonOf(code)), lstr("error", err.Error()))
	n.stopped.Store(true)
	n.hard.Store(true)
	n.closeOnce.Do(func() { close(n.stopc) })
}

// reportUnwrittenMarkers logs each place a stop marker could not be written.
// R9-05 accepts a stop that left one marker of the two; the line says which
// one is missing, so an operator knows what protects the next start.
func (n *Node) reportUnwrittenMarkers(dirErr, runErr error) {
	if dirErr != nil {
		n.obs.warn("consensus: stop marker not written in the data directory", lstr("error", dirErr.Error()))
	}
	if runErr != nil {
		n.obs.warn("consensus: boot-scoped stop marker not written", lstr("error", runErr.Error()))
	}
}

// StopReason returns why the node stopped itself, if it did.
func (n *Node) StopReason() (coreconsensus.StopReasonValue, bool) {
	n.stopMu.Lock()
	defer n.stopMu.Unlock()
	if n.stopReason == nil {
		return coreconsensus.StopReasonValue{}, false
	}
	return *n.stopReason, true
}

// failAllOnStop answers every caller the loop still owes on its way out:
// an unsent proposal or read with NODE_STOPPED, and anything that may have
// been appended with OUTCOME_UNKNOWN, since it may yet commit elsewhere.
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
		p, ok := n.queuedProposal()
		if !ok {
			break
		}
		p.done <- propResult{err: err}
	}
	n.am.Lock()
	for idx, p := range n.waiters {
		delete(n.waiters, idx)
		p.done <- propResult{err: outcomeUnknown("node stopped")}
	}
	n.acond.Broadcast()
	n.am.Unlock()
}

// stoppedErr is NODE_STOPPED, carrying the self-stop's cause when there was
// one, so a caller failed by a stop learns why.
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

// releaseDir gives the data directory back, once, so offline tools may use
// it again; HardClose skips it when a goroutine may still be writing.
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
	// Close goes on whatever the transfer answered: the hand-off only
	// spares the cluster an election timeout, so a failed one is worth a
	// line in the log and nothing more.
	if err := n.transferLocal(tctx, to); err != nil {
		n.obs.info("consensus: leadership hand-off at close did not complete", lattr("to", uint64(to)), lstr("error", err.Error()))
	}
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
func (n *Node) Status() coreconsensus.StatusValue {
	if s := n.status.Load(); s != nil {
		return *s
	}
	return coreconsensus.StatusValue{}
}

// Watch implements LeadershipWatcher.
func (n *Node) Watch(ctx context.Context) <-chan coreconsensus.StatusValue {
	ch := make(chan coreconsensus.StatusValue, 1)
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
func (n *Node) Membership() coreconsensus.MembershipValue {
	var m coreconsensus.MembershipValue
	if !n.exec(func() { m = n.r.Status().Conf }) {
		return coreconsensus.MembershipValue{}
	}
	return m
}
