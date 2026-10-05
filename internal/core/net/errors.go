// Package net — declares the sentinel *errs.Error outcomes of the network
// domain. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// Service implementations and the pkg/v1 facades wrap these sentinels; they
// declare no codes of their own (the ADR 0016 proc precedent).
package net

// sysexits codes restated locally so the sentinels below carry an honest process
// exit status without importing a platform header.
const (
	exitUsage       int = 64 // EX_USAGE — the caller supplied something unusable
	exitDataErr     int = 65 // EX_DATAERR — the peer supplied something unusable
	exitUnavailable int = 69 // EX_UNAVAILABLE — the service could not be reached or served
	exitSoftware    int = 70 // EX_SOFTWARE — an internal invariant broke
	exitOSErr       int = 71 // EX_OSERR — an OS facility refused us
	exitTempFail    int = 75 // EX_TEMPFAIL — transient; retrying later may succeed
	exitNoPerm      int = 77 // EX_NOPERM — the action was refused by policy
	exitConfig      int = 78 // EX_CONFIG — the configuration itself is wrong
)
