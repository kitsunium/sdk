package consensus

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The run of BenchmarkACommitWhileASnapshotStreams: a state of
// scLasts × rate bytes, in values of scValue bytes, so that its stream
// outlasts the scWindow measured under it.
const (
	scWindow = 4 * time.Second
	scLasts  = 6
	scValue  = 4 << 10
)

// BenchmarkACommitWhileASnapshotStreams is #920's third criterion (#1365):
// the data lane's commit p99 at 1 000 proposals/s of 256 B, three voters
// over loopback mutual TLS with memory logs, in three windows: with no
// stream; while a fourth node that joined as a learner is streamed a
// snapshot paced at the rate the sub-benchmark names
// (SnapshotStreamBytesPerSecond), the stream in flight from the window's
// first proposal to its last; and once the learner installed it and
// follows. One op is one run of a new cluster. The run reports the three
// p99s and the ratio of the second to the first, which #920 bounds at
// 1.10; -benchtime=1x -count=N gives N runs. The four nodes share this
// process and its GOMAXPROCS: the learner's receiving side is on the
// leader's CPUs, as it is not in a deployment.
func BenchmarkACommitWhileASnapshotStreams(b *testing.B) {
	for _, rate := range []int64{2 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("rate=%dMiB", rate>>20), func(b *testing.B) {
			var base, under, after, ratios []float64
			for range b.N {
				p := scRun(b, rate)
				base, under, after = append(base, p[0]), append(under, p[1]), append(after, p[2])
				ratios = append(ratios, p[1]/p[0])
				b.Logf("run: baseline p99 %.3f ms, streaming %.3f ms, learner following %.3f ms, ratio %.3f",
					p[0]*1e3, p[1]*1e3, p[2]*1e3, p[1]/p[0])
			}
			b.ReportMetric(ltQuantile(base, 0.5)*1e3, "baseline-p99-ms")
			b.ReportMetric(ltQuantile(under, 0.5)*1e3, "streaming-p99-ms")
			b.ReportMetric(ltQuantile(after, 0.5)*1e3, "following-p99-ms")
			b.ReportMetric(ltQuantile(ratios, 0.5), "ratio")
		})
	}
}

// scRun is one run: a cluster loaded with the state and compacted past
// its first entry on every voter, a baseline window, a learner's JOIN and
// a window while the leader streams it the snapshot, then a window once
// the learner follows. It returns the three windows' commit p99, in
// seconds.
func scRun(b *testing.B, rate int64) [3]float64 {
	b.Helper()
	keys := int(rate * scLasts / scValue)
	ca := newLTAuthority(b)
	ns, _ := ltCluster(b, ca, 3, func(cfg *Config) {
		// One cut once the state is loaded, none in the windows, whose
		// 12 200 entries of 256 B stay below the bytes that trigger it.
		cfg.SnapshotEvery, cfg.SnapshotEveryBytes, cfg.SnapshotTrailing = 1<<20, int64(keys)*scValue, 4
		cfg.SnapshotStreamBytesPerSecond = rate
	})
	defer ltStop(b, ns)
	l := ns[ltLeader(b, ns)]
	scLoad(b, l.node, keys)
	for _, n := range ns {
		scCompacted(b, n.node)
	}
	var p [3]float64
	for i := range ltWarmup {
		if _, _, err := l.node.Propose(context.Background(), ltEntry(i)); err != nil {
			b.Fatalf("warm-up proposal %d: %v", i, err)
		}
	}
	p[0] = scWindowP99(b, l.node, "baseline")
	j, id := scJoin(b, ca, ns)
	defer ltStop(b, []*ltNode{j})
	scStreaming(b, l.node, id)
	p[1] = scWindowP99(b, l.node, "streaming")
	if sent, size := scStream(l.node, id); sent == 0 || sent >= size {
		b.Fatalf("the stream was not in flight at the window's end: %d of %d bytes", sent, size)
	}
	scFollows(b, l.node, j.node)
	p[2] = scWindowP99(b, l.node, "following")
	return p
}

