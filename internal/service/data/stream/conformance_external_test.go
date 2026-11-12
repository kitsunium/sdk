package stream_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// TestOffsetsAreContiguousPerPartitionFromZero is the offset contract: every
// partition counts from zero on its own, one offset per record, a batch's
// records holding every offset between First and Last.
func TestOffsetsAreContiguousPerPartitionFromZero(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 3}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		want := make([]corestream.Offset, 3)
		for round := range 4 {
			for partition := range 3 {
				key := keyOn(t, partition, 3)
				got := appendOK(t, s, keyed(key, "a"), keyed(key, "b"))
				if got.Partition != partition || got.First != want[partition] || got.Last != want[partition]+1 {
					t.Fatalf("round %d: Append = %+v, want partition %d offsets %d..%d",
						round, got, partition, want[partition], want[partition]+1)
				}
				want[partition] += 2
			}
		}
		for partition := range 3 {
			entries := readOK(t, s, partition, 0, 100)
			for i, entry := range entries {
				if entry.Offset != corestream.Offset(i) || entry.Partition != partition {
					t.Fatalf("partition %d entry %d = offset %d partition %d", partition, i, entry.Offset, entry.Partition)
				}
			}
			if bounds := boundsOK(t, s, partition); bounds != (corestream.BoundsValue{First: 0, Next: 8}) {
				t.Fatalf("Bounds(%d) = %+v, want {0 8}", partition, bounds)
			}
		}
	})
}

// TestAReadAtTheNextOffsetIsEmptyAndNotAnError is what lets a reader poll: the
// end of a partition is an empty answer, never a refusal.
func TestAReadAtTheNextOffsetIsEmptyAndNotAnError(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		if entries := readOK(t, s, 0, 0, 10); len(entries) != 0 {
			t.Fatalf("Read of an empty partition = %d entries, want 0", len(entries))
		}
		appendOK(t, s, record("one"), record("two"))
		if entries := readOK(t, s, 0, 2, 10); len(entries) != 0 {
			t.Fatalf("Read(next) = %d entries, want 0", len(entries))
		}
		entries := readOK(t, s, 0, 1, 10)
		if len(entries) != 1 || string(entries[0].Record.Payload) != "two" {
			t.Fatalf("Read(1) = %+v, want the second record alone", entries)
		}
		if limited := readOK(t, s, 0, 0, 1); len(limited) != 1 {
			t.Fatalf("Read(max 1) = %d entries, want 1", len(limited))
		}
	})
}

// TestAReadOutsideTheRetainedRangeIsRefused pins the two refusals of an
// offset: below the first retained is OFFSET_TRIMMED, past the next is
// OFFSET_AHEAD — never a silent skip either way.
func TestAReadOutsideTheRetainedRangeIsRefused(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		trimmer := sibling[corestream.Trimmer](t, s)
		appendOK(t, s, record("a"), record("b"), record("c"))
		if err := trimmer.Trim(t.Context(), 0, 2); err != nil {
			t.Fatalf("Trim(2) = %v, want nil", err)
		}
		if _, err := s.Read(t.Context(), 0, 1, 10); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
			t.Fatalf("Read(1) after Trim(2) = %v, want OFFSET_TRIMMED", err)
		}
		if _, err := s.Read(t.Context(), 0, 4, 10); !errs.HasCode(err, corestream.CodeOffsetAhead) {
			t.Fatalf("Read(4) with next 3 = %v, want OFFSET_AHEAD", err)
		}
		if entries := readOK(t, s, 0, 2, 10); len(entries) != 1 || entries[0].Offset != 2 {
			t.Fatalf("Read(2) = %+v, want offset 2 alone", entries)
		}
	})
}

