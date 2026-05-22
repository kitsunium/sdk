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
	"cmp"
	stdpem "encoding/pem"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

// streamRecordCount is the number of records each StreamEncode /
// StreamDecode iteration writes through the streaming codec. Three is
// enough to surface per-record overhead without inflating per-iteration
// cost beyond Bench's auto-scaling sweet spot.
const streamRecordCount int = 3

// benchOutputRelativePath is the path of BENCH.md relative to the
// workspace root. `bazel run` sets BUILD_WORKSPACE_DIRECTORY so the
// report lands in the source tree, not the runfiles sandbox.
const benchOutputRelativePath = "pkg/v1/codec/BENCH.md"

// benchSink is the package-level escape sink. Assigning bench results to
// a top-level var defeats dead-code elimination outside b.Loop's auto-
// KeepAlive scope (e.g. inside b.RunParallel closure bodies, where the
// auto-KeepAlive does not apply).
var benchSink any

// benchSizes is the canonical three-size sweep applied to every codec.
var benchSizes = []string{"small", "medium", "large"}

// benchReportRow is the typed equivalent of one `BenchmarkXxx-N N ns/op
// B/op allocs/op` line in `go test -bench=.` output. Built directly from
// testing.BenchmarkResult so no text parsing is needed by the report
// renderer below.
type benchReportRow struct {
	//: Category is the BenchmarkXxx prefix (Marshal, Unmarshal, …).
	Category string
	//: Codec is the registered Format name (json, base64, xml, …).
	Codec string
	//: Size is small / medium / large.
	Size string
	//: Iters is the iteration count the testing framework auto-picked.
	Iters int
	//: NsPerOp is the per-op wall-clock cost in nanoseconds.
	NsPerOp int64
	//: BPerOp is the per-op bytes-allocated count.
	BPerOp int64
	//: AllocsPerOp is the per-op allocation count.
	AllocsPerOp int64
}

// machineEnvelope is the reproducibility metadata stamped at the top of
// every BENCH.md. Values are best-effort; missing pieces render as
// `"unknown"` rather than aborting the run.
type machineEnvelope struct {
	CPU          string
	CPUCores     string
	CPUFrequency string
	RAM          string
	OS           string
	Kernel       string
	Architecture string
	Hostname     string
	GoToolchain  string
	Bazel        string
	GitBranch    string
	GitCommit    string
	GeneratedAt  string
	BenchTime    string
}

// meanSet collects the per-column arithmetic means used as the cell
// baseline by writePivotTable / formatCell.
type meanSet struct {
	iters  float64
	nsop   float64
	bop    float64
	allocs float64
}

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
// codec that implements it. Codecs without Appender contribute no rows
// — the optional interface is a structural fact, not a benchable gap.
func BenchmarkAppend(b *testing.B) {
	for _, f := range codec.Available() {
		//: resolve via the core registry; the universal codec facade
		//: only exposes the mandatory contract (Append is optional).
		c, ok := corecodec.Lookup(f)
		if !ok {
			//: defensive — Available guarantees registration.
			continue
		}
		//: type-assert the Appender extension; skip silently when absent.
		appender, supports := c.(corecodec.Appender)
		if !supports {
			//: structurally not an Appender — nothing to measure.
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
// iteration writes the codec-appropriate record batch to a fresh
// bytes.Buffer and closes the encoder. Codecs that do NOT implement
// StreamingCodec contribute no rows — the optional interface is a
// structural fact, not a benchable gap.
func BenchmarkStreamEncode(b *testing.B) {
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		//: only StreamingCodec implementations participate.
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			//: structurally not a StreamingCodec — nothing to measure.
			continue
		}
		for _, sz := range benchSizes {
			//: codec-native record batch (xmlDoc / int64 / complexRT / …).
			records := streamRecordsFor(string(f), sz)
			b.Run(string(f)+"/"+sz, makeStreamEncodeBench(stream, records))
		}
	}
}

// makeStreamEncodeBench builds the BenchmarkStreamEncode subbench closure.
// The records slice carries codec-native typed values inside an []any so
// every codec drives its Encode through its own concrete payload type.
func makeStreamEncodeBench(stream corecodec.StreamingCodec, records []any) func(b *testing.B) {
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
// it in a fresh bytes.Reader and drains the codec-appropriate record
// count. Single-document codecs (TOML) decode exactly one record per
// iteration — the multi-record variant is meaningless for them.
func BenchmarkStreamDecode(b *testing.B) {
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			//: structurally not a StreamingCodec — nothing to measure.
			continue
		}
		for _, sz := range benchSizes {
			//: codec-native record batch the encoder will emit, then
			//: the matching decoder will drain.
			records := streamRecordsFor(string(f), sz)
			seed, serr := seedStream(stream, records)
			if serr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed encode failed", serr))
				continue
			}
			b.Run(string(f)+"/"+sz, makeStreamDecodeBench(stream, string(f), len(records), seed))
		}
	}
}

