package multi

import (
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func Test_fanoutSink_zeroValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"fanoutSink with nil branches has length 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{}
			if len(s.branches) != 0 {
				t.Errorf("fresh fanoutSink branches len = %d, want 0", len(s.branches))
			}
		})
	}
}

func Test_fanoutSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty fanout returns nil error and zero bytes accepted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{}
			n, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if err != nil {
				t.Errorf("empty fanout Write err = %v, want nil", err)
			}
			if n != 0 {
				t.Errorf("empty fanout Write n = %d, want 0", n)
			}
		})
	}
}

func Test_fanoutSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty fanout Flush returns nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{}
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("empty fanout Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_fanoutSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"empty fanout Close returns nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &fanoutSink{}
			if err := s.Close(); err != nil {
				t.Errorf("empty fanout Close err = %v, want nil", err)
			}
		})
	}
}
