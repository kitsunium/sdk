package journald

import (
	"context"
	"net"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the datagram sink satisfies the Sink port (kept in the
// : test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ corelogger.Sink = (*journaldSink)(nil)

func Test_newJournaldSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"wraps a connected socket"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() { ignoreClose(serverEnd.Close()) })
			s := newJournaldSink(clientEnd)
			//: the constructed sink must own and close its connection.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}

// Test_journaldSink_Write exercises the framing + send path. Goroutine lifecycle:
// the happy-path case spawns ONE reader goroutine that does a single blocking
// Read on the pipe's server end, publishes the framed datagram to a buffered
// channel, and returns; the test joins it via the channel receive, so it always
// terminates and never leaks.
func Test_journaldSink_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		closed  bool
		cancel  bool
		wantErr bool
	}{
		{"frames MESSAGE= and sends", false, false, false},
		{"cancelled ctx refused", false, true, true},
		{"closed socket surfaces write code", true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clientEnd, serverEnd := net.Pipe()
			s := newJournaldSink(clientEnd)
			got := make(chan string, 1)
			//: only the happy arm needs a reader draining the datagram.
			if !tc.closed && !tc.cancel {
				go func() {
					//: lifecycle: one blocking Read, publish, return — joined by
					//: the channel receive below; cannot leak.
					buf := make([]byte, 64)
					n, rerr := serverEnd.Read(buf)
					//: a read failure publishes its diagnostic so the test fails loud.
					if rerr != nil && n == 0 {
						got <- "read-err:" + rerr.Error()
						return
					}
					got <- string(buf[:n])
				}()
			}
			//: the closed arm pre-closes the socket so conn.Write fails.
			if tc.closed {
				ignoreClose(clientEnd.Close())
				ignoreClose(serverEnd.Close())
			}
			ctx := t.Context()
			if tc.cancel {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			}
			_, err := s.Write(ctx, corelogger.RecordEvent{}, []byte("ping"))
			//: error arms carry the write sentinel.
			if tc.wantErr {
				if !errs.HasCode(err, CodeJournaldWriteFailed) {
					t.Errorf("%s: err=%v want write-failed", tc.name, err)
				}
				return
			}
			//: happy arm — the peer received the framed entry.
			if frame := <-got; !strings.HasPrefix(frame, "MESSAGE=") || !strings.Contains(frame, "ping") {
				t.Errorf("%s: frame=%q want MESSAGE=…ping", tc.name, frame)
			}
		})
	}
}

// Test_journaldSink_Write_singleNewline proves the encoder's trailing newline is
// not doubled by framing: a newline-terminated payload yields exactly one
// trailing '\n' after the value (single-newline MESSAGE frame). Goroutine
// lifecycle mirrors Test_journaldSink_Write — one blocking Read, joined by the
// channel receive, so it cannot leak.
func Test_journaldSink_Write_singleNewline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		//: the encoder always hands the sink a newline-terminated line; framing
		//: must strip it so exactly one trailing '\n' survives.
		{"newline-terminated line is not doubled", "ping\n", "MESSAGE=ping\n"},
		//: a bare line (no terminator) still gets exactly one framing newline.
		{"unterminated line gets one newline", "ping", "MESSAGE=ping\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() { ignoreClose(serverEnd.Close()) })
			s := newJournaldSink(clientEnd)
			got := make(chan string, 1)
			go func() {
				//: one blocking Read publishes the framed datagram, then returns.
				buf := make([]byte, 64)
				n, rerr := serverEnd.Read(buf)
				//: a read failure publishes its diagnostic so the test fails loud.
				if rerr != nil && n == 0 {
					got <- "read-err:" + rerr.Error()
					return
				}
				got <- string(buf[:n])
			}()
			//: send the payload exactly as the encoder would hand it over.
			if _, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte(tc.input)); err != nil {
				t.Fatalf("%s: Write: %v", tc.name, err)
			}
			//: framing must not double the terminator.
			if frame := <-got; frame != tc.want {
				t.Errorf("%s: frame=%q want %q", tc.name, frame, tc.want)
			}
		})
	}
}

func Test_journaldSink_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cancel  bool
		wantErr bool
	}{
		{"live ctx flushes clean", false, false},
		{"cancelled ctx surfaces error", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() { ignoreClose(serverEnd.Close()) })
			s := newJournaldSink(clientEnd)
			ctx := t.Context()
			if tc.cancel {
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			}
			//: the flush verdict must match the cancellation expectation.
			if err := s.Flush(ctx); (err != nil) != tc.wantErr {
				t.Errorf("%s: Flush err=%v wantErr=%v", tc.name, err, tc.wantErr)
			}
		})
	}
}

// failingConn embeds net.Conn so it satisfies the interface, but overrides Close
// to return a fixed error. Only Close is ever called by journaldSink.Close, so
// the embedded nil net.Conn is never dereferenced — this isolates the close
// error-wrap branch without a real socket that refuses to fail on demand.
type failingConn struct {
	net.Conn
}

// Close always fails, driving journaldSink.Close down its error-wrap branch.
func (failingConn) Close() error {
	//: a fixed failure exercises the close-error path deterministically.
	return errJournaldBoom{}
}

func Test_journaldSink_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fail bool
	}{
		{"close releases the connection", false},
		//: a failing conn.Close must surface the wrapped write sentinel.
		{"close error surfaces write code", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the failing arm swaps in a conn whose Close returns an error so
			//: the journald_sink.go:89-91 wrap branch is exercised directly.
			if tc.fail {
				s := newJournaldSink(failingConn{})
				if cerr := s.Close(); !errs.HasCode(cerr, CodeJournaldWriteFailed) {
					t.Errorf("%s: Close()=%v want write-failed", tc.name, cerr)
				}
				return
			}
			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() { ignoreClose(serverEnd.Close()) })
			s := newJournaldSink(clientEnd)
			//: a clean close on a live connection must succeed.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("%s: close: %v", tc.name, cerr)
			}
		})
	}
}
