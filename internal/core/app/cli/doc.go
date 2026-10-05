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
// unwrapped — the same shape internal/core/data/vfs takes for io/fs, and for the
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
// it. TestPackageNeverEndsTheProcess in internal/service/app/cli is the executable
// form of that claim.
//
// The engine that walks the tree, the help renderer and the config adapter
// live in internal/service/app/cli; this package owns the contract, the two domain
// values, and the sentinels a malformed declaration is refused with.
//
// Package cli — the declared command: a name, what it says about itself, its
// flags, and EITHER what it does OR what it contains.
//
// Package cli — what a resolved command line looks like by the time an
// [Action] sees it.
//
// Package cli — ranges 0.2.32.* (ADR 0065 core/app/cli block) and 0.3.62.*
// (ADR 0065 service/app/cli block, declared here since ADR 0160).
//
// Package cli — declares the sentinel *errs.Error outcomes of the domain: the
// refused declarations, and the engine's verdicts on a command line. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cli
