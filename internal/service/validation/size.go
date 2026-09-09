// Package validation — the size constraints: how long a string is, and how
// many elements a slice holds. Two rules rather than one, because "at most 30
// characters" and "at most 30 entries" are different requirements with
// different fixes, and Go's type system cannot spell len over both anyway.
package validation

import (
	"fmt"
	"unicode/utf8"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// Unbounded is the upper bound that means "no ceiling". It is spelled rather
// than implied by a negative number so a reader of Length(8, Unbounded) does
// not have to know the convention to understand the line.
const Unbounded int = -1

// unboundedSuffix renders the upper half of an open-ended interval.
const unboundedSuffix string = "or more"

// Length refuses a string whose RUNE count falls outside [min, max].
//
// It counts runes, not bytes: "at most 30 characters" is what a form says, and
// counting bytes would refuse a perfectly ordinary accented name at 16
// characters. It does not count grapheme clusters — an emoji built from a
// zero-width-joiner sequence counts as several runes — because clustering
// needs a Unicode table the SDK does not carry, and claiming otherwise would
// be worse than saying so.
//
// max may be [Unbounded]. A negative min, or min > max when max is bounded, is
// refused at construction (ADR 0031).
func Length(minRunes, maxRunes int) (constraint corevalidation.Constraint[string], err error) {
	//: shared bound validation with Count — the two rules differ in what they
	//: measure, never in what a legal interval is.
	if err := checkSizeBounds(ruleLength, minRunes, maxRunes); err != nil {
		//: refused at construction; nothing is ever validated by this call.
		return nil, err
	}
	//: message built once, echoing only the bounds.
	message := sizeMessage("must be", "characters long", minRunes, maxRunes)
	//: one UTF-8 count per validation.
	return func(path, value string) corevalidation.ReportValue {
		//: RuneCountInString walks the bytes once and allocates nothing.
		if withinSize(utf8.RuneCountInString(value), minRunes, maxRunes) {
			//: accepted.
			return nil
		}
		//: outside the interval.
		return one(path, ruleLength, message, CodeLengthOutOfRange)
	}, nil
}

// Count refuses a slice whose ELEMENT count falls outside [min, max]. It is
// also how presence is expressed for a slice, which [Required] cannot cover
// because a slice is not comparable: Count(1, Unbounded).
//
// max may be [Unbounded]. The bound rules are those of [Length].
func Count[E any](minLen, maxLen int) (constraint corevalidation.Constraint[[]E], err error) {
	//: same legality rules as Length.
	if err := checkSizeBounds(ruleCount, minLen, maxLen); err != nil {
		//: refused at construction.
		return nil, err
	}
	//: message built once, echoing only the bounds.
	message := sizeMessage("must contain", "items", minLen, maxLen)
	//: one len() per validation.
	return func(path string, value []E) corevalidation.ReportValue {
		//: a nil slice has length 0, which is the honest answer: it holds
		//: nothing. Count(1, …) therefore refuses nil and empty alike.
		if withinSize(len(value), minLen, maxLen) {
			//: accepted.
			return nil
		}
		//: outside the interval.
		return one(path, ruleCount, message, CodeLengthOutOfRange)
	}, nil
}

// checkSizeBounds refuses a size interval nothing could satisfy.
func checkSizeBounds(rule string, low, high int) error {
	//: a negative floor is not a looser rule, it is a typo — every size is >= 0.
	if low < 0 {
		//: name the clause.
		return rejectConstraint(rule, "the minimum size is negative")
	}
	//: Unbounded is the only legal negative ceiling.
	if high < 0 && high != Unbounded {
		//: name the clause.
		return rejectConstraint(rule, "the maximum size is negative and is not Unbounded")
	}
	//: an inverted interval is satisfied by nothing — the ADR 0031 trap.
	if high != Unbounded && low > high {
		//: name the clause.
		return rejectConstraint(rule, "the minimum size is greater than the maximum")
	}
	//: a legal interval.
	return nil
}

// withinSize reports whether size sits in [low, high], with Unbounded meaning
// no ceiling.
func withinSize(size, low, high int) bool {
	//: below the floor is always a refusal.
	if size < low {
		//: too small.
		return false
	}
	//: no ceiling to breach.
	if high == Unbounded {
		//: accepted.
		return true
	}
	//: inclusive ceiling.
	return size <= high
}

// sizeMessage renders a bounds message, collapsing the open-ended form so
// "must be 8 or more characters long" does not read as "8 to -1".
func sizeMessage(verb, noun string, low, high int) string {
	//: open-ended interval.
	if high == Unbounded {
		//: "must be 8 or more characters long".
		return fmt.Sprintf("%s %d %s %s", verb, low, unboundedSuffix, noun)
	}
	//: closed interval — "must be between 3 and 30 characters long".
	return fmt.Sprintf("%s between %d and %d %s", verb, low, high, noun)
}