// makeStreamDecodeBench builds the BenchmarkStreamDecode subbench closure.
// recordCount + name drive the per-iter decode loop: name picks the decode
// target shape (xmlDoc / int64 / complexRT / …), recordCount tells the
// loop how many Decode calls to issue.
func makeStreamDecodeBench(stream corecodec.StreamingCodec, name string, recordCount int, seed []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			//: fresh reader per iter — Decoders consume the
			//: reader and every iter starts fresh.
			dec := stream.NewDecoder(bytes.NewReader(seed))
			for range recordCount {
				//: target shape matches what streamRecordsFor emitted.
				target := newStreamDecodeTarget(name)
				if derr := dec.Decode(target); derr != nil {
					b.Fatalf("Decode: %v", derr)
				}
				benchSink = target
			}
		}
	}
}

// streamRecordsFor returns the codec-appropriate record batch the
// streaming Encode/Decode pair will iterate over. The slice type is
// []any so every codec drives its Encode through its own concrete
// payload type (xmlDoc for xml, int64 for tlv, complexRT for the
// universal-shape codecs). Single-document codecs (TOML) return a
// one-element slice so the Decode loop matches the codec's grammar.
func streamRecordsFor(name, size string) []any {
	//: lower-case once for case-insensitive dispatch.
	switch strings.ToLower(name) {
	//: xml streams concatenated <doc>…</doc> elements; xmlDoc round-trips
	//: through encoding/xml's Encoder + Decoder identically per record.
	case "xml":
		//: scale via record count, not field count — xml's bench cost is
		//: dominated by start-element / end-element bookkeeping.
		base := sampleXML()
		recs := make([]any, 0, streamRecordCount)
		for range streamRecordCount {
			recs = append(recs, base)
		}
		return recs
	//: tlv streams primitive int64 records (the TLV encoder dispatches
	//: on reflect.Kind, so a scalar exercises the fast path). Three
	//: distinct values keep DCE honest.
	case "tlv":
		return []any{int64(-64_000_000_000), int64(1 << 30), int64(-1 << 29)}
	//: toml is single-document by design — pelletier/go-toml.Decoder
	//: reads the entire stream into one value, so the bench emits ONE
	//: record per stream. Multi-record concat would yield invalid TOML.
	case "toml":
		base := tweakForCodec("toml", sampleComplex())
		switch size {
		case "medium":
			base = scaleComplex(base, 100)
		case "large":
			base = scaleComplex(base, 1000)
		}
		return []any{base}
	}
	//: universal-shape codecs (json, yaml, cbor, msgpack, baseenc family).
	//: streamRecordCount records keep the Encode/Decode loop measurable.
	typed := buildStreamRecords(name, size)
	//: indexed-assignment widens []complexRT into []any without the
	//: make-range-append pattern that triggers KTN-VAR-SLICECLONE
	//: (slices.Clone can't change element type, so it doesn't apply).
	out := make([]any, len(typed))
	for i, r := range typed {
		out[i] = r
	}
	return out
}

