// Package validation — the set-membership constraint.
package validation

import (
	"fmt"
	"strings"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// maxListedValues caps how many allowed values a OneOf message enumerates. The
// allowed set is the caller's own schema, so echoing it is safe and is what
// makes the message actionable — but a 500-entry enum in a message helps
// nobody.
const maxListedValues int = 12

// listSeparator joins the enumerated allowed values.
const listSeparator string = ", "

// OneOf refuses a value absent from allowed.
//
// It REFUSES an empty allowed set at construction. An empty set is satisfied
// by nothing, so the alternative is a constraint that rejects every value it
// is ever shown while looking like a working rule — the ADR 0031 failure mode
// in its rejecting form. "No allowed values" is not a permissive rule, it is a
// forgotten argument.
//
// Duplicates in allowed are accepted and collapse: repeating a value does not
// change the set it denotes, and refusing a duplicate would turn a harmless
// copy-paste in a generated list into a start-up failure.
func OneOf[T comparable](allowed ...T) (constraint corevalidation.Constraint[T], err error) {
	//: an empty set can never be satisfied — refuse it here.
	if len(allowed) == 0 {
		//: name the clause so the fix is obvious from the error alone.
		return nil, rejectConstraint(ruleOneOf, "the allowed set is empty")
	}
	//: build the lookup once, at construction: membership is then one map
	//: probe per validation instead of a linear scan.
	set := make(map[T]struct{}, len(allowed))
	//: duplicates collapse naturally.
	for _, value := range allowed {
		//: presence is all the map records.
		set[value] = struct{}{}
	}
	//: message built once, echoing only the schema.
	message := oneOfMessage(allowed)
	//: one map probe per validation.
	return func(path string, value T) corevalidation.ReportValue {
		//: membership.
		if _, ok := set[value]; ok {
			//: accepted.
			return nil
		}
		//: outside the set.
		return one(path, ruleOneOf, message, CodeNotInSet)
	}, nil
}

// oneOfMessage enumerates the allowed values, clipping a long list.
func oneOfMessage[T comparable](allowed []T) string {
	//: how many entries the message will actually name.
	shown := min(len(allowed), maxListedValues)
	//: exact capacity.
	rendered := make([]string, 0, shown)
	//: render in the order the caller declared them — a sorted list would
	//: reorder an enum whose declaration order is itself documentation.
	for _, value := range allowed[:shown] {
		//: %v is the generic rendering; the values are schema, not input.
		rendered = append(rendered, fmt.Sprintf("%v", value))
	}
	//: the whole set fits.
	if shown == len(allowed) {
		//: "must be one of: red, green, blue".
		return "must be one of: " + strings.Join(rendered, listSeparator)
	}
	//: clipped — say so, and say how many were left out.
	return fmt.Sprintf("must be one of: %s (and %d more)",
		strings.Join(rendered, listSeparator), len(allowed)-shown)
}
