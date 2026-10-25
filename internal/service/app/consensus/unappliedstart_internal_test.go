package consensus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// tailOpen opens the data directory under dir with its boot-scoped marker
// directory inside the test's own temporary directory as well.
func tailOpen(t *testing.T, dir, run string) DataDir {
	t.Helper()
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 4 << 20, RuntimeDir: run})
	//: no flock(2), no data directory (illumos, Solaris): the refusal is
	//: asserted, then the case skips, as openDir's do.
	if !flock.Native {
		if !errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Fatalf("NewDataDirWith on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return d
}

// tailConfig is a one-voter node whose D14 bounds are small: the memory
// budget the configuration states is about 3 MiB.
func tailConfig(cid coreconsensus.ClusterID, id coreconsensus.NodeID, d DataDir, m coreconsensus.StateMachine) Config {
	return Config{
		Cluster: cid, Machine: m, Durability: DurabilityVolatile, Log: d.Log(), Snapshots: d.Snapshots(),
		Transport: NewMemoryTransport(NewMemoryNetwork(), id), InitialTiming: testTimingFast(),
		MaxSnapshotBytes: 1 << 30, SnapshotEveryBytes: 1 << 30, SnapshotEvery: 1 << 30,
		MaxLogBytes: 1 << 30, LogReserveBytes: 1 << 20, MinVoters: 1,
		MaxBatchBytes: 64 << 10, MaxInflightBytes: 64 << 10, MaxEntryBytes: 32 << 10,
		MaxPendingBytes: 1 << 20, MaxApplyLagBytes: 1 << 20, RecentEntriesBytes: 1 << 20,
	}
}

// tailPayload is the command written at index idx, payload bytes long: its
// key and value name the index, so an entry applied out of order, twice or
// with another's payload is seen.
func tailPayload(idx uint64, payload int) []byte {
	data := make([]byte, payload)
	for i := range data {
		data[i] = 'v'
	}
	copy(data, fmt.Sprintf("k%d=%d:", idx%7, idx))
	return data
}

// tailFill appends committed entries of payload bytes after the bootstrap's
// log, chained as a leader chains them, in synced batches of about 512 KiB
// with the hard state that commits them, until the records written reach
// size bytes. It returns the first and last index written and the bytes of
// records written.
func tailFill(t *testing.T, l *fileLog, payload int, size int64) (first, last uint64, written int64) {
	t.Helper()
	_, last = l.Bounds()
	prev, err := l.Entries(last, last+1, 1<<30, nil)
	if err != nil || len(prev) != 1 {
		t.Fatalf("last entry: %v %d", err, len(prev))
	}
	chain, term := prev[0].ChainSum, prev[0].Position.Term
	hs, err := l.HardState()
	if err != nil {
		t.Fatal(err)
	}
	rec := int64(align8(recHeader + recFixed + payload))
	per := max(1, (512<<10)/int(rec))
	first = last + 1
	for written < size {
		es := make([]coreconsensus.EntryValue, 0, per)
		for range per {
			last++
			data := tailPayload(last, payload)
			e := coreconsensus.EntryValue{
				Position: coreconsensus.PositionValue{Term: term, Index: last},
				Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data,
			}
			e.ChainSum = wire.ChainSum(chain, &e)
			chain = e.ChainSum
			es = append(es, e)
			written += rec
		}
		hs.Commit = last
		if err := l.Append(es, &hs); err != nil {
			t.Fatal(err)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	return first, last, written
}

// orderKV is the test's kv, which also checks that every command it applies
// is the one written at its index, right after the previous one.
type orderKV struct {
	inner   *kv
	payload int
	mu      sync.Mutex
	next    uint64
	bad     error
	applied atomic.Uint64
}

func (s *orderKV) Apply(e coreconsensus.EntryValue) any {
	s.mu.Lock()
	if e.Kind == coreconsensus.EntryCommand && s.bad == nil {
		switch want := tailPayload(e.Position.Index, s.payload); {
		case s.next != 0 && e.Position.Index != s.next:
			s.bad = fmt.Errorf("applied %d after %d", e.Position.Index, s.next-1)
		case string(e.Data) != string(want):
			s.bad = fmt.Errorf("applied %d with the payload %.16q, want %.16q", e.Position.Index, e.Data, want)
		}
	}
	s.next = e.Position.Index + 1
	s.mu.Unlock()
	s.applied.Store(e.Position.Index)
	return s.inner.Apply(e)
}

func (s *orderKV) Snapshot() (coreconsensus.Snapshot, error) { return s.inner.Snapshot() }

func (s *orderKV) Restore(r io.Reader, m coreconsensus.SnapshotMetaValue) error {
	return s.inner.Restore(r, m)
}

func (s *orderKV) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bad
}

// TestTheStartHoldsNoMoreOfTheTailThanItsBudget measures what a node's
// start costs past the reopen of its log, which #630 bounded: load() read
// every entry past the snapshot base, payload included, with no bound, and
// cloned each payload, and raft's first Ready handed every committed entry
// to the apply goroutine at once, past MaxApplyLagBytes and
// RecentEntriesBytes. The start now reads the tail within RecentEntriesBytes
// and apply reads the rest back from the log within MaxApplyLagBytes, every
// entry applied once, in order (#663).
//
// The peak is sampled with the collector at GOGC=10: at the default of 100
// the heap sampled also holds the garbage the collector lets grow up to the
// live heap again, the baseline's included, whatever the start holds. Each
// payload is measured in a child process of this test binary, so the
// baseline holds nothing but the start's: in the package's own process the
// tests before it left about 600 MiB live, a tenth of which in garbage the
// collector let grow on top of the start's own, and the peak sampled 60 to
// 87 MiB over the baseline whatever the start held (startInAChild).
func TestTheStartHoldsNoMoreOfTheTailThanItsBudget(t *testing.T) {
	if p := os.Getenv(startTailChildEnv); p != "" {
		payload, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("%s=%q: %v", startTailChildEnv, p, err)
		}
		startHoldsItsBudget(t, payload)
		return
	}
	for _, payload := range []int{64, 4096} {
		t.Run(fmt.Sprintf("payload %d B", payload), func(t *testing.T) { startInAChild(t, payload) })
	}
}

// startTailChildEnv names the payload size a child process of
// TestTheStartHoldsNoMoreOfTheTailThanItsBudget measures the start for; the
// test only launches its children when it is unset.
const startTailChildEnv = "CONSENSUS_START_TAIL_PAYLOAD"

// startInAChild runs startHoldsItsBudget for payload in a child process of
// this test binary, which runs that test alone, and relays what it measured.
func startInAChild(t *testing.T, payload int) {
	if !flock.Native {
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestTheStartHoldsNoMoreOfTheTailThanItsBudget$", "-test.v") //nolint:gosec
	child.Env = append(os.Environ(), startTailChildEnv+"="+strconv.Itoa(payload))
	out, err := child.CombinedOutput()
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "unappliedstart_internal_test.go:") {
			t.Log(strings.TrimSpace(line))
		}
	}
	if err != nil {
		t.Errorf("payload %d B: the child process measuring the start failed: %v\n%s", payload, err, out)
	}
}

