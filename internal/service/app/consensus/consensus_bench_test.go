package consensus

// The hand-run bench lane of ADR 0152 D15 and D16 (#911). It measures what
// the design's allocation budgets never may: the absolute numbers, fdatasync
// p50/p99, commit p50/p99 and entries per second, per device, written into
// BENCH.md by `make bench` and compared across changes only as benchstat
// deltas (`make profile-consensus`, `make benchstat-diff`).
//
//   - BenchmarkSegmentedLogAppendSync: the shipped segmented log, Append of
//     64 × 256 B then one Sync, per device (D15's budget table).
//   - BenchmarkThreeNodePropose: Propose → applied on three in-process nodes
//     over the memory doubles, allocations reported.
//   - BenchmarkTCPCommit: three nodes over loopback mTLS, each on its own
//     real data directory on the device, paced at 1 000 proposals/s and
//     saturated by 4096 writers.
//   - TestGenerateBenchMD: the generator. It drives the three through
//     testing.Benchmark, runs the simulator distributions D16 asks for in a
//     synctest bubble (a Benchmark cannot: synctest.Test takes a *testing.T),
//     and writes BENCH.md beside this file.
//
// The file is gazelle-excluded and compiled into the `manual`, race-off
// target `consensus_bench_test` alone under Bazel (rule 12): nothing in
// `bazel test //...` runs it. Under `go test` it joins the package's binary,
// where its benchmarks run only under -bench and the generator stays idle
// unless -run names it. It is self-contained: it uses the package's
// production code and nothing of its other test files, so the Bazel target
// needs this one source.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

const (
	// benchOutputRelativePath is BENCH.md's path from the workspace root.
	benchOutputRelativePath = "internal/service/app/consensus/BENCH.md"
	// benchEntryBytes is D16's entry size.
	benchEntryBytes = 256
	// benchBatchEntries is the entries of one Append in D15's log row.
	benchBatchEntries = 64
	// benchSegmentBytes is the shipped segment size (D4).
	benchSegmentBytes = 64 << 20
	// benchPacedRate is D16's offered load, proposals per second.
	benchPacedRate = 1000
	// benchWriters is the concurrent proposers of the saturated run:
	// enough that the batches, not the writers, bound the throughput.
	benchWriters = 4096
	// benchSimProposals is how many proposals the simulated commit run
	// times, at benchPacedRate.
	benchSimProposals = 2000
	// benchFailoverSeeds is how many seeds draw the failover distribution.
	benchFailoverSeeds = 60
	// benchDevicesEnv names the devices to measure, as name=dir pairs
	// separated by commas; unset, the data disk and /dev/shm.
	benchDevicesEnv = "CONSENSUS_BENCH_DEVICES"
)

// The RTT matrix of ADR 0152's Context, the three sites' measured means.
const (
	benchRTTFraRbx = 9600 * time.Microsecond
	benchRTTWawFra = 22800 * time.Microsecond
	benchRTTWawRbx = 27300 * time.Microsecond
)

// benchSlots are the three sites, in the order the simulator founds them.
var benchSlots = []string{"fra", "rbx", "waw"}

// benchRTT is the round trip between two sites of benchSlots.
func benchRTT(a, b string) time.Duration {
	switch {
	case a == b:
		return 0
	case a != "waw" && b != "waw":
		return benchRTTFraRbx
	case a == "fra" || b == "fra":
		return benchRTTWawFra
	default:
		return benchRTTWawRbx
	}
}

// benchLatencies is a distribution of durations, safe for concurrent adds.
type benchLatencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *benchLatencies) add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

func (l *benchLatencies) reset() {
	l.mu.Lock()
	l.d = l.d[:0]
	l.mu.Unlock()
}

func (l *benchLatencies) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.d)
}

// quantile is the nearest-rank q-quantile, 0 for an empty distribution.
func (l *benchLatencies) quantile(q float64) time.Duration {
	l.mu.Lock()
	s := slices.Clone(l.d)
	l.mu.Unlock()
	if len(s) == 0 {
		return 0
	}
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// benchMS is d in milliseconds.
func benchMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// benchSyncs collects a log's fdatasyncs through fileLog.timedSync.
type benchSyncs struct {
	lat     benchLatencies
	batches atomic.Int64
}

// hook is the timedSync the log calls after each fdatasync.
func (s *benchSyncs) hook(took time.Duration, end bool) {
	s.lat.add(took)
	if end {
		s.batches.Add(1)
	}
}

func (s *benchSyncs) reset() {
	s.lat.reset()
	s.batches.Store(0)
}

// perBatch is the fdatasyncs per synced batch.
func (s *benchSyncs) perBatch() float64 {
	n := s.batches.Load()
	if n == 0 {
		return 0
	}
	return float64(s.lat.len()) / float64(n)
}

// ─── Devices ─────────────────────────────────────────────────────────────

// benchDevice is one place the disk benchmarks write: the directory, the
// device and filesystem it sits on, or why it is not measured here.
type benchDevice struct {
	Name, Dir, Device, FS, Skip string
}

// benchWorkspace is the workspace root: BUILD_WORKSPACE_DIRECTORY under
// `bazel run`, git's top level under `go test`, "" when neither answers.
func benchWorkspace() string {
	if root := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); root != "" {
		return root
	}
	if v, ok := benchCmd("git", "rev-parse", "--show-toplevel"); ok {
		return v
	}
	return ""
}

