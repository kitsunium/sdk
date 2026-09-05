package server_test

import (
	"context"
	"io"
	stdnet "net"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// TestEveryGroupOptionIsWiredThrough pins that each public option actually
// reaches the engine. A facade forwarder that silently drops its argument would
// compile, pass a smoke test, and quietly disable a timeout in production.
func TestEveryGroupOptionIsWiredThrough(t *testing.T) {
	t.Parallel()
	srv := server.New(server.WithDrainTimeout(time.Second))
	srv.Group("api",
		server.Listen("tcp", "127.0.0.1:0"),
		server.ReadTimeout(2*time.Second),
		server.WriteTimeout(2*time.Second),
		server.IdleTimeout(5*time.Second),
		server.ReadBufferSize(4096),
	).HandleFunc(func(_ context.Context, c server.Conn) error {
		//: the scratch buffer must arrive at the requested size, ready to read
		//: into — a zero-length slice would force every caller to reslice it.
		buf := c.Buffer()
		if len(buf) != 4096 {
			t.Errorf("Buffer() length = %d, want 4096", len(buf))
		}
		n, err := c.Read(buf)
		if err != nil {
			return err
		}
		_, err = c.Write(buf[:n])
		return err
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { closeOrFail(t, srv) }()

	if got := echo(t, srv.State().Listeners[0].Address, "buffered"); got != "buffered\n" {
		t.Fatalf("echo = %q", got)
	}
}

// TestTLSOptionServesOverTLS proves the identity actually reaches the listener
// by completing a real handshake, rather than asserting a struct field.
func TestTLSOptionServesOverTLS(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintCert(t)
	identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("server identity: %v", err)
	}
	clientID, err := tlsid.New(tlsid.Params{RootsPEM: certPEM, ServerName: "kitsunium-test"})
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}

	srv := server.New()
	srv.Group("secure", server.Listen("tcp", "127.0.0.1:0"), server.TLS(identity)).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, werr := io.WriteString(c, "secure\n")
			return werr
		})
	if serr := srv.Start(t.Context()); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	defer func() { closeOrFail(t, srv) }()

	reply := dialTLS(t, srv.State().Listeners[0].Address, clientID.ClientConfig())
	if reply != "secure\n" {
		t.Fatalf("reply = %q, want %q", reply, "secure\n")
	}
}

// TestPlainDialAgainstATLSGroupFails pins that the TLS option is not decorative:
// a plaintext client must not be served by a TLS listener.
func TestPlainDialAgainstATLSGroupFails(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintCert(t)
	identity, err := tlsid.New(tlsid.Params{CertPEM: certPEM, KeyPEM: keyPEM})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	srv := server.New()
	srv.Group("secure", server.Listen("tcp", "127.0.0.1:0"), server.TLS(identity)).
		HandleFunc(func(_ context.Context, c server.Conn) error {
			_, werr := io.WriteString(c, "secure\n")
			return werr
		})
	if serr := srv.Start(t.Context()); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	defer func() { closeOrFail(t, srv) }()

	c, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer func() { closeOrFail(t, c) }()
	if _, werr := io.WriteString(c, "plaintext\n"); werr != nil {
		//: a write failure is already proof the peer rejected us.
		return
	}
	if derr := c.SetReadDeadline(time.Now().Add(2 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, 16)
	n, rerr := c.Read(buf)
	//: a TLS listener must never answer a plaintext peer with our payload.
	if rerr == nil && strings.Contains(string(buf[:n]), "secure") {
		t.Fatal("a TLS listener served a plaintext client")
	}
}

// TestChainIsReachableFromTheFacade pins that middleware composition works
// without importing internal/*.
func TestChainIsReachableFromTheFacade(t *testing.T) {
	t.Parallel()
	var order []string
	tag := func(name string) server.Middleware {
		return func(next server.Handler) server.Handler {
			return server.HandlerFunc(func(ctx context.Context, c server.Conn) error {
				order = append(order, name)
				return next.ServeConn(ctx, c)
			})
		}
	}
	base := server.HandlerFunc(func(context.Context, server.Conn) error {
		order = append(order, "handler")
		return nil
	})
	chained := server.Chain(base, tag("a"), tag("b"))
	if err := chained.ServeConn(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(order, ",") != "a,b,handler" {
		t.Fatalf("order = %v, want a,b,handler", order)
	}
}
