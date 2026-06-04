package dbsink_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	servicelogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/dbsink"
)

// CodeTestExecFailed is a test-local sentinel for the exec-failure routing case;
// it lives in a *_test.go file the AST audit excludes, so it never collides with
// a production code octet.
const CodeTestExecFailed errs.Code = 0x00_03_28_FF // 0.3.40.255 (test-only)

// execFailed is the test-only error the failing seam returns.
var execFailed = errs.Define(CodeTestExecFailed, "TEST_EXEC_FAILED",
	"test exec failed",
	"dbsink_test: injected exec failure for the OnError routing case")

// recorder captures the records a deliver seam receives so a test can assert on
// the coalesced batch. It is safe for concurrent use: the async drainer calls
// the seam on its own goroutine.
type recorder struct {
	mu      sync.Mutex
	batches [][]corelogger.RecordEvent
}

// exec records one delivered batch (cloned so a later caller reuse cannot tear
// the captured slice) and reports success.
func (r *recorder) exec(_ context.Context, batch []corelogger.RecordEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: copy the batch header so the recorder owns it independent of the caller.
	r.batches = append(r.batches, slices.Clone(batch))
	//: report the batch persisted.
	return nil
}

// total reports how many records have been delivered across all batches.
func (r *recorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	//: sum every delivered batch's length.
	for _, b := range r.batches {
		n += len(b)
	}
	//: the running delivered-record count.
	return n
}

// first returns the first delivered record, or false when none have arrived.
func (r *recorder) first() (corelogger.RecordEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: no batch yet — signal the caller to keep polling.
	if len(r.batches) == 0 || len(r.batches[0]) == 0 {
		return corelogger.RecordEvent{}, false
	}
	//: hand back the head record of the first delivered batch.
	return r.batches[0][0], true
}

// waitFor polls cond until it holds or a 2s deadline elapses, so the async
// drainer's background delivery can be observed without a fixed sleep. KTN
// forbids t.Skip, so a bounded poll (not an unconditional sleep) keeps the test
// deterministic on a slow CI box.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	//: poll on a short interval until the condition holds or time runs out.
	for time.Now().Before(deadline) {
		if cond() {
			//: condition observed within the budget.
			return true
		}
		time.Sleep(time.Millisecond)
	}
	//: final check after the budget so a just-in-time delivery still counts.
	return cond()
}

