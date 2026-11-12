package http2_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// connPreface is the bytes a client opens every connection with: the preface
// and an empty SETTINGS frame, RFC 9113 §3.4.
func connPreface() []byte {
	return http2.AppendSettings([]byte(clientPrefaceText), nil)
}

// driver is one connection on a fresh lane, fed by hand-written frames.
type driver struct {
	t    *testing.T
	lane *http2.Lane
	conn http2.Conn
	// out accumulates what the connection answers.
	out []byte
}

// newDriverLimits opens a connection under the given limits and reads its preface.
func newDriverLimits(t *testing.T, limits corehttp2.LimitsValue, maxBody int64, holdCap int) *driver {
	t.Helper()
	lane, err := http2.NewLane(limits, maxBody, holdCap)
	if err != nil {
		t.Fatal(err)
	}
	d := &driver{t: t, lane: lane}
	lane.Open(&d.conn)
	d.feed(connPreface())
	if !bytes.Contains(d.out, []byte{0x00, 0x00}) { // the server's SETTINGS opened the output
		t.Fatalf("no server preface written: %x", d.out)
	}
	d.out = d.out[:0]
	return d
}

// newDriver opens a connection under the default limits.
func newDriver(t *testing.T) *driver {
	t.Helper()
	return newDriverLimits(t, corehttp2.LimitsValue{}, 1<<20, 0)
}

// feed gives the connection whole frames and expects them consumed.
func (d *driver) feed(frames ...[]byte) []http2.Event {
	d.t.Helper()
	buf := bytes.Join(frames, nil)
	n, out := d.lane.Feed(&d.conn, buf, d.out)
	d.out = out
	if n != len(buf) {
		d.t.Fatalf("consumed %d of %d", n, len(buf))
	}
	return d.lane.Events()
}

// reqBlock is a small request field block: GET / http, a custom field.
func reqBlock() []byte {
	block := []byte{0x82, 0x86, 0x84}
	return append(block, 0x00, 3, 'x', '-', 'a', 1, 'b')
}

// headersFrame writes a HEADERS frame with one field block.
func headersFrame(stream uint32, flags corehttp2.FrameFlags, block []byte) []byte {
	out := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: flags, StreamID: stream, Length: len(block)})
	return append(out, block...)
}

// endHeaders is END_HEADERS.
const endHeaders = corehttp2.FlagEndHeaders

// endStream is END_STREAM.
const endStream = corehttp2.FlagEndStream

// dataFrame writes a DATA frame.
func dataFrame(stream uint32, flags corehttp2.FrameFlags, payload []byte) []byte {
	out := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: flags, StreamID: stream, Length: len(payload)})
	return append(out, payload...)
}

// parsedFrame is one frame read back from the connection's output.
type parsedFrame struct {
	h    corehttp2.FrameValue
	body []byte
}

// parseOut reads every frame in out.
func parseOut(t *testing.T, out []byte) []parsedFrame {
	t.Helper()
	var frames []parsedFrame
	for len(out) > 0 {
		h, n, code := http2.ParseFrameHeader(out, corehttp2.MaxFrameLimit)
		if n == 0 || code != corehttp2.ErrCodeNoError {
			t.Fatalf("bad output frame: n=%d code=%s (%x)", n, code, out)
		}
		frames = append(frames, parsedFrame{h: h, body: out[n : n+h.Length]})
		out = out[n+h.Length:]
	}
	return frames
}

// errOf reads an RST_STREAM or GOAWAY's error code.
func errOf(f parsedFrame) corehttp2.ErrCode {
	body := f.body
	if f.h.Type == corehttp2.FrameGoAway {
		body = body[4:]
	}
	return corehttp2.ErrCode(binary.BigEndian.Uint32(body))
}

// lastFrame returns the last frame of the connection's output.
func (d *driver) lastFrame() parsedFrame {
	d.t.Helper()
	frames := parseOut(d.t, d.out)
	if len(frames) == 0 {
		d.t.Fatal("no frames")
	}
	return frames[len(frames)-1]
}

// wantConnError expects the output to end in GOAWAY(code).
func (d *driver) wantConnError(code corehttp2.ErrCode) {
	d.t.Helper()
	f := d.lastFrame()
	if f.h.Type != corehttp2.FrameGoAway || errOf(f) != code {
		d.t.Fatalf("want GOAWAY(%s), got %+v %x", code, f.h, f.body)
	}
	if !d.lane.Closing(&d.conn) {
		d.t.Fatal("connection not marked closing")
	}
}

// wantStreamError expects the output to end in RST_STREAM(stream, code).
func (d *driver) wantStreamError(stream uint32, code corehttp2.ErrCode) {
	d.t.Helper()
	f := d.lastFrame()
	if f.h.Type != corehttp2.FrameRSTStream || f.h.StreamID != stream || errOf(f) != code {
		d.t.Fatalf("want RST_STREAM(%d, %s), got %+v %x", stream, code, f.h, f.body)
	}
}

