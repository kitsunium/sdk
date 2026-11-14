package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// benchPromotedSmall is the two-field struct the pre-checked Formats promote:
// the smallest value that still takes the promotion path.
type benchPromotedSmall struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// benchPromotedItem is the one element of benchPromotedRecord's list.
type benchPromotedItem struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Score float64  `json:"score"`
	Tags  []string `json:"tags"`
}

// benchPromotedRecord is the shape of the family's shared record (#899) at its
// small size: scalars and one item, 99 bytes of JSON.
type benchPromotedRecord struct {
	ID     int64               `json:"id"`
	Name   string              `json:"name"`
	Active bool                `json:"active"`
	Score  float64             `json:"score"`
	Items  []benchPromotedItem `json:"items"`
}

// BenchmarkMarshalPromoted measures Marshal of a struct into each Format that
// carries it through the facade's promotion (#910): the codec refuses the
// struct, so the facade encodes it as JSON and wraps it in the format's native
// shape. It uses the public API alone, so it compiles on every commit the A/B
// lane compares (scripts/ci/bench-ab.conf, suite codec-promote), and the
// allocs/op of a row is the figure the rule judges.
func BenchmarkMarshalPromoted(b *testing.B) {
	values := []struct {
		name string
		v    any
	}{
		{"small", benchPromotedSmall{ID: 7, Name: "ada"}},
		{"record", benchPromotedRecord{
			ID: 7, Name: "ada", Active: true, Score: 1.5,
			Items: []benchPromotedItem{{ID: 1, Name: "x", Score: 2.5, Tags: []string{"a"}}},
		}},
	}
	for _, f := range []codec.Format{codec.Form, codec.NDJSON, codec.CSV, codec.PEM} {
		b.Run(string(f), func(b *testing.B) {
			for _, tc := range values {
				b.Run(tc.name, func(b *testing.B) {
					if _, err := codec.Marshal(f, tc.v); err != nil {
						b.Fatalf("Marshal(%s): %v", f, err)
					}
					b.ReportAllocs()
					for b.Loop() {
						_, _ = codec.Marshal(f, tc.v)
					}
				})
			}
		})
	}
}
