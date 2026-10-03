package multipart

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failingReader yields n bytes then a hard read fault, so the read-fault
// branch of readBounded is reachable without an OS-level failure.
type failingReader struct {
	remaining int
}

// Read hands back filler bytes until the budget runs out, then fails.
func (f *failingReader) Read(p []byte) (n int, err error) {
	if f.remaining <= 0 {
		return 0, errors.New("read fault") //nolint:err113 // test double, not SDK code.
	}
	n = min(len(p), f.remaining)
	f.remaining -= n
	return n, nil
}

// TestReadBounded pins the extra-byte trick: a payload exactly at the cap is
// accepted, one byte past it reports overflow rather than being truncated, and
// a read fault surfaces verbatim for the caller to wrap. The MaxInt64 case was
// seen failing with the wrap restored in probeSize: it read nothing at all
// where the source held three.
func TestReadBounded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name         string
		src          io.Reader
		max          int64
		wantLen      int
		wantOverflow bool
		wantErr      bool
	}
	tests := []tc{
		{"under the cap", strings.NewReader("abc"), 8, 3, false, false},
		{"exactly at the cap", strings.NewReader("abcd"), 4, 4, false, false},
		{"one byte over the cap", strings.NewReader("abcde"), 4, 0, true, false},
		{"empty source", strings.NewReader(""), 4, 0, false, false},
		{"read fault", &failingReader{remaining: 2}, 8, 0, false, true},
		{"a MaxInt64 cap reads the whole body", strings.NewReader("abc"), math.MaxInt64, 3, false, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, overflow, err := readBounded(tc.src, tc.max)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected a read fault", tc.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: readBounded err=%v", tc.name, err)
		}
		if overflow != tc.wantOverflow {
			t.Errorf("%s: overflow=%v want %v", tc.name, overflow, tc.wantOverflow)
		}
		if len(got) != tc.wantLen {
			t.Errorf("%s: read %d bytes want %d", tc.name, len(got), tc.wantLen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestProbeSize pins the one-byte probe and its single exception: at
// math.MaxInt64 the probe saturates instead of wrapping to a negative limit,
// through which io.LimitReader would read nothing at all.
//
// SEEN FAILING with the guard widened to `bound <= math.MaxInt64`, i.e. the
// wrap restored:
//
//	the ceiling saturates: probeSize(9223372036854775807) = -9223372036854775808, want 9223372036854775807
func TestProbeSize(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		bound int64
		want  int64
	}
	tests := []tc{
		{"zero probes one byte", 0, 1},
		{"an ordinary bound probes one past it", 41, 42},
		{"one below the ceiling still fits", math.MaxInt64 - 1, math.MaxInt64},
		{"the ceiling saturates", math.MaxInt64, math.MaxInt64},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := probeSize(tc.bound); got != tc.want {
			t.Errorf("%s: probeSize(%d) = %d, want %d", tc.name, tc.bound, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// countingReader serves size filler bytes and records how many were pulled,
// so a test can bound what readBody consumes exactly — through a real
// *mime/multipart.Part the count would be blurred by the stdlib's own 4 KiB
// read-ahead.
type countingReader struct {
	remaining int
	served    int
}

// Read hands back filler bytes until the budget runs out, then io.EOF.
func (c *countingReader) Read(p []byte) (n int, err error) {
	if c.remaining == 0 {
		return 0, io.EOF
	}
	n = min(len(p), c.remaining)
	c.remaining -= n
	c.served += n
	return n, nil
}

// TestReadBodyStopsAtTheTighterBudget pins the aggregate bound on the LAST
// part. A part used to be read up to MaxPartBytes before the running total was
// charged, so the final part could overrun MaxTotalBytes by up to a whole part
// — 1.5× the aggregate with the defaults. Now the read stops one byte past the
// tighter of MaxPartBytes and what is left of MaxTotalBytes, and the refusal
// names the knob that stopped it.
//
// SEEN FAILING with readBudget reverted to the old read bound (always the
// per-part cap):
//
//	the aggregate binds the last part: pulled 1024 bytes from the part, the budget allows at most 41
//
// The knob assertion alone would have passed — admitPart still names
// MaxTotalBytes once the over-read is done — so the byte count is the
// assertion that carries the defect.
func TestReadBodyStopsAtTheTighterBudget(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		limits    LimitsConfig
		charged   int64
		size      int
		wantKnob  string
		wantBound int64
		maxServed int
	}
	tests := []tc{
		{"the aggregate binds the last part", LimitsConfig{1024, 8, 100}, 60, 1024, "MaxTotalBytes", 100, 41},
		{"the per-part cap binds", LimitsConfig{10, 8, 100}, 0, 50, "MaxPartBytes", 10, 11},
		{"a tie names the per-part cap", LimitsConfig{40, 8, 100}, 60, 50, "MaxPartBytes", 40, 41},
		{"a last part exactly at the remaining budget is admitted", LimitsConfig{1024, 8, 100}, 60, 40, "", 0, 41},
		{"a part exactly at the per-part cap is admitted", LimitsConfig{10, 8, 100}, 0, 10, "", 0, 11},
		{"an exhausted aggregate still admits an empty part", LimitsConfig{1024, 8, 100}, 100, 0, "", 0, 1},
		{"an exhausted aggregate refuses one byte", LimitsConfig{1024, 8, 100}, 100, 1, "MaxTotalBytes", 100, 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: the aggregate already admitted, as earlier parts would have left it.
		d := &multipartDecoder{count: counter{limits: tc.limits, parts: 1, total: tc.charged}}
		src := &countingReader{remaining: tc.size}
		body, err := d.readBody(src)
		if src.served > tc.maxServed {
			t.Errorf("%s: pulled %d bytes from the part, the budget allows at most %d", tc.name, src.served, tc.maxServed)
		}
		if tc.wantKnob == "" {
			if err != nil {
				t.Fatalf("%s: readBody err=%v", tc.name, err)
			}
			if len(body) != tc.size {
				t.Errorf("%s: materialised %d bytes want %d", tc.name, len(body), tc.size)
			}
			return
		}
		if !errs.HasReason(err, "LIMIT_EXCEEDED") {
			t.Fatalf("%s: expected LIMIT_EXCEEDED, got %v", tc.name, err)
		}
		if got := fieldOf(err, "knob"); got != tc.wantKnob {
			t.Errorf("%s: refusal names knob %q want %q", tc.name, got, tc.wantKnob)
		}
		if got := fieldOf(err, "bound"); got != strconv.FormatInt(tc.wantBound, 10) {
			t.Errorf("%s: refusal names bound %s want %d", tc.name, got, tc.wantBound)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// fieldOf returns the rendered value of the first field named key on err, or
// "" when there is none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// TestAssignPart pins the JSON-mediated projection: a non-nil pointer reads
// the part body as JSON, and the two unusable targets are refused with the
// reason that names the mistake rather than encoding/json's phrasing.
func TestAssignPart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		part    PartValue
		target  any
		wantErr string
	}
	tests := []tc{
		{"json target", PartValue{Name: JSONPartName, Data: []byte(`{"N":1}`)}, &struct{ N int }{}, ""},
		{"non-pointer target", PartValue{Data: []byte("1")}, 0, "VALUE_INVALID"},
		{"nil pointer target", PartValue{Data: []byte("1")}, (*int)(nil), "VALUE_INVALID"},
		{"body is not json", PartValue{Data: []byte("not json")}, &struct{ N int }{}, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := assignPart(&tc.part, tc.target)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Errorf("%s: assignPart err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPublishPart pins the verbatim projection onto a *PartValue target,
// including the nil-pointer refusal.
func TestPublishPart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		part    PartValue
		nilDest bool
		wantErr string
	}
	tests := []tc{
		{"copies every field", PartValue{Name: "a", FileName: "f", ContentType: "t", Data: []byte("v")}, false, ""},
		{"nil destination refused", PartValue{Name: "a"}, true, "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var dest *PartValue
		if !tc.nilDest {
			dest = &PartValue{}
		}
		err := publishPart(&tc.part, dest)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: publishPart err=%v", tc.name, err)
		}
		if dest.Name != tc.part.Name || dest.FileName != tc.part.FileName ||
			dest.ContentType != tc.part.ContentType || !bytes.Equal(dest.Data, tc.part.Data) {
			t.Errorf("%s: published %+v want %+v", tc.name, *dest, tc.part)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecoderLookAhead pins the one-part look-ahead: More() may be called any
// number of times without consuming a part, and it flips to false exactly when
// the stream is drained.
func TestDecoderLookAhead(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		parts int
	}
	tests := []tc{
		{"empty body", 0},
		{"one part", 1},
		{"three parts", 3},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		form := FormValue{}
		for i := range tc.parts {
			form.Parts = append(form.Parts, PartValue{Name: "p", Data: []byte{byte('a' + i)}})
		}
		data, merr := New().Marshal(form)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		boundary, berr := Boundary(data)
		if berr != nil {
			t.Fatalf("%s: Boundary err=%v", tc.name, berr)
		}
		c := &multipartCodec{limits: defaultLimits()}
		dec := c.decoderFor(bytes.NewReader(data), boundary)
		seen := 0
		for dec.More() {
			//: repeated More() must be idempotent — it fills a slot, never drains one.
			if !dec.More() {
				t.Fatalf("%s: More() flipped to false without an intervening Decode", tc.name)
			}
			var got PartValue
			if derr := dec.Decode(&got); derr != nil {
				t.Fatalf("%s: Decode err=%v", tc.name, derr)
			}
			seen++
		}
		if seen != tc.parts {
			t.Errorf("%s: decoded %d parts want %d", tc.name, seen, tc.parts)
		}
		var extra PartValue
		if derr := dec.Decode(&extra); !errIsEOF(derr) {
			t.Errorf("%s: Decode past the end = %v want io.EOF", tc.name, derr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSniffReaderPreservesStream pins the non-consuming peek: the delimiter
// line the sniff read is still available to mime/multipart afterwards, for a
// body both shorter and longer than the sniff window.
func TestSniffReaderPreservesStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		fill int
	}
	tests := []tc{
		{"body shorter than the window", 16},
		{"body longer than the window", boundarySniffWindow * 3},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, merr := New().Marshal(FormValue{
			Parts: []PartValue{{Name: "p", Data: bytes.Repeat([]byte("A"), tc.fill)}},
		})
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		buffered, boundary, serr := sniffReader(bytes.NewReader(data))
		if serr != nil {
			t.Fatalf("%s: sniffReader err=%v", tc.name, serr)
		}
		rest, rerr := io.ReadAll(buffered)
		if rerr != nil {
			t.Fatalf("%s: draining the buffered reader err=%v", tc.name, rerr)
		}
		if !bytes.Equal(rest, data) {
			t.Errorf("%s: sniffing consumed %d bytes", tc.name, len(data)-len(rest))
		}
		if boundary == "" {
			t.Errorf("%s: no boundary recovered", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestSniffReaderDoesNotWaitForTheWindow pins that the sniff returns once the
// first delimiter line is complete. It used to Peek the whole 4 KiB window, so
// a producer that sent a short prefix and paused stalled NewDecoder until it
// sent 4 KiB or closed the stream. The pausing reader here holds the rest of
// the body until the test releases it; the wait is bounded so a regression
// fails instead of hanging. Seen failing with the single Peek restored:
// "sniffReader was still waiting for the window after 10s".
//
// Goroutine lifecycle: one goroutine, ending when sniffReader returns — at
// once when the fix holds, or when the cleanup releases the reader when it
// does not. The result channel is buffered, so it never parks on a receiver
// that timed out.
func TestSniffReaderDoesNotWaitForTheWindow(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r := &pausingReader{
		prefix:  []byte("--abc\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nx"),
		rest:    []byte("\r\n--abc--\r\n"),
		release: release,
	}
	type result struct {
		boundary string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, boundary, err := sniffReader(r)
		done <- result{boundary, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.boundary != "abc" {
			t.Fatalf("sniffReader = %q, %v; want \"abc\", nil", got.boundary, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sniffReader was still waiting for the window after 10s")
	}
}

// TestSniffReaderWaitsOutTheAmbiguousLine pins the one line that must still
// wait: "--ab--" can be a zero-part body's close delimiter or a boundary that
// itself ends in "--", and only the bytes after it tell which. Fed one byte at
// a time, a sniff that answered on the first complete line read "ab" where the
// body's boundary is "ab--" — seen failing so, with the ambiguity check
// removed. The unambiguous body beside it is recovered and left unconsumed.
func TestSniffReaderWaitsOutTheAmbiguousLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		body string
		want string
	}
	tests := []tc{
		{"a boundary ending in two hyphens", "--ab--\r\nbody\r\n--ab----\r\n", "ab--"},
		{"a zero-part body", "--abc--\r\n", "abc"},
		{"an ordinary body", "--abc\r\nbody\r\n--abc--\r\n", "abc"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		buffered, boundary, err := sniffReader(iotest.OneByteReader(strings.NewReader(c.body)))
		if err != nil {
			t.Fatalf("%s: sniffReader err=%v", c.name, err)
		}
		if boundary != c.want {
			t.Errorf("%s: boundary %q, want %q", c.name, boundary, c.want)
		}
		rest, rerr := io.ReadAll(buffered)
		if rerr != nil || string(rest) != c.body {
			t.Errorf("%s: the sniff consumed the stream: rest %q, err %v", c.name, rest, rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// pausingReader hands out prefix, then blocks until release is closed before
// handing out rest and the end of the stream — a live producer that sent a
// first chunk and is waiting on something else.
type pausingReader struct {
	prefix  []byte
	rest    []byte
	release chan struct{}
	stage   int
}

// Read serves the prefix, then waits for release, then the rest, then EOF.
func (r *pausingReader) Read(p []byte) (int, error) {
	switch r.stage {
	case 0:
		r.stage++
		return copy(p, r.prefix), nil
	case 1:
		<-r.release
		r.stage++
		return copy(p, r.rest), nil
	default:
		return 0, io.EOF
	}
}