// benchDevices is what CONSENSUS_BENCH_DEVICES names, else the data disk
// (.bench/consensus under the workspace, the disk the checkout is on) and
// the tmpfs at /dev/shm where one is mounted.
func benchDevices() []benchDevice {
	var out []benchDevice
	if spec := os.Getenv(benchDevicesEnv); spec != "" {
		for pair := range strings.SplitSeq(spec, ",") {
			name, dir, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || name == "" || dir == "" {
				continue
			}
			out = append(out, benchDescribe(name, dir))
		}
		return out
	}
	data := os.TempDir()
	if root := benchWorkspace(); root != "" {
		data = filepath.Join(root, ".bench", "consensus")
	}
	out = append(out, benchDescribe("data-disk", data))
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		out = append(out, benchDescribe("tmpfs", "/dev/shm"))
	} else {
		out = append(out, benchDevice{
			Name: "tmpfs", Dir: "/dev/shm", Device: "-", FS: "-",
			Skip: "no tmpfs is mounted at /dev/shm on " + runtime.GOOS,
		})
	}
	return out
}

// benchDescribe names the device and filesystem dir sits on, best effort:
// `df -P` for the device, `stat -f` on Linux and `mount` elsewhere for the
// filesystem.
func benchDescribe(name, dir string) benchDevice {
	d := benchDevice{Name: name, Dir: dir, Device: "unknown", FS: "unknown"}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.Skip = "cannot create " + dir + ": " + err.Error()
		return d
	}
	if out, ok := benchCmd("df", "-P", dir); ok {
		lines := strings.Split(out, "\n")
		if f := strings.Fields(lines[len(lines)-1]); len(f) > 0 {
			d.Device = f[0]
		}
	}
	if runtime.GOOS == "linux" {
		if v, ok := benchCmd("stat", "-f", "-c", "%T", dir); ok {
			d.FS = v
		}
		return d
	}
	if out, ok := benchCmd("mount"); ok {
		for line := range strings.SplitSeq(out, "\n") {
			_, rest, found := strings.Cut(line, d.Device+" on ")
			if !found || !strings.HasPrefix(line, d.Device) {
				continue
			}
			if _, opts, ok := strings.Cut(rest, "("); ok {
				d.FS, _, _ = strings.Cut(opts, ",")
				d.FS = strings.TrimSuffix(d.FS, ")")
			}
			break
		}
	}
	return d
}

// benchShownDir is dir as BENCH.md shows it: relative to the workspace
// when it lies inside, so the report names no home directory.
func benchShownDir(dir string) string {
	if root := benchWorkspace(); root != "" {
		if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return dir
}

// benchCmd runs a command and returns its trimmed output.
func benchCmd(name string, args ...string) (string, bool) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// benchRuntimeDir is the boot-scoped marker directory the benchmarks'
// data directories use, out of /run.
func benchRuntimeDir() string { return filepath.Join(os.TempDir(), "kitsunium-consensus-bench") }

// benchOpenDir opens a fresh data directory under dev, removed by cleanup.
func benchOpenDir(tb testing.TB, dev benchDevice, pattern string) DataDir {
	tb.Helper()
	dir, err := os.MkdirTemp(dev.Dir, pattern)
	if err != nil {
		tb.Fatalf("data directory on %s: %v", dev.Name, err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	dd, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: benchSegmentBytes, RuntimeDir: benchRuntimeDir()})
	if err != nil {
		_ = os.RemoveAll(dir)
		tb.Skipf("no data directory on %s: %v", dev.Name, err)
	}
	return dd
}

// ─── BenchmarkSegmentedLogAppendSync ─────────────────────────────────────

// BenchmarkSegmentedLogAppendSync measures the shipped log: one op is an
// Append of 64 entries of 256 B and one Sync, the batch made durable by its
// two fdatasyncs (#417). Reported per device, as D15's table asks.
func BenchmarkSegmentedLogAppendSync(b *testing.B) {
	for _, dev := range benchDevices() {
		b.Run(dev.Name, func(b *testing.B) { benchAppendSync(b, dev) })
	}
}

// benchAppendSync is one device's run of BenchmarkSegmentedLogAppendSync.
func benchAppendSync(b *testing.B, dev benchDevice) {
	if dev.Skip != "" {
		b.Skip(dev.Skip)
	}
	dd := benchOpenDir(b, dev, "append-*")
	defer func() {
		if err := dd.Close(); err != nil {
			b.Errorf("close: %v", err)
		}
	}()
	l := dd.d.log
	var syncs benchSyncs
	l.timedSync = syncs.hook
	var lat benchLatencies
	data := bytes.Repeat([]byte{'x'}, benchEntryBytes)
	sum := wire.BodySum(data)
	ents := make([]coreconsensus.EntryValue, benchBatchEntries)
	_, last := l.Bounds()
	next := last + 1
	b.SetBytes(benchBatchEntries * benchEntryBytes)
	b.ResetTimer()
	for range b.N {
		for i := range ents {
			ents[i] = coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: 1, Index: next},
				Kind:     coreconsensus.EntryCommand, BodySum: sum, Data: data,
			}
			next++
		}
		if err := l.Append(ents, nil); err != nil {
			b.Fatalf("append: %v", err)
		}
		start := time.Now()
		if err := l.Sync(); err != nil {
			b.Fatalf("sync: %v", err)
		}
		lat.add(time.Since(start))
	}
	b.StopTimer()
	b.ReportMetric(benchMS(lat.quantile(0.50)), "sync-p50-ms")
	b.ReportMetric(benchMS(lat.quantile(0.99)), "sync-p99-ms")
	b.ReportMetric(benchMS(syncs.lat.quantile(0.50)), "fdatasync-p50-ms")
	b.ReportMetric(benchMS(syncs.lat.quantile(0.99)), "fdatasync-p99-ms")
	b.ReportMetric(syncs.perBatch(), "fdatasyncs/batch")
	b.ReportMetric(float64(b.N*benchBatchEntries)/b.Elapsed().Seconds(), "entries/s")
}

// ─── Clusters ────────────────────────────────────────────────────────────

// benchSM is a state machine that counts what it applies: the benchmarks
// price the engine, not a machine.
type benchSM struct{ applied atomic.Uint64 }

