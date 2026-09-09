// Package scheduler provides the concrete half of the scheduling domain: a
// five-field POSIX cron parser, a fixed-interval schedule, and the engine that
// fires core/scheduler.Job values through core/scheduler.Schedule. ADR 0041.
//
// The engine depends on internal/kernel/clock.Timed, never on package time
// directly, so a test drives cadence, missed deadlines and overlap by moving a
// ManualClock instead of sleeping (ADR 0039).
//
// Cross-OS: 100% portable Go (time, context, sync, sync/atomic, strconv,
// strings). Cron's finest field is the minute, which is deliberately coarser
// than any platform's timer resolution — see CLAUDE.md §What is guaranteed.
package scheduler

import (
	"strconv"
	"strings"
	"time"

	coresched "github.com/kitsunium/sdk/internal/core/scheduler"
)

// cronFieldCount is the number of fields in the accepted dialect: minute,
// hour, day-of-month, month, day-of-week. Five, and only five — see
// rejectFieldCount for why six is refused rather than read as "with seconds".
const cronFieldCount int = 5

// horizonYears bounds the forward search for a matching instant. It is 9
// because the widest gap a REACHABLE expression can have is Feb 29 across a
// non-leap century year (2096 → 2104, just under eight years); anything that
// finds nothing in nine years matches nothing at all.
const horizonYears int = 9

// probeYear anchors the reachability probe. It is a FIXED year, not "now", so
// that parsing the same expression twice always gives the same answer — a
// parser whose verdict depends on the day it runs is a parser that passes CI
// and fails in production. 2000 is a leap year, so a Feb 29 schedule proves
// itself on the first pass.
const probeYear int = 2000

// The two field counts that identify a neighbouring cron dialect rather than a
// typo: six fields is the seconds dialect, seven adds a year.
const (
	secondsDialectFields int = cronFieldCount + 1
	yearDialectFields    int = cronFieldCount + 2
)

// cronSchedule is a parsed cron expression: five membership sets, the two
// restriction flags the POSIX day rule needs, and the location its wall-clock
// fields are read in.
type cronSchedule struct {
	// minutes … weekdays are membership sets indexed by field value.
	minutes  []bool
	hours    []bool
	days     []bool
	months   []bool
	weekdays []bool
	// domRestricted / dowRestricted record whether each day field was written
	// as something other than "*". POSIX makes the two fields an OR when BOTH
	// are restricted, and an AND otherwise — see matchesDay.
	domRestricted bool
	dowRestricted bool
	// loc is the location the expression's wall-clock fields are read in.
	loc *time.Location
}

// Parse compiles a five-field POSIX cron expression, evaluated in UTC.
//
// UTC is the default rather than time.Local because time.Local depends on TZ
// and on the host: the same expression would mean different instants on two
// machines in one fleet, and the difference would surface as a job that ran an
// hour early on one node. A caller who wants local time says so, with
// ParseInLocation(expr, time.Local).
//
// Accepted: five space-separated fields — minute (0-59), hour (0-23),
// day-of-month (1-31), month (1-12 or JAN-DEC), day-of-week (0-6 or SUN-SAT,
// Sunday = 0) — each of which may be "*", a value, an "a-b" range, a
// comma-separated list, or a "*/n" / "a-b/n" step. Names are
// case-insensitive. The macros @yearly, @annually, @monthly, @weekly, @daily,
// @midnight and @hourly expand to the expressions above.
//
// Refused, by name rather than by guessing: six- and seven-field expressions
// (seconds / year), @reboot, @every, the Quartz operators L, W, # and ?, a
// step written over a single value, an inverted range, 7 for Sunday, and a
// valid expression that matches no date on the calendar.
//
// When both day fields are restricted, POSIX makes them an OR: "0 0 13 * FRI"
// fires on every 13th AND on every Friday, not only on Friday the 13th.
func Parse(expr string) (schedule coresched.Schedule, err error) {
	//: UTC is the one location with no DST and no political drift; every other
	//: default would be a guess at the caller's fleet.
	return ParseInLocation(expr, time.UTC)
}

