package http2

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// hpackSeeds are valid and broken blocks the fuzz starts from.
func hpackSeeds() [][]byte {
	return [][]byte{
		hexMust("828684410f7777772e6578616d706c652e636f6d"),
		hexMust("828684be58086e6f2d6361636865"),
		hexMust("828785bf400a637573746f6d2d6b65790c637573746f6d2d76616c7565"),
		hexMust("828684418cf1e3c2e5f23a6ba0ab90f4ff"),
		hexMust("4803333032"),
		hexMust("88c16196d07abe941054d444a8200595040b8166e084a62d1bff"),
		{0x80},
		{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x20},
		{0x3f, 0xff, 0x7f, 0x01},
		{0x40, 0x81, 0xff, 0x01, 'v'},
		bytes.Repeat([]byte{0xbe}, 64),
	}
}

// hexMust decodes a hex string at test-setup time.
func hexMust(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := range out {
		out[i] = hexNibble(s[2*i])<<4 | hexNibble(s[2*i+1])
	}
	return out
}

// hexNibble decodes one hex digit.
func hexNibble(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'a' + 10
}

// inBytes reports whether s is empty or lies within base.
func inBytes(base, s []byte) bool {
	if len(s) == 0 {
		return true
	}
	if len(base) == 0 {
		return false
	}
	lo, hi := uintptr(unsafe.Pointer(&base[0])), uintptr(unsafe.Pointer(&base[len(base)-1]))
	first, last := uintptr(unsafe.Pointer(&s[0])), uintptr(unsafe.Pointer(&s[len(s)-1]))
	return first >= lo && last <= hi
}

// checkFields holds what Decode promises of every field whatever the block: no
// field outside its allowed homes, at most HTTP1MaxFields, and the list size
// no more than the ceiling plus one field.
func checkFields(t *testing.T, req *corenet.HTTP1RequestValue, block, scratch []byte, maxList int) {
	t.Helper()
	if req.NFields > corenet.HTTP1MaxFields {
		t.Fatalf("%d fields", req.NFields)
	}
	list := 0
	for i := range req.NFields {
		f := req.Fields[i]
		for _, s := range [][]byte{f.Name, f.Value} {
			if !inBytes(block, s) && !inBytes(scratch, s) && !inBytes(staticBytes[:], s) {
				t.Fatalf("field %d aliases outside block, scratch and static: %q", i, s)
			}
		}
		list += len(f.Name) + len(f.Value) + 32
	}
	if list > maxList {
		t.Fatalf("list %d past the ceiling %d", list, maxList)
	}
}

// FuzzDecode holds the decoder's safety on hostile blocks: no panic, every
// slice inside its homes, the ceiling held, the table's bookkeeping sane, and
// the table usable afterwards.
func FuzzDecode(f *testing.F) {
	for _, s := range hpackSeeds() {
		f.Add(s, uint16(8192), s)
	}
	f.Fuzz(func(t *testing.T, block []byte, listBound uint16, second []byte) {
		maxList := 64 + int(listBound)%8192
		d := NewDecoder(4096, maxList)
		mem := make([]byte, TableMem(4096))
		var req corenet.HTTP1RequestValue
		scratch, err := d.Decode(mem, block, make([]byte, 0, 256), &req)
		checkFields(t, &req, block, scratch, maxList)
		if d.size < 0 || d.size > d.maxSize || d.count < 0 {
			t.Fatalf("table: size %d max %d count %d", d.size, d.maxSize, d.count)
		}
		// a malformed block ends the connection: a further block must not run. Otherwise, a
		// second block sees the table the first one left.
		if err == nil {
			scratch2, err2 := d.Decode(mem, second, make([]byte, 0, 256), &req)
			_ = err2
			checkFields(t, &req, second, scratch2, maxList)
		}
	})
}

// FuzzParseFrameHeader holds the frame reader's reading of every prefix: no
// panic, the same verdict once enough bytes are there, and a length never past
// the frame bound on acceptance.
func FuzzParseFrameHeader(f *testing.F) {
	f.Add([]byte{0, 0, 5, 0, 9, 0, 0, 0, 1, 2, 3}, 16384)
	f.Add([]byte{0, 0, 0, 4, 1, 0, 0, 0, 0}, 16384)
	f.Fuzz(func(t *testing.T, buf []byte, maxFrame int) {
		maxFrame = 1 + (maxFrame%(1<<24)+(1<<24))%(1<<24)
		h, n, code := ParseFrameHeader(buf, maxFrame)
		if n != 0 && n != corehttp2.FrameHeaderLen {
			t.Fatalf("n=%d", n)
		}
		if code == corehttp2.ErrCodeNoError && n != 0 && h.Length > maxFrame {
			t.Fatalf("accepted a length %d past %d", h.Length, maxFrame)
		}
		if h.StreamID&(1<<31) != 0 {
			t.Fatal("reserved bit not cleared")
		}
		// a refusal or a parse never turns into something else with more bytes.
		if n != 0 {
			h2, n2, code2 := ParseFrameHeader(append(append([]byte(nil), buf...), 0xff), maxFrame)
			if n2 != n || code2 != code || h2 != h {
				t.Fatalf("more bytes changed the answer")
			}
		}
	})
}

