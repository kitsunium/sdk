package websocket_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	stdnet "net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// xconnectRole marks the child the RFC 8441 tests re-run in: net/http reads
// GODEBUG=http2xconnect=1 at its init, so a test cannot turn extended CONNECT
// on for itself. The child runs the tests and never starts another.
const xconnectRole string = "SDK_WEBSOCKET_TEST_XCONNECT"

// xconnectLifetime bounds the child, whatever becomes of the test.
const xconnectLifetime time.Duration = 120 * time.Second

// h2ServerName is the name the test identity is issued to.
const h2ServerName string = "ws-h2-test"

// TestExtendedConnect runs the RFC 8441 tests (xconnect*) in a child of the
// test binary with GODEBUG=http2xconnect=1, the role variable removed from the
// inherited environment before it is set, so the child cannot start another.
func TestExtendedConnect(t *testing.T) {
	if os.Getenv(xconnectRole) != "" {
		t.Skip("the child runs the xconnect tests themselves")
	}
	ctx, cancel := context.WithTimeout(t.Context(), xconnectLifetime)
	defer cancel()
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, xconnectRole+"=") || strings.HasPrefix(kv, "GODEBUG=")
	})
	godebug := "http2xconnect=1"
	if prior := os.Getenv("GODEBUG"); prior != "" {
		godebug = prior + "," + godebug
	}
	env = append(env, xconnectRole+"=1", "GODEBUG="+godebug)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestXConnect", "-test.v", "-test.count=1")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestXConnectEcho") {
		t.Fatalf("the xconnect child: %v\n%s", err, out)
	}
	for _, name := range []string{"TestXConnectEcho", "TestXConnectAdversarial", "TestXConnectDrain"} {
		if !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("the child did not pass %s:\n%s", name, out)
		}
	}
}

// inXConnectChild skips a test outside the child.
func inXConnectChild(t *testing.T) {
	t.Helper()
	if os.Getenv(xconnectRole) == "" {
		t.Skip("run by TestExtendedConnect, in a child with GODEBUG=http2xconnect=1")
	}
}

// h2Identity is a self-signed identity and the pool that trusts it.
func h2Identity(t *testing.T) (corenet.IdentityValue, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: h2ServerName}, DNSNames: []string{h2ServerName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	})
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return id, pool
}

// h2Group serves h on a TLS group with Protocols("h2") and returns the server
// and its address.
func h2Group(t *testing.T, h http.Handler) (*server.Server, string, *x509.CertPool) {
	t.Helper()
	id, pool := h2Identity(t)
	srv := server.New()
	srv.Group("ws", server.Listen("tcp", "127.0.0.1:0"), server.TLS(id), server.Protocols("h2")).HandleHTTP(h)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(srv) })
	return srv, srv.State().Listeners[0].Address, pool
}

// h2WS is a raw HTTP/2 client on one TLS connection, one stream at a time:
// what a Go http.Transport will not send — net/http's client refuses a
// :protocol field before HTTP/2 is chosen — and, as the test peer's
// connection, an extended CONNECT stream: Write sends DATA, Read returns the
// stream's DATA, crediting it back, until END_STREAM or RST_STREAM.
type h2WS struct {
	stdnet.Conn
	t        *testing.T
	c        *tls.Conn
	br       *bufio.Reader
	stream   uint32
	pending  []byte
	ended    bool
	xconnect bool
}

