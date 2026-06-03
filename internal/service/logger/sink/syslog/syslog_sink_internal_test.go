package syslog

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// localUDPSink dials a free local UDP socket and returns a syslogSink ready
// for white-box assertions. The corresponding listener and dial conn are
// registered for cleanup by t.Cleanup.
func localUDPSink(t testing.TB) *syslogSink {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket err = %v", err)
	}
	t.Cleanup(func() {
		//: best-effort close — the test cleanup is past assertion time, but
		//: an unexpected error here would still mask a leak; surface via t.Log.
		if cerr := pc.Close(); cerr != nil && !isClosedNetErr(cerr) {
			t.Logf("ListenPacket cleanup Close err = %v", cerr)
		}
	})
	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Dial err = %v", err)
	}
	t.Cleanup(func() {
		//: best-effort close — Close test may have closed the conn already;
		//: surface unexpected errors via t.Log without failing the test.
		if cerr := conn.Close(); cerr != nil && !isClosedNetErr(cerr) {
			t.Logf("Dial cleanup Close err = %v", cerr)
		}
	})
	return &syslogSink{conn: conn}
}

// closedSyslogSink returns a syslog sink whose underlying conn was already
// closed, so subsequent Write / Close calls hit the net.Conn error paths
// and exercise the WriteFailed / CloseFailed wrap branches.
func closedSyslogSink(t testing.TB) *syslogSink {
	t.Helper()
	s := localUDPSink(t)
	//: pre-close the conn so subsequent operations hit the error path.
	if cerr := s.conn.Close(); cerr != nil {
		t.Fatalf("pre-close conn err = %v", cerr)
	}
	return s
}

func Test_syslogSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		closed    bool
		wantErr   bool
		wantBytes bool
		wantCode  errs.Code
	}{
		{"happy path returns bytes accepted", false, false, false, false, true, 0},
		{"nil ctx is treated as live and writes", true, false, false, false, true, 0},
		{"cancelled ctx surfaces ctx.Err", false, true, false, true, false, CodeSyslogCtxCancelled},
		{"closed conn surfaces WriteFailed", false, false, true, true, false, CodeSyslogWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *syslogSink
			if tc.closed {
				s = closedSyslogSink(t)
			} else {
				s = localUDPSink(t)
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
			n, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("payload"))
			if (err != nil) != tc.wantErr {
				t.Errorf("Write err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantBytes && n == 0 {
				t.Errorf("Write n = 0, want >0")
			}
			//: failure cases must carry the documented typed code, not a bare error.
			if tc.wantCode != 0 && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func Test_syslogSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		nilCtx    bool
		ctxCancel bool
		wantErr   bool
		wantCode  errs.Code
	}{
		{"Flush with live ctx returns nil", false, false, false, 0},
		{"Flush with nil ctx returns nil", true, false, false, 0},
		{"Flush with cancelled ctx surfaces ctx.Err", false, true, true, CodeSyslogCtxCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := localUDPSink(t)
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
			//: cancellation must surface the typed CtxCancelled code.
			if tc.wantCode != 0 && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func Test_syslogSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		closed   bool
		wantErr  bool
		wantCode errs.Code
	}{
		{"Close releases the net.Conn without error", false, false, 0},
		{"Close on already-closed conn surfaces CloseFailed", true, true, CodeSyslogCloseFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var s *syslogSink
			if tc.closed {
				s = closedSyslogSink(t)
			} else {
				s = localUDPSink(t)
			}
			err := s.Close()
			if (err != nil) != tc.wantErr {
				t.Errorf("Close err = %v, wantErr = %v", err, tc.wantErr)
			}
			//: a failed close must carry the documented CloseFailed code.
			if tc.wantCode != 0 && !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

// Test_syslogSink_ConcurrentWrite drives many producers through a single
// sink to exercise the Write mutex under the race detector.
//
// Lifecycle: each writer goroutine is started via sync.WaitGroup.Go and is
// joined by wg.Wait before the error channel is drained, so no goroutine
// outlives the subtest and the buffered channel cannot leak.
func Test_syslogSink_ConcurrentWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		goroutines   int
		perGoroutine int
	}{
		{"10 goroutines x 10 writes race-free", 10, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := localUDPSink(t)
			//: collect each goroutine's first error so a data race or a write
			//: failure under the mutex surfaces as a test failure.
			errc := make(chan error, tc.goroutines)
			var wg sync.WaitGroup
			for range tc.goroutines {
				wg.Go(func() {
					for range tc.perGoroutine {
						//: every write shares the same sink, exercising the mutex path.
						_, err := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("payload"))
						if err != nil {
							//: report the first error and stop this goroutine early.
							errc <- err
							return
						}
					}
				})
			}
			wg.Wait()
			close(errc)
			//: any buffered error means a concurrent write path returned non-nil.
			for err := range errc {
				t.Errorf("concurrent Write err = %v, want nil", err)
			}
		})
	}
}

func Test_makeFrame(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pri     int
		payload string
		needles []string
	}{
		{"info frame contains PRI + payload", 14, "hello", []string{"<14>", "hello"}},
		{"error frame contains <11> prefix", 11, "msg", []string{"<11>", "msg"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(makeFrame(tc.pri, []byte(tc.payload)))
			for _, needle := range tc.needles {
				if !strings.Contains(got, needle) {
					t.Errorf("frame missing %q: %q", needle, got)
				}
			}
		})
	}
}

func Test_swallowDialClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil error is a no-op", nil},
		{"non-nil error is silently dropped", errSyslogBoom{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				swallowDialClose(tc.err)
			}()
			if panicked {
				t.Errorf("swallowDialClose panicked, want silent drop")
			}
		})
	}
}

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

type errSyslogBoom struct{}

func (errSyslogBoom) Error() string { return "boom" }

// isClosedNetErr reports whether err is a "use of closed network
// connection" error returned by net.Conn.Close on an already-closed conn.
// Used by cleanup paths to avoid t.Log spam when the test under
// assertion intentionally closed the connection.
func isClosedNetErr(err error) (closed bool) {
	//: nil errors do not represent any close failure.
	if err == nil {
		return false
	}
	//: net.ErrClosed surfaces on closed PacketConn/Conn since Go 1.16.
	return errors.Is(err, net.ErrClosed)
}
