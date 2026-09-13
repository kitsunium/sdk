// Package selfupdate — the half of an error that never goes on a wire, made
// available to the one reader who is entitled to all of it.
//
// errs.Error.Error() renders "[<code> <REASON>] <public>" and deliberately
// nothing else: no private detail, no fields, and not one word from the cause.
// That is right for anything crossing a boundary, and it is exactly wrong for
// the person who just typed `upgrade` and is owed "no space left on device".
//
// So the split is not "throw the detail away", it is "send it somewhere else".
// This file is that somewhere else.
package selfupdate

import (
	"errors"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// diagnose renders what err carries beyond its public sentence: every field,
// then the words of the first cause this SDK did not write.
//
// It returns the empty string for an error with neither, which is the signal a
// caller uses to print nothing rather than an empty line.
func diagnose(err error) string {
	fields := errs.FieldsOf(err)
	parts := make([]string, 0, len(fields)+1)
	//: fields first: they are this package's own vocabulary and the shortest
	//: route to which step of which stage refused.
	for _, field := range fields {
		//: one key=value per field, in the order the call site listed them.
		parts = append(parts, field.Key()+"="+field.StringValue())
	}
	//: then whatever the outside world said, if anything did.
	if cause := foreignCause(err); cause != nil {
		//: errors.Join renders its members one per line; a terminal line and
		//: a log line are both better served by one line.
		parts = append(parts, "cause="+strings.ReplaceAll(cause.Error(), "\n", "; "))
	}
	//: single space between parts keeps it greppable and one line long.
	return strings.Join(parts, " ")
}

// foreignCause returns the first error in err's chain that this SDK did not
// build — the operating system's, the decoder's, the transport's.
//
// It returns nil when the whole chain is ours. A refusal this package DECIDED
// has no foreign cause, and rendering its sentinel back would repeat, word for
// word, the sentence the reader has just been shown.
func foreignCause(err error) error {
	//: walk the single-unwrap chain; errors.Join stops it, and that is
	//: deliberate — a join's own Error() already names both members.
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		//: ours: keep walking, there may be a foreign error underneath.
		if _, ours := cur.(*errs.Error); ours {
			continue
		}
		//: the first thing we did not write is the one worth quoting.
		return cur
	}
	//: every link was ours.
	return nil
}
