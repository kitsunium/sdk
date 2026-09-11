package scheduler_test

import (
	"testing"
	"time"
	// tzdata is embedded so LoadLocation resolves on every GOOS and inside a
	// hermetic build sandbox, where the host tz database may not exist. It is
	// a TEST-only import on purpose: it costs ~450 KB in whatever binary
	// carries it, and that is the consumer's decision to make, not the SDK's.
	_ "time/tzdata"

	svcsched "github.com/kitsunium/sdk/internal/service/scheduler"
)

// newYork is the reference DST zone. 2031-03-09 skips 02:00-03:00 and
// 2031-11-02 repeats 01:00-02:00, which is both halves of the problem in one
// location.
func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	//: never t.Skip here. A skipped DST test is exactly the "gate that
	//: verifies nothing" rule 12 is about, and the embedded tzdata above is
	//: what makes failing correct rather than harsh.
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York) failed even with time/tzdata embedded: %v", err)
	}
	return loc
}

// TestSpringForwardSkipsTheDay pins the first DST arbitrage: a wall-clock time
// that does not exist that day does not fire, and is NOT shifted to a
// neighbouring hour.
//
// 2031-03-09 in New York jumps from 01:59 EST straight to 03:00 EDT, so
// "0 2 * * *" has no instant to run at. Shifting it to 03:00 would run the job
// at a time the expression does not name; shifting it to 01:00 would run it
// early. Skipping is the only answer that keeps the expression's meaning, and
// it costs one missed run a year.
func TestSpringForwardSkipsTheDay(t *testing.T) {
	t.Parallel()
	loc := newYork(t)
	schedule, err := svcsched.ParseInLocation("0 2 * * *", loc)
	if err != nil {
		t.Fatalf("ParseInLocation failed: %v", err)
	}
	tests := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "an ordinary day fires at 02:00",
			after: time.Date(2031, time.March, 7, 12, 0, 0, 0, loc),
			want:  time.Date(2031, time.March, 8, 2, 0, 0, 0, loc),
		},
		{
			name:  "the transition day is skipped entirely",
			after: time.Date(2031, time.March, 8, 12, 0, 0, 0, loc),
			want:  time.Date(2031, time.March, 10, 2, 0, 0, 0, loc),
		},
		{
			name:  "the day after the transition fires normally",
			after: time.Date(2031, time.March, 10, 12, 0, 0, 0, loc),
			want:  time.Date(2031, time.March, 11, 2, 0, 0, 0, loc),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := schedule(tc.after)
			if !ok {
				t.Fatalf("no next instant after %s", tc.after)
			}
			if !got.Equal(tc.want) {
				t.Errorf("next = %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestSpringForwardKeepsSchedulesOutsideTheGap is the contrast that stops the
// previous test from passing for the wrong reason: only the missing hour is
// skipped, not the whole transition day.
func TestSpringForwardKeepsSchedulesOutsideTheGap(t *testing.T) {
	t.Parallel()
	loc := newYork(t)
	schedule, err := svcsched.ParseInLocation("30 4 * * *", loc)
	if err != nil {
		t.Fatalf("ParseInLocation failed: %v", err)
	}
	got, ok := schedule(time.Date(2031, time.March, 8, 12, 0, 0, 0, loc))
	if !ok {
		t.Fatalf("no next instant")
	}
	want := time.Date(2031, time.March, 9, 4, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("next = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestFallBackFiresOnceAtTheFirstOccurrence pins the second DST arbitrage: a
// wall-clock time that happens TWICE fires once, at the first occurrence.
//
// 2031-11-02 in New York replays 01:00-02:00, so 01:30 exists at both
// 05:30Z (EDT) and 06:30Z (EST). The schedule is strictly increasing by
// contract, so once 05:30Z has been produced the next answer must be after it
// — and the walk moves on to the next calendar day rather than re-offering the
// same wall-clock reading in the other offset. The UTC assertions below are
// what make "the FIRST occurrence" a checkable claim rather than a wording.
func TestFallBackFiresOnceAtTheFirstOccurrence(t *testing.T) {
	t.Parallel()
	loc := newYork(t)
	schedule, err := svcsched.ParseInLocation("30 1 * * *", loc)
	if err != nil {
		t.Fatalf("ParseInLocation failed: %v", err)
	}
	first, ok := schedule(time.Date(2031, time.November, 1, 12, 0, 0, 0, loc))
	if !ok {
		t.Fatalf("no next instant on the fall-back day")
	}
	wantFirst := time.Date(2031, time.November, 2, 5, 30, 0, 0, time.UTC)
	if !first.Equal(wantFirst) {
		t.Fatalf("first fire = %s (UTC %s), want UTC %s — the EDT occurrence",
			first.Format(time.RFC3339), first.UTC().Format(time.RFC3339),
			wantFirst.Format(time.RFC3339))
	}
	second, ok := schedule(first)
	if !ok {
		t.Fatalf("no instant after the fall-back fire")
	}
	//: 2031-11-02T06:30Z is 01:30 EST — the SECOND occurrence of the same wall
	//: clock. Producing it would run the job twice for one nominal fire.
	repeat := time.Date(2031, time.November, 2, 6, 30, 0, 0, time.UTC)
	if second.Equal(repeat) {
		t.Fatalf("second fire = %s: the repeated hour fired twice", second.UTC().Format(time.RFC3339))
	}
	wantSecond := time.Date(2031, time.November, 3, 6, 30, 0, 0, time.UTC)
	if !second.Equal(wantSecond) {
		t.Errorf("second fire = UTC %s, want UTC %s",
			second.UTC().Format(time.RFC3339), wantSecond.Format(time.RFC3339))
	}
}

// TestFallBackDayIsOtherwiseNormal is the contrast for the fall-back case: an
// hour outside the repeated window fires exactly once, at its ordinary offset.
func TestFallBackDayIsOtherwiseNormal(t *testing.T) {
	t.Parallel()
	loc := newYork(t)
	schedule, err := svcsched.ParseInLocation("0 3 * * *", loc)
	if err != nil {
		t.Fatalf("ParseInLocation failed: %v", err)
	}
	got, ok := schedule(time.Date(2031, time.November, 1, 12, 0, 0, 0, loc))
	if !ok {
		t.Fatalf("no next instant")
	}
	//: 03:00 EST on the fall-back day is 08:00Z; the hour it replaced is over.
	want := time.Date(2031, time.November, 2, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("next = UTC %s, want UTC %s", got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// loadZone resolves a tz database name. Like newYork it fails rather than
// skips: the embedded tzdata makes a missing zone a real defect.
func loadZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%s) failed even with time/tzdata embedded: %v", name, err)
	}
	return loc
}

// TestFallBackEastOfUTCFiresAtTheFirstOccurrence pins "fires ONCE, at the
// FIRST occurrence" in the zones where it used to be false.
//
// time.Date does not promise which occurrence of a repeated reading it
// returns, and its lookup — the zone in force at the wall-clock reading taken
// as a UTC instant — lands on the LATER one east of UTC. New York, the only
// zone TestFallBackFiresOnceAtTheFirstOccurrence uses, is west of UTC and
// comes back first, which is how the defect stayed invisible: in Berlin a job
// at 02:30 fired once, but an hour late. Berlin repeats 02:00-03:00 on
// 2026-10-25, so 02:30 exists at 00:30Z (CEST) and 01:30Z (CET). Lord Howe
// repeats 01:30-02:00 on 2026-04-05 — a thirty-minute shift, so the rule
// cannot be "subtract an hour" — and 01:45 exists at 14:45Z and 15:15Z the
// day before in UTC. Every expectation is in UTC, so "first" is checkable.
//
// The third check asks from INSIDE the second pass, before the reading comes
// round again: the repeat is still not offered, because the reading has
// already fired once that day.
//
// Mutation: making materialise return time.Date's answer as-is (the code
// before the fix) failed with "first fire = 2026-10-25T01:30:00Z, want
// 2026-10-25T00:30:00Z" for Berlin and "first fire = 2026-04-04T15:15:00Z,
// want 2026-04-04T14:45:00Z" for Lord Howe.
func TestFallBackEastOfUTCFiresAtTheFirstOccurrence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		zone   string
		expr   string
		after  time.Time
		first  time.Time
		repeat time.Time
		inside time.Time
		next   time.Time
	}{
		{
			name:   "Europe/Berlin repeats a whole hour",
			zone:   "Europe/Berlin",
			expr:   "30 2 * * *",
			after:  time.Date(2026, time.October, 24, 10, 0, 0, 0, time.UTC),
			first:  time.Date(2026, time.October, 25, 0, 30, 0, 0, time.UTC),
			repeat: time.Date(2026, time.October, 25, 1, 30, 0, 0, time.UTC),
			inside: time.Date(2026, time.October, 25, 1, 10, 0, 0, time.UTC),
			next:   time.Date(2026, time.October, 26, 1, 30, 0, 0, time.UTC),
		},
		{
			name:   "Australia/Lord_Howe repeats thirty minutes",
			zone:   "Australia/Lord_Howe",
			expr:   "45 1 * * *",
			after:  time.Date(2026, time.April, 4, 1, 0, 0, 0, time.UTC),
			first:  time.Date(2026, time.April, 4, 14, 45, 0, 0, time.UTC),
			repeat: time.Date(2026, time.April, 4, 15, 15, 0, 0, time.UTC),
			inside: time.Date(2026, time.April, 4, 15, 5, 0, 0, time.UTC),
			next:   time.Date(2026, time.April, 5, 15, 15, 0, 0, time.UTC),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedule, err := svcsched.ParseInLocation(tc.expr, loadZone(t, tc.zone))
			if err != nil {
				t.Fatalf("ParseInLocation failed: %v", err)
			}
			first, ok := schedule(tc.after)
			if !ok || !first.Equal(tc.first) {
				t.Fatalf("first fire = %s, want %s", first.UTC().Format(time.RFC3339), tc.first.Format(time.RFC3339))
			}
			//: once is once: from the first occurrence, the next answer is the
			//: following day, never the repeat an hour (or half an hour) later.
			second, ok := schedule(first)
			if !ok || second.Equal(tc.repeat) || !second.Equal(tc.next) {
				t.Errorf("after the first fire, next = %s, want %s (the repeat is %s)",
					second.UTC().Format(time.RFC3339), tc.next.Format(time.RFC3339), tc.repeat.Format(time.RFC3339))
			}
			//: asked from inside the second pass, the repeat is still not due.
			fromInside, ok := schedule(tc.inside)
			if !ok || !fromInside.Equal(tc.next) {
				t.Errorf("from inside the repeat, next = %s, want %s (the repeat is %s)",
					fromInside.UTC().Format(time.RFC3339), tc.next.Format(time.RFC3339), tc.repeat.Format(time.RFC3339))
			}
		})
	}
}

// TestUTCHasNoTransitions is the control. The same two expressions in UTC fire
// on every single day, which is what makes UTC the default: there is no
// arbitrage to make.
func TestUTCHasNoTransitions(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	cursor := time.Date(2031, time.March, 7, 12, 0, 0, 0, time.UTC)
	//: walk across both transition dates; every step must advance exactly one
	//: day and land on 02:00.
	for day := range 250 {
		next, ok := schedule(cursor)
		if !ok {
			t.Fatalf("day %d: schedule exhausted", day)
		}
		if next.Hour() != 2 || next.Minute() != 0 {
			t.Fatalf("day %d: fired at %s, not 02:00", day, next.Format(time.RFC3339))
		}
		cursor = next
	}
}
