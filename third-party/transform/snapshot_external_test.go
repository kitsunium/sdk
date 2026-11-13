package transform_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/service/app/consensus"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// The consensus engine's snapshots read with this module's zstd (#917):
// consensus links no vendor, so a node runs zstd once the product imports
// this package and sets Config.SnapshotCodec to it, and every node that
// links it reads the snapshots whose metadata name "zstd". A node that does
// not link it stops VERSION_UNSUPPORTED on such a snapshot, which the
// consensus suite proves, in a binary that cannot link zstd
// (TestAFollowerThatDoesNotLinkTheLeadersCodecStops).

// paddedState is a consensus state machine whose snapshot is its applied
// count followed by pad bytes of the JSON corpus: what is measured is the
// snapshot's cut and install, not the machine. Restore reads the whole
// stream and checks its length.
type paddedState struct {
	mu      sync.Mutex
	applied uint64
	pad     int64
	corpus  []byte
}

func newPaddedState(pad int64, corpus []byte) *paddedState {
	return &paddedState{pad: pad, corpus: corpus}
}

func (s *paddedState) Apply(coreconsensus.EntryValue) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied++
	return s.applied
}

func (s *paddedState) count() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied
}

// paddedSnapshot writes the count, then pad bytes cycled from the corpus.
type paddedSnapshot struct {
	applied uint64
	pad     int64
	corpus  []byte
}

func (p paddedSnapshot) WriteTo(w io.Writer) (int64, error) {
	var head [8]byte
	binary.LittleEndian.PutUint64(head[:], p.applied)
	n, err := w.Write(head[:])
	total := int64(n)
	for left := p.pad; err == nil && left > 0; {
		chunk := p.corpus[:min(int64(len(p.corpus)), left)]
		n, err = w.Write(chunk)
		total += int64(n)
		left -= int64(n)
	}
	return total, err
}

func (paddedSnapshot) Release() {}

func (s *paddedState) Snapshot() (coreconsensus.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return paddedSnapshot{applied: s.applied, pad: s.pad, corpus: s.corpus}, nil
}

func (s *paddedState) Restore(r io.Reader, _ coreconsensus.SnapshotMetaValue) error {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	if n != s.pad {
		return fmt.Errorf("restored %d bytes of pad, want %d", n, s.pad)
	}
	s.mu.Lock()
	s.applied = binary.LittleEndian.Uint64(head[:])
	s.mu.Unlock()
	return nil
}

// snapNode is one node of a memory cluster and the stores that outlive it.
type snapNode struct {
	id    coreconsensus.NodeID
	log   *consensus.MemoryLog
	snaps *consensus.MemorySnapshots
	sm    *paddedState
	node  *consensus.Node
}

// snapCluster is a three-node memory cluster whose every node cuts and
// reads its snapshots with codec.
type snapCluster struct {
	tb     testing.TB
	net    *consensus.MemoryNetwork
	cid    coreconsensus.ClusterID
	codec  coretransform.BoundedDecompressor
	every  uint64
	pad    int64
	corpus []byte
	nodes  []*snapNode
}

func (c *snapCluster) config(n *snapNode) consensus.Config {
	return consensus.Config{
		Cluster: c.cid, Machine: n.sm, Durability: consensus.DurabilityVolatile, Log: n.log, Snapshots: n.snaps,
		Transport: consensus.NewMemoryTransport(c.net, n.id),
		InitialTiming: coreconsensus.TimingContractSpec{
			Generation: 1, HeartbeatInterval: 20 * time.Millisecond,
			ElectionTimeoutMin: 300 * time.Millisecond, ElectionTimeoutMax: 600 * time.Millisecond, MaxClockDrift: 0.05,
		},
		MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		SnapshotEvery: c.every, SnapshotTrailing: 4, SnapshotCodec: c.codec,
	}
}

// newSnapCluster bootstraps and starts the three nodes.
func newSnapCluster(tb testing.TB, codec coretransform.BoundedDecompressor, every uint64, pad int64) *snapCluster {
	tb.Helper()
	c := &snapCluster{
		tb: tb, net: consensus.NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 1, 7},
		codec: codec, every: every, pad: pad, corpus: buildJSONCorpus(4 << 20),
	}
	var members []coreconsensus.MemberValue
	for i := range 3 {
		n := &snapNode{log: consensus.NewMemoryLog(), snaps: consensus.NewMemorySnapshots()}
		slot := fmt.Sprintf("slot-%d", i)
		id, inc, err := consensus.InitLog(n.log, c.cid, slot)
		if err != nil {
			tb.Fatal(err)
		}
		n.id = id
		c.nodes = append(c.nodes, n)
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: consensus.MemorySPKI(id), Voter: true})
	}
	for _, n := range c.nodes {
		n.sm = newPaddedState(pad, c.corpus)
		cfg := c.config(n)
		if err := consensus.Bootstrap(context.Background(), &cfg, members); err != nil {
			tb.Fatal(err)
		}
	}
	for _, n := range c.nodes {
		c.start(n)
	}
	tb.Cleanup(func() {
		for _, n := range c.nodes {
			if n.node != nil {
				_ = n.node.HardClose()
			}
		}
	})
	return c
}

func (c *snapCluster) start(n *snapNode) {
	c.tb.Helper()
	n.sm = newPaddedState(c.pad, c.corpus)
	node, err := consensus.NewNode(c.config(n))
	if err != nil {
		c.tb.Fatalf("NewNode: %v", err)
	}
	n.node = node
}

