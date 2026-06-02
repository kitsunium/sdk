package nettransport

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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
		name string
		fail bool
	}{
		{"dials and ships verbatim over the conn", false},
		{"dialer failure surfaces dial code", true},
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
		name    string
		status  int
		badURL  bool
		wantErr bool
	}{
		{"2xx accepted", http.StatusOK, false, false},
		{"5xx rejected", http.StatusInternalServerError, false, true},
		{"malformed url errors", 0, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			url := "http://collector.local/ingest"
			//: the bad-url arm uses a literal that http.NewRequest rejects.
			if tc.badURL {
				url = "http://%zz"
			}
			err := postRecord(t.Context(), stubClient(tc.status), url, []byte("rec"))
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