// ParseInLocation compiles a cron expression evaluated in loc.
//
// A nil loc is REFUSED, not read as UTC: nil is what an unchecked
// time.LoadLocation returns on failure, so accepting it would turn a missing
// tzdata into a schedule that silently runs on a different clock. Parse is the
// way to ask for UTC.
//
// Around a DST transition in loc the semantics are exact and are not
// negotiable per caller:
//
//   - Spring forward. A wall-clock time that does not exist that day does not
//     fire. "0 2 * * *" in a zone that skips 02:00-03:00 skips that day
//     entirely; it is not shifted to 01:00 or 03:00, because both of those are
//     instants the expression does not name.
//   - Fall back. A wall-clock time that happens twice fires ONCE, at the first
//     occurrence. The schedule is strictly increasing by contract, and the
//     second occurrence is behind the instant already returned.
func ParseInLocation(expr string, loc *time.Location) (parsed coresched.Schedule, err error) {
	//: a nil location is an unchecked LoadLocation, not a request for UTC.
	if loc == nil {
		//: refuse rather than substituting a zone the caller did not name.
		return nil, rejectLocation()
	}
	fields, err := splitExpression(expr)
	//: a shape the parser cannot even split into fields.
	if err != nil {
		//: propagate the refusal unchanged.
		return nil, err
	}
	compiled, err := buildSchedule(fields, loc)
	//: a field that does not parse.
	if err != nil {
		//: propagate the refusal unchanged.
		return nil, err
	}
	//: an expression that parses but matches nothing is refused HERE rather
	//: than accepted as a job that never runs — the distinction ADR 0031 draws
	//: between an empty scheduler (legitimate) and an inert one (a defect).
	if _, ok := compiled.next(time.Date(probeYear, time.January, 1, 0, 0, 0, 0, loc)); !ok {
		//: name the horizon so the claim is falsifiable.
		return nil, rejectUnreachable(expr)
	}
	//: the port is a func, so the method value IS the Schedule.
	return compiled.next, nil
}

// splitExpression trims, expands a macro, and checks the field count.
func splitExpression(expr string) (fields []string, err error) {
	text := strings.TrimSpace(expr)
	//: an empty expression has no honest reading.
	if text == "" {
		//: refuse before anything downstream has to guess.
		return nil, rejectExpression("expression is empty", expr, "")
	}
	//: a leading @ means the whole expression is a macro, never a field.
	if strings.HasPrefix(text, "@") {
		expanded, err := expandMacro(text)
		//: an unknown or refused macro stops here.
		if err != nil {
			//: propagate the refusal unchanged.
			return nil, err
		}
		text = expanded
	}
	split := strings.Fields(text)
	//: the field count is the first thing a dialect mismatch shows up in.
	if len(split) != cronFieldCount {
		//: six fields gets its own message; see rejectFieldCount.
		return nil, rejectFieldCount(text, len(split))
	}
	//: exactly five fields, ready to parse.
	return split, nil
}

// expandMacro resolves an @-shorthand, refusing the ones outside the subset.
// The accepted set is written out as a switch rather than a table because the
// two refusals below belong beside the acceptances: they are the same decision.
func expandMacro(text string) (expanded string, err error) {
	//: macros are matched case-insensitively and as a whole expression.
	lowered := strings.ToLower(text)
	//: dispatch on the macro, taking the leading word so "@every 5m" is
	//: recognised as @every rather than as an unknown macro.
	switch name, _, _ := strings.Cut(lowered, " "); lowered {
	//: midnight every 1 January.
	case "@yearly", "@annually":
		//: the January 1st expression.
		return "0 0 1 1 *", nil
	//: midnight on the 1st.
	case "@monthly":
		//: the first-of-the-month expression.
		return "0 0 1 * *", nil
	//: midnight on Sunday.
	case "@weekly":
		//: the Sunday expression.
		return "0 0 * * 0", nil
	//: midnight every day.
	case "@daily", "@midnight":
		//: the every-day expression.
		return "0 0 * * *", nil
	//: on the hour, every hour.
	case "@hourly":
		//: the every-hour expression.
		return "0 * * * *", nil
	//: everything else is refused, by name where the name is known.
	default:
		//: refuseMacro picks the message.
		return "", refuseMacro(name, text)
	}
}

// refuseMacro names the refusal for an @-shorthand outside the subset.
func refuseMacro(name, text string) error {
	//: dispatch on the leading word so the message can be specific.
	switch name {
	//: @every is a Quartz/robfig extension, not cron. Refusing it and offering
	//: Every(d) keeps the two ideas apart: one parses a calendar expression,
	//: the other counts a duration from the last fire.
	case "@every":
		//: point at the constructor that does express an interval.
		return rejectSyntax("@every is not cron syntax; use Every(d) for a fixed interval", text)
	//: a library has no boot to observe — Run starts when the caller calls it.
	//: Accepting @reboot would mean inventing a meaning for it.
	case "@reboot":
		//: point at the only thing a library can honestly offer instead.
		return rejectSyntax("@reboot has no meaning in a library; run the job yourself before Run", text)
	//: an unknown macro is listed against the accepted set, not just rejected.
	default:
		//: list the accepted set rather than only rejecting.
		return rejectSyntax(
			"unknown macro; accepted: @yearly @annually @monthly @weekly @daily @midnight @hourly", text)
	}
}

