package stream_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// compactOK compacts partition and fails the test on a refusal.
func compactOK(t *testing.T, s corestream.Stream, partition int) corestream.CompactValue {
	t.Helper()
	value, err := sibling[corestream.Compactor](t, s).Compact(t.Context(), partition)
	if err != nil {
		t.Fatalf("Compact(%d) = %v, want nil", partition, err)
	}
	return value
}

// readAll reads partition from offset from to its end, in pages of max.
func readAll(t *testing.T, s corestream.Stream, partition int, from corestream.Offset, maxEntries int) []corestream.EntryValue {
	t.Helper()
	var all []corestream.EntryValue
	for {
		page := readOK(t, s, partition, from, maxEntries)
		if len(page) == 0 {
			return all
		}
		all = append(all, page...)
		from = page[len(page)-1].Offset + 1
	}
}

// offsetsAndPayloads renders entries as "offset:payload" for a comparison.
func offsetsAndPayloads(entries []corestream.EntryValue) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = string(rune('0'+entry.Offset)) + ":" + string(entry.Record.Payload)
	}
	return out
}

// sameStrings reports whether two slices hold the same strings in order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCompactKeepsTheLastRecordOfEachKeyAndEveryYoungRecord is the first
// criterion of #824: after a compaction a read from offset 0 yields the
// last record of each key among those at least CompactAfter old, every
// unkeyed one, and every younger record, in offset order at their offsets;
// Bounds does not move, and a read inside a hole reads on.
func TestCompactKeepsTheLastRecordOfEachKeyAndEveryYoungRecord(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{CompactAfter: time.Hour}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, keyed("a", "a1"))
		appendOK(t, s, keyed("b", "b1"))
		appendOK(t, s, record("u1"))
		appendOK(t, s, keyed("a", "a2"), keyed("b", "b2"))
		clk.Advance(time.Hour)
		appendOK(t, s, keyed("a", "a3"), keyed("c", "c1"))
		got := compactOK(t, s, 0)
		if got != (corestream.CompactValue{Scanned: 5, Kept: 3, Dropped: 2}) {
			t.Fatalf("Compact = %+v, want {5 3 2}", got)
		}
		want := []string{"2:u1", "3:a2", "4:b2", "5:a3", "6:c1"}
		if read := offsetsAndPayloads(readAll(t, s, 0, 0, 2)); !sameStrings(read, want) {
			t.Fatalf("Read from 0 = %v, want %v", read, want)
		}
		if bounds := boundsOK(t, s, 0); bounds != (corestream.BoundsValue{First: 0, Next: 7}) {
			t.Fatalf("Bounds after Compact = %+v, want {0 7}: compaction never moves them", bounds)
		}
		if inHole := readOK(t, s, 0, 1, 1); len(inHole) != 1 || inHole[0].Offset != 2 {
			t.Fatalf("Read(1, max 1) inside a hole = %+v, want offset 2 alone", inHole)
		}
		if again := compactOK(t, s, 0); again != (corestream.CompactValue{Scanned: 3, Kept: 3}) {
			t.Fatalf("a second Compact = %+v, want {3 3 0}", again)
		}
		if next := appendOK(t, s, keyed("a", "a4")); next.First != 7 {
			t.Fatalf("Append after Compact took offset %d, want 7", next.First)
		}
	})
}

// TestATombstoneOutlivesOneWindowThenGoesWithItsKey is the second
// criterion: a tombstone and the records before it under its key are gone
// after two compactions CompactAfter apart, the first keeping the tombstone;
// a reader inside the compacted range is never refused, one below the first
// retained offset always is.
func TestATombstoneOutlivesOneWindowThenGoesWithItsKey(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{CompactAfter: time.Hour}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, keyed("k", "v1"))
		appendOK(t, s, keyed("k", "v2"))
		appendOK(t, s, corestream.RecordValue{Key: []byte("k")})
		appendOK(t, s, keyed("x", "x1"))
		clk.Advance(time.Hour)
		if got := compactOK(t, s, 0); got != (corestream.CompactValue{Scanned: 4, Kept: 2, Dropped: 2}) {
			t.Fatalf("first Compact = %+v, want {4 2 2}", got)
		}
		want := []string{"2:", "3:x1"}
		if read := offsetsAndPayloads(readAll(t, s, 0, 0, 10)); !sameStrings(read, want) {
			t.Fatalf("Read after the first Compact = %v, want %v: the tombstone stays a window", read, want)
		}
		clk.Advance(time.Hour)
		if got := compactOK(t, s, 0); got != (corestream.CompactValue{Scanned: 2, Kept: 1, Dropped: 1}) {
			t.Fatalf("second Compact = %+v, want {2 1 1}", got)
		}
		for from := corestream.Offset(0); from <= 3; from++ {
			read := offsetsAndPayloads(readAll(t, s, 0, from, 10))
			if !sameStrings(read, []string{"3:x1"}) {
				t.Fatalf("Read(%d) after the second Compact = %v, want [3:x1]", from, read)
			}
		}
		if bounds := boundsOK(t, s, 0); bounds != (corestream.BoundsValue{First: 0, Next: 4}) {
			t.Fatalf("Bounds = %+v, want {0 4}", bounds)
		}
		if err := sibling[corestream.Trimmer](t, s).Trim(t.Context(), 0, 2); err != nil {
			t.Fatalf("Trim(2) = %v", err)
		}
		if _, err := s.Read(t.Context(), 0, 1, 10); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
			t.Fatalf("Read(1) below the first retained = %v, want OFFSET_TRIMMED", err)
		}
		if read := offsetsAndPayloads(readAll(t, s, 0, 2, 10)); !sameStrings(read, []string{"3:x1"}) {
			t.Fatalf("Read(2) at the first retained, a hole = %v, want [3:x1]", read)
		}
	})
}

// TestARecordYoungerThanCompactAfterIsNeverTouched: a reader within the
// window behind the head sees every update.
func TestARecordYoungerThanCompactAfterIsNeverTouched(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{CompactAfter: time.Hour}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, keyed("k", "v1"))
		clk.Advance(time.Hour - time.Nanosecond)
		appendOK(t, s, keyed("k", "v2"), corestream.RecordValue{Key: []byte("k")})
		if got := compactOK(t, s, 0); got != (corestream.CompactValue{}) {
			t.Fatalf("Compact one nanosecond early = %+v, want nothing looked at", got)
		}
		if read := readAll(t, s, 0, 0, 10); len(read) != 3 {
			t.Fatalf("Read = %d entries, want all 3", len(read))
		}
	})
}

// TestAZeroCompactAfterNeverCompacts: the zero has one reading.
func TestAZeroCompactAfterNeverCompacts(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, keyed("k", "v1"), keyed("k", "v2"))
		clk.Advance(1000 * time.Hour)
		if got := compactOK(t, s, 0); got != (corestream.CompactValue{}) {
			t.Fatalf("Compact under a zero CompactAfter = %+v, want zero", got)
		}
		if read := readAll(t, s, 0, 0, 10); len(read) != 2 {
			t.Fatalf("Read = %d entries, want both", len(read))
		}
	})
}
