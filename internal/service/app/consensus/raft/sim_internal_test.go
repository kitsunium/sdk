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
)

type event struct {
	at   time.Time
	ord  int
	kind evKind
	node core.NodeID
	life int
	msg  core.Message
}

type eventQueue []*event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if !q[i].at.Equal(q[j].at) {
		return q[i].at.Before(q[j].at)
	}
	return q[i].ord < q[j].ord
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

type simWrite struct {
	rd     Ready
	doneAt time.Time
}

type simNode struct {
	id       core.NodeID
	r        *Raft
	alive    bool
	life     int
	hs       core.HardState
	ents     []core.Entry
	writes   []simWrite
	lastDone time.Time
	applied  []core.Entry
	checked  uint64
	maxTerm  uint64
}

type entryKey struct {
	term uint64
	kind core.EntryKind
	sum  uint32
}

func keyOf(e *core.Entry) entryKey {
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
	timing    core.TimingContract
	nextCrash time.Time
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
		opt:     opt,
		rng:     rand.New(rand.NewPCG(opt.seed, 0x5eed)),
		now:     time.Unix(1_700_000_000, 0),
		nodes:   map[core.NodeID]*simNode{},
		cut:     map[[2]core.NodeID]bool{},
		leaders: map[uint64]core.NodeID{},
		votes:   map[[2]uint64]core.NodeID{},
		applied: map[uint64]entryKey{},
		commits: map[uint64]entryKey{},
		timing: core.TimingContract{Generation: 1, HeartbeatInterval: 100 * time.Millisecond,
			ElectionTimeoutMin: time.Second, ElectionTimeoutMax: 2 * time.Second, MaxClockDrift: 0.01},
	}
	var conf core.Membership
	for i := 1; i <= opt.nodes; i++ {
		id := core.NodeID(i)
		s.ids = append(s.ids, id)
		conf.Members = append(conf.Members, core.Member{ID: id, Voter: true, SPKI: [32]byte{byte(i)}})
	}
	// The founding configuration is entry 1 on every founder, committed.
	data := wire.AppendAdmin(nil, &wire.Admin{Change: wire.ChangeFounding, Membership: conf})
	found := core.Entry{Position: core.Position{Term: 1, Index: 1}, Kind: core.EntryMembership, BodySum: wire.BodySum(data), Data: data}
	found.ChainSum = wire.ChainSum(0, &found)
	for _, id := range s.ids {
		n := &simNode{id: id, hs: core.HardState{Term: 1, Commit: 1}, ents: []core.Entry{found}}
		s.nodes[id] = n
		s.start(n)
	}
	return s
}

func (s *sim) cfg(id core.NodeID) Config {
	return Config{ID: id, Group: 1, Seed: s.opt.seed*31 + uint64(id), Timing: s.timing,
		MaxInflight: 16, MaxInflightBytes: 1 << 20, MaxBatchBytes: 64 << 10, MinVoters: 1,
		PromoteLag: 64, Lease: s.opt.lease, CanVote: true, MaxChainRefusals: 3, mut: s.opt.mut}
}

func (s *sim) start(n *simNode) {
	n.alive = true
	n.life++
	n.writes = nil
	n.lastDone = s.now
	n.applied = nil
	n.checked = 0
	n.r = New(s.cfg(n.id), Start{HardState: n.hs, Entries: slices.Clone(n.ents), Now: s.now})
	s.push(&event{at: s.now.Add(time.Duration(s.rng.Int64N(int64(10 * time.Millisecond)))), kind: evTick, node: n.id, life: n.life})
}

func (s *sim) crash(id core.NodeID) {
	n := s.nodes[id]
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

func (s *sim) deliver(m core.Message) {
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
		for i := range rd.Apply {
			e := &rd.Apply[i]
			if len(n.applied) > 0 && n.applied[len(n.applied)-1].Position.Index+1 != e.Position.Index {
				s.fail("node %d applied %d after %d", n.id, e.Position.Index, n.applied[len(n.applied)-1].Position.Index)
			}
			n.applied = append(n.applied, *e)
			k := keyOf(e)
			if prev, ok := s.applied[e.Position.Index]; ok && prev != k {
				s.fail("agreement: node %d applied %v at %d, another node applied %v", n.id, k, e.Position.Index, prev)
			}
			s.applied[e.Position.Index] = k
		}
	}
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

func (s *sim) storageDone(n *simNode) {
	w := n.writes[0]
	n.writes = n.writes[1:]
	rd := w.rd
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
	if rd.TruncateFrom != 0 && rd.TruncateFrom <= uint64(len(n.ents)) {
		n.ents = n.ents[:rd.TruncateFrom-1]
	}
	for _, e := range rd.Entries {
		if e.Position.Index != uint64(len(n.ents))+1 {
			s.fail("node %d disk gap: append %d after %d", n.id, e.Position.Index, len(n.ents))
			return
		}
		n.ents = append(n.ents, e)
	}
	if rd.Commit != 0 {
		// A persisted commit never passes the last synced index (R7-13).
		n.hs.Commit = min(rd.Commit, uint64(len(n.ents)))
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
	e := core.Entry{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	_, code := n.r.Propose(s.now, []core.Entry{e})
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
				s.start(n)
			}
		case evTick:
			if !n.alive || e.life != n.life {
				continue
			}
			n.r.Tick(s.now)
			s.process(n)
			s.push(&event{at: s.now.Add(10 * time.Millisecond), kind: evTick, node: n.id, life: n.life})
		case evDeliver:
			if !n.alive {
				continue
			}
			n.r.Step(s.now, e.msg)
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
