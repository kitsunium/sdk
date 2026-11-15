package transform

import (
	"runtime"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// BenchmarkDecompressSizedAcrossGC is BenchmarkDecompressSized with two
// collections in the middle of the loop, the timer stopped around them: the
// pools a collection empties are the only thing it changes, so the bytes the
// loop is charged afterwards are what a decode into place rebuilds from a cold
// pool (kitsunium/sdk#1102). It is how the two sources of the sized rows'
// last bytes were told apart — the spill chunk a dst that holds the payload
// exactly used to take for finding the end of the stream, and the decoder a
// pool miss rebuilds — and BENCH.md §12.2 reads it.
func BenchmarkDecompressSizedAcrossGC(b *testing.B) {
	benchEach(b, func(b *testing.B, comp coretransform.Compressor, payload []byte) {
		encoded, err := comp.Compress(nil, payload)
		if err != nil {
			b.Fatalf("Compress: %v", err)
		}
		dst := make([]byte, 0, len(payload))
		var out []byte
		benchPrimeDecoder(b, comp, dst, encoded)
		b.ResetTimer()
		for i := range b.N {
			//: halfway, two collections: the second drops what the first moved
			//: to the pools' victim caches.
			if i == b.N/2 {
				b.StopTimer()
				runtime.GC()
				runtime.GC()
				b.StartTimer()
			}
			out, err = comp.Decompress(dst, encoded)
			if err != nil {
				b.Fatalf("Decompress: %v", err)
			}
		}
		b.StopTimer()
		benchSinkBytes = out
	})
}
