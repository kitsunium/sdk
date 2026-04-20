package syslog

import (
	"context"
	"net"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
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
		//: best-effort close — the test cleanup is past assertion time.
		if cerr := pc.Close(); cerr != nil {
			//: nothing to do; cleanup errors are noise.
			_ = cerr
		}
	})
	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Dial err = %v", err)
	}
	t.Cleanup(func() {
		//: best-effort close — Close test already closed it; ignore the second error.
		if cerr := conn.Close(); cerr != nil {
			//: nothing to do; cleanup errors are noise.
			_ = cerr
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
	}{
		{"happy path returns bytes accepted", false, false, false, false, true},
		{"nil ctx is treated as live and writes", true, false, false, false, true},
		{"cancelled ctx surfaces ctx.Err", false, true, false, true, false},
		{"closed conn surfaces WriteFailed", false, false, true, true, false},
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
	}{
		{"Flush with live ctx returns nil", false, false, false},
		{"Flush with nil ctx returns nil", true, false, false},
		{"Flush with cancelled ctx surfaces ctx.Err", false, true, true},
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
		})
	}
}

func Test_syslogSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		closed  bool
		wantErr bool
	}{
		{"Close releases the net.Conn without error", false, false},
		{"Close on already-closed conn surfaces CloseFailed", true, true},
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
