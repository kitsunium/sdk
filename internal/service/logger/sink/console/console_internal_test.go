package console

import (
	"bytes"
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// faultyWriter always fails on Write, driving the WriteFailed branch.
type faultyWriter struct{}

func (faultyWriter) Write(p []byte) (int, error) { return 0, errors.New("boom") }

func Test_consoleSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		faulty    bool
		wantBytes int
		wantErr   bool
	}{
		{"happy path writes bytes", false, false, false, 5, false},
		{"nil ctx is treated as live and writes", true, false, false, 5, false},
		{"cancelled context returns 0 bytes", false, true, false, 0, true},
		{"faulty writer surfaces WriteFailed", false, false, true, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *consoleSink
			if tc.faulty {
				s = &consoleSink{w: faultyWriter{}}
			} else {
				s = &consoleSink{w: &bytes.Buffer{}}
			}
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxCancel {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			n, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("hello"))
			if n != tc.wantBytes {
				t.Errorf("Write n = %d, want %d", n, tc.wantBytes)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_consoleSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		wantErr   bool
	}{
		{"flush with live ctx is nil", false, false, false},
		{"flush with nil ctx is nil", true, false, false},
		{"flush with cancelled ctx surfaces error", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &consoleSink{w: &bytes.Buffer{}}
			var ctx context.Context
			if tc.nilCtx {
				ctx = nil
			} else {
				ctx = t.Context()
				if tc.ctxCancel {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
			}
			err := s.Flush(ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("Flush err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func Test_consoleSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close is a no-op returning nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &consoleSink{w: &bytes.Buffer{}}
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v, want nil", err)
			}
		})
	}
}

// Test_exitIOErr pins the EX_IOERR sentinel used by WriteFailed so future
// refactors do not silently switch the documented exit code.
func Test_exitIOErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"sysexits EX_IOERR is 74", 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if exitIOErr != tc.want {
				t.Errorf("exitIOErr = %d, want %d", exitIOErr, tc.want)
			}
		})
	}
}
