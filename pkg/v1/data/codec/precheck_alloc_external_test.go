//go:build !race

// Package codec_test — #910's allocation budget, in the race-off alloc lane
// (tools/alloc-lane-targets.txt already names //pkg/v1/data/codec:codec_test):
// an allocation count is what it asserts, and the race detector inflates it.

package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// promotedRecord is a small struct the four pre-checked Formats promote.
type promotedRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TestPromotedMarshalBuildsNoRefusal is #910's acceptance: Marshal of a struct
// to Form, NDJSON, CSV or PEM promotes it at once, without the codec first
// building a refusal to throw away. Measured on the base (ca04929e7): form 10,
// ndjson 8, csv 10, pem 8 allocations, of which the discarded refusal cost 2,
// 1, 1 and 2 (the codec's Marshal of the struct alone allocates 3, 2, 2, 3,
// one of them boxing the struct, which promotion pays as well). The budget
// is the base minus the refusal: every allocation the refusal made is gone.
func TestPromotedMarshalBuildsNoRefusal(t *testing.T) {
	v := promotedRecord{ID: 7, Name: "ada"}
	budgets := map[codec.Format]float64{codec.Form: 8, codec.NDJSON: 7, codec.CSV: 9, codec.PEM: 6}
	for f, budget := range budgets {
		if _, err := codec.Marshal(f, v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		allocs := testing.AllocsPerRun(200, func() { _, _ = codec.Marshal(f, v) })
		if allocs > budget {
			t.Errorf("%s: Marshal(struct) allocates %v, budget %v", f, allocs, budget)
		}
		t.Logf("%s: %v allocs/op", f, allocs)
	}
}
