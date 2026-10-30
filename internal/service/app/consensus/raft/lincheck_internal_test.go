package raft

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The linearizability checker of D15: Wing & Gong's search with Lowe's
// memoization, partitioned per key, over a register model. It is written in
// the test tree, stdlib only. It is validated on hand-labelled histories
// first, and a run with lease reads and clock skew beyond MaxClockDrift must
// produce a history it REJECTS: a checker never seen failing is not one.

// kvOp is one client operation: a write of val, or a read that returned
// val. ret is math.MaxInt64 for an operation whose outcome is unknown; it
// may have taken effect at any instant after its call, or never.
type kvOp struct {
	key   string
	write bool
	val   string
	call  int64
	ret   int64
}

// linearizable reports whether the history has a linearization; on
// failure it names the key whose history has none.
func linearizable(ops []kvOp) (bool, string) {
	byKey := map[string][]kvOp{}
	for _, o := range ops {
		byKey[o.key] = append(byKey[o.key], o)
	}
	keys := slices.Sorted(maps.Keys(byKey))
	for _, k := range keys {
		if !linearizableKey(byKey[k]) {
			return false, k
		}
	}
	return true, ""
}

// linearizableKey searches one key's history. Unknown-outcome writes may be
// placed anywhere after their call, or left out; reads with no return were
// never recorded.
func linearizableKey(ops []kvOp) bool {
	slices.SortFunc(ops, func(a, b kvOp) int {
		switch {
		case a.call < b.call:
			return -1
		case a.call > b.call:
			return 1
		}
		return 0
	})
	n := len(ops)
	done := make([]bool, n)
	required := 0
	for _, o := range ops {
		if o.ret != math.MaxInt64 {
			required++
		}
	}
	memo := map[string]bool{}
	var search func(state string, left int) bool
	search = func(state string, left int) bool {
		if left == 0 {
			return true
		}
		var key bytes.Buffer
		for _, d := range done {
			if d {
				key.WriteByte('1')
			} else {
				key.WriteByte('0')
			}
		}
		key.WriteString("|" + state)
		k := key.String()
		if v, ok := memo[k]; ok {
			return v
		}
		// The earliest return among the operations still to place: nothing
		// called after it can come first.
		minRet := int64(math.MaxInt64)
		for i, o := range ops {
			if !done[i] && o.ret < minRet {
				minRet = o.ret
			}
		}
		for i, o := range ops {
			if done[i] || o.call > minRet {
				continue
			}
			next := state
			if o.write {
				next = o.val
			} else if o.val != state {
				continue
			}
			done[i] = true
			l := left
			if o.ret != math.MaxInt64 {
				l--
			}
			ok := search(next, l)
			done[i] = false
			if ok {
				memo[k] = true
				return true
			}
		}
		memo[k] = false
		return false
	}
	return search("", required)
}

func TestLinearizabilityCheckerOnKnownHistories(t *testing.T) {
	const unknown int64 = math.MaxInt64
	cases := []struct {
		name string
		ops  []kvOp
		want bool
	}{
		{"sequential write then read", []kvOp{{key: "x", write: true, val: "1", call: 0, ret: 1}, {key: "x", val: "1", call: 2, ret: 3}}, true},
		{"stale read after a completed write", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 3},
			{key: "x", val: "1", call: 4, ret: 5},
		}, false},
		{"read concurrent with a write sees either", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "x", write: true, val: "2", call: 2, ret: 6},
			{key: "x", val: "1", call: 3, ret: 4},
		}, true},
		{"read of a value never written", []kvOp{{key: "x", val: "9", call: 0, ret: 1}}, false},
		{"read of the initial value", []kvOp{{key: "x", val: "", call: 0, ret: 1}}, true},
		{"lost update: two reads disagree on the order", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 10},
			{key: "x", write: true, val: "2", call: 0, ret: 10},
			{key: "x", val: "1", call: 11, ret: 12},
			{key: "x", val: "2", call: 13, ret: 14},
		}, false},
		{"unknown write may take effect", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: unknown},
			{key: "x", val: "1", call: 5, ret: 6},
		}, true},
		{"unknown write may not take effect", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: unknown},
			{key: "x", val: "", call: 5, ret: 6},
		}, true},
		{"unknown write cannot flip back", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: unknown},
			{key: "x", val: "1", call: 5, ret: 6},
			{key: "x", val: "", call: 7, ret: 8},
		}, false},
		{"keys are independent", []kvOp{
			{key: "x", write: true, val: "1", call: 0, ret: 1},
			{key: "y", val: "", call: 2, ret: 3},
		}, true},
	}
	for _, c := range cases {
		if got, _ := linearizable(c.ops); got != c.want {
			t.Errorf("%s: linearizable %v, want %v", c.name, got, c.want)
		}
	}
}

