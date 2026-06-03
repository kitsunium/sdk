package memory_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/service/logger/sink/memory"
)

const (
	// kindCancelled cancels the context outright (context.Canceled).
	kindCancelled cancelKind = iota
	// kindDeadline gives the context a deadline already in the past
	// (context.DeadlineExceeded) without sleeping.
	kindDeadline
)

// cancelKind selects which flavour of context expiry a cancellation-identity
// case exercises, so each row builds its own dead context deterministically.
type cancelKind int

// nilCtx is a typed nil context the nil-context contract tests pass to Write and
// Flush; routing the untyped nil through a variable keeps the literal out of the
// call site while still exercising the production ctx != nil guard.
func nilCtx() context.Context {
	//: returning a nil interface value drives the non-cancelled nil-ctx path.
	return nil
}

// Test_NewMemory verifies that NewMemory returns an empty sink across the
// construction scenarios.
func Test_NewMemory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantNil bool
	}{
		{name: "fresh sink has nil records", wantNil: true},
	}

	//: each construction scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			got := m.Records()
			//: a fresh sink must report nil records.
			if (got == nil) != tc.wantNil {
				t.Fatalf("Records() == nil = %v, want %v", got == nil, tc.wantNil)
			}
		})
	}
}

// Test_Memory_Write verifies that Write buffers records in arrival order,
// returns len(p), and honours context cancellation.
func Test_Memory_Write(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		records   []corelogger.RecordEvent
		payload   []byte
		cancelled bool
		wantLen   int
		wantN     int
		wantErr   bool
	}{
		{
			name:    "no records leaves the buffer empty",
			records: nil,
			payload: []byte("x"),
			wantLen: 0,
			wantN:   0,
		},
		{
			name:    "single record returns payload length",
			records: []corelogger.RecordEvent{{Message: "hello"}},
			payload: []byte("hello-bytes"),
			wantLen: 1,
			wantN:   len("hello-bytes"),
		},
		{
			name: "multiple records preserve order",
			records: []corelogger.RecordEvent{
				{Message: "first"},
				{Message: "second"},
				{Message: "third"},
			},
			payload: []byte{},
			wantLen: 3,
			wantN:   0,
		},
		{
			name:      "cancelled context skips append",
			records:   []corelogger.RecordEvent{{Message: "dropped"}},
			payload:   []byte("ignored"),
			cancelled: true,
			wantLen:   0,
			wantN:     0,
			wantErr:   true,
		},
	}

	//: exercise each buffering scenario independently.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			ctx := t.Context()
			//: a cancelled context exercises the early-return guard.
			if tc.cancelled {
				c, cancel := context.WithCancel(t.Context())
				cancel()
				ctx = c
			}
			var (
				lastN   int
				lastErr error
			)
			//: feed every record through the sink under test.
			for _, rec := range tc.records {
				lastN, lastErr = m.Write(ctx, rec, tc.payload)
			}
			//: the error expectation must match whether the ctx was cancelled.
			if (lastErr != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", lastErr, tc.wantErr)
			}
			//: the reported byte count must equal the payload length on success.
			if lastN != tc.wantN {
				t.Fatalf("n = %d, want %d", lastN, tc.wantN)
			}
			got := m.Records()
			//: the buffer length must match the number of buffered records.
			if len(got) != tc.wantLen {
				t.Fatalf("expected %d records, got %d", tc.wantLen, len(got))
			}
			//: stored messages must match the buffered order.
			for i := range tc.wantLen {
				if got[i].Message != tc.records[i].Message {
					t.Fatalf("record %d: expected %q, got %q", i, tc.records[i].Message, got[i].Message)
				}
			}
		})
	}
}

