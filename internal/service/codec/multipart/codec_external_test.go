package multipart_test

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"math"
	"mime"
	stdmp "mime/multipart"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/multipart"
)

// injectedMarker is the header line every injection case tries to smuggle in.
// It must never reach the wire, and never reach the error either.
const injectedMarker string = "X-Injected: 1"

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
		//: RFC 7578 §4.2 requires a name on every part, and the encoder
		//: refuses to write one without — decoding it used to yield a
		//: PartValue this codec could not re-encode.
		{
			"a part with no form-data name",
			[]byte("--b\r\nContent-Disposition: form-data\r\n\r\nx\r\n--b--\r\n"),
			&multipart.FormValue{}, "UNMARSHAL_FAILED",
		},
		{
			"a part that is not form-data at all",
			[]byte("--b\r\nContent-Disposition: attachment; filename=\"a.txt\"\r\n\r\nx\r\n--b--\r\n"),
			&multipart.FormValue{}, "UNMARSHAL_FAILED",
		},
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

// TestDecodeChargesTheLastPartAgainstTheAggregate is the public face of the
// aggregate fix. With MaxPartBytes 64 and MaxTotalBytes 100, a 60-byte part
// leaves a 40-byte budget, so a 70-byte second part is stopped by the
// AGGREGATE one byte past that budget — and the refusal says so, where the
// decoder used to read on to the per-part cap and name MaxPartBytes instead. A
// body that lands exactly on the aggregate is admitted.
//
// SEEN FAILING against the original decoder, and again with readBudget
// reverted to the per-part cap:
//
//	the last part crosses the aggregate first: refusal names "MaxPartBytes",
//	  want the bound that stopped the read, "MaxTotalBytes"
func TestDecodeChargesTheLastPartAgainstTheAggregate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		sizes    []int
		wantKnob string
	}
	tests := []tc{
		{"the last part crosses the aggregate first", []int{60, 70}, "MaxTotalBytes"},
		{"a body exactly at the aggregate is admitted", []int{60, 40}, ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		form := multipart.FormValue{}
		for i, size := range tc.sizes {
			form.Parts = append(form.Parts, multipart.PartValue{Name: string(rune('a' + i)), Data: bytes.Repeat([]byte("A"), size)})
		}
		//: the DEFAULT-bounded codec writes the fixture, so only decoding is judged.
		data, merr := multipart.New().Marshal(form)
		if merr != nil {
			t.Fatalf("%s: fixture Marshal err=%v", tc.name, merr)
		}
		bounded, cerr := multipart.NewWithLimits(multipart.LimitsConfig{MaxPartBytes: 64, MaxTotalBytes: 100})
		if cerr != nil {
			t.Fatalf("%s: NewWithLimits err=%v", tc.name, cerr)
		}
		var back multipart.FormValue
		uerr := bounded.Unmarshal(data, &back)
		if tc.wantKnob == "" {
			if uerr != nil || len(back.Parts) != len(tc.sizes) {
				t.Fatalf("%s: Unmarshal err=%v, %d parts want %d", tc.name, uerr, len(back.Parts), len(tc.sizes))
			}
			return
		}
		if !errs.HasReason(uerr, "LIMIT_EXCEEDED") {
			t.Fatalf("%s: expected LIMIT_EXCEEDED, got %v", tc.name, uerr)
		}
		knob := ""
		for _, f := range errs.FieldsOf(uerr) {
			if f.Key() == "knob" {
				knob = f.StringValue()
			}
		}
		if knob != tc.wantKnob {
			t.Errorf("%s: refusal names %q, want the bound that stopped the read, %q", tc.name, knob, tc.wantKnob)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecodeAtTheInt64Ceiling pins the one accepted bound the per-part probe
// could not add a byte to. MaxPartBytes = math.MaxInt64 is a valid LimitsConfig
// value, and the probe read limit was bound+1, which wraps negative there:
// io.LimitReader reads nothing through a negative limit, so every part decoded
// silently EMPTY and Unmarshal reported success. Both ceilings are set in the
// second case because that is the setting where the per-part cap, not the
// remaining aggregate, bounds the first read.
//
// SEEN FAILING against the original decoder, both cases, with no error at all:
//
//	part 0 decoded as {Name:field FileName: ContentType: Data:[]}
//	  want {Name:field FileName: ContentType: Data:[118 97 108 117 101]}
//
// Once the aggregate budget bounds the read, the first case no longer reaches
// the wrap; restoring the wrap in probeSize fails the second case alone.
func TestDecodeAtTheInt64Ceiling(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		limits multipart.LimitsConfig
	}
	tests := []tc{
		{"MaxPartBytes at MaxInt64", multipart.LimitsConfig{MaxPartBytes: math.MaxInt64}},
		{"both byte ceilings at MaxInt64", multipart.LimitsConfig{MaxPartBytes: math.MaxInt64, MaxTotalBytes: math.MaxInt64}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c, cerr := multipart.NewWithLimits(tc.limits)
		if cerr != nil {
			t.Fatalf("%s: NewWithLimits err=%v", tc.name, cerr)
		}
		want := sampleForm()
		data, merr := c.Marshal(want)
		if merr != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, merr)
		}
		var back multipart.FormValue
		if uerr := c.Unmarshal(data, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if len(back.Parts) != len(want.Parts) {
			t.Fatalf("%s: decoded %d parts want %d", tc.name, len(back.Parts), len(want.Parts))
		}
		for i := range want.Parts {
			if !partEqual(back.Parts[i], want.Parts[i]) {
				t.Errorf("%s: part %d decoded as %+v want %+v", tc.name, i, back.Parts[i], want.Parts[i])
			}
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

// TestEncoderRefusesHeaderInjection is the regression for a part header built
// from caller-influenced strings. mime/multipart.Writer.CreatePart writes every
// header value verbatim, so a CR or an LF in PartValue.Name, FileName or
// ContentType does not corrupt the header — it ENDS the line, and the rest of
// the value becomes a header line of its own, or, after an empty line, the
// start of the body. The refusal is the one ADR 0064 applies to mail headers:
// refused, never repaired. It must name the FIELD, carry nothing of the value,
// and leave nothing behind on any of the three write paths.
//
// SEEN FAILING against the unfixed encoder, all twelve cases, for example:
//
//	ContentType carries \r\n (Encode): expected VALUE_INVALID (0.3.41.3), got <nil>
//	ContentType carries \r\n: Encode wrote 141 bytes before refusing: "--48ae…\r\n
//	  Content-Disposition: form-data; name=\"f\"\r\nContent-Type: x\r\nX-Injected: 1\r\n\r\nb"
//
// — the smuggled line is a real header on the wire. Also mutation-checked: an
// LF-only filter, the usual half-fix, fails exactly the six \r and \x00 cases;
// putting the value in the Private diagnostic fails all twelve with "refusal
// repeats the value"; refusing after CreatePart instead of before fails all
// twelve with "Encode wrote 136 bytes before refusing".
func TestEncoderRefusesHeaderInjection(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		field string
		part  multipart.PartValue
	}
	var tests []tc
	for _, octets := range []string{"\r\n", "\n", "\r", "\x00"} {
		value := "x" + octets + injectedMarker
		label := strings.NewReplacer("\r", `\r`, "\n", `\n`, "\x00", `\x00`).Replace(octets)
		tests = append(tests,
			tc{"Name carries " + label, "Name", multipart.PartValue{Name: value, Data: []byte("b")}},
			tc{"FileName carries " + label, "FileName", multipart.PartValue{Name: "f", FileName: value, Data: []byte("b")}},
			tc{"ContentType carries " + label, "ContentType", multipart.PartValue{Name: "f", ContentType: value, Data: []byte("b")}},
		)
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		streaming, ok := multipart.New().(codec.StreamingCodec)
		if !ok {
			t.Fatalf("%s: codec does not implement StreamingCodec", tc.name)
		}
		var wire bytes.Buffer
		enc := streaming.NewEncoder(&wire)
		assertHeaderRefused(t, tc.name+" (Encode)", tc.field, enc.Encode(tc.part))
		if wire.Len() != 0 {
			t.Errorf("%s: Encode wrote %d bytes before refusing: %q", tc.name, wire.Len(), wire.Bytes())
		}
		data, merr := multipart.New().Marshal(multipart.FormValue{Parts: []multipart.PartValue{tc.part}})
		assertHeaderRefused(t, tc.name+" (Marshal)", tc.field, merr)
		if data != nil {
			t.Errorf("%s: Marshal returned %d bytes alongside the refusal", tc.name, len(data))
		}
		appender, ok := multipart.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: codec does not implement Appender", tc.name)
		}
		prefix := []byte("PREFIX")
		out, aerr := appender.Append(slices.Clone(prefix), tc.part)
		assertHeaderRefused(t, tc.name+" (Append)", tc.field, aerr)
		if !bytes.Equal(out, prefix) {
			t.Errorf("%s: Append mutated dst on refusal: %q", tc.name, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// assertHeaderRefused checks that err is the codec's VALUE_INVALID, that it
// names field, and that no rendering of it repeats the injected value.
func assertHeaderRefused(t *testing.T, name, field string, err error) {
	t.Helper()
	if !errs.HasCode(err, multipart.CodeMultipartValueInvalid) || !errs.HasReason(err, "VALUE_INVALID") {
		t.Errorf("%s: expected VALUE_INVALID (0.3.41.3), got %v", name, err)
		return
	}
	named := false
	renderings := []string{err.Error(), errs.PublicOf(err), errs.PrivateOf(err)}
	for _, f := range errs.FieldsOf(err) {
		named = named || (f.Key() == "field" && f.StringValue() == field)
		renderings = append(renderings, f.StringValue())
	}
	if !named {
		t.Errorf("%s: refusal does not name field %q: fields %v", name, field, errs.FieldsOf(err))
	}
	for _, text := range renderings {
		if strings.Contains(text, "X-Injected") {
			t.Errorf("%s: refusal repeats the value: %q", name, text)
		}
	}
}

// TestEncodedHeadersReparseWithTheStdlib is the other half of the injection
// fix: every body the codec DOES write must read back, through the stdlib
// mime/multipart reader and the Content-Type header a real server would be
// handed, with exactly the header block the caller asked for — no line more,
// no line less. It covers the escapes the refusal must not disturb: quotes and
// backslashes in both quoted parameters, a UTF-8 filename (RFC 7578 §4.2 has
// form-data send it raw), and the JSON-mediated _json part.
//
// It passes on the unfixed encoder as well: it pins what the fix must not
// break. MUTATION-CHECKED: dropping the quote escape on FileName fails "quotes
// and backslashes" — the stdlib can no longer parse the disposition, so even
// the field name reads back as "" — and emitting Content-Type when it is empty
// fails "plain field" and "quotes and backslashes":
//
//	header block map["Content-Disposition":[…] "Content-Type":[""]],
//	  want exactly map["Content-Disposition":[…]]
func TestEncodedHeadersReparseWithTheStdlib(t *testing.T) {
	t.Parallel()
	type wantPart struct {
		header   textproto.MIMEHeader
		formName string
		fileName string
		body     string
	}
	disposition := func(value string) textproto.MIMEHeader {
		return textproto.MIMEHeader{"Content-Disposition": {value}}
	}
	typed := func(value, contentType string) textproto.MIMEHeader {
		return textproto.MIMEHeader{"Content-Disposition": {value}, "Content-Type": {contentType}}
	}
	type tc struct {
		name  string
		value any
		want  []wantPart
	}
	tests := []tc{
		{"plain field", multipart.PartValue{Name: "field", Data: []byte("value")}, []wantPart{
			{disposition(`form-data; name="field"`), "field", "", "value"},
		}},
		{"file part", multipart.PartValue{Name: "upload", FileName: "a.txt", ContentType: "text/plain", Data: []byte("file")}, []wantPart{
			{typed(`form-data; name="upload"; filename="a.txt"`, "text/plain"), "upload", "a.txt", "file"},
		}},
		{"quotes and backslashes", multipart.PartValue{Name: `a"b\c`, FileName: `q"uo\te.txt`, Data: []byte("x")}, []wantPart{
			{disposition(`form-data; name="a\"b\\c"; filename="q\"uo\\te.txt"`), `a"b\c`, `q"uo\te.txt`, "x"},
		}},
		{"utf-8 filename", multipart.PartValue{Name: "cv", FileName: "résumé-日本語.pdf", ContentType: "application/pdf", Data: []byte("%PDF")}, []wantPart{
			{typed(`form-data; name="cv"; filename="résumé-日本語.pdf"`, "application/pdf"), "cv", "résumé-日本語.pdf", "%PDF"},
		}},
		{"json-mediated value", payload{Name: "Ada", Age: 36}, []wantPart{
			{typed(`form-data; name="_json"`, "application/json"), multipart.JSONPartName, "", `{"name":"Ada","age":36}`},
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := multipart.New().Marshal(tc.value)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		header, herr := multipart.ContentType(data)
		if herr != nil {
			t.Fatalf("%s: ContentType err=%v", tc.name, herr)
		}
		_, params, perr := mime.ParseMediaType(header)
		if perr != nil {
			t.Fatalf("%s: the stdlib refuses the Content-Type %q: %v", tc.name, header, perr)
		}
		reader := stdmp.NewReader(bytes.NewReader(data), params["boundary"])
		for i, want := range tc.want {
			part, nerr := reader.NextPart()
			if nerr != nil {
				t.Fatalf("%s: part %d: NextPart err=%v\n%s", tc.name, i, nerr, data)
			}
			body, rerr := io.ReadAll(part)
			if rerr != nil {
				t.Fatalf("%s: part %d: reading the body err=%v", tc.name, i, rerr)
			}
			if !maps.EqualFunc(part.Header, want.header, slices.Equal) {
				t.Errorf("%s: part %d: header block %q, want exactly %q", tc.name, i, part.Header, want.header)
			}
			if part.FormName() != want.formName || part.FileName() != want.fileName || string(body) != want.body {
				t.Errorf("%s: part %d: read back (%q, %q, %q), want (%q, %q, %q)", tc.name, i,
					part.FormName(), part.FileName(), body, want.formName, want.fileName, want.body)
			}
		}
		if _, nerr := reader.NextPart(); !errors.Is(nerr, io.EOF) {
			t.Errorf("%s: a part beyond the %d expected, or a broken close: %v", tc.name, len(tc.want), nerr)
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
