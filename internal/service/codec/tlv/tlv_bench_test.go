package tlv

import (
	"reflect"
	"runtime"
	"strconv"
	"testing"
)

// benchScratchCap pre-sizes the Append destination so the encode benchmarks
// measure the ENCODER and not the allocator growing a buffer under it.
const benchScratchCap int = 1 << 16

// benchLargeElems is how many records the wide-message benchmarks carry.
// A thousand rows is a page of a report — big enough that per-element cost
// dominates the per-call overhead, small enough to stay off the 10 MiB cap.
const benchLargeElems int = 1000

// benchSinkBytes observes every encode result so the compiler cannot delete
// the call the benchmark exists to time.
var benchSinkBytes []byte

// benchSinkErr observes the error half of every result.
var benchSinkErr error

// benchSinkAny observes an untyped decode target.
var benchSinkAny any

// benchSinkInfo observes a type-info lookup.
var benchSinkInfo *structTypeInfo

// benchFields1 is the one-field baseline for the per-field cost sweep.
// Every struct in the sweep uses the same field TYPE so the delta between
// two rows is field COUNT and nothing else.
type benchFields1 struct {
	// F00 is the only field.
	F00 int64
}

// benchFields4 is the four-field step of the sweep.
type benchFields4 struct {
	// F00 through F03 are identical int64 fields.
	F00, F01, F02, F03 int64
}

// benchFields16 is the sixteen-field step of the sweep.
type benchFields16 struct {
	// F00 through F15 are identical int64 fields.
	F00, F01, F02, F03, F04, F05, F06, F07 int64
	// F08 through F15 continue the same run.
	F08, F09, F10, F11, F12, F13, F14, F15 int64
}

// benchMixed is a struct shaped like a real payload rather than like a
// microbenchmark: one of every scalar family the encoder narrows.
type benchMixed struct {
	// ID is a wide integer, so the narrowest-tag rule emits tagInt64.
	ID int64
	// Name is a short string.
	Name string
	// Active is a bool.
	Active bool
	// Ratio is a float64.
	Ratio float64
	// Count is a small integer, so the narrowest-tag rule emits tagInt8.
	Count int
}

// benchNode is the nesting chain. Each level is a struct with two scalars
// and one pointer to the next, so the per-level delta is one nested struct
// record plus its two fields.
type benchNode struct {
	// Name is a scalar field at this level.
	Name string
	// Value is a scalar field at this level.
	Value int64
	// Child is the next level, or nil at the leaf.
	Child *benchNode
}

// benchNestDepths are the nesting depths the chain benchmark sweeps. The top
// stays under maxTLVDepth (32) so every case encodes successfully.
var benchNestDepths = []int{1, 4, 16}

// benchFieldCounts labels the per-field sweep.
var benchFieldCounts = []int{1, 4, 16}

// benchChain builds a benchNode chain of the given depth, outside any timed
// region.
func benchChain(depth int) *benchNode {
	var head *benchNode
	for level := depth; level > 0; level-- {
		head = &benchNode{Name: "level", Value: int64(level), Child: head}
	}
	return head
}

// benchFieldValues returns the three sweep structs as interface values, built
// once so the boxing is not inside the loop.
func benchFieldValues() []any {
	return []any{
		benchFields1{},
		benchFields4{},
		benchFields16{},
	}
}

// benchLargeSlice builds the wide message: benchLargeElems mixed structs.
func benchLargeSlice() []benchMixed {
	out := make([]benchMixed, benchLargeElems)
	for i := range out {
		out[i] = benchMixed{
			ID:     int64(i) * 1_000_003,
			Name:   "row-" + strconv.Itoa(i),
			Active: i%2 == 0,
			Ratio:  float64(i) / 7,
			Count:  i % 100,
		}
	}
	return out
}

// BenchmarkAppendFieldSweep is the per-field cost measurement. Append writes
// into a buffer that already has the capacity, so what is left is the
// encoder: the cached name prefix, the scalar dispatch, and the varint.
// The slope between the 1-, 4- and 16-field rows is the cost of one field.
func BenchmarkAppendFieldSweep(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	dst := make([]byte, 0, benchScratchCap)
	for index, value := range benchFieldValues() {
		b.Run(strconv.Itoa(benchFieldCounts[index])+"fields", func(b *testing.B) {
			for range b.N {
				benchSinkBytes, benchSinkErr = codecUnderTest.Append(dst[:0], value)
			}
		})
	}
}

// BenchmarkMarshalFieldSweep is the same sweep through the public Marshal,
// which hands Append a nil destination. The difference against
// BenchmarkAppendFieldSweep is exactly what the fresh-slice allocation costs
// — the claim the package CLAUDE.md makes about Append and never measured.
func BenchmarkMarshalFieldSweep(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	for index, value := range benchFieldValues() {
		b.Run(strconv.Itoa(benchFieldCounts[index])+"fields", func(b *testing.B) {
			for range b.N {
				benchSinkBytes, benchSinkErr = codecUnderTest.Marshal(value)
			}
		})
	}
}