// Test_Memory_Write_Defensive verifies that mutating the caller's Attrs slice
// after Write does not corrupt the recorded snapshot, and that the record's
// level is preserved.
func Test_Memory_Write_Defensive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		lvl       level.Level
		wantValue string
		wantLevel level.Level
	}{
		{
			name:      "attr clone insulates snapshot and level survives",
			lvl:       level.Warn,
			wantValue: "original",
			wantLevel: level.Warn,
		},
	}

	//: each defensive-copy scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			attrs := []corelogger.AttrValue{{Key: "k", Value: corelogger.StringValue("original")}}
			m := memory.NewMemory()
			n, err := m.Write(t.Context(), corelogger.RecordEvent{Level: tc.lvl, Message: "msg", Attrs: attrs}, nil)
			//: Write must accept the record without error.
			if err != nil {
				t.Fatalf("Write returned error: %v (n=%d)", err, n)
			}
			attrs[0] = corelogger.AttrValue{Key: "k", Value: corelogger.StringValue("mutated")}

			got := m.Records()
			//: the snapshot must retain the attr value present at Write time.
			if v := got[0].Attrs[0].Value.String(); v != tc.wantValue {
				t.Fatalf("snapshot value = %q, want %q", v, tc.wantValue)
			}
			//: the stored level must match what was written.
			if got[0].Level != tc.wantLevel {
				t.Fatalf("level = %v, want %v", got[0].Level, tc.wantLevel)
			}
		})
	}
}

// Test_Memory_Flush verifies that Flush is a no-op on a live context and
// honours cancellation.
func Test_Memory_Flush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cancelled bool
		wantErr   bool
	}{
		{name: "live context flushes clean", cancelled: false, wantErr: false},
		{name: "cancelled context surfaces error", cancelled: true, wantErr: true},
	}

	//: each flush scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			ctx := t.Context()
			//: a cancelled context exercises the early-return guard.
			if tc.cancelled {
				c, cancel := context.WithCancel(t.Context())
				cancel()
				ctx = c
			}
			err := m.Flush(ctx)
			//: the error expectation must match whether the ctx was cancelled.
			if (err != nil) != tc.wantErr {
				t.Fatalf("Flush err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Test_Memory_Close verifies that Close is a no-op and leaves buffered records
// readable.
func Test_Memory_Close(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		writes  int
		wantLen int
	}{
		{name: "records survive close", writes: 1, wantLen: 1},
	}

	//: each close scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: pre-load the buffer with the configured number of writes.
			for range tc.writes {
				n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "a"}, nil)
				//: each write must succeed before Close.
				if err != nil {
					t.Fatalf("Write returned error: %v (n=%d)", err, n)
				}
			}
			//: Close must succeed.
			if err := m.Close(); err != nil {
				t.Fatalf("Close returned error: %v", err)
			}
			//: records buffered before Close must remain readable afterwards.
			if got := m.Records(); len(got) != tc.wantLen {
				t.Fatalf("expected %d records after Close, got %d", tc.wantLen, len(got))
			}
		})
	}
}

// Test_Memory_Records verifies that Records returns a slice detached from the
// internal buffer so callers cannot mutate recorded history.
func Test_Memory_Records(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{name: "mutating a returned copy does not affect later reads", want: "one"},
	}

	//: each isolation scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "one"}, nil)
			//: the seed write must succeed.
			if err != nil {
				t.Fatalf("Write returned error: %v (n=%d)", err, n)
			}
			first := m.Records()
			first[0].Message = "tampered"

			second := m.Records()
			//: a fresh read must be unaffected by mutation of an earlier copy.
			if second[0].Message != tc.want {
				t.Fatalf("Records()[0].Message = %q, want %q", second[0].Message, tc.want)
			}
		})
	}
}

// Test_Memory_Reset verifies that Reset clears all buffered records.
func Test_Memory_Reset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		writes int
	}{
		{name: "reset empties a populated buffer", writes: 2},
	}

	//: each reset scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: pre-load the buffer with the configured number of writes.
			for range tc.writes {
				n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, nil)
				//: each seed write must succeed.
				if err != nil {
					t.Fatalf("Write returned error: %v (n=%d)", err, n)
				}
			}
			m.Reset()
			//: after Reset the buffer must be empty.
			if got := m.Records(); got != nil {
				t.Fatalf("expected nil records after Reset, got %v", got)
			}
		})
	}
}

