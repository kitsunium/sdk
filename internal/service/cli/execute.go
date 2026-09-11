// Package cli — resolution and dispatch: the loop that turns an argument
// vector into one [corecli.InvocationValue] and one [corecli.Action] call.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// typicalDepth is the initial capacity of the per-invocation flag-set slice:
// a root plus one sub-command covers most tools, and a deeper tree costs one
// append. It is a sizing hint and never a limit — the resolution loop has no
// depth bound at all.
const typicalDepth int = 2

// Execute resolves args against the tree and runs the command it names.
//
// The loop is the whole algorithm, and it borrows package flag's own rule
// rather than inventing one: a FlagSet stops at the first argument that is not
// a flag. That token is therefore unambiguously the name of a sub-command, and
// `tool --verbose db migrate --dry-run` needs no lookahead, no two-pass parse
// and no re-ordering — --verbose belongs to the group that declared it and
// --dry-run to the leaf that declared it, because each set stopped where the
// next command began.
//
// Three outcomes, and their exit statuses are the point of the domain:
//
//   - the command ran: its own error, VERBATIM, or nil;
//   - help was requested: the help is written and the result is nil, because
//     asking a question is not a failure;
//   - the command line was wrong: the help is written AND a typed EX_USAGE
//     error is returned.
//
// Nothing in this path calls os.Exit.
func (e *executor) Execute(ctx context.Context, args []string) error {
	current := e.root
	path := []string{current.Name}
	sets := make([]*flag.FlagSet, 0, typicalDepth)
	remaining := args
	//: unbounded: the loop ends at a leaf or at a refusal, never at a depth.
	for {
		set := newFlagSet(strings.Join(path, " "))
		//: a nil Binder is a working declaration: this command takes no flags.
		if current.Flags != nil {
			current.Flags(set)
		}
		//: -h is not a parse failure and must not become one.
		if err := set.Parse(remaining); err != nil {
			//: reportParse decides which of the two this was.
			return e.reportParse(current, path, set, err)
		}
		sets = append(sets, set)
		rest := set.Args()
		//: a leaf is the end of the walk: everything left is positional.
		if !current.IsGroup() {
			//: the only place an Action is ever called.
			return e.dispatch(ctx, current, path, sets, rest)
		}
		child, err := e.resolveChild(current, path, set, rest)
		if err != nil {
			//: MISSING_COMMAND or UNKNOWN_COMMAND; the help is already written.
			return err
		}
		current = child
		path = append(path, child.Name)
		remaining = rest[1:]
	}
}

// reportParse turns package flag's Parse outcome into this domain's verdict.
func (e *executor) reportParse(cmd corecli.CommandValue, path []string, set *flag.FlagSet, cause error) error {
	joined := strings.Join(path, " ")
	//: -h / -help with no such flag defined. flag signals it as an error
	//: value, which it is not: the operator asked a question and got an
	//: answer, so the help goes out and the status is 0.
	if errors.Is(cause, flag.ErrHelp) {
		//: asking a question is not a failure, so a help the stream took is
		//: status 0 — and one it did not is an answer nobody received.
		if werr := e.writeHelp(cmd, path, set); werr != nil {
			//: the writer's own error stays matchable beneath the verdict.
			return kerrs.Wrap(werr, helpWriteWrap, kerrs.String("command", joined))
		}
		//: the answer was delivered.
		return nil
	}
	//: a genuine usage error: the help is the actionable half and the SDK
	//: writes it; the error is the caller's to render, and it stays the
	//: verdict even if the help could not be written.
	werr := e.writeHelp(cmd, path, set)
	//: flag's own text quotes the operator's value, so it travels as a field
	//: and never as Public.
	return kerrs.Wrap(InvalidFlags, kerrs.WrapParams{}, withHelpFailure(werr,
		kerrs.String("command", joined), kerrs.String("flag_error", cause.Error()))...)
}

// withHelpFailure returns fields, plus one naming the stream's error when the
// help that should have accompanied a usage verdict was not delivered. The
// verdict stays the usage error — the operator's mistake earned it — and the
// missing page stays visible to whoever reads that error.
func withHelpFailure(werr error, fields ...kerrs.FieldValue) []kerrs.FieldValue {
	//: a delivered help adds nothing to the verdict.
	if werr == nil {
		//: the verdict's own fields, untouched.
		return fields
	}
	//: the stream's text names a descriptor or a path, never the operator's
	//: input, and a field is log-only.
	return append(fields, kerrs.String("help_write_error", werr.Error()))
}

