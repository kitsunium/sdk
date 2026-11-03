package codec_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// detectPrefix is a 512-byte head every sniffer reads to its end: a JSON
// object the window cuts inside a string.
var detectPrefix = append([]byte(`{"payload":"`), bytes.Repeat([]byte("x"), 500)...)

// BenchmarkDetect measures Detect on a 512-byte prefix (#909: 0 allocs/op).
func BenchmarkDetect(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, _ = codec.Detect(detectPrefix)
	}
}
