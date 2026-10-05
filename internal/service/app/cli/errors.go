package cli

import (
	corecli "github.com/kitsunium/sdk/internal/core/app/cli"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitIOErr matches sysexits EX_IOERR (74), the status corecli.HelpWriteFailed
// carries: an error occurred while doing I/O — here, on the stream the help
// is written to.
const exitIOErr int = 74

// helpWriteWrap is the WrapParams the engine attaches to the writer's own
// error. Its fields mirror corecli.HelpWriteFailed, so errors.Is answers both
// the sentinel and the cause the writer reported.
var helpWriteWrap = errs.WrapParams{
	Code:     corecli.CodeHelpWriteFailed,
	Reason:   "HELP_WRITE_FAILED",
	Public:   "The help could not be written",
	Private:  "service/app/cli: the diagnostic stream refused the help -h asked for, or took part of it; the fields carry the command path",
	ExitCode: exitIOErr,
}
