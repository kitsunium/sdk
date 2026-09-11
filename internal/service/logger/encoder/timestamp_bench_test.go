package encoder

import (
	"testing"
	"time"
)

// benchTimestampSink defeats dead-code elimination without allocating.
var benchTimestampSink []byte

// benchTimestampUTC and benchTimestampZoned are the two instants §5.2 compares:
// UTC takes the "Z" branch of the offset verb, the zoned one takes the
// "+hh:mm" branch and is therefore the longer of the two renderings.
var (
	benchTimestampUTC   = time.Date(2026, 9, 10, 14, 30, 45, 123_000_000, time.UTC)
	benchTimestampZoned = time.Date(2026, 9, 10, 14, 30, 45, 123_000_000, time.FixedZone("+02:00", 2*60*60))
)

// BenchmarkAppendTimestamp measures the hand-rolled renderer that replaced
// time.Time.AppendFormat on the record-timestamp path.
//
// It exists because the ratio in timestamp.go's package comment and in
// BENCH.md §5.2 was originally produced by a throwaway harness written before
// the change landed. A ratio quoted in a comment that will outlive everyone
// who remembers measuring it needs to be reproducible from the tree, or the
// next reader has to take it on trust — and the next optimiser has nothing to
// re-run before deciding the fallback is not worth keeping. Pair with
// BenchmarkAppendTimestampStdlib; the quotient is the claim.
func BenchmarkAppendTimestamp(b *testing.B) {
	//: reused across iterations so the buffer growth is not what is measured.
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchTimestampSink = appendTimestamp(dst[:0], benchTimestampUTC)
	}
}

// BenchmarkAppendTimestampStdlib is the control: the same layout, the same
// instant, through the generic formatter appendTimestamp replaced.
func BenchmarkAppendTimestampStdlib(b *testing.B) {
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchTimestampSink = benchTimestampUTC.AppendFormat(dst[:0], timestampLayout)
	}
}

// BenchmarkAppendTimestampZoned renders an offset zone, where the verb emits
// "+02:00" instead of a single "Z" — the longer of the two branches.
func BenchmarkAppendTimestampZoned(b *testing.B) {
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchTimestampSink = appendTimestamp(dst[:0], benchTimestampZoned)
	}
}

// BenchmarkAppendTimestampZonedStdlib is the offset-zone control.
func BenchmarkAppendTimestampZonedStdlib(b *testing.B) {
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchTimestampSink = benchTimestampZoned.AppendFormat(dst[:0], timestampLayout)
	}
}

// BenchmarkAppendTimestampFallback measures the guard rather than the fast
// path: a year outside [0, 9999] cannot be spelled at four digits, so
// appendTimestamp hands the instant to AppendFormat. The number that matters
// is that this row is NOT faster than the stdlib control — if it ever were,
// the fallback would have stopped being taken and the equivalence test would
// be the only thing standing between a caller and a silently truncated date.
func BenchmarkAppendTimestampFallback(b *testing.B) {
	//: year 12026 needs five digits, so the fixed-width path must decline it.
	far := time.Date(12026, 9, 10, 14, 30, 45, 123_000_000, time.UTC)
	dst := make([]byte, 0, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchTimestampSink = appendTimestamp(dst[:0], far)
	}
}
