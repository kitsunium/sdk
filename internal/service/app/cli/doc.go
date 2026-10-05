// Package cli is the command-line engine: it validates a whole command tree
// once, resolves an argument vector against it, renders the help the tree
// implies, and returns a typed error carrying an exit status. It never ends
// the process.
//
// Package cli — the engine's configuration: two writers, and deliberately
// nothing else.
//
// Package cli — the one error the engine builds rather than declares. The
// domain's codes and sentinels are declared in internal/core/app/cli
// (ADR 0160); this file holds only the WrapParams that raise
// HELP_WRITE_FAILED over the writer's own error.
//
// Package cli — resolution and dispatch: the loop that turns an argument
// vector into one [corecli.InvocationValue] and one [corecli.Action] call.
//
// Package cli — the one seam between this domain and internal/core/app/config.
//
// Package cli — the help, generated from the declarations and from nowhere
// else.
package cli
