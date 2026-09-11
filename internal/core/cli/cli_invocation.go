// Package cli — what a resolved command line looks like by the time an
// [Action] sees it.
package cli

import (
	"flag"
	"io"
)

// InvocationValue is one resolved command line: which command was reached,
// what was left over after its flags were parsed, the sets that parsed them,
// and the writer the command may print to.
//
// InvocationValue is a published concrete shape (pkg/v1/cli.Invocation aliases
// it), so ADR 0040 applies: it may still change while the module is v0, said
// out loud, and not after v1.
type InvocationValue struct {
	// Path is the resolved command path, root first — {"tool", "db",
	// "migrate"}. It is what the help and every error message name the command
	// by, so a message and a shell prompt spell the same thing.
	Path []string
	// Args are the positional arguments left after the LEAF's flags were
	// parsed: flag.FlagSet.Args of the last set in Flags.
	//
	// It is nil when there were none, which is Go's ordinary spelling — len
	// and range answer identically for a nil and an empty slice, and manufacturing
	// an empty one would only hide that distinction from a caller who does not
	// need it.
	Args []string
	// Flags are the flag sets that parsed this invocation, root first, one per
	// element of Path; the leaf's own set is the last. A group's flags are
	// parsed by the group's set before its child's name is even read, which is
	// why they are separate sets and not one merged one.
	//
	// The stdlib's set is handed over unchanged rather than wrapped, so
	// Lookup, Visit, VisitAll, NFlag and Arg all work exactly as their
	// documentation says. Calling Parse on one again is not defended against:
	// the engine has already parsed it, and re-parsing it is a caller's bug
	// the SDK cannot distinguish from a caller's intent.
	Flags []*flag.FlagSet
	// Output is the writer this command may print to. It is never nil.
	//
	// It is NOT os.Stdout unless the caller said so: ADR 0030 makes stdout a
	// protocol channel that no SDK default may claim, so the zero
	// configuration resolves this to os.Stderr and a tool whose output IS a
	// protocol sets it explicitly, in main, on one visible line. An Action is
	// handed the writer precisely so it never has to reach for a stream
	// itself.
	Output io.Writer
}

// Leaf returns the flag set of the command that is actually running — the last
// element of [InvocationValue.Flags] — or nil when the command declared no
// flags anywhere on its path.
//
// It exists so a caller reading its own flags does not index a slice whose
// length is a property of how deep the command happens to sit in the tree.
func (i InvocationValue) Leaf() *flag.FlagSet {
	//: an invocation with no sets at all is the no-flags-anywhere case.
	if len(i.Flags) == 0 {
		//: nothing parsed anything; there is no set to hand back.
		return nil
	}
	//: the leaf's set is always the last one the engine appended.
	return i.Flags[len(i.Flags)-1]
}
