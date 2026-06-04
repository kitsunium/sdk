package sample_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/sample"
)

type countingSink struct {
	writes atomic.Int64
}

func (c *countingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}

func (c *countingSink) Flush(_ context.Context) error { return nil }
func (c *countingSink) Close() error                  { return nil }

// controlledSink is a hand-written Sink whose Write/Flush/Close failure modes
// are dialled in per case, mirroring the failover package's test double. It
// lets a kept Write surface a downstream error so the sample wrapper's
// pass-through behaviour can be asserted against a known fault.
type controlledSink struct {
	writes atomic.Int64
	werr   error
	ferr   error
	cerr   error
}

func (c *controlledSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	c.writes.Add(1)
	if c.werr != nil {
		return 0, c.werr
	}
	return len(p), nil
}

func (c *controlledSink) Flush(_ context.Context) error { return c.ferr }
func (c *controlledSink) Close() error                  { return c.cerr }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rate     int
		nilDown  bool
		wantCode errs.Code
	}{
		{"valid rate + downstream succeeds", 10, false, 0},
		{"zero rate yields RateInvalid", 0, false, sample.CodeSampleRateInvalid},
		{"negative rate yields RateInvalid", -1, false, sample.CodeSampleRateInvalid},
		{"nil downstream yields DownstreamNil", 1, true, sample.CodeSampleDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink
			if !tc.nilDown {
				down = &countingSink{}
			}
			s, err := sample.New(down, tc.rate)
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("New err = %v, want nil", err)
				}
				if s == nil {
					t.Error("New returned nil sink on happy path")
				}
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func TestSample_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rate     int
		writes   int
		wantHits int64
	}{
		{"rate 1 forwards every write", 1, 5, 5},
		{"rate 10 keeps every 10th write", 10, 100, 10},
		{"rate 5 over 12 writes keeps 2", 5, 12, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingSink{}
			s, err := sample.New(down, tc.rate)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			for range tc.writes {
				if _, werr := s.Write(t.Context(), rec, []byte("x")); werr != nil {
					t.Errorf("Write err = %v", werr)
				}
			}
			if got := down.writes.Load(); got != tc.wantHits {
				t.Errorf("downstream hits = %d, want %d", got, tc.wantHits)
			}
		})
	}
}

func TestSample_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close delegate to downstream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingSink{}
			s, err := sample.New(down, 5)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Errorf("Flush err = %v", ferr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
		})
	}
}

func TestSampleSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"RateInvalid carries 0.3.20.1", sample.RateInvalid, sample.CodeSampleRateInvalid},
		{"DownstreamNil carries 0.3.20.2", sample.DownstreamNil, sample.CodeSampleDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// TestSample_Write_ErrorPropagation pins the kept-write branch: when the
// counter aligns with the rate the call is forwarded, so a downstream Write
// fault must surface to the caller rather than be swallowed by the modulo arm.
func TestSample_Write_ErrorPropagation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rate    int
		writes  int
		wantN   int
		wantErr bool
	}{
		{"downstream Write error propagates on kept write", 1, 1, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a failing downstream Write must reach the caller on a kept record.
			down := &controlledSink{werr: errors.New("boom")}
			s, err := sample.New(down, tc.rate)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			var (
				n    int
				werr error
			)
			//: rate 1 keeps the first call (1%1==0), so the fault is forwarded.
			for range tc.writes {
				n, werr = s.Write(t.Context(), rec, []byte("x"))
			}
			if (werr != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", werr, tc.wantErr)
			}
			if n != tc.wantN {
				t.Errorf("Write n = %d, want %d", n, tc.wantN)
			}
		})
	}
}

