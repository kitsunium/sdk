//go:build !race

package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// TestDetectAllocatesNothing is #909's allocation bar, run by the race-off
// alloc lane: Detect and DetectCompression on a 512-byte prefix allocate
// nothing.
func TestDetectAllocatesNothing(t *testing.T) {
	//: Detect asks every sniffer.
	if n := testing.AllocsPerRun(100, func() { _, _ = codec.Detect(detectPrefix) }); n != 0 {
		t.Errorf("Detect allocates %v times per call, want 0", n)
	}
	//: DetectCompression reads magic numbers.
	if n := testing.AllocsPerRun(100, func() { _, _ = codec.DetectCompression(detectPrefix) }); n != 0 {
		t.Errorf("DetectCompression allocates %v times per call, want 0", n)
	}
}
