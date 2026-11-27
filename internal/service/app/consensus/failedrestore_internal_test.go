package consensus

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// partialRestore is the lagging node's machine: a kv whose first Restore
// once armed fails after it read part of its stream. With inPlace set it
// writes what it read into the kv before it fails, as a machine that
// decodes straight into its live state; without, it reads aside and keeps
// its state, as the tests' kv does. Every Apply made while the machine
// holds the prefix it wrote is counted.
type partialRestore struct {
	*kv
	inPlace bool
	armed   *atomic.Bool
	once    sync.Once
	// node, when set, is handed two committed entries below the snapshot
	// right after the failed restore, as the core hands the entries (A,
	// match] it committed after it sent the restore.
	node *atomic.Pointer[Node]
	// failed is closed once the first restore once armed failed.
	failed chan struct{}
	// prefix is set while the kv holds the prefix a failed restore wrote,
	// and onPrefix counts the entries applied over it.
	prefix   atomic.Bool
	onPrefix atomic.Int64
	// refused is set as the failing restore returns, and afterRefusal
	// counts every Apply made once it is.
	refused      atomic.Bool
	afterRefusal atomic.Int64
	// refusal, when set, is the error the failing restore returns in place
	// of SNAPSHOT_CORRUPT.
	refusal error
}

// Apply implements StateMachine, counting an entry applied over a prefix.
func (p *partialRestore) Apply(e coreconsensus.EntryValue) any {
	//: an entry applied while the kv holds a failed restore's prefix.
	if p.prefix.Load() {
		p.onPrefix.Add(1)
	}
	//: an entry applied once a restore was refused.
	if p.refused.Load() {
		p.afterRefusal.Add(1)
	}
	return p.kv.Apply(e)
}

// Restore implements StateMachine: the first restore once armed fails
// after it read half of the stream, the others replace the state whole.
func (p *partialRestore) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	first := false
	//: only the first restore once armed fails.
	if p.armed.Load() {
		p.once.Do(func() { first = true })
	}
	//: any other restore replaces the state whole and leaves no prefix.
	if !first {
		err := p.kv.Restore(r, meta)
		//: a whole state replaces the prefix.
		if err == nil {
			p.prefix.Store(false)
		}
		return err
	}
	b, err := io.ReadAll(r)
	//: a stream that cannot be read leaves nothing to write.
	if err != nil {
		return err
	}
	half := newKV()
	//: half of the stream decodes into a kv of its own.
	if err := half.Restore(bytes.NewReader(b[:prefixEnd(b)]), meta); err != nil {
		return err
	}
	//: an in-place machine writes the prefix over its live state.
	if p.inPlace {
		p.mu.Lock()
		p.m = maps.Clone(half.m)
		p.mu.Unlock()
		p.prefix.Store(true)
	}
	//: the entries below the snapshot are handed after its restore, as the core may.
	if n := p.node.Load(); n != nil {
		n.aq.push(applyItem{entries: []coreconsensus.EntryValue{
			{Kind: coreconsensus.EntryCommand, Position: coreconsensus.PositionValue{Index: meta.Position.Index - 2, Term: meta.Position.Term}, Data: []byte("onprefix=1")},
			{Kind: coreconsensus.EntryCommand, Position: coreconsensus.PositionValue{Index: meta.Position.Index - 1, Term: meta.Position.Term}, Data: []byte("onprefix=2")},
		}})
	}
	p.refused.Store(true)
	close(p.failed)
	//: a refusal the case names is returned as is.
	if p.refusal != nil {
		return p.refusal
	}
	return origin(coreconsensus.SnapshotCorrupt, errs.String("why", "the test's machine failed half way"))
}

// prefixEnd is the end of the kv records that make up the first half of
// the encoded kv b, on a record boundary.
func prefixEnd(b []byte) int {
	end := 0
	//: records are walked until half the bytes are behind.
	for end < len(b)/2 {
		end = skipRecord(b, skipRecord(b, end))
	}
	return end
}

// skipRecord is the offset past the length-prefixed field at off in b.
func skipRecord(b []byte, off int) int {
	n, k := binary.Uvarint(b[off:])
	return off + k + int(n)
}

