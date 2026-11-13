package server

import (
	"context"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// countingLogger counts the records it is given and keeps the last message.
type countingLogger struct {
	mu   sync.Mutex
	n    int
	last string
}

func (c *countingLogger) Log(_ context.Context, _ level.Level, msg string, _ ...corelogger.AttrValue) {
	c.mu.Lock()
	c.n++
	c.last = msg
	c.mu.Unlock()
}
func (c *countingLogger) With(...corelogger.AttrValue) corelogger.Logger { return c }
func (c *countingLogger) WithGroup(string) corelogger.Logger             { return c }
func (c *countingLogger) Enabled(context.Context, level.Level) bool      { return true }

// TestErrorLog_capsARecordPerSecond pins the flood case: 10 000 lines at one
// instant write the cap and count the rest; the next second writes the cap again.
func TestErrorLog_capsARecordPerSecond(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC))
	lg := &countingLogger{}
	s := newServer(func(string) (string, bool) { return "", false }, WithLogger(lg))
	s.clk = clk
	w := s.errorLog("api").Writer()
	const flood = 10000
	for range flood {
		if _, err := w.Write([]byte("http: TLS handshake error from 192.0.2.1:1: EOF\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if lg.n != errorLogCap {
		t.Fatalf("%d records written, want the cap %d", lg.n, errorLogCap)
	}
	if got := s.State().SuppressedLogs; got != uint64(flood-errorLogCap) {
		t.Fatalf("SuppressedLogs = %d, want the remainder %d", got, flood-errorLogCap)
	}
	//: the window elapsed: the budget is whole again.
	clk.Advance(time.Second)
	for range 3 {
		if _, err := w.Write([]byte("http: Accept error: boom; retrying\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if lg.n != errorLogCap+3 {
		t.Fatalf("%d records after the window, want %d", lg.n, errorLogCap+3)
	}
	if !strings.HasPrefix(lg.last, "http: Accept error") || strings.HasSuffix(lg.last, "\n") {
		t.Fatalf("last message = %q, want the line without its newline", lg.last)
	}
}

// TestErrorLog_clipsADetail pins that the lines after the first are bounded.
func TestErrorLog_clipsADetail(t *testing.T) {
	t.Parallel()
	var detail string
	lg := detailLogger{got: &detail}
	s := newServer(func(string) (string, bool) { return "", false }, WithLogger(lg))
	w := s.errorLog("api").Writer()
	line := "http: panic serving x: y\n" + strings.Repeat("s", 10*errorLogDetailMax) + "\n"
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(detail) != errorLogDetailMax {
		t.Fatalf("detail is %d bytes, want it clipped at %d", len(detail), errorLogDetailMax)
	}
}

// detailLogger keeps the detail attribute of the record it is given.
type detailLogger struct{ got *string }

func (d detailLogger) Log(_ context.Context, _ level.Level, _ string, attrs ...corelogger.AttrValue) {
	for _, a := range attrs {
		if a.Key == "detail" {
			*d.got = a.Value.String()
		}
	}
}
func (d detailLogger) With(...corelogger.AttrValue) corelogger.Logger { return d }
func (d detailLogger) WithGroup(string) corelogger.Logger             { return d }
func (d detailLogger) Enabled(context.Context, level.Level) bool      { return true }

// floodDial connects to addr, again after a refusal or a reset: under the
// flood NetBSD answered one connect in 10 000 with a reset before the server
// accepted it (connection reset by peer), a connection that never reached
// a handshake and that the count must not owe.
func floodDial(t *testing.T, addr string) (stdnet.Conn, error) {
	var err error
	for attempt := range 5 {
		var c stdnet.Conn
		if c, err = stdnet.DialTimeout("tcp", addr, 5*time.Second); err == nil {
			return c, nil
		}
		t.Logf("dial %d: %v", attempt+1, err)
		time.Sleep(10 * time.Millisecond)
	}
	return nil, err
}

// TestErrorLog_handshakeFlood holds #846's second criterion on real
// handshakes: 10 000 plaintext connections to a TLS HTTP group within one
// second of the server's clock — frozen, so the window holds them all
// whatever the machine's pace — write the cap's records and count the
// remainder in SuppressedLogs, each failure counted once.
func TestErrorLog_handshakeFlood(t *testing.T) {
	const flood, workers = 10000, 16
	clk := clock.NewManualClock(time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC))
	lg := &countingLogger{}
	s := newServer(func(string) (string, bool) { return "", false }, WithLogger(lg))
	s.clk = clk
	s.Group("api", Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t))).
		HandleHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := s.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	addr := s.State().Listeners[0].Address
	var wg sync.WaitGroup
	next := make(chan struct{}, flood)
	for range flood {
		next <- struct{}{}
	}
	close(next)
	for range workers {
		wg.Go(func() {
			for range next {
				c, err := floodDial(t, addr)
				if err != nil {
					t.Errorf("dial: %v", err)
					return
				}
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}
		})
	}
	wg.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for {
		lg.mu.Lock()
		n := lg.n
		lg.mu.Unlock()
		if uint64(n)+s.State().SuppressedLogs >= flood || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	lg.mu.Lock()
	n, last := lg.n, lg.last
	lg.mu.Unlock()
	if n != errorLogCap {
		t.Fatalf("%d records written, want the cap %d", n, errorLogCap)
	}
	if got := s.State().SuppressedLogs; got != uint64(flood-errorLogCap) {
		t.Fatalf("SuppressedLogs = %d, want the remainder %d", got, flood-errorLogCap)
	}
	if !strings.HasPrefix(last, "http: TLS handshake error from 127.0.0.1:") {
		t.Fatalf("last record = %q, want a handshake failure", last)
	}
}
