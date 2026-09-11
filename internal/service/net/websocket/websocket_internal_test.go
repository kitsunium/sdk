// Package websocket — nothing follows the Close frame onto the wire.
//
// RFC 6455 §5.5.1: "The application MUST NOT send any more data frames after
// sending a Close frame." Every path that sends a Close ends the connection at
// once, but not atomically: sendClose writes the frame and releases the write
// lock, and only then does terminate close done. A Send or a Ping queued on the
// lock in between found done still open and followed the Close onto the wire.
package websocket

import (
	"bufio"
	"errors"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wireWait bounds how long a test waits for the peer side of a pipe to see the
// connection end. It is a failure deadline, never a synchronisation delay: the
// wait ends the instant the socket closes.
const wireWait time.Duration = 5 * time.Second

// Test_Conn_sendFrame pins the refusal at the exact state the race used to
// expose: the Close is on the wire, the connection has not ended yet.
//
// That state lasts a few instructions in production — from sendClose releasing
// the write lock to terminate closing done — which is why a concurrent test can
// only sample it. Here it is held open on purpose by calling sendClose alone,
// so every frame kind that goes through sendFrame is checked against it
// deterministically: a data frame, which §5.5.1 forbids outright, and the two
// control frames, which this endpoint refuses too because the connection is
// already ending.
//
// MUTATION-CHECKED. Deleting the closeSent check from sendFrame — the pre-fix
// code — fails every case, the text one with:
//
//	the text message was sent after the Close: <nil>
//	after the Close the wire carried [close text], want [close]
func Test_Conn_sendFrame(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the frame a goroutine queued behind the Close tries to write.
		send func(c *Conn) error
		//: what that frame is called in a failure message.
		frame string
	}
	tests := []tc{
		{
			name:  "a text message",
			send:  func(c *Conn) error { return c.SendText("queued behind the close") },
			frame: "text message",
		},
		{
			name:  "a binary message",
			send:  func(c *Conn) error { return c.SendBinary([]byte{1, 2, 3}) },
			frame: "binary message",
		},
		{
			name:  "a Ping the caller asked for",
			send:  func(c *Conn) error { return c.Ping(nil) },
			frame: "Ping",
		},
		{
			//: the reader answers a peer's Ping through sendFrame too.
			name:  "a Pong owed to the peer",
			send:  func(c *Conn) error { return c.sendFrame(corenet.WSPong, nil) },
			frame: "Pong",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		conn, wire := pipeConn(t)

		//: the Close is written and the lock released; terminate has not run.
		if err := conn.sendClose(corenet.WSCloseNormal, ""); err != nil {
			t.Fatalf("sendClose: %v", err)
		}
		err := c.send(conn)
		conn.terminate()
		conn.join()

		if !errs.HasCode(err, corenet.CodeWSConnClosed) {
			t.Errorf("the %s was sent after the Close: %v", c.frame, err)
		}
		if got := wireOpcodes(t, wire.bytes(t)); len(got) != 1 || got[0] != corenet.WSClose {
			t.Errorf("after the Close the wire carried %v, want [close]", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Conn_sendFrameUnderARacingClose is the same property the way a handler
// meets it: several goroutines sending while another calls CloseWith, each
// round checked on the bytes the peer actually received.
//
// It samples a window a few instructions wide, so it is the weaker of the two
// guards and Test_Conn_sendFrame is the one that pins the fix; this one pins
// what the fix must not break. Every round must end with exactly ONE Close —
// §5.5.1 allows no second one — as the last frame, and CloseWith must still
// return nil. The connection keeps its one reading goroutine throughout, as
// the Conn contract requires of every connection. It can never fail on correct
// code: the sampling only decides how OFTEN a broken sendFrame is caught.
//
// MUTATION-CHECKED. Deleting the closeSent check from sendFrame fails it with:
//
//	round N: a text frame followed the Close onto the wire
//
// Measured on that mutation over -count=20 under -race, the suite's default:
// all 20 runs failed, and 39 of the 40 case runs of 1000 rounds did, the
// latest at round 619 — at 200 rounds, the first figure tried, hits reached
// round 196, which is why the count is 1000. Without -race only 5 of the 40
// failed: the detector's slowdown is what widens the window. Sampling is all
// this test can do, and that is exactly why the deterministic test above
// exists.
func Test_Conn_sendFrameUnderARacingClose(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many goroutines race the Close.
		senders int
		//: how many connections are opened and closed under the race.
		rounds int
	}
	tests := []tc{
		{name: "one sender racing the close", senders: 1, rounds: 1000},
		{name: "several senders racing the close", senders: 4, rounds: 1000},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for round := range c.rounds {
			ops, cerr := raceTheClose(t, c.senders)

			if cerr != nil {
				t.Fatalf("round %d: CloseWith = %v, want nil", round, cerr)
			}
			if closes := countOpcode(ops, corenet.WSClose); closes != 1 {
				t.Fatalf("round %d: the wire carried %d Close frames, want exactly 1", round, closes)
			}
			if last := ops[len(ops)-1]; last != corenet.WSClose {
				t.Fatalf("round %d: a %v frame followed the Close onto the wire", round, last)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// raceTheClose opens one connection, has senders goroutines send on it while
// CloseWith runs, and returns the frames the peer received and what CloseWith
// returned.
//
// Goroutine lifecycle: one reader plus senders writers, all joined before it
// returns. The reader ends when CloseWith closes the socket under it; each
// writer ends at the first Send the connection refuses, which CloseWith
// guarantees will come.
func raceTheClose(t *testing.T, senders int) (ops []corenet.WSOpCode, closeErr error) {
	t.Helper()
	conn, wire := pipeConn(t)
	var wg sync.WaitGroup
	//: the one reader every connection needs; it ends with the socket.
	wg.Add(1)
	go receiveUntilEnded(&wg, conn)
	start := make(chan struct{})
	//: every writer is parked on start, so they all hit the lock together.
	for range senders {
		wg.Add(1)
		go sendUntilRefused(&wg, conn, start)
	}
	close(start)

	closeErr = conn.CloseWith(corenet.WSCloseNormal, "")
	wg.Wait()
	//: every frame the peer received, and CloseWith's own verdict.
	return wireOpcodes(t, wire.bytes(t)), closeErr
}

// receiveUntilEnded is the connection's one reading goroutine: it loops on
// Receive until the connection ends.
func receiveUntilEnded(wg *sync.WaitGroup, conn *Conn) {
	defer wg.Done()
	//: a terminal error is the only way out, and every test ends the connection.
	for {
		if _, err := conn.Receive(); err != nil {
			//: the connection is over.
			return
		}
	}
}

// sendUntilRefused sends on conn from the moment start closes until the
// connection refuses a message.
func sendUntilRefused(wg *sync.WaitGroup, conn *Conn, start <-chan struct{}) {
	defer wg.Done()
	<-start
	//: as fast as the write lock allows, so a writer is always waiting on it
	//: when the Close releases it.
	for {
		if err := conn.SendText("racing the close"); err != nil {
			//: refused — the connection is closing or closed.
			return
		}
	}
}

// pipeWire collects every byte the connection writes, as the peer receives it.
type pipeWire struct {
	// done is closed once the connection has closed its end of the pipe and
	// got and err are final.
	done chan struct{}
	// got is the whole stream the peer received.
	got []byte
	// err is what reading or closing the peer's end reported.
	err error
}

// bytes returns everything the peer received, once the connection has closed
// its end of the pipe.
func (w *pipeWire) bytes(t *testing.T) []byte {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(wireWait):
		t.Fatalf("the connection never closed its socket within %s", wireWait)
	}
	if w.err != nil {
		t.Fatalf("reading the peer's end of the pipe: %v", w.err)
	}
	//: the connection closed its socket; this is the whole stream.
	return w.got
}

// pipeConn assembles a connection over one end of an in-memory pipe and starts
// reading the other end the way a peer would.
//
// Goroutine lifecycle: exactly one, reading the peer's end until the
// connection closes its own — which every test here makes it do — and then
// handing the bytes over.
func pipeConn(t *testing.T) (conn *Conn, wire *pipeWire) {
	t.Helper()
	cfg, err := resolve([]Option{WithoutPing()})
	//: a refused option set would leave the test measuring nothing.
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	server, peer := stdnet.Pipe()
	wire = &pipeWire{done: make(chan struct{})}
	go func() {
		defer close(wire.done)
		//: io.ReadAll ends when the connection closes its end of the pipe.
		got, rerr := io.ReadAll(peer)
		wire.got, wire.err = got, errors.Join(rerr, peer.Close())
	}()
	//: the heartbeat is off, so every frame on the wire is one the test sent.
	conn = NewConn(server, bufio.NewReader(server), "", &cfg, nil)
	return conn, wire
}

// wireOpcodes decodes the server-to-client frames in wire, in order.
func wireOpcodes(t *testing.T, wire []byte) []corenet.WSOpCode {
	t.Helper()
	var ops []corenet.WSOpCode
	//: one frame per iteration, header first, exactly as a peer reads them.
	for len(wire) > 0 {
		size := corenet.WSFrameHeaderLen(wire)
		if size == 0 || size > len(wire) {
			t.Fatalf("a truncated frame header on the wire: % x", wire)
		}
		header, err := corenet.ParseWSFrameHeader(wire[:size])
		if err != nil {
			t.Fatalf("an unparseable frame on the wire: %v", err)
		}
		end := size + int(header.Length)
		if end > len(wire) {
			t.Fatalf("a truncated %v frame on the wire", header.OpCode)
		}
		ops = append(ops, header.OpCode)
		wire = wire[end:]
	}
	//: every frame the peer received, in order.
	return ops
}

// countOpcode counts the frames of one kind.
func countOpcode(ops []corenet.WSOpCode, want corenet.WSOpCode) int {
	count := 0
	//: a linear scan; a round writes a few thousand frames at most.
	for _, op := range ops {
		if op == want {
			count++
		}
	}
	//: how many there were.
	return count
}