// TestRecordsOfOneKeyLandOnOnePartitionInAppendOrder is per-key ordering: the
// one ordering a stream promises.
func TestRecordsOfOneKeyLandOnOnePartitionInAppendOrder(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 4}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		order := []string{"created", "paid", "shipped", "delivered"}
		want := corestream.PartitionOf([]byte("order-42"), 4)
		for _, step := range order {
			if got := appendOK(t, s, keyed("order-42", step)); got.Partition != want {
				t.Fatalf("Append(%s) partition = %d, want PartitionOf's %d", step, got.Partition, want)
			}
			appendOK(t, s, record("noise"))
		}
		var seen []string
		for _, entry := range readOK(t, s, want, 0, 100) {
			if string(entry.Record.Key) == "order-42" {
				seen = append(seen, string(entry.Record.Payload))
			}
		}
		if len(seen) != len(order) {
			t.Fatalf("read %v, want %v", seen, order)
		}
		for i := range order {
			if seen[i] != order[i] {
				t.Fatalf("read %v, want %v in append order", seen, order)
			}
		}
	})
}

// TestUnkeyedBatchesGoToThePartitionsInTurn: a batch with no key has no
// partition of its own, and the engine spreads them.
func TestUnkeyedBatchesGoToThePartitionsInTurn(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 3}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		counts := make([]int, 3)
		for range 9 {
			counts[appendOK(t, s, record("x")).Partition]++
		}
		for partition, count := range counts {
			if count != 3 {
				t.Fatalf("partition %d took %d of 9 unkeyed batches, want 3 (%v)", partition, count, counts)
			}
		}
	})
}

// TestABatchWhoseKeysNameTwoPartitionsIsRefusedWhole: splitting it would be
// two syncs, and a crash between them keeps half.
func TestABatchWhoseKeysNameTwoPartitionsIsRefusedWhole(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		left, right := keyOn(t, 0, 2), keyOn(t, 1, 2)
		_, err := s.Append(t.Context(), []corestream.RecordValue{keyed(left, "a"), record("b"), keyed(right, "c")})
		if !errs.HasCode(err, corestream.CodePartitionsMixed) {
			t.Fatalf("Append(mixed) = %v, want PARTITIONS_MIXED", err)
		}
		for partition := range 2 {
			if bounds := boundsOK(t, s, partition); bounds.Next != 0 {
				t.Fatalf("partition %d next = %d after a refused batch, want 0", partition, bounds.Next)
			}
		}
		if got := appendOK(t, s, keyed(right, "a"), record("b")); got.Partition != 1 || got.Last != 1 {
			t.Fatalf("Append(keyed + unkeyed) = %+v, want partition 1 offsets 0..1", got)
		}
	})
}

// TestARecordTooLargeIsRefusedBeforeAnythingIsWritten: the whole batch, at
// the producer that can still make the record smaller.
func TestARecordTooLargeIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{MaxRecordBytes: 8}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		fits := corestream.RecordValue{Key: []byte("k"), Payload: []byte("1234567")}
		over := corestream.RecordValue{Key: []byte("k"), Payload: []byte("12345678")}
		_, err := s.Append(t.Context(), []corestream.RecordValue{fits, over})
		if !errs.HasCode(err, corestream.CodeRecordTooLarge) {
			t.Fatalf("Append(9 bytes over 8) = %v, want RECORD_TOO_LARGE", err)
		}
		if bounds := boundsOK(t, s, 0); bounds.Next != 0 {
			t.Fatalf("next = %d after a refused batch, want 0: part of it was written", bounds.Next)
		}
		appendOK(t, s, fits)
	})
}

// TestAnEmptyBatchAndAnEmptyReadAreRefused: a loop that processes nothing
// would do so forever.
func TestAnEmptyBatchAndAnEmptyReadAreRefused(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		if _, err := s.Append(t.Context(), nil); !errs.HasCode(err, corestream.CodeInvalidBatchSize) {
			t.Fatalf("Append(nil) = %v, want INVALID_BATCH_SIZE", err)
		}
		for _, maxEntries := range []int{0, -1} {
			if _, err := s.Read(t.Context(), 0, 0, maxEntries); !errs.HasCode(err, corestream.CodeInvalidBatchSize) {
				t.Fatalf("Read(max %d) = %v, want INVALID_BATCH_SIZE", maxEntries, err)
			}
		}
	})
}

