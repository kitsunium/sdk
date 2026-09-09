package scheduler_test

import (
	"testing"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcsched "github.com/kitsunium/sdk/internal/service/scheduler"
)

// utcOrigin is the reference instant every accepted-expression case starts
// from: a Friday, 04:05:06 UTC, deliberately not on a minute boundary so the
// "strictly after, truncated to the minute" contract is exercised by default.
var utcOrigin = time.Date(2031, time.March, 7, 4, 5, 6, 0, time.UTC)

// TestParseAcceptedExpressions pins the first instant each accepted form
// produces. Expectations are written as literal instants, never re-derived
// from the parser's own fields, so a change to the walk shows up here.
func TestParseAcceptedExpressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
		want time.Time
	}{
		{"every minute", "* * * * *", time.Date(2031, time.March, 7, 4, 6, 0, 0, time.UTC)},
		{"quarter hour step", "*/15 * * * *", time.Date(2031, time.March, 7, 4, 15, 0, 0, time.UTC)},
		{"explicit list", "10,20,30 * * * *", time.Date(2031, time.March, 7, 4, 10, 0, 0, time.UTC)},
		{"range with step", "0-30/10 * * * *", time.Date(2031, time.March, 7, 4, 10, 0, 0, time.UTC)},
		{"daily at two", "0 2 * * *", time.Date(2031, time.March, 8, 2, 0, 0, 0, time.UTC)},
		{"day of month", "0 0 15 * *", time.Date(2031, time.March, 15, 0, 0, 0, 0, time.UTC)},
		{"named month", "0 0 1 JAN *", time.Date(2032, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"named weekday", "0 0 * * MON", time.Date(2031, time.March, 10, 0, 0, 0, 0, time.UTC)},
		{"named weekday range", "0 0 * * MON-FRI", time.Date(2031, time.March, 10, 0, 0, 0, 0, time.UTC)},
		{"leap day", "0 0 29 2 *", time.Date(2032, time.February, 29, 0, 0, 0, 0, time.UTC)},
		{"macro hourly", "@hourly", time.Date(2031, time.March, 7, 5, 0, 0, 0, time.UTC)},
		{"macro daily", "@daily", time.Date(2031, time.March, 8, 0, 0, 0, 0, time.UTC)},
		{"macro midnight", "@midnight", time.Date(2031, time.March, 8, 0, 0, 0, 0, time.UTC)},
		{"macro weekly", "@weekly", time.Date(2031, time.March, 9, 0, 0, 0, 0, time.UTC)},
		{"macro monthly", "@monthly", time.Date(2031, time.April, 1, 0, 0, 0, 0, time.UTC)},
		{"macro yearly", "@yearly", time.Date(2032, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"macro annually", "@annually", time.Date(2032, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{"macro is case insensitive", "@DAILY", time.Date(2031, time.March, 8, 0, 0, 0, 0, time.UTC)},
		{"surrounding whitespace", "  0 2 * * *  ", time.Date(2031, time.March, 8, 2, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedule, err := svcsched.Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tc.expr, err)
			}
			got, ok := schedule(utcOrigin)
			if !ok {
				t.Fatalf("Parse(%q) produced no next instant after %s", tc.expr, utcOrigin)
			}
			if !got.Equal(tc.want) {
				t.Errorf("next = %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestParseRefusedExpressions pins WHICH refusal each rejected form earns. The
// distinction is the point of the subset: "not valid" and "valid cron this
// parser does not accept" send a reader to two different fixes.
func TestParseRefusedExpressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
		code kerrs.Code
	}{
		{"empty", "", svcsched.CodeInvalidExpression},
		{"blank", "   ", svcsched.CodeInvalidExpression},
		{"four fields", "* * * *", svcsched.CodeInvalidExpression},
		{"six fields with seconds", "0 0 0 12 * *", svcsched.CodeUnsupportedSyntax},
		{"seven fields with year", "0 0 12 * * * 2031", svcsched.CodeUnsupportedSyntax},
		{"reboot macro", "@reboot", svcsched.CodeUnsupportedSyntax},
		{"every macro", "@every 5m", svcsched.CodeUnsupportedSyntax},
		{"unknown macro", "@fortnightly", svcsched.CodeUnsupportedSyntax},
		{"quartz last day", "0 0 L * *", svcsched.CodeUnsupportedSyntax},
		{"quartz nearest weekday", "0 0 15W * *", svcsched.CodeUnsupportedSyntax},
		{"quartz nth weekday", "0 0 * * MON#2", svcsched.CodeUnsupportedSyntax},
		{"quartz no specific value", "0 0 ? * MON", svcsched.CodeUnsupportedSyntax},
		{"step over a single value", "5/10 * * * *", svcsched.CodeUnsupportedSyntax},
		{"minute out of range", "60 * * * *", svcsched.CodeInvalidExpression},
		{"hour out of range", "* 24 * * *", svcsched.CodeInvalidExpression},
		{"day zero", "0 0 0 * *", svcsched.CodeInvalidExpression},
		{"month out of range", "0 0 * 13 *", svcsched.CodeInvalidExpression},
		{"sunday as seven", "0 0 * * 7", svcsched.CodeInvalidExpression},
		{"inverted range", "10-5 * * * *", svcsched.CodeInvalidExpression},
		{"zero step", "*/0 * * * *", svcsched.CodeInvalidExpression},
		{"negative step", "*/-1 * * * *", svcsched.CodeInvalidExpression},
		{"non numeric", "abc * * * *", svcsched.CodeInvalidExpression},
		{"unknown name", "0 0 * * FUNDAY", svcsched.CodeInvalidExpression},
		{"empty list item", "0,,5 * * * *", svcsched.CodeInvalidExpression},
		{"dangling range", "5- * * * *", svcsched.CodeInvalidExpression},
		{"february thirtieth", "0 0 30 2 *", svcsched.CodeUnreachableSchedule},
		{"april thirty first", "0 0 31 4 *", svcsched.CodeUnreachableSchedule},
		{"thirty first of every short month", "0 0 31 2,4,6,9,11 *", svcsched.CodeUnreachableSchedule},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schedule, err := svcsched.Parse(tc.expr)
			if err == nil {
				t.Fatalf("Parse(%q) was accepted; want refusal %#x", tc.expr, uint32(tc.code))
			}
			if schedule != nil {
				t.Errorf("Parse(%q) returned a non-nil Schedule alongside its error", tc.expr)
			}
			if !kerrs.HasCode(err, tc.code) {
				t.Errorf("Parse(%q) = %v; want code %#x", tc.expr, err, uint32(tc.code))
			}
		})
	}
}

// TestParseInLocationRefusesNilLocation pins the ADR 0031 refusal. nil is what
// an unchecked time.LoadLocation leaves behind, so reading it as UTC would run
// the caller's job on a clock they did not choose and never say so.
func TestParseInLocationRefusesNilLocation(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.ParseInLocation("0 2 * * *", nil)
	if err == nil {
		t.Fatalf("ParseInLocation with a nil location was accepted")
	}
	if schedule != nil {
		t.Errorf("ParseInLocation returned a non-nil Schedule alongside its error")
	}
	if !kerrs.HasCode(err, svcsched.CodeInvalidLocation) {
		t.Errorf("err = %v; want INVALID_LOCATION", err)
	}
}

// TestBothDayFieldsRestrictedIsAnOr pins the one POSIX rule implementations
// routinely get wrong. crontab(5): when day-of-month AND day-of-week are both
// restricted, a day matches when EITHER matches. Under the AND reading that
// looks more natural, "0 0 13 * FRI" would mean Friday the 13th, and the
// answer below would be 2031-01-17 rather than 2031-01-13 (a Monday).
func TestBothDayFieldsRestrictedIsAnOr(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Parse("0 0 13 * FRI")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	tests := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "matches the weekday",
			after: time.Date(2031, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2031, time.January, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "matches the day of month even though it is a Monday",
			after: time.Date(2031, time.January, 10, 0, 0, 1, 0, time.UTC),
			want:  time.Date(2031, time.January, 13, 0, 0, 0, 0, time.UTC),
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

// TestOneRestrictedDayFieldIsNotAnOr is the contrast that makes the previous
// test meaningful: with day-of-week left as "*", only the 13th matches, so an
// implementation that ORed unconditionally would fire every day.
func TestOneRestrictedDayFieldIsNotAnOr(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Parse("0 0 13 * *")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	got, ok := schedule(time.Date(2031, time.January, 1, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatalf("no next instant")
	}
	want := time.Date(2031, time.January, 13, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("next = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestNextIsStrictlyIncreasing pins the contract the engine's missed-deadline
// loop depends on to terminate: feeding a returned instant back in must always
// move forward. A Schedule that answered "the same instant" would spin that
// loop forever, so this is a liveness property, not a nicety.
func TestNextIsStrictlyIncreasing(t *testing.T) {
	t.Parallel()
	schedule, err := svcsched.Parse("*/7 * * * *")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	//: a full day of fires is enough to cross hour and day boundaries, which
	//: is where a rounding mistake would show up.
	cursor := utcOrigin
	for i := range 250 {
		next, ok := schedule(cursor)
		if !ok {
			t.Fatalf("iteration %d: schedule exhausted", i)
		}
		if !next.After(cursor) {
			t.Fatalf("iteration %d: next %s is not after %s", i, next, cursor)
		}
		if next.Second() != 0 || next.Nanosecond() != 0 {
			t.Fatalf("iteration %d: next %s is not minute-aligned", i, next)
		}
		cursor = next
	}
}
