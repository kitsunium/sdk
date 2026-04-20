package route

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// dummyCtx silences the unused import warning when context is only used
// inside the noopRouteSink interface signatures.
var _ context.Context

// noopRouteSink is the trivial Sink used by the internal tests below.
type noopRouteSink struct{}

func (noopRouteSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (noopRouteSink) Flush(_ context.Context) error { return nil }
func (noopRouteSink) Close() error                  { return nil }

func Test_routerSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		hasFb   bool
		wantErr bool
	}{
		{"empty router with nil fallback returns NoMatch", false, true},
		{"empty router with fallback delegates to it", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if tc.hasFb {
				s.fallback = noopRouteSink{}
			}
			_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_routerSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		hasFb bool
	}{
		{"empty router Flush returns nil", false},
		{"router with fallback Flush returns nil", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if tc.hasFb {
				s.fallback = noopRouteSink{}
			}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_routerSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		hasFb bool
	}{
		{"empty router Close returns nil", false},
		{"router with fallback Close returns nil", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if tc.hasFb {
				s.fallback = noopRouteSink{}
			}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

func Test_routerSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"routerSink zero value has empty entries and nil fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &routerSink{}
			if len(s.entries) != 0 {
				t.Errorf("entries len = %d, want 0", len(s.entries))
			}
			if s.fallback != nil {
				t.Errorf("fallback = %v, want nil", s.fallback)
			}
		})
	}
}