// startHoldsItsBudget runs TestTheStartHoldsNoMoreOfTheTailThanItsBudget for
// one payload size.
func startHoldsItsBudget(t *testing.T, payload int) {
	const logSize = 16 << 20
	dir := t.TempDir()
	run := filepath.Join(t.TempDir(), "run")
	cid := coreconsensus.ClusterID{0x5d, 0x1c}
	id, last, records := startTail(t, dir, run, cid, payload, logSize)

	// 1. load() alone, over a reopened log: what NewNode reads before
	// raft or any bound of D14 exists.
	d := tailOpen(t, dir, run)
	cfg := tailConfig(cid, id, d, newKV())
	c, err := withDefaults(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	budget := c.MemoryBudget(2)
	stamp, err := startStamp(&c)
	if err != nil {
		t.Fatal(err)
	}
	n := newNode(&c, &stamp)
	n.obs = newObs(&c)
	var before, loaded, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start, err := n.load()
	runtime.ReadMemStats(&loaded)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held, payloads := len(start.Entries), payloadBytes(start.Entries)
	runtime.KeepAlive(start)
	start, n, cfg, c = raft.StartState{}, nil, Config{}, Config{}
	allocated := int64(loaded.TotalAlloc - before.TotalAlloc)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("payload %d B: %d entries past the base, %.1f MiB of records; D14 budget %.1f MiB; load() allocated %.1f MiB (%.2fx the records) and its StartState keeps %.1f MiB live (%.2fx), %d entries with %.1f MiB of payloads",
		payload, last, mib(records), mib(budget), mib(allocated), float64(allocated)/float64(records),
		mib(retained), float64(retained)/float64(records), held, mib(payloads))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// 2. The whole NewNode, until the tail is applied: the peak heap.
	d = tailOpen(t, dir, run)
	m := &orderKV{inner: newKV(), payload: payload}
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	cfg = tailConfig(cid, id, d, m)
	tr := cfg.Transport.(*MemoryTransport)
	node, err := NewNode(cfg)
	cfg = Config{}
	if err != nil {
		close(stop)
		<-done
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for m.applied.Load() < last && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	applied := m.applied.Load()
	close(stop)
	<-done
	runtime.ReadMemStats(&loaded)
	lag := node.applyLag.Load()
	for deadline := time.Now().Add(5 * time.Second); lag != 0 && time.Now().Before(deadline); lag = node.applyLag.Load() {
		// The machine saw the last entry before apply released its bytes.
		time.Sleep(time.Millisecond)
	}
	if err := node.HardClose(); err != nil {
		t.Error(err)
	}
	// The transport's own goroutine holds the node until it is closed.
	if err := tr.Close(); err != nil {
		t.Error(err)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
	over := int64(peak.Load()) - int64(before.HeapAlloc)
	t.Logf("payload %d B: NewNode then apply to %d (want %d): heap peaked %.1f MiB over the baseline of %.1f MiB (%.2fx the records), %.1f MiB allocated in all",
		payload, applied, last, mib(over), mib(int64(before.HeapAlloc)), float64(over)/float64(records), mib(int64(loaded.TotalAlloc-before.TotalAlloc)))
	if applied < last {
		t.Errorf("payload %d B: applied %d of %d within 60 s", payload, applied, last)
	}
	if err := m.err(); err != nil {
		t.Errorf("payload %d B: %v", payload, err)
	}
	if lag != 0 {
		t.Errorf("payload %d B: an apply lag of %d bytes is left once everything is applied", payload, lag)
	}
	// The ADR's amendment for #630: the open costs about a segment plus
	// 40 B a record, then a node needs the larger of that and the
	// budget, plus a 32 B index an entry. The fix's own test bound for
	// the open is 3 segments plus 160 B an entry.
	bound := max(3*(4<<20)+160*int64(last), budget) + 32*int64(last)
	if over > bound {
		t.Errorf("payload %d B: the start peaked %.1f MiB over the opened log (load() alone kept %.1f MiB live), over %.1f MiB, the larger of the open's own test bound and the D14 budget, plus the index: the whole tail is read into memory",
			payload, mib(over), mib(retained), mib(bound))
	}
}

// startTail founds a one-voter node in dir and writes after its bootstrap
// committed entries of payload bytes, size bytes of records, then closes
// it: the node's id, the last index and the bytes of records written.
func startTail(t *testing.T, dir, run string, cid coreconsensus.ClusterID, payload int, size int64) (coreconsensus.NodeID, uint64, int64) {
	t.Helper()
	d := tailOpen(t, dir, run)
	id, inc, err := Init(d, cid, "s0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := tailConfig(cid, id, d, newKV())
	members := []coreconsensus.MemberValue{{ID: id, Incarnation: inc, Slot: "s0", SPKI: MemorySPKI(id), Voter: true}}
	if err := Bootstrap(context.Background(), &cfg, members); err != nil {
		t.Fatal(err)
	}
	_, last, records := tailFill(t, d.d.log, payload, size)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return id, last, records
}

// resumeKV is an orderKV that is also a DurableStateMachine: it reports
// applied the position it was made at, then each entry it applies.
type resumeKV struct {
	*orderKV
	pmu sync.Mutex
	li  coreconsensus.LogIdentityValue
	pos coreconsensus.PositionValue
}

func (d *resumeKV) Apply(e coreconsensus.EntryValue) any {
	r := d.orderKV.Apply(e)
	d.pmu.Lock()
	d.pos = e.Position
	d.pmu.Unlock()
	return r
}

func (d *resumeKV) AppliedPosition() (coreconsensus.LogIdentityValue, coreconsensus.PositionValue, error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()
	return d.li, d.pos, nil
}

// A durable machine that already holds part of a long tail the start loads
// without its payloads resumes right after its position, in the part read
// back from the log, and applies the rest once each, in order: nothing it
// holds is applied again, nothing after it skipped (#663).
func TestADurableMachineResumesInsideTheTailReadBack(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(t.TempDir(), "run")
	cid := coreconsensus.ClusterID{0x5d, 0x1d}
	id, last, _ := startTail(t, dir, run, cid, 64, 4<<20)
	d := tailOpen(t, dir, run)
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	stamp := must(d.Log().Stamp())(t)
	from := last / 3
	term := must(d.Log().Term(from))(t)
	m := &resumeKV{
		orderKV: &orderKV{inner: newKV(), payload: 64, next: from + 1}, li: stamp.Log,
		pos: coreconsensus.PositionValue{Term: term, Index: from},
	}
	cfg := tailConfig(cid, id, d, m)
	cfg.Durability = DurabilityDurable
	tr := cfg.Transport.(*MemoryTransport)
	c, err := withDefaults(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := startStamp(&c)
	if err != nil {
		t.Fatal(err)
	}
	x := newNode(&c, &st)
	x.obs = newObs(&c)
	start, err := x.load()
	if err != nil {
		t.Fatal(err)
	}
	if start.Evicted <= from || start.Applied != from {
		t.Fatalf("setup: the start loads without payloads through %d and resumes after %d, want past %d after %d", start.Evicted, start.Applied, from, from)
	}
	start, x = raft.StartState{}, nil
	node, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := node.HardClose(); err != nil {
			t.Error(err)
		}
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	}()
	waitFor(t, "the tail to be applied", func() bool { return m.applied.Load() >= last })
	if err := m.err(); err != nil {
		t.Fatal(err)
	}
	if got := m.inner.lastApplied(); got != last {
		t.Fatalf("the machine applied through %d, want %d", got, last)
	}
}

// lastApplied is the index of the last command s applied.
func (s *kv) lastApplied() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}
