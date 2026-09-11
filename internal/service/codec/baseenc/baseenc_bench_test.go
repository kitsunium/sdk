package baseenc

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
)

// benchTableSize is the raw payload size every nine-way comparison uses.
// 1 KiB sits comfortably under maxConvBytes (4 KiB), so the two
// base-conversion variants participate in the same table as the seven
// block encodings instead of being excluded from it — which is exactly
// what happened in pkg/v1/codec/BENCH.md, where base58/base62 report 0.
const benchTableSize int = 1024

// benchLargeSize is the payload used to show what the block encodings do
// when the input stops being an identifier. The base-conversion variants
// refuse it by cap, which is the point.
const benchLargeSize int = 64 * 1024

// benchXorshiftSeed seeds the deterministic payload generator. Any non-zero
// constant works; a fixed one makes two runs of this file comparable.
const benchXorshiftSeed uint32 = 0x9E3779B9

// benchSinkBytes observes every encode/decode result so the compiler cannot
// eliminate the call it is meant to measure.
var benchSinkBytes []byte

// benchSinkErr observes the error half of the decode results.
var benchSinkErr error

// benchSinkAny observes the Unmarshal target.
var benchSinkAny any

// benchSinkLen observes a length where retaining the slice itself would alias
// a buffer already handed back to the pool.
var benchSinkLen int

// benchVariant names one registered base-N variant for the comparison tables.
type benchVariant struct {
	// label is the registered Format name, used as the sub-benchmark name.
	label string
	// value is the internal variant discriminator.
	value variant
	// conversion marks the O(n²) base-conversion variants, which carry the
	// 4 KiB maxConvBytes cap and so cannot take benchLargeSize.
	conversion bool
}

// benchVariants is the full nine-way table, ordered by radix so the report
// reads as a progression rather than as the iota order.
var benchVariants = []benchVariant{
	{label: "base16", value: variantBase16},
	{label: "hex", value: variantHex},
	{label: "base32", value: variantBase32},
	{label: "base45", value: variantBase45},
	{label: "base58", value: variantBase58, conversion: true},
	{label: "base62", value: variantBase62, conversion: true},
	{label: "base64", value: variantBase64},
	{label: "base64url", value: variantBase64URL},
	{label: "ascii85", value: variantASCII85},
}

// benchScalingPair is the two-variant contrast the scaling sweeps use: one
// block encoding and one base conversion, so the shape of the two curves is
// the comparison rather than the absolute numbers.
var benchScalingPair = []benchVariant{
	{label: "base64", value: variantBase64},
	{label: "base58", value: variantBase58, conversion: true},
}

// benchScalingSizes are the raw input sizes the scaling benchmarks sweep.
// The top of the sweep is maxConvBytes itself, so the quadratic variants are
// measured at the largest input they will ever legally accept.
var benchScalingSizes = []int{64, 256, 1024, 4096}

// benchPayload returns a deterministic pseudo-random slice of n bytes.
// Deterministic because every one of these encodings costs a function of the
// input LENGTH and not of its content: a fixed payload removes a source of
// run-to-run variance without hiding anything.
func benchPayload(n int) []byte {
	out := make([]byte, n)
	state := benchXorshiftSeed
	for i := range out {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		out[i] = byte(state)
	}
	return out
}

// BenchmarkEncode is the choice table's cost column: what one base-N encode
// step costs for each of the nine variants on the same 1 KiB input, with the
// JSON envelope deliberately excluded so the alphabet is what is measured.
// Each sub-benchmark also reports its expansion ratio, which is the other
// half of the decision.
func BenchmarkEncode(b *testing.B) {
	raw := benchPayload(benchTableSize)
	for _, v := range benchVariants {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			b.ResetTimer()
			for range b.N {
				benchSinkBytes = codecUnderTest.encodeBytes(raw)
			}
			b.StopTimer()
			b.ReportMetric(float64(len(benchSinkBytes))/float64(len(raw)), "x-expand")
		})
	}
}

