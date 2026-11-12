package stream_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// epoch anchors every ManualClock in this suite, so an instant printed in a
// failure is readable.
var epoch = time.Date(2026, time.November, 12, 9, 0, 0, 0, time.UTC)

// streamFactory builds one engine over an injected clock and policy.
//
// The suite is written against this rather than against a constructor so the
// SAME cases run over every engine: a double nobody checks against the real
// thing has already drifted. Adding an engine is one row of everyStream.
type streamFactory struct {
	name string
	make func(t *testing.T, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream
}

// everyStream is the table every conformance case ranges over: the memory
// engine and the file engine (#821); the SQL engine (#822) joins it.
func everyStream() []streamFactory {
	return []streamFactory{
		{name: "memory", make: makeMemory},
		{name: "file", make: makeFile},
	}
}

// makeMemory builds the memory engine.
func makeMemory(t *testing.T, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream {
	t.Helper()
	s, err := svcstream.NewMemory(svcstream.MemoryConfig{Clock: clk, Policy: policy})
	if err != nil {
		t.Fatalf("NewMemory() = %v, want nil", err)
	}
	return s
}

// makeFile builds the file engine over a directory of the test's own.
func makeFile(t *testing.T, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream {
	t.Helper()
	return openFile(t, t.TempDir(), clk, policy)
}

// forEachStream runs body as a parallel subtest over every engine, each with
// a ManualClock of its own at epoch.
func forEachStream(
	t *testing.T, policy corestream.PolicyValue,
	body func(t *testing.T, clk *clock.ManualClock, s corestream.Stream),
) {
	t.Helper()
	for _, factory := range everyStream() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			body(t, clk, factory.make(t, clk, policy))
		})
	}
}

// record builds an unkeyed record of payload.
func record(payload string) corestream.RecordValue {
	return corestream.RecordValue{Payload: []byte(payload)}
}

// keyed builds a record of key and payload.
func keyed(key, payload string) corestream.RecordValue {
	return corestream.RecordValue{Key: []byte(key), Payload: []byte(payload)}
}

// appendOK appends records and fails the test on a refusal.
func appendOK(t *testing.T, s corestream.Stream, records ...corestream.RecordValue) corestream.AppendValue {
	t.Helper()
	value, err := s.Append(t.Context(), records)
	if err != nil {
		t.Fatalf("Append() = %v, want nil", err)
	}
	return value
}

// readOK reads partition from offset from and fails the test on a refusal.
func readOK(t *testing.T, s corestream.Stream, partition int, from corestream.Offset, maxEntries int) []corestream.EntryValue {
	t.Helper()
	entries, err := s.Read(t.Context(), partition, from, maxEntries)
	if err != nil {
		t.Fatalf("Read(%d, %d, %d) = %v, want nil", partition, from, maxEntries, err)
	}
	return entries
}

// boundsOK reads partition's bounds and fails the test on a refusal.
func boundsOK(t *testing.T, s corestream.Stream, partition int) corestream.BoundsValue {
	t.Helper()
	bounds, err := s.Bounds(t.Context(), partition)
	if err != nil {
		t.Fatalf("Bounds(%d) = %v, want nil", partition, err)
	}
	return bounds
}

// keyOn returns a key PartitionOf places on partition among partitions.
func keyOn(t *testing.T, partition, partitions int) string {
	t.Helper()
	for i := range 10_000 {
		key := "key-" + strconv.Itoa(i)
		if corestream.PartitionOf([]byte(key), partitions) == partition {
			return key
		}
	}
	t.Fatalf("no key among 10 000 lands on partition %d of %d", partition, partitions)
	return ""
}

// sibling returns s as the sibling T, or skips the test for an engine that
// does not offer it — a sibling is optional by ADR 0039, and a missing one
// costs the caller a capability, never a record.
func sibling[T any](t *testing.T, s corestream.Stream) T {
	t.Helper()
	found, ok := s.(T)
	if !ok {
		var zero T
		t.Skipf("this engine does not implement %T", &zero)
	}
	return found
}
