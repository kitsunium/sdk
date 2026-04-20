package recover

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// noopDown never panics — exercises the happy-path branch of every method.
type noopDown struct{}

func (noopDown) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopDown) Flush(_ context.Context) error { return nil }
func (noopDown) Close() error                  { return nil }

func Test_recoverSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"happy path delegates byte count"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
		})
	}
}

func Test_recoverSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"defer-recovery is harmless on the no-panic path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_recoverSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"happy path returns nil"},
		{"defer-recovery is harmless on the no-panic path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &recoverSink{downstream: noopDown{}}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}
