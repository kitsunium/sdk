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

// TestDrain_requestHeardInTheDrainIsAnswered opens a connection, lets the
// lane take it, starts the drain, and only once the listener closed — the
// drain under way, read on Linux in this process's descriptors and elsewhere
// in a refused dial (listenerCloseWatch) — sends its request: the answer must
// come back before the drain closes the connection. The line echo answers from OnData; the
// HTTP/1.1 and h2c adapters answer at OnBatchEnd, after the drain has judged
// the batch's connections, and lost the answer to that judgement (#1385).
func TestDrain_requestHeardInTheDrainIsAnswered(t *testing.T) {
	type tc struct {
		name string
		// group declares the group on srv and returns its open and close
		// counts, read once the server stopped.
		group func(srv *Server, b corenet.LoopBackend) func() (int64, int64)
		// request makes one exchange on c and says what went wrong, empty when
		// nothing did.
		request func(c stdnet.Conn) string
	}
	protos := drainProtos()
	cases := []tc{
		{name: "line", group: protos[0].group, request: func(c stdnet.Conn) string {
			if _, err := c.Write([]byte("a\n")); err != nil {
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
		}, request: func(c stdnet.Conn) string {
			if _, err := c.Write([]byte("GET / HTTP/1.1\r\nHost: drain\r\n\r\n")); err != nil {
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
		{name: "h2c", group: protos[1].group, request: func(c stdnet.Conn) string {
			return h2cRequest(c, bufio.NewReader(c))
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
		// The lane holds the connection, silent: the drain leaves it open for
		// its quiet period, its request still to come.
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
