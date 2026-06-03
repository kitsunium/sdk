package failover_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/failover"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
)

type controlledSink struct {
	writes atomic.Int64
	flush  atomic.Int64
	closed atomic.Int64
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

func (c *controlledSink) Flush(_ context.Context) error {
	c.flush.Add(1)
	return c.ferr
}

func (c *controlledSink) Close() error {
	c.closed.Add(1)
	return c.cerr
}

// closeIgnore swallows the Close error in cleanup paths where the failure
// is not the assertion target, surfacing any anomaly through testing.TB so
// teardown problems stay visible without flipping the assertion target.
func closeIgnore(tb testing.TB, s corelogger.Sink) {
	tb.Helper()
	//: touch the receiver so the unused-param audit treats this no-op as intentional.
	if s == nil {
		//: nothing to close on the happy path.
		return
	}
	//: best-effort close — caller already asserted the meaningful failure.
	if cerr := s.Close(); cerr != nil {
		//: surface cleanup anomalies via t.Log so they are not invisible.
		tb.Logf("closeIgnore: Close err = %v", cerr)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		branches int
		wantErr  bool
	}{
		{"two branches succeeds", 2, false},
		{"single branch succeeds", 1, false},
		{"zero branches yields Empty", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			branches := make([]corelogger.Sink, tc.branches)
			for i := range tc.branches {
				branches[i] = &controlledSink{}
			}
			s, err := failover.New(branches...)
			if (err != nil) != tc.wantErr {
				t.Errorf("New err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && s == nil {
				t.Error("New returned nil sink on happy path")
			}
		})
	}
}

// TestNew_SkipsNilBranches covers the nil-skip arm of New: nil entries are
// silently dropped from the chain (documented contract), so a slate mixing
// nils with one real sink still yields a usable failover Sink.
func TestNew_SkipsNilBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		real    int
		wantErr bool
	}{
		{"nils plus one real branch succeeds", 1, false},
		{"only nils collapses to Empty", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: interleave two nil branches with tc.real concrete sinks.
			branches := []corelogger.Sink{nil, nil}
			for range tc.real {
				branches = append(branches, &controlledSink{})
			}
			s, err := failover.New(branches...)
			//: an all-nil slate must collapse to the Empty sentinel.
			if tc.wantErr {
				if !errs.HasCode(err, failover.CodeFailoverEmpty) {
					t.Errorf("New err = %v, want Empty", err)
				}
				return
			}
			//: a surviving real branch must yield a usable sink.
			if err != nil || s == nil {
				t.Errorf("New = (%v, %v), want a non-nil sink", s, err)
			}
		})
	}
}

func TestFailover_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		primaryFail  bool
		fallbackFail bool
		wantHits     int
		wantErr      bool
	}{
		{"primary succeeds — fallback skipped", false, false, 1, false},
		{"primary fails, fallback succeeds — both tried", true, false, 2, false},
		{"both fail — Exhausted returned", true, true, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			primary := &controlledSink{}
			fallback := &controlledSink{}
			if tc.primaryFail {
				primary.werr = errors.New("primary boom")
			}
			if tc.fallbackFail {
				fallback.werr = errors.New("fallback boom")
			}
			s, err := failover.New(primary, fallback)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.wantErr {
				if !errs.HasCode(werr, failover.CodeFailoverExhausted) {
					t.Errorf("err = %v, want Exhausted", werr)
				}
			} else if werr != nil {
				t.Errorf("Write err = %v, want nil", werr)
			}
			total := int(primary.writes.Load() + fallback.writes.Load())
			if total != tc.wantHits {
				t.Errorf("hits = %d, want %d", total, tc.wantHits)
			}
		})
	}
}

func TestFailover_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		bFails  bool
		wantErr bool
	}{
		{"clean flush hits every branch", false, false},
		{"a failing branch is aggregated, others still flush", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{}
			b := &controlledSink{}
			//: a failing downstream Flush must be collected, not short-circuited.
			if tc.bFails {
				b.ferr = errors.New("flush boom")
			}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(t.Context())
			//: the failure case must surface the joined branch error.
			if tc.wantErr {
				if !errors.Is(ferr, b.ferr) {
					t.Errorf("Flush err = %v, want it to wrap %v", ferr, b.ferr)
				}
			} else if ferr != nil {
				t.Errorf("Flush err = %v, want nil", ferr)
			}
			//: every branch is flushed unconditionally regardless of failures.
			if a.flush.Load() != 1 || b.flush.Load() != 1 {
				t.Errorf("flush counts: a=%d b=%d, want both 1", a.flush.Load(), b.flush.Load())
			}
		})
	}
}

