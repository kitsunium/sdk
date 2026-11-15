package consensus

// #920's third criterion, #1365's row: the data lane's commit p99 within
// 10 % of the no-stream baseline while a paced snapshot stream runs. A
// hand-run bench measured it against its own heap — memory logs, four
// key-value states and four snapshots in one process, up to 1.47 GB of
// heap for a state of 48 MiB, collections that marked for 319 ms — and
// missed the bound two runs in five at 8 MiB/s. Here no node keeps a state:
// the snapshot is pseudo-random bytes written on the fly, so the one thing
// that differs between the windows of an op is the stream.
//
// The file is gazelle-excluded and compiled into the `manual`, race-off
// target `consensus_bench_test` (rule 12), whose generator writes its
// `TCP/<device>/paced+stream` rows into BENCH.md; the A/B lane's suites
// `cstream` and `cstream-scale` run it. It builds on the generator's
// helpers and on the pacing of #920, which the lane's base predates, so the
// base leaves it out and both rows a rule divides are the head's.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// One op of BenchmarkTCPCommitStream: the windows, the snapshot, and the
// load that cuts it.
const (
	// csWindowA is each window with no stream, one before B and one after.
	csWindowA = 4 * time.Second
	// csWindowB is the window a stream is in flight through, from its first
	// proposal to its last.
	csWindowB = 8 * time.Second
	// csLasts is the seconds of the rate the snapshot holds: a stream of
	// csLasts seconds outlasts csWindowB and the wait before it, at every
	// rate.
	csLasts = 10
	// csLoadEntries entries of csLoadBytes pass csCutBytes once, so every
	// voter cuts one snapshot; the windows' 16 000 entries of 256 B never
	// pass it again.
	csLoadEntries = 2400
	csLoadBytes   = 4 << 10
	csCutBytes    = 8 << 20
	// csLoadWriters propose the load together, so a disk batches it.
	csLoadWriters = 64
	// csTrailing is the entries a cut keeps behind it: few, so a node that
	// joins needs the snapshot.
	csTrailing = 4
	// csSettle is the paced load, measured and dropped, that precedes each
	// A window: the load, the cut and the learner's removal leave
	// transients a p99 of two pooled windows would lean to, lowering B / A.
	csSettle = 2 * time.Second
	// csJudgedRate is the rate #920 is judged at, fixed before measuring:
	// the rate the hand-run bench failed at, 32 times the data lane's
	// 256 KiB/s, some 6 % of a 1 Gbit/s link.
	csJudgedRate = 8 << 20
	// csBound is #920's bound on the p99 of B over A's.
	csBound = 1.10
)

// csRates are the rates of BenchmarkTCPCommitStream: csJudgedRate, and 2
// and 32 MiB/s for the curve, which the lane's scale suite publishes.
var csRates = []int64{2 << 20, csJudgedRate, 32 << 20}

// csCID is the stream cluster's id.
var csCID = coreconsensus.ClusterID{0xbe, 0x11, 0x92}

// BenchmarkTCPCommitStream measures #920's third criterion: the data lane's
// commit p99 while a paced snapshot stream runs, against the same
// cluster's with none. Three voters over loopback mutual TLS, each on a
// data directory of the device as in BenchmarkTCPCommit, take 1 000
// proposals/s of 256 B through the three windows of one op: A, csWindowA
// with no stream; B, csWindowB while a learner that joined on tmpfs is
// streamed the voters' one snapshot at the rate the sub-benchmark names
// (SnapshotStreamBytesPerSecond), in flight from B's first proposal to its
// last; A again, once the learner is closed and removed. Each A follows
// csSettle of the same load, measured and dropped. Its two rows report the
// windows' figures: paced, both A windows pooled, and paced+stream, B.
// The op is one cluster, so their ratio never compares two runs, and the
// order A B A cancels a linear drift. -benchtime=1x runs one op a rate; a
// run of the binary is one measure (-count repeats the report, not the op).
func BenchmarkTCPCommitStream(b *testing.B) {
	learner := csLearnerDevice()
	for _, dev := range benchDevices() {
		b.Run(dev.Name, func(b *testing.B) {
			if dev.Skip != "" {
				b.Skip(dev.Skip)
			}
			for _, rate := range csRates {
				b.Run(csRateName(rate), func(b *testing.B) {
					r := benchCommitStream(b, dev, learner, rate)
					b.Run("paced", func(b *testing.B) { r.reportPaced(b) })
					b.Run("paced+stream", func(b *testing.B) { r.stream.report(b) })
				})
			}
		})
	}
}

