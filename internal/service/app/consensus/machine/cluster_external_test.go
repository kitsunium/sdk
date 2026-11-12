package machine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcconsensus "github.com/kitsunium/sdk/internal/service/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
)

// The cluster suite of the machines: three nodes over the memory transport,
// each its own log, snapshot store and machine, built through the engine's
// published API only.

// member is one node of a test cluster.
type member struct {
	id    coreconsensus.NodeID
	log   *svcconsensus.MemoryLog
	snaps *svcconsensus.MemorySnapshots
	sm    coreconsensus.StateMachine
	node  *svcconsensus.Node
}

// testCluster3 is a founded cluster; machineFor builds node i's machine at
// each of its starts.
type testCluster3 struct {
	t          *testing.T
	net        *svcconsensus.MemoryNetwork
	cid        coreconsensus.ClusterID
	durability svcconsensus.Durability
	machineFor func(i int) coreconsensus.StateMachine
	members    []*member
	// mu guards each member's node and sm, which a restart replaces while
	// clients look for the leader.
	mu sync.Mutex
}

func clusterTiming() coreconsensus.TimingContractSpec {
	return coreconsensus.TimingContractSpec{
		Generation: 1, HeartbeatInterval: 20 * time.Millisecond,
		ElectionTimeoutMin: 200 * time.Millisecond, ElectionTimeoutMax: 400 * time.Millisecond, MaxClockDrift: 0.05,
	}
}

func (c *testCluster3) config(m *member) svcconsensus.Config {
	return svcconsensus.Config{
		Cluster: c.cid, Machine: m.sm, Durability: c.durability, Log: m.log, Snapshots: m.snaps,
		Transport: svcconsensus.NewMemoryTransport(c.net, m.id), InitialTiming: clusterTiming(),
		MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 20, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20,
		MinVoters: 1, SnapshotEvery: 100000, SnapshotTrailing: 4,
	}
}

func newTestCluster(t *testing.T, durability svcconsensus.Durability, machineFor func(i int) coreconsensus.StateMachine) *testCluster3 {
	t.Helper()
	c := &testCluster3{t: t, net: svcconsensus.NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 1, 6}, durability: durability, machineFor: machineFor}
	var founders []coreconsensus.MemberValue
	for i := range 3 {
		m := &member{log: svcconsensus.NewMemoryLog(), snaps: svcconsensus.NewMemorySnapshots()}
		slot := fmt.Sprintf("slot-%d", i)
		id, inc, err := svcconsensus.InitLog(m.log, c.cid, slot)
		if err != nil {
			t.Fatal(err)
		}
		m.id = id
		c.members = append(c.members, m)
		founders = append(founders, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: svcconsensus.MemorySPKI(id), Voter: true})
	}
	for i, m := range c.members {
		m.sm = machineFor(i)
		if err := svcconsensus.Bootstrap(context.Background(), new(c.config(m)), founders); err != nil {
			t.Fatal(err)
		}
	}
	for i := range c.members {
		c.start(i)
	}
	t.Cleanup(func() {
		for _, m := range c.members {
			if m.node != nil {
				_ = m.node.HardClose()
			}
		}
	})
	return c
}

// start opens node i over a machine built afresh, on what its log and its
// database kept.
func (c *testCluster3) start(i int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.members[i]
	m.sm = c.machineFor(i)
	n, err := svcconsensus.NewNode(c.config(m))
	if err != nil {
		c.t.Fatalf("NewNode %d: %v", i, err)
	}
	m.node = n
}

// at returns node i and its machine as they stand.
func (c *testCluster3) at(i int) (*svcconsensus.Node, coreconsensus.StateMachine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.members[i].node, c.members[i].sm
}

// kill stops node i as a crash would, its transport with it.
func (c *testCluster3) kill(i int) {
	c.mu.Lock()
	m := c.members[i]
	n := m.node
	m.node = nil
	c.mu.Unlock()
	_ = n.HardClose()
	_ = svcconsensus.NewMemoryTransport(c.net, m.id).Close()
}