// Test_Memory_Concurrent verifies that concurrent Write calls are race-free and
// that every record is retained.
func Test_Memory_Concurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		goroutines   int
		perGoroutine int
	}{
		{name: "16 writers x 32 records", goroutines: 16, perGoroutine: 32},
	}

	//: each concurrency scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			ctx := t.Context()
			var wg sync.WaitGroup
			wg.Add(tc.goroutines)
			//: fan out concurrent writers to stress the mutex guard.
			for range tc.goroutines {
				go func() {
					defer wg.Done()
					//: each goroutine writes a fixed batch of records.
					for range tc.perGoroutine {
						_, err := m.Write(ctx, corelogger.RecordEvent{Message: "concurrent"}, nil)
						//: a write failure under contention is a real defect.
						if err != nil {
							t.Errorf("concurrent Write returned error: %v", err)
							return
						}
					}
				}()
			}
			wg.Wait()

			got := m.Records()
			want := tc.goroutines * tc.perGoroutine
			//: every written record across all goroutines must be retained.
			if len(got) != want {
				t.Fatalf("expected %d records, got %d", want, len(got))
			}
			//: the write counter must agree with the retained record count.
			if m.Len() != want {
				t.Fatalf("Len() = %d, want %d", m.Len(), want)
			}
		})
	}
}

// Test_Len verifies that Len reports the number of accepted Write calls and
// resets to zero after Reset.
func Test_Len(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		writes    int
		reset     bool
		wantAfter int
	}{
		{name: "counts writes", writes: 3, reset: false, wantAfter: 3},
		{name: "reset zeroes the counter", writes: 3, reset: true, wantAfter: 0},
	}

	//: each counter scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: drive the configured number of writes through the sink.
			for range tc.writes {
				n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "x"}, nil)
				//: each seed write must succeed.
				if err != nil {
					t.Fatalf("Write returned error: %v (n=%d)", err, n)
				}
			}
			//: an optional Reset exercises the counter-clearing path.
			if tc.reset {
				m.Reset()
			}
			//: Len must report the expected post-condition count.
			if got := m.Len(); got != tc.wantAfter {
				t.Fatalf("Len() = %d, want %d", got, tc.wantAfter)
			}
		})
	}
}

// deadCtx returns a context already expired in the manner kind selects, so the
// identity tests assert on the exact sentinel the production guard surfaces.
func deadCtx(t *testing.T, kind cancelKind) context.Context {
	t.Helper()
	//: branch on the requested expiry flavour so the row controls the sentinel.
	switch kind {
	case kindCancelled:
		//: cancel immediately so ctx.Err() is context.Canceled.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		return ctx
	case kindDeadline:
		//: a past deadline makes ctx.Err() context.DeadlineExceeded without a sleep.
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	default:
		//: an unknown kind is a test-author bug, not a runtime condition.
		t.Fatalf("unknown cancelKind %d", kind)
		return nil
	}
}

// Test_Memory_Write_CancelledErrorIdentity verifies that Write surfaces the
// exact cancellation cause of the supplied context, not a relabelled error, so
// errors.Is keeps matching the stdlib sentinel.
func Test_Memory_Write_CancelledErrorIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    cancelKind
		wantErr error
	}{
		{name: "cancelled context returns context.Canceled", kind: kindCancelled, wantErr: context.Canceled},
		{name: "deadline-exceeded context returns context.DeadlineExceeded", kind: kindDeadline, wantErr: context.DeadlineExceeded},
	}

	//: each cancellation flavour must surface its own sentinel.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			n, err := m.Write(deadCtx(t, tc.kind), corelogger.RecordEvent{Message: "dropped"}, []byte("ignored"))
			//: the surfaced error must preserve the context's cancellation identity.
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Write err = %v, want errors.Is(%v)", err, tc.wantErr)
			}
			//: a cancelled Write must report zero bytes written.
			if n != 0 {
				t.Fatalf("n = %d, want 0 on cancellation", n)
			}
			//: a cancelled Write must skip the append entirely.
			if got := m.Records(); got != nil {
				t.Fatalf("expected no records after cancelled Write, got %v", got)
			}
		})
	}
}