// csRateName is a rate's sub-benchmark: rate=8MiB.
func csRateName(rate int64) string { return fmt.Sprintf("rate=%dMiB", rate>>20) }

// csLearnerDevice is where the learner keeps its data directory: the tmpfs
// at /dev/shm, as another host's disk would be in a deployment, so its
// writes never compete with the voters' fdatasyncs. Where none is mounted,
// the op skips.
func csLearnerDevice() benchDevice {
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		return benchDevice{Name: "learner", Dir: "/dev/shm"}
	}
	return benchDevice{Name: "learner", Dir: "/dev/shm", Skip: "no tmpfs is mounted at /dev/shm for the learner on " + runtime.GOOS}
}

// csResultValue is one op: both A windows pooled, B, each A window alone,
// and the snapshot B streamed.
type csResultValue struct {
	paced, stream csWindowValue
	// before and after are the A windows: their p99s apart tell a drift the
	// order A B A cancels from a stall that lands in one.
	before, after csWindowValue
	// size is the snapshot's encoded size; left what the stream still had
	// to send at B's end.
	size, left int64
}

// benchCommitStream is one op of BenchmarkTCPCommitStream at rate, which
// the generator also runs: a cluster on dev whose voters cut one snapshot
// of csLasts seconds of rate, then the windows A, B and A. A leader that
// changes, a proposal that fails, a learner that does not leave or a stream
// that is not in flight from B's first proposal to its last fails it: the
// op never reports a ratio it did not measure.
func benchCommitStream(tb testing.TB, dev, learner benchDevice, rate int64) csResultValue {
	tb.Helper()
	if learner.Skip != "" {
		tb.Skip(learner.Skip)
	}
	ca, err := newBenchCA()
	if err != nil {
		tb.Fatal(err)
	}
	size := csLasts * rate
	ms := benchCluster(tb, benchClusterSpec{
		cid: csCID, timing: TimingDefaults(), seed: 920,
		store: benchDiskStore(dev, "stream-*"), transport: benchTLSTransport(ca, csCID),
		machine: func(int) coreconsensus.StateMachine { return &csBlob{size: size} },
		tune: func(c *Config) {
			c.SnapshotEvery, c.SnapshotEveryBytes, c.SnapshotTrailing = 1<<20, csCutBytes, csTrailing
			c.SnapshotStreamBytesPerSecond = rate
		},
	})
	l := benchLeader(ms, nil, 30*time.Second)
	if l == nil {
		tb.Fatal("no leader within thirty seconds")
	}
	csLoad(tb, l)
	for _, m := range ms {
		csCompacted(tb, m)
	}
	// One leader at one term for the whole op: a leader whose fdatasync
	// stalls past the maximum election timeout hands its leadership over
	// (#554), and windows under two leaders are no ratio.
	term := l.node.Status().Term
	csWindow(tb, l, nil, term, csSettle, "settle")
	res := csResultValue{before: csWindow(tb, l, nil, term, csWindowA, "A")}
	j := csJoin(tb, ca, learner, ms, l, term, size)
	csStreaming(tb, l, term, j.id)
	res.stream = csWindow(tb, l, j, term, csWindowB, "B")
	res.size, res.left = res.stream.size, res.stream.size-res.stream.sentEnd
	csRetire(tb, j)
	csRemove(tb, l, term, j.id)
	csWindow(tb, l, nil, term, csSettle, "settle again")
	res.after = csWindow(tb, l, nil, term, csWindowA, "A again")
	res.paced = res.before.pooled(res.after)
	return res
}

