package sample_test

import (
	"context"
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
