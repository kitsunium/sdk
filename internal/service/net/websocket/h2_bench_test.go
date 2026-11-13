package websocket_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

// errNotUpgraded is a bench client's extended CONNECT not answered 200.
var errNotUpgraded = errors.New("extended CONNECT not answered 200")

// h2BenchPeer is the benchmark's HTTP/2 client: one TLS connection, one
// extended CONNECT stream carrying the WebSocket, its DATA credited back as it
// is read. It is written by hand for the reason h2WS is: Go 1.27's
// http.Transport refuses the :protocol field before HTTP/2 is chosen
// (TestXConnectTransportRefusesProtocolField).
type h2BenchPeer struct {
	c   *tls.Conn
	br  *bufio.Reader
	hdr [9]byte
	buf []byte
}

// dialH2Bench opens the extended CONNECT stream 1 on a fresh connection.
func dialH2Bench(addr string, pool *x509.CertPool) (*h2BenchPeer, error) {
	d := &tls.Dialer{NetDialer: &stdnet.Dialer{Timeout: peerDeadline}, Config: &tls.Config{RootCAs: pool, ServerName: h2ServerName, NextProtos: []string{"h2"}}}
	nc, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &h2BenchPeer{c: nc.(*tls.Conn), br: bufio.NewReaderSize(nc, 64<<10)}
	_ = p.c.SetDeadline(time.Now().Add(peerDeadline))
	block := literal(":method", http.MethodConnect)
	block = append(block, literal(":scheme", "https")...)
	block = append(block, literal(":path", "/")...)
	block = append(block, literal(":authority", h2ServerName)...)
	block = append(block, literal(":protocol", "websocket")...)
	block = append(block, literal("sec-websocket-version", "13")...)
	out := []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	out = p.appendFrame(out, 0x4, 0, 0, nil)
	if _, err := p.c.Write(out); err != nil {
		return nil, err
	}
	// The server's SETTINGS first, acknowledged before the CONNECT: the
	// stream may only use extended CONNECT once the setting is known.
	for {
		typ, flags, _, err := p.read()
		if err != nil {
			return nil, err
		}
		if typ == 0x4 && flags&0x1 == 0 {
			break
		}
	}
	out = p.appendFrame(out[:0], 0x4, 0x1, 0, nil)
	out = p.appendFrame(out, 0x1, 0x4, 1, block)
	if _, err := p.c.Write(out); err != nil {
		return nil, err
	}
	for {
		typ, _, stream, err := p.read()
		if err != nil {
			return nil, err
		}
		if stream == 1 && typ == 0x1 {
			if len(p.buf) == 0 || p.buf[0] != 0x88 {
				return nil, errNotUpgraded
			}
			return p, nil
		}
		if stream == 1 && typ == 0x3 {
			return nil, errNotUpgraded
		}
	}
}

// appendFrame appends one frame to out.
func (p *h2BenchPeer) appendFrame(out []byte, typ, flags byte, stream uint32, payload []byte) []byte {
	out = append(out, byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)), typ, flags)
	out = binary.BigEndian.AppendUint32(out, stream)
	return append(out, payload...)
}

// read reads one frame into p.buf.
func (p *h2BenchPeer) read() (typ, flags byte, stream uint32, err error) {
	if _, err = io.ReadFull(p.br, p.hdr[:]); err != nil {
		return 0, 0, 0, err
	}
	n := int(p.hdr[0])<<16 | int(p.hdr[1])<<8 | int(p.hdr[2])
	if cap(p.buf) < n {
		p.buf = make([]byte, n)
	}
	p.buf = p.buf[:n]
	if _, err = io.ReadFull(p.br, p.buf); err != nil {
		return 0, 0, 0, err
	}
	return p.hdr[3], p.hdr[4], binary.BigEndian.Uint32(p.hdr[5:]) & 0x7fffffff, nil
}

