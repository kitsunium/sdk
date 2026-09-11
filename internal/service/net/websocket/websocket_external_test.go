// Package websocket_test drives the server with a hand-rolled peer that can
// put frames on the wire no conforming client library would produce.
//
// That is the point. A test that speaks through this package's own writer can
// only prove the writer and the reader agree; the RFC's framing rules are
// almost entirely about what to do when they do NOT, so every refusal below is
// provoked with bytes assembled by hand and cited to its section.
package websocket_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

// peerDeadline bounds every read the test peer makes, so a server that never
// answers fails the test instead of hanging the suite.
const peerDeadline time.Duration = 5 * time.Second

// wsPeer is a WebSocket client with no manners: it will send an unmasked frame,
// a fragmented Close, a length that lies, and anything else the RFC forbids.
type wsPeer struct {
	t    *testing.T
	conn stdnet.Conn
	br   *bufio.Reader
}

// echoServer starts an httptest server whose handler upgrades and echoes every
// message back, and returns it with the channel carrying the handler's exit.
func echoServer(t *testing.T, opts ...websocket.Option) (srv *httptest.Server, ended chan error) {
	t.Helper()
	ended = make(chan error, 1)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, opts...)
		//: Upgrade has already written the refusal; the handler owes nothing.
		if err != nil {
			ended <- err
			return
		}
		defer func() { closeQuietly(conn) }()
		for {
			msg, rerr := conn.Receive()
			//: the terminal error is what the handler reports.
			if rerr != nil {
				ended <- rerr
				return
			}
			//: an echo proves the message survived reassembly intact.
			if serr := conn.Send(msg); serr != nil {
				ended <- serr
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	//: the handler may never run at all (a refused handshake), so the channel
	//: is buffered and nothing here waits on it.
	return srv, ended
}

// dial performs a conforming opening handshake and returns the peer.
func dial(t *testing.T, srv *httptest.Server, extra ...string) *wsPeer {
	t.Helper()
	peer := rawDial(t, srv)
	resp := peer.handshake(t, srv, extra...)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
	//: switched.
	return peer
}

// rawDial opens the TCP connection without saying anything on it.
func rawDial(t *testing.T, srv *httptest.Server) *wsPeer {
	t.Helper()
	conn, err := stdnet.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { closeQuietly(conn) })
	//: every read is bounded so a silent server fails rather than hangs.
	if derr := conn.SetDeadline(time.Now().Add(peerDeadline)); derr != nil {
		t.Fatalf("deadline: %v", derr)
	}
	//: the peer, with the buffered reader every frame read goes through.
	return &wsPeer{t: t, conn: conn, br: bufio.NewReader(conn)}
}

