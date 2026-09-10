// Package cli — range 0.2.32.* (ADR 0065 core/cli block).
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
