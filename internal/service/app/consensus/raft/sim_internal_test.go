package raft

import (
	"container/heap"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"slices"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The simulator of D15: N step functions on ONE goroutine against a virtual
// clock and an event queue, so a run is a pure function of its seed. The
// disk model separates written from synced bytes: a crash drops every write
// whose sync has not completed. The safety oracle runs after every event.

type evKind uint8

const (
	evDeliver evKind = iota
	evStorage
	evTick
	evRestart
	// evInstall carries a snapshot from a leader to a follower; evSnapDone
	// carries the outcome back.
	evInstall
	evSnapDone
)

type event struct {
	at   time.Time
	ord  int
	kind evKind
	node core.NodeID
	life int
	msg  core.MessageValue
	meta core.SnapshotMetaValue
	ok   bool
}

type eventQueue []*event

func (q *eventQueue) Len() int { return len(*q) }
func (q *eventQueue) Less(i, j int) bool {
	a, b := (*q)[i], (*q)[j]
	if !a.at.Equal(b.at) {
		return a.at.Before(b.at)
	}
	return a.ord < b.ord
}
func (q *eventQueue) Swap(i, j int) { (*q)[i], (*q)[j] = (*q)[j], (*q)[i] }
func (q *eventQueue) Push(x any)    { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

type simWrite struct {
	rd     ReadyValue
	doneAt time.Time
}

type simNode struct {
	// rate is the node's clock rate against true time: 1 is exact (D15,
	// clock-rate skew per node).
	rate float64
	// rateFrom and clockAt anchor the node's clock where its rate last
	// changed.
	rateFrom, clockAt time.Time
	id                core.NodeID
	r                 *Raft
	alive             bool
	life              int
	hs                core.HardState
	// base is the durable snapshot the log continues; ents are the durable
	// entries after it.
	base core.SnapshotMetaValue
	ents []core.EntryValue
	// noTable is set while the node lost its snapshot's body: it restarts
	// with no table and asks for an install (R7-18).
	noTable bool
	// appliedFrom is the index the state machine resumes after.
	appliedFrom uint64
	writes      []simWrite
	lastDone    time.Time
	applied     []core.EntryValue
	checked     uint64
	maxTerm     uint64
}

type entryKey struct {
	term uint64
	kind core.EntryKind
	sum  uint32
}

func keyOf(e *core.EntryValue) entryKey {
	return entryKey{term: e.Position.Term, kind: e.Kind, sum: crc32.ChecksumIEEE(e.Data)}
}

type simOptions struct {
	seed      uint64
	nodes     int
	drop      float64
	dup       float64
	diskMin   time.Duration
	diskMax   time.Duration
	crashRate float64 // crashes per second across the cluster, drawn every 100 ms
	mut       mutation
	lease     bool
	recent    int64
	// snapEvery cuts a snapshot on each node every snapEvery applied
	// entries; 0 cuts none.
	snapEvery uint64
	// snapLoss is the chance a restart loses the snapshot's body, the node
	// keeping its log and starting with no table.
	snapLoss float64
}

type sim struct {
	opt       simOptions
	rng       *rand.Rand
	now       time.Time
	q         eventQueue
	ord       int
	nodes     map[core.NodeID]*simNode
	ids       []core.NodeID
	cut       map[[2]core.NodeID]bool
	leaders   map[uint64]core.NodeID
	votes     map[[2]uint64]core.NodeID
	applied   map[uint64]entryKey
	commits   map[uint64]entryKey
	proposed  int
	err       error
	timing    core.TimingContractSpec
	nextCrash time.Time
	forwarded map[[2]uint64]core.MessageValue
	fetches   int
	kv        *kvWorld
}

func rtt(a, b core.NodeID) time.Duration {
	if a > b {
		a, b = b, a
	}
	switch {
	case a == 1 && b == 2:
		return 9600 * time.Microsecond
	case a == 1 && b == 3:
		return 22800 * time.Microsecond
	case a == 2 && b == 3:
		return 27300 * time.Microsecond
	}
	return 20 * time.Millisecond
}

func newSim(opt simOptions) *sim {
	if opt.nodes == 0 {
		opt.nodes = 3
	}
	if opt.diskMax == 0 {
		opt.diskMin, opt.diskMax = 500*time.Microsecond, 5*time.Millisecond
	}
	s := &sim{
		opt:       opt,
		rng:       rand.New(rand.NewPCG(opt.seed, 0x5eed)),
		now:       time.Unix(1_700_000_000, 0),
		nodes:     map[core.NodeID]*simNode{},
		cut:       map[[2]core.NodeID]bool{},
		leaders:   map[uint64]core.NodeID{},
		votes:     map[[2]uint64]core.NodeID{},
		applied:   map[uint64]entryKey{},
		commits:   map[uint64]entryKey{},
		forwarded: map[[2]uint64]core.MessageValue{},
		timing: core.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01,
		},
	}
	var conf core.MembershipValue
	for i := 1; i <= opt.nodes; i++ {
		id := core.NodeID(i)
		s.ids = append(s.ids, id)
		conf.Members = append(conf.Members, core.MemberValue{ID: id, Voter: true, SPKI: [32]byte{byte(i)}})
	}
	// The founding configuration is entry 1 on every founder, committed.
	data := wire.AppendAdmin(nil, &wire.AdminValue{Change: wire.ChangeFounding, Membership: conf})
	found := core.EntryValue{Position: core.PositionValue{Term: 1, Index: 1}, Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}
	found.ChainSum = wire.ChainSum(0, &found)
	for _, id := range s.ids {
		n := &simNode{id: id, hs: core.HardState{Term: 1, Commit: 1}, ents: []core.EntryValue{found}}
		s.nodes[id] = n
		s.start(n)
	}
	return s
}

func (s *sim) cfg(id core.NodeID) Config {
	return Config{
		ID: id, Group: 1, Seed: s.opt.seed*31 + uint64(id), Timing: s.timing,
		MaxInflight: 16, MaxInflightBytes: 1 << 20, MaxBatchBytes: 64 << 10, MinVoters: 1,
		PromoteLag: 64, Lease: s.opt.lease, CanVote: true, MaxChainRefusals: 3, mut: s.opt.mut, RecentEntriesBytes: s.opt.recent,
	}
}

func (s *sim) start(n *simNode) {
	if s.kv != nil {
		s.kv.restart(n)
	}
	n.alive = true
	n.life++
	n.writes = nil
	n.lastDone = s.now
	n.applied = nil
	n.appliedFrom = n.base.Position.Index
	n.checked = 0
	cfg := s.cfg(n.id)
	cfg.NoTable = n.noTable
	n.r = NewRaft(&cfg, &StartState{HardState: n.hs, Snapshot: n.base, Entries: slices.Clone(n.ents), Applied: n.base.Position.Index, Now: s.clock(n)})
	s.push(&event{at: s.now.Add(time.Duration(s.rng.Int64N(int64(10 * time.Millisecond)))), kind: evTick, node: n.id, life: n.life})
}

// clock is the node's own reading of the true time s.now.
func (s *sim) clock(n *simNode) time.Time {
	if n.rate == 0 || n.rateFrom.IsZero() {
		return s.now
	}
	return n.clockAt.Add(time.Duration(float64(s.now.Sub(n.rateFrom)) * n.rate))
}

// setRate changes a node's clock rate from now on; its clock stays
// monotonic.
func (s *sim) setRate(id core.NodeID, rate float64) {
	n := s.nodes[id]
	n.clockAt, n.rateFrom, n.rate = s.clock(n), s.now, rate
}

func (s *sim) crash(id core.NodeID) {
	n := s.nodes[id]
	simTrace("t=%v n%d crash base=%d last=%d", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, n.base.Position.Index, n.last())
	n.alive = false
	n.life++
	n.writes = nil // what was written and not synced is lost
}

func (s *sim) push(e *event) {
	s.ord++
	e.ord = s.ord
	heap.Push(&s.q, e)
}

func (s *sim) fail(format string, args ...any) {
	if s.err == nil {
		s.err = fmt.Errorf("t=%v: "+format, append([]any{s.now.Sub(time.Unix(1_700_000_000, 0))}, args...)...)
	}
}

func (s *sim) deliver(m core.MessageValue) {
	if s.cut[[2]core.NodeID{m.From, m.To}] || s.rng.Float64() < s.opt.drop {
		return
	}
	m.Entries = slices.Clone(m.Entries)
	d := rtt(m.From, m.To)/2 + time.Duration(s.rng.Int64N(int64(2*time.Millisecond)))
	s.push(&event{at: s.now.Add(d), kind: evDeliver, node: m.To, msg: m})
	if s.rng.Float64() < s.opt.dup {
		s.push(&event{at: s.now.Add(d + time.Millisecond), kind: evDeliver, node: m.To, msg: m})
	}
}

func (s *sim) process(n *simNode) {
	for n.alive && n.r.HasReady() {
		rd := n.r.Ready()
		for _, m := range rd.Messages {
			s.deliver(m)
		}
		if rd.NeedsWrite() {
			d := s.opt.diskMin + time.Duration(s.rng.Int64N(int64(s.opt.diskMax-s.opt.diskMin)+1))
			at := n.lastDone
			if at.Before(s.now) {
				at = s.now
			}
			at = at.Add(d)
			n.lastDone = at
			n.writes = append(n.writes, simWrite{rd: rd, doneAt: at})
			s.push(&event{at: at, kind: evStorage, node: n.id, life: n.life})
		}
		for _, f := range rd.Fetch {
			// The store read, as the driver's reader goroutine does it: the
			// durable entries, from the disk model.
			var out []core.EntryValue
			compacted := f.Lo <= n.base.Position.Index
			for i := f.Lo; !compacted && i < f.Hi && i <= n.last(); i++ {
				e, _ := n.entry(i)
				out = append(out, e)
				if len(out) >= 64 {
					break
				}
			}
			s.fetches++
			n.r.Fetched(f.To, f.Lo, out, compacted)
		}
		if s.kv != nil {
			for _, rs := range rd.ReadStates {
				s.kv.readState(s, n, rs)
			}
		}
		for _, m := range rd.Forwarded {
			s.forwarded[[2]uint64{uint64(n.id), m.Context}] = m
		}
		for _, to := range rd.SendSnapshot {
			s.sendSnapshot(n, to)
		}
		for i := range rd.Apply {
			e := &rd.Apply[i]
			prev := n.appliedFrom
			if len(n.applied) > 0 {
				prev = n.applied[len(n.applied)-1].Position.Index
			}
			if prev+1 != e.Position.Index {
				s.fail("node %d applied %d after %d", n.id, e.Position.Index, prev)
			}
			n.applied = append(n.applied, *e)
			if s.kv != nil {
				s.kv.applied(s, n, e)
			}
			k := keyOf(e)
			if prev, ok := s.applied[e.Position.Index]; ok && prev != k {
				s.fail("agreement: node %d applied %v at %d, another node applied %v", n.id, k, e.Position.Index, prev)
			}
			s.applied[e.Position.Index] = k
		}
	}
	s.cutSnapshot(n, false)
	s.checkNode(n)
}

func (s *sim) checkNode(n *simNode) {
	if !n.alive {
		return
	}
	st := n.r.Status()
	if st.Role == core.RoleLeader {
		if l, ok := s.leaders[st.Term]; ok && l != n.id {
			s.fail("two leaders in term %d: %d and %d", st.Term, l, n.id)
		}
		s.leaders[st.Term] = n.id
	}
	for i := n.checked + 1; i <= st.Commit; i++ {
		e, ok := n.r.Entry(i)
		if !ok {
			continue
		}
		k := keyOf(&e)
		if prev, ok := s.commits[i]; ok && prev != k {
			s.fail("committed history: node %d committed %v at %d, another committed %v", n.id, k, i, prev)
		}
		s.commits[i] = k
	}
	n.checked = max(n.checked, st.Commit)
}

// simTrace logs the disk model's steps while a failing seed is studied.
var simTrace func(string, ...any) = func(string, ...any) {}

func (s *sim) storageDone(n *simNode) {
	w := n.writes[0]
	n.writes = n.writes[1:]
	rd := w.rd
	if len(rd.Entries) > 0 || rd.TruncateFrom != 0 {
		var lo, hi uint64
		if len(rd.Entries) > 0 {
			lo, hi = rd.Entries[0].Position.Index, rd.Entries[len(rd.Entries)-1].Position.Index
		}
		simTrace("t=%v n%d write trunc=%d ents=%d..%d base=%d last=%d", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, rd.TruncateFrom, lo, hi, n.base.Position.Index, n.last())
	}
	if rd.HardState != nil {
		hs := *rd.HardState
		if hs.Term < n.maxTerm {
			s.fail("term went back on disk on node %d: %d < %d", n.id, hs.Term, n.maxTerm)
		}
		n.maxTerm = hs.Term
		if hs.Vote != 0 {
			k := [2]uint64{uint64(n.id), hs.Term}
			if v, ok := s.votes[k]; ok && v != hs.Vote {
				s.fail("vote uniqueness: node %d voted %d and %d in term %d", n.id, v, hs.Vote, hs.Term)
			}
			s.votes[k] = hs.Vote
		}
		n.hs.Term, n.hs.Vote = hs.Term, hs.Vote
	}
	if tf := rd.TruncateFrom; tf != 0 && tf <= n.last() {
		if tf <= n.base.Position.Index {
			n.ents = nil
		} else {
			n.ents = n.ents[:tf-n.base.Position.Index-1]
		}
	}
	for _, e := range rd.Entries {
		if e.Position.Index <= n.base.Position.Index {
			continue // an install moved the base past a write in flight
		}
		if e.Position.Index != n.last()+1 {
			s.fail("node %d disk gap: append %d after %d", n.id, e.Position.Index, n.last())
			return
		}
		n.ents = append(n.ents, e)
	}
	if rd.Commit != 0 {
		// A persisted commit never passes the last synced index (R7-13).
		n.hs.Commit = min(rd.Commit, n.last())
	}
	var idx, term uint64
	if k := len(rd.Entries); k > 0 {
		idx, term = rd.Entries[k-1].Position.Index, rd.Entries[k-1].Position.Term
	}
	n.r.StorageDone(rd.Seq, idx, term)
	for _, m := range rd.AfterSync {
		s.deliver(m)
	}
	s.process(n)
}

func (s *sim) leader() *simNode {
	var best *simNode
	for _, id := range s.ids {
		n := s.nodes[id]
		if n.alive && n.r.Status().Role == core.RoleLeader && (best == nil || n.r.Status().Term > best.r.Status().Term) {
			best = n
		}
	}
	return best
}

func (s *sim) propose(n *simNode, data []byte) bool {
	e := core.EntryValue{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	_, code := n.r.Propose(s.clock(n), []core.EntryValue{e})
	s.process(n)
	return code == 0
}

// run advances the simulation to until.
func (s *sim) run(until time.Time, load bool) {
	nextLoad := s.now
	for s.err == nil && len(s.q) > 0 {
		e := s.q[0]
		if e.at.After(until) {
			break
		}
		heap.Pop(&s.q)
		s.now = e.at
		if load && !s.now.Before(nextLoad) {
			nextLoad = s.now.Add(20 * time.Millisecond)
			if l := s.leader(); l != nil {
				s.proposed++
				s.propose(l, fmt.Appendf(nil, "cmd-%d", s.proposed))
			}
		}
		if s.opt.crashRate > 0 && !s.now.Before(s.nextCrash) {
			s.nextCrash = s.now.Add(100 * time.Millisecond)
			if s.rng.Float64() >= s.opt.crashRate/10 {
				goto dispatch
			}
			id := s.ids[s.rng.IntN(len(s.ids))]
			if s.nodes[id].alive {
				s.crash(id)
				down := time.Duration(100+s.rng.IntN(900)) * time.Millisecond
				s.push(&event{at: s.now.Add(down), kind: evRestart, node: id})
			}
		}
	dispatch:
		n := s.nodes[e.node]
		switch e.kind {
		case evRestart:
			if !n.alive {
				// A lost snapshot is a rare fault: one node at a time, so a
				// majority keeps a table and can lead (R7-18).
				if n.base.Position.Index > 0 && s.noTables() == 0 && s.rng.Float64() < s.opt.snapLoss {
					n.noTable = true
				}
				s.start(n)
			}
		case evInstall:
			s.install(n, e)
		case evSnapDone:
			if n.alive {
				n.r.SnapshotDone(e.msg.From, e.ok)
				s.process(n)
			}
		case evTick:
			if !n.alive || e.life != n.life {
				continue
			}
			n.r.Tick(s.clock(n))
			s.process(n)
			s.push(&event{at: s.now.Add(10 * time.Millisecond), kind: evTick, node: n.id, life: n.life})
		case evDeliver:
			if !n.alive {
				continue
			}
			n.r.Step(s.clock(n), e.msg)
			s.process(n)
		case evStorage:
			if !n.alive || e.life != n.life || len(n.writes) == 0 {
				continue
			}
			s.storageDone(n)
		}
	}
	if s.now.Before(until) {
		s.now = until
	}
}

func (s *sim) partition(groups ...[]core.NodeID) {
	s.cut = map[[2]core.NodeID]bool{}
	side := map[core.NodeID]int{}
	for _, id := range s.ids {
		side[id] = len(groups)
	}
	for g, ids := range groups {
		for _, id := range ids {
			side[id] = g
		}
	}
	for _, a := range s.ids {
		for _, b := range s.ids {
			if side[a] != side[b] {
				s.cut[[2]core.NodeID{a, b}] = true
			}
		}
	}
}

func (s *sim) heal() { s.cut = map[[2]core.NodeID]bool{} }

func (s *sim) maxCommit() uint64 {
	var c uint64
	for _, id := range s.ids {
		if n := s.nodes[id]; n.alive {
			c = max(c, n.r.Status().Commit)
		}
	}
	return c
}

func (s *sim) after(d time.Duration) time.Time { return s.now.Add(d) }

// last is the index of the node's last durable entry.
func (n *simNode) last() uint64 { return n.base.Position.Index + uint64(len(n.ents)) }

// entry returns the durable entry at i, if the disk holds it.
func (n *simNode) entry(i uint64) (core.EntryValue, bool) {
	b := n.base.Position.Index
	if i <= b || i > n.last() {
		return core.EntryValue{}, false
	}
	return n.ents[i-b-1], true
}

// cutSnapshot snapshots what the node applied, as the driver does every
// snapEvery entries, and compacts the log under it.
func (s *sim) cutSnapshot(n *simNode, force bool) {
	if s.opt.snapEvery == 0 || n.noTable || len(n.applied) == 0 {
		return
	}
	c := n.applied[len(n.applied)-1].Position.Index
	if c <= n.base.Position.Index || (!force && c < n.base.Position.Index+s.opt.snapEvery) {
		return
	}
	e, ok := n.entry(c)
	if !ok {
		return
	}
	simTrace("t=%v n%d cut %d base=%d last=%d", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, c, n.base.Position.Index, n.last())
	n.ents = slices.Clone(n.ents[c-n.base.Position.Index:])
	n.base = core.SnapshotMetaValue{
		ID: fmt.Sprintf("s-%d-%d", n.id, c), Position: e.Position, ChainTip: e.ChainSum,
		Membership: n.r.ConfAt(c), Timing: s.timing,
	}
	n.r.Compact(c)
}

// sendSnapshot ships the leader's snapshot to a peer.
func (s *sim) sendSnapshot(n *simNode, to core.NodeID) {
	simTrace("t=%v n%d send snapshot to %d base=%d", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, to, n.base.Position.Index)
	if n.base.Position.Index == 0 || n.noTable {
		// A peer asks for a snapshot this leader never cut: cut one now,
		// and the next probe sends it, as the driver does.
		s.cutSnapshot(n, true)
		n.r.SnapshotDone(to, false)
		return
	}
	m := core.MessageValue{From: n.id, To: to, Term: n.r.Status().Term}
	if s.cut[[2]core.NodeID{n.id, to}] || s.rng.Float64() < s.opt.drop {
		s.push(&event{at: s.now.Add(rtt(n.id, to)), kind: evSnapDone, node: n.id, life: n.life, msg: core.MessageValue{From: to}})
		return
	}
	s.push(&event{at: s.now.Add(rtt(n.id, to)), kind: evInstall, node: to, msg: m, meta: n.base})
}

// install is the receiver's side: the offer checked on the loop, the
// snapshot made durable, the log moved onto it, then the answer.
func (s *sim) install(n *simNode, e *event) {
	ok := false
	defer func() {
		s.push(&event{at: s.now.Add(rtt(n.id, e.msg.From) / 2), kind: evSnapDone, node: e.msg.From, msg: core.MessageValue{From: n.id}, ok: ok})
	}()
	if !n.alive {
		return
	}
	// The install waits for the writes queued before it, as the storage
	// actor serves them in order.
	for n.alive && len(n.writes) > 0 {
		s.storageDone(n)
	}
	meta := e.meta
	code := n.r.CheckOffer(e.msg.From, core.InstallOfferValue{Term: e.msg.Term, SnapshotID: meta.ID, Meta: meta}, n.r.NoTable())
	simTrace("t=%v n%d offer %d from %d: %v", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, meta.Position.Index, e.msg.From, code)
	if code == core.CodeAdminPreconditionFailed {
		ok = true // held: the receiver already covers it
		return
	}
	if code == core.CodeCompacted {
		// The receiver's log starts past it: the leader cuts a newer one,
		// as the driver does.
		if l := s.nodes[e.msg.From]; l.alive {
			s.cutSnapshot(l, true)
		}
	}
	if code != 0 {
		return
	}
	idx := meta.Position.Index
	simTrace("t=%v n%d install %d base=%d last=%d noTable=%v", s.now.Sub(time.Unix(1_700_000_000, 0)), n.id, idx, n.base.Position.Index, n.last(), n.noTable)
	if idx == n.base.Position.Index && meta.Position.Term == n.base.Position.Term {
		// The boundary is the base itself: the log is kept whole.
	} else if x, has := n.entry(idx); has && x.Position.Term == meta.Position.Term {
		n.ents = slices.Clone(n.ents[idx-n.base.Position.Index:])
	} else {
		n.ents = nil
	}
	n.base = meta
	n.noTable = false
	n.applied, n.appliedFrom = nil, idx
	n.hs.Commit = max(n.hs.Commit, idx)
	n.r.InstallSnapshot(meta)
	s.process(n)
	ok = true
}

func (s *sim) noTables() int {
	k := 0
	for _, id := range s.ids {
		if s.nodes[id].noTable {
			k++
		}
	}
	return k
}
