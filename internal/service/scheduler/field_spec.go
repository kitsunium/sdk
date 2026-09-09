// Package scheduler — hosts fieldSpec, the description of one cron field and
// the parser that turns its text into a membership set.
package scheduler

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// quartzOperators are the Quartz day-of-month / day-of-week operators this
// parser refuses by name. They are checked only against a token that is
// neither a number nor a known name, so JUL (an L) and WED (a W) are never
// mistaken for them.
const quartzOperators string = "LW#?"

// The inclusive bounds of each cron field. Month and weekday are derived from
// package time's own constants rather than written out, so the two numberings
// cannot drift from the ones cursor.weekday and cursor.month report.
const (
	minMinute     int = 0
	maxMinute     int = 59
	minHour       int = 0
	maxHour       int = 23
	minDayOfMonth int = 1
	maxDayOfMonth int = 31
	minMonth      int = int(time.January)
	maxMonth      int = int(time.December)
	minWeekday    int = int(time.Sunday)
	maxWeekday    int = int(time.Saturday)
)

// The position of each field in a five-field expression.
const (
	minuteField int = iota
	hourField
	domField
	monthField
	dowField
)

// fieldSpec describes one cron field: its human label, its inclusive bounds,
// the names it accepts instead of numbers, and a hint appended to an
// out-of-range refusal where a neighbouring dialect would have accepted the
// value.
type fieldSpec struct {
	// label names the field in every refusal ("minute", "day-of-week", …).
	label string
	// minValue and maxValue are the inclusive bounds of the field.
	minValue int
	maxValue int
	// names are the upper-case aliases this field accepts, in value order:
	// the index of a name PLUS minValue is its numeric value, so the table
	// cannot disagree with the bounds beside it.
	names []string
	// hint is appended to an out-of-range refusal. It exists for the one case
	// where the refusal would otherwise look arbitrary: Vixie cron accepts 7
	// for Sunday, this subset does not, and the caller deserves to be told
	// which value to write instead.
	hint string
}

var (
	// monthNames runs JAN..DEC, so index 0 + minMonth is January.
	monthNames = []string{
		"JAN", "FEB", "MAR", "APR", "MAY", "JUN",
		"JUL", "AUG", "SEP", "OCT", "NOV", "DEC",
	}

	// weekdayNames runs SUN..SAT, so index 0 + minWeekday is Sunday.
	weekdayNames = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

	// fieldSpecs describes the five POSIX fields, in expression order.
	fieldSpecs = [cronFieldCount]fieldSpec{
		minuteField: {label: "minute", minValue: minMinute, maxValue: maxMinute},
		hourField:   {label: "hour", minValue: minHour, maxValue: maxHour},
		domField:    {label: "day-of-month", minValue: minDayOfMonth, maxValue: maxDayOfMonth},
		monthField:  {label: "month", minValue: minMonth, maxValue: maxMonth, names: monthNames},
		dowField: {
			label: "day-of-week", minValue: minWeekday, maxValue: maxWeekday, names: weekdayNames,
			hint: "Sunday is 0 or SUN; 7 is a Vixie extension this subset does not accept",
		},
	}
)

// parse turns one field's text into a membership set indexed by value, of
// length maxValue+1 so a lookup is a direct index with no offset arithmetic.
//
// field is never empty: it comes from strings.Fields, which does not produce
// empty tokens. The invariant is stated rather than guarded, because a guard
// here would be a branch no test can reach and no reader can trust — the
// stance internal/kernel/clock takes on the same shape of impossible input.
// An empty item WITHIN a field ("0,,5") is a different case and is refused
// below, by value.
func (fs *fieldSpec) parse(field string) (set []bool, err error) {
	set = make([]bool, fs.maxValue+1)
	//: a field is a comma-separated list; each item independently marks values.
	for item := range strings.SplitSeq(field, ",") {
		lo, hi, step, boundsErr := fs.bounds(item)
		//: one bad item invalidates the field — no partial acceptance.
		if boundsErr != nil {
			//: the item's own refusal already names the field and the text.
			return nil, boundsErr
		}
		//: mark every value the item selects.
		for value := lo; value <= hi; value += step {
			set[value] = true
		}
	}
	//: the field is a usable membership set.
	return set, nil
}

