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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &sampleSink{downstream: noopDownstream{}, rate: tc.rate}
			rec := corelogger.RecordEvent{Level: level.Info}
			for range tc.writes {
				if _, err := s.Write(t.Context(), rec, []byte("x")); err != nil {
					t.Errorf("Write err = %v", err)
				}
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