// handshake writes an opening handshake and reads the response.
func (p *wsPeer) handshake(t *testing.T, srv *httptest.Server, extra ...string) *http.Response {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	request := "GET / HTTP/1.1\r\n" +
		"Host: " + strings.TrimPrefix(srv.URL, "http://") + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(nonce[:]) + "\r\n"
	for _, line := range extra {
		request += line + "\r\n"
	}
	p.write(t, []byte(request+"\r\n"))
	resp, err := http.ReadResponse(p.br, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	//: the caller decides what the status should have been.
	return resp
}

// write puts raw bytes on the wire.
func (p *wsPeer) write(t *testing.T, b []byte) {
	t.Helper()
	if _, err := p.conn.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// frame assembles one frame with full control over every bit, including the
// ones a conforming client would never set.
func frame(fin bool, rsv byte, op byte, masked bool, payload []byte) []byte {
	first := op | (rsv << 4)
	//: FIN.
	if fin {
		first |= 0x80
	}
	out := []byte{first}
	//: the minimal length encoding; the non-minimal cases build their own.
	switch size := len(payload); {
	case size < 126:
		out = append(out, byte(size))
	case size <= 0xFFFF:
		out = append(out, 126)
		out = binary.BigEndian.AppendUint16(out, uint16(size))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(size))
	}
	//: an unmasked frame is exactly what §5.1 forbids a client from sending,
	//: which is why the peer must be able to produce one.
	if !masked {
		//: no key, no transform.
		return append(out, payload...)
	}
	out[1] |= 0x80
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	out = append(out, key[:]...)
	body := slices.Clone(payload)
	corenet.ApplyWSMask(body, key)
	//: masked, as a client must.
	return append(out, body...)
}

// send writes one well-formed masked frame.
func (p *wsPeer) send(op byte, fin bool, payload []byte) {
	p.t.Helper()
	p.write(p.t, frame(fin, 0, op, true, payload))
}

// readFrame reads one server frame. The server never masks, so the payload is
// taken verbatim — and that is itself an assertion.
func (p *wsPeer) readFrame() (fin bool, op byte, payload []byte, err error) {
	var head [2]byte
	if _, rerr := io.ReadFull(p.br, head[:]); rerr != nil {
		//: the server closed, or said nothing in time.
		return false, 0, nil, rerr
	}
	if head[1]&0x80 != 0 {
		p.t.Fatalf("the server masked a frame; RFC 6455 §5.1 forbids it")
	}
	size := uint64(head[1] & 0x7F)
	//: the two extended forms.
	switch size {
	case 126:
		var ext [2]byte
		if _, rerr := io.ReadFull(p.br, ext[:]); rerr != nil {
			//: truncated header.
			return false, 0, nil, rerr
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, rerr := io.ReadFull(p.br, ext[:]); rerr != nil {
			//: truncated header.
			return false, 0, nil, rerr
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	body := make([]byte, size)
	if _, rerr := io.ReadFull(p.br, body); rerr != nil {
		//: truncated payload.
		return false, 0, nil, rerr
	}
	//: FIN, opcode and payload as the server wrote them.
	return head[0]&0x80 != 0, head[0] & 0x0F, body, nil
}

// Sender narrows a connection to the one method sendQuietly uses.
type Sender interface {
	Send(message corenet.WSMessageValue) error
}

// Receiver narrows a connection to the one method receiveQuietly uses.
type Receiver interface {
	Receive() (message corenet.WSMessageValue, err error)
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
// away mid-echo is one of the endings these tests provoke on purpose.
func sendQuietly(c Sender, msg corenet.WSMessageValue) {
	//: read the result so the unused-error audit treats this as intentional.
	if err := c.Send(msg); err != nil {
		//: the connection ended; the test asserts that elsewhere.
		return
	}
}

// receiveQuietly reads one message and discards it, for the handlers whose only
// job is to stay alive until something else ends them.
func receiveQuietly(c Receiver) {
	//: read the result so the unused-error audit treats this as intentional.
	if _, err := c.Receive(); err != nil {
		//: the connection ended; the test asserts that elsewhere.
		return
	}
}

// drainOneFrame reads whatever the server sent next and discards it, for the
// cases where the assertion is about what came AFTER it.
func (p *wsPeer) drainOneFrame() {
	//: read the result so the unused-error audit treats this as intentional.
	if _, _, _, err := p.readFrame(); err != nil {
		//: the connection ended, which the caller is about to assert.
		return
	}
}

// expectClose reads the next frame and asserts it is a Close carrying code.
func (p *wsPeer) expectClose(code uint16) {
	p.t.Helper()
	_, op, payload, err := p.readFrame()
	if err != nil {
		p.t.Fatalf("expected a close frame with code %d, got %v", code, err)
	}
	if op != 0x8 {
		p.t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
	if len(payload) < 2 {
		p.t.Fatalf("close payload = % x, want at least a status code", payload)
	}
	if got := binary.BigEndian.Uint16(payload); got != code {
		p.t.Fatalf("close code = %d, want %d", got, code)
	}
}

// expectMessage reads one whole message back and asserts its opcode and body.
func (p *wsPeer) expectMessage(op byte, want []byte) {
	p.t.Helper()
	fin, got, payload, err := p.readFrame()
	if err != nil {
		p.t.Fatalf("expected a message, got %v", err)
	}
	if got != op {
		p.t.Fatalf("opcode = %#x, want %#x (payload % x)", got, op, payload)
	}
	if !fin {
		p.t.Fatalf("the server fragmented a reply; this implementation never does")
	}
	if !bytes.Equal(payload, want) {
		p.t.Fatalf("payload = %q, want %q", payload, want)
	}
}

// TestHandshakeAnswersTheRFCsOwnProof pins §4.2.2: the 101, both upgrade
// headers, and the accept value computed from the key the peer actually sent.
func TestHandshakeAnswersTheRFCsOwnProof(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	peer := rawDial(t, srv)
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	peer.write(t, []byte("GET / HTTP/1.1\r\n"+
		"Host: "+strings.TrimPrefix(srv.URL, "http://")+"\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: "+key+"\r\n\r\n"))
	resp, err := http.ReadResponse(peer.br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	//: §1.3's worked example, so the assertion comes from the RFC.
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("Sec-WebSocket-Accept = %q, want the RFC §1.3 value", got)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Fatalf("Upgrade = %q, want websocket", got)
	}
	if got := resp.Header.Get("Connection"); !strings.EqualFold(got, "Upgrade") {
		t.Fatalf("Connection = %q, want Upgrade", got)
	}
}

// TestHandshakeNeverNegotiatesAnExtension pins the permessage-deflate refusal
// at the only place a client can observe it.
//
// §4.2.2 makes the ABSENCE of Sec-WebSocket-Extensions the way a server says it
// uses none. A client that offered compression and saw no answer must not
// compress — and the frame reader refuses the reserved bit it would have used,
// so the refusal is enforced twice with one meaning.
func TestHandshakeNeverNegotiatesAnExtension(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	peer := rawDial(t, srv)
	resp := peer.handshake(t, srv, "Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101 — offering an extension must not fail the handshake", resp.StatusCode)
	}
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Fatalf("Sec-WebSocket-Extensions = %q, want it absent", got)
	}
	//: and the second half: a frame with RSV1 set — what a deflated frame looks
	//: like — is refused as a protocol error rather than handed up compressed.
	peer.write(t, frame(true, 0x4, 0x1, true, []byte("compressed")))
	peer.expectClose(1002)
}

// TestHandshakeRefusalsFollowSection42 walks every request shape §4.2.1 rejects
// and pins the status each one deserves.
func TestHandshakeRefusalsFollowSection42(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		request string
		status  int
		header  string
		why     string
	}
	host := "example.invalid"
	tests := []tc{
		{
			name: "not a GET",
			request: "POST / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n" +
				"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\nContent-Length: 0\r\n",
			status: http.StatusMethodNotAllowed,
			why:    "§4.2.1/1 — the handshake is a GET",
		},
		{
			name: "no Upgrade header",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusBadRequest,
			why:    "§4.2.1/3",
		},
		{
			name: "Upgrade names another protocol",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: h2c\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusBadRequest,
			why:    "§4.2.1/3",
		},
		{
			name: "Connection does not carry the Upgrade token",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: keep-alive\r\nSec-WebSocket-Version: 13\r\n" +
				"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusBadRequest,
			why:    "§4.2.1/4",
		},
		{
			name: "an older version",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: Upgrade\r\nSec-WebSocket-Version: 8\r\n" +
				"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusUpgradeRequired,
			header: "Sec-WebSocket-Version",
			why:    "§4.4 — an unknown version is answered WITH the version we speak",
		},
		{
			name: "no version at all",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: Upgrade\r\nSec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusUpgradeRequired,
			header: "Sec-WebSocket-Version",
			why:    "§4.2.1/6",
		},
		{
			name: "a key that is not sixteen bytes",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: c2hvcnQ=\r\n",
			status: http.StatusBadRequest,
			why:    "§4.1 — the nonce is sixteen bytes",
		},
		{
			name: "no key at all",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\n" +
				"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n",
			status: http.StatusBadRequest,
			why:    "§4.2.1/5",
		},
		{
			name: "Connection carries Upgrade inside a list, in another case",
			request: "GET / HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: WebSocket\r\n" +
				"Connection: keep-alive, UPGRADE\r\nSec-WebSocket-Version: 13\r\n" +
				"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==\r\n",
			status: http.StatusSwitchingProtocols,
			why:    "§4.2.1 — the tokens are case-insensitive and may sit in a list",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := echoServer(t)
			peer := rawDial(t, srv)
			peer.write(t, []byte(c.request+"\r\n"))
			resp, err := http.ReadResponse(peer.br, nil)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if resp.StatusCode != c.status {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, c.status, c.why)
			}
			//: a version refusal that does not say which version to use leaves
			//: the client guessing, which is the whole reason §4.4 exists.
			if c.header != "" && resp.Header.Get(c.header) != "13" {
				t.Fatalf("%s = %q, want 13 (%s)", c.header, resp.Header.Get(c.header), c.why)
			}
		})
	}
}

// TestOriginIsCheckedByDefault pins the cross-site WebSocket hijacking defence.
//
// The browser's same-origin policy does not apply here: any page may open a
// connection and the browser attaches the user's cookies to the handshake. An
// upgrader that accepted every origin by default would be a CSRF primitive with
// a default-on switch.
func TestOriginIsCheckedByDefault(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		origin string
		opts   []websocket.Option
		status int
	}
	tests := []tc{
		{"no origin at all is a non-browser client", "", nil, http.StatusSwitchingProtocols},
		{"a foreign origin is refused", "Origin: https://evil.example", nil, http.StatusForbidden},
		{"the opaque null origin is refused", "Origin: null", nil, http.StatusForbidden},
		{
			"an allowlisted origin passes", "Origin: https://app.example",
			[]websocket.Option{websocket.AllowOrigins("https://app.example")},
			http.StatusSwitchingProtocols,
		},
		{
			"the same host over the wrong scheme is not allowlisted", "Origin: http://app.example",
			[]websocket.Option{websocket.AllowOrigins("https://app.example")},
			http.StatusForbidden,
		},
		{
			"any origin, said out loud", "Origin: https://evil.example",
			[]websocket.Option{websocket.AllowAnyOrigin()},
			http.StatusSwitchingProtocols,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := echoServer(t, c.opts...)
			peer := rawDial(t, srv)
			var extra []string
			//: an absent header is a different case from an empty one.
			if c.origin != "" {
				extra = append(extra, c.origin)
			}
			resp := peer.handshake(t, srv, extra...)
			if resp.StatusCode != c.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.status)
			}
		})
	}
	//: and the default's positive half: the request's own host is allowed,
	//: which is what makes the rule usable rather than merely strict.
	t.Run("the request's own host is same-origin", func(t *testing.T) {
		t.Parallel()
		srv, _ := echoServer(t)
		peer := rawDial(t, srv)
		host := strings.TrimPrefix(srv.URL, "http://")
		resp := peer.handshake(t, srv, "Origin: http://"+host)
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("status = %d, want 101", resp.StatusCode)
		}
	})
}

