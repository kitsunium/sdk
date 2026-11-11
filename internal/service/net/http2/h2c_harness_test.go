package http2_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// clientPrefaceText is the 24 bytes every client connection opens with, RFC 9113
// §3.4.
const clientPrefaceText = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// harnessTimeout bounds every round trip of a differential test.
const harnessTimeout = 10 * time.Second

// serveFunc answers one decoded request: its fields as "name=value", the
// block's bytes consumed reported as the slice given.
type serveFunc func(fields []string) *corenet.HTTP1ResponseValue

// h2cHarness is a server of one purpose: the real Go client talking to the
// frame layer and HPACK. It reads frames and field blocks the Decoder judges,
// and writes frames the Encoder and the Append family write, nothing else.
type h2cHarness struct {
	// ln listens on the loopback, on an ephemeral port.
	ln net.Listener
	// serve answers each request.
	serve serveFunc
	// done closes when the accept loop ends.
	done chan struct{}
	// wg counts the connections' goroutines.
	wg sync.WaitGroup
	// mu guards conns, the connections to close on Close.
	mu sync.Mutex
	// conns holds the live connections.
	conns []net.Conn
	// failures records a harness failure from a server goroutine, reported
	// on the test's goroutine.
	failures chan error
}

// newH2cHarness starts a harness on an ephemeral loopback port.
func newH2cHarness(tb testing.TB, serve serveFunc) *h2cHarness {
	tb.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	h := &h2cHarness{ln: ln, serve: serve, done: make(chan struct{}), failures: make(chan error, 8)}
	go h.acceptLoop()
	return h
}

// acceptLoop serves each connection on its own goroutine until the listener
// closes.
func (h *h2cHarness) acceptLoop() {
	defer close(h.done)
	for {
		c, err := h.ln.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		h.conns = append(h.conns, c)
		h.mu.Unlock()
		h.wg.Add(1)
		go h.serveConn(c)
	}
}

// close stops the harness and every connection's goroutine.
func (h *h2cHarness) close() {
	_ = h.ln.Close()
	<-h.done
	h.mu.Lock()
	for _, c := range h.conns {
		_ = c.Close()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// serveConn speaks HTTP/2 on one connection until the peer closes it.
func (h *h2cHarness) serveConn(c net.Conn) {
	defer h.wg.Done()
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	var preface [len(clientPrefaceText)]byte
	if _, err := io.ReadFull(r, preface[:]); err != nil || string(preface[:]) != clientPrefaceText {
		return
	}
	w := http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingMaxConcurrentStreams, Val: 100}})
	if _, err := c.Write(w); err != nil {
		return
	}
	d := http2.NewDecoder(4096, 1<<20)
	mem := make([]byte, http2.TableMem(4096))
	h.frames(c, r, &d, mem)
}

// frames reads whole frames, assembling field blocks and answering requests.
func (h *h2cHarness) frames(c net.Conn, r *bufio.Reader, d *http2.Decoder, mem []byte) {
	var hdr [corehttp2.FrameHeaderLen]byte
	var block []byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		fr, _, code := http2.ParseFrameHeader(hdr[:], 1<<20)
		if code != corehttp2.ErrCodeNoError {
			h.failures <- errString("frame refused: " + code.String())
			return
		}
		payload := make([]byte, fr.Length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return
		}
		switch fr.Type {
		case corehttp2.FrameSettings:
			if fr.Flags&corehttp2.FlagAck == 0 {
				if _, err := c.Write(http2.AppendSettingsAck(nil)); err != nil {
					return
				}
			}
		case corehttp2.FrameHeaders, corehttp2.FrameContinuation:
			block = append(block, stripHeadersFrame(fr, payload)...)
			if fr.Flags&corehttp2.FlagEndHeaders != 0 {
				h.answer(c, d, mem, block, fr.StreamID)
				block = block[:0]
			}
		case corehttp2.FramePing:
			if fr.Flags&corehttp2.FlagAck == 0 {
				var data [8]byte
				copy(data[:], payload)
				if _, err := c.Write(http2.AppendPing(nil, true, &data)); err != nil {
					return
				}
			}
		}
	}
}

// stripHeadersFrame returns a HEADERS or CONTINUATION payload's field-block
// fragment: without pad length, padding and priority.
func stripHeadersFrame(fr corehttp2.FrameValue, payload []byte) []byte {
	if fr.Type != corehttp2.FrameHeaders {
		return payload
	}
	pad := 0
	if fr.Flags&corehttp2.FlagPadded != 0 {
		pad = int(payload[0])
		payload = payload[1:]
	}
	if fr.Flags&corehttp2.FlagPriority != 0 {
		payload = payload[5:]
	}
	return payload[:len(payload)-pad]
}

// answer decodes a whole field block and answers it on stream.
func (h *h2cHarness) answer(c net.Conn, d *http2.Decoder, mem, block []byte, stream uint32) {
	var req corenet.HTTP1RequestValue
	if _, err := d.Decode(mem, block, make([]byte, 0, 1024), &req); err != nil {
		h.failures <- err
		return
	}
	fields := make([]string, 0, req.NFields)
	for i := range req.NFields {
		fields = append(fields, string(req.Fields[i].Name)+"="+string(req.Fields[i].Value))
	}
	res := h.serve(fields)
	endStream := len(res.Body) == 0
	out, err := http2.NewEncoder(16384).AppendHeaders(nil, stream, res, endStream)
	if err != nil {
		h.failures <- err
		return
	}
	if !endStream {
		out = http2.AppendFrameHeader(out, corehttp2.FrameValue{
			Type: corehttp2.FrameData, Flags: corehttp2.FlagEndStream, StreamID: stream, Length: len(res.Body),
		})
		out = append(out, res.Body...)
	}
	_, _ = c.Write(out)
}

// errString is a harness failure.
type errString string

// Error implements error.
func (e errString) Error() string { return string(e) }

// h2cClient is a Go http client speaking prior-knowledge h2c, RFC 9113 §3.3.
func h2cClient(tb testing.TB) (*http.Client, func()) {
	tb.Helper()
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: tr, Timeout: harnessTimeout}
	return client, tr.CloseIdleConnections
}

// reportFailures fails the test with the harness's first failure, if any.
func (h *h2cHarness) reportFailures(tb testing.TB) {
	tb.Helper()
	select {
	case err := <-h.failures:
		tb.Fatal(err)
	default:
	}
}

// fieldsContain reports whether fields holds name=value.
func fieldsContain(fields []string, name, value string) bool {
	want := name + "=" + value
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// lowerName lowercases an ASCII header name.
func lowerName(s string) string {
	return strings.ToLower(s)
}