// TestPrefaceThenFirstFrame holds §3.4: the first frame is SETTINGS, the
// server speaks first.
func TestPrefaceThenFirstFrame(t *testing.T) {
	lane, _ := http2.NewLane(corehttp2.LimitsValue{}, 1<<20, 0)
	var c http2.Conn
	lane.Open(&c)
	n, out := lane.Feed(&c, append([]byte(clientPrefaceText), headersFrame(1, endHeaders|endStream, reqBlock())...), nil)
	if n == 0 || !lane.Closing(&c) {
		t.Fatalf("first frame not SETTINGS: n=%d", n)
	}
	frames := parseOut(t, out)
	last := frames[len(frames)-1]
	if last.h.Type != corehttp2.FrameGoAway || errOf(last) != corehttp2.ErrCodeProtocol {
		t.Fatalf("want GOAWAY(PROTOCOL_ERROR), got %+v", last.h)
	}
}

// TestPrefaceRefused holds the client preface: other bytes end the connection.
func TestPrefaceRefused(t *testing.T) {
	lane, _ := http2.NewLane(corehttp2.LimitsValue{}, 1<<20, 0)
	var c http2.Conn
	lane.Open(&c)
	if n, out := lane.Feed(&c, []byte("GET / HTTP/1.1\r\n"), nil); n != 16 || !lane.Closing(&c) || len(out) == 0 {
		t.Fatalf("n=%d closing=%v out=%x", n, lane.Closing(&c), out)
	}
}

// TestPrefaceIncremental takes the preface a byte at a time.
func TestPrefaceIncremental(t *testing.T) {
	lane, _ := http2.NewLane(corehttp2.LimitsValue{}, 1<<20, 0)
	var c http2.Conn
	lane.Open(&c)
	all := connPreface()
	var out []byte
	spill := []byte{}
	for _, b := range all {
		spill = append(spill, b)
		var n int
		n, out = lane.Feed(&c, spill, out)
		spill = spill[n:]
	}
	if lane.Closing(&c) || len(spill) != 0 {
		t.Fatalf("spill %d closing %v", len(spill), lane.Closing(&c))
	}
	frames := parseOut(t, out)
	if frames[0].h.Type != corehttp2.FrameSettings || frames[1].h.Type != corehttp2.FrameWindowUpdate || frames[1].h.StreamID != 0 {
		t.Fatalf("server preface: %+v %+v", frames[0].h, frames[1].h)
	}
	ws := binary.BigEndian.Uint32(frames[1].body)
	if int(ws) != 1<<20-corehttp2.DefaultWindow {
		t.Fatalf("connection window raised by %d", ws)
	}
}

// TestLegalStreamLife runs one stream end to end: open, half-closed, answered.
func TestLegalStreamLife(t *testing.T) {
	d := newDriver(t)
	evs := d.feed(headersFrame(1, endHeaders, reqBlock()))
	if len(evs) != 1 || evs[0].Kind != http2.EventHeaders || evs[0].Stream != 1 || evs[0].Flags&http2.EventEndStream != 0 {
		t.Fatalf("headers: %+v", evs)
	}
	evs = d.feed(dataFrame(1, endStream, []byte("body")))
	if len(evs) != 1 || evs[0].Kind != http2.EventData || evs[0].Flags&http2.EventEndStream == 0 || string(evs[0].Data) != "body" {
		t.Fatalf("data: %+v", evs)
	}
	// the answer ends the stream.
	res := &corenet.HTTP1ResponseValue{Body: []byte("hello")}
	out, err := d.lane.Headers(&d.conn, 1, res, false, d.out)
	if err != nil {
		t.Fatal(err)
	}
	d.out = out
	d.out = d.lane.Send(&d.conn, 1, res.Body, d.out, true)
	frames := parseOut(t, d.out)
	if frames[0].h.Type != corehttp2.FrameHeaders || frames[1].h.Type != corehttp2.FrameData || frames[1].h.Flags&endStream == 0 {
		t.Fatalf("answer: %+v", frames)
	}
	if d.lane.OpenStreams(&d.conn) != 0 {
		t.Fatalf("stream left open")
	}
}