func TestFailover_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		aFails  bool
		wantErr bool
	}{
		{"clean close hits every branch", false, false},
		{"a failing branch is aggregated, others still close", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{}
			b := &controlledSink{}
			//: a failing downstream Close must be collected, not short-circuited.
			if tc.aFails {
				a.cerr = errors.New("close boom")
			}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			cerr := s.Close()
			//: the failure case must surface the joined branch error.
			if tc.wantErr {
				if !errors.Is(cerr, a.cerr) {
					t.Errorf("Close err = %v, want it to wrap %v", cerr, a.cerr)
				}
			} else if cerr != nil {
				t.Errorf("Close err = %v, want nil", cerr)
			}
			//: every branch is closed unconditionally regardless of failures.
			if a.closed.Load() != 1 || b.closed.Load() != 1 {
				t.Errorf("close counts: a=%d b=%d, want both 1", a.closed.Load(), b.closed.Load())
			}
		})
	}
}

// TestNew_EmptyReturnsSentinel pins the exact identity of the error New
// returns for a zero-branch chain: it must both carry the dotted-quad code
// and unwrap to the public Empty sentinel.
func TestNew_EmptyReturnsSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"zero branches yields the Empty sentinel by code and identity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: New with no arguments must reject the empty chain.
			s, err := failover.New()
			//: a rejected construction must hand back no sink.
			if s != nil {
				t.Errorf("New() sink = %v, want nil", s)
			}
			//: the error must carry the dotted-quad Empty code for routing.
			if !errs.HasCode(err, failover.CodeFailoverEmpty) {
				t.Errorf("HasCode(err, CodeFailoverEmpty) = false, err = %v", err)
			}
			//: and it must unwrap to the exported Empty sentinel identity.
			if !errors.Is(err, failover.Empty) {
				t.Errorf("errors.Is(err, Empty) = false, err = %v", err)
			}
		})
	}
}

// TestFailover_Write_ExhaustedJoin proves the errors.Join inner chain inside
// Exhausted is reachable: every per-branch cause survives the wrap and stays
// matchable via errors.Is.
func TestFailover_Write_ExhaustedJoin(t *testing.T) {
	t.Parallel()
	//: three distinct sentinels so a collapsed join would fail at least one Is.
	sinkErr1 := errors.New("primary down")
	sinkErr2 := errors.New("secondary down")
	sinkErr3 := errors.New("tertiary down")
	tests := []struct {
		name string
	}{
		{"every per-branch cause is reachable through the joined Exhausted error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := failover.New(
				&controlledSink{werr: sinkErr1},
				&controlledSink{werr: sinkErr2},
				&controlledSink{werr: sinkErr3},
			)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: the top-level wrap must carry the Exhausted code.
			if !errs.HasCode(werr, failover.CodeFailoverExhausted) {
				t.Errorf("HasCode(werr, CodeFailoverExhausted) = false, werr = %v", werr)
			}
			//: each cause must remain individually matchable through the join.
			for _, cause := range []error{sinkErr1, sinkErr2, sinkErr3} {
				if !errors.Is(werr, cause) {
					t.Errorf("errors.Is(werr, %v) = false", cause)
				}
			}
		})
	}
}

// TestFailover_Write_ByteCount asserts Write returns the winning branch's
// byte count verbatim, not a hardcoded constant.
func TestFailover_Write_ByteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: payload's length is the exact count the primary must report.
		payload []byte
	}{
		{"primary success returns its own byte count", []byte("7 bytes")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a single always-succeed primary echoes len(payload).
			s, err := failover.New(&controlledSink{})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			n, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, tc.payload)
			if werr != nil {
				t.Fatalf("Write err = %v, want nil", werr)
			}
			//: the count must mirror the payload length exactly.
			if n != len(tc.payload) {
				t.Errorf("Write n = %d, want %d", n, len(tc.payload))
			}
		})
	}
}

// TestFailover_Flush_BothFail proves a two-branch Flush aggregates every
// downstream failure into a join reachable for both causes.
func TestFailover_Flush_BothFail(t *testing.T) {
	t.Parallel()
	errA := errors.New("flush a boom")
	errB := errors.New("flush b boom")
	tests := []struct {
		name string
	}{
		{"both flush failures are aggregated and individually matchable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{ferr: errA}
			b := &controlledSink{ferr: errB}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(t.Context())
			//: both independent flush causes must survive the join.
			if !errors.Is(ferr, errA) || !errors.Is(ferr, errB) {
				t.Errorf("Flush err = %v, want it to wrap both %v and %v", ferr, errA, errB)
			}
			//: Flush never short-circuits — both branches are hit exactly once.
			if a.flush.Load() != 1 || b.flush.Load() != 1 {
				t.Errorf("flush counts: a=%d b=%d, want both 1", a.flush.Load(), b.flush.Load())
			}
		})
	}
}