// bounds resolves one comma-separated item into an inclusive [lo, hi] range
// and a step: "*", "a", "a-b", "*/n" or "a-b/n".
func (fs *fieldSpec) bounds(item string) (lo, hi, step int, err error) {
	spec, stepText, hasStep := strings.Cut(item, "/")
	step = 1
	//: a step is only meaningful over a span, so validate it before the span.
	if hasStep {
		step, err = fs.step(stepText, item)
		//: a malformed step invalidates the item.
		if err != nil {
			//: propagate the refusal unchanged.
			return 0, 0, 0, err
		}
		//: "5/10" is a Vixie extension meaning "from 5 to the max, every 10".
		//: Refuse it: a step written over a single value reads as a typo far
		//: more often than as that extension, and guessing which is the defect
		//: this parser exists to avoid.
		if spec != "*" && !strings.Contains(spec, "-") {
			//: name the requirement rather than the dialect.
			return 0, 0, 0, rejectSyntax(
				"a step needs '*' or an explicit a-b range on its left", item)
		}
	}
	//: "*" spans the whole field.
	if spec == "*" {
		//: the step, if any, applies across the full span.
		return fs.minValue, fs.maxValue, step, nil
	}
	//: everything else is a single value or an explicit range.
	return fs.span(spec, item, step)
}

// span resolves the non-"*" half of an item: "a" or "a-b".
func (fs *fieldSpec) span(spec, item string, step int) (lo, hi, resolvedStep int, err error) {
	loText, hiText, isRange := strings.Cut(spec, "-")
	lo, err = fs.value(loText, item)
	//: the low bound must resolve before anything else is worth checking.
	if err != nil {
		//: propagate the refusal unchanged.
		return 0, 0, 0, err
	}
	//: a bare value selects exactly itself.
	if !isRange {
		//: lo == hi, so the step never advances past it.
		return lo, lo, step, nil
	}
	hi, err = fs.value(hiText, item)
	//: the high bound must resolve too.
	if err != nil {
		//: propagate the refusal unchanged.
		return 0, 0, 0, err
	}
	//: a wrapping range ("FRI-MON") is a real cron idiom in some dialects and
	//: is NOT accepted here: it would silently select a different set than the
	//: text reads. Write the two spans instead.
	if hi < lo {
		//: name the fix, not just the fault.
		return 0, 0, 0, rejectExpression(
			"range end is before its start; write two comma-separated spans instead",
			item, fs.label)
	}
	//: an ordered, in-range span.
	return lo, hi, step, nil
}

// value resolves one token — a name or a number — inside its field's bounds.
func (fs *fieldSpec) value(token, item string) (value int, err error) {
	//: an empty token comes from "5-" or "-5"; both are malformed.
	if token == "" {
		//: refuse before strconv gets a chance to say something vaguer.
		return 0, rejectExpression("missing value", item, fs.label)
	}
	//: names win over numbers so JUL and WED never reach the operator check.
	//: A field with no name table gets index -1 and falls through.
	if index := slices.Index(fs.names, strings.ToUpper(token)); index >= 0 {
		//: the table is in value order, so the offset IS the field's floor.
		return fs.minValue + index, nil
	}
	value, convErr := strconv.Atoi(token)
	//: not a name and not a number — decide WHICH refusal it earns.
	if convErr != nil {
		//: a Quartz operator is refused by name; anything else is malformed.
		return 0, fs.rejectToken(token, item)
	}
	//: a number outside the field's bounds is not a cron value.
	if value < fs.minValue || value > fs.maxValue {
		//: the refusal carries the accepted range, and the hint when there is one.
		return 0, rejectExpression("value out of range for "+fs.label+" ("+
			rangeText(fs.minValue, fs.maxValue)+")"+fs.hintSuffix(), item, fs.label)
	}
	//: a usable value.
	return value, nil
}

// rejectToken picks between the "unsupported dialect" and the "malformed"
// refusal for a token that is neither a name nor a number.
func (fs *fieldSpec) rejectToken(token, item string) error {
	//: L / W / # / ? are Quartz, not POSIX — say so instead of "not a number".
	if strings.ContainsAny(strings.ToUpper(token), quartzOperators) {
		//: refuse by name so the caller knows the expression is unsupported
		//: here rather than wrong everywhere.
		return rejectSyntax("Quartz operators L, W, # and ? are not accepted", item)
	}
	//: anything else is simply not a cron token for this field.
	return rejectExpression("not a number or a known name for "+fs.label, item, fs.label)
}

// hintSuffix renders the field's hint as a trailing clause, or nothing.
func (fs *fieldSpec) hintSuffix() string {
	//: most fields have no hint; only day-of-week does today.
	if fs.hint == "" {
		//: nothing to append.
		return ""
	}
	//: a separator the message reads naturally with.
	return " — " + fs.hint
}

// step parses the "/n" half of an item.
func (fs *fieldSpec) step(text, item string) (stride int, err error) {
	stride, convErr := strconv.Atoi(text)
	//: a zero or negative step would never advance, and a non-numeric one is
	//: not a step at all — both are refused identically.
	if convErr != nil || stride <= 0 {
		//: name the requirement.
		return 0, rejectExpression("step must be a positive number", item, fs.label)
	}
	//: a usable stride.
	return stride, nil
}