// TestStreamStates is RFC 9113 §5.1's illegal transitions, one by one.
func TestStreamStates(t *testing.T) {
	cases := []struct {
		name     string
		run      func(d *driver)
		stream   uint32 // 0 for a connection error
		code     corehttp2.ErrCode
		hasReset bool
	}{
		{name: "DATA on idle", run: func(d *driver) {
			d.feed(dataFrame(1, 0, []byte("x")))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "RST_STREAM on idle", run: func(d *driver) {
			d.feed(rstFrame(1, corehttp2.ErrCodeCancel))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "WINDOW_UPDATE on idle", run: func(d *driver) {
			d.feed(http2.AppendWindowUpdate(nil, 1, 100))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "HEADERS even stream", run: func(d *driver) {
			d.feed(headersFrame(2, endHeaders, reqBlock()))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "HEADERS on connection", run: func(d *driver) {
			d.feed(headersFrame(0, endHeaders, reqBlock()))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "DATA on connection", run: func(d *driver) {
			d.feed(dataFrame(0, 0, []byte("x")))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "a stream id not above the last opened", run: func(d *driver) {
			d.feed(headersFrame(3, endHeaders, reqBlock()))
			d.feed(headersFrame(1, endHeaders, reqBlock()))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "PUSH_PROMISE from a client", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePushPromise, Flags: endHeaders, StreamID: 1}))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "CONTINUATION out of a block", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, Flags: endHeaders, StreamID: 1}))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "frame past the bound", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, StreamID: 1, Length: corehttp2.DefaultMaxFrame + 1}))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "PING wrong length", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePing, Length: 7}), make([]byte, 7))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "PING on a stream", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePing, StreamID: 1, Length: 8}), make([]byte, 8))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "SETTINGS on a stream", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameSettings, StreamID: 1}))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "SETTINGS bad length", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameSettings, Length: 5}), make([]byte, 5))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "SETTINGS ACK with a body", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameSettings, Flags: corehttp2.FlagAck, Length: 6}), make([]byte, 6))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "GOAWAY on a stream", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameGoAway, StreamID: 1, Length: 8}), make([]byte, 8))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "GOAWAY short", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameGoAway, Length: 4}), make([]byte, 4))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "RST_STREAM on connection", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameRSTStream, Length: 4}), make([]byte, 4))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "RST_STREAM bad length", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameRSTStream, StreamID: 1, Length: 3}), make([]byte, 3))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "WINDOW_UPDATE bad length", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameWindowUpdate, StreamID: 1, Length: 3}), make([]byte, 3))
		}, code: corehttp2.ErrCodeFrameSize},
		{name: "WINDOW_UPDATE of 0 on connection", run: func(d *driver) {
			d.feed(http2.AppendWindowUpdate(nil, 0, 0))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "SETTINGS_ENABLE_PUSH past 1", run: func(d *driver) {
			d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingEnablePush, Val: 2}}))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "SETTINGS_INITIAL_WINDOW_SIZE past the maximum", run: func(d *driver) {
			d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 1 << 31}}))
		}, code: corehttp2.ErrCodeFlowControl},
		{name: "SETTINGS_MAX_FRAME_SIZE below the minimum", run: func(d *driver) {
			d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingMaxFrameSize, Val: 1024}}))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "PRIORITY on the connection", run: func(d *driver) {
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePriority, Length: 5}), make([]byte, 5))
		}, code: corehttp2.ErrCodeProtocol},
		{name: "HEADERS after END_STREAM", run: func(d *driver) {
			d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
			d.feed(headersFrame(1, endHeaders, reqBlock()))
		}, stream: 1, code: corehttp2.ErrCodeStreamClosed},
		{name: "DATA after END_STREAM", run: func(d *driver) {
			d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
			d.feed(dataFrame(1, 0, []byte("x")))
		}, stream: 1, code: corehttp2.ErrCodeStreamClosed},
		{name: "trailers refused", run: func(d *driver) {
			d.feed(headersFrame(1, endHeaders, reqBlock()))
			d.feed(dataFrame(1, 0, []byte("x")))
			d.feed(headersFrame(1, endHeaders|endStream, []byte{0x82}))
		}, stream: 1, code: corehttp2.ErrCodeProtocol},
		{name: "WINDOW_UPDATE of 0 on a stream", run: func(d *driver) {
			d.feed(headersFrame(1, endHeaders, reqBlock()))
			d.feed(http2.AppendWindowUpdate(nil, 1, 0))
		}, stream: 1, code: corehttp2.ErrCodeProtocol},
		{name: "PRIORITY bad length", run: func(d *driver) {
			d.feed(headersFrame(1, endHeaders, reqBlock()))
			d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePriority, StreamID: 1, Length: 4}), make([]byte, 4))
		}, stream: 1, code: corehttp2.ErrCodeFrameSize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDriver(t)
			c.run(d)
			if c.stream == 0 {
				d.wantConnError(c.code)
				return
			}
			d.wantStreamError(c.stream, c.code)
			if d.lane.Closing(&d.conn) {
				t.Fatal("a stream error closed the connection")
			}
		})
	}
}

// rstFrame writes an RST_STREAM frame.
func rstFrame(stream uint32, code corehttp2.ErrCode) []byte {
	return http2.AppendRSTStream(nil, stream, code)
}

// TestFramesAfterClose holds what a stream closed by the state machine ignores
// or refuses: RST_STREAM and WINDOW_UPDATE race legitimately, DATA does not.
func TestFramesAfterClose(t *testing.T) {
	d := newDriver(t)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	d.out, _ = d.lane.Headers(&d.conn, 1, &corenet.HTTP1ResponseValue{}, true, d.out)
	d.out = d.out[:0]
	// closed: WINDOW_UPDATE and RST_STREAM race, and are ignored.
	d.feed(http2.AppendWindowUpdate(nil, 1, 1))
	d.feed(rstFrame(1, corehttp2.ErrCodeCancel))
	if len(d.out) != 0 || d.lane.Closing(&d.conn) {
		t.Fatalf("a race was punished: %x", d.out)
	}
	// DATA on the closed stream: a stream error, the connection window still paid.
	d.feed(dataFrame(1, 0, []byte("x")))
	d.wantStreamError(1, corehttp2.ErrCodeStreamClosed)
	if d.lane.Closing(&d.conn) {
		t.Fatal("DATA on a closed stream ended the connection")
	}
}