// dialRawH2 handshakes h2 with addr, exchanges SETTINGS and records whether
// the server announced SETTINGS_ENABLE_CONNECT_PROTOCOL.
func dialRawH2(t *testing.T, addr string, pool *x509.CertPool) *h2WS {
	t.Helper()
	d := &tls.Dialer{NetDialer: &stdnet.Dialer{Timeout: peerDeadline}, Config: &tls.Config{RootCAs: pool, ServerName: h2ServerName, NextProtos: []string{"h2"}}}
	nc, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c, _ := nc.(*tls.Conn)
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	h := &h2WS{t: t, c: c, br: bufio.NewReader(c)}
	if _, err := io.WriteString(c, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	h.frame(0x4, 0, 0, nil)
	for {
		typ, flags, _, payload, err := h.readFrame()
		if err != nil {
			t.Fatalf("server SETTINGS: %v", err)
		}
		if typ != 0x4 || flags&0x1 != 0 {
			continue
		}
		for k := 0; k+6 <= len(payload); k += 6 {
			if binary.BigEndian.Uint16(payload[k:]) == 8 && binary.BigEndian.Uint32(payload[k+2:]) == 1 {
				h.xconnect = true
			}
		}
		h.frame(0x4, 0x1, 0, nil)
		return h
	}
}

// frame writes one frame.
func (h *h2WS) frame(typ, flags byte, stream uint32, payload []byte) {
	var hdr [9]byte
	hdr[0], hdr[1], hdr[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	hdr[3], hdr[4] = typ, flags
	binary.BigEndian.PutUint32(hdr[5:], stream)
	if _, err := h.c.Write(append(hdr[:], payload...)); err != nil {
		h.t.Logf("frame %d: %v", typ, err)
	}
}

// readFrame reads one frame.
func (h *h2WS) readFrame() (typ, flags byte, stream uint32, payload []byte, err error) {
	var hdr [9]byte
	if _, err = io.ReadFull(h.br, hdr[:]); err != nil {
		return 0, 0, 0, nil, err
	}
	n := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
	payload = make([]byte, n)
	if _, err = io.ReadFull(h.br, payload); err != nil {
		return 0, 0, 0, nil, err
	}
	return hdr[3], hdr[4], binary.BigEndian.Uint32(hdr[5:]) & 0x7fffffff, payload, nil
}

// literal is an HPACK literal field without indexing, both strings plain.
func literal(name, value string) []byte {
	b := append([]byte{0x00, byte(len(name))}, name...)
	b = append(b, byte(len(value)))
	return append(b, value...)
}

// request opens stream with method and the given extra fields, END_HEADERS
// set, END_STREAM not: the stream stays open for DATA.
func (h *h2WS) request(stream uint32, method, authority string, fields ...string) {
	block := literal(":method", method)
	block = append(block, literal(":scheme", "https")...)
	block = append(block, literal(":path", "/")...)
	block = append(block, literal(":authority", authority)...)
	for k := 0; k+1 < len(fields); k += 2 {
		block = append(block, literal(fields[k], fields[k+1])...)
	}
	h.stream, h.ended, h.pending = stream, false, nil
	h.frame(0x1, 0x4, stream, block)
}

// response reads to the stream's HEADERS, returning its field block, or nil
// when the stream was reset first.
func (h *h2WS) response() []byte {
	for {
		typ, _, stream, payload, err := h.readFrame()
		if err != nil {
			h.t.Fatalf("response: %v", err)
		}
		switch {
		case stream != h.stream:
		case typ == 0x1:
			return payload
		case typ == 0x3:
			return nil
		}
	}
}

// statusOK reports whether a response block is :status 200: the static
// table's entry 8, indexed, which net/http's encoder writes first.
func statusOK(block []byte) bool {
	return len(block) > 0 && block[0] == 0x88
}

func (h *h2WS) Write(p []byte) (int, error) {
	h.frame(0x0, 0, h.stream, p)
	return len(p), nil
}

func (h *h2WS) Read(p []byte) (int, error) {
	for len(h.pending) == 0 {
		if h.ended {
			return 0, io.EOF
		}
		typ, flags, stream, payload, err := h.readFrame()
		if err != nil {
			return 0, err
		}
		//: a GOAWAY spares the streams it names, this one included: it is
		//: the stream's own END_STREAM or RST_STREAM that ends it, the first
		//: read as io.EOF, the second as a streamReset: a reset is not an
		//: orderly end.
		switch {
		case stream != h.stream:
		case typ == 0x3:
			code := uint32(0)
			if len(payload) >= 4 {
				code = binary.BigEndian.Uint32(payload)
			}
			return 0, streamReset{code: code}
		case typ == 0x1:
			// Trailers: END_STREAM on a HEADERS frame ends the stream too.
			h.ended = flags&0x1 != 0
		case typ == 0x0:
			if len(payload) > 0 {
				var inc [4]byte
				binary.BigEndian.PutUint32(inc[:], uint32(len(payload)))
				h.frame(0x8, 0, 0, inc[:])
				h.frame(0x8, 0, h.stream, inc[:])
			}
			h.pending, h.ended = payload, flags&0x1 != 0
		}
	}
	n := copy(p, h.pending)
	h.pending = h.pending[n:]
	return n, nil
}

// streamReset is a stream ended by RST_STREAM, with its error code.
type streamReset struct{ code uint32 }

func (e streamReset) Error() string { return fmt.Sprintf("RST_STREAM, error code %d", e.code) }

// expectEndStream reads past the close frame and fails unless the stream
// ended with END_STREAM: a reset, a connection lost or a frame more is not
// the orderly end RFC 8441 §5 gives a closed WebSocket.
func expectEndStream(t *testing.T, peer *wsPeer, after string) {
	t.Helper()
	_, op, _, err := peer.readFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("after %s the stream read op %#x, %v; want END_STREAM (io.EOF)", after, op, err)
	}
}

func (h *h2WS) SetDeadline(time.Time) error { return nil }

func (h *h2WS) Close() error { return h.c.Close() }

// dialH2WS opens an extended CONNECT for protocol on a fresh connection and
// returns whether it was answered 200 and, when it was, the peer over it.
func dialH2WS(t *testing.T, addr string, pool *x509.CertPool, protocol string) (bool, *wsPeer) {
	t.Helper()
	h := dialRawH2(t, addr, pool)
	if !h.xconnect {
		t.Fatal("the server did not announce SETTINGS_ENABLE_CONNECT_PROTOCOL")
	}
	h.request(1, http.MethodConnect, h2ServerName, ":protocol", protocol, "sec-websocket-version", "13")
	block := h.response()
	if !statusOK(block) {
		return false, nil
	}
	return true, &wsPeer{t: t, conn: h, br: bufio.NewReader(h)}
}

// TestXConnectEcho is #967's first criterion's exchange: a Go client opens
// the extended CONNECT, is answered 200 — never 101 —, and 1 000 masked text
// frames are echoed; a CONNECT naming another protocol opens no tunnel.
func TestXConnectEcho(t *testing.T) {
	inXConnectChild(t)
	ended := make(chan error, 4)
	_, addr, pool := h2Group(t, echoHandler(ended))
	ok, peer := dialH2WS(t, addr, pool, "websocket")
	if !ok {
		t.Fatal("extended CONNECT was not answered 200")
	}
	for i := range 1000 {
		msg := []byte("message " + strings.Repeat("x", i%64))
		peer.send(0x1, true, msg)
		peer.expectMessage(0x1, msg)
	}
	peer.send(0x8, true, []byte{0x03, 0xe8})
	peer.expectClose(1000)
	if ok, _ := dialH2WS(t, addr, pool, "connect-udp"); ok {
		t.Fatal("a CONNECT naming connect-udp was answered 200: a tunnel outside WebSocket")
	}
}

// TestXConnectAdversarial replays TestAdversarialFramesFailTheConnection's
// table over HTTP/2 streams: each hostile frame is answered with its close
// code and the stream ends.
func TestXConnectAdversarial(t *testing.T) {
	inXConnectChild(t)
	ended := make(chan error, 64)
	_, addr, pool := h2Group(t, echoHandler(ended))
	for _, c := range adversarialTable() {
		t.Run(c.name, func(t *testing.T) {
			_, peer := dialH2WS(t, addr, pool, "websocket")
			peer.t = t
			c.write(peer)
			peer.expectClose(c.code)
			expectEndStream(t, peer, "a "+c.name+" ("+c.why+")")
		})
	}
}

// TestXConnectDrain is #967's third criterion: the server's drain sends 1001
// on an open h2 WebSocket, and the stream ends, the client reading the close
// frame then EOF within the grace.
func TestXConnectDrain(t *testing.T) {
	inXConnectChild(t)
	ended := make(chan error, 4)
	srv, addr, pool := h2Group(t, echoHandler(ended))
	_, peer := dialH2WS(t, addr, pool, "websocket")
	peer.send(0x1, true, []byte("before"))
	peer.expectMessage(0x1, []byte("before"))
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- srv.Shutdown(ctx)
	}()
	peer.expectClose(1001)
	peer.send(0x8, true, []byte{0x03, 0xe9})
	expectEndStream(t, peer, "the drain's close handshake")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the drain did not end")
	}
}

