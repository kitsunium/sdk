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

// maxForeignCauses bounds how many foreign messages one error may contribute.
//
// The tree is this package's own and is two deep, so the bound is not reached
// in practice; it is here because diagnose runs on the failure path, where a
// cycle somebody else introduced must not become an unbounded string.
const maxForeignCauses int = 8

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
	if causes := foreignCauses(err); len(causes) > 0 {
		//: one line, whatever shape the tree was: a terminal line and a log
		//: line are both worse for a newline in the middle.
		parts = append(parts, "cause="+strings.Join(causes, "; "))
	}
	//: single space between parts keeps it greppable and one line long.
	return strings.Join(parts, " ")
}

// foreignCauses returns the messages in err's tree that this SDK did not
// write — the operating system's, the decoder's, the transport's — in the
// order they appear.
//
// It returns nothing when the whole tree is ours. A refusal this package
// DECIDED has no foreign cause, and rendering its sentinel back would repeat,
// word for word, the sentence the reader has just been shown.
//
// It walks errors.Join as well as the single-unwrap chain. Stopping at the
// join was the first shape of this and it was wrong in the one case that
// composes: finalizeReplacement joins the rename's os.ErrPermission with the
// elevation's own error, so a reader got "permission denied" followed by our
// own ELEVATION_FAILED sentence — the sentence they had just read — and never
// the process result underneath it. When `sudo -n mv` fails silently, that
// result is the only thing there is.
func foreignCauses(err error) []string {
	found := make([]string, 0, maxForeignCauses)
	collectForeign(err, &found)
	//: the caller decides how to render them; this only decides which.
	return found
}

// collectForeign appends err's foreign messages to found, depth first.
func collectForeign(err error, found *[]string) {
	//: nothing here, or the bound is spent.
	if err == nil || len(*found) >= maxForeignCauses {
		return
	}
	//: a multi-unwrapper is a container: errors.Join has no message of its
	//: own beyond its members, so descend rather than quoting the whole of it
	//: and folding our own sentences back in.
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		//: each member is examined on its own terms.
		for _, member := range multi.Unwrap() {
			collectForeign(member, found)
		}
		return
	}
	//: one of ours: its public sentence is already on the line above, so
	//: descend past it to whatever it was told about.
	if _, ours := err.(*errs.Error); ours {
		//: the *errs.Error source, which may be nil.
		collectForeign(errors.Unwrap(err), found)
		return
	}
	//: something we did not write. Its own Error() already carries whatever
	//: it wraps, so this is a leaf for our purposes.
	*found = append(*found, err.Error())
}