// partialDurable is a partialRestore that is a DurableStateMachine: the
// position it holds moves with its applies and its whole restores.
type partialDurable struct {
	*partialRestore
	pmu sync.Mutex
	li  coreconsensus.LogIdentityValue
	pos coreconsensus.PositionValue
}

// Apply implements StateMachine, recording the position applied.
func (d *partialDurable) Apply(e coreconsensus.EntryValue) any {
	r := d.partialRestore.Apply(e)
	d.pmu.Lock()
	d.li = coreconsensus.LogIdentityValue{Cluster: e.Token.Cluster, Epoch: e.Token.Epoch}
	d.pos = e.Position
	d.pmu.Unlock()
	return r
}

// Restore implements StateMachine, recording the position of a whole
// restore.
func (d *partialDurable) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	//: a refused restore records no position.
	if err := d.partialRestore.Restore(r, meta); err != nil {
		return err
	}
	d.pmu.Lock()
	d.li, d.pos = meta.Log, meta.Position
	d.pmu.Unlock()
	return nil
}

// AppliedPosition implements DurableStateMachine.
func (d *partialDurable) AppliedPosition() (coreconsensus.LogIdentityValue, coreconsensus.PositionValue, error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()
	return d.li, d.pos, nil
}

// sameState reports whether two kvs hold the same pairs.
func sameState(a, b *kv) bool {
	a.mu.Lock()
	am := maps.Clone(a.m)
	a.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Equal(am, b.m)
}

// markedLog is a memory log that counts the stop markers a stop writes.
type markedLog struct {
	*MemoryLog
	marks atomic.Int64
}

// markStopEach counts a stop marker, as a file log writes one.
func (l *markedLog) markStopEach(errs.Code, string) (dirErr, runErr error) {
	l.marks.Add(1)
	return nil, nil
}