// TestAPartitionOutsideTheStreamIsRefusedByEveryCall holds every call that
// takes a partition to the one range.
func TestAPartitionOutsideTheStreamIsRefusedByEveryCall(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		if s.Partitions() != 2 {
			t.Fatalf("Partitions() = %d, want 2", s.Partitions())
		}
		calls := map[string]func(partition int) error{
			"Read":   func(p int) error { _, err := s.Read(t.Context(), p, 0, 1); return err },
			"Bounds": func(p int) error { _, err := s.Bounds(t.Context(), p); return err },
		}
		if seeker, ok := s.(corestream.Seeker); ok {
			calls["OffsetAt"] = func(p int) error { _, err := seeker.OffsetAt(t.Context(), p, epoch); return err }
		}
		if trimmer, ok := s.(corestream.Trimmer); ok {
			calls["Trim"] = func(p int) error { return trimmer.Trim(t.Context(), p, 0) }
		}
		if compactor, ok := s.(corestream.Compactor); ok {
			calls["Compact"] = func(p int) error { _, err := compactor.Compact(t.Context(), p); return err }
		}
		if committer, ok := s.(corestream.Committer); ok {
			calls["Commit"] = func(p int) error { return committer.Commit(t.Context(), "g", p, 0) }
			calls["Committed"] = func(p int) error { _, err := committer.Committed(t.Context(), "g", p); return err }
		}
		for name, call := range calls {
			for _, partition := range []int{-1, 2} {
				if err := call(partition); !errs.HasCode(err, corestream.CodeUnknownPartition) {
					t.Fatalf("%s(%d) = %v, want UNKNOWN_PARTITION", name, partition, err)
				}
			}
		}
	})
}

// TestTrimRaisesTheFirstOffsetAndNeverMovesTheNext: offsets are never reused.
func TestTrimRaisesTheFirstOffsetAndNeverMovesTheNext(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		trimmer := sibling[corestream.Trimmer](t, s)
		appendOK(t, s, record("a"), record("b"), record("c"), record("d"))
		steps := []struct {
			before corestream.Offset
			want   corestream.BoundsValue
		}{
			{before: 2, want: corestream.BoundsValue{First: 2, Next: 4}},
			{before: 1, want: corestream.BoundsValue{First: 2, Next: 4}},
			{before: 4, want: corestream.BoundsValue{First: 4, Next: 4}},
		}
		for _, step := range steps {
			if err := trimmer.Trim(t.Context(), 0, step.before); err != nil {
				t.Fatalf("Trim(%d) = %v, want nil", step.before, err)
			}
			if got := boundsOK(t, s, 0); got != step.want {
				t.Fatalf("Bounds after Trim(%d) = %+v, want %+v", step.before, got, step.want)
			}
		}
		if err := trimmer.Trim(t.Context(), 0, 5); !errs.HasCode(err, corestream.CodeOffsetAhead) {
			t.Fatalf("Trim(5) with next 4 = %v, want OFFSET_AHEAD", err)
		}
		if got := appendOK(t, s, record("e")); got.First != 4 {
			t.Fatalf("Append after trimming everything took offset %d, want 4", got.First)
		}
	})
}

// TestACommittedOffsetRoundTripsAndANewGroupIsUnknown: a group's cursor
// survives its reader, and a group that never committed is told so rather
// than given a zero.
func TestACommittedOffsetRoundTripsAndANewGroupIsUnknown(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		committer := sibling[corestream.Committer](t, s)
		if _, err := committer.Committed(t.Context(), "billing", 0); !errs.HasCode(err, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed(new group) = %v, want UNKNOWN_GROUP", err)
		}
		key := keyOn(t, 0, 2)
		appendOK(t, s, keyed(key, "a"), keyed(key, "b"), keyed(key, "c"))
		for _, next := range []corestream.Offset{3, 1} {
			if err := committer.Commit(t.Context(), "billing", 0, next); err != nil {
				t.Fatalf("Commit(%d) = %v, want nil", next, err)
			}
			got, err := committer.Committed(t.Context(), "billing", 0)
			if err != nil || got != next {
				t.Fatalf("Committed() = %d, %v, want %d, nil", got, err, next)
			}
		}
		if _, err := committer.Committed(t.Context(), "billing", 1); !errs.HasCode(err, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed(other partition) = %v, want UNKNOWN_GROUP", err)
		}
		if _, err := committer.Committed(t.Context(), "audit", 0); !errs.HasCode(err, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed(other group) = %v, want UNKNOWN_GROUP", err)
		}
		if err := committer.Commit(t.Context(), "billing", 0, 4); !errs.HasCode(err, corestream.CodeOffsetAhead) {
			t.Fatalf("Commit(4) with next 3 = %v, want OFFSET_AHEAD", err)
		}
		if err := committer.Commit(t.Context(), "", 0, 0); !errs.HasCode(err, corestream.CodeStreamMisconfigured) {
			t.Fatalf("Commit(empty group) = %v, want STREAM_MISCONFIGURED", err)
		}
	})
}

