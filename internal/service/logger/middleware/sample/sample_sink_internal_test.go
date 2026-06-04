package sample

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// noopDownstream is the trivial Sink used by the internal sample tests.
type noopDownstream struct{}

func (noopDownstream) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopDownstream) Flush(_ context.Context) error { return nil }
func (noopDownstream) Close() error                  { return nil }

func Test_sampleSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		rate   uint64
		writes int
	}{
		{"rate 1 keeps every write", 1, 3},
		{"rate 4 over 8 writes keeps 2", 4, 8},
		{"rate 4 over 8 writes keeps 2 — verify returned n", 4, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &sampleSink{downstream: noopDownstream{}, rate: tc.rate}
			rec := corelogger.RecordEvent{Level: level.Info}
			//: count kept writes — noopDownstream returns len(p), drops return 0.
			var kept int
			for range tc.writes {
				n, err := s.Write(t.Context(), rec, []byte("x"))
				if err != nil {
					t.Errorf("Write err = %v", err)
				}
				if n > 0 {
					kept++
				}
			}
			//: deterministic 1-of-N selection must keep exactly writes/rate records.
			if want := tc.writes / int(tc.rate); kept != want {
				t.Errorf("kept writes (n>0) = %d, want %d", kept, want)
			}
		})
	}
}

func Test_sampleSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush delegates to downstream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &sampleSink{downstream: noopDownstream{}, rate: 1}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_sampleSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close delegates to downstream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &sampleSink{downstream: noopDownstream{}, rate: 1}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}
