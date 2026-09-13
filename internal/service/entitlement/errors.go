// Package entitlement — the sentinels this IMPLEMENTATION emits, as opposed to
// the fifteen the domain contract names.
//
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public here is wire-safe, so none of them names a path, a URL, a host,
// a subject or a key identifier. That is not squeamishness: the public half is
// the one documented safe to put in a response body, and every input this
// package has is a publication endpoint, an untrusted document or a directory
// on somebody's disk. Where it happened and what the filesystem said live in
// Private and in Fields, and diagnose renders them for the one place that is a
// log rather than a wire.
package entitlement

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// CacheUnwritable is returned when the offline bundle cannot be replaced.
	//
	// It is deliberately NOT a refusal, and nothing upstream treats it as
	// one: the verification that produced these bytes has already succeeded,
	// and a cache nobody can write costs the offline fallback and nothing
	// else. rememberRoster logs it and carries on, which is the whole of its
	// reachability — pinned by
	// Test_rememberRoster_keepsWhatItCanAndSurvivesWhatItCannot.
	CacheUnwritable = errs.Define(CodeCacheUnwritable, "CACHE_UNWRITABLE",
		"the offline entitlement cache could not be written",
		"service/entitlement: staging or installing the cached bundle failed; the fields name the step and the path")
)
