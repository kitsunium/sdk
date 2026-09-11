// Package validation — the kind-bound halves of the tag dialect. Each builder
// resolves the field's kind ONCE, at compile time, and returns a closure that
// reads the value through the one accessor that kind supports. A validation
// therefore performs no type dispatch at all.
package validation

import (
	"errors"
	"reflect"
	"strconv"
	"unicode/utf8"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// decimalBase is the radix every tag argument is parsed in.
const decimalBase int = 10

// oneOfSeparator splits the allowed values of a oneof rule. It is '|' rather
// than ',' because ',' already separates the rules of the tag itself, and a
// list that could be cut in half by the outer separator is a list that will be.
const oneOfSeparator string = "|"

// buildNumeric compiles min= / max= against a numeric field.
//
// The argument is read as a value of the FIELD's type — its width, not 64 bits.
// Read wider, min=200 on an int8 compiles to a floor no int8 reaches and
// refuses every value it is ever shown, while max=300 on a uint8 compiles to a
// ceiling every uint8 is already under and never fires: two constraints that
// cannot be honoured, looking like two that can (ADR 0031). A literal that
// does not fit is refused here. One that fits is rounded as Go rounds a
// constant of that type, which is also what keeps an inclusive float32 bound
// inclusive: max=0.1 admits the float32 its own literal spells.
func buildNumeric(name string, typ reflect.Type, rule, arg string, hasArg bool) (check fieldCheck, err error) {
	//: a bound with no value is a typo that would otherwise compare against 0.
	if !hasArg || arg == "" {
		//: show the shape that works.
		return nil, rejectRule(name, rule, "the rule needs a numeric argument, as in "+rule+"=10")
	}
	//: max is the upper half; min the lower.
	upper := rule == tagMax
	//: the message echoes the caller's own literal, never the value.
	message := boundVerb(upper) + " " + arg
	//: reported rule name, shared with the programmatic constraints.
	reported := boundRule(upper)
	//: dispatch on the field's kind, once.
	switch typ.Kind() {
	//: signed integers read through Value.Int().
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		//: signed comparison.
		return signedBound(name, rule, arg, typ, boundSpec{upper: upper, rule: reported, message: message})
	//: unsigned integers read through Value.Uint().
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		//: unsigned comparison.
		return unsignedBound(name, rule, arg, typ, boundSpec{upper: upper, rule: reported, message: message})
	//: floats read through Value.Float().
	case reflect.Float32, reflect.Float64:
		//: floating comparison.
		return floatBound(name, rule, arg, typ, boundSpec{upper: upper, rule: reported, message: message})
	//: a string has no ordering this rule means; the caller wanted minlen.
	case reflect.String:
		//: the single most likely mistake — name the rule that was meant.
		return nil, rejectRule(name, rule, "min and max compare numbers; use minlen or maxlen for a string length")
	//: a collection has no ordering either; the caller wanted mincount.
	case reflect.Slice, reflect.Array:
		//: the second most likely one.
		return nil, rejectRule(name, rule, "min and max compare numbers; use mincount or maxcount for an element count")
	//: anything else — refused rather than compared through an accessor it
	//: does not have.
	default:
		//: everything else has no ordering this dialect can assume.
		return nil, rejectRule(name, rule, "min and max require a numeric field; this field is a "+typ.Kind().String())
	}
}

// boundSpec groups what a bound closure needs beyond its parsed limit.
type boundSpec struct {
	upper   bool
	rule    string
	message string
}

// signedBound compiles a bound over a signed integer field of type typ.
func signedBound(name, rule, arg string, typ reflect.Type, spec boundSpec) (check fieldCheck, err error) {
	//: parse at compile time, at the field's own width; a bad literal never
	//: reaches a request.
	limit, parseErr := strconv.ParseInt(arg, decimalBase, typ.Bits())
	//: refuse rather than default to zero.
	if parseErr != nil {
		//: name what the argument must be.
		return nil, rejectRule(name, rule, literalClause(parseErr, "the argument", "is not a whole number", typ))
	}
	//: one comparison per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: inclusive on both sides.
		if withinBound(fieldValue.Int() >= limit, fieldValue.Int() <= limit, spec.upper) {
			//: accepted.
			return nil
		}
		//: outside the bound.
		return one(path, spec.rule, spec.message, CodeOutOfRange)
	}, nil
}

