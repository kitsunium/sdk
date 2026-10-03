package cbor_test

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// benchRecordCount is how many records the wide-message benchmarks carry: a
// page of a report, large enough for the per-record cost to dominate the
// per-call overhead.
const benchRecordCount int = 1000

// benchStreamCount is how many records one streaming benchmark iteration
// writes then reads back through a single Encoder / Decoder pair.
const benchStreamCount int = 100

// benchMapEntries is the width of the string-keyed map benchmarks; sixteen
// entries is where ordering the keys stops being free.
const benchMapEntries int = 16

// benchDstCap pre-sizes the Append destination so the Append benchmark
// measures the ENCODER and not the allocator growing a buffer under it.
const benchDstCap int = 1 << 12

// benchBlobBytes is the size of the byte-string field each record carries.
const benchBlobBytes int = 16

// Sinks observe every result so no encode or decode can be proven unused.
var (
	benchSinkBytes []byte
	benchSinkErr   error
	benchSinkAny   any
)

// benchSmall is the two-field struct the facade's universal round trip uses.
type benchSmall struct {
	// Name is a short text field.
	Name string `cbor:"name"`
	// Age is a small integer field.
	Age int `cbor:"age"`
}

// benchRecord is shaped like a real payload rather than like a
// microbenchmark: one of every family the codec maps.
type benchRecord struct {
	// ID is a wide integer.
	ID int64 `cbor:"id"`
	// Name is a short text field.
	Name string `cbor:"name"`
	// Email is a longer text field.
	Email string `cbor:"email"`
	// Active is a boolean.
	Active bool `cbor:"active"`
	// Score is a float64.
	Score float64 `cbor:"score"`
	// Tags is a slice of strings.
	Tags []string `cbor:"tags"`
	// Created is a whole-second timestamp.
	Created time.Time `cbor:"created"`
	// Attrs is a small string map.
	Attrs map[string]string `cbor:"attrs"`
	// Blob is a short byte string.
	Blob []byte `cbor:"blob"`
}

// newBenchRecord builds the i-th deterministic record.
func newBenchRecord(i int) benchRecord {
	idx := strconv.Itoa(i)
	return benchRecord{
		ID:      int64(i) * 7919,
		Name:    "user-" + idx,
		Email:   "user-" + idx + "@example.test",
		Active:  i%2 == 0,
		Score:   float64(i) * 1.25,
		Tags:    []string{"alpha", "beta", "t" + idx},
		Created: time.Unix(1_700_000_000+int64(i), 0).UTC(),
		Attrs:   map[string]string{"tier": "gold", "region": "eu-west", "k": idx},
		Blob:    bytes.Repeat([]byte{byte(i)}, benchBlobBytes),
	}
}

// newBenchRecords builds the wide-message fixture.
func newBenchRecords() []benchRecord {
	out := make([]benchRecord, benchRecordCount)
	for i := range out {
		out[i] = newBenchRecord(i)
	}
	return out
}

// newBenchMap builds the string-keyed map fixture.
func newBenchMap() map[string]int {
	out := make(map[string]int, benchMapEntries)
	for i := range benchMapEntries {
		out["key-"+strconv.Itoa(i)] = i
	}
	return out
}

// newBenchDoc builds an untyped document of the shape a JSON-like payload
// takes once it has been decoded without a schema.
func newBenchDoc() map[string]any {
	owner := make(map[string]any, 2)
	owner["name"], owner["age"] = "ada", uint64(36)
	doc := make(map[string]any, 6)
	doc["id"], doc["name"], doc["ok"] = uint64(4711), "kitsune", true
	doc["ratio"], doc["tags"], doc["owner"] = 0.75, []any{"a", "b", "c"}, owner
	return doc
}

// mustMarshal encodes v or stops the benchmark before it measures nothing.
func mustMarshal(b *testing.B, v any) []byte {
	b.Helper()
	data, err := cbor.New().Marshal(v)
	if err != nil {
		b.Fatalf("seed Marshal: %v", err)
	}
	return data
}

// BenchmarkMarshalSmall encodes the two-field struct.
func BenchmarkMarshalSmall(b *testing.B) {
	c := cbor.New()
	v := benchSmall{Name: "Ada", Age: 36}
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = c.Marshal(v)
	}
}