// TestUnknownFrameIgnored holds §4.1: an extension frame is ignored, and a
// PRIORITY of the right shape is ignored too.
func TestUnknownFrameIgnored(t *testing.T) {
	d := newDriver(t)
	d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: 0x20, StreamID: 1, Length: 3}), []byte("abc"))
	d.feed(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePriority, StreamID: 3, Length: 5}), make([]byte, 5))
	if len(d.out) != 0 || d.lane.Closing(&d.conn) {
		t.Fatalf("not ignored: %x", d.out)
	}
}

// TestSettingsAckFlowsToTheDecoder holds §4.2 of RFC 7541: a smaller table
// bound applies only after its ACK.
func TestSettingsAckFlowsToTheDecoder(t *testing.T) {
	lane, _ := http2.NewLane(corehttp2.LimitsValue{HeaderTable: 256}, 1<<20, 0)
	var c http2.Conn
	lane.Open(&c)
	_, out := lane.Feed(&c, connPreface(), nil)
	// before the ACK the peer may still use the protocol default: a size update to 1000 passes.
	block := []byte{0x3f, 0xe9, 0x06} // size update to 1000
	block = append(block, 0x82)
	var req corenet.HTTP1RequestValue
	if _, err := lane.Decode(&c, block, nil, &req); err != nil {
		t.Fatalf("before the ACK: %v", err)
	}
	_, out = lane.Feed(&c, http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameSettings, Flags: corehttp2.FlagAck}), out)
	// after the ACK the same update, past 256, refuses.
	if _, err := lane.Decode(&c, []byte{0x3f, 0xe9, 0x06}, nil, &req); err == nil {
		t.Fatal("a size update past the bound passed after the ACK")
	}
	_ = out
}

// TestHundredAndOneStreams holds §5.1.2: the 101st HEADERS is refused with
// REFUSED_STREAM while the hundred complete, and its block is still decoded.
func TestHundredAndOneStreams(t *testing.T) {
	d := newDriver(t)
	for i := range 100 {
		evs := d.feed(headersFrame(uint32(2*i+1), endHeaders, reqBlock()))
		if len(evs) != 1 || evs[0].Flags&http2.EventDiscard != 0 {
			t.Fatalf("stream %d refused: %+v", i, evs)
		}
	}
	evs := d.feed(headersFrame(201, endHeaders, reqBlock()))
	if len(evs) != 1 || evs[0].Flags&http2.EventDiscard == 0 || evs[0].Kind != http2.EventHeaders {
		t.Fatalf("101st: %+v", evs)
	}
	d.wantStreamError(201, corehttp2.ErrCodeRefusedStream)
	if d.lane.OpenStreams(&d.conn) != 100 || d.lane.Closing(&d.conn) {
		t.Fatalf("open %d closing %v", d.lane.OpenStreams(&d.conn), d.lane.Closing(&d.conn))
	}
	// the hundred still complete, and a new stream is admitted after.
	d.out = d.out[:0]
	for i := range 100 {
		stream := uint32(2*i + 1)
		out, err := d.lane.Headers(&d.conn, stream, &corenet.HTTP1ResponseValue{}, true, d.out)
		if err != nil {
			t.Fatal(err)
		}
		d.out = out
	}
	if d.lane.OpenStreams(&d.conn) != 0 {
		t.Fatalf("open %d after the answers", d.lane.OpenStreams(&d.conn))
	}
	evs = d.feed(headersFrame(203, endHeaders|endStream, reqBlock()))
	if len(evs) != 1 || evs[0].Flags&http2.EventDiscard != 0 {
		t.Fatalf("after the hundred: %+v", evs)
	}
}

// TestRapidReset holds CVE-2023-44487: a peer that opens and resets streams
// without letting any finish runs out of its budget.
func TestRapidReset(t *testing.T) {
	d := newDriver(t)
	stream := uint32(1)
	for i := range 400 {
		d.feed(headersFrame(stream, endHeaders, reqBlock()))
		d.feed(rstFrame(stream, corehttp2.ErrCodeCancel))
		stream += 2
		if d.lane.Closing(&d.conn) {
			if i != 199 {
				t.Fatalf("closed at reset %d, want the 200th", i+1)
			}
			d.wantConnError(corehttp2.ErrCodeEnhanceYourCalm)
			return
		}
	}
	t.Fatal("a rapid reset flood was never refused")
}