// ---- clients over the simulator ----

type pendingWrite struct {
	op   int
	node core.NodeID
	life int
	pos  core.PositionValue
}

type pendingRead struct {
	op    int
	key   string
	index uint64
	ok    bool
}

// kvWorld runs clients against the simulated nodes: writes proposed at the
// node that leads, reads through ReadIndex, or under the lease, then from
// the node's own key-value state once it applied the read's index.
type kvWorld struct {
	ops    []kvOp
	state  map[core.NodeID]map[string]string
	writes []pendingWrite
	reads  map[core.NodeID]map[uint64]*pendingRead
	ctx    uint64
	noLeas bool
	// fwds are the writes forwarded and not answered yet, by follower and
	// submission id; fctx numbers the submissions.
	fwds map[[2]uint64]pendingFwd
	fctx uint64
	// firstApplied is when a command was first applied on any node, by its
	// payload: every write's payload is unique.
	firstApplied map[string]int64
}

// pendingFwd is a forwarded write waiting for the leader's answer, from the
// follower's life that sent it.
type pendingFwd struct {
	op   int
	life int
}

func newKVWorld() *kvWorld {
	return &kvWorld{state: map[core.NodeID]map[string]string{}, reads: map[core.NodeID]map[uint64]*pendingRead{}, fwds: map[[2]uint64]pendingFwd{}, firstApplied: map[string]int64{}}
}

func (w *kvWorld) restart(n *simNode) {
	w.state[n.id] = map[string]string{}
	// Reads in flight on a node that crashed never returned: dropped.
	delete(w.reads, n.id)
	for i := range w.writes {
		if w.writes[i].node == n.id {
			w.writes[i].node = 0 // outcome unknown from now on
		}
	}
}

func (w *kvWorld) applied(s *sim, n *simNode, e *core.EntryValue) {
	st := w.state[n.id]
	if st == nil {
		st = map[string]string{}
		w.state[n.id] = st
	}
	if e.Kind == core.EntryCommand {
		k, v, _ := strings.Cut(string(e.Data), "=")
		st[k] = v
		if _, ok := w.firstApplied[string(e.Data)]; !ok {
			w.firstApplied[string(e.Data)] = s.now.UnixNano()
		}
	}
	for i := range w.writes {
		pw := &w.writes[i]
		if pw.node != n.id || pw.life != n.life || pw.pos.Index != e.Position.Index {
			continue
		}
		if pw.pos.Term == e.Position.Term {
			w.ops[pw.op].ret = s.now.UnixNano()
		} else {
			// Another entry committed at its position: certainly not
			// committed, so it never took effect.
			w.ops[pw.op].ret = -1
		}
		pw.node = 0
	}
	w.finishReads(s, n)
}

func (w *kvWorld) readState(s *sim, n *simNode, rs ReadState) {
	r := w.reads[n.id][rs.Context]
	if r == nil {
		return
	}
	if rs.Code != 0 {
		delete(w.reads[n.id], rs.Context)
		return
	}
	r.index, r.ok = rs.Index, true
	w.finishReads(s, n)
}

func (w *kvWorld) finishReads(s *sim, n *simNode) {
	var appliedIdx uint64
	if k := len(n.applied); k > 0 {
		appliedIdx = n.applied[k-1].Position.Index
	}
	for ctx, r := range w.reads[n.id] {
		if r.ok && appliedIdx >= r.index {
			w.ops[r.op].val = w.state[n.id][r.key]
			w.ops[r.op].ret = s.now.UnixNano()
			delete(w.reads[n.id], ctx)
		}
	}
}

