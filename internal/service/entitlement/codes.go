// Package entitlement — the error-code range this IMPLEMENTATION owns, as
// opposed to the fifteen the CONTRACT declares in internal/core/entitlement.
//
// The split is the same one ADR 0079 drew through the domain: core names the
// operator situations every implementation of the port must be able to report —
// revoked, expired, unreachable, no possession — and this range names the
// failures that exist only because this engine has a cache, a network and a
// filesystem. A caller matching on the contract's codes is unaffected by
// anything declared here.
//
// Hand-registered in codeRangeOwners (ADR 0035) and in //:audit_sources, so a
// code declared here that reaches into another package's range fails the build
// rather than passing quietly.
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.67.0 - 0.3.67.255

// CodeCacheUnwritable identifies an offline cache that could not be replaced.
//
// It is never a refusal. The verification that produced the bundle already
// succeeded, and what is lost is the next start-up's ability to answer without
// the network — which is why rememberRoster logs it and carries on rather than
// returning it. The code exists so that line has something to name, and so the
// step that failed (mkdir, create, write, close, rename) travels as a field
// instead of as prose.
const CodeCacheUnwritable errs.Code = 0x00_03_43_01 // 0.3.67.1
