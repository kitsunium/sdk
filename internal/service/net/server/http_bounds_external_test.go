// Package server_test — the two HTTP-only group options, observed from a real
// client over a real socket.
package server_test

import (
	"bufio"
	"errors"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// boundsDeadline bounds the test's own reads, so a server that never answers
// fails the test instead of hanging the suite.
const boundsDeadline time.Duration = 10 * time.Second

// startBoundedServer serves a handler answering 200 on a loopback group built
// with opts, and returns the bound address.
func startBoundedServer(t *testing.T, opts ...server.GroupOption) string {
	t.Helper()
	srv := server.New()
	opts = append([]server.GroupOption{server.Listen("tcp", "127.0.0.1:0")}, opts...)
	srv.Group("api", opts...).HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	//: the address actually bound.
	return srv.State().Listeners[0].Address
}

// statusFor sends one GET carrying a header of headerSize bytes and returns
// the status line's code.
func statusFor(t *testing.T, addr string, headerSize int) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("X-Padding", strings.Repeat("a", headerSize))
	client := &http.Client{Timeout: boundsDeadline}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeOrFail(t, response.Body)
	return response.StatusCode
}

// TestMaxHeaderBytesRefusesAnOversizedHeader pins the cap: a header past it is
// refused with 431 before any handler runs, one under it is served, and a
// group that set no cap keeps net/http's 1 MiB default.
func TestMaxHeaderBytesRefusesAnOversizedHeader(t *testing.T) {
	t.Parallel()
	capped := startBoundedServer(t, server.MaxHeaderBytes(1<<10))
	if got := statusFor(t, capped, 32<<10); got != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("a 32 KiB header under a 1 KiB cap = %d, want 431", got)
	}
	if got := statusFor(t, capped, 512); got != http.StatusOK {
		t.Errorf("a 512 B header under a 1 KiB cap = %d, want 200", got)
	}
	uncapped := startBoundedServer(t)
	if got := statusFor(t, uncapped, 32<<10); got != http.StatusOK {
		t.Errorf("a 32 KiB header with no cap set = %d, want 200 (net/http's 1 MiB default)", got)
	}
}

// TestReadHeaderTimeoutBoundsTheHeaderPhaseAlone pins the slowloris defence: a
// client that sends half a request line and stalls is disconnected at the
// header deadline, long before the read timeout a body is allowed.
func TestReadHeaderTimeoutBoundsTheHeaderPhaseAlone(t *testing.T) {
	t.Parallel()
	const headerBound time.Duration = 200 * time.Millisecond
	addr := startBoundedServer(t, server.ReadTimeout(30*time.Second), server.ReadHeaderTimeout(headerBound))
	conn, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, conn) })
	if deadlineErr := conn.SetDeadline(time.Now().Add(boundsDeadline)); deadlineErr != nil {
		t.Fatalf("deadline: %v", deadlineErr)
	}
	started := time.Now()
	//: half a request line, then nothing.
	if _, writeErr := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	_, readErr := bufio.NewReader(conn).ReadString('\n')
	elapsed := time.Since(started)
	//: nothing is served for a request whose header never finished.
	if readErr == nil {
		t.Fatal("the server answered a request whose header never finished")
	}
	//: the server hung up; the test's own deadline did not expire.
	if netErr, ok := errors.AsType[stdnet.Error](readErr); ok && netErr.Timeout() {
		t.Fatalf("the test's deadline expired after %v: the stalled header phase was never ended", elapsed)
	}
	//: and it hung up at the header bound, not at the read timeout.
	if elapsed > 5*time.Second {
		t.Fatalf("the stalled header phase lasted %v — the 30 s read timeout, not the %v header bound", elapsed, headerBound)
	}
}
