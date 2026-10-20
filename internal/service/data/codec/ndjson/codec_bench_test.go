package ndjson_test

import (
	"strconv"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// benchEvent is the record shape NDJSON exists for: many small, flat
// records — log lines, events, rows — one per line.
type benchEvent struct {
	ID     int    `json:"id"`
	Kind   string `json:"kind"`
	Actor  string `json:"actor"`
	OK     bool   `json:"ok"`
	Millis int64  `json:"millis"`
}

// benchSink defeats dead-code elimination.
var benchSink []byte

// benchEvents builds n distinct records.
func benchEvents(n int) []benchEvent {
	out := make([]benchEvent, n)
	for i := range out {
		out[i] = benchEvent{ID: i, Kind: "login", Actor: "user-" + strconv.Itoa(i), OK: i%3 != 0, Millis: int64(i) * 7}
	}
	return out
}

// BenchmarkMarshalBatch prices Marshal over batches of small records, where
// the per-record cost — not the per-call constant — is what a caller pays.
func BenchmarkMarshalBatch(b *testing.B) {
	c := ndjson.New()
	for _, n := range []int{10, 1000} {
		recs := benchEvents(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := c.Marshal(recs)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = out
			}
		})
	}
}

// BenchmarkAppendBatch prices Append into a reused caller buffer over the
// same batches.
func BenchmarkAppendBatch(b *testing.B) {
	c, ok := ndjson.New().(corecodec.Appender)
	if !ok {
		b.Fatal("ndjson does not implement codec.Appender")
	}
	for _, n := range []int{10, 1000} {
		recs := benchEvents(n)
		dst := make([]byte, 0, 128*n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := c.Append(dst[:0], recs)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = out
			}
		})
	}
}
