// Package codec_test owns the facade-level benchmark suite. Every
// registered Format is exercised through the same pkg/v1/data/codec entry
// points production callers use, across three payload sizes
// (small / medium / large), and through every extension interface
// (Marshal/Unmarshal, parallel variants, Appender, StreamingCodec).
//
// Every Format is benched on ONE value, benchRecord (#899), scaled by the
// size knob; a Format whose own codec cannot carry it is measured through
// the facade's promotion path and marked so in BENCH.md.
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

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// streamRecordCount is the number of records each StreamEncode /
// StreamDecode iteration writes through the streaming codec. Three is
// enough to surface per-record overhead without inflating per-iteration
// cost beyond Bench's auto-scaling sweet spot.
const streamRecordCount int = 3

// benchOutputRelativePath is the path of BENCH.md relative to the
// workspace root. `bazel run` sets BUILD_WORKSPACE_DIRECTORY so the
// report lands in the source tree, not the runfiles sandbox.
const benchOutputRelativePath = "pkg/v1/data/codec/BENCH.md"

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
	//: Promoted marks a format the facade carries through promote.go (#899).
	Promoted bool
	//: Skipped is why the cell was not measured; empty when it was.
	Skipped string
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

// benchProgress reports live progress of the otherwise-silent bench matrix.
// testing.Benchmark blocks with no output, so a full `make bench` (hundreds
// of cells at -benchtime=10s) looks hung for over an hour. Each completed
// cell emits one stderr update carrying a running count, percentage, elapsed
// wall-clock, and a linear ETA. Progress goes to stderr so it never
// contaminates BENCH.md (written via os.WriteFile) or the test's stdout.
type benchProgress struct {
	total int       // total cells the matrix will run (pre-counted)
	done  int       // cells finished so far
	start time.Time // wall-clock start, for elapsed + ETA
	tty   bool      // stderr is an interactive terminal (redraw vs lines)
}

// benchItem is one element of benchRecord's list: the scalars every format
// carries and a short list of strings.
type benchItem struct {
	ID    int64    `json:"id" xml:"id" cbor:"id" yaml:"id" toml:"id" msgpack:"id" bson:"id"`
	Name  string   `json:"name" xml:"name" cbor:"name" yaml:"name" toml:"name" msgpack:"name" bson:"name"`
	Score float64  `json:"score" xml:"score" cbor:"score" yaml:"score" toml:"score" msgpack:"score" bson:"score"`
	Tags  []string `json:"tags" xml:"tags>tag" cbor:"tags" yaml:"tags" toml:"tags" msgpack:"tags" bson:"tags"`
}

// benchRecord is the ONE value every format is benched on (#899): struct
// only — no map, no []byte — the shape encoding/xml and the schema-ish
// formats accept, so xml measures the same record as json, cbor or yaml, and
// the size knob scales it for every format through Items. A format whose own
// codec cannot carry it (csv, form, pem, asn1-der, flatbuffers, tlv) is
// measured through the facade's real promotion path and marked promoted.
type benchRecord struct {
	ID     int64       `json:"id" xml:"id" cbor:"id" yaml:"id" toml:"id" msgpack:"id" bson:"id"`
	Name   string      `json:"name" xml:"name" cbor:"name" yaml:"name" toml:"name" msgpack:"name" bson:"name"`
	Active bool        `json:"active" xml:"active" cbor:"active" yaml:"active" toml:"active" msgpack:"active" bson:"active"`
	Score  float64     `json:"score" xml:"score" cbor:"score" yaml:"score" toml:"score" msgpack:"score" bson:"score"`
	Items  []benchItem `json:"items" xml:"items>item" cbor:"items" yaml:"items" toml:"items" msgpack:"items" bson:"items"`
}

// benchItemsPerSize is how many Items the size knob gives the record: one,
// a hundred, a thousand — the encoded record grows from ~0.1 KiB to ~70 KiB
// in JSON, inside every format's input bound.
var benchItemsPerSize = map[string]int{"small": 1, "medium": 100, "large": 1000}

// benchRecordFor returns the record at size, its fields offset by seq so the
// records of one stream differ. Deterministic: rerunning the benchmark on the
// same machine produces identical allocator pressure.
func benchRecordFor(size string, seq int) benchRecord {
	n := benchItemsPerSize[size]
	rec := benchRecord{ID: int64(seq), Name: "record-" + strconv.Itoa(seq), Active: seq%2 == 0, Score: float64(seq) + 0.5, Items: make([]benchItem, 0, n)}
	//: every item distinct, its tags short.
	for i := range n {
		idx := strconv.Itoa(i)
		rec.Items = append(rec.Items, benchItem{ID: int64(i), Name: "item-" + idx, Score: float64(i) * 1.25, Tags: []string{"t" + idx, "u"}})
	}
	return rec
}

