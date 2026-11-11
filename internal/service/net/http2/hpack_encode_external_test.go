package http2_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// decodeResponse reads a whole response block back: frames, HEADERS then
// CONTINUATIONs, into fields through a fresh decoder.
func decodeResponse(t *testing.T, buf []byte) ([]string, corehttp2.FrameValue) {
	t.Helper()
	var block []byte
	var first corehttp2.FrameValue
	for frame := 0; ; frame++ {
		h, n, code := http2.ParseFrameHeader(buf, 16384)
		if n == 0 || code != corehttp2.ErrCodeNoError {
			t.Fatalf("frame %d: n=%d code=%s", frame, n, code)
		}
		if frame == 0 {
			first = h
		}
		block = append(block, buf[n:n+h.Length]...)
		buf = buf[n+h.Length:]
		if h.Flags&corehttp2.FlagEndHeaders != 0 {
			break
		}
	}
	d, mem := newDecoder(4096, 1<<20)
	req, _, err := decode(d, mem, block)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return fieldsOf(req), first
}

// TestAppendHeaders writes :status, fields lowercased, and content-length.
func TestAppendHeaders(t *testing.T) {
	res := &corenet.HTTP1ResponseValue{
		Status: 404,
		Header: []byte("Content-Type: text/plain\r\nX-Hello: world\r\n"),
		Body:   []byte("nope"),
	}
	out, err := http2.NewEncoder(16384).AppendHeaders(nil, 3, res, true)
	if err != nil {
		t.Fatal(err)
	}
	fields, h := decodeResponse(t, out)
	want := []string{":status=404", "content-type=text/plain", "x-hello=world", "content-length=4"}
	if strings.Join(fields, ",") != strings.Join(want, ",") {
		t.Fatalf("fields %v", fields)
	}
	if h.Type != corehttp2.FrameHeaders || h.StreamID != 3 || h.Flags&corehttp2.FlagEndStream == 0 || h.Flags&corehttp2.FlagEndHeaders == 0 {
		t.Fatalf("header %+v", h)
	}
}

// TestAppendHeadersStatus reads each status, indexed and literal.
func TestAppendHeadersStatus(t *testing.T) {
	for _, status := range []int{0, 200, 204, 206, 304, 400, 404, 500, 201, 301, 599, 999} {
		res := &corenet.HTTP1ResponseValue{Status: status}
		out, err := http2.NewEncoder(16384).AppendHeaders(nil, 1, res, true)
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		fields, _ := decodeResponse(t, out)
		want := status
		if want == 0 {
			want = 200
		}
		if !strings.HasPrefix(fields[0], ":status=") || fields[0] != ":status="+itoa(want) {
			t.Fatalf("%d: %v", status, fields)
		}
	}
}

// itoa is strconv.Itoa for a small positive status.
func itoa(n int) string {
	return strings.TrimSpace(strings.Join([]string{string(rune('0' + n/100)), string(rune('0' + n/10%10)), string(rune('0' + n%10))}, ""))
}

// TestAppendHeadersDropsConnectionFields holds RFC 9113 §8.2.2 and the codec's
// ownership of the length.
func TestAppendHeadersDropsConnectionFields(t *testing.T) {
	res := &corenet.HTTP1ResponseValue{
		Header: []byte("Connection: close\r\nKeep-Alive: x\r\nProxy-Connection: y\r\nTransfer-Encoding: z\r\nUpgrade: h2c\r\nContent-Length: 99\r\nX-Kept: 1\r\n"),
		Body:   []byte("ab"),
	}
	out, err := http2.NewEncoder(16384).AppendHeaders(nil, 1, res, false)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := decodeResponse(t, out)
	got := strings.Join(fields, ",")
	if got != ":status=200,x-kept=1,content-length=2" {
		t.Fatalf("fields %q", got)
	}
}

// TestAppendHeadersRefusals holds what a response may not be, nothing appended.
func TestAppendHeadersRefusals(t *testing.T) {
	cases := map[string]*corenet.HTTP1ResponseValue{
		"interim status":  {Status: 103},
		"status 1000":     {Status: 1000},
		"204 with a body": {Status: 204, Body: []byte("x")},
		"304 with a body": {Status: 304, Body: []byte("x")},
		"no colon":        {Header: []byte("NoColon\r\n")},
		"no CRLF":         {Header: []byte("A: b\n")},
		"CR in a value":   {Header: []byte("A: b\rc\r\n")},
		"NUL in a value":  {Header: []byte("A: b\x00c\r\n")},
		"empty name":      {Header: []byte(": b\r\n")},
	}
	for name, res := range cases {
		dst := []byte("kept")
		out, err := http2.NewEncoder(16384).AppendHeaders(dst, 1, res, false)
		if !errors.Is(err, corenet.HTTP1ResponseInvalid) || !bytes.Equal(out, []byte("kept")) {
			t.Fatalf("%s: %v %q", name, err, out)
		}
	}
}

// TestAppendHeadersSplits frames a block past the peer's frame size over
// HEADERS and CONTINUATION, in order, reading back every field.
func TestAppendHeadersSplits(t *testing.T) {
	var hdr bytes.Buffer
	want := []string{":status=200"}
	for i := range 40 {
		name := "x-f-" + itoa(100+i)
		value := strings.Repeat("v", 500)
		hdr.WriteString(name + ": " + value + "\r\n")
		want = append(want, name+"="+value)
	}
	want = append(want, "content-length=0")
	res := &corenet.HTTP1ResponseValue{Header: hdr.Bytes()}
	const maxFrame = 16384
	out, err := http2.NewEncoder(maxFrame).AppendHeaders(nil, 9, res, true)
	if err != nil {
		t.Fatal(err)
	}
	// walk the frames: one HEADERS without END_HEADERS, at least one
	// CONTINUATION, the last with END_HEADERS.
	buf := out
	frames := 0
	sawEnd := false
	for len(buf) > 0 {
		h, n, code := http2.ParseFrameHeader(buf, maxFrame)
		if n == 0 || code != corehttp2.ErrCodeNoError {
			t.Fatalf("frame %d: %d %s", frames, n, code)
		}
		if frames == 0 && (h.Type != corehttp2.FrameHeaders || h.Flags&corehttp2.FlagEndHeaders != 0) {
			t.Fatalf("first frame: %+v", h)
		}
		if frames > 0 && h.Type != corehttp2.FrameContinuation {
			t.Fatalf("frame %d: %+v", frames, h)
		}
		if sawEnd {
			t.Fatal("a frame after END_HEADERS")
		}
		sawEnd = h.Flags&corehttp2.FlagEndHeaders != 0
		buf = buf[n+h.Length:]
		frames++
	}
	if frames < 2 || !sawEnd {
		t.Fatalf("%d frames, end %v", frames, sawEnd)
	}
	fields, first := decodeResponse(t, out)
	if strings.Join(fields, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fields %d, want %d", len(fields), len(want))
	}
	if first.Flags&corehttp2.FlagEndStream == 0 {
		t.Fatalf("END_STREAM not on the HEADERS frame")
	}
}

// TestAppendHeadersWholeLine keeps a value that holds a colon whole.
func TestAppendHeadersWholeLine(t *testing.T) {
	res := &corenet.HTTP1ResponseValue{Status: 200, Header: []byte("Location: http://example.test:8080/x\r\n")}
	out, err := http2.NewEncoder(16384).AppendHeaders(nil, 1, res, true)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := decodeResponse(t, out)
	if fields[1] != "location=http://example.test:8080/x" {
		t.Fatalf("%v", fields)
	}
}
