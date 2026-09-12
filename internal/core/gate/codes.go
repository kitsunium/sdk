// Package gate — the dotted-quad code range this domain owns.
package gate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// CodePolicyInvalid identifies a gate policy that could not be used as written
// — an unset update action, a recovery command that is gated, or no exemption
// at all. Every one of them is a construction fault, reported at start-up
// rather than at the first refused invocation.
const CodePolicyInvalid errs.Code = 0x00_02_24_01 // 0.2.36.1
