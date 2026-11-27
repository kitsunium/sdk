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
}

// Apply implements StateMachine, counting an entry applied over a prefix.
func (p *partialRestore) Apply(e coreconsensus.EntryValue) any {
	//: an entry applied while the kv holds a failed restore's prefix.
	if p.prefix.Load() {
		p.onPrefix.Add(1)
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
	close(p.failed)
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

// A Restore that fails after it wrote part of its stream in place leaves
// the machine on a prefix no correct node holds. The entries (A, match]
// handed after the restore were applied over it, and a durable machine
// recorded their positions with that state, which a restart resumed after
// (#1582). The node now applies nothing once an install's Restore refused
// its stream, until a Restore succeeds: the leader offers a snapshot again,
// its restore replaces the state, and the entries it covers are skipped. A
// DurableStateMachine may have written its disk, so its refusal stops the
// node STATE_MACHINE_FAILED. A machine that reads aside ends the same way
// as before: on the leader's state.
func TestAFailedRestoreAppliesNothingUntilARestoreSucceeds(t *testing.T) {
	type tc struct {
		name    string
		inPlace bool
		durable bool
	}
	runCase := func(t *testing.T, tcase tc) {
		var lagID atomic.Uint64
		var armed atomic.Bool
		var node atomic.Pointer[Node]
		var machine atomic.Pointer[partialRestore]
		c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 5, 8, 2}, every: 20, size: 3}
		c.configure = func(tn *testNode, cfg *Config) {
			//: only the lagging node runs the failing machine.
			if uint64(tn.id) != lagID.Load() {
				return
			}
			p := &partialRestore{kv: tn.sm, inPlace: tcase.inPlace, armed: &armed, node: &node, failed: make(chan struct{})}
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
		deadline := time.Now().Add(15 * time.Second)
		//: the node stops, or ends on the leader's state.
		for {
			//: a stopped node is judged below.
			if lag.node.stopped.Load() {
				break
			}
			//: caught up on the leader's state.
			if lag.node.Status().Applied >= tok.Index && sameState(lag.sm, l.sm) {
				break
			}
			//: neither in time.
			if time.Now().After(deadline) {
				t.Fatalf("the lagging node neither stopped nor caught up: Applied %d against %d, %d keys against the leader's %d",
					lag.node.Status().Applied, tok.Index, machineKeys(lag), machineKeys(l))
			}
			time.Sleep(5 * time.Millisecond)
		}
		reason, stopped := lag.node.StopReason()
		//: a durable machine that refused a restore stops the node.
		if tcase.durable && (!stopped || !errs.HasCode(reason.Err, coreconsensus.CodeStateMachineFailed)) {
			t.Errorf("a DurableStateMachine refused a restore it may have written, and the node goes on (stopped %v, %v): Applied %d, %d entries applied over the prefix",
				stopped, reason.Err, lag.node.Status().Applied, p.onPrefix.Load())
		}
		//: a volatile machine is never stopped by a refused restore.
		if !tcase.durable && stopped {
			t.Errorf("a refused restore stopped the node: %v", reason.Err)
		}
		//: no entry is applied over the prefix a failed restore wrote.
		if k := p.onPrefix.Load(); k != 0 {
			t.Errorf("%d entries were applied over the prefix a failed restore wrote in place", k)
		}
		//: a volatile node ends on the leader's state.
		if !tcase.durable && !sameState(lag.sm, l.sm) {
			t.Errorf("the lagging node holds %d keys, the leader %d", machineKeys(lag), machineKeys(l))
		}
	}
	//: each case runs its own cluster.
	for _, tcase := range []tc{
		{name: "a restore that wrote a prefix in place applies nothing over it", inPlace: true},
		{name: "a restore that read aside ends on the leader's state"},
		{name: "a durable restore that wrote a prefix stops the node", inPlace: true, durable: true},
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
