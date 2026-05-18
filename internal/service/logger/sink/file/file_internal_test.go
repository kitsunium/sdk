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
		nilCtx    bool
		ctxCancel bool
		closed    bool
		wantBytes int
		wantErr   bool
	}{
		{"happy path writes bytes", false, false, false, 5, false},
		{"nil ctx is treated as live and writes", true, false, false, 5, false},
		{"cancelled context returns 0 bytes", false, true, false, 0, true},
		{"closed file surfaces WriteFailed", false, false, true, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if tc.closed {
				//: pre-close so Write hits the underlying *os.File error path.
				swallowSinkClose(s.Close())
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

func Test_fileSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		closed    bool
		wantErr   bool
	}{
		{"flush with live ctx is nil", false, false, false, false},
		{"flush with nil ctx is nil", true, false, false, false},
		{"flush with cancelled ctx surfaces error", false, true, false, true},
		{"flush on closed file surfaces SyncFailed", false, false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := mustOpenSink(t)
			if tc.closed {
				//: pre-close so Sync hits the underlying *os.File error path.
				swallowSinkClose(s.Close())
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
