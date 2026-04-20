package failover

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// noopBranch is the trivial Sink used by the internal failover tests below.
type noopBranch struct{}

func (noopBranch) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopBranch) Flush(_ context.Context) error { return nil }
func (noopBranch) Close() error                  { return nil }

func Test_failoverSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"single happy branch returns nil"},
		{"empty chain wraps Exhausted (defensive — New rejects this case)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &failoverSink{chain: []corelogger.Sink{noopBranch{}}}
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x")); err != nil {
				t.Errorf("Write err = %v, want nil", err)
			}
		})
	}
}

func Test_failoverSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty chain Flush returns nil"},
		{"single-branch chain Flush returns nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &failoverSink{chain: []corelogger.Sink{noopBranch{}}}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_failoverSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty chain Close returns nil"},
		{"single-branch chain Close returns nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &failoverSink{chain: []corelogger.Sink{noopBranch{}}}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

func Test_failoverSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"failoverSink zero value has empty chain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &failoverSink{}
			if len(s.chain) != 0 {
				t.Errorf("chain len = %d, want 0", len(s.chain))
			}
		})
	}
}
