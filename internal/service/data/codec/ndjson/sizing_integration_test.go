//go:build !race

package ndjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// tenStrings is a record of ten strings: 160 bytes a slot on 64-bit.
type tenStrings struct {
	A, B, C, D, E, F, G, H, I, J string
}

// bytesPerUnmarshal is what one Unmarshal of data into a fresh *[]T
// allocates in total, measured by the testing package's own counter.
func bytesPerUnmarshal[T any](t *testing.T, data []byte) int64 {
	t.Helper()
	c := ndjson.New()
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var out []T
			if err := c.Unmarshal(data, &out); err != nil {
				b.Fatal(err)
			}
			if len(out) != 0 && bytes.Count(data, []byte("{")) == 0 {
				b.Fatalf("decoded %d records from blank lines", len(out))
			}
		}
	})
	return res.AllocedBytesPerOp()
}

// TestUnmarshalSizesByRecordsNotNewlines is #1098: the output is sized by the
// records the input holds, not by its newline count. 1 MiB of '\n' decoded
// into ten-string records used to reserve a slot per newline — 160 MiB — and
// now allocates almost nothing; an empty slice comes back. Race off: the
// race runtime's allocations would blur the total.
func TestUnmarshalSizesByRecordsNotNewlines(t *testing.T) {
	blank := bytes.Repeat([]byte{'\n'}, 1<<20)
	spaced := []byte(strings.Repeat(" \t\r\n", 1<<18))
	for name, data := range map[string][]byte{"newlines": blank, "whitespace lines": spaced} {
		var out []tenStrings
		if err := ndjson.New().Unmarshal(data, &out); err != nil || len(out) != 0 {
			t.Fatalf("%s: Unmarshal = %d records, %v; want 0, nil", name, len(out), err)
		}
		got := bytesPerUnmarshal[tenStrings](t, data)
		t.Logf("%s: allocated %d bytes per Unmarshal", name, got)
		if got >= 2<<20 {
			t.Fatalf("%s: Unmarshal of 1 MiB of blank lines allocated %d bytes, want under 2 MiB", name, got)
		}
		if got := bytesPerUnmarshal[any](t, data); got >= 2<<20 {
			t.Fatalf("%s: Unmarshal into []any allocated %d bytes, want under 2 MiB", name, got)
		}
	}
}

// TestUnmarshalRefusalReservesNoSlotPerLine pins the cap on the initial
// reservation: an input whose every line would fail, 1 MiB of "1\n" into
// ten-string records, is refused at its first line without reserving a slot
// for each of its half million lines (80 MiB before).
func TestUnmarshalRefusalReservesNoSlotPerLine(t *testing.T) {
	data := bytes.Repeat([]byte("1\n"), 1<<19)
	c := ndjson.New()
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var out []tenStrings
			if err := c.Unmarshal(data, &out); err == nil {
				b.Fatal("a number decoded into a struct")
			}
		}
	})
	t.Logf("allocated %d bytes per Unmarshal", res.AllocedBytesPerOp())
	if got := res.AllocedBytesPerOp(); got >= 2<<20 {
		t.Fatalf("a refusal at the first line allocated %d bytes, want under 2 MiB", got)
	}
}
