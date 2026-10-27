// Package server_test — the simple API (ADR 0187 §D6, ADR 0197): HTTP, TCP
// and UDP choose the engine by the handler's type, name the group after its
// address, require Plaintext of an HTTP group without TLS, and refuse every
// handler they cannot place with a typed error.
package server_test

import (
	"bufio"
	"context"
	"io"
	stdnet "net"
	"net/http"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// bothHTTP is an http.Handler and an HTTP1Handler: Engine chooses.
type bothHTTP struct{}

func (bothHTTP) ServeHTTP(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "std") }

func (bothHTTP) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Body = []byte("h1")
}

// lineLoop is a LoopHandler that answers each read with "loop\n".
type lineLoop struct{}

func (lineLoop) OnOpen(corenet.LoopIO, corenet.LoopConnRef) {}
func (lineLoop) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	_ = io.Write(ref, []byte("loop\n"))
	return len(buf)
}
func (lineLoop) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)      {}
func (lineLoop) OnWritable(corenet.LoopIO, corenet.LoopConnRef)     {}
func (lineLoop) OnBatchEnd(corenet.LoopIO)                          {}
func (lineLoop) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) {}

// startSimple starts srv and returns its first listener's address.
func startSimple(t *testing.T, srv *server.Server) string {
	t.Helper()
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.State().Listeners[0].Address
}

// bodyOf GETs / on addr and returns the body.
func bodyOf(t *testing.T, addr string) string {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestSimpleHTTP pins HTTP's engine choice: an http.Handler on net/http, an
// HTTP1Handler on the loop's codec, a handler that is both on the engine
// Engine names; the group is named "tcp+" + addr, or by Name.
func TestSimpleHTTP(t *testing.T) {
	t.Parallel()
	std := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "std") })
	h1 := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = []byte("h1")
	})
	for _, c := range []struct {
		name  string
		h     any
		opts  []server.GroupOption
		body  string
		loops int
		group string
	}{
		{"an http.Handler", std, nil, "std", 0, "tcp+127.0.0.1:0"},
		{"an HTTP1Handler", h1, nil, "h1", 1, "tcp+127.0.0.1:0"},
		{"both, on the goroutine engine", bothHTTP{}, []server.GroupOption{server.Engine(corenet.EngineGoroutine)}, "std", 0, "tcp+127.0.0.1:0"},
		{"both, on the loop", bothHTTP{}, []server.GroupOption{server.Engine(corenet.EngineLoop)}, "h1", 1, "tcp+127.0.0.1:0"},
		{"named", std, []server.GroupOption{server.Name("api")}, "std", 0, "api"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			srv.HTTP("127.0.0.1:0", c.h, append([]server.GroupOption{server.Plaintext()}, c.opts...)...)
			addr := startSimple(t, srv)
			if got := bodyOf(t, addr); got != c.body {
				t.Errorf("body %q, want %q", got, c.body)
			}
			st := srv.State()
			if len(st.Loops) != c.loops || st.Listeners[0].Group != c.group {
				t.Errorf("loops %d group %q, want %d %q", len(st.Loops), st.Listeners[0].Group, c.loops, c.group)
			}
		})
	}
}

// TestSimpleTCP pins TCP's: a ConnHandler on a goroutine per connection, a
// LoopHandler on the loop.
func TestSimpleTCP(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		h     any
		want  string
		loops int
	}{
		{"a ConnHandler", corenet.ConnHandlerFunc(func(_ context.Context, c corenet.Conn) error {
			//: the client's byte is read first: a close with unread input is a
			//: reset on Windows, which would discard the answer before it is read.
			if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
				return err
			}
			_, err := io.WriteString(c, "conn\n")
			return err
		}), "conn\n", 0},
		{"a LoopHandler", lineLoop{}, "loop\n", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			srv.TCP("127.0.0.1:0", c.h)
			addr := startSimple(t, srv)
			conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.WriteString(conn, "x")
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil || line != c.want {
				t.Fatalf("read %q, %v, want %q", line, err, c.want)
			}
			if st := srv.State(); len(st.Loops) != c.loops || st.Listeners[0].Group != "tcp+127.0.0.1:0" {
				t.Errorf("loops %d group %q", len(st.Loops), st.Listeners[0].Group)
			}
		})
	}
}

// TestSimpleUDP pins UDP's: a PacketHandler, the group named "udp+" + addr.
func TestSimpleUDP(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	srv := server.New()
	srv.UDP("127.0.0.1:0", corenet.PacketHandlerFunc(func(_ context.Context, p corenet.Packet) error {
		got <- string(p.Data())
		return nil
	}))
	addr := startSimple(t, srv)
	if g := srv.State().Listeners[0].Group; g != "udp+127.0.0.1:0" {
		t.Fatalf("group %q", g)
	}
	c, err := stdnet.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = io.WriteString(c, "ping")
	select {
	case s := <-got:
		if s != "ping" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no datagram")
	}
}

