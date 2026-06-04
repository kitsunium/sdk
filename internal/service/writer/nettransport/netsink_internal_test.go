package nettransport

import (
	"context"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the per-record sink satisfies the Sink port (kept in the
// : test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ corelogger.Sink = (*netSink)(nil)

// errNetBoom is a sentinel transport failure injected by the white-box tests to
// exercise the error-wrapping branches without a real socket.
type errNetBoom struct{}

func (errNetBoom) Error() string { return "boom" }

func Test_newNetSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCloser bool
	}{
		{"non-nil closer is kept", false},
		{"nil closer degrades to no-op", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var closer func() error
			if !tc.nilCloser {
				closer = func() error { return nil }
			}
			s := newNetSink("tcp", func(context.Context, []byte) error { return nil }, closer)
			//: Close must never panic, even when the caller passed a nil closer.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v, want nil", cerr)
			}
		})
	}
}

func Test_netSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		cancel    bool
		sendErr   error
		wantErr   bool
		wantBytes int
	}{
		{"happy path ships bytes", false, nil, false, 5},
		{"cancelled ctx is refused", true, nil, true, 0},
		{"send failure is wrapped", false, errNetBoom{}, true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen []byte
			s := newNetSink("tcp", func(_ context.Context, p []byte) error {
				seen = append(seen[:0], p...)
				return tc.sendErr
			}, nil)
			ctx := t.Context()
			if tc.cancel {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			}
			n, err := s.Write(ctx, corelogger.RecordEvent{}, []byte("hello"))
			//: every error arm must carry the write sentinel + zero count.
			if tc.wantErr {
				if !errs.HasCode(err, CodeNetTransportWriteFailed) || n != 0 {
					t.Errorf("%s: n=%d err=%v want 0+write-failed", tc.name, n, err)
				}
				return
			}
			//: happy arm — full byte count, nil error, payload forwarded.
			if err != nil || n != tc.wantBytes || string(seen) != "hello" {
				t.Errorf("%s: n=%d err=%v seen=%q", tc.name, n, err, seen)
			}
		})
	}
}

func Test_netSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cancel  bool
		wantErr bool
	}{
		{"live ctx flushes clean", false, false},
		{"cancelled ctx surfaces error", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newNetSink("udp", func(context.Context, []byte) error { return nil }, nil)
			ctx := t.Context()
			if tc.cancel {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			}
			err := s.Flush(ctx)
			//: the cancelled arm must carry the write sentinel, not just any error.
			if tc.wantErr {
				if !errs.HasCode(err, CodeNetTransportWriteFailed) {
					t.Errorf("%s: Flush err = %v want write-failed", tc.name, err)
				}
				return
			}
			//: the live arm must flush clean.
			if err != nil {
				t.Errorf("%s: Flush err = %v, want nil", tc.name, err)
			}
		})
	}
}

func Test_netSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		closeErr error
		wantErr  bool
	}{
		{"clean close", nil, false},
		{"closer failure is wrapped", errNetBoom{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newNetSink("tcp", func(context.Context, []byte) error { return nil }, func() error { return tc.closeErr })
			err := s.Close()
			//: a closer failure must surface the write sentinel.
			if tc.wantErr {
				if !errs.HasCode(err, CodeNetTransportWriteFailed) {
					t.Errorf("%s: Close err = %v want write-failed", tc.name, err)
				}
				return
			}
			//: clean close returns nil.
			if err != nil {
				t.Errorf("%s: Close err = %v, want nil", tc.name, err)
			}
		})
	}
}

// Test_netSink_Write_Close_concurrent hammers Write from many goroutines while a
// late Close races them, proving the mutex serialises send against close (the -race
// flag is the real assertion: any unsynchronised access fails the build). The send
// sleeps briefly so a Write is reliably in flight when Close lands. Goroutine
// lifecycle: a WaitGroup joins all N writers; Close runs on its own goroutine that
// the WaitGroup also covers, so nothing leaks past the test.
func Test_netSink_Write_Close_concurrent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		writers int
	}{
		{"eight writers race a late close", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a send that sleeps keeps a Write in flight when Close lands.
			s := newNetSink("tcp", func(context.Context, []byte) error {
				time.Sleep(time.Millisecond)
				return nil
			}, func() error { return nil })
			var wg sync.WaitGroup
			//: N concurrent writers plus the single closer goroutine.
			wg.Add(tc.writers + 1)
			for range tc.writers {
				go func() {
					defer wg.Done()
					//: each Write must complete or fail with the write sentinel only.
					if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("x")); werr != nil && !errs.HasCode(werr, CodeNetTransportWriteFailed) {
						t.Errorf("Write err = %v want nil or write-failed", werr)
					}
				}()
			}
			closeErr := make(chan error, 1)
			go func() {
				defer wg.Done()
				//: a short delay lets writers get in flight before the close.
				time.Sleep(time.Millisecond / 2)
				closeErr <- s.Close()
			}()
			wg.Wait()
			//: Close must report nil or the write sentinel — never anything else.
			if cerr := <-closeErr; cerr != nil && !errs.HasCode(cerr, CodeNetTransportWriteFailed) {
				t.Errorf("%s: Close err = %v want nil or write-failed", tc.name, cerr)
			}
		})
	}
}