// reportPaced writes the paced row: both A windows pooled, then each A
// window's commit and fdatasync p99 apart.
func (r *csResultValue) reportPaced(b *testing.B) {
	r.paced.report(b)
	b.ReportMetric(benchMS(r.before.commitP99()), "before-p99-ms/commit")
	b.ReportMetric(benchMS(r.after.commitP99()), "after-p99-ms/commit")
	b.ReportMetric(benchMS(r.before.syncP99()), "before-fdatasync-p99-ms")
	b.ReportMetric(benchMS(r.after.syncP99()), "after-fdatasync-p99-ms")
}

// csWindowValue is what one window measured: its commits, the leader's
// fdatasyncs, the collections and the scheduling delays of the process, and
// the stream's progress through it.
type csWindowValue struct {
	commits, syncs []time.Duration
	gcCycles       uint64
	gcPause, sched csHist
	elapsed        time.Duration
	// streamed is what the stream sent during the window, sentEnd how far
	// it was at its end, size the snapshot's encoded size.
	streamed, sentEnd, size int64
}

// pooled is w and o as one window: both A windows, #920's no-stream
// baseline.
func (w csWindowValue) pooled(o csWindowValue) csWindowValue {
	return csWindowValue{
		commits:  append(append([]time.Duration(nil), w.commits...), o.commits...),
		syncs:    append(append([]time.Duration(nil), w.syncs...), o.syncs...),
		gcCycles: w.gcCycles + o.gcCycles, gcPause: w.gcPause.add(o.gcPause), sched: w.sched.add(o.sched),
		elapsed: w.elapsed + o.elapsed, streamed: w.streamed + o.streamed,
	}
}

// commitP99 is the window's commit p99, the figure #920 bounds.
func (w csWindowValue) commitP99() time.Duration {
	return (&benchLatencies{d: w.commits}).quantile(0.99)
}

// syncP99 is the leader's fdatasync p99 over the window: on a disk, the
// tail its commits wait for, which drifts from one window to the next with
// no stream at all.
func (w csWindowValue) syncP99() time.Duration {
	return (&benchLatencies{d: w.syncs}).quantile(0.99)
}

// streamRate is the stream's pace over the window, in MiB/s.
func (w csWindowValue) streamRate() float64 {
	if w.elapsed <= 0 {
		return 0
	}
	return float64(w.streamed) / (1 << 20) / w.elapsed.Seconds()
}

// report writes the window's figures as the metrics of b's row:
// p99-ms/commit is the one #920's rule reads, the others tell a disk, a
// collection or the scheduler apart from the stream.
func (w csWindowValue) report(b *testing.B) {
	commits := &benchLatencies{d: w.commits}
	b.ReportMetric(benchMS(commits.quantile(0.50)), "p50-ms/commit")
	b.ReportMetric(benchMS(commits.quantile(0.99)), "p99-ms/commit")
	b.ReportMetric(benchMS(commits.quantile(1)), "max-ms/commit")
	b.ReportMetric(float64(len(w.commits)), "commits/op")
	b.ReportMetric(benchMS(w.syncP99()), "fdatasync-p99-ms")
	b.ReportMetric(float64(w.gcCycles), "gc-cycles/op")
	b.ReportMetric(w.gcPause.quantile(0.99)*1e6, "gc-pause-p99-us")
	b.ReportMetric(w.sched.quantile(0.99)*1e6, "sched-p99-us")
	b.ReportMetric(w.streamRate(), "stream-MiB/s")
}