// A Restore that fails after it wrote part of its stream in place leaves
// the machine on a prefix no correct node holds. The entries (A, match]
// handed after the restore were applied over it, and a durable machine
// recorded their positions with that state, which a restart resumed after
// (#1582). The engine cannot tell such a machine from one that read aside,
// so every refusal of an install's Restore now stops the node, on the apply
// goroutine, before the restore is answered: no entry is applied after it.
// A volatile machine stops STATE_MACHINE_FAILED with no marker, and a node
// started again on a fresh machine ends on the leader's state; a
// DurableStateMachine stops STATE_MACHINE_FAILED, and VERSION_UNSUPPORTED
// stops on its own code, both with their marker.
func TestARefusedRestoreStopsTheNodeBeforeAnyApply(t *testing.T) {
	type tc struct {
		name    string
		inPlace bool
		durable bool
		// version has the machine refuse VERSION_UNSUPPORTED.
		version bool
		// code is the stop's code, marked whether it leaves a marker.
		code   errs.Code
		marked bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		var armed atomic.Bool
		var node atomic.Pointer[Node]
		var machine atomic.Pointer[partialRestore]
		var marks atomic.Pointer[markedLog]
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 8, 2}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			//: only the lagging node runs the failing machine.
			if uint64(tn.id) != lagID.Load() {
				return
			}
			ml := &markedLog{MemoryLog: tn.log}
			marks.Store(ml)
			cfg.Log = ml
			p := &partialRestore{kv: tn.sm, inPlace: tcase.inPlace, armed: &armed, node: &node, failed: make(chan struct{})}
			//: the version case's machine refuses a format it does not read.
			if tcase.version {
				p.refusal = origin(coreconsensus.VersionUnsupported, errs.String("why", "the test's machine met a record it does not read"))
			}
			machine.Store(p)
			cfg.Machine = p
			//: the durable case's machine says where it stands.
			if tcase.durable {
				cfg.Machine = &partialDurable{partialRestore: p}
				cfg.Durability = DurabilityDurable
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
		node.Store(lag.node)
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
		deadline := time.Now().Add(10 * time.Second)
		//: the refusal stops the node.
		for !lag.node.stopped.Load() {
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("a refused restore did not stop the node: Applied %d, %d entries applied after the refusal",
					lag.node.Status().Applied, p.afterRefusal.Load())
			}
			time.Sleep(5 * time.Millisecond)
		}
		reason, _ := lag.node.StopReason()
		//: the stop's code is the case's.
		if !errs.HasCode(reason.Err, tcase.code) {
			t.Errorf("a refused restore stopped the node with %v, want code %v", reason.Err, tcase.code)
		}
		//: no entry is applied once the restore was refused.
		if k := p.afterRefusal.Load(); k != 0 {
			t.Errorf("%d entries were applied after a refused restore, %d over the prefix it wrote", k, p.onPrefix.Load())
		}
		//: a durable machine's stop, and a version's, leave their marker; a volatile machine's none.
		if got := marks.Load().marks.Load() != 0; got != tcase.marked {
			t.Errorf("the stop wrote %d markers, want marked %v", marks.Load().marks.Load(), tcase.marked)
		}
		//: the durable and version cases end on the stop.
		if tcase.durable || tcase.version {
			return
		}
		// Started again on a fresh machine, as a supervisor does: the
		// install is offered again and its restore succeeds.
		armed.Store(false)
		c.kill(lag)
		lagID.Store(0)
		c.start(lag)
		deadline = time.Now().Add(15 * time.Second)
		//: the restarted node ends on the leader's state.
		for lag.node.Status().Applied < tok.Index || !sameState(lag.sm, l.sm) {
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("the restarted node did not catch up: Applied %d against %d, %d keys against the leader's %d",
					lag.node.Status().Applied, tok.Index, machineKeys(lag), machineKeys(l))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a restore that wrote a prefix in place stops the node unmarked", inPlace: true, code: coreconsensus.CodeStateMachineFailed},
		{name: "a restore that read aside stops the node unmarked", code: coreconsensus.CodeStateMachineFailed},
		{name: "a durable restore that wrote a prefix stops the node marked", inPlace: true, durable: true, code: coreconsensus.CodeStateMachineFailed, marked: true},
		{name: "a restore that refused its version stops the node on it, marked", inPlace: true, version: true, code: coreconsensus.CodeVersionUnsupported, marked: true},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}

// A DurableStateMachine whose Restore is refused at a start may have
// written its disk: the start fails STATE_MACHINE_FAILED, the marker it
// leaves refuses the next one STOP_MARKER_PRESENT, and no older base or
// replay from 1 is taken over the state it wrote (#1582). A machine whose
// restore succeeds starts as before.
func TestADurableRestoreRefusedAtStartStopsTheNode(t *testing.T) {
	type tc struct {
		name   string
		refuse bool
	}
	runCase := func(t *testing.T, tcase tc) {
		b := newDurableBench(t, coreconsensus.ClusterID{9, 5, 8, 2})
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
		armed.Store(tcase.refuse)
		// A machine that holds no position: the start restores the
		// snapshot into it.
		p := &partialRestore{kv: newKV(), inPlace: true, armed: &armed, node: new(atomic.Pointer[Node]), failed: make(chan struct{})}
		m := &partialDurable{partialRestore: p}
		f.dd = b.openDD(f.dir)
		cfg := b.conf(f)
		cfg.Machine, cfg.Log, cfg.Snapshots = m, f.dd.Log(), f.dd.Snapshots()
		n, err := NewNode(cfg)
		//: a started node is closed with its directory.
		if err == nil {
			abandon(n.HardClose())
		}
		abandon(NewMemoryTransport(b.net, f.id).Close())
		abandon(f.dd.Close())
		//: a machine whose restore succeeds starts.
		if !tcase.refuse && err != nil {
			t.Fatalf("a start whose restore succeeded failed: %v %v", err, errs.FieldsOf(err))
		}
		//: the rest judges a refused restore.
		if !tcase.refuse {
			return
		}
		//: a refused restore fails the start STATE_MACHINE_FAILED.
		if !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
			t.Fatalf("a DurableStateMachine refused its restore at a start, which returned %v (want STATE_MACHINE_FAILED); %d entries applied over the prefix",
				err, p.onPrefix.Load())
		}
		f.dd = b.openDD(f.dir)
		cfg.Log, cfg.Snapshots = f.dd.Log(), f.dd.Snapshots()
		n, err = NewNode(cfg)
		//: a node that started anyway is closed.
		if err == nil {
			abandon(n.HardClose())
		}
		abandon(NewMemoryTransport(b.net, f.id).Close())
		abandon(f.dd.Close())
		//: the marker refuses the next start.
		if !errs.HasCode(err, coreconsensus.CodeStopMarkerPresent) {
			t.Errorf("the start after a refused durable restore returned %v, want STOP_MARKER_PRESENT", err)
		}
	}
	//: each case runs its own bench.
	for _, tcase := range []tc{
		{name: "a refused restore stops the start and leaves a marker", refuse: true},
		{name: "a restore that succeeds starts"},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}

// A volatile machine whose Restore is refused at a start may hold the
// prefix it decoded in place: falling back to an older snapshot it skipped
// or to a replay from index 1 applied the log over that prefix (#1582).
// The start now fails STATE_MACHINE_FAILED, with no marker: the state is
// the process's, so a start with a machine of its own restores again. A
// snapshot the engine refuses before Restore, on its checksums, touched no
// machine and still falls back.
func TestAVolatileRestoreRefusedAtStartRefusesTheStart(t *testing.T) {
	type tc struct {
		name string
		// corrupt flips a byte of the published snapshot, which the engine
		// refuses before Restore.
		corrupt bool
	}
	runCase := func(t *testing.T, tcase tc) {
		b := newDurableBench(t, coreconsensus.ClusterID{9, 5, 8, 3})
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
		//: the corrupt case's snapshot fails its checksums before Restore.
		if tcase.corrupt && corruptPublishedSnapshots(t, f.dir) == 0 {
			t.Fatal("setup: no published snapshot to corrupt")
		}
		var armed atomic.Bool
		armed.Store(true)
		p := &partialRestore{kv: newKV(), inPlace: true, armed: &armed, node: new(atomic.Pointer[Node]), failed: make(chan struct{})}
		start := func(m coreconsensus.StateMachine) error {
			f.dd = b.openDD(f.dir)
			cfg := b.conf(f)
			cfg.Machine, cfg.Durability, cfg.Log, cfg.Snapshots = m, DurabilityVolatile, f.dd.Log(), f.dd.Snapshots()
			n, err := NewNode(cfg)
			//: a started node is closed with its directory.
			if err == nil {
				abandon(n.HardClose())
			}
			abandon(NewMemoryTransport(b.net, f.id).Close())
			abandon(f.dd.Close())
			return err
		}
		err := start(p)
		//: a snapshot the engine refused falls back, and the machine's Restore never failed.
		if tcase.corrupt {
			select {
			//: the failing restore ran.
			case <-p.failed:
				t.Errorf("the machine was asked to restore a snapshot that failed its checksums")
			//: it did not.
			default:
			}
			//: the fallback starts.
			if err != nil {
				t.Errorf("a start whose snapshot failed its checksums did not fall back: %v %v", err, errs.FieldsOf(err))
			}
			return
		}
		//: a refused restore fails the start STATE_MACHINE_FAILED, applying nothing over the prefix.
		if !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
			t.Fatalf("a volatile machine refused its restore at a start, which returned %v (want STATE_MACHINE_FAILED); %d entries applied over the prefix",
				err, p.onPrefix.Load())
		}
		//: nothing applied over the prefix.
		if k := p.onPrefix.Load(); k != 0 {
			t.Errorf("%d entries were applied over the prefix a refused start restore wrote", k)
		}
		//: no marker: a start with a machine of its own restores again.
		if err := start(newKV()); err != nil {
			t.Errorf("a start with a fresh volatile machine after a refused restore failed: %v", err)
		}
	}
	//: each case runs its own bench.
	for _, tcase := range []tc{
		{name: "a restore the machine refused fails the start and leaves no marker"},
		{name: "a snapshot the engine refused before Restore still falls back", corrupt: true},
	} {
		t.Run(tcase.name, func(t *testing.T) { runCase(t, tcase) })
	}
}