// echo sends frame as one DATA frame and reads DATA until want bytes came
// back, crediting each to the connection and the stream.
func (p *h2BenchPeer) echo(out, frame []byte, want int) ([]byte, error) {
	_ = p.c.SetDeadline(time.Now().Add(peerDeadline))
	out = p.appendFrame(out[:0], 0x0, 0, 1, frame)
	if _, err := p.c.Write(out); err != nil {
		return out, err
	}
	for got := 0; got < want; {
		typ, _, stream, err := p.read()
		if err != nil {
			return out, err
		}
		if typ != 0x0 || stream != 1 || len(p.buf) == 0 {
			continue
		}
		got += len(p.buf)
		var inc [4]byte
		binary.BigEndian.PutUint32(inc[:], uint32(len(p.buf)))
		out = p.appendFrame(out[:0], 0x8, 0, 0, inc[:])
		out = p.appendFrame(out, 0x8, 0, 1, inc[:])
		if _, err := p.c.Write(out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// BenchmarkXConnectLoopback is BenchmarkLoopback over HTTP/2 (#967): one
// echo round trip of a masked binary message per iteration, on an RFC 8441
// stream of a TLS group with Protocols("h2"), the kernel in the path. net/http
// reads GODEBUG=http2xconnect=1 at its init, so the rows run only in a binary
// started with it:
//
//	cd internal/service && GOWORK=off GODEBUG=http2xconnect=1 SDK_WEBSOCKET_TEST_XCONNECT=1 \
//	  go test -run='^$' -bench='Loopback' -benchmem -count=5 ./net/websocket/
//
// It is TLS where BenchmarkLoopback is cleartext HTTP/1.1, so the difference
// carries the record layer as well as the HTTP/2 framing.
func BenchmarkXConnectLoopback(b *testing.B) {
	if os.Getenv(xconnectRole) == "" {
		b.Skip("needs GODEBUG=http2xconnect=1 and " + xconnectRole + "=1 at the binary's start")
	}
	for _, size := range []int{64, 4 << 10} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			ended := make(chan error, 1)
			_, addr, pool := h2Group(b, echoHandler(ended, websocket.WithoutPing()))
			peer, err := dialH2Bench(addr, pool)
			if err != nil {
				b.Fatalf("dial: %v", err)
			}
			b.Cleanup(func() { _ = peer.c.Close() })
			frame := maskedBinary(size)
			want := size + 2
			if size > 125 {
				want += 2
			}
			out := make([]byte, 0, len(frame)+64)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if out, err = peer.echo(out, frame, want); err != nil {
					select {
					case end := <-ended:
						b.Fatalf("echo: %v; the handler ended: %v", err, end)
					default:
						b.Fatalf("echo: %v", err)
					}
				}
			}
		})
	}
}

// maskedBinary is one masked binary client frame of size bytes.
func maskedBinary(size int) []byte {
	key := [4]byte{0x37, 0xFA, 0x21, 0x3D}
	out := []byte{0x80 | 0x2}
	if size <= 125 {
		out = append(out, 0x80|byte(size))
	} else {
		out = append(out, 0x80|126)
		out = binary.BigEndian.AppendUint16(out, uint16(size))
	}
	out = append(out, key[:]...)
	for i := range size {
		out = append(out, byte(i%251)^key[i%4])
	}
	return out
}

// TestXConnectTransportRefusesProtocolField is the witness of #967's
// amendment: Go 1.27's http.Transport validates every header field name
// before it chooses HTTP/2 (transport.go, validateHeaders), and :protocol is
// no valid name, so a client cannot send RFC 8441's extended CONNECT through
// it — net/http's HTTP/2 client half sends :protocol only for a caller that
// skips that check, which no public API is. The tests frame HTTP/2 by hand
// for that reason; this one fails once a Go release lets the Transport
// through, and they can move to it.
func TestXConnectTransportRefusesProtocolField(t *testing.T) {
	inXConnectChild(t)
	_, addr, pool := h2Group(t, echoHandler(make(chan error, 1)))
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: h2ServerName}, ForceAttemptHTTP2: true}
	t.Cleanup(tr.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodConnect, "https://"+addr+"/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header[":protocol"] = []string{"websocket"}
	req.Header.Set("Sec-WebSocket-Version", "13")
	resp, err := tr.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("http.Transport sent an extended CONNECT (%s): the tests can use it now", resp.Status)
	}
	if want := `invalid header field name ":protocol"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("RoundTrip = %v, want %q", err, want)
	}
}
