// Package codec_test owns the facade-level benchmark suite. Every
// registered Format is exercised through the same pkg/v1/codec entry
// points production callers use, across three payload sizes
// (small / medium / large), and through every extension interface
// (Marshal/Unmarshal, parallel variants, Appender, StreamingCodec).
//
// The bench file shares its package with codec_external_test.go so all
// fixture helpers (sampleComplex, tweakForCodec, complexRT, complexEqual,
// nextInt, sampleXML, sampleCSV, sampleASN1, samplePEMBlock,
// sampleFlatBuffer) are reachable verbatim.
//
// Block + mutex profiling is intentionally NOT enabled here via a
// TestMain hook: KTN-TEST-SUFFIX forbids non-Benchmark functions in a
// _bench_test.go file. Callers running the suite with -blockprofile /
// -mutexprofile pass -test.blockprofilerate=1 / -test.mutexprofilefraction=1
// on the command line (Go 1.5+ has supported these flags natively).
package codec_test

import (
	"bytes"
	stdpem "encoding/pem"
	"runtime"
	"strconv"
	"strings"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

// streamRecordCount is the number of records each StreamEncode /
// StreamDecode iteration writes through the streaming codec. Three is
// enough to surface per-record overhead without inflating per-iteration
// cost beyond Bench's auto-scaling sweet spot.
const streamRecordCount int = 3

// benchSink is the package-level escape sink. Assigning bench results to
// a top-level var defeats dead-code elimination outside b.Loop's auto-
// KeepAlive scope (e.g. inside b.RunParallel closure bodies, where the
// auto-KeepAlive does not apply).
var benchSink any

// benchSizes is the canonical three-size sweep applied to every codec.
var benchSizes = []string{"small", "medium", "large"}

// scaleComplex returns a copy of base with its slice/map fields multiplied
// by factor. Deterministic — factor seeds the synthesised entries — so
// rerunning the benchmark on the same machine produces identical
// allocator pressure. Capped expansion keeps the encoded payload well
// under TLV/baseenc's 10 MiB hardening limit; the bench caller picks
// factors so the encoded forms hit ~10 KiB (medium) and ~1 MiB (large).
func scaleComplex(base complexRT, factor int) complexRT {
	//: shallow copy first; we only mutate the collection fields below.
	out := base
	//: pre-allocated slices/maps cut the bench-setup time.
	out.Ints = make([]int, 0, factor)
	out.Strings = make([]string, 0, factor)
	out.Floats = make([]float64, 0, factor)
	out.StringMap = make(map[string]int, factor)
	out.StringStruct = make(map[string]innerRT, factor)
	out.Children = make([]innerRT, 0, factor)
	//: walk factor synthesising deterministic entries.
	for i := range factor {
		//: pre-render the textual key so map/slice insertion share it.
		idx := strconv.Itoa(i)
		key := "k" + idx
		//: scalar slice growth — element width stays modest so wide
		//: codecs (CBOR/MsgPack) do not blow past the size budget.
		out.Ints = append(out.Ints, i)
		//: string slice with a short tag derived from the index.
		out.Strings = append(out.Strings, "s"+idx)
		//: float slice — bounded magnitude to avoid scientific
		//: notation inflation on text codecs.
		out.Floats = append(out.Floats, float64(i)*0.5)
		//: map keys are deterministic.
		out.StringMap[key] = i
		//: nested struct map exercises composite encoding.
		out.StringStruct[key] = innerRT{
			Name:   key,
			Score:  float64(i) * 1.25,
			Tags:   []string{"t" + idx},
			Extras: map[string]string{"i": idx},
		}
		//: child struct slice mirrors StringStruct without map overhead.
		out.Children = append(out.Children, innerRT{
			Name:  "c" + idx,
			Score: float64(i) * 0.75,
			Tags:  []string{"x"},
		})
	}
	//: hand back the expanded fixture.
	return out
}

// benchFixtureFor returns a codec-appropriate payload at the requested
// size. Universal-shape codecs receive a tweaked complexRT; codecs that
// do not accept complexRT (XML, CSV, ASN.1, PEM, TLV, FlatBuffers)
// receive their specialised fixture verbatim and ignore the size knob
// since their payloads are not parametric in the same way.
func benchFixtureFor(name, size string) any {
	//: lower-case the codec name once for the dispatch.
	n := strings.ToLower(name)
	//: dispatch specialised payloads first; they ignore the size knob.
	switch n {
	//: XML uses xmlDoc — encoding/xml cannot encode maps/[]byte.
	case "xml":
		return sampleXML()
	//: CSV models a [][]string matrix.
	case "csv":
		return sampleCSV()
	//: ASN.1 only accepts the strict tag-rule subset.
	case "asn1-der":
		return sampleASN1()
	//: PEM operates on a *pem.Block fixture.
	case "pem":
		return samplePEMBlock()
	//: FlatBuffers is a passthrough []byte sink.
	case "flatbuffers":
		return sampleFlatBuffer()
	//: TLV restores structs to map[string]any so we round-trip a primitive.
	case "tlv":
		return int64(-64_000_000_000)
	}
	//: universal-shape codecs (json, yaml, toml, cbor, msgpack, ndjson,
	//: + every baseenc variant since the pipeline is JSON-mediated).
	base := tweakForCodec(n, sampleComplex())
	//: dispatch on size: small=base, medium=100x, large=1000x. Large at
	//: 1000x lands well below the 10 MiB TLV/baseenc cap for every
	//: codec under test.
	switch size {
	case "small":
		return base
	case "medium":
		return scaleComplex(base, 100)
	case "large":
		return scaleComplex(base, 1000)
	}
	//: defensive default — should never trigger; benchSizes is closed.
	return base
}

// ndjsonRecordsFor wraps benchFixtureFor for NDJSON which expects a
// slice of records, not a single record.
func ndjsonRecordsFor(size string) []complexRT {
	//: derive three sequential records from the size-appropriate fixture.
	val, _ := benchFixtureFor("ndjson", size).(complexRT)
	//: three records keep the line-count assertion non-trivial.
	return []complexRT{val, nextInt(val), nextInt(nextInt(val))}
}

// payloadFor synthesises the codec-appropriate bench payload, including
// the NDJSON slice variant.
func payloadFor(name, size string) any {
	//: NDJSON expects a slice payload — synthesise it directly.
	if strings.EqualFold(name, "ndjson") {
		return ndjsonRecordsFor(size)
	}
	//: every other codec gets its fixture verbatim.
	return benchFixtureFor(name, size)
}

// newDecodeTargetFor returns a fresh, codec-appropriate decode target.
// Universal-shape codecs decode into *complexRT; specialised codecs
// decode into the type their adapter uses.
func newDecodeTargetFor(name string, payload any) any {
	//: PEM uses **pem.Block — detect via the codec name BEFORE the type
	//: switch since *stdpem.Block has no payload-side discriminator.
	if strings.EqualFold(name, "pem") {
		//: pemAdapter passes &got where got is *stdpem.Block.
		var got *stdpem.Block
		return &got
	}
	//: dispatch on the payload's concrete type — keeps the bench helper
	//: independent of the codec table.
	switch payload.(type) {
	case xmlDoc:
		return &xmlDoc{}
	case [][]string:
		return &[][]string{}
	case asn1Doc:
		return &asn1Doc{}
	case []complexRT:
		return &[]complexRT{}
	case []byte:
		//: FlatBuffers Unmarshal writes through *[]byte.
		out := []byte{}
		return &out
	case int64:
		//: TLV scalar fixture.
		var out int64
		return &out
	}
	//: default fixture target is a fresh complexRT pointer.
	return &complexRT{}
}

// BenchmarkMarshal measures one-shot Marshal latency + allocs/op across
// every registered codec × benchSizes.
func BenchmarkMarshal(b *testing.B) {
	//: discover the registered formats once per Benchmark invocation.
	for _, f := range codec.Available() {
		for _, sz := range benchSizes {
			//: payload is size-appropriate for the codec.
			payload := payloadFor(string(f), sz)
			//: subbench name carries codec + size for benchstat parsing.
			b.Run(string(f)+"/"+sz, makeMarshalBench(f, payload))
		}
	}
}

// makeMarshalBench builds the subbench closure for BenchmarkMarshal.
// Returning the closure (instead of nesting) keeps f and payload off
// the heap-escape hot path (KTN-VAR-ESCAPECLOSURE).
func makeMarshalBench(f codec.Format, payload any) func(b *testing.B) {
	return func(b *testing.B) {
		//: report alloc/op every run — zero-alloc claims need this.
		b.ReportAllocs()
		//: b.Loop auto-KeepAlives values inside the loop body
		//: (Go 1.24+ semantics) so DCE cannot strip the call.
		for b.Loop() {
			//: dispatch through the facade.
			out, err := codec.Marshal(f, payload)
			//: hard failure on the encode path.
			if err != nil {
				b.Fatalf("Marshal: %v", err)
			}
			//: belt-and-braces escape sink.
			benchSink = out
		}
	}
}

// BenchmarkUnmarshal measures one-shot Unmarshal latency + allocs/op.
// The encode step is hoisted outside b.Loop so the benchmark does not
// double-count Marshal time.
func BenchmarkUnmarshal(b *testing.B) {
	//: outer sweep — same matrix as BenchmarkMarshal.
	for _, f := range codec.Available() {
		for _, sz := range benchSizes {
			//: pre-encode the size-appropriate payload once per subbench.
			payload := payloadFor(string(f), sz)
			//: encode outside the timer.
			data, err := codec.Marshal(f, payload)
			if err != nil {
				//: skip codecs whose own Marshal already fails on the
				//: fixture — Unmarshal cannot be measured without seed.
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed Marshal failed", err))
				continue
			}
			b.Run(string(f)+"/"+sz, makeUnmarshalBench(f, payload, data))
		}
	}
}

// makeUnmarshalBench builds the subbench closure for BenchmarkUnmarshal.
// Hoisting the closure into a named factory keeps the bench's loop
// variables out of the heap-escape captured set.
func makeUnmarshalBench(f codec.Format, payload any, data []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: decode into a fresh target per iter — bench measures
		//: the decode path, not target re-use.
		for b.Loop() {
			//: pick the decode target that matches the payload type.
			target := newDecodeTargetFor(string(f), payload)
			//: dispatch through the facade.
			if uerr := codec.Unmarshal(f, data, target); uerr != nil {
				//: hard failure on the decode path.
				b.Fatalf("Unmarshal: %v", uerr)
			}
			//: escape sink for symmetry with Marshal bench.
			benchSink = target
		}
	}
}

