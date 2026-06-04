package console_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

// failingWriter returns a canned error from Write so the WriteFailed branch
// gets exercised under test.
type failingWriter struct{ err error }

func (f failingWriter) Write(_ []byte) (int, error) { return 0, f.err }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		nilArg  bool
		wantErr error
	}{
		{"non-nil writer succeeds", false, nil},
		{"nil writer is rejected", true, console.WriterNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf *bytes.Buffer
			if !tc.nilArg {
				buf = &bytes.Buffer{}
			}
			//: nil arg flows through New(nil); buf is the typed-non-nil case.
			var got corelogger.Sink
			var err error
			if tc.nilArg {
				got, err = console.New(nil)
			} else {
				got, err = console.New(buf)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.nilArg && got != nil {
				t.Errorf("nil writer returned non-nil sink: %v", got)
			}
		})
	}
}

func TestNewStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NewStderr returns a non-nil Sink"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := console.NewStderr()
			if s == nil {
				t.Error("NewStderr returned nil")
			}
		})
	}
}

func TestNewStdout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"NewStdout returns a non-nil Sink"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := console.NewStdout()
			if s == nil {
				t.Error("NewStdout returned nil")
			}
		})
	}
}

func TestConsoleSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		writer    string
		want      string
		wantCode  errs.Code
	}{
		{"happy path writes verbatim", false, "ok", "hello", 0},
		{"cancelled context wraps CtxCancelled", true, "ok", "", console.CodeCtxCancelled},
		{"writer error wraps WriteFailed", false, "fail", "", console.CodeWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sink corelogger.Sink
			var captured *bytes.Buffer
			//: choose between a bytes.Buffer (capture) and a failingWriter.
			if tc.writer == "fail" {
				s, err := console.New(failingWriter{err: errors.New("boom")})
				if err != nil {
					t.Fatalf("New err = %v", err)
				}
				sink = s
			} else {
				captured = &bytes.Buffer{}
				s, err := console.New(captured)
				if err != nil {
					t.Fatalf("New err = %v", err)
				}
				sink = s
			}
			//: build a context that may be cancelled depending on the case.
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			_, err := sink.Write(ctx, rec, []byte("hello"))
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
			} else if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
			if captured != nil && tc.want != "" && captured.String() != tc.want {
				t.Errorf("captured = %q, want %q", captured.String(), tc.want)
			}
		})
	}
}

func TestConsoleSink_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
		wantErr   bool
	}{
		{"flush with live ctx returns nil", false, false},
		{"flush with cancelled ctx returns error", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(&bytes.Buffer{})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			ferr := s.Flush(ctx)
			if (ferr != nil) != tc.wantErr {
				t.Errorf("Flush err = %v, wantErr = %v", ferr, tc.wantErr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v, want nil", cerr)
			}
		})
	}
}

func TestConsoleSink_WriteIsConcurrentSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goroutines int
		perRoutine int
	}{
		{"10×100 concurrent writes interleave atomically", 10, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			s, err := console.New(&buf)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			rec := corelogger.RecordEvent{Level: level.Info}
			payload := []byte("line\n")
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perRoutine {
						if _, werr := s.Write(t.Context(), rec, payload); werr != nil {
							t.Errorf("Write err = %v", werr)
						}
					}
				})
			}
			wg.Wait()
			//: sanity-check the byte count rather than the line count to keep
			//: the assertion robust against the writer's framing.
			want := tc.goroutines * tc.perRoutine * len(payload)
			if buf.Len() != want {
				t.Errorf("buf len = %d, want %d", buf.Len(), want)
			}
		})
	}
}

// TestErrorsCarryConsoleCodes ensures the sentinels carry the documented
// errs codes for downstream HasCode-style introspection.
func TestErrorsCarryConsoleCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"WriterNil carries 0.3.13.1", console.WriterNil, console.CodeWriterNil},
		{"CtxCancelled carries 0.3.13.10", console.CtxCancelled, console.CodeCtxCancelled},
		{"WriteFailed carries 0.3.13.20", console.WriteFailed, console.CodeWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// TestConsoleSink_Write_RealPipe drives production Write through a real
// os.Pipe (a kernel-backed file descriptor pair, not a bytes.Buffer) and reads
// the bytes back from the other end, proving the sink performs genuine I/O.
//
// Lifecycle: spawns a single reader goroutine that blocks on the read FD and
// exits when the producer closes the write end (EOF) — it always sends exactly
// one value on the buffered channel, so it never leaks or orphans.
func TestConsoleSink_Write_RealPipe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"single record crosses the pipe verbatim", "real-pipe-line\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: os.Pipe yields real FDs; the write end is the production io.Writer.
			rd, wr, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe err = %v", err)
			}
			t.Cleanup(func() {
				if cerr := rd.Close(); cerr != nil {
					t.Errorf("read end Close err = %v", cerr)
				}
			})
			s, nerr := console.New(wr)
			if nerr != nil {
				t.Fatalf("New err = %v", nerr)
			}
			got := make(chan string, 1)
			//: lifecycle: this goroutine blocks on the read FD until the producer
			//: closes the write end (EOF below); it always terminates and reports
			//: its single line on the buffered channel — no leak, no orphan.
			go func() {
				line, rerr := bufio.NewReader(rd).ReadString('\n')
				if rerr != nil {
					got <- ""
					return
				}
				got <- line
			}()
			rec := corelogger.RecordEvent{Level: level.Info}
			if _, werr := s.Write(t.Context(), rec, []byte(tc.want)); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			//: closing the write end flushes EOF so the reader unblocks deterministically.
			if cerr := wr.Close(); cerr != nil {
				t.Fatalf("write end Close err = %v", cerr)
			}
			//: the bytes observed on the read FD must match what production emitted.
			if line := <-got; line != tc.want {
				t.Errorf("pipe received %q, want %q", line, tc.want)
			}
		})
	}
}
