package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// CodeCLIFailed marks the span of a CLI command that exited non-zero.
const CodeCLIFailed errs.Code = ikit.CodeCLIFailed

// Stdio is where a CLI command reads and writes: the process's own streams
// under Main, buffers in a test.
type Stdio = ikit.StdioValue

// CLIFunc is a CLI command's body: its arguments after its name, and the
// streams; it returns the process exit status (0 success, 64 usage, 70
// software — the sysexits a shell script branches on).
type CLIFunc = ikit.CLIFunc

// CLI is a short command-line command of the product: it runs once, in the
// CLI profile, and exits with its status. Main runs it when the process's
// first argument is its name.
type CLI = ikit.CLI

// CLIOption tunes a CLI command.
type CLIOption = ikit.CommandLineConfigurer

// DefaultCommand makes the command Main's default: it runs with every
// argument when the first names no other command — neither one of the
// product's nor one of Main's (serve, help…) —, and with none when there
// is none. Main then never answers a usage error. One command of an app may
// be its default.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func DefaultCommand() CLIOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DefaultCommand()
}

// FailSafe makes the command's status always 0: a status line, a prompt
// hook, anything a shell runs on every keystroke must never fail the shell.
// A non-zero status, a start that fails and a panic are logged instead.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func FailSafe() CLIOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.FailSafe()
}