func (w *kvWorld) write(s *sim, n *simNode, key, val string) {
	data := []byte(key + "=" + val)
	e := core.EntryValue{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	pos, code := n.r.Propose(s.clock(n), []core.EntryValue{e})
	if code != 0 {
		return // refused before append: not part of the history
	}
	w.ops = append(w.ops, kvOp{key: key, write: true, val: val, call: s.now.UnixNano(), ret: math.MaxInt64})
	w.writes = append(w.writes, pendingWrite{op: len(w.ops) - 1, node: n.id, life: n.life, pos: pos[0]})
	s.process(n)
}

// writeForwarded submits a write on a follower, as the driver does: one
// MsgForward to the leader it knows, under a submission id that never
// repeats. The network may deliver it twice.
func (w *kvWorld) writeForwarded(s *sim, f *simNode, key, val string) {
	st := f.r.Status()
	if st.Lead == 0 || st.Lead == f.id {
		return // refused before anything was sent: not part of the history
	}
	w.fctx++
	data := []byte(key + "=" + val)
	m := core.MessageValue{
		Version: 1, Group: 1, Kind: core.MsgForward, From: f.id, To: st.Lead, Term: st.Term, Context: w.fctx,
		Entries: []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}},
	}
	w.ops = append(w.ops, kvOp{key: key, write: true, val: val, call: s.now.UnixNano(), ret: math.MaxInt64})
	w.fwds[[2]uint64{uint64(f.id), w.fctx}] = pendingFwd{op: len(w.ops) - 1, life: f.life}
	s.deliver(m)
}

// forwardAnswered takes the first answer to a forwarded write and drops any
// other, as the driver does: a refusal says nothing was appended, a position
// is waited on like a local write's. A follower that restarted since never
// learns the outcome.
func (w *kvWorld) forwardAnswered(n *simNode, m core.MessageValue) {
	k := [2]uint64{uint64(n.id), m.Context}
	pf, ok := w.fwds[k]
	if !ok {
		return
	}
	delete(w.fwds, k)
	switch {
	case pf.life != n.life:
	case m.Reject:
		w.ops[pf.op].ret = -1
	default:
		w.writes = append(w.writes, pendingWrite{op: pf.op, node: n.id, life: n.life, pos: core.PositionValue{Term: m.LogTerm, Index: m.Index}})
	}
}

func (w *kvWorld) read(s *sim, n *simNode, key string) {
	w.ctx++
	w.ops = append(w.ops, kvOp{key: key, call: s.now.UnixNano(), ret: math.MaxInt64})
	if w.reads[n.id] == nil {
		w.reads[n.id] = map[uint64]*pendingRead{}
	}
	w.reads[n.id][w.ctx] = &pendingRead{op: len(w.ops) - 1, key: key}
	n.r.ReadIndex(s.clock(n), w.ctx, w.noLeas)
	s.process(n)
}

// settleUnknownWrites gives every write of unknown outcome the outcome the
// cluster reached once it is quiet and caught up: it took effect by the
// instant a node first applied it, or never if none did. Without it, the
// writes whose forward or answer a fault lost leave the checker a search
// exponential in their number.
func (w *kvWorld) settleUnknownWrites() {
	for i := range w.ops {
		o := &w.ops[i]
		if !o.write || o.ret != math.MaxInt64 {
			continue
		}
		if at, ok := w.firstApplied[o.key+"="+o.val]; ok {
			o.ret = at
		} else {
			o.ret = -1
		}
	}
}

// history returns the operations: returned ones, and writes of unknown
// outcome; reads that never returned and writes certainly not committed
// are left out.
func (w *kvWorld) history() []kvOp {
	var out []kvOp
	for _, o := range w.ops {
		switch {
		case o.ret == -1:
		case !o.write && o.ret == math.MaxInt64:
		default:
			out = append(out, o)
		}
	}
	return out
}

// TestLinearizableUnderFaults runs clients reading through ReadIndex, on
// every node, with partitions, drops and crashes, and checks the history.
func TestLinearizableUnderFaults(t *testing.T) {
	eachSeed(t, seeds(t, 12), func(t *testing.T, seed uint64) {
		if err := linearizableRun(seed, 0); err != nil {
			t.Fatal(err)
		}
	})
}

// linearizableRun runs one seed of the client workload and checks it.
func linearizableRun(seed uint64, mut mutation) error {
	{
		s := newSim(simOptions{seed: seed, drop: 0.03, crashRate: 0.3, mut: mut})
		s.kv = newKVWorld()
		s.kv.noLeas = true
		for _, id := range s.ids {
			s.kv.restart(s.nodes[id])
		}
		keys := []string{"a", "b", "c"}
		step := 0
		for k := 0; k < 6 && s.err == nil; k++ {
			switch k % 3 {
			case 0:
				s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
			case 1:
				s.heal()
			}
			end := s.after(3 * time.Second)
			for s.now.Before(end) && s.err == nil {
				s.run(s.after(15*time.Millisecond), false)
				step++
				key := keys[step%len(keys)]
				id := s.ids[step%len(s.ids)]
				n := s.nodes[id]
				if !n.alive {
					continue
				}
				if step%2 == 0 {
					if l := s.leader(); l != nil {
						s.kv.write(s, l, key, fmt.Sprint(step))
					}
				} else {
					s.kv.read(s, n, key)
				}
			}
		}
		if s.err != nil {
			return fmt.Errorf("seed %d: %w", seed, s.err)
		}
		h := s.kv.history()
		reads := 0
		for _, o := range h {
			if !o.write {
				reads++
			}
		}
		if reads < 20 {
			return fmt.Errorf("seed %d: only %d reads returned", seed, reads)
		}
		if ok, key := linearizable(h); !ok {
			return fmt.Errorf("seed %d: history of key %q is not linearizable (%d ops)", seed, key, len(h))
		}
	}
	return nil
}

