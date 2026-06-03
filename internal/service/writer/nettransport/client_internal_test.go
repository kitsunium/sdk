package nettransport

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// closedLoopbackAddr is a loopback port that is reliably not listening, so a dial
// to it is refused — the deterministic signal that net.Dial ran (the nil-dialer
// fallback branch) on a host where loopback connect is otherwise blocked.
const closedLoopbackAddr = "127.0.0.1:1"

// roundTripFunc adapts a function to http.RoundTripper so the HTTP seam tests
// inject a canned response with no real socket (the bazel sandbox restricts
// listening).
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls the adapted function.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	//: delegate to the test-supplied behaviour.
	return f(r)
}

// stubClient returns an *http.Client whose transport always responds with
// status, no network involved.
func stubClient(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		//: a canned response with an empty, closable body.
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
}

// Test_newConnSeam exercises the conn seam over an in-memory net.Pipe. Goroutine
// lifecycle: the happy-path case spawns ONE reader goroutine that does a single
// blocking Read on the pipe's server end, publishes the frame to a buffered
// channel, and returns; the test joins it via the channel receive and closes the
// server end, so the goroutine always terminates and never leaks.
func Test_newConnSeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fail    bool
		realTCP bool
	}{
		{"dials and ships verbatim over the conn", false, false},
		{"dialer failure surfaces dial code", true, false},
		{"nil dialer falls back to net.Dial", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: failure arm — a failing dialer must surface the dial code.
			if tc.fail {
				_, _, err := newConnSeam("tcp", "x:1", failingDialer)
				if !errs.HasCode(err, CodeNetTransportDialFailed) {
					t.Errorf("%s: err=%v want dial-failed", tc.name, err)
				}
				return
			}
			//: nil-dialer arm — proves a nil dialer takes the stdlib net.Dial branch
			//: (client.go:39-42). Where loopback connect works it runs a real E2E
			//: send; where the sandbox blocks loopback it asserts the same branch
			//: via a closed-port refusal. Either way the fallback path is covered
			//: and the test passes (no skip).
			if tc.realTCP {
				assertNilDialerFallback(t, tc.name)
				return
			}
			//: an in-memory net.Pipe injected via the Dialer seam exercises the
			//: send path deterministically with no real socket (the bazel
			//: sandbox restricts TCP accept; net.Pipe needs none).
			clientEnd, serverEnd := net.Pipe()
			dialer := func(_, _ string) (net.Conn, error) { return clientEnd, nil }
			got := make(chan string, 1)
			go func() {
				//: lifecycle: one blocking Read, publish to the buffered `got`
				//: channel, return. Cannot leak — the buffer never blocks the
				//: send and serverEnd is closed by the test below.
				buf := make([]byte, 64)
				n, rerr := serverEnd.Read(buf)
				//: a read failure publishes its diagnostic so the test fails loud.
				if rerr != nil && n == 0 {
					got <- "read-err:" + rerr.Error()
					return
				}
				//: forward exactly the bytes the seam wrote.
				got <- string(buf[:n])
			}()
			send, closer, err := newConnSeam("tcp", "ignored", dialer)
			//: the injected dial must succeed.
			if err != nil {
				t.Fatalf("%s: newConnSeam: %v", tc.name, err)
			}
			//: net.Pipe is synchronous: send blocks until the goroutine reads.
			if serr := send(t.Context(), []byte("ping")); serr != nil {
				t.Fatalf("%s: send: %v", tc.name, serr)
			}
			//: the peer must have received the record verbatim.
			if received := <-got; received != "ping" {
				t.Errorf("%s: received %q want ping", tc.name, received)
			}
			//: the closer must release the connection.
			if cerr := closer(); cerr != nil {
				t.Errorf("%s: closer: %v", tc.name, cerr)
			}
			swallowErr(serverEnd.Close())
		})
	}
}

func Test_newHTTPSeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"posts to a 2xx endpoint"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			send, closer := newHTTPSeam("http://collector.local/ingest", stubClient(http.StatusOK))
			//: a 2xx response is a clean send.
			if serr := send(t.Context(), []byte("rec")); serr != nil {
				t.Errorf("%s: send: %v", tc.name, serr)
			}
			//: the idle-teardown closer must never fail.
			if cerr := closer(); cerr != nil {
				t.Errorf("%s: closer: %v", tc.name, cerr)
			}
		})
	}
}

