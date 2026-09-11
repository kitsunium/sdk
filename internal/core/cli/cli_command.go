// Package cli — the declared command: a name, what it says about itself, its
// flags, and EITHER what it does OR what it contains.
package cli

// CommandValue declares one command. The tree it forms is the whole
// declaration: the engine walks it to resolve an argument vector, and the help
// renderer walks the SAME tree to render the text, so a command that exists
// and a command that is documented are one fact rather than two.
//
// A command is a LEAF or a GROUP, never both:
//
//   - a leaf has [CommandValue.Run] and no [CommandValue.Commands];
//   - a group has [CommandValue.Commands] and no Run.
//
// Both other combinations are refused at construction. Neither Run nor
// Commands is an inert command — a name an operator can type that does
// nothing, which is exactly the zero value ADR 0031 refuses. Both is worse: it
// makes `tool db migrate` mean "run db with the argument migrate" until
// somebody adds a child named migrate, at which point the same command line
// silently means something else. A declaration whose meaning changes when a
// SIBLING is added is not a declaration.
//
// CommandValue is a published concrete shape (pkg/v1/cli.Command aliases it),
// so ADR 0040 applies: it may still change while the module is v0, said out
// loud, and not after v1.
type CommandValue struct {
	// Name is the single token an operator types to reach this command. It is
	// compared by exact byte equality — there is deliberately no abbreviation
	// matching and no case folding, because both make the meaning of an
	// existing command line depend on which siblings exist today.
	//
	// It must be non-empty, must contain no space, and must not begin with
	// "-": a name beginning with "-" could never be typed, since the parent's
	// flag parse would consume it as a flag and stop before it.
	Name string
	// Summary is the one-line description listed beside this command in its
	// parent's help. It is required for a child, because a command listed with
	// no summary is a name an operator has to guess at.
	Summary string
	// Description is the optional long form shown in this command's OWN help,
	// under the usage line. It may span several lines; it is written verbatim.
	Description string
	// Flags declares this command's flags. A nil Binder is a working
	// declaration and means "this command takes no flags" — unlike a nil Run,
	// which is a real absence, a command with no flags is an ordinary command.
	Flags Binder
	// Run is what a LEAF does. Exactly one of Run and Commands is set.
	Run Action
	// Commands are a GROUP's children, in the order the help lists them. Two
	// children may not share a Name.
	Commands []CommandValue
}

// IsGroup reports whether this command dispatches to children rather than
// running an [Action]. It reads the declaration rather than a flag set at
// construction, so it answers the same on a CommandValue the caller has just
// written and on one the engine has already validated.
func (c CommandValue) IsGroup() bool {
	//: the constructor has already refused every other combination, so the
	//: presence of children is the whole question.
	return len(c.Commands) > 0
}