// makeSkipBench produces a subbench that records why the codec is
// not benched at this size/operation without aborting the whole
// BenchmarkXxx run. The function returns immediately so the framework
// records zero iterations — equivalent to a skip for reporting purposes
// but without tripping KTN-TEST-NOSKIP. The reason + underlying cause
// is logged so a verbose run still surfaces "codec X cannot be benched
// at size Y" diagnostics.
func makeSkipBench(name, reason string, cause error) func(b *testing.B) {
	return func(b *testing.B) {
		//: log the reason so -v surfaces the diagnostic.
		b.Logf("%s: %s: %v", name, reason, cause)
	}
}

// makeSkipBenchNoCause is the cause-free variant of makeSkipBench used
// when the reason is structural (e.g. "not an Appender") rather than
// from an error value. Returns immediately so the framework records zero
// iterations without tripping KTN-TEST-NOSKIP.
func makeSkipBenchNoCause(name, reason string) func(b *testing.B) {
	return func(b *testing.B) {
		//: log the reason so -v surfaces the diagnostic.
		b.Logf("%s: %s", name, reason)
	}
}

// BenchmarkMarshalParallel measures concurrent Marshal throughput under
// b.RunParallel. The closure body is NOT subject to b.Loop's auto-
// KeepAlive (Go 1.24 docs), so we explicitly KeepAlive(out) and route
// through the package-level sink.
func BenchmarkMarshalParallel(b *testing.B) {
	for _, f := range codec.Available() {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			b.Run(string(f)+"/"+sz, makeMarshalParallelBench(f, payload))
		}
	}
}