func Test_postRecord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		badURL    bool
		transFail bool
		wantErr   bool
	}{
		{"2xx accepted", http.StatusOK, false, false, false},
		{"5xx rejected", http.StatusInternalServerError, false, false, true},
		{"malformed url errors", 0, true, false, true},
		{"transport failure surfaces raw error", 0, false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url := "http://collector.local/ingest"
			//: the bad-url arm uses a literal that http.NewRequest rejects.
			if tc.badURL {
				url = "http://%zz"
			}
			//: the transport arm injects a RoundTripper that fails the dial so
			//: postRecord returns the raw cause (client.go:105-108) — no status,
			//: no body to drain.
			client := stubClient(tc.status)
			if tc.transFail {
				client = errClient("dial failed")
			}
			err := postRecord(t.Context(), client, url, []byte("rec"))
			//: the verdict must match the status/url expectation.
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: postRecord err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func Test_swallowErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"nil is a no-op", nil},
		{"non-nil is silently dropped", errNetBoom{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			panicked := false
			func() {
				defer func() {
					//: a recovered value means swallowErr panicked — a bug.
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				swallowErr(tc.err)
			}()
			//: swallowErr must silently drop both arms without panicking.
			if panicked {
				t.Errorf("%s: swallowErr panicked, want silent drop", tc.name)
			}
		})
	}
}

// errClient returns an *http.Client whose RoundTripper always fails the dial, so
// the caller exercises postRecord's raw-transport-error branch (client.go:105-108)
// with no real socket.
func errClient(msg string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		//: a fixed transport failure drives the raw-error path deterministically.
		return nil, errors.New(msg)
	})}
}

// assertNilDialerFallback proves a nil dialer takes the stdlib net.Dial branch
// (client.go:39-42). When loopback connect is available it binds a real listener
// and asserts the seam ships bytes verbatim end-to-end; otherwise (some CI
// sandboxes seccomp-block loopback connect) it asserts the same branch via a
// closed-port refusal — the nil dialer still reaches net.Dial, whose error is
// wrapped as the dial sentinel. Either way the fallback path is exercised against
// production code and the test passes without a skip.
func assertNilDialerFallback(t *testing.T, name string) {
	t.Helper()
	//: pick the assertion that this host's loopback stack can satisfy.
	if loopbackConnectWorks() {
		assertNilDialerShipsOverTCP(t, name)
		return
	}
	//: refusal arm — a nil dialer to a closed port still proves net.Dial was
	//: selected (the branch under test) and its error surfaces the dial sentinel.
	_, _, err := newConnSeam("tcp", closedLoopbackAddr, nil)
	if !errs.HasCode(err, CodeNetTransportDialFailed) {
		t.Errorf("%s: nil-dialer refusal err=%v want dial-failed", name, err)
	}
}

