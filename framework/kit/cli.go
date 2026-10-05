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

// defaultCommand is DefaultCommand's body: decl_gen.go writes DefaultCommand, from the
// design, as one call of it.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func defaultCommand() CLIOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.DefaultCommand()
}

// failSafe is FailSafe's body: decl_gen.go writes FailSafe, from the
// design, as one call of it.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func failSafe() CLIOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.FailSafe()
}
