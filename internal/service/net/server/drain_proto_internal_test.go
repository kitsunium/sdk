// Package server — the protocols the ADR 0196 drain suite replays: the lane's
// line echo, and h2c on the HTTP/2 adapter (#961), so the drain's promises —
// the grace under arrivals, the listen queue taken, nothing cut — are held on
// HTTP/2 connections by the same suite, not by a test of their own.
package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	stdnet "net"
	"strconv"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// drainProto is one protocol the drain suite runs on: its group, one
// exchange on a fresh connection, a connection kept open once heard, and the
// end such a connection must see.
type drainProto struct {
	// name names the protocol in the subtest.
	name string
	// group declares the group on srv and returns its open and close counts,
	// read once the server stopped.
	group func(srv *Server, b corenet.LoopBackend) func() (opens, closes int64)
	// exchange dials addr, makes one exchange and closes, saying whether the
	// dial succeeded and what went wrong, empty when nothing did.
	exchange func(addr string) (bool, string)
	// keep dials addr and makes one exchange, keeping the connection open.
	keep func(t *testing.T, addr string) stdnet.Conn
	// end reads a kept connection to its end and says what went wrong: empty
	// for the protocol's clean end.
	end func(c stdnet.Conn) string
}

// drainProtos are the protocols of the drain suite.
func drainProtos() []drainProto {
	return []drainProto{
		{
			name: "line",
			group: func(srv *Server, b corenet.LoopBackend) func() (int64, int64) {
				h := &laneEchoH{}
				srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: b, Loops: corenet.LoopsAuto})).HandleLoop(h)
				return func() (int64, int64) { return h.opens.Load(), h.closes.Load() }
			},
			exchange: exchange,
			keep: func(t *testing.T, addr string) stdnet.Conn {
				c := loopDial(t, addr)
				lineEcho(t, c, "k\n")
				return c
			},
			end: func(c stdnet.Conn) string {
				rest, err := io.ReadAll(c)
				if err != nil || len(rest) > 0 {
					return errString(err) + " " + string(rest)
				}
				return ""
			},
		},
		{
			name: "h2c",
			group: func(srv *Server, b corenet.LoopBackend) func() (int64, int64) {
				hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
				srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Loop(corenet.LoopValue{Backend: b, Loops: corenet.LoopsAuto})).HandleLoop(http2.HandleLoop(hello, http1.Parser{}))
				return func() (int64, int64) { return ringCounts(srv) }
			},
			exchange: h2cExchange,
			keep: func(t *testing.T, addr string) stdnet.Conn {
				c := loopDial(t, addr)
				br := bufio.NewReader(c)
				if msg := h2cRequest(c, br); msg != "" {
					t.Fatalf("kept h2c connection: %s", msg)
				}
				return &bufConn{Conn: c, br: br}
			},
			end: h2cEnd,
		},
	}
}

// bufConn is a connection whose reads go through the reader its first
// exchange used, so nothing that reader buffered is lost.
type bufConn struct {
	stdnet.Conn
	br *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// ringCounts is the connections every ring of srv's loop groups accepted and
// closed.
func ringCounts(srv *Server) (opens, closes int64) {
	for _, l := range srv.State().Loops {
		for _, r := range l.RingStates {
			opens += int64(r.Accepted)
			closes += int64(r.Closed)
		}
	}
	return opens, closes
}

// h2cExchange is exchange on h2c: the preface, SETTINGS and one GET on
// stream 1, answered "ok"; a reset, a GOAWAY with an error or an end before
// the answer is what went wrong.
func h2cExchange(addr string) (bool, string) {
	c, err := stdnet.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false, "dial: " + err.Error()
	}
	defer func() { swallowErr(c.Close()) }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return true, h2cRequest(c, bufio.NewReader(c))
}

// h2cRequest opens h2c on c and makes one GET on stream 1, reading its
// answer through br.
func h2cRequest(c stdnet.Conn, br *bufio.Reader) string {
	out := append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), frameBytes(corehttp2.FrameSettings, 0, 0, nil)...)
	out = append(out, frameBytes(corehttp2.FrameHeaders, corehttp2.FlagEndStream|corehttp2.FlagEndHeaders, 1, requestBlock("GET", "/"))...)
	if _, err := c.Write(out); err != nil {
		return "write: " + err.Error()
	}
	var body []byte
	for {
		typ, flags, stream, payload, err := readH2Frame(br)
		if err != nil {
			return "before the answer: " + err.Error()
		}
		switch {
		case typ == corehttp2.FrameSettings && flags&corehttp2.FlagAck == 0:
			if _, err := c.Write(frameBytes(corehttp2.FrameSettings, corehttp2.FlagAck, 0, nil)); err != nil {
				return "settings ack: " + err.Error()
			}
		case typ == corehttp2.FrameRSTStream:
			return "a reset on stream " + strconv.Itoa(int(stream))
		case typ == corehttp2.FrameGoAway && len(payload) >= 8:
			if code := binary.BigEndian.Uint32(payload[4:8]); code != uint32(corehttp2.ErrCodeNoError) {
				return "GOAWAY code " + strconv.Itoa(int(code))
			}
			if last := binary.BigEndian.Uint32(payload[:4]) & 0x7fffffff; last < 1 {
				return "GOAWAY before stream 1 was taken"
			}
		case stream == 1 && (typ == corehttp2.FrameData || typ == corehttp2.FrameHeaders):
			if typ == corehttp2.FrameData {
				body = append(body, payload...)
			}
			if flags&corehttp2.FlagEndStream != 0 {
				if !bytes.Equal(body, []byte("ok")) {
					return "answer body " + strconv.Quote(string(body))
				}
				return ""
			}
		}
	}
}

// h2cEnd reads a kept h2c connection to its end: GOAWAY(NO_ERROR), nothing
// reset, then the end of the stream.
func h2cEnd(c stdnet.Conn) string {
	br, ok := c.(*bufConn)
	if !ok {
		return "not a kept h2c connection"
	}
	goaway := false
	for {
		typ, _, stream, payload, err := readH2Frame(br.br)
		if err == io.EOF {
			if !goaway {
				return "the end without a GOAWAY"
			}
			return ""
		}
		if err != nil {
			return "read: " + err.Error()
		}
		switch {
		case typ == corehttp2.FrameRSTStream:
			return "a reset on stream " + strconv.Itoa(int(stream))
		case typ == corehttp2.FrameGoAway && len(payload) >= 8:
			if code := binary.BigEndian.Uint32(payload[4:8]); code != uint32(corehttp2.ErrCodeNoError) {
				return "GOAWAY code " + strconv.Itoa(int(code))
			}
			goaway = true
		}
	}
}

// readH2Frame reads one frame from br.
func readH2Frame(br *bufio.Reader) (corehttp2.FrameType, corehttp2.FrameFlags, uint32, []byte, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return 0, 0, 0, nil, err
	}
	n := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
	payload := make([]byte, n)
	if _, err := io.ReadFull(br, payload); err != nil {
		return 0, 0, 0, nil, err
	}
	return corehttp2.FrameType(hdr[3]), corehttp2.FrameFlags(hdr[4]), binary.BigEndian.Uint32(hdr[5:]) & 0x7fffffff, payload, nil
}
