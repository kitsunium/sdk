// Package server_test — the two HTTP-only group options, observed from a real
// client over a real socket.
package server_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
	srv.Group("api", append(opts, server.Plaintext())...).HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

// bodyOutcome is what a handler saw of a request body.
type bodyOutcome struct {
	// read is how many bytes it read.
	read int
	// err is how the read ended, nil at a clean EOF.
	err error
	// cancelled reports that the request's context was done when it answered.
	cancelled bool
	// reads dates each read of the body, in order.
	reads []bodyRead
}

// bodyRead dates one read of a request body: the server's per-read deadline
// is installed as the read starts, so start plus the bound is its deadline.
type bodyRead struct {
	// start is when the handler asked for the read.
	start time.Time
	// end is when the read returned.
	end time.Time
	// n is how many bytes it returned.
	n int
	// err is how it ended.
	err error
}

// datedBody records the date of each read of the body it wraps.
type datedBody struct {
	// inner is the body the server handed the handler.
	inner io.ReadCloser
	// reads is the record, written by the handler's goroutine alone.
	reads *[]bodyRead
}

// Read implements io.Reader.
func (b datedBody) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := b.inner.Read(p)
	*b.reads = append(*b.reads, bodyRead{start: start, end: time.Now(), n: n, err: err})
	return n, err
}

// Close implements io.Closer.
func (b datedBody) Close() error {
	return b.inner.Close()
}

// bodyDates renders the peer's writes and the handler's reads, each against
// origin, with each read's deadline: a read that timed out while the peer's
// next write left after its deadline met a late peer; one whose next write
// left before it met a bound that did not hold.
func bodyDates(origin time.Time, writes []time.Time, reads []bodyRead, bound time.Duration) string {
	var b strings.Builder
	at := func(d time.Time) time.Duration { return d.Sub(origin).Round(time.Millisecond) }
	for i, w := range writes {
		fmt.Fprintf(&b, "\n  write %d at %v", i, at(w))
	}
	for i, r := range reads {
		fmt.Fprintf(&b, "\n  read %d at %v, deadline %v, returned at %v: %d bytes, err %v",
			i, at(r.start), at(r.start.Add(bound)), at(r.end), r.n, r.err)
	}
	return b.String()
}

// startBodyServer serves an HTTP group bounded by read whose handler reads the
// whole body — pausing pause after its first byte, and again after the last —
// reports what it saw, and answers 200.
func startBodyServer(t *testing.T, read, pause time.Duration) (addr string, seen <-chan bodyOutcome) {
	t.Helper()
	outcomes := make(chan bodyOutcome, 1)
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0"), server.ReadTimeout(read), server.Plaintext()).
		HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reads []bodyRead
			body := datedBody{inner: r.Body, reads: &reads}
			first := make([]byte, 1)
			n, err := io.ReadFull(body, first)
			if err == nil {
				time.Sleep(pause)
				var rest []byte
				rest, err = io.ReadAll(body)
				n += len(rest)
			}
			//: the time after the body is the handler's too.
			time.Sleep(pause)
			outcomes <- bodyOutcome{read: n, err: err, cancelled: r.Context().Err() != nil, reads: reads}
			w.WriteHeader(http.StatusOK)
		}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	//: the address actually bound.
	return srv.State().Listeners[0].Address, outcomes
}

// TestReadTimeoutBoundsEachReadOfAnHTTPBody pins issue #454: on an HTTP group
// the read bound is per read of the body, as TimeoutsValue promises and as the
// stream path applies it — not http.Server.ReadTimeout, a budget for the whole
// request that cut off a peer whose every read was inside the bound. A peer
// that stops sending is still cut off, and the handler's own time — between
// two reads, after the last — is never charged to the peer: once the body has
// ended, the deadline is cleared, so net/http's watch for a vanished peer does
// not cancel the request's context while the handler works.
//
// It is deliberately NOT parallel with the package's other tests: its peer
// paces its writes with its own sleeps, a fifth of the bound apart, so the
// test holds only while no write leaves more than the bound after the read
// waiting for it. On illumos, run beside the package's parallel tests and
// their thousands of connections, a read timed out after 12 and after 14
// bytes (#1406): the deadline is installed afresh as each read starts, so a
// write came the bound late, and the server cut off a late peer, rightly. Its
// cases still run beside one another, and a failure dates every write and
// every read's deadline, which tells a late peer from a bound that was not
// renewed.
func TestReadTimeoutBoundsEachReadOfAnHTTPBody(t *testing.T) {
	const (
		// read is the per-read bound.
		read time.Duration = 500 * time.Millisecond
		// size is the declared body length.
		size int = 20
	)
	type tc struct {
		name string
		// chunk is how many bytes the peer sends at a time, gap apart.
		chunk int
		gap   time.Duration
		// stallAfter is how many bytes the peer sends before saying nothing
		// more; zero sends the whole body.
		stallAfter int
		// pause is the handler's own time after its first byte and after the
		// last.
		pause time.Duration
		// wantErr reports that the body must end in an error.
		wantErr bool
	}
	tests := []tc{
		//: the audit's case: ten reads of two bytes, 100 ms apart — a second
		//: in all, every read a fifth of the bound.
		{name: "a slow but steady body", chunk: 2, gap: 100 * time.Millisecond},
		{name: "a body sent at once", chunk: size},
		{name: "a handler slower than the bound between and after its reads", chunk: size, pause: 2 * read},
		{name: "a peer that stops mid-body", chunk: 2, gap: 50 * time.Millisecond, stallAfter: 4, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr, seen := startBodyServer(t, read, c.pause)
		conn, err := stdnet.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { closeOrFail(t, conn) })
		if err := conn.SetDeadline(time.Now().Add(boundsDeadline)); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		origin := time.Now()
		if _, err := fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", size); err != nil {
			t.Fatalf("write the header: %v", err)
		}
		limit := size
		if c.stallAfter > 0 {
			limit = c.stallAfter
		}
		var writes []time.Time
		for sent := 0; sent < limit; sent += c.chunk {
			time.Sleep(c.gap)
			if _, err := conn.Write([]byte(strings.Repeat("a", min(c.chunk, limit-sent)))); err != nil {
				t.Fatalf("write the body: %v", err)
			}
			writes = append(writes, time.Now())
		}

		var got bodyOutcome
		select {
		case got = <-seen:
		case <-time.After(boundsDeadline):
			t.Fatal("the handler never finished reading the body")
		}

		if c.wantErr {
			//: a peer that stops sending is cut off at the bound.
			if got.err == nil {
				t.Fatalf("a body that stalled after %d bytes read cleanly", c.stallAfter)
			}
			return
		}
		if got.err != nil || got.read != size {
			t.Fatalf("the handler read %d bytes, err %v; want %d and nil — every read was inside the %v bound:%s",
				got.read, got.err, size, read, bodyDates(origin, writes, got.reads, read))
		}
		if got.cancelled {
			t.Fatal("the request's context was cancelled while the handler worked after the body")
		}
		status, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || !strings.Contains(status, "200") {
			t.Fatalf("the response = %q, %v; want 200", status, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
