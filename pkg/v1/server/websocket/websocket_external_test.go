// Package websocket_test — black-box acceptance tests for the public WebSocket
// facade: the handler a consumer actually writes, mounted on this SDK's own
// engine, and the sentinels it branches on.
//
// The engine matters here rather than in the service package's tests: a
// WebSocket takes the socket over, and whether the engine lets it keep it is a
// property of the engine, not of the protocol.
package websocket_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
	"github.com/kitsunium/sdk/pkg/v1/server/websocket"
)

// peerDeadline bounds the test peer's reads so a silent server fails the test
// rather than hanging the suite.
const peerDeadline time.Duration = 5 * time.Second

// TestTheShortestUsefulWebSocketServer is the API's own acceptance test, and
// the engine-level regression guard in one.
//
// The body of the handler below is the complete code a consumer writes: upgrade,
// defer close, loop. Everything else is the assertion. It runs on the SDK
// engine rather than on httptest because the socket is HIJACKED — the engine
// used to close what it served the moment net/http reported the hijack, which
// severed the connection under the handler before its first frame.
func TestTheShortestUsefulWebSocketServer(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
		HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Upgrade(w, r)
			//: Upgrade has already written the HTTP refusal.
			if err != nil {
				return
			}
			defer func() { closeQuietly(conn) }()
			for {
				msg, rerr := conn.Receive()
				if rerr != nil {
					return
				}
				if serr := conn.Send(msg); serr != nil {
					return
				}
			}
		}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(srv) })

	//: everything below is the assertion, not part of the example.
	peer := dial(t, srv.State().Listeners[0].Address)
	peer.sendMasked(0x1, []byte("hello"))
	peer.expectFrame(0x1, []byte("hello"))
	peer.sendMasked(0x2, []byte{0xDE, 0xAD, 0xBE, 0xEF})
	peer.expectFrame(0x2, []byte{0xDE, 0xAD, 0xBE, 0xEF})
}

