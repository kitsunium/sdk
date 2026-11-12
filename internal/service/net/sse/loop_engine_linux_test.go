//go:build linux

package sse_test

import (
	"bytes"
	"context"
	"io"
	stdnet "net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/internal/service/net/sse"
)

// engines are the backends a real loop group is exercised on; the portable
// backend exists elsewhere too, the two native ones only on Linux.
var engines = []struct {
	name    string
	backend corenet.LoopBackend
}{
	{"epoll", corenet.LoopBackendEpoll},
	{"io_uring", corenet.LoopBackendIOURing},
	{"portable", corenet.LoopBackendPortable},
}

// countingSource sends n events on each stream it opens and ends it.
type countingSource struct {
	n      int
	opened atomic.Int64
	closed atomic.Int64
}

func (s *countingSource) OpenSSE(*corenet.HTTP1RequestValue, []byte) int { return 0 }

func (s *countingSource) StreamOpened(st sse.LoopStreamValue) {
	s.opened.Add(1)
	for i := range s.n {
		if err := st.Send(corenet.SSEEventValue{ID: strconv.Itoa(i), Data: "x"}); err != nil {
			return
		}
	}
	if s.n > 0 {
		_ = st.End()
	}
}

func (s *countingSource) StreamClosed(sse.LoopStreamValue, error) { s.closed.Add(1) }

// startSSE serves src over a group of the given backend and returns the address.
func startSSE(t *testing.T, backend corenet.LoopBackend, src sse.LoopSource, tune corenet.LoopValue, opts ...sse.Option) (*server.Server, string) {
	t.Helper()
	h, err := sse.NewLoopHandler(src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	tune.Backend, tune.Loops = backend, 1
	srv := server.New()
	srv.Group("sse", server.Listen("tcp", "127.0.0.1:0"), server.Loop(tune), server.Plaintext()).
		HandleLoop(http1.HandleLoop(h, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Skipf("%v not available here: %v", backend, err)
	}
	st := srv.State()
	return srv, st.Listeners[0].Address
}

// TestLoopSSE_engineDeliversAThousandEvents is the acceptance: 1 000 events over
// one connection to a net/http client reading an unbounded body, the head
// carrying Connection: close and no Content-Length, the connection ending
// cleanly at End.
func TestLoopSSE_engineDeliversAThousandEvents(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			src := &countingSource{n: 1000}
			srv, addr := startSSE(t, e.backend, src, corenet.LoopValue{})
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}()
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + addr + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.ContentLength != -1 || !resp.Close || resp.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("head: length %d, close %v, %v", resp.ContentLength, resp.Close, resp.Header)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %d bytes: %v", len(body), err)
			}
			if got := strings.Count(string(body), "data: x\n\n"); got != 1000 {
				t.Fatalf("%d events, want 1000", got)
			}
		})
	}
}

// TestLoopSSE_engineDrainEndsWithRetry holds that a drain ends a stream within
// the grace, the client reading the last retry field the handler wrote.
func TestLoopSSE_engineDrainEndsWithRetry(t *testing.T) {
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) {
			src := &countingSource{}
			srv, addr := startSSE(t, e.backend, src, corenet.LoopValue{})
			c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Write([]byte("GET /e HTTP/1.1\r\nHost: h\r\n\r\n")); err != nil {
				t.Fatal(err)
			}
			waitTrue(t, func() bool { return src.opened.Load() == 1 })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			var got bytes.Buffer
			if _, err := io.Copy(&got, c); err != nil {
				t.Fatalf("read: %v, got %q", err, got.Bytes())
			}
			if !strings.Contains(got.String(), "retry: 3000\n\n") {
				t.Fatalf("%q", got.Bytes())
			}
		})
	}
}

func waitTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