// BenchmarkDecode is the same table in the other direction. The encoded input
// is built once, outside the timed region.
func BenchmarkDecode(b *testing.B) {
	raw := benchPayload(benchTableSize)
	for _, v := range benchVariants {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			encoded := codecUnderTest.encodeBytes(raw)
			b.ResetTimer()
			for range b.N {
				benchSinkBytes, benchSinkErr = codecUnderTest.decodeBytes(encoded)
			}
		})
	}
}

// BenchmarkEncodeScaling sweeps the input size for one block encoding and one
// base-conversion encoding. The block encoding should stay linear in the input
// and the conversion should not; this is the benchmark that says by how much,
// at the largest input maxConvBytes permits.
func BenchmarkEncodeScaling(b *testing.B) {
	for _, v := range benchScalingPair {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			for _, size := range benchScalingSizes {
				b.Run(strconv.Itoa(size), func(b *testing.B) {
					raw := benchPayload(size)
					b.ResetTimer()
					for range b.N {
						benchSinkBytes = codecUnderTest.encodeBytes(raw)
					}
				})
			}
		})
	}
}

// BenchmarkDecodeScaling is the decode half of the scaling sweep. It is a
// separate benchmark because the two directions of a base conversion are not
// symmetric: encode divides a shrinking magnitude, decode grows one.
func BenchmarkDecodeScaling(b *testing.B) {
	for _, v := range benchScalingPair {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			for _, size := range benchScalingSizes {
				b.Run(strconv.Itoa(size), func(b *testing.B) {
					encoded := codecUnderTest.encodeBytes(benchPayload(size))
					b.ResetTimer()
					for range b.N {
						benchSinkBytes, benchSinkErr = codecUnderTest.decodeBytes(encoded)
					}
				})
			}
		})
	}
}

// BenchmarkEncodeLarge runs the seven block encodings at 64 KiB. The two
// base-conversion variants are absent by construction — maxConvBytes refuses
// this input — and their absence here is the report's clearest statement of
// what they are for.
func BenchmarkEncodeLarge(b *testing.B) {
	raw := benchPayload(benchLargeSize)
	for _, v := range benchVariants {
		if v.conversion {
			continue
		}
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			b.ResetTimer()
			for range b.N {
				benchSinkBytes = codecUnderTest.encodeBytes(raw)
			}
		})
	}
}

// BenchmarkMarshal measures the public path — JSON envelope plus base-N step —
// so the JSON tax can be subtracted from it using BenchmarkMarshalJSONStep.
// The value is the same 1 KiB payload; encoding/json renders a []byte as a
// base64 string, so what reaches the base-N step is ~1.37× longer.
func BenchmarkMarshal(b *testing.B) {
	raw := benchPayload(benchTableSize)
	for _, v := range benchVariants {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			b.ResetTimer()
			for range b.N {
				benchSinkBytes, benchSinkErr = codecUnderTest.Marshal(raw)
			}
		})
	}
}

// BenchmarkUnmarshal is the public decode path for the same payload.
func BenchmarkUnmarshal(b *testing.B) {
	raw := benchPayload(benchTableSize)
	for _, v := range benchVariants {
		b.Run(v.label, func(b *testing.B) {
			codecUnderTest := &baseencCodec{variant: v.value}
			encoded, err := codecUnderTest.Marshal(raw)
			if err != nil {
				b.Fatalf("Marshal(%s): %v", v.label, err)
			}
			b.ResetTimer()
			for range b.N {
				var out []byte
				benchSinkErr = codecUnderTest.Unmarshal(encoded, &out)
				benchSinkAny = out
			}
		})
	}
}

// BenchmarkMarshalJSONStep isolates the envelope every variant shares. The
// difference between this and a BenchmarkMarshal row is that row's base-N
// step, plus the copy out of the pooled buffer.
func BenchmarkMarshalJSONStep(b *testing.B) {
	raw := benchPayload(benchTableSize)
	for range b.N {
		jsonBytes, buf, err := marshalJSONPooled(raw)
		// jsonBytes aliases buf and is invalidated by the release below, so
		// only its length is observed — enough to defeat elimination without
		// retaining a pointer into a recycled buffer.
		benchSinkLen, benchSinkErr = len(jsonBytes), err
		scratch.ReleaseBuffer(buf)
	}
}
