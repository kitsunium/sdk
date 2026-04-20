package console

import (
	"bytes"
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func Test_consoleSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		wantBytes int
	}{
		{"happy path writes bytes", false, 5},
		{"cancelled context returns 0 bytes", true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			s := &consoleSink{w: &buf}
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			n, _ := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("hello"))
			if n != tc.wantBytes {
				t.Errorf("Write n = %d, want %d", n, tc.wantBytes)
			}
		})
	}
}

func Test_consoleSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		wantErr   bool
	}{
		{"flush with live ctx is nil", false, false},
		{"flush with cancelled ctx surfaces error", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &consoleSink{w: &bytes.Buffer{}}
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
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
