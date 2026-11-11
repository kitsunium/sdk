// Package server — the PROXY protocol layer of a TCP group (#841).
package server

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// trustLoopback is a policy that believes the test's own balancer.
func trustLoopback(extra ...string) corenet.ProxyProtocolValue {
	return corenet.ProxyProtocolValue{Trusted: append([]string{"127.0.0.0/8"}, extra...), Timeout: corenet.DurationValue(5 * time.Second)}
}

// v2Header builds a PROXY v2 header announcing ip:port, TCP over IPv4 or IPv6.
func v2Header(ip stdnet.IP, port int) []byte {
	out := append([]byte{}, proxyV2Signature[:]...)
	out = append(out, proxyV2Version<<4|proxyV2Proxy)
	var family byte
	var addrs []byte
	if v4 := ip.To4(); v4 != nil {
		family = proxyV2TCP4
		addrs = append(append(addrs, v4...), 10, 0, 0, 1)
	} else {
		family = proxyV2TCP6
		addrs = append(append(addrs, ip.To16()...), make([]byte, 16)...)
	}
	addrs = binary.BigEndian.AppendUint16(addrs, uint16(port))
	addrs = binary.BigEndian.AppendUint16(addrs, 443)
	out = append(out, family)
	out = binary.BigEndian.AppendUint16(out, uint16(len(addrs)))
	return append(out, addrs...)
}

// serveProxied runs a TCP group under the option and reports each connection's
// remote address, with the first bytes it read.
func serveProxied(t *testing.T, v corenet.ProxyProtocolValue, opts ...GroupOption) (srv *Server, addr string, seen <-chan string) {
	t.Helper()
	got := make(chan string, 256)
	srv = New()
	all := append([]GroupOption{Listen("tcp", "127.0.0.1:0"), ProxyProtocol(v)}, opts...)
	srv.Group("proxied", all...).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		buf := make([]byte, 16)
		n, err := c.Read(buf)
		if err != nil && !errors.Is(err, io.EOF) {
			got <- "read error: " + err.Error()
			return err
		}
		got <- c.RemoteAddr().String() + " " + string(buf[:n])
		return nil
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	return srv, srv.State().Listeners[0].Address, got
}

// dialBalancer connects and sends a header, returning the conn.
func dialBalancer(t *testing.T, addr string, header []byte) stdnet.Conn {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { swallowErr(c.Close()) })
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	return c
}

// await reads one report of the handler.
func await(t *testing.T, seen <-chan string) string {
	t.Helper()
	select {
	case line := <-seen:
		return line
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never ran")
		return ""
	}
}

