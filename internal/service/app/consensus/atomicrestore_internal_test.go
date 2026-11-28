package consensus

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// applyOrder records the rank of every test command a machine applies,
// "k<rank>=…", and counts each one that does not follow the rank the
// machine's state ends on: a command applied over a state it does not
// continue. Ranks, not log indexes, are compared, so the entries the
// engine applies without the machine (a leader's no-op, a membership
// change) open no false gap, and a missing command still shows.
type applyOrder struct {
	// last is the rank the state ends on, valid once known is set.
	last     atomic.Int64
	known    atomic.Bool
	disorder atomic.Int64
}

// rank returns the rank of the test command data, false for any other.
func rank(data []byte) (int64, bool) {
	k, _, ok := bytes.Cut(data, []byte("="))
	//: a test command is "k<rank>=…".
	if !ok || len(k) < 2 || k[0] != 'k' {
		return 0, false
	}
	r, err := strconv.ParseInt(string(k[1:]), 10, 64)
	return r, err == nil
}

// applied records the Apply of e.
func (o *applyOrder) applied(e coreconsensus.EntryValue) {
	r, ok := rank(e.Data)
	//: only the test's commands have a rank.
	if !ok {
		return
	}
	//: a command past a known state must follow it.
	if o.known.Load() && r != o.last.Load()+1 {
		o.disorder.Add(1)
	}
	o.last.Store(r)
	o.known.Store(true)
}

// restored records the state a whole restore left in s: the next command
// follows the highest rank it holds.
func (o *applyOrder) restored(s *kv) {
	s.mu.Lock()
	defer s.mu.Unlock()
	top := int64(-1)
	//: the highest rank of the state.
	for k := range s.m {
		//: a key of the test's commands.
		if r, ok := rank([]byte(k + "=")); ok && r > top {
			top = r
		}
	}
	o.last.Store(top)
	o.known.Store(true)
}

// orderedRestore is a partialRestore whose applies are checked in order
// against what its whole restores and its applies left. It reads aside,
// so its failed Restore leaves its state as it was, but it does not say so.
type orderedRestore struct {
	*partialRestore
	ord applyOrder
}

// Apply implements StateMachine, checking the entry's order.
func (o *orderedRestore) Apply(e coreconsensus.EntryValue) any {
	o.ord.applied(e)
	return o.partialRestore.Apply(e)
}

// Restore implements StateMachine; a whole restore resets the order.
func (o *orderedRestore) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	err := o.partialRestore.Restore(r, meta)
	//: a whole restore is where the next entry follows.
	if err == nil {
		o.ord.restored(o.kv)
	}
	return err
}

// atomicRestore is an orderedRestore that declares its Restore atomic.
type atomicRestore struct{ *orderedRestore }

// RestoresAtomically implements AtomicRestorer.
func (atomicRestore) RestoresAtomically() {}

// orderedDurable is a partialDurable whose applies are checked in order.
type orderedDurable struct {
	*partialDurable
	ord applyOrder
	// breaks moves the position to the snapshot's on a refused restore:
	// an AtomicRestorer that breaks its contract where the engine sees it.
	breaks bool
}

// Apply implements StateMachine, checking the entry's order.
func (o *orderedDurable) Apply(e coreconsensus.EntryValue) any {
	o.ord.applied(e)
	return o.partialDurable.Apply(e)
}

// Restore implements StateMachine; a whole restore resets the order.
func (o *orderedDurable) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	err := o.partialDurable.Restore(r, meta)
	//: a whole restore is where the next entry follows.
	if err == nil {
		o.ord.restored(o.kv)
	}
	//: a machine that breaks its contract moves its position on a refusal.
	if err != nil && o.breaks {
		o.pmu.Lock()
		o.li, o.pos = meta.Log, meta.Position
		o.pmu.Unlock()
	}
	return err
}

// atomicDurable is an orderedDurable that declares its Restore atomic.
type atomicDurable struct{ *orderedDurable }

// RestoresAtomically implements AtomicRestorer.
func (atomicDurable) RestoresAtomically() {}

// refusedCopyStays is a snapshot store whose Remove of an install's copy
// that did not restore fails as a failed unlink or directory sync does;
// every other Remove goes through.
type refusedCopyStays struct{ *MemorySnapshots }

// Remove implements SnapshotStore, failing the removal restoreFailed makes.
func (s refusedCopyStays) Remove(id string) error {
	//: only the removal of a copy that did not restore fails.
	if calledFrom(".(*Node).restoreFailed") {
		return storageFailed("remove", errImpossible)
	}
	return s.MemorySnapshots.Remove(id)
}