// newStreamDecodeTarget returns a fresh, codec-shaped pointer the
// streaming decoder writes through. Symmetric with streamRecordsFor:
// every record type emitted there has a matching target here.
func newStreamDecodeTarget(name string) any {
	//: lower-case once for case-insensitive dispatch.
	switch strings.ToLower(name) {
	case "xml":
		//: encoding/xml.Decoder.Decode reads into a typed struct.
		var d xmlDoc
		return &d
	case "tlv":
		//: tlv scalar — Decode writes through *int64.
		var i int64
		return &i
	}
	//: universal target is *complexRT — every other streaming codec
	//: round-trips that shape.
	var c complexRT
	return &c
}

// buildStreamRecords synthesises streamRecordCount sequential complexRT
// records for the universal-shape streaming benches. Records share the
// size-appropriate fixture and offset their integer fields via nextInt
// so each record is distinguishable on decode.
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

// seedStream pre-encodes the record batch through stream's encoder and
// returns the resulting bytes for the decode bench to consume. The
// records slice is []any so single-doc codecs (TOML, 1 record) and
// multi-doc codecs (3 records) share the same plumbing.
func seedStream(stream corecodec.StreamingCodec, records []any) ([]byte, error) {
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

// ═══════════════════════════════════════════════════════════════════════════
// BENCH.md report generator — driven by `make bench` (bazel run … --
// -test.run=TestGenerateBenchMD). Runs every Benchmark* function above
// programmatically via testing.Benchmark, pivots the rows by codec,
// computes a per-column mean baseline, and writes pkg/v1/codec/BENCH.md
// next to this file (the bazel sandbox is lifted via $BUILD_WORKSPACE_DIRECTORY).
//
// Living here keeps the bench logic + the report in one file. The
// KTN-TEST-PLACEMENT linter rule (Test functions belong in
// _internal/_external) is excluded for this file in .ktn-linter.yaml —
// the report-gen Test reuses the package-local makeXxxBench helpers,
// which only exist in this file.
// ═══════════════════════════════════════════════════════════════════════════

// TestGenerateBenchMD runs the full bench matrix programmatically and
// rewrites pkg/v1/codec/BENCH.md. The canonical entry point is
// `make bench`, which translates to:
//
//	bazel run //pkg/v1/codec:codec_bench_test -- \
//	    -test.run=TestGenerateBenchMD -test.timeout=1h -test.benchtime=2s
//
// `bazel run` exposes BUILD_WORKSPACE_DIRECTORY so the report lands in
// the source tree, not the runfiles sandbox. The default BUILD.bazel
// args (`-test.run=^$`) keep this test off `bazel test //...` runs —
// only an explicit `-test.run=TestGenerateBenchMD` triggers it.
func TestGenerateBenchMD(t *testing.T) {
	t.Helper()
	rows := runAllBenches()
	env := collectMachineEnvelope()
	md := renderBenchMarkdown(env, rows)
	outPath := resolveBenchOutputPath()
	if werr := os.WriteFile(outPath, []byte(md), 0o644); werr != nil {
		t.Fatalf("write %s: %v", outPath, werr)
	}
	t.Logf("wrote %s (%d rows)", outPath, len(rows))
}

// resolveBenchOutputPath returns the absolute path where BENCH.md will
// be written. Three runtime contexts are supported:
//
//  1. `bazel run` — bazel sets BUILD_WORKSPACE_DIRECTORY to the user's
//     workspace root, so the file lands at <root>/pkg/v1/codec/BENCH.md.
//  2. `go test ./pkg/v1/codec` — CWD is already pkg/v1/codec/, so a
//     bare "BENCH.md" lands correctly next to the sources.
//  3. `bazel test` — the test runs in a sandbox; the file is written
//     there and lost on cleanup. The Makefile target uses `bazel run`
//     (which lifts the sandbox) for that reason.
func resolveBenchOutputPath() string {
	if root, ok := os.LookupEnv("BUILD_WORKSPACE_DIRECTORY"); ok && root != "" {
		return root + "/" + benchOutputRelativePath
	}
	return "BENCH.md"
}

// runAllBenches drives every BenchmarkXxx family through testing.Benchmark
// and returns a flat row list ready for rendering. Zero duplication
// between `go test -bench=.` and this report generator — both call the
// exact same make<Op>Bench helpers.
func runAllBenches() []benchReportRow {
	rows := make([]benchReportRow, 0, 400)
	formats := codec.Available()

	rows = collectMarshal(rows, formats)
	rows = collectUnmarshal(rows, formats)
	rows = collectMarshalParallel(rows, formats)
	rows = collectUnmarshalParallel(rows, formats)
	rows = collectAppend(rows, formats)
	rows = collectStreamEncode(rows, formats)
	rows = collectStreamDecode(rows, formats)
	return rows
}

// collectMarshal benches every codec × size through codec.Marshal.
func collectMarshal(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			r := testing.Benchmark(makeMarshalBench(f, payload))
			rows = append(rows, toRow("Marshal", string(f), sz, r))
		}
	}
	return rows
}