// TestProxy_v2AnnouncesTheClient pins a v2 header over IPv4 and IPv6, the bytes
// after it reaching the handler untouched: nothing past the header is consumed.
func TestProxy_v2AnnouncesTheClient(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   stdnet.IP
		want string
	}{
		{"ipv4", stdnet.ParseIP("203.0.113.7"), "203.0.113.7:4242 payload"},
		{"ipv6", stdnet.ParseIP("2001:db8::9"), "[2001:db8::9]:4242 payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, addr, seen := serveProxied(t, trustLoopback())
			c := dialBalancer(t, addr, append(v2Header(tc.ip, 4242), "payload"...))
			defer func() { swallowErr(c.Close()) }()
			if got := await(t, seen); got != tc.want {
				t.Fatalf("handler saw %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProxy_v1AnnouncesTheClient pins the text form.
func TestProxy_v1AnnouncesTheClient(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"tcp4", "PROXY TCP4 198.51.100.1 10.0.0.1 40000 443\r\n", "198.51.100.1:40000 hello"},
		{"tcp6", "PROXY TCP6 2001:db8::2 2001:db8::1 40001 443\r\n", "[2001:db8::2]:40001 hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, addr, seen := serveProxied(t, trustLoopback())
			c := dialBalancer(t, addr, []byte(tc.line+"hello"))
			defer func() { swallowErr(c.Close()) }()
			if got := await(t, seen); got != tc.want {
				t.Fatalf("handler saw %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProxy_keepsTheSocketsAddress pins the headers that announce no client.
func TestProxy_keepsTheSocketsAddress(t *testing.T) {
	v2Local := append(append([]byte{}, proxyV2Signature[:]...), proxyV2Version<<4|proxyV2Local, proxyV2Unspec, 0, 0)
	for _, tc := range []struct {
		name   string
		header []byte
	}{
		{"v2 local", v2Local},
		{"v1 unknown", []byte("PROXY UNKNOWN\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, addr, seen := serveProxied(t, trustLoopback())
			c := dialBalancer(t, addr, append(tc.header, "ping"...))
			defer func() { swallowErr(c.Close()) }()
			got := await(t, seen)
			if !strings.HasPrefix(got, "127.0.0.1:") || !strings.HasSuffix(got, " ping") {
				t.Fatalf("handler saw %q, want the socket's own address", got)
			}
		})
	}
}

// TestProxy_refusals pins every refusal: closed, counted, the handler never run.
func TestProxy_refusals(t *testing.T) {
	long := "PROXY TCP4 1.1.1.1 2.2.2.2 1 2" + strings.Repeat(" ", 200) + "\r\n"
	over := append(append([]byte{}, proxyV2Signature[:]...), proxyV2Version<<4|proxyV2Proxy, proxyV2TCP4, 0xff, 0xff)
	for _, tc := range []struct {
		name   string
		header []byte
	}{
		{"no header", []byte("GET / HTTP/1.1\r\n\r\n")},
		{"v1 too long", []byte(long)},
		{"v1 bad address", []byte("PROXY TCP4 nonsense 10.0.0.1 1 2\r\n")},
		{"v1 family mismatch", []byte("PROXY TCP4 2001:db8::1 2001:db8::2 1 2\r\n")},
		{"v1 bad port", []byte("PROXY TCP4 1.1.1.1 2.2.2.2 099 2\r\n")},
		{"v1 bare newline", []byte("PROXY TCP4 1.1.1.1 2.2.2.2 1 2\n")},
		{"v2 too long", over},
		{"v2 bad version", append(append([]byte{}, proxyV2Signature[:]...), 3<<4|proxyV2Proxy, proxyV2TCP4, 0, 12, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)},
		{"v2 bad family", append(append([]byte{}, proxyV2Signature[:]...), proxyV2Version<<4|proxyV2Proxy, 0x12, 0, 12, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)},
		{"v2 short address", append(append([]byte{}, proxyV2Signature[:]...), proxyV2Version<<4|proxyV2Proxy, proxyV2TCP4, 0, 4, 1, 2, 3, 4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, addr, seen := serveProxied(t, trustLoopback())
			c := dialBalancer(t, addr, tc.header)
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("a refused header was answered")
			}
			if got := refusals(srv, "proxied"); got != 1 {
				t.Fatalf("ProxyRefused = %d, want 1", got)
			}
			select {
			case line := <-seen:
				t.Fatalf("the handler ran: %q", line)
			default:
			}
		})
	}
}

// refusals reads the group's refusal count from State.
func refusals(srv *Server, group string) uint64 {
	for _, row := range srv.State().Listeners {
		if row.Group == group {
			return row.ProxyRefused
		}
	}
	return 0
}

// TestProxy_untrustedPeerIsClosedBeforeAByte pins that a peer outside Trusted is
// closed at Accept, counted, with no read made: a dial then a TLS ClientHello
// gets nothing back.
func TestProxy_untrustedPeerIsClosedBeforeAByte(t *testing.T) {
	v := corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.0/8"}}
	srv, addr, seen := serveProxied(t, v, TLS(testIdentity(t)))
	c, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { swallowErr(c.Close()) }()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Even a full, valid header from an untrusted peer is ignored.
	_, _ = c.Write(v2Header(stdnet.ParseIP("203.0.113.7"), 4242))
	if n, err := c.Read(make([]byte, 16)); n != 0 || err == nil {
		t.Fatalf("read = %d, %v, want a closed connection", n, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for refusals(srv, "proxied") != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := refusals(srv, "proxied"); got != 1 {
		t.Fatalf("ProxyRefused = %d, want 1", got)
	}
	select {
	case line := <-seen:
		t.Fatalf("the handler ran: %q", line)
	default:
	}
}

// TestProxy_cutHeaderRefusedOnTheClock pins a v2 header cut at 15 bytes refused
// when Timeout elapses on the server's clock, a manual one.
func TestProxy_cutHeaderRefusedOnTheClock(t *testing.T) {
	manual := clock.NewManualClock(time.Unix(0, 0))
	srv := New()
	srv.clk = manual
	srv.Group("proxied", Listen("tcp", "127.0.0.1:0"),
		ProxyProtocol(corenet.ProxyProtocolValue{Trusted: []string{"127.0.0.0/8"}, Timeout: corenet.DurationValue(time.Hour)})).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			t.Error("the handler ran on a cut header")
			return nil
		})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	full := v2Header(stdnet.ParseIP("203.0.113.7"), 4242)
	c := dialBalancer(t, srv.State().Listeners[0].Address, full[:15])
	// The timer is armed by the serving goroutine: wait, then cross it.
	waitArmed(t, manual)
	time.Sleep(50 * time.Millisecond)
	if got := refusals(srv, "proxied"); got != 0 {
		t.Fatalf("ProxyRefused = %d before the budget elapsed", got)
	}
	manual.Advance(time.Hour + time.Second)
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection was not closed")
	}
	if got := refusals(srv, "proxied"); got != 1 {
		t.Fatalf("ProxyRefused = %d, want 1", got)
	}
}

// proxiedHandshake dials through the test balancer, starts a TLS handshake from
// ip, and returns the connection, the balancer's ip announced.
func proxiedHandshake(t *testing.T, addr string, ip stdnet.IP) *tls.Conn {
	t.Helper()
	raw := dialBalancer(t, addr, v2Header(ip, 1234))
	tlsClient := tls.Client(raw, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test's self-signed peer.
	return tlsClient
}

// TestProxy_admissionKeysOnTheAnnouncedClient pins the issue's acceptance: 100
// handshakes from 100 announced clients are refused none, and 5 from one are
// refused exactly the fifth.
func TestProxy_admissionKeysOnTheAnnouncedClient(t *testing.T) {
	admission := NewAdmission(AdmissionConfig{PerAddress: 4})
	var served sync.WaitGroup
	srv := New()
	srv.Group("proxied", Listen("tcp", "127.0.0.1:0"), TLS(testIdentity(t)), PreHandshake(admission),
		ProxyProtocol(trustLoopback())).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		defer served.Done()
		_, _ = io.Copy(io.Discard, c)
		return nil
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	addr := srv.State().Listeners[0].Address

	// 100 handshakes held open through their handshake, from 100 clients.
	conns := make([]*tls.Conn, 0, 100)
	served.Add(100)
	var handshakes sync.WaitGroup
	for i := range 100 {
		client := proxiedHandshake(t, addr, stdnet.IPv4(203, 0, 113, byte(i+1)))
		conns = append(conns, client)
		handshakes.Add(1)
		go func() {
			defer handshakes.Done()
			if err := client.Handshake(); err != nil {
				t.Errorf("handshake %d: %v", i, err)
			}
		}()
	}
	handshakes.Wait()
	if got := admission.Refused(); got != 0 {
		t.Fatalf("100 announced clients: Refused() = %d", got)
	}
	for _, c := range conns {
		swallowErr(c.Close())
	}
	served.Wait()

	// Five handshakes from one announced client, none finishing its flight: the
	// fifth is the one refused. The header is sent and the ClientHello held back.
	const client5 = "198.51.100.200"
	var held []stdnet.Conn
	for range 4 {
		raw := dialBalancer(t, addr, v2Header(stdnet.ParseIP(client5), 1234))
		held = append(held, raw)
	}
	waitPending(t, admission, client5, 4)
	fifth := dialBalancer(t, addr, v2Header(stdnet.ParseIP(client5), 1234))
	if _, err := fifth.Read(make([]byte, 1)); err == nil {
		t.Fatal("the fifth connection from one client was served")
	}
	if got := admission.Refused(); got != 1 {
		t.Fatalf("Refused() = %d, want exactly the fifth", got)
	}
	for _, c := range held {
		swallowErr(c.Close())
	}
}

// waitPending waits until admission holds n connections from ip.
func waitPending(t *testing.T, a *Admission, ip string, n int) {
	t.Helper()
	key := admissionKeyOf(stdnet.ParseIP(ip))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		got := a.pending[key]
		a.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("admission never held %d connections from %s", n, ip)
}

// TestProxy_refusedOptions pins the Start refusals.
func TestProxy_refusedOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    corenet.ProxyProtocolValue
		net  string
		want errs.Code
	}{
		{"empty trusted", corenet.ProxyProtocolValue{}, "tcp", corenet.CodeOptionValueInvalid},
		{"bad range", corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.1"}}, "tcp", corenet.CodeOptionValueInvalid},
		{"negative timeout", corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.0/8"}, Timeout: -1}, "tcp", corenet.CodeOptionValueInvalid},
		{"unix group", corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.0/8"}}, "unix", corenet.CodeOptionValueInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New()
			addr := "127.0.0.1:0"
			if tc.net == "unix" {
				addr = socketDir(t) + "/p.sock"
			}
			srv.Group("x", Listen(tc.net, addr), ProxyProtocol(tc.v)).HandleFunc(func(context.Context, corenet.Conn) error { return nil })
			err := srv.Start(t.Context())
			t.Cleanup(func() { swallowErr(srv.Close()) })
			if !errs.HasCode(err, tc.want) {
				t.Fatalf("Start = %v, want %s", err, fmt.Sprint(tc.want))
			}
		})
	}
	t.Run("loop group", func(t *testing.T) {
		srv := New()
		srv.Group("l", Listen("tcp", "127.0.0.1:0"), ProxyProtocol(corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.0/8"}})).HandleLoop(&loopLineH{})
		err := srv.Start(t.Context())
		t.Cleanup(func() { swallowErr(srv.Close()) })
		if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
			t.Fatalf("Start = %v, want LoopOptionUnsupported", err)
		}
	})
	t.Run("packet group", func(t *testing.T) {
		srv := New()
		srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), ProxyProtocol(corenet.ProxyProtocolValue{Trusted: []string{"10.0.0.0/8"}})).HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		err := srv.Start(t.Context())
		t.Cleanup(func() { swallowErr(srv.Close()) })
		if !errs.HasCode(err, corenet.CodePacketOptionUnsupported) {
			t.Fatalf("Start = %v, want PacketOptionUnsupported", err)
		}
	})
}

// TestProxy_trackedGroup pins the layers together: a ceilinged plaintext HTTP
// group's socket reports the announced client through the tracker.
func TestProxy_passesThroughTheCloseTracker(t *testing.T) {
	v := trustLoopback()
	srv, addr, seen := serveProxied(t, v, MaxConns(2))
	c := dialBalancer(t, addr, append(v2Header(stdnet.ParseIP("203.0.113.7"), 80), "x"...))
	defer func() { swallowErr(c.Close()) }()
	if got := await(t, seen); !strings.HasPrefix(got, "203.0.113.7:80") {
		t.Fatalf("handler saw %q", got)
	}
	_ = srv
}
