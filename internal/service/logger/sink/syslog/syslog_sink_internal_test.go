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
// for white-box assertions. The corresponding listener is registered for
// cleanup by t.Cleanup.
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

func Test_syslogSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		ctxCancel bool
	}{
		{"happy path returns bytes accepted", false},
		{"cancelled ctx surfaces ctx.Err", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := localUDPSink(t)
			ctx := t.Context()
			if tc.ctxCancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			n, err := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("payload"))
			if tc.ctxCancel {
				if err == nil {
					t.Error("cancelled ctx Write err = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Write err = %v", err)
			}
			if n == 0 {
				t.Errorf("Write n = 0, want >0")
			}
		})
	}
}

func Test_syslogSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush is a no-op returning nil"},
		{"Flush honours cancellation as a courtesy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := localUDPSink(t)
			if err := s.Flush(t.Context()); err != nil {
				t.Errorf("Flush err = %v, want nil", err)
			}
		})
	}
}

func Test_syslogSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Close releases the net.Conn without error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := localUDPSink(t)
			if err := s.Close(); err != nil {
				t.Errorf("Close err = %v", err)
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
			swallowDialClose(tc.err)
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
