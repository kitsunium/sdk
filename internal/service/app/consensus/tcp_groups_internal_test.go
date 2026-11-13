package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// groupMember is one group's node on a machine of groupCluster: its own
// log, snapshots and machine, over the machine's transport.
type groupMember struct {
	log   *MemoryLog
	snaps *MemorySnapshots
	sm    *kv
	node  *Node
}

// groupMachine is one machine of groupCluster: one transport and one
// identity, a member of groups 1 and 2.
type groupMachine struct {
	tn     *tcpNode
	groups map[coreconsensus.GroupID]*groupMember
}

// groupCluster is three machines, each a member of groups 1 and 2 over one
// TCPTransport and one listener (#925).
type groupCluster struct {
	t   *testing.T
	ca  *testCA
	cid coreconsensus.ClusterID
	ms  []*groupMachine
}

// multiGroups are the groups groupCluster runs.
var multiGroups = []coreconsensus.GroupID{1, 2}

func (gc *groupCluster) config(m *groupMachine, g coreconsensus.GroupID) Config {
	gm := m.groups[g]
	c := tcpConfig(gc.cid, m.tn)
	c.Group, c.Log, c.Snapshots, c.Machine = g, gm.log, gm.snaps, gm.sm
	c.SnapshotEvery, c.SnapshotTrailing = 20, 4
	return c
}

func (gc *groupCluster) start(m *groupMachine) {
	gc.t.Helper()
	for _, g := range multiGroups {
		gm := m.groups[g]
		gm.sm = newKV()
		n, err := NewNode(gc.config(m, g))
		if err != nil {
			gc.t.Fatalf("group %d: %v", g, err)
		}
		gm.node = n
	}
}

func (gc *groupCluster) stop(m *groupMachine) {
	gc.t.Helper()
	for _, g := range multiGroups {
		if gm := m.groups[g]; gm.node != nil {
			if err := gm.node.HardClose(); err != nil {
				gc.t.Errorf("hard close group %d: %v", g, err)
			}
			gm.node = nil
		}
	}
	if m.tn.tr != nil {
		if err := m.tn.tr.Close(); err != nil {
			gc.t.Errorf("close transport: %v", err)
		}
		m.tn.tr = nil
	}
}

// newGroupCluster founds groups 1 and 2 on three machines. A machine is one
// member of both, under one id, incarnation, slot and key: the transport
// admits a key once (review M5), so the second group's log is stamped as
// the first's.
func newGroupCluster(t *testing.T) *groupCluster {
	gc := &groupCluster{t: t, ca: newTestCA(t), cid: coreconsensus.ClusterID{0x6e, 0x92, 0x5}}
	var members []coreconsensus.MemberValue
	for i := range 3 {
		tn := &tcpNode{slot: fmt.Sprintf("site-%d", i), log: NewMemoryLog(), snaps: NewMemorySnapshots()}
		tn.cert, tn.spki = gc.ca.issue(t, SAN(gc.cid, tn.slot), nil)
		tn.listen(t, gc.ca)
		id, inc, err := InitLog(tn.log, gc.cid, tn.slot)
		if err != nil {
			t.Fatal(err)
		}
		tn.id, tn.inc = id, inc
		second := NewMemoryLog()
		st := must(tn.log.Stamp())(t)
		if err := second.WriteStamp(st); err != nil {
			t.Fatal(err)
		}
		m := &groupMachine{tn: tn, groups: map[coreconsensus.GroupID]*groupMember{
			1: {log: tn.log, snaps: tn.snaps},
			2: {log: second, snaps: NewMemorySnapshots()},
		}}
		gc.ms = append(gc.ms, m)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: tn.slot, SPKI: tn.spki, Address: tn.addr, Voter: true})
	}
	for _, m := range gc.ms {
		for _, g := range multiGroups {
			m.groups[g].sm = newKV()
			if err := Bootstrap(context.Background(), new(gc.config(m, g)), members); err != nil {
				t.Fatalf("bootstrap group %d: %v", g, err)
			}
		}
		gc.start(m)
	}
	t.Cleanup(func() {
		for _, m := range gc.ms {
			gc.stop(m)
		}
	})
	return gc
}

// leader is group g's leader among the running machines.
func (gc *groupCluster) leader(g coreconsensus.GroupID) *groupMember {
	gc.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for _, m := range gc.ms {
			if gm := m.groups[g]; gm.node != nil && gm.node.Status().Role == coreconsensus.RoleLeader {
				return gm
			}
		}
	}
	gc.t.Fatalf("group %d elected no leader over the shared transports", g)
	return nil
}

// propose writes kv in group g on whichever node leads it now.
func (gc *groupCluster) propose(g coreconsensus.GroupID, kv string) coreconsensus.TokenValue {
	gc.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, _, err := gc.leader(g).node.Propose(ctx, []byte(kv))
		cancel()
		if err == nil {
			return tok
		}
		retry := errors.Is(err, coreconsensus.NotLeader) ||
			errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) || errs.HasCode(err, coreconsensus.CodeEntrySuperseded)
		if !retry || time.Now().After(deadline) {
			gc.t.Fatalf("group %d: propose %q: %v", g, kv, err)
		}
	}
}