// kill isolates n, so the network keeps no frame for it, then stops it at
// once: restarted, it catches up from what the leader sends it then, never
// from the appends sent while it was down.
func (c *snapCluster) kill(n *snapNode) {
	c.tb.Helper()
	c.net.Isolate(n.id, true)
	if err := n.node.HardClose(); err != nil {
		c.tb.Fatalf("hard close: %v", err)
	}
	if err := consensus.NewMemoryTransport(c.net, n.id).Close(); err != nil {
		c.tb.Fatalf("close transport: %v", err)
	}
	n.node = nil
}

// restart starts the killed n again, its links restored: what reaches it is
// what the leader sends it from now on.
func (c *snapCluster) restart(n *snapNode) {
	c.tb.Helper()
	c.net.Isolate(n.id, false)
	c.start(n)
}

// leader waits for a leader that committed an entry of its own term.
func (c *snapCluster) leader() *snapNode {
	c.tb.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range c.nodes {
			if n.node == nil || n.node.Status().Role != coreconsensus.RoleLeader {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _, err := n.node.Propose(ctx, []byte("settle"))
			cancel()
			if err == nil {
				return n
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.tb.Fatal("no leader")
	return nil
}

func (c *snapCluster) other(not ...*snapNode) *snapNode {
	for _, n := range c.nodes {
		taken := false
		for _, x := range not {
			taken = taken || n == x
		}
		if !taken {
			return n
		}
	}
	return nil
}

// propose proposes count entries on l, retried while the leader is busy.
func (c *snapCluster) propose(l *snapNode, count int) coreconsensus.TokenValue {
	c.tb.Helper()
	var tok coreconsensus.TokenValue
	for i := 0; i < count; {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t, _, err := l.node.Propose(ctx, fmt.Appendf(nil, "e%d", i))
		cancel()
		if err != nil {
			if errors.Is(err, coreconsensus.Overloaded) {
				time.Sleep(time.Millisecond)
				continue
			}
			c.tb.Fatalf("propose: %v", err)
		}
		tok, i = t, i+1
	}
	return tok
}

// waitApplied waits for n to apply index.
func (c *snapCluster) waitApplied(n *snapNode, index uint64, timeout time.Duration) {
	c.tb.Helper()
	deadline := time.Now().Add(timeout)
	for n.node.Status().Applied < index {
		if r, stopped := n.node.StopReason(); stopped {
			c.tb.Fatalf("the node stopped: %+v", r)
		}
		if time.Now().After(deadline) {
			c.tb.Fatalf("applied %d < %d", n.node.Status().Applied, index)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// writeUntilCompactedPast proposes on l, a round of every entries at a
// time, until l's log starts past index: a cut refused for a moment waits a
// second before the next, and only an applied entry asks for one again.
func (c *snapCluster) writeUntilCompactedPast(l *snapNode, index uint64) coreconsensus.TokenValue {
	c.tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		tok := c.propose(l, int(c.every))
		if first, _ := l.log.Bounds(); first > index+1 {
			return tok
		}
		if time.Now().After(deadline) {
			first, last := l.log.Bounds()
			c.tb.Fatalf("the leader's log [%d, %d] never started past %d", first, last, index)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// zstdCodec is the registered zstd, as a product sets Config.SnapshotCodec.
func zstdCodec(tb testing.TB) coretransform.BoundedDecompressor {
	tb.Helper()
	codec, ok := tptransform.Zstd.(coretransform.BoundedDecompressor)
	if !ok {
		tb.Fatal("the registered zstd is not a BoundedDecompressor")
	}
	return codec
}

// flateCodec is consensus's default snapshot codec.
func flateCodec(tb testing.TB) coretransform.BoundedDecompressor {
	tb.Helper()
	codec, ok := svctransform.FlateCompressor.(coretransform.BoundedDecompressor)
	if !ok {
		tb.Fatal("flate is not a BoundedDecompressor")
	}
	return codec
}

// TestAClusterInstallsAZstdSnapshot is #917's second acceptance line, its
// half that needs the codec linked: a cluster whose leader cuts its
// snapshots with zstd installs one on a lagging follower configured with
// zstd, the metadata naming "zstd", the state of several chunks restored
// whole.
func TestAClusterInstallsAZstdSnapshot(t *testing.T) {
	c := newSnapCluster(t, zstdCodec(t), 20, 4<<20)
	l := c.leader()
	lag := c.other(l)
	c.kill(lag)
	_, behind := lag.log.Bounds()
	tok := c.writeUntilCompactedPast(l, behind+100)
	metas, err := l.snaps.List()
	if err != nil || len(metas) == 0 || metas[0].Codec != string(tptransform.ZstdAlgorithm) {
		t.Fatalf("the leader's snapshots: %+v (%v), want them cut with zstd", metas, err)
	}
	c.restart(lag)
	c.waitApplied(lag, tok.Index, 30*time.Second)
	installed, err := lag.snaps.List()
	if err != nil || len(installed) == 0 || installed[0].Codec != string(tptransform.ZstdAlgorithm) {
		t.Fatalf("the follower's snapshots: %+v (%v), want the zstd one installed", installed, err)
	}
	if installed[0].DecodedSize <= c.pad || lag.sm.count() == 0 {
		t.Fatalf("the follower restored %d decoded bytes and applied %d entries, want the %d-byte state", installed[0].DecodedSize, lag.sm.count(), c.pad)
	}
	t.Logf("installed %s: %d bytes decoded from %d encoded", installed[0].ID, installed[0].DecodedSize, installed[0].EncodedSize)
}