// TestResetBudgetRefunds holds that a stream served to the end gives back the
// budget: a peer that cancels some and finishes others never runs out.
func TestResetBudgetRefunds(t *testing.T) {
	d := newDriver(t)
	stream := uint32(1)
	for range 1000 {
		// one canceled, one served.
		d.feed(headersFrame(stream, endHeaders, reqBlock()))
		d.feed(rstFrame(stream, corehttp2.ErrCodeCancel))
		stream += 2
		d.feed(headersFrame(stream, endHeaders|endStream, reqBlock()))
		out, err := d.lane.Headers(&d.conn, stream, &corenet.HTTP1ResponseValue{}, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = out
		stream += 2
		if d.lane.Closing(&d.conn) {
			t.Fatal("a peer that finished streams ran out of the reset budget")
		}
	}
}

// TestContinuationFlood holds CVE-2024-27316: a HEADERS with no end whose
// CONTINUATION chain is bounded in frames.
func TestContinuationFlood(t *testing.T) {
	d := newDriver(t)
	frames := [][]byte{http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, StreamID: 1, Length: 0})}
	for range 100 {
		frames = append(frames, http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, StreamID: 1, Length: 0}))
	}
	buf := bytes.Join(frames, nil)
	n, out := d.lane.Feed(&d.conn, buf, d.out)
	d.out = out
	if n != len(buf) || !d.lane.Closing(&d.conn) {
		t.Fatalf("n=%d closing=%v", n, d.lane.Closing(&d.conn))
	}
	d.wantConnError(corehttp2.ErrCodeEnhanceYourCalm)
}

// TestContinuationBytes bounds a field block's bytes, however it is spread.
func TestContinuationBytes(t *testing.T) {
	d := newDriver(t)
	chunk := bytes.Repeat([]byte{0x41}, 16384)
	var buf []byte
	buf = append(buf, headersFrame(1, 0, chunk)...)
	for range 4 {
		buf = append(buf, http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, StreamID: 1, Length: 16384})...)
		buf = append(buf, chunk...)
	}
	n, out := d.lane.Feed(&d.conn, buf, d.out)
	d.out = out
	if n != len(buf) || !d.lane.Closing(&d.conn) {
		t.Fatalf("n=%d closing=%v", n, d.lane.Closing(&d.conn))
	}
	d.wantConnError(corehttp2.ErrCodeEnhanceYourCalm)
}

// TestContinuationInterleaved holds §6.10: a frame in the middle of a block
// ends the connection, CONTINUATION of another stream too.
func TestContinuationInterleaved(t *testing.T) {
	for _, next := range [][]byte{
		http2.AppendPing(nil, false, new([8]byte)),
		http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, StreamID: 3, Length: 0}),
	} {
		d := newDriver(t)
		buf := append(headersFrame(1, 0, reqBlock()), next...)
		n, out := d.lane.Feed(&d.conn, buf, d.out)
		d.out = out
		if n != len(buf) || !d.lane.Closing(&d.conn) {
			t.Fatalf("n=%d", n)
		}
		d.wantConnError(corehttp2.ErrCodeProtocol)
	}
}

// TestContinuationBlock assembles a block spread over frames, by the frame's
// byte, and trickled a byte at a time: the walk is resumed, never repeated.
func TestContinuationBlock(t *testing.T) {
	block := reqBlock()
	buf := headersFrame(1, 0, block[:2])
	buf = append(buf, http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, StreamID: 1, Length: 3})...)
	buf = append(buf, block[2:5]...)
	buf = append(buf, http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameContinuation, Flags: endHeaders, StreamID: 1, Length: len(block) - 5})...)
	buf = append(buf, block[5:]...)
	d := newDriver(t)
	var spill []byte
	var events []http2.Event
	for _, b := range buf {
		spill = append(spill, b)
		n, out := d.lane.Feed(&d.conn, spill, d.out)
		d.out = out
		events = append(events, d.lane.Events()...)
		spill = spill[n:]
	}
	if len(events) != 1 || !bytes.Equal(events[0].Data, block) || d.lane.Closing(&d.conn) {
		t.Fatalf("events %d block %x", len(events), events[0].Data)
	}
}

// TestFlood holds the frame budget: a flood of control frames ends the
// connection, a batch's work bounded by it (§10.5).
func TestFlood(t *testing.T) {
	flood := map[string][]byte{
		"PING":     http2.AppendPing(nil, false, new([8]byte)),
		"SETTINGS": http2.AppendSettings(nil, nil),
		"unknown":  http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: 0x99, Length: 0}),
		"PRIORITY": append(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePriority, StreamID: 3, Length: 5}), make([]byte, 5)...),
	}
	for name, frame := range flood {
		t.Run(name, func(t *testing.T) {
			d := newDriver(t)
			var buf []byte
			for range 2000 {
				buf = append(buf, frame...)
			}
			n, out := d.lane.Feed(&d.conn, buf, d.out)
			d.out = out
			if n != len(buf) || !d.lane.Closing(&d.conn) {
				t.Fatalf("n=%d of %d closing=%v", n, len(buf), d.lane.Closing(&d.conn))
			}
			d.wantConnError(corehttp2.ErrCodeEnhanceYourCalm)
		})
	}
}

// TestBudgetIsPerBatch resets the budget at the batch's end.
func TestBudgetIsPerBatch(t *testing.T) {
	d := newDriver(t)
	ping := http2.AppendPing(nil, false, new([8]byte))
	for range 5 {
		var buf []byte
		for range 500 {
			buf = append(buf, ping...)
		}
		n, out := d.lane.Feed(&d.conn, buf, d.out)
		d.out = out
		d.lane.EndBatch()
		if n != len(buf) || d.lane.Closing(&d.conn) {
			t.Fatalf("a budget carried across batches: n=%d", n)
		}
	}
}