// leader waits for a node that leads.
func (c *testCluster3) leader() (int, *svcconsensus.Node) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for i := range c.members {
			if n, _ := c.at(i); n != nil && n.Status().Role == coreconsensus.RoleLeader {
				return i, n
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatal("no leader")
	return -1, nil
}

// propose replicates data through whichever node leads, again on a new
// leader when a proposal certainly did not append (NOT_LEADER). It returns
// the token and the machine's answer.
func (c *testCluster3) propose(data []byte) (coreconsensus.TokenValue, any) {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, n := c.leader()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, r, err := n.Propose(ctx, data)
		cancel()
		if err == nil {
			return tok, r
		}
		if !errors.Is(err, coreconsensus.NotLeader) || time.Now().After(deadline) {
			c.t.Fatalf("propose %q: %v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitPosition waits until node i's durable machine holds index applied.
func (c *testCluster3) waitPosition(i int, index uint64) {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, sm := c.at(i)
		if _, pos, _ := sm.(coreconsensus.DurableStateMachine).AppliedPosition(); pos.Index >= index {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.t.Fatalf("node %d never applied %d", i, index)
}

// TestADurableSQLMachineCrashedBeforeItsPositionAppliesEveryEntryOnce is
// the crash of ADR 0152 D3 that a hand-written machine gets wrong: the
// batch is applied and the process dies before the position is written. A
// position written outside the batch's transaction replays the batch (a
// position behind the state) or skips it (ahead of it); the machine writes
// both in one transaction, so the restart replays exactly the entries the
// database does not hold.
func TestADurableSQLMachineCrashedBeforeItsPositionAppliesEveryEntryOnce(t *testing.T) {
	dbs := make([]*memDB, 3)
	for i := range dbs {
		dbs[i] = &memDB{}
	}
	c := newTestCluster(t, svcconsensus.DurabilityDurable, func(i int) coreconsensus.StateMachine {
		m, err := machine.NewSQL(context.Background(), entriesConfig(transactorOver(t, dbs[i])))
		if err != nil {
			t.Fatalf("NewSQL() = %v", err)
		}
		return m
	})
	var want []string
	write := func(from, to int) uint64 {
		var last uint64
		for k := from; k <= to; k++ {
			data := fmt.Sprintf("w%02d", k)
			tok, r := c.propose([]byte(data))
			if r != data {
				t.Fatalf("propose %q answered %v", data, r)
			}
			want, last = append(want, data), tok.Index
		}
		return last
	}
	last := write(1, 10)
	li, _ := c.leader()
	victim := (li + 1) % 3
	c.waitPosition(victim, last)
	dbs[victim].armCrash(int64(last) + 1)
	last = write(11, 20)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if n, _ := c.at(victim); n != nil {
			if reason, ok := n.StopReason(); ok {
				if !errs.HasCode(reason.Err, coreconsensus.CodeStateMachineFailed) {
					t.Fatalf("the victim stopped on %v, want STATE_MACHINE_FAILED", reason.Err)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the victim's machine never failed at its position write")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !dbs[victim].crashed() {
		t.Fatal("the crash was never injected")
	}
	held := len(dbs[victim].rows())
	c.kill(victim)
	c.start(victim)
	last = write(21, 25)
	for i := range c.members {
		c.waitPosition(i, last)
	}
	for i, d := range dbs {
		rows := d.rows()
		if len(rows) != len(want) {
			t.Fatalf("node %d applied %d commands, want %d (the victim held %d at its crash)", i, len(rows), len(want), held)
		}
		for k, r := range rows {
			if r.data != want[k] || (k > 0 && r.idx <= rows[k-1].idx) {
				t.Fatalf("node %d's row %d is %+v after %+v, want %q in log order, once", i, k, r, rows[max(k-1, 0)], want[k])
			}
		}
	}
	if held >= len(want)-5 {
		t.Fatalf("the victim held %d rows at its crash: the crash hit no batch", held)
	}
}