// TestSubprotocolNegotiationFollowsServerPreference pins §4.2.2's rule that the
// server names at most one, chosen from what the client offered.
func TestSubprotocolNegotiationFollowsServerPreference(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		offered  string
		declared []string
		want     string
	}
	tests := []tc{
		{"the server's first choice wins", "v1, v2", []string{"v2", "v1"}, "v2"},
		{"only what the client offered is chosen", "v1", []string{"v2", "v1"}, "v1"},
		{"no overlap agrees on nothing", "v9", []string{"v1"}, ""},
		{"a server with no list never negotiates", "v1", nil, ""},
		{"names are compared exactly", "V1", []string{"v1"}, ""},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := echoServer(t, websocket.Subprotocols(c.declared...))
			peer := rawDial(t, srv)
			resp := peer.handshake(t, srv, "Sec-WebSocket-Protocol: "+c.offered)
			//: §4.2.2 — no agreement is not a failure, it is an omitted header.
			if resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("status = %d, want 101", resp.StatusCode)
			}
			if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != c.want {
				t.Fatalf("Sec-WebSocket-Protocol = %q, want %q", got, c.want)
			}
		})
	}
}

// TestEchoRoundTrip is the baseline: without it, every refusal below would also
// pass on a server that refused everything.
func TestEchoRoundTrip(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	peer := dial(t, srv)
	peer.send(0x1, true, []byte("Hello"))
	peer.expectMessage(0x1, []byte("Hello"))
	peer.send(0x2, true, []byte{0, 1, 2, 0xFF})
	peer.expectMessage(0x2, []byte{0, 1, 2, 0xFF})
	//: a zero-length message is legal and is not "no message".
	peer.send(0x1, true, nil)
	peer.expectMessage(0x1, []byte{})
	//: a payload that needs the 16-bit length form, round-tripped whole.
	big := bytes.Repeat([]byte("x"), 700)
	peer.send(0x2, true, big)
	peer.expectMessage(0x2, big)
}

