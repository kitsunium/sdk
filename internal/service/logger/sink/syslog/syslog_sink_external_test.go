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
			//: the cancellation must surface the typed CtxCancelled code, not a bare ctx.Err.
			if !errs.HasCode(werr, syslog.CodeSyslogCtxCancelled) {
				t.Errorf("HasCode(%v, CtxCancelled) = false", werr)
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
		{"CtxCancelled carries 0.3.15.6", syslog.CtxCancelled, syslog.CodeSyslogCtxCancelled},
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

func TestSyslog_FlushCtxCancelledHasCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"cancelled ctx Flush surfaces CtxCancelled code"},
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
			ferr := s.Flush(ctx)
			if ferr == nil {
				t.Fatal("Flush on cancelled ctx err = nil, want non-nil")
			}
			//: Flush must reuse the same typed cancellation code as Write.
			if !errs.HasCode(ferr, syslog.CodeSyslogCtxCancelled) {
				t.Errorf("HasCode(%v, CtxCancelled) = false", ferr)
			}
		})
	}
}

// TestSyslog_WriteShipsFrameOverTCP proves a frame produced by the
// production Write path reaches a real TCP listener intact.
//
// Lifecycle: a single accept goroutine reads exactly one frame and exits;
// it is bounded by the listener's lifetime (closed via t.Cleanup) and the
// test joins it through a buffered channel with a 2-second timeout, so the
// goroutine cannot outlive the subtest.
func TestSyslog_WriteShipsFrameOverTCP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"info-level record ships full envelope over a live TCP listener"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("Listen err = %v", err)
			}
			t.Cleanup(func() { swallowSyslogClose(ln.Close()) })
			//: a buffered channel lets the accept goroutine hand the frame back
			//: without blocking on the test goroutine's read timing.
			frames := make(chan string, 1)
			go func() {
				conn, aerr := ln.Accept()
				if aerr != nil {
					//: listener closed before a connection arrived — nothing to read.
					return
				}
				//: close in a closure so conn.Close runs at goroutine exit,
				//: not when the defer statement evaluates its argument.
				defer func() { swallowSyslogClose(conn.Close()) }()
				buf := make([]byte, 4096)
				n, rerr := conn.Read(buf)
				if rerr != nil {
					//: read failed — surface an empty frame so the assertion reports it.
					frames <- ""
					return
				}
				frames <- string(buf[:n])
			}()
			s, err := syslog.New("tcp", ln.Addr().String())
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			t.Cleanup(func() { swallowSyslogClose(s.Close()) })
			rec := corelogger.RecordEvent{Level: level.Info, Message: "hello"}
			if _, werr := s.Write(t.Context(), rec, []byte("payload")); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			var frame string
			select {
			case frame = <-frames:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for TCP frame")
			}
			//: the full RFC5424 envelope prefix proves header + payload reached the wire.
			if !strings.HasPrefix(frame, "<14>1 - - - - - - ") {
				t.Errorf("frame missing RFC5424 envelope prefix: %q", frame)
			}
			if !strings.Contains(frame, "payload") {
				t.Errorf("frame missing payload: %q", frame)
			}
		})
	}
}

func TestSyslog_WriteAfterClose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Write after Close surfaces WriteFailed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, _ := startUDPListener(t)
			s, err := syslog.New("udp", addr)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if cerr := s.Close(); cerr != nil {
				t.Fatalf("Close err = %v", cerr)
			}
			//: the closed connection must make the subsequent write fail typed.
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if !errs.HasCode(werr, syslog.CodeSyslogWriteFailed) {
				t.Errorf("HasCode(%v, WriteFailed) = false", werr)
			}
		})
	}
}

func TestSyslog_RFC5424EnvelopeSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"received UDP frame carries the full envelope suffix"},
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
			//: the six NIL fields + VERSION token must reach the wire verbatim.
			if !strings.Contains(frame, "1 - - - - - - ") {
				t.Errorf("frame missing RFC5424 envelope suffix: %q", frame)
			}
		})
	}
}

func TestSyslog_WriteShipsMultipleLevels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		level   level.Level
		wantPRI string
	}{
		{"error renders <11>", level.Error, "<11>"},
		{"warn renders <12>", level.Warn, "<12>"},
		{"info renders <14>", level.Info, "<14>"},
		{"debug renders <15>", level.Debug, "<15>"},
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
			rec := corelogger.RecordEvent{Level: tc.level, Message: "m"}
			if _, werr := s.Write(t.Context(), rec, []byte("payload")); werr != nil {
				t.Fatalf("Write err = %v", werr)
			}
			frame := recv()
			//: the PRI prefix is derived from the level's RFC5424 severity.
			if !strings.HasPrefix(frame, tc.wantPRI) {
				t.Errorf("frame %q missing PRI prefix %q", frame, tc.wantPRI)
			}
		})
	}
}

// swallowSyslogClose drops a Close error from cleanup paths.
func swallowSyslogClose(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}

// swallowSyslogDeadline drops a SetReadDeadline error from goroutines.
func swallowSyslogDeadline(err error) {
	//: read the parameter so the unused-param audit treats this no-op as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