// assertNilDialerShipsOverTCP binds a loopback listener and proves the nil-dialer
// seam ships bytes verbatim. Goroutine lifecycle: one accept goroutine signals
// readiness, then does a single blocking Accept+Read, publishes the frame to the
// buffered `got` channel, and returns; the test joins it via the receive, so it
// never leaks. The readiness handshake guarantees the acceptor is scheduled before
// the dial.
func assertNilDialerShipsOverTCP(t *testing.T, name string) {
	t.Helper()
	//: a port-0 loopback listener gives a real, ephemeral TCP endpoint.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("%s: net.Listen: %v", name, lerr)
	}
	defer swallowErr(ln.Close())
	got := make(chan string, 1)
	ready := make(chan struct{})
	go func() {
		//: announce the acceptor is live so the dial below cannot outrun Accept.
		close(ready)
		//: accept exactly one connection, read one frame, publish, return.
		conn, aerr := ln.Accept()
		if aerr != nil {
			got <- "accept-err:" + aerr.Error()
			return
		}
		defer swallowErr(conn.Close())
		buf := make([]byte, 64)
		n, rerr := conn.Read(buf)
		//: a read failure publishes its diagnostic so the test fails loud.
		if rerr != nil && n == 0 {
			got <- "read-err:" + rerr.Error()
			return
		}
		got <- string(buf[:n])
	}()
	//: block until the acceptor goroutine is running before dialing.
	<-ready
	//: nil dialer forces the stdlib net.Dial fallback (client.go:39-42).
	send, closer, err := newConnSeam("tcp", ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("%s: newConnSeam: %v", name, err)
	}
	//: the seam must ship the payload over the real socket.
	if serr := send(t.Context(), []byte("ping")); serr != nil {
		t.Fatalf("%s: send: %v", name, serr)
	}
	//: the peer must have received the record verbatim.
	if received := <-got; received != "ping" {
		t.Errorf("%s: received %q want ping", name, received)
	}
	//: the closer must release the dialed connection.
	if cerr := closer(); cerr != nil {
		t.Errorf("%s: closer: %v", name, cerr)
	}
}

// loopbackConnectWorks reports whether this host can complete a loopback
// accept+connect round-trip, so the real-socket assertions only run where the
// sandbox permits them. It performs one full handshake (ready signal, accept,
// dial, close) and treats any failure as "unavailable". Goroutine lifecycle: the
// lone acceptor closes `accepted` then returns after a single Accept; the dialer
// joins via the channel, so it cannot leak.
func loopbackConnectWorks() bool {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	//: no listener at all means loopback is unusable.
	if err != nil {
		return false
	}
	defer swallowErr(ln.Close())
	accepted := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		//: signal liveness so the dial cannot outrun Accept on a shallow backlog.
		close(ready)
		conn, aerr := ln.Accept()
		//: a clean accept is reported by closing the channel after teardown.
		if aerr == nil {
			swallowErr(conn.Close())
		}
		close(accepted)
	}()
	<-ready
	conn, derr := net.Dial("tcp", ln.Addr().String())
	//: a refused dial is exactly the blocked-loopback condition we detect.
	if derr != nil {
		<-accepted
		return false
	}
	swallowErr(conn.Close())
	<-accepted
	//: a completed round-trip means the real-socket assertions are safe to run.
	return true
}

// Test_newHTTPSeam_realServer drives the production HTTP seam through a full POST:
// the request body is actually drained and the Content-Type header validated,
// covering the 2xx happy path, the non-2xx rejection, and the transport-error
// path. Where loopback connect works it uses a real httptest server (true socket
// egress); where the sandbox blocks loopback it drives the same seam against a
// recording RoundTripper that performs the identical body drain + header check.
// Both paths assert production behaviour; neither skips.
func Test_newHTTPSeam_realServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		closeFirst bool
		wantErr    bool
	}{
		{"2xx handler accepts payload and sets Content-Type", http.StatusOK, false, false},
		{"5xx handler rejects payload", http.StatusInternalServerError, false, true},
		{"server closed mid-flight triggers transport error", http.StatusOK, true, true},
	}
	realServer := loopbackConnectWorks()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a body-read failure inside the handler/transport publishes empty so
			//: the verbatim-body assertion below fails loud rather than silently.
			gotCT, gotBody := runHTTPSeamCase(t, realServer, tc.status, tc.closeFirst)
			//: the 2xx arm proves the body and header reached the server verbatim.
			if !tc.wantErr {
				if gotCT != contentTypeJSON {
					t.Errorf("%s: Content-Type = %q want %q", tc.name, gotCT, contentTypeJSON)
				}
				if gotBody != "rec-body" {
					t.Errorf("%s: body = %q want %q", tc.name, gotBody, "rec-body")
				}
			}
		})
	}
}