// TestFailover_Close_BothFail mirrors the Flush aggregation proof for Close.
func TestFailover_Close_BothFail(t *testing.T) {
	t.Parallel()
	errA := errors.New("close a boom")
	errB := errors.New("close b boom")
	tests := []struct {
		name string
	}{
		{"both close failures are aggregated and individually matchable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{cerr: errA}
			b := &controlledSink{cerr: errB}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			cerr := s.Close()
			//: both independent close causes must survive the join.
			if !errors.Is(cerr, errA) || !errors.Is(cerr, errB) {
				t.Errorf("Close err = %v, want it to wrap both %v and %v", cerr, errA, errB)
			}
			//: Close never short-circuits — both branches are hit exactly once.
			if a.closed.Load() != 1 || b.closed.Load() != 1 {
				t.Errorf("close counts: a=%d b=%d, want both 1", a.closed.Load(), b.closed.Load())
			}
		})
	}
}

// TestFailover_Write_Concurrent drives 50 concurrent producers through one
// failoverSink whose primary always succeeds; the -race detector guards the
// stateless fan-out contract and every write must return nil.
func TestFailover_Write_Concurrent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: goroutines is the producer fan-in the sink must tolerate raceless.
		goroutines int
	}{
		{"fifty concurrent producers all succeed without a data race", 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			primary := &controlledSink{}
			s, err := failover.New(primary)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			//: errCount lets goroutines flag failures without sharing t directly.
			var errCount atomic.Int64
			var wg sync.WaitGroup
			wg.Add(tc.goroutines)
			for range tc.goroutines {
				go func() {
					defer wg.Done()
					//: each producer drives a full Write through the shared sink;
					//: the WaitGroup keeps every goroutine inside the test scope so
					//: t.Context() stays live for the duration of the fan-in.
					if _, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); werr != nil {
						errCount.Add(1)
					}
				}()
			}
			wg.Wait()
			//: no producer may observe an error from the always-succeed primary.
			if got := errCount.Load(); got != 0 {
				t.Errorf("concurrent Write errors = %d, want 0", got)
			}
			//: every goroutine must have reached the primary exactly once.
			if got := primary.writes.Load(); got != int64(tc.goroutines) {
				t.Errorf("primary writes = %d, want %d", got, tc.goroutines)
			}
		})
	}
}

// TestFailover_Write_RealFileFallback is an end-to-end check: a failing
// primary cascades to a REAL on-disk file sink, and the test asserts the
// payload bytes actually landed in the file via the production Write path.
func TestFailover_Write_RealFileFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: payload is the exact byte sequence that must reach the disk file.
		payload []byte
	}{
		{"a failing primary falls back to a real file sink that persists the bytes", []byte("network down, persisted locally\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a real append-only file under TempDir is the terminal fallback.
			path := filepath.Join(t.TempDir(), "failover.log")
			fileBranch, err := filesink.New(path)
			if err != nil {
				t.Fatalf("file sink New err = %v", err)
			}
			//: release the descriptor deterministically even on failure.
			t.Cleanup(func() { closeIgnore(t, fileBranch) })
			//: primary always fails so traversal must reach the real file sink.
			primary := &controlledSink{werr: errors.New("collector unreachable")}
			s, err := failover.New(primary, fileBranch)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			n, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, tc.payload)
			//: the fallback success must surface a clean error end-to-end.
			if werr != nil {
				t.Fatalf("Write err = %v, want nil", werr)
			}
			//: the byte count must reflect what the file sink actually wrote.
			if n != len(tc.payload) {
				t.Errorf("Write n = %d, want %d", n, len(tc.payload))
			}
			//: flush fsyncs the page cache so the read below sees durable bytes.
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v, want nil", ferr)
			}
			//: read the real file back — the bytes must match the payload exactly.
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("ReadFile err = %v", rerr)
			}
			if string(got) != string(tc.payload) {
				t.Errorf("file contents = %q, want %q", got, tc.payload)
			}
		})
	}
}

func TestFailoverSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Exhausted carries 0.3.19.1", failover.Exhausted, failover.CodeFailoverExhausted},
		{"Empty carries 0.3.19.2", failover.Empty, failover.CodeFailoverEmpty},
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