// TestSimple_refused pins every refusal of the simple API, each reported by
// Start before anything is bound, with its code.
func TestSimple_refused(t *testing.T) {
	t.Parallel()
	std := http.NotFoundHandler()
	var nilStd *nilHandler
	pkt := corenet.PacketHandlerFunc(func(context.Context, corenet.Packet) error { return nil })
	conn := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil })
	plain := server.Plaintext()
	for _, c := range []struct {
		name    string
		declare func(srv *server.Server)
		want    errs.Code
	}{
		{"HTTP nil", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", nil, plain) }, corenet.CodeHandlerMissing},
		{"HTTP typed nil", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", nilStd, plain) }, corenet.CodeHandlerMissing},
		{"HTTP nil func", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", http.HandlerFunc(nil), plain) }, corenet.CodeHandlerMissing},
		{"HTTP both, no Engine", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", bothHTTP{}, plain) }, corenet.CodeHandlerAmbiguous},
		{"HTTP an http.Handler on the loop", func(srv *server.Server) {
			srv.HTTP("127.0.0.1:0", std, plain, server.Engine(corenet.EngineLoop))
		}, corenet.CodeEngineUnsupported},
		{"HTTP a PacketHandler", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", pkt, plain) }, corenet.CodeHandlerUnsupported},
		{"HTTP a ConnHandler", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", conn, plain) }, corenet.CodeHandlerUnsupported},
		{"HTTP a LoopHandler", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", lineLoop{}, plain) }, corenet.CodeHandlerUnsupported},
		{"HTTP without TLS or Plaintext", func(srv *server.Server) { srv.HTTP("127.0.0.1:0", std) }, corenet.CodePlaintextRequired},
		{"HTTP1Handler without TLS or Plaintext", func(srv *server.Server) {
			srv.HTTP("127.0.0.1:0", corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {}))
		}, corenet.CodePlaintextRequired},
		{"HTTP Plaintext with TLS", func(srv *server.Server) {
			srv.HTTP("127.0.0.1:0", std, plain, server.TLS(selfSignedIdentity(t)))
		}, corenet.CodePlaintextWithTLS},
		{"TCP an http.Handler", func(srv *server.Server) { srv.TCP("127.0.0.1:0", std) }, corenet.CodeHandlerUnsupported},
		{"TCP an HTTP1Handler", func(srv *server.Server) {
			srv.TCP("127.0.0.1:0", corenet.HTTP1HandlerFunc(func(*corenet.HTTP1RequestValue, *corenet.HTTP1ResponseValue) {}))
		}, corenet.CodeHandlerUnsupported},
		{"TCP a PacketHandler", func(srv *server.Server) { srv.TCP("127.0.0.1:0", pkt) }, corenet.CodeHandlerUnsupported},
		{"TCP nil", func(srv *server.Server) { srv.TCP("127.0.0.1:0", nil) }, corenet.CodeHandlerMissing},
		{"TCP a ConnHandler on the loop", func(srv *server.Server) {
			srv.TCP("127.0.0.1:0", conn, server.Engine(corenet.EngineLoop))
		}, corenet.CodeEngineUnsupported},
		{"UDP a LoopHandler", func(srv *server.Server) { srv.UDP("127.0.0.1:0", lineLoop{}) }, corenet.CodeHandlerUnsupported},
		{"UDP nil", func(srv *server.Server) { srv.UDP("127.0.0.1:0", nil) }, corenet.CodeHandlerMissing},
		{"UDP an option it cannot honour", func(srv *server.Server) {
			srv.UDP("127.0.0.1:0", pkt, server.ReadTimeout(time.Second))
		}, corenet.CodePacketOptionUnsupported},
		{"a datagram group given TLS", func(srv *server.Server) {
			srv.PacketGroup("g", server.Listen("udp", "127.0.0.1:0"), server.TLS(selfSignedIdentity(t))).Handle(pkt)
		}, corenet.CodePacketOptionUnsupported},
		{"a HandleHTTP group without TLS or Plaintext", func(srv *server.Server) {
			srv.Group("g", server.Listen("tcp", "127.0.0.1:0")).HandleHTTP(std)
		}, corenet.CodePlaintextRequired},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			c.declare(srv)
			err := srv.Start(t.Context())
			t.Cleanup(func() { _ = srv.Close() })
			if !errs.HasCode(err, c.want) {
				t.Fatalf("Start = %v, want %v", err, c.want)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("a refused Start bound %d listeners", n)
			}
		})
	}
}

// nilHandler is an http.Handler whose nil pointer the simple API refuses.
type nilHandler struct{ body string }

func (h *nilHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, strings.ToUpper(h.body))
}