// unsignedBound compiles a bound over an unsigned integer field of type typ.
func unsignedBound(name, rule, arg string, typ reflect.Type, spec boundSpec) (check fieldCheck, err error) {
	//: parse at compile time, at the field's own width.
	limit, parseErr := strconv.ParseUint(arg, decimalBase, typ.Bits())
	//: a negative bound on an unsigned field is a design mistake, not a bound.
	if parseErr != nil {
		//: name what the argument must be.
		return nil, rejectRule(name, rule, literalClause(parseErr, "the argument", "is not a non-negative whole number", typ))
	}
	//: one comparison per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: inclusive on both sides.
		if withinBound(fieldValue.Uint() >= limit, fieldValue.Uint() <= limit, spec.upper) {
			//: accepted.
			return nil
		}
		//: outside the bound.
		return one(path, spec.rule, spec.message, CodeOutOfRange)
	}, nil
}

// floatBound compiles a bound over a floating-point field of type typ.
func floatBound(name, rule, arg string, typ reflect.Type, spec boundSpec) (check fieldCheck, err error) {
	//: parse at compile time, at the field's own width: a float32 bound is
	//: the float32 its literal spells, which is the value the field compares.
	limit, parseErr := strconv.ParseFloat(arg, typ.Bits())
	//: refuse rather than default to zero.
	if parseErr != nil {
		//: name what the argument must be.
		return nil, rejectRule(name, rule, literalClause(parseErr, "the argument", "is not a number", typ))
	}
	//: one comparison per validation. NaN fails both comparisons and is
	//: therefore refused by either bound — which is the right answer: NaN is
	//: not within any interval.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: inclusive on both sides.
		if withinBound(fieldValue.Float() >= limit, fieldValue.Float() <= limit, spec.upper) {
			//: accepted.
			return nil
		}
		//: outside the bound.
		return one(path, spec.rule, spec.message, CodeOutOfRange)
	}, nil
}

// literalClause explains why a numeric tag literal was refused. A literal the
// field's width cannot hold is a different mistake from one that is not a
// number at all — the fix is the literal, or the field's type — so it gets its
// own clause, naming the field's KIND: the name of a defined type such as
// Level would hide the width that decided it.
func literalClause(parseErr error, subject, malformed string, typ reflect.Type) string {
	//: a number, but not one this field can hold.
	if errors.Is(parseErr, strconv.ErrRange) {
		//: e.g. "the argument is out of range for this field's kind (int8)".
		return subject + " is out of range for this field's kind (" + typ.Kind().String() + ")"
	}
	//: not a number of the expected shape at all.
	return subject + " " + malformed
}

// withinBound picks the half of the comparison the rule asked for.
func withinBound(atLeast, atMost, upper bool) bool {
	//: max compares upward.
	if upper {
		//: inclusive ceiling.
		return atMost
	}
	//: inclusive floor.
	return atLeast
}

// boundVerb renders the half of a bound message that does not carry the limit.
func boundVerb(upper bool) string {
	//: max.
	if upper {
		//: "must be at most 10".
		return "must be at most"
	}
	//: "must be at least 10".
	return "must be at least"
}

// countVerb renders the half of an element-count message that does not carry
// the limit. It is spelled out rather than derived from boundVerb because a
// message built by slicing another message breaks silently the day either is
// reworded.
func countVerb(upper bool) string {
	//: max.
	if upper {
		//: "must contain at most 10 items".
		return "must contain at most"
	}
	//: "must contain at least 1 items".
	return "must contain at least"
}

