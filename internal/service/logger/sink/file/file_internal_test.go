package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func mustOpenSink(tb testing.TB) (sink *fileSink, path string) {
	tb.Helper()
	path = filepath.Join(tb.TempDir(), "sink.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, defaultFilePerm)
	if err != nil {
		tb.Fatalf("OpenFile err = %v", err)
	}
	//: defensive defer satisfies the lifecycle linter even though the sink
	//: takes ownership on the success path.
	defer func() {
		//: skip the close on the success path — sink owns the descriptor.
		if sink != nil {
			//: nothing to do; tb.Cleanup releases it after the test.
			return
		}
		//: factory failed downstream — release the descriptor explicitly.
		swallowSinkClose(f.Close())
	}()
	sink = &fileSink{f: f}
	//: register a cleanup so the descriptor is released even on test failure.
	tb.Cleanup(func() {
		//: best-effort close; ignore the error in cleanup to keep the test signal clean.
		swallowSinkClose(sink.Close())
	})
	return sink, path
}

// swallowSinkClose is the test-only counterpart of swallowHandlerError —
// it documents the intent of dropping a Close error from cleanup paths.
//
// Params:
//   - err: close error to discard; non-nil values are intentionally dropped.
func swallowSinkClose(err error) {
	//: the test is already past its assertion phase; close errors are noise.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

func Test_fileSink_Write(t *testing.T) {
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
			s, _ := mustOpenSink(t)
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

func Test_fileSink_Flush(t *testing.T) {
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
			s, _ := mustOpenSink(t)
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

func Test_fileSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close releases the descriptor without error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
			}
		})
	}
}