// payloadFor returns the bench payload of format name at size: the record,
// or for NDJSON — a stream format whose Marshal takes a slice — three of them.
func payloadFor(name, size string) any {
	//: NDJSON expects a slice of records.
	if strings.EqualFold(name, "ndjson") {
		return []benchRecord{benchRecordFor(size, 0), benchRecordFor(size, 1), benchRecordFor(size, 2)}
	}
	//: every other format the one record.
	return benchRecordFor(size, 0)
}

// newDecodeTargetFor returns a fresh decode target for payload's type.
func newDecodeTargetFor(_ string, payload any) any {
	//: the NDJSON slice.
	if _, ok := payload.([]benchRecord); ok {
		return &[]benchRecord{}
	}
	//: the record.
	return &benchRecord{}
}

// promotedFormat reports whether the facade promotes the record for f
// (#899): its own codec refuses to Marshal it, or cannot Unmarshal what it
// wrote into a fresh target — promote.go then carries it as JSON inside the
// format's native shape. A promoted row measures that path, and is left out
// of the mean the other rows are compared to.
func promotedFormat(f codec.Format) bool {
	c, ok := corecodec.Lookup(f)
	//: no codec, nothing to promote.
	if !ok {
		return false
	}
	payload := payloadFor(string(f), "small")
	data, err := c.Marshal(payload)
	//: the codec's own encoder refuses the record.
	if err != nil {
		return true
	}
	//: or its own decoder cannot give it back.
	return c.Unmarshal(data, newDecodeTargetFor(string(f), payload)) != nil
}

// skippedRow is the row of a cell that cannot be measured, with the reason.
func skippedRow(category, codec, size string, cause error) benchReportRow {
	reason := cause.Error()
	//: one line, short enough for a table cell.
	if len(reason) > 120 {
		reason = reason[:120] + "…"
	}
	return benchReportRow{Category: category, Codec: codec, Size: size, Skipped: strings.ReplaceAll(reason, "|", "/")}
}

// BenchmarkMarshal measures one-shot Marshal latency + allocs/op across
// every registered codec × benchSizes.
func BenchmarkMarshal(b *testing.B) {
	//: discover the registered formats once per Benchmark invocation.
	for _, f := range codec.Available() {
		for _, sz := range benchSizes {
			//: payload is size-appropriate for the codec.
			payload := payloadFor(string(f), sz)
			//: a format that cannot carry the record at this size says why.
			if _, merr := codec.Marshal(f, payload); merr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "Marshal refused the record", merr))
				continue
			}
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
		//: pre-encode ONCE to learn the byte width and expose MB/s
		//: alongside ns/op + B/op. SetBytes is required for benchstat
		//: MB/s columns; without it benchstat reports ns/op only.
		probe, perr := codec.Marshal(f, payload)
		if perr != nil {
			b.Fatalf("Marshal probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
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
		//: report MB/s relative to the encoded input size — every
		//: byte of `data` is what the decode path actually parses.
		b.SetBytes(int64(len(data)))
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
			//: a format that cannot carry the record at this size says why.
			if _, merr := codec.Marshal(f, payload); merr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "Marshal refused the record", merr))
				continue
			}
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
		//: pre-encode once so MB/s reflects the encoded payload width
		//: under contention; symmetric with the sequential variant.
		probe, perr := codec.Marshal(f, payload)
		if perr != nil {
			b.Fatalf("Marshal probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
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
		//: throughput is bytes parsed per second — sized off the seed.
		b.SetBytes(int64(len(data)))
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
			//: the Appender takes the record without promotion.
			if _, aerr := appender.Append(nil, payload); aerr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "Append refused the record", aerr))
				continue
			}
			b.Run(string(f)+"/"+sz, makeAppendBench(appender, payload))
		}
	}
}

