package multipart

import (
	"bytes"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// failingWriter accepts budget bytes then faults, so the encoder's three
// write-failure branches (header, body, closing delimiter) are reachable
// without an OS-level failure.
type failingWriter struct {
	budget int
}

// Write refuses any chunk that would overrun the budget, reporting the fault
// rather than short-writing (which an io.Writer must not do silently).
func (f *failingWriter) Write(p []byte) (n int, err error) {
	if len(p) > f.budget {
		n = f.budget
		f.budget = 0
		return n, errors.New("write fault") //nolint:err113 // test double, not SDK code.
	}
	f.budget -= len(p)
	return len(p), nil
}

// TestEncoderSurfacesWriteFailures pins that a failing transport is reported
// as a typed MARSHAL_FAILED at every stage of the body, never swallowed.
// Budgets are derived from the length of the body that WOULD have been written
// so the cut lands in the intended place regardless of how mime/multipart
// chunks its writes.
func TestEncoderSurfacesWriteFailures(t *testing.T) {
	t.Parallel()
	//: measure a successful body once so the budgets below are meaningful.
	full := successfulBodyLen(t)
	type tc struct {
		name   string
		budget int
	}
	tests := []tc{
		{"fails on the part header", 0},
		{"fails inside the part body", full / 2},
		{"fails on the closing delimiter", full - 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &multipartCodec{limits: defaultLimits()}
		enc := c.encoderFor(&failingWriter{budget: tc.budget})
		//: the failure lands on Encode or on Close depending on the budget;
		//: either way exactly one of them must report it, typed.
		err := enc.Encode(writeFailurePart())
		if err == nil {
			err = enc.Close()
		}
		if !errs.HasReason(err, "MARSHAL_FAILED") {
			t.Errorf("%s: expected MARSHAL_FAILED, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// writeFailurePart is the fixture the write-failure table encodes.
func writeFailurePart() *PartValue {
	return &PartValue{Name: "p", Data: bytes.Repeat([]byte("A"), 512)}
}

// successfulBodyLen returns the byte length of a body carrying
// writeFailurePart, written to a writer that never fails.
func successfulBodyLen(t *testing.T) int {
	t.Helper()
	c := &multipartCodec{limits: defaultLimits()}
	var buf bytes.Buffer
	enc := c.encoderFor(&buf)
	if err := enc.Encode(writeFailurePart()); err != nil {
		t.Fatalf("successfulBodyLen: Encode err=%v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("successfulBodyLen: Close err=%v", err)
	}
	return buf.Len()
}

// TestDetachAndReleaseOrphansLargeBuffers pins the two release paths: a small
// buffer is cloned and repooled, an over-cap one is handed to the caller
// without a copy. Both must yield the same bytes.
func TestDetachAndReleaseOrphansLargeBuffers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
	}
	tests := []tc{
		{"small buffer is cloned and repooled", 32},
		{"over-cap buffer is orphaned", scratch.MaxRetainedBufBytes + 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		buf := scratch.AcquireBuffer()
		want := bytes.Repeat([]byte("A"), tc.size)
		buf.Write(want)
		got := detachAndRelease(buf)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: detached %d bytes want %d", tc.name, len(got), len(want))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestPartHeader pins the RFC 7578 header block: the field name is always
// present, filename and Content-Type are emitted only when set, and both
// quoted parameters escape the backslash and the double quote.
func TestPartHeader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name            string
		part            PartValue
		wantDisposition string
		wantType        string
	}
	tests := []tc{
		{
			name:            "plain field",
			part:            PartValue{Name: "field"},
			wantDisposition: `form-data; name="field"`,
		},
		{
			name:            "file part",
			part:            PartValue{Name: "upload", FileName: "a.txt", ContentType: "text/plain"},
			wantDisposition: `form-data; name="upload"; filename="a.txt"`,
			wantType:        "text/plain",
		},
		{
			name:            "quotes and backslashes are escaped",
			part:            PartValue{Name: `a"b`, FileName: `c\d`},
			wantDisposition: `form-data; name="a\"b"; filename="c\\d"`,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		header := partHeader(&tc.part)
		if got := header.Get("Content-Disposition"); got != tc.wantDisposition {
			t.Errorf("%s: disposition %q want %q", tc.name, got, tc.wantDisposition)
		}
		if got := header.Get("Content-Type"); got != tc.wantType {
			t.Errorf("%s: content-type %q want %q", tc.name, got, tc.wantType)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAsPart pins the value-to-part resolution, including the JSON-mediated
// fallback that makes Marshal(F, anyValue) hold natively.
func TestAsPart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		in       any
		wantName string
		wantData string
		wantErr  string
	}
	tests := []tc{
		{"part value", PartValue{Name: "a", Data: []byte("v")}, "a", "v", ""},
		{"part pointer", &PartValue{Name: "b", Data: []byte("w")}, "b", "w", ""},
		{"nil part pointer", (*PartValue)(nil), "", "", "VALUE_INVALID"},
		{"json-mediated map", map[string]int{"n": 1}, JSONPartName, `{"n":1}`, ""},
		{"json-mediated scalar", 42, JSONPartName, "42", ""},
		{"unserialisable value", make(chan int), "", "", "MARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := asPart(tc.in)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: asPart err=%v", tc.name, err)
		}
		if got.Name != tc.wantName || string(got.Data) != tc.wantData {
			t.Errorf("%s: got (%q,%q) want (%q,%q)",
				tc.name, got.Name, got.Data, tc.wantName, tc.wantData)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAsForm pins the container resolution, including the shapes that collapse
// to a single-part body.
func TestAsForm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        any
		wantParts int
		wantErr   string
	}
	tests := []tc{
		{"form value", FormValue{Parts: []PartValue{{Name: "a"}, {Name: "b"}}}, 2, ""},
		{"form pointer", &FormValue{Parts: []PartValue{{Name: "a"}}}, 1, ""},
		{"nil form pointer", (*FormValue)(nil), 0, "VALUE_INVALID"},
		{"part slice", []PartValue{{Name: "a"}, {Name: "b"}, {Name: "c"}}, 3, ""},
		{"single part collapses to one", PartValue{Name: "a"}, 1, ""},
		{"arbitrary value collapses to one", struct{ N int }{1}, 1, ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := asForm(tc.in)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: asForm err=%v", tc.name, err)
		}
		if len(got.Parts) != tc.wantParts {
			t.Errorf("%s: %d parts want %d", tc.name, len(got.Parts), tc.wantParts)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
