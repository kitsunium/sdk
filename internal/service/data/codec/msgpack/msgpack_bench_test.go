package msgpack_test

import (
	"bytes"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// The benchmarks below drive the codec ONLY through the core/data/codec contract
// (Marshal, Unmarshal, Append, NewEncoder, NewDecoder), so the same file
// measures whichever implementation sits behind msgpack.New(). That is what
// lets BENCH.md compare the vendor-backed codec with the native one on the
// same machine, the same payloads and the same harness.

// benchScaleMedium and benchScaleLarge multiply every collection field of the
// record — the same factors pkg/v1/data/codec/BENCH.md uses for its medium and
// large rows — so the encoded form is 611 B (small), 11 753 B (medium) and
// 122 085 B (large).
const (
	// benchScaleMedium is the collection length of the medium record.
	benchScaleMedium int = 100
	// benchScaleLarge is the collection length of the large record.
	benchScaleLarge int = 1000
	// benchStreamRecords is how many small records one stream benchmark
	// iteration writes and reads back.
	benchStreamRecords int = 64
	// benchAppendCap pre-sizes the Append destination so the benchmark times
	// the encoder and not the growth of the caller's buffer.
	benchAppendCap int = 2 << 20
)

// Sinks observe every result so the compiler cannot delete the call a
// benchmark exists to time.
var (
	// benchSinkBytes observes encode results.
	benchSinkBytes []byte
	// benchSinkRecord observes typed decode results.
	benchSinkRecord benchRecord
	// benchSinkAny observes untyped decode results.
	benchSinkAny any
)

// benchInner is the nested record the benchmark payload repeats.
type benchInner struct {
	Name   string            `msgpack:"name"`
	Score  float64           `msgpack:"score"`
	Tags   []string          `msgpack:"tags"`
	Extras map[string]string `msgpack:"extras"`
}

// benchRecord mirrors pkg/v1/data/codec's complexRT fixture: one field of every
// family the codec encodes, so the numbers describe a realistic payload and
// not a single type's fast path.
type benchRecord struct {
	Bool         bool                  `msgpack:"bool"`
	Int8         int8                  `msgpack:"int8"`
	Int16        int16                 `msgpack:"int16"`
	Int32        int32                 `msgpack:"int32"`
	Int64        int64                 `msgpack:"int64"`
	Uint8        uint8                 `msgpack:"uint8"`
	Uint16       uint16                `msgpack:"uint16"`
	Uint32       uint32                `msgpack:"uint32"`
	Uint64       uint64                `msgpack:"uint64"`
	Float32      float32               `msgpack:"float32"`
	Float64      float64               `msgpack:"float64"`
	Plain        string                `msgpack:"plain"`
	Unicode      string                `msgpack:"unicode"`
	Binary       []byte                `msgpack:"binary"`
	Ints         []int                 `msgpack:"ints"`
	Strings      []string              `msgpack:"strings"`
	Floats       []float64             `msgpack:"floats"`
	StringMap    map[string]int        `msgpack:"stringMap"`
	StringStruct map[string]benchInner `msgpack:"stringStruct"`
	Inner        benchInner            `msgpack:"inner"`
	PInner       *benchInner           `msgpack:"pInner"`
	Children     []benchInner          `msgpack:"children"`
	Timestamp    time.Time             `msgpack:"timestamp"`
}

// benchRecordOf builds a deterministic record whose collections hold scale
// entries each; scale 0 yields the small record.
func benchRecordOf(scale int) benchRecord {
	rec := benchRecord{
		Bool: true, Int8: -8, Int16: -1600, Int32: -32_000_000, Int64: -64_000_000_000,
		Uint8: 200, Uint16: 60_000, Uint32: 4_000_000_000, Uint64: 9_000_000_000,
		Float32: 3.1415927, Float64: 2.718281828459045,
		Plain:     "the quick brown fox jumps over the lazy dog",
		Unicode:   "héllo 世界 🌸 \u200fעברית",
		Binary:    []byte{0x00, 0x01, 0x02, 0xfe, 0xff},
		Ints:      []int{0, 1, 2, 3, 5, 8, 13, 21, 34},
		Strings:   []string{"alpha", "beta", "gamma", "δelta"},
		Floats:    []float64{0, -1.5, 1e10, 1e-10},
		StringMap: map[string]int{"one": 1, "two": 2, "three": 3},
		StringStruct: map[string]benchInner{
			"alpha": {Name: "alpha", Score: 1.5, Tags: []string{"x"}, Extras: map[string]string{"k": "v"}},
		},
		Inner:     benchInner{Name: "root", Score: 42.42, Tags: []string{"red", "green"}, Extras: map[string]string{"env": "test"}},
		PInner:    &benchInner{Name: "pinner", Score: 99.99, Tags: []string{"p1"}, Extras: map[string]string{"ptr": "yes"}},
		Children:  []benchInner{{Name: "c1", Score: 1, Tags: []string{"a"}}},
		Timestamp: time.Date(2024, time.January, 15, 9, 30, 0, 0, time.UTC),
	}
	for i := range scale {
		idx := strconv.Itoa(i)
		rec.Ints = append(rec.Ints, i)
		rec.Strings = append(rec.Strings, "s"+idx)
		rec.Floats = append(rec.Floats, float64(i)*0.5)
		rec.StringMap["k"+idx] = i
		rec.StringStruct["k"+idx] = benchInner{Name: "k" + idx, Score: float64(i) * 1.25, Tags: []string{"t" + idx}, Extras: map[string]string{"i": idx}}
		rec.Children = append(rec.Children, benchInner{Name: "c" + idx, Score: float64(i) * 0.75, Tags: []string{"x"}})
	}
	return rec
}

// benchSizes pairs a sub-benchmark name with the record it encodes.
func benchSizes() []struct {
	name string
	rec  benchRecord
} {
	return []struct {
		name string
		rec  benchRecord
	}{
		{"small", benchRecordOf(0)},
		{"medium", benchRecordOf(benchScaleMedium)},
		{"large", benchRecordOf(benchScaleLarge)},
	}
}

// benchEncoded returns the wire form of rec, failing the benchmark on error.
func benchEncoded(b *testing.B, rec benchRecord) []byte {
	b.Helper()
	data, err := msgpack.New().Marshal(rec)
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	return data
}

// BenchmarkMarshal measures one Marshal of a typed record.
func BenchmarkMarshal(b *testing.B) {
	c := msgpack.New()
	for _, sz := range benchSizes() {
		b.Run(sz.name, func(b *testing.B) {
			b.SetBytes(int64(len(benchEncoded(b, sz.rec))))
			b.ReportAllocs()
			for b.Loop() {
				out, err := c.Marshal(sz.rec)
				if err != nil {
					b.Fatal(err)
				}
				benchSinkBytes = out
			}
		})
	}
}

// BenchmarkAppend measures Append into a destination that already has room.
func BenchmarkAppend(b *testing.B) {
	ap, ok := msgpack.New().(codec.Appender)
	if !ok {
		b.Fatal("msgpack codec does not implement codec.Appender")
	}
	dst := make([]byte, 0, benchAppendCap)
	for _, sz := range benchSizes() {
		b.Run(sz.name, func(b *testing.B) {
			b.SetBytes(int64(len(benchEncoded(b, sz.rec))))
			b.ReportAllocs()
			for b.Loop() {
				out, err := ap.Append(dst[:0], sz.rec)
				if err != nil {
					b.Fatal(err)
				}
				benchSinkBytes = out
			}
		})
	}
}

// BenchmarkUnmarshal measures one Unmarshal into a fresh typed record.
func BenchmarkUnmarshal(b *testing.B) {
	c := msgpack.New()
	for _, sz := range benchSizes() {
		b.Run(sz.name, func(b *testing.B) {
			data := benchEncoded(b, sz.rec)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var out benchRecord
				if err := c.Unmarshal(data, &out); err != nil {
					b.Fatal(err)
				}
				benchSinkRecord = out
			}
		})
	}
}