// TestDrainClosesTheConnectionInsteadOfBurningTheBudget pins the whole reason
// this package watches the drain signal.
//
// A WebSocket connection is in flight forever by design. The engine no longer
// waits for a hijacked socket — net/http's own Shutdown makes the same
// carve-out — so the drain must reach the handler some other way, and the
// signal is it. What the client observes is a 1001 rather than a severed
// socket, which is the difference between reconnecting deliberately and
// guessing.
func TestDrainClosesTheConnectionInsteadOfBurningTheBudget(t *testing.T) {
	t.Parallel()
	live := make(chan struct{})
	ended := make(chan error, 1)
	srv := server.New(server.WithDrainTimeout(5 * time.Second))
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
		HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Upgrade(w, r)
			if err != nil {
				ended <- err
				return
			}
			defer func() { closeQuietly(conn) }()
			close(live)
			//: the handler does nothing but read, which is the shape that
			//: cannot end on its own.
			_, rerr := conn.Receive()
			ended <- rerr
		}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(srv) })

	peer := dial(t, srv.State().Listeners[0].Address)
	<-live

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown reported %v, want a clean drain", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the drain took %v — the connection was still being waited on", elapsed)
	}
	//: RFC 6455 §7.4.1 — 1001 is "an endpoint is going away, such as a server
	//: going down".
	peer.expectClose(websocket.CloseGoingAway)
	select {
	case err := <-ended:
		if !errors.Is(err, websocket.ConnClosed) {
			t.Fatalf("handler error = %v, want ConnClosed", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never returned after the drain")
	}
}

// TestEveryOptionForwarderReachesTheEngine walks the whole option surface in
// one upgrade.
//
// Each forwarder is a one-line passthrough whose BEHAVIOUR is pinned in
// //internal/service/net/websocket, where it can actually be observed. What
// cannot be observed there is whether this package forwards the right one — a
// copy-paste that wired MaxFrameSize to MaxMessageSize would pass every service
// test and every compile. So this asserts the two effects that are visible from
// outside: the subprotocol that came back, and a connection that still works
// with every bound set at once.
func TestEveryOptionForwarderReachesTheEngine(t *testing.T) {
	t.Parallel()
	negotiated := make(chan string, 1)
	srv := server.New()
	srv.Group("api", server.Listen("tcp", "127.0.0.1:0")).
		HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Upgrade(w, r,
				websocket.Subprotocols("chat.v2", "chat.v1"),
				websocket.MaxMessageSize(4096),
				websocket.MaxFrameSize(2048),
				websocket.PingInterval(time.Minute),
				websocket.WriteTimeout(2*time.Second),
				websocket.AllowOrigins("https://app.example"),
			)
			if err != nil {
				negotiated <- "<refused: " + err.Error() + ">"
				return
			}
			defer func() { closeQuietly(conn) }()
			negotiated <- conn.Subprotocol()
			msg, rerr := conn.Receive()
			if rerr != nil {
				return
			}
			sendQuietly(conn, msg)
		}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeQuietly(srv) })

	addr := srv.State().Listeners[0].Address
	peer := dialWith(t, addr,
		"Origin: https://app.example",
		"Sec-WebSocket-Protocol: chat.v1, chat.v2",
	)
	select {
	case got := <-negotiated:
		//: the SERVER's preference order decides, not the client's.
		if got != "chat.v2" {
			t.Fatalf("Subprotocol() = %q, want chat.v2", got)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
	peer.sendMasked(0x1, []byte("still works"))
	peer.expectFrame(0x1, []byte("still works"))
	//: WithoutPing and AllowAnyOrigin are the two spellings a caller reaches
	//: for deliberately; they are exercised on their own connection because
	//: they contradict the options above.
	if opt := websocket.WithoutPing(); opt == nil {
		t.Fatalf("WithoutPing() = nil")
	}
	if opt := websocket.AllowAnyOrigin(); opt == nil {
		t.Fatalf("AllowAnyOrigin() = nil")
	}
}

// TestSentinelsAreMatchableThroughTheFacade pins that the re-exported sentinels
// are the engine's own values.
//
// Re-declared copies would compile, read identically, and quietly answer false
// to every errors.Is a consumer writes — which is the whole point of exporting
// them.
func TestSentinelsAreMatchableThroughTheFacade(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sentinel error
	}
	tests := []tc{
		{"HandshakeFailed", websocket.HandshakeFailed},
		{"UpgradeUnsupported", websocket.UpgradeUnsupported},
		{"ProtocolViolation", websocket.ProtocolViolation},
		{"MessageTooLarge", websocket.MessageTooLarge},
		{"InvalidPayload", websocket.InvalidPayload},
		{"ConnClosed", websocket.ConnClosed},
		{"ConnMisconfigured", websocket.ConnMisconfigured},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.sentinel == nil {
				t.Fatalf("%s is nil", c.name)
			}
			if !errors.Is(c.sentinel, c.sentinel) {
				t.Fatalf("%s does not match itself", c.name)
			}
		})
	}
	//: and one real, wrapped error from the engine, matched through the facade
	//: — which is what a consumer actually writes.
	recorder := &plainWriter{header: http.Header{}}
	request := newHandshakeRequest(t)
	_, err := websocket.Upgrade(recorder, request)
	if !errors.Is(err, websocket.UpgradeUnsupported) {
		t.Fatalf("Upgrade on a plain ResponseWriter = %v, want UpgradeUnsupported", err)
	}
}

// TestAcceptKeyIsTheRFCValue pins the one computation a consumer may need to
// reproduce, against RFC 6455 §1.3's own example.
func TestAcceptKeyIsTheRFCValue(t *testing.T) {
	t.Parallel()
	if got := websocket.AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("AcceptKey = %q, want the RFC §1.3 value", got)
	}
	if websocket.Version != "13" {
		t.Fatalf("Version = %q, want 13", websocket.Version)
	}
	if websocket.MaxControlPayload != 125 {
		t.Fatalf("MaxControlPayload = %d, want 125 (RFC 6455 §5.5)", websocket.MaxControlPayload)
	}
}