// TestAWakeSignalIsClosedByTheNextAppend is the Waker contract: a channel
// taken before an empty Read is closed by the append that follows it.
func TestAWakeSignalIsClosedByTheNextAppend(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		waker := sibling[corestream.Waker](t, s)
		signal := waker.Wake()
		select {
		case <-signal:
			t.Fatal("the wake signal was closed before any Append")
		default:
		}
		readOK(t, s, 0, 0, 1)
		appendOK(t, s, keyed(keyOn(t, 1, 2), "x"))
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatal("the wake signal was not closed by an Append")
		}
		if next := waker.Wake(); next == signal {
			t.Fatal("Wake() after a fire returned the closed channel")
		}
	})
}

// TestOffsetAtFindsTheFirstRecordAppendedAtOrAfterAnInstant is the Seeker's
// "everything since nine o'clock".
func TestOffsetAtFindsTheFirstRecordAppendedAtOrAfterAnInstant(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		seeker := sibling[corestream.Seeker](t, s)
		appendOK(t, s, record("a"), record("b"))
		clk.Advance(time.Minute)
		appendOK(t, s, record("c"))
		clk.Advance(time.Minute)
		cases := []struct {
			at   time.Time
			want corestream.Offset
		}{
			{at: epoch.Add(-time.Hour), want: 0},
			{at: epoch, want: 0},
			{at: epoch.Add(time.Second), want: 2},
			{at: epoch.Add(time.Minute), want: 2},
			{at: epoch.Add(time.Hour), want: 3},
		}
		for _, c := range cases {
			got, err := seeker.OffsetAt(t.Context(), 0, c.at)
			if err != nil || got != c.want {
				t.Fatalf("OffsetAt(%s) = %d, %v, want %d", c.at, got, err, c.want)
			}
		}
		if entries := readOK(t, s, 0, 2, 1); !entries[0].AppendedAt.Equal(epoch.Add(time.Minute)) {
			t.Fatalf("AppendedAt = %s, want the clock's %s", entries[0].AppendedAt, epoch.Add(time.Minute))
		}
	})
}

// TestARecordIsKeptForRetainForAndNoLonger: retention by age, on the
// engine's clock — a record expires once its age reaches the bound.
func TestARecordIsKeptForRetainForAndNoLonger(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{RetainFor: time.Hour}, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, record("old"))
		clk.Advance(30 * time.Minute)
		appendOK(t, s, record("young"))
		clk.Advance(30*time.Minute - time.Nanosecond)
		if bounds := boundsOK(t, s, 0); bounds.First != 0 {
			t.Fatalf("First = %d one nanosecond before the hour, want 0", bounds.First)
		}
		clk.Advance(time.Nanosecond)
		if bounds := boundsOK(t, s, 0); bounds != (corestream.BoundsValue{First: 1, Next: 2}) {
			t.Fatalf("Bounds at the hour = %+v, want {1 2}", bounds)
		}
		if _, err := s.Read(t.Context(), 0, 0, 10); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
			t.Fatalf("Read(0) past its retention = %v, want OFFSET_TRIMMED", err)
		}
	})
}

