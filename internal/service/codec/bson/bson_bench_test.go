// Package bson_test — the BSON codec's own benchmarks. They reach the codec
// only through its registered singleton, so the same file measured the
// library-backed implementation this package replaced and measures the native
// one; BENCH.md compares the two runs.
package bson_test

import (
	"runtime"
	"strconv"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/bson"
)

// benchBatchSize is how many records the wide-document benchmarks carry: a
// page of results, large enough that per-record cost dominates the per-call
// overhead and far below the 10 MiB decode cap.
const benchBatchSize int = 1000

// benchScratchCap pre-sizes the Append destination, so the Append rows measure
// the encoder and not a buffer growing under it.
const benchScratchCap int = 1 << 20

// benchSinkBytes observes every encode so the compiler keeps the call.
var benchSinkBytes []byte

// benchSinkAny observes every untyped decode.
var benchSinkAny any

// benchAddress is the nested document every record carries.
type benchAddress struct {
	// Street is a short string.
	Street string `bson:"street"`
	// City is a short string.
	City string `bson:"city"`
	// Zip is a short string.
	Zip string `bson:"zip"`
}

// benchRecord is shaped like a stored document rather than a microbenchmark:
// one of each scalar a schema usually holds, a date, a list, a sub-document
// and a few raw bytes.
type benchRecord struct {
	// ID is a wide integer key.
	ID int64 `bson:"_id"`
	// Name is a short string.
	Name string `bson:"name"`
	// Email is a short string.
	Email string `bson:"email"`
	// Active is a boolean flag.
	Active bool `bson:"active"`
	// Score is a double.
	Score float64 `bson:"score"`
	// Visits is a small integer, written as int32.
	Visits int `bson:"visits"`
	// Created is a date, written as a UTC datetime.
	Created time.Time `bson:"created"`
	// Tags is an array of strings.
	Tags []string `bson:"tags"`
	// Address is an embedded document.
	Address benchAddress `bson:"address"`
	// Payload is a generic binary.
	Payload []byte `bson:"payload"`
}

// benchBatch wraps the records, since BSON's top level must be a document.
type benchBatch struct {
	// Records is the array of documents.
	Records []benchRecord `bson:"records"`
}

// benchRecordAt builds the i-th deterministic record.
func benchRecordAt(i int) benchRecord {
	//: the index seeds every field, so two records never encode identically.
	idx := strconv.Itoa(i)
	return benchRecord{
		ID:      int64(i) + 1<<40,
		Name:    "user-" + idx,
		Email:   "user-" + idx + "@example.com",
		Active:  i%2 == 0,
		Score:   float64(i) * 1.25,
		Visits:  i % 1000,
		Created: time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		Tags:    []string{"alpha", "beta-" + idx},
		Address: benchAddress{Street: idx + " Main Street", City: "Lyon", Zip: "69001"},
		Payload: []byte{0x00, 0x01, 0x02, 0xFE, 0xFF, byte(i)},
	}
}

// benchBatchValue builds the benchBatchSize-record document.
func benchBatchValue() benchBatch {
	//: pre-sized, so the fixture build is not what the benchmark measures.
	records := make([]benchRecord, 0, benchBatchSize)
	//: deterministic content.
	for i := range benchBatchSize {
		records = append(records, benchRecordAt(i))
	}
	return benchBatch{Records: records}
}

// benchMapValue builds a generic document: sixteen keys of mixed types, the
// shape a caller without a schema holds.
func benchMapValue() map[string]any {
	//: one entry per scalar family, plus a nested map and an array.
	entries := []struct {
		key   string
		value any
	}{
		{"name", "kitsunium"},
		{"replicas", int32(3)},
		{"uptime", int64(1 << 40)},
		{"ratio", 0.75},
		{"enabled", true},
		{"region", "eu-west-3"},
		{"owner", "platform"},
		{"tier", "gold"},
		{"zone", "a"},
		{"labels", map[string]any{"app": "sdk", "env": "prod"}},
		{"ports", []any{int32(80), int32(443)}},
		{"created", time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)},
		{"blob", []byte("kitsune")},
		{"weight", 12.5},
		{"version", "v0.16.0"},
		{"notes", ""},
	}
	doc := make(map[string]any, len(entries))
	//: the document, key by key.
	for _, e := range entries {
		doc[e.key] = e.value
	}
	return doc
}

// benchFixtures are the three payloads every encode row runs over.
func benchFixtures() []struct {
	name  string
	value any
} {
	//: a record, a page of records, and a schemaless document.
	return []struct {
		name  string
		value any
	}{
		{"record", benchRecordAt(7)},
		{"batch1000", benchBatchValue()},
		{"map16", benchMapValue()},
	}
}

