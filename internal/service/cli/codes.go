// Package cli — range 0.3.62.* (ADR 0065 service/cli block).
package cli

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.62.0 - 0.3.62.255

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