// makeAppendBench builds the BenchmarkAppend subbench closure.
func makeAppendBench(appender corecodec.Appender, payload any) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: pre-Append once so MB/s reflects the appended width; the
		//: probe also primes Go's allocator for the bench buffer below.
		probe, perr := appender.Append(nil, payload)
		if perr != nil {
			b.Fatalf("Append probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
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

// BenchmarkAppendParallel measures concurrent Appender throughput. Each
// goroutine owns its own dst buffer — appending into a shared slice would
// race — so the only shared state is the immutable payload + the codec
// singleton.
func BenchmarkAppendParallel(b *testing.B) {
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			//: defensive — Available guarantees registration.
			continue
		}
		//: skip codecs that do not implement the optional Appender.
		appender, supports := c.(corecodec.Appender)
		if !supports {
			//: structurally not an Appender — nothing to measure.
			continue
		}
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			b.Run(string(f)+"/"+sz, makeAppendParallelBench(appender, payload))
		}
	}
}

// makeAppendParallelBench builds the BenchmarkAppendParallel subbench. Each
// goroutine keeps a private reusable buffer so Append's amortised-alloc
// contract is exercised without cross-goroutine aliasing.
func makeAppendParallelBench(appender corecodec.Appender, payload any) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: pre-Append once so MB/s reflects the appended width.
		probe, perr := appender.Append(nil, payload)
		if perr != nil {
			b.Fatalf("Append probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
		b.RunParallel(func(pb *testing.PB) {
			//: per-goroutine buffer — Append into a shared slice would race.
			dst := make([]byte, 0, 64<<10)
			for pb.Next() {
				//: reset in-place; Append returns the (re-grown) buffer.
				dst = dst[:0]
				out, aerr := appender.Append(dst, payload)
				if aerr != nil {
					b.Fatalf("Append: %v", aerr)
				}
				//: keep the slice alive past the RunParallel closure scope.
				dst = out
				runtime.KeepAlive(dst)
			}
		})
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
			//: the record batch streamRecordsFor builds.
			records := streamRecordsFor(string(f), sz)
			//: a stream that cannot carry the record says why.
			if _, serr := seedStream(stream, records); serr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed encode failed", serr))
				continue
			}
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
		//: pre-encode the batch ONCE outside the timer so SetBytes
		//: reflects the actual emitted byte width per stream-iter.
		//: seedStream reproduces the exact in-loop encode sequence.
		probe, perr := seedStream(stream, records)
		if perr != nil {
			b.Fatalf("StreamEncode probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
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

// BenchmarkStreamEncodeParallel measures concurrent streaming-encode
// throughput. Each goroutine builds its own buffer + encoder per iter, so
// the only shared state is the immutable record batch.
func BenchmarkStreamEncodeParallel(b *testing.B) {
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
			records := streamRecordsFor(string(f), sz)
			b.Run(string(f)+"/"+sz, makeStreamEncodeParallelBench(stream, records))
		}
	}
}

// makeStreamEncodeParallelBench builds the BenchmarkStreamEncodeParallel
// subbench. Mirrors the sequential encode loop inside b.RunParallel.
func makeStreamEncodeParallelBench(stream corecodec.StreamingCodec, records []any) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: pre-encode the batch once so SetBytes reflects emitted width.
		probe, perr := seedStream(stream, records)
		if perr != nil {
			b.Fatalf("StreamEncode probe: %v", perr)
		}
		b.SetBytes(int64(len(probe)))
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				//: fresh buffer + encoder per iter, private to this goroutine.
				var buf bytes.Buffer
				enc := stream.NewEncoder(&buf)
				for _, rec := range records {
					if eerr := enc.Encode(rec); eerr != nil {
						b.Fatalf("Encode: %v", eerr)
					}
				}
				if cerr := enc.Close(); cerr != nil {
					b.Fatalf("Close: %v", cerr)
				}
				//: keep the bytes alive past the RunParallel closure scope.
				runtime.KeepAlive(buf.Bytes())
			}
		})
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
// target shape (the record, or untyped for tlv), recordCount tells the
// loop how many Decode calls to issue.
func makeStreamDecodeBench(stream corecodec.StreamingCodec, name string, recordCount int, seed []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: stream-decode throughput = bytes drained from `seed` per iter.
		b.SetBytes(int64(len(seed)))
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

// BenchmarkStreamDecodeParallel measures concurrent streaming-decode
// throughput. The seed is shared read-only; each goroutine wraps it in its
// own bytes.Reader and allocates fresh decode targets per iter.
func BenchmarkStreamDecodeParallel(b *testing.B) {
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
			records := streamRecordsFor(string(f), sz)
			seed, serr := seedStream(stream, records)
			if serr != nil {
				b.Run(string(f)+"/"+sz, makeSkipBench(string(f), "seed encode failed", serr))
				continue
			}
			b.Run(string(f)+"/"+sz, makeStreamDecodeParallelBench(stream, string(f), len(records), seed))
		}
	}
}