func (s *benchSM) Apply(e coreconsensus.EntryValue) any {
	s.applied.Store(e.Position.Index)
	return nil
}

type benchSnap struct{ data []byte }

func (s benchSnap) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(s.data)
	return int64(n), err
}

func (benchSnap) Release() {}

func (s *benchSM) Snapshot() (coreconsensus.Snapshot, error) {
	return benchSnap{data: fmt.Appendf(nil, "%d", s.applied.Load())}, nil
}

func (s *benchSM) Restore(r io.Reader, _ coreconsensus.SnapshotMetaValue) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// benchAllowJoin admits every JOIN; the benchmarks found their members.
type benchAllowJoin struct{}

func (benchAllowJoin) Authorize(_ context.Context, r coreconsensus.JoinRequestValue) (coreconsensus.Operator, error) {
	return coreconsensus.Operator("automation:" + r.Slot), nil
}

// benchMember is one node of a benchmark's cluster.
type benchMember struct {
	slot  string
	id    coreconsensus.NodeID
	inc   coreconsensus.Incarnation
	spki  [32]byte
	addr  string
	log   coreconsensus.LogStore
	snaps coreconsensus.SnapshotStore
	tr    coreconsensus.Transport
	// closeTr closes the transport after the node; release the store.
	closeTr, release func() error
	syncs            benchSyncs
	node             *Node
}

// benchClusterSpec says how a benchmark's cluster is built.
type benchClusterSpec struct {
	cid       coreconsensus.ClusterID
	timing    coreconsensus.TimingContractSpec
	seed      uint64
	preferred []string
	// store gives member i its log and snapshots and, when it is a real
	// data directory, the fileLog the fdatasyncs are timed on.
	store func(tb testing.TB, i int) (coreconsensus.LogStore, coreconsensus.SnapshotStore, *fileLog, func() error)
	// transport gives a member its transport once its ID is known, its
	// SPKI and its address.
	transport func(tb testing.TB, m *benchMember) (coreconsensus.Transport, [32]byte, string, func() error)
}

// benchCluster founds and starts three nodes; cleanup closes them.
func benchCluster(tb testing.TB, spec benchClusterSpec) []*benchMember {
	tb.Helper()
	var ms []*benchMember
	var members []coreconsensus.MemberValue
	for i, slot := range benchSlots {
		m := &benchMember{slot: slot}
		var fl *fileLog
		m.log, m.snaps, fl, m.release = spec.store(tb, i)
		if fl != nil {
			fl.timedSync = m.syncs.hook
		}
		id, inc, err := InitLog(m.log, spec.cid, slot)
		if err != nil {
			tb.Fatalf("init %s: %v", slot, err)
		}
		m.id, m.inc = id, inc
		m.tr, m.spki, m.addr, m.closeTr = spec.transport(tb, m)
		ms = append(ms, m)
		members = append(members, coreconsensus.MemberValue{
			ID: id, Incarnation: inc, Slot: slot, SPKI: m.spki, Address: m.addr, Voter: true,
		})
	}
	var prefs []coreconsensus.NodeID
	for _, m := range ms {
		if slices.Contains(spec.preferred, m.slot) {
			prefs = append(prefs, m.id)
		}
	}
	cfgOf := func(i int, m *benchMember) Config {
		seed := uint64(0)
		if spec.seed != 0 {
			seed = spec.seed + uint64(i)
		}
		return Config{
			Cluster: spec.cid, Machine: &benchSM{}, Durability: DurabilityVolatile, Log: m.log, Snapshots: m.snaps,
			Transport: m.tr, InitialTiming: spec.timing, MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 16 << 20,
			MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 3, JoinAuthorizer: benchAllowJoin{},
			PreferredLeaders: prefs, Seed: seed,
		}
	}
	tb.Cleanup(func() {
		for _, m := range ms {
			benchStop(tb, m)
			if m.release != nil {
				if err := m.release(); err != nil {
					tb.Errorf("release %s: %v", m.slot, err)
				}
			}
		}
	})
	for i, m := range ms {
		if err := Bootstrap(context.Background(), new(cfgOf(i, m)), members); err != nil {
			tb.Fatalf("bootstrap %s: %v", m.slot, err)
		}
	}
	for i, m := range ms {
		n, err := NewNode(cfgOf(i, m))
		if err != nil {
			tb.Fatalf("start %s: %v", m.slot, err)
		}
		m.node = n
	}
	return ms
}

// benchStop closes m's node, then its transport; a second call is a no-op.
func benchStop(tb testing.TB, m *benchMember) {
	if m.node != nil {
		if err := m.node.HardClose(); err != nil {
			tb.Errorf("hard close %s: %v", m.slot, err)
		}
		m.node = nil
	}
	if m.closeTr != nil {
		if err := m.closeTr(); err != nil {
			tb.Errorf("close transport %s: %v", m.slot, err)
		}
		m.closeTr = nil
	}
}

