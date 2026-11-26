package consensus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// retiredKeyCluster is a three-node cluster on steadyTiming, its leader's
// term settled, raised to protocol version v through its leader (none when
// v is 0).
func retiredKeyCluster(t *testing.T, v uint32) *cluster {
	t.Helper()
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{6, 4, 8}, size: 3, timing: steadyTiming()})
	c.settledLeader(5 * time.Second)
	if v != 0 {
		raiseNodesTo(t, liveNodes(c), v)
	}
	return c
}

// liveNodes are the nodes of c that run.
func liveNodes(c *cluster) []*Node {
	var out []*Node
	for _, tn := range c.nodes {
		if tn.node != nil {
			out = append(out, tn.node)
		}
	}
	return out
}

// joinRateReason is the refusal of a JOIN within its slot's rate window.
const joinRateReason = "one JOIN per slot per election timeout"

// joinOn is a JOIN of id on slot, its connection proving key, answered by
// whichever node of c leads, asked again while the slot's rate window
// refuses it.
func joinOn(t *testing.T, c *cluster, id coreconsensus.NodeID, slot string, key [32]byte) (errs.Code, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	peer := coreconsensus.AuthenticatedPeerValue{Slot: slot, SPKI: key}
	for {
		l := c.settledLeader(5 * time.Second)
		_, code, why := l.node.joinReason(ctx, peer, coreconsensus.JoinRequestValue{Cluster: c.cid, ID: id, Slot: slot})
		if why != joinRateReason || ctx.Err() != nil {
			return code, why
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// removeOn removes id through whichever node of c leads, and waits until
// every running node's configuration retired it.
func removeOn(t *testing.T, c *cluster, id coreconsensus.NodeID) {
	t.Helper()
	l := c.settledLeader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.node.Remove(ctx, "op:remove", id); err != nil {
		t.Fatalf("remove %d: %v", id, err)
	}
	for wait := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if !slices.ContainsFunc(liveNodes(c), func(n *Node) bool { return !n.Membership().IsRetired(id) }) {
			return
		}
		if time.Now().After(wait) {
			t.Fatalf("the removal of %d did not reach every node", id)
		}
	}
}

// entryReader is the part of a log store membershipVersions reads.
type entryReader interface {
	Bounds() (first, last uint64)
	Entries(lo, hi uint64, maxBytes int, dst []coreconsensus.EntryValue) ([]coreconsensus.EntryValue, error)
}

// membershipVersions are the payload versions of the membership entries
// log holds, in its order.
func membershipVersions(t *testing.T, log entryReader) []uint32 {
	t.Helper()
	first, last := log.Bounds()
	if last < first {
		return nil
	}
	es := must(log.Entries(first, last+1, 1<<30, nil))(t)
	var out []uint32
	for _, e := range es {
		if e.Kind == coreconsensus.EntryMembership {
			out = append(out, e.Version)
		}
	}
	return out
}

// A member removed once the cluster is at protocol 3 leaves its key in the
// configuration, and a JOIN that proves that key is refused under any new
// id, as is an AddLearner naming it: one key, one id, for the cluster's
// whole life. A host that comes back over the key file of a removed member
// is refused, and joins with a fresh key (#648). Before, it was admitted
// under its new id with the same key, and every node whose configuration
// still listed the removed member under that key refused it as leader.
func TestAJoinOnARemovedMembersKeyIsRefusedOnceTheProtocolIsRaised(t *testing.T) {
	c := retiredKeyCluster(t, raft.Protocol3)
	key, fresh := [32]byte{0x99}, [32]byte{0x9a}
	if code, why := joinOn(t, c, 991, "y", key); code != 0 {
		t.Fatalf("setup: the first JOIN of slot y was refused: %v %q", code, why)
	}
	removeOn(t, c, 991)
	for _, n := range liveNodes(c) {
		if !n.Membership().IsRetiredKey(key) {
			t.Fatalf("a node's configuration does not hold the removed member's key retired: %v", n.Membership().RetiredKeys)
		}
	}

	code, why := joinOn(t, c, 992, "y", key)
	if code != coreconsensus.CodeJoinRefused {
		t.Fatalf("a JOIN under a new id over the key removed member 991 held was answered %v %q: one key, one id", code, why)
	}
	if !strings.Contains(why, "key") {
		t.Fatalf("the refusal does not say the key is the cause: %q", why)
	}
	l := c.settledLeader(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := l.node.AddLearner(ctx, "op:add", coreconsensus.MemberValue{ID: 993, Slot: "y", SPKI: key, Address: "mem"})
	if !errors.Is(err, coreconsensus.MembershipInvalid) {
		t.Fatalf("AddLearner over a removed member's key: %v, want MEMBERSHIP_INVALID", err)
	}
	if code, why := joinOn(t, c, 994, "y", fresh); code != 0 {
		t.Fatalf("the JOIN of the same slot with a fresh key was refused: %v %q", code, why)
	}
	for _, tn := range c.nodes {
		if vs := membershipVersions(t, tn.log); slices.Contains(vs, wire.AdminVersion2) || !slices.Contains(vs, wire.AdminVersion3) {
			t.Fatalf("node %d: membership entries of payload versions %v at protocol 3", tn.id, vs)
		}
	}
}

// Below protocol 3 nothing changes and nothing new is written: the key of
// a removed member is admitted again under a new id, as it always was, no
// configuration keeps a retired key, and no membership entry is of payload
// version 3, which a node of protocol 2 cannot read (D18, #648).
func TestBelowProtocol3ARemovedMembersKeyIsAdmittedAsBefore(t *testing.T) {
	c := retiredKeyCluster(t, raft.Protocol2)
	key := [32]byte{0x99}
	if code, why := joinOn(t, c, 991, "y", key); code != 0 {
		t.Fatalf("setup: the first JOIN of slot y was refused: %v %q", code, why)
	}
	removeOn(t, c, 991)
	if code, why := joinOn(t, c, 992, "y", key); code != 0 {
		t.Fatalf("below protocol 3 a JOIN over a removed member's key was refused: %v %q", code, why)
	}
	for _, tn := range c.nodes {
		if keys := tn.node.Membership().RetiredKeys; len(keys) != 0 {
			t.Fatalf("node %d keeps retired keys below protocol 3: %v", tn.id, keys)
		}
		if vs := membershipVersions(t, tn.log); slices.Contains(vs, wire.AdminVersion3) {
			t.Fatalf("node %d: a membership entry of payload version 3 below protocol 3: %v", tn.id, vs)
		}
	}
}

// A snapshot cut at protocol 3 carries the retired keys, in a metadata of
// version 3 on disk, and a node that was down across the removal and
// catches up by installing it holds them: led by it, the cluster refuses
// the removed member's key, the log that retired it long compacted (#648).
func TestANodeThatInstallsASnapshotRefusesItsRetiredKeys(t *testing.T) {
	c, stores := fileSnapshotCluster(t)
	raiseNodesTo(t, liveNodes(c), raft.Protocol3)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	// Isolated before it stops, and what it was sent before is dropped: the
	// memory network keeps what it is sent while it is down and delivers it
	// at the restart, which catches it up by appends where the test means an
	// install (#374, #1564).
	end := stopBehind(c, lag)
	key := [32]byte{0x99}
	if code, why := joinOn(t, c, 991, "y", key); code != 0 {
		t.Fatalf("setup: JOIN: %v %q", code, why)
	}
	removeOn(t, c, 991)
	var tok coreconsensus.TokenValue
	for i := range 70 {
		tok = proposeOnLeader(t, c, fmt.Sprintf("k%d=%d", i, i))
	}
	// Past the entry after the stopped node's end, not only past its end:
	// a log that starts at end+1 still feeds it by appends, which a slower
	// runner left it to, and nothing was installed.
	compactedPast(t, c, end+1)
	c.net.Isolate(lag.id, false)
	c.start(lag)
	c.waitApplied(tok.Index, 10*time.Second)
	if !lag.node.Membership().IsRetiredKey(key) {
		t.Fatalf("the node that caught up does not hold the removed member's key retired: %v", lag.node.Membership().RetiredKeys)
	}
	s, dir := stores(lag.id)
	metas := must(s.List())(t)
	if len(metas) == 0 || !metas[0].Membership.IsRetiredKey(key) {
		t.Fatalf("the installed snapshot does not carry the retired key: %+v", metas)
	}
	if b := must(os.ReadFile(filepath.Join(dir, metas[0].ID+".meta")))(t); b[0] != 3 {
		t.Fatalf("a meta of a cluster at protocol 3 written in version %d", b[0])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for l = c.settledLeader(5 * time.Second); l != lag; l = c.settledLeader(5 * time.Second) {
		// A transfer races the fast timing's elections: asked again of
		// whichever node leads until the node that installed leads.
		if err := l.node.TransferLeadership(ctx, "op:transfer", lag.id); err != nil && ctx.Err() != nil {
			t.Fatalf("transfer to the node that installed the snapshot: %v", err)
		}
	}
	err := lag.node.AddLearner(ctx, "op:add", coreconsensus.MemberValue{ID: 993, Slot: "y", SPKI: key, Address: "mem"})
	if !errors.Is(err, coreconsensus.MembershipInvalid) {
		t.Fatalf("the leader that installed the snapshot admitted its retired key: %v", err)
	}
}

// stopBehind stops tn, cuts it off and drops the frames its end of the
// network still queues, then returns the end of its log, which the caller
// waits for the live logs to start past. Cut and dropped after the stop,
// nothing is queued for it that its end did not count (#1564).
func stopBehind(c *cluster, tn *testNode) uint64 {
	//: a node already stopped is not stopped twice.
	if tn.node != nil {
		c.kill(tn)
	}
	stopHearing(c, tn)
	_, end := tn.log.Bounds()
	return end
}

// #1564: a node stopped isolated was still owed what the leader sent it
// before the cut and the node had not read: the memory network keeps it and
// delivers it at the restart, past the end the setup read, so a log that
// started just past that end fed the node by appends and nothing was
// installed. The frame is queued while the node is stopped and not yet
// isolated, as one the node had not read when it stopped is.
func TestANodeStoppedBehindIsOwedNothingSentBeforeItsCut(t *testing.T) {
	type tc struct{ name string }
	runCase := func(t *testing.T, _ tc) {
		c := retiredKeyCluster(t, raft.Protocol3)
		lag := c.otherThan(c.settledLeader(5 * time.Second))
		c.kill(lag)
		//: an entry the stopped node was sent and never read.
		tok := proposeOnLeader(t, c, "owed=1")
		end := stopBehind(c, lag)
		tr := NewMemoryTransport(c.net, lag.id)
		//: a frame waiting for the node is delivered at its restart.
		if data, ctrl := len(tr.data), len(tr.ctrl); data+ctrl != 0 {
			t.Fatalf("%d frames on the data lane and %d on the control lane wait for the stopped node, whose end %d the setup read: the entry %d they carry is delivered at its restart, so a log starting at %d feeds it by appends", data, ctrl, end, tok.Index, end+1)
		}
	}
	for _, tt := range []tc{{name: "an entry sent before the cut"}} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}

// A Send routed before its destination is cut off queues nothing once the
// cut and the drain of stopHearing are done: it took its destination under
// the network's lock, then queued the frame after releasing it, so a Send
// already past the route queued its frame after the drain, and the node
// restarted reading entries past the end stopBehind returned. The sender is
// held, with nothing but its own lock, between the two: the admission
// model, on, reads the sender's admitted members under that lock (#1564).
func TestASendRoutedBeforeACutQueuesNothingAfterTheDrain(t *testing.T) {
	type tc struct {
		name string
		kind coreconsensus.MessageKind
	}
	runCase := func(t *testing.T, tcase tc) {
		net := NewMemoryNetwork()
		net.admission = true
		from := NewMemoryTransport(net, 1)
		to := &testNode{id: 2}
		dst := NewMemoryTransport(net, to.id)
		from.SetMembership(1, []coreconsensus.MemberValue{{ID: to.id, SPKI: memSPKI(to.id)}})
		m := coreconsensus.MessageValue{Version: 1, Group: 1, Kind: tcase.kind, From: 1, To: to.id, Term: 3}
		frame := wire.AppendMessage(nil, &m)
		from.mu.Lock()
		sent := make(chan struct{})
		go func() {
			defer close(sent)
			from.Send(to.id, frame)
		}()
		blockedInSend(t)
		stopHearing(&cluster{net: net}, to)
		from.mu.Unlock()
		<-sent
		//: a frame queued now is delivered at the node's restart.
		if data, ctrl := len(dst.data), len(dst.ctrl); data+ctrl != 0 {
			t.Fatalf("%d frames on the data lane and %d on the control lane were queued for node %d after its cut and drain, by a Send routed before them: delivered at its restart, past the end stopBehind read", data, ctrl, to.id)
		}
	}
	for _, tt := range []tc{
		{name: "an append on the data lane", kind: coreconsensus.MsgApp},
		{name: "a heartbeat on the control lane", kind: coreconsensus.MsgHeartbeat},
	} {
		t.Run(tt.name, func(t *testing.T) { runCase(t, tt) })
	}
}

// blockedInSend waits until a goroutine is parked on a mutex inside
// MemoryTransport.Send, read from the goroutines' stacks: the sender holds
// no clock and signals nothing, and its stack is the one place that says it
// reached the lock.
func blockedInSend(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(10 * time.Second); ; runtime.Gosched() {
		n := runtime.Stack(buf, true)
		//: a goroutine parked on a mutex with Send on its stack.
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			//: the sender is parked on the lock the test holds.
			if strings.Contains(g, "sync.Mutex.Lock") && strings.Contains(g, "(*MemoryTransport).Send(") {
				return
			}
		}
		//: the sender never reached the lock.
		if time.Now().After(deadline) {
			t.Fatal("setup: the Send never parked on the sender's lock")
		}
	}
}

// keyReuseRig is a node S and a leader L, which rejoined slot "y" as member
// 9 over a fresh key once the JOIN over the key K member 5 held there was
// refused: S's configuration may still list 5 under K.
type keyReuseRig struct {
	s, l      *tcpNode
	old       [32]byte
	delivered atomic.Int64
	frame     []byte
}

func newKeyReuseRig(t *testing.T) *keyReuseRig {
	t.Helper()
	ca := newTestCA(t)
	cid := coreconsensus.ClusterID{0x5d, 0x4b}
	r := &keyReuseRig{s: &tcpNode{id: 1, slot: "s"}, l: &tcpNode{id: 9, slot: "y"}}
	r.s.cert, r.s.spki = ca.issue(t, SAN(cid, "s"), nil)
	r.l.cert, r.l.spki = ca.issue(t, SAN(cid, "y"), nil)
	_, r.old = ca.issue(t, SAN(cid, "y"), must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))(t))
	if r.old == r.l.spki {
		t.Fatal("setup: the fresh key is the old one")
	}
	r.s.listen(t, ca)
	r.l.listen(t, ca)
	t.Cleanup(func() { _ = r.s.tr.Close(); _ = r.l.tr.Close() })
	if err := r.s.tr.Bind(1, func(coreconsensus.AuthenticatedPeerValue, []byte) { r.delivered.Add(1) }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	r.l.tr.SetMembership(1, []coreconsensus.MemberValue{r.sMember(), r.lMember()})
	m := coreconsensus.MessageValue{Version: 1, Group: 1, Kind: coreconsensus.MsgHeartbeat, From: 9, To: 1, Term: 5}
	r.frame = wire.AppendMessage(nil, &m)
	return r
}

func (r *keyReuseRig) sMember() coreconsensus.MemberValue {
	return coreconsensus.MemberValue{ID: r.s.id, Slot: r.s.slot, SPKI: r.s.spki, Address: r.s.addr, Voter: true}
}

func (r *keyReuseRig) lMember() coreconsensus.MemberValue {
	return coreconsensus.MemberValue{ID: 9, Slot: "y", SPKI: r.l.spki, Address: r.l.addr, Voter: true}
}

// removed is member 5 as S's stale configuration lists it: slot "y", the
// old key K.
func (r *keyReuseRig) removed() coreconsensus.MemberValue {
	return coreconsensus.MemberValue{ID: 5, Slot: "y", SPKI: r.old, Address: r.l.addr, Voter: true}
}

// sendUntilDelivered sends L's heartbeat to S every 50 ms until S delivered
// one, or for d, and returns how many S delivered meanwhile.
func (r *keyReuseRig) sendUntilDelivered(d time.Duration) int64 {
	before := r.delivered.Load()
	for end := time.Now().Add(d); time.Now().Before(end) && r.delivered.Load() == before; time.Sleep(50 * time.Millisecond) {
		r.l.tr.Send(r.s.id, r.frame)
	}
	return r.delivered.Load() - before
}

// The issue's stale voter over two real transports (#648). Once the JOIN
// over a removed member's key is refused, the leader that rejoined the
// slot holds a fresh key: the admission set the stale voter publishes
// after the leader record lists the removed member under the old key and
// the leader under the new one, and the transport delivers the leader's
// frames. Under one key the transport admitted neither (review M5), and
// the voter stayed frozen for as long as that leader led.
func TestAStaleVoterHearsALeaderThatRejoinedOverAFreshKey(t *testing.T) {
	r := newKeyReuseRig(t)
	r.s.tr.SetMembership(1, []coreconsensus.MemberValue{r.sMember(), r.removed(), r.lMember()})
	got := r.sendUntilDelivered(3 * time.Second)
	t.Logf("vouched-for leader beside the removed member, each under its own key: %d frames delivered", got)
	if got == 0 {
		t.Fatal("the vouched-for leader's frames never reach the stale voter")
	}
}

// The issue's joiner (#648): its JOIN answer lists the leader, under its
// fresh key, and its replay reached a configuration that still lists the
// removed member 5 under the old key — an older snapshot, or the log
// between AddLearner(5) and Remove(5). It keeps hearing the leader that
// feeds it, the Remove included.
func TestAJoinerReplayingARemovedMemberHearsTheLeaderOfItsJoinAnswer(t *testing.T) {
	r := newKeyReuseRig(t)
	r.s.tr.SetMembership(joinGroup, []coreconsensus.MemberValue{r.sMember(), r.lMember()})
	if n := r.sendUntilDelivered(3 * time.Second); n == 0 {
		t.Fatal("setup: the JOIN answer alone does not admit L")
	}
	r.s.tr.SetMembership(1, []coreconsensus.MemberValue{r.sMember(), r.removed()})
	time.Sleep(200 * time.Millisecond)
	got := r.sendUntilDelivered(3 * time.Second)
	t.Logf("joiner whose replay lists removed member 5 under the old key: %d frames delivered", got)
	if got == 0 {
		t.Fatal("the joiner stops hearing the leader that feeds it once its replay lists member 5")
	}
}

// onDiskLeader runs op on whichever node of nodes leads, again while none
// leads or a membership change is still pending.
func onDiskLeader(t *testing.T, nodes []*diskNode, what string, op func(context.Context, *Node) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		var err error = coreconsensus.NotLeader
		for _, dn := range nodes {
			if dn.node.Status().Role == coreconsensus.RoleLeader {
				err = op(ctx, dn.node)
				break
			}
		}
		if err == nil {
			return
		}
		if (!errors.Is(err, coreconsensus.NotLeader) && !errors.Is(err, coreconsensus.MembershipChangePending)) || ctx.Err() != nil {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Recover in a history at protocol 3 keeps the retired keys of the
// configuration it forces, as it keeps the retired IDs, in an entry of
// payload version 3: the survivor that leads the new history refuses the
// key of a member removed in the old one (#648).
func TestRecoverKeepsTheRetiredKeysOfAHistoryAtProtocol3(t *testing.T) {
	c, nodes := threeOnDisk(t, 10)
	live := make([]*Node, 0, len(nodes))
	for _, dn := range nodes {
		live = append(live, dn.node)
	}
	raiseNodesTo(t, live, raft.Protocol3)
	key := [32]byte{0x99}
	onDiskLeader(t, nodes, "add learner 991", func(ctx context.Context, n *Node) error {
		return n.AddLearner(ctx, "op:add", coreconsensus.MemberValue{ID: 991, Slot: "y", SPKI: key, Address: "mem"})
	})
	onDiskLeader(t, nodes, "remove 991", func(ctx context.Context, n *Node) error { return n.Remove(ctx, "op:remove", 991) })
	tok := proposeOnDisk(t, nodes, "after=remove")
	c.waitAppliedDisk(nodes, tok.Index)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	recoverAlone(t, c, nodes, keep, coreconsensus.ClusterID{9})
	defer keep.crash(t, c)
	if m := keep.node.Membership(); !m.IsRetiredKey(key) || !m.IsRetired(991) {
		t.Fatalf("the recovered configuration lost the old history's retired member: %+v", m)
	}
	if vs := membershipVersions(t, keep.dd.Log()); len(vs) == 0 || vs[len(vs)-1] != wire.AdminVersion3 {
		t.Fatalf("the forced entry of a history at protocol 3 is not of payload version 3: %v", vs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := keep.node.AddLearner(ctx, "op:add", coreconsensus.MemberValue{ID: 993, Slot: "y", SPKI: key, Address: "mem"})
	if !errors.Is(err, coreconsensus.MembershipInvalid) {
		t.Fatalf("the recovered history admitted a key the old one retired: %v", err)
	}
}

// RestoreSnapshot's new history keeps the snapshot's retired keys beside
// its retired IDs, under the new members (#648).
func TestARestoredSnapshotKeepsItsRetiredKeys(t *testing.T) {
	exp := coreconsensus.SnapshotMetaValue{
		Position: coreconsensus.PositionValue{Term: 2, Index: 20}, ClusterVersion: raft.Protocol3,
		Membership: coreconsensus.MembershipValue{Retired: []coreconsensus.NodeID{5}, RetiredKeys: [][32]byte{{0x99}}},
	}
	members := []coreconsensus.MemberValue{{ID: 1, SPKI: [32]byte{1}, Voter: true}}
	got := restoredMeta(&exp, coreconsensus.ClusterID{9}, members).Membership
	if !got.IsRetired(5) || !got.IsRetiredKey([32]byte{0x99}) || len(got.Members) != 1 {
		t.Fatalf("restored configuration %+v, want the new members with the retired ID and key", got)
	}
}