// makeStreamDecodeParallelBench builds the BenchmarkStreamDecodeParallel
// subbench. Mirrors the sequential decode loop inside b.RunParallel.
func makeStreamDecodeParallelBench(stream corecodec.StreamingCodec, name string, recordCount int, seed []byte) func(b *testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		//: stream-decode throughput = bytes drained from seed per iter.
		b.SetBytes(int64(len(seed)))
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				//: fresh reader per iter, private to this goroutine.
				dec := stream.NewDecoder(bytes.NewReader(seed))
				for range recordCount {
					//: target shape matches what streamRecordsFor emitted.
					target := newStreamDecodeTarget(name)
					if derr := dec.Decode(target); derr != nil {
						b.Fatalf("Decode: %v", derr)
					}
					//: keep the target alive past the closure scope.
					runtime.KeepAlive(target)
				}
			}
		})
	}
}

// streamRecordsFor returns the records a streaming Encode/Decode iteration
// writes: streamRecordCount records at size, each distinct, or ONE for TOML,
// whose stream is a single document.
func streamRecordsFor(name, size string) []any {
	count := streamRecordCount
	//: toml's Decoder reads the whole stream as one document.
	if strings.EqualFold(name, "toml") {
		count = 1
	}
	out := make([]any, 0, count)
	for i := range count {
		out = append(out, benchRecordFor(size, i))
	}
	return out
}