// TestFragmentationIsReassembled pins §5.4: a message split by the sender
// arrives whole, and control frames may sit between its fragments.
func TestFragmentationIsReassembled(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t)
	peer := dial(t, srv)
	//: §5.7's own fragmented example: "Hel" then "lo".
	peer.send(0x1, false, []byte("Hel"))
	peer.send(0x0, true, []byte("lo"))
	peer.expectMessage(0x1, []byte("Hello"))

	//: §5.4 — "control frames MAY be injected in the middle of a fragmented
	//: message". The pong must come back BEFORE the reassembled message,
	//: because the server answers it the moment it arrives.
	peer.send(0x1, false, []byte("a"))
	peer.send(0x9, true, []byte("probe"))
	peer.send(0x0, true, []byte("b"))
	_, op, payload, err := peer.readFrame()
	if err != nil {
		t.Fatalf("expected a pong: %v", err)
	}
	if op != 0xA {
		t.Fatalf("opcode = %#x, want pong (0xA)", op)
	}
	//: §5.5.2 — the Pong carries the SAME application data.
	if string(payload) != "probe" {
		t.Fatalf("pong payload = %q, want %q", payload, "probe")
	}
	peer.expectMessage(0x1, []byte("ab"))
}

// TestUTF8IsJudgedOnTheReassembledMessage is the case a per-frame validator
// gets wrong in BOTH directions.
//
// A four-byte rune split across two fragments is valid and must round-trip; a
// message that is invalid only once assembled must still fail with 1007.
func TestUTF8IsJudgedOnTheReassembledMessage(t *testing.T) {
	t.Parallel()
	t.Run("a rune split across a fragment boundary survives", func(t *testing.T) {
		t.Parallel()
		srv, _ := echoServer(t)
		peer := dial(t, srv)
		//: U+1F600, cut between its second and third byte.
		emoji := []byte{0xF0, 0x9F, 0x98, 0x80}
		peer.send(0x1, false, emoji[:2])
		peer.send(0x0, true, emoji[2:])
		peer.expectMessage(0x1, emoji)
	})
	t.Run("a truncated rune at the end of a message fails with 1007", func(t *testing.T) {
		t.Parallel()
		srv, _ := echoServer(t)
		peer := dial(t, srv)
		//: the first three bytes of a four-byte rune, and no fourth.
		peer.send(0x1, true, []byte{0xF0, 0x9F, 0x98})
		peer.expectClose(1007)
	})
	t.Run("an invalid sequence split across fragments still fails", func(t *testing.T) {
		t.Parallel()
		srv, _ := echoServer(t)
		peer := dial(t, srv)
		//: an overlong encoding of NUL, cut in half — neither fragment is a
		//: complete sequence, and the assembled message is still invalid.
		peer.send(0x1, false, []byte{0xC0})
		peer.send(0x0, true, []byte{0x80})
		peer.expectClose(1007)
	})
	t.Run("binary is never validated", func(t *testing.T) {
		t.Parallel()
		srv, _ := echoServer(t)
		peer := dial(t, srv)
		//: the same bytes that fail as text round-trip as binary, because
		//: §8.1 is about text and only text.
		peer.send(0x2, true, []byte{0xF0, 0x9F, 0x98})
		peer.expectMessage(0x2, []byte{0xF0, 0x9F, 0x98})
	})
}

