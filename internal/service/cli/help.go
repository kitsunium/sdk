// Package cli — the help, generated from the declarations and from nowhere
// else.
package cli

import (
	"flag"
	"io"
	"strings"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
)

// helpColumnGap is the minimum run of spaces between a listed sub-command name
// and its summary, so the summaries line up without a tab stop the terminal
// may or may not honour.
const helpColumnGap int = 2

// helpIndent is the two-space indent every list entry and usage line carries.
const helpIndent string = "  "

// writeHelp renders the help for one command and writes it in ONE call.
//
// Everything in it is read from the declaration: the summary and description
// come from the [corecli.CommandValue], the sub-command list from its
// Commands, and the flag table from package flag's own PrintDefaults over the
// set the Binder just filled. Nothing is written twice and nothing is written
// by hand, which is CLAUDE.md rule 11 applied to the executable: a help text
// maintained beside the declarations is a help text that eventually describes
// a flag that was renamed, and a help that lies is worse than no help — an
// operator acts on it.
//
// It is assembled into a buffer and flushed once. A help page interleaved with
// another goroutine's output is a page nobody can read, and a partial one is
// worse: it stops mid-list, which reads as "these are all the commands".
func (e *executor) writeHelp(cmd corecli.CommandValue, path []string, set *flag.FlagSet) {
	var out strings.Builder
	joined := strings.Join(path, " ")
	//: the one-liner, when the command declares one. The root may not.
	if cmd.Summary != "" {
		out.WriteString(cmd.Summary)
		out.WriteString("\n\n")
	}
	//: the long form, verbatim: it is prose and the SDK does not reflow it.
	if cmd.Description != "" {
		out.WriteString(strings.TrimRight(cmd.Description, "\n"))
		out.WriteString("\n\n")
	}
	writeUsage(&out, cmd, joined, set)
	writeCommands(&out, cmd)
	writeFlags(&out, set)
	//: a group's help ends by naming the one thing that answers the question
	//: it did not: what does THAT sub-command do.
	if cmd.IsGroup() {
		out.WriteString("Use \"")
		out.WriteString(joined)
		out.WriteString(" <command> -h\" for more information about a command.\n")
	}
	//: one Write. A reader never sees half a help page.
	_, _ = e.errOut.Write([]byte(out.String()))
}

// writeUsage renders the single usage line the declaration implies.
func writeUsage(out *strings.Builder, cmd corecli.CommandValue, joined string, set *flag.FlagSet) {
	out.WriteString("Usage:\n")
	out.WriteString(helpIndent)
	out.WriteString(joined)
	//: the flag placeholder appears only when this command actually has
	//: flags, so a usage line never advertises a section that is not below it.
	if hasFlags(set) {
		out.WriteString(" [flags]")
	}
	//: a group takes a verb; a leaf takes whatever its Action reads.
	if cmd.IsGroup() {
		out.WriteString(" <command>")
	} else {
		//: a leaf must not invite a sub-command it does not have.
		out.WriteString(" [arguments]")
	}
	out.WriteString("\n\n")
}

// writeCommands renders a group's children, in declaration order.
func writeCommands(out *strings.Builder, cmd corecli.CommandValue) {
	//: a leaf has none, and an empty section reads as "no commands exist".
	if !cmd.IsGroup() {
		//: nothing to list; the usage line already said so.
		return
	}
	out.WriteString("Commands:\n")
	width := 0
	//: one pass for the column, so every summary starts at the same offset.
	for _, child := range cmd.Commands {
		//: the widest name decides where every summary begins.
		if len(child.Name) > width {
			width = len(child.Name)
		}
	}
	//: declaration order, not map order: a help that re-orders cannot be diffed.
	for _, child := range cmd.Commands {
		out.WriteString(helpIndent)
		out.WriteString(child.Name)
		//: pad with spaces rather than a tab: a tab stop is the terminal's
		//: opinion and it differs from the one the author was looking at.
		out.WriteString(strings.Repeat(" ", width-len(child.Name)+helpColumnGap))
		out.WriteString(child.Summary)
		out.WriteString("\n")
	}
	out.WriteString("\n")
}

// writeFlags renders package flag's own defaults table.
//
// PrintDefaults is used rather than reimplemented: it already knows how to
// render a flag.Value's zero, how to pull the type name out of the usage
// string's backquotes, and how to fold a multi-line usage. Reimplementing it
// would be a second renderer that disagrees with the stdlib the day a caller
// uses a stdlib idiom this domain had not heard of.
//
// The set's output is redirected at this exact moment and nowhere else: for
// the whole of Parse it is io.Discard, so flag never reports a failure the
// engine is about to report itself, and so nothing this domain writes can
// reach os.Stdout by way of a writer flag chose (ADR 0030).
func writeFlags(out *strings.Builder, set *flag.FlagSet) {
	//: no flags, no section — see writeCommands for the same rule.
	if !hasFlags(set) {
		//: an empty Flags header would advertise options that do not exist.
		return
	}
	out.WriteString("Flags:\n")
	set.SetOutput(out)
	set.PrintDefaults()
	//: back to silent, so a later Parse on this set cannot speak either.
	set.SetOutput(io.Discard)
	out.WriteString("\n")
}