// csWindow proposes 256 B to the leader l at benchPacedRate for d, each
// proposal on its own goroutine at its due time so that a slow commit never
// slows the pace, and returns what the window measured. With a learner j,
// the stream to it must be in flight at the window's first proposal and
// still at its last commit. A failed proposal, or a leader that no longer
// leads at the op's term, fails the op.
func csWindow(tb testing.TB, l, j *benchMember, term uint64, d time.Duration, name string) csWindowValue {
	tb.Helper()
	data := bytes.Repeat([]byte{'x'}, benchEntryBytes)
	commits := make([]time.Duration, int(d*benchPacedRate/time.Second))
	var (
		wg     sync.WaitGroup
		failed atomic.Pointer[error]
		sent0  int64
	)
	if j != nil {
		sent0, _ = csStream(l.node, j.id)
	}
	_ = l.syncs.lat.drain()
	rt0 := csReadRuntime()
	start := time.Now()
	for i := range commits {
		due := start.Add(time.Duration(i) * time.Second / benchPacedRate)
		time.Sleep(time.Until(due))
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, _, err := l.node.Propose(ctx, data); err != nil {
				failed.CompareAndSwap(nil, &err)
				return
			}
			commits[i] = time.Since(due)
		})
	}
	wg.Wait()
	w := csWindowValue{commits: commits, elapsed: time.Since(start), syncs: l.syncs.lat.drain()}
	w.gcCycles, w.gcPause, w.sched = csRuntimeSince(rt0, csReadRuntime())
	if errp := failed.Load(); errp != nil {
		tb.Fatalf("window %s: a proposal failed: %v", name, *errp)
	}
	csStillLeads(tb, l, term, "window "+name)
	if j != nil {
		sent, size := csStream(l.node, j.id)
		if sent0 <= 0 || sent <= sent0 || sent >= size {
			tb.Fatalf("window %s: the stream was not in flight from its first proposal to its last: %d, then %d of %d bytes", name, sent0, sent, size)
		}
		w.streamed, w.sentEnd, w.size = sent-sent0, sent, size
	}
	return w
}

// csLoad proposes csLoadEntries entries of csLoadBytes through the leader
// l, csLoadWriters at a time: their payload passes csCutBytes once, so
// every voter cuts one snapshot.
func csLoad(tb testing.TB, l *benchMember) {
	tb.Helper()
	data := bytes.Repeat([]byte{'s'}, csLoadBytes)
	var (
		wg     sync.WaitGroup
		next   atomic.Int64
		failed atomic.Pointer[error]
	)
	for range csLoadWriters {
		wg.Go(func() {
			for next.Add(1) <= csLoadEntries {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, _, err := l.node.Propose(ctx, data)
				cancel()
				if err != nil {
					failed.CompareAndSwap(nil, &err)
					return
				}
			}
		})
	}
	wg.Wait()
	if errp := failed.Load(); errp != nil {
		tb.Fatalf("the load that cuts the snapshot: %v", *errp)
	}
}

// csCompacted waits until m's log no longer starts at its first entry: m
// cut its snapshot and compacted behind it, so a node that joins needs it.
func csCompacted(tb testing.TB, m *benchMember) {
	tb.Helper()
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if first, _ := m.log.Bounds(); first > 1 {
			return
		}
	}
	tb.Fatalf("%s did not cut its snapshot and compact its log within two minutes", m.slot)
}

// csStillLeads fails the op unless l leads at term: the leadership moved
// during the op, whose windows would no longer be one cluster's.
func csStillLeads(tb testing.TB, l *benchMember, term uint64, at string) {
	tb.Helper()
	if st := l.node.Status(); st.Term != term || st.Role != coreconsensus.RoleLeader {
		tb.Fatalf("%s: the leader changed during the op, term %d -> %d, role %v", at, term, st.Term, st.Role)
	}
}

