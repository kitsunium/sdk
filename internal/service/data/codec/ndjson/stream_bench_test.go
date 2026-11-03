package ndjson_test

import (
	"bytes"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// streamBenchRecords is the batch #901's acceptance names: 64 small records.
const streamBenchRecords = 64

// streamBenchInput is 64 small records as NDJSON.
func streamBenchInput(b *testing.B) []byte {
	b.Helper()
	data, err := ndjson.New().Marshal(benchEvents(streamBenchRecords))
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkUnmarshal64 decodes the 64 records at once into a []benchEvent:
// the per-record figure #901's stream decoder is held to.
func BenchmarkUnmarshal64(b *testing.B) {
	data := streamBenchInput(b)
	c := ndjson.New()
	b.ReportAllocs()
	for b.Loop() {
		var out []benchEvent
		if err := c.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStreamDecode64 decodes the same records one Decode at a time from
// a reader, each into the same benchEvent — the stream's steady state.
func BenchmarkStreamDecode64(b *testing.B) {
	data := streamBenchInput(b)
	sc, ok := ndjson.New().(corecodec.StreamingCodec)
	if !ok {
		b.Fatal("ndjson does not implement codec.StreamingCodec")
	}
	r := bytes.NewReader(nil)
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(data)
		dec := sc.NewDecoder(r)
		var rec benchEvent
		for dec.More() {
			if err := dec.Decode(&rec); err != nil {
				b.Fatal(err)
			}
		}
	}
}
