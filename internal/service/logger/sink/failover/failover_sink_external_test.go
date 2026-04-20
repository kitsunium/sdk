package failover_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/failover"
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
		name string
	}{
		{"Flush hits every branch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{}
			b := &controlledSink{}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Errorf("Flush err = %v", ferr)
			}
			if a.flush.Load() != 1 || b.flush.Load() != 1 {
				t.Errorf("flush counts: a=%d b=%d, want both 1", a.flush.Load(), b.flush.Load())
			}
		})
	}
}

func TestFailover_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close hits every branch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &controlledSink{}
			b := &controlledSink{}
			s, err := failover.New(a, b)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
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
		code int
	}{
		{"Exhausted carries 3901", failover.Exhausted, failover.CodeFailoverExhausted},
		{"Empty carries 3902", failover.Empty, failover.CodeFailoverEmpty},
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