// collectUnmarshal benches every codec × size; Marshal is hoisted as a
// one-shot seed outside b.Loop so only the decode cost is timed.
func collectUnmarshal(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			data, merr := codec.Marshal(f, payload)
			if merr != nil {
				//: skip — Marshal already failed, decode would be moot.
				continue
			}
			r := testing.Benchmark(makeUnmarshalBench(f, payload, data))
			rows = append(rows, toRow("Unmarshal", string(f), sz, r))
		}
	}
	return rows
}

// collectMarshalParallel measures Marshal under b.RunParallel
// (GOMAXPROCS goroutines, shared codec singleton).
func collectMarshalParallel(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			r := testing.Benchmark(makeMarshalParallelBench(f, payload))
			rows = append(rows, toRow("MarshalParallel", string(f), sz, r))
		}
	}
	return rows
}

// collectUnmarshalParallel benches Unmarshal under b.RunParallel.
func collectUnmarshalParallel(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			data, merr := codec.Marshal(f, payload)
			if merr != nil {
				continue
			}
			r := testing.Benchmark(makeUnmarshalParallelBench(f, payload, data))
			rows = append(rows, toRow("UnmarshalParallel", string(f), sz, r))
		}
	}
	return rows
}

// collectAppend benches the optional Appender extension. Codecs that
// do NOT implement Appender contribute no rows — the optional interface
// absence is a structural fact, not a benchmark gap.
func collectAppend(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		appender, supports := c.(corecodec.Appender)
		if !supports {
			continue
		}
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			r := testing.Benchmark(makeAppendBench(appender, payload))
			rows = append(rows, toRow("Append", string(f), sz, r))
		}
	}
	return rows
}

// collectStreamEncode benches Encoder.Encode for codecs implementing
// StreamingCodec. Per-codec record shape via streamRecordsFor.
func collectStreamEncode(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			continue
		}
		for _, sz := range benchSizes {
			records := streamRecordsFor(string(f), sz)
			r := testing.Benchmark(makeStreamEncodeBench(stream, records))
			rows = append(rows, toRow("StreamEncode", string(f), sz, r))
		}
	}
	return rows
}

// collectStreamDecode benches Decoder.Decode; the encoded seed is
// pre-built once per (format, size) outside the timer.
func collectStreamDecode(rows []benchReportRow, formats []codec.Format) []benchReportRow {
	for _, f := range formats {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		stream, supports := c.(corecodec.StreamingCodec)
		if !supports {
			continue
		}
		for _, sz := range benchSizes {
			records := streamRecordsFor(string(f), sz)
			seed, serr := seedStream(stream, records)
			if serr != nil {
				continue
			}
			r := testing.Benchmark(makeStreamDecodeBench(stream, string(f), len(records), seed))
			rows = append(rows, toRow("StreamDecode", string(f), sz, r))
		}
	}
	return rows
}

