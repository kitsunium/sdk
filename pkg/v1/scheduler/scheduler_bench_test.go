package scheduler_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/scheduler"
)

// sinks so no parse or computation can be proven unused and elided.
var (
	scheduleSink scheduler.Schedule
	timeSink     time.Time
	okSink       bool
	errSink      error
)

// benchBase is the instant every Next benchmark starts from. It is a Monday in
// March so the spring-forward transition is one week away — close enough that
// a weekly or monthly expression has to reason about it, far enough that the
// common case is still the common case.
var benchBase = time.Date(2026, 3, 22, 10, 30, 0, 0, time.UTC)

// benchSchedule parses once, outside the timed loop.
func benchSchedule(b *testing.B, expr string) scheduler.Schedule {
	b.Helper()
	s, err := scheduler.Parse(expr)
	if err != nil {
		b.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

// BenchmarkParse_* price the startup path. An expression is parsed once per
// process, so these numbers matter only in aggregate — a service registering
// two hundred jobs pays them all at boot, before it serves anything.
func BenchmarkParse_EveryMinute(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		scheduleSink, errSink = scheduler.Parse("* * * * *")
	}
}

func BenchmarkParse_Dense(b *testing.B) {
	//: every field populated with a list, a range and a step — the widest
	//: expression the supported subset can express.
	const expr = "0,15,30,45 1-6/2 1,15 1-6 MON-FRI"
	b.ReportAllocs()
	for b.Loop() {
		scheduleSink, errSink = scheduler.Parse(expr)
	}
}

// BenchmarkParse_Refused is the failure path, and it is here for the same
// reason the refusals are benchmarked in core/trace and token: an expression
// can come from configuration a stranger wrote, and a refusal that costs far
// more than an acceptance is an amplification.
func BenchmarkParse_Refused(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		scheduleSink, errSink = scheduler.Parse("@reboot")
	}
}

func BenchmarkEvery(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		scheduleSink, errSink = scheduler.Every(30 * time.Second)
	}
}

// BenchmarkNext_EveryMinute is the hot path: the engine calls Next once per
// fire, per job, to find the following one. A schedule that fires every minute
// finds its answer in the next minute, so this is the cheap case.
func BenchmarkNext_EveryMinute(b *testing.B) {
	s := benchSchedule(b, "* * * * *")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(benchBase)
	}
}

// BenchmarkNext_Daily has to walk forward through the rest of the day, so the
// delta against EveryMinute is what searching costs per unit skipped.
func BenchmarkNext_Daily(b *testing.B) {
	s := benchSchedule(b, "30 3 * * *")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(benchBase)
	}
}

// BenchmarkNext_Sparse is the worst realistic case: 03:30 on the 29th of
// February, which exists once every four years. If Next searches minute by
// minute this is where it shows; if it searches by field it should not be far
// from Daily.
func BenchmarkNext_Sparse(b *testing.B) {
	s := benchSchedule(b, "30 3 29 2 *")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(benchBase)
	}
}

// BenchmarkNext_DayFieldOr exercises the POSIX rule that makes cron subtle:
// when both day-of-month and day-of-week are restricted, a date matching
// EITHER fires. That is two searches, not one.
func BenchmarkNext_DayFieldOr(b *testing.B) {
	s := benchSchedule(b, "0 9 1 * MON")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(benchBase)
	}
}

// BenchmarkNext_AcrossDST computes across the spring-forward transition in a
// zone that has one. ADR 0041 decided that a non-existent wall-clock time does
// not fire and a repeated one fires once; this prices that decision.
func BenchmarkNext_AcrossDST(b *testing.B) {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		b.Skipf("zoneinfo unavailable: %v", err)
	}
	s, err := scheduler.ParseInLocation("30 2 * * *", loc)
	if err != nil {
		b.Fatalf("ParseInLocation: %v", err)
	}
	//: the day before the 2026 spring-forward, so the very next fire is the
	//: one at a wall-clock time that does not exist.
	base := time.Date(2026, 3, 28, 12, 0, 0, 0, loc)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(base)
	}
}

// BenchmarkNext_Every is the fixed-interval schedule, which needs no search at
// all — the delta against every cron row is what parsing a calendar costs.
func BenchmarkNext_Every(b *testing.B) {
	s, err := scheduler.Every(30 * time.Second)
	if err != nil {
		b.Fatalf("Every: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		timeSink, okSink = s(benchBase)
	}
}
