package multipart_test

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/multipart"
)

type payload struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// sampleForm is the two-part fixture: one plain field, one file part.
func sampleForm() multipart.FormValue {
	return multipart.FormValue{
		Parts: []multipart.PartValue{
			{Name: "field", Data: []byte("value")},
			{Name: "upload", FileName: "a.txt", ContentType: "text/plain", Data: []byte("file body")},
		},
	}
}

// TestNew verifies the constructor returns a non-nil singleton carrying the
// canonical name, the single MIME alias, and no file extension.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func(c codec.Codec) bool
	}
	tests := []tc{
		{"canonical name", func(c codec.Codec) bool { return c.Name() == "multipart" }},
		{"single mime alias", func(c codec.Codec) bool {
			return len(c.MIMETypes()) == 1 && c.MIMETypes()[0] == "multipart/form-data"
		}},
		{"no file extension", func(c codec.Codec) bool { return len(c.Extensions()) == 0 }},
		{"implements streaming", func(c codec.Codec) bool { _, ok := c.(codec.StreamingCodec); return ok }},
		{"implements appender", func(c codec.Codec) bool { _, ok := c.(codec.Appender); return ok }},
		{"implements boundary codec", func(c codec.Codec) bool {
			_, ok := c.(multipart.BoundaryCodec)
			return ok
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := multipart.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
		if !tc.check(c) {
			t.Errorf("%s: contract check failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal covers the native container shapes, the JSON-mediated fallback,
// and the two rejection branches.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		want    string // substring the body must carry on success
		wantErr string
	}
	tests := []tc{
		{"form value", sampleForm(), `name="upload"`, ""},
		{"form pointer", new(sampleForm()), `filename="a.txt"`, ""},
		{"part slice", sampleForm().Parts, `name="field"`, ""},
		{"single part", multipart.PartValue{Name: "solo", Data: []byte("x")}, `name="solo"`, ""},
		{"json-mediated struct", payload{Name: "Ada", Age: 36}, `name="` + multipart.JSONPartName + `"`, ""},
		{"nil form pointer", (*multipart.FormValue)(nil), "", "VALUE_INVALID"},
		{"unnamed part", multipart.PartValue{Data: []byte("x")}, "", "VALUE_INVALID"},
		{"unserialisable value", make(chan int), "", "MARSHAL_FAILED"},
		{"boundary out of spec", multipart.FormValue{Boundary: strings.Repeat("x", 71)}, "", "BOUNDARY_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := multipart.New().Marshal(tc.in)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		if !strings.Contains(string(data), tc.want) {
			t.Errorf("%s: body missing %q\n%s", tc.name, tc.want, data)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripForm pins the container contract: every field of every part
// survives Marshal → Unmarshal, and the recovered Boundary re-frames the body
// identically on a second encode.
func TestRoundTripForm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		form multipart.FormValue
	}
	tests := []tc{
		{"two parts", sampleForm()},
		{"empty body", multipart.FormValue{}},
		{"quotes in the field name", multipart.FormValue{
			Parts: []multipart.PartValue{{Name: `a"b\c`, Data: []byte("v")}},
		}},
		{"empty part body", multipart.FormValue{Parts: []multipart.PartValue{{Name: "blank"}}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := multipart.New()
		data, err := c.Marshal(tc.form)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		var back multipart.FormValue
		if uerr := c.Unmarshal(data, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v\n%s", tc.name, uerr, data)
		}
		if len(back.Parts) != len(tc.form.Parts) {
			t.Fatalf("%s: decoded %d parts want %d", tc.name, len(back.Parts), len(tc.form.Parts))
		}
		for i, want := range tc.form.Parts {
			if !partEqual(back.Parts[i], want) {
				t.Errorf("%s: part %d mismatch\n  got:  %+v\n  want: %+v", tc.name, i, back.Parts[i], want)
			}
		}
		//: the recovered delimiter must re-frame the body byte-for-byte.
		again, aerr := c.Marshal(back)
		if aerr != nil {
			t.Fatalf("%s: re-Marshal err=%v", tc.name, aerr)
		}
		if !bytes.Equal(again, data) {
			t.Errorf("%s: re-encode with the recovered boundary diverged\n  got:  %s\n  want: %s",
				tc.name, again, data)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripJSONMediated pins the universal contract: an ordinary Go value
// survives Marshal → Unmarshal through the single-part JSON envelope.
func TestRoundTripJSONMediated(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   payload
	}
	tests := []tc{
		{"populated", payload{Name: "Ada", Age: 36}},
		{"zero value", payload{}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := multipart.New()
		data, err := c.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		var back payload
		if uerr := c.Unmarshal(data, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if back != tc.in {
			t.Errorf("%s: round-trip mismatch got %+v want %+v", tc.name, back, tc.in)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshalFailures covers every documented decode rejection.
func TestUnmarshalFailures(t *testing.T) {
	t.Parallel()
	c := multipart.New()
	body, merr := c.Marshal(sampleForm())
	if merr != nil {
		t.Fatalf("fixture Marshal err=%v", merr)
	}
	type tc struct {
		name    string
		data    []byte
		target  any
		wantErr string
	}
	tests := []tc{
		{"no delimiter at all", []byte("plain text, no parts"), &multipart.FormValue{}, "BOUNDARY_INVALID"},
		{"empty input", nil, &multipart.FormValue{}, "BOUNDARY_INVALID"},
		{"truncated part header", []byte("--b\r\nContent"), &multipart.FormValue{}, "UNMARSHAL_FAILED"},
		{"no json part for a value target", body, &payload{}, "UNMARSHAL_FAILED"},
		{"non-pointer target", jsonBody(t), payload{}, "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := multipart.New().Unmarshal(tc.data, tc.target)
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestContentType pins the answer to the boundary problem on the write side:
// the header value the caller must send is recoverable from the bytes Marshal
// returned, and it names the very delimiter the body uses.
func TestContentType(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		boundary string
	}
	tests := []tc{
		{"generated boundary", ""},
		{"caller-supplied boundary", "----SdkBoundary42"},
		{"boundary needing quotes", "sdk boundary=x"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		form := sampleForm()
		form.Boundary = tc.boundary
		data, err := multipart.New().Marshal(form)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		header, herr := multipart.ContentType(data)
		if herr != nil {
			t.Fatalf("%s: ContentType err=%v", tc.name, herr)
		}
		if !strings.HasPrefix(header, "multipart/form-data;") {
			t.Errorf("%s: header %q is not a form-data content type", tc.name, header)
		}
		recovered, berr := multipart.Boundary(data)
		if berr != nil {
			t.Fatalf("%s: Boundary err=%v", tc.name, berr)
		}
		if tc.boundary != "" && recovered != tc.boundary {
			t.Errorf("%s: recovered %q want %q", tc.name, recovered, tc.boundary)
		}
		//: the header must name the delimiter the body actually uses.
		if !strings.Contains(header, recovered) {
			t.Errorf("%s: header %q does not carry boundary %q", tc.name, header, recovered)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStreamingRoundTrip drives the StreamingCodec path end to end: three
// values in, three values out, one part each.
func TestStreamingRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		records []payload
	}
	tests := []tc{
		{"three records", []payload{{Name: "a", Age: 1}, {Name: "b", Age: 2}, {Name: "c", Age: 3}}},
		{"single record", []payload{{Name: "solo", Age: 9}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		streaming, ok := multipart.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var buf bytes.Buffer
		enc := streaming.NewEncoder(&buf)
		//: the generated delimiter must be readable BEFORE the body ships.
		provider, hasBoundary := enc.(multipart.BoundaryProvider)
		if !hasBoundary || provider.Boundary() == "" {
			t.Fatalf("%s: encoder does not expose its boundary", tc.name)
		}
		for i, rec := range tc.records {
			if eerr := enc.Encode(rec); eerr != nil {
				t.Fatalf("%s: Encode[%d] err=%v", tc.name, i, eerr)
			}
		}
		if cerr := enc.Close(); cerr != nil {
			t.Fatalf("%s: Close err=%v", tc.name, cerr)
		}
		dec := streaming.NewDecoder(bytes.NewReader(buf.Bytes()))
		for i, want := range tc.records {
			var got payload
			if !dec.More() {
				t.Fatalf("%s: More() false before record %d", tc.name, i)
			}
			if derr := dec.Decode(&got); derr != nil {
				t.Fatalf("%s: Decode[%d] err=%v", tc.name, i, derr)
			}
			if got != want {
				t.Errorf("%s: record %d got %+v want %+v", tc.name, i, got, want)
			}
		}
		if dec.More() {
			t.Errorf("%s: More() true after the last record", tc.name)
		}
		var extra payload
		if derr := dec.Decode(&extra); !errors.Is(derr, io.EOF) {
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

// TestNewDecoderReportsBoundaryFailure pins the fail-loud contract for the one
// constructor that cannot return an error: a stream whose delimiter cannot be
// recovered still surfaces BOUNDARY_INVALID, and More() promises exactly one
// Decode so a More/Decode loop cannot swallow it.
func TestNewDecoderReportsBoundaryFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		body string
	}
	tests := []tc{
		{"no delimiter", "just some prose without a delimiter line"},
		{"empty stream", ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		streaming, ok := multipart.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		dec := streaming.NewDecoder(strings.NewReader(tc.body))
		if !dec.More() {
			t.Fatalf("%s: More() false — the failure would be swallowed", tc.name)
		}
		var got payload
		if err := dec.Decode(&got); !errs.HasReason(err, "BOUNDARY_INVALID") {
			t.Errorf("%s: expected BOUNDARY_INVALID, got %v", tc.name, err)
		}
		if dec.More() {
			t.Errorf("%s: More() still true after the failure was reported", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBoundaryCodec covers the authoritative streaming path — the caller
// supplies the delimiter it read from the real Content-Type header.
func TestBoundaryCodec(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		boundary string
		wantErr  string
	}
	tests := []tc{
		{"explicit boundary round-trips", "----SdkExplicit", ""},
		{"empty boundary refused", "", "BOUNDARY_INVALID"},
		{"over-long boundary refused", strings.Repeat("y", 71), "BOUNDARY_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		bc, ok := multipart.New().(multipart.BoundaryCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement BoundaryCodec", tc.name)
		}
		var buf bytes.Buffer
		enc, eerr := bc.NewEncoderWithBoundary(&buf, tc.boundary)
		if tc.wantErr != "" {
			if !errs.HasReason(eerr, tc.wantErr) {
				t.Errorf("%s: encoder expected %s, got %v", tc.name, tc.wantErr, eerr)
			}
			_, derr := bc.NewDecoderWithBoundary(strings.NewReader(""), tc.boundary)
			if !errs.HasReason(derr, tc.wantErr) {
				t.Errorf("%s: decoder expected %s, got %v", tc.name, tc.wantErr, derr)
			}
			return
		}
		if eerr != nil {
			t.Fatalf("%s: NewEncoderWithBoundary err=%v", tc.name, eerr)
		}
		if eerr = enc.Encode(payload{Name: "x", Age: 1}); eerr != nil {
			t.Fatalf("%s: Encode err=%v", tc.name, eerr)
		}
		if cerr := enc.Close(); cerr != nil {
			t.Fatalf("%s: Close err=%v", tc.name, cerr)
		}
		dec, derr := bc.NewDecoderWithBoundary(bytes.NewReader(buf.Bytes()), tc.boundary)
		if derr != nil {
			t.Fatalf("%s: NewDecoderWithBoundary err=%v", tc.name, derr)
		}
		var got payload
		if rerr := dec.Decode(&got); rerr != nil {
			t.Fatalf("%s: Decode err=%v", tc.name, rerr)
		}
		if got.Name != "x" {
			t.Errorf("%s: decoded %+v", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppend pins the Appender contract: the prefix survives, the appended
// bytes decode, and a rejected value leaves dst byte-identical.
func TestAppend(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr bool
	}
	tests := []tc{
		{"form appends", sampleForm(), false},
		{"json-mediated appends", payload{Name: "Ada", Age: 36}, false},
		{"rejected value leaves dst intact", multipart.PartValue{Data: []byte("x")}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		appender, ok := multipart.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.name)
		}
		prefix := []byte("PREFIX\x00")
		dst := slices.Clone(prefix)
		out, err := appender.Append(dst, tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if !bytes.Equal(out, prefix) {
				t.Errorf("%s: dst mutated on failure: %q", tc.name, out)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Append err=%v", tc.name, err)
		}
		if !bytes.HasPrefix(out, prefix) {
			t.Fatalf("%s: prefix clobbered: %q", tc.name, out)
		}
		var back multipart.FormValue
		if uerr := multipart.New().Unmarshal(out[len(prefix):], &back); uerr != nil {
			t.Errorf("%s: appended bytes do not decode: %v", tc.name, uerr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewWithLimits pins ADR 0031 at the package edge: a zero bound clamps to
// the documented default, a negative bound is refused, and a bound the caller
// chose is actually enforced.
func TestNewWithLimits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		limits  multipart.LimitsConfig
		wantErr string
		encErr  string
	}
	tests := []tc{
		{"zero limits clamp to defaults", multipart.LimitsConfig{}, "", ""},
		{"negative part bound refused", multipart.LimitsConfig{MaxPartBytes: -1}, "LIMITS_INVALID", ""},
		{"negative count bound refused", multipart.LimitsConfig{MaxParts: -1}, "LIMITS_INVALID", ""},
		{"negative total bound refused", multipart.LimitsConfig{MaxTotalBytes: -1}, "LIMITS_INVALID", ""},
		{"tiny part bound is enforced", multipart.LimitsConfig{MaxPartBytes: 1}, "", "LIMIT_EXCEEDED"},
		{"tiny part count is enforced", multipart.LimitsConfig{MaxParts: 1}, "", "LIMIT_EXCEEDED"},
		{"tiny total bound is enforced", multipart.LimitsConfig{MaxTotalBytes: 2}, "", "LIMIT_EXCEEDED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c, err := multipart.NewWithLimits(tc.limits)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: NewWithLimits err=%v", tc.name, err)
		}
		_, merr := c.Marshal(sampleForm())
		if tc.encErr == "" {
			if merr != nil {
				t.Errorf("%s: Marshal err=%v", tc.name, merr)
			}
			return
		}
		if !errs.HasReason(merr, tc.encErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.encErr, merr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeRefusesOversizedPart proves the bound guards the DECODE side —
// the direction that faces untrusted bytes — not merely the encode side.
func TestDecodeRefusesOversizedPart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		limits multipart.LimitsConfig
		body   int
	}
	tests := []tc{
		{"part body over the cap", multipart.LimitsConfig{MaxPartBytes: 8}, 64},
		{"aggregate over the cap", multipart.LimitsConfig{MaxTotalBytes: 8}, 64},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: build the body with the DEFAULT-bounded codec so only the decode
		//: side is under test.
		data, merr := multipart.New().Marshal(multipart.FormValue{
			Parts: []multipart.PartValue{{Name: "big", Data: bytes.Repeat([]byte("A"), tc.body)}},
		})
		if merr != nil {
			t.Fatalf("%s: fixture Marshal err=%v", tc.name, merr)
		}
		bounded, cerr := multipart.NewWithLimits(tc.limits)
		if cerr != nil {
			t.Fatalf("%s: NewWithLimits err=%v", tc.name, cerr)
		}
		var back multipart.FormValue
		if uerr := bounded.Unmarshal(data, &back); !errs.HasReason(uerr, "LIMIT_EXCEEDED") {
			t.Errorf("%s: expected LIMIT_EXCEEDED, got %v", tc.name, uerr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load,
// including the parameter-bearing header form an HTTP server actually holds.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("multipart")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("multipart/form-data"); return ok }},
		{"MIME with boundary parameter resolved", func() bool {
			_, ok := codec.LookupMIME("multipart/form-data; boundary=----X")
			return ok
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// partEqual compares two parts field by field, treating a nil and an empty
// body as equal (the wire cannot tell them apart).
func partEqual(got, want multipart.PartValue) bool {
	return got.Name == want.Name &&
		got.FileName == want.FileName &&
		got.ContentType == want.ContentType &&
		bytes.Equal(got.Data, want.Data)
}

// jsonBody returns a JSON-mediated body for the non-pointer-target case.
func jsonBody(t *testing.T) []byte {
	t.Helper()
	data, err := multipart.New().Marshal(payload{Name: "Ada", Age: 36})
	if err != nil {
		t.Fatalf("jsonBody: Marshal err=%v", err)
	}
	return data
}