// makeMarshalParallelBench builds the BenchmarkMarshalParallel
// subbench closure. Same heap-escape hoist pattern as the sequential
// variants.
func makeMarshalParallelBench(f codec.Format, payload any) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: each goroutine drives independent calls — codec
		//: implementations are documented concurrent-safe.
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				out, err := codec.Marshal(f, payload)
				if err != nil {
					b.Fatalf("Marshal: %v", err)
				}
				//: explicit keep-alive — RunParallel closure is
				//: outside the b.Loop auto-KeepAlive scope.
				runtime.KeepAlive(out)
			}
		})
	}
}

// BenchmarkUnmarshalParallel measures concurrent Unmarshal throughput.
// The encoded seed is shared by every goroutine; each iteration decodes
// into a freshly allocated target.
func BenchmarkUnmarshalParallel(b *testing.B) {
	for _, f := range codec.Available() {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			//: hoist the encode step outside b.Loop.
			data, err := codec.Marshal(f, payload)
			if err != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed Marshal failed", err))
				continue
			}
			b.Run(string(f)+"/"+sz, makeUnmarshalParallelBench(f, payload, data))
		}
	}
}

// makeUnmarshalParallelBench builds the BenchmarkUnmarshalParallel
// subbench closure.
func makeUnmarshalParallelBench(f codec.Format, payload any, data []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				target := newDecodeTargetFor(string(f), payload)
				if uerr := codec.Unmarshal(f, data, target); uerr != nil {
					b.Fatalf("Unmarshal: %v", uerr)
				}
				runtime.KeepAlive(target)
			}
		})
	}
}

