package xml_test

import (
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/xml"
)

// benchSink keeps each benchmark's result alive.
var benchSink []byte

// BenchmarkMarshal1KiB encodes a value of about 1 KiB into a fresh slice.
func BenchmarkMarshal1KiB(b *testing.B) {
	c := xml.New()
	var v any = kibValue()
	b.ReportAllocs()
	for b.Loop() {
		out, err := c.Marshal(v)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}

// BenchmarkAppend1KiB encodes the same value onto a caller buffer that has
// the room — #1124's case: what Append costs beyond the encoder itself.
func BenchmarkAppend1KiB(b *testing.B) {
	a, ok := xml.New().(corecodec.Appender)
	if !ok {
		b.Fatal("not an Appender")
	}
	var v any = kibValue()
	dst := make([]byte, 0, 8<<10)
	b.ReportAllocs()
	for b.Loop() {
		out, err := a.Append(dst[:0], v)
		if err != nil {
			b.Fatal(err)
		}
		benchSink = out
	}
}
