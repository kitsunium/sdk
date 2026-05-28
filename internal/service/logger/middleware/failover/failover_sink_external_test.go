package failover_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/failover"
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
