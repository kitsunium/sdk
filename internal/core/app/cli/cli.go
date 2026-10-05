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
