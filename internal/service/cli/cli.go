// Package cli is the command-line engine: it validates a whole command tree
// once, resolves an argument vector against it, renders the help the tree
// implies, and returns a typed error carrying an exit status. It never ends
// the process.
package cli

import (
	"flag"
	"io"
	"slices"
	"strings"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// reservedFlagNames are the two flag names package flag answers itself. An
// undefined -h makes Parse return flag.ErrHelp, which is the ONLY signal that
// separates a help request (exit 0) from a usage error (exit 64); a command
// that binds either name takes that signal away.
var reservedFlagNames = []string{"h", "help"}

// executor is the concrete [corecli.Executor]. It holds an already-validated
// tree and two writers, and nothing that changes between invocations, so the
// ENGINE is safe for concurrent use.
//
// What is not, and cannot be made so from here, is a [corecli.Binder] writing
// into a variable the caller closed over: that memory belongs to the caller
// and every concurrent invocation writes it. The contract is stated on
// corecli.Binder and pinned by TestTheEngineHoldsNothingPerInvocation.
type executor struct {
	root   corecli.CommandValue
	out    io.Writer
	errOut io.Writer
}

// New validates a command tree and returns the [corecli.Executor] that runs it.
//
// The WHOLE tree is validated here, not the branch an invocation happens to
// take. A declaration is a wiring fact, and a wiring fault discovered by the
// operator who typed the one command nobody had tried is a fault discovered in
// production: `tool db migrate` being unreachable must fail on the first run
// of the binary, not on the first run of the migration.
//
// Every refusal is one of internal/core/cli's sentinels and carries EX_CONFIG
// (78). A [corecli.Binder] is CALLED here, once, on a throwaway set — that is
// what proves it binds no reserved name — so a Binder must be safe to call
// more than once. A Binder that panics is deliberately not recovered: it runs
// inside the caller's own main, on the line that wrote it, and a recovered
// panic there would replace a stack pointing at the bug with a sentence about
// a tree.
func New(cfg Config, root corecli.CommandValue) (runner corecli.Executor, err error) {
	//: the tree the executor walks must be the one validate saw. CommandValue
	//: is copied by value but its Commands slices are the caller's, so a caller
	//: editing them after New used to change what ran — unvalidated, and
	//: racing with any Execute in flight.
	root = cloneTree(root)
	//: the root is the only command whose Summary is optional: nothing lists
	//: it beside its siblings, because it has none.
	if refused := validate(root, nil, true); refused != nil {
		//: propagate the core sentinel verbatim — it already names the path.
		return nil, refused
	}
	//: the tree is frozen from here: the executor never mutates it, so the
	//: same *executor may be used from several goroutines.
	return &executor{
		root:   root,
		out:    cfg.resolveOutput(),
		errOut: cfg.resolveErrOutput(),
	}, nil
}

// cloneTree returns cmd with every Commands slice copied, all the way down. A
// nil slice stays nil, so a leaf is still a leaf.
func cloneTree(cmd corecli.CommandValue) corecli.CommandValue {
	//: a leaf has nothing to share with the caller.
	if cmd.Commands == nil {
		//: the value itself is already a copy.
		return cmd
	}
	children := make([]corecli.CommandValue, len(cmd.Commands))
	//: each child is cloned in turn, so no level keeps the caller's array.
	for index, child := range cmd.Commands {
		children[index] = cloneTree(child)
	}
	cmd.Commands = children
	//: the copy, owned by the executor alone.
	return cmd
}

// validate checks one command and recurses into its children. parents is the
// path ABOVE cmd, root first; isRoot suppresses only the Summary requirement.
func validate(cmd corecli.CommandValue, parents []string, isRoot bool) error {
	//: an unreachable or unspellable name; nothing below it matters.
	if err := validateName(cmd, parents); err != nil {
		//: the refusal already names what is wrong and where.
		return err
	}
	path := append(slices.Clone(parents), cmd.Name)
	joined := strings.Join(path, " ")
	//: leaf/group trichotomy and the summary requirement.
	if err := validateShape(cmd, joined, isRoot); err != nil {
		//: refused shapes are the ADR 0031 half of this constructor.
		return err
	}
	//: a Binder that claims -h or -help.
	if err := validateFlags(cmd, joined); err != nil {
		//: taking flag.ErrHelp away breaks the exit-status distinction.
		return err
	}
	//: a leaf's Commands is empty, so this loop IS the recursion's base case.
	seen := make(map[string]struct{}, len(cmd.Commands))
	//: one pass per sibling: uniqueness first, then the child's own subtree.
	for _, child := range cmd.Commands {
		//: a repeated name makes the second declaration unreachable code that
		//: looks reachable, so it is refused rather than shadowed.
		if _, dup := seen[child.Name]; dup {
			//: the refusal names the clause and the path; nothing else is guessed.
			return kerrs.Wrap(corecli.DuplicateCommand, kerrs.WrapParams{},
				kerrs.String("path", joined), kerrs.String("name", child.Name))
		}
		seen[child.Name] = struct{}{}
		//: depth is unbounded; the recursion is the whole tree walk.
		if err := validate(child, path, false); err != nil {
			//: the child's own refusal already names its full path.
			return err
		}
	}
	//: every command on this branch is reachable and runnable.
	return nil
}

// validateName refuses a name that could never be typed.
func validateName(cmd corecli.CommandValue, parents []string) error {
	under := strings.Join(parents, " ")
	//: a blank name names nothing and would render as a gap in the help.
	if cmd.Name == "" {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.InvalidCommand, kerrs.WrapParams{},
			kerrs.String("under", under), kerrs.String("clause", "name is empty"))
	}
	//: a name with a space could only be reached by quoting, and the parent's
	//: parse hands the resolver ONE token.
	if strings.ContainsAny(cmd.Name, " \t\n") {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.InvalidCommand, kerrs.WrapParams{},
			kerrs.String("under", under), kerrs.String("name", cmd.Name),
			kerrs.String("clause", "name contains whitespace"))
	}
	//: a leading '-' is unreachable by construction: the parent's flag parse
	//: consumes it as a flag and stops before the resolver ever sees it.
	if strings.HasPrefix(cmd.Name, "-") {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.InvalidCommand, kerrs.WrapParams{},
			kerrs.String("under", under), kerrs.String("name", cmd.Name),
			kerrs.String("clause", "name begins with '-' and would be parsed as a flag"))
	}
	//: the name is one token an operator can actually type.
	return nil
}