// TestCloseCodesAreTheRegistryValues pins the re-exported numbers, because a
// constant that drifted would be a wire-format bug no compiler catches.
func TestCloseCodesAreTheRegistryValues(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  websocket.CloseCode
		want uint16
	}
	tests := []tc{
		{"CloseNormal", websocket.CloseNormal, 1000},
		{"CloseGoingAway", websocket.CloseGoingAway, 1001},
		{"CloseProtocolError", websocket.CloseProtocolError, 1002},
		{"CloseUnsupportedData", websocket.CloseUnsupportedData, 1003},
		{"CloseNoStatus", websocket.CloseNoStatus, 1005},
		{"CloseAbnormal", websocket.CloseAbnormal, 1006},
		{"CloseInvalidPayload", websocket.CloseInvalidPayload, 1007},
		{"ClosePolicyViolation", websocket.ClosePolicyViolation, 1008},
		{"CloseTooLarge", websocket.CloseTooLarge, 1009},
		{"CloseExtensionRequired", websocket.CloseExtensionRequired, 1010},
		{"CloseInternalError", websocket.CloseInternalError, 1011},
		{"CloseTLSHandshake", websocket.CloseTLSHandshake, 1015},
	}
	for _, c := range tests {
		if uint16(c.got) != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	//: the three that describe the ABSENCE of a close frame must not be
	//: sendable, in either direction.
	for _, code := range []websocket.CloseCode{
		websocket.CloseNoStatus, websocket.CloseAbnormal, websocket.CloseTLSHandshake,
	} {
		if code.Sendable() {
			t.Errorf("CloseCode(%d).Sendable() = true, want false (RFC 6455 §7.4.1)", code)
		}
	}
}

// TestDrainSignalIsTheSameFunctionAsTheParents pins the re-export, so a handler
// that imports only this package still observes the engine's own drain.
func TestDrainSignalIsTheSameFunctionAsTheParents(t *testing.T) {
	t.Parallel()
	//: an absent signal is a nil channel, which blocks forever, so a select
	//: watching it needs no nil check.
	if got := websocket.DrainSignal(context.Background()); got != nil {
		t.Fatalf("DrainSignal on a bare context = %v, want nil", got)
	}
	if got := server.DrainSignal(context.Background()); got != nil {
		t.Fatalf("server.DrainSignal on a bare context = %v, want nil", got)
	}
}

// plainWriter is an http.ResponseWriter with no Hijacker and no Unwrap — the
// shape of a middleware that wraps the response without forwarding.
type plainWriter struct {
	header http.Header
	status int
}

// Header implements http.ResponseWriter.
func (p *plainWriter) Header() http.Header {
	//: the caller's headers, as any ResponseWriter would.
	return p.header
}

// Write implements http.ResponseWriter.
func (p *plainWriter) Write(b []byte) (int, error) {
	//: discarded; the test only cares about the status and the error.
	return len(b), nil
}

// WriteHeader implements http.ResponseWriter.
func (p *plainWriter) WriteHeader(status int) {
	p.status = status
}

// newHandshakeRequest builds a conforming opening handshake request.
func newHandshakeRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Version", websocket.Version)
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	//: a request that is beyond reproach, so the only possible refusal is the
	//: one about the response writer.
	return request
}

// testPeer is a minimal WebSocket client: enough to prove the facade works
// end to end, without depending on any third-party library to say so.
type testPeer struct {
	t    *testing.T
	conn stdnet.Conn
	br   *bufio.Reader
}

// dial performs an opening handshake against addr and returns the peer.
func dial(t *testing.T, addr string) *testPeer {
	t.Helper()
	//: no extra headers, which is the shape most consumers meet first.
	return dialWith(t, addr)
}