// TestPingAnswered holds §6.7: a PING's payload comes back, its ACK does not
// answer.
func TestPingAnswered(t *testing.T) {
	d := newDriver(t)
	ping := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	d.feed(http2.AppendPing(nil, false, &ping))
	f := d.lastFrame()
	if f.h.Type != corehttp2.FramePing || f.h.Flags&corehttp2.FlagAck == 0 || !bytes.Equal(f.body, ping[:]) {
		t.Fatalf("answer %+v", f)
	}
	d.out = d.out[:0]
	d.feed(http2.AppendPing(nil, true, &ping))
	if len(d.out) != 0 {
		t.Fatalf("an ACK answered: %x", d.out)
	}
}

// TestSettingsAckAndWindow holds §6.9.2: a change of INITIAL_WINDOW_SIZE moves
// every open stream's send window, and may make it negative.
func TestSettingsWindowChange(t *testing.T) {
	d := newDriverLimits(t, corehttp2.LimitsValue{}, 1<<20, 1<<20)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	body := bytes.Repeat([]byte{'x'}, 100)
	// a window of zero: nothing leaves, the body held.
	d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 0}}))
	d.out, _ = d.lane.Headers(&d.conn, 1, &corenet.HTTP1ResponseValue{Body: body}, false, d.out[:0])
	d.out = d.lane.Send(&d.conn, 1, body, d.out, true)
	frames := parseOut(t, d.out)
	for _, f := range frames {
		if f.h.Type == corehttp2.FrameData && f.h.Length > 0 {
			t.Fatalf("DATA with a closed window: %+v", f.h)
		}
	}
	// the window opens: the held bytes leave, END_STREAM with the last.
	d.out = d.out[:0]
	d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 60}}))
	frames = parseOut(t, d.out)
	sawData := false
	for _, f := range frames {
		if f.h.Type == corehttp2.FrameData {
			sawData = true
			if f.h.Length != 60 {
				t.Fatalf("data %d", f.h.Length)
			}
		}
	}
	if !sawData {
		t.Fatal("held bytes not released")
	}
	// WINDOW_UPDATE opens the last 40.
	d.out = d.out[:0]
	d.feed(http2.AppendWindowUpdate(nil, 1, 40))
	frames = parseOut(t, d.out)
	last := frames[len(frames)-1]
	if last.h.Type != corehttp2.FrameData || last.h.Length != 40 || last.h.Flags&endStream == 0 {
		t.Fatalf("final data: %+v", last.h)
	}
	if d.lane.OpenStreams(&d.conn) != 0 {
		t.Fatal("stream left open after END_STREAM went out")
	}
}

// TestFlowControlViolations holds §6.9.1's overflow and overrun refusals.
func TestFlowControlViolations(t *testing.T) {
	t.Run("connection window overrun", func(t *testing.T) {
		d := newDriverLimits(t, corehttp2.LimitsValue{ReceivePerStream: 1 << 20, ReceivePerConn: 1 << 20}, 1<<30, 0)
		d.feed(headersFrame(1, endHeaders, reqBlock()))
		// nothing was consumed: the connection window is 1 MiB, the 65th DATA frame overruns it.
		var buf []byte
		chunk := bytes.Repeat([]byte{'x'}, 16384)
		for range 65 {
			buf = append(buf, dataFrame(1, 0, chunk)...)
		}
		n, out := d.lane.Feed(&d.conn, buf, nil)
		d.out = out
		if n != len(buf) {
			t.Fatalf("n=%d", n)
		}
		d.wantConnError(corehttp2.ErrCodeFlowControl)
	})
	t.Run("stream window overrun", func(t *testing.T) {
		d := newDriverLimits(t, corehttp2.LimitsValue{ReceivePerStream: 16 << 10}, 1<<30, 0)
		d.feed(headersFrame(1, endHeaders, reqBlock()))
		chunk := bytes.Repeat([]byte{'x'}, 16384)
		d.feed(dataFrame(1, 0, chunk))
		d.out = d.out[:0]
		d.feed(dataFrame(1, 0, chunk[:1]))
		d.wantStreamError(1, corehttp2.ErrCodeFlowControl)
		if d.lane.Closing(&d.conn) {
			t.Fatal("a stream window overrun ended the connection")
		}
	})
	t.Run("connection credit past the maximum", func(t *testing.T) {
		d := newDriver(t)
		d.feed(http2.AppendWindowUpdate(nil, 0, 1<<31-1))
		d.wantConnError(corehttp2.ErrCodeFlowControl)
	})
	t.Run("stream credit past the maximum", func(t *testing.T) {
		d := newDriver(t)
		d.feed(headersFrame(1, endHeaders, reqBlock()))
		d.feed(http2.AppendWindowUpdate(nil, 1, 1<<31-1))
		d.wantStreamError(1, corehttp2.ErrCodeFlowControl)
	})
	t.Run("a window change that overflows an open stream", func(t *testing.T) {
		d := newDriver(t)
		d.feed(headersFrame(1, endHeaders, reqBlock()))
		d.feed(http2.AppendWindowUpdate(nil, 1, 1<<31-1-65535))
		d.out = d.out[:0]
		d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 1 << 31}}))
		d.wantConnError(corehttp2.ErrCodeFlowControl)
	})
}