// resolveChild picks the sub-command rest names, or reports why it cannot.
func (e *executor) resolveChild(
	cmd corecli.CommandValue, path []string, set *flag.FlagSet, rest []string,
) (child corecli.CommandValue, err error) {
	joined := strings.Join(path, " ")
	//: a group declares no action of its own, so an empty rest is an
	//: incomplete command line and never a successful no-op.
	if len(rest) == 0 {
		//: the help is secondary here; MissingCommand stays the verdict.
		werr := e.writeHelp(cmd, path, set)
		//: an incomplete command line, never a successful no-op.
		return corecli.CommandValue{}, kerrs.Wrap(MissingCommand, kerrs.WrapParams{},
			withHelpFailure(werr, kerrs.String("command", joined))...)
	}
	name := rest[0]
	//: a linear scan over the declared order, which the benchmarks show is
	//: invisible beside one flag.NewFlagSet — and it keeps the order the help
	//: lists in, which a lookup map would lose.
	for _, candidate := range cmd.Commands {
		//: exact byte equality. No prefix matching, no case folding: both make
		//: what an existing command line MEANS depend on which siblings exist
		//: in today's release.
		if candidate.Name == name {
			//: the resolver stops at the first match; New refused duplicates.
			return candidate, nil
		}
	}
	//: the help that just went out lists every name that would have worked,
	//: which is why this domain ships no edit-distance suggester. Secondary
	//: here too: UnknownCommand stays the verdict.
	werr := e.writeHelp(cmd, path, set)
	//: the mistyped token is echoed in a field, never in Public.
	return corecli.CommandValue{}, kerrs.Wrap(UnknownCommand, kerrs.WrapParams{},
		withHelpFailure(werr, kerrs.String("command", joined), kerrs.String("token", name))...)
}

// dispatch assembles the invocation and runs the leaf's Action under a guard.
func (e *executor) dispatch(
	ctx context.Context, cmd corecli.CommandValue, path []string, sets []*flag.FlagSet, rest []string,
) error {
	//: both slices are cloned: Execute's own path keeps growing through
	//: append, and an Action that outlives the call must not observe a backing
	//: array the engine still owns.
	invocation := corecli.InvocationValue{
		Path:   slices.Clone(path),
		Args:   slices.Clip(slices.Clone(rest)),
		Flags:  slices.Clone(sets),
		Output: e.out,
	}
	//: the only call site of an Action, and the only recovered panic.
	return guard(ctx, cmd.Run, invocation)
}

// guard runs one Action and converts a panic into a typed failure.
//
// The Action's own error is returned VERBATIM and is deliberately not wrapped.
// Wrapping would buy nothing and could cost two things: origin-wins
// (CLAUDE.md rule 6) already preserves an *errs.Error's code and exit status,
// so there is nothing to add; and a plain stdlib error wrapped with empty
// WrapParams would come back as INVALID_WRAP_PARAMS, relabelling a command
// failure as an SDK defect. Verbatim also keeps the caller's own errors.Is
// working, which is the same rule internal/service/lifecycle applies to a
// component's error.
func guard(ctx context.Context, run corecli.Action, invocation corecli.InvocationValue) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path — leave err exactly as the Action returned it.
		if value == nil {
			//: verbatim, so the caller's own errors.Is keeps working.
			return
		}
		//: the recovered value travels as a FIELD, never as the wrap origin,
		//: so a panic carrying an *errs.Error cannot hijack COMMAND_PANICKED.
		//: The stack is captured HERE, inside the deferred function, so it is
		//: the stack of the goroutine that actually failed.
		err = kerrs.Wrap(CommandPanicked, kerrs.WrapParams{},
			kerrs.String("command", strings.Join(invocation.Path, " ")),
			kerrs.String("panic", fmt.Sprint(value)),
			kerrs.String("stack", string(debug.Stack())))
	}()
	//: the Action gets the context Execute was called with, untouched: a
	//: deadline is the caller's to set and this domain adds none of its own.
	return run(ctx, invocation)
}
