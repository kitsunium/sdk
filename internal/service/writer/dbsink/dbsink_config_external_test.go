package dbsink_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