// csJoin starts a fourth node on dev, with the voters' machine, and JOINs
// it as a learner through the voters ms while l leads at term: the leader
// streams it the snapshot. A JOIN refused while a hand-off of l's
// leadership runs is sent again, the op failing once l no longer leads. The
// op retires the learner before its last window; a cleanup retires it
// whatever happened.
func csJoin(tb testing.TB, ca *benchCA, dev benchDevice, ms []*benchMember, l *benchMember, term uint64, size int64) *benchMember {
	tb.Helper()
	j := &benchMember{slot: "learner"}
	dd := benchOpenDir(tb, dev, "learner-*")
	j.log, j.snaps, j.release = dd.Log(), dd.Snapshots(), dd.Close
	tb.Cleanup(func() { csRetire(tb, j) })
	id, inc, err := InitLog(j.log, csCID, j.slot)
	if err != nil {
		tb.Fatalf("init the learner: %v", err)
	}
	j.id, j.inc = id, inc
	j.tr, j.spki, j.addr, j.closeTr = benchTLSTransport(ca, csCID)(tb, j)
	j.node, err = NewNode(Config{
		Cluster: csCID, Machine: &csBlob{size: size}, Durability: DurabilityVolatile, Log: j.log, Snapshots: j.snaps,
		Transport: j.tr, InitialTiming: TimingDefaults(), MaxSnapshotBytes: 1 << 30, MaxLogBytes: 1 << 30,
		LogReserveBytes: 1 << 20, MinVoters: 3, JoinAuthorizer: benchAllowJoin{},
	})
	if err != nil {
		tb.Fatalf("start the learner: %v", err)
	}
	stamp, err := j.log.Stamp()
	if err != nil {
		tb.Fatalf("the learner's stamp: %v", err)
	}
	addrs, pins := make([]string, len(ms)), make([][32]byte, len(ms))
	for i, m := range ms {
		addrs[i], pins[i] = m.addr, m.spki
	}
	tr, ok := j.tr.(*TCPTransport)
	if !ok {
		tb.Fatalf("the learner's transport is a %T", j.tr)
	}
	cfg := coreconsensus.JoinConfig{Addresses: addrs, Stamp: stamp, Address: j.addr, Pins: pins}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := tr.Join(ctx, cfg)
		cancel()
		if err == nil {
			return j
		}
		// NOT_LEADER and MEMBERSHIP_CHANGE_PENDING appended nothing: the
		// JOIN goes again while l leads at term.
		again := errs.HasCode(err, coreconsensus.CodeNotLeader) || errs.HasCode(err, coreconsensus.CodeMembershipChangePending)
		if !again || time.Now().After(deadline) {
			tb.Fatalf("the learner's JOIN: %v", err)
		}
		csStillLeads(tb, l, term, "the learner's JOIN")
	}
}

// csRetire closes the learner j's node, its transport and its data
// directory; a second call does nothing.
func csRetire(tb testing.TB, j *benchMember) {
	benchStop(tb, j)
	if j.release != nil {
		if err := j.release(); err != nil {
			tb.Errorf("release the learner: %v", err)
		}
		j.release = nil
	}
}

// csStreaming waits until the leader l, leading at term, streams a
// snapshot to the peer to.
func csStreaming(tb testing.TB, l *benchMember, term uint64, to coreconsensus.NodeID) {
	tb.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if sent, _ := csStream(l.node, to); sent > 0 {
			return
		}
		csStillLeads(tb, l, term, "the wait for the stream")
	}
	tb.Fatal("the leader streamed no snapshot to the learner within a minute")
}

// csStream is the leader l's stream to the peer to: the bytes the transport
// has read and the snapshot's encoded size, both 0 with no stream.
func csStream(l *Node, to coreconsensus.NodeID) (sent, size int64) {
	for _, p := range l.Health().Peers {
		if p.ID == to {
			return p.SnapshotSent, p.SnapshotSize
		}
	}
	return 0, 0
}