// runHTTPSeamCase exercises newHTTPSeam for one case and returns the Content-Type
// and body the server/transport observed. It fatals when the send verdict (error
// vs nil) disagrees with the closeFirst/status expectation. realServer selects a
// true httptest socket; otherwise an in-process RoundTripper drives the same seam.
func runHTTPSeamCase(t *testing.T, realServer bool, status int, closeFirst bool) (gotCT, gotBody string) {
	t.Helper()
	//: a non-2xx status or a torn-down server is the failure expectation.
	wantErr := closeFirst || status >= httpStatusCeil || status < httpStatusFloor
	if realServer {
		gotCT, gotBody = httpSeamViaServer(t, status, closeFirst, wantErr)
		return gotCT, gotBody
	}
	gotCT, gotBody = httpSeamViaTransport(t, status, closeFirst, wantErr)
	return gotCT, gotBody
}

// httpSeamViaServer runs the case against a real httptest server.
func httpSeamViaServer(t *testing.T, status int, closeFirst, wantErr bool) (gotCT, gotBody string) {
	t.Helper()
	//: buffered channels carry the handler's observations back race-free.
	ctCh := make(chan string, 1)
	bodyCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctCh <- r.Header.Get("Content-Type")
		body, rerr := io.ReadAll(r.Body)
		//: a drain failure publishes empty so the verbatim check fails loud.
		if rerr != nil {
			body = nil
		}
		bodyCh <- string(body)
		w.WriteHeader(status)
	}))
	//: the close-first arm tears the server down before the send so the POST
	//: fails at the transport layer (real connection-refused).
	if closeFirst {
		srv.Close()
	} else {
		//: t.Cleanup (not defer) is required with parallel subtests.
		t.Cleanup(srv.Close)
	}
	send, closer := newHTTPSeam(srv.URL, srv.Client())
	assertSeamSend(t, send, closer, wantErr)
	//: the closed-server arm never reached the handler — return empty captures.
	if closeFirst {
		return "", ""
	}
	return <-ctCh, <-bodyCh
}

// httpSeamViaTransport runs the case against an in-process RoundTripper that drains
// the request body and validates the header exactly as a server would.
func httpSeamViaTransport(t *testing.T, status int, closeFirst, wantErr bool) (gotCT, gotBody string) {
	t.Helper()
	//: a recording transport captures the seam's request without a socket.
	rt := &recordingRoundTripper{status: status, fail: closeFirst}
	send, closer := newHTTPSeam("http://collector.local/ingest", &http.Client{Transport: rt})
	assertSeamSend(t, send, closer, wantErr)
	//: the transport-error arm produced no observations — return empty captures.
	if closeFirst {
		return "", ""
	}
	return rt.gotCT, rt.gotBody
}

// assertSeamSend drives one send + closer and fatals when the verdict disagrees.
func assertSeamSend(t *testing.T, send sendFunc, closer func() error, wantErr bool) {
	t.Helper()
	serr := send(t.Context(), []byte("rec-body"))
	//: the verdict must match the handler/transport expectation.
	if (serr != nil) != wantErr {
		t.Fatalf("send err = %v, wantErr = %v", serr, wantErr)
	}
	//: the idle-teardown closer must never fail.
	if cerr := closer(); cerr != nil {
		t.Errorf("closer: %v", cerr)
	}
}

// recordingRoundTripper drains the request body and records its Content-Type, then
// returns the configured status — or a transport error when fail is set, mirroring
// a connection-refused without a socket.
type recordingRoundTripper struct {
	// status is the HTTP status the canned response carries.
	status int
	// fail, when true, makes RoundTrip return a transport error.
	fail bool
	// gotCT is the Content-Type header the seam transmitted.
	gotCT string
	// gotBody is the request body the seam transmitted.
	gotBody string
}

// RoundTrip records the request and returns the canned response or a transport
// error, satisfying http.RoundTripper.
func (rt *recordingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	//: the fail arm models a transport failure (no response, raw error).
	if rt.fail {
		return nil, errors.New("dial failed")
	}
	rt.gotCT = r.Header.Get("Content-Type")
	body, rerr := io.ReadAll(r.Body)
	//: a drain failure records empty so the verbatim check fails loud.
	if rerr != nil {
		body = nil
	}
	rt.gotBody = string(body)
	//: a canned response with an empty, closable body and the wanted status.
	return &http.Response{StatusCode: rt.status, Body: io.NopCloser(strings.NewReader(""))}, nil
}