// benchLeader waits for a live leader among ms whose slot is one of slots
// (any slot when empty), polling every 5 ms, nil past timeout.
func benchLeader(ms []*benchMember, slots []string, timeout time.Duration) *benchMember {
	deadline := time.Now().Add(timeout)
	for {
		for _, m := range ms {
			if m.node == nil {
				continue
			}
			if m.node.Status().Role == coreconsensus.RoleLeader && (len(slots) == 0 || slices.Contains(slots, m.slot)) {
				return m
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// benchRetryable reports whether a Propose failed because leadership moved.
func benchRetryable(err error) bool {
	return errors.Is(err, coreconsensus.NotLeader) ||
		errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) || errs.HasCode(err, coreconsensus.CodeEntrySuperseded)
}

// benchProposer proposes to whichever member leads, finding it again when
// leadership moved.
type benchProposer struct {
	ms   []*benchMember
	lead atomic.Pointer[benchMember]
}

// propose proposes data until it commits, within timeout.
func (p *benchProposer) propose(data []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		l := p.lead.Load()
		if l == nil || l.node == nil {
			if l = benchLeader(p.ms, nil, time.Until(deadline)); l == nil {
				return errors.New("no leader")
			}
			p.lead.Store(l)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		_, _, err := l.node.Propose(ctx, data)
		cancel()
		if err == nil {
			return nil
		}
		if !benchRetryable(err) || time.Now().After(deadline) {
			return err
		}
		p.lead.CompareAndSwap(l, nil)
		time.Sleep(5 * time.Millisecond)
	}
}

// benchMemoryStore is a memory log and memory snapshots.
func benchMemoryStore(testing.TB, int) (coreconsensus.LogStore, coreconsensus.SnapshotStore, *fileLog, func() error) {
	return NewMemoryLog(), NewMemorySnapshots(), nil, nil
}

// benchMemoryTransport is the member's end of net, wrapped by wrap when set.
func benchMemoryTransport(net *MemoryNetwork, wrap func(*MemoryTransport, *benchMember) coreconsensus.Transport) func(testing.TB, *benchMember) (coreconsensus.Transport, [32]byte, string, func() error) {
	return func(_ testing.TB, m *benchMember) (coreconsensus.Transport, [32]byte, string, func() error) {
		mt := NewMemoryTransport(net, m.id)
		var tr coreconsensus.Transport = mt
		if wrap != nil {
			tr = wrap(mt, m)
		}
		return tr, MemorySPKI(m.id), "", mt.Close
	}
}

// ─── BenchmarkThreeNodePropose ───────────────────────────────────────────

// BenchmarkThreeNodePropose measures one Propose → applied on the leader
// of three in-process nodes over the memory log and transport (allocations
// counted over every goroutine of the three nodes). It is no kit budget
// (#921): kit's 60 000 calls with the collector off would hold some 2 GB, so
// this hand-run row is where its allocs/op are measured.
func BenchmarkThreeNodePropose(b *testing.B) {
	ms := benchCluster(b, benchClusterSpec{
		cid: coreconsensus.ClusterID{0xbe, 0x11, 0x01}, timing: TimingDefaults(), seed: 1,
		store: benchMemoryStore, transport: benchMemoryTransport(NewMemoryNetwork(), nil),
	})
	p := &benchProposer{ms: ms}
	data := bytes.Repeat([]byte{'x'}, benchEntryBytes)
	if err := p.propose(data, 30*time.Second); err != nil {
		b.Fatalf("settle: %v", err)
	}
	b.ReportAllocs()
	b.SetBytes(benchEntryBytes)
	b.ResetTimer()
	for b.Loop() {
		if err := p.propose(data, 30*time.Second); err != nil {
			b.Fatalf("propose: %v", err)
		}
	}
}

// ─── BenchmarkTCPCommit ──────────────────────────────────────────────────

// BenchmarkTCPCommit measures three nodes over loopback mutual TLS, each on
// a real data directory of the device, in two modes: paced, one proposal
// every millisecond (D16's 1 000 proposals/s, open loop, each latency from
// its due time), and saturated, 4096 writers proposing back to back. It
// reports commit p50/p99 (Propose on the leader, returning once the entry
// is committed and applied there), the leader's fdatasync p50/p99, entries
// per second and fdatasyncs per batch. The three nodes share one device,
// so each fdatasync competes with two others: a floor on what three hosts
// reach, never a forecast of them.
func BenchmarkTCPCommit(b *testing.B) {
	for _, dev := range benchDevices() {
		b.Run(dev.Name+"/paced", func(b *testing.B) { benchTCPCommit(b, dev, false) })
		b.Run(dev.Name+"/saturated", func(b *testing.B) { benchTCPCommit(b, dev, true) })
	}
}

// benchTCPCommit is one device and mode of BenchmarkTCPCommit.
func benchTCPCommit(b *testing.B, dev benchDevice, saturated bool) {
	if dev.Skip != "" {
		b.Skip(dev.Skip)
	}
	ca, err := newBenchCA()
	if err != nil {
		b.Fatal(err)
	}
	cid := coreconsensus.ClusterID{0xbe, 0x11, 0x02}
	ms := benchCluster(b, benchClusterSpec{
		cid: cid, timing: TimingDefaults(), seed: 7,
		store: func(tb testing.TB, _ int) (coreconsensus.LogStore, coreconsensus.SnapshotStore, *fileLog, func() error) {
			dd := benchOpenDir(tb, dev, "tcp-*")
			return dd.Log(), dd.Snapshots(), dd.d.log, dd.Close
		},
		transport: func(tb testing.TB, m *benchMember) (coreconsensus.Transport, [32]byte, string, func() error) {
			cert, spki, err := ca.issue(SAN(cid, m.slot))
			if err != nil {
				tb.Fatal(err)
			}
			tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0"})
			if err != nil {
				tb.Fatalf("listen %s: %v", m.slot, err)
			}
			return tr, spki, tr.Addr(), tr.Close
		},
	})
	p := &benchProposer{ms: ms}
	data := bytes.Repeat([]byte{'x'}, benchEntryBytes)
	for range 10 {
		if err := p.propose(data, 30*time.Second); err != nil {
			b.Fatalf("settle: %v", err)
		}
	}
	for _, m := range ms {
		m.syncs.reset()
	}
	var lat benchLatencies
	var failed atomic.Pointer[error]
	one := func(due time.Time) {
		if err := p.propose(data, 30*time.Second); err != nil {
			failed.CompareAndSwap(nil, &err)
			return
		}
		lat.add(time.Since(due))
	}
	b.SetBytes(benchEntryBytes)
	b.ResetTimer()
	if saturated {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range benchWriters {
			wg.Go(func() {
				for next.Add(1) <= int64(b.N) {
					one(time.Now())
				}
			})
		}
		wg.Wait()
	} else {
		start := time.Now()
		var wg sync.WaitGroup
		for i := range b.N {
			due := start.Add(time.Duration(i) * time.Second / benchPacedRate)
			time.Sleep(time.Until(due))
			wg.Go(func() { one(due) })
		}
		wg.Wait()
	}
	b.StopTimer()
	if errp := failed.Load(); errp != nil {
		b.Fatalf("propose: %v", *errp)
	}
	lead := p.lead.Load()
	if lead == nil {
		lead = ms[0]
	}
	b.ReportMetric(benchMS(lat.quantile(0.50)), "commit-p50-ms")
	b.ReportMetric(benchMS(lat.quantile(0.99)), "commit-p99-ms")
	b.ReportMetric(benchMS(lead.syncs.lat.quantile(0.50)), "fdatasync-p50-ms")
	b.ReportMetric(benchMS(lead.syncs.lat.quantile(0.99)), "fdatasync-p99-ms")
	b.ReportMetric(lead.syncs.perBatch(), "fdatasyncs/batch")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "entries/s")
}

// benchCA is a throwaway consensus intermediate for the TCP benchmark.
type benchCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newBenchCA() (*benchCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "consensus bench CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &benchCA{cert: c, key: key, pool: pool}, nil
}

// issue is a member certificate for san and its SPKI pin.
func (ca *benchCA) issue(san *url.URL) (*tls.Certificate, [32]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, [32]byte{}, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, [32]byte{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		URIs: []*url.URL{san}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, [32]byte{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, SPKIOf(leaf), nil
}

// ─── The simulator (synctest) ────────────────────────────────────────────

// benchLatencyTransport delays each frame by half the RTT between its
// sender's site and its receiver's, on the bubble's clock.
type benchLatencyTransport struct {
	*MemoryTransport
	from  string
	sites map[coreconsensus.NodeID]string
}

// Send implements Transport: the frame is copied and sent once its one-way
// delay has passed.
func (t *benchLatencyTransport) Send(to coreconsensus.NodeID, frame []byte) {
	d := benchRTT(t.from, t.sites[to]) / 2
	if d <= 0 {
		t.MemoryTransport.Send(to, frame)
		return
	}
	f := bytes.Clone(frame)
	time.AfterFunc(d, func() { t.MemoryTransport.Send(to, f) })
}

// benchSlowLog is a memory log whose Sync takes delay, on the bubble's
// clock: the simulator's disk.
type benchSlowLog struct {
	*MemoryLog
	delay time.Duration
}

func (l *benchSlowLog) Sync() error {
	time.Sleep(l.delay)
	return l.MemoryLog.Sync()
}

// benchSimResult is what the simulator measured.
type benchSimResult struct {
	sync     time.Duration
	commit   benchLatencies
	failover benchLatencies
	leaders  map[string]int
}

// benchSimCluster is three memory nodes over the RTT matrix, each Sync
// taking syncDelay, FRA and RBX preferred as leaders (D8), on D5's timing.
func benchSimCluster(t *testing.T, seed uint64, syncDelay time.Duration) []*benchMember {
	net := NewMemoryNetwork()
	sites := map[coreconsensus.NodeID]string{}
	return benchCluster(t, benchClusterSpec{
		cid: coreconsensus.ClusterID{0xbe, 0x11, 0x03}, timing: TimingDefaults(), seed: seed,
		preferred: []string{"fra", "rbx"},
		store: func(testing.TB, int) (coreconsensus.LogStore, coreconsensus.SnapshotStore, *fileLog, func() error) {
			return &benchSlowLog{MemoryLog: NewMemoryLog(), delay: syncDelay}, NewMemorySnapshots(), nil, nil
		},
		transport: benchMemoryTransport(net, func(mt *MemoryTransport, m *benchMember) coreconsensus.Transport {
			sites[m.id] = m.slot
			return &benchLatencyTransport{MemoryTransport: mt, from: m.slot, sites: sites}
		}),
	})
}

// benchSimulate runs D16's two simulator distributions in synctest
// bubbles: quorum commit at 1 000 proposals/s from a FRA or RBX leader, and
// failover (the leader killed, then the first commit under the next one)
// over benchFailoverSeeds seeds. The disk is a constant syncDelay per Sync.
func benchSimulate(t *testing.T, syncDelay time.Duration) *benchSimResult {
	res := &benchSimResult{sync: syncDelay, leaders: map[string]int{}}
	data := bytes.Repeat([]byte{'x'}, benchEntryBytes)
	synctest.Test(t, func(t *testing.T) {
		ms := benchSimCluster(t, 1000, syncDelay)
		l := benchSettle(t, ms, data)
		res.leaders[l.slot]++
		start := time.Now()
		var wg sync.WaitGroup
		for i := range benchSimProposals {
			due := start.Add(time.Duration(i) * time.Second / benchPacedRate)
			time.Sleep(time.Until(due))
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, _, err := l.node.Propose(ctx, data); err != nil {
					t.Errorf("simulated propose: %v", err)
					return
				}
				res.commit.add(time.Since(due))
			})
		}
		wg.Wait()
	})
	for seed := uint64(1); seed <= benchFailoverSeeds; seed++ {
		synctest.Test(t, func(t *testing.T) {
			ms := benchSimCluster(t, seed*1000, syncDelay)
			l := benchSettle(t, ms, data)
			benchStop(t, l)
			killed := time.Now()
			p := &benchProposer{ms: ms}
			if err := p.propose(data, 60*time.Second); err != nil {
				t.Errorf("seed %d: no commit after the leader kill: %v", seed, err)
				return
			}
			res.failover.add(time.Since(killed))
		})
	}
	return res
}

// benchSettle waits for a FRA or RBX leader that committed an entry of its
// term, the state the simulator measures from.
func benchSettle(t *testing.T, ms []*benchMember, data []byte) *benchMember {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		l := benchLeader(ms, []string{"fra", "rbx"}, time.Until(deadline))
		if l == nil {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		tok, _, err := l.node.Propose(ctx, data)
		cancel()
		st := l.node.Status()
		if err == nil && st.Role == coreconsensus.RoleLeader && st.Term == tok.Term {
			return l
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no FRA or RBX leader settled")
	return nil
}

// ─── BENCH.md generator ──────────────────────────────────────────────────

// TestGenerateBenchMD runs the lane and rewrites BENCH.md beside this file.
// `make bench` runs it as
//
//	bazel run //internal/service/app/consensus:consensus_bench_test -- \
//	    -test.run=TestGenerateBenchMD -test.bench=^$ -test.benchtime=1s
//
// It is opt-in by name, as the codec's generator is (rule 12): a run that
// does not name it, `go test ./...` included, returns at once.
func TestGenerateBenchMD(t *testing.T) {
	if !strings.Contains(benchRunPattern(), "TestGenerateBenchMD") {
		t.Log("consensus bench generator idle — run `make bench` (or -run TestGenerateBenchMD) to regenerate BENCH.md")
		return
	}
	rep := &benchReport{devices: benchDevices()}
	for _, dev := range rep.devices {
		rep.logRows = append(rep.logRows, benchRow{
			dev: dev, mode: "append 64 × 256 B + Sync",
			r: benchRun(dev, func(b *testing.B) { benchAppendSync(b, dev) }),
		})
	}
	for _, dev := range rep.devices {
		for _, saturated := range []bool{false, true} {
			mode := "paced 1 000/s"
			if saturated {
				mode = fmt.Sprintf("saturated, %d writers", benchWriters)
			}
			rep.tcpRows = append(rep.tcpRows, benchRow{
				dev: dev, mode: mode, saturated: saturated,
				r: benchRun(dev, func(b *testing.B) { benchTCPCommit(b, dev, saturated) }),
			})
		}
	}
	rep.propose = testing.Benchmark(BenchmarkThreeNodePropose)
	rep.sim = benchSimulate(t, rep.simSync())
	out := resolveConsensusBenchOutputPath()
	before, _ := os.ReadFile(out) // none yet: nothing hand-run to keep
	if err := os.WriteFile(out, []byte(keepHandRun(rep.render(collectBenchEnvelope()), before)), 0o644); err != nil {
		panic(fmt.Sprintf("consensus bench: write %s: %v", out, err))
	}
	t.Logf("wrote %s", out)
}

// benchRun runs f through testing.Benchmark, marking a skipped device.
func benchRun(dev benchDevice, f func(*testing.B)) testing.BenchmarkResult {
	if dev.Skip != "" {
		return testing.BenchmarkResult{}
	}
	return testing.Benchmark(f)
}

// benchRunPattern is the -test.run pattern, "" when the flag is absent.
func benchRunPattern() string {
	if f := flag.Lookup("test.run"); f != nil {
		return f.Value.String()
	}
	return ""
}

// resolveConsensusBenchOutputPath is where BENCH.md is written: under
// BUILD_WORKSPACE_DIRECTORY with `bazel run`, beside the sources (the
// working directory) with `go test`.
func resolveConsensusBenchOutputPath() string {
	if root := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); root != "" {
		return filepath.Join(root, benchOutputRelativePath)
	}
	return "BENCH.md"
}

// benchRow is one measured row.
type benchRow struct {
	dev       benchDevice
	mode      string
	saturated bool
	r         testing.BenchmarkResult
}

// id is the row's name in BENCH.md, which ADR 0152 cites.
func (r benchRow) id(prefix string) string {
	switch {
	case prefix == "LOG":
		return "LOG/" + r.dev.Name
	case r.saturated:
		return prefix + "/" + r.dev.Name + "/saturated"
	default:
		return prefix + "/" + r.dev.Name + "/paced"
	}
}

func (r benchRow) metric(name string) float64 { return r.r.Extra[name] }

// benchReport is everything BENCH.md says.
type benchReport struct {
	devices []benchDevice
	logRows []benchRow
	tcpRows []benchRow
	propose testing.BenchmarkResult
	sim     *benchSimResult
}

// dataDisk is the first measured log row, the device the simulator's disk
// and D16's targets are read from.
func (rep *benchReport) dataDisk() (benchRow, bool) {
	for _, r := range rep.logRows {
		if r.dev.Skip == "" && r.r.N > 0 {
			return r, true
		}
	}
	return benchRow{}, false
}

// simSync is the simulator's constant Sync: the data disk's measured Sync
// p99 (one batch, its two fdatasyncs), 5 ms when nothing was measured.
func (rep *benchReport) simSync() time.Duration {
	if r, ok := rep.dataDisk(); ok && r.metric("sync-p99-ms") > 0 {
		return time.Duration(r.metric("sync-p99-ms") * float64(time.Millisecond))
	}
	return 5 * time.Millisecond
}

// tcpRow is the data disk's TCP row of the mode.
func (rep *benchReport) tcpRow(saturated bool) (benchRow, bool) {
	d, ok := rep.dataDisk()
	if !ok {
		return benchRow{}, false
	}
	for _, r := range rep.tcpRows {
		if r.dev.Name == d.dev.Name && r.saturated == saturated && r.r.N > 0 {
			return r, true
		}
	}
	return benchRow{}, false
}

// benchEnvelope is the host BENCH.md was measured on.
type benchEnvelope struct {
	CPU, Cores, RAM, OS, Kernel, Arch, Go, Branch, Commit, At, BenchTime string
}

func collectBenchEnvelope() benchEnvelope {
	or := func(v string, ok bool) string {
		if !ok || v == "" {
			return "unknown"
		}
		return v
	}
	cpu, ok := benchCmd("sysctl", "-n", "machdep.cpu.brand_string")
	if !ok {
		cpu, ok = benchProcField("/proc/cpuinfo", "model name")
	}
	ram := "unknown"
	if v, ok := benchCmd("sysctl", "-n", "hw.memsize"); ok {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			ram = fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
		}
	} else if v, ok := benchProcField("/proc/meminfo", "MemTotal"); ok {
		var kb int64
		if _, err := fmt.Sscanf(v, "%d", &kb); err == nil {
			ram = fmt.Sprintf("%.1f GiB", float64(kb)/(1<<20))
		}
	}
	osName := runtime.GOOS
	if name, ok := benchCmd("sw_vers", "-productName"); ok {
		ver, _ := benchCmd("sw_vers", "-productVersion")
		osName = strings.TrimSpace(name + " " + ver)
	} else if v, ok := benchProcField("/etc/os-release", "PRETTY_NAME"); ok {
		osName = strings.Trim(v, `"`)
	}
	kernel, kok := benchCmd("uname", "-sr")
	branch, bok := benchCmd("git", "rev-parse", "--abbrev-ref", "HEAD")
	commit, cok := benchCmd("git", "rev-parse", "--short", "HEAD")
	bt := "1s"
	if f := flag.Lookup("test.benchtime"); f != nil {
		bt = f.Value.String()
	}
	return benchEnvelope{
		CPU: or(cpu, ok), Cores: fmt.Sprint(runtime.NumCPU()), RAM: ram, OS: osName, Kernel: or(kernel, kok),
		Arch: runtime.GOARCH, Go: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
		Branch: or(branch, bok), Commit: or(commit, cok), At: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		BenchTime: bt,
	}
}

// benchProcField is the value of key in a "key: value" or "key=value" file.
func benchProcField(path, key string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			k, v, ok = strings.Cut(line, "=")
		}
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), true
		}
	}
	return "", false
}