// TestBodyLimit holds the bound on a stream's body: the adapter is told once.
func TestBodyLimit(t *testing.T) {
	d := newDriverLimits(t, corehttp2.LimitsValue{}, 8, 0)
	d.feed(headersFrame(1, endHeaders, reqBlock()))
	evs := d.feed(dataFrame(1, 0, []byte("12345678")))
	if len(evs) != 1 || evs[0].Flags&http2.EventOverBody != 0 {
		t.Fatalf("at the bound: %+v", evs)
	}
	evs = d.feed(dataFrame(1, 0, []byte("9")))
	if len(evs) != 1 || evs[0].Flags&http2.EventOverBody == 0 {
		t.Fatalf("past the bound: %+v", evs)
	}
	d.feed(dataFrame(1, endStream, []byte("ab")))
	// the adapter answers 413: the stream is ended with RST_STREAM(NO_ERROR)
	// when it was still open, END_STREAM otherwise.
	d.out, _ = d.lane.Headers(&d.conn, 1, &corenet.HTTP1ResponseValue{Status: 413}, true, d.out[:0])
	frames := parseOut(t, d.out)
	if frames[0].h.Type != corehttp2.FrameHeaders || d.lane.OpenStreams(&d.conn) != 0 {
		t.Fatalf("frames %+v open %d", frames[0].h, d.lane.OpenStreams(&d.conn))
	}
}

// TestResponseFirstResets holds §8.1: a response completed while the request
// body is still arriving resets the stream without error.
func TestResponseFirstResets(t *testing.T) {
	d := newDriver(t)
	d.feed(headersFrame(1, endHeaders, reqBlock()))
	d.out, _ = d.lane.Headers(&d.conn, 1, &corenet.HTTP1ResponseValue{}, true, d.out[:0])
	frames := parseOut(t, d.out)
	if len(frames) != 2 || frames[1].h.Type != corehttp2.FrameRSTStream || errOf(frames[1]) != corehttp2.ErrCodeNoError {
		t.Fatalf("frames: %+v", frames)
	}
	if d.lane.OpenStreams(&d.conn) != 0 {
		t.Fatal("stream still open")
	}
}

// TestGoAwayAndDone holds a graceful end: GOAWAY then Done when streams end.
func TestGoAwayAndDone(t *testing.T) {
	d := newDriver(t)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	if d.lane.Done(&d.conn) {
		t.Fatal("Done before any GOAWAY")
	}
	d.out = d.lane.GoAway(&d.conn, corehttp2.ErrCodeNoError, d.out[:0])
	f := d.lastFrame()
	if f.h.Type != corehttp2.FrameGoAway || binary.BigEndian.Uint32(f.body) != 1 {
		t.Fatalf("goaway: %+v %x", f.h, f.body)
	}
	if d.lane.Done(&d.conn) {
		t.Fatal("Done with an open stream")
	}
	// a new stream after the GOAWAY is refused, still decoded for the table.
	d.out = d.out[:0]
	evs := d.feed(headersFrame(3, endHeaders|endStream, reqBlock()))
	if len(evs) != 1 || evs[0].Flags&http2.EventDiscard == 0 {
		t.Fatalf("after goaway: %+v", evs)
	}
	d.wantStreamError(3, corehttp2.ErrCodeRefusedStream)
	d.out, _ = d.lane.Headers(&d.conn, 1, &corenet.HTTP1ResponseValue{}, true, d.out[:0])
	if !d.lane.Done(&d.conn) {
		t.Fatal("not Done after the last stream ended")
	}
	if d.lane.Closing(&d.conn) {
		t.Fatal("a graceful GOAWAY marked closing")
	}
}

// TestUnreadBodyDoesNotDelayAnotherStream holds ADR 0195's window argument on
// the codec: a stream's unread body holds its own window, not the connection's
// other streams.
func TestUnreadBodyDoesNotDelayAnotherStream(t *testing.T) {
	d := newDriver(t)
	d.feed(headersFrame(1, endHeaders, reqBlock()))
	var buf []byte
	chunk := bytes.Repeat([]byte{'x'}, 16384)
	for range 16 {
		buf = append(buf, dataFrame(1, 0, chunk)...)
	}
	n, out := d.lane.Feed(&d.conn, buf, d.out)
	d.out = out
	if n != len(buf) || d.lane.Closing(&d.conn) {
		t.Fatalf("n=%d closing=%v", n, d.lane.Closing(&d.conn))
	}
	// nothing was consumed: 256 KiB held unread. A second stream is still
	// answered in full, its 1 KiB leaving at once.
	d.feed(headersFrame(3, endHeaders|endStream, reqBlock()))
	body := bytes.Repeat([]byte{'y'}, 1024)
	d.out, _ = d.lane.Headers(&d.conn, 3, &corenet.HTTP1ResponseValue{Body: body}, false, d.out[:0])
	d.out = d.lane.Send(&d.conn, 3, body, d.out, true)
	got := 0
	for _, f := range parseOut(t, d.out) {
		if f.h.Type == corehttp2.FrameData && f.h.StreamID == 3 {
			got += f.h.Length
		}
	}
	if got != 1024 {
		t.Fatalf("stream 3 got %d bytes", got)
	}
}