// validateShape enforces the leaf-or-group trichotomy and the summary rule.
func validateShape(cmd corecli.CommandValue, path string, isRoot bool) error {
	hasRun, hasChildren := cmd.Run != nil, len(cmd.Commands) > 0
	//: both is the declaration whose meaning changes when a sibling is added.
	if hasRun && hasChildren {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.AmbiguousCommand, kerrs.WrapParams{},
			kerrs.String("path", path), kerrs.Int("children", len(cmd.Commands)))
	}
	//: neither is the inert command ADR 0031 refuses: a name an operator can
	//: type that does nothing, and reports success while doing it.
	if !hasRun && !hasChildren {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.InvalidCommand, kerrs.WrapParams{},
			kerrs.String("path", path), kerrs.String("clause", "neither Run nor Commands"))
	}
	//: only a child is ever LISTED, so only a child needs a summary.
	if !isRoot && cmd.Summary == "" {
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.InvalidCommand, kerrs.WrapParams{},
			kerrs.String("path", path), kerrs.String("clause", "summary is empty"))
	}
	//: a leaf with an action, or a group with children, and a summary if listed.
	return nil
}

// validateFlags runs the Binder once on a throwaway set and refuses a reserved
// name. Running it is the point: a declaration cannot be inspected without
// executing it, and the alternative — discovering the collision on the first
// -h an operator types — is the same fault found later by somebody else.
func validateFlags(cmd corecli.CommandValue, path string) error {
	//: a command with no flags has nothing to probe.
	if cmd.Flags == nil {
		//: a nil Binder is a working declaration, unlike a nil Run.
		return nil
	}
	probe := newFlagSet(path)
	cmd.Flags(probe)
	//: two names, checked by Lookup rather than by reading the Binder's source.
	for _, name := range reservedFlagNames {
		//: Lookup is the whole check: flag reports an UNDEFINED -h as
		//: ErrHelp, so a defined one silently turns a help page into a
		//: successful parse.
		if probe.Lookup(name) == nil {
			continue
		}
		//: the refusal names the clause and the path; nothing else is guessed.
		return kerrs.Wrap(corecli.ReservedFlag, kerrs.WrapParams{},
			kerrs.String("path", path), kerrs.String("flag", name))
	}
	//: -h and -help are still the SDK's, so flag.ErrHelp still means help.
	return nil
}

// newFlagSet builds the one shape of flag set this domain ever creates.
//
// Three properties, each of which is a decision:
//
//   - flag.ContinueOnError, never ExitOnError and never PanicOnError. The
//     first calls os.Exit from a library; the second is os.Exit with a
//     corrupted stack on the way out. Both take the "is this process still
//     alive" decision away from main.
//   - output io.Discard. On a parse failure flag writes its own message AND
//     calls the usage function before returning the error, so a set left at
//     its default would report the failure to os.Stderr in flag's words and
//     then let the caller report it again in its own. The engine renders the
//     help itself and hands the caller the error; flag stays silent.
//   - a no-op Usage. It is what flag calls on -h and on failure, and it is the
//     only way to stop the stdlib's own usage text competing with the help
//     generated from the declarations.
func newFlagSet(path string) *flag.FlagSet {
	set := flag.NewFlagSet(path, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	//: flag calls this on -h and inside failf; the engine renders help itself.
	set.Usage = func() {}
	//: the one shape; nothing else in this package builds a FlagSet.
	return set
}

// hasFlags reports whether any flag was bound onto set.
func hasFlags(set *flag.FlagSet) bool {
	bound := false
	//: VisitAll is the only way to ask a FlagSet whether it is empty.
	set.VisitAll(func(*flag.Flag) { bound = true })
	//: used by the usage line and by the Flags section, which must agree.
	return bound
}