// rejectFieldCount refuses a field count that is not five, distinguishing the
// dialect mismatch from a plain typo.
func rejectFieldCount(text string, got int) error {
	//: six and seven fields are the seconds and year dialects. They are the
	//: likeliest mistake AND the most dangerous one: read as five fields, "0 0
	//: 12 * * *" would silently mean something else entirely. A seconds field
	//: would also promise a resolution this domain does not guarantee — see
	//: CLAUDE.md §What is guaranteed.
	if got == secondsDialectFields || got == yearDialectFields {
		//: name the dialect so the caller knows what to strip.
		return rejectSyntax("six-field (seconds) and seven-field (year) expressions "+
			"are not accepted; this parser is five-field POSIX cron", text)
	}
	//: any other count is a malformed expression.
	return rejectExpression("expected 5 fields (minute hour day-of-month month day-of-week)",
		text, "got "+strconv.Itoa(got))
}

// buildSchedule parses the five fields into membership sets.
func buildSchedule(fields []string, loc *time.Location) (schedule *cronSchedule, err error) {
	var sets [cronFieldCount][]bool
	//: the spec table and the field order are the same list, by construction.
	for i := range fieldSpecs {
		set, parseErr := fieldSpecs[i].parse(fields[i])
		//: one unparseable field invalidates the whole expression.
		if parseErr != nil {
			//: propagate the refusal unchanged.
			return nil, parseErr
		}
		sets[i] = set
	}
	//: "restricted" is a TEXTUAL property in POSIX — the field is not "*" —
	//: not a semantic one, so "*/1" counts as restricted exactly as crontab(5)
	//: describes it.
	return &cronSchedule{
		minutes:       sets[minuteField],
		hours:         sets[hourField],
		days:          sets[domField],
		months:        sets[monthField],
		weekdays:      sets[dowField],
		domRestricted: fields[domField] != "*",
		dowRestricted: fields[dowField] != "*",
		loc:           loc,
	}, nil
}

// next reports the first instant strictly after `after` at which this
// expression is due. It satisfies core/scheduler.Schedule as a method value.
//
// The walk advances a calendar [cursor] — wall-clock fields normalised through
// UTC — and only resolves a candidate into a real instant once every field
// matches. Skipping a coarse field jumps the cursor to the start of the next
// one, so an expression like "0 0 29 2 *" costs a few hundred steps per match,
// not four years of minutes.
func (c *cronSchedule) next(after time.Time) (fireAt time.Time, ok bool) {
	//: read the fields in the schedule's own location, then step one minute so
	//: the answer is STRICTLY after the argument.
	cur := newCursor(after.In(c.loc)).nextMinute()
	limit := cur.year() + horizonYears
	//: walk the calendar forward, coarsest field first, until something matches
	//: or the horizon proves the expression unreachable.
	for cur.year() <= limit {
		//: dispatch on the coarsest field that does NOT match, so a skip jumps
		//: the cursor to the start of the next unit instead of stepping minutes.
		switch {
		//: skip the whole month, landing on the 1st at 00:00.
		case !c.months[cur.month()]:
			cur = cur.nextMonth()
		//: skip the whole day, landing at 00:00.
		case !c.matchesDay(cur):
			cur = cur.nextDay()
		//: skip the whole hour, landing at minute 00.
		case !c.hours[cur.hour()]:
			cur = cur.nextHour()
		//: the finest field — one minute at a time.
		case !c.minutes[cur.minute()]:
			cur = cur.nextMinute()
		//: every field matches; the only question left is whether this
		//: wall-clock reading exists in loc and lies after `after`.
		default:
			got, exists := cur.materialise(c.loc)
			//: a match that exists and is in the future is the answer.
			if exists && got.After(after) {
				//: done.
				return got, true
			}
			//: !exists is the spring-forward gap; !After is the repeated hour on
			//: a fall-back day. Both mean "keep looking", never "fire anyway".
			cur = cur.nextMinute()
		}
	}
	//: nothing matched within the horizon — the expression is unreachable.
	return time.Time{}, false
}

// matchesDay applies the POSIX day rule: when BOTH day fields are restricted a
// day matches if it matches EITHER, and otherwise it must match both.
//
// This is the rule people get wrong. crontab(5) is explicit: "If both fields
// are restricted (i.e., are not *), the command will be run when either field
// matches the current time." Implementing it as an AND makes "0 0 13 * FRI"
// mean Friday the 13th, which is not what POSIX cron does.
func (c *cronSchedule) matchesDay(cur cursor) bool {
	dom := c.days[cur.day()]
	dow := c.weekdays[cur.weekday()]
	//: both restricted → OR, per crontab(5).
	if c.domRestricted && c.dowRestricted {
		//: either field is enough.
		return dom || dow
	}
	//: at most one is restricted; the other's set is all-true, so AND is the
	//: same as "the restricted one matches" and reads more plainly.
	return dom && dow
}