// FuzzEncoderDecoderRoundTrip writes a response and reads it back: every field
// survives, in order, with its name lowercased, through any frame size.
func FuzzEncoderDecoderRoundTrip(f *testing.F) {
	f.Add(uint16(200), uint16(16384), "Content-Type: text/plain\r\nX-A: 1\r\n", "ab")
	f.Add(uint16(404), uint16(16384), "X-B: a:b\r\n", "")
	f.Fuzz(func(t *testing.T, status, frame uint16, header, body string) {
		maxFrame := 16384 + int(frame)
		res := &corenet.HTTP1ResponseValue{Status: 200 + int(status)%800, Header: []byte(header), Body: []byte(body)}
		enc := NewEncoder(maxFrame)
		out, err := enc.AppendHeaders(nil, 1, res, true)
		if err != nil {
			return
		}
		var block []byte
		rest := out
		sawEnd := false
		for len(rest) > 0 {
			h, n, code := ParseFrameHeader(rest, maxFrame)
			if n == 0 || code != corehttp2.ErrCodeNoError || n+h.Length > len(rest) {
				t.Fatalf("bad frame: n=%d code=%s", n, code)
			}
			block = append(block, rest[n:n+h.Length]...)
			rest = rest[n+h.Length:]
			sawEnd = h.Flags&corehttp2.FlagEndHeaders != 0
		}
		if !sawEnd {
			t.Fatal("no END_HEADERS")
		}
		d := NewDecoder(4096, 1<<20)
		mem := make([]byte, TableMem(4096))
		var req corenet.HTTP1RequestValue
		if _, err := d.Decode(mem, block, make([]byte, 0, 256), &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		checkResponseFields(t, res, &req)
	})
}

// checkResponseFields holds that what AppendHeaders wrote decodes back to the
// response's own status, its fields in order, names lowercased, and its length.
func checkResponseFields(t *testing.T, res *corenet.HTTP1ResponseValue, req *corenet.HTTP1RequestValue) {
	t.Helper()
	got := make([]string, 0, req.NFields)
	for i := range req.NFields {
		got = append(got, string(req.Fields[i].Name)+"="+string(req.Fields[i].Value))
	}
	want := []string{":status=" + strconv.Itoa(res.Status)}
	for _, line := range strings.Split(strings.TrimSuffix(string(res.Header), "\r\n"), "\r\n") {
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("a refused line was accepted: %q", line)
		}
		lower := strings.ToLower(name)
		if lower == "connection" || lower == "keep-alive" || lower == "proxy-connection" ||
			lower == "transfer-encoding" || lower == "upgrade" || lower == "content-length" {
			continue
		}
		want = append(want, lower+"="+strings.Trim(value, " \t"))
	}
	if res.Status != 204 && res.Status != 304 {
		want = append(want, "content-length="+strconv.Itoa(len(res.Body)))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fields\n got %q\nwant %q", got, want)
	}
}

// FuzzFeed holds the connection state machine on hostile input: no panic,
// at most the buffer consumed, the event slices inside buf or the lane's
// scratch, and the bookkeeping sane whatever the frames.
func FuzzFeed(f *testing.F) {
	f.Add([]byte{0, 0, 0, 4, 0, 0, 0, 0, 0}, uint8(1))
	f.Add([]byte{0, 0, 4, 1, 4, 0, 0, 0, 1, 0x82, 0x86, 0x84, 0x88}, uint8(2))
	f.Add([]byte{0, 0, 2, 0, 1, 0, 0, 0, 1, 'x', 'y'}, uint8(3))
	f.Add(bytes.Repeat([]byte{0, 0, 0, 6, 0, 0, 0, 0, 0}, 8), uint8(4))
	f.Fuzz(func(t *testing.T, in []byte, split uint8) {
		lane, err := NewLane(corehttp2.LimitsValue{}, 1<<16, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		var c Conn
		lane.Open(&c)
		preface := AppendSettings([]byte(clientPrefaceText), nil)
		used, dst := lane.Feed(&c, preface, nil)
		if used != len(preface) || lane.Closing(&c) {
			t.Fatalf("preface: %d", used)
		}
		chunk := 1 + int(split)
		spill := []byte{}
		for len(in) > 0 && !lane.Closing(&c) {
			n := min(chunk, len(in))
			spill = append(spill, in[:n]...)
			in = in[n:]
			var consumed int
			consumed, dst = lane.Feed(&c, spill, dst[:0])
			if consumed > len(spill) {
				t.Fatalf("consumed %d of %d", consumed, len(spill))
			}
			checkEvents(t, lane, spill)
			var req corenet.HTTP1RequestValue
			for _, ev := range lane.Events() {
				if ev.Kind == EventHeaders {
					_, _ = lane.Decode(&c, ev.Data, nil, &req)
				}
			}
			dst = lane.AppendCredits(&c, dst)
			lane.EndBatch()
			spill = append(spill[:0], spill[consumed:]...)
		}
		if int(c.open) < 0 || c.held < 0 {
			t.Fatalf("open %d held %d", c.open, c.held)
		}
		lane.Close(&c)
	})
}

// checkEvents holds that every event's bytes lie in the input or the lane's
// scratch.
func checkEvents(t *testing.T, l *Lane, spill []byte) {
	t.Helper()
	for _, ev := range l.Events() {
		if !inBytes(spill, ev.Data) && !inBytes(l.join, ev.Data) {
			t.Fatalf("event %d data outside its homes", ev.Kind)
		}
	}
}
