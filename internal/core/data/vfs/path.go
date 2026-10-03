// Package vfs — the path grammar every implementation shares.
package vfs

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rootName is the one path fs.ValidPath accepts that a writer must not act on:
// the tree itself.
const rootName string = "."

// ValidatePath reports whether name is a path this domain will resolve, and
// returns [InvalidPath] when it is not.
//
// The rule is fs.ValidPath and nothing else, on purpose. io/fs already imposes
// it on every reader, so a second, subtly different grammar for writers would
// mean a name that can be written and not read back — and the SDK would have
// invented that discrepancy itself. Concretely it rejects the empty string, a
// leading or trailing slash, a doubled slash, a Windows-style backslash path,
// and any "." or ".." element.
//
// This is a LEXICAL guarantee. It closes "../../etc/passwd" and it does not
// close a symbolic link that leaves the tree — see [PathEscaped], which is the
// implementation's half of the same promise.
//
// One result is worth stating because it surprises people: the separator here
// is "/" and nothing else, so `..\..\etc\passwd` is ACCEPTED — it is one legal,
// if peculiar, POSIX filename, and a file written under it lands inside the
// root rather than above it. Rejecting backslashes would make a legal filename
// unwritable to buy a confinement the grammar already provides.
func ValidatePath(name string) error {
	//: one rule, the stdlib's, applied to readers and writers alike.
	if !fs.ValidPath(name) {
		//: the offending name travels as a log-only field, never in Public.
		return errs.Wrap(InvalidPath, errs.WrapParams{}, errs.String("path", name))
	}
	//: lexically confined.
	return nil
}

// ValidateWritePath is [ValidatePath] plus the one refusal a writer needs: the
// root itself is not a target.
//
// fs.ValidPath accepts "." because a reader legitimately opens the tree to list
// it. A writer that accepts it accepts `RemoveAll(".")`, which is a call that
// empties the entire filesystem and reads, in a diff, exactly like a
// no-op — and accepts `WriteFile(".")`, which cannot mean anything at all.
func ValidateWritePath(name string) error {
	//: the lexical grammar first, so the caller hears about a malformed name
	//: before it hears about a well-formed one that is out of scope.
	if pathErr := ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	//: the root is readable and is not writable, removable or replaceable.
	if name == rootName {
		//: InvalidPath again — a target the domain declines, not a grammar
		//: violation, but the caller's fix is the same: name something.
		return errs.Wrap(InvalidPath, errs.WrapParams{}, errs.String("path", name))
	}
	//: a nameable target inside the tree.
	return nil
}