// BenchmarkAppendNestSweep measures what one level of nesting costs. Each
// level adds two scalar fields and one nested struct record, so the slope is
// the price of recursing rather than of the payload.
func BenchmarkAppendNestSweep(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	dst := make([]byte, 0, benchScratchCap)
	for _, depth := range benchNestDepths {
		b.Run("depth"+strconv.Itoa(depth), func(b *testing.B) {
			chain := benchChain(depth)
			b.ResetTimer()
			for range b.N {
				benchSinkBytes, benchSinkErr = codecUnderTest.Append(dst[:0], chain)
			}
		})
	}
}

// BenchmarkAppendMixed encodes the realistic five-field struct.
func BenchmarkAppendMixed(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	dst := make([]byte, 0, benchScratchCap)
	value := benchMixed{ID: 9_007_199_254_740_993, Name: "kitsunium", Active: true, Ratio: 0.5, Count: 42}
	for range b.N {
		benchSinkBytes, benchSinkErr = codecUnderTest.Append(dst[:0], value)
	}
}

// BenchmarkAppendLarge encodes the thousand-row message. Divided by
// benchLargeElems it gives the per-record cost at scale, which is the number
// that says whether the per-call overhead amortises.
func BenchmarkAppendLarge(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	dst := make([]byte, 0, benchScratchCap)
	value := benchLargeSlice()
	b.ResetTimer()
	for range b.N {
		benchSinkBytes, benchSinkErr = codecUnderTest.Append(dst[:0], value)
	}
	b.StopTimer()
	b.ReportMetric(float64(len(benchSinkBytes)), "wire-B")
}

// BenchmarkUnmarshalTyped decodes the wide message straight into its Go type,
// which is the tryDecodeRootInto fast path.
func BenchmarkUnmarshalTyped(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	encoded, err := codecUnderTest.Marshal(benchLargeSlice())
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ResetTimer()
	for range b.N {
		out := make([]benchMixed, 0, benchLargeElems)
		benchSinkErr = codecUnderTest.Unmarshal(encoded, &out)
		benchSinkAny = out
	}
}

// BenchmarkUnmarshalAny decodes the same bytes into an interface target,
// which takes the untyped walker and materialises []any / map[string]any.
// The gap against BenchmarkUnmarshalTyped is what the typed root buys.
func BenchmarkUnmarshalAny(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	encoded, err := codecUnderTest.Marshal(benchLargeSlice())
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ResetTimer()
	for range b.N {
		var out any
		benchSinkErr = codecUnderTest.Unmarshal(encoded, &out)
		benchSinkAny = out
	}
}

// BenchmarkUnmarshalMixed decodes one realistic struct — the per-call figure
// a request handler pays, as opposed to the amortised one above.
func BenchmarkUnmarshalMixed(b *testing.B) {
	codecUnderTest := &tlvCodec{}
	encoded, err := codecUnderTest.Marshal(benchMixed{ID: 7, Name: "kitsunium", Ratio: 0.5, Count: 42})
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	b.ResetTimer()
	for range b.N {
		var out benchMixed
		benchSinkErr = codecUnderTest.Unmarshal(encoded, &out)
		benchSinkAny = out
	}
}

// BenchmarkTypeInfoHit is the cached lookup every encode and every typed
// decode performs — a sync.Map read on a warm entry.
func BenchmarkTypeInfoHit(b *testing.B) {
	target := reflect.TypeFor[benchMixed]()
	benchSinkInfo = cachedStructTypeInfo(target)
	b.ResetTimer()
	for range b.N {
		benchSinkInfo = cachedStructTypeInfo(target)
	}
}

// BenchmarkTypeInfoHitParallel is the same read under contention. sync.Map's
// read-mostly path should not serialise; if it did, the singleflight question
// below would have a different answer.
func BenchmarkTypeInfoHitParallel(b *testing.B) {
	target := reflect.TypeFor[benchMixed]()
	benchSinkInfo = cachedStructTypeInfo(target)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// The sink is per-goroutine on purpose. An earlier draft of this
		// benchmark stored into the package-level benchSinkInfo from every
		// P, and reported 25.96 ns/op against a serial 21.89 — it was
		// measuring the cache line under the sink, not the sync.Map.
		var local *structTypeInfo
		for pb.Next() {
			local = cachedStructTypeInfo(target)
		}
		runtime.KeepAlive(local)
	})
}

// BenchmarkTypeInfoBuild is the MISS — the reflect walk the cache exists to
// perform once per type per process. This is the number that decides whether
// wrapping the cache in a kernel/singleflight Group could ever pay: a leading
// singleflight call is measured at ~2 µs in internal/kernel/singleflight/BENCH.md,
// so if a build costs materially less than that, the Group is a pure loss.
func BenchmarkTypeInfoBuild(b *testing.B) {
	for _, target := range []reflect.Type{
		reflect.TypeFor[benchFields1](),
		reflect.TypeFor[benchMixed](),
		reflect.TypeFor[benchFields16](),
	} {
		b.Run(strconv.Itoa(target.NumField())+"fields", func(b *testing.B) {
			for range b.N {
				benchSinkInfo = buildStructTypeInfo(target)
			}
		})
	}
}