// toRow projects a testing.BenchmarkResult onto the typed row record
// the renderer consumes.
func toRow(category, codec, size string, r testing.BenchmarkResult) benchReportRow {
	return benchReportRow{
		Category:    category,
		Codec:       codec,
		Size:        size,
		Iters:       r.N,
		NsPerOp:     r.NsPerOp(),
		BPerOp:      r.AllocedBytesPerOp(),
		AllocsPerOp: r.AllocsPerOp(),
	}
}

// ─── Markdown renderer ──────────────────────────────────────────────────────

// renderBenchMarkdown assembles BENCH.md from the typed envelope + row
// set. The output structure is documented in BENCH.md itself: an
// envelope table, then one pivoted sub-table per (operation, size)
// combination, each ranked by ns/op with mean-baseline deltas.
func renderBenchMarkdown(env machineEnvelope, rows []benchReportRow) string {
	var b strings.Builder
	writeReportHeader(&b)
	writeEnvelopeTable(&b, env)
	writeResultsIntro(&b)
	writeAllPivotTables(&b, rows)
	writeReproduce(&b)
	return b.String()
}

// writeReportHeader emits the markdown title block + the auto-generated
// banner that warns readers not to hand-edit the file.
func writeReportHeader(b *strings.Builder) {
	b.WriteString("<!-- generated by pkg/v1/codec/codec_bench_test.go — do not edit by hand -->\n")
	b.WriteString("# Benchmarks — `pkg/v1/codec`\n\n")
	b.WriteString("Bazel target: `pkg/v1/codec:codec_bench_test` (tag `manual,benchmark` — excluded from `bazel test //...`)\n\n")
}

// writeEnvelopeTable emits the per-machine reproducibility envelope so
// cross-machine deltas can be evaluated honestly.
func writeEnvelopeTable(b *strings.Builder, env machineEnvelope) {
	b.WriteString("## Reproducibility envelope\n\n")
	b.WriteString("> **Numbers vary across machines.** This report stamps the box that produced\n")
	b.WriteString("> them so cross-machine deltas can be evaluated honestly.\n\n")
	b.WriteString("| Dimension | Value |\n|---|---|\n")
	fmt.Fprintf(b, "| CPU                | %s |\n", env.CPU)
	fmt.Fprintf(b, "| CPU cores          | %s |\n", env.CPUCores)
	fmt.Fprintf(b, "| CPU frequency      | %s |\n", env.CPUFrequency)
	fmt.Fprintf(b, "| RAM                | %s |\n", env.RAM)
	fmt.Fprintf(b, "| OS                 | %s |\n", env.OS)
	fmt.Fprintf(b, "| Kernel             | %s |\n", env.Kernel)
	fmt.Fprintf(b, "| Architecture       | %s |\n", env.Architecture)
	fmt.Fprintf(b, "| Hostname           | %s |\n", env.Hostname)
	fmt.Fprintf(b, "| Go toolchain       | %s |\n", env.GoToolchain)
	fmt.Fprintf(b, "| Bazel              | %s |\n", env.Bazel)
	fmt.Fprintf(b, "| Git branch         | %s |\n", env.GitBranch)
	fmt.Fprintf(b, "| Git commit         | %s |\n", env.GitCommit)
	fmt.Fprintf(b, "| Generated (UTC)    | %s |\n", env.GeneratedAt)
	fmt.Fprintf(b, "| Bench wall-clock   | %s |\n\n", env.BenchTime)
}

// writeResultsIntro explains the mean-baseline + color convention used
// in every sub-table below.
func writeResultsIntro(b *strings.Builder) {
	b.WriteString("## Results\n\n")
	b.WriteString("Each `(operation, size)` table is pivoted by codec and ranked by `ns/op`. Cells compare each codec to the **arithmetic mean** of every codec in that table:\n\n")
	b.WriteString("- 🟢 = **below** the mean for that column (faster, lighter, fewer allocs — or more iters for `Iters`).\n")
	b.WriteString("- 🔴 = **above** the mean.\n")
	b.WriteString("- Delta is shown as `±X%` up to ±999% and `×N` / `÷N` past 10× either way.\n\n")
	b.WriteString("Every registered Format participates in the average — `flatbuffers` (passthrough) and `tlv` (scalar payload) are included verbatim, so the mean reflects the full surface.\n\n")
}