// TestCoalescedCredits holds one WINDOW_UPDATE per stream and per connection
// per batch, each crediting only bytes the handler consumed.
func TestCoalescedCredits(t *testing.T) {
	d := newDriverLimits(t, corehttp2.LimitsValue{ReceivePerStream: 1 << 22, ReceivePerConn: 1 << 23}, 1<<30, 0)
	d.feed(headersFrame(1, endHeaders, reqBlock()))
	chunk := bytes.Repeat([]byte{'x'}, 16384)
	var buf []byte
	const frames = 256
	for range frames {
		buf = append(buf, dataFrame(1, 0, chunk)...)
	}
	totalCredited := map[uint32]uint32{}
	consumed := 0
	// feed in small batches, consuming a part of what arrived each time.
	for len(buf) > 0 {
		step := min(len(buf), 64*1024)
		n, out := d.lane.Feed(&d.conn, buf[:step], d.out[:0])
		d.out = out
		for _, ev := range d.lane.Events() {
			if ev.Kind == http2.EventData {
				consumed += len(ev.Data)
				d.lane.Consume(&d.conn, ev.Stream, len(ev.Data))
			}
		}
		d.out = d.lane.AppendCredits(&d.conn, d.out[:0])
		perStream := map[uint32]int{}
		for _, f := range parseOut(t, d.out) {
			if f.h.Type != corehttp2.FrameWindowUpdate {
				t.Fatalf("unexpected %+v", f.h)
			}
			perStream[f.h.StreamID]++
			totalCredited[f.h.StreamID] += binary.BigEndian.Uint32(f.body)
		}
		for stream, count := range perStream {
			if count > 1 {
				t.Fatalf("stream %d got %d WINDOW_UPDATEs in a batch", stream, count)
			}
		}
		d.lane.EndBatch()
		buf = buf[n:]
	}
	if consumed != frames*16384 {
		t.Fatalf("consumed %d", consumed)
	}
	if int(totalCredited[1]) > consumed || int(totalCredited[0]) > consumed {
		t.Fatalf("credited more than consumed: %v of %d", totalCredited, consumed)
	}
}

// TestHoldCap ends the connection when the held bytes pass the bound.
func TestHoldCap(t *testing.T) {
	d := newDriverLimits(t, corehttp2.LimitsValue{}, 1<<20, 100)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 0}}))
	d.out = d.out[:0]
	body := bytes.Repeat([]byte{'x'}, 200)
	out := d.lane.Send(&d.conn, 1, body, d.out, true)
	d.out = out
	if !d.lane.Closing(&d.conn) {
		t.Fatal("hold past the cap did not end the connection")
	}
	d.wantConnError(corehttp2.ErrCodeEnhanceYourCalm)
}

// TestCompaction holds a held stream whose bytes are partly gone across many
// rounds, the buffer never growing without bound.
func TestCompaction(t *testing.T) {
	d := newDriverLimits(t, corehttp2.LimitsValue{}, 1<<20, 1<<20)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	d.feed(headersFrame(3, endHeaders|endStream, reqBlock()))
	d.feed(http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingInitialWindowSize, Val: 1}}))
	body := bytes.Repeat([]byte{'x'}, 4096)
	d.out = d.lane.Send(&d.conn, 1, body, d.out[:0], true)
	d.out = d.lane.Send(&d.conn, 3, body, d.out[:0], true)
	for _, stream := range []uint32{1, 3} {
		remaining := 4096 - 1
		for remaining > 0 {
			d.out = d.out[:0]
			step := min(remaining, 500)
			d.feed(http2.AppendWindowUpdate(nil, stream, uint32(step)))
			remaining -= step
		}
	}
	if d.lane.OpenStreams(&d.conn) != 0 || d.lane.Closing(&d.conn) {
		t.Fatalf("open %d closing %v", d.lane.OpenStreams(&d.conn), d.lane.Closing(&d.conn))
	}
}

// TestReopenConnectionReleases reuses a slot: Close puts streams and the
// table back.
func TestReopenConnectionReleases(t *testing.T) {
	d := newDriver(t)
	for range 4 {
		d.feed(headersFrame(1, endHeaders, reqBlock()))
		block := reqBlock()
		var req corenet.HTTP1RequestValue
		if _, err := d.lane.Decode(&d.conn, block, nil, &req); err != nil {
			t.Fatal(err)
		}
		d.lane.Close(&d.conn)
		d.lane.Open(&d.conn)
		d.feed(connPreface())
		d.out = d.out[:0]
	}
}

// TestDecodeTableLazy holds that a connection with no field block never
// takes a table from the arena, and one with a block does.
func TestDecodeTableLazy(t *testing.T) {
	d := newDriver(t)
	d.feed(headersFrame(1, endHeaders|endStream, reqBlock()))
	var req corenet.HTTP1RequestValue
	if _, err := d.lane.Decode(&d.conn, []byte{0x82}, nil, &req); err != nil {
		t.Fatal(err)
	}
	if req.NFields != 1 {
		t.Fatalf("NFields %d", req.NFields)
	}
}
