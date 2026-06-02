package nettransport

import (
	"context"
	"testing"

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
			//: the flush verdict must match the cancellation expectation.
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: Flush err = %v, wantErr = %v", tc.name, err, tc.wantErr)
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