// writeN writes n identical records through sink, failing the test on any error.
func writeN(t *testing.T, sink corelogger.Sink, n int) {
	t.Helper()
	//: drive n writes; the configured cap policy is the subject under test.
	for range n {
		if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, []byte("x")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
}

// TestCompose asserts Compose returns a usable, non-nil Sink that delivers a
// written record through the supplied seam — the smoke test for the public
// constructor before the behaviour-specific cases below.
func TestCompose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  dbsink.Config
	}
	tests := []tc{
		{"zero Config composes a working sink", dbsink.Config{}},
		{"explicit caps compose a working sink", dbsink.Config{MaxRows: 4}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		sink := dbsink.Compose(rec.exec, c.cfg)
		//: a nil Sink would panic the logger handler at first Write.
		if sink == nil {
			t.Fatalf("%s: Compose returned nil Sink", c.name)
		}
		if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Message: "m"}, []byte("m")); err != nil {
			t.Fatalf("%s: Write: %v", c.name, err)
		}
		if err := sink.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		//: the composed chain must have delivered the single record on Close.
		if !waitFor(func() bool { return rec.total() == 1 }) {
			t.Fatalf("%s: delivered=%d want 1", c.name, rec.total())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDeliveryLifecycle exercises the batch delivery triggers Compose wires:
// explicit Flush, eager flush on MaxRows, and final-batch drain on Close.
func TestDeliveryLifecycle(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		maxRows int
		writes  int
		flush   bool
		closeIt bool
		want    int
	}
	tests := []tc{
		{"explicit Flush delivers the pending record", 1 << 30, 1, true, false, 1},
		{"reaching MaxRows eagerly flushes the full batch", 2, 2, false, false, 2},
		{"Close drains a sub-cap trailing batch", 1 << 30, 1, false, true, 1},
		{"Close drains the remainder after an eager flush", 2, 3, false, true, 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		sink := dbsink.Compose(rec.exec, dbsink.Config{MaxRows: c.maxRows})
		writeN(t, sink, c.writes)
		//: an explicit Flush must propagate the seam verdict synchronously.
		if c.flush {
			if err := sink.Flush(t.Context()); err != nil {
				t.Fatalf("Flush: %v", err)
			}
		}
		//: Close drains any trailing batch and joins the ticker / drainer.
		if c.closeIt {
			if err := sink.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}
		//: the async ring drains in the background; poll for the expected count.
		if !waitFor(func() bool { return rec.total() == c.want }) {
			t.Fatalf("delivered=%d want %d", rec.total(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDeliversRecordFaithfully asserts the structured record — Message and Attrs
// — reaches the seam intact, which is the whole point of a record-batching DB
// sink (the seam persists the structured record, not the encoded bytes p).
func TestDeliversRecordFaithfully(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      corelogger.RecordEvent
		wantMsg string
		wantKey string
	}
	tests := []tc{
		{
			name:    "message and attribute survive the batch",
			in:      corelogger.RecordEvent{Message: "hello", Attrs: []corelogger.AttrValue{{Key: "user"}}},
			wantMsg: "hello",
			wantKey: "user",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		sink := dbsink.Compose(rec.exec, dbsink.Config{MaxRows: 1 << 30})
		if _, err := sink.Write(t.Context(), c.in, []byte("ignored")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := sink.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		//: poll for the single delivery, then inspect the delivered record.
		if !waitFor(func() bool { return rec.total() == 1 }) {
			t.Fatalf("delivered=%d want 1", rec.total())
		}
		got, ok := rec.first()
		//: the delivered record must carry the original message and attribute.
		if !ok || got.Message != c.wantMsg || len(got.Attrs) != 1 || got.Attrs[0].Key != c.wantKey {
			t.Fatalf("delivered=%+v want Message=%q Attrs=[{%s}]", got, c.wantMsg, c.wantKey)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestExecErrorRoutesToOnError asserts a failing seam on a ticker-driven flush
// surfaces through Config.OnError rather than the producer's Write.
func TestExecErrorRoutesToOnError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"ticker-flush failure reaches OnError, never Write"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var mu sync.Mutex
		seen := 0
		failing := func(_ context.Context, _ []corelogger.RecordEvent) error {
			//: always fail so the ticker flush routes through OnError.
			return execFailed
		}
		onError := func(err error) {
			mu.Lock()
			defer mu.Unlock()
			//: count only the typed sentinel so unrelated noise is ignored.
			if errs.HasCode(err, CodeTestExecFailed) {
				seen++
			}
		}
		//: a short ticker drives a background flush whose failure must route out.
		sink := dbsink.Compose(failing, dbsink.Config{
			MaxRows:    1 << 30,
			FlushEvery: 5 * time.Millisecond,
			OnError:    onError,
		})
		//: Write itself must never return the downstream failure to the producer.
		if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Message: "f"}, []byte("f")); err != nil {
			t.Fatalf("Write must not surface downstream error: %v", err)
		}
		//: the ticker flush should fire and route the failure to OnError.
		ok := waitFor(func() bool {
			mu.Lock()
			defer mu.Unlock()
			return seen >= 1
		})
		//: Close before asserting so the goroutine is joined deterministically.
		if cerr := sink.Close(); cerr != nil {
			t.Fatalf("Close: %v", cerr)
		}
		if !ok {
			t.Fatalf("OnError never saw the exec failure")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestComposeMinLevelGate asserts the levelgate floor Compose wires drops
// below-threshold records before they ever reach the batch, and passes records
// at or above the floor.
func TestComposeMinLevelGate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		min       level.Level
		recordLvl level.Level
		want      int
	}
	tests := []tc{
		{"below floor is dropped by the gate", level.Error, level.Info, 0},
		{"at floor passes the gate", level.Error, level.Error, 1},
		{"above floor passes the gate", level.Warn, level.Error, 1},
		{"zero MinLevel (Info) inherits and passes Info", level.Info, level.Info, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		sink := dbsink.Compose(rec.exec, dbsink.Config{MaxRows: 1 << 30, MinLevel: c.min})
		//: a single record at the case level; the gate decides whether it batches.
		if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Level: c.recordLvl}, []byte("x")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		//: Close drains whatever survived the gate so the count is deterministic.
		if err := sink.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !waitFor(func() bool { return rec.total() == c.want }) {
			t.Fatalf("delivered=%d want %d", rec.total(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestComposeDefaultRowCap asserts the zero-value MaxRows applies the shell's
// default cap rather than batching unbounded: writing past the default forces an
// eager flush even though no explicit Flush / Close ran.
func TestComposeDefaultRowCap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		writes      int
		wantAtLeast int
	}
	tests := []tc{
		// defaultMaxRows is 256; 300 writes must trigger at least one eager flush.
		{"writing past the default cap forces an eager flush", 300, 256},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		//: zero MaxRows must fall back to the finite default, not batch forever.
		sink := dbsink.Compose(rec.exec, dbsink.Config{})
		writeN(t, sink, c.writes)
		//: an eager flush must have delivered a full default-sized batch already.
		got := waitFor(func() bool { return rec.total() >= c.wantAtLeast })
		//: Close first so the drainer goroutine is always joined, pass or fail.
		if err := sink.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !got {
			t.Fatalf("delivered=%d want >=%d from default-cap eager flush", rec.total(), c.wantAtLeast)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestComposeFlushEvery_HappyPath asserts the FlushEvery ticker delivers a
// partial sub-cap batch on its own — without any explicit Flush or Close — so a
// trickle of records still reaches the database within the interval bound.
func TestComposeFlushEvery_HappyPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		flushEvery time.Duration
	}
	tests := []tc{
		{"ticker delivers a partial batch without explicit Flush or Close", 5 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &recorder{}
		//: a cap that is never hit isolates the ticker as the sole delivery trigger.
		sink := dbsink.Compose(rec.exec, dbsink.Config{MaxRows: 1 << 30, FlushEvery: c.flushEvery})
		writeN(t, sink, 1)
		//: only the background ticker can flush this sub-cap batch; poll for it.
		if !waitFor(func() bool { return rec.total() == 1 }) {
			t.Fatalf("%s: ticker delivered=%d want 1", c.name, rec.total())
		}
		//: Close joins the ticker goroutine so the test leaves no drainer running.
		if err := sink.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestComposeOnDrop asserts a saturated async ring fires Config.OnDrop: when the
// deliver seam blocks so the ring cannot drain, writes past its capacity are
// dropped and the back-pressure count surfaces to the observer rather than
// blocking the producer on a stalled database.
func TestComposeOnDrop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		writes int
	}
	tests := []tc{
		{"saturated ring fires OnDrop", 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		release := make(chan struct{})
		blocking := func(context.Context, []corelogger.RecordEvent) error {
			//: stall the drainer until released so the single-slot ring saturates.
			<-release
			return nil
		}
		var dropped atomic.Int64
		onDrop := func(missed int) {
			//: accumulate the dropped count the saturated ring reports.
			dropped.Add(int64(missed))
		}
		//: BufferSize 1 plus a blocked seam guarantees the ring cannot absorb the
		//: burst, so back-pressure must surface as drops.
		sink := dbsink.Compose(blocking, dbsink.Config{MaxRows: 1 << 30, BufferSize: 1, OnDrop: onDrop})
		//: drive the burst directly (not writeN): the default DropNewest policy
		//: surfaces BufferFull on the saturating writes, which is the back-pressure
		//: contract under test, not a test failure — so swallow that sentinel here.
		for range c.writes {
			//: a saturated ring returns BufferFull (typed); any OTHER error is a bug.
			if _, err := sink.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, []byte("x")); err != nil && !errs.HasCode(err, async.CodeAsyncBufferFull) {
				t.Fatalf("%s: Write surfaced unexpected error: %v", c.name, err)
			}
		}
		//: a saturated ring under a blocked seam must have dropped at least once.
		gotDrop := waitFor(func() bool { return dropped.Load() >= 1 })
		//: release the seam so the drainer can finish and Close can join it.
		close(release)
		if err := sink.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		if !gotDrop {
			t.Fatalf("%s: dropped=%d want >=1 from a saturated ring", c.name, dropped.Load())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// capturedRecords is the no-clone deliver-seam sink for the recycle-aliasing
// repro: it retains each delivered record's Attrs slice header BY REFERENCE
// (not slices.Clone like recorder), so a later use-after-recycle of the backing
// array is observable here / by -race.
type capturedRecords struct {
	mu   sync.Mutex
	recs []corelogger.RecordEvent
}

// exec retains the live RecordEvent values (and their Attrs slice headers) so a
// use-after-recycle of the backing array would be observable downstream.
func (c *capturedRecords) exec(_ context.Context, batch []corelogger.RecordEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: keep the record headers verbatim — no defensive copy on purpose.
	c.recs = append(c.recs, batch...)
	return nil
}

// snapshotAttrs returns an independent string view of every captured record's
// attrs so later mutation of the underlying array cannot retroactively change
// what we compare against.
func snapshotAttrs(recs []corelogger.RecordEvent) [][]string {
	out := make([][]string, 0, len(recs))
	//: stringify each record's attrs into an owned row.
	for _, r := range recs {
		row := make([]string, 0, len(r.Attrs))
		//: copy every key=value into the owned snapshot row.
		for _, a := range r.Attrs {
			row = append(row, a.Key+"="+a.Value.String())
		}
		out = append(out, row)
	}
	return out
}

// TestRecycledAttrsNotCorruptedUnderRace is the V101/V43/V118 repro. It warms
// the producer recordPool, parks a victim record carrying distinctive string
// attrs in the dbSink batch, then forces many subsequent recycled Build().Send()
// cycles whose attrs would overwrite an aliased backing array, then flushes and
// asserts the parked record's attrs survived intact. Run with -race.
//
// Result: PASS — genericHandler.Handle reassigns r.Attrs to a FRESH slice on
// every record before the sink chain sees it (handler.go mergeAttrs clone), so
// the recycled b.attrs backing array never crosses into async/dbSink. V101 is
// REFUTED; V118's untested-invariant gap is closed by this -race regression.
func TestRecycledAttrsNotCorruptedUnderRace(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		warm    int
		churn   int
		wantKey string
		wantVal string
	}
	tests := []tc{
		{"light recycle storm", 64, 512, "victim", "ORIGINAL-VALUE"},
		{"heavy recycle storm", 256, 2048, "victim", "ORIGINAL-VALUE"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seam := &capturedRecords{}
		//: real production sink chain: levelgate(async(dbSink)). A huge MaxRows
		//: keeps every record parked in the batch until the explicit Flush, so
		//: the window between "record batched" and "seam reads Attrs" stays open
		//: across all the recycling churn below — the worst case for aliasing.
		sink := dbsink.Compose(seam.exec, dbsink.Config{MaxRows: 1 << 30})
		//: front the sink with the SHIPPED handler — the component the audit says
		//: the no-clone safety actually depends on (handler.go mergeAttrs clone).
		h, err := servicelogger.NewHandler(encoder.NewText(nil), sink, level.Debug)
		if err != nil {
			t.Fatalf("%s: NewHandler: %v", c.name, err)
		}
		lg, err := servicelogger.New(h)
		if err != nil {
			t.Fatalf("%s: New: %v", c.name, err)
		}
		ctx := t.Context()
		//: warm the recordPool so the victim Send pulls a recycled builder whose
		//: b.attrs backing array has already been used (and will be reused).
		for i := range c.warm {
			servicelogger.Build(lg, level.Info).Str("warm", "warm").Int("i", i).Send(ctx, "warmup")
		}
		//: drain the warmups out of the batch so only the victim + churn remain.
		if err := sink.Flush(ctx); err != nil {
			t.Fatalf("%s: Flush warmups: %v", c.name, err)
		}
		seam.mu.Lock()
		seam.recs = seam.recs[:0]
		seam.mu.Unlock()
		//: the victim — distinctive attr values we will check survived recycling.
		servicelogger.Build(lg, level.Info).
			Str(c.wantKey, c.wantVal).Str("trace", "victim-trace-id").Int("n", 7).
			Send(ctx, "victim record")
		//: force many recycled Build().Send() cycles. Each pulls the same recycled
		//: *chainBuilder (b.attrs[:0]) and re-appends DIFFERENT values onto the SAME
		//: backing array. If the victim's Attrs aliased that array (V101's claim),
		//: these appends would overwrite it in place and trip -race.
		for range c.churn {
			servicelogger.Build(lg, level.Info).
				Str("victim", "POISON-OVERWRITE").Str("trace", "poison-trace").Int("n", 9999).
				Send(ctx, "churn")
		}
		//: deliver everything to the seam now (after the churn already happened).
		if err := sink.Flush(ctx); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		seam.mu.Lock()
		got := snapshotAttrs(seam.recs)
		seam.mu.Unlock()
		//: at least the victim must have been delivered.
		if len(got) == 0 {
			t.Fatalf("%s: no records delivered to the seam", c.name)
		}
		//: the FIRST delivered record is the victim; its attrs must be ORIGINAL.
		victim := got[0]
		foundVictim := false
		//: scan the victim's attrs for the original value and any poison overwrite.
		for _, kv := range victim {
			//: the original key=value must be present and untouched.
			if kv == c.wantKey+"="+c.wantVal {
				foundVictim = true
			}
			//: any poison value here means the backing array was aliased.
			if kv == "victim=POISON-OVERWRITE" || kv == "trace=poison-trace" || kv == "n=9999" {
				t.Fatalf("%s: CORRUPTION: parked victim attrs overwritten by a "+
					"recycled producer — got %q (aliased recycled backing array). "+
					"V101 reproduced.", c.name, victim)
			}
		}
		if !foundVictim {
			t.Fatalf("%s: victim attr %s=%s missing from delivered record %q", c.name, c.wantKey, c.wantVal, victim)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestComposeWriteAfterClose pins the real post-Close contract of the composed
// chain: the OUTERMOST async middleware (Compose wraps levelgate(async(dbSink)))
// owns the Close lifecycle, so a Write after Close is rejected at the async layer
// with the typed ASYNC_STOPPED sentinel returned to the caller — it never reaches
// the inner dbSink batcher, so Config.OnError (which only observes batcher/drainer
// failures) is correctly NOT invoked, and no record is delivered. (The dbSink-level
// BATCHER_CLOSED → onError relay is exercised white-box in the internal test, where
// the bare batcher is reachable without the async interceptor.)
func TestComposeWriteAfterClose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"Write after Close returns ASYNC_STOPPED and does not invoke OnError"},
	}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var (
			mu       sync.Mutex
			errSeen  int
			rec      = &recorder{}
			onErrorf = func(error) {
				mu.Lock()
				defer mu.Unlock()
				//: count any routed error so its absence on this path is provable.
				errSeen++
			}
		)
		sink := dbsink.Compose(rec.exec, dbsink.Config{MaxRows: 1 << 30, OnError: onErrorf})
		//: close first so the subsequent Write hits the stopped async layer.
		if err := sink.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		//: the late Write must surface the typed ASYNC_STOPPED to the caller.
		_, err := sink.Write(t.Context(), corelogger.RecordEvent{Message: "late"}, []byte("late"))
		if !errs.HasCode(err, async.CodeAsyncStopped) {
			t.Fatalf("post-Close Write err=%v want HasCode ASYNC_STOPPED", err)
		}
		//: the rejected record must never have reached the deliver seam.
		if rec.total() != 0 {
			t.Fatalf("delivered=%d want 0: a post-Close record must not be delivered", rec.total())
		}
		//: OnError observes batcher/drainer failures only; this path bypasses it.
		mu.Lock()
		defer mu.Unlock()
		if errSeen != 0 {
			t.Fatalf("OnError fired %d times: the async-stopped path must not route to OnError", errSeen)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