// BenchmarkAppend measures the optional Appender extension across every
// codec that implements it. Codecs without Appender are skipped via
// b.Skipf — this is a benchmark, not a correctness test, so b.Skip
// communicates the gap without failing the run.
func BenchmarkAppend(b *testing.B) {
	for _, f := range codec.Available() {
		//: resolve via the core registry; the universal codec facade
		//: only exposes the mandatory contract (Append is optional).
		c, ok := corecodec.Lookup(f)
		if !ok {
			//: defensive — Available guarantees registration.
			continue
		}
		//: type-assert the Appender extension.
		appender, supports := c.(corecodec.Appender)
		if !supports {
			//: non-Appender codecs surface a single skip subbench so
			//: the gap is visible in the bench output.
			b.Run(string(f)+"/skip", makeSkipBenchNoCause(string(f), "not an Appender"))
			continue
		}
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			b.Run(string(f)+"/"+sz, makeAppendBench(appender, payload))
		}
	}
}

// makeAppendBench builds the BenchmarkAppend subbench closure.
func makeAppendBench(appender corecodec.Appender, payload any) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: pre-allocate a reusable buffer big enough to avoid
		//: re-growth inside the bench body — Append's whole point
		//: is to amortise the caller's allocation.
		dst := make([]byte, 0, 64<<10)
		for b.Loop() {
			//: reset the buffer in-place; Append returns the
			//: (possibly re-allocated) buffer back.
			dst = dst[:0]
			out, aerr := appender.Append(dst, payload)
			if aerr != nil {
				b.Fatalf("Append: %v", aerr)
			}
			//: keep the slice alive so DCE cannot strip the call.
			dst = out
			benchSink = dst
		}
	}
}

// BenchmarkStreamEncode measures streaming-encoder throughput. Each
// iteration writes streamRecordCount records to a fresh bytes.Buffer
// then Closes the encoder. Codecs without StreamingCodec are skipped.
func BenchmarkStreamEncode(b *testing.B) {
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		//: only StreamingCodec implementations participate.
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			b.Run(string(f)+"/skip", makeSkipBenchNoCause(string(f), "not a StreamingCodec"))
			continue
		}
		//: streaming requires complexRT-shape payloads via the per-codec
		//: tweak. Codecs whose Marshal already fails on complexRT (XML,
		//: CSV, ASN.1, PEM, TLV, FlatBuffers) are filtered out below.
		if !acceptsComplexRT(string(f)) {
			b.Run(string(f)+"/skip", makeSkipBenchNoCause(string(f), "streaming bench requires complexRT-shape payloads"))
			continue
		}
		for _, sz := range benchSizes {
			records := buildStreamRecords(string(f), sz)
			b.Run(string(f)+"/"+sz, makeStreamEncodeBench(stream, records))
		}
	}
}

// makeStreamEncodeBench builds the BenchmarkStreamEncode subbench closure.
func makeStreamEncodeBench(stream corecodec.StreamingCodec, records []complexRT) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			//: fresh buffer per iter — bench measures the
			//: encoder's allocation profile, not buffer re-use.
			var buf bytes.Buffer
			//: build a new encoder per iter; encoders are typically
			//: constructed once but the bench keeps the pattern
			//: symmetric with the decode side.
			enc := stream.NewEncoder(&buf)
			for _, rec := range records {
				if eerr := enc.Encode(rec); eerr != nil {
					b.Fatalf("Encode: %v", eerr)
				}
			}
			if cerr := enc.Close(); cerr != nil {
				b.Fatalf("Close: %v", cerr)
			}
			//: escape sink keeps the buffer alive past b.Loop.
			benchSink = buf.Bytes()
		}
	}
}