// BenchmarkUnmarshalSmall decodes the two-field struct.
func BenchmarkUnmarshalSmall(b *testing.B) {
	c := cbor.New()
	data := mustMarshal(b, benchSmall{Name: "Ada", Age: 36})
	b.ReportAllocs()
	for b.Loop() {
		var out benchSmall
		benchSinkErr = c.Unmarshal(data, &out)
		benchSinkAny = out
	}
}

// BenchmarkMarshalRecord encodes one realistic record.
func BenchmarkMarshalRecord(b *testing.B) {
	c := cbor.New()
	v := newBenchRecord(42)
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = c.Marshal(v)
	}
}

// BenchmarkAppendRecord encodes one realistic record into a pre-sized buffer.
func BenchmarkAppendRecord(b *testing.B) {
	ap, ok := cbor.New().(codec.Appender)
	if !ok {
		b.Fatal("cbor codec does not implement codec.Appender")
	}
	v := newBenchRecord(42)
	dst := make([]byte, 0, benchDstCap)
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = ap.Append(dst[:0], v)
	}
}

// BenchmarkUnmarshalRecord decodes one realistic record.
func BenchmarkUnmarshalRecord(b *testing.B) {
	c := cbor.New()
	data := mustMarshal(b, newBenchRecord(42))
	b.ReportAllocs()
	for b.Loop() {
		var out benchRecord
		benchSinkErr = c.Unmarshal(data, &out)
		benchSinkAny = out
	}
}

// BenchmarkMarshalMap16 encodes a sixteen-entry string-keyed map.
func BenchmarkMarshalMap16(b *testing.B) {
	c := cbor.New()
	v := newBenchMap()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = c.Marshal(v)
	}
}

// BenchmarkUnmarshalMap16 decodes a sixteen-entry string-keyed map.
func BenchmarkUnmarshalMap16(b *testing.B) {
	c := cbor.New()
	data := mustMarshal(b, newBenchMap())
	b.ReportAllocs()
	for b.Loop() {
		var out map[string]int
		benchSinkErr = c.Unmarshal(data, &out)
		benchSinkAny = out
	}
}

// BenchmarkMarshalDocAny encodes an untyped document.
func BenchmarkMarshalDocAny(b *testing.B) {
	c := cbor.New()
	v := newBenchDoc()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = c.Marshal(v)
	}
}

// BenchmarkUnmarshalDocAny decodes a document into an untyped target.
func BenchmarkUnmarshalDocAny(b *testing.B) {
	c := cbor.New()
	data := mustMarshal(b, newBenchDoc())
	b.ReportAllocs()
	for b.Loop() {
		var out any
		benchSinkErr = c.Unmarshal(data, &out)
		benchSinkAny = out
	}
}

// BenchmarkMarshalRecords1000 encodes a thousand records in one message.
func BenchmarkMarshalRecords1000(b *testing.B) {
	c := cbor.New()
	v := newBenchRecords()
	b.ReportAllocs()
	for b.Loop() {
		benchSinkBytes, benchSinkErr = c.Marshal(v)
	}
}

// BenchmarkUnmarshalRecords1000 decodes a thousand records in one message.
func BenchmarkUnmarshalRecords1000(b *testing.B) {
	c := cbor.New()
	data := mustMarshal(b, newBenchRecords())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		var out []benchRecord
		benchSinkErr = c.Unmarshal(data, &out)
		benchSinkAny = out
	}
}

// BenchmarkStreamEncode writes a hundred records through one Encoder.
func BenchmarkStreamEncode(b *testing.B) {
	sc, ok := cbor.New().(codec.StreamingCodec)
	if !ok {
		b.Fatal("cbor codec does not implement codec.StreamingCodec")
	}
	recs := newBenchRecords()[:benchStreamCount]
	var buf bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		enc := sc.NewEncoder(&buf)
		for i := range recs {
			benchSinkErr = enc.Encode(recs[i])
		}
		benchSinkErr = enc.Close()
	}
}

// BenchmarkStreamDecode reads a hundred records back through one Decoder.
func BenchmarkStreamDecode(b *testing.B) {
	sc, ok := cbor.New().(codec.StreamingCodec)
	if !ok {
		b.Fatal("cbor codec does not implement codec.StreamingCodec")
	}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	for _, r := range newBenchRecords()[:benchStreamCount] {
		if err := enc.Encode(r); err != nil {
			b.Fatalf("seed Encode: %v", err)
		}
	}
	data := buf.Bytes()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		dec := sc.NewDecoder(bytes.NewReader(data))
		for range benchStreamCount {
			var out benchRecord
			benchSinkErr = dec.Decode(&out)
		}
	}
}