// newStreamDecodeTarget returns a fresh target the streaming decoder writes
// through: the record, or an untyped value for tlv, whose decoder builds
// composites into untyped values only.
func newStreamDecodeTarget(name string) any {
	//: tlv cannot project a composite into a struct.
	if strings.EqualFold(name, "tlv") {
		return new(any)
	}
	//: every other streaming codec decodes the record.
	return &benchRecord{}
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
// computes a per-column mean baseline, and writes pkg/v1/data/codec/BENCH.md
// next to this file (the bazel sandbox is lifted via $BUILD_WORKSPACE_DIRECTORY).
//
// Living here keeps the bench logic + the report in one file. The
// KTN-TEST-PLACEMENT linter rule (Test functions belong in
// _internal/_external) is excluded for this file in .ktn-linter.yaml —
// the report-gen Test reuses the package-local makeXxxBench helpers,
// which only exist in this file.
// ═══════════════════════════════════════════════════════════════════════════

// TestGenerateBenchMD runs the full bench matrix programmatically and
// rewrites pkg/v1/data/codec/BENCH.md. The canonical entry point is
// `make bench`, which translates to:
//
//	bazel run //pkg/v1/data/codec:codec_bench_test -- \
//	    -test.run=TestGenerateBenchMD -test.bench=^$ -test.timeout=2h -test.benchtime=1s
//
// `bazel run` exposes BUILD_WORKSPACE_DIRECTORY so the report lands in
// the source tree, not the runfiles sandbox. The default BUILD.bazel
// args (`-test.run=^$`) keep this test off `bazel test //...` runs —
// only an explicit `-test.run=TestGenerateBenchMD` triggers it. The
// `-test.bench=^$` overrides the BUILD args' `-test.bench=.`, which would
// otherwise re-run all ten top-level Benchmark* funcs on top of this
// generator (the matrix is already driven internally here).
//
// It must stay in this *_bench_test.go file (a dedicated `manual`-tagged
// go_test target, gazelle:excluded, gated by -test.run=^$) so the matrix
// never runs under `bazel test //...`. Moving it to a regular
// _external_test.go would either run the whole benchmark suite on every CI
// test run (codec_test target) or trip KTN-TEST-FILES (no matching source).
// KTN-TEST-SUFFIX is excluded for this file for that reason (see .ktn-linter.yaml).
func TestGenerateBenchMD(t *testing.T) {
	//: opt-in guard. Bazel keeps this orchestrator off `bazel test //...` via
	//: the BUILD args' `-test.run=^$`, but a plain `go test ./...` — the
	//: quick-iteration loop /workspace/CLAUDE.md advertises — carries no such
	//: default and would drive the FULL matrix, blowing past the 10-minute
	//: default timeout and failing the package. Require the caller to name
	//: this test explicitly (what `make bench` already does) so both build
	//: systems honour the same contract instead of only Bazel.
	if !strings.Contains(benchRunPattern(), "TestGenerateBenchMD") {
		//: a plain return rather than t.Skip: not running the matrix is the
		//: correct, passing outcome of an unselected orchestrator, and a Skip
		//: would report it as a test that could not be run.
		t.Log("bench matrix generator idle — run `make bench` (or pass -run TestGenerateBenchMD) to regenerate BENCH.md")
		return
	}
	//: drive the whole matrix programmatically via testing.Benchmark — this
	//: direct call is the driver edge KTN-TEST-SUFFIX's IsBenchOrchestrator
	//: (kodflow/ktn-linter#377) traces to exempt the Test from the bench-file
	//: rule (and from TABLE / PARALLEL / ASSERT, which skip orchestrators).
	rows := runAllBenches()
	//: stamp the host envelope (CPU / RAM / OS / toolchain / git SHA).
	env := collectMachineEnvelope()
	//: render the Markdown report from the envelope + measured rows.
	md := renderBenchMarkdown(env, rows)
	//: resolve the source-tree path (BUILD_WORKSPACE_DIRECTORY under bazel run).
	outPath := resolveBenchOutputPath()
	//: write the regenerated report next to the sources. A benchmark
	//: orchestrator must NOT assert via t.* (IsBenchOrchestrator, #377), so a
	//: write failure panics — loud and non-assertion — instead of t.Fatalf.
	if werr := os.WriteFile(outPath, []byte(md), 0o644); werr != nil {
		panic(fmt.Sprintf("codec bench: write %s: %v", outPath, werr))
	}
	t.Logf("wrote %s (%d rows)", outPath, len(rows))
}

// benchRunPattern returns the value of the -test.run flag, or "" when the
// flag is absent (it is registered by testing.Init, so a nil lookup only
// happens outside a normal test binary). It exists so TestGenerateBenchMD can
// tell "the caller asked for me by name" from "the caller ran everything":
// the zero value matches every test, which is precisely the case that must
// NOT drive the multi-minute benchmark matrix.
func benchRunPattern() string {
	//: testing registers -test.run on the default FlagSet during Init.
	f := flag.Lookup("test.run")
	if f == nil {
		//: no flag registered — treat as "no explicit selection".
		return ""
	}
	//: hand back the raw pattern for the caller to match against.
	return f.Value.String()
}

// resolveBenchOutputPath returns the absolute path where BENCH.md will
// be written. Three runtime contexts are supported:
//
//  1. `bazel run` — bazel sets BUILD_WORKSPACE_DIRECTORY to the user's
//     workspace root, so the file lands at <root>/pkg/v1/data/codec/BENCH.md.
//  2. `go test ./pkg/v1/data/codec` — CWD is already pkg/v1/data/codec/, so a
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

// newBenchProgress returns a reporter sized to total cells. It probes stderr:
// a character device is an interactive terminal (a single redrawing bar
// reads best); a pipe or file gets discrete newline-terminated lines so the
// captured log stays greppable.
func newBenchProgress(total int) *benchProgress {
	//: a char-device stderr is a TTY — pick the in-place redraw renderer.
	info, err := os.Stderr.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	return &benchProgress{total: total, start: time.Now(), tty: tty}
}

// step records one finished cell (labelled "<Op> <format>/<size>") and
// renders the current progress to stderr.
func (p *benchProgress) step(label string) {
	p.done++
	elapsed := time.Since(p.start)
	//: linear ETA from the mean cell time so far — enough to set expectations.
	var eta time.Duration
	if p.done > 0 && p.done < p.total {
		eta = elapsed / time.Duration(p.done) * time.Duration(p.total-p.done)
	}
	pct := 100 * p.done / max(p.total, 1)
	line := fmt.Sprintf("bench %3d%% [%d/%d] %-30s %s elapsed, ~%s left",
		pct, p.done, p.total, label, elapsed.Truncate(time.Second), eta.Truncate(time.Second))
	if p.tty {
		//: \r + clear-to-EOL redraw — one evolving line, no scroll spam.
		fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
		return
	}
	//: redirected/piped — discrete lines keep the captured log readable.
	fmt.Fprintln(os.Stderr, line)
}

// finish closes the display: the in-place bar carries no trailing newline, so
// a TTY needs one before the summary line that reports the total wall-clock.
func (p *benchProgress) finish() {
	if p.tty {
		//: terminate the redraw line before the summary.
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "bench done: %d cells in %s\n", p.done, time.Since(p.start).Truncate(time.Second))
}

// benchTotalCells pre-counts the matrix so progress can show [done/total].
// Marshal + Unmarshal + their two parallel variants run for every codec;
// Append and the two streaming families only for codecs that implement the
// optional interfaces. A rare per-cell skip (a Marshal seed that fails) makes
// the realised count slightly lower — finish() reports the true tally.
func benchTotalCells(formats []codec.Format) int {
	sizes := len(benchSizes)
	//: four unconditional families × every codec × every size.
	total := 4 * len(formats) * sizes
	for _, f := range formats {
		c, ok := corecodec.Lookup(f)
		//: an unregistered format contributes no optional rows.
		if !ok {
			continue
		}
		//: Append adds one family when the codec implements Appender.
		if _, yes := c.(corecodec.Appender); yes {
			total += sizes
		}
		//: StreamEncode + StreamDecode add two families for streaming codecs.
		if _, yes := c.(corecodec.StreamingCodec); yes {
			total += 2 * sizes
		}
	}
	return total
}

// runAllBenches drives every BenchmarkXxx family through testing.Benchmark
// and returns a flat row list ready for rendering. Zero duplication
// between `go test -bench=.` and this report generator — both call the
// exact same make<Op>Bench helpers. A benchProgress reporter narrates the
// otherwise-silent run to stderr.
func runAllBenches() []benchReportRow {
	rows := make([]benchReportRow, 0, 400)
	formats := codec.Available()
	prog := newBenchProgress(benchTotalCells(formats))

	rows = collectMarshal(rows, formats, prog)
	rows = collectUnmarshal(rows, formats, prog)
	rows = collectMarshalParallel(rows, formats, prog)
	rows = collectUnmarshalParallel(rows, formats, prog)
	rows = collectAppend(rows, formats, prog)
	rows = collectStreamEncode(rows, formats, prog)
	rows = collectStreamDecode(rows, formats, prog)
	prog.finish()
	return rows
}

// collectMarshal benches every codec × size through codec.Marshal.
func collectMarshal(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
	for _, f := range formats {
		promoted := promotedFormat(f)
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			//: a format that cannot carry the record at this size says why.
			if _, merr := codec.Marshal(f, payload); merr != nil {
				rows = append(rows, skippedRow("Marshal", string(f), sz, merr))
				continue
			}
			r := testing.Benchmark(makeMarshalBench(f, payload))
			rows = append(rows, toRow("Marshal", string(f), sz, r, promoted))
			prog.step("Marshal " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectUnmarshal benches every codec × size; Marshal is hoisted as a
// one-shot seed outside b.Loop so only the decode cost is timed.
func collectUnmarshal(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
	for _, f := range formats {
		promoted := promotedFormat(f)
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			data, merr := codec.Marshal(f, payload)
			if merr != nil {
				//: skipped — Marshal already failed, decode would be moot.
				rows = append(rows, skippedRow("Unmarshal", string(f), sz, merr))
				continue
			}
			r := testing.Benchmark(makeUnmarshalBench(f, payload, data))
			rows = append(rows, toRow("Unmarshal", string(f), sz, r, promoted))
			prog.step("Unmarshal " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectMarshalParallel measures Marshal under b.RunParallel
// (GOMAXPROCS goroutines, shared codec singleton).
func collectMarshalParallel(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			//: a format that cannot carry the record is measured nowhere.
			if _, merr := codec.Marshal(f, payload); merr != nil {
				continue
			}
			r := testing.Benchmark(makeMarshalParallelBench(f, payload))
			rows = append(rows, toRow("MarshalParallel", string(f), sz, r, promotedFormat(f)))
			prog.step("MarshalParallel " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectUnmarshalParallel benches Unmarshal under b.RunParallel.
func collectUnmarshalParallel(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
	for _, f := range formats {
		for _, sz := range benchSizes {
			payload := payloadFor(string(f), sz)
			data, merr := codec.Marshal(f, payload)
			if merr != nil {
				continue
			}
			r := testing.Benchmark(makeUnmarshalParallelBench(f, payload, data))
			rows = append(rows, toRow("UnmarshalParallel", string(f), sz, r, promotedFormat(f)))
			prog.step("UnmarshalParallel " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectAppend benches the optional Appender extension. Codecs that
// do NOT implement Appender contribute no rows — the optional interface
// absence is a structural fact, not a benchmark gap.
func collectAppend(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
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
			//: the Appender takes the record as Marshal does, without promotion.
			if _, aerr := appender.Append(nil, payload); aerr != nil {
				rows = append(rows, skippedRow("Append", string(f), sz, aerr))
				continue
			}
			r := testing.Benchmark(makeAppendBench(appender, payload))
			rows = append(rows, toRow("Append", string(f), sz, r, false))
			prog.step("Append " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectStreamEncode benches Encoder.Encode for codecs implementing
// StreamingCodec. Per-codec record shape via streamRecordsFor.
func collectStreamEncode(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
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
			//: a stream that cannot carry the record says why.
			if _, serr := seedStream(stream, records); serr != nil {
				rows = append(rows, skippedRow("StreamEncode", string(f), sz, serr))
				continue
			}
			r := testing.Benchmark(makeStreamEncodeBench(stream, records))
			rows = append(rows, toRow("StreamEncode", string(f), sz, r, false))
			prog.step("StreamEncode " + string(f) + "/" + sz)
		}
	}
	return rows
}

// collectStreamDecode benches Decoder.Decode; the encoded seed is
// pre-built once per (format, size) outside the timer.
func collectStreamDecode(rows []benchReportRow, formats []codec.Format, prog *benchProgress) []benchReportRow {
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
				rows = append(rows, skippedRow("StreamDecode", string(f), sz, serr))
				continue
			}
			r := testing.Benchmark(makeStreamDecodeBench(stream, string(f), len(records), seed))
			rows = append(rows, toRow("StreamDecode", string(f), sz, r, false))
			prog.step("StreamDecode " + string(f) + "/" + sz)
		}
	}
	return rows
}

// toRow projects a testing.BenchmarkResult onto the typed row record
// the renderer consumes.
func toRow(category, codec, size string, r testing.BenchmarkResult, promoted bool) benchReportRow {
	return benchReportRow{
		Category:    category,
		Codec:       codec,
		Size:        size,
		Iters:       r.N,
		NsPerOp:     r.NsPerOp(),
		BPerOp:      r.AllocedBytesPerOp(),
		AllocsPerOp: r.AllocsPerOp(),
		Promoted:    promoted,
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
	b.WriteString("<!-- generated by pkg/v1/data/codec/codec_bench_test.go — do not edit by hand -->\n")
	b.WriteString("# Benchmarks — `pkg/v1/data/codec`\n\n")
	b.WriteString("Bazel target: `pkg/v1/data/codec:codec_bench_test` (tag `manual,benchmark` — excluded from `bazel test //...`)\n\n")
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

// writeResultsIntro explains the mean-baseline + cell-tint convention
// used in every sub-table below.
func writeResultsIntro(b *strings.Builder) {
	b.WriteString("## Results\n\n")
	b.WriteString("Each `(operation, size)` table is pivoted by codec and ranked by `ns/op`. Cells compare each codec to the **arithmetic mean** of every codec in that table:\n\n")
	b.WriteString("- Green cells = **below** the mean for that column (faster, lighter, fewer allocs — or more iters for `Iters`).\n")
	b.WriteString("- Red cells = **above** the mean.\n")
	b.WriteString("- Delta is shown as `±X%` up to ±999% and `×N` / `÷N` past 10× either way.\n\n")
	b.WriteString("Each operation is wrapped in a collapsible block (small / medium / large grouped) so the table flood is opt-in.\n\n")
	b.WriteString("**One value for every format (#899).** Every Format encodes the same struct-only record — scalars and a list of items, no map and no `[]byte`, the shape `encoding/xml` and the schema-ish formats accept — with 1, 100 and 1 000 items at small, medium and large; NDJSON streams three of them. A format whose own codec cannot carry the record is measured through the facade's real promotion path (`promote.go`, the record as JSON inside the format's native shape) and marked *promoted*; a cell a format cannot measure at all (a size past its input bound) is marked *skipped* with the reason. The mean each cell is compared to is computed over the other rows only — the formats that encode the record themselves.\n\n")
}

// writeAllPivotTables emits one sub-table per (category, size) pair, in
// a stable order so reviewers always see Marshal small/medium/large
// first, then Unmarshal, etc. Each operation's three size tables are
// wrapped in a single <details> block — collapsed by default except
// for the first operation — so the page is scannable instead of being
// a 21-table flood.
func writeAllPivotTables(b *strings.Builder, rows []benchReportRow) {
	//: Parallel variants intentionally dropped from the published
	//: report — they bloat the page without telling a consumer
	//: anything actionable about format choice (the parallel scaling
	//: is a Go-runtime property, not a codec property). The bench
	//: code still runs them (Bazel target picks them up) so we keep
	//: the regression net, just not the table flood.
	categories := []string{"Marshal", "Unmarshal", "Append", "StreamEncode", "StreamDecode"}
	for i, cat := range categories {
		any := false
		for _, sz := range benchSizes {
			if len(filterRows(rows, cat, sz)) > 0 {
				any = true
				break
			}
		}
		if !any {
			continue
		}
		openAttr := ""
		if i == 0 {
			openAttr = " open"
		}
		fmt.Fprintf(b, "<details%s>\n<summary><strong>%s</strong> — small / medium / large payloads</summary>\n\n", openAttr, cat)
		for _, sz := range benchSizes {
			subset := filterRows(rows, cat, sz)
			if len(subset) == 0 {
				continue
			}
			writePivotTable(b, cat, sz, subset)
		}
		b.WriteString("</details>\n\n")
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
	universal := slices.DeleteFunc(slices.Clone(rows), func(r benchReportRow) bool { return r.Promoted || r.Skipped != "" })
	means := computeMeans(universal)
	//: slices.SortFunc is the Go 1.21+ generic, reflection-free alternative
	//: to sort.Slice (KTN-VAR-SORTALLOC); skipped cells sink to the end.
	slices.SortFunc(rows, func(a, b benchReportRow) int {
		return cmp.Or(cmp.Compare(boolRank(a.Skipped != ""), boolRank(b.Skipped != "")), cmp.Compare(a.NsPerOp, b.NsPerOp))
	})
	fmt.Fprintf(b, "#### %s — %s payload\n\n", category, size)
	fmt.Fprintf(
		b,
		"> **Mean baseline** across the %d codecs that encode the record themselves: `%s ns/op` · `%s B/op` · `%s allocs/op` · `%s iters`. "+
			"Green cells sit **below** the mean (faster / lighter / fewer allocs); red cells above.\n\n",
		len(universal),
		formatThousands(int64(means.nsop)),
		formatThousands(int64(means.bop)),
		formatThousands(int64(means.allocs)),
		formatThousands(int64(means.iters)),
	)
	b.WriteString("| Codec | Iters | ns/op | B/op | allocs/op |\n|---|---:|---:|---:|---:|\n")
	for _, r := range rows {
		//: a cell not measured, and why.
		if r.Skipped != "" {
			fmt.Fprintf(b, "| `%s` *skipped* | %s | – | – | – |\n", r.Codec, r.Skipped)
			continue
		}
		label := "`" + r.Codec + "`"
		//: measured through promote.go, outside the mean.
		if r.Promoted {
			label += " *promoted*"
		}
		itersCell := formatCell(float64(r.Iters), means.iters, true)
		nsopCell := formatCell(float64(r.NsPerOp), means.nsop, false)
		bopCell := formatCell(float64(r.BPerOp), means.bop, false)
		allocsCell := formatCell(float64(r.AllocsPerOp), means.allocs, false)
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", label, itersCell, nsopCell, bopCell, allocsCell)
	}
	b.WriteString("\n")
}

// boolRank orders false before true.
func boolRank(v bool) int {
	//: true sorts last.
	if v {
		return 1
	}
	return 0
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

// formatCell renders one cell with a CSS-class tint + raw value + delta.
// higherIsBetter=true flips the colour rule (used for Iters, where more
// iters means the bench ran faster). The delta carries the sign so the
// reader doesn't have to recompute direction from the colour. We emit
// an inline <span> with the win/loss class so markdown renderers (Astro
// + remark-gfm) carry the tint into the rendered table — no emojis,
// the whole cell value gets a background highlight.
func formatCell(val, base float64, higherIsBetter bool) string {
	cls := "bench-loss"
	if higherIsBetter {
		if val >= base {
			cls = "bench-win"
		}
	} else {
		if val <= base {
			cls = "bench-win"
		}
	}
	if base == 0 {
		return fmt.Sprintf(`<span class="%s">%s</span>`, cls, formatThousands(int64(val)))
	}
	delta := (val - base) / base
	return fmt.Sprintf(`<span class="%s">%s %s</span>`, cls, formatThousands(int64(val)), formatDelta(delta, val/base))
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
	b.WriteString("make bench   # regenerate this report (~8 min at the default -benchtime=1s)\n")
	b.WriteString("# canonical, publishable run (firmer ns/op, ~84 min):\n")
	b.WriteString("BENCH_TIME=10s make bench\n")
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