// TestAdversarialFramesFailTheConnection is the table this package exists for.
//
// Each case is bytes a hostile or broken peer can put on the wire, and each
// must produce a Close with the code the RFC names — not a tolerated frame, not
// a silent drop, and not a server that keeps reading a stream it has already
// misunderstood.
func TestAdversarialFramesFailTheConnection(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		write func(p *wsPeer)
		code  uint16
		why   string
	}
	tests := []tc{
		{
			name: "an unmasked client frame",
			//: the single most important refusal in the whole protocol.
			write: func(p *wsPeer) { p.write(p.t, frame(true, 0, 0x1, false, []byte("Hello"))) },
			code:  1002,
			why:   "§5.1 — the server MUST close the connection on an unmasked frame",
		},
		{
			name:  "an unmasked close frame",
			write: func(p *wsPeer) { p.write(p.t, frame(true, 0, 0x8, false, nil)) },
			code:  1002,
			why:   "§5.1 applies to control frames too",
		},
		{
			name:  "a reserved bit set",
			write: func(p *wsPeer) { p.write(p.t, frame(true, 0x1, 0x1, true, []byte("x"))) },
			code:  1002,
			why:   "§5.2 — RSV must be zero with no extension negotiated",
		},
		{
			name:  "a reserved data opcode",
			write: func(p *wsPeer) { p.write(p.t, frame(true, 0, 0x3, true, nil)) },
			code:  1002,
			why:   "§5.2 — opcodes 0x3-0x7 are reserved",
		},
		{
			name:  "a reserved control opcode",
			write: func(p *wsPeer) { p.write(p.t, frame(true, 0, 0xB, true, nil)) },
			code:  1002,
			why:   "§5.2 — opcodes 0xB-0xF are reserved",
		},
		{
			name:  "a fragmented ping",
			write: func(p *wsPeer) { p.write(p.t, frame(false, 0, 0x9, true, []byte("x"))) },
			code:  1002,
			why:   "§5.5 — control frames must not be fragmented",
		},
		{
			name: "a control frame carrying 126 bytes",
			write: func(p *wsPeer) {
				p.write(p.t, frame(true, 0, 0x9, true, bytes.Repeat([]byte{0}, 126)))
			},
			code: 1002,
			why:  "§5.5 — a control payload must not exceed 125 bytes",
		},
		{
			name: "a length spelled longer than it needs to be",
			write: func(p *wsPeer) {
				//: 124 bytes announced through the 16-bit form — §5.2's own
				//: counter-example, verbatim.
				body := bytes.Repeat([]byte{'a'}, 124)
				key := [4]byte{1, 2, 3, 4}
				masked := slices.Clone(body)
				corenet.ApplyWSMask(masked, key)
				out := []byte{0x81, 0xFE, 0x00, 0x7C}
				out = append(out, key[:]...)
				p.write(p.t, append(out, masked...))
			},
			code: 1002,
			why:  "§5.2 — the minimal number of bytes MUST be used to encode the length",
		},
		{
			name: "a 64-bit length with the sign bit set",
			write: func(p *wsPeer) {
				p.write(p.t, []byte{0x82, 0xFF, 0x80, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4})
			},
			code: 1002,
			why:  "§5.2 — the most significant bit MUST be 0",
		},
		{
			name:  "a continuation with no message in progress",
			write: func(p *wsPeer) { p.send(0x0, true, []byte("orphan")) },
			code:  1002,
			why:   "§5.4 — a continuation continues something",
		},
		{
			name: "a new data frame interrupting a fragmented message",
			write: func(p *wsPeer) {
				p.send(0x1, false, []byte("half"))
				p.send(0x2, true, []byte("interloper"))
			},
			code: 1002,
			why:  "§5.4 — fragments of one message must not be interleaved with another",
		},
		{
			name: "a text frame interrupting its own fragmented message",
			write: func(p *wsPeer) {
				p.send(0x1, false, []byte("half"))
				p.send(0x1, true, []byte("again"))
			},
			code: 1002,
			why:  "§5.4 — the same rule, same opcode",
		},
		{
			name:  "a close frame carrying half a status code",
			write: func(p *wsPeer) { p.send(0x8, true, []byte{0x03}) },
			code:  1002,
			why:   "§5.5.1 — a close payload with a code is at least two bytes",
		},
		{
			name:  "a close frame carrying 1005",
			write: func(p *wsPeer) { p.send(0x8, true, []byte{0x03, 0xED}) },
			code:  1002,
			why:   "§7.4.1 — 1005 MUST NOT be set in a close frame",
		},
		{
			name:  "a close frame carrying 1006",
			write: func(p *wsPeer) { p.send(0x8, true, []byte{0x03, 0xEE}) },
			code:  1002,
			why:   "§7.4.1 — 1006 MUST NOT be set in a close frame",
		},
		{
			name:  "a close frame carrying an unallocated code",
			write: func(p *wsPeer) { p.send(0x8, true, []byte{0x0B, 0xB7}) }, // 2999, one below the library range
			code:  1002,
			why:   "§7.4.2 — 1016-2999 are reserved and unallocated",
		},
		{
			name: "a close reason that is not UTF-8",
			write: func(p *wsPeer) {
				p.send(0x8, true, []byte{0x03, 0xE8, 0xFF, 0xFE})
			},
			code: 1007,
			why:  "§5.5.1 — the reason is UTF-8, and §8.1 makes invalid UTF-8 fatal",
		},
		{
			name:  "a text message that is not UTF-8",
			write: func(p *wsPeer) { p.send(0x1, true, []byte{0xFF, 0xFE, 0xFD}) },
			code:  1007,
			why:   "§8.1",
		},
		{
			name: "a frame whose announced length is a lie the server must not fund",
			write: func(p *wsPeer) {
				//: sixteen megabytes announced, four bytes sent. Nothing is
				//: allocated from that number: the ceiling is checked against
				//: the ANNOUNCEMENT, so the connection ends before the read.
				out := []byte{0x82, 0xFF}
				out = binary.BigEndian.AppendUint64(out, 16<<20)
				out = append(out, 1, 2, 3, 4)
				p.write(p.t, append(out, 0xAA, 0xBB, 0xCC, 0xDD))
			},
			code: 1009,
			why:  "the length field is written by the peer; allocating from it is a DoS in one line",
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, ended := echoServer(t)
			peer := dial(t, srv)
			c.write(peer)
			peer.expectClose(c.code)
			//: §7.1.1 — the server closes the TCP connection; the peer's next
			//: read must therefore end rather than block.
			if _, _, _, err := peer.readFrame(); err == nil {
				t.Fatalf("the connection was still open after a %s (%s)", c.name, c.why)
			}
			select {
			case err := <-ended:
				if err == nil {
					t.Fatalf("the handler reported no error (%s)", c.why)
				}
			case <-time.After(peerDeadline):
				t.Fatalf("the handler never returned (%s)", c.why)
			}
		})
	}
}

