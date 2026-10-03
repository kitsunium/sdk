// Package cli — ranges 0.2.32.* (ADR 0065 core/app/cli block) and 0.3.62.*
// (ADR 0065 service/app/cli block, declared here since ADR 0160).
package cli

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.32.0 - 0.2.32.255

// CodeInvalidCommand identifies a declaration refused because the command
// could never be reached or could never run: a blank name, a name carrying a
// space or leading "-", a child with no summary, or neither a Run nor any
// Commands.
const CodeInvalidCommand errs.Code = 0x00_02_20_01 // 0.2.32.1

// CodeAmbiguousCommand identifies a declaration refused for carrying BOTH a
// Run and Commands, where the meaning of an existing command line would change
// the day a child is added.
const CodeAmbiguousCommand errs.Code = 0x00_02_20_02 // 0.2.32.2

// CodeDuplicateCommand identifies a declaration refused because two children
// of one group share a name, so the resolver could only ever reach one of
// them.
const CodeDuplicateCommand errs.Code = 0x00_02_20_03 // 0.2.32.3

// CodeReservedFlag identifies a declaration refused because its Binder bound
// "h" or "help", the two names the domain answers itself.
const CodeReservedFlag errs.Code = 0x00_02_20_04 // 0.2.32.4

// range: 0.3.62.0 - 0.3.62.255
//
// The engine's outcomes. The range was allocated to internal/service/app/cli,
// which raises these codes while it resolves and runs a command line, and it
// is declared here with the declaration refusals so that every code of the
// domain is in one place (ADR 0160). A code keeps the value its allocation
// gave it whichever layer declares it, so the layer byte still reads 3.

// CodeUnknownCommand identifies an argument vector naming a sub-command the
// group does not declare. The group's help — which lists every name it DOES
// declare — has been written to the diagnostic stream by the time this is
// returned.
const CodeUnknownCommand errs.Code = 0x00_03_3E_01 // 0.3.62.1

// CodeMissingCommand identifies a group invoked with no sub-command at all. A
// group has no action of its own, so this is an incomplete command line and
// never a successful no-op.
const CodeMissingCommand errs.Code = 0x00_03_3E_02 // 0.3.62.2

// CodeInvalidFlags identifies a flag vector package flag refused: an unknown
// flag, a missing value, or a value its flag.Value could not parse. flag's own
// text is preserved in Private, never in Public, because it quotes the
// operator's value verbatim.
const CodeInvalidFlags errs.Code = 0x00_03_3E_03 // 0.3.62.3

// CodeCommandPanicked identifies an Action whose goroutine panicked; the
// engine recovered it, captured the originating stack, and turned it into this
// command's failure so main still gets a typed exit status instead of the
// runtime's 2.
const CodeCommandPanicked errs.Code = 0x00_03_3E_04 // 0.3.62.4

// CodeHelpWriteFailed identifies a help request (-h) whose answer the
// diagnostic stream did not take: a closed pipe, a full disk, a writer that
// accepted only part of the page. Asking a question is not a failure, but an
// answer nobody received is, so this is EX_IOERR and not the 0 a delivered
// help earns.
const CodeHelpWriteFailed errs.Code = 0x00_03_3E_05 // 0.3.62.5
