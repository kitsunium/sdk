//go:build linux

package websocket_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

var loopBackends = []struct {
	name    string
	backend corenet.LoopBackend
}{
	{"epoll", corenet.LoopBackendEpoll},
	{"io_uring", corenet.LoopBackendIOURing},
	{"portable", corenet.LoopBackendPortable},
}

// loopEcho echoes every message and counts opens and closes.
type loopEcho struct {
	opened atomic.Int64
	closed atomic.Int64
}

func (*loopEcho) OpenWS(*corenet.HTTP1RequestValue) int      { return 0 }
func (e *loopEcho) WSOpened(websocket.LoopConnValue, string) { e.opened.Add(1) }
func (e *loopEcho) WSClosed(websocket.LoopConnValue, error)  { e.closed.Add(1) }
func (*loopEcho) WSMessage(c websocket.LoopConnValue, m corenet.WSMessageValue) {
	_ = c.Send(m)
}

// startWS serves an echo over a loop group of the given backend.
func startWS(t *testing.T, backend corenet.LoopBackend, src websocket.LoopConnHandler, opts ...websocket.Option) (*server.Server, string) {
	t.Helper()
	opts = append([]websocket.Option{websocket.AllowAnyOrigin()}, opts...)
	h, err := websocket.NewLoopHandler(src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New()
	srv.Group("ws", server.Listen("tcp", "127.0.0.1:0"), server.Loop(corenet.LoopValue{Backend: backend, Loops: 1}), server.Plaintext()).
		HandleLoop(http1.HandleLoop(h, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Skipf("%v not available here: %v", backend, err)
	}
	return srv, srv.State().Listeners[0].Address
}

// dialLoop opens a connection and completes the handshake, returning a peer the
// adversarial table's helpers can drive.
func dialLoop(t *testing.T, addr string) *wsPeer {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(wsHandshake)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	var status string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		if status == "" {
			status = line
		}
		if line == "\r\n" {
			break
		}
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status line %q", status)
	}
	return &wsPeer{t: t, conn: c, br: br}
}

// TestAdversarial_onTheLoop is the acceptance: the table every hostile frame is
// judged by, replayed against the loop engine, produces the same close codes.
func TestAdversarial_onTheLoop(t *testing.T) {
	cases := adversarialTable()
	for _, b := range loopBackends[:2] {
		t.Run(b.name, func(t *testing.T) {
			src := &loopEcho{}
			srv, addr := startWS(t, b.backend, src)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			})
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					p := dialLoop(t, addr)
					c.write(p)
					p.expectClose(c.code)
					if _, _, _, err := p.readFrame(); err == nil {
						t.Fatalf("still open after %s (%s)", c.name, c.why)
					}
				})
			}
		})
	}
}

// TestLoopWS_echoAndPing holds a text message echoed and a ping answered, on
// every backend, through a fragmented message too.
func TestLoopWS_echoAndPing(t *testing.T) {
	for _, b := range loopBackends {
		t.Run(b.name, func(t *testing.T) {
			src := &loopEcho{}
			srv, addr := startWS(t, b.backend, src)
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}()
			p := dialLoop(t, addr)
			p.write(t, frame(true, 0, 0x1, true, []byte("hello")))
			p.expectMessage(0x1, []byte("hello"))
			p.write(t, frame(true, 0, 0x9, true, []byte("ping!")))
			fin, op, payload, err := p.readFrame()
			if err != nil || !fin || op != 0xA || string(payload) != "ping!" {
				t.Fatalf("pong: fin %v op %x %q %v", fin, op, payload, err)
			}
			//: a message in fragments, a control frame between them.
			p.write(t, frame(false, 0, 0x1, true, []byte("frag")))
			p.write(t, frame(true, 0, 0x9, true, []byte("mid")))
			fin, op, _, err = p.readFrame()
			if err != nil || op != 0xA || !fin {
				t.Fatalf("pong mid: %x %v", op, err)
			}
			p.write(t, frame(true, 0, 0x0, true, []byte("ments")))
			p.expectMessage(0x1, []byte("fragments"))
			//: a closing handshake is echoed.
			p.write(t, frame(true, 0, 0x8, true, []byte{0x03, 0xE8}))
			p.expectClose(1000)
		})
	}
}

// TestLoopWS_drainSends1001 holds the drain's close code.
func TestLoopWS_drainSends1001(t *testing.T) {
	for _, b := range loopBackends {
		t.Run(b.name, func(t *testing.T) {
			src := &loopEcho{}
			srv, addr := startWS(t, b.backend, src)
			p := dialLoop(t, addr)
			waitOpen := time.Now().Add(5 * time.Second)
			for src.opened.Load() != 1 {
				if time.Now().After(waitOpen) {
					t.Fatal("never opened")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			p.expectClose(1001)
		})
	}
}

// TestLoopWS_handshakeRefusals holds the handshake rules on the loop: the codec
// answers what Upgrade would answer, the program its own status.
func TestLoopWS_handshakeRefusals(t *testing.T) {
	src := &loopEcho{}
	srv, addr := startWS(t, corenet.LoopBackendEpoll, src, websocket.AllowOrigins("https://a.example"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	for _, c := range []struct {
		name, raw, want string
	}{
		{"not a GET", strings.Replace(wsHandshake, "GET", "POST", 1), "405"},
		{"no Upgrade", strings.Replace(wsHandshake, "Upgrade: websocket\r\n", "", 1), "400"},
		{"a wrong version", strings.Replace(wsHandshake, "Version: 13", "Version: 8", 1), "426"},
		{"a bad key", strings.Replace(wsHandshake, "dGhlIHNhbXBsZSBub25jZQ==", "short", 1), "400"},
		{"an origin not allowed", strings.Replace(wsHandshake, "Host: h\r\n", "Host: h\r\nOrigin: https://evil.example\r\n", 1), "403"},
	} {
		c1, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c1.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c1.Write([]byte(c.raw))
		line, err := bufio.NewReader(c1).ReadString('\n')
		_ = c1.Close()
		if err != nil || !strings.Contains(line, c.want) {
			t.Errorf("%s: %q %v, want %s", c.name, line, err, c.want)
		}
	}
}
