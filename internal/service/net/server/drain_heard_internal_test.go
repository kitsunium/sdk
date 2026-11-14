// Package server — a request the drain hears is answered (#1385): the
// HTTP adapters write a batch's answers at its end, so a connection whose
// request came in the batch that judged it settled must not be closed before
// that end has written to it.
package server

import (
	"bufio"
	"context"
	"io"
	stdnet "net"
	"net/http"
	"runtime"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// h2cHead is the start of h2c's preface: a sniff that waits for the rest.
const h2cHead = "PRI * HTTP/2.0\r\n"

// TestDrain_requestHeardInTheDrainIsAnswered opens a connection, lets the
// lane take it with the start of a request, starts the drain, and only once
// the listener closed — the drain under way, read on Linux in this process's
// descriptors and elsewhere in a refused dial (listenerCloseWatch) — sends
// the rest: the answer must come back before the drain closes the
// connection. The line echo answers from OnData; the HTTP/1.1 and h2c
// adapters answer at OnBatchEnd, after the drain had judged the batch's
// connections, and lost the answer to that judgement (#1385).
//
// The connection holds the start of its request, not nothing: a connection
// that sent nothing is closed after drainQuiet, a second from its accept,
// and the listener's close, which the rest waits for, may come later than
// that on a loaded host — the bazel job's h2c/auto read an end before any
// frame 1.46 s into the case, its request written after that quiet close.
// Mid-request, the connection waits for the rest whatever the drain's pace.
func TestDrain_requestHeardInTheDrainIsAnswered(t *testing.T) {
	type tc struct {
		name string
		// group declares the group on srv and returns its open and close
		// counts, read once the server stopped.
		group func(srv *Server, b corenet.LoopBackend) func() (int64, int64)
		// head is the start of the request, sent before the drain; request
		// sends the rest, makes the exchange on c and says what went wrong,
		// empty when nothing did.
		head    []byte
		request func(c stdnet.Conn) string
	}
	protos := drainProtos()
	cases := []tc{
		{name: "line", group: protos[0].group, head: []byte("a"), request: func(c stdnet.Conn) string {
			if _, err := c.Write([]byte("\n")); err != nil {
				return "write: " + err.Error()
			}
			got := make([]byte, 2)
			if _, err := io.ReadFull(c, got); err != nil || string(got) != "a\n" {
				return "echo " + string(got) + ": " + errString(err)
			}
			return ""
		}},
		{name: "http1", group: func(srv *Server, b corenet.LoopBackend) func() (int64, int64) {
			hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
			srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Loop(corenet.LoopValue{Backend: b, Loops: corenet.LoopsAuto})).HandleLoop(http1.HandleLoop(hello, http1.Parser{}))
			return func() (int64, int64) { return ringCounts(srv) }
		}, head: []byte("GET / HTTP/1.1\r\n"), request: func(c stdnet.Conn) string {
			if _, err := c.Write([]byte("Host: drain\r\n\r\n")); err != nil {
				return "write: " + err.Error()
			}
			res, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				return "before the answer: " + err.Error()
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, 16))
			swallowErr(res.Body.Close())
			if res.StatusCode != http.StatusOK || string(body) != "ok" {
				return "answer " + res.Status + " " + string(body) + ": " + errString(err)
			}
			return ""
		}},
		{name: "h2c", group: protos[1].group, head: []byte(h2cHead), request: func(c stdnet.Conn) string {
			return h2cRequestFrom(c, bufio.NewReader(c), len(h2cHead))
		}},
	}
	if runtime.GOOS == "windows" {
		// The close is read in a refused dial off Linux, and Windows answers
		// a SYN to a closed port only after retrying it for seconds: no
		// refusal comes within a probe's bound.
		t.Skip("a refused dial cannot date the drain on Windows")
	}
	runCase := func(t *testing.T, c tc, b corenet.LoopBackend) {
		srv := New(WithDrainGrace(10 * time.Millisecond))
		t.Cleanup(func() { swallowErr(srv.Close()) })
		counts := c.group(srv, b)
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		conn := loopDial(t, addr)
		// The lane holds the connection mid-request: the drain leaves it
		// open, the rest of its request still to come.
		if _, err := conn.Write(c.head); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); ; {
			if opens, _ := counts(); opens >= 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the lane never opened the connection")
			}
			time.Sleep(time.Millisecond)
		}
		closed := listenerCloseWatch(t, addr)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shut := make(chan error, 1)
		go func() { shut <- srv.Shutdown(ctx) }()
		// The listener closed: the drain began before it did.
		for deadline := time.Now().Add(5 * time.Second); !closed(); {
			if time.Now().After(deadline) {
				t.Fatal("the listener never closed")
			}
		}
		if msg := c.request(conn); msg != "" {
			t.Errorf("a request heard during the drain was not answered: %s", msg)
		}
		// Answered: the client closes, which ends the drain's lingering
		// close of the connection.
		swallowErr(conn.Close())
		if err := <-shut; err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if o, cl := counts(); o != cl {
			t.Fatalf("%d opened, %d closed", o, cl)
		}
	}
	for _, c := range cases {
		for _, b := range loopBackends() {
			t.Run(c.name+"/"+b.String(), func(t *testing.T) { runCase(t, c, b) })
		}
	}
}