// Test_Memory_Flush_CancelledErrorIdentity verifies that Flush surfaces the
// exact cancellation cause of the supplied context, mirroring Write's contract.
func Test_Memory_Flush_CancelledErrorIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    cancelKind
		wantErr error
	}{
		{name: "cancelled context returns context.Canceled", kind: kindCancelled, wantErr: context.Canceled},
		{name: "deadline-exceeded context returns context.DeadlineExceeded", kind: kindDeadline, wantErr: context.DeadlineExceeded},
	}

	//: each cancellation flavour must surface its own sentinel.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			err := m.Flush(deadCtx(t, tc.kind))
			//: the surfaced error must preserve the context's cancellation identity.
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Flush err = %v, want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// Test_Memory_Write_NilContext verifies that a nil context is treated as
// non-cancelled, pinning the ctx != nil guard so Write buffers the record.
func Test_Memory_Write_NilContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		message string
	}{
		{name: "nil context is treated as non-cancelled", payload: []byte("payload"), message: "kept"},
	}

	//: each nil-context scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: nil ctx must take the non-cancelled path rather than panic.
			n, err := m.Write(nilCtx(), corelogger.RecordEvent{Message: tc.message}, tc.payload)
			//: a nil context must not produce an error.
			if err != nil {
				t.Fatalf("Write err = %v, want nil for nil ctx", err)
			}
			//: the reported byte count must equal the payload length.
			if n != len(tc.payload) {
				t.Fatalf("n = %d, want %d", n, len(tc.payload))
			}
			got := m.Records()
			//: the record must be buffered exactly once.
			if len(got) != 1 {
				t.Fatalf("expected 1 record, got %d", len(got))
			}
			//: the buffered record must carry the written message.
			if got[0].Message != tc.message {
				t.Fatalf("Records()[0].Message = %q, want %q", got[0].Message, tc.message)
			}
		})
	}
}

// Test_Memory_Flush_NilContext verifies that Flush treats a nil context as
// non-cancelled and returns no error.
func Test_Memory_Flush_NilContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "nil context is treated as non-cancelled"},
	}

	//: each nil-context scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: nil ctx must take the non-cancelled path rather than panic.
			err := m.Flush(nilCtx())
			//: a nil context must not produce an error.
			if err != nil {
				t.Fatalf("Flush err = %v, want nil for nil ctx", err)
			}
		})
	}
}

// Test_NewMemory_Len verifies that a freshly constructed sink reports a zero
// write counter, pinning the writes-counter zero value.
func Test_NewMemory_Len(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantLen int
	}{
		{name: "fresh sink has a zero write counter", wantLen: 0},
	}

	//: each construction scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: a never-written sink must report a zero accepted-write count.
			if got := m.Len(); got != tc.wantLen {
				t.Fatalf("Len() = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

// Test_Memory_ResetThenWrite verifies that the sink is fully reusable after
// Reset: a second batch of writes neither inherits the pre-Reset records nor
// the pre-Reset counter.
func Test_Memory_ResetThenWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		first   int
		second  int
		wantLen int
	}{
		{name: "sink is fully reusable after Reset", first: 5, second: 2, wantLen: 2},
	}

	//: each lifecycle scenario is independent.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := memory.NewMemory()
			//: seed the pre-Reset batch that Reset must discard.
			for range tc.first {
				n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "before"}, nil)
				//: each pre-Reset write must succeed.
				if err != nil {
					t.Fatalf("pre-Reset Write returned error: %v (n=%d)", err, n)
				}
			}
			m.Reset()
			//: write the post-Reset batch whose count the sink must report.
			for range tc.second {
				n, err := m.Write(t.Context(), corelogger.RecordEvent{Message: "after"}, nil)
				//: each post-Reset write must succeed.
				if err != nil {
					t.Fatalf("post-Reset Write returned error: %v (n=%d)", err, n)
				}
			}
			//: Records must hold only the post-Reset batch, not the sum of both.
			if got := m.Records(); len(got) != tc.wantLen {
				t.Fatalf("expected %d records after Reset+write, got %d", tc.wantLen, len(got))
			}
			//: the write counter must also reflect only the post-Reset batch.
			if got := m.Len(); got != tc.wantLen {
				t.Fatalf("Len() = %d, want %d after Reset+write", got, tc.wantLen)
			}
		})
	}
}