// benchEncoded marshals v once, outside any timer, for the decode rows.
func benchEncoded(b *testing.B, v any) []byte {
	b.Helper()
	data, err := bson.New().Marshal(v)
	//: a fixture the codec refuses would make every decode row meaningless.
	if err != nil {
		b.Fatalf("Marshal(%T) = %v", v, err)
	}
	return data
}

// BenchmarkMarshal measures Marshal, which returns a slice the caller owns.
func BenchmarkMarshal(b *testing.B) {
	for _, fx := range benchFixtures() {
		b.Run(fx.name, func(b *testing.B) {
			codc := bson.New()
			b.ReportAllocs()
			b.SetBytes(int64(len(benchEncoded(b, fx.value))))
			for b.Loop() {
				out, err := codc.Marshal(fx.value)
				//: an encode failure inside the loop is a regression, not noise.
				if err != nil {
					b.Fatalf("Marshal = %v", err)
				}
				benchSinkBytes = out
			}
		})
	}
}

// BenchmarkAppend measures Append into a destination that already has room.
func BenchmarkAppend(b *testing.B) {
	for _, fx := range benchFixtures() {
		b.Run(fx.name, func(b *testing.B) {
			appender, ok := bson.New().(corecodec.Appender)
			//: the codec has always implemented the optional Appender.
			if !ok {
				b.Fatal("the BSON codec does not implement corecodec.Appender")
			}
			dst := make([]byte, 0, benchScratchCap)
			b.ReportAllocs()
			b.SetBytes(int64(len(benchEncoded(b, fx.value))))
			for b.Loop() {
				out, err := appender.Append(dst[:0], fx.value)
				//: an encode failure inside the loop is a regression, not noise.
				if err != nil {
					b.Fatalf("Append = %v", err)
				}
				benchSinkBytes = out
			}
		})
	}
}

// BenchmarkUnmarshalTyped decodes into the struct the document was written
// from — the path a caller with a schema takes.
func BenchmarkUnmarshalTyped(b *testing.B) {
	b.Run("record", func(b *testing.B) {
		data := benchEncoded(b, benchRecordAt(7))
		codc := bson.New()
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var got benchRecord
			//: a decode failure inside the loop is a regression, not noise.
			if err := codc.Unmarshal(data, &got); err != nil {
				b.Fatalf("Unmarshal = %v", err)
			}
			benchSinkAny = got
		}
	})
	b.Run("batch1000", func(b *testing.B) {
		data := benchEncoded(b, benchBatchValue())
		codc := bson.New()
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var got benchBatch
			//: a decode failure inside the loop is a regression, not noise.
			if err := codc.Unmarshal(data, &got); err != nil {
				b.Fatalf("Unmarshal = %v", err)
			}
			benchSinkAny = got
		}
	})
}

// BenchmarkUnmarshalMap decodes into map[string]any — the path a caller
// without a schema takes, where every value's Go type is chosen by the codec.
func BenchmarkUnmarshalMap(b *testing.B) {
	for _, fx := range benchFixtures() {
		b.Run(fx.name, func(b *testing.B) {
			data := benchEncoded(b, fx.value)
			codc := bson.New()
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var got map[string]any
				//: a decode failure inside the loop is a regression, not noise.
				if err := codc.Unmarshal(data, &got); err != nil {
					b.Fatalf("Unmarshal = %v", err)
				}
				benchSinkAny = got
			}
		})
	}
}

// BenchmarkMarshalParallel runs Marshal on every P, which is where a shared
// per-type cache would show contention.
func BenchmarkMarshalParallel(b *testing.B) {
	value := benchRecordAt(7)
	codc := bson.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(benchEncoded(b, value))))
	b.RunParallel(func(pb *testing.PB) {
		//: a per-goroutine sink: the package sinks are not safe to share.
		var out []byte
		for pb.Next() {
			var err error
			out, err = codc.Marshal(value)
			//: an encode failure inside the loop is a regression, not noise.
			if err != nil {
				b.Errorf("Marshal = %v", err)
				return
			}
		}
		runtime.KeepAlive(out)
	})
}

// BenchmarkUnmarshalParallel runs the typed decode on every P.
func BenchmarkUnmarshalParallel(b *testing.B) {
	data := benchEncoded(b, benchRecordAt(7))
	codc := bson.New()
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.RunParallel(func(pb *testing.PB) {
		//: a per-goroutine target: the package sinks are not safe to share.
		var got benchRecord
		for pb.Next() {
			got = benchRecord{}
			//: a decode failure inside the loop is a regression, not noise.
			if err := codc.Unmarshal(data, &got); err != nil {
				b.Errorf("Unmarshal = %v", err)
				return
			}
		}
		runtime.KeepAlive(got)
	})
}