// writeAllPivotTables emits one sub-table per (category, size) pair, in
// a stable order so reviewers always see Marshal small/medium/large
// first, then Unmarshal, etc.
func writeAllPivotTables(b *strings.Builder, rows []benchReportRow) {
	categories := []string{"Marshal", "Unmarshal", "MarshalParallel", "UnmarshalParallel", "Append", "StreamEncode", "StreamDecode"}
	for _, cat := range categories {
		for _, sz := range benchSizes {
			subset := filterRows(rows, cat, sz)
			if len(subset) == 0 {
				continue
			}
			writePivotTable(b, cat, sz, subset)
		}
	}
}

// filterRows returns the subset of rows matching the given (category,
// size) pair. Callers use it to feed writePivotTable.
func filterRows(rows []benchReportRow, category, size string) []benchReportRow {
	out := make([]benchReportRow, 0, len(rows))
	for _, r := range rows {
		if r.Category == category && r.Size == size {
			out = append(out, r)
		}
	}
	return out
}

// writePivotTable emits one mean-baseline sub-table. The codec column
// is sorted by ns/op ascending so the fastest codecs sit at the top.
func writePivotTable(b *strings.Builder, category, size string, rows []benchReportRow) {
	means := computeMeans(rows)
	//: slices.SortFunc is the Go 1.21+ generic, reflection-free alternative
	//: to sort.Slice (KTN-VAR-SORTALLOC).
	slices.SortFunc(rows, func(a, b benchReportRow) int {
		return cmp.Compare(a.NsPerOp, b.NsPerOp)
	})
	fmt.Fprintf(b, "### %s — %s payload\n\n", category, size)
	fmt.Fprintf(b,
		"> **Mean baseline** across %d codecs: `%s ns/op` · `%s B/op` · `%s allocs/op` · `%s iters`. "+
			"Cells are 🟢 when **below** the mean (faster / lighter / fewer allocs) and 🔴 when above.\n\n",
		len(rows),
		formatThousands(int64(means.nsop)),
		formatThousands(int64(means.bop)),
		formatThousands(int64(means.allocs)),
		formatThousands(int64(means.iters)),
	)
	b.WriteString("| Codec | Iters | ns/op | B/op | allocs/op |\n|---|---:|---:|---:|---:|\n")
	for _, r := range rows {
		itersCell := formatCell(float64(r.Iters), means.iters, true)
		nsopCell := formatCell(float64(r.NsPerOp), means.nsop, false)
		bopCell := formatCell(float64(r.BPerOp), means.bop, false)
		allocsCell := formatCell(float64(r.AllocsPerOp), means.allocs, false)
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n", r.Codec, itersCell, nsopCell, bopCell, allocsCell)
	}
	b.WriteString("\n")
}

// computeMeans averages every column of the given row set. Caller
// passes a non-empty slice; otherwise the means default to zero (which
// formatCell handles as "no comparison").
func computeMeans(rows []benchReportRow) meanSet {
	if len(rows) == 0 {
		return meanSet{}
	}
	var sumIters, sumNs, sumBop, sumAl float64
	for _, r := range rows {
		sumIters += float64(r.Iters)
		sumNs += float64(r.NsPerOp)
		sumBop += float64(r.BPerOp)
		sumAl += float64(r.AllocsPerOp)
	}
	n := float64(len(rows))
	return meanSet{
		iters:  sumIters / n,
		nsop:   sumNs / n,
		bop:    sumBop / n,
		allocs: sumAl / n,
	}
}