// boundRule maps a bound to the rule name it reports, which is the same name
// the programmatic AtLeast / AtMost constraints report — the tag spelling, so
// one rule reads the same whichever front end produced it.
func boundRule(upper bool) string {
	//: max.
	if upper {
		//: reported as "max".
		return ruleMax
	}
	//: reported as "min".
	return ruleMin
}

// buildLength compiles minlen= / maxlen= against a string field.
func buildLength(name string, typ reflect.Type, rule, arg string, hasArg bool) (check fieldCheck, err error) {
	//: a size rule needs a size.
	if !hasArg || arg == "" {
		//: show the shape that works.
		return nil, rejectRule(name, rule, "the rule needs a whole-number argument, as in "+rule+"=3")
	}
	//: the dialect measures RUNES, and only a string has them.
	if typ.Kind() != reflect.String {
		//: name the rule that was meant.
		return nil, rejectRule(name, rule, "minlen and maxlen measure a string in runes; use mincount or maxcount for an element count")
	}
	//: parse and range-check at compile time.
	limit, limitErr := parseSize(name, rule, arg)
	//: refuse rather than default.
	if limitErr != nil {
		//: hand the typed refusal back.
		return nil, limitErr
	}
	//: max is the upper half.
	upper := rule == tagMaxLen
	//: message echoes the bound only.
	message := boundVerb(upper) + " " + arg + " characters long"
	//: one UTF-8 count per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: RuneCountInString walks the bytes once and allocates nothing.
		size := utf8.RuneCountInString(fieldValue.String())
		//: inclusive on both sides.
		if withinBound(size >= limit, size <= limit, upper) {
			//: accepted.
			return nil
		}
		//: outside the bound.
		return one(path, ruleLength, message, CodeLengthOutOfRange)
	}, nil
}

// buildCount compiles mincount= / maxcount= against a slice or array field.
func buildCount(name string, typ reflect.Type, rule, arg string, hasArg bool) (check fieldCheck, err error) {
	//: a size rule needs a size.
	if !hasArg || arg == "" {
		//: show the shape that works.
		return nil, rejectRule(name, rule, "the rule needs a whole-number argument, as in "+rule+"=1")
	}
	//: only a slice or an array has elements to count.
	if typ.Kind() != reflect.Slice && typ.Kind() != reflect.Array {
		//: name the rule that was meant.
		return nil, rejectRule(name, rule, "mincount and maxcount count elements; use minlen or maxlen for a string length")
	}
	//: parse and range-check at compile time.
	limit, limitErr := parseSize(name, rule, arg)
	//: refuse rather than default.
	if limitErr != nil {
		//: hand the typed refusal back.
		return nil, limitErr
	}
	//: max is the upper half.
	upper := rule == tagMaxCount
	//: message echoes the bound only.
	message := countVerb(upper) + " " + arg + " items"
	//: one len() per validation.
	return func(path string, fieldValue reflect.Value) corevalidation.ReportValue {
		//: a nil slice has length 0 — it holds nothing, which is the honest
		//: answer to "how many".
		size := fieldValue.Len()
		//: inclusive on both sides.
		if withinBound(size >= limit, size <= limit, upper) {
			//: accepted.
			return nil
		}
		//: outside the bound.
		return one(path, ruleCount, message, CodeLengthOutOfRange)
	}, nil
}

// parseSize parses a size argument and refuses a negative one: every size is
// non-negative, so a negative bound is a typo rather than a looser rule.
func parseSize(name, rule, arg string) (size int, err error) {
	//: parse at compile time.
	parsed, parseErr := strconv.Atoi(arg)
	//: refuse a non-numeric argument.
	if parseErr != nil {
		//: name what the argument must be.
		return 0, rejectRule(name, rule, "the argument is not a whole number")
	}
	//: refuse a negative size.
	if parsed < 0 {
		//: name the clause.
		return 0, rejectRule(name, rule, "the size is negative")
	}
	//: a legal size.
	return parsed, nil
}
