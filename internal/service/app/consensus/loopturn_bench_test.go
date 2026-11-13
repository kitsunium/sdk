package consensus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// The paced run of BenchmarkALeaderLoopTurnAt1000PerSecond: D16's rate and
// entry size, for long enough that the p99 rests on some fifty turns.
const (
	ltRate     = 1000
	ltDuration = 5 * time.Second
	ltPayload  = 256
	ltWarmup   = 200
)

// BenchmarkALeaderLoopTurnAt1000PerSecond is #912's second criterion
// (#1364): consensus_loop_turn_seconds on the leader, at 1 000 proposals/s
// of 256 B, three and five nodes over loopback mutual TLS with memory logs,
// so a turn is the loop's work and not a disk's. The leader's Meter keeps
// every observation of the series raw, and the run reports their p50, p99
// and maximum; one op is one paced run of ltDuration, so -benchtime=1x
// -count=N gives N runs. The file uses nothing lot L1 added (the "before"
// side is this file copied onto main before the lot, a7eb89f0d): TCP so
// that the leader takes the BatchSender path of #912, which the memory
// transport does not implement.
func BenchmarkALeaderLoopTurnAt1000PerSecond(b *testing.B) {
	for _, nodes := range []int{3, 5} {
		b.Run(fmt.Sprintf("followers=%d", nodes-1), func(b *testing.B) {
			var turns, commits []float64
			for range b.N {
				t, c := ltRun(b, nodes)
				turns = append(turns, t...)
				commits = append(commits, c...)
			}
			b.ReportMetric(ltQuantile(turns, 0.50)*1e6, "turn-p50-us")
			b.ReportMetric(ltQuantile(turns, 0.99)*1e6, "turn-p99-us")
			b.ReportMetric(slices.Max(turns)*1e6, "turn-max-us")
			b.ReportMetric(float64(len(turns))/float64(b.N), "turns/run")
			b.ReportMetric(ltQuantile(commits, 0.99)*1e3, "commit-p99-ms")
		})
	}
}

// ltRun starts a cluster of nodes over TCP, warms its leader up, then
// proposes at ltRate for ltDuration, each proposal on its own goroutine at
// its due time so that a slow commit never slows the pace. It returns the
// leader's loop turns of the paced window, in seconds, and each proposal's
// latency from its due time, in seconds; a leadership change or a failed
// proposal fails the run.
func ltRun(b *testing.B, nodes int) (turns, commits []float64) {
	b.Helper()
	ns, meters := ltCluster(b, newLTAuthority(b), nodes, nil)
	defer ltStop(b, ns)
	leader := ltLeader(b, ns)
	ctx := context.Background()
	for i := range ltWarmup {
		if _, _, err := ns[leader].node.Propose(ctx, ltEntry(i)); err != nil {
			b.Fatalf("warm-up proposal %d: %v", i, err)
		}
	}
	term := ns[leader].node.Status().Term
	m := meters[leader]
	m.on.Store(true)
	commits, failed := ltPace(ns[leader].node, ltDuration)
	m.on.Store(false)
	if st := ns[leader].node.Status(); st.Term != term || st.Role != coreconsensus.RoleLeader {
		b.Fatalf("the leader changed during the paced run: term %d -> %d, role %v", term, st.Term, st.Role)
	}
	if failed > 0 {
		b.Fatalf("%d proposals failed during the paced run", failed)
	}
	return m.taken(), commits
}

// ltPace proposes to n at ltRate for d, each proposal on its own goroutine
// at its due time so that a slow commit never slows the pace, and returns
// each proposal's latency from its due time, in seconds and in the order
// they were due, and how many failed (their latency left at 0).
func ltPace(n *Node, d time.Duration) (commits []float64, failed int64) {
	var (
		wg    sync.WaitGroup
		fails atomic.Int64
	)
	commits = make([]float64, int(d*ltRate/time.Second))
	start := time.Now()
	for i := range commits {
		due := start.Add(time.Duration(i) * time.Second / ltRate)
		time.Sleep(time.Until(due))
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, _, err := n.Propose(ctx, ltEntry(i)); err != nil {
				fails.Add(1)
				return
			}
			commits[i] = time.Since(due).Seconds()
		})
	}
	wg.Wait()
	return commits, fails.Load()
}

// ltEntry is proposal i: one of 64 keys, padded to ltPayload bytes.
func ltEntry(i int) []byte {
	e := fmt.Appendf(make([]byte, 0, ltPayload), "k%d=", i%64)
	for len(e) < ltPayload {
		e = append(e, 'x')
	}
	return e
}

// ltNode is one member of the benchmark's cluster.
type ltNode struct {
	tr   *TCPTransport
	node *Node
	spki [32]byte
}

// ltCID is the benchmarks' cluster.
var ltCID = coreconsensus.ClusterID{0x91, 0x2b, 0xe0}