// scWindowP99 proposes to the leader l at ltRate for scWindow and returns
// the commit p99, in seconds; a failed proposal or a change of leader
// fails the run.
func scWindowP99(b *testing.B, l *Node, window string) float64 {
	b.Helper()
	term := l.Status().Term
	commits, failed := ltPace(l, scWindow)
	if failed > 0 {
		b.Fatalf("%s: %d proposals failed", window, failed)
	}
	for s := 0; s < len(commits); s += ltRate {
		b.Logf("%s: second %d: p99 %.3f ms, max %.3f ms", window, s/ltRate, ltQuantile(commits[s:s+ltRate], 0.99)*1e3, slices.Max(commits[s:s+ltRate])*1e3)
	}
	if st := l.Status(); st.Term != term || st.Role != coreconsensus.RoleLeader {
		b.Fatalf("%s: the leader changed: term %d -> %d, role %v", window, term, st.Term, st.Role)
	}
	return ltQuantile(commits, 0.99)
}

// scFollows waits until the learner j installed the snapshot and applied
// what the leader l committed.
func scFollows(b *testing.B, l, j *Node) {
	b.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if j.Status().Applied >= l.Status().Commit {
			return
		}
	}
	b.Fatalf("the learner did not follow within a minute: %+v", j.Status())
}

// scLoad writes keys values of scValue random bytes through n, 64 at a
// time: random, so that the snapshot's compression (#917) leaves its size.
func scLoad(b *testing.B, n *Node, keys int) {
	b.Helper()
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for w := range 64 {
		wg.Go(func() {
			for k := w; k < keys; k += 64 {
				e := fmt.Appendf(make([]byte, 0, scValue+16), "s%d=", k)
				v := make([]byte, scValue)
				_, _ = rand.NewChaCha8([32]byte{byte(k), byte(k >> 8)}).Read(v)
				e = append(e, v...)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, _, err := n.Propose(ctx, e)
				cancel()
				if err != nil {
					errc <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errc)
	if err := <-errc; err != nil {
		b.Fatalf("loading the state: %v", err)
	}
}

// scCompacted waits until n's log no longer starts at its first entry: n
// cut its snapshot, and a node that joins needs it.
func scCompacted(b *testing.B, n *Node) {
	b.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if first, _ := n.cfg.Log.Bounds(); first > 1 {
			return
		}
	}
	b.Fatal("the leader's log was not compacted within thirty seconds")
}

// scJoin starts a fourth node and JOINs it as a learner through the
// members, and returns it with its ID.
func scJoin(b *testing.B, ca *ltAuthority, ns []*ltNode) (*ltNode, coreconsensus.NodeID) {
	b.Helper()
	const slot = "site-learner"
	cert, spki := ca.issue(b, SAN(ltCID, slot))
	tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0"})
	if err != nil {
		b.Fatalf("listen %s: %v", slot, err)
	}
	j := &ltNode{tr: tr, spki: spki}
	log := NewMemoryLog()
	id, _, err := InitLog(log, ltCID, slot)
	if err != nil {
		b.Fatal(err)
	}
	j.node, err = NewNode(Config{
		Cluster: ltCID, Machine: newKV(), Durability: DurabilityVolatile, Log: log, Snapshots: NewMemorySnapshots(),
		Transport: tr, InitialTiming: TimingDefaults(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		MinVoters: 3, JoinAuthorizer: allowJoin{},
	})
	if err != nil {
		b.Fatal(err)
	}
	st, err := log.Stamp()
	if err != nil {
		b.Fatal(err)
	}
	addrs, pins := make([]string, len(ns)), make([][32]byte, len(ns))
	for i, n := range ns {
		addrs[i], pins[i] = n.tr.Addr(), n.spki
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := tr.Join(ctx, coreconsensus.JoinConfig{Addresses: addrs, Stamp: st, Address: tr.Addr(), Pins: pins}); err != nil {
		b.Fatalf("join: %v", err)
	}
	return j, id
}

// scStreaming waits until the leader l streams a snapshot to the peer to.
func scStreaming(b *testing.B, l *Node, to coreconsensus.NodeID) {
	b.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if sent, _ := scStream(l, to); sent > 0 {
			return
		}
	}
	b.Fatal("no snapshot stream to the learner within thirty seconds")
}

// scStream is the leader l's stream to the peer to: the bytes sent and the
// snapshot's encoded size, both 0 with no stream.
func scStream(l *Node, to coreconsensus.NodeID) (sent, size int64) {
	for _, p := range l.Health().Peers {
		if p.ID == to {
			return p.SnapshotSent, p.SnapshotSize
		}
	}
	return 0, 0
}
