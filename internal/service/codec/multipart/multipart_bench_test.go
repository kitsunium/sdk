package multipart_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/codec/multipart"
)

// sinks so no encode or decode can be proven unused and elided.
var (
	bytesSink []byte
	formSink  multipart.FormValue
	strSink   string
	errSink   error
)

// benchFileBytes is the payload the file-part benchmarks carry, built once.
var benchFileBytes = make([]byte, 1<<20)

// benchTextForm is the shape an ordinary HTML form posts: a handful of small
// text fields and no upload.
func benchTextForm() multipart.FormValue {
	return multipart.FormValue{
		Boundary: "bench-boundary-0123456789",
		Parts: []multipart.PartValue{
			{Name: "email", Data: []byte("user@example.test")},
			{Name: "subject", Data: []byte("Invoice 4711")},
			{Name: "message", Data: []byte("Please find the invoice attached.")},
			{Name: "csrf", Data: []byte("01HQ8Z3M4N5P6Q7R8S9T0V1W2X")},
		},
	}
}

// benchUploadForm is the shape that actually matters: two text fields and one
// megabyte of file. The delta against the text form is what the payload costs
// as opposed to the framing.
func benchUploadForm(size int) multipart.FormValue {
	return multipart.FormValue{
		Boundary: "bench-boundary-0123456789",
		Parts: []multipart.PartValue{
			{Name: "token", Data: []byte("01HQ8Z3M4N5P6Q7R8S9T0V1W2X")},
			{Name: "kind", Data: []byte("invoice")},
			{
				Name:        "file",
				FileName:    "invoice-4711.pdf",
				ContentType: "application/pdf",
				Data:        benchFileBytes[:size],
			},
		},
	}
}

// BenchmarkMarshal_TextForm is the framing cost with the payload held small:
// four parts, each a few dozen bytes, so almost all of it is delimiters and
// headers.
func BenchmarkMarshal_TextForm(b *testing.B) {
	c := multipart.New()
	form := benchTextForm()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = c.Marshal(form)
	}
	if errSink != nil {
		b.Fatalf("Marshal: %v", errSink)
	}
}

// BenchmarkMarshal_Upload_* scale the file part, so the pair says whether the
// codec's cost is per BYTE or per PART — which is what decides whether a large
// upload should go through this codec at all.
func BenchmarkMarshal_Upload_64KiB(b *testing.B) { benchMarshalUpload(b, 64<<10) }
func BenchmarkMarshal_Upload_1MiB(b *testing.B)  { benchMarshalUpload(b, 1<<20) }

func benchMarshalUpload(b *testing.B, size int) {
	b.Helper()
	c := multipart.New()
	form := benchUploadForm(size)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = c.Marshal(form)
	}
	if errSink != nil {
		b.Fatalf("Marshal: %v", errSink)
	}
}

// BenchmarkUnmarshal_TextForm and _Upload_1MiB are the inbound half — the one a
// server runs on every upload it accepts.
func BenchmarkUnmarshal_TextForm(b *testing.B) {
	c := multipart.New()
	wire, err := c.Marshal(benchTextForm())
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var out multipart.FormValue
		errSink = c.Unmarshal(wire, &out)
		formSink = out
	}
	if errSink != nil {
		b.Fatalf("Unmarshal: %v", errSink)
	}
}

func BenchmarkUnmarshal_Upload_1MiB(b *testing.B) {
	c := multipart.New()
	wire, err := c.Marshal(benchUploadForm(1 << 20))
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.SetBytes(int64(1 << 20))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var out multipart.FormValue
		errSink = c.Unmarshal(wire, &out)
		formSink = out
	}
	if errSink != nil {
		b.Fatalf("Unmarshal: %v", errSink)
	}
}

// BenchmarkBoundary and BenchmarkContentType are the two extension calls ADR
// 0037 added rather than widening Marshal: a caller reads them once per
// response to build the header the bytes cannot carry.
func BenchmarkBoundary(b *testing.B) {
	c := multipart.New()
	wire, err := c.Marshal(benchTextForm())
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = multipart.Boundary(wire)
	}
	if errSink != nil {
		b.Fatalf("Boundary: %v", errSink)
	}
}

func BenchmarkContentType(b *testing.B) {
	c := multipart.New()
	wire, err := c.Marshal(benchTextForm())
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = multipart.ContentType(wire)
	}
	if errSink != nil {
		b.Fatalf("ContentType: %v", errSink)
	}
}

// BenchmarkUnmarshal_OverLimit is the refusal path, and it is the one that
// matters on a public endpoint: a body larger than the configured ceiling must
// be refused for far less than accepting it would cost, or the limit is an
// amplifier rather than a defence.
func BenchmarkUnmarshal_OverLimit(b *testing.B) {
	c, err := multipart.NewWithLimits(multipart.LimitsConfig{
		MaxPartBytes: 4 << 10, MaxParts: 8, MaxTotalBytes: 16 << 10,
	})
	if err != nil {
		b.Fatalf("NewWithLimits: %v", err)
	}
	permissive := multipart.New()
	wire, err := permissive.Marshal(benchUploadForm(1 << 20))
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var out multipart.FormValue
		errSink = c.Unmarshal(wire, &out)
		formSink = out
	}
	if errSink == nil {
		b.Fatal("an over-limit body was accepted")
	}
}