// TestXConnectRefusedWithoutGODEBUG is #967's second criterion in the default
// binary: net/http does not announce extended CONNECT, refuses one sent all
// the same with RST_STREAM before the handler, and an HTTP/2 GET to the
// WebSocket handler is answered 426; no 101 is ever written on HTTP/2.
func TestXConnectRefusedWithoutGODEBUG(t *testing.T) {
	if os.Getenv(xconnectRole) != "" {
		t.Skip("the default binary's behaviour")
	}
	_, addr, pool := h2Group(t, echoHandler(make(chan error, 4)))
	h := dialRawH2(t, addr, pool)
	if h.xconnect {
		t.Fatal("SETTINGS_ENABLE_CONNECT_PROTOCOL announced without GODEBUG")
	}
	h.request(1, http.MethodConnect, h2ServerName, ":protocol", "websocket", "sec-websocket-version", "13")
	if block := h.response(); block != nil {
		t.Fatalf("an extended CONNECT without GODEBUG was answered: % x", block)
	}
	h.request(3, http.MethodGet, h2ServerName, "sec-websocket-version", "13")
	block := h.response()
	if block == nil || statusOK(block) || strings.Contains(string(block), "101") || !strings.Contains(string(block), "426") {
		t.Fatalf("an HTTP/2 GET to the WebSocket handler: % x, want 426", block)
	}
}
