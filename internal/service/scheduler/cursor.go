// Package scheduler — hosts the calendar cursor the cron walk advances.
package scheduler

import "time"

// cursor is a WALL-CLOCK position — year/month/day/hour/minute — not an
// instant. It is carried as a time.Time in UTC purely because UTC has no
// transitions, which makes time.Date there a pure calendar normaliser: it
// turns "February 30" into "March 1" and "hour 24" into "next day 00:00"
// without ever consulting a DST rule.
//
// That separation is the whole reason the cron walk survives DST. Advancing
// the cursor is calendar arithmetic on fields; deciding whether the resulting
// wall-clock reading EXISTS in the schedule's real location is a separate step
// ([cursor.materialise]). Doing both with one time.Time in the target location
// is how cron implementations end up shifting a job by an hour twice a year.
type cursor struct {
	// t holds the wall-clock fields, in UTC, with seconds and nanoseconds
	// zeroed — cron's finest field is the minute.
	t time.Time
}

// newCursor builds a cursor from a reading already expressed in the target
// location, keeping the fields and dropping the offset.
func newCursor(t time.Time) cursor {
	//: re-anchor the same field values in UTC; only the calendar matters here.
	return cursor{t: time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)}
}

// year returns the cursor's year.
func (c cursor) year() int {
	//: the field, not an instant.
	return c.t.Year()
}

// month returns the cursor's month as 1-12.
func (c cursor) month() int {
	//: cron fields are 1-based months; time.Month already is.
	return int(c.t.Month())
}

// day returns the cursor's day of month as 1-31.
func (c cursor) day() int {
	//: normalisation guarantees this is a day the month actually has.
	return c.t.Day()
}

// hour returns the cursor's hour as 0-23.
func (c cursor) hour() int {
	//: the field, not an instant.
	return c.t.Hour()
}

// minute returns the cursor's minute as 0-59.
func (c cursor) minute() int {
	//: the field, not an instant.
	return c.t.Minute()
}

// weekday returns the cursor's day of week as 0-6, Sunday = 0. The weekday of
// a calendar date does not depend on the location, so reading it off the UTC
// carrier is exact.
func (c cursor) weekday() int {
	//: time.Weekday is already Sunday = 0, matching the cron field.
	return int(c.t.Weekday())
}

// nextMinute advances one minute. UTC has no transitions, so adding a duration
// here is exact calendar arithmetic — which it would NOT be in a zone with DST.
func (c cursor) nextMinute() cursor {
	//: the smallest step the cron grid has.
	return cursor{t: c.t.Add(time.Minute)}
}

// nextHour advances to the next hour at minute 00.
func (c cursor) nextHour() cursor {
	//: hour+1 normalises past 23 into the next day.
	return cursor{t: time.Date(c.year(), c.t.Month(), c.day(), c.hour()+1, 0, 0, 0, time.UTC)}
}

// nextDay advances to the next day at 00:00.
func (c cursor) nextDay() cursor {
	//: day+1 normalises past the month's length into the next month.
	return cursor{t: time.Date(c.year(), c.t.Month(), c.day()+1, 0, 0, 0, 0, time.UTC)}
}

// nextMonth advances to the 1st of the next month at 00:00.
func (c cursor) nextMonth() cursor {
	//: month+1 normalises past December into January of the next year.
	return cursor{t: time.Date(c.year(), c.t.Month()+1, 1, 0, 0, 0, 0, time.UTC)}
}

// materialise resolves the cursor's wall-clock fields into a real instant in
// loc, reporting false when that reading does not exist there.
//
// The false case is the spring-forward gap: time.Date is documented to
// normalise a non-existent local time into the neighbouring offset, so asking
// for 02:30 on a day where 02:00-03:00 is skipped yields 03:30. Comparing the
// fields back is what turns that silent shift into an honest "this minute does
// not exist", which the walk then skips.
//
// The ambiguous case — a wall-clock reading that happens twice on a
// fall-back day — needs no special handling: time.Date resolves it to the
// FIRST occurrence, the fields match, and the walk's strictly-increasing
// contract means the second occurrence is never revisited. The job runs once.
func (c cursor) materialise(loc *time.Location) (time.Time, bool) {
	//: ask the location to place these fields on its own timeline.
	got := time.Date(c.year(), c.t.Month(), c.day(), c.hour(), c.minute(), 0, 0, loc)
	//: a reading that came back different is a reading that does not exist.
	if got.Year() != c.year() || got.Month() != c.t.Month() || got.Day() != c.day() ||
		got.Hour() != c.hour() || got.Minute() != c.minute() {
		//: the walk treats it as a minute the calendar skipped.
		return time.Time{}, false
	}
	//: the wall-clock reading exists in loc and this is its instant.
	return got, true
}
