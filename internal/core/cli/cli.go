// Package cli declares the command-line port of the SDK: the [Action] one
// command runs, the [Binder] that declares its flags, and the [Executor] that
// resolves an argument vector against a tree of [CommandValue] and dispatches
// it. A core sibling admitted by ADR 0065.
//
// # What this domain adds to package flag, and what it does not touch
//
// The stdlib's flag package parses flags. It does that well and this domain
// does not reimplement one byte of it: the flag SYNTAX, the [flag.Value]
// interface, the -x=v / -x v / --x forms, the "stop at the first non-flag
// argument" rule and the rendering of the defaults table all stay flag's. A
// [Binder] receives the stdlib's own *[flag.FlagSet], unchanged and
// unwrapped — the same shape internal/core/vfs takes for io/fs, and for the
// same reason: a wrapper around a stdlib contract is a second contract to
// learn, to keep in sync, and to get subtly wrong.
//
// What flag does NOT have is exactly what this domain adds, and nothing else:
//
//   - sub-commands, to arbitrary depth;
//   - one help text GENERATED from the declarations, so it cannot lie;
//   - a typed exit status, carried by the SDK's own errs codes;
//   - a seam to the rest of the SDK — the flags an operator actually typed can
//     become the top layer of a config load.
//
// # Nothing here can end the process
//
// flag's own answer to a bad argument is [flag.ExitOnError], which calls
// os.Exit from inside a library. That is refused by name: it makes the parse
// untestable, it skips every deferred function in the program, and it takes a
// decision — whether this process should still be alive — that belongs to
// main and to nobody else. Every failure in this domain is a returned
// *errs.Error carrying an exit status; main is the only place that may act on
// it. TestPackageNeverEndsTheProcess in internal/service/cli is the executable
// form of that claim.
//
// The engine that walks the tree, the help renderer and the config adapter
// live in internal/service/cli; this package owns the contract, the two domain
// values, and the sentinels a malformed declaration is refused with.
package cli

import (
	"context"
	"flag"
)

// Action is what one command DOES. It receives the context [Executor.Execute]
// was called with — a cancelled context is how a caller aborts a running
// command — and the resolved [InvocationValue].
//
// Returning a non-nil error is how a command fails. If it is an *errs.Error
// the SDK's origin-wins rule (CLAUDE.md rule 6) preserves its Code and its
// exit status all the way out of Execute, so a command's own exit status is
// never overwritten by the framework that called it.
type Action func(ctx context.Context, invocation InvocationValue) error

// Binder declares one command's flags onto the set that will parse them.
//
// It is handed the stdlib's *[flag.FlagSet] deliberately: every flag type the
// stdlib ships, and every [flag.Value] a caller has already written, works
// here with no adapter. The set is fresh, its name is the command path, and
// its error handling is always [flag.ContinueOnError].
//
// A Binder MUST only touch the set it is given, and MUST be safe to call more
// than once: the engine runs it once at construction to validate the
// declaration and to render help, and once per invocation to parse.
//
// # What concurrency the SDK cannot give you
//
// The engine keeps no per-invocation state, so two concurrent
// [Executor.Execute] calls do not interfere. A Binder, however, usually writes
// into a variable the CALLER closed over — fs.IntVar(&port, …) — and that
// variable is the caller's, invisible to the SDK and written by every
// concurrent invocation. Concurrent execution is therefore safe only when the
// Binder allocates its destination per call (fs.Int, fs.String, …) and the
// [Action] reads it back through [InvocationValue.Flags]. That is what those
// sets are for. The alternative would be the SDK taking a lock around memory
// it does not own, for a duration it cannot know.
type Binder func(flags *flag.FlagSet)

// Executor resolves an argument vector against a command tree and runs the
// command it names.
//
// IFACE-PLUGIN: the concrete engine stays unexported behind its constructor in
// internal/service/cli.
type Executor interface {
	// Execute resolves args against the tree and runs the command it names.
	// args is the vector WITHOUT the program name — os.Args[1:] at a call
	// site — because a library that reads os.Args itself cannot be tested and
	// cannot be embedded.
	//
	// It returns nil when the command succeeded AND when help was requested,
	// because asking for help is not a failure. Every other outcome is an
	// *errs.Error whose exit status the caller reads with errs.ExitCodeOf.
	Execute(ctx context.Context, args []string) error
}
