package http2_test

import (
	"bytes"
	"testing"

	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// TestFrameHeaderRoundTrip reads what the writer wrote, on the reserved bit too.
func TestFrameHeaderRoundTrip(t *testing.T) {
	cases := []corehttp2.FrameValue{
		{Type: corehttp2.FrameData, Flags: corehttp2.FlagEndStream, StreamID: 1, Length: 0},
		{Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagEndHeaders, StreamID: 3, Length: 16384},
		{Type: corehttp2.FrameContinuation, StreamID: 1<<31 - 1, Length: 1},
		{Type: 0xab, Flags: 0xff, StreamID: 7, Length: 1},
	}
	for _, c := range cases {
		buf := http2.AppendFrameHeader(nil, c)
		got, n, code := http2.ParseFrameHeader(buf, corehttp2.MaxFrameLimit)
		if n != corehttp2.FrameHeaderLen || code != corehttp2.ErrCodeNoError || got != c {
			t.Fatalf("%+v: got %+v n=%d code=%s", c, got, n, code)
		}
	}
	// the reserved bit of the stream field is ignored.
	buf := http2.AppendFrameHeader(nil, corehttp2.FrameValue{StreamID: 5})
	buf[5] |= 0x80
	if got, _, _ := http2.ParseFrameHeader(buf, corehttp2.DefaultMaxFrame); got.StreamID != 5 {
		t.Fatalf("reserved bit read: %d", got.StreamID)
	}
}

// TestParseFrameHeaderShort needs nine bytes, and a padded frame's tenth.
func TestParseFrameHeaderShort(t *testing.T) {
	full := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Length: 10, Flags: corehttp2.FlagPadded})
	full = append(full, 3)
	for k := range len(full) {
		if _, n, code := http2.ParseFrameHeader(full[:k], corehttp2.DefaultMaxFrame); n != 0 || code != corehttp2.ErrCodeNoError {
			t.Fatalf("prefix %d: n=%d code=%s", k, n, code)
		}
	}
	if _, n, code := http2.ParseFrameHeader(full, corehttp2.DefaultMaxFrame); n != corehttp2.FrameHeaderLen || code != corehttp2.ErrCodeNoError {
		t.Fatalf("whole: n=%d code=%s", n, code)
	}
}

// TestParseFrameHeaderRefusals holds what the header alone refuses.
func TestParseFrameHeaderRefusals(t *testing.T) {
	cases := []struct {
		name  string
		frame corehttp2.FrameValue
		pad   int
		want  corehttp2.ErrCode
	}{
		{"over the frame bound", corehttp2.FrameValue{Type: corehttp2.FrameData, Length: corehttp2.DefaultMaxFrame + 1}, -1, corehttp2.ErrCodeFrameSize},
		{"padding fills the payload", corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: corehttp2.FlagPadded, Length: 5}, 5, corehttp2.ErrCodeProtocol},
		{"padding past the payload", corehttp2.FrameValue{Type: corehttp2.FrameHeaders, Flags: corehttp2.FlagPadded, Length: 5}, 200, corehttp2.ErrCodeProtocol},
		{"padded frame with no payload", corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: corehttp2.FlagPadded, Length: 0}, -1, corehttp2.ErrCodeProtocol},
	}
	for _, c := range cases {
		buf := http2.AppendFrameHeader(nil, c.frame)
		if c.pad >= 0 {
			buf = append(buf, byte(c.pad))
		}
		if _, _, code := http2.ParseFrameHeader(buf, corehttp2.DefaultMaxFrame); code != c.want {
			t.Fatalf("%s: code %s, want %s", c.name, code, c.want)
		}
	}
	// a frame exactly at the bound is accepted, and padding of length-1 too.
	at := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, Length: corehttp2.DefaultMaxFrame})
	if _, _, code := http2.ParseFrameHeader(at, corehttp2.DefaultMaxFrame); code != corehttp2.ErrCodeNoError {
		t.Fatalf("at the bound: %s", code)
	}
	pad := append(http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, Flags: corehttp2.FlagPadded, Length: 5}), 4)
	if _, _, code := http2.ParseFrameHeader(pad, corehttp2.DefaultMaxFrame); code != corehttp2.ErrCodeNoError {
		t.Fatalf("padding length-1: %s", code)
	}
}

// TestAppendFrames checks the writers' layouts, read back by the parser.
func TestAppendFrames(t *testing.T) {
	ping := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	cases := []struct {
		name  string
		frame []byte
		want  corehttp2.FrameValue
		body  []byte
	}{
		{"settings", http2.AppendSettings(nil, []corehttp2.SettingValue{{ID: corehttp2.SettingMaxFrameSize, Val: 1 << 20}}), corehttp2.FrameValue{Type: corehttp2.FrameSettings, Length: 6}, []byte{0, 5, 0, 16, 0, 0}},
		{"settings ack", http2.AppendSettingsAck(nil), corehttp2.FrameValue{Type: corehttp2.FrameSettings, Flags: corehttp2.FlagAck}, nil},
		{"window update", http2.AppendWindowUpdate(nil, 3, 42), corehttp2.FrameValue{Type: corehttp2.FrameWindowUpdate, StreamID: 3, Length: 4}, []byte{0, 0, 0, 42}},
		{"rst", http2.AppendRSTStream(nil, 3, corehttp2.ErrCodeCancel), corehttp2.FrameValue{Type: corehttp2.FrameRSTStream, StreamID: 3, Length: 4}, []byte{0, 0, 0, 8}},
		{"goaway", http2.AppendGoAway(nil, 7, corehttp2.ErrCodeEnhanceYourCalm), corehttp2.FrameValue{Type: corehttp2.FrameGoAway, Length: 8}, []byte{0, 0, 0, 7, 0, 0, 0, 11}},
		{"ping", http2.AppendPing(nil, false, &ping), corehttp2.FrameValue{Type: corehttp2.FramePing, Length: 8}, ping[:]},
		{"ping ack", http2.AppendPing(nil, true, &ping), corehttp2.FrameValue{Type: corehttp2.FramePing, Flags: corehttp2.FlagAck, Length: 8}, ping[:]},
	}
	for _, c := range cases {
		got, n, code := http2.ParseFrameHeader(c.frame, corehttp2.DefaultMaxFrame)
		if n != corehttp2.FrameHeaderLen || code != corehttp2.ErrCodeNoError || got != c.want {
			t.Fatalf("%s: got %+v n=%d code=%s", c.name, got, n, code)
		}
		if !bytes.Equal(c.frame[n:], c.body) {
			t.Fatalf("%s: body %x, want %x", c.name, c.frame[n:], c.body)
		}
	}
}