// TestSample_Write_DroppedReturnsZero pins the dropped-write branch: a call
// that misses the sampling window must satisfy the documented (0, nil)
// contract so callers cannot distinguish a drop from a success.
func TestSample_Write_DroppedReturnsZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		rate   int
		writes int
	}{
		{"dropped write returns (0, nil)", 3, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a non-nil werr would prove the drop never touched downstream.
			down := &controlledSink{werr: errors.New("must not be called")}
			s, err := sample.New(down, tc.rate)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			var (
				n    int
				werr error
			)
			//: counter reaches 1, 1%3!=0, so the single call is dropped.
			for range tc.writes {
				n, werr = s.Write(t.Context(), rec, []byte("x"))
			}
			if n != 0 || werr != nil {
				t.Errorf("dropped Write = (%d, %v), want (0, nil)", n, werr)
			}
			//: a dropped write must never reach the downstream sink.
			if down.writes.Load() != 0 {
				t.Errorf("downstream hits = %d, want 0", down.writes.Load())
			}
		})
	}
}

// TestSample_Write_KeptReturnsN proves the kept-write branch passes the
// downstream byte count straight back to the caller — the wrapper must not
// rewrite n on success.
func TestSample_Write_KeptReturnsN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rate    int
		payload []byte
		wantN   int
	}{
		{"kept write returns len(p)", 1, []byte("hello"), 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a clean downstream returns len(p), which must flow through unchanged.
			down := &controlledSink{}
			s, err := sample.New(down, tc.rate)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			n, werr := s.Write(t.Context(), rec, tc.payload)
			if werr != nil {
				t.Errorf("Write err = %v, want nil", werr)
			}
			if n != tc.wantN {
				t.Errorf("Write n = %d, want %d", n, tc.wantN)
			}
		})
	}
}

// TestSample_Flush_ErrorPropagates proves Flush is a pure pass-through — a
// downstream Flush fault must reach the caller, not be swallowed.
func TestSample_Flush_ErrorPropagates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ferr error
	}{
		{"Flush propagates downstream error", errors.New("flush boom")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &controlledSink{ferr: tc.ferr}
			s, err := sample.New(down, 1)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			//: the wrapper has no buffers, so the downstream error must survive intact.
			if ferr := s.Flush(t.Context()); !errors.Is(ferr, tc.ferr) {
				t.Errorf("Flush err = %v, want it to wrap %v", ferr, tc.ferr)
			}
		})
	}
}

// TestSample_Close_ErrorPropagates proves Close is a pure pass-through — a
// downstream Close fault must reach the caller, not be swallowed.
func TestSample_Close_ErrorPropagates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cerr error
	}{
		{"Close propagates downstream error", errors.New("close boom")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &controlledSink{cerr: tc.cerr}
			s, err := sample.New(down, 1)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			//: the wrapper holds no resources, so the downstream error must survive intact.
			if cerr := s.Close(); !errors.Is(cerr, tc.cerr) {
				t.Errorf("Close err = %v, want it to wrap %v", cerr, tc.cerr)
			}
		})
	}
}

// TestSample_Write_Concurrent exercises the atomic counter under real
// contention: the package docstring claims concurrent producers stay correct
// without a mutex, so 1000 writes at rate 10 must yield exactly 100 forwarded
// records regardless of goroutine interleaving (validated under -race).
func TestSample_Write_Concurrent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rate        int
		goroutines  int
		perRoutine  int
		wantForward int64
	}{
		{"1000 writes at rate 10 forward exactly 100", 10, 20, 50, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &controlledSink{}
			s, err := sample.New(down, tc.rate)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			//: a clean downstream never errors — a non-zero count flags a regression.
			var writeErrs atomic.Int64
			var wg sync.WaitGroup
			//: every producer races the same atomic counter on the hot path.
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perRoutine {
						if _, werr := s.Write(t.Context(), rec, []byte("x")); werr != nil {
							writeErrs.Add(1)
						}
					}
				})
			}
			wg.Wait()
			if writeErrs.Load() != 0 {
				t.Errorf("concurrent Write errors = %d, want 0", writeErrs.Load())
			}
			//: a lost increment would drop the forwarded count below total/rate.
			if got := down.writes.Load(); got != tc.wantForward {
				t.Errorf("forwarded = %d, want %d", got, tc.wantForward)
			}
		})
	}
}