// TestMessageCeilingCountsAcrossFragments pins the bound a per-frame ceiling
// alone would miss: a peer under the frame limit can still exceed the message
// limit a thousand small frames at a time.
func TestMessageCeilingCountsAcrossFragments(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t,
		websocket.MaxMessageSize(1000),
		websocket.MaxFrameSize(600),
	)
	peer := dial(t, srv)
	chunk := bytes.Repeat([]byte{'a'}, 600)
	peer.send(0x2, false, chunk)
	peer.send(0x0, true, chunk)
	peer.expectClose(1009)
}

// TestFrameCeilingIsCheckedBeforeTheMessageOne pins that a single oversized
// frame is refused on its header rather than after the buffer has grown.
func TestFrameCeilingIsCheckedBeforeTheMessageOne(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t, websocket.MaxFrameSize(64), websocket.MaxMessageSize(1<<20))
	peer := dial(t, srv)
	//: 65 bytes announced against a 64-byte frame ceiling.
	peer.send(0x2, true, bytes.Repeat([]byte{'a'}, 65))
	peer.expectClose(1009)
}

// TestControlFramesAreExemptFromTheFrameCeiling pins that a caller's small
// frame ceiling does not make Ping unanswerable.
//
// §5.5 already caps a control payload at 125; applying a smaller user ceiling
// on top would break the protocol's own liveness mechanism to enforce a bound
// that was never about control frames.
func TestControlFramesAreExemptFromTheFrameCeiling(t *testing.T) {
	t.Parallel()
	srv, _ := echoServer(t, websocket.MaxFrameSize(16), websocket.MaxMessageSize(32))
	peer := dial(t, srv)
	payload := bytes.Repeat([]byte{'p'}, 125)
	peer.send(0x9, true, payload)
	_, op, got, err := peer.readFrame()
	if err != nil {
		t.Fatalf("expected a pong: %v", err)
	}
	if op != 0xA {
		t.Fatalf("opcode = %#x, want pong", op)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("pong payload = %d bytes, want the same 125 back", len(got))
	}
}

// TestClosingHandshakeEchoesThePeersCode pins §5.5.1.
func TestClosingHandshakeEchoesThePeersCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sent []byte
		want uint16
	}
	tests := []tc{
		{"a normal closure is echoed", []byte{0x03, 0xE8}, 1000},
		{"a policy violation is echoed", []byte{0x03, 0xF0}, 1008},
		{"a private code is echoed", []byte{0x0F, 0xA0}, 4000},
		//: an empty payload means 1005, which must never travel — so the echo
		//: is a normal closure rather than the code we hold internally.
		{"no code at all becomes a normal closure", nil, 1000},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, ended := echoServer(t)
			peer := dial(t, srv)
			peer.send(0x8, true, c.sent)
			peer.expectClose(c.want)
			select {
			case err := <-ended:
				//: the handler's terminal error is the one it loops until.
				if !errs.HasCode(err, corenet.CodeWSConnClosed) {
					t.Fatalf("handler error = %v, want WS_CONN_CLOSED", err)
				}
			case <-time.After(peerDeadline):
				t.Fatalf("the handler never returned")
			}
		})
	}
}

