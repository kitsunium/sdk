// Package process — StdioMode re-exports for wiring a child's standard streams.
package process

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// StdioInherit shares the parent's stdin/stdout/stderr with the child — the
// default and the only behaviour before per-process wiring existed.
const StdioInherit StdioMode = coreproc.StdioInherit

// StdioNull connects every stream to the null device: output is discarded and
// the child's stdin returns EOF immediately.
const StdioNull StdioMode = coreproc.StdioNull

// StdioCapture connects the child to Spec.Stdout/Stderr (io.Writer) and
// Spec.Stdin (io.Reader); a nil stream falls back to the null device for that
// one stream. The systemd analogue is StandardOutput=/StandardError=/StandardInput=.
const StdioCapture StdioMode = coreproc.StdioCapture

// StdioMode selects how a spawned child's standard streams (stdin, stdout,
// stderr) are wired. It is an alias of the core port type; set it on Spec.Stdio.
// The zero value is StdioInherit.
type StdioMode = coreproc.StdioMode