// simLinearizableSeeds is how many seeds, from 1, checkSimLinearizable
// runs. TestChecksPassUnmutated runs the same seeds, one parallel subtest
// each.
const simLinearizableSeeds = 8

func checkSimLinearizable(mut mutation) error {
	for seed := uint64(1); seed <= simLinearizableSeeds; seed++ {
		if err := linearizableRun(seed, mut); err != nil {
			return err
		}
	}
	return nil
}

// TestLeaseReadsUnderClockSkewAreCaught is the mandatory negative control:
// lease reads on a leader whose clock runs at a tenth of the others' rate,
// far beyond MaxClockDrift, cut from the majority while a new leader
// commits a write. The old leader still believes its lease and serves the
// old value: the checker must reject the history.
func TestLeaseReadsUnderClockSkewAreCaught(t *testing.T) {
	run := func(rate float64) (bool, int) {
		s := newSim(simOptions{seed: 42, lease: true})
		s.kv = newKVWorld()
		for _, id := range s.ids {
			s.kv.restart(s.nodes[id])
		}
		s.run(s.after(3*time.Second), false)
		l := s.leader()
		if l == nil {
			t.Fatal("no leader")
		}
		s.kv.write(s, l, "x", "1")
		s.run(s.after(500*time.Millisecond), false)
		// Confirm a heartbeat round so the lease holds.
		s.kv.read(s, l, "x")
		s.run(s.after(200*time.Millisecond), false)
		var rest []core.NodeID
		for _, id := range s.ids {
			if id != l.id {
				rest = append(rest, id)
			}
		}
		s.setRate(l.id, rate)
		s.partition([]core.NodeID{l.id}, rest)
		var nl *simNode
		for i := 0; i < 100 && nl == nil; i++ {
			s.run(s.after(50*time.Millisecond), false)
			for _, id := range rest {
				if n := s.nodes[id]; n.r.Status().Role == core.RoleLeader {
					nl = n
				}
			}
		}
		if nl == nil {
			t.Fatal("the majority did not elect")
		}
		s.kv.write(s, nl, "x", "2")
		s.run(s.after(300*time.Millisecond), false)
		// The client co-located with the old leader reads under its lease.
		stale := 0
		for range 5 {
			s.kv.read(s, l, "x")
			s.run(s.after(10*time.Millisecond), false)
		}
		for _, o := range s.kv.history() {
			if !o.write && o.val == "1" && o.ret != math.MaxInt64 {
				stale++
			}
		}
		ok, _ := linearizable(s.kv.history())
		return ok, stale
	}
	ok, stale := run(0.1)
	t.Logf("skewed clock: linearizable %v, %d old-value reads", ok, stale)
	if ok || stale < 2 {
		t.Fatalf("clock skew beyond the drift: linearizable %v with %d old-value reads; the checker must reject", ok, stale)
	}
	if ok, _ := run(1); !ok {
		t.Fatal("exact clocks: the lease history must be linearizable")
	}
}