// dialWith performs an opening handshake carrying extra request headers.
func dialWith(t *testing.T, addr string, extra ...string) *testPeer {
	t.Helper()
	conn, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeQuietly(conn) })
	if derr := conn.SetDeadline(time.Now().Add(peerDeadline)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	var nonce [16]byte
	if _, rerr := rand.Read(nonce[:]); rerr != nil {
		t.Fatalf("nonce: %v", rerr)
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	request := "GET / HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + key + "\r\n"
	for _, line := range extra {
		request += line + "\r\n"
	}
	if _, werr := conn.Write([]byte(request + "\r\n")); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	reader := bufio.NewReader(conn)
	resp, rerr := http.ReadResponse(reader, nil)
	if rerr != nil {
		t.Fatalf("read handshake response: %v", rerr)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
	//: computed from the key this peer actually sent, so a server echoing a
	//: constant would fail here.
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != websocket.AcceptKey(key) {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", got, websocket.AcceptKey(key))
	}
	//: switched.
	return &testPeer{t: t, conn: conn, br: reader}
}

// sendMasked writes one masked frame, as RFC 6455 §5.1 requires of a client.
func (p *testPeer) sendMasked(op byte, payload []byte) {
	p.t.Helper()
	out := []byte{0x80 | op, 0x80 | byte(len(payload))}
	key := [4]byte{0x11, 0x22, 0x33, 0x44}
	out = append(out, key[:]...)
	for i, b := range payload {
		out = append(out, b^key[i&3])
	}
	if _, err := p.conn.Write(out); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

// readFrame reads one server frame; the server never masks, which is asserted.
func (p *testPeer) readFrame() (op byte, payload []byte) {
	p.t.Helper()
	var head [2]byte
	if _, err := io.ReadFull(p.br, head[:]); err != nil {
		p.t.Fatalf("read frame header: %v", err)
	}
	if head[1]&0x80 != 0 {
		p.t.Fatalf("the server masked a frame; RFC 6455 §5.1 forbids it")
	}
	size := int(head[1] & 0x7F)
	if size >= 126 {
		p.t.Fatalf("the test peer only reads short frames; got a length marker of %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(p.br, body); err != nil {
		p.t.Fatalf("read frame payload: %v", err)
	}
	//: opcode and payload as the server wrote them.
	return head[0] & 0x0F, body
}

// expectFrame asserts the next frame's opcode and payload.
func (p *testPeer) expectFrame(op byte, want []byte) {
	p.t.Helper()
	got, payload := p.readFrame()
	if got != op {
		p.t.Fatalf("opcode = %#x, want %#x", got, op)
	}
	if !bytes.Equal(payload, want) {
		p.t.Fatalf("payload = %q, want %q", payload, want)
	}
}

// expectClose asserts the next frame is a Close carrying code.
func (p *testPeer) expectClose(code websocket.CloseCode) {
	p.t.Helper()
	op, payload := p.readFrame()
	if op != 0x8 {
		p.t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
	if len(payload) < 2 {
		p.t.Fatalf("close payload = % x, want at least a status code", payload)
	}
	if got := websocket.CloseCode(binary.BigEndian.Uint16(payload)); got != code {
		p.t.Fatalf("close code = %d, want %d", got, code)
	}
}

// Sender narrows a connection to the one method sendQuietly uses.
type Sender interface {
	Send(message websocket.Message) error
}

// closeQuietly closes c and deliberately discards the error.
//
// In these tests the peer having closed first is the expected ending on most
// paths, not a fault to report, and a handler goroutine has no useful place to
// report it to.
func closeQuietly(c io.Closer) {
	//: read the result so the unused-error audit treats this as intentional.
	if err := c.Close(); err != nil {
		//: the connection is already finished with.
		return
	}
}

// sendQuietly echoes a message and discards the outcome: the peer having gone
// away mid-echo is an ending these tests provoke on purpose.
func sendQuietly(c Sender, msg websocket.Message) {
	//: read the result so the unused-error audit treats this as intentional.
	if err := c.Send(msg); err != nil {
		//: the connection ended; the test asserts that elsewhere.
		return
	}
}