// formatCell renders one cell with its emoji + raw value + delta.
// higherIsBetter=true flips the colour rule (used for Iters, where more
// iters means the bench ran faster). The delta carries the sign so the
// reader doesn't have to recompute direction from the colour.
func formatCell(val, base float64, higherIsBetter bool) string {
	mark := "🔴"
	if higherIsBetter {
		if val >= base {
			mark = "🟢"
		}
	} else {
		if val <= base {
			mark = "🟢"
		}
	}
	if base == 0 {
		return fmt.Sprintf("%s %s", mark, formatThousands(int64(val)))
	}
	delta := (val - base) / base
	return fmt.Sprintf("%s %s %s", mark, formatThousands(int64(val)), formatDelta(delta, val/base))
}

// formatDelta renders the relative gap vs the baseline using the
// `±X%` / `×N` / `÷N` convention validated with the user. Stays in
// percent up to ±999% then switches to multiplicative ratios so the
// scale stays readable for the extreme outliers (yaml/large at
// ×64,000+).
func formatDelta(delta, ratio float64) string {
	if math.Abs(delta) < 0.005 {
		return "`±0%`"
	}
	if math.Abs(delta) < 10 {
		sign := ""
		if delta >= 0 {
			sign = "+"
		}
		return fmt.Sprintf("`%s%.0f%%`", sign, delta*100)
	}
	if delta > 0 {
		return fmt.Sprintf("`×%.1f`", ratio)
	}
	return fmt.Sprintf("`÷%.1f`", 1/ratio)
}

// formatThousands turns 5749803 into "5,749,803". Stays integer-only —
// the bench results are always whole numbers (ns/op rounds to int64 by
// testing.BenchmarkResult.NsPerOp).
func formatThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	//: walk right-to-left inserting a comma every three digits.
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// writeReproduce closes the report with the one-liner that regenerates
// it. The Reproduce shell block is the contract between the file and
// its operator.
func writeReproduce(b *strings.Builder) {
	b.WriteString("## Reproduce\n\n")
	b.WriteString("```shell\n")
	b.WriteString("make bench   # regenerate this report (long: ~15-25 min at -benchtime=2s)\n")
	b.WriteString("# or set a shorter wall-clock for a smoke regen:\n")
	b.WriteString("BENCH_TIME=200ms make bench\n")
	b.WriteString("```\n")
}

// ─── Machine envelope ──────────────────────────────────────────────────────

// collectMachineEnvelope gathers the reproducibility fingerprint that
// stamps every report. Cross-platform: prefers Linux /proc lookups,
// falls back to uname + sysctl on Darwin/BSD, never aborts on a missing
// piece (renders `unknown` instead).
func collectMachineEnvelope() machineEnvelope {
	host, herr := os.Hostname()
	if herr != nil {
		host = "unknown"
	}
	return machineEnvelope{
		CPU:          readCPUModel(),
		CPUCores:     strconv.Itoa(runtime.NumCPU()),
		CPUFrequency: readCPUFrequency(),
		RAM:          readRAM(),
		OS:           readOSName(),
		Kernel:       readKernel(),
		Architecture: runtime.GOARCH,
		Hostname:     host,
		GoToolchain:  runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
		Bazel:        readBazelVersion(),
		GitBranch:    readGit("rev-parse", "--abbrev-ref", "HEAD"),
		GitCommit:    readGit("rev-parse", "--short", "HEAD"),
		GeneratedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		BenchTime:    benchTimeDisplay(),
	}
}

// benchTimeDisplay returns the configured -test.benchtime value
// formatted for the envelope. Reads the testing package's flag directly
// so the envelope value always matches what testing.Benchmark used.
func benchTimeDisplay() string {
	if fl := flag.Lookup("test.benchtime"); fl != nil {
		return "`-test.benchtime=" + fl.Value.String() + "`"
	}
	return "`-test.benchtime=1s`"
}

// readCPUModel returns the human-readable CPU model. On Linux uses the
// `model name` line of /proc/cpuinfo; on Darwin falls back to `sysctl -n
// machdep.cpu.brand_string`; unknown otherwise.
func readCPUModel() string {
	if v, ok := readProcCPUField("model name"); ok {
		return v
	}
	if v, ok := readCmd("sysctl", "-n", "machdep.cpu.brand_string"); ok {
		return v
	}
	return "unknown"
}