// BenchmarkUnmarshalAny measures one Unmarshal into an untyped target — the
// path a caller takes when it does not know the document's shape.
func BenchmarkUnmarshalAny(b *testing.B) {
	c := msgpack.New()
	for _, sz := range benchSizes() {
		b.Run(sz.name, func(b *testing.B) {
			data := benchEncoded(b, sz.rec)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var out any
				if err := c.Unmarshal(data, &out); err != nil {
					b.Fatal(err)
				}
				benchSinkAny = out
			}
		})
	}
}

// BenchmarkStreamEncode measures a streaming Encoder writing
// benchStreamRecords small records per iteration.
func BenchmarkStreamEncode(b *testing.B) {
	sc, ok := msgpack.New().(codec.StreamingCodec)
	if !ok {
		b.Fatal("msgpack codec does not implement codec.StreamingCodec")
	}
	rec := benchRecordOf(0)
	b.SetBytes(int64(len(benchEncoded(b, rec)) * benchStreamRecords))
	b.ReportAllocs()
	var buf bytes.Buffer
	for b.Loop() {
		buf.Reset()
		enc := sc.NewEncoder(&buf)
		for range benchStreamRecords {
			if err := enc.Encode(rec); err != nil {
				b.Fatal(err)
			}
		}
		if err := enc.Close(); err != nil {
			b.Fatal(err)
		}
	}
	benchSinkBytes = buf.Bytes()
}

// BenchmarkStreamDecode measures a streaming Decoder reading
// benchStreamRecords small records back per iteration.
func BenchmarkStreamDecode(b *testing.B) {
	sc, ok := msgpack.New().(codec.StreamingCodec)
	if !ok {
		b.Fatal("msgpack codec does not implement codec.StreamingCodec")
	}
	one := benchEncoded(b, benchRecordOf(0))
	stream := bytes.Repeat(one, benchStreamRecords)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		dec := sc.NewDecoder(bytes.NewReader(stream))
		for {
			var out benchRecord
			err := dec.Decode(&out)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			benchSinkRecord = out
		}
	}
}
