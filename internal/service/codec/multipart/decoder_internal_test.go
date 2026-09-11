package multipart

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

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
// a read fault surfaces verbatim for the caller to wrap.
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