// readCPUFrequency returns the per-core MHz reading from /proc/cpuinfo
// on Linux, falling back to sysctl on Darwin. Best-effort.
func readCPUFrequency() string {
	if v, ok := readProcCPUField("cpu MHz"); ok {
		return v + " MHz"
	}
	if v, ok := readCmd("sysctl", "-n", "hw.cpufrequency"); ok {
		return v + " Hz"
	}
	return "unknown"
}

// readRAM returns total RAM in GiB. Linux: MemTotal in /proc/meminfo.
// Darwin: sysctl hw.memsize. Best-effort.
func readRAM() string {
	if v, ok := readProcField("/proc/meminfo", "MemTotal:"); ok {
		v = strings.TrimSuffix(strings.TrimSpace(v), " kB")
		var kb int64
		if _, perr := fmt.Sscanf(v, "%d", &kb); perr == nil {
			return fmt.Sprintf("%.1f GiB", float64(kb)/1024/1024)
		}
	}
	if v, ok := readCmd("sysctl", "-n", "hw.memsize"); ok {
		var bytes int64
		if _, perr := fmt.Sscanf(v, "%d", &bytes); perr == nil {
			return fmt.Sprintf("%.1f GiB", float64(bytes)/1024/1024/1024)
		}
	}
	return "unknown"
}

// readOSName returns the human-friendly OS name. Linux: PRETTY_NAME in
// /etc/os-release. Darwin: `sw_vers -productName` + ` ` + version.
func readOSName() string {
	if v, ok := readOSReleaseField("PRETTY_NAME"); ok {
		return v
	}
	if name, ok := readCmd("sw_vers", "-productName"); ok {
		if ver, vok := readCmd("sw_vers", "-productVersion"); vok {
			return name + " " + ver
		}
		return name
	}
	return runtime.GOOS
}

// readKernel returns the kernel name + release (`Linux 6.x.y` /
// `Darwin 23.x.y`). Uses `uname -sr` which exists on every Unix.
func readKernel() string {
	if v, ok := readCmd("uname", "-sr"); ok {
		return v
	}
	return runtime.GOOS
}

// readBazelVersion returns the Bazel build label from `bazel version`.
// Optional — when bazel is absent the report renders `unknown` and the
// run still succeeds.
func readBazelVersion() string {
	out, oerr := exec.Command("bazel", "version").Output()
	if oerr != nil {
		return "unknown"
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "Build label:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return "unknown"
}

// readGit runs `git <args...>` and returns the trimmed stdout. Returns
// `unknown` on failure (e.g. running outside a checkout).
func readGit(args ...string) string {
	if v, ok := readCmd("git", args...); ok {
		return v
	}
	return "unknown"
}

// readCmd runs the given command and returns (trimmed stdout, true) on
// success or ("", false) on any failure.
func readCmd(name string, args ...string) (string, bool) {
	out, oerr := exec.Command(name, args...).Output()
	if oerr != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// readProcField returns the first matching field value from a /proc-
// style file (lines of `key: value`).
func readProcField(path, key string) (string, bool) {
	data, derr := os.ReadFile(path)
	if derr != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, key) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			return strings.TrimSpace(parts[1]), true
		}
	}
	return "", false
}

// readProcCPUField is the /proc/cpuinfo specialisation of readProcField
// — pinned to the first matching field so we don't pick up multi-core
// duplicates.
func readProcCPUField(key string) (string, bool) {
	return readProcField("/proc/cpuinfo", key)
}

// readOSReleaseField parses /etc/os-release for the given key. Values
// are typically double-quoted; quotes are stripped.
func readOSReleaseField(key string) (string, bool) {
	data, derr := os.ReadFile("/etc/os-release")
	if derr != nil {
		return "", false
	}
	prefix := key + "="
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			rest = strings.Trim(rest, "\"")
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}
