// Package entitlement — the one error code this IMPLEMENTATION owns and the
// sentinel that carries it, as opposed to the fifteen the CONTRACT declares in
// internal/core/entitlement.
//
// The split is the same one ADR 0079 drew through the domain: core names the
// operator situations every implementation of the port must be able to report —
// revoked, expired, unreachable, no possession — and this range names the
// failures that exist only because this engine has a cache, a network and a
// filesystem. A caller matching on the contract's codes is unaffected by
// anything declared here.
//
// The code and its sentinel share a file rather than the codes.go / errors.go
// pair the larger service packages use: with exactly one of each, splitting
// them puts a two-member group across two files and nothing else in either,
// which is what KTN-STRUCT-PARTITION is for.
//
// Hand-registered in codeRangeOwners (ADR 0035) and in //:audit_sources, so a
// code declared here that reaches into another package's range fails the build
// rather than passing quietly.
//
// The Public below is wire-safe and names no path: the public half is the one
// documented safe to put in a response body, and every input this package has
// is a publication endpoint, an untrusted document or a directory on somebody's
// disk. Where it happened lives in Private and in Fields, and diagnose renders
// them for the one place here that is a log rather than a wire.
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

// CacheUnwritable is returned when the offline bundle cannot be replaced.
//
// It is deliberately NOT a refusal, and nothing upstream treats it as
// one: the verification that produced these bytes has already succeeded,
// and a cache nobody can write costs the offline fallback and nothing
// else. rememberRoster logs it and carries on, which is the whole of its
// reachability — pinned by
// Test_rememberRoster_keepsWhatItCanAndSurvivesWhatItCannot.
var CacheUnwritable = errs.Define(CodeCacheUnwritable, "CACHE_UNWRITABLE",
	"the offline entitlement cache could not be written",
	"service/entitlement: staging or installing the cached bundle failed; the fields name the step and the path")
