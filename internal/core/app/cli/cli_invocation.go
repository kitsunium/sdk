package cli

import (
	"flag"
)

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