// ltCluster bootstraps and starts nodes members over loopback mutual TLS,
// each with its own turn meter, as the package's TCP tests do but on a
// testing.B; tweak, when set, adjusts each member's configuration.
func ltCluster(b *testing.B, ca *ltAuthority, nodes int, tweak func(*Config)) ([]*ltNode, []*ltMeter) {
	b.Helper()
	cid := ltCID
	ns := make([]*ltNode, nodes)
	meters := make([]*ltMeter, nodes)
	cfgs := make([]Config, nodes)
	var members []coreconsensus.MemberValue
	for i := range nodes {
		slot := fmt.Sprintf("site-%d", i)
		cert, spki := ca.issue(b, SAN(cid, slot))
		tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0"})
		if err != nil {
			b.Fatalf("listen %s: %v", slot, err)
		}
		log := NewMemoryLog()
		id, inc, err := InitLog(log, cid, slot)
		if err != nil {
			b.Fatal(err)
		}
		ns[i], meters[i] = &ltNode{tr: tr, spki: spki}, &ltMeter{}
		cfgs[i] = Config{
			Cluster: cid, Machine: newKV(), Durability: DurabilityVolatile, Log: log, Snapshots: NewMemorySnapshots(),
			Transport: tr, InitialTiming: TimingDefaults(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
			MinVoters: 3, JoinAuthorizer: allowJoin{}, Meter: meters[i],
		}
		if tweak != nil {
			tweak(&cfgs[i])
		}
		members = append(members, coreconsensus.MemberValue{ID: id, Incarnation: inc, Slot: slot, SPKI: spki, Address: tr.Addr(), Voter: true})
	}
	for i := range ns {
		if err := Bootstrap(context.Background(), new(cfgs[i]), members); err != nil {
			b.Fatal(err)
		}
		n, err := NewNode(cfgs[i])
		if err != nil {
			b.Fatal(err)
		}
		ns[i].node = n
	}
	return ns, meters
}

// ltStop closes every node, then every transport.
func ltStop(b *testing.B, ns []*ltNode) {
	b.Helper()
	for _, n := range ns {
		if n.node != nil {
			if err := n.node.HardClose(); err != nil {
				b.Errorf("hard close: %v", err)
			}
		}
		if err := n.tr.Close(); err != nil {
			b.Errorf("close transport: %v", err)
		}
	}
}

// ltLeader is the index of the node that leads, within ten seconds.
func ltLeader(b *testing.B, ns []*ltNode) int {
	b.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		for i, n := range ns {
			if n.node.Status().Role == coreconsensus.RoleLeader {
				return i
			}
		}
	}
	b.Fatal("no leader within ten seconds")
	return -1
}

// ltAuthority is the certificate authority of one run.
type ltAuthority struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pool *x509.CertPool
}

// newLTAuthority is a certificate authority for one run.
func newLTAuthority(b *testing.B) *ltAuthority {
	b.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "consensus loop-turn bench CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		b.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		b.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &ltAuthority{key: key, cert: cert, pool: pool}
}

// issue is a member certificate for san, signed by the authority, and its
// pin.
func (ca *ltAuthority) issue(b *testing.B, san *url.URL) (*tls.Certificate, [32]byte) {
	b.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		b.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		b.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		b.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, SPKIOf(leaf)
}

// ltQuantile is the q-quantile of xs by the nearest rank, 0 when empty.
func ltQuantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	i := int(q*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// ltMeter is a Meter that keeps every consensus_loop_turn_seconds
// observation while on, raw, and drops every other series: the histogram's
// buckets (0.5 ms, 1 ms, 2.5 ms, ...) are too coarse for a p99 of turns that
// last tens of microseconds.
type ltMeter struct {
	on    atomic.Bool
	mu    sync.Mutex
	turns []float64
}

// taken returns the turns kept so far.
func (m *ltMeter) taken() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.turns)
}

func (*ltMeter) Counter(string, ...coreotel.AttrValue) coremetrics.Counter { return ltDropCount{} }
func (*ltMeter) Gauge(string, ...coreotel.AttrValue) coremetrics.Gauge     { return ltDropGauge{} }
func (*ltMeter) Collect() coremetrics.SnapshotValue                        { return coremetrics.SnapshotValue{} }

func (m *ltMeter) Histogram(name string, _ []float64, _ ...coreotel.AttrValue) coremetrics.Histogram {
	if name != mLoopTurn {
		return ltDrop{}
	}
	return ltTurns{m}
}

// ltTurns is the loop-turn series of an ltMeter.
type ltTurns struct{ m *ltMeter }

func (h ltTurns) Record(v float64) {
	if !h.m.on.Load() {
		return
	}
	h.m.mu.Lock()
	h.m.turns = append(h.m.turns, v)
	h.m.mu.Unlock()
}

func (h ltTurns) RecordDuration(d time.Duration) { h.Record(d.Seconds()) }

// ltDrop, ltDropCount and ltDropGauge are every other series: they keep
// nothing.
type (
	ltDrop      struct{}
	ltDropCount struct{}
	ltDropGauge struct{}
)

func (ltDrop) Record(float64)               {}
func (ltDrop) RecordDuration(time.Duration) {}
func (ltDropCount) Add(int64)               {}
func (ltDropCount) Inc()                    {}
func (ltDropGauge) Set(float64)             {}
func (ltDropGauge) Add(float64)             {}
