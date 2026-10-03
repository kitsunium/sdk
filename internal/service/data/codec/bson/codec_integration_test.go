//go:build !race

package bson_test

import (
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/bson"
)

// Typed sinks defeat dead-code elimination in the AllocsPerRun probes without
// boxing: assigning a slice or a struct to an interface would allocate, and the
// budget would measure the test instead of the codec.
var (
	// allocSinkBytes observes an encoding.
	allocSinkBytes []byte
	// allocSinkRecord observes a decoded struct.
	allocSinkRecord allocRecord
	// allocSinkMap observes a decoded map.
	allocSinkMap map[string]int
)

// allocRecord is a struct payload: the shape a typed caller encodes.
type allocRecord struct {
	// ID is a wide integer.
	ID int64 `bson:"_id"`
	// Name is a short string.
	Name string `bson:"name"`
	// Active is a boolean.
	Active bool `bson:"active"`
	// Score is a double.
	Score float64 `bson:"score"`
	// Created is a datetime.
	Created time.Time `bson:"created"`
	// Tags is an array of strings.
	Tags []string `bson:"tags"`
}

// TestAllocBudget pins BSON's per-call allocation ceilings over a small map
// and a struct. Carries //go:build !race (testing.AllocsPerRun is +1 under
// -race) and no t.Parallel (AllocsPerRun reads a process-global counter).
// Budgets are ceilings — re-pin with intent on a Go toolchain bump.
//
// What they hold: encoding a struct allocates the one slice Marshal hands the
// caller and nothing else, the pooled scratch buffer and the cached plan
// carrying the rest; Append into a destination with room allocates nothing,
// a map included, since the reflect slots its entries are walked through are
// pooled per map type. Decoding the struct allocates the target, its one
// non-trivial string and its slice — single-byte strings come from the
// runtime's static table — and the map, the map and its growth. Mutation-checked: encoding into a fresh slice instead of the pooled
// scratch buffer puts both Marshal rows at 6.
func TestAllocBudget(t *testing.T) {
	c := bson.New()
	appender, ok := c.(corecodec.Appender)
	if !ok {
		t.Fatal("the BSON codec does not implement corecodec.Appender")
	}
	smallMap := map[string]int{"a": 1, "b": 2, "c": 3}
	//: boxed once, as a caller of the any-taking API holds it; boxing per call
	//: would measure the test, not the codec.
	var record any = allocRecord{ID: 1 << 40, Name: "kitsunium", Active: true, Score: 1.5, Created: time.Unix(5, 0).UTC(), Tags: []string{"x", "y"}}
	mapSeed, err := c.Marshal(smallMap)
	if err != nil {
		t.Fatalf("seed Marshal(map): %v", err)
	}
	recordSeed, err := c.Marshal(record)
	if err != nil {
		t.Fatalf("seed Marshal(record): %v", err)
	}
	dst := make([]byte, 0, 1024)
	type tc struct {
		name string
		ceil float64
		fn   func()
	}
	//: ceilings measured on the race-off lane at the native-codec commit.
	tests := []tc{
		{"marshal a struct", 1, func() {
			out, merr := c.Marshal(record)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSinkBytes = out
		}},
		{"append a struct", 0, func() {
			out, aerr := appender.Append(dst[:0], record)
			if aerr != nil {
				t.Fatalf("Append: %v", aerr)
			}
			allocSinkBytes = out
		}},
		{"unmarshal a struct", 4, func() {
			var got allocRecord
			if uerr := c.Unmarshal(recordSeed, &got); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSinkRecord = got
		}},
		{"marshal a map", 1, func() {
			out, merr := c.Marshal(smallMap)
			if merr != nil {
				t.Fatalf("Marshal: %v", merr)
			}
			allocSinkBytes = out
		}},
		{"append a map", 0, func() {
			out, aerr := appender.Append(dst[:0], smallMap)
			if aerr != nil {
				t.Fatalf("Append: %v", aerr)
			}
			allocSinkBytes = out
		}},
		{"unmarshal a map", 3, func() {
			var got map[string]int
			if uerr := c.Unmarshal(mapSeed, &got); uerr != nil {
				t.Fatalf("Unmarshal: %v", uerr)
			}
			allocSinkMap = got
		}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := testing.AllocsPerRun(100, tc.fn)
		t.Logf("%s: allocs/op=%.0f (ceil %.0f)", tc.name, got, tc.ceil)
		if got > tc.ceil {
			t.Errorf("%s: allocs/op=%.0f > ceil %.0f", tc.name, got, tc.ceil)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}