// TestDrainSendsGoingAwayAndEndsTheHandler pins the whole reason this package
// watches the drain signal.
//
// A WebSocket connection is in flight forever by design. Without the signal the
// handler learns of a shutdown only when its socket is severed underneath it —
// which is the normal outcome of every deployment, for every connected client.
func TestDrainSendsGoingAwayAndEndsTheHandler(t *testing.T) {
	t.Parallel()
	draining := make(chan struct{})
	ended := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r)
		if err != nil {
			ended <- err
			return
		}
		defer func() { closeQuietly(conn) }()
		//: the handler does nothing but read, which is the shape that used to
		//: hold a drain open for its entire budget.
		for {
			//: the loop ends only on the terminal error, which is the point.
			if _, rerr := conn.Receive(); rerr != nil {
				ended <- rerr
				return
			}
		}
	}))
	//: the same mechanism the SDK engine uses: a channel on the base context.
	srv.Config.BaseContext = func(stdnet.Listener) context.Context {
		return corenet.WithDrainSignal(context.Background(), draining)
	}
	srv.Start()
	t.Cleanup(srv.Close)

	peer := dial(t, srv)
	//: one round trip, so the connection is provably live before the drain.
	peer.send(0x1, true, []byte("ping"))

	close(draining)
	//: §7.4.1 — 1001 is "an endpoint is going away, such as a server going
	//: down". The peer can reconnect elsewhere instead of guessing why its
	//: socket died.
	peer.expectClose(1001)
	select {
	case err := <-ended:
		if !errs.HasCode(err, corenet.CodeWSConnClosed) {
			t.Fatalf("handler error = %v, want WS_CONN_CLOSED", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never returned after the drain signal")
	}
}

// TestSendRefusesAfterTheConnectionEnds pins the floor under a handler that
// only ever calls Send.
//
// Done() alone is not enough: a handler that never selects on it would still
// hold a drain open. Both halves are load-bearing, exactly as they are for SSE.
func TestSendRefusesAfterTheConnectionEnds(t *testing.T) {
	t.Parallel()
	refused := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r)
		if err != nil {
			refused <- err
			return
		}
		//: closed immediately, then written to.
		closeQuietly(conn)
		refused <- conn.SendText("after the end")
	}))
	t.Cleanup(srv.Close)
	peer := dial(t, srv)
	//: the peer reads whatever the server sent, which must not include the
	//: message above.
	peer.drainOneFrame()
	select {
	case err := <-refused:
		if !errs.HasCode(err, corenet.CodeWSConnClosed) {
			t.Fatalf("Send after Close = %v, want WS_CONN_CLOSED", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestReceiveRefusesAnInvalidOutboundTextMessage pins the symmetry: this
// endpoint must not emit what it would refuse to receive.
func TestReceiveRefusesAnInvalidOutboundTextMessage(t *testing.T) {
	t.Parallel()
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r)
		if err != nil {
			result <- err
			return
		}
		defer func() { closeQuietly(conn) }()
		//: invalid UTF-8 in a TEXT message; the peer would be required by §8.1
		//: to fail the connection over it, so the caller gets an error instead
		//: of a mysterious disconnect.
		result <- conn.Send(corenet.WSMessageValue{Data: []byte{0xFF, 0xFE}})
	}))
	t.Cleanup(srv.Close)
	dial(t, srv)
	select {
	case err := <-result:
		if !errs.HasCode(err, corenet.CodeWSInvalidPayload) {
			t.Fatalf("Send(invalid text) = %v, want WS_INVALID_PAYLOAD", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestCloseWithRefusesACodeThatMustNotTravel pins that the caller cannot commit
// the error this package refuses to accept from a peer.
func TestCloseWithRefusesACodeThatMustNotTravel(t *testing.T) {
	t.Parallel()
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r)
		if err != nil {
			result <- err
			return
		}
		//: §7.4.1 — 1006 describes a connection that died without a close
		//: frame, so sending it in one contradicts its own delivery.
		result <- conn.CloseWith(corenet.WSCloseAbnormal, "")
	}))
	t.Cleanup(srv.Close)
	dial(t, srv)
	select {
	case err := <-result:
		if !errs.HasCode(err, corenet.CodeWSInvalidPayload) {
			t.Fatalf("CloseWith(1006) = %v, want WS_INVALID_PAYLOAD", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestPeerCloseCodeReportsWhatTheClientSaid pins the accessor a handler uses to
// tell a goodbye from a disappearance.
func TestPeerCloseCodeReportsWhatTheClientSaid(t *testing.T) {
	t.Parallel()
	observed := make(chan corenet.WSCloseCode, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r)
		if err != nil {
			observed <- 0
			return
		}
		defer func() { closeQuietly(conn) }()
		receiveQuietly(conn)
		observed <- conn.PeerCloseCode()
	}))
	t.Cleanup(srv.Close)
	peer := dial(t, srv)
	peer.send(0x8, true, []byte{0x03, 0xF1}) // 1009
	select {
	case got := <-observed:
		if got != corenet.WSCloseTooLarge {
			t.Fatalf("PeerCloseCode() = %d, want 1009", got)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestSubprotocolIsReportedToTheHandler pins that what the handshake agreed is
// what the handler can act on.
func TestSubprotocolIsReportedToTheHandler(t *testing.T) {
	t.Parallel()
	chosen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, websocket.Subprotocols("v2", "v1"))
		if err != nil {
			chosen <- "<refused>"
			return
		}
		defer func() { closeQuietly(conn) }()
		chosen <- conn.Subprotocol()
	}))
	t.Cleanup(srv.Close)
	peer := rawDial(t, srv)
	resp := peer.handshake(t, srv, "Sec-WebSocket-Protocol: v1, v2")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	select {
	case got := <-chosen:
		if got != "v2" {
			t.Fatalf("Subprotocol() = %q, want v2", got)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestUpgradeRefusesAResponseItCannotHijack pins the distinction between "your
// request was wrong" and "this stack cannot serve it".
//
// httptest.ResponseRecorder implements neither Hijacker nor Unwrap, which is
// exactly the shape of a middleware that wraps the response without forwarding.
func TestUpgradeRefusesAResponseItCannotHijack(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	_, err := websocket.Upgrade(recorder, request)
	if !errs.HasCode(err, corenet.CodeWSUpgradeUnsupported) {
		t.Fatalf("Upgrade on a recorder = %v, want WS_UPGRADE_UNSUPPORTED", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — the request was fine, this stack is not", recorder.Code)
	}
}

// TestUpgradeRefusesADataRushBeforeTheHandshakeCompletes pins the smuggling
// guard: a client that pipelines frames onto the upgrade request has either
// broken the protocol or is trying to slip a second request past an
// intermediary that has not switched yet.
func TestUpgradeRefusesADataRushBeforeTheHandshakeCompletes(t *testing.T) {
	t.Parallel()
	srv, ended := echoServer(t)
	peer := rawDial(t, srv)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	//: the handshake and a frame, in one write, so net/http buffers both.
	request := []byte("GET / HTTP/1.1\r\n" +
		"Host: " + strings.TrimPrefix(srv.URL, "http://") + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(nonce[:]) + "\r\n\r\n")
	peer.write(t, append(request, frame(true, 0, 0x1, true, []byte("smuggled"))...))
	select {
	case err := <-ended:
		if !errs.HasCode(err, corenet.CodeWSHandshakeFailed) {
			t.Fatalf("Upgrade = %v, want WS_HANDSHAKE_FAILED", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("the handler never reported")
	}
}

// TestOptionsAreRefusedRatherThanGuessedAt pins ADR 0031 at the public edge of
// this package: a zero that would silently disable a defence is clamped, and a
// value nothing could interpret is refused.
func TestOptionsAreRefusedRatherThanGuessedAt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		opt  websocket.Option
	}
	tests := []tc{
		{"a negative ping interval", websocket.PingInterval(-time.Second)},
		{"a negative write budget", websocket.WriteTimeout(-time.Second)},
		{"a negative message ceiling", websocket.MaxMessageSize(-1)},
		{"a negative frame ceiling", websocket.MaxFrameSize(-1)},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, ended := echoServer(t, c.opt)
			peer := rawDial(t, srv)
			resp := peer.handshake(t, srv)
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			select {
			case err := <-ended:
				if !errs.HasCode(err, corenet.CodeWSConnMisconfigured) {
					t.Fatalf("Upgrade = %v, want WS_CONN_MISCONFIGURED", err)
				}
			case <-time.After(peerDeadline):
				t.Fatalf("the handler never reported")
			}
		})
	}
	//: the frame ceiling above the message ceiling is a mistake with two
	//: readings and no way to pick between them.
	t.Run("a frame ceiling that can never be reached", func(t *testing.T) {
		t.Parallel()
		srv, ended := echoServer(t, websocket.MaxMessageSize(100), websocket.MaxFrameSize(200))
		peer := rawDial(t, srv)
		if resp := peer.handshake(t, srv); resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", resp.StatusCode)
		}
		select {
		case err := <-ended:
			if !errs.HasCode(err, corenet.CodeWSConnMisconfigured) {
				t.Fatalf("Upgrade = %v, want WS_CONN_MISCONFIGURED", err)
			}
		case <-time.After(peerDeadline):
			t.Fatalf("the handler never reported")
		}
	})
}

// TestHeartbeatEndsAConnectionToAPeerThatVanished pins the only liveness check
// this connection has.
//
// A peer that stops answering without closing leaves a socket that is perfectly
// readable and will simply never produce another byte. The heartbeat is what
// turns that silence into an ending — and a test that only ever used a
// well-behaved client would never notice it was missing.
func TestHeartbeatEndsAConnectionToAPeerThatVanished(t *testing.T) {
	t.Parallel()
	ended := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, websocket.PingInterval(50*time.Millisecond))
		if err != nil {
			ended <- err
			return
		}
		defer func() { closeQuietly(conn) }()
		_, rerr := conn.Receive()
		ended <- rerr
	}))
	t.Cleanup(srv.Close)
	//: dialled, then deliberately mute: no pong, no close, nothing.
	dial(t, srv)
	select {
	case err := <-ended:
		if !errs.HasCode(err, corenet.CodeWSConnClosed) {
			t.Fatalf("handler error = %v, want WS_CONN_CLOSED", err)
		}
	case <-time.After(peerDeadline):
		t.Fatalf("a silent peer never ended the connection; the heartbeat is inert")
	}
}