// A machine that declares its Restore atomic, an AtomicRestorer, leaves
// its state as it was when its Restore fails: the engine refuses the
// install as before #1582 and the node keeps running, with no marker, every
// entry applied in order after the state it held, until a later offer
// restores it (#1607). The same machine without the declaration stops as
// #1582 decided, an AtomicRestorer that refuses its version still stops on
// it (D18), and a durable one whose position moved across its refusal is
// stopped with its marker: the paths are apart. A refusal that keeps the
// node running still removes its copy, and a removal that fails there is
// this node's disk failing: it stops the node STORAGE_FAILED, with its
// marker (D4).
func TestAnAtomicRestorerRefusedAtAnInstallKeepsRunning(t *testing.T) {
	type tc struct {
		name    string
		atomic  bool
		durable bool
		// breaks has the durable machine move its position on the refusal.
		breaks bool
		// version has the machine refuse VERSION_UNSUPPORTED.
		version bool
		// removeFails has the removal of the refused copy fail
		// STORAGE_FAILED.
		removeFails bool
		// code is the stop's code when the node must stop, 0 when it must
		// keep running; marked whether that stop leaves a marker.
		code   errs.Code
		marked bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		var armed atomic.Bool
		var machine atomic.Pointer[partialRestore]
		var order atomic.Pointer[applyOrder]
		var marks atomic.Pointer[markedLog]
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 6, 0, 7}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			//: only the lagging node runs the failing machine.
			if uint64(tn.id) != lagID.Load() {
				return
			}
			ml := &markedLog{MemoryLog: tn.log}
			marks.Store(ml)
			cfg.Log = ml
			//: the removal case's store fails the removal of the refused copy.
			if tcase.removeFails {
				cfg.Snapshots = refusedCopyStays{tn.snaps}
			}
			// No entry is handed past the refusal by the test: the order the
			// engine applies in is the one judged.
			p := &partialRestore{kv: tn.sm, armed: &armed, node: new(atomic.Pointer[Node]), failed: make(chan struct{})}
			//: the version case's machine refuses a format it does not read.
			if tcase.version {
				p.refusal = origin(coreconsensus.VersionUnsupported, errs.String("why", "the test's machine met a record it does not read"))
			}
			machine.Store(p)
			switch {
			//: a durable machine that declares its restore atomic.
			case tcase.durable && tcase.atomic:
				o := &orderedDurable{partialDurable: &partialDurable{partialRestore: p}, breaks: tcase.breaks}
				order.Store(&o.ord)
				cfg.Machine, cfg.Durability = atomicDurable{o}, DurabilityDurable
			//: a durable machine that does not.
			case tcase.durable:
				o := &orderedDurable{partialDurable: &partialDurable{partialRestore: p}}
				order.Store(&o.ord)
				cfg.Machine, cfg.Durability = o, DurabilityDurable
			//: a volatile machine that declares its restore atomic.
			case tcase.atomic:
				o := &orderedRestore{partialRestore: p}
				order.Store(&o.ord)
				cfg.Machine = atomicRestore{o}
			//: a volatile machine that does not.
			default:
				o := &orderedRestore{partialRestore: p}
				order.Store(&o.ord)
				cfg.Machine = o
			}
		}
		newClusterWith(t, c)
		l := c.settledLeader(5 * time.Second)
		lag := c.otherThan(l)
		var tok coreconsensus.TokenValue
		//: a first state the lagging node holds before it stops.
		for i := range 100 {
			tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
		}
		c.waitApplied(tok.Index, 10*time.Second)
		held := tok.Index
		// Isolated while stopped: past the live logs' start, only an install
		// can catch it up.
		c.net.Isolate(lag.id, true)
		c.kill(lag)
		//: the state the install carries, past the logs the others keep.
		for i := 100; i < 300; i++ {
			tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
		}
		compactedPast(t, c, held+1)
		lagID.Store(uint64(lag.id))
		c.start(lag)
		// Armed once started: the start's own restore reads its snapshot
		// whole.
		armed.Store(true)
		c.net.Isolate(lag.id, false)
		p := machine.Load()
		select {
		//: the install's restore failed half way.
		case <-p.failed:
		//: no install reached the lagging node.
		case <-time.After(10 * time.Second):
			t.Fatal("setup: the lagging node never failed an install's restore")
		}
		//: a node that must stop is judged on its stop.
		if tcase.code != 0 {
			deadline := time.Now().Add(10 * time.Second)
			//: the refusal stops the node.
			for !lag.node.stopped.Load() {
				//: not in time.
				if time.Now().After(deadline) {
					t.Fatalf("a refused restore did not stop the node: Applied %d", lag.node.Status().Applied)
				}
				time.Sleep(5 * time.Millisecond)
			}
			reason, _ := lag.node.StopReason()
			//: the stop's code is the case's.
			if !errs.HasCode(reason.Err, tcase.code) {
				t.Errorf("a refused restore stopped the node with %v, want code %v", reason.Err, tcase.code)
			}
			//: a refusal that stops the node applies no entry after it; an atomic one kept a state its entries continue.
			if k := p.afterRefusal.Load(); k != 0 && !tcase.removeFails {
				t.Errorf("%d entries were applied after a refused restore", k)
			}
			//: every entry continued the state before it.
			if k := order.Load().disorder.Load(); k != 0 {
				t.Errorf("%d entries were applied over a state they do not continue", k)
			}
			//: the marker is the case's.
			if got := marks.Load().marks.Load() != 0; got != tcase.marked {
				t.Errorf("the stop wrote %d markers, want marked %v", marks.Load().marks.Load(), tcase.marked)
			}
			return
		}
		deadline := time.Now().Add(15 * time.Second)
		//: the node, still running, ends on the leader's state through a later install.
		for lag.node.Status().Applied < tok.Index || !sameState(lag.sm, l.sm) {
			//: a stop ends the wait.
			if lag.node.stopped.Load() {
				reason, _ := lag.node.StopReason()
				t.Fatalf("an AtomicRestorer's refused restore stopped the node: %v", reason.Err)
			}
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("the node did not catch up after a refused atomic restore: Applied %d against %d, %d keys against the leader's %d",
					lag.node.Status().Applied, tok.Index, machineKeys(lag), machineKeys(l))
			}
			time.Sleep(5 * time.Millisecond)
		}
		//: the node never stopped.
		if lag.node.stopped.Load() {
			t.Errorf("the node stopped after it caught up")
		}
		//: no marker was written.
		if k := marks.Load().marks.Load(); k != 0 {
			t.Errorf("an AtomicRestorer's refused restore wrote %d stop markers", k)
		}
		//: every entry continued the state before it.
		if k := order.Load().disorder.Load(); k != 0 {
			t.Errorf("%d entries were applied over a state they do not continue", k)
		}
		//: a durable machine's position is the one it applied.
		if tcase.durable {
			_, pos, _ := lag.node.cfg.Machine.(coreconsensus.DurableStateMachine).AppliedPosition()
			//: its position caught up with its state.
			if pos.Index < tok.Index {
				t.Errorf("the durable machine reports position %d, want at least %d", pos.Index, tok.Index)
			}
		}
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a volatile AtomicRestorer keeps running and catches up", atomic: true},
		{name: "a durable AtomicRestorer keeps running and catches up", atomic: true, durable: true},
		{name: "the same volatile machine undeclared stops unmarked", code: coreconsensus.CodeStateMachineFailed},
		{name: "the same durable machine undeclared stops marked", durable: true, code: coreconsensus.CodeStateMachineFailed, marked: true},
		{name: "an AtomicRestorer that refuses its version stops on it, marked", atomic: true, version: true, code: coreconsensus.CodeVersionUnsupported, marked: true},
		{name: "a durable AtomicRestorer whose refusal moved its position stops marked", atomic: true, durable: true, breaks: true, code: coreconsensus.CodeStateMachineFailed, marked: true},
		{name: "a volatile AtomicRestorer whose refused copy cannot be removed stops on its disk", atomic: true, removeFails: true, code: coreconsensus.CodeStorageFailed, marked: true},
		{name: "a durable AtomicRestorer whose refused copy cannot be removed stops on its disk", atomic: true, durable: true, removeFails: true, code: coreconsensus.CodeStorageFailed, marked: true},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}