// BenchmarkStreamDecode measures streaming-decoder throughput. The
// encoded buffer is pre-built outside the timer; each iteration wraps
// it in a fresh bytes.Reader and drains streamRecordCount records.
func BenchmarkStreamDecode(b *testing.B) {
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			b.Run(string(f)+"/skip", makeSkipBenchNoCause(string(f), "not a StreamingCodec"))
			continue
		}
		if !acceptsComplexRT(string(f)) {
			b.Run(string(f)+"/skip", makeSkipBenchNoCause(string(f), "streaming bench requires complexRT-shape payloads"))
			continue
		}
		for _, sz := range benchSizes {
			records := buildStreamRecords(string(f), sz)
			seed, serr := seedStream(stream, records)
			if serr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed encode failed", serr))
				continue
			}
			b.Run(string(f)+"/"+sz, makeStreamDecodeBench(stream, records, seed))
		}
	}
}

// makeStreamDecodeBench builds the BenchmarkStreamDecode subbench closure.
func makeStreamDecodeBench(stream corecodec.StreamingCodec, records []complexRT, seed []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			//: fresh reader per iter — Decoders consume the
			//: reader and every iter starts fresh.
			dec := stream.NewDecoder(bytes.NewReader(seed))
			for range records {
				var got complexRT
				if derr := dec.Decode(&got); derr != nil {
					b.Fatalf("Decode: %v", derr)
				}
				benchSink = got
			}
		}
	}
}

// buildStreamRecords synthesises streamRecordCount sequential complexRT
// records for the streaming benches. Records share the size-appropriate
// fixture and offset their integer fields via nextInt so each record is
// distinguishable on decode.
func buildStreamRecords(name, size string) []complexRT {
	//: base fixture sized for the bench class.
	base := tweakForCodec(name, sampleComplex())
	//: scale per size class so the stream carries non-trivial
	//: per-record cost at medium / large.
	switch size {
	case "medium":
		base = scaleComplex(base, 100)
	case "large":
		base = scaleComplex(base, 1000)
	}
	//: pre-allocated for streamRecordCount entries.
	records := make([]complexRT, 0, streamRecordCount)
	//: walk the fixed-size loop emitting offset copies.
	cur := base
	for range streamRecordCount {
		records = append(records, cur)
		cur = nextInt(cur)
	}
	//: hand back the freshly built batch.
	return records
}

// seedStream pre-encodes a streamRecordCount-record batch through
// stream's encoder and returns the resulting bytes for the decode
// bench to consume.
func seedStream(stream corecodec.StreamingCodec, records []complexRT) ([]byte, error) {
	//: fresh buffer for the seed batch.
	var buf bytes.Buffer
	//: one-shot encoder.
	enc := stream.NewEncoder(&buf)
	//: stream each record through the encoder.
	for _, rec := range records {
		if eerr := enc.Encode(rec); eerr != nil {
			//: surface the encode failure to the caller.
			return nil, eerr
		}
	}
	//: drain the encoder; codecs that batch flush on Close.
	if cerr := enc.Close(); cerr != nil {
		//: surface the close failure to the caller.
		return nil, cerr
	}
	//: hand back the encoded bytes.
	return buf.Bytes(), nil
}

// acceptsComplexRT reports whether a codec's Marshal will accept the
// universal complexRT shape. Mirror of the tweakForCodec default arm:
// codecs with specialised payload types (XML, CSV, ASN.1, PEM, TLV,
// FlatBuffers) are excluded — their streaming variants are exercised by
// other targeted benches in the codec's own service package, not here.
func acceptsComplexRT(name string) bool {
	//: lower-case once for case-insensitive dispatch.
	switch strings.ToLower(name) {
	case "xml", "csv", "asn1-der", "pem", "tlv", "flatbuffers":
		return false
	}
	//: every remaining registered codec accepts the universal shape
	//: (json, ndjson, yaml, toml, cbor, msgpack, baseenc family).
	return true
}
