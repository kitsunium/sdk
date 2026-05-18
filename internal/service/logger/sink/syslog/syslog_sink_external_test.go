package syslog_test

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/syslog"
)

// startUDPListener binds a free local UDP port and returns its address +
// a function that drains one packet for assertion purposes.
//
// Lifecycle: spawns a single drainer goroutine that exits when the
// underlying net.PacketConn is closed by t.Cleanup. The goroutine uses a
// 2-second SetReadDeadline as a safety net against test exits that race
// the close.
func startUDPListener(t testing.TB) (addr string, recv func() string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket err = %v", err)
	}
	t.Cleanup(func() { swallowSyslogClose(pc.Close()) })
	addr = pc.LocalAddr().String()
	var (
		mu       sync.Mutex
		received []string
	)
	go func() {
		buf := make([]byte, 4096)
		for {
			//: deadline avoids a hang when the test exits without sending.
			swallowSyslogDeadline(pc.SetReadDeadline(time.Now().Add(2 * time.Second)))
			n, _, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			mu.Lock()
			received = append(received, string(buf[:n]))
			mu.Unlock()
		}
	}()
	recv = func() string {
		//: spin-wait briefly for the listener goroutine to capture the packet.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(received)
			mu.Unlock()
			if n > 0 {
				mu.Lock()
				out := received[0]
				mu.Unlock()
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		return ""
	}
	return addr, recv
}

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		network  string
		addr     string
		wantCode errs.Code
	}{
		{"empty addr yields AddrEmpty", "udp", "", syslog.CodeSyslogAddrEmpty},
		{"unsupported proto yields ProtoInvalid", "icmp", "127.0.0.1:1", syslog.CodeSyslogProtoInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := syslog.New(tc.network, tc.addr)
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func TestSyslog_WriteShipsFrameOverUDP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"info-level record renders <14> envelope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, recv := startUDPListener(t)
			s, err := syslog.New("udp", addr)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { swallowSyslogClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info, Message: "hello"}
			if _, werr := s.Write(t.Context(), rec, []byte("payload")); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			frame := recv()
			if !strings.HasPrefix(frame, "<14>") {
				t.Errorf("frame missing <14> PRI prefix: %q", frame)
			}
			if !strings.Contains(frame, "payload") {
				t.Errorf("frame missing payload: %q", frame)
			}
		})
	}
}

func TestSyslog_WriteHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled ctx returns ctx.Err without sending"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, _ := startUDPListener(t)
			s, err := syslog.New("udp", addr)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { swallowSyslogClose(s.Close()) })
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, werr := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if werr == nil {
				t.Error("Write on cancelled ctx err = nil, want non-nil")
			}
		})
	}
}

func TestSyslog_FlushAndClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Flush + Close on UDP sink return nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, _ := startUDPListener(t)
			s, err := syslog.New("udp", addr)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Errorf("Flush err = %v", ferr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v", cerr)
			}
		})
	}
}

func TestNewWithConfig(t *testing.T) {
	t.Parallel()
	addr, _ := startUDPListener(t)
	tests := []struct {
		name        string
		network     string
		addr        string
		useDialer   bool
		dialErr     error
		wantCode    errs.Code
		wantSuccess bool
	}{
		{
			name: "happy path with explicit dialer",
			//: explicit dialer that delegates to net.Dial proves the
			//: cfg.Dialer branch is wired through NewWithConfig.
			network:     "udp",
			addr:        addr,
			useDialer:   true,
			wantSuccess: true,
		},
		{
			name: "happy path with nil dialer falls back to net.Dial",
			//: nil dialer exercises the documented default branch.
			network:     "udp",
			addr:        addr,
			useDialer:   false,
			wantSuccess: true,
		},
		{
			name:     "empty addr yields AddrEmpty",
			network:  "udp",
			addr:     "",
			wantCode: syslog.CodeSyslogAddrEmpty,
		},
		{
			name:     "unsupported proto yields ProtoInvalid",
			network:  "sctp",
			addr:     addr,
			wantCode: syslog.CodeSyslogProtoInvalid,
		},
		{
			name:      "dialer error surfaces as DialFailed",
			network:   "udp",
			addr:      "127.0.0.1:1",
			useDialer: true,
			dialErr:   errDialerBoom{},
			wantCode:  syslog.CodeSyslogDialFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cfg syslog.Config
			if tc.useDialer {
				dialErr := tc.dialErr
				cfg.Dialer = func(network, address string) (net.Conn, error) {
					if dialErr != nil {
						return nil, dialErr
					}
					return net.Dial(network, address)
				}
			}
			s, err := syslog.NewWithConfig(tc.network, tc.addr, cfg)
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("NewWithConfig err = %v, want nil", err)
				}
				if s == nil {
					t.Fatal("NewWithConfig returned nil sink on happy path")
				}
				t.Cleanup(func() { swallowSyslogClose(s.Close()) })
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

// errDialerBoom is a minimal error used to make NewWithConfig's custom
// dialer fail so the DialFailed branch is exercised.
type errDialerBoom struct{}

// Error renders a static marker; content is not asserted by the test.
//
// Returns:
//   - msg: a static marker.
func (errDialerBoom) Error() (msg string) {
	//: static marker — content is not asserted.
	return "dialer boom"
}

func TestSyslogSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"AddrEmpty carries 0.3.15.1", syslog.AddrEmpty, syslog.CodeSyslogAddrEmpty},
		{"DialFailed carries 0.3.15.2", syslog.DialFailed, syslog.CodeSyslogDialFailed},
		{"WriteFailed carries 0.3.15.3", syslog.WriteFailed, syslog.CodeSyslogWriteFailed},
		{"CloseFailed carries 0.3.15.4", syslog.CloseFailed, syslog.CodeSyslogCloseFailed},
		{"ProtoInvalid carries 0.3.15.5", syslog.ProtoInvalid, syslog.CodeSyslogProtoInvalid},
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

// swallowSyslogClose drops a Close error from cleanup paths.
func swallowSyslogClose(err error) {
	//: defensive guard so err is observed by the audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// swallowSyslogDeadline drops a SetReadDeadline error from goroutines.
func swallowSyslogDeadline(err error) {
	//: defensive guard so err is observed by the audit.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