// A durable AtomicRestorer whose Restore is refused at a start kept the
// state it held: the start falls back as before #1582, leaves no marker,
// and applies nothing out of order, a refusal coded STATE_MACHINE_FAILED,
// as machine.SQL's, included; the same machine undeclared fails the start
// and leaves the marker #1582 decided (#1607).
func TestAnAtomicRestorerRefusedAtStartFallsBack(t *testing.T) {
	type tc struct {
		name   string
		atomic bool
		// smf has the machine refuse STATE_MACHINE_FAILED, as machine.SQL
		// does, rather than SNAPSHOT_CORRUPT; version VERSION_UNSUPPORTED.
		smf, version bool
		// breaks has the machine move its position on the refusal: the
		// start must stop it with its marker, whatever the code.
		breaks bool
	}
	runCase := func(t *testing.T, tcase tc) {
		b := newDurableBench(t, coreconsensus.ClusterID{9, 6, 0, 7})
		var tok coreconsensus.TokenValue
		//: enough entries for a snapshot of some size.
		for i := range 30 {
			tok = propose(t, b.leader().node, fmt.Sprintf("k%d=v%d", i, i))
		}
		l := b.leader()
		var f *durableNode
		//: the follower is the node whose start is judged.
		for _, x := range b.nodes {
			//: any node but the leader.
			if x != l {
				f = x
				break
			}
		}
		b.waitMachine(f, tok.Index)
		b.save(f)
		b.crash(f)
		var armed atomic.Bool
		armed.Store(true)
		// A machine that holds no position: the start restores the
		// snapshot into it, and the first restore fails, reading aside.
		p := &partialRestore{kv: newKV(), armed: &armed, node: new(atomic.Pointer[Node]), failed: make(chan struct{})}
		//: the SQL-like case refuses on the code a database's failure carries.
		if tcase.smf {
			p.refusal = origin(coreconsensus.StateMachineFailed, errs.String("why", "the test's database refused the restore"))
		}
		//: the version case refuses a format it does not read.
		if tcase.version {
			p.refusal = origin(coreconsensus.VersionUnsupported, errs.String("why", "the test's machine met a record it does not read"))
		}
		o := &orderedDurable{partialDurable: &partialDurable{partialRestore: p}, breaks: tcase.breaks}
		var m coreconsensus.StateMachine = o
		//: the declared case's machine says its restore is atomic.
		if tcase.atomic {
			m = atomicDurable{o}
		}
		start := func() (*Node, error) {
			f.dd = b.openDD(f.dir)
			cfg := b.conf(f)
			cfg.Machine, cfg.Log, cfg.Snapshots = m, f.dd.Log(), f.dd.Snapshots()
			n, err := NewNode(cfg)
			//: a start that failed leaves nothing open.
			if err != nil {
				abandon(NewMemoryTransport(b.net, f.id).Close())
				abandon(f.dd.Close())
			}
			return n, err
		}
		stopNode := func(n *Node) {
			abandon(n.HardClose())
			abandon(NewMemoryTransport(b.net, f.id).Close())
			abandon(f.dd.Close())
		}
		n, err := start()
		//: a node that started is closed with the test, whatever follows.
		if err == nil {
			first := n
			t.Cleanup(func() { stopNode(first) })
		}
		select {
		//: the failing restore ran.
		case <-p.failed:
		//: it did not.
		default:
			t.Fatal("setup: the start never asked the machine to restore")
		}
		//: the undeclared machine, and a declared one that moved its position, fail the start, and the marker refuses the next.
		if !tcase.atomic || tcase.breaks {
			//: the stop #1582 decided.
			if !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
				t.Fatalf("a durable machine undeclared, or declared and moving its position, refused its restore at a start, which returned %v (want STATE_MACHINE_FAILED)", err)
			}
			second, err := start()
			//: a node that started anyway is closed.
			if err == nil {
				stopNode(second)
			}
			//: the marker refuses the next start.
			if !errs.HasCode(err, coreconsensus.CodeStopMarkerPresent) {
				t.Errorf("the start after a refused undeclared restore returned %v, want STOP_MARKER_PRESENT", err)
			}
			return
		}
		//: the AtomicRestorer's start falls back and runs.
		if err != nil {
			t.Fatalf("an AtomicRestorer refused its restore at a start, which returned %v %v (want a fallback)", err, errs.FieldsOf(err))
		}
		deadline := time.Now().Add(10 * time.Second)
		//: the node ends on the cluster's state.
		for {
			_, pos, _ := m.(coreconsensus.DurableStateMachine).AppliedPosition()
			//: caught up.
			if pos.Index >= tok.Index && sameState(p.kv, l.sm.kv) {
				break
			}
			//: a stop ends the wait.
			if n.stopped.Load() {
				reason, _ := n.StopReason()
				t.Fatalf("an AtomicRestorer's start fallback stopped the node: %v", reason.Err)
			}
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("the started node did not reach the cluster's state: position %d against %d", pos.Index, tok.Index)
			}
			time.Sleep(5 * time.Millisecond)
		}
		//: every entry continued the state before it.
		if k := o.ord.disorder.Load(); k != 0 {
			t.Errorf("%d entries were applied over a state they do not continue", k)
		}
	}
	//: each case runs its own bench.
	for _, tcase := range []tc{
		{name: "a durable AtomicRestorer refused at a start falls back and runs", atomic: true},
		{name: "a durable AtomicRestorer refused STATE_MACHINE_FAILED at a start falls back and runs", atomic: true, smf: true},
		{name: "a durable AtomicRestorer that moved its position and refused its version stops marked", atomic: true, version: true, breaks: true},
		{name: "the same machine undeclared fails the start and leaves its marker"},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}