// checkDeposedLeaderRead: the leader transfers leadership, is cut off right
// after its TimeoutNow left, and keeps believing it leads while the target
// commits a write. A read it serves without a confirmation round is stale;
// with the round it is never answered. The checker must see the difference.
func checkDeposedLeaderRead(mut mutation) error {
	s := newSim(simOptions{seed: 7, mut: mut})
	s.kv = newKVWorld()
	s.kv.noLeas = true
	for _, id := range s.ids {
		s.kv.restart(s.nodes[id])
	}
	s.run(s.after(3*time.Second), false)
	l := s.leader()
	if l == nil {
		return fmt.Errorf("no leader")
	}
	s.kv.write(s, l, "x", "1")
	s.run(s.after(300*time.Millisecond), false)
	var to core.NodeID
	var rest []core.NodeID
	for _, id := range s.ids {
		if id != l.id {
			rest = append(rest, id)
			to = id
		}
	}
	if code := l.r.TransferLeadership(s.clock(l), to); code != 0 {
		return fmt.Errorf("transfer: %v", code)
	}
	s.process(l)
	// The TimeoutNow is already on its way; nothing after it reaches l.
	s.partition([]core.NodeID{l.id}, rest)
	s.run(s.after(200*time.Millisecond), false)
	nl := s.nodes[to]
	if nl.r.Status().Role != core.RoleLeader {
		return fmt.Errorf("setup: the target did not win")
	}
	s.kv.write(s, nl, "x", "2")
	s.run(s.after(100*time.Millisecond), false)
	for range 3 {
		s.kv.read(s, l, "x")
		s.run(s.after(5*time.Millisecond), false)
	}
	if ok, key := linearizable(s.kv.history()); !ok {
		return fmt.Errorf("a deposed leader served a stale read of %q", key)
	}
	return nil
}

// TestForwardedWritesUnderDuplicationAreAppliedOnce writes through followers'
// forwards on a network that delivers a message twice in four, with drops,
// partitions and crashes: every message kind is duplicated — appends and
// their answers, votes, heartbeats, reads, forwards and their answers — and
// the history stays linearizable, with no command applied twice (#401).
func TestForwardedWritesUnderDuplicationAreAppliedOnce(t *testing.T) {
	eachSeed(t, seeds(t, 8), func(t *testing.T, seed uint64) {
		if err := forwardedRun(seed, 0); err != nil {
			t.Fatal(err)
		}
	})
}

// TestForwardedDuplicatesAreCaught is the scenario's negative control: with
// the leader proposing every delivery of a forward, some command is applied
// twice, and the run says so.
func TestForwardedDuplicatesAreCaught(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		if err := forwardedRun(seed, mutForwardNoDedupe); err != nil {
			t.Logf("caught: %v", err)
			return
		}
	}
	t.Fatal("no seed applied a duplicated forward twice with the deduplication off")
}

// forwardedRun runs one seed of the forwarded workload, then checks that no
// command was applied twice and that the history is linearizable.
func forwardedRun(seed uint64, mut mutation) error {
	s := newSim(simOptions{seed: seed, drop: 0.03, dup: 0.25, crashRate: 0.3, mut: mut})
	s.kv = newKVWorld()
	s.kv.noLeas = true
	for _, id := range s.ids {
		s.kv.restart(s.nodes[id])
	}
	keys := []string{"a", "b", "c"}
	step := 0
	for k := 0; k < 6 && s.err == nil; k++ {
		switch k % 3 {
		case 0:
			s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
		case 1:
			s.heal()
		}
		end := s.after(3 * time.Second)
		for s.now.Before(end) && s.err == nil {
			s.run(s.after(15*time.Millisecond), false)
			step++
			n := s.nodes[s.ids[step%len(s.ids)]]
			if !n.alive {
				continue
			}
			key := keys[step%len(keys)]
			switch {
			case step%2 == 1:
				s.kv.read(s, n, key)
			case n.r.Status().Role == core.RoleLeader:
				s.kv.write(s, n, key, fmt.Sprint(step))
			default:
				s.kv.writeForwarded(s, n, key, fmt.Sprint(step))
			}
		}
	}
	// Quiet and whole again, every node catches up: what is committed is
	// applied everywhere, and what is not never will be.
	s.heal()
	s.opt.crashRate, s.opt.drop, s.opt.dup = 0, 0, 0
	for _, id := range s.ids {
		if !s.nodes[id].alive {
			s.start(s.nodes[id])
		}
	}
	s.run(s.after(5*time.Second), true)
	if s.err != nil {
		return fmt.Errorf("seed %d: %w", seed, s.err)
	}
	s.kv.settleUnknownWrites()
	if s.kv.fctx < 50 {
		return fmt.Errorf("seed %d: only %d writes forwarded", seed, s.kv.fctx)
	}
	seen := map[uint32]uint64{}
	for _, i := range slices.Sorted(maps.Keys(s.applied)) {
		k := s.applied[i]
		if k.kind != core.EntryCommand {
			continue
		}
		if j, ok := seen[k.sum]; ok {
			return fmt.Errorf("seed %d: one command applied at %d and at %d", seed, j, i)
		}
		seen[k.sum] = i
	}
	if ok, key := linearizable(s.kv.history()); !ok {
		return fmt.Errorf("seed %d: history of key %q is not linearizable", seed, key)
	}
	return nil
}