// csRemove removes the closed learner id through the leader l, leading at
// term, and waits until l no longer lists it: the last window runs with the
// three voters alone, as the first did.
func csRemove(tb testing.TB, l *benchMember, term uint64, id coreconsensus.NodeID) {
	tb.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := l.node.Remove(ctx, "automation:bench", id)
		cancel()
		if err == nil {
			break
		}
		if !errs.HasCode(err, coreconsensus.CodeMembershipChangePending) || time.Now().After(deadline) {
			tb.Fatalf("remove the learner: %v", err)
		}
		csStillLeads(tb, l, term, "the learner's removal")
		time.Sleep(10 * time.Millisecond)
	}
	for csListed(l.node, id) {
		if time.Now().After(deadline) {
			tb.Fatal("the leader still lists the learner it removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// csListed reports whether the leader l lists the peer id.
func csListed(l *Node, id coreconsensus.NodeID) bool {
	for _, p := range l.Health().Peers {
		if p.ID == id {
			return true
		}
	}
	return false
}

// csBlob is the stream cluster's state machine: it keeps the index it
// applied and nothing else, and its snapshot is size pseudo-random bytes
// from a fixed key, written on the fly, which the snapshot codec cannot
// shrink. Restore reads the stream and keeps nothing.
type csBlob struct {
	applied atomic.Uint64
	size    int64
}

func (s *csBlob) Apply(e coreconsensus.EntryValue) any {
	s.applied.Store(e.Position.Index)
	return nil
}

func (s *csBlob) Snapshot() (coreconsensus.Snapshot, error) { return csBlobSnap{size: s.size}, nil }

func (*csBlob) Restore(r io.Reader, _ coreconsensus.SnapshotMetaValue) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// csBlobSnap is a csBlob's snapshot: size bytes of ChaCha8 under a fixed
// key, generated as they are written.
type csBlobSnap struct{ size int64 }

func (s csBlobSnap) WriteTo(w io.Writer) (int64, error) {
	r := rand.NewChaCha8([32]byte{0x09, 0x20})
	buf := make([]byte, 32<<10)
	var n int64
	for n < s.size {
		k := min(int64(len(buf)), s.size-n)
		_, _ = r.Read(buf[:k])
		m, err := w.Write(buf[:k])
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (csBlobSnap) Release() {}

// The runtime series a window reads at its edges: the collections, their
// stop-the-world pauses and the delay of a goroutine made runnable.
const (
	csGCCycles = "/gc/cycles/total:gc-cycles"
	csGCPauses = "/sched/pauses/total/gc:seconds"
	csSchedLat = "/sched/latencies:seconds"
)

// csReadRuntime reads the three series.
func csReadRuntime() []metrics.Sample {
	s := []metrics.Sample{{Name: csGCCycles}, {Name: csGCPauses}, {Name: csSchedLat}}
	metrics.Read(s)
	return s
}

// csRuntimeSince is what the runtime counted between the reads before and
// after: the collections, and the histograms of their pauses and of the
// scheduling delays.
func csRuntimeSince(before, after []metrics.Sample) (cycles uint64, pauses, sched csHist) {
	if before[0].Value.Kind() == metrics.KindUint64 && after[0].Value.Kind() == metrics.KindUint64 {
		cycles = after[0].Value.Uint64() - before[0].Value.Uint64()
	}
	return cycles, csHistSince(before[1], after[1]), csHistSince(before[2], after[2])
}

// csHist is a runtime histogram's counts over one window, bucket by bucket.
type csHist struct {
	buckets []float64
	counts  []uint64
}

// csHistSince is the counts after added since before; empty when the
// runtime does not keep the series.
func csHistSince(before, after metrics.Sample) csHist {
	if before.Value.Kind() != metrics.KindFloat64Histogram || after.Value.Kind() != metrics.KindFloat64Histogram {
		return csHist{}
	}
	b, a := before.Value.Float64Histogram(), after.Value.Float64Histogram()
	if len(b.Counts) != len(a.Counts) {
		return csHist{}
	}
	h := csHist{buckets: a.Buckets, counts: make([]uint64, len(a.Counts))}
	for i := range a.Counts {
		h.counts[i] = a.Counts[i] - b.Counts[i]
	}
	return h
}

// add is h and o pooled, bucket by bucket.
func (h csHist) add(o csHist) csHist {
	switch {
	case len(h.counts) == 0:
		return o
	case len(o.counts) != len(h.counts):
		return h
	}
	sum := csHist{buckets: h.buckets, counts: make([]uint64, len(h.counts))}
	for i := range h.counts {
		sum.counts[i] = h.counts[i] + o.counts[i]
	}
	return sum
}

// quantile is the upper bound, in seconds, of the bucket that holds the
// q-quantile of h's counts; 0 when h counted nothing.
func (h csHist) quantile(q float64) float64 {
	var total uint64
	for _, c := range h.counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := uint64(math.Ceil(q * float64(total)))
	var seen uint64
	for i, c := range h.counts {
		if seen += c; seen >= max(rank, 1) {
			if hi := h.buckets[i+1]; !math.IsInf(hi, 1) {
				return hi
			}
			return h.buckets[i]
		}
	}
	return 0
}
