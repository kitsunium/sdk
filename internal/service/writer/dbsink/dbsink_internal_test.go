package dbsink

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/batcher"
)

// TestNewDBSink pins newDBSink's two zero-value substitutions: a non-positive
// maxRows falls back to defaultMaxRows, and a nil OnError degrades to a callable
// no-op (so the Write path can route to it unconditionally).
func TestNewDBSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		maxRows int
		onError func(error)
	}
	tests := []tc{
		{"non-positive maxRows and nil OnError take defaults", 0, nil},
		{"negative maxRows takes the default", -5, nil},
		{"explicit OnError is preserved as callable", 4, func(error) {}},
	}
	exec := func(context.Context, []corelogger.RecordEvent) error { return nil }
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := newDBSink(exec, c.maxRows, Config{OnError: c.onError})
		//: the constructor must always leave a callable onError so Write is
		//: branch-free; invoking it here proves it is non-nil.
		s.onError(nil)
		//: the seam must be wired so deliver can relay batches.
		if s.exec == nil {
			t.Fatalf("%s: exec seam not captured", c.name)
		}
		//: a constructed batcher is required for Add / Flush / Close to work.
		if s.batch == nil {
			t.Fatalf("%s: batcher not constructed", c.name)
		}
		//: Close joins any ticker and is the lifecycle terminal — must be clean.
		if err := s.Close(); err != nil {
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

// TestDbSink_deliver asserts deliver forwards the batch verbatim to the captured
// exec seam and relays its verdict unchanged.
func TestDbSink_deliver(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		execErr error
	}
	sentinel := errSentinel("boom")
	tests := []tc{
		{"nil seam result relays as success", nil},
		{"seam error relays unchanged", sentinel},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var gotLen int
		exec := func(_ context.Context, batch []corelogger.RecordEvent) error {
			//: record what the seam observed so the relay can be checked.
			gotLen = len(batch)
			return c.execErr
		}
		s := newDBSink(exec, 8, Config{})
		batch := []corelogger.RecordEvent{{Message: "a"}, {Message: "b"}}
		//: deliver is the batcher's Sink; call it directly to test the relay.
		err := s.deliver(t.Context(), batch)
		//: the seam must have seen the whole batch.
		if gotLen != len(batch) {
			t.Fatalf("%s: seam saw %d records want %d", c.name, gotLen, len(batch))
		}
		//: deliver relays the seam verdict identically (the batcher wraps it).
		if err != c.execErr { //nolint:errorlint // identity relay is the contract under test
			t.Fatalf("%s: deliver err=%v want %v", c.name, err, c.execErr)
		}
		if cerr := s.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", c.name, cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDbSink_Write asserts dbSink.Write appends a record to the batch (reported
// as accepted) and never returns a downstream error to the producer; the batched
// record is observable after a Flush.
func TestDbSink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload string
	}
	tests := []tc{
		{"a payload is reported accepted and batched", "hello"},
		{"an empty payload reports zero bytes", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var delivered int
		exec := func(_ context.Context, batch []corelogger.RecordEvent) error {
			//: count the records the seam received on Flush.
			delivered += len(batch)
			return nil
		}
		s := newDBSink(exec, 1<<30, Config{})
		//: Write must report the payload length and never surface an error.
		n, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "m"}, []byte(c.payload))
		if err != nil || n != len(c.payload) {
			t.Fatalf("%s: Write=(%d,%v) want (%d,nil)", c.name, n, err, len(c.payload))
		}
		//: Flush forces the batched record through the seam synchronously.
		if ferr := s.Flush(t.Context()); ferr != nil {
			t.Fatalf("%s: Flush: %v", c.name, ferr)
		}
		if delivered != 1 {
			t.Fatalf("%s: delivered=%d want 1", c.name, delivered)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDbSink_Write_OnErrorRouting asserts that a cap-triggered eager flush whose
// seam fails routes the wrapped failure to onError while Write still reports the
// payload accepted — the producer is never blocked nor failed by a slow/failing
// DB. This exercises the dbSink.Write cap-flush-failure branch (the onError relay).
func TestDbSink_Write_OnErrorRouting(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		maxRows int
	}
	tests := []tc{
		{"cap-flush failure routes to onError and Write returns accepted", 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var (
			mu   sync.Mutex
			errc []error
		)
		exec := func(context.Context, []corelogger.RecordEvent) error {
			//: always fail so reaching the cap triggers a routed delivery failure.
			return errSentinel("cap-boom")
		}
		onError := func(err error) {
			mu.Lock()
			defer mu.Unlock()
			//: capture every routed error so the wrap identity can be asserted.
			errc = append(errc, err)
		}
		s := newDBSink(exec, c.maxRows, Config{OnError: onError})
		first := []byte("one")
		second := []byte("two")
		//: first write fills the batch to the cap edge without yet flushing.
		if n, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "1"}, first); err != nil || n != len(first) {
			t.Fatalf("%s: first Write=(%d,%v) want (%d,nil)", c.name, n, err, len(first))
		}
		//: second write reaches the cap, eager-flushes, and the seam fails — the
		//: failure must route to onError, never surface to this caller.
		if n, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "2"}, second); err != nil || n != len(second) {
			t.Fatalf("%s: second Write=(%d,%v) want (%d,nil)", c.name, n, err, len(second))
		}
		mu.Lock()
		got := slices.Clone(errc)
		mu.Unlock()
		//: onError must have fired at least once on the failing cap-flush.
		if len(got) == 0 {
			t.Fatalf("%s: onError never fired on the failing cap-flush", c.name)
		}
		//: the routed error is the batcher's DeliverFailed wrap (cause reachable).
		if !errors.Is(got[0], batcher.BatcherDeliverFailed) {
			t.Fatalf("%s: routed err=%v want errors.Is batcher.BatcherDeliverFailed", c.name, got[0])
		}
		//: Close drains the remainder and joins any lifecycle goroutine.
		if cerr := s.Close(); cerr != nil && !errors.Is(cerr, batcher.BatcherDeliverFailed) {
			t.Fatalf("%s: Close: %v", c.name, cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDbSink_OnError_SerializedAcrossPaths is the V42 regression: Config.OnError
// is routed from two distinct goroutines — the cap-flush on the async drainer
// (dbSink.Write) and the FlushEvery ticker — so without serialization a user
// hook that touches shared state is entered concurrently. The hook here counts
// concurrent entrants without its own lock; the dbsink's serializing wrapper must
// keep the observed maximum at 1. Before the fix the two paths overlap (max == 2,
// and the unguarded counter trips -race); after it they queue behind one mutex.
func TestDbSink_OnError_SerializedAcrossPaths(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		writes     int
		flushEvery time.Duration
	}
	tests := []tc{
		{"cap-flush vs ticker race over many writes", 200, time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var (
			inFlight atomic.Int32
			maxSeen  atomic.Int32
			calls    atomic.Int32
		)
		onError := func(error) {
			//: record the peak concurrent entrant count; the wrapper must keep it 1.
			cur := inFlight.Add(1)
			//: publish a new peak when this entry exceeds the running maximum.
			for {
				prev := maxSeen.Load()
				//: stop once the recorded peak already covers this entry.
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			//: dwell inside the hook so a second path would overlap if unserialized.
			time.Sleep(2 * time.Millisecond)
			calls.Add(1)
			inFlight.Add(-1)
		}
		exec := func(context.Context, []corelogger.RecordEvent) error {
			//: every batch fails so both flush paths route to onError.
			return errSentinel("boom")
		}
		//: MaxRows 1 makes each Write cap-flush eagerly; FlushEvery keeps the ticker
		//: firing so the two failing paths race into the shared hook.
		s := newDBSink(exec, 1, Config{OnError: onError, FlushEvery: c.flushEvery})
		//: drive the cap-flush path hard from a producer goroutine.
		var wg sync.WaitGroup
		wg.Go(func() {
			//: many cap-flushes give the ticker chances to overlap a Write flush.
			for range c.writes {
				//: each Write reaches the cap and eager-flushes a failing batch.
				if _, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, []byte("x")); err != nil {
					t.Errorf("%s: Write: %v", c.name, err)
					return
				}
				//: yield so the ticker goroutine can interleave a flush.
				time.Sleep(time.Millisecond)
			}
		})
		wg.Wait()
		//: Close joins the ticker goroutine before the assertions read the counters.
		if err := s.Close(); err != nil && !errors.Is(err, batcher.BatcherDeliverFailed) {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		//: the hook fired on the failing flushes — the test is exercising the path.
		if calls.Load() == 0 {
			t.Fatalf("%s: onError never fired; the test never exercised the routed path", c.name)
		}
		//: the serializing wrapper must keep the user hook single-entrant.
		if peak := maxSeen.Load(); peak > 1 {
			t.Fatalf("%s: onError entered concurrently: peak in-flight=%d want 1 (V42)", c.name, peak)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFlush asserts dbSink.Flush delivers the pending batch and propagates the
// seam verdict to the caller (the caller-facing path does not route to onError).
func TestFlush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		execErr error
	}
	boom := errSentinel("flush-boom")
	tests := []tc{
		{"empty batch flushes clean", nil},
		{"seam failure propagates to the Flush caller", boom},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		exec := func(context.Context, []corelogger.RecordEvent) error { return c.execErr }
		s := newDBSink(exec, 1<<30, Config{})
		//: one buffered record so Flush has something to deliver.
		if _, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, []byte("x")); err != nil {
			t.Fatalf("%s: Write: %v", c.name, err)
		}
		//: a seam failure must reach the Flush caller (batcher wraps the cause).
		err := s.Flush(t.Context())
		if (err != nil) != (c.execErr != nil) {
			t.Fatalf("%s: Flush err=%v want non-nil=%v", c.name, err, c.execErr != nil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClose asserts dbSink.Close drains the final batch and is idempotent.
func TestClose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		writes int
		want   int
	}
	tests := []tc{
		{"Close drains a buffered record", 1, 1},
		{"Close on an empty sink delivers nothing", 0, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var delivered int
		exec := func(_ context.Context, batch []corelogger.RecordEvent) error {
			delivered += len(batch)
			return nil
		}
		s := newDBSink(exec, 1<<30, Config{})
		//: buffer c.writes sub-cap records so only Close forces delivery.
		for range c.writes {
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, []byte("x")); err != nil {
				t.Fatalf("%s: Write: %v", c.name, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		//: a second Close must be a clean no-op (idempotent contract).
		if err := s.Close(); err != nil {
			t.Fatalf("%s: second Close: %v", c.name, err)
		}
		if delivered != c.want {
			t.Fatalf("%s: delivered=%d want %d", c.name, delivered, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDbSink_Write_FillsZeroTime asserts dbSink.Write stamps the injected clock
// onto a record whose Time is the zero-value "fill at handle time" sentinel
// before batching, and leaves an already-set Time untouched — so the drivers
// never persist a year-0001 timestamp. Regression for F2-dbtime.
func TestDbSink_Write_FillsZeroTime(t *testing.T) {
	t.Parallel()
	frozen := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	explicit := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	type tc struct {
		name string
		in   time.Time
		want time.Time
	}
	tests := []tc{
		{"zero Time is filled from the injected clock", time.Time{}, frozen},
		{"a set Time is preserved verbatim", explicit, explicit},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got time.Time
		exec := func(_ context.Context, batch []corelogger.RecordEvent) error {
			//: capture the delivered record's Time so the fill can be asserted.
			got = batch[0].Time
			return nil
		}
		//: inject a frozen clock so the handle-time fill is deterministic.
		s := newDBSink(exec, 1<<30, Config{Clock: frozenClock{at: frozen}})
		if _, err := s.Write(t.Context(), corelogger.RecordEvent{Message: "m", Time: c.in}, []byte("p")); err != nil {
			t.Fatalf("%s: Write: %v", c.name, err)
		}
		//: Flush forces the batched record through the seam synchronously.
		if ferr := s.Flush(t.Context()); ferr != nil {
			t.Fatalf("%s: Flush: %v", c.name, ferr)
		}
		if !got.Equal(c.want) {
			t.Fatalf("%s: delivered Time=%v want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// frozenClock is a deterministic test Clock returning a fixed instant so the
// handle-time timestamp fill can be asserted without wall-clock flakiness.
type frozenClock struct{ at time.Time }

// Now returns the frozen instant.
func (f frozenClock) Now() time.Time {
	//: a fixed instant makes the fill assertion deterministic.
	return f.at
}

// Since returns the elapsed duration from t to the frozen instant.
func (f frozenClock) Since(t time.Time) time.Duration {
	//: subtraction against the frozen instant keeps the fake self-consistent.
	return f.at.Sub(t)
}

// errSentinel is a minimal test-only error type so the relay test can assert
// identity without depending on the errs package in a white-box test.
type errSentinel string

// Error implements the error interface for the white-box relay assertion.
func (e errSentinel) Error() string {
	//: the literal string is the message; identity is what the test checks.
	return string(e)
}