// Two groups on one TCPTransport and one listener per machine elect,
// commit, snapshot and install independently, and a frame of a group no
// node bound is dropped and counted (#925). Bind kept one set of
// callbacks: the second node on a transport replaced the first's, which
// then heard nothing, and was handed every frame of both groups.
func TestTwoGroupsOnOneTransportElectCommitSnapshotAndInstallIndependently(t *testing.T) {
	gc := newGroupCluster(t)
	for _, g := range multiGroups {
		gc.propose(g, fmt.Sprintf("g%d=first", g))
	}
	lag := gc.ms[0]
	lagEnd := map[coreconsensus.GroupID]uint64{}
	for _, g := range multiGroups {
		_, lagEnd[g] = lag.groups[g].log.Bounds()
	}
	gc.stop(lag)
	toks := map[coreconsensus.GroupID]coreconsensus.TokenValue{}
	for i := range 120 {
		for _, g := range multiGroups {
			toks[g] = gc.propose(g, fmt.Sprintf("g%dk%d=%d", g, i, i))
		}
	}
	// Each group compacts its own log, the lagging member's end behind it.
	for _, g := range multiGroups {
		deadline := time.Now().Add(10 * time.Second)
		for {
			first, _ := gc.leader(g).log.Bounds()
			if first > lagEnd[g]+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("group %d never compacted past %d (first %d)", g, lagEnd[g], first)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	lag.tn.listen(t, gc.ca)
	gc.start(lag)
	for _, g := range multiGroups {
		gm := lag.groups[g]
		if err := gm.node.WaitApplied(context.Background(), toks[g]); err != nil {
			t.Fatalf("group %d: the restarted member never caught up: %v", g, err)
		}
		if v, _ := gm.sm.get(fmt.Sprintf("g%dk119", g)); v != "119" {
			t.Fatalf("group %d: g%dk119=%q on the restarted member", g, g, v)
		}
		if metas := must(gm.snaps.List())(t); len(metas) == 0 {
			t.Fatalf("group %d: the restarted member caught up without an install", g)
		}
		for k := range gm.sm.m {
			if !strings.HasPrefix(k, fmt.Sprintf("g%d", g)) {
				t.Fatalf("group %d applied %q, another group's write", g, k)
			}
		}
	}

	// A frame of group 3, which no node bound, is dropped and counted; the
	// two groups go on.
	from, to := gc.ms[1].tn, gc.ms[2].tn
	before := to.tr.unroutedFrames()
	frame := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 3, Kind: coreconsensus.MsgHeartbeat, From: from.id, To: to.id, Term: 1})
	for deadline := time.Now().Add(10 * time.Second); to.tr.unroutedFrames() == before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a frame of group 3 was never counted")
		}
		from.tr.Send(to.id, frame)
	}
	for _, g := range multiGroups {
		gc.propose(g, fmt.Sprintf("g%d=after", g))
	}
}

// A snapshot offer for a group the receiver does not run is refused
// PEER_REJECTED and counted, never handed to the node of another group.
func TestAnOfferForAnUnboundGroupIsRefusedAndCounted(t *testing.T) {
	gc := newGroupCluster(t)
	gc.leader(1)
	from, to := gc.ms[0].tn, gc.ms[1].tn
	before := to.tr.unroutedFrames()
	offer := coreconsensus.InstallOfferValue{Term: 1, SnapshotID: "s", Meta: coreconsensus.SnapshotMetaValue{ID: "s", Version: 1}}
	err := from.tr.streamGroup(context.Background(), 3, to.id, offer, func(int64) (io.ReadCloser, error) {
		t.Error("an offer the receiver refused was streamed")
		return nil, io.EOF
	})
	if !errs.HasCode(err, coreconsensus.CodePeerRejected) {
		t.Fatalf("an offer for group 3: %v, want PEER_REJECTED", err)
	}
	if to.tr.unroutedFrames() != before+1 {
		t.Fatalf("unrouted %d, want %d", to.tr.unroutedFrames(), before+1)
	}
}

// A lane that names no group — laneSnapshot, the JOIN lanes — is served to
// the only group bound, whatever its number, and with several to group 1,
// or to none (ADR 0244).
func TestALaneThatNamesNoGroupIsServedToTheLegacyGroup(t *testing.T) {
	type tc struct {
		name  string
		bound []coreconsensus.GroupID
		want  coreconsensus.GroupID
	}
	runCase := func(t *testing.T, c tc) {
		tr := &TCPTransport{groups: map[coreconsensus.GroupID]*binding{}}
		for _, g := range c.bound {
			tr.bindingOf(g)
		}
		got := tr.legacyBinding()
		if c.want == 0 {
			if got != nil {
				t.Fatal("served a group with no legacy group bound")
			}
			return
		}
		if got != tr.groups[c.want] {
			t.Fatalf("served another group than %d", c.want)
		}
	}
	for _, c := range []tc{
		{"one group 1", []coreconsensus.GroupID{1}, 1},
		{"one group 7", []coreconsensus.GroupID{7}, 7},
		{"groups 1 and 2", []coreconsensus.GroupID{2, 1}, 1},
		{"groups 2 and 3", []coreconsensus.GroupID{2, 3}, 0},
		{"none", nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