// render writes BENCH.md.
func (rep *benchReport) render(env benchEnvelope) string {
	var b strings.Builder
	b.WriteString("<!-- generated by internal/service/app/consensus/consensus_bench_test.go (make bench) — do not edit by hand -->\n")
	b.WriteString("# Benchmarks — `internal/service/app/consensus`\n\n")
	b.WriteString("The hand-run bench lane of ADR 0152 D15 and D16 (#911). Bazel target ")
	b.WriteString("`//internal/service/app/consensus:consensus_bench_test` (`manual`, race off — ")
	b.WriteString("excluded from `bazel test //...`). These numbers are this host's, never design ")
	b.WriteString("budgets (D15): a change is judged by `make profile-consensus` waves compared with ")
	b.WriteString("`make benchstat-diff`, not by an absolute figure here.\n\n")

	b.WriteString("## Host\n\n| Dimension | Value |\n|---|---|\n")
	for _, kv := range [][2]string{
		{"CPU", env.CPU},
		{"CPU cores", env.Cores},
		{"RAM", env.RAM},
		{"OS", env.OS},
		{"Kernel", env.Kernel},
		{"Architecture", env.Arch},
		{"Go toolchain", env.Go},
		{"Git branch", env.Branch},
		{"Git commit", env.Commit},
		{"Generated (UTC)", env.At},
		{"Bench wall-clock", "`-test.benchtime=" + env.BenchTime + "`"},
	} {
		fmt.Fprintf(&b, "| %s | %s |\n", kv[0], kv[1])
	}

	b.WriteString("\n## Devices\n\n| Device | Directory | Block device | Filesystem | Measured |\n|---|---|---|---|---|\n")
	for _, d := range rep.devices {
		measured := "yes"
		if d.Skip != "" {
			measured = "no — " + d.Skip
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n", d.Name, benchShownDir(d.Dir), d.Device, d.FS, measured)
	}

	b.WriteString("\n## Segmented log — `Append` 64 × 256 B + one `Sync` (`BenchmarkSegmentedLogAppendSync`)\n\n")
	b.WriteString("One op is one batch of 64 entries made durable by its two fdatasyncs, the end record after the rest (#417). ")
	b.WriteString("`fdatasync` is each call; `Sync` is the batch.\n\n")
	b.WriteString("| Row | Batches | fdatasync p50 | fdatasync p99 | Sync p50 | Sync p99 | fdatasyncs / batch | entries/s |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rep.logRows {
		if r.r.N == 0 {
			fmt.Fprintf(&b, "| `%s` | — | — | — | — | — | — | not measured |\n", r.id("LOG"))
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %.3f ms | %.3f ms | %.3f ms | %.3f ms | %.2f | %.0f |\n", r.id("LOG"), r.r.N,
			r.metric("fdatasync-p50-ms"), r.metric("fdatasync-p99-ms"), r.metric("sync-p50-ms"), r.metric("sync-p99-ms"),
			r.metric("fdatasyncs/batch"), r.metric("entries/s"))
	}

	b.WriteString("\n## Three nodes over loopback mTLS, real logs (`BenchmarkTCPCommit`)\n\n")
	b.WriteString("Commit is `Propose` on the leader, from the proposal's due time to its return (committed by a majority and applied on the leader). ")
	b.WriteString("fdatasync is the leader's log. The three nodes share the device, so every fdatasync competes with the two others'; the loopback RTT is ~0.\n\n")
	b.WriteString("| Row | Proposals | commit p50 | commit p99 | fdatasync p50 | fdatasync p99 | fdatasyncs / batch | entries/s |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rep.tcpRows {
		if r.r.N == 0 {
			fmt.Fprintf(&b, "| `%s` | — | — | — | — | — | — | not measured |\n", r.id("TCP"))
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %.3f ms | %.3f ms | %.3f ms | %.3f ms | %.2f | %.0f |\n", r.id("TCP"), r.r.N,
			r.metric("commit-p50-ms"), r.metric("commit-p99-ms"), r.metric("fdatasync-p50-ms"), r.metric("fdatasync-p99-ms"),
			r.metric("fdatasyncs/batch"), r.metric("entries/s"))
	}

	b.WriteString("\n## `Propose` → applied, three in-process nodes over the memory doubles (`BenchmarkThreeNodePropose`)\n\n")
	b.WriteString("Allocations are counted over every goroutine of the three nodes. The row is no kit budget: kit's 60 000 calls with the collector off would hold some 2 GB on three nodes, so its allocs/op are compared here, wave to wave (D15's 4, ADR 0152's amendment of 2026-11-12, #921).\n\n")
	b.WriteString("| Row | Proposals | ns/op | B/op | allocs/op |\n|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| `MEM/propose` | %d | %d | %d | %d |\n", rep.propose.N, rep.propose.NsPerOp(),
		rep.propose.AllocedBytesPerOp(), rep.propose.AllocsPerOp())

	s := rep.sim
	b.WriteString("\n## Simulator — synctest, the RTT matrix (`benchSimulate`)\n\n")
	fmt.Fprintf(&b, "Three memory nodes in a synctest bubble, every frame delayed by half its pair's RTT (FRA–RBX %.1f ms, WAW–FRA %.1f ms, WAW–RBX %.1f ms, ADR 0152 Context), ",
		benchMS(benchRTTFraRbx), benchMS(benchRTTWawFra), benchMS(benchRTTWawRbx))
	fmt.Fprintf(&b, "each `Sync` a constant %.3f ms (the data disk's `Sync` p99 above), FRA and RBX preferred as leaders (D8), D5's timing (`TimingDefaults`: heartbeat 100 ms, election 1–2 s). ", benchMS(s.sync))
	b.WriteString("Times are the bubble's clock: they depend on the seeds, never on this host's speed.\n\n")
	b.WriteString("| Row | Samples | p50 | p99 | max |\n|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| `SIM/commit` (1 000 proposals/s from a FRA or RBX leader) | %d | %.3f ms | %.3f ms | %.3f ms |\n", s.commit.len(),
		benchMS(s.commit.quantile(0.5)), benchMS(s.commit.quantile(0.99)), benchMS(s.commit.quantile(1)))
	fmt.Fprintf(&b, "| `SIM/failover` (leader killed → first commit under the next, %d seeds) | %d | %.0f ms | %.0f ms | %.0f ms |\n", benchFailoverSeeds,
		s.failover.len(), benchMS(s.failover.quantile(0.5)), benchMS(s.failover.quantile(0.99)), benchMS(s.failover.quantile(1)))

	rep.renderTargets(&b)

	b.WriteString("\n## Reproduce\n\n```sh\nmake bench                       # every generator, this file included\n")
	b.WriteString("make bench-consensus             # this file alone\n")
	b.WriteString("CONSENSUS_BENCH_DEVICES=nvme=/mnt/nvme/bench,tmpfs=/dev/shm make bench-consensus\n")
	b.WriteString("make profile-consensus WAVE=before && make profile-consensus WAVE=after\n")
	b.WriteString("make benchstat-diff BEFORE=before AFTER=after\n```\n")
	return b.String()
}

// renderTargets writes D16's three provisional targets against the rows
// they are read from on this host.
func (rep *benchReport) renderTargets(b *strings.Builder) {
	b.WriteString("\n## D16's provisional targets on this host\n\n")
	b.WriteString("`fsync` in a target is one fdatasync; a batch costs two. ADR 0152's amendment of 2026-11-11 records the verdicts and what is still to measure on the three VPS.\n\n")
	b.WriteString("| Target | Row | Measured | Bound | On this host |\n|---|---|---|---|---|\n")
	verdict := func(ok bool) string {
		if ok {
			return "met"
		}
		return "**not met**"
	}
	d, ok := rep.dataDisk()
	fsync99 := d.metric("fdatasync-p99-ms")
	// commitRow writes the commit target with k fdatasyncs: D16's 2, and
	// the revision's 4 — a batch costs two since #417, and an entry
	// proposed during a Sync waits for it, then for its own.
	commitRow := func(name string, k float64) {
		if ok {
			bound := 2*benchMS(benchRTTFraRbx) + k*fsync99
			got := benchMS(rep.sim.commit.quantile(0.99))
			fmt.Fprintf(b, "| %s | `SIM/commit`, fsync from `%s` | %.3f ms | %.3f ms | %s |\n",
				name, d.id("LOG"), got, bound, verdict(got <= bound))
		}
		if r, ok := rep.tcpRow(false); ok {
			bound := k * r.metric("fdatasync-p99-ms")
			got := r.metric("commit-p99-ms")
			fmt.Fprintf(b, "| the same, over loopback (RTT ~0) | `%s` | %.3f ms | %.3f ms | %s |\n", r.id("TCP"), got, bound, verdict(got <= bound))
		}
	}
	commitRow("D16: p99 quorum commit ≤ 2 × nearest RTT + 2 × fsync p99, at 1 000 proposals/s", 2)
	commitRow("revised (2026-11-11): p99 quorum commit ≤ 2 × nearest RTT + 4 × fsync p99, at 1 000 proposals/s", 4)
	if r, ok := rep.tcpRow(true); ok {
		got, per := r.metric("entries/s"), r.metric("fdatasyncs/batch")
		fmt.Fprintf(b, "| D16: ≥ 10 000 entries/s of 256 B, two fdatasyncs per batch | `%s` | %.0f entries/s, %.2f fdatasyncs / batch | 10 000 entries/s | %s |\n",
			r.id("TCP"), got, per, verdict(got >= 10000))
	}
	got := benchMS(rep.sim.failover.quantile(0.99))
	fmt.Fprintf(b, "| D16: failover p99 ≤ 3.5 s | `SIM/failover` | %.0f ms | 3500 ms | %s |\n", got, verdict(got <= 3500))
}
