package ipc_test

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ipcRange is the 0.3.91.* block with its serial byte cleared.
const ipcRange errs.Code = 0x00_03_5B_00

// Test_sentinels pins what moved here from internal/service/proc/ipc
// (ADR 0160): each sentinel carries the code and the reason it carried before
// the move, inside the range the service layer allocated (ADR 0148). A code
// is a wire contract a deployment script branches on — IN_USE means "talk to
// it", PATH_UNSAFE means "look at the path" — so a value that drifted while
// its declaration moved is the one regression this half of the package can
// have.
func Test_sentinels(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel *errs.Error
		code     errs.Code
		reason   string
	}
	tests := []tc{
		{"a misconfiguration", ipc.Misconfigured, ipc.CodeMisconfigured, "MISCONFIGURED"},
		{"an unsafe directory", ipc.DirectoryUnsafe, ipc.CodeDirectoryUnsafe, "DIRECTORY_UNSAFE"},
		{"a socket in use", ipc.InUse, ipc.CodeInUse, "IN_USE"},
		{"a failed listen", ipc.ListenFailed, ipc.CodeListenFailed, "LISTEN_FAILED"},
		{"a refused peer", ipc.PeerRefused, ipc.CodePeerRefused, "PEER_REFUSED"},
		{"a failed dial", ipc.DialFailed, ipc.CodeDialFailed, "DIAL_FAILED"},
		{"a foreign endpoint", ipc.EndpointForeign, ipc.CodeEndpointForeign, "ENDPOINT_FOREIGN"},
		{"a closed listener", ipc.Closed, ipc.CodeClosed, "CLOSED"},
		{"an unsafe path", ipc.PathUnsafe, ipc.CodePathUnsafe, "PATH_UNSAFE"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the code is the one the service declared, unchanged by the move.
		if got := c.sentinel.Code(); got != c.code {
			t.Errorf("Code() = %v, want %v", got, c.code)
		}
		//: and it stays inside the range codeRangeOwners gives this package.
		if got := c.code &^ 0xFF; got != ipcRange {
			t.Errorf("code %v is outside 0.3.91.*", c.code)
		}
		//: the reason is what a log query matches on, so it did not move either.
		if got := c.sentinel.Reason(); got != c.reason {
			t.Errorf("Reason() = %q, want %q", got, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// pipeListener is the double the ports exist for: it hands out the server end
// of one net.Pipe with the peer the test chose, then answers CLOSED, as the
// engine does after Close.
type pipeListener struct {
	next *ipc.Conn
}

// Accept returns the prepared connection once, then CLOSED.
func (l *pipeListener) Accept() (*ipc.Conn, error) {
	//: the one connection this double was given.
	if c := l.next; c != nil {
		l.next = nil
		return c, nil
	}
	//: the engine's own verdict for a listener with nothing more to give.
	return nil, errs.Wrap(ipc.Closed, errs.WrapParams{})
}

// Addr is the pipe's address.
func (l *pipeListener) Addr() net.Addr {
	//: net.Pipe's ends report a "pipe" address.
	return pipeAddr{}
}

// Close drops what was not accepted.
func (l *pipeListener) Close() error {
	l.next = nil
	return nil
}

// Path is a path no socket was ever created at.
func (l *pipeListener) Path() string {
	//: a double needs no file.
	return "/nonexistent/double.sock"
}

// Refused is always zero: this double admits whoever it was given.
func (l *pipeListener) Refused() int64 {
	//: nothing is ever refused here.
	return 0
}

// pipeAddr is net.Pipe's address.
type pipeAddr struct{}

// Network names the pipe.
func (pipeAddr) Network() string { return "pipe" }

// String names the pipe.
func (pipeAddr) String() string { return "pipe" }

// pipeDialer connects to whatever conn it was given, as the peer it was told.
type pipeDialer struct {
	conn net.Conn
	peer ipc.PeerValue
}

// Dial hands out the client end, or the engine's verdict for a dial whose
// context already ended.
func (d pipeDialer) Dial(ctx context.Context) (*ipc.Conn, error) {
	//: a dial that cannot start is a DIAL_FAILED, as the engine reports it.
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(ipc.DialFailed, errs.WrapParams{}, errs.String("cause", err.Error()))
	}
	return &ipc.Conn{Conn: d.conn, Peer: d.peer}, nil
}

// greet is code under test that holds the ports, not the engine: it reads one
// line a dialled peer wrote through an accepted connection, and the peer the
// listener reported.
//
// Goroutine lifecycle: one writer goroutine per call, started only once both
// ends exist. It ends when the read takes its line, and greet then receives
// its result; if the read fails instead, the test's cleanup closes the pipe,
// which fails the blocked write, and the result lands in the buffered channel
// nobody reads.
func greet(ctx context.Context, ln ipc.Listener, d ipc.Dialer) (line string, peer ipc.PeerValue, err error) {
	client, err := d.Dial(ctx)
	if err != nil {
		return "", ipc.PeerValue{}, err
	}
	server, err := ln.Accept()
	if err != nil {
		return "", ipc.PeerValue{}, err
	}
	done := make(chan error, 1)
	//: net.Pipe is synchronous: the write completes only as the read takes it.
	go func() { _, werr := io.WriteString(client, "hello\n"); done <- werr }()
	buf := make([]byte, len("hello\n"))
	if _, err := io.ReadFull(server, buf); err != nil {
		return "", ipc.PeerValue{}, err
	}
	if err := <-done; err != nil {
		return "", ipc.PeerValue{}, err
	}
	return string(buf), server.Peer, nil
}

// Test_theConnectionPortsTakeADouble shows what the ports are for (ADR 0160
// §1): code that holds a Listener and a Dialer runs against net.Pipe and a
// peer the test chose, with no socket, no directory and no kernel credentials
// involved, and still sees the CLOSED and DIAL_FAILED verdicts the engine
// gives.
//
// Goroutine lifecycle: greet starts one writer per call; it ends when the
// reader takes the line, which greet waits for before returning.
func Test_theConnectionPortsTakeADouble(t *testing.T) {
	t.Parallel()
	admitted := ipc.PeerValue{UID: 1000, GID: 1000, PID: 42, Verified: true}
	type tc struct {
		name     string
		cancel   bool
		wantLine string
		wantCode errs.Code
	}
	tests := []tc{
		{"a dialled peer reaches the accepted end", false, "hello\n", 0},
		{"a dial whose context ended is refused", true, "", ipc.CodeDialFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clientEnd, serverEnd := net.Pipe()
		t.Cleanup(func() {
			for _, end := range []net.Conn{clientEnd, serverEnd} {
				if err := end.Close(); err != nil {
					t.Logf("closing a pipe end: %v", err)
				}
			}
		})
		ln := &pipeListener{next: &ipc.Conn{Conn: serverEnd, Peer: admitted}}
		ctx, cancel := context.WithCancel(t.Context())
		if c.cancel {
			cancel()
		}
		defer cancel()
		line, peer, err := greet(ctx, ln, pipeDialer{conn: clientEnd, peer: admitted})
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("greet = %v, want %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("greet = %v, want nil", err)
		}
		if line != c.wantLine || peer != admitted {
			t.Errorf("greet = %q from %+v, want %q from %+v", line, peer, c.wantLine, admitted)
		}
		//: a second Accept is the verdict a closed engine gives.
		if _, err := ln.Accept(); !errs.HasCode(err, ipc.CodeClosed) {
			t.Errorf("Accept after the last connection = %v, want CLOSED", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
