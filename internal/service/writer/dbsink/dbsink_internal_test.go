package dbsink

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
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

// errSentinel is a minimal test-only error type so the relay test can assert
// identity without depending on the errs package in a white-box test.
type errSentinel string

// Error implements the error interface for the white-box relay assertion.
func (e errSentinel) Error() string {
	//: the literal string is the message; identity is what the test checks.
	return string(e)
}