// TestAPartitionKeepsRetainBytesAndTrimsTheOldestAboveIt: retention by size,
// which never trims the batch just appended.
func TestAPartitionKeepsRetainBytesAndTrimsTheOldestAboveIt(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{RetainBytes: 10}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, record("aaaa"), record("bbbb"))
		appendOK(t, s, record("cccc"))
		if bounds := boundsOK(t, s, 0); bounds != (corestream.BoundsValue{First: 1, Next: 3}) {
			t.Fatalf("Bounds = %+v, want {1 3}: 12 bytes above 10 drop the oldest", bounds)
		}
		appendOK(t, s, record("dddddddddddd"))
		if bounds := boundsOK(t, s, 0); bounds != (corestream.BoundsValue{First: 3, Next: 4}) {
			t.Fatalf("Bounds = %+v, want {3 4}: the newest batch alone is kept", bounds)
		}
	})
}

// TestAFullStreamRefusesOrWaitsForRoom is the producers' backpressure: at
// once with no BlockTimeout, after it otherwise, and as soon as a Trim
// makes room.
func TestAFullStreamRefusesOrWaitsForRoom(t *testing.T) {
	t.Parallel()
	refusing := corestream.PolicyValue{MaxBytes: 8, MaxRecordBytes: 8}
	forEachStream(t, refusing, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, record("12345678"))
		if _, err := s.Append(t.Context(), []corestream.RecordValue{record("x")}); !errs.HasCode(err, corestream.CodeStreamFull) {
			t.Fatalf("Append on a full stream = %v, want STREAM_FULL", err)
		}
	})
	waiting := corestream.PolicyValue{MaxBytes: 8, MaxRecordBytes: 8, BlockTimeout: time.Minute}
	forEachStream(t, waiting, func(t *testing.T, clk *clock.ManualClock, s corestream.Stream) {
		trimmer := sibling[corestream.Trimmer](t, s)
		appendOK(t, s, record("12345678"))
		timedOut := make(chan error, 1)
		go func() { _, err := s.Append(t.Context(), []corestream.RecordValue{record("x")}); timedOut <- err }()
		clk.BlockUntil(1)
		clk.Advance(time.Minute)
		if err := <-timedOut; !errs.HasCode(err, corestream.CodeStreamFull) {
			t.Fatalf("Append after BlockTimeout = %v, want STREAM_FULL", err)
		}
		admitted := make(chan error, 1)
		go func() { _, err := s.Append(t.Context(), []corestream.RecordValue{record("y")}); admitted <- err }()
		clk.BlockUntil(1)
		if err := trimmer.Trim(t.Context(), 0, 1); err != nil {
			t.Fatalf("Trim(1) = %v, want nil", err)
		}
		if err := <-admitted; err != nil {
			t.Fatalf("Append after a Trim made room = %v, want nil", err)
		}
	})
}

// TestTheEngineKeepsItsOwnCopyOfEveryByte: a producer may reuse its buffer at
// once, and a reader may scribble on what it was given.
func TestTheEngineKeepsItsOwnCopyOfEveryByte(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		buffer := []byte("original")
		appendOK(t, s, corestream.RecordValue{Key: []byte("k"), Payload: buffer})
		copy(buffer, "mutated!")
		first := readOK(t, s, 0, 0, 1)
		first[0].Record.Payload[0] = 'X'
		if got := string(readOK(t, s, 0, 0, 1)[0].Record.Payload); got != "original" {
			t.Fatalf("Payload = %q, want %q", got, "original")
		}
	})
}

// TestConcurrentAppendsTakeDistinctContiguousOffsets is the concurrency
// contract under the race detector.
func TestConcurrentAppendsTakeDistinctContiguousOffsets(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		const writers, each = 8, 50
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				for range each {
					if _, err := s.Append(t.Context(), []corestream.RecordValue{record("r")}); err != nil {
						t.Errorf("Append() = %v", err)
					}
				}
			})
		}
		wg.Wait()
		entries := readOK(t, s, 0, 0, writers*each+1)
		if len(entries) != writers*each {
			t.Fatalf("read %d entries, want %d", len(entries), writers*each)
		}
		for i, entry := range entries {
			if entry.Offset != corestream.Offset(i) {
				t.Fatalf("entry %d has offset %d", i, entry.Offset)
			}
		}
	})
}
